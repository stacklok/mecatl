package childstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/childstore"
)

// admitter admits through child and resumed; a nil func admits.
type admitter struct {
	child, resumed func(context.Context, *session.Session, port.ChildSessionInfo) error
}

func (a admitter) AdmitChild(ctx context.Context, s *session.Session, c port.ChildSessionInfo) error {
	if a.child == nil {
		return nil
	}
	return a.child(ctx, s, c)
}

func (a admitter) AdmitResumedChild(ctx context.Context, s *session.Session, c port.ChildSessionInfo) error {
	if a.resumed == nil {
		return nil
	}
	return a.resumed(ctx, s, c)
}

func newSession(t *testing.T) *session.Session {
	t.Helper()
	return session.New("child-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
}

// TestCreateChildPersistsThenAdmits pins the order: the child is persisted
// before it is admitted, so a refused child stays persisted, and the refusal
// is returned.
func TestCreateChildPersistsThenAdmits(t *testing.T) {
	inner := memstore.New()
	refused := errors.New("refused")
	var persistedFirst bool
	var got port.ChildSessionInfo
	store := childstore.New(inner, admitter{child: func(ctx context.Context, s *session.Session, child port.ChildSessionInfo) error {
		_, err := inner.Load(ctx, s.ID)
		persistedFirst = err == nil
		got = child
		return refused
	}})
	creator, ok := store.(port.ChildSessionCreator)
	if !ok {
		t.Fatal("the wrapped store is not a ChildSessionCreator")
	}
	err := creator.CreateChild(t.Context(), newSession(t), port.ChildSessionInfo{DefinitionName: "reviewer"})
	if !errors.Is(err, refused) {
		t.Fatalf("CreateChild = %v, want the refusal", err)
	}
	if !persistedFirst || got.DefinitionName != "reviewer" {
		t.Fatalf("admitted persisted=%t with %+v, want persisted first with the definition", persistedFirst, got)
	}
}

// noCreateStore negotiated no create support.
type noCreateStore struct{ *memstore.Store }

func (noCreateStore) SupportsSessionCreate() bool         { return false }
func (noCreateStore) SupportsSessionMetadataPaging() bool { return false }
func (noCreateStore) SupportsSessionLineage() bool        { return false }

// TestCreateSupportFollowsTheWrappedStore pins that the wrapper reports the
// wrapped store's create support, and that without it a child is admitted
// without being created.
func TestCreateSupportFollowsTheWrappedStore(t *testing.T) {
	admit := admitter{}
	if !port.SupportsSessionCreate(childstore.New(memstore.New(), admit)) {
		t.Fatal("a wrapped creating store reports no create support")
	}
	inner := noCreateStore{memstore.New()}
	store := childstore.New(inner, admit)
	if port.SupportsSessionCreate(store) {
		t.Fatal("a wrapped store without create support reports create support")
	}
	if err := store.(port.ChildSessionCreator).CreateChild(t.Context(), newSession(t), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if _, err := inner.Load(t.Context(), "child-1"); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("a store without create support was created into: %v", err)
	}
}

// TestResumeChildAdmitsWithoutPublishing pins that a resumed child is admitted
// through AdmitResumedChild, with its refusal returned, and is not published.
func TestResumeChildAdmitsWithoutPublishing(t *testing.T) {
	inner := memstore.New()
	refused := errors.New("refused")
	var resumed session.SessionID
	store := childstore.New(inner, admitter{
		child: func(context.Context, *session.Session, port.ChildSessionInfo) error {
			t.Fatal("a resume admitted the child as a new one")
			return nil
		},
		resumed: func(_ context.Context, s *session.Session, _ port.ChildSessionInfo) error {
			resumed = s.ID
			return refused
		},
	})
	err := store.(port.ChildSessionResumer).ResumeChild(t.Context(), newSession(t), port.ChildSessionInfo{})
	if !errors.Is(err, refused) || resumed != "child-1" {
		t.Fatalf("ResumeChild = %v for %q, want the refusal for child-1", err, resumed)
	}
	if _, err := inner.Load(t.Context(), "child-1"); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("a resume published the child: %v", err)
	}
}
