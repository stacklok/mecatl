package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// driveCompletedTurn starts a run, drains it to a terminal result, persists the
// terminal state, and finishes the run — leaving the session durably COMPLETED in
// the store (so ForkSession's loadAndReopen recovers it to idle). Returns the
// terminal result text.
func driveCompletedTurn(t *testing.T, svc *server.Service, id session.SessionID, prompt string) string {
	t.Helper()
	run, err := svc.StartRun(context.Background(), id, prompt)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	text := drainServerRun(run)
	svc.Persist(context.Background(), id)
	svc.FinishRun(id, run)
	return text
}

// TestForkSessionInheritsHistoryAndLabels is the headline unit guard: a selector
// session driven to a completed turn, forked, yields a NEW peer session that
// inherits the source's history verbatim and its mode/workspace/limits/provider/
// model/profile/title labels, starts idle with zeroed Counters/Usage, and has a
// rehydrated per-session engine built from the SAME selector.
func TestForkSessionInheritsHistoryAndLabels(t *testing.T) {
	ctx := context.Background()

	wantSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet"}

	var (
		gotSel  atomic.Value
		calls   atomic.Int32
		srcText = "SOURCE-ASSISTANT-TEXT"
	)
	// Two turns: the source's and the fork's (each rehydrated engine is a fresh
	// mockllm, but give two so a re-drive is robust).
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		calls.Add(1)
		gotSel.Store(sel)
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn(srcText), mockllm.TextTurn(srcText)),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { return nil }}, nil
	}
	svc, store := newMCPServiceStore(t, srcText, factory)

	src, err := svc.CreateSessionWithProvider(ctx, session.ModeAccept, session.Limits{MaxTurns: 7}, wantSel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	if got := driveCompletedTurn(t, svc, src.ID, "what is the plan?"); got != srcText {
		t.Fatalf("source turn reply = %q, want %q", got, srcText)
	}
	// Stash the source's persisted snapshot for later label comparison (the live
	// aggregate is mutated by loadAndReopen on fork).
	srcSnap, err := store.Load(ctx, src.ID)
	if err != nil {
		t.Fatalf("Load src: %v", err)
	}

	// Reset the factory recorder so the NEXT call is unambiguously the fork's.
	calls.Store(0)
	gotSel.Store(server.ProviderSelector{})
	newID, err := forkSession(svc, ctx, src.ID, "", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if newID == "" || newID == src.ID {
		t.Fatalf("fork id = %q, want a new non-empty id distinct from source %q", newID, src.ID)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times for the fork, want exactly 1 (rehydration)", calls.Load())
	}
	if got := gotSel.Load(); got != wantSel {
		t.Fatalf("fork rehydration factory saw selector %v, want the source's %v", got, wantSel)
	}
	if !svc.HasSessionEngineForTest(newID) {
		t.Fatalf("fork has no per-session engine registered (a selector fork MUST rehydrate one)")
	}

	forked, err := store.Load(ctx, newID)
	if err != nil {
		t.Fatalf("Load forked: %v", err)
	}
	// History inherited verbatim, same length.
	if got, want := len(forked.Conversation.Messages), len(srcSnap.Conversation.Messages); got != want {
		t.Fatalf("forked history len = %d, want source's %d", got, want)
	}
	// The user prompt + assistant text survive verbatim.
	var sawUser, sawAssistant bool
	for _, m := range forked.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "what is the plan?") {
			sawUser = true
		}
		if m.Role == session.RoleAssistant && strings.Contains(m.Text, srcText) {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("forked history missing user/assistant text (user=%v assistant=%v)", sawUser, sawAssistant)
	}
	// Labels inherited.
	if forked.Mode != srcSnap.Mode || forked.EnvironmentRef.ID != srcSnap.EnvironmentRef.ID ||
		forked.ProviderID != srcSnap.ProviderID || forked.ModelID != srcSnap.ModelID ||
		forked.ReasoningEffort != srcSnap.ReasoningEffort || forked.Profile != srcSnap.Profile ||
		forked.Title != srcSnap.Title {
		t.Fatalf("forked labels differ from source:\nforked=%+v\nsource=%+v", forked, srcSnap)
	}
	if forked.Limits != srcSnap.Limits {
		t.Fatalf("forked limits = %+v, want source's %+v", forked.Limits, srcSnap.Limits)
	}
	// Fresh aggregate: idle + zeroed counters/usage.
	if forked.State != session.StateIdle {
		t.Fatalf("forked state = %q, want idle", forked.State)
	}
	if forked.Counters != (session.Counters{}) {
		t.Fatalf("forked counters = %+v, want zeroed (fresh budget)", forked.Counters)
	}
	if got := forked.UsageFor(session.UsageKindMain); got != (session.Usage{}) {
		t.Fatalf("forked usage = %+v, want zero (fresh budget)", got)
	}

	// The fork can itself run a Converse to completion (history replays, no
	// provider 400 from an orphaned tool result — the ForkSnapshot+SeedHistory
	// pairing guarantee).
	if got := driveCompletedTurn(t, svc, newID, "continue the plan"); got != srcText {
		t.Fatalf("fork turn reply = %q, want %q", got, srcText)
	}

	// The source is unaffected: still loadable, still runnable.
	if _, err := store.Load(ctx, src.ID); err != nil {
		t.Fatalf("source not loadable after fork: %v", err)
	}
}

