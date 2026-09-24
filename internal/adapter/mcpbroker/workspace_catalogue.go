package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/oauth2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type completedWorkspaceEnrollment struct {
	ref    contract.WorkspaceEnrollmentRef
	routes []route
}

func cloneRoutes(routes []route) []route {
	out := make([]route, len(routes))
	for i, route := range routes {
		out[i] = route
		out[i].spec = copySpec(route.spec)
		if route.oauth != nil {
			copyRoute := *route.oauth
			copyRoute.scopes = append([]string(nil), route.oauth.scopes...)
			out[i].oauth = &copyRoute
		}
	}
	return out
}

func (a *Attachment) installCompletedEnrollment(completed *completedWorkspaceEnrollment) (contract.WorkspaceCatalogue, error) {
	if completed == nil {
		return nil, ErrAuthenticatedDiscovery
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, contract.ErrAttachmentClosed
	}
	if err := a.stateErrorLocked(); err != nil {
		return nil, err
	}
	if a.catalogue != nil && a.catalogue.frozen != nil {
		if a.catalogue.frozen.Ref() != completed.ref {
			return nil, ErrAuthenticatedDiscovery
		}
		return a.catalogue.frozen, nil
	}
	routes := cloneRoutes(completed.routes)
	tools := make([]tool.Tool, 0, len(routes))
	for _, route := range routes {
		base := &sessionTool{attachment: a, route: route}
		if route.oauth != nil {
			tools = append(tools, &protectedSessionTool{sessionTool: base})
		} else {
			tools = append(tools, base)
		}
	}
	frozen, err := contract.NewWorkspaceCatalogue(completed.ref, tools)
	if err != nil {
		return nil, fmt.Errorf("%w: materialize completed catalogue", ErrInvalidCatalogue)
	}
	a.catalogue = newAttachmentCatalogue(routes, frozen.Tools(), frozen)
	return frozen, nil
}

// attachment. It is replaced, never amended: callers can therefore observe
// either the anonymous catalogue or the complete enrolled catalogue.
type attachmentCatalogue struct {
	routes map[string]route
	tools  []tool.Tool
	frozen contract.WorkspaceCatalogue
	// refreshedFor is the zero value until a lazy bundle grant refresh
	// publishes this exact catalogue; it then names the granting transaction.
	// A retry against the SAME granted transaction (e.g. after a downstream
	// session-engine rebuild failure) short-circuits on this rather than
	// re-running live authenticated discovery — which would both waste the
	// round trip and risk publishing a different snapshot than the one the
	// caller already promised for that grant.
	refreshedFor authorizationIdentity
}

func newAttachmentCatalogue(routes []route, tools []tool.Tool, frozen contract.WorkspaceCatalogue) *attachmentCatalogue {
	byName := make(map[string]route, len(routes))
	for _, route := range routes {
		byName[route.spec.Name] = route
	}
	return &attachmentCatalogue{routes: byName, tools: append([]tool.Tool(nil), tools...), frozen: frozen}
}

func (c *attachmentCatalogue) route(name string) (route, bool) {
	if c == nil {
		return route{}, false
	}
	route, ok := c.routes[name]
	return route, ok
}

func (c *attachmentCatalogue) Tools() []tool.Tool {
	if c == nil {
		return nil
	}
	return append([]tool.Tool(nil), c.tools...)
}

// grantedBundle is the exact bundle authorization state read from the logical
// session's locked fields, snapshotted for use after the lock is released.
type grantedBundle struct {
	transaction *authorizationTransaction
	backends    []string
	grant       *oauthGrant
}

// resolveGrantedBundle looks up the exact granted authorization and returns
// its bundle backends and broker credential under the logical session lock.
// unchanged reports a valid non-bundle grant, for which the caller must
// return the existing catalogue without staging anything.
func resolveGrantedBundle(logical *logicalSession, authorization session.ExternalAuthorization) (bundle grantedBundle, unchanged bool, err error) {
	logical.mu.Lock()
	defer logical.mu.Unlock()
	transaction, err := lookupAuthorization(logical, authorization)
	if err != nil || transaction.status != session.AuthorizationGranted {
		return grantedBundle{}, false, contract.ErrAuthorizationNotFound
	}
	if transaction.bundleBackends == nil {
		return grantedBundle{}, true, nil
	}
	grant := logical.brokerCredential
	if grant == nil {
		return grantedBundle{}, false, contract.ErrAuthorizationNotFound
	}
	return grantedBundle{
		transaction: transaction,
		backends:    append([]string(nil), transaction.bundleBackends...),
		grant:       grant,
	}, false, nil
}

