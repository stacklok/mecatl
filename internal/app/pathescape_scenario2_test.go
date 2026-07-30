package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// pathescape_scenario2_test.go pins the path-escape-posture Scenario 2
// acceptance criteria (docs/acceptance/path-escape-posture.md): at posture
// yolo and auto an out-of-root absolute-path Read succeeds through the FS tool
// (no ErrPathEscape) with full audit parity; strict stays unchanged; pseudo-fs
// (/proc/self/environ) is never served even at yolo; a nested symlink escape
// is refused by the serving *os.Root; and a relaxed session restarted
// rehydrates the SAME relaxed workspace. All offline (mockllm + a real osfs
// workspace under t.TempDir).

// escapeFixture is the on-disk layout every Scenario-2 test shares: the
// session workspace root plus an out-of-root target file.
type escapeFixture struct {
	workspace string
	outside   string
	target    string // out-of-root file the model reads
	content   string
}

// setupEscapeFS builds workspace + an out-of-root file with distinctive
// content (so a Bash `cat` substitution or an in-root fixture file cannot
// satisfy the assertion by accident).
func setupEscapeFS(t *testing.T) escapeFixture {
	t.Helper()
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{workspace, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
	f := escapeFixture{
		workspace: workspace,
		outside:   outside,
		target:    filepath.Join(outside, "secret.txt"),
		content:   "escape-read-content-7f3a",
	}
	if err := os.WriteFile(f.target, []byte(f.content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", f.target, err)
	}
	return f
}

// escapeCfg returns the offline Build config for a relaxed-read e2e: mockllm
// provider, real osfs workspace, no network. Posture is set by the caller.
func escapeCfg(t *testing.T, f escapeFixture, posture Posture, turns ...mockllm.Turn) Config {
	t.Helper()
	return Config{
		Workspace:           f.workspace,
		NoSoul:              true,
		Posture:             posture,
		PostureFlagSet:      true,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(turns...)
		},
	}
}

// auditRecorder is a test ToolCallRecorder capturing each call verbatim.
type auditRecorder struct {
	calls []recordedCall
}

type recordedCall struct {
	name    string
	args    string
	isError bool
}

func (r *auditRecorder) ToolCall(_ session.SessionID, call session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	r.calls = append(r.calls, recordedCall{name: call.Name, args: string(call.Args), isError: result.IsError})
}

// runOneTurn drives one prompt through the service and collects its events.
// The scripted turn issues ONE Read of the out-of-root target, then a closing
// text turn. It returns the tool result (if any) and the terminal stop.
func runOneTurn(t *testing.T, built *Built, sessID session.SessionID) (result *session.ToolResult, stop session.StopReason) {
	t.Helper()
	run, err := built.Service.StartRun(context.Background(), sessID, "read the file outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			result = ev.ToolResult
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	built.Service.FinishRun(sessID, run)
	return result, stop
}

// readEscapeTurns scripts the two-turn mockllm conversation: turn 1 issues the
// out-of-root Read tool call, turn 2 closes with text (so the run completes).
func readEscapeTurns(target string) []mockllm.Turn {
	args, _ := json.Marshal(map[string]string{"path": target})
	return []mockllm.Turn{
		mockllm.ToolCallTurn(session.NewToolCall("r1", "Read", json.RawMessage(args))),
		mockllm.TextTurn("done"),
	}
}

// TestPathEscapePosture_Scenario2_YoloReadEscapeAllowed pins AC2.1: at posture
// yolo, Read of an out-of-root absolute path returns the file's contents (no
// ErrPathEscape) — the relax flows through the ordinary FS tool, not a Bash
// workaround.
func TestPathEscapePosture_Scenario2_YoloReadEscapeAllowed(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	built, err := Build(context.Background(), escapeCfg(t, f, PostureYolo, readEscapeTurns(f.target)...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	result, stop := runOneTurn(t, built, sess.ID)
	if result == nil {
		t.Fatal("no EvToolResult emitted for the Read call")
	}
	if result.IsError {
		t.Fatalf("yolo Read escape errored: %q (want the file contents, no ErrPathEscape)", result.Content)
	}
	if !strings.Contains(result.Content, f.content) {
		t.Fatalf("yolo Read escape content = %q, want it to contain %q", result.Content, f.content)
	}
	if stop != session.StopEndTurn {
		t.Fatalf("run stop = %q, want %q", stop, session.StopEndTurn)
	}
}

// TestPathEscapePosture_Scenario2_AutoReadEscapeAllowed pins AC2.2: at posture
// auto with no escape guardrail knob, Read of an out-of-root absolute path
// succeeds (Bash parity — at auto Bash already reads the same bytes).
func TestPathEscapePosture_Scenario2_AutoReadEscapeAllowed(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	built, err := Build(context.Background(), escapeCfg(t, f, PostureAuto, readEscapeTurns(f.target)...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	result, _ := runOneTurn(t, built, sess.ID)
	if result == nil {
		t.Fatal("no EvToolResult emitted for the Read call")
	}
	if result.IsError {
		t.Fatalf("auto Read escape errored: %q (want Bash-parity read success)", result.Content)
	}
	if !strings.Contains(result.Content, f.content) {
		t.Fatalf("auto Read escape content = %q, want it to contain %q", result.Content, f.content)
	}
}

// TestPathEscapePosture_Scenario2_ReadEscapeAuditParity pins AC2.3: an allowed
// read escape records the call VERBATIM in the ToolCallRecorder and emits
// EvToolResult, exactly as an in-root read — the relax flows through the
// ordinary authorize → preHook + execute tail, never a side channel.
func TestPathEscapePosture_Scenario2_ReadEscapeAuditParity(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	recorder := &auditRecorder{}
	cfg := escapeCfg(t, f, PostureYolo, readEscapeTurns(f.target)...)
	cfg.ToolCallRecorder = recorder
	built, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	result, _ := runOneTurn(t, built, sess.ID)
	// EvToolResult fired with the content (the model-visible half of parity).
	if result == nil || result.IsError || !strings.Contains(result.Content, f.content) {
		t.Fatalf("EvToolResult = %+v, want a non-error result containing %q", result, f.content)
	}
	// The ToolCallRecorder recorded the call VERBATIM: the tool name and the
	// exact out-of-root path arg, not a redacted or rewritten form — the same
	// record an in-root read produces.
	var found bool
	for _, c := range recorder.calls {
		if c.name == "Read" && strings.Contains(c.args, f.target) {
			found = true
			if c.isError {
				t.Fatalf("recorded Read result is an error (audit parity — the recorded call must be the allowed read)")
			}
		}
	}
	if !found {
		t.Fatalf("no ToolCallRecorder entry for Read(%q) — the allowed escape must be audited verbatim like an in-root read", f.target)
	}
}

// TestPathEscapePosture_Scenario2_StrictReadUnchanged pins AC2.4: at posture
// strict, a Read escape does NOT silently succeed in this wave — behaviour is
// unchanged from today. Scenario 4 later moved the strict/trusted escape onto
// the FS-tool ASK (never a silent allow, and never the ErrPathEscape
// dead-end), so "unchanged" pins the two halves that survive: no ask means
// the run still dead-ends (the cancellation-waits below would hang forever if
// the ask were missing), and the result is never the file's contents. With
// the ask wired (post-Scenario-4), a DENY verdict records a deny result and
// never reads the file — still not a silent success.
func TestPathEscapePosture_Scenario2_StrictReadUnchanged(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	built, err := Build(context.Background(), escapeCfg(t, f, PostureStrict, readEscapeTurns(f.target)...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "read the file outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askSeen, cancelled bool
	var result *session.ToolResult
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && !askSeen {
			askSeen = true
			run.Approve(ev.Ask.AskID, session.VerdictDeny)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			result = ev.ToolResult
		}
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
			cancelled = true
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if result != nil && !result.IsError {
		t.Fatalf("strict Read escape SUCCEEDED with content %q — a strict escape must never silently succeed (it asks, or dead-ends)", result.Content)
	}
	if askSeen {
		// Scenario 4 landed: the escape surfaced the FS-tool ask and the deny
		// verdict refused it. The deny result must name WHY (the escape ask
		// reason), never the file contents.
		if result == nil {
			t.Fatal("no tool result after the denied escape ask — the denied read must record a deny result")
		}
		if !strings.Contains(result.Content, "denied by user") && !strings.Contains(result.Content, "outside the workspace") {
			t.Fatalf("denied strict escape result = %q, want the deny surface (never the file contents)", result.Content)
		}
	} else if !cancelled {
		// Pre-Scenario-4 shape: no ask surfaced, so the read dead-ended on
		// ErrPathEscape and the run COMPLETED (StopEndTurn) — never a cancel.
		if result == nil {
			t.Fatal("no EvToolResult emitted for the Read call")
		}
		if !strings.Contains(result.Content, "escapes workspace root") && !strings.Contains(result.Content, "path escapes") {
			t.Fatalf("strict Read escape error = %q, want the ErrPathEscape surface", result.Content)
		}
	}
}

// TestPathEscapePosture_Scenario2_ProcEnvironNotExposed pins AC2.5: at yolo,
// Read /proc/self/environ does NOT return the raw server environment — the
// pseudo-fs hard-deny holds even at the most relaxed posture, because an
// in-process read would leak the SERVER's env (Bash reads the envscrub-
// scrubbed child env; the FS read must never be a new exfiltration channel).
func TestPathEscapePosture_Scenario2_ProcEnvironNotExposed(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("/proc is POSIX-specific")
	}
	f := setupEscapeFS(t)
	built, err := Build(context.Background(), escapeCfg(t, f, PostureYolo, readEscapeTurns("/proc/self/environ")...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	result, _ := runOneTurn(t, built, sess.ID)
	if result == nil {
		t.Fatal("no EvToolResult emitted for the Read call")
	}
	// Hard-deny: the result is an error, and it must NOT carry any
	// secret-shaped server env substring. Derive the forbidden set from the
	// process's OWN env (not a hardcoded literal) so the assertion tracks the
	// real leak surface.
	if !result.IsError {
		t.Fatalf("yolo Read /proc/self/environ SUCCEEDED — pseudo-fs must stay hard-denied at every posture (content: %.200q)", result.Content)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasSuffix(name, "_API_KEY") || strings.HasSuffix(name, "_TOKEN") || strings.HasSuffix(name, "_SECRET") {
			if strings.Contains(result.Content, kv) {
				t.Fatalf("Read /proc/self/environ leaked the server env var %q — pseudo-fs must never serve the raw server environment", name)
			}
		}
	}
}

// TestPathEscapePosture_Scenario2_NestedSymlinkEscapeRejected pins AC2.6 at
// the composition level: a symlink inside an allowed out-of-root target dir
// whose target escapes FURTHER (to a third out-of-root dir) is refused by the
// serving *os.Root — containment survives the relax, even at yolo.
func TestPathEscapePosture_Scenario2_NestedSymlinkEscapeRejected(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	f := setupEscapeFS(t)
	// A THIRD out-of-root dir the symlink escapes to, and the symlink inside
	// the (relaxed-readable) outside dir pointing at it.
	third := filepath.Join(filepath.Dir(f.outside), "third")
	if err := os.MkdirAll(third, 0o755); err != nil {
		t.Fatalf("MkdirAll(third): %v", err)
	}
	if err := os.WriteFile(filepath.Join(third, "deep.txt"), []byte("third-dir-content"), 0o644); err != nil {
		t.Fatalf("WriteFile(third): %v", err)
	}
	if err := os.Symlink(third, filepath.Join(f.outside, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	linkTarget := filepath.Join(f.outside, "link", "deep.txt")

	built, err := Build(context.Background(), escapeCfg(t, f, PostureYolo, readEscapeTurns(linkTarget)...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	result, _ := runOneTurn(t, built, sess.ID)
	if result == nil {
		t.Fatal("no EvToolResult emitted for the Read call")
	}
	if !result.IsError {
		t.Fatalf("yolo Read through the nested symlink SUCCEEDED with %q — the serving *os.Root must refuse a symlink escaping further", result.Content)
	}
	if strings.Contains(result.Content, "third-dir-content") {
		t.Fatalf("yolo Read through the nested symlink returned the third dir's content — containment lost")
	}
}

// TestPathEscapePosture_Scenario2_RestartRehydratesRelaxedWorkspace pins
// AC2.7: a session that ran relaxed and is RESTARTED rehydrates the SAME
// relaxed workspace (via Service.rehydrateSession reading the persisted
// labels), so a resumed out-of-root Read still succeeds rather than
// dead-ending on ErrPathEscape. Two-Build shape: build1 runs a relaxed read
// and persists; build2 (a fresh process over the SAME store) resumes the
// session and the relaxed read still succeeds.
func TestPathEscapePosture_Scenario2_RestartRehydratesRelaxedWorkspace(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	storeDir := t.TempDir()

	// build1: run one relaxed read so the session persists a completed state.
	cfg1 := escapeCfg(t, f, PostureYolo, readEscapeTurns(f.target)...)
	cfg1.StoreDir = storeDir
	built1, err := Build(context.Background(), cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	result1, _ := runOneTurn(t, built1, sess.ID)
	if result1 == nil || result1.IsError {
		built1.Close()
		t.Fatalf("pre-restart relaxed read failed: %+v (the session must run relaxed before restart)", result1)
	}
	built1.Close() // process death: the in-memory per-session policy is gone.

	// build2: a fresh process over the SAME store. The session persists its
	// workspace label; the run-entry seam must rehydrate the SAME relaxed
	// workspace/policy so the resumed out-of-root Read succeeds.
	cfg2 := escapeCfg(t, f, PostureYolo, readEscapeTurns(f.target)...)
	cfg2.StoreDir = storeDir
	built2, err := Build(context.Background(), cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()
	result2, _ := runOneTurn(t, built2, sess.ID)
	if result2 == nil {
		t.Fatal("no EvToolResult emitted after restart")
	}
	if result2.IsError {
		t.Fatalf("post-restart relaxed read errored: %q — rehydration must restore the SAME relaxed workspace, not dead-end on ErrPathEscape", result2.Content)
	}
	if !strings.Contains(result2.Content, f.content) {
		t.Fatalf("post-restart relaxed read content = %q, want it to contain %q", result2.Content, f.content)
	}
}