// TestForkSessionEffortOverride verifies the ADR 0068 effort override: a source
// with provider+model+effort "low", forked with override "high", yields a peer
// whose ReasoningEffort label is "high" while ProviderID/ModelID inherit verbatim,
// with a per-session engine rehydrated on the override selector, and
// svc.ResolvedModel(forkID) echoing the new effort.
func TestForkSessionEffortOverride(t *testing.T) {
	ctx := context.Background()

	wantSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet", ReasoningEffort: "low"}

	var (
		gotSel atomic.Value
		calls  atomic.Int32
	)
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		calls.Add(1)
		gotSel.Store(sel)
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("r"), mockllm.TextTurn("r")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { return nil }, ReasoningEffort: sel.ReasoningEffort}, nil
	}
	svc, store := newMCPServiceStore(t, "shared", factory)

	src, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, wantSel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "hi")

	// Reset the factory recorder so the NEXT call is unambiguously the fork's.
	calls.Store(0)
	gotSel.Store(server.ProviderSelector{})
	newID, err := forkSession(svc, ctx, src.ID, "", "high")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	// The fork rehydrated a per-session engine on the OVERRIDE selector (provider +
	// model inherited, effort replaced).
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times for the fork, want exactly 1 (the override needs a per-session engine)", calls.Load())
	}
	wantForkSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "anthropic/claude-3.5-sonnet", ReasoningEffort: "high"}
	if got := gotSel.Load(); got != wantForkSel {
		t.Fatalf("fork rehydration factory saw selector %v, want %v (effort overridden, provider/model inherited)", got, wantForkSel)
	}
	if !svc.HasSessionEngineForTest(newID) {
		t.Fatalf("fork has no per-session engine registered (an effort-override fork MUST rehydrate one)")
	}

	forked, err := store.Load(ctx, newID)
	if err != nil {
		t.Fatalf("Load forked: %v", err)
	}
	if forked.ReasoningEffort != "high" {
		t.Fatalf("forked ReasoningEffort = %q, want high (the override)", forked.ReasoningEffort)
	}
	if forked.ProviderID != wantSel.ProviderID || forked.ModelID != wantSel.ModelID {
		t.Fatalf("forked provider/model = %q/%q, want the source's %q/%q (ALWAYS inherited)",
			forked.ProviderID, forked.ModelID, wantSel.ProviderID, wantSel.ModelID)
	}
	// The ResolvedModel echo carries the new effort.
	if got := svc.ResolvedModel(newID); got.ReasoningEffort != "high" {
		t.Fatalf("ResolvedModel(fork).ReasoningEffort = %q, want high", got.ReasoningEffort)
	}
	// The source is untouched.
	srcAfter, err := store.Load(ctx, src.ID)
	if err != nil {
		t.Fatalf("Load src: %v", err)
	}
	if srcAfter.ReasoningEffort != "low" {
		t.Fatalf("source ReasoningEffort = %q after the fork, want low (the override touches ONLY the fork)", srcAfter.ReasoningEffort)
	}
}

