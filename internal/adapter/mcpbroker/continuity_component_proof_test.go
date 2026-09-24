package mcpbroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// continuityProofFixture deliberately uses the same encrypted Redis storage,
// embedded Fosite server, incoming middleware, and UpstreamInject path as a
// production protected process. The local MCP server is only the upstream.
type countingAccessTokenStorage struct {
	storage.Storage
	issued atomic.Int32
}

func (s *countingAccessTokenStorage) CreateAccessTokenSession(ctx context.Context, signature string, requester fosite.Requester) error {
	if err := s.Storage.CreateAccessTokenSession(ctx, signature, requester); err != nil {
		return err
	}
	s.issued.Add(1)
	return nil
}

type continuityProofFixture struct {
	process       *Process
	assertion     contract.CustodyAssertion
	upstreamCalls *atomic.Int32
	issued        *atomic.Int32
}

func newContinuityProofFixture(t *testing.T, deadline time.Time) *continuityProofFixture {
	t.Helper()
	upstreamCalls := new(atomic.Int32)
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "continuity", Version: "test"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: "status", Description: "status"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, struct{}, error) {
		return &mcpsdk.CallToolResult{}, struct{}{}, nil
	})
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-access" {
			http.Error(w, "missing injected credential", http.StatusUnauthorized)
			return
		}
		upstreamCalls.Add(1)
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(upstreamServer.Close)

	mini := miniredis.RunT(t)
	keyFile := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(keyFile, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatal(err)
	}
	clientFactory := func(ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		return redis.NewClient(&redis.Options{Addr: mini.Addr()}), nil
	}
	t.Setenv("testdata/client-secret", "continuity-secret")
	profile := ToolHiveProfile{Name: "private", URL: upstreamServer.URL, Auth: authOAuth, OAuth: &ToolHiveOAuth{
		AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token",
		ClientID: "upstream-client", ClientSecretFile: "testdata/client-secret", Scopes: []string{"openid"},
	}}
	process, err := newToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: "https://broker.example/callback", Profiles: []ToolHiveProfile{profile},
		ProtectedStorage: &ProtectedStorageConfig{
			Redis:      ProtectedRedisConfig{Client: clientFactory, ClientConfig: ProtectedRedisClientConfig{TLS: true}, HealthTimeout: time.Second},
			Encryption: ProtectedEncryptionConfig{ActiveID: "test", Keys: []ProtectedEncryptionKey{{ID: "test", File: keyFile}}},
		},
	}, toolHiveProcessOptions{runtimeOptions: []Option{WithLimits(Limits{LogicalRetention: time.Minute, SweepInterval: 5 * time.Millisecond})}, allowLoopbackUpstreamsForTest: true})
	if err != nil {
		t.Fatalf("newToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	countingStorage := &countingAccessTokenStorage{Storage: process.authStorage}
	process.authStorage = countingStorage

	provider := process.providers[0]
	now := time.Now()
	if err := process.authStorage.StoreUpstreamTokens(t.Context(), "verified-tsid", provider, &storage.UpstreamTokens{
		ProviderID: provider, AccessToken: "upstream-access", ExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("StoreUpstreamTokens: %v", err)
	}
	guard := contract.ContinuityGuard{SessionID: "recovered-session", SessionIncarnation: session.NewIncarnationID(), Providers: append([]string(nil), process.providers...), ProfileDigest: process.profileDigest}
	guard.OwnerPartition[0], guard.WorkloadPartition[0] = 1, 2
	request := custodyRequest{Guard: custodyGuardFromContract(guard), AttemptDeadline: deadline}
	staged, err := process.custody.Stage(t.Context(), request, "verified-tsid")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	assertion := contract.CustodyAssertion{Guard: guard, RecoveryReference: string(staged.Recovery), AttemptDeadline: deadline}
	if err := process.CommitCredentialCustody(t.Context(), assertion); err != nil {
		t.Fatalf("CommitCredentialCustody: %v", err)
	}
	return &continuityProofFixture{process: process, assertion: assertion, upstreamCalls: upstreamCalls, issued: &countingStorage.issued}
}

func TestRecoveredCredentialComponent_IssuesFreshJWTThroughToolHiveMiddleware(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	recovered, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "component-jwt")
	if err != nil {
		t.Fatalf("RecoverCredentialAttachment: %v", err)
	}
	handle := recovered.Attachment.(*SessionHandle)
	source := handle.logical.recoveredSource
	token, err := source.Token()
	if err != nil {
		t.Fatalf("recovered Token: %v", err)
	}
	if token.RefreshToken != "" || token.Expiry.After(time.Now().Add(recoveredBearerTTL+time.Second)) {
		t.Fatalf("recovered token = %#v; want access-only token expiring at min(2m, custody)", token)
	}
	shortCustodyExpiry := time.Now().Add(30 * time.Second).UTC()
	shortLived, err := issueRecoveredBrokerCredential(t.Context(), f.process.authStorage, f.process.authKeyProvider, f.process.issuer, f.process.protectedTarget.clientID, f.assertion.Guard.OwnerPartition, "verified-tsid", shortCustodyExpiry)
	if err != nil || !shortLived.Expiry.Equal(shortCustodyExpiry) || shortLived.RefreshToken != "" {
		t.Fatalf("short-custody issue = %#v, %v; want access-only min(now+2m, custody) expiry", shortLived, err)
	}
	// This second query proves the minted bearer enters the real incoming OIDC
	// middleware and provides the encrypted native token to UpstreamInject.
	if _, err := f.process.QueryAuthenticatedCapabilities(t.Context(), source, "private"); err != nil {
		t.Fatalf("QueryAuthenticatedCapabilities with recovered JWT: %v", err)
	}
	if f.upstreamCalls.Load() == 0 {
		t.Fatal("recovered JWT did not reach the UpstreamInject protected upstream")
	}
}

func TestRecoveredCredentialComponent_RecoveryIsPrivateUntilExactCommitAndSurvivesSweep(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(120*time.Millisecond))
	recovered, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "component-commit")
	if err != nil {
		t.Fatal(err)
	}
	handle := recovered.Attachment.(*SessionHandle)
	provisional, _, err := f.process.Runtime.AttachSessionExpectedBinding(t.Context(), f.assertion.Guard.SessionID, handle.Binding())
	if err != nil {
		t.Fatalf("exact provisional reattach: %v", err)
	}
	if _, _, err := f.process.Runtime.AttachSession(t.Context(), f.assertion.Guard.SessionID); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("observer attach published recovered state: %v", err)
	}
	if err := handle.Commit(t.Context()); err != nil {
		t.Fatalf("exact Commit: %v", err)
	}
	committed, outcome, err := f.process.Runtime.AttachSessionExpectedBinding(t.Context(), f.assertion.Guard.SessionID, handle.Binding())
	if err != nil || outcome != contract.AttachReattached {
		t.Fatalf("committed reattach = %q, %v", outcome, err)
	}
	for _, attachment := range []contract.SessionHandle{provisional, committed, handle} {
		if _, err := attachment.Close(t.Context()); err != nil {
			t.Fatalf("close reattached handle: %v", err)
		}
	}
	logical := handle.logical
	logical.mu.RLock()
	attachments, expiresAt := logical.attachments, logical.expiresAt
	logical.mu.RUnlock()
	if attachments != 0 {
		t.Fatalf("attachments after close = %d, want 0", attachments)
	}
	passAt := f.assertion.AttemptDeadline.Add(time.Millisecond)
	if !expiresAt.After(passAt) {
		t.Fatalf("committed retention expiry = %v, must outlive recovered admission deadline %v", expiresAt, passAt)
	}
	f.process.Runtime.sweepAt(passAt)
	f.process.Runtime.mu.RLock()
	stillPresent := f.process.Runtime.sessions[f.assertion.Guard.SessionID] == logical
	f.process.Runtime.mu.RUnlock()
	if !stillPresent {
		t.Fatal("sweeper removed committed recovered session after its admission deadline")
	}
}

