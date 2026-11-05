package as1server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// InProcessClient returns a client that serves requests for AS-1's issuer
// straight from Handler, without a network listener, so a validator in the
// same process can fetch discovery and keys before listeners start. It
// refuses any request outside the issuer's scheme, host, and path. A request
// whose context ends before the handler finishes returns the context's error,
// so a client Timeout bounds the call; the abandoned handler finishes into its
// own buffer.
func (s *Server) InProcessClient() *http.Client {
	return &http.Client{
		Transport:     inProcessTransport{server: s},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type inProcessTransport struct{ server *Server }

func (t inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	issuer := t.server.issuer
	p := req.URL.Path
	if req.URL.Scheme != issuer.Scheme || req.URL.Host != issuer.Host || req.URL.User != nil ||
		path.Clean(p) != p || !strings.HasPrefix(p, issuer.Path+"/") {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("as1server: in-process client refuses %s://%s%s: outside the issuer", req.URL.Scheme, req.URL.Host, p)
	}
	ctx := req.Context()
	inner := req.Clone(ctx)
	inner.RequestURI = req.URL.RequestURI()
	inner.Host = issuer.Host
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	// The handler runs in its own goroutine so the caller can stop waiting
	// when ctx ends. Only that goroutine touches recorded until done closes,
	// and it closes the body, which RoundTrip may leave to it.
	recorded := &bufferedResponse{header: make(http.Header)}
	done := make(chan struct{})
	var panicked any
	go func() {
		defer close(done)
		defer func() { _ = inner.Body.Close() }()
		defer func() { panicked = recover() }()
		t.server.handler.ServeHTTP(recorded, inner)
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
	if recorded.status == 0 {
		recorded.status = http.StatusOK
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", recorded.status, http.StatusText(recorded.status)),
		StatusCode:    recorded.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        recorded.header,
		Body:          io.NopCloser(bytes.NewReader(recorded.body.Bytes())),
		ContentLength: int64(recorded.body.Len()),
		Request:       req,
	}, nil
}

// bufferedResponse collects a handler's response in memory.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}
