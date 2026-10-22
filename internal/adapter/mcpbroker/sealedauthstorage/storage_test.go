package sealedauthstorage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/credentialenvelope"
)

//nolint:gosec // Persisted test namespace, not credential material.
const testNamespace = "mecatl:authserver:"

func testCredentialKeyRing(t *testing.T) *credentialenvelope.KeyRing {
	t.Helper()
	ring, err := credentialenvelope.NewKeyRing("key-a", map[string][]byte{"key-a": []byte(strings.Repeat("k", credentialenvelope.KeyBytes))})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func TestNamespaceBinding(t *testing.T) {
	inner := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = inner.Close() })
	ring := testCredentialKeyRing(t)
	if _, err := New(inner, ring, ""); !errors.Is(err, credentialenvelope.ErrUnavailable) {
		t.Fatalf("missing namespace = %v", err)
	}
	original, err := New(inner, ring, testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(inner, ring, "other:authserver:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := original.StoreUpstreamTokens(ctx, "session", "provider", &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "namespace-canary"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.GetUpstreamTokens(ctx, "session", "provider"); !errors.Is(err, credentialenvelope.ErrUnavailable) {
		t.Fatalf("upstream ciphertext opened in a different namespace: %v", err)
	}
	creds := &storage.DCRCredentials{
		Key:      storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"},
		ClientID: "client", ClientSecret: "namespace-secret", RegistrationAccessToken: "namespace-registration",
		AuthorizationEndpoint: "https://issuer/authorize", TokenEndpoint: "https://issuer/token",
	}
	if _, err := original.StoreDCRCredentialsIfAbsent(ctx, creds); err != nil {
		t.Fatal(err)
	}
	if _, err := other.GetDCRCredentials(ctx, creds.Key); !errors.Is(err, credentialenvelope.ErrUnavailable) {
		t.Fatalf("DCR ciphertext opened in a different namespace: %v", err)
	}
}

func TestEncryptedAuthStorage_EncryptsAndBindsFields(t *testing.T) {
	inner := storage.NewMemoryStorage()
	decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	tokens := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "access-canary", RefreshToken: "refresh-canary", IDToken: "id-token-canary", UpstreamSubject: "subject-canary"}
	if err := decorated.StoreUpstreamTokens(ctx, "tsid", "provider", tokens); err != nil {
		t.Fatal(err)
	}
	raw, err := inner.GetUpstreamTokens(ctx, "tsid", "provider")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"access-canary", "refresh-canary", "id-token-canary", "subject-canary"} {
		if strings.Contains(raw.AccessToken+raw.RefreshToken+raw.IDToken+raw.UpstreamSubject, secret) {
			t.Fatalf("plaintext %q persisted", secret)
		}
	}
	swapped := *raw
	swapped.AccessToken, swapped.RefreshToken = raw.RefreshToken, raw.AccessToken
	if err := inner.StoreUpstreamTokens(ctx, "tsid", "provider", &swapped); err != nil {
		t.Fatal(err)
	}
	if _, err := decorated.GetUpstreamTokens(ctx, "tsid", "provider"); err == nil {
		t.Fatal("ciphertext moved across fields decrypted")
	}
	movedProvider := *raw
	movedProvider.ProviderID = "other"
	if err := inner.StoreUpstreamTokens(ctx, "tsid", "other", &movedProvider); err != nil {
		t.Fatal(err)
	}
	if _, err := decorated.GetUpstreamTokens(ctx, "tsid", "other"); err == nil {
		t.Fatal("ciphertext moved across providers decrypted")
	}
	if _, err := decorated.GetAllUpstreamTokens(ctx, "tsid"); err == nil {
		t.Fatal("bulk provider mismatch accepted")
	}
	if err := inner.StoreUpstreamTokens(ctx, "tsid", "plain", &storage.UpstreamTokens{ProviderID: "plain", AccessToken: "plaintext"}); err != nil {
		t.Fatal(err)
	}
	if _, err := decorated.GetUpstreamTokens(ctx, "tsid", "plain"); err == nil {
		t.Fatal("plaintext protected field accepted")
	}
	if _, err := decorated.GetLatestUpstreamTokensForUser(ctx, "user", "provider"); !errors.Is(err, credentialenvelope.ErrUnavailable) {
		t.Fatalf("latest lookup = %v", err)
	}
}

func TestEncryptedAuthStorage_NilAndCASSemantics(t *testing.T) {
	inner := storage.NewMemoryStorage()
	decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := decorated.StoreUpstreamTokens(ctx, "nil", "provider", nil); err != nil {
		t.Fatalf("store nil: %v", err)
	}
	got, err := decorated.GetUpstreamTokens(ctx, "nil", "provider")
	if err != nil || got != nil {
		t.Fatalf("get nil = %#v, %v", got, err)
	}
	all, err := decorated.GetAllUpstreamTokens(ctx, "nil")
	if err != nil || all["provider"] != nil {
		t.Fatalf("all nil = %#v, %v", all, err)
	}
	if err := decorated.CompareAndSwapUpstreamTokens(ctx, "nil", "provider", "", nil); err != nil {
		t.Fatalf("CAS nil = %v", err)
	}
	nilReplacement := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "replacement", RefreshToken: "replacement-refresh"}
	if err := decorated.CompareAndSwapUpstreamTokens(ctx, "nil", "provider", "", nilReplacement); err != nil {
		t.Fatalf("CAS nil replacement = %v", err)
	}
	rawReplacement, err := inner.GetUpstreamTokens(ctx, "nil", "provider")
	if err != nil || strings.Contains(rawReplacement.AccessToken+rawReplacement.RefreshToken, "replacement") {
		t.Fatalf("CAS nil replacement persisted plaintext = %#v, %v", rawReplacement, err)
	}
	got, err = decorated.GetUpstreamTokens(ctx, "nil", "provider")
	if err != nil || got.AccessToken != nilReplacement.AccessToken || got.RefreshToken != nilReplacement.RefreshToken {
		t.Fatalf("CAS nil replacement decrypt = %#v, %v", got, err)
	}
	row := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "a", RefreshToken: "r"}
	if err := decorated.StoreUpstreamTokens(ctx, "row", "provider", row); err != nil {
		t.Fatal(err)
	}
	if err := decorated.CompareAndSwapUpstreamTokens(ctx, "row", "provider", "wrong", nil); !errors.Is(err, storage.ErrConcurrentRefresh) {
		t.Fatalf("wrong CAS = %v", err)
	}
	if err := decorated.CompareAndSwapUpstreamTokens(ctx, "row", "provider", "r", nil); err != nil {
		t.Fatalf("nil replacement CAS = %v", err)
	}
}

