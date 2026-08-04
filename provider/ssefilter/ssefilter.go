// Package ssefilter is the SSE keepalive guard shared by the openai-go-family
// adapters (the Chat Completions adapter in provider/openaichat and the
// Responses adapter in provider/openai).
//
// WHY THIS EXISTS: the openai-go ssestream decoder dispatches an Event on EVERY
// blank line (packages/ssestream/ssestream.go) and then json.Unmarshals the
// frame's accumulated data with no empty-payload check. A frame that carries no
// data — an SSE comment keepalive, a bare extra blank line, an event:-only
// frame, or a data: line with an empty value (which appends a lone '\n' to the
// accumulator) — therefore yields an EMPTY payload, and
// json.Unmarshal([]byte{}, …) fails with "unexpected end of JSON input". That
// error is sticky: Stream.Next() latches it and returns false forever, so a
// SINGLE keepalive kills an otherwise healthy turn mid-stream. Once text has
// already streamed the turn is post-commit, so the harness's no-replay rule
// makes it TERMINAL — the run dies rather than retrying.
//
// OpenCode Go does exactly this on long turns, observed on the wire as:
//
//	: ping - 2026-07-26 19:11:49.346444+00:00
//
// arriving 496 frames into a streaming response. It is rare per-request but
// fatal whenever it lands, which reads to an operator as frequent unexplained
// failures. The mechanism was confirmed to also fire on Responses-shaped
// frames (event: response.output_text.delta), so this guard guards BOTH
// openai-go-family adapters.
//
// WHY A MIDDLEWARE and not ssestream.RegisterDecoder: the registry is a plain
// package-level map written without a mutex, and mecatl re-mints these adapters
// per session (see WithReasoningEffort), so registering from a constructor would
// be a concurrent map write. Middleware is per-client, needs no global state, and
// is content-type agnostic (the registry lookup uses the RAW content-type
// header, so it silently misses "text/event-stream; charset=utf-8").
//
// WHY WHOLE-FRAME BUFFERING: the filter buffers a complete SSE frame (every line
// up to the blank-line boundary) and decides at the boundary whether the WHOLE
// frame survives — a frame is kept iff it carries at least one data: line with a
// non-empty value. A data-less frame is dropped IN ITS ENTIRETY, including any
// event:/id:/retry:/comment lines it carried. Filtering line-at-a-time would
// forward those metadata lines before the boundary decision and then merge them
// into the NEXT frame (e.g. `event: thread.ping\n\n` becoming the event name of
// the following data frame — openai-go special-cases thread.* events, so that
// silently corrupts or drops the next chunk). Whole-frame buffering also stays
// byte-exact for every frame the SDK can parse and holds no more than the current
// frame, so SSE arrival timing (and llmresilience's idle watchdog) is preserved:
// the SDK's own decoder only dispatches on the blank line, so releasing a frame
// at its boundary is exactly when the SDK would have seen it anyway.
package ssefilter

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/openai/openai-go/v3/option"
)

// NewKeepaliveFilter returns the outermost middleware that wraps an SSE response
// body in the keepalive stripper, removing data-less frames before the SDK's
// ssestream decoder can dispatch them as empty-payload Events. It MUST be
// installed as the FIRST request option so it is the OUTERMOST middleware, i.e.
// it filters the body the SDK's decoder ultimately reads, after any inner
// middleware. Non-event-stream bodies (JSON responses, error bodies) pass
// through untouched so the SDK's error path parses them byte-for-byte.
func NewKeepaliveFilter() option.Middleware {
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
		res.Body = &stripper{src: res.Body}
		return res, nil
	}
}

// New returns a keepalive-filtering ReadCloser wrapping body: Read drains the
// filtered SSE stream; Close closes body. Use NewKeepaliveFilter instead when
// wiring the filter as openai-go middleware; this constructor is for direct use
// (and tests).
func New(body io.ReadCloser) io.ReadCloser { return &stripper{src: body} }

