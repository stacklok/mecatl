package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/automaticconformance"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
	"github.com/stacklok/mecatl/internal/adapter/automaticstore"
)

func TestInvariant_automatic_reservation_reconciliation_is_build_owned_and_joined(t *testing.T) {
	policy := learning.AutomaticAdmissionPolicy{
		Window: time.Hour, DedupeWindow: 24 * time.Hour, MaxCount: 1, MaxTokens: 100,
		MaxCountPerPrincipal: 1, MaxTokensPerPrincipal: 100, ReservationClaimDuration: time.Minute,
	}
	ledger, attempts := automaticRepositories(t, policy)
	blocking := &blockingDiscoveryLedger{AutomaticAdmissionLedger: ledger, entered: make(chan struct{}), exited: make(chan struct{})}
	loop := newAutomaticReservationReconciliationLoop(context.Background(), blocking, attempts, time.Hour, nil)
	select {
	case <-blocking.entered:
	case <-time.After(time.Second):
		t.Fatal("reconciliation loop did not start discovery")
	}
	closed := make(chan struct{})
	go func() {
		loop.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel and join reservation discovery")
	}
	select {
	case <-blocking.exited:
	default:
		t.Fatal("Close returned before discovery exited")
	}
}

type blockingDiscoveryLedger struct {
	learning.AutomaticAdmissionLedger
	entered chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func (l *blockingDiscoveryLedger) DiscoverExpired(ctx context.Context, _ uint32) ([]learning.AutomaticReservation, error) {
	l.once.Do(func() { close(l.entered) })
	<-ctx.Done()
	close(l.exited)
	return nil, ctx.Err()
}

func TestADR_0259_AutomaticReservationsReconcileWithoutExceedingGlobalMaximum(t *testing.T) {
	ctx := context.Background()
	policy := learning.AutomaticAdmissionPolicy{
		Window: time.Hour, Cooldown: 0, DedupeWindow: 24 * time.Hour,
		MaxCount: 1, MaxTokens: 100, MaxCountPerPrincipal: 1, MaxTokensPerPrincipal: 100,
		ReservationClaimDuration: time.Minute,
	}
	partition, create := automaticAttemptFixture(t, "primary")

	t.Run("crash before reservation spends nothing", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t, policy)
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		record, err := reconciler.reserveAndCreate(ctx, automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy), create)
		if err != nil || record.ID != create.ID {
			t.Fatalf("retry after pre-reservation crash = %+v, err=%v", record, err)
		}
	})

	t.Run("lost reserve response retries one identity and one charge", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t, policy)
		lossy := &reserveResponseLossLedger{AutomaticAdmissionLedger: ledger}
		reconciler := automaticReservationReconciler{ledger: lossy, attempts: attempts}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy)
		if _, err := reconciler.reserveAndCreate(ctx, req, create); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost reserve response error = %v", err)
		}
		reconciler.ledger = ledger
		if _, err := reconciler.reserveAndCreate(ctx, req, create); err != nil {
			t.Fatalf("reserve retry: %v", err)
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, policy)
	})

	t.Run("lost create response reconciles after expiry and reassignment", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t, policy)
		lossy := &createResponseLossRepository{AttemptRepository: attempts}
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: lossy}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy)
		if _, err := reconciler.reserveAndCreate(ctx, req, create); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost create response error = %v", err)
		}
		reconciler.attempts = attempts
		retry := req
		if _, err := reconciler.reserveAndCreate(ctx, retry, create); err != nil {
			t.Fatalf("post-expiry retry: %v", err)
		}
		reservation, found, err := ledger.Get(ctx, req.ID)
		if err != nil || !found || reservation.Fence.Generation != 0 || reservation.Charge != learning.AutomaticChargeRetained {
			t.Fatalf("reconciled reservation = %+v, found=%v err=%v", reservation, found, err)
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, policy)
	})

	t.Run("concurrent retries converge without duplicate attempts", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t, policy)
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy)
		const retries = 16
		start := make(chan struct{})
		results := make(chan error, retries)
		var wg sync.WaitGroup
		for range retries {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := reconciler.reserveAndCreate(ctx, req, create)
				results <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("concurrent retry: %v", err)
			}
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, policy)
	})

	t.Run("expired pre-create reservation is reclaimed", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t, policy)
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy)
		reservation, err := ledger.Reserve(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		resolved, err := reconciler.reconcile(ctx, reservation.ID, nil)
		if err != nil || resolved.Charge != learning.AutomaticChargeReclaimed {
			t.Fatalf("reclaim = %+v, err=%v", resolved, err)
		}
		replacementPartition, replacement := automaticAttemptFixture(t, "replacement")
		replacementReq := automaticRequestForAttempt(t, replacement.ID, replacementPartition, "digest-replacement", policy)
		if _, err := ledger.Reserve(ctx, replacementReq); err != nil {
			t.Fatalf("replacement after reclaim: %v", err)
		}
	})

	t.Run("replacement Build discovers crash immediately after Reserve and reclaims", func(t *testing.T) {
		base, ledger, attempts, buildPolicy := automaticBuildRepositories(t)
		buildPartition, buildCreate := automaticAttemptFixture(t, "build-reserve")
		req := automaticRequestForAttempt(t, buildCreate.ID, buildPartition, "digest-build-reserve", buildPolicy)
		reserved, err := ledger.Reserve(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if !time.Now().After(reserved.Fence.ExpiresAt) {
			t.Fatalf("crash fixture fence is not expired: %+v", reserved.Fence)
		}
		replacement := startAutomaticReplacementBuild(t, base)
		defer replacement.Close()
		resolved := waitForAutomaticCharge(t, ledger, req.ID, learning.AutomaticChargeReclaimed)
		if resolved.AttemptCreated {
			t.Fatalf("reserve-only crash invented attempt linkage: %+v", resolved)
		}
		page, err := attempts.List(ctx, buildPartition, learning.AttemptList{})
		if err != nil || len(page.Records) != 0 {
			t.Fatalf("reserve-only replacement created attempts: %+v, err=%v", page.Records, err)
		}
	})

	t.Run("replacement Build discovers crash after attempt Create and retains without duplicate", func(t *testing.T) {
		base, ledger, attempts, buildPolicy := automaticBuildRepositories(t)
		buildPartition, buildCreate := automaticAttemptFixture(t, "build-create")
		req := automaticRequestForAttempt(t, buildCreate.ID, buildPartition, "digest-build-create", buildPolicy)
		reserved, err := ledger.Reserve(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = attempts.Create(ctx, buildPartition, buildCreate); err != nil {
			t.Fatal(err)
		}
		if !time.Now().After(reserved.Fence.ExpiresAt) {
			t.Fatalf("crash fixture fence is not expired: %+v", reserved.Fence)
		}
		replacement := startAutomaticReplacementBuild(t, base)
		defer replacement.Close()
		resolved := waitForAutomaticCharge(t, ledger, req.ID, learning.AutomaticChargeRetained)
		if !resolved.AttemptCreated {
			t.Fatalf("post-create crash did not retain linkage: %+v", resolved)
		}
		page, err := attempts.List(ctx, buildPartition, learning.AttemptList{})
		if err != nil || len(page.Records) != 1 || page.Records[0].ID != buildCreate.ID {
			t.Fatalf("replacement attempts = %+v, err=%v; want one %q", page.Records, err, buildCreate.ID)
		}
	})

	t.Run("failed create keeps conservative charge until same identity retries", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t, policy)
		failing := &createFailureRepository{AttemptRepository: attempts}
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: failing}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy)
		if _, err := reconciler.reserveAndCreate(ctx, req, create); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failed create error = %v", err)
		}
		reservation, found, err := ledger.Get(ctx, req.ID)
		if err != nil || !found || reservation.Charge != learning.AutomaticChargeRetained {
			t.Fatalf("failed-create reservation = %+v, found=%v, err=%v", reservation, found, err)
		}
		if _, found, err = attempts.Get(ctx, partition, create.ID); err != nil || found {
			t.Fatalf("failed create attempt found=%v, err=%v", found, err)
		}
		replacementPartition, replacement := automaticAttemptFixture(t, "failed-create-replacement")
		_, err = ledger.Reserve(ctx, automaticRequestForAttempt(t, replacement.ID, replacementPartition, "digest-failed-create-replacement", policy))
		if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
			t.Fatalf("replacement reserve error = %v, want retained global charge", err)
		}
		reconciler.attempts = attempts
		if _, err = reconciler.reserveAndCreate(ctx, req, create); err != nil {
			t.Fatalf("same-identity retry: %v", err)
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, policy)
	})

	t.Run("reconciler reclaim fences a paused late creator", func(t *testing.T) {
		clock := automaticconformance.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		ledger, attempts := automaticRepositoriesWithClock(t, policy, clock)
		paused := &pauseAfterAbsentGetRepository{
			AttemptRepository: attempts,
			seenAbsent:        make(chan struct{}),
			resume:            make(chan struct{}),
		}
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: paused}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy)
		creatorDone := make(chan error, 1)
		go func() {
			_, err := reconciler.reserveAndCreate(ctx, req, create)
			creatorDone <- err
		}()

		select {
		case <-paused.seenAbsent:
		case <-time.After(time.Second):
			t.Fatal("creator did not pause after observing the absent attempt")
		}
		reservation, found, err := ledger.Get(ctx, req.ID)
		if err != nil || !found {
			t.Fatalf("held reservation = %+v, found=%v, err=%v", reservation, found, err)
		}
		clock.Set(reservation.Fence.ExpiresAt)
		expired, err := ledger.DiscoverExpired(ctx, 1)
		if err != nil || len(expired) != 1 {
			t.Fatalf("expired reservations = %+v, err=%v", expired, err)
		}
		background := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		resolved, err := background.reconcileReservation(ctx, expired[0], nil)
		if err != nil || resolved.Charge != learning.AutomaticChargeReclaimed {
			t.Fatalf("background reclaim = %+v, err=%v", resolved, err)
		}

		replacementPartition, replacement := automaticAttemptFixture(t, "paused-replacement")
		replacementReq := automaticRequestForAttempt(t, replacement.ID, replacementPartition, "digest-paused-replacement", policy)
		if _, err = background.reserveAndCreate(ctx, replacementReq, replacement); err != nil {
			t.Fatalf("replacement create: %v", err)
		}
		close(paused.resume)
		select {
		case err = <-creatorDone:
			if err == nil {
				t.Fatal("stale creator proceeded after its reservation was reclaimed")
			}
		case <-time.After(time.Second):
			t.Fatal("stale creator did not finish")
		}
		if _, found, err = attempts.Get(ctx, partition, create.ID); err != nil || found {
			t.Fatalf("stale primary attempt found=%v, err=%v", found, err)
		}
		page, err := attempts.List(ctx, replacementPartition, learning.AttemptList{})
		if err != nil || len(page.Records) != 1 || page.Records[0].ID != replacement.ID {
			t.Fatalf("replacement attempts = %+v, err=%v", page.Records, err)
		}
		thirdPartition, third := automaticAttemptFixture(t, "paused-third")
		_, err = ledger.Reserve(ctx, automaticRequestForAttempt(t, third.ID, thirdPartition, "digest-paused-third", policy))
		if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
			t.Fatalf("third reserve error = %v, want global limit", err)
		}
	})

	t.Run("abandoned created attempt retains charge", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t, policy)
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", policy)
		record, err := reconciler.reserveAndCreate(ctx, req, create)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = attempts.Abandon(ctx, partition, record.ID, record.Version); err != nil {
			t.Fatal(err)
		}
		resolved, err := reconciler.reconcile(ctx, req.ID, &create)
		if err != nil || resolved.Charge != learning.AutomaticChargeRetained {
			t.Fatalf("abandoned reconciliation = %+v, err=%v", resolved, err)
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, policy)
	})
}

