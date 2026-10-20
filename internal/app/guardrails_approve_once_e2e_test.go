package app

import (
	"bufio"
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// guardrailMarker is the filename the scripted Shell command creates in the workspace.
// Asserting its presence/absence after the run is the MODEL-FACING proof that the tool
// actually ran (allow) or did not (deny/headless) — not merely that no block result
// appeared on the stream.
const guardrailMarker = "guard_marker"

// markerExists reports whether the scripted Shell command's marker file landed in ws.
func markerExists(t *testing.T, ws string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(ws, guardrailMarker))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat marker: %v", err)
	}
	return err == nil
}

// guardrails_approve_once_e2e_test.go is the FULL-composition e2e for the guardrail
// approve-once flow: app.Build + server.Service over the HTTP SSE relay, offline. The
// model issues a single mutating Shell call (`gh pr merge`); the engine-backed guardrail
// checker (driven by the SAME mock provider, which scripts the verdict turn between the
// agent's tool-call turn and its final turn) judges it UNSAFE. Under an INTERACTIVE
// service the block surfaces as a permission ask (HookOriginated) resolved via /controls/resolve-ask;
// under a HEADLESS service it degrades to a terminal block (no ask ever); under posture
// YOLO it demotes to advisory (tool runs, no ask, no block).

// guardrailE2EScript scripts the shared mock provider for one Shell call: the agent's
// tool-call turn, then the reviewer's PROHIBITED assessment turn (a single JSON
// object — the reviewer fires during the Shell call's preHook, between the agent's
// two turns), then the agent's final turn.
func guardrailE2EScript(cmd string) []mockllm.Turn {
	return []mockllm.Turn{
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Shell", json.RawMessage(`{"command":"`+cmd+`"}`))),
		mockllm.TextTurn(`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"merges a PR unattended","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`),
		mockllm.TextTurn("done"),
	}
}

func guardrailE2ECfg(t *testing.T, interactive bool, posture Posture, cmd string) Config {
	t.Helper()
	// auto/yolo grant allow-all so the POLICY does not ask for the mutating Shell call —
	// only the GUARDRAIL gates it (the point of the test). They refuse to boot as root
	// without a declared sandbox; affirm one so the test boots in any environment.
	if posture >= PostureAuto {
		t.Setenv("MECATL_SANDBOX", "1")
	}
	// No UseMock (that short-circuits the provider to a canned mock, ignoring the
	// providerConstructor seam). Instead a fake OPENAI key makes the provider
	// AVAILABLE and the providerConstructor returns our scripted mock. The model ids
	// contain "-" so lookupModelAlias treats them as concrete (known) — guardrails
	// then wire (normalizeGuardrailsModel passes for a concrete id off the mock path).
	// Shell must be set or the Shell tool is not registered (buildCommandRunner). An
	// EXPLICIT Shell pre/block rule makes the enforcement deterministic.
	return Config{
		Workspace:           t.TempDir(),
		NoSoul:              true,
		StoreDir:            t.TempDir(),
		MemoryDir:           t.TempDir(),
		Model:               "test-model",
		GuardrailsModel:     "guard-model",
		GuardrailsRules:     []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}},
		Shell:               "/bin/sh",
		Interactive:         interactive,
		Posture:             posture,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(guardrailE2EScript(cmd)...)
		},
	}
}

// sseGuardEvent decodes only the fields the guardrail e2e asserts. NOTE:
// PendingAsk.HookOriginated is an engine-internal + snapshot field, NOT on the proto
// wire (approve-once added no proto field), so the e2e asserts only that an ask SURFACED;
// the HookOriginated marker + its snapshot round-trip are pinned by the engine unit
// tests (engine/agent/guardrail_ask_test.go).
type sseGuardEvent struct {
	Type  string `json:"type"`
	RunID string `json:"run_id"`
	Ask   struct {
		AskID     string `json:"ask_id"`
		Guardrail struct {
			ReviewID string `json:"review_id"`
			Kind     int32  `json:"kind"`
		} `json:"guardrail"`
	} `json:"ask"`
	ToolResult struct {
		IsError bool   `json:"is_error"`
		Content string `json:"content"`
	} `json:"tool_result"`
}