// isEventStream reports whether a Content-Type names text/event-stream, ignoring
// any parameters (";charset=utf-8") and case.
func isEventStream(ct string) bool {
	mediaType, _, _ := bytes.Cut([]byte(ct), []byte(";"))
	return bytes.EqualFold(bytes.TrimSpace(mediaType), []byte("text/event-stream"))
}

// stripper is a whole-frame SSE filter over a streaming body. It forwards every
// frame that carries dispatchable data byte-for-byte, and drops every data-less
// frame in its entirety (see the package doc for why whole-frame, not per-line).
type stripper struct {
	src io.ReadCloser

	frame     []byte       // raw bytes since the last frame boundary
	lineStart int          // index of the current line within frame
	hasData   bool         // frame carries at least one non-empty data: line
	out       bytes.Buffer // filtered bytes ready for the caller
	buf       []byte       // reusable read buffer (allocated once, not per Read)
	srcErr    error        // sticky terminal error from src
	zeroReads int          // consecutive (0,nil) reads from src, bounded below
	maxFrame  int          // per-frame cap; 0 means maxSSEFrameBytes (tests set it small)
}

// maxSSEFrameBytes bounds a single buffered SSE frame. This filter must see a
// frame's blank-line boundary before it can decide whether the frame survives,
// so it holds one frame in memory — and because it sits IN FRONT of ssestream's
// scanner, that scanner's own cap no longer engages first. Without this bound a
// newline-less stream grows the buffer without limit AND Read never returns
// (measured: 1.1 GB buffered, 3.3 GB heap, 5 seconds), which is strictly worse
// than the "bufio.Scanner: token too long" the SDK's scanner produced at 32 MB
// before the filter existed. MIRRORS that cap (bufio.MaxScanTokenSize<<9) so the
// guard fails at the same order of magnitude, rather than shifting the limit.
// Same posture as the openai/openaichat adapters' maxToolArgsBytes.
const maxSSEFrameBytes = bufio.MaxScanTokenSize << 9

// maxZeroReads bounds consecutive (0, nil) reads from a misbehaving src before
// the filter gives up: a well-behaved io.Reader eventually returns data or an
// error, and (0, nil) is discouraged (io.Reader contract), so a source that only
// ever returns it would otherwise spin Read forever. Failing closed after a
// bounded wait is strictly better than an unkillable goroutine. One hundred
// matches the standard library's own no-progress posture.
const maxZeroReads = 100

// errFrameTooLong fails the stream closed when one frame exceeds the cap. It is
// deliberately a PLAIN error — NOT a *json.SyntaxError and NOT wrapping
// io.ErrUnexpectedEOF — so llmresilience's DefaultClassifier treats it as
// NON-retryable: a 32 MB frame is a broken or hostile endpoint, not a transient
// truncation worth replaying.
var errFrameTooLong = fmt.Errorf("ssefilter: SSE frame exceeded %d bytes", maxSSEFrameBytes)

// errStuckReader fails the stream closed when src returns (0, nil) indefinitely.
// It wraps io.ErrNoProgress, which remains non-retryable, for the same reason as
// errFrameTooLong: a reader that
// never makes progress is broken, not transiently slow.
var errStuckReader = fmt.Errorf(
	"ssefilter: source made no progress after %d zero-byte reads: %w",
	maxZeroReads,
	io.ErrNoProgress,
)

// frameCap returns the effective per-frame cap.
func (s *stripper) frameCap() int {
	if s.maxFrame > 0 {
		return s.maxFrame
	}
	return maxSSEFrameBytes
}

