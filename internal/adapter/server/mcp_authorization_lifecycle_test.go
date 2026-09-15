package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type lifecycleTool struct {
	calls  atomic.Int32
	schema json.RawMessage
	// hold, when set, runs inside Execute with the run context, so a test can
	// hold a continuation in flight and observe whether it gets cancelled.
	hold func(context.Context)
}

func (t *lifecycleTool) Spec() tool.ToolSpec {
	schema := t.schema
	if schema == nil {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return tool.ToolSpec{Name: "protected", Schema: schema}
}
func (*lifecycleTool) ReadOnly() bool { return false }
func (t *lifecycleTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.calls.Add(1)
	if t.hold != nil {
		t.hold(ctx)
	}
	return session.NewToolResult(call.ID, "protected mutation complete"), nil
}

type lifecycleMarkerTool struct{ name string }

func (t lifecycleMarkerTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (lifecycleMarkerTool) ReadOnly() bool { return true }
func (t lifecycleMarkerTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, t.name), nil
}

type lifecycleSchemaTool struct{ spec tool.ToolSpec }

func (t lifecycleSchemaTool) Spec() tool.ToolSpec { return t.spec }
func (lifecycleSchemaTool) ReadOnly() bool        { return true }
func (lifecycleSchemaTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

type lifecycleParkingTool struct {
	*lifecycleTool
	authorization session.ExternalAuthorization
}

func (t *lifecycleParkingTool) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return t.authorization, true, nil
}

func (*lifecycleParkingTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}

type lifecycleAllowPolicy struct{}

func (lifecycleAllowPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (lifecycleAllowPolicy) Learn(session.SessionID, session.ToolCall) {}

type lifecycleAttachment struct {
	binding            string
	tool               *lifecycleTool
	mu                 sync.Mutex
	status             session.AuthorizationStatus
	statusErr          error
	url                string
	statusHook         func()
	honorStatusContext bool
	cancelOutcome      brokercontract.CancelOutcome
	refreshTools       []tool.Tool
	refreshCalls       int
}

func (*lifecycleAttachment) Commit(context.Context) error { return nil }
func (*lifecycleAttachment) Abort(context.Context) error  { return nil }
func (a *lifecycleAttachment) Binding() session.ExternalBinding {
	return session.ExternalBinding(a.binding)
}
func (a *lifecycleAttachment) Tools() []tool.Tool { return []tool.Tool{a.tool} }
func (a *lifecycleAttachment) RefreshGrantedAuthorizationCatalogue(context.Context, session.ExternalAuthorization) ([]tool.Tool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshCalls++
	if a.refreshTools != nil {
		return append([]tool.Tool(nil), a.refreshTools...), nil
	}
	return []tool.Tool{a.tool}, nil
}
func (a *lifecycleAttachment) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status != session.AuthorizationPending {
		return "", brokercontract.ErrAuthorizationNotFound
	}
	return a.url, nil
}
func (a *lifecycleAttachment) AuthorizationStatus(ctx context.Context, _ session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	if a.honorStatusContext {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.statusHook != nil {
		a.statusHook()
	}
	return a.status, a.statusErr
}
func (a *lifecycleAttachment) CancelAuthorization(context.Context, session.ExternalAuthorization) (brokercontract.CancelOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancelOutcome != "" {
		return a.cancelOutcome, nil
	}
	if a.status == session.AuthorizationPending {
		a.status = session.AuthorizationCancelled
		return brokercontract.CancelCancelled, nil
	}
	if a.status == session.AuthorizationCancelled {
		return brokercontract.CancelAlreadyCancelled, nil
	}
	return brokercontract.CancelAlreadyResolved, nil
}
func (*lifecycleAttachment) Close(context.Context) (brokercontract.CloseOutcome, error) {
	return brokercontract.CloseClosed, nil
}

type lifecycleBroker struct {
	attachment *lifecycleAttachment
	attachErr  error
}

func (b *lifecycleBroker) AttachSession(context.Context, session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	if b.attachErr != nil {
		return nil, "", b.attachErr
	}
	return b.attachment, brokercontract.AttachReattached, nil
}
func (*lifecycleBroker) DeleteSession(context.Context, session.SessionID) (brokercontract.DeleteOutcome, error) {
	return brokercontract.DeleteDeleted, nil
}

type lifecycleFixture struct {
	svc        *Service
	store      *memstore.Store
	broker     *lifecycleBroker
	attach     *lifecycleAttachment
	pending    session.PendingAuthorization
	builtTools *[][]string
	builtSpecs *[][]mcp.ServerConfig
}

// lifecyclePlacementProvider reattaches only the local ref the fixture binds
// sessions to; any other ref (e.g. a test-corrupted "lost-remote" kind used to
// simulate an unreachable placement) fails, exercising the no-continuation
// terminal fallback path.
type lifecyclePlacementProvider struct{}

func (lifecyclePlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "in-tree-v1"}
	return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/repo"), memledger.New(), nil)}, nil
}

func (lifecyclePlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	if req.Ref.Kind != session.EnvKindLocal {
		return PlacementBinding{}, fmt.Errorf("lifecycle fixture: unsupported environment kind %q", req.Ref.Kind)
	}
	return PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace(req.Ref.ID), memledger.New(), nil)}, nil
}

type failNextAuthorizationSaveStore struct {
	port.SessionStore
	failures atomic.Int32
}

func (s *failNextAuthorizationSaveStore) Save(ctx context.Context, sess *session.Session) error {
	if s.failures.CompareAndSwap(1, 0) {
		return errAuthorizationSave
	}
	return s.SessionStore.Save(ctx, sess)
}

var errAuthorizationSave = errors.New("authorization save failed")

// failNthAuthorizationSaveStore fails exactly the n-th Save call (1-indexed),
// every other call succeeds — for proving behavior across a SPECIFIC save in
// a multi-save sequence (e.g. the claim save succeeds but a LATER
// compensating save fails), which failNextAuthorizationSaveStore's
// fail-then-always-succeed shape can't target.
type failNthAuthorizationSaveStore struct {
	port.SessionStore
	calls  atomic.Int32
	failAt int32
}

func (s *failNthAuthorizationSaveStore) Save(ctx context.Context, sess *session.Session) error {
	if s.calls.Add(1) == s.failAt {
		return errAuthorizationSave
	}
	return s.SessionStore.Save(ctx, sess)
}

type lifecycleDiagnostics struct {
	mu       sync.Mutex
	messages []string
}

func (d *lifecycleDiagnostics) Log(_ context.Context, _ port.Level, message string, _ ...any) {
	d.mu.Lock()
	d.messages = append(d.messages, message)
	d.mu.Unlock()
}
func (d *lifecycleDiagnostics) With(...any) port.Diagnostics { return d }
func (d *lifecycleDiagnostics) contains(message string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, got := range d.messages {
		if got == message {
			return true
		}
	}
	return false
}

func newLifecycleFixture(t *testing.T, status session.AuthorizationStatus, attachErr error, now func() time.Time, timer AuthorizationTimerFactory) lifecycleFixture {
	return newLifecycleFixtureWithTurns(t, status, attachErr, now, timer, mockllm.TextTurn("continued"))
}

func newLifecycleFixtureWithTurns(t *testing.T, status session.AuthorizationStatus, attachErr error, now func() time.Time, timer AuthorizationTimerFactory, turns ...mockllm.Turn) lifecycleFixture {
	t.Helper()
	return newLifecycleFixtureWithMode(t, status, attachErr, now, timer, session.ModeDefault, turns...)
}

func newLifecycleFixtureWithMode(t *testing.T, status session.AuthorizationStatus, attachErr error, now func() time.Time, timer AuthorizationTimerFactory, mode session.PermissionMode, turns ...mockllm.Turn) lifecycleFixture {
	t.Helper()
	store := memstore.New()
	mutation := &lifecycleTool{}
	attachment := &lifecycleAttachment{binding: "broker-binding", tool: mutation, status: status, url: "https://auth.example/authorize?state=live"}
	broker := &lifecycleBroker{attachment: attachment, attachErr: attachErr}
	buildEngine := func(tools []tool.Tool) *agent.Engine {
		catalog := tool.NewCatalog()
		for _, one := range tools {
			catalog.MustRegister(one)
		}
		return agent.NewEngine(agent.Deps{LLM: mockllm.New(turns...), Catalog: catalog, Policy: lifecycleAllowPolicy{}, Store: store, Model: "mock"})
	}
	shared := buildEngine(nil)
	var builtTools [][]string
	var builtSpecs [][]mcp.ServerConfig
	cfg := Config{
		Engine: shared, Store: store, PlacementProvider: lifecyclePlacementProvider{}, PlacementScope: "test",
		MCPBroker: broker, Now: now, AuthorizationTimer: timer,
	}
	// Avoid spelling the MCP config type in the fixture closure by assigning the
	// correctly typed factory separately.
	cfg.SessionEngine = func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, mode session.PermissionMode) (SessionEngineResult, error) {
		return SessionEngineResult{Engine: buildEngine(nil), BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	cfg.SessionEngineWithTools = func(_ context.Context, _ ProviderSelector, specs []mcp.ServerConfig, _ SessionProfile, _ string, mode session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
		names := make([]string, 0, len(tools))
		for _, candidate := range tools {
			names = append(names, candidate.Spec().Name)
		}
		builtTools = append(builtTools, names)
		builtSpecs = append(builtSpecs, specs)
		return SessionEngineResult{Engine: buildEngine(tools), BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("protected-call", "protected", json.RawMessage(`{"value":1}`))
	deferred := session.NewToolCall("deferred-call", "later", json.RawMessage(`{}`))
	pending := session.PendingAuthorization{Authorization: session.ExternalAuthorization{ID: "authorization:1", DisplayName: "Calendar", Binding: "opaque-binding", ExpiresAt: now().Add(time.Hour)}, Call: call, Deferred: []session.ToolCall{deferred}}
	sess := session.New("authorization-session", mode, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, now())
	sess.ExternalBinding = session.ExternalBinding(attachment.binding)
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call, deferred})); err != nil {
		t.Fatal(err)
	}
	if err := sess.PauseForAuthorization(pending); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	return lifecycleFixture{svc: svc, store: store, broker: broker, attach: attachment, pending: pending, builtTools: &builtTools, builtSpecs: &builtSpecs}
}