type pauseAfterAbsentGetRepository struct {
	learning.AttemptRepository
	seenAbsent chan struct{}
	resume     chan struct{}
	once       sync.Once
}

func (r *pauseAfterAbsentGetRepository) Get(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID) (learning.AttemptRecord, bool, error) {
	record, found, err := r.AttemptRepository.Get(ctx, partition, id)
	if err == nil && !found {
		paused := false
		r.once.Do(func() {
			paused = true
			close(r.seenAbsent)
		})
		if paused {
			select {
			case <-ctx.Done():
				return learning.AttemptRecord{}, false, ctx.Err()
			case <-r.resume:
			}
		}
	}
	return record, found, err
}

type reserveResponseLossLedger struct {
	learning.AutomaticAdmissionLedger
	lost bool
}

func (l *reserveResponseLossLedger) Reserve(ctx context.Context, req learning.AutomaticReservationRequest) (learning.AutomaticReservation, error) {
	reservation, err := l.AutomaticAdmissionLedger.Reserve(ctx, req)
	if err == nil && !l.lost {
		l.lost = true
		return learning.AutomaticReservation{}, context.DeadlineExceeded
	}
	return reservation, err
}

type createFailureRepository struct {
	learning.AttemptRepository
}

func (*createFailureRepository) Create(context.Context, learning.AttemptPartition, learning.AttemptCreate) (learning.AttemptRecord, error) {
	return learning.AttemptRecord{}, context.DeadlineExceeded
}