func (s *stripper) Read(p []byte) (int, error) {
	// Serve whatever is already filtered first — never withhold ready bytes, or
	// the stream's arrival timing (and llmresilience's idle watchdog) would shift.
	for s.out.Len() == 0 {
		if s.srcErr != nil {
			// Source is done: flush any unterminated trailing bytes verbatim so a
			// genuinely truncated stream still LOOKS truncated to the caller (the
			// adapters map a clean EOF with no terminal event to a truncation
			// error). A trailing partial frame that never reached a data-worthy
			// boundary is still forwarded — the adapter's own terminal-event check,
			// not this filter, decides truncation.
			if s.flushTrailing() {
				break
			}
			return 0, s.srcErr
		}
		if s.buf == nil {
			s.buf = make([]byte, 8<<10)
		}
		n, err := s.src.Read(s.buf)
		if n > 0 {
			s.zeroReads = 0
			if cerr := s.consume(s.buf[:n]); cerr != nil {
				// Discard the oversized partial frame before latching the error, so
				// the trailing flush below cannot emit it (and thereby swallow the
				// error) on the next iteration.
				s.discard()
				s.srcErr = cerr
				continue
			}
		} else if err == nil {
			// (0, nil): permitted but discouraged. Bound it so a source that never
			// makes progress cannot spin Read forever.
			s.zeroReads++
			if s.zeroReads >= maxZeroReads {
				s.discard()
				s.srcErr = errStuckReader
				continue
			}
		}
		if err != nil {
			s.srcErr = err
		}
	}
	return s.out.Read(p)
}

// flushTrailing emits any bytes still buffered when src ended without a final
// blank-line boundary: the in-progress frame lines followed by the current
// partial line, verbatim. It returns true if it wrote anything. Emitting a
// data-less trailing fragment verbatim is harmless — the SDK never dispatches it
// (no terminating blank line), and the adapter's terminal-event check is what
// surfaces the truncation.
func (s *stripper) flushTrailing() bool {
	if len(s.frame) == 0 {
		return false
	}
	s.out.Write(s.frame)
	s.discard()
	return true
}

// reset clears the frame accumulator after it was either emitted or dropped.
func (s *stripper) reset() {
	s.frame = s.frame[:0]
	s.lineStart = 0
	s.hasData = false
}

// discard drops the whole in-progress frame. It is used on a fail-closed error
// so the trailing flush cannot later emit the oversized fragment and thereby
// swallow the error.
func (s *stripper) discard() { s.reset() }

// consume splits b into lines, accumulating each into the current frame. On a
// blank line (frame boundary) it flushes or drops the whole frame. It returns
// errFrameTooLong if a single frame outgrows the cap.
func (s *stripper) consume(b []byte) error {
	for _, c := range b {
		if len(s.frame) >= s.frameCap() {
			return errFrameTooLong
		}
		s.frame = append(s.frame, c)
		if c == '\n' {
			s.endLine()
		}
	}
	return nil
}

// endLine inspects the just-completed raw line; a blank line closes the frame.
// The raw bytes stay in frame so surviving LF and CRLF streams are reproduced
// exactly rather than normalized by the filter.
func (s *stripper) endLine() {
	lineEnd := len(s.frame) - 1 // exclude the trailing '\n'
	line := s.frame[s.lineStart:lineEnd]
	core := bytes.TrimSuffix(line, []byte("\r"))
	if len(core) == 0 {
		// Frame boundary: dispatch iff the completed frame carries real data.
		s.closeFrame()
		return
	}

	// Note the frame carries dispatchable data if this is a non-empty data: line.
	name, value, _ := bytes.Cut(core, []byte(":"))
	if string(name) == "data" {
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		if len(bytes.TrimSpace(value)) != 0 {
			s.hasData = true
		}
	}

	s.lineStart = len(s.frame)
}

// closeFrame flushes the buffered frame + its terminating blank line verbatim
// iff it carried dispatchable data, else drops the whole frame.
func (s *stripper) closeFrame() {
	if s.hasData {
		s.out.Write(s.frame)
	}
	s.reset()
}

// Close closes the wrapped body.
func (s *stripper) Close() error { return s.src.Close() }