// TestForkSessionEmptyEffortInherits verifies an empty effort override inherits the
// source's effort verbatim (ADR 0068 default), provider/model included. A non-empty
// effort needs a per-session engine, so the source is built on a factory-backed
// service (the effort label only sticks when the engine factory runs).
func TestForkSessionEmptyEffortInherits(t *testing.T) {
	ctx := context.Background()
	wantSel := server.ProviderSelector{ProviderID: "openrouter", ModelID: "m", ReasoningEffort: "low"}
	factory := func(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		eng := agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("r"), mockllm.TextTurn("r")),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		})
		return server.SessionEngineResult{Engine: eng, Close: func() error { return nil }, ReasoningEffort: sel.ReasoningEffort}, nil
	}
	svc, store := newMCPServiceStore(t, "shared", factory)

	src, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{}, wantSel)
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "hi")

	newID, err := forkSession(svc, ctx, src.ID, "", "")
	if err != nil {
		t.Fatalf("ForkSession empty effort: %v", err)
	}
	forked, err := store.Load(ctx, newID)
	if err != nil {
		t.Fatalf("Load forked: %v", err)
	}
	if forked.ReasoningEffort != wantSel.ReasoningEffort {
		t.Fatalf("forked ReasoningEffort = %q, want the source's %q (empty override inherits)", forked.ReasoningEffort, wantSel.ReasoningEffort)
	}
	if forked.ProviderID != wantSel.ProviderID || forked.ModelID != wantSel.ModelID {
		t.Fatalf("forked provider/model = %q/%q, want the source's %q/%q", forked.ProviderID, forked.ModelID, wantSel.ProviderID, wantSel.ModelID)
	}
}

// TestForkSessionTitleOverride verifies the optional title parameter: empty
// inherits the source's title, non-empty overrides it.
func TestForkSessionTitleOverride(t *testing.T) {
	ctx := context.Background()
	svc, store := newMCPServiceStore(t, "shared", nil)

	src, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "what is the plan?")
	srcSnap, _ := store.Load(ctx, src.ID)
	if srcSnap.Title == "" {
		t.Fatalf("source has no title after a turn")
	}

	// Empty title → inherits source's.
	inherited, err := forkSession(svc, ctx, src.ID, "", "")
	if err != nil {
		t.Fatalf("ForkSession empty title: %v", err)
	}
	inheritedSnap, _ := store.Load(ctx, inherited)
	if inheritedSnap.Title != srcSnap.Title {
		t.Fatalf("inherited title = %q, want source's %q", inheritedSnap.Title, srcSnap.Title)
	}

	// Non-empty title → overrides.
	override := "fix the bug first"
	overridden, err := forkSession(svc, ctx, src.ID, override, "")
	if err != nil {
		t.Fatalf("ForkSession override title: %v", err)
	}
	overriddenSnap, _ := store.Load(ctx, overridden)
	if overriddenSnap.Title != override {
		t.Fatalf("overridden title = %q, want %q", overriddenSnap.Title, override)
	}
}

// TestForkSessionDefaultFSRidesSharedEngine pins gotcha #1: a DEFAULT-FS session
// (empty selector, default profile, non-empty workspace) fork rides the SHARED
// engine — no per-session engine is registered for the fork, and a run on the
// fork succeeds (history replays).
func TestForkSessionDefaultFSRidesSharedEngine(t *testing.T) {
	ctx := context.Background()
	// Build the service directly so the SHARED engine carries TWO text turns
	// (newMCPService wires a single-turn mockllm): the source's and the fork's.
	shared := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("SHARED-FORK-REPLY"), mockllm.TextTurn("SHARED-FORK-REPLY")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{
		Engine: shared,
		Store:  store,

		DefaultLimits: session.Limits{MaxTurns: 10, MaxToolCalls: 20},
		Now:           func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	src, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "hello")

	newID, err := forkSession(svc, ctx, src.ID, "", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if newID == "" || newID == src.ID {
		t.Fatalf("fork id = %q, want a new distinct id", newID)
	}
	if svc.HasSessionEngineForTest(newID) {
		t.Fatalf("default-FS fork registered a per-session engine; it MUST ride the shared engine")
	}
	// The fork runs on the shared engine and replays its inherited history.
	if got := driveCompletedTurn(t, svc, newID, "again"); got != "SHARED-FORK-REPLY" {
		t.Fatalf("fork turn reply = %q, want the shared engine's reply", got)
	}
}

// TestForkSessionRejectsRunningSource pins gotcha #2: a running/awaiting source is
// rejected with ErrFailedPrecondition — fork requires a turn boundary. The source
// is persisted in StateRunning directly (loadAndReopen reads the store, which holds
// the running snapshot), so the running-state check after loadAndReopen fires.
func TestForkSessionRejectsRunningSource(t *testing.T) {
	ctx := context.Background()
	svc, store := newMCPServiceStore(t, "shared", nil)

	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive the persisted snapshot into StateRunning directly: BeginTurn is the
	// idle→running transition, then persist. This is the genuine "fork mid-run"
	// state loadAndReopen reads from the store.
	loaded, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := loaded.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatalf("Save running: %v", err)
	}

	_, ferr := forkSession(svc, ctx, sess.ID, "", "")
	if !errors.Is(ferr, server.ErrFailedPrecondition) {
		t.Fatalf("ForkSession on a running source: err = %v, want ErrFailedPrecondition", ferr)
	}
}

