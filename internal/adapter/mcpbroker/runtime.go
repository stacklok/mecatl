// Package mcpbroker implements the in-process, session-scoped MCP broker.
// Model-facing catalogues contain only neutral tool specifications; backend
// routing remains private to this adapter.
package mcpbroker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

const (
	diagnosticEventTokenRefresh                  = "token_refresh"
	diagnosticEventRouteUnavailable              = "route_unavailable"
	diagnosticEventAuthenticatedCatalogueFreeze  = "authenticated_catalogue_freeze"
	diagnosticEventAuthenticatedCatalogueBackend = "authenticated_catalogue_backend"
	diagnosticReasonSucceeded                    = "succeeded"
	diagnosticReasonFailed                       = "failed"
	diagnosticReasonReauthRequired               = "reauth_required"
	diagnosticReasonDiscoveryFailed              = "discovery_failed"
	diagnosticReasonDiscovered                   = "discovered"
	diagnosticReasonValidationFailed             = "validation_failed"
	diagnosticReasonMaterializeFailed            = "materialize_failed"
	diagnosticReasonAuthorityUnavailable         = "authority_unavailable"
	diagnosticCredentialRoute                    = "route"
	diagnosticCredentialBroker                   = "broker"
	// authorization is the Mecatl-owned OAuth transaction lifecycle. Its fields
	// are closed values only; never add protocol values or identifiers here.
	diagnosticEventAuthorization               = "authorization"
	diagnosticEventAuthorizationLookup         = "authorization_lookup"
	diagnosticReasonRequestStarted             = "request_started"
	diagnosticReasonCallbackSucceeded          = "callback_succeeded"
	diagnosticReasonCallbackDenied             = "callback_denied"
	diagnosticReasonCallbackExpired            = "callback_expired"
	diagnosticReasonCallbackFailed             = "callback_failed"
	diagnosticReasonAuthorizationFound         = "found"
	diagnosticReasonAuthorizationNotFound      = "not_found"
	diagnosticReasonAuthorizationLookupFailed  = "lookup_failed"
	diagnosticReasonStateUnavailable           = "state_unavailable"
	diagnosticAuthorizationSurfacePresent      = "present"
	diagnosticAuthorizationSurfaceStatus       = "status"
	diagnosticAuthorizationSurfaceCancel       = "cancel"
	diagnosticRouteSurfaceNative               = "native"
	diagnosticRouteSurfaceQuery                = "query"
	diagnosticEventWorkspaceEnrollment         = "workspace_enrollment"
	diagnosticEventSessionAttach               = "session_attach"
	diagnosticReasonSessionCreated             = "created"
	diagnosticReasonSessionReattached          = "reattached"
	diagnosticEventQueryFailure                = "query_failure"
	diagnosticQueryReasonTargetUnavailable     = "target_unavailable"
	diagnosticQueryReasonTransport             = "transport"
	diagnosticQueryReasonProjectionLimit       = "projection_limit"
	diagnosticQueryReasonExecutionUncertain    = "execution_uncertain"
	diagnosticEnrollmentOperationBegin         = "begin"
	diagnosticEnrollmentOperationObserve       = "observe"
	diagnosticEnrollmentOperationCancel        = "cancel"
	diagnosticEnrollmentReasonRequestStarted   = "request_started"
	diagnosticEnrollmentReasonRequestObserved  = "request_observed"
	diagnosticEnrollmentReasonRequestCancelled = "request_cancelled"
	diagnosticEnrollmentReasonAlreadyCompleted = "already_completed"
	diagnosticEnrollmentReasonRejected         = "rejected"
	diagnosticEnrollmentReasonStarted          = "started"
	diagnosticEnrollmentReasonCompleted        = "completed"
)

var (
	// ErrInvalidCatalogue reports an invalid declaration, discovery result, or
	// model-visible tool-name collision.
	ErrInvalidCatalogue = errors.New("mcpbroker: invalid catalogue")
	// ErrProtectedRouteUnsupported reports the P08 boundary: OAuth routes are
	// declared by P07 but are not executable until protected-route custody lands.
	ErrProtectedRouteUnsupported = errors.New("mcpbroker: protected route unsupported")
	// ErrInvalidSessionID means the caller supplied an identifier outside the
	// broker's bounded logical-session grammar.
	ErrInvalidSessionID = errors.New("mcpbroker: invalid session ID")
)

// ToolDefinition is the neutral result of discovering one tool on a configured
// MCP route. Backend is matched to a P07 route declaration and is never copied
// into the model-facing ToolSpec.
type ToolDefinition struct {
	Backend     string
	Name        string
	Description string
	Schema      json.RawMessage
	ReadOnly    bool
}

