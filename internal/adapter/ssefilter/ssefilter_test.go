package ssefilter

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
)

// filtered wraps r in the production keepalive stripper.
func filtered(r io.Reader) io.Reader {
	return New(io.NopCloser(r))
}

// newStripper builds a *stripper directly so a test can set the small per-frame
// cap seam without exporting it.
func newStripper(r io.Reader) *stripper {
	return &stripper{src: io.NopCloser(r)}
}

// frames mirrors the openai-go ssestream eventStreamDecoder's accumulator over an
// SSE byte stream: a frame is the blank-line-terminated run, and its Data is the
// concatenation of every "data:" line's value plus a trailing '\n' per line
// (exactly what packages/ssestream/ssestream.go builds), while Type is the LAST
// "event:" line's value (the SDK keeps the final one). A frame dispatched with an
// EMPTY Data buffer (no data: line) is the keepalive defect; a frame whose Data
// buffer is a lone '\n' (a data: line with an empty value) is the sibling defect;
// and a frame whose Type is set from a PRIOR data-less frame's event: line is the
// metadata-merge defect. This helper lets the tests assert all three without
// depending on the SDK's typed event shapes.
type sseFrame struct {
	Type string
	Data string
}

func parseFrames(s string) []sseFrame {
	// Trim a single trailing newline so a stream ending in "\n\n" does not produce
	// a phantom empty frame after the final blank line (the SDK's bufio.Scanner
	// does not emit one at EOF either).
	s = strings.TrimSuffix(s, "\n")
	var out []sseFrame
	var cur sseFrame
	var data strings.Builder
	flush := func() {
		cur.Data = data.String()
		out = append(out, cur)
		cur = sseFrame{}
		data.Reset()
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch name {
		case "event":
			cur.Type = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	return out
}

// noEmptyPayload asserts every dispatched frame carries non-whitespace data.
// Empty data: lines may legitimately coexist with a real data line in the same
// frame; the decoder then sees JSON surrounded by whitespace, which is valid.
func noEmptyPayload(t *testing.T, out string) {
	t.Helper()
	for i, f := range parseFrames(out) {
		if strings.TrimSpace(f.Data) == "" {
			t.Errorf("frame %d dispatched with empty/whitespace data (keepalive bug)", i)
		}
	}
}

// TestStripperLeavesValidStreamsByteIdentical guards the filter's blast radius: a
// stream with no data-less frame must pass through unchanged, byte for byte.
func TestStripperLeavesValidStreamsByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream string
	}{
		{
			name: "lf",
			stream: "event: a\n" +
				"data:\n" +
				`data: {"x":1}` + "\n" +
				"id: 1\n" +
				": a comment line is NOT data but must pass verbatim\n" +
				"\n" +
				"retry: 5000\n" +
				`data: {"x":2}` + "\n\n",
		},
		{
			name: "crlf",
			stream: "event: a\r\n" +
				`data: {"x":1}` + "\r\n" +
				"id: 1\r\n" +
				"\r\n" +
				`data: {"x":2}` + "\r\n\r\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := []byte(tc.stream)
			got, err := io.ReadAll(filtered(bytes.NewReader(want)))
			if err != nil {
				t.Fatalf("read filtered: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("filter altered a valid stream:\n got %q\nwant %q", got, want)
			}
			noEmptyPayload(t, string(got))
		})
	}
}

// TestStripperSuppressesEveryDataLessShape covers all frame shapes that make
// ssestream unmarshal an empty payload — not just the comment form actually seen
// on the wire, since a gateway may use any of them. After filtering, the stream
// must contain NO empty-payload frame.
func TestStripperSuppressesEveryDataLessShape(t *testing.T) {
	const good = `data: {"x":1}` + "\n\n"
	const term = `data: {"stop":true}` + "\n\n" + "data: [DONE]\n\n"

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
		{"event plus comment", "event: thread.ping\n: keepalive\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := io.ReadAll(filtered(strings.NewReader(good + tc.frame + term)))
			if err != nil {
				t.Fatalf("read filtered: %v", err)
			}
			noEmptyPayload(t, string(out))
		})
	}
}

// TestStripperDoesNotMergeMetadataIntoNextFrame is the whole-frame-buffering
// guard: a data-less frame carrying an event: line must be dropped IN ITS
// ENTIRETY, never leaving its event: name to bind to the FOLLOWING data frame.
// A line-at-a-time filter would forward `event: thread.ping` before the boundary
// decision, so the SDK would read the next frame as `event: thread.ping` +
// data — openai-go special-cases thread.* events, corrupting or dropping that
// chunk.
func TestStripperDoesNotMergeMetadataIntoNextFrame(t *testing.T) {
	const stream = "event: thread.ping\n\n" +
		`data: {"x":1}` + "\n\n"
	out, err := io.ReadAll(filtered(strings.NewReader(stream)))
	if err != nil {
		t.Fatalf("read filtered: %v", err)
	}
	got := parseFrames(string(out))
	if len(got) != 1 {
		t.Fatalf("want exactly 1 surviving frame, got %d: %q", len(got), out)
	}
	if got[0].Type != "" {
		t.Errorf("surviving frame Type = %q, want empty "+
			"(the dropped thread.ping event: leaked into the next frame)", got[0].Type)
	}
	if got[0].Data != `{"x":1}`+"\n" {
		t.Errorf("surviving frame Data = %q, want the intact delta", got[0].Data)
	}
}

