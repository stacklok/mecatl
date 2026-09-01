package mcpbroker_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

type fakeService struct {
	mu             sync.Mutex
	sessions       map[session.SessionID]*fakeLogicalSession
	nextGeneration uint64
}

type authorizationKey struct {
	id      string
	binding session.AuthorizationBinding
}

type fakeLogicalSession struct {
	generation     uint64
	authorizations map[authorizationKey]session.AuthorizationStatus
}

type fakeAttachment struct {
	service    *fakeService
	id         session.SessionID
	generation uint64
	closed     bool
}

var _ mcpbroker.Service = (*fakeService)(nil)
var _ mcpbroker.Attachment = (*fakeAttachment)(nil)

func newFakeService() *fakeService {
	return &fakeService{sessions: make(map[session.SessionID]*fakeLogicalSession)}
}

func authorizationIdentity(authorization session.ExternalAuthorization) authorizationKey {
	return authorizationKey{id: authorization.ID, binding: authorization.Binding}
}

func (s *fakeService) AttachSession(_ context.Context, id session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	outcome := mcpbroker.AttachReattached
	if s.sessions[id] == nil {
		s.nextGeneration++
		s.sessions[id] = &fakeLogicalSession{
			generation:     s.nextGeneration,
			authorizations: make(map[authorizationKey]session.AuthorizationStatus),
		}
		outcome = mcpbroker.AttachCreated
	}
	return &fakeAttachment{service: s, id: id, generation: s.sessions[id].generation}, outcome, nil
}

func (s *fakeService) DeleteSession(_ context.Context, id session.SessionID) (mcpbroker.DeleteOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[id] == nil {
		return mcpbroker.DeleteNotFound, nil
	}
	delete(s.sessions, id)
	return mcpbroker.DeleteDeleted, nil
}

func (s *fakeService) addAuthorization(id session.SessionID, authorization session.ExternalAuthorization) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id].authorizations[authorizationIdentity(authorization)] = session.AuthorizationPending
}

func (a *fakeAttachment) PresentAuthorization(_ context.Context, authorization session.ExternalAuthorization) (string, error) {
	status, err := a.AuthorizationStatus(context.Background(), authorization)
	if err != nil {
		return "", err
	}
	if status != session.AuthorizationPending {
		return "", mcpbroker.ErrAuthorizationNotFound
	}
	return "https://authorize.invalid/live/" + authorization.ID, nil
}

func (a *fakeAttachment) AuthorizationStatus(_ context.Context, authorization session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if a.closed {
		return "", mcpbroker.ErrAttachmentClosed
	}
	logical := a.service.sessions[a.id]
	if logical == nil || logical.generation != a.generation {
		return "", mcpbroker.ErrStateUnavailable
	}
	status, ok := logical.authorizations[authorizationIdentity(authorization)]
	if !ok {
		return "", mcpbroker.ErrAuthorizationNotFound
	}
	return status, nil
}

func (a *fakeAttachment) CancelAuthorization(_ context.Context, authorization session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if a.closed {
		return "", mcpbroker.ErrAttachmentClosed
	}
	logical := a.service.sessions[a.id]
	if logical == nil || logical.generation != a.generation {
		return "", mcpbroker.ErrStateUnavailable
	}
	status, ok := logical.authorizations[authorizationIdentity(authorization)]
	if !ok {
		return "", mcpbroker.ErrAuthorizationNotFound
	}
	switch status {
	case session.AuthorizationPending:
		logical.authorizations[authorizationIdentity(authorization)] = session.AuthorizationCancelled
		return mcpbroker.CancelCancelled, nil
	case session.AuthorizationCancelled:
		return mcpbroker.CancelAlreadyCancelled, nil
	default:
		return mcpbroker.CancelAlreadyResolved, nil
	}
}

func (a *fakeAttachment) Close(_ context.Context) (mcpbroker.CloseOutcome, error) {
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if a.closed {
		return mcpbroker.CloseAlreadyClosed, nil
	}
	a.closed = true
	return mcpbroker.CloseClosed, nil
}

