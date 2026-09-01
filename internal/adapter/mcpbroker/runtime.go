// Package mcpbroker implements the in-process, session-scoped MCP broker.
// Model-facing catalogues contain only neutral tool specifications; backend
// routing remains private to this adapter.
package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
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
	backends := make(map[string]string, len(config.Routes))
	for _, declaration := range config.Routes {
		key := strings.ToLower(declaration.Name)
		if key == "" {
			return nil, fmt.Errorf("%w: route name is required", ErrInvalidCatalogue)
		}
		if _, exists := backends[key]; exists {
			return nil, fmt.Errorf("%w: duplicate route %q", ErrInvalidCatalogue, declaration.Name)
		}
		if declaration.Auth.Mode != "none" {
			return nil, fmt.Errorf("%w: route %q uses auth mode %q", ErrProtectedRouteUnsupported, declaration.Name, declaration.Auth.Mode)
		}
		backends[key] = declaration.Name
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
		backend, configured := backends[strings.ToLower(definition.Backend)]
		if !configured {
			return nil, fmt.Errorf("%w: discovery references unconfigured route %q", ErrInvalidCatalogue, definition.Backend)
		}
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
		routes = append(routes, route{
			backend: backend,
			spec: tool.ToolSpec{
				Name:        definition.Name,
				Description: definition.Description,
				Schema:      append(json.RawMessage(nil), definition.Schema...),
			},
			readOnly: definition.ReadOnly,
		})
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

func (r SessionRef) SessionID() session.SessionID { return r.id }

type logicalSession struct {
	mu      sync.RWMutex
	ref     SessionRef
	deleted bool
}

// Caller is the private execution seam used by the in-process transport. The
// opaque session incarnation and configured backend are passed separately from
// model-controlled arguments.
type Caller func(context.Context, SessionRef, string, session.ToolCall) (session.ToolResult, error)

// Runtime owns logical broker sessions and creates process-local attachments.
type Runtime struct {
	mu             sync.RWMutex
	catalogue      *Catalogue
	caller         Caller
	sessions       map[session.SessionID]*logicalSession
	nextGeneration uint64
}

var _ contract.Service = (*Runtime)(nil)

// New constructs an in-process anonymous-route broker.
func New(catalogue *Catalogue, caller Caller) (*Runtime, error) {
	if catalogue == nil {
		return nil, fmt.Errorf("%w: catalogue is required", ErrInvalidCatalogue)
	}
	if caller == nil {
		return nil, fmt.Errorf("%w: caller is required", ErrInvalidCatalogue)
	}
	return &Runtime{catalogue: catalogue, caller: caller, sessions: make(map[session.SessionID]*logicalSession)}, nil
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
	logical, exists := r.sessions[id]
	outcome := contract.AttachReattached
	if !exists {
		r.nextGeneration++
		logical = &logicalSession{ref: SessionRef{id: id, generation: r.nextGeneration}}
		r.sessions[id] = logical
		outcome = contract.AttachCreated
	}
	r.mu.Unlock()

	attachment := &Attachment{runtime: r, logical: logical}
	attachment.tools = make([]tool.Tool, len(r.catalogue.routes))
	for i, route := range r.catalogue.routes {
		attachment.tools[i] = &sessionTool{attachment: attachment, route: route}
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
	logical, exists := r.sessions[id]
	if !exists {
		r.mu.Unlock()
		return contract.DeleteNotFound, nil
	}
	delete(r.sessions, id)
	r.mu.Unlock()

	logical.mu.Lock()
	logical.deleted = true
	logical.mu.Unlock()
	return contract.DeleteDeleted, nil
}

// Attachment is a local handle to a logical broker session. Tools is an
// adapter-specific projection used by composition; the P06 lifecycle methods
// satisfy the neutral contract.
type Attachment struct {
	mu      sync.RWMutex
	runtime *Runtime
	logical *logicalSession
	closed  bool
	tools   []tool.Tool
}

var _ contract.Attachment = (*Attachment)(nil)

// Tools returns a copy of this attachment's stable session-bound wrappers.
func (a *Attachment) Tools() []tool.Tool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]tool.Tool(nil), a.tools...)
}

func (a *Attachment) stateError() error {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return contract.ErrAttachmentClosed
	}
	a.logical.mu.RLock()
	deleted := a.logical.deleted
	a.logical.mu.RUnlock()
	if deleted {
		return contract.ErrStateUnavailable
	}
	return nil
}

// PresentAuthorization has no authorization state for anonymous routes.
func (a *Attachment) PresentAuthorization(ctx context.Context, _ session.ExternalAuthorization) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := a.stateError(); err != nil {
		return "", err
	}
	return "", contract.ErrAuthorizationNotFound
}

// AuthorizationStatus has no authorization state for anonymous routes.
func (a *Attachment) AuthorizationStatus(ctx context.Context, _ session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := a.stateError(); err != nil {
		return "", err
	}
	return "", contract.ErrAuthorizationNotFound
}

// CancelAuthorization has no authorization state for anonymous routes.
func (a *Attachment) CancelAuthorization(ctx context.Context, _ session.ExternalAuthorization) (contract.CancelOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := a.stateError(); err != nil {
		return "", err
	}
	return "", contract.ErrAuthorizationNotFound
}

// Close releases only this process-local attachment.
func (a *Attachment) Close(ctx context.Context) (contract.CloseOutcome, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return contract.CloseAlreadyClosed, nil
	}
	a.closed = true
	return contract.CloseClosed, nil
}

type sessionTool struct {
	attachment *Attachment
	route      route
}

func (t *sessionTool) Spec() tool.ToolSpec { return copySpec(t.route.spec) }
func (t *sessionTool) ReadOnly() bool      { return t.route.readOnly }

func (t *sessionTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.attachment.mu.RLock()
	defer t.attachment.mu.RUnlock()
	if t.attachment.closed {
		return session.ToolResult{}, contract.ErrAttachmentClosed
	}
	t.attachment.logical.mu.RLock()
	defer t.attachment.logical.mu.RUnlock()
	if t.attachment.logical.deleted {
		return session.ToolResult{}, contract.ErrStateUnavailable
	}
	if call.Name != t.route.spec.Name {
		return session.NewToolError(call.ID, "broker tool call does not match wrapper"), nil
	}
	if err := ctx.Err(); err != nil {
		return session.ToolResult{}, err
	}
	call.Args = append(json.RawMessage(nil), call.Args...)
	result, err := t.attachment.runtime.caller(ctx, t.attachment.logical.ref, t.route.backend, call)
	result.CallID = call.ID
	return result, err
}

func copySpec(spec tool.ToolSpec) tool.ToolSpec {
	spec.Schema = append(json.RawMessage(nil), spec.Schema...)
	return spec
}
