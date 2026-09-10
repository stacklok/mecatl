package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sync"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type oauthCredentialState struct {
	mu             sync.Mutex
	reader         credentialstore.Reader
	writer         credentialstore.ConditionalWriter
	key            []byte
	identity       oauthCredentialIdentity
	registration   oauthRegistration
	origins        map[string]struct{}
	client         *http.Client
	requestRefresh bool
	allowInMemory  bool
	lifetime       context.Context

	record               *credentialstore.Record
	envelope             oauthCredentialEnvelope
	dcrGrant             oauthDCRGrantEnvelope
	authorizationVersion *credentialstore.Version
	config               *oauth2.Config
	token                *oauth2.Token
}

type persistentTokenSource struct {
	state *oauthCredentialState
	ctx   context.Context
}

func (s *oauthCredentialState) waitUntilIdle() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifetime.Err()
}

func (s *oauthCredentialState) operationContext(ctx context.Context) (context.Context, func()) {
	if s.lifetime == nil {
		return ctx, func() {}
	}
	opCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.lifetime, cancel)
	if s.lifetime.Err() != nil {
		cancel()
	}
	return opCtx, func() {
		stop()
		cancel()
	}
}

func restoreOAuthCredential(ctx context.Context, reader credentialstore.Reader, writer credentialstore.ConditionalWriter, identity oauthCredentialIdentity, registration oauthRegistration, origins map[string]struct{}, client *http.Client, requestRefresh, allowInMemory bool) (*oauthCredentialState, error) {
	key, err := oauthCredentialKey(identity)
	if registration.kind == oauthDCRClientKind {
		key, err = oauthDCRCredentialKey(identity, registration.generation)
	}
	if err != nil {
		return nil, err
	}
	state := &oauthCredentialState{reader: reader, writer: writer, key: key, identity: identity, registration: registration, origins: origins, client: client, requestRefresh: requestRefresh, allowInMemory: allowInMemory}
	record, err := reader.Get(ctx, key)
	if errors.Is(err, credentialstore.ErrNotFound) {
		if registration.kind != oauthDCRClientKind {
			return state, nil
		}
		if writer == nil {
			return nil, projectOAuthError(ErrOAuthUnavailable)
		}
		reset := newOAuthDCRResetGrant(identity, registration.generation)
		value, encodeErr := encodeOAuthDCRGrant(reset, identity, registration.generation, origins)
		if encodeErr != nil {
			return nil, projectOAuthError(encodeErr)
		}
		record, err = writer.Put(ctx, key, value, nil)
		if errors.Is(err, credentialstore.ErrConflict) {
			record, err = reader.Get(ctx, key)
		}
	}
	if err != nil {
		return nil, projectOAuthError(err)
	}
	if err := state.installRecordLocked(record); err != nil {
		return nil, projectOAuthError(err)
	}
	return state, nil
}

func (s *oauthCredentialState) hasCredential() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record != nil && s.config != nil && s.token != nil
}

func (s *oauthCredentialState) initialTokenSource() oauth2.TokenSource {
	return s.tokenSource(context.Background())
}

func (s *oauthCredentialState) tokenSource(ctx context.Context) oauth2.TokenSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.config == nil || s.token == nil {
		return nil
	}
	return &persistentTokenSource{state: s, ctx: ctx}
}

