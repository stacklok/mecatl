package mcpbroker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

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

// hasKeys reports whether raw is a JSON JWK Set with at least one key.
func hasKeys(raw []byte) bool {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	return json.Unmarshal(raw, &set) == nil && len(set.Keys) > 0
}

// writeKeySet writes the key set raw to a new file in a new private temporary
// directory, as ToolHive reads a delegate client's keys only from a file, and
// returns its path and a function that removes the directory. ToolHive reads
// the file once, at construction. hasKeys and writeKeySet are copies of
// as1server's (delegate.go); fix both together.
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
