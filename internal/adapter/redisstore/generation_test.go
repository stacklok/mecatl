package redisstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/stacklok/toolhive-core/redis"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/filewatch"
)

func newGenerationTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	store, err := New(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, server
}

func candidateClient(t *testing.T, server *miniredis.Miniredis) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNoConfiguredFilesRemainWatcherFree(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	if store.reload != nil {
		t.Fatal("plaintext/file-free store unexpectedly started reload lifecycle")
	}
}

func TestNewWithConfigMetadataFailureReleasesInitialLeaseBeforeCleanup(t *testing.T) {
	server := miniredis.RunT(t)
	tracked := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	cfg := Config{Addr: server.Addr(), AllowPlaintext: true}
	deps := defaultStoreDependencies()
	deps.initialClient = func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
		return &metadataFailClient{closeTrackingClient: tracked}, nil
	}
	err := boundedConstructorError(t, cfg, deps)
	if err == nil {
		t.Fatal("NewWithConfig succeeded with metadata initialization failure")
	}
	if tracked.closes.Load() != 1 {
		t.Fatalf("initial client closes = %d, want 1", tracked.closes.Load())
	}
}

type metadataFailClient struct {
	*closeTrackingClient
}

func (*metadataFailClient) Get(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("", errors.New("metadata initialization failed"))
}

func TestNewWithConfigWatcherFailureReleasesInitialLeaseBeforeCleanup(t *testing.T) {
	server := miniredis.RunT(t)
	_, ca := reloadTLSFixture(t)
	caFile := reloadWrite(t, t.TempDir(), "ca.pem", ca)
	tracked := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	cfg := Config{Addr: server.Addr(), CAFile: caFile}
	deps := defaultStoreDependencies()
	deps.initialClient = func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
		return tracked, nil
	}
	deps.watcher = func([]string, time.Duration, time.Duration, func(), func(error)) (*filewatch.Watcher, error) {
		return nil, errors.New("watcher startup failed")
	}
	err := boundedConstructorError(t, cfg, deps)
	if err == nil {
		t.Fatal("NewWithConfig succeeded with watcher startup failure")
	}
	if tracked.closes.Load() != 1 {
		t.Fatalf("initial client closes = %d, want 1", tracked.closes.Load())
	}
}

func boundedConstructorError(t *testing.T, cfg Config, deps storeDependencies) error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		store, err := newWithConfig(cfg, deps)
		if store != nil {
			_ = store.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("NewWithConfig deadlocked during startup cleanup")
		return nil
	}
}

func TestGenerationSwapRoutesNewOperationsAndScheduleStore(t *testing.T) {
	store, oldServer := newGenerationTestStore(t)
	newServer := miniredis.RunT(t)
	scheduleStore := store.ScheduleStore()
	if err := store.Save(context.Background(), session.New("old", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	candidate := candidateClient(t, newServer)
	if err := store.clients.swap(candidate); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), session.New("new", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	if oldServer.Exists(sessionKey("new")) || !newServer.Exists(sessionKey("new")) {
		t.Fatal("new session operation did not use replacement generation")
	}
	schedule := port.Schedule{Spec: port.ScheduleSpec{Name: "replacement", CreatedAt: time.Now()}}
	if err := scheduleStore.Save(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	if oldServer.Exists(scheduleKey("replacement")) || !newServer.Exists(scheduleKey("replacement")) {
		t.Fatal("ScheduleStore retained the startup client")
	}
}

func TestRetiredGenerationClosesAfterLeaseRelease(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	old, release, err := store.clients.acquire()
	if err != nil {
		t.Fatal(err)
	}
	newServer := miniredis.RunT(t)
	if err := store.clients.swap(candidateClient(t, newServer)); err != nil {
		t.Fatal(err)
	}
	if err := old.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("retired client closed with an in-flight lease: %v", err)
	}
	release()
	awaitClientClosed(t, old)
}

func awaitClientClosed(t *testing.T, client redis.UniversalClient) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if client.Ping(context.Background()).Err() != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("client did not close")
}

func TestStoreCloseGraceBoundsRetiredClientCloseAndSwap(t *testing.T) {
	server := miniredis.RunT(t)
	started := make(chan struct{})
	unblock := make(chan struct{})
	old := &blockingCloseClient{
		UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()}),
		started:         started,
		unblock:         unblock,
		finished:        make(chan struct{}),
	}
	manager := newClientGenerations(old)
	candidate := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	if err := manager.swap(candidate); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("retired close did not start")
	}

	startedAt := time.Now()
	if pending := manager.close(20 * time.Millisecond); pending != 1 {
		t.Fatalf("pending generations = %d, want blocked retired generation", pending)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("manager close exceeded grace: %v", elapsed)
	}
	if got := candidate.closes.Load(); got != 1 {
		t.Fatalf("current generation closes = %d, want 1", got)
	}
	close(unblock)
	select {
	case <-old.finished:
	case <-time.After(time.Second):
		t.Fatal("retired client did not eventually close")
	}
	if got := old.closes.Load(); got != 1 {
		t.Fatalf("retired closes = %d, want 1", got)
	}
}