// buildRefreshedCatalogue stages every configured backend's live metadata and
// assembles the candidate attachment catalogue that replaces the static
// declarations, without publishing it.
func (a *Attachment) buildRefreshedCatalogue(ctx context.Context, logical *logicalSession, process *Process, backends []string) (*attachmentCatalogue, error) {
	stagedRoutes, err := stageAuthenticatedDeclaredRoutes(ctx, process, &brokerTokenSource{runtime: a.runtime, logical: logical, ctx: ctx}, backends, a.catalogue, process.reservedToolNames)
	if err != nil {
		return nil, err
	}
	allRoutes := make([]route, 0, len(a.catalogue.routes)+len(stagedRoutes))
	for _, route := range a.catalogue.routes {
		// Static protected routes are replaced; prior broker routes are likewise
		// replaced if this exact operation is retried after publication.
		if route.oauth == nil && !route.broker {
			allRoutes = append(allRoutes, route)
		}
	}
	allRoutes = append(allRoutes, stagedRoutes...)
	sortRoutes(allRoutes)
	allTools := make([]tool.Tool, 0, len(allRoutes))
	for _, route := range allRoutes {
		base := &sessionTool{attachment: a, route: route}
		if route.oauth != nil {
			allTools = append(allTools, &protectedSessionTool{sessionTool: base})
		} else {
			allTools = append(allTools, base)
		}
	}
	return newAttachmentCatalogue(allRoutes, allTools, nil), nil
}

// publishRefreshedCatalogue installs candidate only if the granting
// authorization, broker credential, and process/attachment lifecycle are all
// still exactly what they were when candidate was built. Holding the grant
// and process lifecycle locks through the assignment gives close/deletion and
// publication one winner.
func (a *Attachment) publishRefreshedCatalogue(logical *logicalSession, process *Process, bundle grantedBundle, candidate *attachmentCatalogue) bool {
	logical.mu.Lock()
	defer logical.mu.Unlock()
	process.lifecycleMu.Lock()
	defer process.lifecycleMu.Unlock()
	valid := !logical.deleted && logical.authorizations[bundle.transaction.identity] == bundle.transaction &&
		bundle.transaction.status == session.AuthorizationGranted && logical.brokerCredential == bundle.grant &&
		!process.closed && process.Runtime == a.runtime && !a.closed
	if valid {
		candidate.refreshedFor = bundle.transaction.identity
		a.catalogue = candidate
	}
	return valid
}

// RefreshGrantedAuthorizationCatalogue replaces the static protected declarations
// with admitted live metadata after the exact lazy bundle authorization succeeds.
// A valid non-bundle grant returns the unchanged catalogue. Bundle publication
// happens only after every configured backend has been queried successfully;
// authenticated tools without declarations remain hidden.
func (a *Attachment) RefreshGrantedAuthorizationCatalogue(ctx context.Context, authorization session.ExternalAuthorization) ([]tool.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, ErrAuthenticatedDiscovery
	}
	opCtx, done, err := a.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, contract.ErrAttachmentClosed
	}
	if err := a.stateErrorLocked(); err != nil {
		return nil, err
	}
	if a.catalogue == nil {
		return nil, ErrAuthenticatedDiscovery
	}
	logical := a.logical
	bundle, unchanged, err := resolveGrantedBundle(logical, authorization)
	if err != nil {
		return nil, err
	}
	if unchanged {
		return a.catalogue.Tools(), nil
	}
	if a.catalogue.refreshedFor != (authorizationIdentity{}) && a.catalogue.refreshedFor == bundle.transaction.identity {
		// A retry for the exact same granted transaction (e.g. a caller whose
		// downstream session-engine rebuild failed and restored the claim for
		// another attempt): reuse the already-published, already-validated
		// snapshot rather than re-running live authenticated discovery, which
		// could return different metadata on a second call.
		return a.catalogue.Tools(), nil
	}

	process := a.runtime.process
	backends, _, ok := process.catalogueInputs(a.runtime)
	if !ok || len(backends) == 0 || !slices.Equal(bundle.backends, backends) {
		return nil, ErrAuthenticatedDiscovery
	}
	if a.catalogue.frozen != nil {
		return a.catalogue.Tools(), nil
	}

	candidate, err := a.buildRefreshedCatalogue(opCtx, logical, process, backends)
	if err != nil {
		return nil, err
	}
	if !a.publishRefreshedCatalogue(logical, process, bundle, candidate) {
		return nil, ErrAuthenticatedDiscovery
	}
	return candidate.Tools(), nil
}

