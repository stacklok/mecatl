package app

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/childstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// helperRegisterFunc registers a helper agent whose session the engine is
// publishing; see server.Service.RegisterHelper.
type helperRegisterFunc func(context.Context, *session.Session, port.ChildSessionInfo) error

// helperRegistrar registers helper agents with the identity broker when the
// engine publishes their sessions. The session catalogs that publish them are
// built before the server exists, so Build binds the server's registration
// once it does. A nil helperRegistrar, the one built without an identity
// broker, wraps nothing.
type helperRegistrar struct {
	register atomic.Pointer[helperRegisterFunc]
}

// newHelperRegistrar returns a registrar when cfg enables the identity broker,
// and nil otherwise.
func newHelperRegistrar(cfg Config) *helperRegistrar {
	if !cfg.IdentityBroker {
		return nil
	}
	return &helperRegistrar{}
}

// bind sets the registration the registrar calls. Binding a nil registrar does
// nothing.
func (h *helperRegistrar) bind(register helperRegisterFunc) {
	if h != nil {
		h.register.Store(&register)
	}
}

// wrap returns store wrapped so each child session the engine publishes to it
// is registered; a nil registrar returns store unchanged.
func (h *helperRegistrar) wrap(store port.SessionStore) port.SessionStore {
	if h == nil || store == nil {
		return store
	}
	return childstore.New(store, h.registerHelper)
}

// registerHelper calls the bound registration. A child published before Build
// bound one is refused: no session runs before the server exists.
func (h *helperRegistrar) registerHelper(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error {
	register := h.register.Load()
	if register == nil {
		return fmt.Errorf("%w: helper agent registration is not ready", server.ErrUnavailable)
	}
	return (*register)(ctx, sess, child)
}
