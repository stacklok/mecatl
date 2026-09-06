package mcpbrokergrpc_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestInitialProductionMCPBroker_Scenario3_ProtectedCallContinuation(t *testing.T) {
	local := newContinuationBroker()
	remote := newRemote(t, local)
	attachment, _, err := remote.AttachSession(t.Context(), "session-continuation")
	if err != nil {
		t.Fatal(err)
	}
	protected := attachment.Tools()[0].(tool.AuthorizationRequester)
	call := session.NewToolCall("call-1", "protected", []byte(`{"request":"original"}`))
	auth, required, err := protected.RequestAuthorization(t.Context(), call)
	if err != nil || !required {
		t.Fatalf("RequestAuthorization = (%+v, %v, %v)", auth, required, err)
	}
	if got, err := attachment.PresentAuthorization(t.Context(), auth); err != nil || got != "https://broker.example/authorize/opaque-auth" {
		t.Fatalf("PresentAuthorization = %q, %v", got, err)
	}
	local.grant()
	if got, err := attachment.AuthorizationStatus(t.Context(), auth); err != nil || got != session.AuthorizationGranted {
		t.Fatalf("AuthorizationStatus = %q, %v", got, err)
	}
	result, err := protected.Execute(t.Context(), call, tool.Environment{})
	if err != nil || result.Content != "continued original" || result.CallID != call.ID {
		t.Fatalf("continued Execute = %+v, %v", result, err)
	}
	if got := local.executionCount(); got != 1 {
		t.Fatalf("execution count = %d, want 1", got)
	}
}

func TestInvariant_remote_broker_continues_exact_parked_call(t *testing.T) {
	local := newContinuationBroker()
	remote := newRemote(t, local)
	attachment, _, err := remote.AttachSession(t.Context(), "session-exact")
	if err != nil {
		t.Fatal(err)
	}
	protected := attachment.Tools()[0].(tool.AuthorizationRequester)
	original := session.NewToolCall("call-1", "protected", []byte(`{"request":"original"}`))
	if _, required, err := protected.RequestAuthorization(t.Context(), original); err != nil || !required {
		t.Fatalf("RequestAuthorization required=%v err=%v", required, err)
	}
	local.grant()
	changed := session.NewToolCall("call-1", "protected", []byte(`{"request":"changed"}`))
	if _, err := protected.Execute(t.Context(), changed, tool.Environment{}); err == nil {
		t.Fatal("remote broker executed a call that did not match the parked invocation")
	}
	if got := local.executionCount(); got != 0 {
		t.Fatalf("changed invocation reached effect: %d", got)
	}
	if _, err := protected.Execute(t.Context(), original, tool.Environment{}); err != nil {
		t.Fatalf("exact parked invocation did not continue: %v", err)
	}
}