type refusingKeyProvider struct{}

func (refusingKeyProvider) SigningKey(context.Context) (*keys.SigningKeyData, error) {
	return nil, errors.New("rotated")
}
func (refusingKeyProvider) PublicKeys(context.Context) ([]*keys.PublicKeyData, error) {
	return nil, errors.New("rotated")
}

func TestRecoveredCredentialComponent_RefusesWrongClientOrSigningKey(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	record, err := f.process.custody.Load(t.Context(), custodyAssertionFromContract(f.assertion))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueRecoveredBrokerCredential(t.Context(), f.process.authStorage, f.process.authKeyProvider, f.process.issuer, "rotated-client", f.assertion.Guard.OwnerPartition, record.TSID, record.ExpiresAt); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("wrong client issuance = %v", err)
	}
	if _, err := issueRecoveredBrokerCredential(t.Context(), f.process.authStorage, refusingKeyProvider{}, f.process.issuer, f.process.protectedTarget.clientID, f.assertion.Guard.OwnerPartition, record.TSID, record.ExpiresAt); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("rotated signing key issuance = %v", err)
	}
}

func TestRecoveredCredentialComponent_RejectsInvalidAssertionsBeforeIssuance(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	for name, mutate := range map[string]func(*contract.CustodyAssertion){
		"deadline": func(a *contract.CustodyAssertion) { a.AttemptDeadline = time.Now().Add(-time.Second) },
		"profile":  func(a *contract.CustodyAssertion) { a.Guard.ProfileDigest[0]++ },
		"workload": func(a *contract.CustodyAssertion) { a.Guard.WorkloadPartition[0]++ },
	} {
		t.Run(name, func(t *testing.T) {
			a := f.assertion
			mutate(&a)
			before := f.issued.Load()
			if _, err := f.process.RecoverCredentialAttachment(t.Context(), a, "reject-"+name); !errors.Is(err, contract.ErrContinuityUnavailable) {
				t.Fatalf("RecoverCredentialAttachment = %v", err)
			}
			if got := f.issued.Load(); got != before {
				t.Fatalf("invalid %s issued %d credentials, want %d", name, got, before)
			}
		})
	}
	if err := f.process.TombstoneCredentialCustody(t.Context(), f.assertion); err != nil {
		t.Fatal(err)
	}
	before := f.issued.Load()
	if _, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "reject-tombstone"); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("tombstoned recovery = %v", err)
	}
	if got := f.issued.Load(); got != before {
		t.Fatalf("tombstoned recovery issued %d credentials, want %d", got, before)
	}
}

