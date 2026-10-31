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
// refuses any request outside the issuer's scheme, host, and path.
func (s *Server) InProcessClient() *http.Client {
	return &http.Client{
		Transport:     inProcessTransport{server: s},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type inProcessTransport struct{ server *Server }

func (t inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}
	issuer := t.server.issuer
	p := req.URL.Path
	if req.URL.Scheme != issuer.Scheme || req.URL.Host != issuer.Host || req.URL.User != nil ||
		path.Clean(p) != p || !strings.HasPrefix(p, issuer.Path+"/") {
		return nil, fmt.Errorf("as1server: in-process client refuses %s://%s%s: outside the issuer", req.URL.Scheme, req.URL.Host, p)
	}
	inner := req.Clone(req.Context())
	inner.RequestURI = req.URL.RequestURI()
	inner.Host = issuer.Host
	if inner.Body == nil {
		inner.Body = http.NoBody
	}
	recorded := &bufferedResponse{header: make(http.Header)}
	t.server.handler.ServeHTTP(recorded, inner)
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
