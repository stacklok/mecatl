package server

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp/source"
)

const (
	maxMCPRefreshGrantNames      = 1_024
	maxMCPHistoricalGrantedNames = 16_384
	mcpRefreshSaveTimeout        = 5 * time.Second
	mcpRefreshConfirmTimeout     = 5 * time.Second
)

// MCPRefreshSnapshot is the immutable direct-runtime snapshot considered by one
// explicit refresh request. ToolNames are active direct names only.
type MCPRefreshSnapshot struct {
	Revision  uint64
	Changed   bool
	ToolNames []string
}

// MCPSourceStatus is the reconciler's cached public status. Sources are the
// published/pre-shadow inventory and are never populated by a status-time probe.
type MCPSourceStatus struct {
	Sources     []source.SourceInfo
	Revision    uint64
	Stale       bool
	Reconciling bool
}

// MCPRefreshResult reports the exact runtime revision considered by this
// request and whether that cycle published a runtime or added authority.
type MCPRefreshResult struct {
	Revision uint64
	Changed  bool
}

// RefreshMcpSources reconciles direct MCP sources and stable-unions missing
// active direct names into one eligible owner-controlled ordinary root.
//
//nolint:gocyclo // the ordered ownership, lifecycle, lease, cancellation, save, and confirmation gates are the contract.
func (s *Service) RefreshMcpSources(ctx context.Context, id session.SessionID) (MCPRefreshResult, error) {
	if s.cfg.MCPRefresh == nil {
		return MCPRefreshResult{}, fmt.Errorf("%w: direct MCP refresh is unavailable", ErrFailedPrecondition)
	}
	absent, err := s.managementOwnershipPreflight(ctx, id, false)
	if err != nil {
		return MCPRefreshResult{}, err
	}
	if absent {
		return MCPRefreshResult{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}

	unlock := s.runEntryMu.lock(id)
	defer unlock()
	current, err := s.mcpRefreshTarget(ctx, id)
	if err != nil {
		return MCPRefreshResult{}, err
	}

	snapshot, reconcileErr := s.cfg.MCPRefresh(ctx)
	if reconcileErr != nil {
		if errors.Is(reconcileErr, ErrInternal) {
			s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "direct MCP source reconciliation failed", "session", string(id), "err", reconcileErr.Error())
			return MCPRefreshResult{}, fmt.Errorf("%w: reconcile direct MCP sources", ErrInternal)
		}
		return MCPRefreshResult{}, reconcileErr
	}
	result := MCPRefreshResult{Revision: snapshot.Revision, Changed: snapshot.Changed}
	if len(snapshot.ToolNames) > maxMCPRefreshGrantNames {
		return MCPRefreshResult{}, fmt.Errorf("%w: direct MCP grant count exceeds limit", ErrFailedPrecondition)
	}
	names := append([]string(nil), snapshot.ToolNames...)

	additions, err := mcpRefreshAdditions(current, names)
	if err != nil {
		return MCPRefreshResult{}, err
	}
	if len(additions) == 0 {
		return result, nil
	}

	release, err := s.acquireMutationLease(ctx, id)
	if err != nil {
		return MCPRefreshResult{}, err
	}
	defer release()

	old, err := s.mcpRefreshTarget(ctx, id)
	if err != nil {
		return MCPRefreshResult{}, err
	}
	additions, err = mcpRefreshAdditions(old, names)
	if err != nil {
		return MCPRefreshResult{}, err
	}
	if len(additions) == 0 {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return MCPRefreshResult{}, err
	}

	oldSnapshot, err := sessnap.Of(old)
	if err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "snapshot direct MCP authority candidate failed", "session", string(id), "err", err.Error())
		return MCPRefreshResult{}, fmt.Errorf("%w: snapshot direct MCP authority candidate", ErrInternal)
	}
	candidate, err := oldSnapshot.Restore()
	if err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "restore direct MCP authority candidate failed", "session", string(id), "err", err.Error())
		return MCPRefreshResult{}, fmt.Errorf("%w: restore direct MCP authority candidate", ErrInternal)
	}
	if err := candidate.GrantToolAuthority(additions); err != nil {
		return MCPRefreshResult{}, fmt.Errorf("%w: grant direct MCP authority: %v", ErrFailedPrecondition, err)
	}
	result.Changed = true

	// The final caller-cancellation check above is the save-start boundary. Once
	// Save begins, it and any ambiguous-commit confirmation are detached and
	// bounded while the same run-entry and mutation exclusions remain held.
	saveParent, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), mcpRefreshSaveTimeout)
	saveCtx, stopSave, leaseHeld := s.mutationLeaseContext(saveParent, id)
	saveErr := s.saveSession(saveCtx, candidate)
	stopSave()
	saveCancel()
	if !leaseHeld() {
		return MCPRefreshResult{}, fmt.Errorf("%w: session lease was lost during MCP refresh", ErrSessionLeasedElsewhere)
	}
	if saveErr == nil {
		if err := ctx.Err(); err != nil {
			return MCPRefreshResult{}, err
		}
		return result, nil
	}

	confirmCtx, confirmCancel := context.WithTimeout(context.WithoutCancel(ctx), mcpRefreshConfirmTimeout)
	confirmed, confirmErr := s.cfg.Store.Load(confirmCtx, id)
	confirmCancel()
	if confirmErr != nil || confirmed == nil || confirmed.ID != id {
		return MCPRefreshResult{}, fmt.Errorf("%w: MCP refresh save outcome is uncertain", ErrUnavailable)
	}
	if reflect.DeepEqual(confirmed, candidate) {
		if err := ctx.Err(); err != nil {
			return MCPRefreshResult{}, err
		}
		return result, nil
	}
	if reflect.DeepEqual(confirmed, old) {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "persist MCP authority failed", "session", string(id), "err", saveErr.Error())
		return MCPRefreshResult{}, fmt.Errorf("%w: persist MCP authority", ErrInternal)
	}
	return MCPRefreshResult{}, fmt.Errorf("%w: MCP refresh save outcome is uncertain", ErrUnavailable)
}

