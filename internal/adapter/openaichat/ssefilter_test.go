package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// filtered wraps r in the production keepalive filter.
func filtered(r io.Reader) io.Reader {
	return &sseKeepaliveStripper{src: io.NopCloser(r)}
}

// TestKeepalivePingKillsUnfilteredStream is the ORACLE for the whole fix: it pins
// the upstream SDK defect using the EXACT keepalive comment captured from OpenCode
// Go on the wire. If openai-go ever grows an empty-payload guard this test fails,
// which is the signal that the filter is no longer load-bearing.
func TestKeepalivePingKillsUnfilteredStream(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "stream_keepalive_ping.sse"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	_, derr := decodeSSE(bytes.NewReader(data))
	if derr == nil {
		t.Fatal("want the unfiltered stream to FAIL on the keepalive frame, got nil error")
	}
	if !strings.Contains(derr.Error(), "unexpected end of JSON input") {
		t.Fatalf("want 'unexpected end of JSON input', got %v", derr)
	}
}

// TestKeepaliveFilterSurvivesPing is the fix: the same bytes, filtered, decode to
// a complete turn — both text deltas AND the terminal, so the ping neither kills
// the stream nor silently truncates it.
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
// on the wire, since a gateway may use any of them.
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

// TestKeepaliveFilterDoesNotWithholdBytes pins the streaming property: the filter
// must forward a completed frame WITHOUT waiting for the source to close, or SSE
// arrival timing (and llmresilience's idle watchdog) would shift.
func TestKeepaliveFilterDoesNotWithholdBytes(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()

	f := &sseKeepaliveStripper{src: pr}
	go func() {
		_, _ = pw.Write([]byte("data: {\"a\":1}\n\n"))
		// Deliberately do NOT close: the frame above must arrive on its own.
	}()

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	buf := make([]byte, 64)
	go func() {
		n, err := f.Read(buf)
		done <- result{n, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read: %v", got.err)
		}
		if want := "data: {\"a\":1}\n\n"; string(buf[:got.n]) != want {
			t.Errorf("got %q, want %q", buf[:got.n], want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("filter withheld a complete frame while the source stayed open")
	}
}

// TestNewInstallsKeepaliveFilter is the load-bearing wiring test: it drives the
// REAL constructor -> middleware -> SDK client -> ssestream path against a server
// replaying the captured keepalive, so deleting the option.WithMiddleware line in
// New fails CI. The other tests exercise the filter in isolation and would all
// still pass with the wiring removed.
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

// endlessNoNewline yields bytes forever and never a '\n'.
type endlessNoNewline struct{ n int64 }

func (e *endlessNoNewline) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	e.n += int64(len(p))
	return len(p), nil
}

func (*endlessNoNewline) Close() error { return nil }

// TestKeepaliveFilterBoundsLineLength pins the memory guard. The filter must see a
// line's terminator before deciding whether the line survives, so it holds one line
// in memory — and it sits IN FRONT of ssestream's scanner, whose own 32 MB cap
// therefore never engages. Unbounded, a newline-less stream grew the buffer to
// 1.1 GB in 5s and Read never returned; the cap must make it fail closed instead.
// maxLine is set small so this costs bytes rather than 32 MB.
func TestKeepaliveFilterBoundsLineLength(t *testing.T) {
	f := &sseKeepaliveStripper{
		src:     io.NopCloser(strings.NewReader("data: " + strings.Repeat("x", 4096))),
		maxLine: 64,
	}
	got, err := io.ReadAll(f)
	if !errors.Is(err, errSSELineTooLong) {
		t.Fatalf("want errSSELineTooLong, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("must not emit the oversized partial line, got %d bytes", len(got))
	}
}

// TestKeepaliveFilterTerminatesOnEndlessLine is the regression proper: Read must
// RETURN on a newline-less stream rather than looping forever consuming memory.
func TestKeepaliveFilterTerminatesOnEndlessLine(t *testing.T) {
	src := &endlessNoNewline{}
	f := &sseKeepaliveStripper{src: src, maxLine: 1 << 16}

	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(f)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errSSELineTooLong) {
			t.Fatalf("want errSSELineTooLong, got %v", err)
		}
		if src.n > 8<<20 {
			t.Errorf("consumed %d MB before failing closed; the cap is not bounding intake", src.n>>20)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Read never returned on a newline-less stream (consumed %d MB)", src.n>>20)
	}
}

// TestErrSSELineTooLongIsNotRetryable pins the classification: the cap error must
// NOT masquerade as a transient truncation, or llmresilience would replay a stream
// from a broken endpoint. See errSSELineTooLong's doc comment.
func TestErrSSELineTooLongIsNotRetryable(t *testing.T) {
	var syntaxErr *json.SyntaxError
	if errors.As(errSSELineTooLong, &syntaxErr) {
		t.Error("must not be a *json.SyntaxError (llmresilience would retry it)")
	}
	if errors.Is(errSSELineTooLong, io.ErrUnexpectedEOF) {
		t.Error("must not wrap io.ErrUnexpectedEOF (llmresilience would retry it)")
	}
}

// TestIsEventStream pins the content-type gate, including the parameterised form
// that a naive ssestream.RegisterDecoder lookup would silently miss.
func TestIsEventStream(t *testing.T) {
	for _, tc := range []struct {
		ct   string
		want bool
	}{
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"text/event-stream;charset=UTF-8", true},
		{"TEXT/EVENT-STREAM", true},
		{" text/event-stream ", true},
		{"application/json", false},
		{"text/plain;charset=UTF-8", false},
		{"", false},
	} {
		if got := isEventStream(tc.ct); got != tc.want {
			t.Errorf("isEventStream(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}

// TestKeepaliveFilterMiddlewareOnlyWrapsEventStreams pins that a non-SSE body —
// notably the text/plain error bodies this gateway returns on 401 — reaches the
// SDK's error path untouched.
func TestKeepaliveFilterMiddlewareOnlyWrapsEventStreams(t *testing.T) {
	const errBody = `{"type":"error","error":{"type":"AuthError","message":"Missing API key."}}`

	mw := keepaliveFilter()
	next := func(ct, body string) option.MiddlewareNext {
		return func(*http.Request) (*http.Response, error) {
			return &http.Response{
				Header: http.Header{"Content-Type": {ct}},
				Body:   io.NopCloser(strings.NewReader(body)),
			}, nil
		}
	}
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	res, err := mw(req, next("text/plain;charset=UTF-8", errBody))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if _, ok := res.Body.(*sseKeepaliveStripper); ok {
		t.Error("must NOT wrap a non-event-stream body")
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != errBody {
		t.Errorf("error body altered: got %q", got)
	}

	res, err = mw(req, next("text/event-stream", "data: {\"a\":1}\n\n"))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if _, ok := res.Body.(*sseKeepaliveStripper); !ok {
		t.Error("must wrap an event-stream body")
	}
}
