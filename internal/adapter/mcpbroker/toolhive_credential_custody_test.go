package mcpbroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/engine/session"
)

type custodyTestClock struct{ now time.Time }

func (c custodyTestClock) Now() time.Time { return c.now }

type mutableCustodyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableCustodyClock) Now() time.Time    { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *mutableCustodyClock) Set(now time.Time) { c.mu.Lock(); c.now = now; c.mu.Unlock() }

type custodyFixture struct {
	core      *credentialCustody
	inner     *storage.MemoryStorage
	client    redis.UniversalClient
	clock     custodyTestClock
	request   custodyRequest
	retention custodyRetention
	id        session.SessionID
}

type trackingRows struct {
	storage *storage.MemoryStorage
	calls   []string
	drift   string
}

func (r *trackingRows) GetUpstreamTokens(ctx context.Context, sessionID, provider string) (*storage.UpstreamTokens, error) {
	r.calls = append(r.calls, provider)
	row, err := r.storage.GetUpstreamTokens(ctx, sessionID, provider)
	if err == nil && provider == r.drift && len(r.calls) > 1 {
		copy := *row
		copy.SessionExpiresAt = copy.SessionExpiresAt.Add(time.Minute)
		row = &copy
	}
	return row, err
}

func TestCredentialCustodyStageDerivesEarliestNativeSessionExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inner := storage.NewMemoryStorage()
	first := now.Add(30 * time.Minute)
	second := now.Add(time.Hour)
	for provider, expiry := range map[string]time.Time{"one": first, "two": second} {
		if err := inner.StoreUpstreamTokens(t.Context(), "tsid", provider, &storage.UpstreamTokens{ProviderID: provider, AccessToken: provider, SessionExpiresAt: expiry}); err != nil {
			t.Fatal(err)
		}
	}
	rows := &trackingRows{storage: inner}
	client := newMiniRedis(t)
	clock := custodyTestClock{now: now}
	core, err := newCredentialCustody(client, testCredentialKeyRing(t), upstreamtoken.NewInProcessService(inner, nil), clock, rows)
	if err != nil {
		t.Fatal(err)
	}
	request := custodyRequest{Guard: custodyGuard{SessionID: "session", Incarnation: session.NewIncarnationID(), Providers: []string{"one", "two"}}, AttemptDeadline: now.Add(time.Minute)}
	staged, err := core.Stage(t.Context(), request, "tsid")
	if err != nil {
		t.Fatal(err)
	}
	if !staged.ExpiresAt.Equal(first) {
		t.Fatalf("expiry = %v, want %v", staged.ExpiresAt, first)
	}
	if len(rows.calls) != 4 || rows.calls[0] != "one" || rows.calls[1] != "two" || rows.calls[2] != "one" || rows.calls[3] != "two" {
		t.Fatalf("row-read order = %v", rows.calls)
	}
}

func TestCredentialCustodyStageRejectsSessionExpiryDrift(t *testing.T) {
	f := newFixture(t)
	rows := &trackingRows{storage: f.inner, drift: "provider"}
	core, err := newCredentialCustody(f.client, testCredentialKeyRing(t), upstreamtoken.NewInProcessService(f.inner, nil), f.clock, rows)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Stage(t.Context(), f.request, "tsid"); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("drifted Stage = %v", err)
	}
	keys, _, err := f.client.Scan(t.Context(), 0, custodyPrefix+"*", 10).Result()
	if err != nil || len(keys) != 0 {
		t.Fatalf("drifted Stage published custody: %v, %v", keys, err)
	}
}

func TestToolHiveCredentialCustody_StorageDecoratorEncryptsAndBindsFields(t *testing.T) {
	inner := storage.NewMemoryStorage()
	decorated, err := newEncryptedAuthStorage(inner, testCredentialKeyRing(t))
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
	if _, err := decorated.GetLatestUpstreamTokensForUser(ctx, "user", "provider"); !errors.Is(err, errCredentialEnvelope) {
		t.Fatalf("latest lookup = %v", err)
	}
}