// TestForkSessionCompletedSourceStaysCompleted verifies canonical successor
// creation is non-destructive: a completed source is forkable without recovery.
func TestForkSessionCompletedSourceStaysCompleted(t *testing.T) {
	ctx := context.Background()
	svc, store := newMCPServiceStore(t, "shared", nil)
	id := persistCompleted(t, store)

	newID, err := forkSession(svc, ctx, id, "", "")
	if err != nil {
		t.Fatalf("ForkSession on a completed source: %v", err)
	}
	if newID == "" || newID == id {
		t.Fatalf("fork id = %q, want a new distinct id", newID)
	}
	// Canonical successor creation does not mutate the source.
	src, err := store.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load src: %v", err)
	}
	if src.State != session.StateCompleted {
		t.Fatalf("source state after fork = %q, want completed (non-destructive successor)", src.State)
	}
}

// TestGRPCForkSessionRoundTrip is the gRPC e2e: CreateSession → Converse to a
// terminal result → ForkSession → GetSession on the fork (workspace matches, idle,
// turns 0) → Converse on the fork completes (history replays, no provider 400).
func TestGRPCForkSessionRoundTrip(t *testing.T) {
	// Two text turns: the source's Converse and the fork's Converse.
	svc := newService(t, mockllm.New(
		mockllm.TextTurn("gRPC reply"),
		mockllm.TextTurn("gRPC reply"),
	), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Drive one Converse to a terminal result.
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "hi"}},
	}); err != nil {
		t.Fatalf("Send prompt: %v", err)
	}
	_ = stream.CloseSend()
	events := recvAll(t, stream)
	res := lastResult(t, events)
	if res.GetStop() != "end_turn" || res.GetText() != "gRPC reply" {
		t.Fatalf("source result = %+v", res)
	}
	// Persist the terminal state so ForkSession's loadAndReopen recovers it.
	svc.Persist(ctx, session.SessionID(cs.GetSessionId()))

	forkResp, err := client.ForkSession(ctx, &mecatlv1.ForkSessionRequest{SourceSessionId: cs.GetSessionId()})
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkID := forkResp.GetSessionId()
	if forkID == "" || forkID == cs.GetSessionId() {
		t.Fatalf("fork id = %q, want a new distinct id", forkID)
	}

	got, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: forkID})
	if err != nil {
		t.Fatalf("GetSession on fork: %v", err)
	}
	if got.GetSession().GetPlacement().GetKind() == "" {
		t.Fatal("fork placement metadata is missing")
	}
	if got.GetSession().GetState() != "idle" {
		t.Fatalf("fork state = %q, want idle", got.GetSession().GetState())
	}
	if got.GetSession().GetTurns() != 0 {
		t.Fatalf("fork turns = %d, want 0 (fresh counters)", got.GetSession().GetTurns())
	}

	// The fork can itself Converse to completion (history replays, no provider 400).
	stream2, err := client.Converse(ctx)
	if err != nil {
		t.Fatalf("second Converse: %v", err)
	}
	if err := stream2.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: forkID, Text: "continue"}},
	}); err != nil {
		t.Fatalf("Send fork prompt: %v", err)
	}
	_ = stream2.CloseSend()
	events2 := recvAll(t, stream2)
	res2 := lastResult(t, events2)
	if res2.GetStop() != "end_turn" {
		t.Fatalf("fork Converse result = %+v, want end_turn (history replayed cleanly)", res2)
	}
}

