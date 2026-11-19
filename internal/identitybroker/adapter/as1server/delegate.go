package as1server

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/internal/identitybroker"
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
	// KeySetFile is the absolute path of the client's public signing keys as
	// a JSON JWK Set (RFC 7517 section 5), which must hold at least one key
	// and which ToolHive reads once, at construction, and derives the signing
	// algorithm from. It may hold an old and a new key while the client
	// rotates.
	KeySetFile string
	// AllowedSourceClients are the public clients whose users' access tokens
	// the client may exchange. At least one is required.
	AllowedSourceClients []string
	// IDJAGMaxLifetime caps the lifetime of refresh tokens that issue
	// ID-JAGs (ToolHive's id_jag_max_lifetime): at least a second, as ToolHive
	// caps in whole seconds, and at most maxIDJAGLifetime.
	IDJAGMaxLifetime time.Duration
	// IDJAG, when set, lets the client also exchange those refresh tokens for
	// per-call ID-JAGs. Nil issues none.
	IDJAG *IDJAGIssuance
}

// IDJAGIssuance configures the per-call ID-JAGs a delegate client may obtain
// (design step (7)). Each request carries the acting agent's JWT-SVID as its
// actor token, of type identitybroker.TokenTypeJWT, which AS-1 checks with the
// identity broker's CredentialVerifier for the grant's owner once
// Server.UseCredentialVerifier binds it. The ID-JAG names the agent in its act
// claim, with iss ActIssuer and sub the verifier's VerifiedAgent.ActorID,
// which must lie in ActIssuer's trust domain (AS-1 answers server_error
// otherwise), and carries the user's sub_id and upstream_subject.
type IDJAGIssuance struct {
	// ActIssuer is the agent trust domain as act.iss: spiffe://<trust
	// domain>, with a trust domain identitybroker.ValidateTrustDomain accepts,
	// and nothing after it. It is never AS-1's issuer.
	ActIssuer string
	// Targets are the authorization servers, such as the gateway's AS-2, the
	// client may obtain ID-JAGs for. At least one is required.
	Targets []IDJAGTarget
}

// IDJAGTarget is an authorization server a delegate client may obtain
// ID-JAGs for. ToolHive checks its values when New builds AS-1.
type IDJAGTarget struct {
	// Issuer is the target's issuer, the ID-JAG's aud exactly as written: an
	// https URL, or http on a loopback host, in canonical form, unique among
	// the targets and not AS-1's issuer.
	Issuer string
	// ClientID is the delegate client's client ID at the target.
	ClientID string
	// Resources are the resources behind the target, such as a vMCP
	// endpoint, the client may request an ID-JAG for. At least one is
	// required.
	Resources []string
	// Lifetime is each ID-JAG's lifetime in whole seconds, at most five
	// minutes. Zero selects ToolHive's default of a minute.
	Lifetime time.Duration
}

// idjagSubjectClaims are the subject claims every ID-JAG carries: the
// user's company identity and their identity at the upstream IdP.
var idjagSubjectClaims = []string{"sub_id", "upstream_subject"}

// maxIDJAGLifetime is ToolHive's largest id_jag_max_lifetime.
const maxIDJAGLifetime = 720 * time.Hour

// delegateScopes are the scopes a delegate client may request. ToolHive
// requires a non-empty list; the refresh-token exchange takes no scope, so
// this is the narrowest one.
var delegateScopes = []string{"openid"}

// delegateRunConfig is ToolHive's configuration of d for AS-1's audience.
func delegateRunConfig(d DelegateClient, audience string) authserver.DelegateClientRunConfig {
	rc := authserver.DelegateClientRunConfig{
		ClientID:                d.ID,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		JWKSFile:                d.KeySetFile,
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
	if d.IDJAG != nil {
		targets := make([]authserver.DelegateClientIDJAGTarget, 0, len(d.IDJAG.Targets))
		for _, target := range d.IDJAG.Targets {
			var lifetime string
			if target.Lifetime != 0 {
				lifetime = target.Lifetime.String()
			}
			targets = append(targets, authserver.DelegateClientIDJAGTarget{
				Issuer:                target.Issuer,
				ClientID:              target.ClientID,
				Resources:             slices.Clone(target.Resources),
				Lifetime:              lifetime,
				RequiredSubjectClaims: slices.Clone(idjagSubjectClaims),
			})
		}
		rc.TokenExchange.IDJAG = &authserver.DelegateClientIDJAG{Targets: targets}
		rc.TokenExchange.ActorProfile = &authserver.DelegateClientActorProfile{
			Validator: actorValidatorName, ActorTokenType: identitybroker.TokenTypeJWT,
		}
	}
	return rc
}

// validate checks d against the public client IDs.
func (d DelegateClient) validate(publicClients map[string]bool) error {
	switch {
	case d.ID == "":
		return fmt.Errorf("%w: delegate client ID is required", ErrInvalidConfig)
	case publicClients[d.ID]:
		return fmt.Errorf("%w: delegate client ID %q is also a public client's", ErrInvalidConfig, d.ID)
	case d.KeySetFile == "":
		return fmt.Errorf("%w: delegate client %q needs a JWK Set file", ErrInvalidConfig, d.ID)
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
	if d.IDJAG != nil {
		if err := d.IDJAG.validate(d.ID); err != nil {
			return err
		}
	}
	// Read last, so a configuration error is reported without file access.
	if err := checkKeySetFile(d.KeySetFile); err != nil {
		return fmt.Errorf("%w: delegate client %q: %w", ErrInvalidConfig, d.ID, err)
	}
	return nil
}

// validate checks i, the ID-JAG issuance of delegate client clientID. ToolHive
// checks the targets' values.
func (i IDJAGIssuance) validate(clientID string) error {
	td, ok := strings.CutPrefix(i.ActIssuer, "spiffe://")
	switch {
	case !ok || identitybroker.ValidateTrustDomain(td) != nil:
		return fmt.Errorf("%w: delegate client %q ID-JAG act issuer %q is not spiffe:// and a lowercase trust domain", ErrInvalidConfig, clientID, i.ActIssuer)
	case len(i.Targets) == 0:
		return fmt.Errorf("%w: delegate client %q issues ID-JAGs for no target", ErrInvalidConfig, clientID)
	}
	for _, target := range i.Targets {
		if target.Lifetime < 0 || target.Lifetime%time.Second != 0 {
			return fmt.Errorf("%w: delegate client %q ID-JAG lifetime %v for %q is not whole seconds", ErrInvalidConfig, clientID, target.Lifetime, target.Issuer)
		}
	}
	return nil
}
