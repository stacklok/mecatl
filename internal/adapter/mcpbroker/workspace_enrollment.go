package mcpbroker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// ErrWorkspaceEnrollmentUnsupported reports that this attachment's Runtime has
// no owning Process, or that Process has no protected backend requiring
// pre-prompt authenticated discovery (every protected backend is either
// anonymous or has a trusted static tool declaration).
var ErrWorkspaceEnrollmentUnsupported = errors.New("mcpbroker: workspace enrollment is not required")

var _ contract.WorkspaceEnrollmentAttachment = (*Attachment)(nil)

// bundleBackends returns the deterministic, configured protected backends that
// require live authenticated discovery, the shared bundle-wide authorization
// target, and the owning Process. It returns a nil slice when workspace
// enrollment does not apply to this attachment.
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

// BeginWorkspaceEnrollment starts, or idempotently re-presents, the one
// bundle-wide pre-prompt authorization transaction for every protected
// backend that requires live discovery.
func (a *Attachment) BeginWorkspaceEnrollment(ctx context.Context) (contract.WorkspaceEnrollmentPresentation, error) {
	if err := ctx.Err(); err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	if err := a.stateError(); err != nil {
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	backends, target, _ := a.bundleBackends()
	if len(backends) == 0 {
		return contract.WorkspaceEnrollmentPresentation{}, ErrWorkspaceEnrollmentUnsupported
	}

	logical := a.logical
	logical.mu.Lock()
	defer logical.mu.Unlock()
	if logical.deleted {
		return contract.WorkspaceEnrollmentPresentation{}, contract.ErrStateUnavailable
	}
	for _, transaction := range logical.authorizations {
		if transaction.bundleBackends == nil {
			continue
		}
		a.expireLocked(transaction)
		if transaction.status == session.AuthorizationPending {
			url, err := presentWorkspaceTransaction(transaction)
			if err != nil {
				return contract.WorkspaceEnrollmentPresentation{}, err
			}
			return contract.WorkspaceEnrollmentPresentation{Ref: workspaceEnrollmentRef(transaction), URL: url}, nil
		}
	}
	if len(logical.authorizations) >= maxAuthorizationRecords {
		return contract.WorkspaceEnrollmentPresentation{}, errors.New("mcpbroker: authorization record capacity reached")
	}

	var secret string
	var err error
	if target.secretEnv != "" {
		secret, err = a.runtime.oauth.resolveSecret(ctx, target.secretEnv)
		if err != nil {
			return contract.WorkspaceEnrollmentPresentation{}, err
		}
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
	transaction := &authorizationTransaction{
		identity:       authorizationIdentity{id: id, binding: session.AuthorizationBinding(binding)},
		route:          target,
		backend:        backends[0],
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
	url, err := presentWorkspaceTransaction(transaction)
	if err != nil {
		delete(logical.authorizations, transaction.identity)
		a.runtime.removeCallbackState(state, transaction)
		return contract.WorkspaceEnrollmentPresentation{}, err
	}
	return contract.WorkspaceEnrollmentPresentation{Ref: workspaceEnrollmentRef(transaction), URL: url}, nil
}

// ObserveWorkspaceEnrollment reports the current bundle status. A granted
// transaction triggers authenticated discovery across the whole bundle and,
// only if every backend succeeds, publishes the frozen catalogue.
func (a *Attachment) ObserveWorkspaceEnrollment(ctx context.Context, ref contract.WorkspaceEnrollmentRef) (contract.WorkspaceEnrollmentResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	if err := a.stateError(); err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	if !ref.Valid() {
		return contract.WorkspaceEnrollmentResult{}, errors.New("mcpbroker: invalid workspace enrollment reference")
	}

	logical := a.logical
	logical.mu.Lock()
	transaction := lookupWorkspaceTransactionLocked(logical, ref.ID)
	if transaction == nil {
		logical.mu.Unlock()
		return contract.WorkspaceEnrollmentResult{}, contract.ErrStateUnavailable
	}
	a.expireLocked(transaction)
	status := transaction.status
	backends := append([]string(nil), transaction.bundleBackends...)
	backend := transaction.backend

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
	case session.AuthorizationFailed, session.AuthorizationDenied, session.AuthorizationClosed:
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
	grant := logical.grants[backend]
	var grantConfig *oauth2.Config
	var grantToken *oauth2.Token
	if grant != nil {
		// Captured while logical.mu is still held: grant.token may be
		// concurrently reassigned (never mutated in place) by a sibling
		// scopedTokenSource refresh, so reading the field again after
		// unlocking would race with that write.
		grantConfig, grantToken = grant.config, grant.token
	}
	logical.mu.Unlock()
	if grant == nil {
		return contract.WorkspaceEnrollmentResult{}, contract.ErrStateUnavailable
	}

	process := a.runtime.process
	if process == nil {
		return a.failWorkspaceTransaction(logical, transaction, backends), nil
	}
	authSession, err := workspaceAuthSessionFromToken(grantToken)
	if err != nil {
		return a.failWorkspaceTransaction(logical, transaction, backends), nil
	}
	occupied := append([]string(nil), process.occupied...)
	catalogue, err := a.FreezeAuthenticatedCatalogue(ctx, ref, process, authSession, occupied)
	if err != nil {
		return a.failWorkspaceTransaction(logical, transaction, backends), nil
	}

	logical.mu.Lock()
	if !logical.deleted {
		for _, member := range backends {
			if existing, ok := logical.grants[member]; ok {
				existing.firstPending = false
			} else {
				logical.grants[member] = &oauthGrant{config: grantConfig, token: cloneToken(grantToken), executed: make(map[session.ToolCallID][32]byte)}
			}
		}
		delete(logical.authorizations, transaction.identity)
	}
	logical.mu.Unlock()
	a.runtime.removeCallbackState(transaction.state, transaction)

	return contract.WorkspaceEnrollmentResult{Ref: ref, Status: contract.WorkspaceEnrollmentConnected, Catalogue: catalogue}, nil
}

// CancelWorkspaceEnrollment cancels the exact pending bundle and clears every
// backend's transaction/grant state. No terminal outcome leaves a partial grant.
func (a *Attachment) CancelWorkspaceEnrollment(ctx context.Context, ref contract.WorkspaceEnrollmentRef) (contract.WorkspaceEnrollmentResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	if err := a.stateError(); err != nil {
		return contract.WorkspaceEnrollmentResult{}, err
	}
	if !ref.Valid() {
		return contract.WorkspaceEnrollmentResult{}, errors.New("mcpbroker: invalid workspace enrollment reference")
	}
	logical := a.logical
	logical.mu.Lock()
	defer logical.mu.Unlock()
	transaction := lookupWorkspaceTransactionLocked(logical, ref.ID)
	if transaction == nil {
		return contract.WorkspaceEnrollmentResult{}, contract.ErrAuthorizationNotFound
	}
	result := a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentCancelled)
	return result, nil
}

func (a *Attachment) failWorkspaceTransaction(logical *logicalSession, transaction *authorizationTransaction, backends []string) contract.WorkspaceEnrollmentResult {
	logical.mu.Lock()
	defer logical.mu.Unlock()
	if lookupWorkspaceTransactionLocked(logical, session.WorkspaceEnrollmentID(transaction.identity.id)) == nil {
		// Already cleared by a concurrent Cancel/expiry; report the same terminal.
		clearWorkspaceGrantsLocked(logical, backends)
		return contract.WorkspaceEnrollmentResult{Ref: workspaceEnrollmentRef(transaction), Status: contract.WorkspaceEnrollmentFailed}
	}
	return a.terminateWorkspaceTransactionLocked(logical, transaction, contract.WorkspaceEnrollmentFailed)
}

// terminateWorkspaceTransactionLocked clears every bundle backend's grant and
// the transaction record, and must be called with logical.mu held.
func (a *Attachment) terminateWorkspaceTransactionLocked(logical *logicalSession, transaction *authorizationTransaction, status contract.WorkspaceEnrollmentStatus) contract.WorkspaceEnrollmentResult {
	a.runtime.removeCallbackState(transaction.state, transaction)
	if transaction.cancel != nil {
		transaction.cancel()
	}
	transaction.clientSecret, transaction.verifier, transaction.state = "", "", ""
	clearWorkspaceGrantsLocked(logical, transaction.bundleBackends)
	delete(logical.authorizations, transaction.identity)
	return contract.WorkspaceEnrollmentResult{Ref: workspaceEnrollmentRef(transaction), Status: status}
}

func clearWorkspaceGrantsLocked(logical *logicalSession, backends []string) {
	for _, backend := range backends {
		if grant, ok := logical.grants[backend]; ok {
			if grant.token != nil {
				grant.token.AccessToken = ""
				grant.token.RefreshToken = ""
			}
			delete(logical.grants, backend)
		}
	}
}

func lookupWorkspaceTransactionLocked(logical *logicalSession, id session.WorkspaceEnrollmentID) *authorizationTransaction {
	for _, transaction := range logical.authorizations {
		if transaction.bundleBackends != nil && transaction.identity.id == string(id) {
			return transaction
		}
	}
	return nil
}

func workspaceEnrollmentRef(transaction *authorizationTransaction) contract.WorkspaceEnrollmentRef {
	return contract.WorkspaceEnrollmentRef{
		ID:               session.WorkspaceEnrollmentID(transaction.identity.id),
		RequiredServices: uint32(len(transaction.bundleBackends)),
		ExpiresAt:        transaction.expiresAt,
	}
}

func presentWorkspaceTransaction(transaction *authorizationTransaction) (string, error) {
	cfg := transaction.oauthConfig(transaction.clientSecret)
	challenge := sha256.Sum256([]byte(transaction.verifier))
	options := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}
	if transaction.route.requestRefresh {
		options = append(options, oauth2.AccessTypeOffline)
	}
	return cfg.AuthCodeURL(transaction.state, options...), nil
}

// workspaceAuthSessionFromToken recovers the ToolHive-native upstream-token
// storage key from mecatl's own JWT access token. This is safe without
// signature verification: the token is our own recent output from the PKCE
// exchange this same process just ran against its own embedded ToolHive
// authorization server (auth.go's handleCallback) — no attacker-controlled
// token ever reaches this function. The "tsid" claim name is defined by
// ToolHive (pkg/authserver/server/session.TokenSessionIDClaimKey).
func workspaceAuthSessionFromToken(token *oauth2.Token) (ToolHiveAuthSessionID, error) {
	if token == nil || token.AccessToken == "" {
		return "", ErrAuthenticatedDiscovery
	}
	parts := strings.Split(token.AccessToken, ".")
	if len(parts) != 3 {
		return "", ErrAuthenticatedDiscovery
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrAuthenticatedDiscovery
	}
	var claims struct {
		TSID string `json:"tsid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.TSID == "" {
		return "", ErrAuthenticatedDiscovery
	}
	return ToolHiveAuthSessionID(claims.TSID), nil
}
