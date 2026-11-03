package as1server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// DelegateClient is a pre-provisioned confidential client of the
// authorization server, such as the identity broker's. It authenticates with
// private_key_jwt against a configured key set, never jwks_uri, has no secret
// and no redirect URIs, and may use only the token-exchange grant. Its
// exchange policy is restricted: it may exchange only access tokens for AS-1's
// audience issued to AllowedSourceClients, and is configured to obtain refresh
// tokens that issue ID-JAGs. ToolHive also lets it exchange those access
// tokens for access tokens (stacklok/toolhive#7026).
type DelegateClient struct {
	// ID is the client ID. It must differ from every public client's.
	ID string
	// KeySet is the client's public signing keys as a JSON JWK Set (RFC 7517
	// section 5). ToolHive derives the signing algorithm from them.
	KeySet []byte
	// AllowedSourceClients are the public clients whose users' access tokens
	// the client may exchange. At least one is required.
	AllowedSourceClients []string
	// IDJAGMaxLifetime caps the lifetime of refresh tokens that issue
	// ID-JAGs (ToolHive's id_jag_max_lifetime): at least a second, as ToolHive
	// caps in whole seconds, and at most maxIDJAGLifetime.
	IDJAGMaxLifetime time.Duration
}

// maxIDJAGLifetime is ToolHive's largest id_jag_max_lifetime.
const maxIDJAGLifetime = 720 * time.Hour

// delegateScopes are the scopes a delegate client may request. ToolHive
// requires a non-empty list; the refresh-token exchange takes no scope, so
// this is the narrowest one.
var delegateScopes = []string{"openid"}

// delegateRunConfig is ToolHive's configuration of d, whose public key set
// is in jwksFile, for AS-1's audience.
func delegateRunConfig(d DelegateClient, jwksFile, audience string) authserver.DelegateClientRunConfig {
	return authserver.DelegateClientRunConfig{
		ClientID:                d.ID,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		JWKSFile:                jwksFile,
		GrantTypes:              []string{oauthproto.GrantTypeTokenExchange},
		Scopes:                  slices.Clone(delegateScopes),
		Audiences:               []string{audience},
		TokenExchange: &authserver.DelegateClientTokenExchange{
			SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted,
			AllowedSourceClients:    slices.Clone(d.AllowedSourceClients),
			APIAudience:             audience,
			// RefreshTokenMaxLifetime stays unset: no schedule grants yet.
			ExchangeRefreshTokens: &authserver.DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: d.IDJAGMaxLifetime.String()},
		},
	}
}

// validate checks d against the public client IDs.
func (d DelegateClient) validate(publicClients map[string]bool) error {
	switch {
	case d.ID == "":
		return fmt.Errorf("%w: delegate client ID is required", ErrInvalidConfig)
	case publicClients[d.ID]:
		return fmt.Errorf("%w: delegate client ID %q is also a public client's", ErrInvalidConfig, d.ID)
	case !hasKeys(d.KeySet):
		return fmt.Errorf("%w: delegate client %q needs a JWK Set with at least one key", ErrInvalidConfig, d.ID)
	case len(d.AllowedSourceClients) == 0:
		return fmt.Errorf("%w: delegate client %q needs at least one allowed source client", ErrInvalidConfig, d.ID)
	case d.IDJAGMaxLifetime < time.Second || d.IDJAGMaxLifetime > maxIDJAGLifetime:
		return fmt.Errorf("%w: delegate client %q ID-JAG grant lifetime %v is not between 1s and %v", ErrInvalidConfig, d.ID, d.IDJAGMaxLifetime, maxIDJAGLifetime)
	}
	for _, source := range d.AllowedSourceClients {
		if !publicClients[source] {
			return fmt.Errorf("%w: delegate client %q allows source client %q, which is not a public client", ErrInvalidConfig, d.ID, source)
		}
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
// the file once, at construction.
func writeKeySet(raw []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "as1-delegate-")
	if err != nil {
		return "", nil, fmt.Errorf("as1server: delegate key set directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(dir) }
	file := filepath.Join(dir, "jwks.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		remove()
		return "", nil, fmt.Errorf("as1server: write delegate key set: %w", err)
	}
	return file, remove, nil
}
