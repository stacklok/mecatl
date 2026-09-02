package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type enrollmentAttachment struct {
	brokercontract.Attachment
	ref       brokercontract.WorkspaceEnrollmentRef
	result    brokercontract.WorkspaceEnrollmentResult
	catalogue brokercontract.WorkspaceCatalogue
}

func (a *enrollmentAttachment) BeginWorkspaceEnrollment(context.Context) (brokercontract.WorkspaceEnrollmentPresentation, error) {
	return brokercontract.WorkspaceEnrollmentPresentation{Ref: a.ref, URL: "https://broker.example/authorize?state=opaque"}, nil
}

func (a *enrollmentAttachment) ObserveWorkspaceEnrollment(context.Context, brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
	return a.result, nil
}

func (a *enrollmentAttachment) CancelWorkspaceEnrollment(context.Context, brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
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
}

func (b *enrollmentBroker) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
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

func TestWorkspaceEnrollmentPublishesFrozenCatalogueBeforePrompt(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	var catalogues [][]string
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		NewID:      func() session.SessionID { return "enrollment-session" }, MCPBroker: broker,
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
	created, err := svc.CreateSession(t.Context(), "/workspace", session.ModeDefault, session.Limits{})
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

	complete, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{
		enrollmentTool{name: "mcp__calendar__list"}, enrollmentTool{name: "mcp__github__review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.catalogue = complete
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