type route struct {
	backend  string
	spec     tool.ToolSpec
	readOnly bool
	oauth    *oauthRoute
	// broker marks a ToolHive-routed capability that executes with the outer
	// broker credential. Before authorization a static declaration also has an
	// oauth route and therefore requests the aggregate ToolHive authorization;
	// the frozen route retains broker alone.
	broker bool
}

// Catalogue is an immutable compiled broker catalogue. Specs deliberately
// expose no backend route or execution metadata.
type Catalogue struct {
	routes []route
}

// Compile validates anonymous P07 declarations and neutral discovery results.
// reservedToolNames contains names already visible to the model; collisions are rejected
// before any session attachment is created.
func Compile(config mcpauthority.BrokerConfig, discovered []ToolDefinition, reservedToolNames []string) (*Catalogue, error) {
	backends := make(map[string]permconfig.MCPServerProfile, len(config.Routes))
	for _, declaration := range config.Routes {
		key := strings.ToLower(declaration.Name)
		if key == "" {
			return nil, fmt.Errorf("%w: route name is required", ErrInvalidCatalogue)
		}
		if _, exists := backends[key]; exists {
			return nil, fmt.Errorf("%w: duplicate route %q", ErrInvalidCatalogue, declaration.Name)
		}
		if declaration.Auth.Mode != "none" && declaration.Auth.Mode != "oauth" {
			return nil, fmt.Errorf("%w: route %q uses auth mode %q", ErrProtectedRouteUnsupported, declaration.Name, declaration.Auth.Mode)
		}
		if declaration.Auth.Mode == "oauth" {
			if declaration.Auth.OAuth == nil {
				return nil, fmt.Errorf("%w: route %q has no OAuth declaration", ErrProtectedRouteUnsupported, declaration.Name)
			}
			if config.CallbackURL == "" {
				return nil, fmt.Errorf("%w: OAuth routes require a callback URL", ErrInvalidCatalogue)
			}
		}
		backends[key] = declaration
	}

	seen := make(map[string]struct{}, len(reservedToolNames)+len(discovered))
	for _, name := range reservedToolNames {
		if name == "" {
			return nil, fmt.Errorf("%w: reserved tool name is empty", ErrInvalidCatalogue)
		}
		seen[name] = struct{}{}
	}

	routes := make([]route, 0, len(discovered))
	for _, definition := range discovered {
		declaration, configured := backends[strings.ToLower(definition.Backend)]
		if !configured {
			return nil, fmt.Errorf("%w: discovery references unconfigured route %q", ErrInvalidCatalogue, definition.Backend)
		}
		backend := declaration.Name
		if definition.Name == "" {
			return nil, fmt.Errorf("%w: discovered tool name is required", ErrInvalidCatalogue)
		}
		if _, collision := seen[definition.Name]; collision {
			return nil, fmt.Errorf("%w: model-visible tool name collision %q", ErrInvalidCatalogue, definition.Name)
		}
		if len(definition.Schema) != 0 && !json.Valid(definition.Schema) {
			return nil, fmt.Errorf("%w: tool %q has invalid JSON schema", ErrInvalidCatalogue, definition.Name)
		}
		seen[definition.Name] = struct{}{}
		compiledRoute := route{
			backend: backend,
			spec: tool.ToolSpec{
				Name:        definition.Name,
				Description: definition.Description,
				Schema:      append(json.RawMessage(nil), definition.Schema...),
			},
			readOnly: definition.ReadOnly,
		}
		if declaration.Auth.Mode == "oauth" {
			oauthRoute, err := compileOAuthRoute(config.CallbackURL, declaration)
			if err != nil {
				return nil, err
			}
			compiledRoute.oauth = oauthRoute
		}
		routes = append(routes, compiledRoute)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].spec.Name < routes[j].spec.Name })
	return &Catalogue{routes: routes}, nil
}

// Specs returns the stable model-facing catalogue in name order.
func (c *Catalogue) Specs() []tool.ToolSpec {
	if c == nil {
		return nil
	}
	out := make([]tool.ToolSpec, len(c.routes))
	for i, route := range c.routes {
		out[i] = copySpec(route.spec)
	}
	return out
}

// Limits bounds broker-owned logical state. Zero values select safe defaults.
type Limits struct {
	// MaxLogicalSessions caps logical sessions retained by the process, independent of handles.
	MaxLogicalSessions int
	// LogicalRetention keeps a session and its ownership state after its last attachment closes.
	// It is the broker's local retention window, not a client-visible lease duration.
	LogicalRetention time.Duration
	// SweepInterval controls how often expired sessions and callback transactions are reclaimed.
	SweepInterval time.Duration
	// MaxPendingStates caps callback/authorization states that have not reached a terminal outcome.
	MaxPendingStates int
}

