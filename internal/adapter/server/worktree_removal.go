package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Stop-and-delete plus server-created worktree removal (ADR 0374 Decision 4).

// defaultStopActiveTimeout bounds how long DeleteSession{stop_active} waits for
// a cancelled live run to settle before failing with nothing deleted.
const defaultStopActiveTimeout = 10 * time.Second

// Retained-worktree reasons reported by DeleteSessionResult when the session
// was deleted but its worktree was kept.
const (
	WorktreeRetainedDirty        = "dirty"
	WorktreeRetainedShared       = "shared"
	WorktreeRetainedRemoveFailed = "remove_failed"
)

var (
	// ErrWorktreeRemovalRefused is the failed-precondition family returned by
	// DeleteSession{remove_worktree} before anything is stopped or deleted.
	ErrWorktreeRemovalRefused     = fmt.Errorf("%w: worktree cannot be removed", ErrFailedPrecondition)
	errWorktreeRemovalUnavailable = fmt.Errorf("%w: worktree removal is unavailable on this deployment", ErrWorktreeRemovalRefused)
	errWorktreeNotServerCreated   = fmt.Errorf("%w: the session's worktree was not created by the server", ErrWorktreeRemovalRefused)
	errWorktreeShared             = fmt.Errorf("%w: another session or schedule uses the worktree", ErrWorktreeRemovalRefused)
	errWorktreeDirty              = fmt.Errorf("%w: the worktree has uncommitted or untracked changes", ErrWorktreeRemovalRefused)
	errDeleteDelegationChild      = fmt.Errorf("%w: delegation child sessions cannot be stopped by delete", ErrFailedPrecondition)
)

// DeleteSessionOptions are the DeleteSessionRequest knobs beyond the id.
type DeleteSessionOptions struct {
	// StopActive lets deletion cancel a running run or discard a parked ask.
	StopActive bool
	// RemoveWorktree also removes the session's server-created worktree.
	RemoveWorktree bool
}

// DeleteSessionResult reports the worktree outcome of a successful delete.
type DeleteSessionResult struct {
	WorktreeRemoved        bool
	WorktreeRetainedReason string
}

// worktreePathLocks serializes worktree removal against selector binds of the
// same path. It is process-wide because several Services in one process share
// one filesystem. Entries are reference-counted and deleted when unused, so the
// map is bounded by in-flight operations.
var worktreePathLocks = struct {
	mu sync.Mutex
	m  map[string]*worktreePathLock
}{m: map[string]*worktreePathLock{}}

type worktreePathLock struct {
	mu   sync.Mutex
	refs int
}

// worktreePathKey is the symlink-resolved path, falling back to the cleaned
// path once the directory is gone.
func worktreePathKey(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// lockWorktreePath takes the per-path lock for path and returns its release.
func lockWorktreePath(path string) func() {
	key := worktreePathKey(path)
	worktreePathLocks.mu.Lock()
	entry := worktreePathLocks.m[key]
	if entry == nil {
		entry = &worktreePathLock{}
		worktreePathLocks.m[key] = entry
	}
	entry.refs++
	worktreePathLocks.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		worktreePathLocks.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(worktreePathLocks.m, key)
		}
		worktreePathLocks.mu.Unlock()
	}
}

func (s *Service) stopActiveTimeout() time.Duration {
	if s.cfg.StopActiveTimeout > 0 {
		return s.cfg.StopActiveTimeout
	}
	return defaultStopActiveTimeout
}

