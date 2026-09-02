package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

const (
	maxAutomaticReconcileAttempts     = 8
	defaultAutomaticReconcileInterval = time.Second
)

type automaticReservationReconciliationLoop struct {
	cancel context.CancelFunc
	done   sync.WaitGroup
}

func newAutomaticReservationReconciliationLoop(parent context.Context, ledger learning.AutomaticAdmissionLedger, attempts learning.AttemptRepository, interval time.Duration, reportError func(error)) *automaticReservationReconciliationLoop {
	if ledger == nil || attempts == nil {
		return nil
	}
	if interval <= 0 {
		interval = defaultAutomaticReconcileInterval
	}
	ctx, cancel := context.WithCancel(parent)
	loop := &automaticReservationReconciliationLoop{cancel: cancel}
	loop.done.Add(1)
	go func() {
		defer loop.done.Done()
		timer := time.NewTimer(0)
		defer timer.Stop()
		failed := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			reservations, err := ledger.DiscoverExpired(ctx, learning.MaxAutomaticReservationDiscoveryBatch)
			if err == nil {
				reconciler := automaticReservationReconciler{ledger: ledger, attempts: attempts}
				for _, reservation := range reservations {
					if ctx.Err() != nil {
						return
					}
					if _, reconcileErr := reconciler.reconcileReservation(ctx, reservation, nil); reconcileErr != nil && !errors.Is(reconcileErr, context.Canceled) {
						err = reconcileErr
					}
				}
			}
			if err != nil && !failed && reportError != nil && !errors.Is(err, context.Canceled) {
				reportError(err)
			}
			failed = err != nil
			if err == nil && len(reservations) == int(learning.MaxAutomaticReservationDiscoveryBatch) {
				timer.Reset(0)
			} else {
				timer.Reset(interval)
			}
		}
	}()
	return loop
}

func (l *automaticReservationReconciliationLoop) Close() {
	if l == nil {
		return
	}
	l.cancel()
	l.done.Wait()
}

// automaticReservationReconciler closes the non-transactional reservation and
// attempt-create boundary. Both records use the deterministic attempt identity,
// so a retry can inspect either side and converge without adding a charge or an
// attempt.
type automaticReservationReconciler struct {
	ledger   learning.AutomaticAdmissionLedger
	attempts learning.AttemptRepository
}

func (r automaticReservationReconciler) reserveAndCreate(ctx context.Context, req learning.AutomaticReservationRequest, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	if r.ledger == nil || r.attempts == nil || req.AttemptID != create.ID || req.Principal == "" || create.Validate() != nil {
		return learning.AttemptRecord{}, learning.ErrInvalidAutomaticReservation
	}
	reservation, err := r.ledger.Reserve(ctx, req)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if _, err = r.reconcileReservation(ctx, reservation, &create); err != nil {
		return learning.AttemptRecord{}, err
	}
	record, found, err := r.attempts.Get(ctx, req.Principal, create.ID)
	if err != nil {
		return learning.AttemptRecord{}, err
	}
	if !found {
		return learning.AttemptRecord{}, fmt.Errorf("%w: reconciled attempt", learning.ErrAttemptNotFound)
	}
	return record, nil
}

// reconcile resolves an uncertain reservation after timeout, expiry, or worker
// replacement. A nil create means the caller has abandoned work before attempt
// creation: an existing attempt still retains the charge; an absent attempt
// reclaims it.
func (r automaticReservationReconciler) reconcile(ctx context.Context, id learning.AutomaticReservationID, create *learning.AttemptCreate) (learning.AutomaticReservation, error) {
	if r.ledger == nil || r.attempts == nil || id == "" {
		return learning.AutomaticReservation{}, learning.ErrInvalidAutomaticReservation
	}
	reservation, found, err := r.ledger.Get(ctx, id)
	if err != nil {
		return learning.AutomaticReservation{}, err
	}
	if !found {
		return learning.AutomaticReservation{}, learning.ErrAutomaticReservationNotFound
	}
	return r.reconcileReservation(ctx, reservation, create)
}

//nolint:gocyclo // reservation and attempt states stay visibly ordered for crash reconciliation
func (r automaticReservationReconciler) reconcileReservation(ctx context.Context, reservation learning.AutomaticReservation, create *learning.AttemptCreate) (learning.AutomaticReservation, error) {
	if err := reservation.Validate(); err != nil || create != nil && (create.ID != reservation.AttemptID || create.Validate() != nil) {
		return learning.AutomaticReservation{}, learning.ErrInvalidAutomaticReservation
	}
	for range maxAutomaticReconcileAttempts {
		_, found, err := r.attempts.Get(ctx, reservation.Principal, reservation.AttemptID)
		if err != nil {
			return learning.AutomaticReservation{}, err
		}
		switch reservation.Charge {
		case learning.AutomaticChargeRetained:
			if !found {
				return learning.AutomaticReservation{}, learning.ErrAutomaticReservationState
			}
			return reservation, nil
		case learning.AutomaticChargeReclaimed:
			return learning.AutomaticReservation{}, learning.ErrAutomaticReservationState
		case learning.AutomaticChargeHeld:
		default:
			return learning.AutomaticReservation{}, learning.ErrInvalidAutomaticReservation
		}

		var updated learning.AutomaticReservation
		if found {
			updated, err = r.ledger.Retain(ctx, reservation.ID, reservation.Version, reservation.Fence)
		} else if create == nil {
			updated, err = r.ledger.Reclaim(ctx, reservation.ID, reservation.Version, reservation.Fence)
		} else {
			_, err = r.attempts.Create(ctx, reservation.Principal, *create)
			if err == nil {
				continue
			}
		}
		if err == nil {
			return updated, nil
		}
		if errors.Is(err, learning.ErrAutomaticReservationFence) {
			updated, err = r.ledger.Reassign(ctx, reservation.ID, reservation.Version)
			if err == nil {
				reservation = updated
				continue
			}
		}
		if !errors.Is(err, learning.ErrAutomaticReservationVersion) && !errors.Is(err, learning.ErrAutomaticReservationFence) {
			return learning.AutomaticReservation{}, err
		}
		reservation, err = r.reload(ctx, reservation.ID)
		if err != nil {
			return learning.AutomaticReservation{}, err
		}
	}
	return learning.AutomaticReservation{}, learning.ErrAutomaticReservationVersion
}

func (r automaticReservationReconciler) reload(ctx context.Context, id learning.AutomaticReservationID) (learning.AutomaticReservation, error) {
	reservation, found, err := r.ledger.Get(ctx, id)
	if err != nil {
		return learning.AutomaticReservation{}, err
	}
	if !found {
		return learning.AutomaticReservation{}, learning.ErrAutomaticReservationNotFound
	}
	return reservation, nil
}