const (
	defaultMaxLogicalSessions = 1024
	defaultLogicalRetention   = 24 * time.Hour
	defaultBrokerSweep        = time.Minute
	defaultMaxPendingStates   = 1024
	maxLogicalSessionIDBytes  = contract.MaxLogicalSessionIDBytes
)

func (l Limits) withDefaults() Limits {
	if l.MaxLogicalSessions <= 0 {
		l.MaxLogicalSessions = defaultMaxLogicalSessions
	}
	if l.LogicalRetention <= 0 {
		l.LogicalRetention = defaultLogicalRetention
	}
	if l.SweepInterval <= 0 {
		l.SweepInterval = defaultBrokerSweep
	}
	if l.MaxPendingStates <= 0 {
		l.MaxPendingStates = defaultMaxPendingStates
	}
	return l
}

// WithLimits configures bounded logical-session and pending callback state.
func WithLimits(limits Limits) Option {
	return func(runtime *Runtime) {
		runtime.limits = limits.withDefaults()
		runtime.sweeperEnabled = true
	}
}

func validLogicalSessionID(id session.SessionID) bool {
	return contract.ValidLogicalSessionID(id)
}

// SessionRef is an opaque reference to one in-process logical-session incarnation.
// SessionID is exposed for backend attribution; the incarnation remains private.
type SessionRef struct {
	id         session.SessionID
	generation uint64
}

// SessionID returns the canonical mecatl session identity without exposing its incarnation.
func (r SessionRef) SessionID() session.SessionID { return r.id }

type logicalSession struct {
	mu                   sync.RWMutex
	ref                  SessionRef
	deleted              bool
	operationCtx         context.Context
	cancelOps            context.CancelFunc
	activeOps            int
	operationsDone       chan struct{}
	provisional          bool
	recoveredProvisional bool
	cleaned              bool
	cleanupStatus        session.AuthorizationStatus
	authorizations       map[authorizationIdentity]*authorizationTransaction
	grants               map[string]*oauthGrant
	brokerCredential     *oauthGrant
	completedEnrollment  *completedWorkspaceEnrollment
	createdAt            time.Time
	expiresAt            time.Time
	attachments          int
	recoveredSource      *recoveredCredentialSource
	recoveryValid        func(context.Context) error
	recoveryDeadline     time.Time
}

// Caller is the private execution seam used by the in-process transport. The
// opaque session incarnation and configured backend are passed separately from
// model-controlled arguments.
type Caller func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error)

// AuthorizedCaller is the protected-route execution seam. The token source is
// scoped to the exact logical session and route and retains refresh custody.
type AuthorizedCaller func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error)

// QueryCaller invokes one advertised tool and applies the jq projection before
// the raw result can leave the broker transport boundary.
type QueryCaller func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource, string) (session.ToolResult, error)

// Runtime owns logical broker sessions and creates process-local attachments.
type Runtime struct {
	mu               sync.RWMutex
	stateMu          sync.Mutex
	catalogue        *Catalogue
	caller           Caller
	authorizedCaller AuthorizedCaller
	queryCaller      QueryCaller
	diag             port.Diagnostics
	oauth            oauthRuntimeOptions
	sessions         map[session.SessionID]*logicalSession
	states           map[string]callbackState
	nextGeneration   uint64
	bindingPrefix    string
	closed           bool
	drainSessions    []*logicalSession
	// process is set only when this Runtime is owned by a bundled ToolHive
	// Process (NewToolHiveProcess). It lets a SessionHandle reach the pre-prompt
	// authenticated-discovery primitives without widening the neutral contract.
	// nil for a plain Compile-based Runtime, which never supports workspace
	// enrollment.
	process        *Process
	limits         Limits
	sweepStop      chan struct{}
	sweepDone      chan struct{}
	sweeperEnabled bool
}

var _ contract.Service = (*Runtime)(nil)
var _ contract.BindingSessionDeleter = (*Runtime)(nil)
var _ contract.ExpectedBindingAttacher = (*Runtime)(nil)

// Ready verifies the process-local runtime without attaching a session or
// executing upstream work.
func (r *Runtime) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return errors.New("mcpbroker: runtime is unavailable")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.catalogue == nil || r.caller == nil {
		return errors.New("mcpbroker: runtime is unavailable")
	}
	if r.process != nil {
		return r.process.ready(ctx)
	}
	return nil
}