type createResponseLossRepository struct {
	learning.AttemptRepository
	lost bool
}

func (r *createResponseLossRepository) Create(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	record, err := r.AttemptRepository.Create(ctx, partition, create)
	if err == nil && !r.lost {
		r.lost = true
		return learning.AttemptRecord{}, context.DeadlineExceeded
	}
	return record, err
}

func automaticBuildRepositories(t *testing.T) (string, learning.AutomaticAdmissionLedger, learning.AttemptRepository, learning.AutomaticAdmissionPolicy) {
	t.Helper()
	base := t.TempDir()
	policy, err := automaticAdmissionPolicy(defaultLearningAutomaticConfig())
	if err != nil {
		t.Fatal(err)
	}
	clock := automaticconformance.NewClock(time.Now().UTC().Add(-2 * policy.ReservationClaimDuration))
	ledger, err := automaticstore.New(filepath.Join(base, "automatic-admission"), policy, clock)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := attemptstore.New(filepath.Join(base, "learning-attempts"))
	if err != nil {
		t.Fatal(err)
	}
	return base, ledger, attempts, policy
}

func startAutomaticReplacementBuild(t *testing.T, base string) *Built {
	t.Helper()
	built, err := buildIsolated(t, context.Background(), Config{
		Model: "mock", Workspace: t.TempDir(), NoSoul: true, LearningMode: learning.Review,
		UserModelDir: base, MemoryDir: t.TempDir(), MockProvider: mockllm.New(mockllm.TextTurn("unused")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func automaticRepositories(t *testing.T, policy learning.AutomaticAdmissionPolicy) (learning.AutomaticAdmissionLedger, learning.AttemptRepository) {
	t.Helper()
	return automaticRepositoriesWithClock(t, policy, wallclock.Clock{})
}

func automaticRepositoriesWithClock(t *testing.T, policy learning.AutomaticAdmissionPolicy, clock port.Clock) (learning.AutomaticAdmissionLedger, learning.AttemptRepository) {
	t.Helper()
	root := t.TempDir()
	ledger, err := automaticstore.New(root+"/ledger", policy, clock)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := attemptstore.New(root + "/attempts")
	if err != nil {
		t.Fatal(err)
	}
	return ledger, attempts
}

func waitForAutomaticCharge(t *testing.T, ledger learning.AutomaticAdmissionLedger, id learning.AutomaticReservationID, want learning.AutomaticChargeDisposition) learning.AutomaticReservation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		reservation, found, err := ledger.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if found && reservation.Charge == want {
			return reservation
		}
		runtime.Gosched()
	}
	t.Fatalf("reservation %q did not converge to %q", id, want)
	return learning.AutomaticReservation{}
}

func automaticAttemptFixture(t *testing.T, key string) (learning.AttemptPartition, learning.AttemptCreate) {
	t.Helper()
	const caller = "principal"
	partition, err := learning.DeriveAttemptPartition(caller)
	if err != nil {
		t.Fatal(err)
	}
	source := learning.AttemptSource{SessionID: session.SessionID("session-" + key), RunID: learning.DurableRunID("run_aaaaaaaaaaaaaaaaaaaaaaaaaa"), CanonicalDigest: learning.CanonicalDigest(testDigest("source-" + key))}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionWeighted, source, learning.CurrentPromptBinding{Ordinal: 0, Digest: learning.CanonicalDigest(testDigest("prompt-" + key)), Origin: learning.PromptOriginCurrentPrincipal})
	if err != nil {
		t.Fatal(err)
	}
	id, err := learning.DeterministicAttemptID(caller, source)
	if err != nil {
		t.Fatal(err)
	}
	return partition, learning.AttemptCreate{ID: id, Provenance: provenance}
}

func automaticRequestForAttempt(t *testing.T, id learning.AttemptID, partition learning.AttemptPartition, digest string, policy learning.AutomaticAdmissionPolicy) learning.AutomaticReservationRequest {
	t.Helper()
	reservationID, err := learning.AutomaticReservationIDForAttempt(id)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := learning.AutomaticAdmissionPolicyRevisionFor(policy)
	if err != nil {
		t.Fatal(err)
	}
	return learning.AutomaticReservationRequest{ID: reservationID, AttemptID: id, Principal: partition, Digest: learning.CanonicalDigest(testDigest(digest)), Class: learning.AdmissionWeighted, Tokens: 10, ExpectedPolicyRevision: revision}
}

func assertOneAttemptAndNoReplacement(ctx context.Context, t *testing.T, attempts learning.AttemptRepository, ledger learning.AutomaticAdmissionLedger, partition learning.AttemptPartition, id learning.AttemptID, policy learning.AutomaticAdmissionPolicy) {
	t.Helper()
	page, err := attempts.List(ctx, partition, learning.AttemptList{})
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != id {
		t.Fatalf("attempts = %+v, err=%v; want one %q", page.Records, err, id)
	}
	replacementPartition, replacement := automaticAttemptFixture(t, "replacement")
	_, err = ledger.Reserve(ctx, automaticRequestForAttempt(t, replacement.ID, replacementPartition, "digest-replacement", policy))
	if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
		t.Fatalf("replacement reserve error = %v, want global limit", err)
	}
}

func automaticStoreForTest(t *testing.T, dir string, cfg LearningAutomaticConfig) learning.AutomaticAdmissionLedger {
	t.Helper()
	policy, err := automaticAdmissionPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := automaticstore.New(dir, policy, wallclock.Clock{})
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
