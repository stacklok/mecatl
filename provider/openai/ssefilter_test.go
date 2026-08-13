package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/provider/ssefilter"
)

// decodeSSEStream drives the REAL openai-go ssestream decoder over recorded SSE
// bytes and folds each Responses event through translate. Using the SDK decoder
// (not the bespoke stdlib scanner in stream.go) is deliberate: it proves the
// keepalive filter composes with the exact production stream path, including the
// empty-payload dispatch that is the defect this filter guards.
func decodeSSEStream(r io.Reader) ([]port.Chunk, error) {
	res := &http.Response{
		Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body:   io.NopCloser(r),
	}
	stream := ssestream.NewStream[responses.ResponseStreamEventUnion](ssestream.NewDecoder(res), nil)
	var st streamState
	var out []port.Chunk
	for stream.Next() {
		chunks, terr := translate(stream.Current(), &st)
		out = append(out, chunks...)
		if terr != nil {
			return out, terr
		}
	}
	if err := stream.Err(); err != nil {
		return out, err
	}
	// Mirror production Stream: a clean EOF WITHOUT a terminal Responses event is a
	// truncated stream — fail closed. translate sets st.done on
	// response.completed/incomplete/failed/error; if none fired, the body was cut
	// off, which must NOT read as success.
	if !st.done {
		return out, errTruncatedStream
	}
	return out, nil
}

// filtered wraps r in the shared production keepalive filter.
func filtered(r io.Reader) io.Reader {
	return ssefilter.New(io.NopCloser(r))
}

// TestSDKNowGuardsEmptyPayload is the SENTINEL for the Responses half, replacing
// the former ORACLE (which asserted the opposite: that the unfiltered stream
// crashed on the keepalive frame). openai-go >= v3.50.0 added its own
// data.Len()==0 guard in packages/ssestream, so the unfiltered decode of this
// EXACT keepalive comment captured from OpenCode Go on the wire now succeeds —
// this test pins that fact. ssefilter itself is UNCHANGED and stays wired: it
// also bounds a stuck reader (maxZeroReads) and mirrors the SDK's frame-size cap,
// neither of which the SDK provides on its own, so it remains load-bearing
// independent of this upstream fix. If this test ever fails again, the SDK
// regressed the empty-payload guard and the filter's crash workaround is back to
// being the only thing standing between a keepalive and a killed stream.
func TestSDKNowGuardsEmptyPayload(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "stream_keepalive_ping.sse"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, derr := decodeSSEStream(bytes.NewReader(data)); derr != nil {
		t.Fatalf("want the unfiltered stream to decode cleanly (SDK empty-payload guard), got %v", derr)
	}
}

// TestKeepaliveFilterSurvivesPing is the fix: the same Responses-shaped bytes,
// filtered, decode to a complete turn — both text deltas AND the terminal, so the
// ping neither kills the stream nor silently truncates it. Driven through the
// REAL openai-go ssestream decoder so it exercises the exact production path.
func TestKeepaliveFilterSurvivesPing(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "stream_keepalive_ping.sse"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	chunks, derr := decodeSSEStream(filtered(bytes.NewReader(data)))
	if derr != nil {
		t.Fatalf("filtered stream must decode cleanly, got %v", derr)
	}

	var text string
	var sawDone bool
	for _, c := range chunks {
		switch c.Kind {
		case port.ChunkText:
			text += c.Text
		case port.ChunkDone:
			sawDone = true
		}
	}
	if text != "Hello, world" {
		t.Errorf("text: want %q, got %q", "Hello, world", text)
	}
	if !sawDone {
		t.Error("want a terminal ChunkDone after the keepalive")
	}
}