// FreezeAuthenticatedCatalogue stages the configured protected backends in
// their configured order and publishes one immutable attachment catalogue only
// after every backend has supplied valid live metadata. reservedToolNames is the complete
// model-visible name set outside this attachment catalogue (core/global tools).
// brokerCredential is opaque and is passed only through ToolHive's incoming
// identity middleware.
func (a *Attachment) FreezeAuthenticatedCatalogue(ctx context.Context, ref contract.WorkspaceEnrollmentRef, process *Process, brokerCredential oauth2.TokenSource, reservedToolNames []string) (contract.WorkspaceCatalogue, error) {
	frozen, _, err := a.freezeAuthenticatedCatalogue(ctx, ref, process, brokerCredential, reservedToolNames, true)
	return frozen, err
}

// freezeAuthenticatedCatalogue builds a complete immutable catalogue. Enrollment
// uses publish=false so the catalogue remains private until its logical commit.
//
//nolint:gocyclo // every early-return guards a distinct precondition (closed, stale ref, process authority, publish race); splitting would scatter the single freeze/publish invariant
func (a *Attachment) freezeAuthenticatedCatalogue(ctx context.Context, ref contract.WorkspaceEnrollmentRef, process *Process, brokerCredential oauth2.TokenSource, reservedToolNames []string, publish bool) (contract.WorkspaceCatalogue, *attachmentCatalogue, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, nil, contract.ErrAttachmentClosed
	}
	if a.catalogue != nil && a.catalogue.frozen != nil {
		if a.catalogue.frozen.Ref() == ref {
			frozen, catalogue := a.catalogue.frozen, a.catalogue
			a.mu.Unlock()
			return frozen, catalogue, nil
		}
		a.mu.Unlock()
		return nil, nil, ErrAuthenticatedDiscovery
	}
	if err := a.stateErrorLocked(); err != nil {
		a.mu.Unlock()
		return nil, nil, err
	}

	backends, anonymous, ok := process.catalogueInputs(a.runtime)
	if !ok || len(backends) == 0 {
		a.mu.Unlock()
		return nil, nil, ErrAuthenticatedDiscovery
	}
	base := a.catalogue
	if base == nil {
		a.mu.Unlock()
		return nil, nil, ErrAuthenticatedDiscovery
	}
	if wait := a.freezing; wait != nil {
		// Single flight: another freeze is running discovery. Wait for it, then
		// re-evaluate from the top (it returns the published catalogue or retries).
		a.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		return a.freezeAuthenticatedCatalogue(ctx, ref, process, brokerCredential, reservedToolNames, publish)
	}
	done := make(chan struct{})
	a.freezing = done
	defer func() {
		a.mu.Lock()
		if a.freezing == done {
			a.freezing = nil
		}
		a.mu.Unlock()
		close(done)
	}()

	a.mu.Unlock()
	stagedRoutes, verifiedTSID, err := stageAuthenticatedRoutes(ctx, a, process, brokerCredential, backends, base, reservedToolNames)
	if err != nil {
		reason := diagnosticReasonDiscoveryFailed
		if errors.Is(err, ErrInvalidCatalogue) {
			reason = diagnosticReasonValidationFailed
		}
		process.diagnostics().Log(ctx, port.LevelWarn, "mcp broker authenticated catalogue freeze", "event", diagnosticEventAuthenticatedCatalogueFreeze, "reason", reason)
		return nil, nil, err
	}

	sortRoutes(stagedRoutes)
	allRoutes := make([]route, 0, len(anonymous)+len(stagedRoutes))
	allRoutes = append(allRoutes, anonymous...)
	allRoutes = append(allRoutes, stagedRoutes...)
	allTools := make([]tool.Tool, 0, len(allRoutes))
	for _, route := range allRoutes {
		base := &sessionTool{attachment: a, route: route}
		if route.oauth != nil {
			allTools = append(allTools, &protectedSessionTool{sessionTool: base})
		} else {
			allTools = append(allTools, base)
		}
	}
	frozen, err := contract.NewWorkspaceCatalogue(ref, allTools)
	if err != nil {
		process.diagnostics().Log(ctx, port.LevelWarn, "mcp broker authenticated catalogue freeze", "event", diagnosticEventAuthenticatedCatalogueFreeze, "reason", diagnosticReasonMaterializeFailed)
		return nil, nil, fmt.Errorf("%w: freeze attachment catalogue", ErrInvalidCatalogue)
	}

	// A Process may close while a query returns. Do not publish a catalogue whose
	// process no longer owns its discovery authority.
	a.mu.Lock()
	defer a.mu.Unlock()
	if !process.catalogueStillAvailable(a.runtime) || a.closed || a.catalogue != base {
		process.diagnostics().Log(ctx, port.LevelWarn, "mcp broker authenticated catalogue freeze", "event", diagnosticEventAuthenticatedCatalogueFreeze, "reason", diagnosticReasonAuthorityUnavailable)
		return nil, nil, ErrAuthenticatedDiscovery
	}
	if verifiedTSID != "" {
		a.verifiedTSID = verifiedTSID
	}
	candidate := newAttachmentCatalogue(allRoutes, frozen.Tools(), frozen)
	if publish {
		a.catalogue = candidate
	}
	process.diagnostics().Log(ctx, port.LevelInfo, "mcp broker authenticated catalogue freeze", "event", diagnosticEventAuthenticatedCatalogueFreeze, "reason", diagnosticReasonSucceeded, "routes", len(allRoutes))
	return frozen, candidate, nil
}

