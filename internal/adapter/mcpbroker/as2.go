package mcpbroker

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/stacklok/mecatl/internal/inprocesshttp"
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

// newAS2 is AS-2 at issuer for resource, its token endpoint served by the
// embedded authorization server's handler.
func newAS2(issuer, resource string, handler http.Handler) (*AS2, error) {
	tokenEndpoint := issuer + "/oauth/token"
	token, err := url.Parse(tokenEndpoint)
	if err != nil {
		return nil, fmt.Errorf("mcpbroker: AS-2 token endpoint: %w", err)
	}
	return &AS2{Issuer: issuer, TokenEndpoint: tokenEndpoint, Resource: resource, token: token, handler: handler}, nil
}

// InProcessClient returns a client that serves requests for AS-2's token
// endpoint straight from the embedded authorization server, without a network
// listener, and refuses every other URL. A request whose context ends before
// the handler finishes returns the context's error, so a client Timeout bounds
// the call.
func (a *AS2) InProcessClient() *http.Client {
	return inprocesshttp.NewClient(a.handler, a.admitInProcess)
}

// admitInProcess accepts only the token endpoint.
func (a *AS2) admitInProcess(u *url.URL) error {
	token := a.token
	if u.Scheme != token.Scheme || u.Host != token.Host || u.User != nil || u.Path != token.Path {
		return fmt.Errorf("mcpbroker: AS-2 in-process client refuses %s://%s%s: not the token endpoint", u.Scheme, u.Host, u.Path)
	}
	return nil
}
