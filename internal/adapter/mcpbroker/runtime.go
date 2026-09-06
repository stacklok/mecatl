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

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

var (
	// ErrInvalidCatalogue reports an invalid declaration, discovery result, or
	// model-visible tool-name collision.
	ErrInvalidCatalogue = errors.New("mcpbroker: invalid catalogue")
	// ErrProtectedRouteUnsupported reports the P08 boundary: OAuth routes are
	// declared by P07 but are not executable until protected-route custody lands.
	ErrProtectedRouteUnsupported = errors.New("mcpbroker: protected route unsupported")
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
	// broker marks a ToolHive-routed capability admitted only after the
	// session's aggregate workspace enrollment completed. It deliberately does
	// not implement tool.AuthorizationRequester: the outer broker credential is
	// reused without creating a backend-specific mecatl OAuth flow.
	broker bool
}

// Catalogue is an immutable compiled broker catalogue. Specs deliberately
// expose no backend route or execution metadata.
type Catalogue struct {
	routes []route
}

// Compile validates anonymous P07 declarations and neutral discovery results.
// occupied contains names already visible to the model; collisions are rejected
// before any session attachment is created.
func Compile(config mcpauthority.BrokerConfig, discovered []ToolDefinition, occupied []string) (*Catalogue, error) {
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

	seen := make(map[string]struct{}, len(occupied)+len(discovered))
	for _, name := range occupied {
		if name == "" {
			return nil, fmt.Errorf("%w: occupied tool name is empty", ErrInvalidCatalogue)
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

// SessionRef is an opaque reference to one in-process logical-session incarnation.
// SessionID is exposed for backend attribution; the incarnation remains private.
type SessionRef struct {
	id         session.SessionID
	generation uint64
}

// SessionID returns the canonical mecatl session identity without exposing its incarnation.
func (r SessionRef) SessionID() session.SessionID { return r.id }

type logicalSession struct {
	mu                  sync.RWMutex
	ref                 SessionRef
	deleted             bool
	operationCtx        context.Context
	cancelOps           context.CancelFunc
	activeOps           int
	operationsDone      chan struct{}
	provisional         bool
	cleaned             bool
	cleanupStatus       session.AuthorizationStatus
	authorizations      map[authorizationIdentity]*authorizationTransaction
	grants              map[string]*oauthGrant
	brokerCredential    *oauthGrant
	completedEnrollment *completedWorkspaceEnrollment
}

// Caller is the private execution seam used by the in-process transport. The
// opaque session incarnation and configured backend are passed separately from
// model-controlled arguments.
type Caller func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error)

// AuthorizedCaller is the protected-route execution seam. The token source is
// scoped to the exact logical session and route and retains refresh custody.
type AuthorizedCaller func(context.Context, SessionRef, string, session.ToolCall, oauth2.TokenSource) (session.ToolResult, error)

// Runtime owns logical broker sessions and creates process-local attachments.
type Runtime struct {
	mu               sync.RWMutex
	stateMu          sync.Mutex
	catalogue        *Catalogue
	caller           Caller
	authorizedCaller AuthorizedCaller
	oauth            oauthRuntimeOptions
	sessions         map[session.SessionID]*logicalSession
	states           map[string]callbackState
	nextGeneration   uint64
	bindingPrefix    string
	closed           bool
	drainSessions    []*logicalSession
	// process is set only when this Runtime is owned by a bundled ToolHive
	// Process (NewToolHiveProcess). It lets an Attachment reach the pre-prompt
	// authenticated-discovery primitives without widening the neutral contract.
	// nil for a plain Compile-based Runtime, which never supports workspace
	// enrollment.
	process *Process
}

var _ contract.Service = (*Runtime)(nil)

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
		catalogue:     catalogue,
		caller:        caller,
		oauth:         defaultOAuthRuntimeOptions(),
		sessions:      make(map[session.SessionID]*logicalSession),
		states:        make(map[string]callbackState),
		bindingPrefix: base64.RawURLEncoding.EncodeToString(bindingSeed),
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
	return runtime, nil
}

// AttachSession creates or reattaches to logical state keyed by the canonical
// mecatl session ID. Each call returns an independently closeable local handle.
func (r *Runtime) AttachSession(ctx context.Context, id session.SessionID) (contract.Attachment, contract.AttachOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if id == "" {
		return nil, "", fmt.Errorf("%w: session ID is required", ErrInvalidCatalogue)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, "", contract.ErrStateUnavailable
	}
	logical, exists := r.sessions[id]
	outcome := contract.AttachReattached
	if !exists {
		r.nextGeneration++
		operationCtx, cancelOps := context.WithCancel(context.Background())
		logical = &logicalSession{
			ref:            SessionRef{id: id, generation: r.nextGeneration},
			operationCtx:   operationCtx,
			cancelOps:      cancelOps,
			provisional:    true,
			authorizations: make(map[authorizationIdentity]*authorizationTransaction),
			grants:         make(map[string]*oauthGrant),
		}
		r.sessions[id] = logical
		outcome = contract.AttachCreated
	} else {
		// Observation by an independent attachment publishes a provisional
		// creation. Its creator may still Abort its own handle, but can no longer
		// invalidate state another client has acquired.
		logical.mu.Lock()
		logical.provisional = false
		logical.mu.Unlock()
	}
	r.mu.Unlock()

	attachment := &Attachment{runtime: r, logical: logical, creator: outcome == contract.AttachCreated}
	tools := make([]tool.Tool, len(r.catalogue.routes))
	for i, route := range r.catalogue.routes {
		base := &sessionTool{attachment: attachment, route: route}
		if route.oauth != nil {
			tools[i] = &protectedSessionTool{sessionTool: base}
		} else {
			tools[i] = base
		}
	}
	attachment.catalogue = newAttachmentCatalogue(r.catalogue.routes, tools, nil)
	logical.mu.RLock()
	completed := logical.completedEnrollment
	logical.mu.RUnlock()
	if completed != nil {
		if _, err := attachment.installCompletedEnrollment(completed); err != nil {
			return nil, "", err
		}
	}
	return attachment, outcome, nil
}

// DeleteSession logically deletes one broker session and invalidates all handles
// to that incarnation. Reattachment later creates a fresh incarnation.
func (r *Runtime) DeleteSession(ctx context.Context, id session.SessionID) (contract.DeleteOutcome, error) {
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

// Attachment is a local handle to a logical broker session. Tools is an
// adapter-specific projection used by composition; the P06 lifecycle methods
// satisfy the neutral contract.
type Attachment struct {
	mu             sync.RWMutex
	enrollmentMu   sync.Mutex
	runtime        *Runtime
	logical        *logicalSession
	closed         bool
	creator        bool
	settled        bool
	activeOps      int
	operationsDone chan struct{}
	catalogue      *attachmentCatalogue
}

var _ contract.Attachment = (*Attachment)(nil)

// Commit publishes this attachment's private creation. Reattached attachments
// have already published the logical session by observing it, so Commit is a
// harmless idempotent settlement for them.
func (a *Attachment) Commit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settled {
		return nil
	}
	if a.closed {
		return contract.ErrAttachmentClosed
	}
	if a.creator {
		a.runtime.mu.Lock()
		a.logical.mu.Lock()
		current := a.runtime.sessions[a.logical.ref.id]
		if a.runtime.closed || current != a.logical || a.logical.deleted {
			a.logical.mu.Unlock()
			a.runtime.mu.Unlock()
			return contract.ErrStateUnavailable
		}
		a.logical.provisional = false
		a.logical.mu.Unlock()
		a.runtime.mu.Unlock()
	} else {
		a.runtime.mu.RLock()
		available := !a.runtime.closed && a.runtime.sessions[a.logical.ref.id] == a.logical
		a.runtime.mu.RUnlock()
		a.logical.mu.RLock()
		available = available && !a.logical.deleted
		a.logical.mu.RUnlock()
		if !available {
			return contract.ErrStateUnavailable
		}
	}
	a.settled = true
	return nil
}

// Abort closes this attachment and conditionally rolls back only a still-private
// creation. A peer attachment publishes the logical session at reattachment, so
// aborting the creator can never invalidate an observed peer.
func (a *Attachment) Abort(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	rollback := a.creator && !a.settled
	a.closed = true
	a.settled = true
	done := a.operationsDone
	if rollback {
		a.runtime.mu.Lock()
		a.logical.mu.Lock()
		deleted := false
		if a.runtime.sessions[a.logical.ref.id] == a.logical && a.logical.provisional {
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
			return ctx.Err()
		}
	}
	return nil
}

// Binding returns the opaque identity of this logical-session incarnation.
func (a *Attachment) Binding() session.ExternalBinding {
	return session.ExternalBinding(a.runtime.bindingPrefix + "." + fmt.Sprint(a.logical.ref.generation))
}

// Tools returns a copy of the attachment's current whole catalogue. Publication
// replaces the catalogue in one assignment, so callers cannot observe staged
// protected tools.
func (a *Attachment) Tools() []tool.Tool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.catalogue.Tools()
}