func TestStoreCloseGraceBoundsCurrentClientClose(t *testing.T) {
	server := miniredis.RunT(t)
	client := &blockingCloseClient{
		UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()}),
		started:         make(chan struct{}), unblock: make(chan struct{}), finished: make(chan struct{}),
	}
	manager := newClientGenerations(client)
	startedAt := time.Now()
	if pending := manager.close(20 * time.Millisecond); pending != 1 {
		t.Fatalf("pending generations = %d, want 1", pending)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("manager close exceeded grace: %v", elapsed)
	}
	select {
	case <-client.started:
	default:
		t.Fatal("current client close did not start")
	}
	close(client.unblock)
	select {
	case <-client.finished:
	case <-time.After(time.Second):
		t.Fatal("current client did not eventually close")
	}
}

type blockingCloseClient struct {
	redis.UniversalClient
	started    chan struct{}
	unblock    chan struct{}
	finished   chan struct{}
	startOnce  sync.Once
	finishOnce sync.Once
	closes     atomic.Int32
}

func (c *blockingCloseClient) Close() error {
	c.closes.Add(1)
	c.startOnce.Do(func() { close(c.started) })
	<-c.unblock
	err := c.UniversalClient.Close()
	c.finishOnce.Do(func() {
		if c.finished != nil {
			close(c.finished)
		}
	})
	return err
}