func TestEncryptedAuthStorage_DCRAndKeyOverlap(t *testing.T) {
	ctx := t.Context()
	oldRing, err := credentialenvelope.NewKeyRing("old", map[string][]byte{"old": []byte(strings.Repeat("a", 32))})
	if err != nil {
		t.Fatal(err)
	}
	inner := storage.NewMemoryStorage()
	oldStore, err := New(inner, oldRing, testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldStore.StoreUpstreamTokens(ctx, "tsid", "provider", &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "old-access"}); err != nil {
		t.Fatal(err)
	}
	key := storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"}
	creds := &storage.DCRCredentials{Key: key, ProviderName: "provider", ClientID: "id", ClientSecret: "old-secret", RegistrationAccessToken: "registration", AuthorizationEndpoint: "https://issuer/auth", TokenEndpoint: "https://issuer/token"}
	if _, err := oldStore.StoreDCRCredentialsIfAbsent(ctx, creds); err != nil {
		t.Fatal(err)
	}
	newRing, err := credentialenvelope.NewKeyRing("new", map[string][]byte{"old": []byte(strings.Repeat("a", 32)), "new": []byte(strings.Repeat("b", 32))})
	if err != nil {
		t.Fatal(err)
	}
	newStore, err := New(inner, newRing, testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	got, err := newStore.GetUpstreamTokens(ctx, "tsid", "provider")
	if err != nil || got.AccessToken != "old-access" {
		t.Fatalf("old upstream decrypt = %#v, %v", got, err)
	}
	gotDCR, err := newStore.GetDCRCredentials(ctx, key)
	if err != nil || gotDCR.ClientSecret != "old-secret" {
		t.Fatalf("old DCR decrypt = %#v, %v", gotDCR, err)
	}
	raw, err := inner.GetDCRCredentials(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	key2 := key
	key2.RedirectURI = "https://broker/other"
	moved := *raw
	moved.Key = key2
	if _, err := inner.StoreDCRCredentialsIfAbsent(ctx, &moved); err != nil {
		t.Fatal(err)
	}
	if _, err := newStore.GetDCRCredentials(ctx, key2); err == nil {
		t.Fatal("DCR ciphertext moved across keys decrypted")
	}
}

func TestEncryptedAuthStorage_ExpiredAndDCRWinner(t *testing.T) {
	inner := storage.NewMemoryStorage()
	decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	expired := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute)}
	if err := decorated.StoreUpstreamTokens(ctx, "expired", "provider", expired); err != nil {
		t.Fatal(err)
	}
	got, err := decorated.GetUpstreamTokens(ctx, "expired", "provider")
	if !errors.Is(err, storage.ErrExpired) || got == nil || got.AccessToken != "access" {
		t.Fatalf("expired = %#v, %v", got, err)
	}
	key := storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"}
	first := &storage.DCRCredentials{Key: key, ProviderName: "provider", ClientID: "first", ClientSecret: "first-secret", AuthorizationEndpoint: "https://issuer/auth", TokenEndpoint: "https://issuer/token"}
	second := *first
	second.ClientID, second.ClientSecret = "second", "second-secret"
	if _, err := decorated.StoreDCRCredentialsIfAbsent(ctx, first); err != nil {
		t.Fatal(err)
	}
	winner, err := decorated.StoreDCRCredentialsIfAbsent(ctx, &second)
	if err != nil || winner.ClientID != "first" || winner.ClientSecret != "first-secret" {
		t.Fatalf("DCR winner = %#v, %v", winner, err)
	}
}

func TestEncryptedAuthStorage_DCRCanaryAndActiveRotation(t *testing.T) {
	ctx := t.Context()
	oldKey := []byte(strings.Repeat("a", 32))
	newKey := []byte(strings.Repeat("b", 32))
	oldRing, _ := credentialenvelope.NewKeyRing("old", map[string][]byte{"old": oldKey})
	inner := storage.NewMemoryStorage()
	oldStore, _ := New(inner, oldRing, testNamespace)
	key := storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"}
	creds := &storage.DCRCredentials{Key: key, ProviderName: "provider", ClientID: "id", ClientSecret: "client-secret-canary", RegistrationAccessToken: "registration-token-canary", AuthorizationEndpoint: "https://issuer/auth", TokenEndpoint: "https://issuer/token"}
	if _, err := oldStore.StoreDCRCredentialsIfAbsent(ctx, creds); err != nil {
		t.Fatal(err)
	}
	raw, err := inner.GetDCRCredentials(ctx, key)
	if err != nil || strings.Contains(raw.ClientSecret, "client-secret-canary") || strings.Contains(raw.RegistrationAccessToken, "registration-token-canary") {
		t.Fatalf("raw DCR = %#v, %v", raw, err)
	}
	rotated, _ := credentialenvelope.NewKeyRing("new", map[string][]byte{"old": oldKey, "new": newKey})
	newStore, _ := New(inner, rotated, testNamespace)
	if err := newStore.StoreUpstreamTokens(ctx, "rotated", "provider", &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "new-access"}); err != nil {
		t.Fatal(err)
	}
	row, err := inner.GetUpstreamTokens(ctx, "rotated", "provider")
	if err != nil || !strings.Contains(row.AccessToken, ".new.") {
		t.Fatalf("active key row = %#v, %v", row, err)
	}
	retired, _ := credentialenvelope.NewKeyRing("new", map[string][]byte{"new": newKey})
	retiredStore, _ := New(inner, retired, testNamespace)
	if _, err := retiredStore.GetDCRCredentials(ctx, key); err == nil {
		t.Fatal("retired ring decrypted old DCR row")
	}
}

type refreshFaultStorage struct {
	*storage.MemoryStorage
	compare func(context.Context, string, string, string, *storage.UpstreamTokens) error
	deletes int
}