func (s *oauthCredentialState) newTokenSource(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg == nil || token == nil {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	if s.writer == nil {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	cfg = cloneOAuthConfig(cfg)
	token = cloneOAuthToken(token)
	if err := s.validateConfig(cfg); err != nil {
		return nil, projectOAuthError(err)
	}
	if s.registration.kind == oauthDCRClientKind {
		return s.newDCRTokenSource(ctx, cfg, token)
	}
	envelope := newOAuthCredentialEnvelope(s.identity, cfg, token)
	value, err := encodeOAuthCredential(envelope, s.identity, s.requestRefresh, s.origins)
	if err != nil {
		return nil, projectOAuthError(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var expected *credentialstore.Version
	if s.record != nil {
		version := s.record.Version
		expected = &version
	}
	record, err := s.writer.Put(ctx, s.key, value, expected)
	if err == nil {
		s.installLocked(record, envelope, cfg, token)
		return &persistentTokenSource{state: s}, nil
	}
	if !errors.Is(err, credentialstore.ErrConflict) {
		return nil, projectOAuthError(err)
	}
	if err := s.reloadLocked(ctx); err != nil {
		return nil, projectOAuthError(err)
	}
	return &persistentTokenSource{state: s}, nil
}

func (s *oauthCredentialState) validateConfig(cfg *oauth2.Config) error {
	if cfg.ClientID != s.registration.clientID || cfg.ClientSecret != s.registration.clientSecret {
		return errors.New("OAuth token configuration client does not match")
	}
	if s.registration.kind == "preregistered" && cfg.Endpoint.AuthStyle != oauth2.AuthStyleInHeader {
		return errors.New("OAuth token configuration authentication style is invalid")
	}
	if s.registration.kind == oauthDCRClientKind {
		tokenURL, tokenErr := validateHTTPURL("OAuth DCR token URL", cfg.Endpoint.TokenURL, false)
		issuerURL, issuerErr := url.Parse(s.identity.Issuer)
		redirect, redirectErr := url.Parse(cfg.RedirectURL)
		if cfg.ClientSecret != "" || cfg.Endpoint.AuthStyle != oauth2.AuthStyleAutoDetect || len(cfg.Scopes) != 1 || cfg.Scopes[0] != oauthDCRScope || tokenErr != nil || issuerErr != nil || urlOrigin(tokenURL) != urlOrigin(issuerURL) || redirectErr != nil || redirect.Path != s.registration.redirectPath {
			return errors.New("OAuth DCR token configuration is invalid")
		}
	}
	return nil
}

func (s *persistentTokenSource) Token() (*oauth2.Token, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return s.state.tokenLocked(ctx, true)
}

// tokenWithContext is used by controller-facing code so cancellation survives
// oauth2.TokenSource's context-free interface.
func (s *persistentTokenSource) tokenWithContext(ctx context.Context) (*oauth2.Token, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	return s.state.tokenLocked(ctx, true)
}

func (s *oauthCredentialState) tokenLocked(ctx context.Context, allowConflict bool) (*oauth2.Token, error) {
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.record == nil || s.config == nil {
		return nil, projectOAuthError(ErrOAuthLoginRequired)
	}
	if s.registration.kind == oauthDCRClientKind {
		if err := s.reloadLocked(ctx); err != nil {
			return nil, projectOAuthError(err)
		}
		if err := s.validateDCRRegistrationLocked(ctx); err != nil {
			return nil, projectOAuthError(err)
		}
		if s.token == nil || !s.token.Valid() {
			return nil, projectOAuthError(ErrOAuthLoginRequired)
		}
		return cloneOAuthToken(s.token), nil
	}
	if s.token.RefreshToken == "" && !s.token.Valid() {
		return nil, projectOAuthError(ErrOAuthLoginRequired)
	}
	if !s.token.Valid() && s.writer == nil && !s.allowInMemory {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	old := cloneOAuthToken(s.token)
	token, err := s.sourceLocked(ctx).Token()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if isInvalidGrant(err) {
			return s.invalidGrantLocked(ctx)
		}
		return nil, projectOAuthError(err)
	}
	if token.RefreshToken == "" {
		token.RefreshToken = old.RefreshToken
	}
	if tokensEqual(old, token) {
		return cloneOAuthToken(token), nil
	}
	return s.persistRefreshedLocked(ctx, token, allowConflict)
}

func (s *oauthCredentialState) sourceLocked(ctx context.Context) oauth2.TokenSource {
	return s.config.TokenSource(oauthContext(ctx, s.client), cloneOAuthToken(s.token))
}

func (s *oauthCredentialState) persistRefreshedLocked(ctx context.Context, token *oauth2.Token, allowConflict bool) (*oauth2.Token, error) {
	if s.writer == nil {
		if !s.allowInMemory {
			return nil, projectOAuthError(ErrOAuthUnavailable)
		}
		s.envelope = newOAuthCredentialEnvelope(s.identity, s.config, token)
		s.token = cloneOAuthToken(token)
		return cloneOAuthToken(token), nil
	}
	envelope := newOAuthCredentialEnvelope(s.identity, s.config, token)
	value, err := encodeOAuthCredential(envelope, s.identity, s.requestRefresh, s.origins)
	if err != nil {
		return nil, projectOAuthError(err)
	}
	version := s.record.Version
	record, err := s.writer.Put(ctx, s.key, value, &version)
	if err == nil {
		s.installLocked(record, envelope, s.config, token)
		return cloneOAuthToken(token), nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if !errors.Is(err, credentialstore.ErrConflict) || !allowConflict {
		return nil, projectOAuthError(err)
	}
	if err := s.reloadLocked(ctx); err != nil {
		return nil, projectOAuthError(err)
	}
	return s.tokenLocked(ctx, false)
}

func (s *oauthCredentialState) invalidGrantLocked(ctx context.Context) (*oauth2.Token, error) {
	if s.writer == nil {
		s.clearLocked()
		return nil, projectOAuthError(ErrOAuthLoginRequired)
	}
	failedVersion := s.record.Version
	record, err := s.reader.Get(ctx, s.key)
	if errors.Is(err, credentialstore.ErrNotFound) {
		s.clearLocked()
		return nil, projectOAuthError(ErrOAuthLoginRequired)
	}
	if err != nil {
		return nil, projectOAuthError(err)
	}
	if !record.Version.Equal(failedVersion) {
		if err := s.installRecordLocked(record); err != nil {
			return nil, projectOAuthError(err)
		}
		token, retryErr := s.sourceLocked(ctx).Token()
		if retryErr == nil {
			if token.RefreshToken == "" {
				token.RefreshToken = s.token.RefreshToken
			}
			if tokensEqual(s.token, token) {
				return cloneOAuthToken(token), nil
			}
			return s.persistRefreshedLocked(ctx, token, false)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if !isInvalidGrant(retryErr) {
			return nil, projectOAuthError(retryErr)
		}
	}

	version := s.record.Version
	err = s.writer.Delete(ctx, s.key, version)
	if err == nil || errors.Is(err, credentialstore.ErrNotFound) {
		s.clearLocked()
		return nil, projectOAuthError(ErrOAuthLoginRequired)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if !errors.Is(err, credentialstore.ErrConflict) {
		return nil, projectOAuthError(err)
	}
	if err := s.reloadLocked(ctx); err != nil {
		return nil, projectOAuthError(err)
	}
	return s.tokenLocked(ctx, false)
}

func (s *oauthCredentialState) reloadLocked(ctx context.Context) error {
	record, err := s.reader.Get(ctx, s.key)
	if err != nil {
		if errors.Is(err, credentialstore.ErrNotFound) {
			s.clearLocked()
		}
		return err
	}
	return s.installRecordLocked(record)
}

func (s *oauthCredentialState) installRecordLocked(record credentialstore.Record) error {
	if s.registration.kind == oauthDCRClientKind {
		grant, err := decodeOAuthDCRGrant(record.Value, s.identity, s.registration.generation, s.origins)
		if err != nil {
			return err
		}
		record.Value = slices.Clone(record.Value)
		s.record = &record
		s.dcrGrant = grant
		s.envelope = oauthCredentialEnvelope{}
		s.config = nil
		s.token = nil
		if grant.State == "reset" {
			return nil
		}
		tokenURL, tokenErr := url.Parse(grant.Authorization.TokenURL)
		issuerURL, issuerErr := url.Parse(s.identity.Issuer)
		redirect, redirectErr := url.Parse(grant.Authorization.RedirectURL)
		if tokenErr != nil || issuerErr != nil || redirectErr != nil || urlOrigin(tokenURL) != urlOrigin(issuerURL) || redirect.Path != s.registration.redirectPath {
			return errors.New("OAuth DCR grant binding is invalid")
		}
		token, err := oauthDCRGrantToken(grant)
		if err != nil {
			return err
		}
		auth := grant.Authorization
		s.config = &oauth2.Config{ClientID: s.registration.clientID, Endpoint: oauth2.Endpoint{TokenURL: auth.TokenURL, AuthStyle: oauth2.AuthStyleInParams}, RedirectURL: auth.RedirectURL, Scopes: append([]string(nil), auth.Scopes...)}
		s.token = token
		return nil
	}
	envelope, err := decodeOAuthCredential(record.Value, s.identity, s.requestRefresh, s.origins)
	if err != nil {
		return err
	}
	token, err := envelopeToken(envelope)
	if err != nil {
		return err
	}
	cfg := envelopeConfig(envelope, s.registration)
	s.installLocked(record, envelope, cfg, token)
	return nil
}

func (s *oauthCredentialState) installLocked(record credentialstore.Record, envelope oauthCredentialEnvelope, cfg *oauth2.Config, token *oauth2.Token) {
	cfg = cloneOAuthConfig(cfg)
	token = cloneOAuthToken(token)
	record.Value = slices.Clone(record.Value)
	s.record = &record
	s.envelope = envelope
	s.config = cfg
	s.token = token
}

func (s *oauthCredentialState) clearLocked() {
	s.record = nil
	s.envelope = oauthCredentialEnvelope{}
	s.dcrGrant = oauthDCRGrantEnvelope{}
	s.authorizationVersion = nil
	s.config = nil
	s.token = nil
}

func (s *oauthCredentialState) reset(ctx context.Context) error {
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.writer == nil {
		return projectOAuthError(ErrOAuthUnavailable)
	}
	if s.registration.kind == oauthDCRClientKind {
		return s.resetDCRLocked(ctx)
	}
	if s.record == nil {
		s.clearLocked()
		return nil
	}
	version := s.record.Version
	err := s.writer.Delete(ctx, s.key, version)
	if err == nil || errors.Is(err, credentialstore.ErrNotFound) {
		s.clearLocked()
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if !errors.Is(err, credentialstore.ErrConflict) {
		return projectOAuthError(err)
	}
	if err := s.reloadLocked(ctx); err != nil {
		return projectOAuthError(err)
	}
	return nil
}

func (s *oauthCredentialState) beginAuthorization(ctx context.Context) error {
	if s.registration.kind != oauthDCRClientKind {
		return nil
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(ctx); err != nil {
		return projectOAuthError(err)
	}
	if err := s.validateDCRRegistrationLocked(ctx); err != nil {
		return projectOAuthError(err)
	}
	if err := s.resetDCRLocked(ctx); err != nil {
		return err
	}
	version := s.record.Version
	s.authorizationVersion = &version
	return nil
}

func (s *oauthCredentialState) newDCRTokenSource(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
	if token.RefreshToken != "" {
		return nil, projectOAuthError(ErrOAuthUnavailable)
	}
	grant := newOAuthDCRActiveGrant(s.identity, s.registration.generation, cfg, token)
	value, err := encodeOAuthDCRGrant(grant, s.identity, s.registration.generation, s.origins)
	if err != nil {
		return nil, projectOAuthError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authorizationVersion == nil {
		return nil, projectOAuthError(ErrOAuthDCRRecoveryRequired)
	}
	expected := *s.authorizationVersion
	s.authorizationVersion = nil
	if err := s.validateDCRRegistrationLocked(ctx); err != nil {
		return nil, projectOAuthError(err)
	}
	record, err := s.writer.Put(ctx, s.key, value, &expected)
	if err == nil {
		if err := s.installRecordLocked(record); err != nil {
			return nil, projectOAuthError(err)
		}
		if err := s.validateDCRRegistrationLocked(ctx); err != nil {
			return nil, projectOAuthError(err)
		}
		return &persistentTokenSource{state: s}, nil
	}
	if !errors.Is(err, credentialstore.ErrConflict) {
		return nil, projectOAuthError(err)
	}
	if err := s.reloadLocked(ctx); err != nil {
		return nil, projectOAuthError(err)
	}
	if s.dcrGrant.State != "active" {
		return nil, projectOAuthError(ErrOAuthLoginRequired)
	}
	return &persistentTokenSource{state: s}, nil
}

func (s *oauthCredentialState) resetDCRLocked(ctx context.Context) error {
	reset := newOAuthDCRResetGrant(s.identity, s.registration.generation)
	value, err := encodeOAuthDCRGrant(reset, s.identity, s.registration.generation, s.origins)
	if err != nil {
		return projectOAuthError(err)
	}
	var expected *credentialstore.Version
	if s.record != nil {
		version := s.record.Version
		expected = &version
	}
	record, err := s.writer.Put(ctx, s.key, value, expected)
	if err == nil {
		return projectOAuthError(s.installRecordLocked(record))
	}
	if !errors.Is(err, credentialstore.ErrConflict) {
		return projectOAuthError(err)
	}
	if err := s.reloadLocked(ctx); err != nil {
		return projectOAuthError(err)
	}
	return projectOAuthError(ErrOAuthDCRRecoveryRequired)
}

func (s *oauthCredentialState) validateDCRRegistrationLocked(ctx context.Context) error {
	identity := oauthDCRIdentity{Profile: s.identity.Profile, Principal: s.identity.Principal, Resource: s.identity.Resource, Issuer: s.identity.Issuer}
	key, err := oauthDCRRegistrationKey(identity)
	if err != nil {
		return err
	}
	record, err := s.reader.Get(ctx, key)
	if err != nil {
		return ErrOAuthDCRRecoveryRequired
	}
	stored, err := decodeOAuthDCRRecord(record.Value, identity)
	if err != nil || stored.State != oauthDCRStateReady || stored.Generation != s.registration.generation || stored.Registration.ClientID != s.registration.clientID {
		return ErrOAuthDCRRecoveryRequired
	}
	return nil
}

func cloneOAuthConfig(cfg *oauth2.Config) *oauth2.Config {
	clone := *cfg
	clone.Scopes = append([]string(nil), cfg.Scopes...)
	return &clone
}

func cloneOAuthToken(token *oauth2.Token) *oauth2.Token {
	if token == nil {
		return nil
	}
	clone := *token
	return &clone
}

func tokensEqual(a, b *oauth2.Token) bool {
	return a != nil && b != nil && a.AccessToken == b.AccessToken && a.TokenType == b.TokenType && a.RefreshToken == b.RefreshToken && a.Expiry.Equal(b.Expiry)
}

func isInvalidGrant(err error) bool {
	var retrieveError *oauth2.RetrieveError
	return errors.As(err, &retrieveError) && retrieveError.ErrorCode == "invalid_grant"
}
