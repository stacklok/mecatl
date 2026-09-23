package mcpbroker

import (
	"context"
	"errors"
	"sync"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

//nolint:gosec // Stable Redis namespace, not credential material.
const credentialAADNamespace = "mecatl:authserver:"

// encryptedAuthStorage decorates ToolHive's typed storage.Storage and its
// separate DCRCredentialStore seam. It leaves Redis key construction, indexes,
// expiry, and CAS ownership with ToolHive while sealing only the scoped fields.
type encryptedAuthStorage struct {
	storage.Storage
	dcr       storage.DCRCredentialStore
	keys      *credentialKeyRing
	closeOnce sync.Once
	closeErr  error
}

var (
	_ storage.Storage            = (*encryptedAuthStorage)(nil)
	_ storage.DCRCredentialStore = (*encryptedAuthStorage)(nil)
)

func newEncryptedAuthStorage(inner storage.Storage, keys *credentialKeyRing) (*encryptedAuthStorage, error) {
	if inner == nil || keys == nil {
		return nil, errCredentialEnvelope
	}
	dcr, ok := inner.(storage.DCRCredentialStore)
	if !ok || dcr == nil {
		return nil, errCredentialEnvelope
	}
	return &encryptedAuthStorage{Storage: inner, dcr: dcr, keys: keys}, nil
}

// Close closes the native store exactly once. No Unwrap method is provided:
// ToolHive must continue to see this decorator as the DCR store and must not
// reach raw Redis for legacy migration.
func (s *encryptedAuthStorage) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.Storage.Close() })
	return s.closeErr
}

func (s *encryptedAuthStorage) StoreUpstreamTokens(ctx context.Context, sessionID, provider string, tokens *storage.UpstreamTokens) error {
	if tokens == nil {
		return s.Storage.StoreUpstreamTokens(ctx, sessionID, provider, nil)
	}
	sealed, err := s.sealTokens(sessionID, provider, tokens)
	if err != nil {
		return err
	}
	return s.Storage.StoreUpstreamTokens(ctx, sessionID, provider, sealed)
}

func (s *encryptedAuthStorage) GetUpstreamTokens(ctx context.Context, sessionID, provider string) (*storage.UpstreamTokens, error) {
	raw, err := s.Storage.GetUpstreamTokens(ctx, sessionID, provider)
	if err != nil && !errors.Is(err, storage.ErrExpired) {
		return nil, err
	}
	if raw == nil {
		return nil, err
	}
	plain, openErr := s.openTokens(sessionID, provider, raw)
	if openErr != nil {
		return nil, openErr
	}
	return plain, err
}

func (s *encryptedAuthStorage) GetAllUpstreamTokens(ctx context.Context, sessionID string) (map[string]*storage.UpstreamTokens, error) {
	raw, err := s.Storage.GetAllUpstreamTokens(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*storage.UpstreamTokens, len(raw))
	for provider, tokens := range raw {
		if tokens == nil {
			out[provider] = nil
			continue
		}
		plain, openErr := s.openTokens(sessionID, provider, tokens)
		if openErr != nil {
			return nil, openErr
		}
		out[provider] = plain
	}
	return out, nil
}

func (s *encryptedAuthStorage) CompareAndSwapUpstreamTokens(ctx context.Context, sessionID, provider, expectedRefresh string, tokens *storage.UpstreamTokens) error {
	raw, err := s.Storage.GetUpstreamTokens(ctx, sessionID, provider)
	if err != nil && !errors.Is(err, storage.ErrExpired) {
		return err
	}
	if raw == nil {
		if tokens == nil {
			return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, expectedRefresh, nil)
		}
		sealed, sealErr := s.sealTokens(sessionID, provider, tokens)
		if sealErr != nil {
			return sealErr
		}
		return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, expectedRefresh, sealed)
	}
	plain, openErr := s.openTokens(sessionID, provider, raw)
	if openErr != nil {
		return openErr
	}
	if plain.RefreshToken != expectedRefresh {
		return storage.ErrConcurrentRefresh
	}
	if tokens == nil {
		return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, raw.RefreshToken, nil)
	}
	sealed, sealErr := s.sealTokens(sessionID, provider, tokens)
	if sealErr != nil {
		return sealErr
	}
	return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, raw.RefreshToken, sealed)
}

// GetLatestUpstreamTokensForUser deliberately has no session ID to bind into
// AAD. It must not become an authority source for session-scoped custody.
func (*encryptedAuthStorage) GetLatestUpstreamTokensForUser(context.Context, string, string) (*storage.UpstreamTokens, error) {
	return nil, errCredentialEnvelope
}