func (s *refreshFaultStorage) CompareAndSwapUpstreamTokens(ctx context.Context, sessionID, provider, expected string, tokens *storage.UpstreamTokens) error {
	if s.compare != nil {
		return s.compare(ctx, sessionID, provider, expected, tokens)
	}
	return s.MemoryStorage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, expected, tokens)
}
func (s *refreshFaultStorage) DeleteUpstreamTokensForProvider(ctx context.Context, sessionID, provider string) error {
	s.deletes++
	return s.MemoryStorage.DeleteUpstreamTokensForProvider(ctx, sessionID, provider)
}

func TestEncryptedAuthStorage_RealRefresherPersistenceOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		setup       func(*refreshFaultStorage, *Storage)
		want        string
		wantErr     bool
		wantDeletes int
	}{
		{
			name: "non_rotating persistence failure remains usable", body: `{"access_token":"fresh-nonrotating","token_type":"Bearer","expires_in":3600}`,
			setup: func(f *refreshFaultStorage, _ *Storage) {
				f.compare = func(context.Context, string, string, string, *storage.UpstreamTokens) error {
					return errors.New("persist failed")
				}
			},
			want: "fresh-nonrotating", wantDeletes: 0,
		},
		{
			name: "rotating persistence failure deletes stale row", body: `{"access_token":"fresh-rotating","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`,
			setup: func(f *refreshFaultStorage, _ *Storage) {
				f.compare = func(context.Context, string, string, string, *storage.UpstreamTokens) error {
					return errors.New("persist failed")
				}
			},
			wantErr: true, wantDeletes: 1,
		},
		{
			name: "conflict returns valid winner", body: `{"access_token":"loser","refresh_token":"loser-refresh","token_type":"Bearer","expires_in":3600}`,
			setup: func(f *refreshFaultStorage, encrypted *Storage) {
				installed := false
				f.compare = func(ctx context.Context, sessionID, provider, _ string, _ *storage.UpstreamTokens) error {
					if !installed {
						installed = true
						winner := &storage.UpstreamTokens{ProviderID: provider, AccessToken: "winner", RefreshToken: "winner-refresh", ExpiresAt: time.Now().Add(time.Hour), SessionExpiresAt: time.Now().Add(time.Hour)}
						if err := encrypted.StoreUpstreamTokens(ctx, sessionID, provider, winner); err != nil {
							return err
						}
					}
					return storage.ErrConcurrentRefresh
				}
			},
			want: "winner", wantDeletes: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, fault, encrypted, inner := newRealRefresherFixture(t, tc.body)
			tc.setup(fault, encrypted)
			credential, err := service.GetValidTokens(t.Context(), "tsid", "provider")
			if tc.wantErr {
				if err == nil || credential != nil {
					t.Fatalf("GetValidTokens = %#v, %v", credential, err)
				}
			} else if err != nil || credential == nil || credential.AccessToken != tc.want {
				t.Fatalf("GetValidTokens = %#v, %v", credential, err)
			}
			if fault.deletes != tc.wantDeletes {
				t.Fatalf("deletes = %d, want %d", fault.deletes, tc.wantDeletes)
			}
			if !tc.wantErr {
				raw, rawErr := inner.GetUpstreamTokens(t.Context(), "tsid", "provider")
				if (rawErr != nil && !errors.Is(rawErr, storage.ErrExpired)) || raw == nil || strings.Contains(raw.AccessToken+raw.RefreshToken, "fresh") || strings.Contains(raw.AccessToken+raw.RefreshToken, "winner") {
					t.Fatalf("raw persisted row = %#v, %v", raw, rawErr)
				}
			}
		})
	}
}

func newRealRefresherFixture(t *testing.T, response string) (*upstreamtoken.InProcessService, *refreshFaultStorage, *Storage, *storage.MemoryStorage) {
	t.Helper()
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(token.Close)
	inner := storage.NewMemoryStorage()
	fault := &refreshFaultStorage{MemoryStorage: inner}
	encrypted, err := New(fault, testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := runner.NewEmbeddedAuthServerWithStorage(t.Context(), &authserver.RunConfig{Issuer: "https://broker.example", AllowedAudiences: []string{"https://broker.example"}, Upstreams: []authserver.UpstreamRunConfig{{Name: "provider", Type: authserver.UpstreamProviderTypeOAuth2, OAuth2Config: &authserver.OAuth2UpstreamRunConfig{AuthorizationEndpoint: token.URL + "/authorize", TokenEndpoint: token.URL + "/token", ClientID: "client", RedirectURI: "https://broker.example/callback"}}}}, encrypted)
	if err != nil {
		t.Fatalf("embedded auth: %v", err)
	}
	t.Cleanup(func() { _ = auth.Close() })
	expired := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "expired", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute), SessionExpiresAt: time.Now().Add(time.Hour)}
	if err := encrypted.StoreUpstreamTokens(t.Context(), "tsid", "provider", expired); err != nil {
		t.Fatal(err)
	}
	return upstreamtoken.NewInProcessService(auth.IDPTokenStorage(), auth.UpstreamTokenRefresher()), fault, encrypted, inner
}