func TestGenerationRetirementRaceClosesEachClientOnce(t *testing.T) {
	server := miniredis.RunT(t)
	old := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	candidate := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	manager := newClientGenerations(old)
	_, release, err := manager.acquire()
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := manager.swap(candidate); err != nil {
			t.Errorf("swap: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		release()
		release()
	}()
	close(start)
	wg.Wait()
	if pending := manager.close(time.Second); pending != 0 {
		t.Fatalf("pending generations = %d", pending)
	}
	if got := old.closes.Load(); got != 1 {
		t.Fatalf("old closes = %d, want 1", got)
	}
	if got := candidate.closes.Load(); got != 1 {
		t.Fatalf("candidate closes = %d, want 1", got)
	}
}

func TestCloseTimesOutWithoutForceClosingActiveGeneration(t *testing.T) {
	server := miniredis.RunT(t)
	tracked := &closeTrackingClient{UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	diagnostics := &capturedDiagnostics{}
	store := &Store{
		clients: newClientGenerations(tracked), diagnostics: diagnostics,
		closeGrace: 20 * time.Millisecond,
	}
	_, release, err := store.clients.acquire()
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %v", elapsed)
	}
	if got := tracked.closes.Load(); got != 0 {
		t.Fatalf("active client closes at timeout = %d, want 0", got)
	}
	if _, _, err := store.clients.acquire(); !errors.Is(err, errStoreClosed) {
		t.Fatalf("acquire after shutdown started: %v", err)
	}
	candidate := candidateClient(t, server)
	if err := store.clients.swap(candidate); !errors.Is(err, errStoreClosed) {
		t.Fatalf("swap after shutdown started: %v", err)
	}
	_ = candidate.Close()
	logs := diagnostics.text()
	if !strings.Contains(logs, "component redis outcome timed_out reason active_operations count 1") {
		t.Fatalf("shutdown warning = %q", logs)
	}
	if strings.Contains(logs, server.Addr()) {
		t.Fatalf("shutdown warning leaked endpoint: %q", logs)
	}

	release()
	release()
	deadline := time.Now().Add(time.Second)
	for tracked.closes.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := tracked.closes.Load(); got != 1 {
		t.Fatalf("client closes after final release = %d, want 1", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(diagnostics.text(), "redis store shutdown"); got != 1 {
		t.Fatalf("shutdown warning count = %d, want 1", got)
	}
}

func TestCloseRejectsAcquisitionAndSwap(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.clients.acquire(); !errors.Is(err, errStoreClosed) {
		t.Fatalf("acquire after close: %v", err)
	}
	server := miniredis.RunT(t)
	candidate := candidateClient(t, server)
	if err := store.clients.swap(candidate); !errors.Is(err, errStoreClosed) {
		t.Fatalf("swap after close: %v", err)
	}
	_ = candidate.Close()
	if err := store.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestEventIteratorPinsGenerationThroughYield(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	id := session.SessionID("iterator")
	if err := store.Append(context.Background(), id, session.Event{Type: session.EvResult}); err != nil {
		t.Fatal(err)
	}
	old := store.testClient()
	newServer := miniredis.RunT(t)
	for _, err := range store.Read(context.Background(), id) {
		if err != nil {
			t.Fatal(err)
		}
		if err := store.clients.swap(candidateClient(t, newServer)); err != nil {
			t.Fatal(err)
		}
		if err := old.Ping(context.Background()).Err(); err != nil {
			t.Fatalf("iterator did not retain generation while yielding: %v", err)
		}
		break
	}
	awaitClientClosed(t, old)
}

func TestMigrationFamilyAndFinalizeStayOnAcquiredGenerationAfterSwap(t *testing.T) {
	oldServer := miniredis.RunT(t)
	legacy := session.New("legacy-swap", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now().UTC())
	blob, err := sessnap.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	oldServer.HSet(sessionKey(legacy.ID), fieldBlob, string(blob), fieldMtime, "1")
	store, err := New(oldServer.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bound, release, err := store.AcquireSessionMigrationJob(context.Background(), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	newServer := miniredis.RunT(t)
	if err := store.clients.swap(candidateClient(t, newServer)); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.InspectSessionMigration(bound)
	if err != nil || len(inspection.Families) != 1 {
		t.Fatalf("inspection = %+v, %v", inspection, err)
	}
	if reason, err := store.MigrateSessionFamily(bound, inspection.Families[0]); err != nil || reason != "" {
		t.Fatalf("migration = %q, %v", reason, err)
	}
	job := port.SessionMigrationJob{ID: strings.Repeat("a", 32), Generation: inspection.Generation}
	if err := store.SaveSessionMigrationJob(bound, job); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSessionMigrationJob(bound, job.ID)
	if err != nil || loaded.ID != job.ID || loaded.Generation != job.Generation {
		t.Fatalf("loaded migration job = %+v, %v", loaded, err)
	}
	if newServer.Exists(migrationJobKeyBase + job.ID) {
		t.Fatal("migration job load/save escaped to replacement generation")
	}
	published, err := store.FinalizeSessionMigrationCoverage(bound, inspection.Generation, 1)
	if err != nil || !published {
		t.Fatalf("finalize = %v, %v", published, err)
	}
	if got, _ := oldServer.Get(metadataIndexStateKey); got != metadataIndexReady {
		t.Fatalf("old generation state = %q", got)
	}
	if newServer.Exists(metadataIndexStateKey) || newServer.Exists(metadataGlobalIndexKey) {
		t.Fatal("migration work escaped to replacement generation")
	}
}

func TestSwappedBackendCoversStoreAndSchedulerOperationFamilies(t *testing.T) {
	store, oldServer := newGenerationTestStore(t)
	newServer := miniredis.RunT(t)
	seed, err := New(newServer.Addr())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	sess := session.New("swapped-read", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, now)
	if err := seed.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if err := seed.Append(context.Background(), sess.ID, session.Event{Type: session.EvResult}); err != nil {
		t.Fatal(err)
	}
	schedule := port.Schedule{Spec: port.ScheduleSpec{Name: "swapped-schedule", CreatedAt: now}, State: port.ScheduleState{Enabled: true, NextFireAt: now}}
	if err := seed.ScheduleStore().Save(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.clients.swap(candidateClient(t, newServer)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load(context.Background(), sess.ID); err != nil {
		t.Fatalf("store read: %v", err)
	}
	events := 0
	for _, err := range store.Read(context.Background(), sess.ID) {
		if err != nil {
			t.Fatal(err)
		}
		events++
	}
	if events != 1 {
		t.Fatalf("event count = %d", events)
	}
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	schedules := store.ScheduleStore()
	if _, err := schedules.Load(context.Background(), schedule.Spec.Name); err != nil {
		t.Fatalf("scheduler read: %v", err)
	}
	claimed, err := schedules.Claim(context.Background(), schedule.Spec.Name, now, time.Time{})
	if err != nil {
		t.Fatalf("scheduler claim: %v", err)
	}
	if err := schedules.RecordFire(context.Background(), port.ScheduleFire{
		ID: "swapped-fire", ScheduleName: schedule.Spec.Name, SessionID: "fire-session", FiredAt: claimed.State.LastFireAt, Stop: session.StopEndTurn,
	}); err != nil {
		t.Fatalf("scheduler fire: %v", err)
	}
	if !newServer.Exists(scheduleFireKey("swapped-fire")) || oldServer.Exists(scheduleFireKey("swapped-fire")) {
		t.Fatal("scheduler fire did not use replacement generation")
	}
	if err := store.Delete(context.Background(), sess.ID); err != nil {
		t.Fatalf("store delete: %v", err)
	}
	if newServer.Exists(sessionKey(sess.ID)) || newServer.Exists(eventsKey(sess.ID)) {
		t.Fatal("store delete did not remove replacement-backend family")
	}
}

func TestMigrationLockLifecyclePinsGeneration(t *testing.T) {
	store, oldServer := newGenerationTestStore(t)
	bound, release, err := store.AcquireSessionMigrationJob(context.Background(), "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	old := store.testClient()
	newServer := miniredis.RunT(t)
	if err := store.clients.swap(candidateClient(t, newServer)); err != nil {
		t.Fatal(err)
	}
	if err := old.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("migration lock did not retain its generation: %v", err)
	}
	if err := store.CheckSessionMigrationJobOwnership(bound); err != nil {
		t.Fatalf("ownership check did not use acquired generation: %v", err)
	}
	job := port.SessionMigrationJob{ID: "0123456789abcdef0123456789abcdef"}
	if err := store.SaveSessionMigrationJob(bound, job); err != nil {
		t.Fatalf("migration mutation did not use acquired generation: %v", err)
	}
	if !oldServer.Exists(migrationJobKeyBase+job.ID) || newServer.Exists(migrationJobKeyBase+job.ID) {
		t.Fatal("migration checkpoint did not stay on acquired generation")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	awaitClientClosed(t, old)
}
