package mcpbroker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	fositeoauth2 "github.com/ory/fosite/handler/oauth2"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	authsession "github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"golang.org/x/oauth2"
)

const (
	recoveredBearerTTL           = 2 * time.Minute
	recoveredCredentialIOTimeout = 5 * time.Second
)

// issueRecoveredBrokerCredential mints a short-lived B2 access token only. It
// deliberately has no refresh-token or external-grant path.
//
//nolint:gocyclo // one ordered mint transaction over guard validation and token issuance.
func issueRecoveredBrokerCredential(
	ctx context.Context,
	stor storage.Storage,
	keyProvider keys.KeyProvider,
	issuer string,
	clientID string,
	ownerPartition [32]byte,
	verifiedTSID string,
	custodyExpiresAt time.Time,
) (*oauth2.Token, error) {
	if stor == nil || keyProvider == nil || issuer == "" || clientID == "" || verifiedTSID == "" || !custodyExpiresAt.After(time.Now()) {
		return nil, errCustodyUnavailable
	}
	client, err := stor.GetClient(ctx, clientID)
	if err != nil || client == nil || client.GetID() != clientID || client.IsPublic() {
		return nil, errCustodyUnavailable
	}
	key, err := keyProvider.SigningKey(ctx)
	if err != nil || key == nil || key.Key == nil || key.KeyID == "" || key.Algorithm == "" {
		return nil, errCustodyUnavailable
	}
	expiresAt := time.Now().Add(recoveredBearerTTL)
	if custodyExpiresAt.Before(expiresAt) {
		expiresAt = custodyExpiresAt
	}
	if !expiresAt.After(time.Now()) {
		return nil, errCustodyUnavailable
	}
	hmacSecret := make([]byte, 32)
	if _, err := rand.Read(hmacSecret); err != nil {
		return nil, errCustodyUnavailable
	}
	config, err := server.NewAuthorizationServerConfig(&server.AuthorizationServerParams{
		Issuer: issuer, AccessTokenLifespan: recoveredBearerTTL, RefreshTokenLifespan: time.Hour, AuthCodeLifespan: time.Minute, SigningKeyID: key.KeyID,
		SigningKeyAlgorithm: key.Algorithm, SigningKey: key.Key, HMACSecrets: servercrypto.NewHMACSecrets(hmacSecret), AllowedAudiences: []string{issuer},
	})
	if err != nil {
		return nil, errCustodyUnavailable
	}
	signingKey := &jose.JSONWebKey{Key: key.Key, KeyID: key.KeyID, Algorithm: key.Algorithm, Use: "sig"}
	strategy := compose.NewOAuth2JWTStrategy(func(context.Context) (interface{}, error) { return signingKey, nil }, compose.NewOAuth2HMACStrategy(config.Config), config.Config)
	request := fosite.NewAccessRequest(authsession.New("mecatl-recovered:v1:"+base64.RawURLEncoding.EncodeToString(ownerPartition[:]), verifiedTSID, clientID, authsession.UserClaims{}))
	request.Client = client
	request.RequestedAt = time.Now().UTC()
	request.RequestedAudience = fosite.Arguments{issuer}
	request.GrantedAudience = fosite.Arguments{issuer}
	request.Session.SetExpiresAt(fosite.AccessToken, expiresAt)
	requestID := make([]byte, 18)
	if _, err := rand.Read(requestID); err != nil {
		return nil, errCustodyUnavailable
	}
	request.ID = base64.RawURLEncoding.EncodeToString(requestID)
	response := fosite.NewAccessResponse()
	helper := &fositeoauth2.HandleHelper{AccessTokenStrategy: strategy, AccessTokenStorage: stor, Config: config.Config}
	if _, err := helper.IssueAccessToken(ctx, time.Until(expiresAt), request, response); err != nil {
		return nil, fmt.Errorf("%w: issue recovered access token", errCustodyUnavailable)
	}
	if response.GetAccessToken() == "" {
		return nil, errCustodyUnavailable
	}
	return &oauth2.Token{AccessToken: response.GetAccessToken(), TokenType: response.GetTokenType(), Expiry: expiresAt}, nil
}

// recoveredCredentialSource is intentionally private to the exact logical
// attachment that owns it. A copied bearer cannot call Token to renew itself.
type recoveredCredentialSource struct {
	issue    func(context.Context) (*oauth2.Token, error)
	active   func() bool
	validate func(context.Context) error
	mu       sync.Mutex
	token    *oauth2.Token
	dead     atomic.Bool
}

func (s *recoveredCredentialSource) Token() (*oauth2.Token, error) {
	if s.dead.Load() || !s.active() {
		return nil, errors.New("mcpbroker: recovered credential unavailable")
	}
	s.mu.Lock()
	if s.token != nil && s.token.Expiry.After(time.Now()) {
		token := s.token
		s.mu.Unlock()
		return token, nil
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), recoveredCredentialIOTimeout)
	defer cancel()
	token, err := s.issue(ctx)
	if err != nil || token == nil || token.AccessToken == "" || !token.Expiry.After(time.Now()) || s.dead.Load() || !s.active() {
		return nil, errors.New("mcpbroker: recovered credential unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead.Load() || !s.active() {
		return nil, errors.New("mcpbroker: recovered credential unavailable")
	}
	if s.token != nil && s.token.Expiry.After(time.Now()) {
		return s.token, nil
	}
	s.token = token
	return token, nil
}

func (s *recoveredCredentialSource) validateCurrent(ctx context.Context) error {
	if s.dead.Load() || !s.active() {
		return errCustodyUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, recoveredCredentialIOTimeout)
	defer cancel()
	if err := s.validate(bounded); err != nil || s.dead.Load() || !s.active() {
		return errCustodyUnavailable
	}
	return nil
}

func (s *recoveredCredentialSource) close() {
	s.dead.Store(true)
}
