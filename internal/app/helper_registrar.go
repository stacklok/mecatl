package app

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/childstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// helperRegistrations are the server's helper registrations; see
// server.Service.RegisterHelper and RegisterResumedHelper.
type helperRegistrations interface {
	RegisterHelper(context.Context, *session.Session, port.ChildSessionInfo) error
	RegisterResumedHelper(context.Context, *session.Session, port.ChildSessionInfo) error
}

// helperRegistrar registers helper agents with the identity broker when the
// engine publishes or resumes their sessions. The session catalogs that
// publish them are built before the server exists, so Build binds the
// server's registrations once it does. A nil helperRegistrar, the one built
// without an identity broker, wraps nothing.
type helperRegistrar struct {
	server atomic.Pointer[helperRegistrations]
}

// newHelperRegistrar returns a registrar when cfg enables the identity broker,
// and nil otherwise.
func newHelperRegistrar(cfg Config) *helperRegistrar {
	if !cfg.IdentityBroker {
		return nil
	}
	return &helperRegistrar{}
}

// bind sets the registrations the registrar calls. Binding a nil registrar
// does nothing.
func (h *helperRegistrar) bind(registrations helperRegistrations) {
	if h != nil {
		h.server.Store(&registrations)
	}
}

// wrap returns store wrapped so each child session the engine publishes to it
// or resumes from it is registered; a nil registrar returns store unchanged.
func (h *helperRegistrar) wrap(store port.SessionStore) port.SessionStore {
	if h == nil || store == nil {
		return store
	}
	return childstore.New(store, h)
}

// registrations returns the bound registrations. A child published before
// Build bound them is refused: no session runs before the server exists.
func (h *helperRegistrar) registrations() (helperRegistrations, error) {
	bound := h.server.Load()
	if bound == nil {
		return nil, fmt.Errorf("%w: helper agent registration is not ready", server.ErrUnavailable)
	}
	return *bound, nil
}

// AdmitChild registers a helper whose session the engine is publishing.
func (h *helperRegistrar) AdmitChild(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error {
	r, err := h.registrations()
	if err != nil {
		return err
	}
	return r.RegisterHelper(ctx, sess, child)
}

// AdmitResumedChild admits a helper whose session the engine is resuming.
func (h *helperRegistrar) AdmitResumedChild(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error {
	r, err := h.registrations()
	if err != nil {
		return err
	}
	return r.RegisterResumedHelper(ctx, sess, child)
}

// registerHelperTools registers helperTools, a session's gateway tools, in a
// def-less helper's catalog and returns the names it registered. A helper's
// bound authority, derived from its parent's, still decides which it may
// call. A tool whose name the catalog already holds, such as a server-global
// MCP tool of the same name, is skipped and reported to diag (which may be
// nil), never a panic: the session's own catalog refuses the same collision
// gracefully when it registers the session's tools.
func registerHelperTools(classified *classifiedCatalog, helperTools []tool.Tool, diag port.Diagnostics) []string {
	names := make([]string, 0, len(helperTools))
	gateway := classification(server.KindDerived,
		"session-bound gateway wrapper supplied by the root's host attachment; calls present the helper agent's own credential")
	for _, t := range helperTools {
		name := t.Spec().Name
		if err := classified.register(t, gateway); err != nil {
			if diag != nil {
				diag.Log(context.Background(), port.LevelWarn, "helper catalog skipped a gateway tool whose name it already holds", "tool", name)
			}
			continue
		}
		names = append(names, name)
	}
	return names
}
