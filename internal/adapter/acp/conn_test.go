package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// frame builds a newline-delimited (ndjson) JSON-RPC wire message from a raw
// body: the body followed by a single '\n' terminator.
func frame(body string) string {
	return body + "\n"
}

// readFrames parses every ndjson frame out of a buffer, returning the decoded
// message envelopes in order. It splits on '\n' and unmarshals each non-empty
// line (skipping bare blank lines).
func readFrames(t *testing.T, raw []byte) []message {
	t.Helper()
	var out []message
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("unmarshal frame %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestReadMessageClassification(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantMethod string
		wantID     string // raw id, "" for notification/none
		isResponse bool
	}{
		{
			name:       "request",
			body:       `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
			wantMethod: "initialize",
			wantID:     "1",
		},
		{
			name:       "notification",
			body:       `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`,
			wantMethod: "session/cancel",
			wantID:     "",
		},
		{
			name:       "response",
			body:       `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`,
			wantMethod: "",
			wantID:     "7",
			isResponse: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewConn(strings.NewReader(frame(tc.body)), &bytes.Buffer{}, nil)
			msg, err := c.readMessage()
			if err != nil {
				t.Fatalf("readMessage: %v", err)
			}
			if msg.Method != tc.wantMethod {
				t.Errorf("method = %q, want %q", msg.Method, tc.wantMethod)
			}
			gotID := strings.TrimSpace(string(msg.ID))
			if gotID != tc.wantID {
				t.Errorf("id = %q, want %q", gotID, tc.wantID)
			}
			isResp := msg.Method == "" && len(msg.ID) > 0
			if isResp != tc.isResponse {
				t.Errorf("isResponse = %v, want %v", isResp, tc.isResponse)
			}
		})
	}
}

// TestReadFrameDecodeErrors asserts the ndjson decode-error and blank-line
// behaviour: an invalid JSON line surfaces the "acp: decode frame" error, while a
// bare blank line is skipped (no error) and the reader advances to the next frame.
func TestReadFrameDecodeErrors(t *testing.T) {
	t.Run("invalid json line errors", func(t *testing.T) {
		c := NewConn(strings.NewReader("{not json}\n"), &bytes.Buffer{}, nil)
		_, err := c.readMessage()
		if err == nil || !strings.Contains(err.Error(), "acp: decode frame") {
			t.Fatalf("expected decode-frame error, got %v", err)
		}
	})

	t.Run("blank lines skipped, next frame decodes", func(t *testing.T) {
		// Two bare blank lines (one empty, one CRLF) precede a real message; the
		// reader must skip them and decode the message without error.
		wire := "\n\r\n" + `{"jsonrpc":"2.0","method":"x"}` + "\n"
		c := NewConn(strings.NewReader(wire), &bytes.Buffer{}, nil)
		msg, err := c.readMessage()
		if err != nil {
			t.Fatalf("readMessage: %v", err)
		}
		if msg.Method != "x" {
			t.Errorf("method = %q, want x", msg.Method)
		}
	})
}

// TestReadMessageRejectsOversizedFrame asserts an ndjson line longer than the cap
// is rejected as an error (CWE-789) — not silently truncated, and not a hang — and
// that a valid line padded to exactly maxFrameBytes does NOT trip the cap.
func TestReadMessageRejectsOversizedFrame(t *testing.T) {
	t.Run("over-cap line errors", func(t *testing.T) {
		// A single line (terminated by '\n') larger than the cap.
		over := make([]byte, maxFrameBytes+1)
		for i := range over {
			over[i] = 'a'
		}
		wire := append(over, '\n')
		c := NewConn(bytes.NewReader(wire), &bytes.Buffer{}, nil)
		_, err := c.readMessage()
		if err == nil || !strings.Contains(err.Error(), "frame too large") {
			t.Fatalf("expected frame-too-large error, got %v", err)
		}
	})

	t.Run("at-cap valid line does not trip the cap", func(t *testing.T) {
		// Build valid JSON padded with spaces to exactly maxFrameBytes (including the
		// '\n' terminator); the cap compares len(line) including the '\n', so the body
		// is maxFrameBytes-1 and the framed line is exactly maxFrameBytes.
		const prefix = `{"jsonrpc":"2.0","method":"x","params":{"pad":"`
		const suffix = `"}}`
		padLen := (maxFrameBytes - 1) - len(prefix) - len(suffix)
		body := prefix + strings.Repeat(" ", padLen) + suffix
		wire := body + "\n"
		if len(wire) != maxFrameBytes {
			t.Fatalf("test setup: framed line is %d bytes, want exactly %d", len(wire), maxFrameBytes)
		}
		c := NewConn(strings.NewReader(wire), &bytes.Buffer{}, nil)
		msg, err := c.readMessage()
		if err != nil {
			t.Fatalf("at-cap line should decode, got error: %v", err)
		}
		if msg.Method != "x" {
			t.Errorf("method = %q, want x", msg.Method)
		}
	})
}

// TestWriteFrameWireBytes is an anti-regression guard for the framing blind spot:
// it asserts the bytes mecatl PUTS ON THE WIRE are ndjson — no "Content-Length"
// substring, terminated by exactly one '\n', with no embedded '\n' other than the
// terminator — for both a Notify (notification) and a writeResult (response).
func TestWriteFrameWireBytes(t *testing.T) {
	assertNDJSON := func(t *testing.T, raw []byte) {
		t.Helper()
		if bytes.Contains(raw, []byte("Content-Length")) {
			t.Fatalf("wire bytes contain a Content-Length header: %q", raw)
		}
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			t.Fatalf("wire bytes must end with a '\\n' terminator: %q", raw)
		}
		// Splitting on '\n' yields exactly one non-empty segment (the single message)
		// plus a trailing empty segment from the terminator: no embedded newline.
		segs := bytes.Split(raw, []byte("\n"))
		nonEmpty := 0
		for _, s := range segs {
			if len(s) > 0 {
				nonEmpty++
			}
		}
		if nonEmpty != 1 {
			t.Fatalf("wire bytes have %d non-empty newline-delimited segments, want 1: %q", nonEmpty, raw)
		}
	}

	t.Run("Notify", func(t *testing.T) {
		var buf bytes.Buffer
		c := NewConn(strings.NewReader(""), &buf, nil)
		if err := c.Notify("session/update", map[string]any{"sessionId": "s1"}); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		assertNDJSON(t, buf.Bytes())
	})

	t.Run("writeResult", func(t *testing.T) {
		var buf bytes.Buffer
		c := NewConn(strings.NewReader(""), &buf, nil)
		c.writeResult(json.RawMessage("7"), map[string]any{"ok": true})
		assertNDJSON(t, buf.Bytes())
	})
}

// TestReadMessageNDJSONConformance feeds HAND-WRITTEN canonical ndjson bytes (NOT
// produced by mecatl's own writer) into a Conn reader, proving interop with a real
// peer's framing: two newline-delimited messages decode correctly, then a third
// read reports io.EOF.
func TestReadMessageNDJSONConformance(t *testing.T) {
	wire := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n" +
		`{"jsonrpc":"2.0","method":"session/cancel"}` + "\n"
	c := NewConn(strings.NewReader(wire), &bytes.Buffer{}, nil)

	m1, err := c.readMessage()
	if err != nil {
		t.Fatalf("first readMessage: %v", err)
	}
	if m1.Method != "initialize" || strings.TrimSpace(string(m1.ID)) != "1" {
		t.Fatalf("first message = method %q id %q, want initialize/1", m1.Method, m1.ID)
	}
	m2, err := c.readMessage()
	if err != nil {
		t.Fatalf("second readMessage: %v", err)
	}
	if m2.Method != "session/cancel" || len(m2.ID) != 0 {
		t.Fatalf("second message = method %q id %q, want session/cancel/none", m2.Method, m2.ID)
	}
	if _, err := c.readMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("third readMessage err = %v, want io.EOF", err)
	}
}

