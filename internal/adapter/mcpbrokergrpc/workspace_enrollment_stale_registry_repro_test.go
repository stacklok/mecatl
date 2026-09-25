package mcpbrokergrpc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

// TestWorkspaceEnrollment_ToolDiscoveredOnlyByEnrollment_ExecutesThroughSameHandle
// verifies that a connected enrollment's advertised catalogue is also the
// executable catalogue for the attachment handle. It exercises the real client
// and server over the bufconn gRPC transport used by broker_test.go.
func TestWorkspaceEnrollment_ToolDiscoveredOnlyByEnrollment_ExecutesThroughSameHandle(t *testing.T) {
	broker := &enrollmentBroker{}
	remote := newRemote(t, broker)
	ctx := context.Background()

	attachment, outcome, err := remote.AttachSession(ctx, "session-1")
	if err != nil || outcome != mcpbroker.AttachCreated {
		t.Fatalf("AttachSession() = (%v, %v), want created", outcome, err)
	}
	if got := attachment.Tools(); len(got) != 0 {
		t.Fatalf("initial Tools() = %#v, want empty (no static declaration)", got)
	}

	enroller, ok := attachment.(mcpbroker.WorkspaceEnrollmentAttachment)
	if !ok {
		t.Fatal("attachment does not implement WorkspaceEnrollmentAttachment")
	}
	presentation, err := enroller.BeginWorkspaceEnrollment(ctx)
	if err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	result, err := enroller.ObserveWorkspaceEnrollment(ctx, presentation.Ref)
	if err != nil {
		t.Fatalf("ObserveWorkspaceEnrollment: %v", err)
	}
	if result.Status != mcpbroker.WorkspaceEnrollmentConnected {
		t.Fatalf("Status = %q, want connected", result.Status)
	}
	if result.Catalogue == nil {
		t.Fatal("Catalogue is nil on a connected result")
	}
	discovered := result.Catalogue.Tools()
	if len(discovered) != 1 || discovered[0].Spec().Name != "enrolled_only" {
		t.Fatalf("discovered tools = %#v, want exactly [enrolled_only]", discovered)
	}

	// Execute the tool advertised by the enrollment result through the same
	// attachment handle that observed the connected catalogue.
	call := session.NewToolCall("call-1", "enrolled_only", []byte(`{}`))
	got, err := discovered[0].Execute(ctx, call, tool.Environment{})
	if err != nil {
		t.Fatalf("Execute enrollment-discovered tool: %v", err)
	}
	if got.CallID != call.ID || got.Content != "ok" || got.IsError {
		t.Fatalf("Execute enrollment-discovered tool = %#v, want successful result for %q", got, call.ID)
	}
}

func TestWorkspaceEnrollment_CompleteCataloguePreservesWrappers(t *testing.T) {
	remote := newRemote(t, &enrollmentBroker{initial: []tool.Tool{protectedTool{}}})
	ctx := t.Context()
	attachment, _, err := remote.AttachSession(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	initial := attachment.Tools()
	if len(initial) != 1 || initial[0].Spec().Name != "protected" {
		t.Fatalf("initial tools = %v, want protected", initial)
	}
	requester, ok := initial[0].(tool.AuthorizationRequester)
	if !ok {
		t.Fatal("static tool lost authorization capability")
	}
	enroller := attachment.(mcpbroker.WorkspaceEnrollmentAttachment)
	presentation, err := enroller.BeginWorkspaceEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observe := func(t *testing.T) []tool.Tool {
		t.Helper()
		result, err := enroller.ObserveWorkspaceEnrollment(ctx, presentation.Ref)
		if err != nil || result.Status != mcpbroker.WorkspaceEnrollmentConnected || result.Catalogue == nil {
			t.Fatalf("ObserveWorkspaceEnrollment = %#v, %v", result, err)
		}
		tools := result.Catalogue.Tools()
		if len(tools) != 2 || tools[0].Spec().Name != "protected" || tools[1].Spec().Name != "enrolled_only" {
			t.Fatalf("complete catalogue = %v, want protected and enrolled_only", tools)
		}
		return tools
	}
	execute := func(t *testing.T, target tool.Tool, id, content string) {
		t.Helper()
		call := session.NewToolCall(session.ToolCallID(id), target.Spec().Name, []byte(`{}`))
		result, err := target.Execute(ctx, call, tool.Environment{})
		if err != nil || result.CallID != call.ID || result.Content != content || result.IsError {
			t.Fatalf("Execute(%s) = %#v, %v", id, result, err)
		}
	}
	execute(t, initial[0], "before-enrollment", "summary")
	discovered := observe(t)
	for i := range 3 {
		observe(t)
		// Keep using the original wrappers, with fresh call IDs to avoid receipt replay.
		execute(t, initial[0], fmt.Sprintf("static-%d", i), "summary")
		execute(t, discovered[1], fmt.Sprintf("discovered-%d", i), "ok")
	}

	auth := session.ExternalAuthorization{ID: "auth", Binding: "auth-binding", ExpiresAt: presentation.Ref.ExpiresAt}
	for _, operation := range []string{"observe", "execute", "request", "abort"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			for i := range 50 {
				switch operation {
				case "observe":
					observe(t)
				case "execute":
					execute(t, initial[0], fmt.Sprintf("concurrent-static-%d", i), "summary")
					execute(t, discovered[1], fmt.Sprintf("concurrent-discovered-%d", i), "ok")
				case "request":
					call := session.NewToolCall(session.ToolCallID(fmt.Sprintf("request-%d", i)), "protected", []byte(`{}`))
					if _, required, err := requester.RequestAuthorization(ctx, call); err != nil || required {
						t.Fatalf("RequestAuthorization = required %v, %v", required, err)
					}
				case "abort":
					if err := requester.AbortAuthorization(ctx, auth); err != nil {
						t.Fatalf("AbortAuthorization: %v", err)
					}
				}
			}
		})
	}
}