func TestEncryptedAuthStorage_FieldClassification(t *testing.T) {
	decorated, err := New(storage.NewMemoryStorage(), testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	key := storage.DCRKey{Issuer: "https://issuer.example", RedirectURI: "https://broker.example/cb", ScopesHash: "scopes"}
	tokens := storage.UpstreamTokens{ProviderID: "provider"}
	dcr := storage.DCRCredentials{Key: key}
	for _, tc := range []struct {
		name      string
		value     any
		encrypted []string
		public    []string
		seal      func(any) (any, error)
	}{
		{
			name:      "UpstreamTokens",
			value:     &tokens,
			encrypted: []string{"AccessToken", "RefreshToken", "IDToken", "UpstreamSubject"},
			public:    []string{"ProviderID", "UserID", "ClientID", "ExpiresAt", "SessionExpiresAt"}, // Bindings and lifetimes.
			seal: func(in any) (any, error) {
				return decorated.sealTokens("session", "provider", in.(*storage.UpstreamTokens))
			},
		},
		{
			name:      "DCRCredentials",
			value:     &dcr,
			encrypted: []string{"ClientSecret", "RegistrationAccessToken"},
			public: []string{
				"Key", "ProviderName", "ClientID", // Record identity and public client identifier.
				"TokenEndpointAuthMethod", "RegistrationClientURI", "AuthorizationEndpoint", "TokenEndpoint", // Protocol metadata.
				"CreatedAt", "ClientSecretExpiresAt", // Lifetimes.
			},
			seal: func(in any) (any, error) {
				return decorated.sealDCR(key, in.(*storage.DCRCredentials))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typ := reflect.TypeOf(tc.value).Elem()
			classified := make(map[string]bool)
			for _, fields := range [][]string{tc.encrypted, tc.public} {
				for _, name := range fields {
					if classified[name] {
						t.Fatalf("field %s classified twice", name)
					}
					if _, ok := typ.FieldByName(name); !ok {
						t.Fatalf("classified field %s no longer exists", name)
					}
					classified[name] = true
				}
			}
			for i := range typ.NumField() {
				if name := typ.Field(i).Name; !classified[name] {
					t.Errorf("field %s requires an explicit encrypted/public classification", name)
				}
			}
			// The classification must match what the decorator really seals: give
			// every encrypted string field a canary and require it to be gone.
			value := reflect.ValueOf(tc.value).Elem()
			for _, name := range tc.encrypted {
				value.FieldByName(name).SetString("canary-" + name)
			}
			sealedAny, err := tc.seal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			sealed := reflect.ValueOf(sealedAny).Elem()
			for _, name := range tc.encrypted {
				got := sealed.FieldByName(name).String()
				if got == "canary-"+name || strings.Contains(got, "canary") || !strings.HasPrefix(got, "mecatl.") {
					t.Errorf("%s is classified encrypted but was stored as %q", name, got)
				}
			}
			for _, name := range tc.public {
				if !reflect.DeepEqual(sealed.FieldByName(name).Interface(), value.FieldByName(name).Interface()) {
					t.Errorf("%s is classified public but was altered", name)
				}
			}
		})
	}
}

func TestEncryptedAuthStorage_DeviceConfirmationFailsClosed(t *testing.T) {
	inner := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = inner.Close() })
	decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	pending := &storage.PendingDeviceConfirmation{UpstreamTokens: &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "canary", RefreshToken: "canary"}}
	if err := decorated.StorePendingDeviceConfirmation(t.Context(), "token", pending); !errors.Is(err, credentialenvelope.ErrUnavailable) {
		t.Fatalf("store = %v, want ErrUnavailable", err)
	}
	if got, err := decorated.LoadPendingDeviceConfirmation(t.Context(), "token"); got != nil || !errors.Is(err, credentialenvelope.ErrUnavailable) {
		t.Fatalf("load = %v, %v, want ErrUnavailable", got, err)
	}
	if got, err := inner.LoadPendingDeviceConfirmation(t.Context(), "token"); got != nil || err == nil {
		t.Fatal("device confirmation reached native storage unsealed")
	}
}

var encryptedStorageTestBackends = map[string]func(*testing.T) storage.Storage{
	"memory": func(t *testing.T) storage.Storage {
		t.Helper()
		inner := storage.NewMemoryStorage()
		t.Cleanup(func() { _ = inner.Close() })
		return inner
	},
	"redis": func(t *testing.T) storage.Storage {
		t.Helper()
		client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
		inner := storage.NewRedisStorageWithClient(client, "test:encrypted:")
		t.Cleanup(func() { _ = inner.Close() })
		return inner
	},
}

type authoritativeDCRUpdateStorage struct {
	storage.Storage
	storage.DCRCredentialStore
	update func(context.Context, *storage.DCRCredentials) (*storage.DCRCredentials, error)
}

func (s *authoritativeDCRUpdateStorage) UpdateDCRCredentialsIfPresent(ctx context.Context, creds *storage.DCRCredentials) (*storage.DCRCredentials, error) {
	return s.update(ctx, creds)
}

func TestEncryptedAuthStorage_DCRUpdateReturnsAuthoritativeValue(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			inner := makeStorage(t)
			interposed := &authoritativeDCRUpdateStorage{Storage: inner, DCRCredentialStore: inner.(storage.DCRCredentialStore)}
			decorated, err := New(interposed, testCredentialKeyRing(t), testNamespace)
			if err != nil {
				t.Fatal(err)
			}
			creds := storage.DCRCredentials{
				Key:      storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"},
				ClientID: "client", ClientSecret: "initial-secret", RegistrationAccessToken: "initial-registration",
				AuthorizationEndpoint: "https://issuer/authorize", TokenEndpoint: "https://issuer/token",
			}
			if _, err := decorated.StoreDCRCredentialsIfAbsent(t.Context(), &creds); err != nil {
				t.Fatal(err)
			}
			submitted := creds
			submitted.ClientSecret, submitted.RegistrationAccessToken = "submitted-secret", "submitted-registration"
			authoritative := creds
			authoritative.ClientSecret, authoritative.RegistrationAccessToken = "backend-secret", "backend-registration"
			sealed, err := decorated.sealDCR(creds.Key, &authoritative)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			interposed.update = func(ctx context.Context, input *storage.DCRCredentials) (*storage.DCRCredentials, error) {
				called = true
				if input.Key != creds.Key || !strings.HasPrefix(input.ClientSecret, "mecatl.v1.") || !strings.HasPrefix(input.RegistrationAccessToken, "mecatl.v1.") {
					t.Fatal("update sent an invalid key or plaintext to the backend")
				}
				// The backend persists and returns its authoritative representation.
				return interposed.DCRCredentialStore.UpdateDCRCredentialsIfPresent(ctx, sealed)
			}
			input := submitted
			got, err := decorated.UpdateDCRCredentialsIfPresent(t.Context(), &input)
			if !called || err != nil || !reflect.DeepEqual(got, &authoritative) || !reflect.DeepEqual(input, submitted) {
				t.Fatalf("update did not return the backend value without mutating input: %v", err)
			}
			stored, err := decorated.GetDCRCredentials(t.Context(), creds.Key)
			if err != nil || !reflect.DeepEqual(stored, &authoritative) {
				t.Fatalf("authoritative return did not match the durable value: %v", err)
			}
		})
	}
}

