package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Scenario 6 of docs/acceptance/steer-while-running.md: the composition gate.
// The steer inbox is armed by a DEFAULT-ON composition knob (Config.DisableSteer
// is the opt-OUT), threaded through engineDepsForProvider into agent.Deps.EnableSteer
// and reflected — via the SAME wired engine's Engine.SteerEnabled() — in
// ServerCapabilities.steer (task 05 reads the one composition-computed bit).

// blockingSteerTool parks a run mid-dispatch (genuinely LIVE) until its release
// channel is closed, then returns a fixed result. It lets a composition e2e drive a
// steer while the run is provably in-flight (the enqueue hits a live inbox).
type blockingSteerTool struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingSteerTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "SteerBlock", Description: "steer test blocking tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*blockingSteerTool) ReadOnly() bool { return true }
func (b *blockingSteerTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	close(b.started)
	select {
	case <-b.release:
		return session.NewToolResult(in.ID, "read ok"), nil
	case <-ctx.Done():
		return session.ToolResult{}, ctx.Err()
	}
}

var _ tool.Tool = (*blockingSteerTool)(nil)

// writeOperatorSteerFile writes an operator-tier settings.yaml carrying `steer:
// <value>` to a temp file and returns its path, for use as a
// Config.PermissionConfigs entry (the explicit CLI tier — an OPERATOR tier, so the
// steer: key is honoured). Mirrors writeOperatorPostureFile.
func writeOperatorSteerFile(t *testing.T, value string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "operator.yaml")
	if err := os.WriteFile(path, []byte("steer: "+value+"\n"), 0o600); err != nil {
		t.Fatalf("write operator steer file: %v", err)
	}
	return path
}

// steerCapsFromBuild creates a session against the built Service via the gRPC
// CreateSession handler and returns the echoed ServerCapabilities — the SAME
// projection a real client reads (mirrors postureEchoFromBuild).
func steerCapsFromBuild(t *testing.T, built *Built) *mecatlv1.ServerCapabilities {
	t.Helper()
	resp, err := server.NewHarnessServer(built.Service).CreateSession(context.Background(),
		&mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return resp.GetCapabilities()
}

// TestSteer_EnabledByDefaultEndToEnd is AC6.1: with steer enabled (the DEFAULT —
// no Config knob set), (a) the capability is advertised on the CreateSession echo,
// and (b) a mid-run input routed through Service.Steer reaches the model on the
// SAME run (no separate follow-up run): exactly two provider calls happen, the
// second's replay ending on the steered user message.
//
// The blockingSteerTool parks the run mid-dispatch (genuinely LIVE); the steer is
// enqueued while the tool holds the run, then the tool is released so the run
// drains the steer at the upcoming turn boundary and drives the second turn.
func TestSteer_EnabledByDefaultEndToEnd(t *testing.T) {
	ctx := context.Background()
	block := &blockingSteerTool{started: make(chan struct{}), release: make(chan struct{})}
	var reqsMu sync.Mutex
	var reqs []port.LLMRequest
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
			reqsMu.Lock()
			reqs = append(reqs, r)
			reqsMu.Unlock()
		})},
		mockllm.ToolCallTurn(session.NewToolCall("c1", "SteerBlock", json.RawMessage(`{"path":"a.go"}`))),
		mockllm.TextTurn("turn two done"),
	)
	// No DisableSteer set: the DEFAULT is ON. AllowAllTools so the test tool's
	// dispatch auto-approves (Interactive is false; an ask would park the run
	// headless instead of holding it mid-dispatch the way this test needs).
	built, err := buildIsolated(t, ctx, Config{
		Workspace:      t.TempDir(),
		Model:          "mock",
		NoSoul:         true,
		MockProvider:   llm,
		AllowAllTools:  true,
		extraCoreTools: []tool.Tool{block},
		extraCoreToolClassifications: map[string]server.ClassificationEntry{
			"SteerBlock": {Kind: server.KindDerived, Rationale: "test tool is contained within the current authorized run"},
		},
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// (a) The capability is advertised on the CreateSession echo.
	if !steerCapsFromBuild(t, built).GetSteer() {
		t.Fatal("steer capability = false, want true (DEFAULT ON)")
	}

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "look")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Steer while the run is genuinely LIVE (parked mid-dispatch), then release.
	steered := make(chan agent.SteerOutcome, 1)
	go func() {
		<-block.started
		outcome, promoted, promotedRun, err := built.Service.Steer(ctx, sess.ID, "also check b.go", nil, "", "")
		if err != nil {
			t.Errorf("Steer: %v", err)
		}
		if promoted || promotedRun != nil {
			t.Errorf("a live-run steer must ENQUEUE, not promote (promoted=%v)", promoted)
		}
		steered <- outcome
		close(block.release)
	}()

	var sawSteerEcho bool
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvSteer && ev.Steer != nil && ev.Steer.Text == "also check b.go" {
			sawSteerEcho = true
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	if got := <-steered; got != agent.SteerAccepted {
		t.Fatalf("steer outcome = %q, want %q", got, agent.SteerAccepted)
	}
	if !sawSteerEcho {
		t.Fatal("no EvSteer echo carrying the committed text on the run's events")
	}
	if stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want %q (the steered run completes the second turn)", stop, session.StopEndTurn)
	}

	// (b) The input reached the model WITHOUT a separate follow-up run: exactly
	// two provider calls, the second's replay ending on the steered user message.
	reqsMu.Lock()
	defer reqsMu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2 (the steer feeds the SAME run's following turn)", len(reqs))
	}
	msgs := reqs[1].Messages
	final := msgs[len(msgs)-1]
	if final.Role != session.RoleUser || final.Text != "also check b.go" {
		t.Fatalf("final replayed message = (%q, %q), want (user, %q)", final.Role, final.Text, "also check b.go")
	}
}