// enrollmentBroker starts empty unless a test supplies static tools.
type enrollmentBroker struct{ initial []tool.Tool }

func (b *enrollmentBroker) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	return &enrollmentAttachment{initial: b.initial}, mcpbroker.AttachCreated, nil
}
func (*enrollmentBroker) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}

type enrollmentAttachment struct{ initial []tool.Tool }

func (*enrollmentAttachment) Binding() session.ExternalBinding { return "binding-1" }
func (*enrollmentAttachment) Commit(context.Context) error     { return nil }
func (*enrollmentAttachment) Abort(context.Context) error      { return nil }
func (*enrollmentAttachment) Close(context.Context) (mcpbroker.CloseOutcome, error) {
	return mcpbroker.CloseClosed, nil
}

func (a *enrollmentAttachment) Tools() []tool.Tool { return append([]tool.Tool(nil), a.initial...) }
func (a *enrollmentAttachment) RefreshGrantedAuthorizationCatalogue(context.Context, session.ExternalAuthorization) ([]tool.Tool, error) {
	return a.Tools(), nil
}

func (*enrollmentAttachment) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	return "", errors.New("unused")
}
func (*enrollmentAttachment) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	return "", errors.New("unused")
}
func (*enrollmentAttachment) CancelAuthorization(context.Context, session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	return "", errors.New("unused")
}

func (*enrollmentAttachment) ResetWorkspaceEnrollment(context.Context) error { return nil }
func (*enrollmentAttachment) BeginWorkspaceEnrollment(context.Context) (mcpbroker.WorkspaceEnrollmentPresentation, error) {
	ref := mcpbroker.WorkspaceEnrollmentRef{ID: "enrollment-1", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	return mcpbroker.WorkspaceEnrollmentPresentation{Ref: ref, URL: "https://broker.invalid/enroll"}, nil
}

// The complete connected catalogue includes both static and newly discovered tools.
func (a *enrollmentAttachment) ObserveWorkspaceEnrollment(_ context.Context, ref mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	catalogue, err := mcpbroker.NewWorkspaceCatalogue(ref, append(a.Tools(), enrolledOnlyTool{}))
	if err != nil {
		return mcpbroker.WorkspaceEnrollmentResult{}, err
	}
	return mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected, Catalogue: catalogue}, nil
}
func (*enrollmentAttachment) CancelWorkspaceEnrollment(_ context.Context, ref mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	return mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentCancelled}, nil
}

type enrolledOnlyTool struct{}

func (enrolledOnlyTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "enrolled_only", Description: "discovered only via enrollment", Schema: []byte(`{"type":"object"}`)}
}
func (enrolledOnlyTool) ReadOnly() bool { return true }
func (enrolledOnlyTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

var _ mcpbroker.WorkspaceEnrollmentAttachment = (*enrollmentAttachment)(nil)
var _ mcpbroker.Service = (*enrollmentBroker)(nil)