func (a *Attachment) stateErrorLocked() error {
	a.logical.mu.RLock()
	deleted := a.logical.deleted
	a.logical.mu.RUnlock()
	if deleted {
		return contract.ErrStateUnavailable
	}
	return nil
}

func stageAuthenticatedDeclaredRoutes(ctx context.Context, process *Process, brokerCredential oauth2.TokenSource, backends []string, base *attachmentCatalogue, occupied []string) ([]route, error) {
	declared := make(map[string]string)
	for _, route := range process.Runtime.catalogue.routes {
		if route.oauth != nil && route.broker {
			declared[route.spec.Name] = route.backend
		}
	}
	seen := make(map[string]struct{}, len(occupied)+len(base.routes))
	for _, name := range occupied {
		if name == "" {
			return nil, ErrInvalidCatalogue
		}
		seen[name] = struct{}{}
	}
	for _, route := range base.routes {
		if route.oauth == nil && !route.broker {
			seen[route.spec.Name] = struct{}{}
		}
	}
	staged := make([]route, 0, len(declared))
	for _, backend := range backends {
		capabilities, err := process.QueryAuthenticatedCapabilities(ctx, brokerCredential, backend)
		if err != nil || capabilities.Backend != backend {
			process.diagnostics().Log(ctx, port.LevelWarn, "authenticated discovery failed; catalogue freeze aborted", "backend", backend)
			return nil, ErrAuthenticatedDiscovery
		}
		process.diagnostics().Log(ctx, port.LevelInfo, "authenticated discovery succeeded", "backend", backend, "tools", len(capabilities.Tools))
		for _, definition := range capabilities.Tools {
			// Every discovered definition passes the shared admission boundary —
			// qualified name, UTF-8, size, JSON-object schema, private material,
			// and collision — before the declared-name membership rule decides
			// whether it is staged. An invalid or oversized undeclared definition
			// must abort the whole freeze, not be silently dropped.
			route, err := validateAuthenticatedRoute(backend, definition, seen)
			if err != nil {
				return nil, err
			}
			seen[route.spec.Name] = struct{}{}
			if declaredBackend, isDeclared := declared[definition.Name]; !isDeclared || declaredBackend != backend {
				continue
			}
			route.broker = true
			staged = append(staged, route)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return staged, nil
}

func stageAuthenticatedRoutes(ctx context.Context, _ *Attachment, process *Process, brokerCredential oauth2.TokenSource, backends []string, base *attachmentCatalogue, reservedToolNames []string) ([]route, string, error) {
	// Runs without the attachment lock so upstream I/O never blocks other
	// operations on the handle. The caller re-locks and exact-compares the base
	// catalogue before publishing, so a stale result can never be installed and
	// Tools cannot expose a partly staged catalogue.
	seen := make(map[string]struct{}, len(reservedToolNames)+len(base.routes))
	for _, name := range reservedToolNames {
		if name == "" {
			return nil, "", ErrInvalidCatalogue
		}
		seen[name] = struct{}{}
	}
	for _, route := range base.routes {
		if route.oauth == nil {
			seen[route.spec.Name] = struct{}{}
		}
	}
	staged := make([]route, 0)
	verifiedTSID := ""
	for backendIndex, backend := range backends {
		capabilities, err := process.QueryAuthenticatedCapabilities(ctx, brokerCredential, backend)
		if err != nil || capabilities.Backend != backend {
			// The underlying cause is deliberately not distinguishable beyond this
			// point (authenticated_discovery.go collapses every failure mode —
			// unauthenticated, transport, upstream-error, backend-mismatch — into
			// ErrAuthenticatedDiscovery, a single admission boundary, on purpose).
			// This is still the one place an operator can locate WHICH configured
			// backend position broke a catalogue freeze that otherwise fails
			// all-or-nothing, without logging its configured name.
			process.diagnostics().Log(ctx, port.LevelWarn, "mcp broker authenticated catalogue backend", "event", diagnosticEventAuthenticatedCatalogueBackend, "reason", diagnosticReasonDiscoveryFailed, "backend_index", backendIndex)
			return nil, "", ErrAuthenticatedDiscovery
		}
		if process.discovery != nil && process.discovery.captureTSID {
			if capabilities.verifiedTSID == "" || (verifiedTSID != "" && capabilities.verifiedTSID != verifiedTSID) {
				return nil, "", ErrAuthenticatedDiscovery
			}
			verifiedTSID = capabilities.verifiedTSID
		}
		process.diagnostics().Log(ctx, port.LevelInfo, "mcp broker authenticated catalogue backend", "event", diagnosticEventAuthenticatedCatalogueBackend, "reason", diagnosticReasonDiscovered, "backend_index", backendIndex, "tools", len(capabilities.Tools))
		for _, definition := range capabilities.Tools {
			route, err := validateAuthenticatedRoute(backend, definition, seen)
			if err != nil {
				return nil, "", err
			}
			// Execution reuses the one outer ToolHive broker credential. The
			// wrapper intentionally has no oauth route, so the agent loop cannot
			// open a second mecatl authorization flow for this backend.
			route.broker = true
			seen[route.spec.Name] = struct{}{}
			staged = append(staged, route)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if process.discovery != nil && process.discovery.captureTSID {
		if verifiedTSID == "" {
			return nil, "", ErrAuthenticatedDiscovery
		}
	}
	return staged, verifiedTSID, nil
}

// validateAuthenticatedRoute is the single admission boundary for protected
// metadata, whether obtained by authenticated discovery or trusted static declaration.
func validateAuthenticatedRoute(backend string, definition ToolDefinition, seen map[string]struct{}) (route, error) {
	prefix := "mcp__" + backend + "__"
	if backend == "" || definition.Backend != backend || !strings.HasPrefix(definition.Name, prefix) {
		return route{}, ErrInvalidCatalogue
	}
	name := strings.TrimPrefix(definition.Name, prefix)
	if !validDiscoveredToolName(name) || !utf8.ValidString(definition.Description) || len(definition.Description) > 64<<10 || len(definition.Schema) == 0 || len(definition.Schema) > 1<<20 || !json.Valid(definition.Schema) {
		return route{}, ErrInvalidCatalogue
	}
	var schema map[string]any
	if err := json.Unmarshal(definition.Schema, &schema); err != nil || schema == nil {
		return route{}, ErrInvalidCatalogue
	}
	if _, collision := seen[definition.Name]; collision {
		return route{}, ErrInvalidCatalogue
	}
	return route{backend: backend, spec: tool.ToolSpec{Name: definition.Name, Description: definition.Description, Schema: append(json.RawMessage(nil), definition.Schema...)}, readOnly: definition.ReadOnly}, nil
}

func (p *Process) catalogueInputs(runtime *Runtime) ([]string, []route, bool) {
	if p == nil || runtime == nil {
		return nil, nil, false
	}
	p.lifecycleMu.Lock()
	closed := p.closed
	backends := append([]string(nil), p.construction.protectedBackends...)
	p.lifecycleMu.Unlock()
	if closed || p.Runtime != runtime {
		return nil, nil, false
	}
	routes := make([]route, 0, len(runtime.catalogue.routes))
	for _, route := range runtime.catalogue.routes {
		if route.oauth == nil {
			routes = append(routes, route)
		}
	}
	return backends, routes, true
}

func (p *Process) catalogueStillAvailable(runtime *Runtime) bool {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	return !p.closed && p.Runtime == runtime
}

// lookupRoute is the execution-side route identity lookup. It never scans
// wrappers or relies on their concrete types.
func (a *Attachment) lookupRoute(name string) (route, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.catalogue.route(name)
}
