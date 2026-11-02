package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// hookedOpenBroker runs afterOpen after each OpenSession the broker answered.
type hookedOpenBroker struct {
	identitybroker.Service
	afterOpen func(identitybroker.OpenSessionRequest)
}

func (b *hookedOpenBroker) OpenSession(ctx context.Context, req identitybroker.OpenSessionRequest) (identitybroker.Session, error) {
	opened, err := b.Service.OpenSession(ctx, req)
	if b.afterOpen != nil {
		b.afterOpen(req)
	}
	return opened, err
}

// savedSuccessorFixture is a service with a converting identity broker, a
// placement provider whose reference commit can fail, and a store whose
// session creation can fail, holding mcpAuthorizationOwner's source session.
// The successor's ID is always "successor".
type savedSuccessorFixture struct {
	svc      *Service
	store    *successorBrokerStore
	provider *ownershipPlacementProvider
	broker   *hookedOpenBroker
	source   *session.Session
}

func newSavedSuccessorFixture(t *testing.T) savedSuccessorFixture {
	t.Helper()
	store := &successorBrokerStore{Store: memstore.New()}
	provider := &ownershipPlacementProvider{ref: session.EnvironmentRef{Kind: "microvm", ID: "saved-successor", Revision: "1"}}
	broker := &hookedOpenBroker{Service: convertingBroker(t, &switchableAS1{})}
	ids := []session.SessionID{"source", "successor"}
	var next atomic.Int32
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/owned",
		IdentityBroker: broker, Now: time.Now,
		NewID: func() session.SessionID { return ids[next.Add(1)-1] },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	source, err := svc.CreateSession(session.WithPrincipal(t.Context(), mcpAuthorizationOwner), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if source.Owner == nil || *source.Owner != *mcpAuthorizationOwner {
		t.Fatalf("source owner = %v, want %v", source.Owner, mcpAuthorizationOwner)
	}
	return savedSuccessorFixture{svc: svc, store: store, provider: provider, broker: broker, source: source}
}

// stored reports whether the successor can be loaded from the store.
func (f savedSuccessorFixture) stored(t *testing.T) (*session.Session, bool) {
	t.Helper()
	loaded, err := f.store.Load(t.Context(), "successor")
	if errors.Is(err, port.ErrSessionNotFound) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return loaded, true
}

// brokerSessionOpen reports whether the successor has an open broker session
// for the incarnation it was saved with, when saved.
func (f savedSuccessorFixture) brokerSessionOpen(t *testing.T, saved *session.Session) bool {
	t.Helper()
	open, found, err := f.broker.FindSession(context.Background(), identitybroker.FindSessionRequest{RootHostSessionID: "successor"})
	if err != nil {
		t.Fatal(err)
	}
	if found && saved != nil && open.Incarnation != string(saved.Incarnation()) {
		t.Fatalf("successor broker session incarnation %q, want the saved %q", open.Incarnation, saved.Incarnation())
	}
	return found
}

// TestSuccessorSavedThenFailedKeepsItsBrokerSession proves a fork or clear
// whose successor is saved before a later step fails returns the successor's
// ID with the error and leaves the successor's identity broker session open:
// the successor exists, so the broker session is its own, not the request's
// to close.
func TestSuccessorSavedThenFailedKeepsItsBrokerSession(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear bool
	}{
		{name: "fork"},
		{name: "clear", clear: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSavedSuccessorFixture(t)
			// The source's own commit succeeded; the successor's reference
			// publication fails after the successor is saved.
			f.provider.commitErr = errors.New("reference publication lost")
			ctx := requestWithAccessToken(t, mcpAuthorizationOwner, "jti-saved-then-failed")
			var (
				successor session.SessionID
				err       error
			)
			if tc.clear {
				successor, err = f.svc.ClearSessionSuccessor(ctx, f.source.ID, SuccessorPlacement{})
			} else {
				successor, err = f.svc.ForkSessionSuccessor(ctx, ForkSuccessorRequest{Source: f.source.ID})
			}
			if successor != "successor" || !errors.Is(err, ErrInternal) {
				t.Fatalf("successor saved before a failed commit = %q, %v; want its ID with ErrInternal", successor, err)
			}
			saved, ok := f.stored(t)
			if !ok {
				t.Fatal("the successor is not in the store")
			}
			if !f.brokerSessionOpen(t, saved) {
				t.Fatal("the saved successor's broker session was closed")
			}
		})
	}
}

