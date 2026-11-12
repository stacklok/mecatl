// Package inprocesshttp serves HTTP client requests straight from an
// http.Handler in the same process, without a network listener.
package inprocesshttp

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// NewClient returns a client that serves every request admit accepts from
// handler, with the request's Host set to its URL's host, and does not follow
// redirects. A request admit refuses has its body closed and fails with
// admit's error, which should name only the URL's scheme, host, and path. A
// request whose context ends before the handler finishes returns the
// context's error, so a client Timeout bounds the call; the abandoned handler
// finishes into its own buffer. A handler panic is re-raised on the caller's
// goroutine.
func NewClient(handler http.Handler, admit func(*url.URL) error) *http.Client {
	return &http.Client{
		Transport:     transport{handler: handler, admit: admit},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type transport struct {
	handler http.Handler
	admit   func(*url.URL) error
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.admit(req.URL); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	ctx := req.Context()
	inner := req.Clone(ctx)
	inner.RequestURI = req.URL.RequestURI()
	inner.Host = req.URL.Host
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	// The handler runs in its own goroutine so the caller can stop waiting
	// when ctx ends. Only that goroutine touches recorded until done closes,
	// and it closes the body, which RoundTrip may leave to it.
	recorded := NewRecorder()
	done := make(chan struct{})
	var panicked any
	go func() {
		defer close(done)
		defer func() { _ = inner.Body.Close() }()
		defer func() { panicked = recover() }()
		t.handler.ServeHTTP(recorded, inner)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		// A handler that finished as ctx ended still wins: its response may
		// hold a token that cannot be issued again.
		select {
		case <-done:
		default:
			return nil, ctx.Err()
		}
	}
	if panicked != nil {
		// Re-raise the handler's panic on the caller's goroutine, as a
		// synchronous handler would.
		panic(panicked)
	}
	status, body := recorded.Status(), recorded.Body()
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        recorded.header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

// Recorder collects a handler's response in memory. The first status written
// wins; a handler that writes none gets 200.
type Recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{header: make(http.Header)} }

// Header returns the recorded response headers.
func (b *Recorder) Header() http.Header { return b.header }

// WriteHeader records status unless one was already written.
func (b *Recorder) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

// Write appends p to the recorded body.
func (b *Recorder) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

// Status returns the recorded status, 200 if the handler wrote none.
func (b *Recorder) Status() int {
	if b.status == 0 {
		return http.StatusOK
	}
	return b.status
}

// Body returns the recorded body.
func (b *Recorder) Body() []byte { return b.body.Bytes() }