// TestSteer_DisabledCompositionInert covers the composition half of AC6.2: with
// Config.DisableSteer set, the engine inbox is INERT — the capability echo reads
// false and a mid-run Service.Steer reports too_late + promotes to a fresh
// follow-up run (the Service's documented never-drop contract) instead of
// draining into the live run.
//
// (The TUI half of AC6.2 — the byte-identical client-side merge-queue fallback —
// is TestSteer_DisabledFallsBackToLocalQueue in cmd/mecatui/ui.)
func TestSteer_DisabledCompositionInert(t *testing.T) {
	ctx := context.Background()
	// Two text turns: one for the initial run, one for the promoted follow-up run
	// (a single scripted turn would exhaust the cursor on the promote and drive the
	// no-progress path instead of a clean end_turn).
	llm := mockllm.New(mockllm.TextTurn("done"), mockllm.TextTurn("aftermath done"))
	built, err := buildIsolated(t, ctx, Config{
		Workspace:           t.TempDir(),
		Model:               "mock",
		NoSoul:              true,
		MockProvider:        llm,
		DisableSteer:        true, // the opt-out
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if steerCapsFromBuild(t, built).GetSteer() {
		t.Fatal("steer capability = true, want false (DisableSteer)")
	}

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() { // drain to terminal
	}

	// The inbox is inert: a steer for the (now-terminal) session reports too_late
	// and PROMOTES to a follow-up run through the run-entry funnel — never silently
	// dropped, never drained into the finished run.
	outcome, promoted, promotedRun, err := built.Service.Steer(ctx, sess.ID, "aftermath", nil, "", "")
	if err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if outcome != agent.SteerTooLate || !promoted || promotedRun == nil {
		t.Fatalf("disabled steer must report too_late + promote; got outcome=%q promoted=%v run=%v", outcome, promoted, promotedRun)
	}
	var stop session.StopReason
	for ev := range promotedRun.Events() {
		if ev.Type == session.EvSteer {
			t.Error("a disabled steer inbox must never emit EvSteer")
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	built.Service.FinishRun(sess.ID, promotedRun)
	if stop != session.StopEndTurn {
		t.Fatalf("promoted follow-up stop = %q, want %q", stop, session.StopEndTurn)
	}
}

// TestSteer_DisableViaOperatorYAML proves the operator-tier `steer: false`
// settings.yaml key (user-global + CLI tiers ONLY — never the project tier)
// composes to the inbox being off: the capability echo reads false. Mirrors the
// operator-tier posture/reasoning-effort fold discipline.
func TestSteer_DisableViaOperatorYAML(t *testing.T) {
	ctx := context.Background()
	built, err := buildIsolated(t, ctx, Config{
		Workspace:           t.TempDir(),
		Model:               "mock",
		UseMock:             true,
		NoSoul:              true,
		PermissionConfigs:   []string{writeOperatorSteerFile(t, "false")},
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if steerCapsFromBuild(t, built).GetSteer() {
		t.Fatal("steer capability = true, want false (operator-tier steer: false)")
	}
}

// TestSteer_ProjectYAMLIgnored proves a PROJECT-tier `steer: false` key is
// IGNORED with a WARN (operator-tier only, mirroring posture): the capability
// stays ON (the default) — a project repo cannot flip the harness's operator
// surface either way.
func TestSteer_ProjectYAMLIgnored(t *testing.T) {
	ctx := context.Background()
	permEnv := isolatedPermConfigEnv(t)
	withTrustEnv(t, *permEnv)
	ws := t.TempDir()
	mkdirProjectSettings(t, ws, "steer: false\n")
	diag := slogdiagBuffer(t)
	built, err := buildIsolated(t, ctx, Config{
		Workspace:               ws,
		Model:                   "mock",
		UseMock:                 true,
		NoSoul:                  true,
		PermissionsConventional: true, // discover the project file (and ignore its steer:)
		TrustProject:            true, // even trusted, the project tier cannot set steer
		permConfigEnv:           permEnv,
		Diagnostics:             diag.diag,
		envDetector:             fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient:     offlineHTTPClient(),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	if !steerCapsFromBuild(t, built).GetSteer() {
		t.Fatal("a PROJECT-tier steer: false must be IGNORED; capability = false, want true (default ON)")
	}

	// The WARN fires lazily: loadProjectRules runs per root on the FIRST Resolve
	// against it, not at Build. Drive a Resolve through the real permconfig resolver
	// (constructed by buildPermResolver over the SAME isolated env) so the
	// ignore-with-WARN lands. The capability assertion above is the behavioral proof
	// the key was ignored; this is the loud-WARN proof.
	resolver := buildPermResolver(Config{
		Workspace:               ws,
		PermissionsConventional: true,
		TrustProject:            true,
		permConfigEnv:           permEnv,
		Diagnostics:             diag.diag,
	})
	pws, err := osfs.NewWorkspace(ws)
	if err != nil {
		t.Fatalf("osfs.NewWorkspace(%q): %v", ws, err)
	}
	_ = resolver.Resolve(ctx, pws)
	if !strings.Contains(diag.String(), "steer") {
		t.Fatalf("the ignored project-tier steer: key must WARN; log:\n%s", diag.String())
	}
}
