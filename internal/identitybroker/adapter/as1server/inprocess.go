package as1server

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/stacklok/mecatl/internal/inprocesshttp"
)

// InProcessClient returns a client that serves requests for AS-1's issuer
// straight from Handler, without a network listener, so a validator in the
// same process can fetch discovery and keys before listeners start. It
// refuses any request outside the issuer's scheme, host, and path. A request
// whose context ends before the handler finishes returns the context's error,
// so a client Timeout bounds the call.
func (s *Server) InProcessClient() *http.Client {
	return inprocesshttp.NewClient(s.handler, s.admitInProcess)
}

// admitInProcess accepts a URL under the issuer.
func (s *Server) admitInProcess(u *url.URL) error {
	issuer := s.issuer
	p := u.Path
	if u.Scheme != issuer.Scheme || u.Host != issuer.Host || u.User != nil ||
		path.Clean(p) != p || !strings.HasPrefix(p, issuer.Path+"/") {
		return fmt.Errorf("as1server: in-process client refuses %s://%s%s: outside the issuer", u.Scheme, u.Host, p)
	}
	return nil
}
