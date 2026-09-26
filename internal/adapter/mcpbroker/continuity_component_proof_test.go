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
	mini          *miniredis.Miniredis
	keyFile       string
	upstreamURL   string
	clock         *mutableCustodyClock
}

func newContinuityProofFixture(t *testing.T, deadline time.Time) *continuityProofFixture {
	return newContinuityProofFixtureWithSessionExpiry(t, deadline, time.Now().Add(time.Hour))
}

func newContinuityProofFixtureWithSessionExpiry(t *testing.T, deadline, sessionExpiresAt time.Time) *continuityProofFixture {
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
	process := newContinuityProofProcess(t, mini, keyFile, upstreamServer.URL)
	countingStorage := &countingAccessTokenStorage{Storage: process.authStorage}
	process.authStorage = countingStorage

	provider := process.providers[0]
	now := time.Now()
	if err := process.authStorage.StoreUpstreamTokens(t.Context(), "verified-tsid", provider, &storage.UpstreamTokens{
		ProviderID: provider, AccessToken: "upstream-access", ExpiresAt: now.Add(time.Hour), SessionExpiresAt: sessionExpiresAt,
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
	clock := &mutableCustodyClock{now: now}
	return &continuityProofFixture{
		process: process, assertion: assertion, upstreamCalls: upstreamCalls,
		issued: &countingStorage.issued, mini: mini, keyFile: keyFile,
		upstreamURL: upstreamServer.URL, clock: clock,
	}
}

func newContinuityProofProcess(t *testing.T, mini *miniredis.Miniredis, keyFile, upstreamURL string) *Process {
	t.Helper()
	clientFactory := func(ProtectedRedisClientConfig) (redis.UniversalClient, error) {
		return redis.NewClient(&redis.Options{Addr: mini.Addr()}), nil
	}
	t.Setenv("testdata/client-secret", "continuity-secret")
	profile := ToolHiveProfile{Name: "private", URL: upstreamURL, Auth: authOAuth, OAuth: &ToolHiveOAuth{
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
	return process
}

func TestRecoveredCredentialComponent_IssuesFreshJWTThroughToolHiveMiddleware(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	recovered, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "component-jwt")
	if err != nil {
		t.Fatalf("RecoverCredentialAttachment: %v", err)
	}
	handle := recovered.Attachment.(*Attachment)
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

func TestBrokerReplacementRecoveryNeverOutlivesOriginalGrantExpiry(t *testing.T) {
	grantExpiry := time.Now().Add(30 * time.Second)
	f := newContinuityProofFixtureWithSessionExpiry(t, grantExpiry.Add(-time.Second), grantExpiry)
	custody, err := f.process.custody.Load(t.Context(), custodyAssertionFromContract(f.assertion))
	if err != nil {
		t.Fatalf("load original custody: %v", err)
	}
	if custody.ExpiresAt.After(grantExpiry) || grantExpiry.Sub(custody.ExpiresAt) > time.Second {
		t.Fatalf("staged expiry = %v, want the original grant expiry %v", custody.ExpiresAt, grantExpiry)
	}
	grantExpiry = custody.ExpiresAt
	f.process.custody.clock = f.clock

	if err := f.process.Close(); err != nil {
		t.Fatalf("stop original broker: %v", err)
	}
	f.clock.Set(grantExpiry.Add(-20 * time.Second))
	var latest *Process
	var recovered *Attachment
	for attempt, requestID := range []string{"replacement-1", "replacement-2"} {
		latest = newContinuityProofProcess(t, f.mini, f.keyFile, f.upstreamURL)
		latest.custody.clock = f.clock
		attachment, err := latest.RecoverCredentialAttachment(t.Context(), f.assertion, requestID)
		if err != nil {
			t.Fatalf("recovery %d after broker replacement: %v", attempt+1, err)
		}
		var ok bool
		recovered, ok = attachment.Attachment.(*Attachment)
		if !ok {
			t.Fatalf("recovery %d returned attachment %T, want *Attachment", attempt+1, attachment.Attachment)
		}
		if err := recovered.Commit(t.Context()); err != nil {
			t.Fatalf("commit recovery %d: %v", attempt+1, err)
		}
		source := recovered.logical.recoveredSource
		token, err := source.Token()
		if err != nil {
			t.Fatalf("recovery %d token: %v", attempt+1, err)
		}
		if token.Expiry.After(grantExpiry) || token.RefreshToken != "" {
			t.Fatalf("recovery %d token expiry = %v (grant expiry %v), refresh token present=%t", attempt+1, token.Expiry, grantExpiry, token.RefreshToken != "")
		}
		callsBefore := f.upstreamCalls.Load()
		if _, err := latest.QueryAuthenticatedCapabilities(t.Context(), source, "private"); err != nil {
			t.Fatalf("recovery %d protected discovery: %v", attempt+1, err)
		}
		if calls := f.upstreamCalls.Load(); calls <= callsBefore {
			t.Fatalf("recovery %d did not reach the protected upstream", attempt+1)
		}
		if attempt == 0 {
			if err := latest.Close(); err != nil {
				t.Fatalf("stop replacement broker: %v", err)
			}
			f.clock.Set(grantExpiry.Add(-5 * time.Second))
		}
	}

	callsBeforeExpiry := f.upstreamCalls.Load()
	source := recovered.logical.recoveredSource
	source.mu.Lock()
	source.token.Expiry = time.Now().Add(-time.Second)
	source.mu.Unlock()
	f.clock.Set(grantExpiry.Add(time.Second))
	if _, err := source.Token(); err == nil {
		t.Fatal("recovered source reissued a bearer after original grant expiry")
	}
	if _, err := latest.QueryAuthenticatedCapabilities(t.Context(), source, "private"); err == nil {
		t.Fatal("recovered catalogue discovery succeeded after original grant expiry")
	}
	if calls := f.upstreamCalls.Load(); calls != callsBeforeExpiry {
		t.Fatalf("expired recovered source reached upstream: calls %d -> %d", callsBeforeExpiry, calls)
	}
	if err := latest.Close(); err != nil {
		t.Fatalf("stop last replacement broker: %v", err)
	}

	expiredBroker := newContinuityProofProcess(t, f.mini, f.keyFile, f.upstreamURL)
	expiredBroker.custody.clock = f.clock
	if _, err := expiredBroker.RecoverCredentialAttachment(t.Context(), f.assertion, "after-expiry"); !errors.Is(err, contract.ErrContinuityUnavailable) {
		t.Fatalf("replacement recovery after original grant expiry = %v, want continuity-unavailable", err)
	}
}

func TestRecoveredCredentialComponent_RecoveryIsPrivateUntilExactCommitAndSurvivesSweep(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(120*time.Millisecond))
	recovered, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "component-commit")
	if err != nil {
		t.Fatal(err)
	}
	handle := recovered.Attachment.(*Attachment)
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
	for _, attachment := range []contract.Attachment{provisional, committed, handle} {
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
	if _, err := f.process.RecoverCredentialAttachment(t.Context(), f.assertion, "reject-tombstone"); !errors.Is(err, contract.ErrContinuityRevoked) {
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
	handle := recovered.Attachment.(*Attachment)
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
		handle := recovered.Attachment.(*Attachment)
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
		source := recovered.Attachment.(*Attachment).logical.recoveredSource
		if err := f.process.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Token(); err == nil {
			t.Fatal("closed process renewed a recovered bearer")
		}
	})
}

func TestFreshAttachRacingRecoverForSameSessionIDHasOneWinner(t *testing.T) {
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	issuedBefore := f.issued.Load()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	type attachResult struct {
		attachment contract.Attachment
		outcome    contract.AttachOutcome
		err        error
	}
	type recoveryResult struct {
		attachment contract.RecoveredCredentialAttachment
		err        error
	}
	start := make(chan struct{})
	freshDone := make(chan attachResult, 1)
	recoveryDone := make(chan recoveryResult, 1)
	go func() {
		<-start
		attachment, outcome, err := f.process.Runtime.AttachSession(ctx, f.assertion.Guard.SessionID)
		freshDone <- attachResult{attachment: attachment, outcome: outcome, err: err}
	}()
	go func() {
		<-start
		attachment, err := f.process.RecoverCredentialAttachment(ctx, f.assertion, "fresh-auth-race")
		recoveryDone <- recoveryResult{attachment: attachment, err: err}
	}()
	close(start)

	var fresh attachResult
	select {
	case fresh = <-freshDone:
	case <-ctx.Done():
		t.Fatalf("fresh AttachSession did not finish: %v", ctx.Err())
	}
	var recovered recoveryResult
	select {
	case recovered = <-recoveryDone:
	case <-ctx.Done():
		t.Fatalf("RecoverCredentialAttachment did not finish: %v", ctx.Err())
	}

	freshWon, recoveryWon := fresh.err == nil, recovered.err == nil
	if freshWon == recoveryWon {
		t.Fatalf("race must have exactly one winner: fresh err=%v, recovery err=%v", fresh.err, recovered.err)
	}
	if freshWon {
		if fresh.attachment == nil {
			t.Fatal("fresh attach winner returned no attachment")
		}
		if fresh.outcome != contract.AttachCreated {
			t.Fatalf("fresh winner outcome = %q, want %q", fresh.outcome, contract.AttachCreated)
		}
		if !errors.Is(recovered.err, contract.ErrContinuityUnavailable) {
			t.Fatalf("recovery loser error = %v, want ErrContinuityUnavailable", recovered.err)
		}
	} else {
		if !errors.Is(fresh.err, contract.ErrContinuityUnavailable) {
			t.Fatalf("fresh attach loser error = %v, want ErrContinuityUnavailable", fresh.err)
		}
		if recovered.attachment.Attachment == nil {
			t.Fatal("recovery winner returned no attachment")
		}
	}

	runtime := f.process.Runtime
	runtime.mu.RLock()
	logical := runtime.sessions[f.assertion.Guard.SessionID]
	sessionCount := len(runtime.sessions)
	runtime.mu.RUnlock()
	if logical == nil || sessionCount != 1 {
		t.Fatalf("logical sessions after race = %d, target session = %p; want exactly one target session", sessionCount, logical)
	}
	logical.mu.RLock()
	recoveredProvisional := logical.recoveredProvisional
	sourcePresent := logical.recoveredSource != nil
	provisional := logical.provisional
	attachments := logical.attachments
	logical.mu.RUnlock()
	if recoveredProvisional != recoveryWon || sourcePresent != recoveryWon || !provisional || attachments != 1 {
		t.Fatalf("session mixed or leaked authority: recovered=%t source=%t provisional=%t attachments=%d; recovery won=%t", recoveredProvisional, sourcePresent, provisional, attachments, recoveryWon)
	}

	attemptKey := f.process.recoveryAttemptKey(f.assertion, "fresh-auth-race")
	f.process.continuityMu.Lock()
	attempt := f.process.recoveredAttempts[attemptKey]
	f.process.continuityMu.Unlock()
	if freshWon {
		if attempt != nil {
			t.Fatal("losing recovery left a cached attempt receipt")
		}
		if got := f.issued.Load(); got != issuedBefore {
			t.Fatalf("losing recovery issued %d credential(s), want none", got-issuedBefore)
		}
	} else if attempt == nil || !attempt.success || attempt.binding != recovered.attachment.Attachment.Binding() {
		t.Fatalf("winning recovery attempt = %#v; want successful receipt for winning binding %q", attempt, recovered.attachment.Attachment.Binding())
	}
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
