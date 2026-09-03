package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	adapterbroker "github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

func testBrokerRuntime(t *testing.T) *adapterbroker.Runtime {
	t.Helper()
	declaration := mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "calendar", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	discovered := []adapterbroker.ToolDefinition{{Backend: "calendar", Name: "mcp__calendar__list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}
	catalogue, err := adapterbroker.Compile(declaration, discovered, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := adapterbroker.New(catalogue, func(context.Context, adapterbroker.SessionRef, string, session.ToolCall) (session.ToolResult, error) {
		return session.NewToolResult("call", "ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func brokerEngineResult() SessionEngineResult {
	return SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)}), Close: func() error { return nil }}
}

// brokerPlacementProvider binds every session to the same in-memory workspace;
// these tests exercise the broker attachment lifecycle, not placement itself.
type brokerPlacementProvider struct{}

func (brokerPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}
	return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/workspace"), nil)}, nil
}

func (brokerPlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	return PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace(req.Ref.ID), nil)}, nil
}

func TestMCPBrokerCanonicalAttachPersistReattachAndLocalClose(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	store := memstore.New()
	var factoryCalls int
	service, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "canonical" }, MCPBroker: runtime,
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			t.Fatal("broker session used the factory without explicit tools")
			return SessionEngineResult{}, nil
		},
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			factoryCalls++
			if tools == nil {
				t.Fatal("explicit broker tool slice was nil")
			}
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "canonical" || created.ExternalBinding == "" || factoryCalls != 1 {
		t.Fatalf("created = id %q binding %q factory calls %d", created.ID, created.ExternalBinding, factoryCalls)
	}
	loaded, err := store.Load(context.Background(), created.ID)
	if err != nil || loaded.ExternalBinding != created.ExternalBinding {
		t.Fatalf("persisted binding = %q, err %v", loaded.ExternalBinding, err)
	}

	service.CloseSession(created.ID)
	attachment, outcome, err := runtime.AttachSession(context.Background(), created.ID)
	if err != nil || outcome != brokercontract.AttachReattached {
		t.Fatalf("attach after local close = %q, %v", outcome, err)
	}
	_, _ = attachment.Close(context.Background())

	if _, err := service.buildAndRegisterSessionEngine(context.Background(), loaded, ProviderSelector{}, ProfileDefault, loaded.Mode, false); err != nil {
		t.Fatalf("reattach loaded session: %v", err)
	}
	service.CloseSession(created.ID)
	loaded.ExternalBinding = ""
	before := factoryCalls
	if _, err := service.buildAndRegisterSessionEngine(context.Background(), loaded, ProviderSelector{}, ProfileDefault, loaded.Mode, false); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("missing binding error = %v", err)
	}
	loaded.ExternalBinding = "wrong-binding"
	if _, err := service.buildAndRegisterSessionEngine(context.Background(), loaded, ProviderSelector{}, ProfileDefault, loaded.Mode, false); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("binding mismatch error = %v", err)
	}
	if factoryCalls != before {
		t.Fatal("binding mismatch reached catalogue factory")
	}
	service.Close()
}

type brokerSaveFailure struct{ port.SessionStore }

func (brokerSaveFailure) Save(context.Context, *session.Session) error {
	return errors.New("save failed")
}

func TestMCPBrokerPermanentDeleteRemovesLogicalState(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	service, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: memstore.New(),
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "permanent-delete" }, MCPBroker: runtime,
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	oldBinding := created.ExternalBinding
	if err := service.DeleteSession(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	attachment, outcome, err := runtime.AttachSession(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close(context.Background())
	if outcome != brokercontract.AttachCreated || attachment.Binding() == oldBinding {
		t.Fatalf("attach after permanent deletion = outcome %q binding %q, old %q", outcome, attachment.Binding(), oldBinding)
	}
	service.Close()
}

type flakyDeleteBroker struct {
	brokercontract.Service
	mu        sync.Mutex
	failNext  bool
	operation *[]string
}

func (b *flakyDeleteBroker) DeleteSession(ctx context.Context, id session.SessionID) (brokercontract.DeleteOutcome, error) {
	b.mu.Lock()
	*b.operation = append(*b.operation, "broker")
	if b.failNext {
		b.failNext = false
		b.mu.Unlock()
		return "", errors.New("broker unavailable")
	}
	b.mu.Unlock()
	return b.Service.DeleteSession(ctx, id)
}

type orderedDeleteStore struct {
	port.SessionStore
	prunable  port.PrunableStore
	operation *[]string
}

func (s orderedDeleteStore) List(ctx context.Context) ([]port.StoredSession, error) {
	return s.prunable.List(ctx)
}

func (s orderedDeleteStore) Delete(ctx context.Context, id session.SessionID) error {
	*s.operation = append(*s.operation, "store")
	return s.prunable.Delete(ctx, id)
}

func newFlakyDeleteService(t *testing.T, id session.SessionID) (*Service, *adapterbroker.Runtime, *memstore.Store, *[]string) {
	t.Helper()
	runtime := testBrokerRuntime(t)
	base := memstore.New()
	operations := &[]string{}
	store := orderedDeleteStore{SessionStore: base, prunable: base, operation: operations}
	broker := &flakyDeleteBroker{Service: runtime, failNext: true, operation: operations}
	service, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return id }, MCPBroker: broker,
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, runtime, base, operations
}