func TestToolHiveCredentialCustody_StorageDecoratorNilAndCASSemantics(t *testing.T) {
	inner := storage.NewMemoryStorage()
	decorated, err := newEncryptedAuthStorage(inner, testCredentialKeyRing(t))
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

func TestToolHiveCredentialCustody_StorageDecoratorDCRAndKeyOverlap(t *testing.T) {
	ctx := t.Context()
	oldRing, err := newCredentialKeyRing("old", map[string][]byte{"old": []byte(strings.Repeat("a", 32))})
	if err != nil {
		t.Fatal(err)
	}
	inner := storage.NewMemoryStorage()
	oldStore, err := newEncryptedAuthStorage(inner, oldRing)
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
	newRing, err := newCredentialKeyRing("new", map[string][]byte{"old": []byte(strings.Repeat("a", 32)), "new": []byte(strings.Repeat("b", 32))})
	if err != nil {
		t.Fatal(err)
	}
	newStore, err := newEncryptedAuthStorage(inner, newRing)
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

func TestToolHiveCredentialCustody_StorageDecoratorExpiredAndDCRWinner(t *testing.T) {
	inner := storage.NewMemoryStorage()
	decorated, err := newEncryptedAuthStorage(inner, testCredentialKeyRing(t))
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

func TestToolHiveCredentialCustody_GuardComponentsAndDeadlineRejectLoadResolve(t *testing.T) {
	f, assertion := committedFixture(t)
	cases := []struct {
		name   string
		mutate func(*custodyAssertion)
	}{
		{"session", func(a *custodyAssertion) { a.Guard.SessionID = "other" }},
		{"incarnation", func(a *custodyAssertion) { a.Guard.Incarnation = session.NewIncarnationID() }},
		{"owner", func(a *custodyAssertion) { a.Guard.OwnerPartition[0]++ }},
		{"workload", func(a *custodyAssertion) { a.Guard.WorkloadPartition[0]++ }},
		{"profile", func(a *custodyAssertion) { a.Guard.ProfileDigest[0]++ }},
		{"providers", func(a *custodyAssertion) { a.Guard.Providers = []string{"other"} }},
		{"recovery", func(a *custodyAssertion) { other, _ := newRecoveryID(); a.Recovery = other }},
		{"deadline", func(a *custodyAssertion) { a.AttemptDeadline = f.clock.now }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := assertion
			bad.Guard = cloneGuard(assertion.Guard)
			tc.mutate(&bad)
			if _, err := f.core.Load(t.Context(), bad); !errors.Is(err, errCustodyUnavailable) {
				t.Fatalf("Load = %v", err)
			}
			if _, err := f.core.Resolve(t.Context(), bad, "provider"); !errors.Is(err, errCustodyUnavailable) {
				t.Fatalf("Resolve = %v", err)
			}
		})
	}
}

func TestToolHiveCredentialCustody_SameGuardDoesNotConflateNULDelimitedProviders(t *testing.T) {
	guard := custodyGuard{SessionID: "session", Incarnation: session.NewIncarnationID(), Providers: []string{"a", "b\x00c"}}
	other := guard
	other.Providers = []string{"a\x00b", "c"}
	if sameGuard(guard, other) {
		t.Fatal("distinct provider sets compared equal")
	}
}

func TestToolHiveCredentialCustody_RestartStagedCommitAndResolve(t *testing.T) {
	f := newFixture(t)
	staged, err := f.core.Stage(t.Context(), f.request, "tsid")
	ref := staged.Recovery
	if err != nil {
		t.Fatal(err)
	}
	assertion := custodyAssertion{custodyRequest: f.request, Recovery: ref}
	restarted, err := newCredentialCustody(f.client, testCredentialKeyRing(t), upstreamtoken.NewInProcessService(f.inner, nil), f.clock, f.inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Load(t.Context(), assertion); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("staged after restart = %v", err)
	}
	if err := restarted.Commit(t.Context(), assertion); err != nil {
		t.Fatalf("restart commit = %v", err)
	}
	credential, err := restarted.Resolve(t.Context(), assertion, "provider")
	if err != nil || credential.AccessToken != "access" {
		t.Fatalf("restart resolve = %#v, %v", credential, err)
	}
}

func TestToolHiveCredentialCustody_TombstoneRejectsStaleSerializedWrite(t *testing.T) {
	f, assertion := committedFixture(t)
	_, stale, err := f.core.readRecord(t.Context(), assertion.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.core.Tombstone(t.Context(), f.request, assertion.Recovery); err != nil {
		t.Fatal(err)
	}
	record, err := f.core.Load(t.Context(), assertion)
	if err == nil || record.State == custodyCurrent {
		t.Fatal("tombstone remained loadable")
	}
	// A stale writer holding the pre-tombstone serialized envelope cannot replace it.
	ok, err := f.core.replace(t.Context(), assertion.Recovery, stale, stale, f.retention.ExpiresAt, assertion.AttemptDeadline)
	if err != nil || ok {
		t.Fatalf("stale overwrite = %v, %v", ok, err)
	}
}

func TestToolHiveCredentialCustody_RetentionAndAssertionDeadline(t *testing.T) {
	f, assertion := committedFixture(t)
	record, _, err := f.core.readRecord(t.Context(), assertion.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ExpiresAt.Equal(f.retention.ExpiresAt) {
		t.Fatalf("stage expiry = %v, want %v", record.ExpiresAt, f.retention.ExpiresAt)
	}
	ttl, err := f.client.PTTL(t.Context(), f.core.key(assertion.Recovery)).Result()
	if err != nil || ttl <= 0 || ttl > f.retention.ExpiresAt.Sub(f.clock.now) {
		t.Fatalf("custody TTL = %v, %v", ttl, err)
	}
	shorter := assertion
	shorter.AttemptDeadline = f.clock.now.Add(time.Minute)
	if _, err := f.core.Load(t.Context(), shorter); err != nil {
		t.Fatalf("shorter assertion = %v", err)
	}
	future := assertion
	future.AttemptDeadline = f.retention.ExpiresAt.Add(time.Second)
	if _, err := f.core.Load(t.Context(), future); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("future assertion = %v", err)
	}
	if _, err := f.core.Resolve(t.Context(), assertion, "provider"); err != nil {
		t.Fatalf("resolve = %v", err)
	}
	before, _, err := f.core.readRecord(t.Context(), assertion.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.core.Tombstone(t.Context(), f.request, assertion.Recovery); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.core.readRecord(t.Context(), assertion.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) || after.State != custodyTombstoned {
		t.Fatalf("tombstone record = %#v", after)
	}
}

func TestToolHiveCredentialCustody_CrossesCommittedReferencesAndGuards(t *testing.T) {
	f, first := committedFixture(t)
	secondRequest := f.request
	secondRequest.Guard = cloneGuard(f.request.Guard)
	secondRequest.Guard.SessionID = "second-session"
	secondRequest.Guard.Incarnation = session.NewIncarnationID()
	staged, err := f.core.Stage(t.Context(), secondRequest, "tsid")
	second := staged.Recovery
	if err != nil {
		t.Fatal(err)
	}
	secondAssertion := custodyAssertion{custodyRequest: secondRequest, Recovery: second}
	if err := f.core.Commit(t.Context(), secondAssertion); err != nil {
		t.Fatal(err)
	}
	crossGuard := first
	crossGuard.Guard = cloneGuard(secondRequest.Guard)
	if _, err := f.core.Load(t.Context(), crossGuard); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("cross guard = %v", err)
	}
	crossRecovery := first
	crossRecovery.Recovery = second
	if _, err := f.core.Load(t.Context(), crossRecovery); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("cross recovery = %v", err)
	}
}

func TestToolHiveCredentialCustody_RestartRepeatCommitIsIdempotent(t *testing.T) {
	f := newFixture(t)
	staged, err := f.core.Stage(t.Context(), f.request, "tsid")
	ref := staged.Recovery
	if err != nil {
		t.Fatal(err)
	}
	assertion := custodyAssertion{custodyRequest: f.request, Recovery: ref}
	restarted, err := newCredentialCustody(f.client, testCredentialKeyRing(t), upstreamtoken.NewInProcessService(f.inner, nil), f.clock, f.inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Load(t.Context(), assertion); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("staged load = %v", err)
	}
	if err := restarted.Commit(t.Context(), assertion); err != nil {
		t.Fatalf("first commit = %v", err)
	}
	if err := restarted.Commit(t.Context(), assertion); err != nil {
		t.Fatalf("repeat commit = %v", err)
	}
	if _, err := restarted.Resolve(t.Context(), assertion, "provider"); err != nil {
		t.Fatalf("restart resolve = %v", err)
	}
}

func TestToolHiveCredentialCustody_ConcurrentCommitAndTombstoneLeavesTombstone(t *testing.T) {
	f := newFixture(t)
	staged, err := f.core.Stage(t.Context(), f.request, "tsid")
	ref := staged.Recovery
	if err != nil {
		t.Fatal(err)
	}
	assertion := custodyAssertion{custodyRequest: f.request, Recovery: ref}
	start := make(chan struct{})
	var commitErr, tombstoneErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; commitErr = f.core.Commit(context.Background(), assertion) }()
	go func() {
		defer wg.Done()
		<-start
		tombstoneErr = f.core.Tombstone(context.Background(), f.request, ref)
	}()
	close(start)
	wg.Wait()
	if commitErr != nil && !errors.Is(commitErr, errCustodyConflict) {
		t.Fatalf("commit = %v", commitErr)
	}
	if tombstoneErr != nil {
		t.Fatalf("tombstone = %v", tombstoneErr)
	}
	record, _, err := f.core.readRecord(t.Context(), ref)
	if err != nil || record.State != custodyTombstoned || record.TSID != "" {
		t.Fatalf("final record = %#v, %v", record, err)
	}
	if _, err := f.core.Load(t.Context(), assertion); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("tombstoned load = %v", err)
	}
}