// TestReadMessageFinalLineNoTrailingNewline asserts the EOF-with-final-line path:
// a peer that closes WITHOUT a trailing '\n' on its last message still has that
// message decoded (not dropped), and the following read reports io.EOF.
func TestReadMessageFinalLineNoTrailingNewline(t *testing.T) {
	wire := `{"jsonrpc":"2.0","id":1,"method":"a"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"b"}` // no trailing newline
	c := NewConn(strings.NewReader(wire), &bytes.Buffer{}, nil)

	m1, err := c.readMessage()
	if err != nil {
		t.Fatalf("first readMessage: %v", err)
	}
	if m1.Method != "a" {
		t.Fatalf("first method = %q, want a", m1.Method)
	}
	m2, err := c.readMessage()
	if err != nil {
		t.Fatalf("second readMessage (final line, no newline): %v", err)
	}
	if m2.Method != "b" || strings.TrimSpace(string(m2.ID)) != "2" {
		t.Fatalf("final message = method %q id %q, want b/2", m2.Method, m2.ID)
	}
	if _, err := c.readMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("read after final line err = %v, want io.EOF", err)
	}
}

// TestServeOverLimitFrameErrorsNotHang asserts a Serve loop fed an over-limit line
// returns the error promptly (within a short context timeout) rather than hanging.
func TestServeOverLimitFrameErrorsNotHang(t *testing.T) {
	over := make([]byte, maxFrameBytes+1)
	for i := range over {
		over[i] = 'a'
	}
	wire := append(over, '\n')
	c := NewConn(bytes.NewReader(wire), &bytes.Buffer{}, nil)

	// Generous: scanning 16 MiB under -race on a loaded CI runner can exceed a
	// couple of seconds. This only has to tell "slow" from "never returns".
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Serve(ctx) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "frame too large") {
			t.Fatalf("Serve err = %v, want a frame-too-large error", err)
		}
	case <-ctx.Done():
		t.Fatal("Serve hung on an over-limit frame instead of returning the error")
	}
}

// countingReader yields an effectively endless run of a single byte (never a
// '\n'), recording how many bytes were pulled from it.
type countingReader struct {
	fill byte
	read int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.fill
	}
	r.read += int64(len(p))
	return len(p), nil
}

