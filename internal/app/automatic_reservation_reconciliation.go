package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/learning"
)

const maxAutomaticReconcileAttempts = 8

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
