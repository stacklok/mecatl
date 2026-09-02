package mcpbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/tool"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// attachmentCatalogue is the one authoritative route identity source for an
// attachment. It is replaced, never amended: callers can therefore observe
// either the anonymous catalogue or the complete enrolled catalogue.
type attachmentCatalogue struct {
	routes map[string]route
	tools  []tool.Tool
	frozen contract.WorkspaceCatalogue
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

// FreezeAuthenticatedCatalogue stages the configured protected backends in
// their configured order and publishes one immutable attachment catalogue only
// after every backend has supplied valid live metadata. occupied is the complete
// model-visible name set outside this attachment catalogue (core/global tools).
// authSession is opaque and is passed only to the concrete ToolHive process.
func (a *Attachment) FreezeAuthenticatedCatalogue(ctx context.Context, ref contract.WorkspaceEnrollmentRef, process *Process, authSession ToolHiveAuthSessionID, occupied []string) (contract.WorkspaceCatalogue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || process == nil || authSession == "" || !ref.Valid() {
		return nil, ErrAuthenticatedDiscovery
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, contract.ErrAttachmentClosed
	}
	if a.catalogue != nil && a.catalogue.frozen != nil {
		if a.catalogue.frozen.Ref() == ref {
			return a.catalogue.frozen, nil
		}
		return nil, ErrAuthenticatedDiscovery
	}
	if err := a.stateErrorLocked(); err != nil {
		return nil, err
	}

	backends, anonymous, ok := process.catalogueInputs(a.runtime)
	if !ok || len(backends) == 0 {
		return nil, ErrAuthenticatedDiscovery
	}
	base := a.catalogue
	if base == nil {
		return nil, ErrAuthenticatedDiscovery
	}

	stagedRoutes, err := stageAuthenticatedRoutes(ctx, process, authSession, backends, base, occupied)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%w: freeze attachment catalogue", ErrInvalidCatalogue)
	}

	// A Process may close while a query returns. Do not publish a catalogue whose
	// process no longer owns its discovery authority.
	if !process.catalogueStillAvailable(a.runtime) || a.closed {
		return nil, ErrAuthenticatedDiscovery
	}
	a.catalogue = newAttachmentCatalogue(allRoutes, frozen.Tools(), frozen)
	return frozen, nil
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

func stageAuthenticatedRoutes(ctx context.Context, process *Process, authSession ToolHiveAuthSessionID, backends []string, base *attachmentCatalogue, occupied []string) ([]route, error) {
	// The attachment lock remains held by FreezeAuthenticatedCatalogue throughout
	// this work. Cancel/close races have one winner, and Tools cannot expose a
	// partly staged catalogue.
	seen := make(map[string]struct{}, len(occupied)+len(base.routes))
	for _, name := range occupied {
		if name == "" {
			return nil, ErrInvalidCatalogue
		}
		seen[name] = struct{}{}
	}
	for name := range base.routes {
		seen[name] = struct{}{}
	}
	staged := make([]route, 0)
	for _, backend := range backends {
		capabilities, err := process.QueryAuthenticatedCapabilities(ctx, authSession, backend)
		if err != nil || capabilities.Backend != backend {
			return nil, ErrAuthenticatedDiscovery
		}
		for _, definition := range capabilities.Tools {
			route, err := validateAuthenticatedRoute(backend, definition, seen)
			if err != nil {
				return nil, err
			}
			seen[route.spec.Name] = struct{}{}
			staged = append(staged, route)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return staged, nil
}

// validateAuthenticatedRoute is the single admission boundary for live
// protected metadata. Static declarations are intentionally not consulted.
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
	return backends, append([]route(nil), runtime.catalogue.routes...), true
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