// TestReadMessageBoundsMemoryDuringRead proves the MEMORY BOUND (not just
// liveness): against an endless newline-less stream, readMessage must reject with
// "frame too large" after pulling at most ~maxFrameBytes (plus a small bufio-buffer
// slack), NOT after buffering the whole run. A regression (the old ReadBytes path)
// would pull unboundedly before the guard could fire.
func TestReadMessageBoundsMemoryDuringRead(t *testing.T) {
	cr := &countingReader{fill: 'a'}
	c := NewConn(cr, &bytes.Buffer{}, nil)

	_, err := c.readMessage()
	if err == nil || !strings.Contains(err.Error(), "frame too large") {
		t.Fatalf("expected frame-too-large error, got %v", err)
	}
	// The reader must have stopped near the cap. Allow generous slack for one bufio
	// buffer plus a fragment, but well under, say, 2x the cap — a regression to
	// unbounded ReadBytes would have consumed far more before erroring (it only
	// stops at '\n' or EOF, neither of which this stream ever provides).
	const slack = 1 << 20 // 1 MiB headroom for bufio buffering
	if cr.read > maxFrameBytes+slack {
		t.Fatalf("readMessage pulled %d bytes before erroring; want <= %d (cap %d + slack %d) — memory is not bounded during the read",
			cr.read, maxFrameBytes+slack, maxFrameBytes, slack)
	}
}

func TestNotifyWritesFrame(t *testing.T) {
	var buf bytes.Buffer
	c := NewConn(strings.NewReader(""), &buf, nil)
	if err := c.Notify("session/update", map[string]any{"sessionId": "s1"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	frames := readFrames(t, buf.Bytes())
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if frames[0].Method != "session/update" {
		t.Errorf("method = %q", frames[0].Method)
	}
	if len(frames[0].ID) != 0 {
		t.Errorf("notification must have no id, got %q", frames[0].ID)
	}
}

// TestServeHandlesRequestAndError exercises the inbound request path: a handler
// result is written as a response; a *MethodError becomes a JSON-RPC error.
func TestServeHandlesRequestAndError(t *testing.T) {
	in := frame(`{"jsonrpc":"2.0","id":1,"method":"ok","params":{}}`) +
		frame(`{"jsonrpc":"2.0","id":2,"method":"bad","params":{}}`)
	var out bytes.Buffer
	h := func(_ context.Context, method string, _ json.RawMessage, _ bool) (any, error) {
		if method == "bad" {
			return nil, newMethodErr(codeInvalidParams, "nope")
		}
		return map[string]string{"hello": "world"}, nil
	}
	c := NewConn(strings.NewReader(in), &out, h)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Serve(ctx); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	frames := readFrames(t, out.Bytes())
	if len(frames) != 2 {
		t.Fatalf("got %d response frames, want 2", len(frames))
	}
	byID := map[string]message{}
	for _, f := range frames {
		byID[strings.TrimSpace(string(f.ID))] = f
	}
	if got := byID["1"]; got.Error != nil || !strings.Contains(string(got.Result), "world") {
		t.Errorf("id 1 result = %q err=%v", got.Result, got.Error)
	}
	if got := byID["2"]; got.Error == nil || got.Error.Code != codeInvalidParams {
		t.Errorf("id 2 expected invalid-params error, got %+v", got.Error)
	}
}

// TestCallCorrelation drives an OUTBOUND request (Call) against a scripted peer
// that echoes the request id back in a response — verifying response
// correlation, the substance of request_permission.
func TestCallCorrelation(t *testing.T) {
	clientReader, clientWriter := io.Pipe() // peer -> conn (responses arrive here)
	sw := &syncWriter{w: &bytes.Buffer{}}   // conn -> peer (requests captured)

	c := NewConn(clientReader, sw, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = c.Serve(ctx) }()

	// Issue the outbound Call in a goroutine; it blocks for the response.
	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		var out map[string]any
		err := c.Call(ctx, "session/request_permission", map[string]any{"sessionId": "s1"}, &out)
		done <- result{out, err}
	}()

	// Read the request the conn wrote, extract its id, and respond with it.
	id := waitForRequestID(t, sw)
	respBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}`, id)
	if _, err := clientWriter.Write([]byte(frame(respBody))); err != nil {
		t.Fatalf("write response: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Call err: %v", r.err)
		}
		if r.out["ok"] != true {
			t.Errorf("Call out = %v, want ok:true", r.out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return")
	}
}

// waitForRequestID polls the captured outbound buffer until a framed request is
// present, returning its raw id.
func waitForRequestID(t *testing.T, sent *syncWriter) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frames := readFrames(t, sent.snapshot())
		for _, f := range frames {
			if f.Method == "session/request_permission" && len(f.ID) > 0 {
				return strings.TrimSpace(string(f.ID))
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no outbound request observed")
	return ""
}

// syncWriter serializes concurrent writes into a buffer for the test (the Conn
// already locks writes, but the test reads the buffer concurrently).
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// snapshot returns a copy of the bytes written so far, under lock.
func (s *syncWriter) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.w.Bytes()...)
}
