package mcpbroker

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// ErrWorkspaceEnrollmentUnsupported reports that this attachment's Runtime has
// no owning Process or no configured protected ToolHive backend.
var ErrWorkspaceEnrollmentUnsupported = errors.New("mcpbroker: workspace enrollment is not required")

var errWorkspaceEnrollmentAlreadyCompleted = errors.New("mcpbroker: workspace enrollment is already completed")

var _ contract.WorkspaceEnrollmentAttachment = (*Attachment)(nil)

// bundleBackends returns the deterministic configured protected backends, the
// shared bundle-wide authorization target, and the owning Process.
func (a *Attachment) bundleBackends() ([]string, *oauthRoute, *Process) {
	process := a.runtime.process
	if process == nil {
		return nil, nil, nil
	}
	process.lifecycleMu.Lock()
	closed := process.closed
	backends := append([]string(nil), process.construction.protectedBackends...)
	target := process.protectedTarget
	process.lifecycleMu.Unlock()
	if closed || len(backends) == 0 || target == nil {
		return nil, nil, nil
	}
	copyTarget := *target
	return backends, &copyTarget, process
}

// existingWorkspaceEnrollmentLocked is the idempotent fast path
// BeginWorkspaceEnrollment takes both BEFORE and AFTER releasing logical.mu
// for secret resolution, so a concurrent caller's result during that window
// is never overwritten. resolved reports whether the caller should return
// immediately with (result, err); resolved == false means a new transaction
// must be created.
func existingWorkspaceEnrollmentLocked(a *Attachment, logical *logicalSession) (result contract.WorkspaceEnrollmentPresentation, resolved bool, err error) {
	if logical.deleted {
		return contract.WorkspaceEnrollmentPresentation{}, true, contract.ErrStateUnavailable
	}
	if logical.completedEnrollment != nil {
		return contract.WorkspaceEnrollmentPresentation{}, true, errWorkspaceEnrollmentAlreadyCompleted
	}
	for _, transaction := range logical.authorizations {
		if transaction.bundleBackends == nil {
			continue
		}
		a.expireLocked(transaction)
		if transaction.status == session.AuthorizationPending {
			url := presentWorkspaceTransaction(transaction)
			return contract.WorkspaceEnrollmentPresentation{Ref: workspaceEnrollmentRef(transaction), URL: url}, true, nil
		}
	}
	if len(logical.authorizations) >= maxAuthorizationRecords {
		return contract.WorkspaceEnrollmentPresentation{}, true, errors.New("mcpbroker: authorization record capacity reached")
	}
	return contract.WorkspaceEnrollmentPresentation{}, false, nil
}

// BeginWorkspaceEnrollment starts, or idempotently re-presents, the one
// bundle-wide pre-prompt ToolHive authorization transaction for every protected
// backend.
func (a *Attachment) BeginWorkspaceEnrollment(ctx context.Context) (contract.WorkspaceEnrollmentPresentation, error) {
	if err := ctx.Err(); err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	opCtx, done, err := a.beginOperation(ctx)
	if err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	defer done()
	backends, target, _ := a.bundleBackends()
	if len(backends) == 0 {
		return contract.WorkspaceEnrollmentPresentation{}, ErrWorkspaceEnrollmentUnsupported
	}

	logical := a.logical
	a.enrollmentMu.Lock()
	defer a.enrollmentMu.Unlock()
	logical.mu.Lock()
	if result, resolved, err := existingWorkspaceEnrollmentLocked(a, logical); resolved {
		logical.mu.Unlock()
		return result, err
	}
	logical.mu.Unlock()

	// Secret resolution is potentially slow and must NOT run while holding
	// logical.mu: see the identical rationale on RequestAuthorization. Delegates
	// to the shared resolveClientSecret (rather than re-deriving it here) so the
	// already-populated raw target.clientSecret (the confidential embedded
	// broker's own client secret, set once at construction) is never bypassed
	// in favor of a secretEnv lookup that a target like this one never has.
	secret, err := target.resolveClientSecret(opCtx, a.runtime.oauth.resolveSecret)
	if err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	id, err := opaque(a.runtime.oauth.random)
	if err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	binding, err := opaque(a.runtime.oauth.random)
	if err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	state, err := opaque(a.runtime.oauth.random)
	if err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	verifier, err := opaque(a.runtime.oauth.random)
	if err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}

	logical.mu.Lock()
	defer logical.mu.Unlock()
	// Re-verify: a concurrent call (or deletion) may have already resolved
	// enrollment while the secret was resolving above.
	if result, resolved, err := existingWorkspaceEnrollmentLocked(a, logical); resolved {
		return result, err
	}
	transaction := &authorizationTransaction{
		identity:       authorizationIdentity{id: id, binding: session.AuthorizationBinding(binding)},
		route:          target,
		bundleBackends: backends,
		state:          state,
		verifier:       verifier,
		clientSecret:   secret,
		expiresAt:      a.runtime.oauth.now().Add(a.runtime.oauth.ttl),
		status:         session.AuthorizationPending,
	}
	logical.authorizations[transaction.identity] = transaction
	if !a.runtime.registerCallbackState(state, logical, transaction) {
		delete(logical.authorizations, transaction.identity)
		return contract.WorkspaceEnrollmentPresentation{}, errors.New("mcpbroker: create unique callback state")
	}
	url := presentWorkspaceTransaction(transaction)
	return contract.WorkspaceEnrollmentPresentation{Ref: workspaceEnrollmentRef(transaction), URL: url}, nil
}