func TestRecoveredCredentialComponent_ReissuesAfterAttemptDeadlineWhileCustodyCurrent(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(120*time.Millisecond))
	recovered, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "component-reissue")
	if err != nil {
		t.Fatal(err)
	}
	handle := recovered.Attachment.(*SessionHandle)
	if err := handle.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	source := handle.logical.recoveredSource
	first, err := source.Token()
	if err != nil {
		t.Fatalf("initial recovered token: %v", err)
	}
	time.Sleep(time.Until(f.assertion.AttemptDeadline) + 20*time.Millisecond)
	source.mu.Lock()
	source.token.Expiry = time.Now().Add(-time.Second)
	source.mu.Unlock()
	reissued, err := source.Token()
	if err != nil || reissued.AccessToken == "" || !reissued.Expiry.After(time.Now()) || reissued.AccessToken == first.AccessToken {
		t.Fatalf("reissue after attempt deadline = %#v, %v; want a fresh unexpired bearer", reissued, err)
	}
	if _, err := f.process.custody.LoadCurrent(t.Context(), recoveryID(f.assertion.RecoveryReference), custodyGuardFromContract(f.assertion.Guard)); err != nil {
		t.Fatalf("custody should remain current: %v", err)
	}
}

func TestRecoveredCredentialComponent_SourceStopsAfterLogicalDeletionAndProcessClose(t *testing.T) {
	t.Run("logical deletion", func(t *testing.T) {
		f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
		recovered, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "source-delete")
		if err != nil {
			t.Fatal(err)
		}
		handle := recovered.Attachment.(*SessionHandle)
		source := handle.logical.recoveredSource
		if _, err := f.process.DeleteSessionIfBinding(t.Context(), f.assertion.Guard.SessionID, handle.Binding()); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Token(); err == nil {
			t.Fatal("deleted logical session renewed a recovered bearer")
		}
	})
	t.Run("process close", func(t *testing.T) {
		f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
		recovered, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "source-close")
		if err != nil {
			t.Fatal(err)
		}
		source := recovered.Attachment.(*SessionHandle).logical.recoveredSource
		if err := f.process.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Token(); err == nil {
			t.Fatal("closed process renewed a recovered bearer")
		}
	})
}

func TestRecoveredCredentialComponent_CustodyLoadCurrentRejectsExpiry(t *testing.T) {
	f := newFixture(t)
	clock := &mutableCustodyClock{now: f.clock.now}
	f.core.clock = clock
	staged, err := f.core.Stage(t.Context(), f.request, "tsid")
	if err != nil {
		t.Fatal(err)
	}
	assertion := custodyAssertion{custodyRequest: f.request, Recovery: staged.Recovery}
	if err := f.core.Commit(t.Context(), assertion); err != nil {
		t.Fatal(err)
	}
	clock.Set(f.clock.now.Add(2 * time.Hour))
	if _, err := f.core.LoadCurrent(t.Context(), assertion.Recovery, assertion.Guard); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("expired LoadCurrent = %v", err)
	}
}