type refreshPersistor struct {
	store storage.UpstreamTokenStorage
	after func()
}

func (r refreshPersistor) RefreshAndStore(ctx context.Context, sessionID string, expired *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
	refreshed := *expired
	refreshed.AccessToken, refreshed.RefreshToken = "refreshed-access", "refreshed-refresh"
	refreshed.ExpiresAt = time.Now().Add(time.Hour)
	if err := r.store.CompareAndSwapUpstreamTokens(ctx, sessionID, expired.ProviderID, expired.RefreshToken, &refreshed); err != nil {
		return nil, err
	}
	if r.after != nil {
		r.after()
	}
	return &refreshed, nil
}

func TestToolHiveCredentialCustody_ResolveRefreshesThroughEncryptedStorage(t *testing.T) {
	f, assertion := committedFixture(t)
	decorated, err := newEncryptedAuthStorage(f.inner, testCredentialKeyRing(t))
	if err != nil {
		t.Fatal(err)
	}
	expired := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "expired-access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute), SessionExpiresAt: time.Now().Add(time.Hour)}
	if err := decorated.StoreUpstreamTokens(t.Context(), "tsid", "provider", expired); err != nil {
		t.Fatal(err)
	}
	f.core.tokens = upstreamtoken.NewInProcessService(decorated, refreshPersistor{store: decorated})
	credential, err := f.core.Resolve(t.Context(), assertion, "provider")
	if err != nil || credential.AccessToken != "refreshed-access" {
		t.Fatalf("refresh resolve = %#v, %v", credential, err)
	}
	raw, err := f.inner.GetUpstreamTokens(t.Context(), "tsid", "provider")
	if err != nil || strings.Contains(raw.AccessToken+raw.RefreshToken, "refreshed") {
		t.Fatalf("refreshed raw row = %#v, %v", raw, err)
	}
}

