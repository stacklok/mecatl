package openaichat

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/openai/openai-go/v3/option"
)

// The SSE keepalive guard.
//
// WHY THIS EXISTS: the openai-go ssestream decoder dispatches an Event on EVERY
// blank line and then json.Unmarshals the frame's accumulated data with no
// empty-payload check (packages/ssestream/ssestream.go). A frame that carries no
// data — an SSE comment keepalive, a bare extra blank line, an event:-only frame —
// therefore yields an EMPTY payload, and json.Unmarshal([]byte{}, …) fails with
// "unexpected end of JSON input". That error is sticky: Stream.Next() latches it
// and returns false forever, so a SINGLE keepalive kills an otherwise healthy
// turn mid-stream. Once text has already streamed the turn is post-commit, so the
// no-replay rule makes it TERMINAL — the run dies rather than retrying.
//
// OpenCode Go does exactly this on long turns, observed on the wire as:
//
//	: ping - 2026-07-26 19:11:49.346444+00:00
//
// arriving 496 frames into a streaming response. It is rare per-request but fatal
// whenever it lands, which reads to an operator as frequent unexplained failures.
//
// WHY A MIDDLEWARE and not ssestream.RegisterDecoder: the registry is a plain
// package-level map written without a mutex, and mecatl re-mints this adapter
// per session (see WithReasoningEffort), so registering from a constructor would
// be a concurrent map write. Middleware is per-client, needs no global state, and
// is content-type agnostic (the registry lookup uses the RAW content-type header,
// so it silently misses "text/event-stream; charset=utf-8").
//
// The filter is byte-exact for every frame the SDK can parse: it removes ONLY the
// blank line that would dispatch an empty payload, plus data: lines with an empty
// value (which would otherwise leave a stray "\n" in the SDK's accumulator). It
// never buffers beyond the current line, so SSE arrival timing is preserved.
func keepaliveFilter() option.Middleware {
	return func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		res, err := next(req)
		if err != nil || res == nil || res.Body == nil {
			return res, err
		}
		// Only event-streams have frames to filter; leave JSON bodies (incl. error
		// bodies, which the SDK's error path parses) completely untouched.
		if !isEventStream(res.Header.Get("Content-Type")) {
			return res, nil
		}
		res.Body = &sseKeepaliveStripper{src: res.Body}
		return res, nil
	}
}

// isEventStream reports whether a Content-Type names text/event-stream, ignoring
// any parameters (";charset=utf-8") and case.
func isEventStream(ct string) bool {
	mediaType, _, _ := bytes.Cut([]byte(ct), []byte(";"))
	return bytes.EqualFold(bytes.TrimSpace(mediaType), []byte("text/event-stream"))
}

// sseKeepaliveStripper is a streaming line filter over an SSE body. It forwards
// every byte verbatim EXCEPT the two shapes that make ssestream unmarshal an
// empty payload: a dispatching blank line with no accumulated data, and a data:
// line whose value is empty.
type sseKeepaliveStripper struct {
	src io.ReadCloser

	line    []byte       // current partial line (no trailing '\n' yet)
	out     bytes.Buffer // filtered bytes ready for the caller
	buf     []byte       // reusable read buffer (allocated once, not per Read)
	pending int          // non-whitespace data: bytes seen since the last dispatch
	srcErr  error        // sticky terminal error from src
	maxLine int          // per-line cap; 0 means maxSSELineBytes (tests set it small)
}

// maxSSELineBytes bounds a single buffered SSE line. This filter must see a
// line's terminator before it can decide whether the line survives, so it holds
// one line in memory — and because it sits IN FRONT of ssestream's scanner, that
// scanner's own cap no longer engages first. Without this bound a newline-less
// stream grows the buffer without limit AND Read never returns (measured: 1.1 GB
// buffered, 3.3 GB heap, 5 seconds), which is strictly worse than the
// "bufio.Scanner: token too long" the SDK's scanner produced at 32 MB before the
// filter existed. MIRRORS that cap (bufio.MaxScanTokenSize<<9) so the guard fails
// exactly where the SDK would have, rather than shifting the limit. Same posture
// as maxToolArgsBytes.
const maxSSELineBytes = bufio.MaxScanTokenSize << 9

// errSSELineTooLong fails the stream closed when one line exceeds the cap. It is
// deliberately a PLAIN error — NOT a *json.SyntaxError and NOT wrapping
// io.ErrUnexpectedEOF — so llmresilience's DefaultClassifier treats it as
// NON-retryable: a 32 MB line is a broken or hostile endpoint, not a transient
// truncation worth replaying.
var errSSELineTooLong = fmt.Errorf("openaichat: SSE line exceeded %d bytes", maxSSELineBytes)

// lineCap returns the effective per-line cap.
func (s *sseKeepaliveStripper) lineCap() int {
	if s.maxLine > 0 {
		return s.maxLine
	}
	return maxSSELineBytes
}

func (s *sseKeepaliveStripper) Read(p []byte) (int, error) {
	// Serve whatever is already filtered first — never withhold ready bytes, or
	// the stream's arrival timing (and llmresilience's idle watchdog) would shift.
	for s.out.Len() == 0 {
		if s.srcErr != nil {
			// Source is done: flush any unterminated trailing line verbatim so a
			// genuinely truncated stream still LOOKS truncated to the caller
			// (mecatl maps that to errTruncatedStream, a distinct symptom).
			if len(s.line) > 0 {
				s.out.Write(s.line)
				s.line = nil
				break
			}
			return 0, s.srcErr
		}
		if s.buf == nil {
			s.buf = make([]byte, 8<<10)
		}
		n, err := s.src.Read(s.buf)
		if n > 0 {
			if cerr := s.consume(s.buf[:n]); cerr != nil {
				// DISCARD the oversized partial line before latching the error, so
				// the trailing-line flush below cannot emit it (and thereby swallow
				// the error) on the next iteration.
				s.line = nil
				s.srcErr = cerr
				continue
			}
		}
		if err != nil {
			s.srcErr = err
		}
	}
	return s.out.Read(p)
}

// consume splits b into lines, deciding per line whether it survives. It returns
// errSSELineTooLong if a single line outgrows the cap.
func (s *sseKeepaliveStripper) consume(b []byte) error {
	for _, c := range b {
		if c != '\n' {
			if len(s.line) >= s.lineCap() {
				return errSSELineTooLong
			}
			s.line = append(s.line, c)
			continue
		}
		s.emitLine()
	}
	return nil
}

// emitLine applies the filter to the completed line held in s.line.
func (s *sseKeepaliveStripper) emitLine() {
	line := s.line
	s.line = nil

	core := bytes.TrimSuffix(line, []byte("\r"))

	// A blank line is a frame boundary: the SDK dispatches here. Suppress it when
	// nothing would be dispatched, which is precisely the keepalive bug.
	if len(core) == 0 {
		if s.pending > 0 {
			s.out.Write(line)
			s.out.WriteByte('\n')
		}
		s.pending = 0
		return
	}

	name, value, _ := bytes.Cut(core, []byte(":"))
	if len(name) != 0 && string(name) == "data" {
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		// An empty data: value contributes only the '\n' the SDK appends per data
		// line — drop it so no stray whitespace reaches the accumulator.
		if len(bytes.TrimSpace(value)) == 0 {
			return
		}
		s.pending += len(bytes.TrimSpace(value))
	}

	// Everything else (comments, event:, id:, retry:, real data:) passes verbatim.
	s.out.Write(line)
	s.out.WriteByte('\n')
}

func (s *sseKeepaliveStripper) Close() error { return s.src.Close() }