func TestMCPBrokerExplicitDeleteFailureKeepsHostRetryable(t *testing.T) {
	service, runtime, store, operations := newFlakyDeleteService(t, "retry-explicit")
	defer runtime.Close()
	created, err := service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.DeleteSession(t.Context(), created.ID); !errors.Is(err, ErrInternal) {
		t.Fatalf("first DeleteSession error = %v, want ErrInternal", err)
	}
	if _, err := store.Load(t.Context(), created.ID); err != nil {
		t.Fatalf("host session removed after broker failure: %v", err)
	}
	if got := *operations; len(got) != 1 || got[0] != "broker" {
		t.Fatalf("first delete operations = %v, want [broker]", got)
	}

	if err := service.DeleteSession(t.Context(), created.ID); err != nil {
		t.Fatalf("retry DeleteSession: %v", err)
	}
	if _, err := store.Load(t.Context(), created.ID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("host session after successful retry = %v, want ErrSessionNotFound", err)
	}
	if got := *operations; len(got) != 3 || got[1] != "broker" || got[2] != "store" {
		t.Fatalf("retry delete operations = %v, want [broker broker store]", got)
	}
	service.Close()
}

func TestMCPBrokerRetentionDeleteFailureKeepsHostRetryable(t *testing.T) {
	service, runtime, store, operations := newFlakyDeleteService(t, "retry-retention")
	defer runtime.Close()
	created, err := service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	if err := service.DeleteSessionForRetention(t.Context(), created.ID); !errors.Is(err, ErrInternal) {
		t.Fatalf("first retention delete error = %v, want ErrInternal", err)
	}
	if _, err := store.Load(t.Context(), created.ID); err != nil {
		t.Fatalf("host session removed after broker failure: %v", err)
	}
	if got := *operations; len(got) != 1 || got[0] != "broker" {
		t.Fatalf("first retention operations = %v, want [broker]", got)
	}

	if err := service.DeleteSessionForRetention(t.Context(), created.ID); err != nil {
		t.Fatalf("retry retention delete: %v", err)
	}
	if _, err := store.Load(t.Context(), created.ID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("host session after retention retry = %v, want ErrSessionNotFound", err)
	}
	if got := *operations; len(got) != 3 || got[1] != "broker" || got[2] != "store" {
		t.Fatalf("retry retention operations = %v, want [broker broker store]", got)
	}
	service.Close()
}

type gatedSaveFailure struct {
	port.SessionStore
	entered chan struct{}
	release chan struct{}
}

func (s gatedSaveFailure) Save(context.Context, *session.Session) error {
	close(s.entered)
	<-s.release
	return errors.New("save failed")
}

func TestMCPBrokerAttachRollbackSerializesAgainstReattachCommit(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	store := gatedSaveFailure{SessionStore: memstore.New(), entered: make(chan struct{}), release: make(chan struct{})}
	service, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "rollback-race" }, MCPBroker: runtime,
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	createDone := make(chan error, 1)
	go func() {
		_, createErr := service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		createDone <- createErr
	}()
	<-store.entered

	peer, outcome, err := runtime.AttachSession(context.Background(), "rollback-race")
	if err != nil || outcome != brokercontract.AttachReattached {
		t.Fatalf("peer attach = %q, %v", outcome, err)
	}
	defer peer.Close(context.Background())
	binding := peer.Binding()
	close(store.release)
	if err := <-createDone; err == nil {
		t.Fatal("create with failing save succeeded")
	}
	wrapped := peer.Tools()[0]
	if _, err := wrapped.Execute(context.Background(), session.NewToolCall("peer", wrapped.Spec().Name, json.RawMessage(`{}`)), tool.Environment{}); err != nil {
		t.Fatalf("creator rollback invalidated reattached peer: %v", err)
	}
	third, outcome, err := runtime.AttachSession(context.Background(), "rollback-race")
	if err != nil || outcome != brokercontract.AttachReattached || third.Binding() != binding {
		t.Fatalf("attach after creator rollback = %q, %v, binding %q; want %q", outcome, err, third.Binding(), binding)
	}
	_, _ = third.Close(context.Background())
	service.Close()
}

