package microvm

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrEnvironmentUnavailable means the durable generation exists but its exact
	// runtime cannot be reattached. Callers must not provision a replacement.
	ErrEnvironmentUnavailable = errors.New("microvm environment generation is not live; inspect microvmd reconciliation state or delete the session environment")
	// ErrRuntimeIdentityMismatch rejects PID reuse, stale endpoints, and resources
	// belonging to another environment generation.
	ErrRuntimeIdentityMismatch = errors.New("microvm runtime identity does not match the durable environment generation")
)

// DeleteReason records why the durable tombstone was requested.
type DeleteReason string

const (
	// DeleteExplicit is a caller-requested permanent deletion.
	DeleteExplicit DeleteReason = "explicit"
	// DeleteRetention is an automatic retention-policy destruction.
	DeleteRetention DeleteReason = "retention"
	// DeleteRollback cleans a generation whose creation or runtime failed.
	DeleteRollback DeleteReason = "rollback"
)

// RuntimeStatus is the generation-fenced identity observed from the runtime.
// ProcessIdentity is an OS start token in addition to PID, preventing PID reuse
// from being mistaken for the recorded runner.
type RuntimeStatus struct {
	Live            bool
	Generation      uint32
	VMID            string
	PID             int
	ProcessIdentity string
	Endpoint        string
}

// RuntimeReattacher verifies and reconnects the exact live generation. It must
// never call the runtime create path.
type RuntimeReattacher interface {
	Reattach(context.Context, EnvironmentRecord) error
}

// LifecycleRuntime owns process handles, VM/endpoints, and descendant processes.
// Destroy is idempotent and must generation-check before signaling or unlinking.
type LifecycleRuntime interface {
	RuntimeReattacher
	Inspect(context.Context, EnvironmentRecord) (RuntimeStatus, error)
	Detach(context.Context, EnvironmentRecord) error
	Destroy(context.Context, EnvironmentRecord) error
}

// ReconcileRegistry is the durable registry surface required by lifecycle repair.
type ReconcileRegistry interface {
	EnvironmentRegistry
	EnvironmentRegistryReader
	List(context.Context) ([]EnvironmentRecord, error)
}

// WorktreeRetention applies the dirty-state retention policy. Cleanup must be
// idempotent and confined to the exact paths in the durable record.
type WorktreeRetention interface {
	Dirty(context.Context, EnvironmentRecord) (bool, error)
	Cleanup(context.Context, EnvironmentRecord) error
}

// EnvironmentManager separates process-local detach from durable destruction.
type EnvironmentManager struct {
	registry  ReconcileRegistry
	runtime   LifecycleRuntime
	worktrees WorktreeRetention
	admission *AdmissionController
	observer  *OperationsObserver
}

// NewEnvironmentManager constructs the detach/delete lifecycle coordinator.
func NewEnvironmentManager(registry ReconcileRegistry, runtime LifecycleRuntime, worktrees WorktreeRetention) *EnvironmentManager {
	return NewEnvironmentManagerWithAdmission(registry, runtime, worktrees, nil)
}

// NewEnvironmentManagerWithAdmission additionally releases durable reservations
// after destruction commits.
func NewEnvironmentManagerWithAdmission(registry ReconcileRegistry, runtime LifecycleRuntime, worktrees WorktreeRetention, admission *AdmissionController, observers ...*OperationsObserver) *EnvironmentManager {
	var observer *OperationsObserver
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &EnvironmentManager{registry: registry, runtime: runtime, worktrees: worktrees, admission: admission, observer: observer}
}

// Detach drops only process-local handles. The ready durable generation remains
// resolvable after a harness restart.
func (m *EnvironmentManager) Detach(ctx context.Context, ref EnvironmentRef, owner string) error {
	record, err := m.resolveRecord(ctx, ref, owner)
	if err != nil {
		return err
	}
	if err := m.runtime.Reattach(ctx, record); err != nil {
		return fmt.Errorf("reattach exact microvm generation before detach: %w", err)
	}
	if err := m.runtime.Detach(ctx, record); err != nil {
		return fmt.Errorf("detach microvm environment handles: %w", err)
	}
	return nil
}