// TestStripperPreservesTrailingTruncation pins that the filter does NOT paper
// over a genuinely truncated stream: an unterminated partial frame must reach the
// caller verbatim (the adapters map a clean EOF with no terminal event to a
// truncation error), and the read must terminate with the source's EOF/error.
func TestStripperPreservesTrailingTruncation(t *testing.T) {
	const truncated = `data: {"x":1}` + "\n\n" +
		": ping\n\n" + `data: {"x":2` // cut mid-JSON, no trailing newline
	out, err := io.ReadAll(filtered(strings.NewReader(truncated)))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF/ErrUnexpectedEOF, got %v", err)
	}
	if !bytes.HasSuffix(out, []byte(`data: {"x":2`)) {
		t.Errorf("trailing truncated line must pass through verbatim, got %q", out)
	}
}

// TestStripperDoesNotWithholdBytes pins the streaming property: the filter must
// forward a completed frame WITHOUT waiting for the source to close, or SSE
// arrival timing (and llmresilience's idle watchdog) would shift.
func TestStripperDoesNotWithholdBytes(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()

	f := New(pr)
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

// stuckReader always returns (0, nil): the discouraged io.Reader behaviour that
// would spin Read forever without the maxZeroReads bound.
type stuckReader struct{}

func (stuckReader) Read([]byte) (int, error) { return 0, nil }
func (stuckReader) Close() error             { return nil }

// TestStripperBoundsFrameLength pins the memory guard. The filter must see a
// frame's boundary before deciding whether the frame survives, so it holds one
// frame in memory — and it sits IN FRONT of ssestream's scanner, whose own 32 MB
// cap therefore never engages. Unbounded, a newline-less stream grew the buffer
// to 1.1 GB in 5s and Read never returned; the cap must make it fail closed
// instead. maxFrame is set small so this costs bytes rather than 32 MB.
func TestStripperBoundsFrameLength(t *testing.T) {
	f := newStripper(strings.NewReader("data: " + strings.Repeat("x", 4096)))
	f.maxFrame = 64
	got, err := io.ReadAll(f)
	if !errors.Is(err, errFrameTooLong) {
		t.Fatalf("want errFrameTooLong, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("must not emit the oversized partial frame, got %d bytes", len(got))
	}
}

// TestStripperTerminatesOnEndlessLine is the regression proper: Read must RETURN
// on a newline-less stream rather than looping forever consuming memory.
func TestStripperTerminatesOnEndlessLine(t *testing.T) {
	src := &endlessNoNewline{}
	f := &stripper{src: src, maxFrame: 1 << 16}

	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(f)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errFrameTooLong) {
			t.Fatalf("want errFrameTooLong, got %v", err)
		}
		if src.n > 8<<20 {
			t.Errorf("consumed %d MB before failing closed; the cap is not bounding intake", src.n>>20)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Read never returned on a newline-less stream (consumed %d MB)", src.n>>20)
	}
}

// TestStripperTerminatesOnStuckReader pins the (0, nil) spin guard: a source that
// never makes progress must fail the stream closed rather than loop Read forever.
func TestStripperTerminatesOnStuckReader(t *testing.T) {
	f := New(stuckReader{})
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(f)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errStuckReader) {
			t.Fatalf("want errStuckReader, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Read never returned on a (0,nil)-spinning source")
	}
}

// TestCapErrorsAreNotRetryable pins the classification: the fail-closed cap
// errors must NOT masquerade as a transient truncation, or llmresilience would
// replay a stream from a broken endpoint. See errFrameTooLong's doc comment.
func TestCapErrorsAreNotRetryable(t *testing.T) {
	for _, err := range []error{errFrameTooLong, errStuckReader} {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%v must not wrap io.ErrUnexpectedEOF (llmresilience would retry it)", err)
		}
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

// TestMiddlewareOnlyWrapsEventStreams pins that a non-SSE body — notably the
// text/plain error bodies a gateway returns on 401 — reaches the SDK's error
// path untouched, while an event-stream body is wrapped.
func TestMiddlewareOnlyWrapsEventStreams(t *testing.T) {
	const errBody = `{"type":"error","error":{"type":"AuthError","message":"Missing API key."}}`

	mw := NewKeepaliveFilter()
	next := func(ct, body string) option.MiddlewareNext {
		return func(*http.Request) (*http.Response, error) {
			return &http.Response{
				Header: http.Header{"Content-Type": {ct}},
				Body:   io.NopCloser(strings.NewReader(body)),
			}, nil
		}
	}
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/v1/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	res, err := mw(req, next("text/plain;charset=UTF-8", errBody))
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if _, ok := res.Body.(*stripper); ok {
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
	if _, ok := res.Body.(*stripper); !ok {
		t.Error("must wrap an event-stream body")
	}
	_ = res.Body.Close()
}