func TestEncryptedAuthStorage_RedisDCRUpdateTTLAndPresence(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	native := storage.NewRedisStorageWithClient(client, "test:dcr-ttl:")
	t.Cleanup(func() { _ = native.Close() })
	decorated, err := New(native, testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	now := time.Unix(time.Now().Unix(), 0)
	creds := storage.DCRCredentials{
		Key:      storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"},
		ClientID: "client", ClientSecret: "ttl-secret", RegistrationAccessToken: "ttl-registration",
		AuthorizationEndpoint: "https://issuer/authorize", TokenEndpoint: "https://issuer/token",
		ClientSecretExpiresAt: now.Add(time.Hour),
	}
	if _, err := decorated.StoreDCRCredentialsIfAbsent(ctx, &creds); err != nil {
		t.Fatal(err)
	}
	keys := server.Keys()
	if len(keys) != 1 {
		t.Fatalf("native DCR store created %d keys, want 1", len(keys))
	}
	key := keys[0]
	for _, tc := range []struct {
		name   string
		expiry time.Time
		minTTL time.Duration
		maxTTL time.Duration
	}{
		{name: "extend", expiry: now.Add(2 * time.Hour), minTTL: 119 * time.Minute, maxTTL: 2 * time.Hour},
		{name: "shorten", expiry: now.Add(30 * time.Minute), minTTL: 29 * time.Minute, maxTTL: 30 * time.Minute},
		{name: "clear"},
		{name: "expired but physically present", expiry: now.Add(-time.Hour), minTTL: time.Nanosecond, maxTTL: time.Second},
		{name: "rewrite expired row", expiry: now.Add(time.Hour), minTTL: 59 * time.Minute, maxTTL: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds.ClientSecretExpiresAt = tc.expiry
			input := creds
			got, err := decorated.UpdateDCRCredentialsIfPresent(ctx, &creds)
			if err != nil || !reflect.DeepEqual(got, &input) || !reflect.DeepEqual(creds, input) {
				t.Fatalf("native DCR update changed its input or authoritative return: %v", err)
			}
			if ttl := server.TTL(key); ttl < tc.minTTL || ttl > tc.maxTTL {
				t.Fatalf("native TTL = %v, want [%v,%v]", ttl, tc.minTTL, tc.maxTTL)
			}
			stored, err := decorated.GetDCRCredentials(ctx, creds.Key)
			if err != nil || !reflect.DeepEqual(stored, &input) {
				t.Fatalf("native physical-presence read = %v", err)
			}
		})
	}
	server.FastForward(2 * time.Hour)
	if _, err := decorated.GetDCRCredentials(ctx, creds.Key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("evicted DCR Get = %v", err)
	}
	if _, err := decorated.UpdateDCRCredentialsIfPresent(ctx, &creds); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("evicted DCR Update = %v", err)
	}
	if server.Exists(key) {
		t.Fatal("update recreated an evicted DCR row")
	}
}

func TestEncryptedAuthStorage_RejectsCrossSessionCiphertext(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			inner := makeStorage(t)
			decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
			if err != nil {
				t.Fatal(err)
			}
			tokens := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "session-access", RefreshToken: "session-refresh", IDToken: "session-id", UpstreamSubject: "session-subject"}
			if err := decorated.StoreUpstreamTokens(t.Context(), "original-session", "provider", tokens); err != nil {
				t.Fatal(err)
			}
			raw, err := inner.GetUpstreamTokens(t.Context(), "original-session", "provider")
			if err != nil {
				t.Fatal(err)
			}
			if err := inner.StoreUpstreamTokens(t.Context(), "other-session", "provider", raw); err != nil {
				t.Fatal(err)
			}
			if got, err := decorated.GetUpstreamTokens(t.Context(), "other-session", "provider"); got != nil || !errors.Is(err, credentialenvelope.ErrUnavailable) {
				t.Fatalf("cross-session ciphertext opened: %v", err)
			}
			if got, err := decorated.GetAllUpstreamTokens(t.Context(), "other-session"); got != nil || !errors.Is(err, credentialenvelope.ErrUnavailable) {
				t.Fatalf("bulk read accepted cross-session ciphertext: %v", err)
			}
			if got, err := decorated.GetUpstreamTokens(t.Context(), "original-session", "provider"); err != nil || !reflect.DeepEqual(got, tokens) {
				t.Fatalf("relocation affected the original row: %v", err)
			}
		})
	}
}