func TestToolHiveCredentialCustody_FinalAdmissionRejectsDeadlineOrTombstoneDuringRefresh(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(*custodyFixture, custodyAssertion, *mutableCustodyClock)
	}{
		{"deadline", func(_ *custodyFixture, assertion custodyAssertion, clock *mutableCustodyClock) {
			clock.Set(assertion.AttemptDeadline)
		}},
		{"tombstone", func(f *custodyFixture, assertion custodyAssertion, _ *mutableCustodyClock) {
			_ = f.core.Tombstone(context.Background(), f.request, assertion.Recovery)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, assertion := committedFixture(t)
			clock := &mutableCustodyClock{now: f.clock.now}
			f.core.clock = clock
			expired := &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute), SessionExpiresAt: time.Now().Add(time.Hour)}
			if err := f.inner.StoreUpstreamTokens(t.Context(), "tsid", "provider", expired); err != nil {
				t.Fatal(err)
			}
			f.core.tokens = upstreamtoken.NewInProcessService(f.inner, refreshPersistor{store: f.inner, after: func() { tc.after(f, assertion, clock) }})
			if _, err := f.core.Resolve(t.Context(), assertion, "provider"); !errors.Is(err, errCustodyUnavailable) {
				t.Fatalf("Resolve = %v", err)
			}
		})
	}
}

