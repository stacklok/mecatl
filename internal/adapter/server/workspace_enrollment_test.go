package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type enrollmentAttachment struct {
	brokercontract.Attachment
	ref        brokercontract.WorkspaceEnrollmentRef
	result     brokercontract.WorkspaceEnrollmentResult
	catalogue  brokercontract.WorkspaceCatalogue
	cancelWait bool
}

func (a *enrollmentAttachment) BeginWorkspaceEnrollment(context.Context) (brokercontract.WorkspaceEnrollmentPresentation, error) {
	return brokercontract.WorkspaceEnrollmentPresentation{Ref: a.ref, URL: "https://broker.example/authorize?state=opaque"}, nil
}

func (a *enrollmentAttachment) ObserveWorkspaceEnrollment(context.Context, brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
	return a.result, nil
}

func (a *enrollmentAttachment) CancelWorkspaceEnrollment(ctx context.Context, _ brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
	if a.cancelWait {
		<-ctx.Done()
		return brokercontract.WorkspaceEnrollmentResult{}, ctx.Err()
	}
	return brokercontract.WorkspaceEnrollmentResult{Ref: a.ref, Status: brokercontract.WorkspaceEnrollmentCancelled}, nil
}

func (a *enrollmentAttachment) Tools() []tool.Tool {
	if a.catalogue != nil {
		return a.catalogue.Tools()
	}
	return a.Attachment.Tools()
}

type enrollmentBroker struct {
	brokercontract.Service
	attachment *enrollmentAttachment
	attachErr  error
}

func (b *enrollmentBroker) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	if b.attachErr != nil {
		return nil, "", b.attachErr
	}
	attachment, outcome, err := b.Service.AttachSession(ctx, id)
	if err != nil {
		return nil, outcome, err
	}
	if b.attachment == nil {
		b.attachment = &enrollmentAttachment{Attachment: attachment, ref: brokercontract.WorkspaceEnrollmentRef{
			ID: "enrollment-1", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour),
		}}
	}
	return b.attachment, outcome, nil
}

type enrollmentTool struct{ name string }

func (t enrollmentTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (enrollmentTool) ReadOnly() bool { return true }
func (enrollmentTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult("call", "ok"), nil
}

func TestWorkspaceEnrollmentCompensationIsBounded(t *testing.T) {
	oldTimeout := engineCloseTimeout
	engineCloseTimeout = 20 * time.Millisecond
	defer func() { engineCloseTimeout = oldTimeout }()

	attachment := &enrollmentAttachment{cancelWait: true}
	started := time.Now()
	cancelWorkspaceEnrollmentDetached(context.Background(), attachment, brokercontract.WorkspaceEnrollmentRef{})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("compensation took %s", elapsed)
	}
}

func TestWorkspaceEnrollmentStateLossClearsPendingGate(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:     func() session.SessionID { return "lost-enrollment-session" },
		MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}

	svc.mu.Lock()
	delete(svc.brokerAttachments, created.ID)
	svc.mu.Unlock()
	broker.attachErr = brokercontract.ErrStateUnavailable
	failed, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != brokercontract.WorkspaceEnrollmentFailed || !sameWorkspaceEnrollmentRef(failed.Ref, started.Ref) {
		t.Fatalf("failed projection = %#v, want failed for %#v", failed, started.Ref)
	}
	loaded, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := loaded.PendingWorkspaceEnrollment(); pending {
		t.Fatal("unavailable broker left workspace enrollment pending")
	}

	broker.attachErr = nil
	if restarted, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil || restarted.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("restart after state loss = %#v, %v", restarted, err)
	}
}

func TestWorkspaceEnrollmentPublishesFrozenCatalogueBeforePrompt(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	var catalogues [][]string
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "enrollment-session" }, MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			names := make([]string, len(tools))
			for i, candidate := range tools {
				names[i] = candidate.Spec().Name
			}
			catalogues = append(catalogues, names)
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != brokercontract.WorkspaceEnrollmentPending || started.URL == "" {
		t.Fatalf("started = %#v", started)
	}
	loaded, _ := store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending = %#v, %v", pending, ok)
	}

	mismatchedRef := started.Ref
	mismatchedRef.ID = "enrollment-other"
	broker.attachment.ref = mismatchedRef
	if _, err := svc.CancelWorkspaceEnrollment(t.Context(), created.ID, started.Ref.ID); err == nil {
		t.Fatal("mismatched enrollment cancellation succeeded")
	}
	broker.attachment.ref = started.Ref
	loaded, _ = store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending after mismatched cancellation = %#v, %v", pending, ok)
	}

	mismatchedCatalogue, err := brokercontract.NewWorkspaceCatalogue(mismatchedRef, []tool.Tool{enrollmentTool{name: "mcp__calendar__list"}})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{
		Ref: mismatchedRef, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: mismatchedCatalogue,
	}
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("mismatched enrollment observation succeeded")
	}
	loaded, _ = store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending after mismatched observation = %#v, %v", pending, ok)
	}

	complete, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{
		enrollmentTool{name: "mcp__calendar__list"}, enrollmentTool{name: "mcp__github__review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drifted, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{
		enrollmentTool{name: "mcp__calendar__list"},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.catalogue = drifted
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: complete}
	connected, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if connected.Status != brokercontract.WorkspaceEnrollmentConnected || len(catalogues) != 2 {
		t.Fatalf("connected = %#v, catalogues = %v", connected, catalogues)
	}
	if got := catalogues[1]; len(got) != 2 || got[1] != "mcp__github__review" {
		t.Fatalf("rebuilt catalogue = %v", got)
	}
	loaded, _ = store.Load(t.Context(), created.ID)
	if _, ok := loaded.PendingWorkspaceEnrollment(); ok {
		t.Fatal("completed enrollment remained pending")
	}
	authority, ok := loaded.BoundAuthority()
	if !ok || len(authority.CapabilitySet.Tools) != 2 || authority.CapabilitySet.Tools[1] != "mcp__github__review" {
		t.Fatalf("authority = %#v, %v", authority, ok)
	}
}
