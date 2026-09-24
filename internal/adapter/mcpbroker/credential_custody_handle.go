package mcpbroker

import (
	"context"
	"fmt"
	"time"

	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// StageCredentialCustody is the production handle-bound custody capability. The
// verified ToolHive session identity is request-local evidence captured by the
// real incoming middleware during catalogue discovery; callers cannot supply it.
func (a *SessionHandle) StageCredentialCustody(ctx context.Context, requestID string, guard contract.ContinuityGuard, enrollment contract.WorkspaceEnrollmentRef, deadline time.Time) (contract.StagedCredentialCustody, error) {
	if a == nil || requestID == "" || len(requestID) > 256 || !contract.ValidLogicalSessionID(guard.SessionID) || !enrollment.Valid() {
		return contract.StagedCredentialCustody{}, fmt.Errorf("%w: credential custody unavailable", contract.ErrContinuityUnavailable)
	}
	if guard.ProfileDigest != ([32]byte{}) || len(guard.Providers) != 0 {
		return contract.StagedCredentialCustody{}, fmt.Errorf("%w: credential custody unavailable", contract.ErrContinuityUnavailable)
	}
	operationCtx, finish, err := a.beginOperation(ctx)
	if err != nil {
		return contract.StagedCredentialCustody{}, fmt.Errorf("%w: credential custody unavailable", contract.ErrContinuityUnavailable)
	}
	defer finish()
	a.mu.RLock()
	if a.closed || guard.SessionID != a.logical.ref.id || a.verifiedTSID == "" || a.catalogue == nil || a.catalogue.frozen == nil || a.catalogue.frozen.Ref() != enrollment {
		a.mu.RUnlock()
		return contract.StagedCredentialCustody{}, fmt.Errorf("%w: credential custody unavailable", contract.ErrContinuityUnavailable)
	}
	verifiedTSID := a.verifiedTSID
	process := a.runtime.process
	a.mu.RUnlock()
	if process == nil || process.custody == nil || process.custody.clock == nil || !contract.ValidContinuityAttemptDeadline(process.custody.clock.Now(), deadline) {
		return contract.StagedCredentialCustody{}, fmt.Errorf("%w: credential custody unavailable", contract.ErrContinuityUnavailable)
	}
	profileDigest, providers, err := process.continuityProfile()
	if err != nil {
		return contract.StagedCredentialCustody{}, fmt.Errorf("%w: credential custody unavailable", contract.ErrContinuityUnavailable)
	}
	request := custodyRequest{
		Guard: custodyGuard{
			SessionID: guard.SessionID, Incarnation: guard.SessionIncarnation,
			OwnerPartition: guard.OwnerPartition, WorkloadPartition: guard.WorkloadPartition,
			ProfileDigest: profileDigest, Providers: providers,
		},
		AttemptDeadline: deadline,
	}
	staged, err := process.custody.Stage(operationCtx, request, verifiedTSID)
	if err != nil {
		return contract.StagedCredentialCustody{}, continuityCustodyError(ctx, err)
	}
	return contract.StagedCredentialCustody{RecoveryReference: string(staged.Recovery), ExpiresAt: staged.ExpiresAt, ProfileDigest: profileDigest, Providers: append([]string(nil), providers...)}, nil
}

// CredentialContinuity reports whether this handle's process has encrypted
// custody configured, so the host knows custody is required for enrollment.
func (a *SessionHandle) CredentialContinuity() bool {
	if a == nil || a.runtime == nil {
		return false
	}
	process := a.runtime.process
	if process == nil {
		return false
	}
	process.lifecycleMu.Lock()
	defer process.lifecycleMu.Unlock()
	return !process.closed && process.custody != nil && len(process.providers) != 0
}

var (
	_ contract.CredentialCustodyStager        = (*SessionHandle)(nil)
	_ contract.CredentialContinuityAdvertiser = (*SessionHandle)(nil)
)