// ObserveWorkspaceEnrollment reports the current bundle status. A granted
// transaction triggers authenticated discovery across the whole bundle and,
// only if every backend succeeds, publishes the frozen catalogue.
func (a *Attachment) ObserveWorkspaceEnrollment(ctx context.Context, ref contract.WorkspaceEnrollmentRef) (contract.WorkspaceEnrollmentResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	opCtx, done, err := a.beginOperation(ctx)
	if err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	defer done()
	if !ref.Valid() {
		return contract.WorkspaceEnrollmentResult{}, errors.New("mcpbroker: invalid workspace enrollment reference")
	}

	logical := a.logical
	a.enrollmentMu.Lock()
	defer a.enrollmentMu.Unlock()
	logical.mu.Lock()
	if completed := logical.completedEnrollment; completed != nil {
		if completed.ref != ref {
			logical.mu.Unlock()
			return contract.WorkspaceEnrollmentResult{}, contract.ErrAuthorizationNotFound
		}
		logical.mu.Unlock()
		catalogue, err := a.installCompletedEnrollment(completed)
		if err != nil {
			return contract.WorkspaceEnrollmentResult{}, err
		}
		return contract.WorkspaceEnrollmentResult{Ref: ref, Status: contract.WorkspaceEnrollmentConnected, Catalogue: catalogue}, nil
	}
	transaction := lookupWorkspaceTransactionLocked(logical, ref.ID)
	if transaction == nil {
		logical.mu.Unlock()
		return contract.WorkspaceEnrollmentResult{}, contract.ErrStateUnavailable
	}
	if !sameWorkspaceTransactionRef(transaction, ref) {
		logical.mu.Unlock()
		return contract.WorkspaceEnrollmentResult{}, contract.ErrAuthorizationNotFound
	}
	a.expireLocked(transaction)
	status := transaction.status
	switch status {
	case session.AuthorizationPending:
		logical.mu.Unlock()
		return contract.WorkspaceEnrollmentResult{Ref: ref, Status: contract.WorkspaceEnrollmentPending}, nil
	case session.AuthorizationCancelled:
		result := a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentCancelled)
		logical.mu.Unlock()
		return result, nil
	case session.AuthorizationExpired:
		result := a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentExpired)
		logical.mu.Unlock()
		return result, nil
	case session.AuthorizationDenied:
		result := a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentDenied)
		logical.mu.Unlock()
		return result, nil
	case session.AuthorizationFailed, session.AuthorizationClosed:
		result := a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentFailed)
		logical.mu.Unlock()
		return result, nil
	case session.AuthorizationGranted:
		// fall through to discovery below, unlocked.
	default:
		result := a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentFailed)
		logical.mu.Unlock()
		return result, nil
	}
	grant := logical.brokerCredential
	logical.mu.Unlock()
	if grant == nil {
		return contract.WorkspaceEnrollmentResult{}, contract.ErrStateUnavailable
	}

	process := a.runtime.process
	if process == nil {
		return a.failWorkspaceTransaction(logical, transaction), nil
	}
	occupied := append([]string(nil), process.occupied...)
	catalogue, err := a.FreezeAuthenticatedCatalogue(opCtx, ref, process, &brokerTokenSource{runtime: a.runtime, logical: logical, ctx: opCtx}, occupied)
	if err != nil {
		return a.failWorkspaceTransaction(logical, transaction), nil
	}

	logical.mu.Lock()
	if logical.deleted || logical.authorizations[transaction.identity] != transaction || transaction.status != session.AuthorizationGranted || logical.brokerCredential != grant {
		logical.mu.Unlock()
		return contract.WorkspaceEnrollmentResult{}, contract.ErrStateUnavailable
	}
	a.mu.RLock()
	routes := make([]route, 0, len(a.catalogue.routes))
	for _, route := range a.catalogue.routes {
		routes = append(routes, route)
	}
	a.mu.RUnlock()
	sortRoutes(routes)
	routes = cloneRoutes(routes)
	logical.completedEnrollment = &completedWorkspaceEnrollment{ref: ref, routes: routes}
	delete(logical.authorizations, transaction.identity)
	state := transaction.state
	transaction.clientSecret, transaction.verifier, transaction.state = "", "", ""
	logical.mu.Unlock()
	a.runtime.removeCallbackState(state, transaction)

	return contract.WorkspaceEnrollmentResult{Ref: ref, Status: contract.WorkspaceEnrollmentConnected, Catalogue: catalogue}, nil
}

