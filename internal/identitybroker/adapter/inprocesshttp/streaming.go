package inprocesshttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// errHandlerAborted is RoundTrip's error when the handler panics before its
// response is sent, as a server dropping the connection would look.
var errHandlerAborted = errors.New("inprocesshttp: handler aborted the response")

// NewStreamingClient returns a client like NewClient's whose responses
// stream. As net/http's server does, the writer buffers the response: it is
// sent, and RoundTrip returns, at the handler's first Flush, once 4 KiB is
// written, or when the handler returns, and each later Flush sends what was
// written since. Sending only then keeps a handler that finishes its work
// after writing a reply from racing the caller's next request.
//
// The handler's request context ends with the caller's, or when the caller
// closes the body; a body read after the caller's context ended fails with
// its error unless the handler had already finished. The client has no
// timeout. A handler panic, http.ErrAbortHandler included, drops the
// response: before anything was sent RoundTrip fails, after that the body
// fails with io.ErrUnexpectedEOF; any other panic before a response is
// re-raised on the caller's goroutine, as NewClient does.
func NewStreamingClient(handler http.Handler, admit func(*url.URL) error) *http.Client {
	return &http.Client{
		Transport:     streamingTransport{handler: handler, admit: admit},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type streamingTransport struct {
	handler http.Handler
	admit   func(*url.URL) error
}

func (t streamingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.admit(req.URL); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	ctx := req.Context()
	handlerCtx, cancel := context.WithCancel(ctx)
	inner := req.Clone(handlerCtx)
	inner.RequestURI = req.URL.RequestURI()
	inner.Host = req.URL.Host
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	pr, pw := io.Pipe()
	// The first close of pw wins: the handler's return (EOF), its panic, or
	// the caller's context ending.
	stopAfter := context.AfterFunc(ctx, func() { _ = pw.CloseWithError(ctx.Err()) })
	w := &streamingWriter{header: make(http.Header), pipe: pw, ready: make(chan struct{})}
	go func() {
		defer cancel()
		defer func() { _ = inner.Body.Close() }()
		defer func() {
			if p := recover(); p != nil {
				_ = pw.CloseWithError(io.ErrUnexpectedEOF)
				w.abort(p)
				return
			}
			_ = w.flush()
			// A handler that returns because the caller's context ended
			// leaves the body failing with its error, not a clean EOF.
			_ = pw.CloseWithError(ctx.Err())
		}()
		t.handler.ServeHTTP(w, inner)
	}()
	select {
	case <-w.ready:
	case <-ctx.Done():
		cancel()
		_ = pr.CloseWithError(ctx.Err())
		return nil, ctx.Err()
	}
	if w.panicked != nil {
		cancel()
		_ = pr.Close()
		if w.panicked != http.ErrAbortHandler {
			panic(w.panicked)
		}
		return nil, errHandlerAborted
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode:    w.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.committed,
		Body:          &streamingBody{PipeReader: pr, stop: func() { stopAfter(); cancel() }},
		ContentLength: -1,
		Request:       req,
	}, nil
}

// streamingBody stops the handler when the caller closes it.
type streamingBody struct {
	*io.PipeReader
	stop func()
}

func (b *streamingBody) Close() error {
	err := b.PipeReader.Close()
	b.stop()
	return err
}

// streamingBufferSize is how much the writer buffers before it sends the
// response without a Flush, as net/http's server does.
const streamingBufferSize = 4096

// streamingWriter is the handler's ResponseWriter. Only the handler goroutine
// calls it; ready closes once status and committed, or panicked, are final.
type streamingWriter struct {
	header http.Header
	pipe   *io.PipeWriter
	// status and snapshot are what WriteHeader, or the first Write, set;
	// committed is snapshot once the response is sent.
	status    int
	snapshot  http.Header
	buf       bytes.Buffer
	once      sync.Once
	ready     chan struct{}
	committed http.Header
	panicked  any
}

func (w *streamingWriter) Header() http.Header { return w.header }

// WriteHeader sets status and a copy of the headers; later calls are ignored.
func (w *streamingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status, w.snapshot = status, w.header.Clone()
	}
}

// Write buffers p, sending the response once the buffer fills.
func (w *streamingWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	w.buf.Write(p)
	if w.buf.Len() >= streamingBufferSize {
		if err := w.flush(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush sends the response, then the buffered body, returning once the
// caller has read it.
func (w *streamingWriter) Flush() { _ = w.flush() }

func (w *streamingWriter) flush() error {
	w.WriteHeader(http.StatusOK)
	w.once.Do(func() {
		w.committed = w.snapshot
		close(w.ready)
	})
	if w.buf.Len() == 0 {
		return nil
	}
	_, err := w.pipe.Write(w.buf.Bytes())
	w.buf.Reset()
	return err
}

func (w *streamingWriter) abort(p any) {
	w.once.Do(func() {
		w.panicked = p
		close(w.ready)
	})
}