func TestInitialProductionMCPBroker_Scenario3_TerminalOutcomeParity(t *testing.T) {
	local := newContinuationBroker()
	remote := newRemote(t, local)
	attachment, _, err := remote.AttachSession(t.Context(), "session-terminal")
	if err != nil {
		t.Fatal(err)
	}
	protected := attachment.Tools()[0].(tool.AuthorizationRequester)
	auth, _, err := protected.RequestAuthorization(t.Context(), session.NewToolCall("call-1", "protected", []byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := attachment.CancelAuthorization(t.Context(), auth); err != nil || got != mcpbroker.CancelCancelled {
		t.Fatalf("CancelAuthorization = %q, %v", got, err)
	}
	if got, err := attachment.AuthorizationStatus(t.Context(), auth); err != nil || got != session.AuthorizationCancelled {
		t.Fatalf("terminal status = %q, %v", got, err)
	}
	if got, err := attachment.CancelAuthorization(t.Context(), auth); err != nil || got != mcpbroker.CancelAlreadyCancelled {
		t.Fatalf("replayed cancellation = %q, %v", got, err)
	}
}

func TestInvariant_remote_broker_keeps_presentation_and_credentials_private(t *testing.T) {
	local := newContinuationBroker()
	remote := newRemote(t, local)
	attachment, _, err := remote.AttachSession(t.Context(), "session-private")
	if err != nil {
		t.Fatal(err)
	}
	protected := attachment.Tools()[0].(tool.AuthorizationRequester)
	auth, _, err := protected.RequestAuthorization(t.Context(), session.NewToolCall("call-1", "protected", []byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	if auth.ID != "opaque-auth" || auth.Binding != "opaque-binding" {
		t.Fatalf("opaque authorization changed: %+v", auth)
	}
	if auth.ID == local.presentation || string(auth.Binding) == local.presentation {
		t.Fatal("live presentation leaked into the durable authorization reference")
	}
	if _, err := attachment.PresentAuthorization(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	if local.presentationCalls != 1 {
		t.Fatalf("presentation was not fetched live: %d calls", local.presentationCalls)
	}
}

func TestInitialProductionMCPBroker_Scenario3_ProfileParity(t *testing.T) {
	remote := newRemote(t, newContinuationBroker())
	attachment, _, err := remote.AttachSession(t.Context(), "session-profile")
	if err != nil {
		t.Fatal(err)
	}
	tools := attachment.Tools()
	if len(tools) != 1 || tools[0].Spec().Name != "protected" || !tools[0].ReadOnly() {
		t.Fatalf("remote profile = %#v", tools)
	}
	if _, ok := tools[0].(tool.AuthorizationRequester); !ok {
		t.Fatal("remote profile lost authorization capability")
	}
	if _, ok := tools[0].(tool.DispatchSerial); !ok {
		t.Fatal("remote profile lost dispatch-serial capability")
	}
}

func TestInitialProductionMCPBroker_Scenario3_CallbackRoutingAndReplay(t *testing.T) {
	local := newContinuationBroker()
	remote := newRemote(t, local)
	attachment, _, err := remote.AttachSession(t.Context(), "session-callback")
	if err != nil {
		t.Fatal(err)
	}
	protected := attachment.Tools()[0].(tool.AuthorizationRequester)
	auth, _, err := protected.RequestAuthorization(t.Context(), session.NewToolCall("call-1", "protected", []byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := attachment.PresentAuthorization(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	second, err := attachment.PresentAuthorization(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if first != local.presentation || second != first || local.presentationCalls != 2 {
		t.Fatalf("live callback routing/replay = %q then %q (%d calls)", first, second, local.presentationCalls)
	}
}

type continuationBroker struct {
	mu                sync.Mutex
	parked            session.ToolCall
	status            session.AuthorizationStatus
	executions        int
	presentation      string
	presentationCalls int
}

func newContinuationBroker() *continuationBroker {
	return &continuationBroker{status: session.AuthorizationPending, presentation: "https://broker.example/authorize/opaque-auth"}
}

func (b *continuationBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	return &continuationAttachment{broker: b}, mcpbroker.AttachCreated, nil
}
func (*continuationBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteDeleted, nil
}
func (b *continuationBroker) grant() {
	b.mu.Lock()
	b.status = session.AuthorizationGranted
	b.mu.Unlock()
}
func (b *continuationBroker) executionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.executions
}

type continuationAttachment struct{ broker *continuationBroker }

func (*continuationAttachment) Commit(context.Context) error     { return nil }
func (*continuationAttachment) Abort(context.Context) error      { return nil }
func (*continuationAttachment) Binding() session.ExternalBinding { return "session-binding" }
func (a *continuationAttachment) Tools() []tool.Tool {
	return []tool.Tool{continuationTool{broker: a.broker}}
}
func (a *continuationAttachment) PresentAuthorization(_ context.Context, auth session.ExternalAuthorization) (string, error) {
	if auth.ID != "opaque-auth" || auth.Binding != "opaque-binding" {
		return "", mcpbroker.ErrAuthorizationNotFound
	}
	a.broker.mu.Lock()
	defer a.broker.mu.Unlock()
	a.broker.presentationCalls++
	return a.broker.presentation, nil
}
func (a *continuationAttachment) AuthorizationStatus(_ context.Context, auth session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	if auth.ID != "opaque-auth" || auth.Binding != "opaque-binding" {
		return "", mcpbroker.ErrAuthorizationNotFound
	}
	a.broker.mu.Lock()
	defer a.broker.mu.Unlock()
	return a.broker.status, nil
}
func (a *continuationAttachment) CancelAuthorization(_ context.Context, auth session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	if auth.ID != "opaque-auth" || auth.Binding != "opaque-binding" {
		return "", mcpbroker.ErrAuthorizationNotFound
	}
	a.broker.mu.Lock()
	defer a.broker.mu.Unlock()
	if a.broker.status == session.AuthorizationCancelled {
		return mcpbroker.CancelAlreadyCancelled, nil
	}
	if a.broker.status != session.AuthorizationPending {
		return mcpbroker.CancelAlreadyResolved, nil
	}
	a.broker.status = session.AuthorizationCancelled
	return mcpbroker.CancelCancelled, nil
}
func (*continuationAttachment) Close(context.Context) (mcpbroker.CloseOutcome, error) {
	return mcpbroker.CloseClosed, nil
}

type continuationTool struct{ broker *continuationBroker }

func (continuationTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "protected", Description: "protected", Schema: []byte(`{"type":"object"}`)}
}
func (continuationTool) ReadOnly() bool      { return true }
func (continuationTool) DispatchSerialTool() {}
func (t continuationTool) RequestAuthorization(_ context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	t.broker.mu.Lock()
	defer t.broker.mu.Unlock()
	t.broker.parked = call
	return session.ExternalAuthorization{ID: "opaque-auth", Binding: "opaque-binding", ExpiresAt: time.Unix(2_000_000_000, 0)}, true, nil
}
func (continuationTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}
func (t continuationTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.broker.mu.Lock()
	defer t.broker.mu.Unlock()
	if t.broker.status != session.AuthorizationGranted || call.ID != t.broker.parked.ID || call.Name != t.broker.parked.Name || string(call.Args) != string(t.broker.parked.Args) || call.ItemID != t.broker.parked.ItemID {
		return session.ToolResult{}, errors.New("invocation does not match parked authorization")
	}
	t.broker.executions++
	return session.NewToolResult(call.ID, "continued original"), nil
}