// driveGuardrailPrompt POSTs a prompt and drains the SSE stream, calling onEvent per
// event. Returns the decoded events.
func driveGuardrailPrompt(t *testing.T, srvURL, id, text string, onEvent func(ev sseGuardEvent)) []sseGuardEvent {
	t.Helper()
	resp, err := http.Post(srvURL+"/v1/sessions/"+id+"/prompt", "application/json",
		strings.NewReader(`{"text":`+strconv.Quote(text)+`}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	var evs []sseGuardEvent
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimRight(sc.Text(), "\r\n"), "data: ")
		if !ok {
			continue
		}
		var ev sseGuardEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		evs = append(evs, ev)
		if onEvent != nil {
			onEvent(ev)
		}
	}
	return evs
}

// Interactive Allow once: the guardrail block surfaces as a HookOriginated ask; /controls/resolve-ask
// AllowOnce runs the Shell call and the run completes.
func TestGuardrailApproveOnceE2EInteractiveAllow(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, true, PostureAuto, "touch "+guardrailMarker)
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	// ModeAuto: Shell is auto-allowed (allow-all rules); the guardrails block fires.
	sess, err := built.Service.CreateSession(ctx, session.ModeAuto, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()

	var approved bool
	evs := driveGuardrailPrompt(t, srv.URL, string(sess.ID), "merge it", func(ev sseGuardEvent) {
		if ev.Type == "permission.ask" && !approved {
			approved = true
			body, _ := json.Marshal(map[string]any{
				"expected_run_id": ev.RunID,
				"ask_id":          ev.Ask.AskID,
				"verdict":         session.VerdictStringAllowOnce,
				"review_id":       ev.Ask.Guardrail.ReviewID,
				"guardrail_kind":  string(session.GuardrailApprovalAction),
			})
			ar, aerr := http.Post(srv.URL+"/v1/sessions/"+string(sess.ID)+"/controls/resolve-ask", "application/json", strings.NewReader(string(body)))
			if aerr != nil {
				t.Errorf("POST approve: %v", aerr)
				return
			}
			ar.Body.Close()
		}
	})
	if !approved {
		t.Fatal("the guardrail block must surface a permission ask on an interactive service")
	}
	// No blocked tool result should appear (the AllowOnce ran the tool).
	for _, ev := range evs {
		if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "blocked by guardrail") {
			t.Fatal("AllowOnce must run the tool, not surface a guardrail block error")
		}
	}
	// MODEL-FACING proof: the Shell command actually RAN — its marker file exists.
	if !markerExists(t, cfg.Workspace) {
		t.Fatal("AllowOnce must EXECUTE the Shell command — the marker file is missing (the tool did not run)")
	}
	final, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("final state = %q, want completed", final.State)
	}
}

// Interactive Deny: the ask is denied and the Shell call is blocked.
func TestGuardrailApproveOnceE2EInteractiveDeny(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, true, PostureAuto, "touch "+guardrailMarker)
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	// ModeAuto: Shell is auto-allowed (allow-all rules); the guardrails block fires.
	sess, err := built.Service.CreateSession(ctx, session.ModeAuto, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()

	var asked bool
	evs := driveGuardrailPrompt(t, srv.URL, string(sess.ID), "merge it", func(ev sseGuardEvent) {
		if ev.Type == "permission.ask" && !asked {
			asked = true
			body, _ := json.Marshal(map[string]any{
				"expected_run_id": ev.RunID,
				"ask_id":          ev.Ask.AskID,
				"verdict":         session.VerdictStringDeny,
				"review_id":       ev.Ask.Guardrail.ReviewID,
				"guardrail_kind":  string(session.GuardrailApprovalAction),
			})
			ar, aerr := http.Post(srv.URL+"/v1/sessions/"+string(sess.ID)+"/controls/resolve-ask", "application/json", strings.NewReader(string(body)))
			if aerr == nil {
				ar.Body.Close()
			}
		}
	})
	if !asked {
		t.Fatal("the block must surface an ask before the deny")
	}
	var blocked bool
	for _, ev := range evs {
		if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "guardrail") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("a denied guardrail ask must surface the block as an error tool result")
	}
	// MODEL-FACING proof: a deny must NOT execute the command — no marker file.
	if markerExists(t, cfg.Workspace) {
		t.Fatal("a denied guardrail ask must NOT execute the Shell command — but the marker file exists (the tool ran)")
	}
}

// Headless: a non-interactive service NEVER surfaces a guardrail ask — the block is
// terminal and the tool is not run.
func TestGuardrailApproveOnceE2EHeadlessTerminalBlock(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, false, PostureAuto, "touch "+guardrailMarker)
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	// ModeAuto: Shell is auto-allowed (allow-all rules); the guardrails block fires.
	sess, err := built.Service.CreateSession(ctx, session.ModeAuto, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()

	evs := driveGuardrailPrompt(t, srv.URL, string(sess.ID), "merge it", nil)
	for _, ev := range evs {
		if ev.Type == "permission.ask" {
			t.Fatal("SECURITY: a headless service must NOT surface a guardrail ask")
		}
	}
	var blocked bool
	for _, ev := range evs {
		if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "guardrail") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("the headless degrade must produce a terminal guardrail block error result")
	}
	// MODEL-FACING proof: a headless terminal block must NOT execute the command.
	if markerExists(t, cfg.Workspace) {
		t.Fatal("a headless terminal block must NOT execute the Shell command — but the marker file exists (the tool ran)")
	}
}

// Posture YOLO, session switched to AUTO: the yolo demotion is per session mode,
// so the auto session is admitted (an enforcing checker exists for it) and its
// guardrail blocks — headless, a terminal block, and the tool does not run.
func TestGuardrailE2EYoloServerAutoSessionEnforces(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, false, PostureYolo, "touch "+guardrailMarker)
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeYolo, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := built.Service.SetMode(ctx, sess.ID, session.ModeAuto); err != nil {
		t.Fatalf("SetMode(auto) on a yolo server with a checker: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()

	evs := driveGuardrailPrompt(t, srv.URL, string(sess.ID), "merge it", nil)
	var blocked bool
	for _, ev := range evs {
		if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "guardrail") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("an auto session on a yolo server must keep an enforcing checker — no guardrail block surfaced")
	}
	if markerExists(t, cfg.Workspace) {
		t.Fatal("the enforcing auto-session block must NOT execute the Shell command — but the marker file exists")
	}
}

// The yolo demotion follows the executing session's mode on the agent-bound root
// path too: an auto agent-bound session on a yolo server keeps an enforcing checker.
func TestGuardrailE2EYoloServerAutoAgentBoundSessionEnforces(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, false, PostureYolo, "touch "+guardrailMarker)
	agentsDir := t.TempDir()
	writeAgentDefFrontmatterFile(t, agentsDir, "operator", "tools: [Shell]\n", "You run commands.")
	cfg.AgentsDirs = []string{agentsDir}
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeAuto, session.Limits{},
		server.ProviderSelector{}, server.ProfileDefault, server.WithAgentDefinitionName("operator"))
	if err != nil {
		t.Fatalf("CreateSession(auto, agent-bound) on a yolo server with a checker: %v", err)
	}
	if sess.Mode != session.ModeAuto {
		t.Fatalf("Mode = %q, want auto", sess.Mode)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()

	evs := driveGuardrailPrompt(t, srv.URL, string(sess.ID), "merge it", nil)
	var blocked bool
	for _, ev := range evs {
		if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "guardrail") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("an auto agent-bound session on a yolo server must keep an enforcing checker — no guardrail block surfaced")
	}
	if markerExists(t, cfg.Workspace) {
		t.Fatal("the enforcing auto-session block must NOT execute the Shell command — but the marker file exists")
	}
}

// modeRoutedProvider sends guardrail-reviewer requests (the GuardrailsModel) to
// reviewer and every other request to agent, so one session's agent script
// stays in order whether or not a mode consults the checker.
type modeRoutedProvider struct {
	agent, reviewer *mockllm.Provider
	reviewerModel   string
}

func (p modeRoutedProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	if req.Model == p.reviewerModel {
		return p.reviewer.Stream(ctx, req)
	}
	return p.agent.Stream(ctx, req)
}

func (p modeRoutedProvider) Capabilities() port.ProviderCapabilities { return p.agent.Capabilities() }

// modeTurnOutcome is what one executed turn surfaced.
type modeTurnOutcome struct {
	policyAsks, guardrailAsks []string
	results                   []session.ToolResult
}

// runDenyingTurn runs one prompt to completion on the real service, denying
// every ask, and returns the asks (split by origin) and tool results.
func runDenyingTurn(t *testing.T, built *Built, id session.SessionID) modeTurnOutcome {
	t.Helper()
	run, err := built.Service.StartRun(context.Background(), id, "merge it")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var out modeTurnOutcome
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			if ev.Ask.Guardrail != nil {
				out.guardrailAsks = append(out.guardrailAsks, ev.Ask.Tool)
			} else {
				out.policyAsks = append(out.policyAsks, ev.Ask.Tool)
			}
			run.Approve(ev.Ask.AskID, session.VerdictDeny)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			out.results = append(out.results, *ev.ToolResult)
		}
	}
	built.Service.FinishRun(id, run)
	return out
}

// TestGuardrailE2EExecutedYoloAutoDefaultTransitions runs ONE session through
// three completed turns on a yolo server with a rejecting checker, switching
// mode between runs so the engine transitions live: yolo allows the mutation
// with the checker advisory; auto lets the same checker block it while a
// configured Deny still denies; default asks for it instead of auto-allowing.
func TestGuardrailE2EExecutedYoloAutoDefaultTransitions(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, true, PostureYolo, "")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("permissions:\n  deny:\n    - Write\n"), 0o600); err != nil {
		t.Fatalf("write permission config: %v", err)
	}
	cfg.PermissionConfigs = []string{settings}
	shell := func(id session.ToolCallID, marker string) mockllm.Turn {
		return mockllm.ToolCallTurn(session.NewToolCall(id, "Shell", json.RawMessage(`{"command":"touch `+marker+`"}`)))
	}
	denied := filepath.Join(cfg.Workspace, "denied.txt")
	agent := mockllm.New(
		shell("y1", "yolo_marker"), mockllm.TextTurn("done"),
		shell("a1", "auto_marker"),
		mockllm.ToolCallTurn(session.NewToolCall("a2", "Write", json.RawMessage(`{"path":"`+denied+`","content":"x"}`))),
		mockllm.TextTurn("done"),
		shell("d1", "default_marker"), mockllm.TextTurn("done"),
	)
	verdicts := make([]mockllm.Turn, 8)
	for i := range verdicts {
		verdicts[i] = guardrailE2EScript("")[1]
	}
	reviewer := mockllm.New(verdicts...)
	cfg.providerConstructor = func(Config, string, string, string) port.LLMProvider {
		return modeRoutedProvider{agent: agent, reviewer: reviewer, reviewerModel: cfg.GuardrailsModel}
	}
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeYolo, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(cfg.Workspace, name))
		return err == nil
	}

	// yolo: the mutation is auto-allowed and the rejecting checker is advisory.
	yolo := runDenyingTurn(t, built, sess.ID)
	if len(yolo.policyAsks)+len(yolo.guardrailAsks) != 0 || len(yolo.results) != 1 || yolo.results[0].IsError || !exists("yolo_marker") {
		t.Fatalf("yolo turn: asks %v/%v results %+v marker %t; want the Shell call to run unasked", yolo.policyAsks, yolo.guardrailAsks, yolo.results, exists("yolo_marker"))
	}

	if reviewer.Calls() == 0 {
		t.Fatal("yolo turn: the checker was never consulted, so its advisory verdict proves nothing")
	}
	if _, err := built.Service.SetMode(ctx, sess.ID, session.ModeAuto); err != nil {
		t.Fatalf("SetMode(auto) after a completed yolo run: %v", err)
	}
	// auto: no policy ask, but the same checker now blocks the mutation, and the
	// configured Deny still denies Write without an ask.
	auto := runDenyingTurn(t, built, sess.ID)
	if len(auto.policyAsks) != 0 || len(auto.guardrailAsks) != 1 || auto.guardrailAsks[0] != "Shell" {
		t.Fatalf("auto turn asks: policy %v guardrail %v; want exactly one guardrail ask for Shell", auto.policyAsks, auto.guardrailAsks)
	}
	if exists("auto_marker") {
		t.Fatal("auto turn: the checker-blocked Shell command ran")
	}
	if len(auto.results) != 2 || !auto.results[0].IsError || !auto.results[1].IsError {
		t.Fatalf("auto turn results = %+v; want the blocked Shell and denied Write as errors", auto.results)
	}
	if _, err := os.Stat(denied); !os.IsNotExist(err) {
		t.Fatalf("auto turn: configured Deny let Write land (stat err %v)", err)
	}

	if _, err := built.Service.SetMode(ctx, sess.ID, session.ModeDefault); err != nil {
		t.Fatalf("SetMode(default) after a completed auto run: %v", err)
	}
	// default: the mutation is not auto-allowed; it surfaces as an ordinary
	// permission ask, and the denied command never runs.
	def := runDenyingTurn(t, built, sess.ID)
	if len(def.policyAsks) == 0 || def.policyAsks[0] != "Shell" {
		t.Fatalf("default turn asks: policy %v guardrail %v; want an ordinary Shell permission ask", def.policyAsks, def.guardrailAsks)
	}
	if exists("default_marker") {
		t.Fatal("default turn: the denied Shell command ran")
	}
	final, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if final.Mode != session.ModeDefault || final.State != session.StateCompleted {
		t.Fatalf("final session mode %q state %q, want default/completed", final.Mode, final.State)
	}
}

// Posture YOLO: guardrails demote to advisory — the tool RUNS, no ask, no block.
func TestGuardrailApproveOnceE2EYoloAdvisory(t *testing.T) {
	ctx := context.Background()
	cfg := guardrailE2ECfg(t, true, PostureYolo, "touch "+guardrailMarker)
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	// ModeYolo: Shell is auto-allowed (allow-all rules + loose evaluator); guardrails are advisory.
	sess, err := built.Service.CreateSession(ctx, session.ModeYolo, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()

	evs := driveGuardrailPrompt(t, srv.URL, string(sess.ID), "merge it", nil)
	for _, ev := range evs {
		if ev.Type == "permission.ask" {
			t.Fatal("under yolo guardrails are advisory — no ask must surface")
		}
		if ev.Type == "tool.result" && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "blocked by guardrail") {
			t.Fatal("under yolo guardrails are advisory — the tool must not be blocked")
		}
	}
	// MODEL-FACING proof: under yolo (advisory) the command RUNS — marker exists.
	if !markerExists(t, cfg.Workspace) {
		t.Fatal("under yolo (advisory) the Shell command must run — the marker file is missing")
	}
	final, err := built.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("final state = %q, want completed", final.State)
	}
}
