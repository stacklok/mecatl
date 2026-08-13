package openaichat

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

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/provider/ssefilter"
)

// filtered wraps r in the shared production keepalive filter.
func filtered(r io.Reader) io.Reader {
	return ssefilter.New(io.NopCloser(r))
}

// TestSDKNowGuardsEmptyPayload is the SENTINEL for the whole fix, replacing the
// former ORACLE (which asserted the opposite: that the unfiltered stream crashed
// on the keepalive frame). openai-go >= v3.50.0 added its own data.Len()==0
// guard in packages/ssestream, so the unfiltered decode of this EXACT keepalive
// comment captured from OpenCode Go on the wire now succeeds — this test pins
// that fact. ssefilter itself is UNCHANGED and stays wired: it also bounds a
// stuck reader (maxZeroReads) and mirrors the SDK's frame-size cap, neither of
// which the SDK provides on its own, so it remains load-bearing independent of
// this upstream fix. If this test ever fails again, the SDK regressed the
// empty-payload guard and the filter's crash workaround is back to being the
// only thing standing between a keepalive and a killed stream.
func TestSDKNowGuardsEmptyPayload(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "stream_keepalive_ping.sse"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, derr := decodeSSE(bytes.NewReader(data)); derr != nil {
		t.Fatalf("want the unfiltered stream to decode cleanly (SDK empty-payload guard), got %v", derr)
	}
}

// TestKeepaliveFilterSurvivesPing is the fix: the same bytes, filtered, decode to
// a complete turn — both text deltas AND the terminal, so the ping neither kills
// the stream nor silently truncates it. This drives the REAL openai-go ssestream
// decoder so it exercises the exact production path the filter sits in front of.
func TestKeepaliveFilterSurvivesPing(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "stream_keepalive_ping.sse"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	chunks, derr := decodeSSE(filtered(bytes.NewReader(data)))
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
	if text != "hello" {
		t.Errorf("text: want %q, got %q", "hello", text)
	}
	if !sawDone {
		t.Error("want a terminal ChunkDone after the keepalive")
	}
}

// TestKeepaliveFilterLeavesValidStreamsByteIdentical guards the filter's blast
// radius: a stream with no data-less frame must pass through unchanged, byte for
// byte, including the existing OpenCode Go non-standard frames.
func TestKeepaliveFilterLeavesValidStreamsByteIdentical(t *testing.T) {
	for _, name := range []string{"stream_text.sse", "stream_toolcall.sse"} {
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
// through the shared filter against Chat Completions-shaped frames.
func TestKeepaliveFilterHandlesEveryDataLessShape(t *testing.T) {
	const good = `data: {"id":"c","choices":[{"index":0,"delta":{"content":"x"}}]}` + "\n\n"
	const term = `data: {"id":"c","choices":[{"index":0,"finish_reason":"stop","delta":{}}]}` + "\n\n" + "data: [DONE]\n\n"

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
			chunks, err := decodeSSE(filtered(strings.NewReader(good + tc.frame + term)))
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

// TestKeepaliveFilterPreservesTruncation pins that the filter does NOT paper over
// a genuinely truncated stream: mecatl must still fail closed via
// errTruncatedStream rather than treating a cut-off turn as complete.
func TestKeepaliveFilterPreservesTruncation(t *testing.T) {
	const truncated = `data: {"id":"c","choices":[{"index":0,"delta":{"content":"x"}}]}` + "\n\n" +
		": ping\n\n" + `data: {"id":"c","choices":[{"index":0,"delta":{"content":"y"` // cut mid-JSON, no blank line

	_, err := decodeSSE(filtered(strings.NewReader(truncated)))
	if !errors.Is(err, errTruncatedStream) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want a truncation error, got %v", err)
	}
}

// TestNewInstallsKeepaliveFilter is the load-bearing wiring test: it drives the
// REAL constructor -> middleware -> SDK client -> ssestream path against a server
// replaying the captured keepalive, so deleting the option.WithMiddleware line in
// New fails CI. The other tests exercise the filter against fixtures directly and
// would all still pass with the wiring removed.
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

	p := New(WithAPIKey("test-key"), WithBaseURL(srv.URL))
	seq, err := p.Stream(context.Background(), port.LLMRequest{
		Model:    "glm-5.2",
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
	if text != "hello" {
		t.Errorf("text: want %q, got %q", "hello", text)
	}
	if !sawDone {
		t.Error("want a terminal ChunkDone through the real client path")
	}
}
