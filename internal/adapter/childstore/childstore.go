// Package childstore wraps the session store the engine publishes child
// sessions to (subagents, parallel branches, and team members), so the host
// admits each child after it is persisted and before it runs. mecak8s uses it
// to register each helper agent with the identity broker.
package childstore

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// AdmitFunc admits a child session the engine is publishing; an error stops
// the child before it runs.
type AdmitFunc func(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error

// errNoCreate refuses Create on a store without create-only publication.
// Callers check SupportsSessionCreate first, so it is reached only by misuse.
var errNoCreate = errors.New("childstore: the wrapped session store cannot create sessions")

// New wraps store so that each child session the engine publishes to it is
// admitted with admit. It forwards Save, Load, and create-only publication,
// and reports the wrapped store's create support; it adds no other optional
// operation.
func New(store port.SessionStore, admit AdmitFunc) port.SessionStore {
	return wrapped{inner: store, admit: admit}
}

type wrapped struct {
	inner port.SessionStore
	admit AdmitFunc
}

func (w wrapped) Save(ctx context.Context, s *session.Session) error { return w.inner.Save(ctx, s) }

func (w wrapped) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return w.inner.Load(ctx, id)
}

// Create forwards create-only publication.
func (w wrapped) Create(ctx context.Context, s *session.Session) error {
	creator, ok := w.inner.(port.SessionCreator)
	if !ok {
		return errNoCreate
	}
	return creator.Create(ctx, s)
}

// CreateChild persists the child first, as the engine's create-only
// publication does, so its incarnation is durable before the host records it,
// then admits it. A refused child stays persisted but never ran.
func (w wrapped) CreateChild(ctx context.Context, s *session.Session, child port.ChildSessionInfo) error {
	if port.SupportsSessionCreate(w.inner) {
		if err := w.Create(ctx, s); err != nil {
			return err
		}
	}
	return w.admit(ctx, s, child)
}

// SupportsSessionCreate reports the wrapped store's create support.
func (w wrapped) SupportsSessionCreate() bool { return port.SupportsSessionCreate(w.inner) }

// SupportsSessionMetadataPaging reports false: the wrapper does not forward
// metadata paging, which the engine's child stores never use.
func (wrapped) SupportsSessionMetadataPaging() bool { return false }

// SupportsSessionLineage reports false: the wrapper does not forward lineage,
// which the engine's child stores never use.
func (wrapped) SupportsSessionLineage() bool { return false }

var (
	_ port.ChildSessionCreator      = wrapped{}
	_ port.SessionCreator           = wrapped{}
	_ port.SessionCapabilitySupport = wrapped{}
)