func TestEncryptedAuthStorage_NativeRoundTripAndDCRUpdate(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			inner := makeStorage(t)
			decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			now := time.Unix(time.Now().Unix(), 0)
			tokens := storage.UpstreamTokens{
				ProviderID: "provider", AccessToken: "access-plaintext", RefreshToken: "refresh-plaintext",
				IDToken: "id-plaintext", UpstreamSubject: "subject-plaintext", UserID: "user", ClientID: "client",
				ExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(2 * time.Hour),
			}
			original := tokens
			if err := decorated.StoreUpstreamTokens(ctx, "session", "provider", &tokens); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tokens, original) {
				t.Fatal("token write mutated input")
			}
			raw, err := inner.GetUpstreamTokens(ctx, "session", "provider")
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{raw.AccessToken, raw.RefreshToken, raw.IDToken, raw.UpstreamSubject} {
				if !strings.HasPrefix(value, "mecatl.v1.a256gcm.") || strings.Contains(value, "plaintext") {
					t.Fatal("backing store did not encrypt every upstream credential field")
				}
			}
			publicTokens := *raw
			publicTokens.AccessToken, publicTokens.RefreshToken = tokens.AccessToken, tokens.RefreshToken
			publicTokens.IDToken, publicTokens.UpstreamSubject = tokens.IDToken, tokens.UpstreamSubject
			if !reflect.DeepEqual(publicTokens, original) {
				t.Fatal("public token fields changed")
			}
			got, err := decorated.GetUpstreamTokens(ctx, "session", "provider")
			if err != nil || !reflect.DeepEqual(got, &original) {
				t.Fatalf("token round trip mismatch: %v", err)
			}
			got.AccessToken = "mutated-return"
			got, err = decorated.GetUpstreamTokens(ctx, "session", "provider")
			if err != nil || !reflect.DeepEqual(got, &original) {
				t.Fatalf("token return mutated storage: %v", err)
			}

			creds := storage.DCRCredentials{
				Key:          storage.DCRKey{Issuer: "https://issuer.example", UpstreamID: "provider", RedirectURI: "https://broker.example/callback", ScopesHash: "scope"},
				ProviderName: "provider", ClientID: "registered-client", ClientSecret: "secret-plaintext",
				RegistrationAccessToken: "registration-plaintext", TokenEndpointAuthMethod: "client_secret_basic",
				RegistrationClientURI: "https://issuer.example/registration", AuthorizationEndpoint: "https://issuer.example/authorize",
				TokenEndpoint: "https://issuer.example/token", CreatedAt: now, ClientSecretExpiresAt: now.Add(time.Hour),
			}
			if _, err := decorated.UpdateDCRCredentialsIfPresent(ctx, &creds); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("missing DCR update = %v", err)
			}
			for _, update := range []bool{false, true} {
				if update {
					creds.ClientSecret, creds.RegistrationAccessToken = "updated-secret-plaintext", "updated-registration-plaintext"
					creds.ClientSecretExpiresAt = now.Add(2 * time.Hour)
				}
				input := creds
				var stored *storage.DCRCredentials
				if update {
					stored, err = decorated.UpdateDCRCredentialsIfPresent(ctx, &creds)
				} else {
					stored, err = decorated.StoreDCRCredentialsIfAbsent(ctx, &creds)
				}
				if err != nil || !reflect.DeepEqual(stored, &input) || !reflect.DeepEqual(creds, input) {
					t.Fatalf("DCR write/return mismatch (update=%v): %v", update, err)
				}
				rawDCR, err := inner.(storage.DCRCredentialStore).GetDCRCredentials(ctx, creds.Key)
				if err != nil {
					t.Fatal(err)
				}
				for _, value := range []string{rawDCR.ClientSecret, rawDCR.RegistrationAccessToken} {
					if !strings.HasPrefix(value, "mecatl.v1.a256gcm.") || strings.Contains(value, "plaintext") {
						t.Fatal("backing store did not encrypt every DCR credential field")
					}
				}
				publicDCR := *rawDCR
				publicDCR.ClientSecret, publicDCR.RegistrationAccessToken = input.ClientSecret, input.RegistrationAccessToken
				if !reflect.DeepEqual(publicDCR, input) {
					t.Fatal("public DCR fields changed")
				}
				loser := input
				loser.ClientID, loser.ClientSecret = "losing-client", "losing-secret"
				winner, err := decorated.StoreDCRCredentialsIfAbsent(ctx, &loser)
				if err != nil || !reflect.DeepEqual(winner, &input) {
					t.Fatalf("DCR did not return authoritative winner: %v", err)
				}
				stored.ClientSecret = "mutated-return"
				gotDCR, err := decorated.GetDCRCredentials(ctx, creds.Key)
				if err != nil || !reflect.DeepEqual(gotDCR, &input) {
					t.Fatalf("DCR read/defensive copy mismatch: %v", err)
				}
			}
		})
	}
}

func TestEncryptedAuthStorage_NativeNilMissingAndExpiredCAS(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			inner := makeStorage(t)
			decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if got, err := decorated.GetUpstreamTokens(ctx, "missing", "provider"); got != nil || !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("missing read = %v", err)
			}
			fresh := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "fresh-access", RefreshToken: "fresh-refresh"}
			if err := decorated.CompareAndSwapUpstreamTokens(ctx, "missing", "provider", "stale-refresh", fresh); !errors.Is(err, storage.ErrConcurrentRefresh) {
				t.Fatalf("CAS on a missing row with a nonempty expectation = %v, want ErrConcurrentRefresh", err)
			}
			if err := decorated.CompareAndSwapUpstreamTokens(ctx, "missing", "provider", "", fresh); err != nil {
				t.Fatalf("CAS creating a missing row with an empty expectation = %v", err)
			}
			if got, err := decorated.GetUpstreamTokens(ctx, "missing", "provider"); err != nil || got == nil || got.AccessToken != fresh.AccessToken || got.RefreshToken != fresh.RefreshToken {
				t.Fatalf("created row did not read back: %v", err)
			}
			if raw, err := inner.GetUpstreamTokens(ctx, "missing", "provider"); err != nil || raw.AccessToken == fresh.AccessToken || raw.RefreshToken == fresh.RefreshToken {
				t.Fatalf("created row was stored unsealed: %v", err)
			}
			if err := decorated.StoreUpstreamTokens(ctx, "nil", "provider", nil); err != nil {
				t.Fatal(err)
			}
			if got, err := decorated.GetUpstreamTokens(ctx, "nil", "provider"); got != nil || err != nil {
				t.Fatalf("nil read = %v", err)
			}
			if err := decorated.CompareAndSwapUpstreamTokens(ctx, "nil", "provider", "", nil); err != nil {
				t.Fatalf("nil CAS = %v", err)
			}
			replacement := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "replacement-access", RefreshToken: "replacement-refresh"}
			if err := decorated.CompareAndSwapUpstreamTokens(ctx, "nil", "provider", "", replacement); err != nil {
				t.Fatalf("nil row replacement CAS = %v", err)
			}
			expired := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "expired-access", RefreshToken: "expired-refresh", ExpiresAt: time.Now().Add(-time.Minute), SessionExpiresAt: time.Now().Add(time.Hour)}
			if err := decorated.StoreUpstreamTokens(ctx, "expired", "provider", expired); err != nil {
				t.Fatal(err)
			}
			if got, err := decorated.GetUpstreamTokens(ctx, "expired", "provider"); !errors.Is(err, storage.ErrExpired) || got == nil || got.RefreshToken != expired.RefreshToken {
				t.Fatalf("expired read did not return plaintext refresh token: %v", err)
			}
			if err := decorated.CompareAndSwapUpstreamTokens(ctx, "expired", "provider", "wrong", replacement); !errors.Is(err, storage.ErrConcurrentRefresh) {
				t.Fatalf("wrong expired CAS = %v", err)
			}
			if err := decorated.CompareAndSwapUpstreamTokens(ctx, "expired", "provider", expired.RefreshToken, replacement); err != nil {
				t.Fatalf("expired CAS = %v", err)
			}
			if got, err := decorated.GetUpstreamTokens(ctx, "expired", "provider"); err != nil || !reflect.DeepEqual(got, replacement) {
				t.Fatalf("expired CAS replacement mismatch: %v", err)
			}
		})
	}
}