func TestMCPBrokerCloseSerializesAgainstEngineRebuild(t *testing.T) {
	declaration := mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "calendar", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	discovered := []adapterbroker.ToolDefinition{{Backend: "calendar", Name: "mcp__calendar__list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}
	catalogue, err := adapterbroker.Compile(declaration, discovered, nil)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	runtime, err := adapterbroker.New(catalogue, func(_ context.Context, _ adapterbroker.SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
		close(entered)
		<-release
		return session.NewToolResult(call.ID, "ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var factoryCalls atomic.Int32
	var wrapper tool.Tool
	service, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: memstore.New(),
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "close-race" }, MCPBroker: runtime,
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			factoryCalls.Add(1)
			wrapper = tools[0]
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := service.cfg.Store.Load(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, callErr := wrapper.Execute(context.Background(), session.NewToolCall("active", wrapper.Spec().Name, json.RawMessage(`{}`)), tool.Environment{})
		callDone <- callErr
	}()
	<-entered
	closeDone := make(chan struct{})
	go func() {
		service.CloseSession(created.ID)
		close(closeDone)
	}()
	for {
		service.mu.Lock()
		_, attached := service.brokerAttachments[created.ID]
		service.mu.Unlock()
		if !attached {
			break
		}
		time.Sleep(time.Millisecond)
	}
	buildDone := make(chan error, 1)
	go func() {
		_, buildErr := service.buildAndRegisterSessionEngine(context.Background(), loaded, ProviderSelector{}, ProfileDefault, loaded.Mode, false)
		buildDone <- buildErr
	}()
	select {
	case err := <-buildDone:
		t.Fatalf("engine rebuild raced attachment close: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls while close was waiting = %d", got)
	}
	close(release)
	if err := <-callDone; err != nil {
		t.Fatal(err)
	}
	<-closeDone
	if err := <-buildDone; err != nil {
		t.Fatal(err)
	}
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("factory calls after serialized rebuild = %d", got)
	}
	service.Close()
}

func TestMCPBrokerServiceShutdownBoundsAttachmentDrain(t *testing.T) {
	oldTimeout := engineCloseTimeout
	engineCloseTimeout = 25 * time.Millisecond
	defer func() { engineCloseTimeout = oldTimeout }()

	declaration := mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{{Name: "calendar", Auth: permconfig.MCPAuthProfile{Mode: "none"}}}}
	catalogue, err := adapterbroker.Compile(declaration, []adapterbroker.ToolDefinition{{Backend: "calendar", Name: "mcp__calendar__list", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	runtime, err := adapterbroker.New(catalogue, func(_ context.Context, _ adapterbroker.SessionRef, _ string, call session.ToolCall) (session.ToolResult, error) {
		close(entered)
		<-release
		return session.NewToolResult(call.ID, "ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wrapper tool.Tool
	service, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: memstore.New(), Diagnostics: port.NopDiagnostics{},
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "shutdown-drain" }, MCPBroker: runtime,
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			wrapper = tools[0]
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSession(context.Background(), session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, callErr := wrapper.Execute(context.Background(), session.NewToolCall("active", wrapper.Spec().Name, json.RawMessage(`{}`)), tool.Environment{})
		callDone <- callErr
	}()
	<-entered

	started := time.Now()
	service.Close()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Service.Close waited unboundedly for broker operation: %v", elapsed)
	}
	close(release)
	if err := <-callDone; err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPBrokerCreateRollbackDeletesOnlyNewLogicalState(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	service, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             brokerSaveFailure{SessionStore: memstore.New()},
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:     func() session.SessionID { return "rollback" },
		MCPBroker: runtime,
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSession(context.Background(), session.ModeDefault, session.Limits{}); err == nil {
		t.Fatal("create with failing store succeeded")
	}
	attachment, outcome, err := runtime.AttachSession(context.Background(), "rollback")
	if err != nil || outcome != brokercontract.AttachCreated {
		t.Fatalf("attach after rollback = %q, %v; logical state leaked", outcome, err)
	}
	_, _ = attachment.Close(context.Background())
	service.Close()
}