// Close rejects new work through this attachment and joins work that was already
// registered through it. It does not cancel sibling attachments or logical state.
func (a *Attachment) Close(ctx context.Context) (contract.CloseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return contract.CloseAlreadyClosed, nil
	}
	a.closed = true
	done := a.operationsDone
	a.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return contract.CloseClosed, ctx.Err()
		}
	}
	return contract.CloseClosed, nil
}

func (a *Attachment) beginOperation(parent context.Context) (context.Context, func(), error) {
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

func (a *Attachment) finishAttachmentOperation() {
	a.activeOps--
	if a.activeOps == 0 {
		close(a.operationsDone)
		a.operationsDone = nil
	}
}

type sessionTool struct {
	attachment *Attachment
	route      route
}

func (t *sessionTool) Spec() tool.ToolSpec { return copySpec(t.route.spec) }
func (t *sessionTool) ReadOnly() bool      { return t.route.readOnly }

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
		return session.NewToolError(call.ID, "broker tool route is unavailable"), nil
	}
	if err := opCtx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	call.Args = append(json.RawMessage(nil), call.Args...)
	if t.route.oauth != nil {
		return t.executeProtected(opCtx, call)
	}
	if t.route.broker {
		return t.executeBroker(opCtx, call)
	}
	result, err := t.attachment.runtime.caller(opCtx, t.attachment.logical.ref, t.route.backend, call)
	result.CallID = call.ID
	return result, err
}

func copySpec(spec tool.ToolSpec) tool.ToolSpec {
	spec.Schema = append(json.RawMessage(nil), spec.Schema...)
	return spec
}
