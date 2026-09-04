package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func createRecoveryAttempt(t *testing.T, repository learning.AttemptRepository, clock *attemptWorkerClock) (learning.AttemptPartition, learning.AttemptRecord) {
	t.Helper()
	_, partition, record := newAttemptWorkerRecord(t, clock)
	// newAttemptWorkerRecord constructs its own repository; recreate its validated
	// immutable record in the repository under test.
	created, err := repository.Create(context.Background(), partition, learning.AttemptCreate{ID: record.ID, Provenance: record.Provenance})
	if err != nil {
		t.Fatal(err)
	}
	return partition, created
}

func completeDiscoveredAttempt(ctx context.Context, repository learning.AttemptRepository, item learning.AttemptWork) error {
	record, claim, err := repository.AcquireClaim(ctx, item.Partition, item.Record.ID, item.Record.Version, time.Minute)
	if err != nil {
		return err
	}
	_, err = repository.Finalize(ctx, item.Partition, record.ID, record.Version, claim, learning.AttemptFinalization{State: learning.AttemptCompleted, Outcome: learning.AttemptOutcomeSucceeded})
	return err
}

type failOnceDiscoveryRepository struct {
	learning.AttemptRepository
	mu     sync.Mutex
	failed bool
}

func (r *failOnceDiscoveryRepository) DiscoverWork(ctx context.Context, query learning.AttemptWorkList) (learning.AttemptWorkPage, error) {
	r.mu.Lock()
	if !r.failed {
		r.failed = true
		r.mu.Unlock()
		return learning.AttemptWorkPage{}, errors.New("discovery unavailable")
	}
	r.mu.Unlock()
	return r.AttemptRepository.DiscoverWork(ctx, query)
}

func TestAttemptRecoveryRetriesAfterDiscoveryFailure(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(95, 0)}
	base := memattempt.New(clock)
	_, _ = createRecoveryAttempt(t, base, clock)
	repository := &failOnceDiscoveryRepository{AttemptRepository: base}
	reported := make(chan struct{}, 1)
	completed := make(chan struct{}, 1)
	recovery := newAttemptRecoveryLoop(context.Background(), repository, 5*time.Millisecond, func(ctx context.Context, item learning.AttemptWork) error {
		err := completeDiscoveredAttempt(ctx, repository, item)
		if err == nil {
			completed <- struct{}{}
		}
		return err
	}, func(error) { reported <- struct{}{} })
	t.Cleanup(recovery.Close)
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("discovery failure was not reported")
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("discovery did not recover after a transient failure")
	}
}

func TestAttemptRecoveryDiscoversWorkAdmittedAfterStartup(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(100, 0)}
	repository := memattempt.New(clock)
	completed := make(chan struct{}, 1)
	recovery := newAttemptRecoveryLoop(context.Background(), repository, 5*time.Millisecond, func(ctx context.Context, item learning.AttemptWork) error {
		err := completeDiscoveredAttempt(ctx, repository, item)
		if err == nil {
			completed <- struct{}{}
		}
		return err
	}, nil)
	t.Cleanup(recovery.Close)
	_, record := createRecoveryAttempt(t, repository, clock)

	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("work admitted after startup was not discovered")
	}
	work, err := repository.DiscoverWork(context.Background(), learning.AttemptWorkList{Limit: learning.MaxAttemptWorkBatch})
	if err != nil || len(work.Work) != 0 {
		t.Fatalf("completed attempt remained discoverable: %+v err=%v record=%s", work, err, record.ID)
	}
}

func TestAttemptRecoveryDiscoversExpiredClaimReplacement(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(105, 0)}
	repository := memattempt.New(clock)
	partition, record := createRecoveryAttempt(t, repository, clock)
	running, _, err := repository.AcquireClaim(context.Background(), partition, record.ID, record.Version, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	completed := make(chan struct{}, 1)
	recovery := newAttemptRecoveryLoop(context.Background(), repository, 5*time.Millisecond, func(ctx context.Context, item learning.AttemptWork) error {
		err := completeDiscoveredAttempt(ctx, repository, item)
		if err == nil {
			completed <- struct{}{}
		}
		return err
	}, nil)
	t.Cleanup(recovery.Close)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatalf("expired claim was not replaced: %+v", running)
	}
	terminal, found, err := repository.Get(context.Background(), partition, record.ID)
	if err != nil || !found || terminal.State != learning.AttemptCompleted || terminal.ClaimGeneration != 0 {
		t.Fatalf("replacement terminal = %+v found=%v err=%v", terminal, found, err)
	}
}