func TestToolHiveCredentialCustody_DCRCanaryAndActiveRotation(t *testing.T) {
	ctx := t.Context()
	oldKey := []byte(strings.Repeat("a", 32))
	newKey := []byte(strings.Repeat("b", 32))
	oldRing, _ := newCredentialKeyRing("old", map[string][]byte{"old": oldKey})
	inner := storage.NewMemoryStorage()
	oldStore, _ := newEncryptedAuthStorage(inner, oldRing)
	key := storage.DCRKey{Issuer: "issuer", UpstreamID: "provider", RedirectURI: "https://broker/callback", ScopesHash: "scope"}
	creds := &storage.DCRCredentials{Key: key, ProviderName: "provider", ClientID: "id", ClientSecret: "client-secret-canary", RegistrationAccessToken: "registration-token-canary", AuthorizationEndpoint: "https://issuer/auth", TokenEndpoint: "https://issuer/token"}
	if _, err := oldStore.StoreDCRCredentialsIfAbsent(ctx, creds); err != nil {
		t.Fatal(err)
	}
	raw, err := inner.GetDCRCredentials(ctx, key)
	if err != nil || strings.Contains(raw.ClientSecret, "client-secret-canary") || strings.Contains(raw.RegistrationAccessToken, "registration-token-canary") {
		t.Fatalf("raw DCR = %#v, %v", raw, err)
	}
	rotated, _ := newCredentialKeyRing("new", map[string][]byte{"old": oldKey, "new": newKey})
	newStore, _ := newEncryptedAuthStorage(inner, rotated)
	if err := newStore.StoreUpstreamTokens(ctx, "rotated", "provider", &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "new-access"}); err != nil {
		t.Fatal(err)
	}
	row, err := inner.GetUpstreamTokens(ctx, "rotated", "provider")
	if err != nil || !strings.Contains(row.AccessToken, ".new.") {
		t.Fatalf("active key row = %#v, %v", row, err)
	}
	retired, _ := newCredentialKeyRing("new", map[string][]byte{"new": newKey})
	retiredStore, _ := newEncryptedAuthStorage(inner, retired)
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

