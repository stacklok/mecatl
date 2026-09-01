package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/attemptstore"
	"github.com/stacklok/mecatl/internal/adapter/automaticstore"
)

func TestADR_0254_AutomaticReservationsReconcileWithoutExceedingGlobalMaximum(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	policy := learning.AutomaticAdmissionPolicy{
		Window: time.Hour, Cooldown: 0, DedupeWindow: 24 * time.Hour,
		MaxCount: 1, MaxTokens: 100, MaxCountPerPrincipal: 1, MaxTokensPerPrincipal: 100,
		ReservationClaimDuration: time.Minute,
	}
	partition, create := automaticAttemptFixture(t, "primary")

	t.Run("crash before reservation spends nothing", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t)
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		record, err := reconciler.reserveAndCreate(ctx, automaticRequestForAttempt(t, create.ID, partition, "digest-primary", now, policy), create)
		if err != nil || record.ID != create.ID {
			t.Fatalf("retry after pre-reservation crash = %+v, err=%v", record, err)
		}
	})

	t.Run("lost reserve response retries one identity and one charge", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t)
		lossy := &reserveResponseLossLedger{AutomaticAdmissionLedger: ledger}
		reconciler := automaticReservationReconciler{ledger: lossy, attempts: attempts}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", now, policy)
		if _, err := reconciler.reserveAndCreate(ctx, req, create); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost reserve response error = %v", err)
		}
		reconciler.ledger = ledger
		if _, err := reconciler.reserveAndCreate(ctx, req, create); err != nil {
			t.Fatalf("reserve retry: %v", err)
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, now, policy)
	})

	t.Run("lost create response reconciles after expiry and reassignment", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t)
		lossy := &createResponseLossRepository{AttemptRepository: attempts}
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: lossy}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", now, policy)
		if _, err := reconciler.reserveAndCreate(ctx, req, create); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost create response error = %v", err)
		}
		reconciler.attempts = attempts
		retry := req
		retry.Now = now.Add(2 * time.Minute)
		if _, err := reconciler.reserveAndCreate(ctx, retry, create); err != nil {
			t.Fatalf("post-expiry retry: %v", err)
		}
		reservation, found, err := ledger.Get(ctx, req.ID)
		if err != nil || !found || reservation.Fence.Generation != 0 || reservation.Charge != learning.AutomaticChargeRetained {
			t.Fatalf("reconciled reservation = %+v, found=%v err=%v", reservation, found, err)
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, now.Add(2*time.Minute), policy)
	})

	t.Run("concurrent retries converge without duplicate attempts", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t)
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", now, policy)
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
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, now, policy)
	})

	t.Run("expired pre-create reservation is reclaimed", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t)
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", now, policy)
		reservation, err := ledger.Reserve(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		resolved, err := reconciler.reconcile(ctx, reservation.ID, now.Add(2*time.Minute), nil)
		if err != nil || resolved.Charge != learning.AutomaticChargeReclaimed {
			t.Fatalf("reclaim = %+v, err=%v", resolved, err)
		}
		replacementPartition, replacement := automaticAttemptFixture(t, "replacement")
		replacementReq := automaticRequestForAttempt(t, replacement.ID, replacementPartition, "digest-replacement", now.Add(2*time.Minute), policy)
		if _, err := ledger.Reserve(ctx, replacementReq); err != nil {
			t.Fatalf("replacement after reclaim: %v", err)
		}
	})

	t.Run("abandoned created attempt retains charge", func(t *testing.T) {
		ledger, attempts := automaticRepositories(t)
		reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
		req := automaticRequestForAttempt(t, create.ID, partition, "digest-primary", now, policy)
		record, err := reconciler.reserveAndCreate(ctx, req, create)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = attempts.Abandon(ctx, partition, record.ID, record.Version, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		resolved, err := reconciler.reconcile(ctx, req.ID, now.Add(2*time.Minute), &create)
		if err != nil || resolved.Charge != learning.AutomaticChargeRetained {
			t.Fatalf("abandoned reconciliation = %+v, err=%v", resolved, err)
		}
		assertOneAttemptAndNoReplacement(ctx, t, attempts, ledger, partition, create.ID, now.Add(2*time.Minute), policy)
	})
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

func automaticRepositories(t *testing.T) (learning.AutomaticAdmissionLedger, learning.AttemptRepository) {
	t.Helper()
	root := t.TempDir()
	ledger, err := automaticstore.New(root + "/ledger")
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := attemptstore.New(root + "/attempts")
	if err != nil {
		t.Fatal(err)
	}
	return ledger, attempts
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

func automaticRequestForAttempt(t *testing.T, id learning.AttemptID, partition learning.AttemptPartition, digest string, now time.Time, policy learning.AutomaticAdmissionPolicy) learning.AutomaticReservationRequest {
	t.Helper()
	reservationID, err := learning.AutomaticReservationIDForAttempt(id)
	if err != nil {
		t.Fatal(err)
	}
	return learning.AutomaticReservationRequest{ID: reservationID, AttemptID: id, Principal: partition, Digest: learning.CanonicalDigest(testDigest(digest)), Class: learning.AdmissionWeighted, Tokens: 10, Now: now, Policy: policy}
}

func assertOneAttemptAndNoReplacement(ctx context.Context, t *testing.T, attempts learning.AttemptRepository, ledger learning.AutomaticAdmissionLedger, partition learning.AttemptPartition, id learning.AttemptID, now time.Time, policy learning.AutomaticAdmissionPolicy) {
	t.Helper()
	page, err := attempts.List(ctx, partition, learning.AttemptList{})
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != id {
		t.Fatalf("attempts = %+v, err=%v; want one %q", page.Records, err, id)
	}
	replacementPartition, replacement := automaticAttemptFixture(t, "replacement")
	_, err = ledger.Reserve(ctx, automaticRequestForAttempt(t, replacement.ID, replacementPartition, "digest-replacement", now, policy))
	if !errors.Is(err, learning.ErrAutomaticAdmissionLimit) {
		t.Fatalf("replacement reserve error = %v, want global limit", err)
	}
}

func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
