package redisstore

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/stacklok/toolhive-core/redis"

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
	cfg := Config{
		Addr:           server.Addr(),
		AllowPlaintext: true,
		initialClientFactory: func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
			return &metadataFailClient{closeTrackingClient: tracked}, nil
		},
	}
	err := boundedConstructorError(t, cfg)
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
	cfg := Config{
		Addr:   server.Addr(),
		CAFile: caFile,
		initialClientFactory: func(context.Context, *tcredis.Config) (redis.UniversalClient, error) {
			return tracked, nil
		},
		watcherFactory: func([]string, time.Duration, time.Duration, func(), func(error)) (*filewatch.Watcher, error) {
			return nil, errors.New("watcher startup failed")
		},
	}
	err := boundedConstructorError(t, cfg)
	if err == nil {
		t.Fatal("NewWithConfig succeeded with watcher startup failure")
	}
	if tracked.closes.Load() != 1 {
		t.Fatalf("initial client closes = %d, want 1", tracked.closes.Load())
	}
}

func boundedConstructorError(t *testing.T, cfg Config) error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		store, err := NewWithConfig(cfg)
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
	if err := store.Save(context.Background(), session.New("old", session.ModeDefault, "", session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	candidate := candidateClient(t, newServer)
	if err := store.clients.swap(candidate); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), session.New("new", session.ModeDefault, "", session.Limits{}, time.Now())); err != nil {
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
	ctx, release, err := store.pin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	old := store.redis(ctx)
	newServer := miniredis.RunT(t)
	if err := store.clients.swap(candidateClient(t, newServer)); err != nil {
		t.Fatal(err)
	}
	if err := old.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("retired client closed with an in-flight lease: %v", err)
	}
	release()
	if err := old.Ping(context.Background()).Err(); err == nil {
		t.Fatal("retired client remained open after its last lease released")
	}
}

func TestStoreCloseWaitsForRetiredGenerationClose(t *testing.T) {
	server := miniredis.RunT(t)
	started := make(chan struct{})
	unblock := make(chan struct{})
	old := &blockingCloseClient{
		UniversalClient: redis.NewClient(&redis.Options{Addr: server.Addr()}),
		started:         started,
		unblock:         unblock,
	}
	manager := newClientGenerations(old)
	candidate := redis.NewClient(&redis.Options{Addr: server.Addr()})
	swapDone := make(chan error, 1)
	go func() { swapDone <- manager.swap(candidate) }()
	<-started

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.close() }()
	for {
		manager.mu.Lock()
		closed := manager.closed
		manager.mu.Unlock()
		if closed {
			break
		}
		runtime.Gosched()
	}
	select {
	case err := <-closeDone:
		t.Fatalf("store close returned before retired client Close completed: %v", err)
	default:
	}

	close(unblock)
	if err := <-swapDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

type blockingCloseClient struct {
	redis.UniversalClient
	started chan struct{}
	unblock chan struct{}
}

func (c *blockingCloseClient) Close() error {
	close(c.started)
	<-c.unblock
	return c.UniversalClient.Close()
}

func TestCloseRejectsAcquisitionAndSwap(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.pin(context.Background()); !errors.Is(err, errStoreClosed) {
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
	if err := old.Ping(context.Background()).Err(); err == nil {
		t.Fatal("iterator generation remained open after iteration ended")
	}
}

func TestMigrationLockLifecyclePinsGeneration(t *testing.T) {
	store, _ := newGenerationTestStore(t)
	_, release, err := store.AcquireSessionMigrationJob(context.Background(), "0123456789abcdef0123456789abcdef")
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
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := old.Ping(context.Background()).Err(); err == nil {
		t.Fatal("migration generation remained open after lock release")
	}
}