func TestToolHiveCredentialCustody_RealRefresherPersistenceOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		setup       func(*refreshFaultStorage, *encryptedAuthStorage)
		want        string
		wantErr     bool
		wantDeletes int
	}{
		{
			name: "non_rotating persistence failure remains usable", body: `{"access_token":"fresh-nonrotating","token_type":"Bearer","expires_in":3600}`,
			setup: func(f *refreshFaultStorage, _ *encryptedAuthStorage) {
				f.compare = func(context.Context, string, string, string, *storage.UpstreamTokens) error {
					return errors.New("persist failed")
				}
			},
			want: "fresh-nonrotating", wantDeletes: 0,
		},
		{
			name: "rotating persistence failure deletes stale row", body: `{"access_token":"fresh-rotating","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`,
			setup: func(f *refreshFaultStorage, _ *encryptedAuthStorage) {
				f.compare = func(context.Context, string, string, string, *storage.UpstreamTokens) error {
					return errors.New("persist failed")
				}
			},
			wantErr: true, wantDeletes: 1,
		},
		{
			name: "conflict returns valid winner", body: `{"access_token":"loser","refresh_token":"loser-refresh","token_type":"Bearer","expires_in":3600}`,
			setup: func(f *refreshFaultStorage, encrypted *encryptedAuthStorage) {
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

func newRealRefresherFixture(t *testing.T, response string) (*upstreamtoken.InProcessService, *refreshFaultStorage, *encryptedAuthStorage, *storage.MemoryStorage) {
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
	encrypted, err := newEncryptedAuthStorage(fault, testCredentialKeyRing(t))
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

type lostReplyClient struct {
	redis.UniversalClient
	lose bool
	err  error
}

func (c *lostReplyClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	cmd := c.UniversalClient.Eval(ctx, script, keys, args...)
	if c.lose && cmd.Err() == nil {
		out := redis.NewCmd(ctx)
		out.SetErr(c.err)
		return out
	}
	return cmd
}
func (c *lostReplyClient) EvalSha(ctx context.Context, sha1 string, keys []string, args ...interface{}) *redis.Cmd {
	cmd := c.UniversalClient.EvalSha(ctx, sha1, keys, args...)
	if c.lose && cmd.Err() == nil {
		out := redis.NewCmd(ctx)
		out.SetErr(c.err)
		return out
	}
	return cmd
}

func TestToolHiveCredentialCustody_AmbiguousCommitAndTombstoneReplies(t *testing.T) {
	f := newFixture(t)
	staged, err := f.core.Stage(t.Context(), f.request, "tsid")
	ref := staged.Recovery
	if err != nil {
		t.Fatal(err)
	}
	assertion := custodyAssertion{custodyRequest: f.request, Recovery: ref}
	lost := &lostReplyClient{UniversalClient: f.client, lose: true, err: errors.New("reply lost")}
	f.core.client = lost
	if err := f.core.Commit(t.Context(), assertion); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("ambiguous commit = %v", err)
	}
	f.core.client = f.client
	if err := f.core.Commit(t.Context(), assertion); err != nil {
		t.Fatalf("commit retry = %v", err)
	}
	f.core.client = lost
	if err := f.core.Tombstone(t.Context(), f.request, ref); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("ambiguous tombstone = %v", err)
	}
	f.core.client = f.client
	if err := f.core.Tombstone(t.Context(), f.request, ref); err != nil {
		t.Fatalf("tombstone retry = %v", err)
	}
	record, _, err := f.core.readRecord(t.Context(), ref)
	if err != nil || record.State != custodyTombstoned {
		t.Fatalf("final record = %#v, %v", record, err)
	}
}

type blockingUpstreamStorage struct {
	*storage.MemoryStorage
	entered chan struct{}
	release chan struct{}
}

func (s *blockingUpstreamStorage) GetUpstreamTokens(ctx context.Context, sessionID, provider string) (*storage.UpstreamTokens, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return s.MemoryStorage.GetUpstreamTokens(ctx, sessionID, provider)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestToolHiveCredentialCustody_StageDeadlineExpiresBeforePublication(t *testing.T) {
	f := newFixture(t)
	clock := &mutableCustodyClock{now: f.clock.now}
	f.core.clock = clock
	blocked := &blockingUpstreamStorage{MemoryStorage: f.inner, entered: make(chan struct{}, 1), release: make(chan struct{})}
	f.core.tokens = upstreamtoken.NewInProcessService(blocked, nil)
	result := make(chan error, 1)
	go func() { _, err := f.core.Stage(context.Background(), f.request, "tsid"); result <- err }()
	<-blocked.entered
	clock.Set(f.request.AttemptDeadline)
	close(blocked.release)
	if err := <-result; !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("Stage = %v", err)
	}
	keys, _, err := f.client.Scan(t.Context(), 0, custodyPrefix+"*", 10).Result()
	if err != nil || len(keys) != 0 {
		t.Fatalf("published keys = %v, %v", keys, err)
	}
}

func TestToolHiveCredentialCustody_RedisScriptsRejectExpiredDeadlineAtomically(t *testing.T) {
	client := newMiniRedis(t)
	ctx := t.Context()
	deadline := time.Now().Add(-time.Minute).UnixMilli()
	expires := time.Now().Add(time.Hour).UnixMilli()
	created, err := custodyCreateScript.Run(ctx, client, []string{"custody:create"}, "new", expires, deadline).Int()
	if err != nil || created != -1 {
		t.Fatalf("create = %d, %v", created, err)
	}
	if _, err := client.Get(ctx, "custody:create").Result(); !errors.Is(err, redis.Nil) {
		t.Fatalf("expired create wrote key: %v", err)
	}
	if err := client.Set(ctx, "custody:replace", "old", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	replaced, err := custodyReplaceScript.Run(ctx, client, []string{"custody:replace"}, "old", "new", expires, deadline).Int()
	if err != nil || replaced != -1 {
		t.Fatalf("replace = %d, %v", replaced, err)
	}
	value, err := client.Get(ctx, "custody:replace").Result()
	if err != nil || value != "old" {
		t.Fatalf("expired replace changed key = %q, %v", value, err)
	}
}

func TestToolHiveCredentialCustody_TombstoneMissingAndCorrupt(t *testing.T) {
	f, assertion := committedFixture(t)
	missing, err := newRecoveryID()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.core.Tombstone(t.Context(), f.request, missing); err != nil {
		t.Fatalf("missing tombstone = %v", err)
	}
	if err := f.client.Set(t.Context(), f.core.key(assertion.Recovery), "corrupt", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if err := f.core.Tombstone(t.Context(), f.request, assertion.Recovery); !errors.Is(err, errCustodyUnavailable) {
		t.Fatalf("corrupt tombstone = %v", err)
	}
}

func committedFixture(t *testing.T) (*custodyFixture, custodyAssertion) {
	t.Helper()
	f := newFixture(t)
	staged, err := f.core.Stage(t.Context(), f.request, "tsid")
	ref := staged.Recovery
	if err != nil {
		t.Fatal(err)
	}
	a := custodyAssertion{custodyRequest: f.request, Recovery: ref}
	if err := f.core.Commit(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	return f, a
}
func newFixture(t *testing.T) *custodyFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	id := session.SessionID("custody-session")
	guard := custodyGuard{SessionID: id, Incarnation: session.NewIncarnationID(), Providers: []string{"provider"}}
	guard.OwnerPartition[0], guard.WorkloadPartition[0], guard.ProfileDigest[0] = 1, 2, 3
	clock := custodyTestClock{now: now}
	inner := storage.NewMemoryStorage()
	if err := inner.StoreUpstreamTokens(t.Context(), "tsid", "provider", &storage.UpstreamTokens{ProviderID: "provider", AccessToken: "access", ExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	client := newMiniRedis(t)
	core, err := newCredentialCustody(client, testCredentialKeyRing(t), upstreamtoken.NewInProcessService(inner, nil), clock, inner)
	if err != nil {
		t.Fatal(err)
	}
	request := custodyRequest{Guard: guard, AttemptDeadline: now.Add(time.Minute)}
	return &custodyFixture{core: core, inner: inner, client: client, clock: clock, request: request, retention: custodyRetention{ExpiresAt: now.Add(time.Hour)}, id: id}
}
func testCredentialKeyRing(t *testing.T) *credentialKeyRing {
	t.Helper()
	ring, err := newCredentialKeyRing("key-a", map[string][]byte{"key-a": []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}
func newMiniRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}