func TestAttemptRecoveryIgnoresLegacyCoordinatorCapacity(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(110, 0)}
	repository := memattempt.New(clock)
	completed := make(chan struct{}, 1)
	recovery := newAttemptRecoveryLoop(context.Background(), repository, 5*time.Millisecond, func(ctx context.Context, item learning.AttemptWork) error {
		err := completeDiscoveredAttempt(ctx, repository, item)
		if err == nil {
			completed <- struct{}{}
		}
		return err
	}, nil)
	t.Cleanup(recovery.Close)

	release := make(chan struct{})
	starts := make(chan session.SessionID, 1)
	coordinator := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1, Capacity: 1, PrincipalCapacity: 1, Timeout: time.Second})
	t.Cleanup(func() {
		close(release)
		coordinator.Close()
	})
	if receipt, err := coordinator.Enqueue(testJob("same-principal", "occupies-worker", &testReflector{start: starts, release: release})); err != nil || receipt.Disposition != reflectionQueued {
		t.Fatalf("occupying enqueue = %+v, err=%v", receipt, err)
	}
	select {
	case <-starts:
	case <-time.After(time.Second):
		t.Fatal("occupying coordinator job did not start")
	}

	_, _ = createRecoveryAttempt(t, repository, clock)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("legacy coordinator capacity delayed repository-authoritative queued work")
	}
}

type blockingRecoverySessionStore struct {
	started  chan struct{}
	returned chan struct{}
}

func (*blockingRecoverySessionStore) Save(context.Context, *session.Session) error { return nil }

func (s *blockingRecoverySessionStore) Load(ctx context.Context, _ session.SessionID) (*session.Session, error) {
	close(s.started)
	<-ctx.Done()
	close(s.returned)
	return nil, ctx.Err()
}

func TestAttemptRecoveryConfiguredTimeoutBoundsBlockingStoreAndPersistsRetry(t *testing.T) {
	t.Parallel()
	clock := &attemptWorkerClock{now: time.Unix(125, 0)}
	repository := memattempt.New(clock)
	partition, record := createRecoveryAttempt(t, repository, clock)
	store := &blockingRecoverySessionStore{started: make(chan struct{}), returned: make(chan struct{})}
	item := learning.AttemptWork{Partition: partition, Record: record}

	startedAt := time.Now()
	err := recoverAttempt(context.Background(), Config{LearningAttemptTimeout: 20 * time.Millisecond}, nil, store, nil, repository, nil, catalogAssets{}, appTestPlacementProvider{}, "legacy-local", item)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recoverAttempt error = %v, want deadline", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("blocking store exceeded configured attempt bound: %v", elapsed)
	}
	select {
	case <-store.started:
	default:
		t.Fatal("blocking store was not exercised")
	}
	select {
	case <-store.returned:
	default:
		t.Fatal("recoverAttempt returned before blocking store cooperatively stopped")
	}
	persisted, found, getErr := repository.Get(context.Background(), partition, record.ID)
	if getErr != nil || !found {
		t.Fatalf("persisted retry found=%v err=%v", found, getErr)
	}
	if persisted.State != learning.AttemptRunning || persisted.State.Terminal() || persisted.FailureCode != learning.FailureNone || persisted.ClaimExpiresAt.IsZero() {
		t.Fatalf("deadline did not persist transient retry/backoff: %+v", persisted)
	}
}

var _ port.SessionStore = (*blockingRecoverySessionStore)(nil)

func TestAttemptRecoveryCloseCancelsAndJoinsWorker(t *testing.T) {
	clock := &attemptWorkerClock{now: time.Unix(120, 0)}
	repository := memattempt.New(clock)
	_, _ = createRecoveryAttempt(t, repository, clock)
	started := make(chan struct{})
	exited := make(chan struct{})
	recovery := newAttemptRecoveryLoop(context.Background(), repository, time.Hour, func(ctx context.Context, _ learning.AttemptWork) error {
		close(started)
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	}, nil)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("recovery worker did not start")
	}
	closedAt := time.Now()
	recovery.Close()
	if elapsed := time.Since(closedAt); elapsed > time.Second {
		t.Fatalf("Close exceeded the cooperative shutdown bound: %v", elapsed)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before the cancellation-aware worker joined")
	}
}