// DeleteSessionWithOptions is DeleteSession with the ADR 0374 Decision 4
// options. Without options it is exactly DeleteSession. StopActive is admitted
// only after the run-entry lock and the real session lease are held; a live run
// is then cancelled through the same exact-lifecycle mechanism ClearSession uses
// (ADR 0297) and awaited for at most the stop bound. RemoveWorktree follows the
// ADR order: check, stop, re-check, delete, remove.
//
//nolint:gocyclo // Delete keeps lock, lease, stop, re-check, and removal in one ordered transaction.
func (s *Service) DeleteSessionWithOptions(ctx context.Context, id session.SessionID, opts DeleteSessionOptions) (DeleteSessionResult, error) {
	if opts.StopActive && isDelegationChildSessionID(id) {
		return DeleteSessionResult{}, errDeleteDelegationChild
	}
	absent, err := s.managementOwnershipPreflight(ctx, id, true)
	if err != nil || absent {
		return DeleteSessionResult{}, err
	}
	unlock := s.runEntryMu.lock(id)
	defer unlock()
	target := s.managementTargetAwaitingDrain
	if opts.StopActive {
		// Active and awaiting sessions are admitted; the stop below settles them.
		target = s.managementSession
	}
	if _, absent, err = target(ctx, id, true); err != nil || absent {
		return DeleteSessionResult{}, err
	}
	prunable, ok := s.cfg.Store.(port.PrunableStore)
	if !ok {
		return DeleteSessionResult{}, ErrSessionDeleteUnsupported
	}
	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return DeleteSessionResult{}, err
	}
	defer release()
	sess, absent, err := target(ctx, id, true)
	if err != nil || absent {
		return DeleteSessionResult{}, err
	}

	var removal *worktreeRemoval
	if opts.RemoveWorktree {
		removal, err = s.beginWorktreeRemoval(ctx, sess)
		if err != nil {
			return DeleteSessionResult{}, err
		}
		defer removal.unlock()
	}
	if opts.StopActive {
		if err := s.stopForDelete(ctx, id); err != nil {
			return DeleteSessionResult{}, err
		}
		sess, absent, err = s.managementSession(ctx, id, true)
		if err != nil || absent {
			return DeleteSessionResult{}, err
		}
		if s.IsLive(id) {
			return DeleteSessionResult{}, errSessionActiveOrAwaiting
		}
	}

	var result DeleteSessionResult
	if removal != nil {
		result.WorktreeRetainedReason = removal.recheck(ctx)
	}
	if err := s.deleteManagedSessionLocked(ctx, sess, prunable); err != nil {
		return DeleteSessionResult{}, err
	}
	if removal != nil && result.WorktreeRetainedReason == "" {
		if err := removal.remover.RemoveWorktree(ctx, removal.req); err != nil {
			s.logPlacementProviderError(ctx, "remove session worktree", err)
			result.WorktreeRetainedReason = WorktreeRetainedRemoveFailed
		} else {
			result.WorktreeRemoved = true
		}
	}
	return result, nil
}

// stopForDelete cancels the exact registered lifecycle, if any, and waits for
// it to settle within the stop bound. The caller holds runEntryMu and the
// mutation lease. A durable awaiting or crash-orphaned running snapshot has no
// registered lifecycle and needs no stop: deletion discards it.
func (s *Service) stopForDelete(ctx context.Context, id session.SessionID) error {
	stopCtx, cancel := context.WithTimeout(ctx, s.stopActiveTimeout())
	defer cancel()
	if err := s.cancelAndAwaitClearSource(stopCtx, id); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("%w: %q", ErrSessionStopTimeout, id)
		}
		return err
	}
	return nil
}

// worktreeRemoval holds the per-path lock from the first check until removal.
type worktreeRemoval struct {
	s       *Service
	remover PlacementWorktreeRemover
	req     PlacementReattachRequest
	self    session.SessionID
	unlock  func()
}

// beginWorktreeRemoval takes the per-path lock and performs the ADR step-1
// checks: server-created, unshared, clean. Any failure releases the lock and
// returns ErrWorktreeRemovalRefused with nothing changed.
func (s *Service) beginWorktreeRemoval(ctx context.Context, sess *session.Session) (*worktreeRemoval, error) {
	remover, ok := s.cfg.PlacementProvider.(PlacementWorktreeRemover)
	if !ok {
		return nil, errWorktreeRemovalUnavailable
	}
	ref := sess.EnvironmentRef
	if ref.Kind != session.EnvKindLocal || ref.ID == "" {
		return nil, errWorktreeNotServerCreated
	}
	r := &worktreeRemoval{
		s: s, remover: remover, self: sess.ID, unlock: lockWorktreePath(ref.ID),
		req: PlacementReattachRequest{Ref: ref, Principal: sess.Owner.Clone(), Scope: s.cfg.PlacementScope, BindingID: sess.ID},
	}
	refuse := func(err error) (*worktreeRemoval, error) {
		r.unlock()
		return nil, err
	}
	ownership, err := remover.WorktreeOwnership(ctx, r.req)
	if err != nil {
		s.logPlacementProviderError(ctx, "check session worktree ownership", err)
		return refuse(errWorktreeRemovalUnavailable)
	}
	if !ownership.ServerCreated {
		return refuse(errWorktreeNotServerCreated)
	}
	shared, err := s.worktreeSharedByOthers(ctx, sess.ID, ref)
	if err != nil {
		s.logDiscoveryError(ctx, "scan worktree sharing", err)
		return refuse(errWorktreeRemovalUnavailable)
	}
	if shared {
		return refuse(errWorktreeShared)
	}
	if !ownership.Clean {
		return refuse(errWorktreeDirty)
	}
	return r, nil
}

// recheck repeats sharing and cleanliness after the stop (ADR step 3) and
// returns the retained reason, or "" when removal may proceed.
func (r *worktreeRemoval) recheck(ctx context.Context) string {
	shared, err := r.s.worktreeSharedByOthers(ctx, r.self, r.req.Ref)
	if err != nil {
		return WorktreeRetainedRemoveFailed
	}
	if shared {
		return WorktreeRetainedShared
	}
	ownership, err := r.remover.WorktreeOwnership(ctx, r.req)
	if err != nil || !ownership.ServerCreated {
		return WorktreeRetainedRemoveFailed
	}
	if !ownership.Clean {
		return WorktreeRetainedDirty
	}
	return ""
}