// TestHTTPForkSessionRoundTrip is the HTTP e2e: create → /prompt SSE to
// completion → POST /fork (201 + session_id) → GET session (workspace + idle) →
// POST /prompt on the fork (SSE result arrives).
func TestHTTPForkSessionRoundTrip(t *testing.T) {
	// Two text turns: the source's and the fork's.
	svc := newService(t, mockllm.New(
		mockllm.TextTurn("HTTP reply"),
		mockllm.TextTurn("HTTP reply"),
	), allowRules())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	srcID := createHTTPSession(t, srv)

	// Drive a prompt to completion over SSE.
	promptResp, err := http.Post(srv.URL+"/v1/sessions/"+srcID+"/prompt", "application/json",
		strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	events := parseSSE(t, bufio.NewReader(promptResp.Body))
	promptResp.Body.Close()
	res := lastResult(t, events)
	if res.GetStop() != "end_turn" {
		t.Fatalf("source result = %+v", res)
	}
	svc.Persist(context.Background(), session.SessionID(srcID))

	// Fork.
	forkResp, err := http.Post(srv.URL+"/v1/sessions/"+srcID+"/fork", "application/json", nil)
	if err != nil {
		t.Fatalf("POST fork: %v", err)
	}
	defer forkResp.Body.Close()
	if forkResp.StatusCode != http.StatusCreated {
		t.Fatalf("fork status = %d, want 201", forkResp.StatusCode)
	}
	var forkOut struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(forkResp.Body).Decode(&forkOut); err != nil {
		t.Fatalf("decode fork: %v", err)
	}
	forkID := forkOut.SessionID
	if forkID == "" || forkID == srcID {
		t.Fatalf("fork id = %q, want a new distinct id", forkID)
	}

	// GET the fork: public state omits private placement paths.
	getResp, err := http.Get(srv.URL + "/v1/sessions/" + forkID)
	if err != nil {
		t.Fatalf("GET fork: %v", err)
	}
	defer getResp.Body.Close()
	var sess struct {
		State string `json:"state"`
		Turns int32  `json:"turns"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode fork session: %v", err)
	}
	if sess.State != "idle" {
		t.Fatalf("fork state = %q, want idle", sess.State)
	}

	// The fork can itself run a prompt (SSE result arrives).
	p2, err := http.Post(srv.URL+"/v1/sessions/"+forkID+"/prompt", "application/json",
		strings.NewReader(`{"text":"go"}`))
	if err != nil {
		t.Fatalf("POST fork prompt: %v", err)
	}
	ev2 := parseSSE(t, bufio.NewReader(p2.Body))
	p2.Body.Close()
	res2 := lastResult(t, ev2)
	if res2.GetStop() != "end_turn" {
		t.Fatalf("fork prompt result = %+v, want end_turn (history replayed cleanly)", res2)
	}
}

// effortFactory is a SessionEngine factory that stamps the selector's effort onto
// the result so the fork's ResolvedModel echo + label carry it (ADR 0068).
func effortFactory(_ context.Context, sel server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("r"), mockllm.TextTurn("r")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	return server.SessionEngineResult{Engine: eng, Close: func() error { return nil }, ReasoningEffort: sel.ReasoningEffort}, nil
}

// TestGRPCForkSessionEffortOverride is the gRPC ADR 0068 arm: an effort-override
// fork threads reasoning_effort over the wire to the forked session's label.
func TestGRPCForkSessionEffortOverride(t *testing.T) {
	svc, _ := newMCPServiceStore(t, "gRPC reply", effortFactory)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	src, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openrouter", ModelID: "m", ReasoningEffort: "low"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "hi")

	forkResp, err := client.ForkSession(ctx, &mecatlv1.ForkSessionRequest{
		SourceSessionId: string(src.ID),
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("ForkSession effort override: %v", err)
	}
	loaded, err := svc.LoadSession(ctx, session.SessionID(forkResp.GetSessionId()))
	if err != nil {
		t.Fatalf("LoadSession on the effort-override fork: %v", err)
	}
	if loaded.ReasoningEffort != "high" {
		t.Fatalf("effort-override fork reasoning_effort = %q, want high", loaded.ReasoningEffort)
	}
	if loaded.ProviderID != "openrouter" || loaded.ModelID != "m" {
		t.Fatalf("effort-override fork provider/model = %q/%q, want the inherited openrouter/m", loaded.ProviderID, loaded.ModelID)
	}
	// The ResolvedModel echo (what the ui reads) carries the new effort.
	if got := svc.ResolvedModel(session.SessionID(forkResp.GetSessionId())); got.ReasoningEffort != "high" {
		t.Fatalf("ResolvedModel(fork).ReasoningEffort = %q, want high", got.ReasoningEffort)
	}
}

// TestHTTPForkSessionEffortOverride is the HTTP ADR 0068 arm: a fork body carrying
// reasoning_effort threads the field to the forked session's label.
func TestHTTPForkSessionEffortOverride(t *testing.T) {
	svc, _ := newMCPServiceStore(t, "HTTP reply", effortFactory)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	ctx := context.Background()
	src, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openrouter", ModelID: "m", ReasoningEffort: "low"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "hi")

	forkResp, err := http.Post(srv.URL+"/v1/sessions/"+string(src.ID)+"/fork", "application/json",
		strings.NewReader(`{"reasoning_effort":"high"}`))
	if err != nil {
		t.Fatalf("POST effort fork: %v", err)
	}
	defer forkResp.Body.Close()
	if forkResp.StatusCode != http.StatusCreated {
		t.Fatalf("effort fork status = %d, want 201", forkResp.StatusCode)
	}
	var forkOut struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(forkResp.Body).Decode(&forkOut); err != nil {
		t.Fatalf("decode effort fork: %v", err)
	}
	loaded, err := svc.LoadSession(ctx, session.SessionID(forkOut.SessionID))
	if err != nil {
		t.Fatalf("LoadSession on the effort-override fork: %v", err)
	}
	if loaded.ReasoningEffort != "high" {
		t.Fatalf("effort-override fork reasoning_effort = %q, want high", loaded.ReasoningEffort)
	}
}

// TestForkSessionUnknownSourceNotFound: forking a never-created id surfaces
// ErrNotFound (gRPC NotFound / HTTP 404).
func TestForkSessionUnknownSourceNotFound(t *testing.T) {
	t.Run("service", func(t *testing.T) {
		svc := newMCPService(t, "shared", nil)
		_, err := forkSession(svc, context.Background(), "never-created", "", "")
		if !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("ForkSession unknown id: err = %v, want ErrNotFound", err)
		}
	})
	t.Run("grpc", func(t *testing.T) {
		svc := newService(t, mockllm.New(), allowRules())
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()
		_, err := client.ForkSession(context.Background(), &mecatlv1.ForkSessionRequest{SourceSessionId: "never-created"})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("gRPC ForkSession unknown id: code = %v, want NotFound", status.Code(err))
		}
	})
	t.Run("http", func(t *testing.T) {
		svc := newService(t, mockllm.New(), allowRules())
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		defer srv.Close()
		resp, err := http.Post(srv.URL+"/v1/sessions/never-created/fork", "application/json", nil)
		if err != nil {
			t.Fatalf("POST fork: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("HTTP fork unknown id: status = %d, want 404", resp.StatusCode)
		}
	})
}

// TestForkSessionRespectsEngineCap: with MaxSessionEngines=1, a selector session
// fills the single slot; forking it (which must rehydrate a per-session engine)
// is rejected with ErrTooManySessionEngines.
func TestForkSessionRespectsEngineCap(t *testing.T) {
	ctx := context.Background()
	var closed atomic.Int32
	svc := cappedSelectorService(t, 1, &closed)

	src, err := svc.CreateSessionWithProvider(ctx, session.ModeDefault, session.Limits{},
		server.ProviderSelector{ProviderID: "openrouter"})
	if err != nil {
		t.Fatalf("CreateSessionWithProvider: %v", err)
	}
	driveCompletedTurn(t, svc, src.ID, "hi")

	_, ferr := forkSession(svc, ctx, src.ID, "", "")
	if !errors.Is(ferr, server.ErrTooManySessionEngines) {
		t.Fatalf("ForkSession past cap: err = %v, want ErrTooManySessionEngines", ferr)
	}
}