func TestAttachmentReattachesToLogicalAuthorizationState(t *testing.T) {
	ctx := context.Background()
	broker := newFakeService()
	id := session.SessionID("session-1")
	authorization := session.ExternalAuthorization{
		ID:        "authorization-1",
		Binding:   session.AuthorizationBinding("opaque-binding-1"),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	first, outcome, err := broker.AttachSession(ctx, id)
	if err != nil || outcome != mcpbroker.AttachCreated {
		t.Fatalf("first Attach() = (%q, %v), want (%q, nil)", outcome, err, mcpbroker.AttachCreated)
	}
	broker.addAuthorization(id, authorization)
	url, err := first.PresentAuthorization(ctx, authorization)
	if err != nil || url == "" {
		t.Fatalf("PresentAuthorization() = (%q, %v), want live URL", url, err)
	}
	if got, err := first.Close(ctx); err != nil || got != mcpbroker.CloseClosed {
		t.Fatalf("Close() = (%q, %v)", got, err)
	}
	if got, err := first.Close(ctx); err != nil || got != mcpbroker.CloseAlreadyClosed {
		t.Fatalf("second Close() = (%q, %v)", got, err)
	}

	second, outcome, err := broker.AttachSession(ctx, id)
	if err != nil || outcome != mcpbroker.AttachReattached {
		t.Fatalf("second Attach() = (%q, %v), want (%q, nil)", outcome, err, mcpbroker.AttachReattached)
	}
	var restored session.ExternalAuthorization
	encoded, err := json.Marshal(authorization)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if got, err := second.AuthorizationStatus(ctx, restored); err != nil || got != session.AuthorizationPending {
		t.Fatalf("AuthorizationStatus() = (%q, %v), want (%q, nil)", got, err, session.AuthorizationPending)
	}

	wrongBinding := restored
	wrongBinding.Binding = session.AuthorizationBinding("different-binding")
	if _, err := second.CancelAuthorization(ctx, wrongBinding); !errors.Is(err, mcpbroker.ErrAuthorizationNotFound) {
		t.Fatalf("CancelAuthorization(wrong binding) error = %v, want ErrAuthorizationNotFound", err)
	}
	if got, err := second.CancelAuthorization(ctx, authorization); err != nil || got != mcpbroker.CancelCancelled {
		t.Fatalf("CancelAuthorization() = (%q, %v)", got, err)
	}
	if got, err := second.CancelAuthorization(ctx, authorization); err != nil || got != mcpbroker.CancelAlreadyCancelled {
		t.Fatalf("second CancelAuthorization() = (%q, %v)", got, err)
	}
}

func TestCloseDoesNotDeleteLogicalSession(t *testing.T) {
	ctx := context.Background()
	broker := newFakeService()
	id := session.SessionID("session-2")

	attachment, _, err := broker.AttachSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := broker.AttachSession(ctx, id); err != nil || outcome != mcpbroker.AttachReattached {
		t.Fatalf("Attach after Close = (%q, %v), want reattached", outcome, err)
	}
	if got, err := broker.DeleteSession(ctx, id); err != nil || got != mcpbroker.DeleteDeleted {
		t.Fatalf("DeleteSession() = (%q, %v)", got, err)
	}
	if got, err := broker.DeleteSession(ctx, id); err != nil || got != mcpbroker.DeleteNotFound {
		t.Fatalf("second DeleteSession() = (%q, %v)", got, err)
	}
}

func TestDeleteInvalidatesStaleAttachmentAcrossRecreation(t *testing.T) {
	ctx := context.Background()
	broker := newFakeService()
	id := session.SessionID("session-3")
	stale, _, err := broker.AttachSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	fresh, outcome, err := broker.AttachSession(ctx, id)
	if err != nil || outcome != mcpbroker.AttachCreated {
		t.Fatalf("recreate = (%q, %v), want created", outcome, err)
	}
	authorization := session.ExternalAuthorization{ID: "authorization-3", Binding: "binding-3"}
	broker.addAuthorization(id, authorization)
	if _, err := stale.AuthorizationStatus(ctx, authorization); !errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("stale attachment error = %v, want ErrStateUnavailable", err)
	}
	if got, err := fresh.AuthorizationStatus(ctx, authorization); err != nil || got != session.AuthorizationPending {
		t.Fatalf("fresh attachment status = (%q, %v)", got, err)
	}
}