// localRefHolders maps each resolved local ref path to the persisted sessions
// (any kind or owner) and schedules that bind it. Cost: one full metadata scan
// of the session store plus one schedule List; each distinct path is resolved
// once. Holders are "session:<id>" and "schedule:<name>".
func (s *Service) localRefHolders(ctx context.Context) (map[string][]string, error) {
	pager, ok := s.cfg.Store.(port.SessionMetadataPager)
	if !ok || !port.SupportsSessionMetadataPaging(s.cfg.Store) {
		return nil, port.ErrSessionMetadataPagingUnsupported
	}
	rows, err := cleanupMetadata(ctx, pager, nil)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	key := func(path string) string {
		if k, ok := keys[path]; ok {
			return k
		}
		k := worktreePathKey(path)
		keys[path] = k
		return k
	}
	holders := map[string][]string{}
	for _, row := range rows {
		if row.EnvironmentRef.Kind == session.EnvKindLocal && row.EnvironmentRef.ID != "" {
			k := key(row.EnvironmentRef.ID)
			holders[k] = append(holders[k], "session:"+string(row.ID))
		}
	}
	if store := s.scheduleStore(); store != nil {
		schedules, err := store.List(ctx)
		if err != nil && !errors.Is(err, port.ErrScheduleUnsupported) {
			return nil, err
		}
		for _, schedule := range schedules {
			ref := schedule.Spec.EnvironmentRef
			if ref.Kind == session.EnvKindLocal && ref.ID != "" {
				k := key(ref.ID)
				holders[k] = append(holders[k], "schedule:"+schedule.Spec.Name)
			}
		}
	}
	return holders, nil
}

func sharedByOthers(holders map[string][]string, self session.SessionID, ref session.EnvironmentRef) bool {
	for _, holder := range holders[worktreePathKey(ref.ID)] {
		if holder != "session:"+string(self) {
			return true
		}
	}
	return false
}

func (s *Service) worktreeSharedByOthers(ctx context.Context, self session.SessionID, ref session.EnvironmentRef) (bool, error) {
	holders, err := s.localRefHolders(ctx)
	if err != nil {
		return false, err
	}
	return sharedByOthers(holders, self, ref), nil
}

// confirmSelectedWorktree runs under the per-path lock after a selector bind of
// a local worktree: the exact ref must still reattach, so a bind that raced a
// removal fails instead of publishing a session on a vanished worktree.
func (s *Service) confirmSelectedWorktree(ctx context.Context, owner *session.Principal, ref session.EnvironmentRef, bindingID session.SessionID) error {
	probe, err := s.placementBinder.Reattach(ctx, PlacementReattachRequest{Ref: ref, Principal: owner, Scope: s.cfg.PlacementScope, BindingID: bindingID})
	if err != nil {
		return fmt.Errorf("%w: worktree is no longer available", ErrPlacementChanged)
	}
	discardPlacementBinding(probe)
	return nil
}

// annotateRemoveWorktree fills capabilities.remove_worktree for one inventory
// page. The sharing scan runs at most once per page, and only when a row is a
// server-created worktree; the ownership check runs per local-ref main row.
func (s *Service) annotateRemoveWorktree(ctx context.Context, rows []SessionSummary, metas []port.SessionDiscoveryMeta) {
	remover, ok := s.cfg.PlacementProvider.(PlacementWorktreeRemover)
	deletable := sessionDeleteSupported(s.cfg.Store)
	var (
		holders map[string][]string
		scanErr error
		scanned bool
	)
	for i := range rows {
		row, meta := &rows[i], metas[i]
		row.Capabilities.RemoveWorktree = false
		row.Reasons.RemoveWorktree = CapabilityReasonUnavailable
		if !ok || !deletable || row.Kind != session.SessionKindMain || hasLegacyNonChatPrefix(meta.ID) {
			continue
		}
		ref := meta.EnvironmentRef
		if ref.Kind != session.EnvKindLocal || ref.ID == "" {
			row.Reasons.RemoveWorktree = CapabilityReasonNotServerCreated
			continue
		}
		ownership, err := remover.WorktreeOwnership(ctx, PlacementReattachRequest{Ref: ref, Principal: meta.Owner.Clone(), Scope: s.cfg.PlacementScope, BindingID: meta.ID})
		if err != nil {
			continue
		}
		if !ownership.ServerCreated {
			row.Reasons.RemoveWorktree = CapabilityReasonNotServerCreated
			continue
		}
		if !scanned {
			holders, scanErr = s.localRefHolders(ctx)
			scanned = true
		}
		if scanErr != nil {
			continue
		}
		if sharedByOthers(holders, meta.ID, ref) {
			row.Reasons.RemoveWorktree = CapabilityReasonShared
			continue
		}
		row.Capabilities.RemoveWorktree = true
		row.Reasons.RemoveWorktree = ""
	}
}