// New constructs an in-process broker. OAuth options are required only when the
// catalogue contains protected routes.
func New(catalogue *Catalogue, caller Caller, options ...Option) (*Runtime, error) {
	if catalogue == nil {
		return nil, fmt.Errorf("%w: catalogue is required", ErrInvalidCatalogue)
	}
	if caller == nil {
		return nil, fmt.Errorf("%w: caller is required", ErrInvalidCatalogue)
	}
	bindingSeed := make([]byte, 18)
	if _, err := rand.Read(bindingSeed); err != nil {
		return nil, fmt.Errorf("%w: create runtime binding: %v", ErrInvalidCatalogue, err)
	}
	runtime := &Runtime{
		catalogue:      catalogue,
		caller:         caller,
		oauth:          defaultOAuthRuntimeOptions(),
		diag:           port.NopDiagnostics{},
		sessions:       make(map[session.SessionID]*logicalSession),
		states:         make(map[string]callbackState),
		bindingPrefix:  base64.RawURLEncoding.EncodeToString(bindingSeed),
		limits:         (Limits{}).withDefaults(),
		sweeperEnabled: true,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: nil runtime option", ErrInvalidCatalogue)
		}
		option(runtime)
	}
	if (catalogue.protected() || runtime.oauth.forcedTokenEndpoint != "") && runtime.authorizedCaller == nil {
		return nil, fmt.Errorf("%w: protected routes require an authorized caller", ErrInvalidCatalogue)
	}
	// A bundled ToolHive Process may have a protected authorization target used
	// only by pre-prompt workspace enrollment: every protected backend requires
	// live discovery, so the compiled catalogue has no static oauth route and
	// catalogue.protected() alone would miss it.
	if protected := catalogue.protected() || runtime.oauth.forcedTokenEndpoint != ""; protected {
		tokenEndpoint := runtime.oauth.forcedTokenEndpoint
		if tokenEndpoint == "" {
			for _, route := range catalogue.routes {
				if route.oauth != nil {
					tokenEndpoint = route.oauth.tokenEndpoint
					break
				}
			}
		}
		clientOptions := mcpadapter.HardenedOAuthTokenClientOptions{TokenEndpoint: tokenEndpoint, Timeout: runtime.oauth.timeout}
		if runtime.oauth.allowLoopback {
			mcpadapter.AllowHardenedOAuthTokenLoopbackForTest(runtime.oauth.testHelper, &clientOptions, runtime.oauth.testRootCAs)
		}
		client, err := mcpadapter.NewHardenedOAuthTokenClient(clientOptions)
		if err != nil {
			return nil, fmt.Errorf("%w: hardened OAuth token client: %v", ErrInvalidCatalogue, err)
		}
		runtime.oauth.httpClient = client
	}
	if runtime.sweeperEnabled {
		runtime.sweepStop = make(chan struct{})
		runtime.sweepDone = make(chan struct{})
		go runtime.sweep()
	}
	return runtime, nil
}

// AttachSession creates or reattaches to logical state keyed by the canonical
// mecatl session ID. Each call returns an independently closeable local handle.
func (r *Runtime) AttachSession(ctx context.Context, id session.SessionID) (contract.SessionHandle, contract.AttachOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !validLogicalSessionID(id) {
		return nil, "", ErrInvalidSessionID
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, "", contract.ErrStateUnavailable
	}
	logical, exists := r.sessions[id]
	outcome := contract.AttachReattached
	if !exists {
		if len(r.sessions) >= r.limits.MaxLogicalSessions {
			r.mu.Unlock()
			return nil, "", contract.ErrCapacity
		}
		r.nextGeneration++
		now := time.Now()
		operationCtx, cancelOps := context.WithCancel(context.Background())
		logical = &logicalSession{
			ref:            SessionRef{id: id, generation: r.nextGeneration},
			operationCtx:   operationCtx,
			cancelOps:      cancelOps,
			provisional:    true,
			createdAt:      now,
			expiresAt:      now.Add(r.limits.LogicalRetention),
			authorizations: make(map[authorizationIdentity]*authorizationTransaction),
			grants:         make(map[string]*oauthGrant),
		}
		r.sessions[id] = logical
		outcome = contract.AttachCreated
	} else {
		logical.mu.Lock()
		if logical.recoveredProvisional {
			logical.mu.Unlock()
			r.mu.Unlock()
			return nil, "", contract.ErrContinuityUnavailable
		}
		// Observation by an independent attachment publishes a provisional
		// ordinary creation. Recovered provisional state is deliberately excluded
		// above and requires the exact expected-binding path.
		logical.provisional = false
		logical.mu.Unlock()
	}
	logical.mu.Lock()
	logical.attachments++
	logical.mu.Unlock()
	r.mu.Unlock()

	handle, err := r.newAttachedHandle(logical, outcome == contract.AttachCreated, false)
	if err != nil {
		return nil, "", err
	}
	r.logSessionAttach(ctx, id, outcome)
	return handle, outcome, nil
}