type interveningCASStorage struct {
	storage.Storage
	storage.DCRCredentialStore
	beforeSwap func(string)
}

func (s *interveningCASStorage) CompareAndSwapUpstreamTokens(ctx context.Context, sessionID, provider, expected string, tokens *storage.UpstreamTokens) error {
	s.beforeSwap(expected)
	return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, expected, tokens)
}

func TestEncryptedAuthStorage_NativeCASRejectsInterveningWriter(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			for _, refresh := range []string{"rotated-refresh", "original-refresh"} {
				t.Run(refresh, func(t *testing.T) {
					inner := makeStorage(t)
					interposed := &interveningCASStorage{Storage: inner, DCRCredentialStore: inner.(storage.DCRCredentialStore)}
					decorated, err := New(interposed, testCredentialKeyRing(t), testNamespace)
					if err != nil {
						t.Fatal(err)
					}
					ctx := t.Context()
					old := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "old-access", RefreshToken: "original-refresh"}
					if err := decorated.StoreUpstreamTokens(ctx, "session", "provider", old); err != nil {
						t.Fatal(err)
					}
					raw, err := inner.GetUpstreamTokens(ctx, "session", "provider")
					if err != nil {
						t.Fatal(err)
					}
					winner := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "winning-access", RefreshToken: refresh}
					called := false
					interposed.beforeSwap = func(expected string) {
						called = true
						if expected != raw.RefreshToken {
							t.Fatal("CAS did not compare exact stored ciphertext")
						}
						if err := decorated.StoreUpstreamTokens(ctx, "session", "provider", winner); err != nil {
							t.Fatal(err)
						}
					}
					loser := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "losing-access", RefreshToken: "losing-refresh"}
					if err := decorated.CompareAndSwapUpstreamTokens(ctx, "session", "provider", old.RefreshToken, loser); !errors.Is(err, storage.ErrConcurrentRefresh) {
						t.Fatalf("intervening writer CAS = %v", err)
					}
					got, err := decorated.GetUpstreamTokens(ctx, "session", "provider")
					if !called || err != nil || !reflect.DeepEqual(got, winner) {
						t.Fatalf("native CAS overwrote intervening winner: %v", err)
					}
				})
			}
		})
	}
}

func dcrCASFixture(t *testing.T, makeStorage func(*testing.T) storage.Storage) (storage.Storage, *Storage, storage.DCRCredentials) {
	t.Helper()
	inner := makeStorage(t)
	decorated, err := New(inner, testCredentialKeyRing(t), testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	// Sub-second times: Redis persists seconds, so a matching expected must tolerate that.
	creds := storage.DCRCredentials{
		Key:      storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"},
		ClientID: "client", ClientSecret: "initial-secret", RegistrationAccessToken: "initial-registration",
		AuthorizationEndpoint: "https://issuer/authorize", TokenEndpoint: "https://issuer/token",
		CreatedAt: time.Now(), ClientSecretExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := decorated.StoreDCRCredentialsIfAbsent(t.Context(), &creds); err != nil {
		t.Fatal(err)
	}
	return inner, decorated, creds
}

func TestEncryptedAuthStorage_DCRCompareAndSwap(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			inner, decorated, creds := dcrCASFixture(t, makeStorage)
			ctx := t.Context()
			before, err := inner.(storage.DCRCredentialStore).GetDCRCredentials(ctx, creds.Key)
			if err != nil {
				t.Fatal(err)
			}
			next := creds
			next.ClientSecret, next.RegistrationAccessToken = "rotated-secret", "rotated-registration"
			input := next
			got, err := decorated.UpdateDCRCredentialsIfUnchanged(ctx, &input, &creds)
			if err != nil || !reflect.DeepEqual(got, &next) || !reflect.DeepEqual(input, next) {
				t.Fatalf("CAS = %v", err)
			}
			stored, err := decorated.GetDCRCredentials(ctx, creds.Key)
			if err != nil || stored.ClientSecret != "rotated-secret" || stored.RegistrationAccessToken != "rotated-registration" {
				t.Fatalf("update not durable: %v", err)
			}
			raw, err := inner.(storage.DCRCredentialStore).GetDCRCredentials(ctx, creds.Key)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(raw.ClientSecret, "mecatl.v1.") || !strings.HasPrefix(raw.RegistrationAccessToken, "mecatl.v1.") ||
				strings.Contains(raw.ClientSecret+raw.RegistrationAccessToken, "rotated") || raw.ClientSecret == before.ClientSecret {
				t.Fatal("updated row is not freshly sealed")
			}
		})
	}
}

func TestEncryptedAuthStorage_DCRCASStaleExpected(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			_, decorated, creds := dcrCASFixture(t, makeStorage)
			ctx := t.Context()
			winner := creds
			winner.RegistrationAccessToken = "winner-registration"
			if _, err := decorated.UpdateDCRCredentialsIfPresent(ctx, &winner); err != nil {
				t.Fatal(err)
			}
			loser := creds
			loser.RegistrationAccessToken = "loser-registration"
			if _, err := decorated.UpdateDCRCredentialsIfUnchanged(ctx, &loser, &creds); !errors.Is(err, storage.ErrDCRCredentialsChanged) {
				t.Fatalf("stale expected = %v", err)
			}
			stored, err := decorated.GetDCRCredentials(ctx, creds.Key)
			if err != nil || stored.RegistrationAccessToken != "winner-registration" {
				t.Fatalf("stale CAS overwrote the winner: %v", err)
			}
		})
	}
}

