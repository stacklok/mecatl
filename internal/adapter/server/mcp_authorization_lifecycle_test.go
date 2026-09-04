package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

type lifecycleTool struct{ calls atomic.Int32 }

func (*lifecycleTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "protected", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*lifecycleTool) ReadOnly() bool { return false }
func (t *lifecycleTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.calls.Add(1)
	return session.NewToolResult(call.ID, "protected mutation complete"), nil
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
	binding       string
	tool          *lifecycleTool
	mu            sync.Mutex
	status        session.AuthorizationStatus
	statusErr     error
	url           string
	statusHook    func()
	cancelOutcome brokercontract.CancelOutcome
}

func (*lifecycleAttachment) Commit(context.Context) error { return nil }
func (*lifecycleAttachment) Abort(context.Context) error  { return nil }
func (a *lifecycleAttachment) Binding() session.ExternalBinding {
	return session.ExternalBinding(a.binding)
}
func (a *lifecycleAttachment) Tools() []tool.Tool { return []tool.Tool{a.tool} }
func (a *lifecycleAttachment) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.status != session.AuthorizationPending {
		return "", brokercontract.ErrAuthorizationNotFound
	}
	return a.url, nil
}
func (a *lifecycleAttachment) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
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
	svc     *Service
	store   *memstore.Store
	broker  *lifecycleBroker
	attach  *lifecycleAttachment
	pending session.PendingAuthorization
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
	store := memstore.New()
	mutation := &lifecycleTool{}
	attachment := &lifecycleAttachment{binding: "broker-binding", tool: mutation, status: status, url: "https://auth.example/authorize?state=live"}
	broker := &lifecycleBroker{attachment: attachment, attachErr: attachErr}
	buildEngine := func(tools []tool.Tool) *agent.Engine {
		catalog := tool.NewCatalog()
		for _, one := range tools {
			catalog.MustRegister(one)
		}
		return agent.NewEngine(agent.Deps{LLM: mockllm.New(turns...), Catalog: catalog, Policy: permpolicy.NewPolicy(nil, nil), Store: store, Model: "mock"})
	}
	shared := buildEngine(nil)
	cfg := Config{
		Engine: shared, Store: store, PlacementProvider: lifecyclePlacementProvider{}, PlacementScope: "test",
		MCPBroker: broker, Now: now, AuthorizationTimer: timer,
	}
	// Avoid spelling the MCP config type in the fixture closure by assigning the
	// correctly typed factory separately.
	cfg.SessionEngine = func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, mode session.PermissionMode) (SessionEngineResult, error) {
		return SessionEngineResult{Engine: buildEngine(nil), BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	cfg.SessionEngineWithTools = func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, mode session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
		return SessionEngineResult{Engine: buildEngine(tools), BuiltForMode: mode, Close: func() error { return nil }}, nil
	}
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("protected-call", "protected", json.RawMessage(`{"value":1}`))
	deferred := session.NewToolCall("deferred-call", "later", json.RawMessage(`{}`))
	pending := session.PendingAuthorization{Authorization: session.ExternalAuthorization{ID: "authorization:1", DisplayName: "Calendar", Binding: "opaque-binding", ExpiresAt: now().Add(time.Hour)}, Call: call, Deferred: []session.ToolCall{deferred}}
	sess := session.New("authorization-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo", Revision: "in-tree-v1"}, session.Limits{}, now())
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
	return lifecycleFixture{svc: svc, store: store, broker: broker, attach: attachment, pending: pending}
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

func TestMCPAuthorizationContinuationReplacesJustParkedRun(t *testing.T) {
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

func TestMCPAuthorizationTerminalFallbackAppendFailureIsExplicit(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationDenied, nil, time.Now, nil)
	log := &countingEventLog{postWriteFailCalls: map[int]bool{1: true}}
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
	if got := len(log.attempts); got != 1 {
		t.Fatalf("append attempts = %d, want one at-most-once attempt", got)
	}
	if got := len(log.recorded); got != 1 {
		t.Fatalf("durably written events = %d, want the ambiguous first append preserved without retry", got)
	}
	persisted, loadErr := f.store.Load(t.Context(), loaded.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.State != session.StateRunning {
		t.Fatalf("settled snapshot state = %q, want running", persisted.State)
	}
	if err := session.ValidateToolPairing(persisted.Conversation.Messages); err != nil {
		t.Fatalf("settled snapshot pairing: %v", err)
	}
	if f.attach.tool.calls.Load() != 0 {
		t.Fatal("protected mutation executed after ambiguous append failure")
	}
}

func TestMCPAuthorizationCloseSessionSettlesParkedCall(t *testing.T) {
	f := newLifecycleFixture(t, session.AuthorizationPending, nil, time.Now, nil)
	f.svc.CloseSession("authorization-session")
	loaded, err := f.store.Load(t.Context(), "authorization-session")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != session.StateRunning {
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
	f.svc.scheduleAuthorizationExpiry("authorization-session")
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
	f.svc.scheduleAuthorizationExpiry("authorization-session")
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
	if pairErr := session.ValidateToolPairing(repaired.Conversation.Messages); pairErr != nil {
		t.Fatalf("repair pairing: %v", pairErr)
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
	if settled.State != session.StateRunning {
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
	if persisted.State != session.StateRunning {
		t.Fatalf("state after retry = %q", persisted.State)
	}
	f.svc.mu.Lock()
	retained = f.svc.brokerAttachments[loaded.ID] != nil
	f.svc.mu.Unlock()
	if retained {
		t.Fatal("attachment retained after successful retry")
	}
}

func TestMCPAuthorizationServiceCloseSaveFailureIsRetryable(t *testing.T) {
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
	retained := f.svc.brokerAttachments[loaded.ID] != nil
	f.svc.mu.Unlock()
	if persisted.State != session.StateAuthorizing || complete || !retained {
		t.Fatalf("first close: state=%q complete=%t retained=%t", persisted.State, complete, retained)
	}

	f.svc.Close()
	persisted, err = f.store.Load(t.Context(), loaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.mu.Lock()
	complete = f.svc.shutdownComplete
	f.svc.mu.Unlock()
	if persisted.State != session.StateRunning || !complete {
		t.Fatalf("retry close: state=%q complete=%t", persisted.State, complete)
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
	if loaded.State != session.StateRunning {
		t.Fatalf("state = %q, want running after interrupted repair", loaded.State)
	}
	if pairErr := session.ValidateToolPairing(loaded.Conversation.Messages); pairErr != nil {
		t.Fatalf("pairing: %v", pairErr)
	}
	if got := f.attach.tool.calls.Load(); got != 0 {
		t.Fatalf("protected executions = %d", got)
	}
}