// AttachSessionExpectedBinding reattaches only the exact existing logical
// session selected by a persisted opaque binding. It never creates state and
// does not observer-publish recovered provisional state.
func (r *Runtime) AttachSessionExpectedBinding(ctx context.Context, id session.SessionID, expected session.ExternalBinding) (contract.SessionHandle, contract.AttachOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !validLogicalSessionID(id) || expected == "" {
		return nil, "", contract.ErrContinuityUnavailable
	}
	prefix, _, found := strings.Cut(string(expected), ".")
	if !found || prefix == "" {
		return nil, "", contract.ErrContinuityUnavailable
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, "", contract.ErrStateUnavailable
	}
	if prefix != r.bindingPrefix {
		r.mu.Unlock()
		return nil, "", errors.Join(contract.ErrStateUnavailable, contract.ErrBrokerIncarnationLost)
	}
	logical := r.sessions[id]
	if logical == nil || expected != r.bindingFor(logical.ref.generation) {
		r.mu.Unlock()
		return nil, "", contract.ErrContinuityUnavailable
	}
	logical.mu.Lock()
	if logical.deleted {
		logical.mu.Unlock()
		r.mu.Unlock()
		return nil, "", contract.ErrContinuityUnavailable
	}
	recovered := logical.recoveredProvisional && logical.provisional
	logical.attachments++
	logical.mu.Unlock()
	r.mu.Unlock()

	handle, err := r.newAttachedHandle(logical, false, false)
	if err != nil {
		return nil, "", err
	}
	r.logExpectedBindingAttach(ctx, recovered)
	if recovered {
		return handle, contract.AttachRecoveredProvisional, nil
	}
	return handle, contract.AttachReattached, nil
}

func (r *Runtime) newRecoveredProvisional(id session.SessionID, deadline time.Time, source *recoveredCredentialSource) (*SessionHandle, error) {
	if !validLogicalSessionID(id) || source == nil || !deadline.After(time.Now()) {
		return nil, contract.ErrContinuityUnavailable
	}
	r.mu.Lock()
	if r.closed || r.sessions[id] != nil || len(r.sessions) >= r.limits.MaxLogicalSessions {
		r.mu.Unlock()
		return nil, contract.ErrContinuityUnavailable
	}
	r.nextGeneration++
	operationCtx, cancelOps := context.WithCancel(context.Background())
	logical := &logicalSession{
		ref:                  SessionRef{id: id, generation: r.nextGeneration},
		operationCtx:         operationCtx,
		cancelOps:            cancelOps,
		provisional:          true,
		recoveredProvisional: true,
		authorizations:       make(map[authorizationIdentity]*authorizationTransaction),
		grants:               make(map[string]*oauthGrant),
		createdAt:            time.Now(),
		expiresAt:            deadline,
		attachments:          1,
		recoveredSource:      source,
		recoveryValid:        source.validateCurrent,
		recoveryDeadline:     deadline,
	}
	r.sessions[id] = logical
	r.mu.Unlock()
	handle, err := r.newAttachedHandle(logical, false, true)
	if err != nil {
		_, _ = r.DeleteSession(context.Background(), id)
		return nil, err
	}
	time.AfterFunc(time.Until(deadline), func() {
		r.mu.Lock()
		if r.sessions[id] == logical {
			logical.mu.Lock()
			if logical.recoveredProvisional && logical.provisional {
				delete(r.sessions, id)
				logical.markDeletedLocked(session.AuthorizationExpired)
			}
			logical.mu.Unlock()
		}
		r.mu.Unlock()
	})
	return handle, nil
}

func (r *Runtime) bindingFor(generation uint64) session.ExternalBinding {
	return session.ExternalBinding(r.bindingPrefix + "." + fmt.Sprint(generation))
}

func (r *Runtime) newAttachedHandle(logical *logicalSession, creator, recoveredProvisional bool) (*SessionHandle, error) {
	handle := &SessionHandle{runtime: r, logical: logical, creator: creator, recoveredProvisional: recoveredProvisional}
	tools := make([]tool.Tool, len(r.catalogue.routes))
	for i, route := range r.catalogue.routes {
		base := &sessionTool{attachment: handle, route: route}
		if route.oauth != nil {
			tools[i] = &protectedSessionTool{sessionTool: base}
		} else {
			tools[i] = base
		}
	}
	handle.catalogue = newAttachmentCatalogue(r.catalogue.routes, tools, nil)
	logical.mu.RLock()
	completed := logical.completedEnrollment
	logical.mu.RUnlock()
	if completed != nil {
		if _, err := handle.installCompletedEnrollment(completed); err != nil {
			_ = handle.Abort(context.Background())
			return nil, err
		}
	}
	return handle, nil
}

// DeleteSession logically deletes one broker session and invalidates all handles
// to that incarnation. Reattachment later creates a fresh incarnation.
func (r *Runtime) DeleteSession(ctx context.Context, id session.SessionID) (contract.DeleteOutcome, error) {
	return r.deleteSession(ctx, id, "")
}

