package as1server

import (
	"fmt"
	"slices"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/internal/jwksfile"
)

// DelegateClient is a pre-provisioned confidential client of the
// authorization server, such as the identity broker's. It authenticates with
// private_key_jwt against a configured key set, never jwks_uri, has no secret
// and no redirect URIs, and may use only the token-exchange grant. Its
// exchange policy is restricted: it may exchange only access tokens for AS-1's
// audience issued to AllowedSourceClients, and only for refresh tokens that
// issue ID-JAGs. It has no output audience, so it can never obtain an access
// token for AS-1's audience.
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
		// No Audiences: the client gets only refresh tokens, never an access
		// token, and ToolHive refuses an output audience covering its input one.
		TokenExchange: &authserver.DelegateClientTokenExchange{
			SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted,
			AllowedSourceClients:    slices.Clone(d.AllowedSourceClients),
			SubjectTokenAudiences:   []string{audience},
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
	case !jwksfile.HasKeys(d.KeySet):
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