// Every field of expected must take part in the comparison. Reflection keeps
// this honest when ToolHive adds a field.
func TestEncryptedAuthStorage_DCRCASEveryFieldCompared(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			_, decorated, creds := dcrCASFixture(t, makeStorage)
			typ := reflect.TypeFor[storage.DCRCredentials]()
			for i := range typ.NumField() {
				field := typ.Field(i)
				if field.Name == "Key" {
					continue // a different key is invalid input, covered separately
				}
				stale := creds
				v := reflect.ValueOf(&stale).Elem().Field(i)
				switch v.Kind() {
				case reflect.String:
					v.SetString(v.String() + "-stale")
				case reflect.Struct:
					if field.Type != reflect.TypeFor[time.Time]() {
						t.Fatalf("field %s has unhandled type %s", field.Name, field.Type)
					}
					v.Set(reflect.ValueOf(v.Interface().(time.Time).Add(time.Second)))
				default:
					t.Fatalf("field %s has unhandled kind %s", field.Name, v.Kind())
				}
				next := creds
				next.ClientSecret = "should-not-land"
				if _, err := decorated.UpdateDCRCredentialsIfUnchanged(t.Context(), &next, &stale); !errors.Is(err, storage.ErrDCRCredentialsChanged) {
					t.Fatalf("expected differing in %s = %v", field.Name, err)
				}
			}
		})
	}
}

type interveningDCRCASStorage struct {
	storage.Storage
	storage.DCRCredentialStore
	beforeSwap func()
}

func (s *interveningDCRCASStorage) UpdateDCRCredentialsIfUnchanged(ctx context.Context, creds, expected *storage.DCRCredentials) (*storage.DCRCredentials, error) {
	s.beforeSwap()
	return s.DCRCredentialStore.UpdateDCRCredentialsIfUnchanged(ctx, creds, expected)
}

// The decorator's own plaintext compare passes; only the inner store's atomic
// check can catch a writer that lands between the read and the write.
func TestEncryptedAuthStorage_DCRCASRejectsInterveningWriter(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			inner := makeStorage(t)
			interposed := &interveningDCRCASStorage{Storage: inner, DCRCredentialStore: inner.(storage.DCRCredentialStore)}
			decorated, err := New(interposed, testCredentialKeyRing(t), testNamespace)
			if err != nil {
				t.Fatal(err)
			}
			creds := storage.DCRCredentials{
				Key:      storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"},
				ClientID: "client", ClientSecret: "initial-secret", RegistrationAccessToken: "initial-registration",
				AuthorizationEndpoint: "https://issuer/authorize", TokenEndpoint: "https://issuer/token",
			}
			if _, err := decorated.StoreDCRCredentialsIfAbsent(t.Context(), &creds); err != nil {
				t.Fatal(err)
			}
			winner := creds
			winner.RegistrationAccessToken = "winner-registration"
			called := false
			interposed.beforeSwap = func() {
				called = true
				if _, err := decorated.UpdateDCRCredentialsIfPresent(t.Context(), &winner); err != nil {
					t.Fatal(err)
				}
			}
			loser := creds
			loser.RegistrationAccessToken = "loser-registration"
			if _, err := decorated.UpdateDCRCredentialsIfUnchanged(t.Context(), &loser, &creds); !errors.Is(err, storage.ErrDCRCredentialsChanged) {
				t.Fatalf("intervening writer CAS = %v", err)
			}
			stored, err := decorated.GetDCRCredentials(t.Context(), creds.Key)
			if !called || err != nil || stored.RegistrationAccessToken != "winner-registration" {
				t.Fatalf("CAS overwrote the intervening winner: %v", err)
			}
		})
	}
}

func TestEncryptedAuthStorage_DCRCASConcurrentWriters(t *testing.T) {
	const writers = 4
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			_, decorated, creds := dcrCASFixture(t, makeStorage)
			errs := make(chan error, writers)
			for i := range writers {
				go func() {
					next := creds
					next.RegistrationAccessToken = "writer-" + string(rune('a'+i))
					_, err := decorated.UpdateDCRCredentialsIfUnchanged(t.Context(), &next, &creds)
					errs <- err
				}()
			}
			wins := 0
			for range writers {
				switch err := <-errs; {
				case err == nil:
					wins++
				case !errors.Is(err, storage.ErrDCRCredentialsChanged):
					t.Fatalf("loser error = %v", err)
				}
			}
			if wins != 1 {
				t.Fatalf("%d writers won the same expected value, want exactly 1", wins)
			}
		})
	}
}

func TestEncryptedAuthStorage_DCRCASMissingInvalidAndUndecodable(t *testing.T) {
	for name, makeStorage := range encryptedStorageTestBackends {
		t.Run(name, func(t *testing.T) {
			inner, decorated, creds := dcrCASFixture(t, makeStorage)
			ctx := t.Context()
			dcr := inner.(storage.DCRCredentialStore)

			missing := creds
			missing.Key.ScopesHash = "absent"
			if _, err := decorated.UpdateDCRCredentialsIfUnchanged(ctx, &missing, &missing); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("missing row = %v", err)
			}
			if _, err := dcr.GetDCRCredentials(ctx, missing.Key); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("CAS created the missing row: %v", err)
			}

			other := creds
			other.Key.ScopesHash = "other"
			for label, args := range map[string][2]*storage.DCRCredentials{
				"nil creds": {nil, &creds}, "nil expected": {&creds, nil}, "key mismatch": {&creds, &other},
			} {
				if _, err := decorated.UpdateDCRCredentialsIfUnchanged(ctx, args[0], args[1]); !errors.Is(err, credentialenvelope.ErrUnavailable) {
					t.Fatalf("%s = %v", label, err)
				}
			}

			// A row sealed under another namespace cannot be opened: report that, not "changed".
			foreign, err := New(inner, testCredentialKeyRing(t), "other:authserver:")
			if err != nil {
				t.Fatal(err)
			}
			foreignCreds := creds
			foreignCreds.Key.ScopesHash = "foreign"
			if _, err := foreign.StoreDCRCredentialsIfAbsent(ctx, &foreignCreds); err != nil {
				t.Fatal(err)
			}
			if _, err = decorated.UpdateDCRCredentialsIfUnchanged(ctx, &foreignCreds, &foreignCreds); !errors.Is(err, credentialenvelope.ErrUnavailable) || errors.Is(err, storage.ErrDCRCredentialsChanged) {
				t.Fatalf("undecodable row = %v", err)
			}
		})
	}
}