// DeleteSessionIfBinding atomically deletes only the exact opaque logical
// incarnation selected by binding. It leaves a newer incarnation untouched.
func (r *Runtime) DeleteSessionIfBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (contract.DeleteOutcome, error) {
	if binding == "" {
		return "", contract.ErrStateUnavailable
	}
	return r.deleteSession(ctx, id, binding)
}

func (r *Runtime) deleteSession(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (contract.DeleteOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", contract.ErrStateUnavailable
	}
	logical, exists := r.sessions[id]
	if !exists {
		r.mu.Unlock()
		return contract.DeleteNotFound, nil
	}
	if binding != "" && binding != r.bindingFor(logical.ref.generation) {
		r.mu.Unlock()
		return contract.DeleteNotFound, nil
	}
	logical.mu.Lock()
	logical.markDeletedLocked(session.AuthorizationClosed)
	delete(r.sessions, id)
	r.mu.Unlock()
	logical.maybeCleanupLocked(r)
	done := logical.operationsDone
	logical.mu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return contract.DeleteDeleted, ctx.Err()
		}
	}
	return contract.DeleteDeleted, nil
}

// SessionHandle is a local handle to a logical broker session. Tools is an
// adapter-specific projection used by composition; the P06 lifecycle methods
// satisfy the neutral contract.
type SessionHandle struct {
	mu                   sync.RWMutex
	enrollmentMu         sync.Mutex
	runtime              *Runtime
	logical              *logicalSession
	closed               bool
	detached             bool
	creator              bool
	recoveredProvisional bool
	settled              bool
	activeOps            int
	operationsDone       chan struct{}
	verifiedTSID         string
	catalogue            *attachmentCatalogue
}

func (a *SessionHandle) detachLogical() {
	a.mu.Lock()
	if a.detached {
		a.mu.Unlock()
		return
	}
	a.detached = true
	a.mu.Unlock()
	a.logical.mu.Lock()
	if a.logical.attachments > 0 {
		a.logical.attachments--
	}
	a.logical.mu.Unlock()
}

var _ contract.SessionHandle = (*SessionHandle)(nil)

// Commit publishes this attachment's private creation or a recovered provisional
// attempt. Reattached attachments otherwise settle idempotently.
func (a *SessionHandle) Commit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.logical.mu.RLock()
	recoveredProvisional := a.logical.recoveredProvisional && a.logical.provisional
	recoverySource := a.logical.recoveredSource
	recoveryValid := a.logical.recoveryValid
	a.logical.mu.RUnlock()
	if recoveredProvisional && recoverySource != nil && (recoveryValid == nil || recoveryValid(ctx) != nil) {
		return contract.ErrContinuityUnavailable
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settled {
		return nil
	}
	if a.closed {
		return contract.ErrAttachmentClosed
	}
	if a.creator || a.recoveredProvisional {
		a.runtime.mu.Lock()
		a.logical.mu.Lock()
		current := a.runtime.sessions[a.logical.ref.id]
		if a.runtime.closed || current != a.logical || a.logical.deleted {
			a.logical.mu.Unlock()
			a.runtime.mu.Unlock()
			return contract.ErrStateUnavailable
		}
		if !a.recoveredProvisional || (a.logical.recoveredProvisional && a.logical.provisional) {
			wasRecovered := a.logical.recoveredProvisional && a.logical.provisional
			if wasRecovered && !a.logical.recoveryDeadline.IsZero() && !a.logical.recoveryDeadline.After(time.Now()) {
				a.logical.mu.Unlock()
				a.runtime.mu.Unlock()
				return contract.ErrContinuityUnavailable
			}
			a.logical.provisional = false
			a.logical.recoveredProvisional = false
			if wasRecovered {
				a.logical.expiresAt = time.Now().Add(a.runtime.limits.LogicalRetention)
				a.logical.recoveryDeadline = time.Time{}
			}
		}
		a.logical.mu.Unlock()
		a.runtime.mu.Unlock()
	} else {
		a.runtime.mu.RLock()
		a.logical.mu.Lock()
		available := !a.runtime.closed && a.runtime.sessions[a.logical.ref.id] == a.logical && !a.logical.deleted
		if available && a.logical.recoveredProvisional && a.logical.provisional {
			if !a.logical.recoveryDeadline.IsZero() && !a.logical.recoveryDeadline.After(time.Now()) {
				a.logical.mu.Unlock()
				a.runtime.mu.RUnlock()
				return contract.ErrContinuityUnavailable
			}
			a.logical.provisional = false
			a.logical.recoveredProvisional = false
			a.logical.expiresAt = time.Now().Add(a.runtime.limits.LogicalRetention)
			a.logical.recoveryDeadline = time.Time{}
		}
		a.logical.mu.Unlock()
		a.runtime.mu.RUnlock()
		if !available {
			return contract.ErrStateUnavailable
		}
	}
	a.settled = true
	return nil
}