// Delete writes a tombstone before destroying resources. Dirty worktrees are
// preserved; clean worktrees and metadata are removed. Retention uses this same path.
func (m *EnvironmentManager) Delete(ctx context.Context, ref EnvironmentRef, owner string, reason DeleteReason) error {
	if reason != DeleteExplicit && reason != DeleteRetention && reason != DeleteRollback {
		return errors.New("invalid microvm deletion reason")
	}
	record, err := m.resolveRecord(ctx, ref, owner)
	if err != nil {
		return err
	}
	dirty, err := m.worktrees.Dirty(ctx, record)
	if err != nil {
		return fmt.Errorf("inspect microvm worktree before deletion: %w", err)
	}
	record.State = EnvironmentDeleting
	record.Tombstone = true
	record.DeleteReason = reason
	record.PreserveWorktree = dirty
	if dirty {
		record.WorktreeDeleted = true // lifecycle complete by policy: deliberately retained
	}
	if err := m.registry.Save(ctx, record); err != nil {
		return fmt.Errorf("persist microvm deletion tombstone: %w", err)
	}
	return NewReconcilerWithAdmission(m.registry, m.runtime, m.worktrees, m.admission, m.observer).reconcileRecord(ctx, record)
}

// DeleteChild durably destroys a delegated child worktree even when it is dirty.
// A merge conflict remains inspectable because its caller deliberately retains the
// cleanup capability; invoking cleanup is the explicit release boundary.
func (m *EnvironmentManager) DeleteChild(ctx context.Context, ref EnvironmentRef, owner string) error {
	record, err := m.resolveRecord(ctx, ref, owner)
	if err != nil {
		return err
	}
	if record.ParentRef == (EnvironmentRef{}) || record.ForkBase == "" {
		return ErrInvalidFork
	}
	record.State = EnvironmentDeleting
	record.Tombstone = true
	record.DeleteReason = DeleteExplicit
	record.PreserveWorktree = false
	if err := m.registry.Save(ctx, record); err != nil {
		return fmt.Errorf("persist microvm child deletion tombstone: %w", err)
	}
	return NewReconcilerWithAdmission(m.registry, m.runtime, m.worktrees, m.admission, m.observer).reconcileRecord(ctx, record)
}

func (m *EnvironmentManager) resolveRecord(ctx context.Context, ref EnvironmentRef, owner string) (EnvironmentRecord, error) {
	if m == nil || m.registry == nil || m.runtime == nil || m.worktrees == nil {
		return EnvironmentRecord{}, errors.New("microvm environment lifecycle is not configured")
	}
	environmentID, generation, err := parseEnvironmentRef(ref)
	if err != nil || owner == "" {
		return EnvironmentRecord{}, ErrInvalidEnvironmentRef
	}
	record, err := m.registry.Lookup(ctx, environmentID)
	if err != nil {
		return EnvironmentRecord{}, err
	}
	if record.Owner != owner {
		return EnvironmentRecord{}, ErrEnvironmentForeign
	}
	if record.State == EnvironmentDestroyed || record.Tombstone {
		return EnvironmentRecord{}, ErrEnvironmentDestroyed
	}
	if record.Ref != ref || record.Generation != generation {
		return EnvironmentRecord{}, ErrEnvironmentStale
	}
	return record, nil
}

// Reconciler converges durable crash states without provisioning replacements.
type Reconciler struct {
	registry  ReconcileRegistry
	runtime   LifecycleRuntime
	worktrees WorktreeRetention
	admission *AdmissionController
	observer  *OperationsObserver
}

// NewReconciler constructs a durable lifecycle reconciler.
func NewReconciler(registry ReconcileRegistry, runtime LifecycleRuntime, worktrees WorktreeRetention) *Reconciler {
	return NewReconcilerWithAdmission(registry, runtime, worktrees, nil)
}

