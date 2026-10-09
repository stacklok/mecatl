package mcpbroker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker/credentialstore"
)

type fixedCredentialService struct{ calls int }

func (s *fixedCredentialService) GetValidTokens(context.Context, string, string) (*upstreamtoken.UpstreamCredential, error) {
	s.calls++
	return &upstreamtoken.UpstreamCredential{AccessToken: "usable-credential"}, nil
}

func TestCredentialCustodyUsesTokenServiceCapability(t *testing.T) {
	f := newFixture(t)
	service := &fixedCredentialService{}
	core, err := newCredentialCustody(f.client, testCredentialKeyRing(t), service, f.clock, f.inner)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := core.Stage(t.Context(), f.request, "tsid")
	if err != nil {
		t.Fatal(err)
	}
	assertion := custodyAssertion{custodyRequest: f.request, Recovery: staged.Recovery}
	if err := core.Commit(t.Context(), assertion); err != nil {
		t.Fatal(err)
	}
	credential, err := core.Resolve(t.Context(), assertion, "provider")
	if err != nil || credential == nil || credential.AccessToken != "usable-credential" || service.calls != 2 {
		t.Fatalf("token-service capability was not used: calls=%d err=%v", service.calls, err)
	}
}

func TestCredentialCustodyRejectsNilTokenService(t *testing.T) {
	f := newFixture(t)
	for _, service := range []upstreamtoken.Service{nil, (*upstreamtoken.InProcessService)(nil), (*fixedCredentialService)(nil)} {
		core, err := newCredentialCustody(f.client, testCredentialKeyRing(t), service, f.clock, f.inner)
		if core != nil || !errors.Is(err, errCustodyUnavailable) {
			t.Fatalf("nil token service accepted: %v", err)
		}
	}
}

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
		drifted := *row
		drifted.SessionExpiresAt = drifted.SessionExpiresAt.Add(time.Minute)
		row = &drifted
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
	request.Guard.OwnerPartition[0], request.Guard.WorkloadPartition[0] = 1, 2
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

func TestToolHiveCredentialCustody_TombstonedRecordHasDistinctTerminalError(t *testing.T) {
	f, assertion := committedFixture(t)
	if err := f.core.Tombstone(t.Context(), f.request, assertion.Recovery); err != nil {
		t.Fatal(err)
	}
	if err := f.core.Commit(t.Context(), assertion); !errors.Is(err, errCustodyTombstoned) {
		t.Fatalf("Commit tombstoned custody = %v, want terminal tombstone error", err)
	}
	if _, err := f.core.Load(t.Context(), assertion); !errors.Is(err, errCustodyTombstoned) {
		t.Fatalf("Load tombstoned custody = %v, want terminal tombstone error", err)
	}
	wrongGuard := assertion
	wrongGuard.Guard = cloneGuard(assertion.Guard)
	wrongGuard.Guard.ProfileDigest[0]++
	if _, err := f.core.Load(t.Context(), wrongGuard); !errors.Is(err, errCustodyUnavailable) || errors.Is(err, errCustodyTombstoned) {
		t.Fatalf("Load with wrong guard = %v, want opaque unavailable", err)
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
	if commitErr != nil && !errors.Is(commitErr, errCustodyConflict) && !errors.Is(commitErr, errCustodyTombstoned) {
		t.Fatalf("commit = %v", commitErr)
	}
	if tombstoneErr != nil {
		t.Fatalf("tombstone = %v", tombstoneErr)
	}
	record, _, err := f.core.readRecord(t.Context(), ref)
	if err != nil || record.State != custodyTombstoned || record.TSID != "" {
		t.Fatalf("final record = %#v, %v", record, err)
	}
	if _, err := f.core.Load(t.Context(), assertion); !errors.Is(err, errCustodyTombstoned) {
		t.Fatalf("tombstoned load = %v, want terminal tombstone error", err)
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
	decorated, err := credentialstore.New(f.inner, testCredentialKeyRing(t), toolHiveAuthStoragePrefix)
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