// Abort closes this attachment and conditionally rolls back only its still-private
// creation or the recovered handle that minted a recovered provisional attempt.
func (a *SessionHandle) Abort(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	rollback := (a.creator || a.recoveredProvisional) && !a.settled
	a.closed = true
	a.verifiedTSID = ""
	a.settled = true
	done := a.operationsDone
	if rollback {
		a.runtime.mu.Lock()
		a.logical.mu.Lock()
		deleted := false
		if a.runtime.sessions[a.logical.ref.id] == a.logical && a.logical.provisional && ((a.creator && !a.logical.recoveredProvisional) || (a.recoveredProvisional && a.logical.recoveredProvisional)) {
			delete(a.runtime.sessions, a.logical.ref.id)
			a.logical.markDeletedLocked(session.AuthorizationClosed)
			deleted = true
		}
		a.runtime.mu.Unlock()
		if deleted {
			a.logical.maybeCleanupLocked(a.runtime)
		}
		a.logical.mu.Unlock()
	}
	a.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			a.detachLogical()
			return ctx.Err()
		}
	}
	a.detachLogical()
	return nil
}

// Binding returns the opaque identity of this logical-session incarnation.
func (a *SessionHandle) Binding() session.ExternalBinding {
	return a.runtime.bindingFor(a.logical.ref.generation)
}

// Tools returns a copy of the attachment's current whole catalogue. Publication
// replaces the catalogue in one assignment, so callers cannot observe staged
// protected tools.
func (a *SessionHandle) Tools() []tool.Tool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.catalogue.Tools()
}

// Close rejects new work through this attachment and joins work that was already
// registered through it. It does not cancel sibling attachments or logical state.
func (a *SessionHandle) Close(ctx context.Context) (contract.CloseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return contract.CloseAlreadyClosed, nil
	}
	a.closed = true
	a.verifiedTSID = ""
	done := a.operationsDone
	a.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			a.detachLogical()
			return contract.CloseClosed, ctx.Err()
		}
	}
	a.detachLogical()
	return contract.CloseClosed, nil
}

func (a *SessionHandle) beginOperation(parent context.Context) (context.Context, func(), error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, nil, contract.ErrAttachmentClosed
	}
	if a.activeOps == 0 {
		a.operationsDone = make(chan struct{})
	}
	a.activeOps++

	logical := a.logical
	logical.mu.Lock()
	if logical.deleted {
		logical.mu.Unlock()
		a.finishAttachmentOperation()
		a.mu.Unlock()
		return nil, nil, contract.ErrStateUnavailable
	}
	if logical.activeOps == 0 {
		logical.operationsDone = make(chan struct{})
	}
	logical.activeOps++
	operationCtx, cancel := context.WithCancel(logical.operationCtx)
	stop := context.AfterFunc(parent, cancel)
	logical.mu.Unlock()
	a.mu.Unlock()

	var once sync.Once
	return operationCtx, func() {
		once.Do(func() {
			stop()
			cancel()
			logical.mu.Lock()
			logical.activeOps--
			if logical.activeOps == 0 {
				close(logical.operationsDone)
				logical.operationsDone = nil
			}
			logical.maybeCleanupLocked(a.runtime)
			logical.mu.Unlock()
			a.mu.Lock()
			a.finishAttachmentOperation()
			a.mu.Unlock()
		})
	}, nil
}

func (a *SessionHandle) finishAttachmentOperation() {
	a.activeOps--
	if a.activeOps == 0 {
		close(a.operationsDone)
		a.operationsDone = nil
	}
}

type sessionTool struct {
	attachment  *SessionHandle
	route       route
	queryFilter string
}

func (t *sessionTool) Spec() tool.ToolSpec { return copySpec(t.route.spec) }
func (t *sessionTool) ReadOnly() bool      { return t.route.readOnly }

func (t *sessionTool) ExecutionMetadata(session.ToolCall) (contract.ExecutionMetadata, bool) {
	kind := contract.OutboundCredentialNone
	if t.route.broker {
		kind = contract.OutboundCredentialBrokerOAuth
	} else if t.route.oauth != nil {
		kind = contract.OutboundCredentialRouteOAuth
	}
	return contract.ExecutionMetadata{Backend: t.route.backend, OutboundCredentialKind: kind}, t.route.backend != ""
}