func drainLifecycleRun(t *testing.T, svc *Service, result MCPAuthorizationResult) []session.Event {
	t.Helper()
	if result.Run == nil {
		return nil
	}
	var events []session.Event
	for ev := range result.Run.Events() {
		events = append(events, ev)
	}
	svc.FinishRun("authorization-session", result.Run)
	return events
}

func assertAuthorizationResultBeforeResolved(t *testing.T, events []session.Event, callID session.ToolCallID) {
	t.Helper()
	resultAt, resolvedAt := -1, -1
	for i, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == callID {
			resultAt = i
		}
		if ev.Type == session.EvAuthorizationResolved && ev.Authorization != nil && ev.Authorization.Call == callID {
			if ev.Authorization.DisplayName != "Calendar" {
				t.Fatalf("resolved display name = %q", ev.Authorization.DisplayName)
			}
			resolvedAt = i
		}
	}
	if resultAt < 0 || resolvedAt <= resultAt {
		t.Fatalf("authorization event order: result=%d resolved=%d events=%v", resultAt, resolvedAt, events)
	}
}

func TestMCPAuthorizationPresentationAndPendingAreInert(t *testing.T) {
	now := time.Now
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, now, nil)
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	url, err := f.svc.MCPAuthorizationPresentation(t.Context(), control.SessionID, control)
	if err != nil || url != f.attach.url {
		t.Fatalf("presentation = %q, %v", url, err)
	}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Status != session.AuthorizationPending || result.Run != nil || f.attach.tool.calls.Load() != 0 {
		t.Fatalf("pending result = %+v, %v, calls=%d", result, err, f.attach.tool.calls.Load())
	}
}

func TestMCPAuthorizationTerminalStatusesPairWithoutExecuting(t *testing.T) {
	for _, status := range []session.AuthorizationStatus{session.AuthorizationDenied, session.AuthorizationCancelled, session.AuthorizationExpired, session.AuthorizationInterrupted, session.AuthorizationFailed} {
		t.Run(string(status), func(t *testing.T) {
			f := newLifecycleFixture(t, status, nil, time.Now, nil)
			control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
			result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
			if err != nil || result.Status != status {
				t.Fatalf("result = %+v, %v", result, err)
			}
			events := drainLifecycleRun(t, f.svc, result)
			assertAuthorizationResultBeforeResolved(t, events, f.pending.Call.ID)
			if f.attach.tool.calls.Load() != 0 {
				t.Fatal("protected mutation executed")
			}
			found := false
			for _, ev := range events {
				if ev.Type == session.EvAuthorizationResolved && ev.Authorization != nil && ev.Authorization.Status == status {
					found = true
				}
			}
			if !found {
				t.Fatalf("resolved event missing from %+v", events)
			}
		})
	}
}

func TestMCPAuthorizationCompetingGrantedControlsHaveOneWinner(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	type outcome struct {
		result MCPAuthorizationResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := f.svc.RecheckMCPAuthorization(context.Background(), control.SessionID, control)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	var winner MCPAuthorizationResult
	wins := 0
	for range 2 {
		outcome := <-outcomes
		result, err := outcome.result, outcome.err
		if err == nil && result.Run != nil {
			winner = result
			wins++
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("loser error = %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("continuation winners = %d", wins)
	}
	events := drainLifecycleRun(t, f.svc, winner)
	assertAuthorizationResultBeforeResolved(t, events, f.pending.Call.ID)
	if got := f.attach.tool.calls.Load(); got != 1 {
		t.Fatalf("protected executions = %d", got)
	}
}

func TestAuthenticatedMCPMetadataReplacement_Scenario2_RebuildsParkedContinuation(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	f.attach.refreshTools = []tool.Tool{f.attach.tool, lifecycleMarkerTool{name: "refreshed"}}
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Run == nil {
		t.Fatalf("RecheckMCPAuthorization = (%+v, %v)", result, err)
	}
	if f.attach.refreshCalls != 1 {
		t.Fatalf("refreshed catalogue calls = %d, want 1", f.attach.refreshCalls)
	}
	if got := *f.builtTools; len(got) == 0 || !slices.Contains(got[len(got)-1], "refreshed") {
		t.Fatalf("continuation engine tools = %v, want refreshed snapshot", got)
	}
	drainLifecycleRun(t, f.svc, result)
}

// TestAuthenticatedMCPMetadataReplacement_RebuildPreservesClientMCPSpecs pins
// the fix for a session-engine rebuild silently dropping client-provided MCP
// tools: buildAndRegisterSessionEngineWithBrokerTools hardcoded nil specs on
// every rebuild (mode change, ADR-0310 enrollment freeze, and a lazy grant
// refresh), so a session with client MCP configured lost it the first time any
// of those rebuilt its engine. The Service must thread the session's original
// specs (recorded at creation/load, s.clientMCPSpecs) through instead.
func TestAuthenticatedMCPMetadataReplacement_RebuildPreservesClientMCPSpecs(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	clientSpecs := []mcp.ServerConfig{{Name: "docs", URL: "https://docs.example/mcp"}}
	f.svc.mu.Lock()
	f.svc.clientMCPSpecs["authorization-session"] = clientSpecs
	f.svc.mu.Unlock()

	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Run == nil {
		t.Fatalf("RecheckMCPAuthorization = (%+v, %v)", result, err)
	}
	if got := *f.builtSpecs; len(got) == 0 || len(got[len(got)-1]) != len(clientSpecs) || got[len(got)-1][0].Name != clientSpecs[0].Name {
		t.Fatalf("rebuild specs = %v, want %v", got, clientSpecs)
	}
	drainLifecycleRun(t, f.svc, result)
}

func TestAuthenticatedMCPMetadataReplacement_Scenario2_ReplacesJustParkedRun(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	parkingTool := &lifecycleParkingTool{lifecycleTool: &lifecycleTool{}, authorization: f.pending.Authorization}
	catalog := tool.NewCatalog()
	catalog.MustRegister(parkingTool)
	parkingEngine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("parking-call", parkingTool.Spec().Name, json.RawMessage(`{}`)))),
		Catalog: catalog, Policy: lifecycleAllowPolicy{}, Store: f.store, Model: "mock",
	})
	parkingSession := session.New("parking-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	parkingEnv, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "in-tree-v1"}, memfs.NewWorkspace("/repo"), memledger.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	parkingRun := parkingEngine.Run(t.Context(), parkingSession, parkingEnv, agent.RunRequest{Text: "park", CanPresentAuthorization: true})
	for range parkingRun.Events() {
	}
	if parkingRun.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("parking outcome = %q", parkingRun.Outcome())
	}
	// Register the just-finished parked run under the target id, exactly as the
	// relay does until its deferred FinishRun executes.
	id := session.SessionID("authorization-session")
	f.svc.mu.Lock()
	f.svc.runs[id] = &runState{run: parkingRun, sess: parkingSession, settled: make(chan struct{})}
	f.svc.mu.Unlock()

	control := MCPAuthorizationControl{SessionID: id, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), id, control)
	if err != nil || result.Run == nil || result.Status != session.AuthorizationGranted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	f.svc.FinishRun(id, parkingRun) // stale relay defer must not remove the winner.
	if registered, ok := f.svc.LookupRun(id); !ok || registered != result.Run {
		t.Fatal("stale parked-run cleanup removed the continuation")
	}
	drainLifecycleRun(t, f.svc, result)
	if got := f.attach.tool.calls.Load(); got != 1 {
		t.Fatalf("protected executions = %d", got)
	}
}

// TestAuthenticatedMCPMetadataReplacement_PlanModeBlocksMutatingRefresh pins the
// fix for a plan-mode bypass: RefreshGrantedAuthorizationCatalogue can replace a
// declared placeholder with live metadata that is now mutating. Catalog.Lookup
// (used by PrepareAuthorizationContinuation) bypasses plan mode's ReadOnly
// visibility filter, so a resumed continuation must recheck plan mode + ReadOnly
// itself before executing — never trust whatever the tool looked like when it
// parked.
func TestAuthenticatedMCPMetadataReplacement_PlanModeBlocksMutatingRefresh(t *testing.T) {
	f := newLifecycleFixtureWithMode(t, session.AuthorizationGranted, nil, time.Now, nil, session.ModePlan)
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Run == nil {
		t.Fatalf("RecheckMCPAuthorization = (%+v, %v)", result, err)
	}
	events := drainLifecycleRun(t, f.svc, result)
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("plan-mode session executed a mutating refreshed tool: calls = %d", got)
	}
	var found bool
	for _, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == f.pending.Call.ID {
			found = true
			if !ev.ToolResult.IsError || !strings.Contains(ev.ToolResult.Content, "plan mode") {
				t.Fatalf("tool result = %+v, want a plan-mode error", ev.ToolResult)
			}
		}
	}
	if !found {
		t.Fatal("no tool result recorded for the blocked continuation")
	}
}