// TestCreatePlacedSuccessorReportsSavedExactly proves createPlacedSuccessorLocked
// reports saved exactly when the successor can be loaded from the store, and
// that the broker session it opened stays open exactly then: on success, on a
// failure before the save, and on a failure after it.
func TestCreatePlacedSuccessorReportsSavedExactly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(savedSuccessorFixture)
		wantSaved bool
		wantErr   error
	}{
		{name: "saved", configure: func(savedSuccessorFixture) {}, wantSaved: true},
		{name: "store refuses before saving", configure: func(f savedSuccessorFixture) { f.store.failCreate = true }, wantErr: ErrInternal},
		{name: "reference commit fails after saving", configure: func(f savedSuccessorFixture) { f.provider.commitErr = errors.New("reference publication lost") }, wantSaved: true, wantErr: ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSavedSuccessorFixture(t)
			tc.configure(f)
			ctx := requestWithAccessToken(t, mcpAuthorizationOwner, "jti-saved-flag")
			unlock := f.svc.runEntryMu.lock(f.source.ID)
			id, saved, err := f.svc.createPlacedSuccessorLocked(ctx, ForkSuccessorRequest{Source: f.source.ID}, true, false, nil)
			unlock()
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			loaded, stored := f.stored(t)
			if saved != stored || saved != tc.wantSaved {
				t.Fatalf("saved = %t, successor stored = %t; want both %t", saved, stored, tc.wantSaved)
			}
			if want := map[bool]session.SessionID{true: "successor"}[saved]; id != want {
				t.Fatalf("id = %q with saved %t, want %q", id, saved, want)
			}
			if open := f.brokerSessionOpen(t, loaded); open != saved {
				t.Fatalf("successor broker session open = %t, want %t", open, saved)
			}
		})
	}
}

// TestClearRefusesASourceWhoseOwnerChangedBeforeCancelling proves Clear
// compares the owner its successor's broker session was opened for with the
// source reloaded under the mutation lease before it cancels the source: a
// changed owner fails the clear with the source's run state untouched.
func TestClearRefusesASourceWhoseOwnerChangedBeforeCancelling(t *testing.T) {
	f := newSavedSuccessorFixture(t)
	running, err := f.store.Load(t.Context(), f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := running.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Save(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	other := &session.Principal{Issuer: mcpAuthorizationOwner.Issuer, Subject: "mallory-1", GrantType: session.GrantTypeUser}
	f.broker.afterOpen = func(req identitybroker.OpenSessionRequest) {
		if req.HostSessionID != "successor" {
			return
		}
		// The source changes owner between Clear's first load, whose owner
		// the successor's broker session was just opened for, and the reload
		// under the mutation lease.
		changed, err := f.store.Load(context.Background(), f.source.ID)
		if err != nil {
			t.Error(err)
			return
		}
		changed.Owner = other
		if err := f.store.Save(context.Background(), changed); err != nil {
			t.Error(err)
		}
	}
	successor, err := f.svc.ClearSessionSuccessor(requestWithAccessToken(t, mcpAuthorizationOwner, "jti-owner-changed"), f.source.ID, SuccessorPlacement{})
	if successor != "" || !errors.Is(err, ErrInternal) {
		t.Fatalf("clear of a source whose owner changed = %q, %v; want no successor and ErrInternal", successor, err)
	}
	after, err := f.store.Load(t.Context(), f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != session.StateRunning {
		t.Fatalf("source state after the refused clear = %s, want it still running", after.State)
	}
	if _, ok := f.stored(t); ok {
		t.Fatal("the refused clear saved a successor")
	}
	if f.brokerSessionOpen(t, nil) {
		t.Fatal("the refused clear left its successor's broker session open")
	}
}