// NewReconcilerWithAdmission additionally restores destruction-time quota release.
func NewReconcilerWithAdmission(registry ReconcileRegistry, runtime LifecycleRuntime, worktrees WorktreeRetention, admission *AdmissionController, observers ...*OperationsObserver) *Reconciler {
	var observer *OperationsObserver
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &Reconciler{registry: registry, runtime: runtime, worktrees: worktrees, admission: admission, observer: observer}
}

// Reconcile inspects every durable record. Independent record failures are joined
// so one damaged environment cannot prevent cleanup of the rest.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	if r == nil || r.registry == nil || r.runtime == nil || r.worktrees == nil {
		return errors.New("microvm reconciler is not configured")
	}
	records, err := r.registry.List(ctx)
	if err != nil {
		return fmt.Errorf("list microvm reconciliation records: %w", err)
	}
	var result error
	for _, record := range records {
		if err := r.reconcileRecord(ctx, record); err != nil {
			result = errors.Join(result, fmt.Errorf("reconcile microvm %s@%d: %w", record.EnvironmentID, record.Generation, err))
		}
	}
	return result
}

func (r *Reconciler) reconcileRecord(ctx context.Context, record EnvironmentRecord) error {
	cleanup := record.State == EnvironmentProvisioning || record.State == EnvironmentCleanupPending || record.State == EnvironmentDeleting
	err := r.reconcileRecordRaw(ctx, record)
	if !cleanup && record.State == EnvironmentReady {
		if latest, lookupErr := r.registry.Lookup(context.WithoutCancel(ctx), record.EnvironmentID); lookupErr == nil {
			cleanup = latest.State == EnvironmentDestroyed || (latest.State == EnvironmentCleanupPending && latest.Tombstone)
		}
	}
	if cleanup && r.observer != nil {
		outcome := OutcomeSuccess
		if err != nil {
			outcome = OutcomeFailure
		}
		r.observer.CleanupFinished(outcome)
	}
	return err
}

func (r *Reconciler) reconcileRecordRaw(ctx context.Context, record EnvironmentRecord) error {
	latest, err := r.registry.Lookup(ctx, record.EnvironmentID)
	if err != nil {
		return err
	}
	if latest.Generation != record.Generation || latest.Ref != record.Ref {
		return ErrEnvironmentStale
	}
	record, cleanup, err := r.prepareForCleanup(ctx, latest)
	if err != nil || !cleanup {
		return err
	}
	record, err = r.cleanupRuntime(ctx, record)
	if err != nil {
		return err
	}
	record, err = r.cleanupWorktree(ctx, record)
	if err != nil {
		return err
	}
	record.State = EnvironmentDestroyed
	record.Tombstone = true
	if record.DeleteReason == "" {
		record.DeleteReason = DeleteRollback
	}
	if err := r.registry.Save(context.WithoutCancel(ctx), record); err != nil {
		return fmt.Errorf("persist destroyed microvm tombstone: %w", err)
	}
	if r.admission != nil {
		r.admission.Release(record.Owner, record.AdmissionUsage)
	}
	return nil
}

func (r *Reconciler) prepareForCleanup(ctx context.Context, record EnvironmentRecord) (EnvironmentRecord, bool, error) {
	switch record.State {
	case EnvironmentDestroyed:
		return record, false, nil
	case EnvironmentProvisioning, EnvironmentCleanupPending, EnvironmentDeleting:
		return record, true, nil
	case EnvironmentReady:
		status, err := r.runtime.Inspect(ctx, record)
		if errors.Is(err, ErrEnvironmentUnavailable) {
			return r.markUnavailableForCleanup(ctx, record, err)
		}
		if err != nil {
			return record, false, fmt.Errorf("inspect ready microvm runtime: %w", err)
		}
		identityErr := validateRuntimeIdentity(record, status)
		if identityErr == nil {
			attachErr := r.runtime.Reattach(ctx, record)
			if errors.Is(attachErr, ErrEnvironmentUnavailable) {
				return r.markUnavailableForCleanup(ctx, record, attachErr)
			}
			return record, false, attachErr
		}
		record.State = EnvironmentCleanupPending
		record.Tombstone = true
		record.DeleteReason = DeleteRollback
		if saveErr := r.registry.Save(ctx, record); saveErr != nil {
			return record, false, errors.Join(identityErr, saveErr)
		}
		if errors.Is(identityErr, ErrEnvironmentUnavailable) {
			return record, true, nil
		}
		return record, false, identityErr
	default:
		return record, false, fmt.Errorf("unknown microvm lifecycle state %q", record.State)
	}
}