// CancelWorkspaceEnrollment cancels the exact pending bundle and clears its
// aggregate broker credential. No terminal outcome leaves partial authority.
func (a *Attachment) CancelWorkspaceEnrollment(ctx context.Context, ref contract.WorkspaceEnrollmentRef) (contract.WorkspaceEnrollmentResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	_, done, err := a.beginOperation(ctx)
	if err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	defer done()
	if !ref.Valid() {
		return contract.WorkspaceEnrollmentResult{}, errors.New("mcpbroker: invalid workspace enrollment reference")
	}
	logical := a.logical
	a.enrollmentMu.Lock()
	defer a.enrollmentMu.Unlock()
	logical.mu.Lock()
	defer logical.mu.Unlock()
	transaction := lookupWorkspaceTransactionLocked(logical, ref.ID)
	if transaction == nil || !sameWorkspaceTransactionRef(transaction, ref) {
		return contract.WorkspaceEnrollmentResult{}, contract.ErrAuthorizationNotFound
	}
	result := a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentCancelled)
	return result, nil
}

func (a *Attachment) failWorkspaceTransaction(logical *logicalSession, transaction *authorizationTransaction) contract.WorkspaceEnrollmentResult {
	logical.mu.Lock()
	defer logical.mu.Unlock()
	if lookupWorkspaceTransactionLocked(logical, session.WorkspaceEnrollmentID(transaction.identity.id)) == nil {
		// Already cleared by a concurrent Cancel/expiry; report the same terminal.
		clearWorkspaceCredentialLocked(logical)
		return contract.WorkspaceEnrollmentResult{Ref: workspaceEnrollmentRef(transaction), Status: contract.WorkspaceEnrollmentFailed}
	}
	return a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentFailed)
}

// terminateWorkspaceTransactionLocked clears the aggregate broker credential and
// transaction record, and must be called with logical.mu held.
func (a *Attachment) terminateWorkspaceTransactionLocked(logical *logicalSession, transaction *authorizationTransaction, status contract.WorkspaceEnrollmentStatus) contract.WorkspaceEnrollmentResult {
	a.runtime.removeCallbackState(transaction.state, transaction)
	if transaction.cancel != nil {
		transaction.cancel()
	}
	transaction.clientSecret, transaction.verifier, transaction.state = "", "", ""
	clearWorkspaceCredentialLocked(logical)
	delete(logical.authorizations, transaction.identity)
	return contract.WorkspaceEnrollmentResult{Ref: workspaceEnrollmentRef(transaction), Status: status}
}

func clearWorkspaceCredentialLocked(logical *logicalSession) {
	clearGrantToken(logical.brokerCredential)
	logical.brokerCredential = nil
}

func lookupWorkspaceTransactionLocked(logical *logicalSession, id session.WorkspaceEnrollmentID) *authorizationTransaction {
	for _, transaction := range logical.authorizations {
		if transaction.bundleBackends != nil && transaction.identity.id == string(id) {
			return transaction
		}
	}
	return nil
}

func sameWorkspaceTransactionRef(transaction *authorizationTransaction, ref contract.WorkspaceEnrollmentRef) bool {
	expected := workspaceEnrollmentRef(transaction)
	return expected.ID == ref.ID && expected.RequiredServices == ref.RequiredServices && expected.ExpiresAt.Equal(ref.ExpiresAt)
}

func workspaceEnrollmentRef(transaction *authorizationTransaction) contract.WorkspaceEnrollmentRef {
	return contract.WorkspaceEnrollmentRef{
		ID:               session.WorkspaceEnrollmentID(transaction.identity.id),
		RequiredServices: uint32(len(transaction.bundleBackends)), // #nosec G115 -- bundle backend count is operator-configured, never near uint32 max
		ExpiresAt:        transaction.expiresAt,
	}
}

func presentWorkspaceTransaction(transaction *authorizationTransaction) string {
	cfg := transaction.oauthConfig(transaction.clientSecret)
	return cfg.AuthCodeURL(transaction.state, transaction.authCodeOptions()...)
}
