package mcpbroker

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// AS2 is the embedded authorization server set up as the identity broker's
// AS-2: the broker client is registered there and the vMCP's resource is its
// endpoint, distinct from the issuer.
type AS2 struct {
	// Issuer is AS-2's issuer: the client-assertion audience and an ID-JAG's
	// aud.
	Issuer string
	// TokenEndpoint is AS-2's token endpoint.
	TokenEndpoint string
	// Resource is the vMCP resource, an ID-JAG's resource and the audience of
	// the gateway tokens AS-2 issues.
	Resource string

	token *url.URL
	// handler serves the token endpoint under toolHiveBasePath.
	handler http.Handler
}

// InProcessClient returns a client that serves requests for AS-2's token
// endpoint straight from the embedded authorization server, without a network
// listener, and refuses every other URL. A request whose context ends before
// the handler finishes returns the context's error, so a client Timeout bounds
// the call; the abandoned handler finishes into its own buffer. Its twin is
// as1server's Server.InProcessClient (inprocess.go): fix both together.
func (a *AS2) InProcessClient() *http.Client {
	return &http.Client{
		Transport:     as2Transport{as2: a},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type as2Transport struct{ as2 *AS2 }

func (t as2Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	token := t.as2.token
	if req.URL.Scheme != token.Scheme || req.URL.Host != token.Host || req.URL.User != nil || req.URL.Path != token.Path {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("mcpbroker: AS-2 in-process client refuses %s://%s%s: not the token endpoint", req.URL.Scheme, req.URL.Host, req.URL.Path)
	}
	ctx := req.Context()
	inner := req.Clone(ctx)
	inner.RequestURI = req.URL.RequestURI()
	inner.Host = token.Host
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
		t.as2.handler.ServeHTTP(recorded, inner)
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

// writeKeySet writes the key set raw to a new file in a new private temporary
// directory, as ToolHive reads a delegate client's keys only from a file, and
// returns its path and a function that removes the directory. ToolHive reads
// the file once, at construction. Its twin is as1server's writeKeySet
// (delegate.go): fix both together.
func writeKeySet(raw []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "as2-broker-client-")
	if err != nil {
		return "", nil, fmt.Errorf("mcpbroker: broker client key set directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("mcpbroker: write broker client key set: %w", err)
	}
	return file, remove, nil
}