// Execute (sessionTool.Execute) invokes the attachment-bound native tool route.
func (t *sessionTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	opCtx, done, err := t.attachment.beginOperation(ctx)
	if err != nil {
		return session.ToolResult{}, err
	}
	defer done()
	if call.Name != t.route.spec.Name {
		return session.NewToolError(call.ID, "broker tool call does not match wrapper"), nil
	}
	resolved, ok := t.attachment.lookupRoute(call.Name)
	if !ok || resolved.backend != t.route.backend {
		t.attachment.runtime.logRouteUnavailable(ctx, t.attachment.logical.ref.SessionID(), diagnosticRouteSurfaceNative)
		return session.NewToolError(call.ID, "broker tool route is unavailable"), nil
	}
	if err := opCtx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	call.Args = append(json.RawMessage(nil), call.Args...)
	if t.route.broker {
		return t.executeBroker(opCtx, call)
	}
	if t.route.oauth != nil {
		return t.executeProtected(opCtx, call)
	}
	result, err := t.invoke(opCtx, call, nil)
	result.CallID = call.ID
	return result, err
}

func (t *sessionTool) invoke(ctx context.Context, call session.ToolCall, tokens oauth2.TokenSource) (session.ToolResult, error) {
	r := t.attachment.runtime
	if t.queryFilter != "" {
		if r.queryCaller == nil {
			return session.ToolResult{}, errors.New("broker query transport unavailable")
		}
		return r.queryCaller(ctx, t.attachment.logical.ref, t.route.backend, call, tokens, t.queryFilter)
	}
	if tokens != nil {
		return r.authorizedCaller(ctx, t.attachment.logical.ref, t.route.backend, call, tokens)
	}
	return r.caller(ctx, t.attachment.logical.ref, t.route.backend, call)
}

func (r *Runtime) logAuthorization(ctx context.Context, sessionID session.SessionID, event, reason string, level port.Level, surface string, extra ...any) {
	args := []any{"event", event, "reason", reason}
	if sessionID != "" {
		args = append(args, "session", string(sessionID))
	}
	if surface != "" {
		args = append(args, "surface", surface)
	}
	args = append(args, extra...)
	r.diag.Log(ctx, level, "mcp broker authorization", args...)
}

func diagnosticOAuthErrorCode(code string) string {
	switch code {
	case "access_denied", "server_error", "temporarily_unavailable", "invalid_request", "unauthorized_client", "unsupported_response_type", "invalid_scope", "interaction_required", "login_required", "account_selection_required", "consent_required":
		return code
	default:
		return "unknown"
	}
}

func authorizationLookupReason(err error) string {
	if errors.Is(err, contract.ErrStateUnavailable) {
		return diagnosticReasonStateUnavailable
	}
	if errors.Is(err, contract.ErrAuthorizationNotFound) {
		return diagnosticReasonAuthorizationNotFound
	}
	return diagnosticReasonAuthorizationLookupFailed
}

func (r *Runtime) logTokenRefresh(ctx context.Context, sessionID session.SessionID, credential, reason string) {
	r.diag.Log(ctx, levelForTokenRefresh(reason), "mcp broker token refresh", "event", diagnosticEventTokenRefresh, "credential", credential, "reason", reason, "session", string(sessionID))
}

func levelForTokenRefresh(reason string) port.Level {
	if reason == diagnosticReasonSucceeded {
		return port.LevelInfo
	}
	return port.LevelWarn
}

func (r *Runtime) logRouteUnavailable(ctx context.Context, sessionID session.SessionID, surface string) {
	r.diag.Log(ctx, port.LevelWarn, "mcp broker route unavailable", "event", diagnosticEventRouteUnavailable, "surface", surface, "session", string(sessionID))
}

func (r *Runtime) logExpectedBindingAttach(ctx context.Context, recovered bool) {
	reason := diagnosticReasonSessionReattached
	if recovered {
		reason = string(contract.AttachRecoveredProvisional)
	}
	r.diag.Log(ctx, port.LevelInfo, "mcp broker expected-binding attach", "event", diagnosticEventSessionAttach, "reason", reason)
}

func (r *Runtime) logSessionAttach(ctx context.Context, id session.SessionID, outcome contract.AttachOutcome) {
	reason := diagnosticReasonSessionReattached
	if outcome == contract.AttachCreated {
		reason = diagnosticReasonSessionCreated
	}
	r.diag.Log(ctx, port.LevelInfo, "mcp broker session attach", "event", diagnosticEventSessionAttach, "reason", reason, "session", string(id))
}

func (r *Runtime) logWorkspaceEnrollment(ctx context.Context, level port.Level, operation, reason string, args ...any) {
	fields := []any{
		"event", diagnosticEventWorkspaceEnrollment,
		"operation", operation,
		"reason", reason,
	}
	fields = append(fields, args...)
	r.diag.Log(ctx, level, "mcp broker workspace enrollment", fields...)
}

func (r *Runtime) logQueryFailure(ctx context.Context, reason string) {
	r.diag.Log(ctx, port.LevelWarn, "mcp broker query failed", "event", diagnosticEventQueryFailure, "reason", reason)
}

func copySpec(spec tool.ToolSpec) tool.ToolSpec {
	spec.Schema = append(json.RawMessage(nil), spec.Schema...)
	return spec
}