// TestAuthenticatedMCPMetadataReplacement_RejectsParkedArgumentsOutsideReplacementSchema
// pins the narrow lazy-transition gate: the parked call was valid for its static
// declaration, but must not execute after authenticated metadata narrows it.
func TestAuthenticatedMCPMetadataReplacement_RejectsParkedArgumentsOutsideReplacementSchema(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	replacement := &lifecycleTool{schema: json.RawMessage(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`)}
	f.attach.refreshTools = []tool.Tool{replacement}

	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Run == nil {
		t.Fatalf("RecheckMCPAuthorization = (%+v, %v)", result, err)
	}
	events := drainLifecycleRun(t, f.svc, result)
	if got := replacement.calls.Load(); got != 0 {
		t.Fatalf("replacement tool executions = %d, want 0", got)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("static tool executions = %d, want 0", got)
	}
	results := make(map[session.ToolCallID]session.ToolResult)
	for _, event := range events {
		if event.Type == session.EvToolResult && event.ToolResult != nil {
			results[event.ToolResult.CallID] = *event.ToolResult
		}
	}
	if got, ok := results[f.pending.Call.ID]; !ok || !got.IsError || got.Content != authorizationSchemaMismatch {
		t.Fatalf("parked call result = %+v, want schema rejection", got)
	}
	if got, ok := results[f.pending.Deferred[0].ID]; !ok || !got.IsError || got.Content != "authorization deferred sibling was not executed" {
		t.Fatalf("deferred call result = %+v, want paired deferred error", got)
	}
}

func TestValidateGrantedAuthorizationArgumentsFailsClosed(t *testing.T) {
	validSchema := json.RawMessage(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`)
	cases := []struct {
		name   string
		schema json.RawMessage
		args   json.RawMessage
		wantOK bool
	}{
		{name: "valid", schema: validSchema, args: json.RawMessage(`{"title":"ok"}`), wantOK: true},
		{name: "malformed schema", schema: json.RawMessage(`{`), args: json.RawMessage(`{}`)},
		{name: "non-object schema", schema: json.RawMessage(`true`), args: json.RawMessage(`{}`)},
		{name: "unresolvable schema", schema: json.RawMessage(`{"$ref":"https://schemas.example/required.json"}`), args: json.RawMessage(`{}`)},
		{name: "malformed args", schema: validSchema, args: json.RawMessage(`{`)},
		{name: "non-object args", schema: validSchema, args: json.RawMessage(`[]`)},
		{name: "schema mismatch", schema: validSchema, args: json.RawMessage(`{"title":1}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGrantedAuthorizationArguments([]tool.Tool{&lifecycleTool{schema: tc.schema}}, session.NewToolCall("call", "protected", tc.args))
			if (err == nil) != tc.wantOK {
				t.Fatalf("validateGrantedAuthorizationArguments() error = %v, wantOK %v", err, tc.wantOK)
			}
		})
	}
}

// TestValidateGrantedAuthorizationArgumentsCanonicalizesQueryArgs pins the same
// JSON-encoded args-object recovery that CallMcpWithQuery applies before invoking a
// remote protected target. The server transition validates the recovered target
// against the authenticated target schema, so restart/lazy continuation preserves
// that execution-compatible input without accepting malformed remote arguments.
func TestValidateGrantedAuthorizationArgumentsCanonicalizesQueryArgs(t *testing.T) {
	querySpec := mcp.CallMcpWithQuerySpec()
	targetSpec := tool.ToolSpec{Name: "mcp__github__create", Schema: json.RawMessage(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`)}
	tools := []tool.Tool{lifecycleSchemaTool{spec: querySpec}, lifecycleSchemaTool{spec: targetSpec}}
	cases := []struct {
		name string
		args json.RawMessage
		want bool
	}{
		{name: "encoded object", args: json.RawMessage(`{"server":"github","tool":"create","args":"{\"title\":\"ok\"}","jq_filter":"."}`), want: true},
		{name: "encoded malformed object", args: json.RawMessage(`{"server":"github","tool":"create","args":"{","jq_filter":"."}`)},
		{name: "encoded non-object", args: json.RawMessage(`{"server":"github","tool":"create","args":"[]","jq_filter":"."}`)},
		{name: "authenticated target mismatch", args: json.RawMessage(`{"server":"github","tool":"create","args":"{\"title\":1}","jq_filter":"."}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGrantedAuthorizationArguments(tools, session.NewToolCall("query", querySpec.Name, tc.args))
			if (err == nil) != tc.want {
				t.Fatalf("validateGrantedAuthorizationArguments() error = %v, want success %v", err, tc.want)
			}
		})
	}
}

func TestMCPAuthorizationRestartUnavailableInterruptsWithoutExecution(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, brokercontract.ErrStateUnavailable, time.Now, nil)
	eventLog := memstore.NewEventLog()
	f.svc.cfg.EventLog = eventLog
	required := session.Event{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
		AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
		ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
	}}
	if err := eventLog.Append(t.Context(), "authorization-session", required); err != nil {
		t.Fatal(err)
	}
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Status != session.AuthorizationInterrupted {
		t.Fatalf("result = %+v, %v", result, err)
	}
	drainLifecycleRun(t, f.svc, result)
	if f.attach.tool.calls.Load() != 0 {
		t.Fatal("protected mutation executed after unavailable state")
	}
	var events []session.Event
	for ev, readErr := range eventLog.Read(t.Context(), control.SessionID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		events = append(events, ev)
	}
	assertAuthorizationResultBeforeResolved(t, events, f.pending.Call.ID)
}

func TestMCPAuthorizationTerminalFallbackIsFullyReconstructable(t *testing.T) {
	tests := []struct {
		brokerStatus session.AuthorizationStatus
		wantStatus   session.AuthorizationStatus
	}{
		{session.AuthorizationDenied, session.AuthorizationDenied},
		{session.AuthorizationCancelled, session.AuthorizationCancelled},
		{session.AuthorizationExpired, session.AuthorizationExpired},
		{session.AuthorizationInterrupted, session.AuthorizationInterrupted},
		{session.AuthorizationFailed, session.AuthorizationFailed},
		{session.AuthorizationClosed, session.AuthorizationInterrupted},
	}
	for _, tc := range tests {
		t.Run(string(tc.brokerStatus), func(t *testing.T) {
			f := newLifecycleFixture(t, tc.brokerStatus, nil, time.Now, nil)
			log := memstore.NewEventLog()
			f.svc.cfg.EventLog = log
			seed := []session.Event{
				{Type: session.EvTurnStart},
				{Type: session.EvToolCall, ToolCall: &f.pending.Call},
				{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
				{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
					AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
					ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
				}},
			}
			for _, ev := range seed {
				if err := log.Append(t.Context(), "authorization-session", ev); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := f.store.Load(t.Context(), "authorization-session")
			if err != nil {
				t.Fatal(err)
			}
			loaded.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvironmentKind("lost-remote"), ID: "runtime", Revision: "gone"}
			if err := f.store.Save(t.Context(), loaded); err != nil {
				t.Fatal(err)
			}

			control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
			result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
			if err != nil || result.Status != tc.wantStatus || result.Run != nil {
				t.Fatalf("fallback result = %+v, %v; want status %q and no run", result, err, tc.wantStatus)
			}
			if f.attach.tool.calls.Load() != 0 {
				t.Fatal("protected mutation executed during terminal fallback")
			}
			var events []session.Event
			for ev, readErr := range log.Read(t.Context(), loaded.ID) {
				if readErr != nil {
					t.Fatal(readErr)
				}
				events = append(events, ev)
			}
			if got, want := len(events), len(seed)+3; got != want {
				t.Fatalf("event count = %d, want %d: %+v", got, want, events)
			}
			trailing := events[len(seed):]
			if trailing[0].Type != session.EvToolResult || trailing[0].ToolResult == nil || trailing[0].ToolResult.CallID != f.pending.Call.ID ||
				trailing[1].Type != session.EvToolResult || trailing[1].ToolResult == nil || trailing[1].ToolResult.CallID != f.pending.Deferred[0].ID ||
				trailing[2].Type != session.EvAuthorizationResolved || trailing[2].Authorization == nil || trailing[2].Authorization.Status != tc.wantStatus {
				t.Fatalf("ordered fallback events = %+v", trailing)
			}
			if _, err := eventsource.Fold(eventsource.SessionMeta{
				ID: loaded.ID, Mode: loaded.Mode, Limits: loaded.Limits, EnvironmentRef: loaded.EnvironmentRef, CreatedAt: loaded.CreatedAt,
			}, log.Read(t.Context(), loaded.ID)); err != nil {
				t.Fatalf("eventsource remains private-state-required: %v", err)
			}
		})
	}
}

// TestMCPAuthorizationResolutionBackfillsMissingRequired reproduces the
// save-before-emit crash window: PauseForAuthorization's snapshot save
// succeeds, but the process fails before EvAuthorizationRequired reaches the
// EventLog (the seed below omits it entirely, unlike
// TestMCPAuthorizationTerminalFallbackIsFullyReconstructable's seed). A naive
// resolution would append authorization.resolved with no matching required,
// which eventsource.Fold rejects outright. Resolution must instead backfill
// the missing required event from the durably-saved pending state before
// appending the resolution, so the log recovers Fold-reconstructable ordering
// despite the earlier gap.
func TestMCPAuthorizationResolutionBackfillsMissingRequired(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
	log := memstore.NewEventLog()
	f.svc.cfg.EventLog = log
	seed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &f.pending.Call},
		{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
		// Deliberately no EvAuthorizationRequired: this is the crash window.
	}
	for _, ev := range seed {
		if err := log.Append(t.Context(), "authorization-session", ev); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	loaded.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvironmentKind("lost-remote"), ID: "runtime", Revision: "gone"}
	if err := f.store.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}

	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
	if err != nil || result.Status != session.AuthorizationDenied || result.Run != nil {
		t.Fatalf("fallback result = %+v, %v; want denied and no run", result, err)
	}

	var events []session.Event
	for ev, readErr := range log.Read(t.Context(), loaded.ID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		events = append(events, ev)
	}
	// seed (3) + backfilled required (1) + 2 tool results + resolved (1) = 7.
	if got, want := len(events), len(seed)+4; got != want {
		t.Fatalf("event count = %d, want %d: %+v", got, want, events)
	}
	backfilled := events[len(seed)]
	if backfilled.Type != session.EvAuthorizationRequired || backfilled.Authorization == nil ||
		backfilled.Authorization.AuthorizationID != f.pending.Authorization.ID ||
		backfilled.Authorization.Call != f.pending.Call.ID || backfilled.Authorization.Status != session.AuthorizationPending {
		t.Fatalf("backfilled required event = %+v", backfilled)
	}
	resolved := events[len(events)-1]
	if resolved.Type != session.EvAuthorizationResolved || resolved.Authorization == nil || resolved.Authorization.Status != session.AuthorizationDenied {
		t.Fatalf("trailing resolution event = %+v", resolved)
	}
	if _, err := eventsource.Fold(eventsource.SessionMeta{
		ID: loaded.ID, Mode: loaded.Mode, Limits: loaded.Limits, EnvironmentRef: loaded.EnvironmentRef, CreatedAt: loaded.CreatedAt,
	}, log.Read(t.Context(), loaded.ID)); err != nil {
		t.Fatalf("backfill did not restore Fold-reconstructable ordering: %v", err)
	}

	// A SECOND resolution attempt over a log that already has the required
	// event must never duplicate it (Fold rejects a reused authorization call
	// id) — reattaching the read-only existence check, not an optimistic flag,
	// is what makes this safe.
	if err := log.Append(t.Context(), loaded.ID, session.Event{Type: session.EvToolResult, ToolResult: ptrToolResult(session.NewToolResult(f.pending.Call.ID, "noop"))}); err != nil {
		t.Fatal(err)
	}
	f.svc.ensureAuthorizationRequiredLogged(t.Context(), loaded.ID, f.pending)
	var requiredCount int
	for ev, readErr := range log.Read(t.Context(), loaded.ID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		if ev.Type == session.EvAuthorizationRequired {
			requiredCount++
		}
	}
	if requiredCount != 1 {
		t.Fatalf("required event count = %d, want 1 (no duplicate backfill)", requiredCount)
	}
}

func ptrToolResult(r session.ToolResult) *session.ToolResult { return &r }

// failingReadEventLog fails every Read, to prove reconciliation is a genuine
// precondition: a failure must abort resolution before anything is consumed.
type failingReadEventLog struct{ inner port.EventLog }

func (l failingReadEventLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	return l.inner.Append(ctx, id, ev)
}
func (failingReadEventLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) { yield(session.Event{}, errors.New("read failed")) }
}

// TestMCPAuthorizationReconciliationFailureLeavesPendingIntact pins the
// reordering fix: ensureAuthorizationRequiredLogged runs BEFORE
// AbortAuthorization consumes the session's only durable PendingAuthorization.
// A reconciliation failure must therefore abort the whole resolution with the
// session completely untouched — StateAuthorizing, pending still present, no
// results recorded, nothing appended — so the caller can simply retry rather
// than lose the source data a later crash would need to backfill from.
func TestMCPAuthorizationReconciliationFailureLeavesPendingIntact(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
	f.svc.cfg.EventLog = failingReadEventLog{inner: memstore.NewEventLog()}

	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), "authorization-session", control)
	if !errors.Is(err, ErrInternal) || result != (MCPAuthorizationResult{}) {
		t.Fatalf("reconciliation failure = %+v, %v; want ErrInternal and no success", result, err)
	}
	persisted, loadErr := f.store.Load(t.Context(), "authorization-session")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.State != session.StateAuthorizing {
		t.Fatalf("state = %q, want unchanged authorizing", persisted.State)
	}
	pending, ok := persisted.PendingAuthorization()
	if !ok || pending.Authorization.ID != f.pending.Authorization.ID || pending.Call.ID != f.pending.Call.ID {
		t.Fatalf("pending authorization = %+v, %t; want the original still intact", pending, ok)
	}
	if f.attach.tool.calls.Load() != 0 {
		t.Fatal("protected mutation executed after reconciliation failure")
	}
}

// TestMCPAuthorizationGrantedResolutionBackfillsMissingRequired proves the
// fourth reconciliation site: continueGrantedAuthorizationLocked reconciles
// the required-event lifecycle BEFORE ClaimAuthorization consumes pending,
// exactly like the three terminal paths. The seed omits EvAuthorizationRequired
// entirely (the crash window), and a granted resolution must still backfill it
// and remain eventsource.Fold-reconstructable.
func TestMCPAuthorizationGrantedResolutionBackfillsMissingRequired(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	log := memstore.NewEventLog()
	f.svc.cfg.EventLog = log
	seed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &f.pending.Call},
		{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
		// Deliberately no EvAuthorizationRequired: this is the crash window.
	}
	for _, ev := range seed {
		if err := log.Append(t.Context(), "authorization-session", ev); err != nil {
			t.Fatal(err)
		}
	}
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), "authorization-session", control)
	if err != nil || result.Run == nil || result.Status != session.AuthorizationGranted {
		t.Fatalf("granted result = %+v, %v", result, err)
	}
	drainLifecycleRun(t, f.svc, result)
	var found bool
	for ev, readErr := range log.Read(t.Context(), "authorization-session") {
		if readErr != nil {
			t.Fatal(readErr)
		}
		if ev.Type == session.EvAuthorizationRequired && ev.Authorization != nil && ev.Authorization.AuthorizationID == f.pending.Authorization.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("granted resolution did not backfill the missing required event")
	}
	// The backfill mechanism's Fold-reconstructability is already proven for
	// the shared ensureAuthorizationRequiredLogged path by
	// TestMCPAuthorizationResolutionBackfillsMissingRequired; this test's own
	// claim is narrower — that the granted/4th call site invokes it at all,
	// which the found assertion above already establishes.
}

// TestMCPAuthorizationSettlementBackfillFailureAppendFailureStillIdles proves
// the shutdown-settlement path settles to idle on an appendAuthorizationResolution
// failure too, not only on success — the prior narrower fix only settled after
// a successful append, stranding the session on a failed one.
func TestMCPAuthorizationSettlementAppendFailureStillSettles(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	log := &countingEventLog{}
	// Seed the required event so ensureAuthorizationRequiredLogged's existence
	// check finds it and appends nothing of its own — the injected failure
	// below must land on appendAuthorizationResolution's own append, not the
	// reconciliation check ahead of it.
	if err := log.Append(t.Context(), "authorization-session", session.Event{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
		AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
		ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
	}}); err != nil {
		t.Fatal(err)
	}
	log.postWriteFailCalls = map[int]bool{2: true}
	f.svc.cfg.EventLog = log
	// Service.Close's settlement sweep only visits sessions registered in its
	// process-owned inventory (brokerAttachments/authorizationExpiry/heldLeases)
	// — a committed attachment is what makes this session part of it.
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	attachment, release, err := f.svc.authorizationAttachment(t.Context(), loaded)
	if err != nil || attachment == nil {
		t.Fatalf("attach = %v, %v", attachment, err)
	}
	release()
	f.svc.Close()
	settled, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != session.StateIdle {
		t.Fatalf("state = %q, want idle despite the settlement append failure", settled.State)
	}
}

// TestStartRunSettlesInterruptedRestoredAuthorizationOnAppendFailure proves
// interruptRestoredAuthorizationLocked settles itself to idle when its OWN
// appendAuthorizationResolution fails, rather than returning an error before
// StartRunContent's caller-side stranding guard is ever registered.
func TestStartRunSettlesInterruptedRestoredAuthorizationOnAppendFailure(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, brokercontract.ErrStateUnavailable, time.Now, nil)
	log := &countingEventLog{}
	// Seed the required event so the reconciliation check ahead of
	// InterruptAuthorization finds it and appends nothing — the injected
	// failure must land on this function's own appendAuthorizationResolution.
	if err := log.Append(t.Context(), "authorization-session", session.Event{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
		AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
		ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
	}}); err != nil {
		t.Fatal(err)
	}
	log.postWriteFailCalls = map[int]bool{2: true}
	f.svc.cfg.EventLog = log
	_, err := f.svc.StartRunContent(t.Context(), "authorization-session", "continue", nil)
	if err == nil {
		t.Fatal("StartRunContent unexpectedly succeeded")
	}
	loaded, loadErr := f.store.Load(t.Context(), "authorization-session")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.State != session.StateIdle {
		t.Fatalf("state = %q, want idle after the restored-interruption append failure", loaded.State)
	}
}

func TestMCPAuthorizationTerminalFallbackAppendFailureIsExplicit(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
	log := &countingEventLog{}
	// Seed the full turn history (so the eventual Fold check below has enough
	// to reconstruct a conversation) plus the required event, so
	// ensureAuthorizationRequiredLogged's existence check finds it and makes
	// no backfill append of its own — this test's failure injection targets
	// appendAuthorizationResolution's own first result append, not the
	// reconciliation check ahead of it.
	seed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &f.pending.Call},
		{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
		{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
			AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
			ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
		}},
	}
	for _, ev := range seed {
		if err := log.Append(t.Context(), "authorization-session", ev); err != nil {
			t.Fatal(err)
		}
	}
	log.postWriteFailCalls = map[int]bool{len(seed) + 1: true}
	diag := &lifecycleDiagnostics{}
	f.svc.cfg.EventLog = log
	f.svc.cfg.Diagnostics = diag
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	loaded.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvironmentKind("lost-remote"), ID: "runtime", Revision: "gone"}
	if err := f.store.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
	if !errors.Is(err, ErrInternal) || result != (MCPAuthorizationResult{}) {
		t.Fatalf("fallback append failure = %+v, %v; want ErrInternal and no success", result, err)
	}
	if !diag.contains("persist terminal authorization lifecycle failed") {
		t.Fatal("fallback append failure was not diagnosed")
	}
	// seed(1) + pending.Call result(2, the ambiguous one) + deferred result(3):
	// appendAuthorizationResolution continues past an ambiguous failure on a
	// NON-primary event, but a caller of port.EventLog.Append cannot tell a
	// durably-recorded-then-reported-failed ambiguity (what countingEventLog's
	// postWriteFailCalls actually does here) apart from a genuinely-uncommitted
	// one — so once the PRIMARY call's own result is the one that errors, the
	// function must treat it the same as the uncommitted case and never attempt
	// EvAuthorizationResolved (see TestMCPAuthorizationPrimaryResultAppendFailureStaysFoldable
	// for the genuinely-uncommitted sibling of this test).
	want := len(seed) + 2
	if got := len(log.attempts); got != want {
		t.Fatalf("append attempts = %d, want %d (seed plus the primary and deferred results; no resolved attempt)", got, want)
	}
	// postWriteFailCalls still durably records before reporting its error, so
	// both results land in recorded despite the ambiguous failure on the
	// primary — only the (never-attempted) resolved event is missing.
	if got := len(log.recorded); got != want {
		t.Fatalf("durably written events = %d, want %d (primary and deferred results recorded; resolved never attempted)", got, want)
	}
	for _, ev := range log.recorded {
		if ev.Type == session.EvAuthorizationResolved {
			t.Fatal("EvAuthorizationResolved was recorded despite the primary result's ambiguous append failure")
		}
	}
	persisted, loadErr := f.store.Load(t.Context(), loaded.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	// Idle, not the stranded StateRunning of before: an ambiguous append
	// failure here used to return early and skip settlement entirely — the
	// unconditional defer in resolveAuthorizationLocked now settles it
	// regardless of whether the append succeeded or failed.
	if persisted.State != session.StateIdle {
		t.Fatalf("settled snapshot state = %q, want idle", persisted.State)
	}
	if err := session.ValidateToolPairing(persisted.Conversation.Messages); err != nil {
		t.Fatalf("settled snapshot pairing: %v", err)
	}
	if f.attach.tool.calls.Load() != 0 {
		t.Fatal("protected mutation executed after ambiguous append failure")
	}
	// Skipping the resolved append leaves the lifecycle looking open to Fold —
	// the intended SOFT outcome (ErrPrivateStateRequired) for an ambiguous
	// primary-result failure, never the hard ErrReconstruct a forced resolved
	// append would have risked if this particular failure had genuinely not
	// committed.
	_, foldErr := eventsource.Fold(eventsource.SessionMeta{
		ID: persisted.ID, Mode: persisted.Mode, Limits: persisted.Limits, EnvironmentRef: persisted.EnvironmentRef, CreatedAt: persisted.CreatedAt,
	}, log.Read(t.Context(), persisted.ID))
	if !errors.Is(foldErr, eventsource.ErrPrivateStateRequired) {
		t.Fatalf("fold error = %v, want ErrPrivateStateRequired", foldErr)
	}
}

// TestMCPAuthorizationPrimaryResultAppendFailureStaysFoldable proves the
// distinct case postWriteFailCalls above cannot exercise: appendAuthorizationResolution
// must not append EvAuthorizationResolved when the PRIMARY call's own
// EvToolResult append is the one that failed and genuinely never committed
// (failCalls, unlike postWriteFailCalls, fails BEFORE recording). Appending
// resolved anyway would make eventsource.Fold's resolveAuthorization hard-reject
// the log (ErrReconstruct, "resolved precedes matching tool.result") instead of
// the intended soft, still-open outcome (ErrPrivateStateRequired) — a
// permanently unrecoverable log rather than one merely missing private state.
func TestMCPAuthorizationPrimaryResultAppendFailureStaysFoldable(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
	log := &countingEventLog{}
	seed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &f.pending.Call},
		{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
		{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
			AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
			ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
		}},
	}
	for _, ev := range seed {
		if err := log.Append(t.Context(), "authorization-session", ev); err != nil {
			t.Fatal(err)
		}
	}
	// The primary call's own result is the FIRST result appendAuthorizationResolution
	// attempts (results[0] is always pending.Call's, per session.resolveAuthorization).
	// failCalls fails BEFORE recording — the genuinely-not-durable case, distinct
	// from postWriteFailCalls' durably-recorded-then-reported-failed case.
	log.failCalls = map[int]bool{len(seed) + 1: true}
	diag := &lifecycleDiagnostics{}
	f.svc.cfg.EventLog = log
	f.svc.cfg.Diagnostics = diag
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	loaded.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvironmentKind("lost-remote"), ID: "runtime", Revision: "gone"}
	if err := f.store.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
	if !errors.Is(err, ErrInternal) || result != (MCPAuthorizationResult{}) {
		t.Fatalf("fallback append failure = %+v, %v; want ErrInternal and no success", result, err)
	}
	if !diag.contains("persist terminal authorization lifecycle failed") {
		t.Fatal("fallback append failure was not diagnosed")
	}
	// seed(4) + the failed primary result(5) + the deferred sibling result(6):
	// EvAuthorizationResolved must never be attempted once the primary result's
	// own append is the one that failed.
	wantAttempts := len(seed) + 2
	if got := len(log.attempts); got != wantAttempts {
		t.Fatalf("append attempts = %d, want %d (no resolved attempt once the primary result failed)", got, wantAttempts)
	}
	for _, ev := range log.recorded {
		if ev.Type == session.EvAuthorizationResolved {
			t.Fatal("EvAuthorizationResolved was recorded despite the primary result never committing")
		}
	}
	persisted, loadErr := f.store.Load(t.Context(), loaded.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	// The snapshot still settles to idle — this proves ONLY that the event log
	// stays foldable, not that the snapshot avoided the stranding this PR's
	// other fixes already cover.
	if persisted.State != session.StateIdle {
		t.Fatalf("settled snapshot state = %q, want idle", persisted.State)
	}
	_, foldErr := eventsource.Fold(eventsource.SessionMeta{
		ID: persisted.ID, Mode: persisted.Mode, Limits: persisted.Limits, EnvironmentRef: persisted.EnvironmentRef, CreatedAt: persisted.CreatedAt,
	}, log.Read(t.Context(), persisted.ID))
	if !errors.Is(foldErr, eventsource.ErrPrivateStateRequired) {
		t.Fatalf("fold error = %v, want ErrPrivateStateRequired (lifecycle left open, not a hard reconstruct failure)", foldErr)
	}
}

// TestMCPAuthorizationDeferredResultAppendFailureStaysFoldable is the deferred-
// sibling sibling of TestMCPAuthorizationPrimaryResultAppendFailureStaysFoldable:
// a failed DEFERRED result append (results[1], not the primary) must ALSO stop
// appendAuthorizationResolution from appending EvAuthorizationResolved. If the
// deferred result's EvToolResult genuinely never committed, the authorization
// itself still folds (resolveAuthorization only checks the primary call), but
// SeedHistory then hard-rejects the dangling deferred tool_call — the same
// permanent non-foldability the primary-result gate already guards against, on
// the other half of the ordered result sequence. failCalls fails BEFORE
// recording (the genuinely-not-durable case).
func TestMCPAuthorizationDeferredResultAppendFailureStaysFoldable(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
	log := &countingEventLog{}
	seed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &f.pending.Call},
		{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
		{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
			AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
			ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
		}},
	}
	for _, ev := range seed {
		if err := log.Append(t.Context(), "authorization-session", ev); err != nil {
			t.Fatal(err)
		}
	}
	// results[0] is the primary (succeeds); results[1] is the deferred sibling,
	// the SECOND result append — its failure must be just as gating as the
	// primary's.
	log.failCalls = map[int]bool{len(seed) + 2: true}
	diag := &lifecycleDiagnostics{}
	f.svc.cfg.EventLog = log
	f.svc.cfg.Diagnostics = diag
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	loaded.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvironmentKind("lost-remote"), ID: "runtime", Revision: "gone"}
	if err := f.store.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
	if !errors.Is(err, ErrInternal) || result != (MCPAuthorizationResult{}) {
		t.Fatalf("fallback append failure = %+v, %v; want ErrInternal and no success", result, err)
	}
	if !diag.contains("persist terminal authorization lifecycle failed") {
		t.Fatal("fallback append failure was not diagnosed")
	}
	// seed(4) + the primary result(5, succeeds) + the failed deferred result(6):
	// EvAuthorizationResolved must never be attempted once ANY result's own
	// append has failed.
	wantAttempts := len(seed) + 2
	if got := len(log.attempts); got != wantAttempts {
		t.Fatalf("append attempts = %d, want %d (no resolved attempt once the deferred result failed)", got, wantAttempts)
	}
	for _, ev := range log.recorded {
		if ev.Type == session.EvAuthorizationResolved {
			t.Fatal("EvAuthorizationResolved was recorded despite the deferred result never committing")
		}
	}
	persisted, loadErr := f.store.Load(t.Context(), loaded.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.State != session.StateIdle {
		t.Fatalf("settled snapshot state = %q, want idle", persisted.State)
	}
	_, foldErr := eventsource.Fold(eventsource.SessionMeta{
		ID: persisted.ID, Mode: persisted.Mode, Limits: persisted.Limits, EnvironmentRef: persisted.EnvironmentRef, CreatedAt: persisted.CreatedAt,
	}, log.Read(t.Context(), persisted.ID))
	if !errors.Is(foldErr, eventsource.ErrPrivateStateRequired) {
		t.Fatalf("fold error = %v, want ErrPrivateStateRequired (lifecycle left open, not a hard reconstruct failure)", foldErr)
	}
}

// TestMCPAuthorizationDeferredResultAmbiguousPostWriteFailureStaysFoldable
// covers the reviewer-requested POST-write half: postWriteFailCalls durably
// records the deferred result before reporting failure (the ambiguous, possibly
// -already-committed case), which must be treated identically to a genuine
// non-commit — appendAuthorizationResolution cannot tell the two apart, so it
// must conservatively skip EvAuthorizationResolved either way.
func TestMCPAuthorizationDeferredResultAmbiguousPostWriteFailureStaysFoldable(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
	log := &countingEventLog{}
	seed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &f.pending.Call},
		{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
		{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
			AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
			ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
		}},
	}
	for _, ev := range seed {
		if err := log.Append(t.Context(), "authorization-session", ev); err != nil {
			t.Fatal(err)
		}
	}
	log.postWriteFailCalls = map[int]bool{len(seed) + 2: true}
	diag := &lifecycleDiagnostics{}
	f.svc.cfg.EventLog = log
	f.svc.cfg.Diagnostics = diag
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	loaded.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvironmentKind("lost-remote"), ID: "runtime", Revision: "gone"}
	if err := f.store.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
	if !errors.Is(err, ErrInternal) || result != (MCPAuthorizationResult{}) {
		t.Fatalf("fallback append failure = %+v, %v; want ErrInternal and no success", result, err)
	}
	// postWriteFailCalls still durably records before reporting its error, so
	// both results land in recorded despite the ambiguous failure — only the
	// (never-attempted) resolved event is missing.
	wantAttempts := len(seed) + 2
	if got := len(log.attempts); got != wantAttempts {
		t.Fatalf("append attempts = %d, want %d (no resolved attempt once the deferred result's append errored)", got, wantAttempts)
	}
	if got := len(log.recorded); got != wantAttempts {
		t.Fatalf("durably written events = %d, want %d (primary and deferred results recorded; resolved never attempted)", got, wantAttempts)
	}
	for _, ev := range log.recorded {
		if ev.Type == session.EvAuthorizationResolved {
			t.Fatal("EvAuthorizationResolved was recorded despite the deferred result's ambiguous append failure")
		}
	}
	persisted, loadErr := f.store.Load(t.Context(), loaded.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.State != session.StateIdle {
		t.Fatalf("settled snapshot state = %q, want idle", persisted.State)
	}
	_, foldErr := eventsource.Fold(eventsource.SessionMeta{
		ID: persisted.ID, Mode: persisted.Mode, Limits: persisted.Limits, EnvironmentRef: persisted.EnvironmentRef, CreatedAt: persisted.CreatedAt,
	}, log.Read(t.Context(), persisted.ID))
	if !errors.Is(foldErr, eventsource.ErrPrivateStateRequired) {
		t.Fatalf("fold error = %v, want ErrPrivateStateRequired", foldErr)
	}
}

func TestMCPAuthorizationCloseSessionSettlesParkedCall(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	f.svc.CloseSession("authorization-session")
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	// Idle, not the stranded StateRunning of before: settlement starts no run
	// to own it, so Abandon settles it — see ensureAuthorizationRequiredLogged's
	// sibling fix in settleAuthorizationLocked.
	if loaded.State != session.StateIdle {
		t.Fatalf("state = %q", loaded.State)
	}
	if err := session.ValidateToolPairing(loaded.Conversation.Messages); err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if f.attach.tool.calls.Load() != 0 {
		t.Fatal("protected mutation executed on close")
	}
}

var _ port.SessionStore = (*memstore.Store)(nil)

type manualAuthorizationTimer struct {
	fn      func()
	stopped atomic.Bool
}

func (t *manualAuthorizationTimer) Stop() bool { return !t.stopped.Swap(true) }
func (t *manualAuthorizationTimer) Fire() {
	if !t.stopped.Load() {
		t.fn()
	}
}

func TestMCPAuthorizationExpiryAndCancelRaceHasOneResolution(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	created := make(chan *manualAuthorizationTimer, 1)
	factory := func(_ time.Duration, fn func()) AuthorizationTimer {
		timer := &manualAuthorizationTimer{fn: fn}
		created <- timer
		return timer
	}
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, clock, factory)
	f.svc.scheduleAuthorizationExpiry("authorization-session", f.pending, true)
	timer := <-created
	clockMu.Lock()
	now = f.pending.Authorization.ExpiresAt
	clockMu.Unlock()
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	cancelled := make(chan MCPAuthorizationResult, 1)
	cancelErr := make(chan error, 1)
	start := make(chan struct{})
	go func() { <-start; timer.Fire() }()
	go func() {
		<-start
		result, err := f.svc.CancelMCPAuthorization(context.Background(), control.SessionID, control)
		cancelled <- result
		cancelErr <- err
	}()
	close(start)
	result, err := <-cancelled, <-cancelErr
	if err == nil && result.Run != nil {
		drainLifecycleRun(t, f.svc, result)
	}
	deadline := time.Now().Add(time.Second)
	for {
		loaded, loadErr := f.store.Load(t.Context(), control.SessionID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if loaded.State.IsTerminal() {
			if pairErr := session.ValidateToolPairing(loaded.Conversation.Messages); pairErr != nil {
				t.Fatalf("pairing: %v", pairErr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authorization race did not settle")
		}
		time.Sleep(time.Millisecond)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
}

func TestMCPAuthorizationExpiryRetriesTransientFailure(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	created := make(chan *manualAuthorizationTimer, maxAuthorizationExpiryRetries+1)
	factory := func(_ time.Duration, fn func()) AuthorizationTimer {
		timer := &manualAuthorizationTimer{fn: fn}
		created <- timer
		return timer
	}
	transient := errors.New("broker retry")
	f := newLifecycleFixture(t, session.AuthorizationPending, transient, clock, factory)
	f.svc.scheduleAuthorizationExpiry("authorization-session", f.pending, true)
	first := <-created
	clockMu.Lock()
	now = f.pending.Authorization.ExpiresAt
	clockMu.Unlock()

	first.Fire()
	second := <-created
	f.broker.attachErr = nil
	second.Fire()

	deadline := time.Now().Add(time.Second)
	for {
		loaded, err := f.store.Load(t.Context(), "authorization-session")
		if err != nil {
			t.Fatal(err)
		}
		if loaded.State.IsTerminal() {
			if err := session.ValidateToolPairing(loaded.Conversation.Messages); err != nil {
				t.Fatalf("pairing: %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expiry retry did not settle; state=%q", loaded.State)
		}
		time.Sleep(time.Millisecond)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
}

// alwaysFailStore fails every Load, proving scheduleAuthorizationExpiry never
// consults the store at all when the caller already has the pending
// authorization in memory (P1-6).
type alwaysFailStore struct{ port.SessionStore }

func (alwaysFailStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, errors.New("store unavailable")
}

func TestScheduleAuthorizationExpiryUsesInMemoryPendingDespiteFailingStore(t *testing.T) {
	created := make(chan *manualAuthorizationTimer, 1)
	factory := func(_ time.Duration, fn func()) AuthorizationTimer {
		timer := &manualAuthorizationTimer{fn: fn}
		created <- timer
		return timer
	}
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, factory)
	f.svc.cfg.Store = alwaysFailStore{f.store}

	f.svc.scheduleAuthorizationExpiry("authorization-session", f.pending, true)

	select {
	case <-created:
	case <-time.After(time.Second):
		t.Fatal("no expiry timer was armed despite a valid in-memory pending authorization")
	}
}

func TestScheduleAuthorizationExpiryWarnsWhenNoInMemoryPending(t *testing.T) {
	diag := &lifecycleDiagnostics{}
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	f.svc.cfg.Diagnostics = diag

	f.svc.scheduleAuthorizationExpiry("authorization-session", session.PendingAuthorization{}, false)

	if !diag.contains("authorization expiry scheduling skipped: no pending authorization in the finished run's session") {
		t.Fatalf("missing skipped-scheduling diagnostic: %v", diag.messages)
	}
	f.svc.mu.Lock()
	_, armed := f.svc.authorizationExpiry["authorization-session"]
	f.svc.mu.Unlock()
	if armed {
		t.Fatal("an expiry entry was armed despite no in-memory pending authorization")
	}
}

func TestMCPAuthorizationPreparedRegistrationCancellationRestoresClaim(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	loaded, err := f.store.Load(ctx, "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	engine, env, err := f.svc.engineAndEnvironmentFor(ctx, loaded)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := loaded.ClaimAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Save(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	resolution, err := session.NewAuthorizationResolution(session.AuthorizationGranted)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := engine.PrepareAuthorizationContinuation(memory.WithWorkspace(ctx, env.Workspace().Root()), loaded, env, claimed, resolution)
	if err != nil {
		t.Fatal(err)
	}

	// Cancellation is triggered after registration, while the handoff lock is
	// held and immediately before Start can disarm it. The callback must win,
	// abort the inert continuation, and restore the claim.
	f.svc.beforeAuthorizationContinuationStart = cancel
	if err := f.svc.registerAndStartGrantedAuthorization(ctx, loaded, claimed, prepared); !errors.Is(err, context.Canceled) {
		t.Fatalf("register/start error = %v, want context cancellation", err)
	}
	if _, live := f.svc.LookupRun(loaded.ID); live {
		t.Fatal("cancelled prepared continuation registered a run")
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d, want 0", got)
	}
	restored, err := f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := restored.PendingAuthorization()
	if restored.State != session.StateAuthorizing || !ok || pending.Authorization.ID != f.pending.Authorization.ID {
		t.Fatalf("restored authorization = state %q, pending %+v, ok %t", restored.State, pending, ok)
	}
	f.attach.mu.Lock()
	f.attach.status = session.AuthorizationPending
	f.attach.mu.Unlock()
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), restored.ID, MCPAuthorizationControl{SessionID: restored.ID, AuthorizationID: pending.Authorization.ID})
	if err != nil || result.Status != session.AuthorizationPending || result.Run != nil {
		t.Fatalf("restored authorization recheck = %+v, %v", result, err)
	}
}

func TestMCPAuthorizationTerminalResolutionCancellationAtHandoffDoesNotLeaveRun(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	log := memstore.NewEventLog()
	f.svc.cfg.EventLog = log
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	loaded, err := f.store.Load(ctx, "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	seed := []session.Event{
		{Type: session.EvTurnStart},
		{Type: session.EvToolCall, ToolCall: &f.pending.Call},
		{Type: session.EvToolCall, ToolCall: &f.pending.Deferred[0]},
		{Type: session.EvAuthorizationRequired, Authorization: &session.AuthorizationPayload{
			AuthorizationID: f.pending.Authorization.ID, DisplayName: f.pending.Authorization.DisplayName, Call: f.pending.Call.ID,
			ExpiresAt: f.pending.Authorization.ExpiresAt, Status: session.AuthorizationPending,
		}},
	}
	for _, ev := range seed {
		if err := log.Append(t.Context(), loaded.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	f.svc.beforeAuthorizationContinuationStart = cancel
	result, err := f.svc.resolveAuthorizationLocked(ctx, loaded, f.pending, session.AuthorizationCancelled)
	if !errors.Is(err, context.Canceled) || result.Run != nil {
		t.Fatalf("terminal resolution result = %+v, err = %v", result, err)
	}
	if _, live := f.svc.LookupRun(loaded.ID); live {
		t.Fatal("cancelled terminal continuation registered a run")
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d, want 0", got)
	}
	settled, err := f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != session.StateIdle {
		t.Fatalf("settled state = %q, want %q", settled.State, session.StateIdle)
	}
	if err := session.ValidateToolPairing(settled.Conversation.Messages); err != nil {
		t.Fatalf("settled pairing: %v", err)
	}

	// P1-5: the settled snapshot must not be the only place this resolution is
	// recorded — a pre-start cancellation at the registration handoff must
	// still append the terminal EvToolResult/EvAuthorizationResolved events, or
	// a later event-sourced fold sees the authorization as still pending.
	if _, err := eventsource.Fold(eventsource.SessionMeta{
		ID: settled.ID, Mode: settled.Mode, Limits: settled.Limits, EnvironmentRef: settled.EnvironmentRef, CreatedAt: settled.CreatedAt,
	}, log.Read(t.Context(), settled.ID)); err != nil {
		t.Fatalf("eventsource remains private-state-required after cancelled handoff: %v", err)
	}
	var events []session.Event
	for ev, readErr := range log.Read(t.Context(), settled.ID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		events = append(events, ev)
	}
	if got, want := len(events), len(seed)+3; got != want {
		t.Fatalf("event count = %d, want %d: %+v", got, want, events)
	}
	trailing := events[len(seed):]
	if trailing[0].Type != session.EvToolResult || trailing[0].ToolResult == nil || trailing[0].ToolResult.CallID != f.pending.Call.ID ||
		trailing[1].Type != session.EvToolResult || trailing[1].ToolResult == nil || trailing[1].ToolResult.CallID != f.pending.Deferred[0].ID ||
		trailing[2].Type != session.EvAuthorizationResolved || trailing[2].Authorization == nil || trailing[2].Authorization.Status != session.AuthorizationCancelled {
		t.Fatalf("ordered terminal events = %+v", trailing)
	}
}

func TestMCPAuthorizationPreparedRegistrationFailureNeverExecutes(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.svc.engineAndEnvironmentFor(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	blockerSession := session.New("blocker", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	blockerEnv, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "in-tree-v1"}, memfs.NewWorkspace("/repo"), memledger.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	blocker := f.svc.cfg.Engine.Run(t.Context(), blockerSession, blockerEnv, agent.RunRequest{Text: "block"})
	f.svc.mu.Lock()
	f.svc.runs[loaded.ID] = &runState{run: blocker, sess: blockerSession, settled: make(chan struct{})}
	f.svc.mu.Unlock()
	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
	if !errors.Is(err, ErrNoActiveRun) || result.Run != nil {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	for range blocker.Events() {
	}
	f.svc.deregister(loaded.ID, blocker)
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
	repaired, err := f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.State != session.StateAuthorizing {
		t.Fatalf("repaired state = %q, want %q", repaired.State, session.StateAuthorizing)
	}
	restored, ok := repaired.PendingAuthorization()
	if !ok || restored.Authorization.ID != f.pending.Authorization.ID || restored.Call.ID != f.pending.Call.ID {
		t.Fatalf("repaired authorization = %+v, %v", restored, ok)
	}
}

// TestMCPAuthorizationGrantedCompensationSaveFailureSettlesInsteadOfStranding
// proves restoreAuthorizationClaimOrSettle's fallback: registerPrepared fails
// (same blocker-run technique as the sibling test above), which triggers
// restoreAuthorizationClaim to compensate — but this time the COMPENSATING
// save itself also fails (the claim save that already landed StateRunning
// durably succeeded; only the second, restoring save fails). Before this fix
// the durable snapshot would be left stranded StateRunning with neither a
// runtime owner nor a pending authorization to retry; the fallback reloads it
// fresh and settles it to idle via the same repairRunningSession the
// crash-orphan run-entry path uses.
func TestMCPAuthorizationGrantedCompensationSaveFailureSettlesInsteadOfStranding(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.svc.engineAndEnvironmentFor(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	blockerSession := session.New("blocker", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	blockerEnv, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/repo", Revision: "in-tree-v1"}, memfs.NewWorkspace("/repo"), memledger.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	blocker := f.svc.cfg.Engine.Run(t.Context(), blockerSession, blockerEnv, agent.RunRequest{Text: "block"})
	f.svc.mu.Lock()
	f.svc.runs[loaded.ID] = &runState{run: blocker, sess: blockerSession, settled: make(chan struct{})}
	f.svc.mu.Unlock()
	// 1st Save: ClaimAuthorization's own save (must succeed, landing
	// StateRunning durably). 2nd Save: the compensating restoreAuthorizationClaim
	// triggered by registerPrepared's failure below (must fail).
	f.svc.cfg.Store = &failNthAuthorizationSaveStore{SessionStore: f.store, failAt: 2}
	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, control)
	// The compensating restore itself failed this time (unlike the sibling
	// test), so the caller reports that failure directly rather than the
	// ordinary ErrNoActiveRun — restoring did not actually happen.
	if !errors.Is(err, ErrInternal) || result.Run != nil {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	for range blocker.Events() {
	}
	f.svc.deregister(loaded.ID, blocker)
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
	settled, err := f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != session.StateIdle {
		t.Fatalf("settled state = %q, want idle (not stranded running)", settled.State)
	}
}

func TestMCPAuthorizationForeignOwnerDoesNotWaitForControlLock(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	owner := &session.Principal{Issuer: "https://issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	loaded.Owner = owner
	if err := f.store.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	f.svc.cfg.OwnershipEnforced = true
	unlock := f.svc.runEntryMu.lock(loaded.ID)
	defer unlock()
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: owner.Issuer, Subject: "bob", GrantType: session.GrantTypeUser})
	control := MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "presentation", call: func() error { _, err := f.svc.MCPAuthorizationPresentation(foreign, loaded.ID, control); return err }},
		{name: "recheck", call: func() error { _, err := f.svc.RecheckMCPAuthorization(foreign, loaded.ID, control); return err }},
		{name: "cancel", call: func() error { _, err := f.svc.CancelMCPAuthorization(foreign, loaded.ID, control); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- tc.call() }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("foreign error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("foreign owner contended on caller-selected control lock")
			}
		})
	}
}

func TestMCPAuthorizationServiceCloseSettlesParkedCall(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	// A committed attachment is part of the process-owned settlement inventory.
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	attachment, release, err := f.svc.authorizationAttachment(t.Context(), loaded)
	if err != nil || attachment == nil {
		t.Fatalf("attach = %v, %v", attachment, err)
	}
	release()
	f.svc.Close()
	settled, err := f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State != session.StateIdle {
		t.Fatalf("state = %q", settled.State)
	}
	if pairErr := session.ValidateToolPairing(settled.Conversation.Messages); pairErr != nil {
		t.Fatalf("pairing: %v", pairErr)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
}

type authorizationOrderingLease struct{ acquired atomic.Bool }

func (l *authorizationOrderingLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	l.acquired.Store(true)
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: time.Now().Add(time.Hour)}, nil
}
func (*authorizationOrderingLease) Renew(_ context.Context, lease port.Lease) (port.Lease, error) {
	return lease, nil
}
func (*authorizationOrderingLease) Release(context.Context, port.Lease) error { return nil }

func TestMCPAuthorizationAcquiresLeaseBeforeBrokerObservation(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	lease := &authorizationOrderingLease{}
	f.svc.cfg.SessionLease = lease
	f.svc.cfg.LeaseOwner = "test-owner"
	f.svc.cfg.LeaseTTL = time.Hour
	f.svc.cfg.LeaseRenewInterval = time.Hour
	var observedBeforeLease atomic.Bool
	f.attach.statusHook = func() {
		if !lease.acquired.Load() {
			observedBeforeLease.Store(true)
		}
	}
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Status != session.AuthorizationPending {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if !lease.acquired.Load() || observedBeforeLease.Load() {
		t.Fatalf("lease ordering: acquired=%t broker-before=%t", lease.acquired.Load(), observedBeforeLease.Load())
	}
	f.svc.CloseSession(control.SessionID)
}

func TestReconcileAuthorizationCancellation(t *testing.T) {
	statusReadErr := errors.New("authoritative status read failed")
	for _, tc := range []struct {
		name        string
		outcome     brokercontract.CancelOutcome
		freshStatus session.AuthorizationStatus
		status      session.AuthorizationStatus
		statusErr   error
		want        session.AuthorizationStatus
		wantErr     error
		exactErr    bool
	}{
		{name: "fresh expiry", outcome: brokercontract.CancelCancelled, freshStatus: session.AuthorizationExpired, want: session.AuthorizationExpired},
		{name: "fresh explicit cancellation", outcome: brokercontract.CancelCancelled, freshStatus: session.AuthorizationCancelled, want: session.AuthorizationCancelled},
		{name: "already cancelled", outcome: brokercontract.CancelAlreadyCancelled, freshStatus: session.AuthorizationExpired, want: session.AuthorizationCancelled},
		{name: "already resolved", outcome: brokercontract.CancelAlreadyResolved, status: session.AuthorizationGranted, want: session.AuthorizationGranted},
		{name: "already resolved remains pending", outcome: brokercontract.CancelAlreadyResolved, status: session.AuthorizationPending, wantErr: ErrFailedPrecondition},
		{name: "status read error", outcome: brokercontract.CancelAlreadyResolved, statusErr: statusReadErr, wantErr: statusReadErr, exactErr: true},
		{name: "unknown outcome", outcome: "unknown", wantErr: ErrFailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleFixture(t, tc.status, nil, time.Now, nil)
			f.attach.statusErr = tc.statusErr
			got, err := reconcileAuthorizationCancellation(t.Context(), f.attach, f.pending.Authorization, tc.outcome, tc.freshStatus)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.exactErr && err != tc.wantErr {
				t.Fatalf("error = %v, want unchanged %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCancellationCallersSupplyTheirTerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*lifecycleFixture, MCPAuthorizationControl) (MCPAuthorizationResult, error)
		want session.AuthorizationStatus
	}{
		{name: "recheck expiry", call: func(f *lifecycleFixture, control MCPAuthorizationControl) (MCPAuthorizationResult, error) {
			return f.svc.RecheckMCPAuthorization(t.Context(), control.SessionID, control)
		}, want: session.AuthorizationExpired},
		{name: "explicit cancellation", call: func(f *lifecycleFixture, control MCPAuthorizationControl) (MCPAuthorizationResult, error) {
			return f.svc.CancelMCPAuthorization(t.Context(), control.SessionID, control)
		}, want: session.AuthorizationCancelled},
		{name: "timer expiry", call: func(f *lifecycleFixture, _ MCPAuthorizationControl) (MCPAuthorizationResult, error) {
			loaded, err := f.store.Load(t.Context(), "authorization-session")
			if err != nil {
				return MCPAuthorizationResult{}, err
			}
			return f.svc.recheckExpiredAuthorizationLocked(t.Context(), loaded, f.pending)
		}, want: session.AuthorizationExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			f := newLifecycleFixture(t, session.AuthorizationPending, nil, func() time.Time { return now }, nil)
			now = f.pending.Authorization.ExpiresAt
			f.attach.cancelOutcome = brokercontract.CancelCancelled
			control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
			result, err := tc.call(&f, control)
			if err != nil || result.Status != tc.want {
				t.Fatalf("result = %+v, err = %v; want %q", result, err, tc.want)
			}
			drainLifecycleRun(t, f.svc, result)
		})
	}
}

func TestCancelMCPAuthorizationReturnsCancelledContinuation(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.CancelMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Status != session.AuthorizationCancelled || result.Run == nil {
		t.Fatalf("result = %+v, %v", result, err)
	}
	drainLifecycleRun(t, f.svc, result)
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
}

func TestCancelMCPAuthorizationDoesNotOverrideGrantedWinner(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationGranted, nil, time.Now, nil)
	control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
	result, err := f.svc.CancelMCPAuthorization(t.Context(), control.SessionID, control)
	if err != nil || result.Status != session.AuthorizationGranted || result.Run == nil {
		t.Fatalf("result = %+v, %v", result, err)
	}
	drainLifecycleRun(t, f.svc, result)
	if got := f.attach.tool.calls.Load(); got != 1 {
		t.Fatalf("protected executions = %d", got)
	}
}

func TestCancelMCPAuthorizationFailsClosedOnAmbiguousClaim(t *testing.T) {
	for _, outcome := range []brokercontract.CancelOutcome{"unknown", brokercontract.CancelAlreadyResolved} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
			f.attach.cancelOutcome = outcome
			control := MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID}
			result, err := f.svc.CancelMCPAuthorization(t.Context(), control.SessionID, control)
			if err == nil || result.Run != nil {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			loaded, loadErr := f.store.Load(t.Context(), control.SessionID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if loaded.State != session.StateAuthorizing || f.attach.tool.calls.Load() != 0 {
				t.Fatalf("state = %q, protected executions = %d", loaded.State, f.attach.tool.calls.Load())
			}
		})
	}
}

func TestMCPAuthorizationTransientBrokerFailuresDoNotInterrupt(t *testing.T) {
	transient := errors.New("broker temporarily unavailable")
	for _, test := range []struct {
		name string
		call func(*lifecycleFixture) error
	}{
		{name: "recheck", call: func(f *lifecycleFixture) error {
			_, err := f.svc.RecheckMCPAuthorization(context.Background(), "authorization-session", MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID})
			return err
		}},
		{name: "cancel", call: func(f *lifecycleFixture) error {
			_, err := f.svc.CancelMCPAuthorization(context.Background(), "authorization-session", MCPAuthorizationControl{SessionID: "authorization-session", AuthorizationID: f.pending.Authorization.ID})
			return err
		}},
		{name: "prompt re-entry", call: func(f *lifecycleFixture) error {
			_, err := f.svc.StartRunContent(context.Background(), "authorization-session", "continue", nil)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newLifecycleFixture(t, session.AuthorizationPending, transient, time.Now, nil)
			err := test.call(&f)
			if !errors.Is(err, transient) {
				t.Fatalf("error = %v, want transient identity", err)
			}
			loaded, loadErr := f.store.Load(t.Context(), "authorization-session")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if loaded.State != session.StateAuthorizing || f.attach.tool.calls.Load() != 0 {
				t.Fatalf("state = %q, protected executions = %d", loaded.State, f.attach.tool.calls.Load())
			}
		})
	}
}

func TestMCPAuthorizationBindingMismatchDoesNotInterrupt(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	loaded.ExternalBinding = "different-binding"
	if err := f.store.Save(t.Context(), loaded); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.RecheckMCPAuthorization(t.Context(), loaded.ID, MCPAuthorizationControl{SessionID: loaded.ID, AuthorizationID: f.pending.Authorization.ID})
	if !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("error = %v", err)
	}
	loaded, err = f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != session.StateAuthorizing {
		t.Fatalf("state = %q", loaded.State)
	}
}

func TestMCPAuthorizationCloseSessionSaveFailureRetainsAuthorityForRetry(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, release, err := f.svc.authorizationAttachment(t.Context(), loaded); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	store := &failNextAuthorizationSaveStore{SessionStore: f.store}
	store.failures.Store(1)
	diagnostics := &lifecycleDiagnostics{}
	f.svc.cfg.Store = store
	f.svc.cfg.Diagnostics = diagnostics

	f.svc.CloseSession(loaded.ID)
	persisted, err := f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != session.StateAuthorizing {
		t.Fatalf("state after failed close = %q", persisted.State)
	}
	f.svc.mu.Lock()
	retained := f.svc.brokerAttachments[loaded.ID] != nil
	f.svc.mu.Unlock()
	if !retained || !diagnostics.contains("persist external authorization settlement failed") {
		t.Fatalf("retained=%t diagnostics=%v", retained, diagnostics.messages)
	}

	f.svc.CloseSession(loaded.ID)
	persisted, err = f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != session.StateIdle {
		t.Fatalf("state after retry = %q", persisted.State)
	}
	f.svc.mu.Lock()
	retained = f.svc.brokerAttachments[loaded.ID] != nil
	f.svc.mu.Unlock()
	if retained {
		t.Fatal("attachment retained after successful retry")
	}
}

// TestMCPAuthorizationServiceCloseCompletesDespiteSaveFailure pins the P1-7
// fix: a session whose settlement save fails is reported and left untouched,
// but Close() still runs to completion (shutdownComplete set) in that SAME
// call — unlike the old "retryable" contract, a second Close() is a no-op and
// does NOT get a second chance to settle the session (shutdown is a one-shot,
// once-only sequence; a session stuck in StateAuthorizing after shutdown must
// be repaired through the ordinary run-entry recovery paths, not by calling
// Close again).
func TestMCPAuthorizationServiceCloseCompletesDespiteSaveFailure(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, release, err := f.svc.authorizationAttachment(t.Context(), loaded); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	store := &failNextAuthorizationSaveStore{SessionStore: f.store}
	store.failures.Store(1)
	diagnostics := &lifecycleDiagnostics{}
	f.svc.cfg.Store = store
	f.svc.cfg.Diagnostics = diagnostics

	f.svc.Close()
	persisted, err := f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.mu.Lock()
	complete := f.svc.shutdownComplete
	f.svc.mu.Unlock()
	if persisted.State != session.StateAuthorizing {
		t.Fatalf("state after failed settlement = %q, want unchanged StateAuthorizing", persisted.State)
	}
	if !complete {
		t.Fatal("one session's settlement failure aborted Close instead of completing it")
	}
	if !diagnostics.contains("persist external authorization settlement failed") {
		t.Fatalf("missing settlement-failure diagnostic: %v", diagnostics.messages)
	}

	// A second Close() is a no-op (shutdownComplete already true) — it must
	// NOT get a second chance to settle the session.
	f.svc.Close()
	persisted, err = f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != session.StateAuthorizing {
		t.Fatalf("state after second Close = %q, want unchanged StateAuthorizing (Close is not retryable)", persisted.State)
	}
}

func TestStartRunRejectsLiveRestoredAuthorizationAsPending(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)

	_, err := f.svc.StartRunContent(t.Context(), "authorization-session", "another message", nil)
	if !errors.Is(err, errMCPAuthorizationPending) {
		t.Fatalf("StartRunContent error = %v, want MCP authorization pending", err)
	}
	if !strings.Contains(err.Error(), "complete the browser authorization or cancel it before sending another message") {
		t.Fatalf("pending error detail = %q", err)
	}
	entry := classifyError(err)
	if entry.Code != "mcp_authorization_pending" || entry.GRPC != codes.FailedPrecondition || entry.HTTPStatus != http.StatusConflict {
		t.Fatalf("pending authorization classification = %+v", entry)
	}
	loaded, loadErr := f.store.Load(t.Context(), "authorization-session")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.State != session.StateAuthorizing {
		t.Fatalf("state = %q, want authorizing", loaded.State)
	}
	pending, ok := loaded.PendingAuthorization()
	if !ok || pending.Authorization.ID != f.pending.Authorization.ID {
		t.Fatalf("pending authorization = %+v, want %q", pending, f.pending.Authorization.ID)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected tool executions = %d, want 0", got)
	}
	if _, active := f.svc.LookupRun(loaded.ID); active {
		t.Fatal("rejected prompt registered a replacement run")
	}
}

// A parked authorization has no live agent run to drain. An unqualified steer
// therefore cannot promote a new run and must surface the same actionable
// pending-authorization condition without disturbing the parked request.
func TestSteerRejectsLiveAuthorizationAsPending(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)

	outcome, promoted, run, err := f.svc.Steer(t.Context(), "authorization-session", "another message", nil, "", "")
	if !errors.Is(err, errMCPAuthorizationPending) {
		t.Fatalf("Steer error = %v, want MCP authorization pending", err)
	}
	// This is not a liveness conflict: a parked authorization owns no Run for
	// promotedSteerRun to await. Keep it outside the generic precondition family
	// so Steer returns it directly instead of retrying the run-entry funnel.
	if errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("Steer error = %v, unexpectedly classified as a liveness conflict", err)
	}
	if outcome != agent.SteerTooLate || promoted || run != nil {
		t.Fatalf("Steer outcome = (%q, promoted=%t, run=%v), want too_late without promotion", outcome, promoted, run)
	}
	loaded, loadErr := f.store.Load(t.Context(), "authorization-session")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.State != session.StateAuthorizing {
		t.Fatalf("state = %q, want authorizing", loaded.State)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected tool executions = %d, want 0", got)
	}
	if _, active := f.svc.LookupRun(loaded.ID); active {
		t.Fatal("rejected steer registered a replacement run")
	}
}

func TestStartRunRepairsUnavailableRestoredAuthorizationBeforeFailingReattach(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, brokercontract.ErrStateUnavailable, time.Now, nil)
	_, err := f.svc.StartRunContent(t.Context(), "authorization-session", "continue", nil)
	if err == nil {
		t.Fatal("StartRunContent unexpectedly succeeded without broker incarnation")
	}
	loaded, loadErr := f.store.Load(t.Context(), "authorization-session")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	// Idle, not a stranded StateRunning: the interrupt repair succeeded and
	// saved StateRunning on the promise that a real Engine.Run would take
	// ownership next, but the reattach that was supposed to drive it failed
	// first — the deferred repair in StartRunContent settles it back down
	// rather than leaving it misclassifiable as a crash orphan.
	if loaded.State != session.StateIdle {
		t.Fatalf("state = %q, want idle after the interrupted repair's continuation failed to start", loaded.State)
	}
	if pairErr := session.ValidateToolPairing(loaded.Conversation.Messages); pairErr != nil {
		t.Fatalf("pairing: %v", pairErr)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
}