func (r *Reconciler) markUnavailableForCleanup(ctx context.Context, record EnvironmentRecord, unavailable error) (EnvironmentRecord, bool, error) {
	if record.RunnerPID <= 0 || record.ProcessIdentity == "" || record.Endpoint == "" || record.VMID == "" {
		return record, false, errors.Join(ErrRuntimeIdentityMismatch, unavailable)
	}
	record.State = EnvironmentCleanupPending
	record.Tombstone = true
	record.DeleteReason = DeleteRollback
	if err := r.registry.Save(ctx, record); err != nil {
		return record, false, errors.Join(unavailable, err)
	}
	return record, true, nil
}

func (r *Reconciler) cleanupRuntime(ctx context.Context, record EnvironmentRecord) (EnvironmentRecord, error) {
	if record.VMDeleted {
		return record, nil
	}
	status, err := r.runtime.Inspect(ctx, record)
	if errors.Is(err, ErrEnvironmentUnavailable) {
		// Destroy is required to identity-check the persisted PID/start token before
		// signaling or unlinking. A backend unable to prove that identity must fail.
		if err := r.runtime.Destroy(context.WithoutCancel(ctx), record); err != nil {
			return record, fmt.Errorf("destroy unavailable microvm by durable identity: %w", err)
		}
		record.VMDeleted = true
		if err := r.registry.Save(context.WithoutCancel(ctx), record); err != nil {
			return record, fmt.Errorf("persist microvm runtime cleanup: %w", err)
		}
		return record, nil
	}
	if err != nil {
		return record, fmt.Errorf("inspect microvm before cleanup: %w", err)
	}
	if status.Live {
		if err := validateRuntimeIdentity(record, status); err != nil {
			record.State = EnvironmentCleanupPending
			record.Tombstone = true
			return record, errors.Join(err, r.registry.Save(ctx, record))
		}
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if err := r.runtime.Destroy(cleanupCtx, record); err != nil {
		record.State = EnvironmentCleanupPending
		record.Tombstone = true
		return record, errors.Join(err, r.registry.Save(cleanupCtx, record))
	}
	record.VMDeleted = true
	if err := r.registry.Save(cleanupCtx, record); err != nil {
		return record, fmt.Errorf("persist microvm runtime cleanup: %w", err)
	}
	return record, nil
}

func (r *Reconciler) cleanupWorktree(ctx context.Context, record EnvironmentRecord) (EnvironmentRecord, error) {
	if record.WorktreeDeleted {
		return record, nil
	}
	dirty, err := r.worktrees.Dirty(ctx, record)
	if err != nil {
		return record, fmt.Errorf("inspect microvm worktree during cleanup: %w", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	if dirty {
		record.PreserveWorktree = true
	} else if err := r.worktrees.Cleanup(cleanupCtx, record); err != nil {
		record.State = EnvironmentCleanupPending
		record.Tombstone = true
		return record, errors.Join(err, r.registry.Save(cleanupCtx, record))
	}
	record.WorktreeDeleted = true
	if err := r.registry.Save(cleanupCtx, record); err != nil {
		return record, fmt.Errorf("persist microvm worktree cleanup: %w", err)
	}
	return record, nil
}

func validateRuntimeIdentity(record EnvironmentRecord, status RuntimeStatus) error {
	if !status.Live {
		return ErrEnvironmentUnavailable
	}
	if status.Generation != record.Generation || status.VMID != record.VMID || status.Endpoint != record.Endpoint || status.PID != record.RunnerPID || status.ProcessIdentity == "" || status.ProcessIdentity != record.ProcessIdentity {
		return ErrRuntimeIdentityMismatch
	}
	return nil
}