// TestKeepaliveFilterLeavesValidStreamsByteIdentical guards the filter's blast
// radius against the existing Responses fixtures: a stream with no data-less
// frame must pass through unchanged, byte for byte.
func TestKeepaliveFilterLeavesValidStreamsByteIdentical(t *testing.T) {
	// text_turn.sse is the canonical clean Responses turn; skip the error-path
	// fixtures decodeSSE rejects at the typed layer.
	for _, name := range []string{"text_turn.sse", "function_call_turn.sse"} {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			got, err := io.ReadAll(filtered(bytes.NewReader(want)))
			if err != nil {
				t.Fatalf("read filtered: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("filter altered a valid stream:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestKeepaliveFilterHandlesEveryDataLessShape covers all frame shapes that make
// ssestream unmarshal an empty payload — not just the comment form actually seen
// on the wire, since a gateway may use any of them. Drives the REAL SDK decoder
// through the shared filter against Responses-shaped frames.
func TestKeepaliveFilterHandlesEveryDataLessShape(t *testing.T) {
	const good = `event: response.output_text.delta` + "\n" +
		`data: {"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"x"}` + "\n\n"
	const term = `event: response.output_text.delta` + "\n" +
		`data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":", world"}` + "\n\n" +
		`event: response.completed` + "\n" +
		`data: {"type":"response.completed","sequence_number":100,"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":12,"output_tokens":4}}}` + "\n\n"

	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"comment keepalive", ": ping - 2026-07-26 19:11:49.346444+00:00\n\n"},
		{"bare blank line", "\n"},
		{"event only", "event: ping\n\n"},
		{"empty data value", "data:\n\n"},
		{"empty data value with space", "data: \n\n"},
		{"crlf comment", ": ping\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks, err := decodeSSEStream(filtered(strings.NewReader(good + tc.frame + term)))
			if err != nil {
				t.Fatalf("want clean decode, got %v", err)
			}
			var sawDone bool
			for _, c := range chunks {
				if c.Kind == port.ChunkDone {
					sawDone = true
				}
			}
			if !sawDone {
				t.Error("want a terminal ChunkDone")
			}
		})
	}
}

// TestKeepaliveFilterPreservesTruncation is a REAL oracle: a genuinely truncated
// stream (a data delta, a ping, then a body cut mid-JSON with no terminal event)
// must surface errTruncatedStream, NOT decode as a silent success. The filter
// removes the ping but must not paper over the missing terminal event.
func TestKeepaliveFilterPreservesTruncation(t *testing.T) {
	const truncated = `event: response.output_text.delta` + "\n" +
		`data: {"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"x"}` + "\n\n" +
		": ping\n\n" + `data: {"type":"response.output_text.delta" cut mid-JSON` // no trailing newline

	_, err := decodeSSEStream(filtered(strings.NewReader(truncated)))
	if !errors.Is(err, errTruncatedStream) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want a truncation error, got %v", err)
	}
}

// TestTextThenPingThenEOFTruncates is the P1 regression: text → ping → EOF with
// NO terminal Responses event. Before the fail-closed guard, the filter removed
// the ping and the stream ended cleanly with st.done==false, which the engine
// would promote to a successful StopEndTurn — accepting a truncated partial
// answer as complete. Driven through the REAL constructor -> middleware -> SDK ->
// Stream path, the iterator MUST surface a truncation error and MUST NOT yield a
// ChunkDone.
func TestTextThenPingThenEOFTruncates(t *testing.T) {
	const body = "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n" +
		": ping - 2026-07-26 19:11:49.346444+00:00\n\n" // then EOF: no response.completed

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := New(WithAPIKey("test-key"), WithBaseURL(srv.URL+"/v1"))
	seq, err := p.Stream(context.Background(), port.LLMRequest{
		Model:    "gpt-5.2",
		Messages: []session.Message{{Role: session.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var sawErr error
	var text string
	for chunk, cerr := range seq {
		if cerr != nil {
			sawErr = cerr
			continue
		}
		switch chunk.Kind {
		case port.ChunkText:
			text += chunk.Text
		case port.ChunkDone:
			t.Fatal("a truncated stream must NOT yield a terminal ChunkDone")
		}
	}
	if text != "partial" {
		t.Errorf("text: want the streamed %q, got %q", "partial", text)
	}
	if !errors.Is(sawErr, io.ErrUnexpectedEOF) {
		t.Fatalf("want a truncation error wrapping io.ErrUnexpectedEOF, got %v", sawErr)
	}
}

// TestNewInstallsKeepaliveFilter is the load-bearing wiring test for the Responses
// adapter: it drives the REAL constructor -> middleware -> SDK client -> ssestream
// path against a server replaying Responses-shaped keepalive, so deleting the
// option.WithMiddleware line in New fails CI.
func TestNewInstallsKeepaliveFilter(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "stream_keepalive_ping.sse"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := New(WithAPIKey("test-key"), WithBaseURL(srv.URL+"/v1"))
	seq, err := p.Stream(context.Background(), port.LLMRequest{
		Model:    "gpt-5.2",
		Messages: []session.Message{{Role: session.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text string
	var sawDone bool
	for chunk, cerr := range seq {
		if cerr != nil {
			t.Fatalf("stream must survive the keepalive, got %v", cerr)
		}
		switch chunk.Kind {
		case port.ChunkText:
			text += chunk.Text
		case port.ChunkDone:
			sawDone = true
		}
	}
	if text != "Hello, world" {
		t.Errorf("text: want %q, got %q", "Hello, world", text)
	}
	if !sawDone {
		t.Error("want a terminal ChunkDone through the real client path")
	}
}