func (s *encryptedAuthStorage) GetDCRCredentials(ctx context.Context, key storage.DCRKey) (*storage.DCRCredentials, error) {
	raw, err := s.dcr.GetDCRCredentials(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.openDCR(key, raw)
}

func (s *encryptedAuthStorage) StoreDCRCredentialsIfAbsent(ctx context.Context, creds *storage.DCRCredentials) (*storage.DCRCredentials, error) {
	if creds == nil {
		return nil, errCredentialEnvelope
	}
	sealed, err := s.sealDCR(creds.Key, creds)
	if err != nil {
		return nil, err
	}
	raw, err := s.dcr.StoreDCRCredentialsIfAbsent(ctx, sealed)
	if err != nil {
		return nil, err
	}
	return s.openDCR(creds.Key, raw)
}

func (s *encryptedAuthStorage) sealTokens(sessionID, provider string, in *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
	if in == nil || sessionID == "" || provider == "" || in.ProviderID != provider {
		return nil, errCredentialEnvelope
	}
	out := *in
	var err error
	if out.AccessToken, err = s.sealUpstreamField(sessionID, provider, "access", out.AccessToken); err != nil {
		return nil, err
	}
	if out.RefreshToken, err = s.sealUpstreamField(sessionID, provider, "refresh", out.RefreshToken); err != nil {
		return nil, err
	}
	if out.IDToken, err = s.sealUpstreamField(sessionID, provider, "id", out.IDToken); err != nil {
		return nil, err
	}
	if out.UpstreamSubject, err = s.sealUpstreamField(sessionID, provider, "subject", out.UpstreamSubject); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *encryptedAuthStorage) openTokens(sessionID, provider string, in *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
	if in == nil || sessionID == "" || provider == "" || in.ProviderID != provider {
		return nil, errCredentialEnvelope
	}
	out := *in
	var err error
	if out.AccessToken, err = s.openUpstreamField(sessionID, provider, "access", out.AccessToken); err != nil {
		return nil, err
	}
	if out.RefreshToken, err = s.openUpstreamField(sessionID, provider, "refresh", out.RefreshToken); err != nil {
		return nil, err
	}
	if out.IDToken, err = s.openUpstreamField(sessionID, provider, "id", out.IDToken); err != nil {
		return nil, err
	}
	if out.UpstreamSubject, err = s.openUpstreamField(sessionID, provider, "subject", out.UpstreamSubject); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *encryptedAuthStorage) sealDCR(key storage.DCRKey, in *storage.DCRCredentials) (*storage.DCRCredentials, error) {
	if in == nil || in.Key != key {
		return nil, errCredentialEnvelope
	}
	out := *in
	var err error
	if out.ClientSecret, err = s.sealDCRField(key, "client_secret", out.ClientSecret); err != nil {
		return nil, err
	}
	if out.RegistrationAccessToken, err = s.sealDCRField(key, "registration_access_token", out.RegistrationAccessToken); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *encryptedAuthStorage) openDCR(key storage.DCRKey, in *storage.DCRCredentials) (*storage.DCRCredentials, error) {
	if in == nil || in.Key != key {
		return nil, errCredentialEnvelope
	}
	out := *in
	var err error
	if out.ClientSecret, err = s.openDCRField(key, "client_secret", out.ClientSecret); err != nil {
		return nil, err
	}
	if out.RegistrationAccessToken, err = s.openDCRField(key, "registration_access_token", out.RegistrationAccessToken); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *encryptedAuthStorage) sealUpstreamField(sessionID, provider, field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return s.keys.seal(credentialAAD(credentialAADNamespace, "upstream", sessionID, provider, field), value)
}

func (s *encryptedAuthStorage) openUpstreamField(sessionID, provider, field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return s.keys.open(credentialAAD(credentialAADNamespace, "upstream", sessionID, provider, field), value)
}

func (s *encryptedAuthStorage) sealDCRField(key storage.DCRKey, field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return s.keys.seal(credentialAAD(credentialAADNamespace, "dcr", key.Issuer, key.UpstreamID, key.RedirectURI, key.ScopesHash, field), value)
}

func (s *encryptedAuthStorage) openDCRField(key storage.DCRKey, field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return s.keys.open(credentialAAD(credentialAADNamespace, "dcr", key.Issuer, key.UpstreamID, key.RedirectURI, key.ScopesHash, field), value)
}