func (s *Service) mcpRefreshTarget(ctx context.Context, id session.SessionID) (*session.Session, error) {
	sess, absent, err := s.managementSession(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if absent {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if sess.State != session.StateIdle && sess.State != session.StateCompleted {
		return nil, fmt.Errorf("%w: session is not idle or completed", ErrFailedPrecondition)
	}
	if s.IsLive(id) {
		return nil, fmt.Errorf("%w: session has a local active run", ErrFailedPrecondition)
	}
	if sess.ExternalBinding != "" {
		return nil, fmt.Errorf("%w: broker-bound sessions use workspace enrollment", ErrFailedPrecondition)
	}
	if _, ok := sess.BoundAuthority(); !ok {
		return nil, fmt.Errorf("%w: session authority is not bound", ErrFailedPrecondition)
	}
	return sess, nil
}

func mcpRefreshAdditions(sess *session.Session, names []string) ([]string, error) {
	authority, bound := sess.BoundAuthority()
	if !bound {
		return nil, fmt.Errorf("%w: session authority is not bound", ErrFailedPrecondition)
	}
	if len(authority.CapabilitySet.Tools) > maxMCPHistoricalGrantedNames {
		return nil, fmt.Errorf("%w: historical tool authority exceeds limit", ErrFailedPrecondition)
	}
	seen := make(map[string]struct{}, len(authority.CapabilitySet.Tools)+len(names))
	for _, name := range authority.CapabilitySet.Tools {
		seen[name] = struct{}{}
	}
	additions := make([]string, 0, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		additions = append(additions, name)
	}
	if len(authority.CapabilitySet.Tools)+len(additions) > maxMCPHistoricalGrantedNames {
		return nil, fmt.Errorf("%w: historical tool authority exceeds limit", ErrFailedPrecondition)
	}
	return additions, nil
}
