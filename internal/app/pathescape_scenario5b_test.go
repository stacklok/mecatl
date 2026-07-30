package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
)

// pathescape_scenario5b_test.go pins AC5.1b/AC5.1c
// (docs/acceptance/path-escape-posture.md Scenario 5): the base-SHARING
// (nil-forker) read-only Subagent child — wired whenever Bash is disabled
// (--no-bash / an empty shell / the issue-#40 untrusted-workspace gate nils the
// sandboxed runner) — must NOT inherit the main session's relaxed workspace.
// Before the fix the child ran against the parent's escapeWorkspace verbatim
// (forkChildWorkspace returns the parent ws unchanged for a nil forker), so a
// shell-less child silently gained the main session's out-of-root reach. The
// fix gives the base-sharing child a NON-relaxed view over the same root.

// TestPathEscapePosture_Scenario5_SharedWorkspaceChildNotRelaxed pins AC5.1b:
// a SHELL-LESS (nil-forker) read-only child running against the parent's
// relaxed base workspace has its out-of-root Read/Write DENIED — it does not
// inherit the relax. The parent first proves IT runs relaxed (its own
// out-of-root Read succeeds), then delegates; the child's identical
// out-of-root Read errors (osfs ErrPathEscape), and a writable direct-write
// child's out-of-root Write likewise never lands. Runs at both relaxed
// postures (yolo and auto), each with both shell-less triggers (an explicit
// --no-bash and the untrusted-workspace gate).
func TestPathEscapePosture_Scenario5_SharedWorkspaceChildNotRelaxed(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path fixtures")
	}
	triggers := map[string]func(cfg *Config){
		"no-bash":   func(cfg *Config) { cfg.NoBash = true },
		"untrusted": func(cfg *Config) { cfg.TrustProject = false },
	}
	for _, posture := range []Posture{PostureYolo, PostureAuto} {
		posture := posture
		for trigger, apply := range triggers {
			trigger, apply := trigger, apply
			t.Run(posture.String()+"/"+trigger, func(t *testing.T) {
				t.Parallel()
				f := setupEscapeFS(t)
				// NO git init: the whole point is the nil-forker path — with no
				// sandboxed runner buildSubagentTool wires no forker, so the child
				// runs against the parent's (relaxed) base workspace verbatim.
				target := mustJSONStr(t, f.target)
				parentRead := session.NewToolCall("p1", "Read", json.RawMessage(`{"path":`+target+`}`))
				delegate := session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"read the file outside the workspace"}`))
				childRead := session.NewToolCall("k1", "Read", json.RawMessage(`{"path":`+target+`}`))

				cfg := escapeCfg(t, f, posture,
					// Parent turn 1: prove the MAIN session itself runs relaxed.
					mockllm.ToolCallTurn(parentRead),
					// Parent turn 2: delegate to the shell-less child.
					mockllm.ToolCallTurn(delegate),
					// Child turn 1: attempt the SAME out-of-root read; turn 2: end.
					mockllm.ToolCallTurn(childRead),
					mockllm.TextTurn("child done"),
					// Parent turn 3: close.
					mockllm.TextTurn("parent done"),
				)
				apply(&cfg)
				built, err := Build(context.Background(), cfg)
				if err != nil {
					t.Fatalf("Build: %v", err)
				}
				defer built.Close()
				sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
				if err != nil {
					t.Fatalf("CreateSession: %v", err)
				}

				var parentReadOK, childReadDenied bool
				run, err := built.Service.StartRun(context.Background(), sess.ID, "read outside then delegate")
				if err != nil {
					t.Fatalf("StartRun: %v", err)
				}
				for ev := range run.Events() {
					// The parent's own out-of-root Read (call p1) must SUCCEED —
					// this proves the MAIN session genuinely runs relaxed, so the
					// child denial below is a real propagation boundary, not a
					// vacuous "the relax was never on".
					if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == parentRead.ID {
						if ev.ToolResult.IsError || !strings.Contains(ev.ToolResult.Content, f.content) {
							t.Fatalf("parent (main) relaxed read must succeed at %s/%s, got err=%v content=%.120q",
								posture, trigger, ev.ToolResult.IsError, ev.ToolResult.Content)
						}
						parentReadOK = true
					}
					// The child's Read outcome rides the tool.RESULT projection
					// (ADR 0079: the projection now also emits tool.call previews and
					// message/result text previews, so the ok/error outcome is
					// attributed on the tool.result projection — a tool.call preview
					// always reads IsError=false). Its IsError must be TRUE. If the
					// child inherited the relaxed base workspace the read would
					// succeed (IsError=false) and the content would fold into the
					// Subagent ToolResult — both pinned below.
					if ev.Type == session.EvSubagentTool && ev.Subagent != nil &&
						ev.Subagent.InnerKind == session.EvToolResult && ev.Subagent.ToolName == "Read" {
						if !ev.Subagent.IsError {
							t.Fatalf("shell-less child out-of-root Read SUCCEEDED at %s/%s — the base-sharing child must not inherit the relaxed parent workspace", posture, trigger)
						}
						childReadDenied = true
					}
					// Belt-and-suspenders: the Subagent ToolResult must never carry
					// the out-of-root content (the child got an error, so its
					// summary cannot quote the secret).
					if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == delegate.ID {
						if strings.Contains(ev.ToolResult.Content, f.content) {
							t.Fatalf("shell-less child surfaced the out-of-root content at %s/%s via the Subagent result — child relax leak", posture, trigger)
						}
					}
				}
				built.Service.FinishRun(sess.ID, run)

				if !parentReadOK {
					t.Fatal("no EvToolResult for the parent's relaxed Read (call p1)")
				}
				if !childReadDenied {
					t.Fatal("no subagent.tool Read event for the child — the delegation did not run the child's read")
				}
			})
		}
	}
}

// TestPathEscapePosture_Scenario5_SharedWorkspaceChildWriteDenied pins the
// WRITE half of AC5.1b: a writable (mode:"read-write", direct-write, ADR 0041)
// child — the OTHER base-sharing child path, which runs against the parent
// workspace verbatim BY DESIGN — must NOT inherit the relaxed-WRITE reach
// either. Its out-of-root Write is denied (the file never appears), while the
// main session's own out-of-root Write still lands at yolo (the main session's
// relaxed behaviour is unchanged). The child is given Bash (the forker-wired
// shape) so validateMode accepts read-write, but Bash is never scripted.
func TestPathEscapePosture_Scenario5_SharedWorkspaceChildWriteDenied(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path fixtures")
	}
	f := setupEscapeFS(t)
	writeTarget := filepath.Join(f.outside, "child-write.txt")
	parentWrite := session.NewToolCall("p1", "Write", json.RawMessage(`{"path":`+mustJSONStr(t, writeTarget)+`,"content":"parent relaxed write\n"}`))
	delegate := session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"write the file outside the workspace","mode":"read-write"}`))
	childWrite := session.NewToolCall("k1", "Write", json.RawMessage(`{"path":`+mustJSONStr(t, writeTarget)+`,"content":"child escaped write\n"}`))

	cfg := escapeCfg(t, f, PostureYolo,
		// Parent turn 1: prove the MAIN session itself runs relaxed-WRITE.
		mockllm.ToolCallTurn(parentWrite),
		// Parent turn 2: delegate a WRITABLE child (direct-write, base-sharing).
		mockllm.ToolCallTurn(delegate),
		// Child turn 1: attempt the SAME out-of-root write; turn 2: end.
		mockllm.ToolCallTurn(childWrite),
		mockllm.TextTurn("child done"),
		// Parent turn 3: close.
		mockllm.TextTurn("parent done"),
	)
	// A real shell so the writable path is wired (validateMode rejects
	// read-write when no writable engine is wired); Bash is never scripted.
	cfg.Shell = "/bin/sh"
	built, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var parentWriteOK, childWriteDenied bool
	run, err := built.Service.StartRun(context.Background(), sess.ID, "write outside then delegate writable")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for ev := range run.Events() {
		// The parent's own out-of-root Write (call p1) must SUCCEED at yolo —
		// the main session's relaxed-write behaviour is unchanged.
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == parentWrite.ID {
			if ev.ToolResult.IsError {
				t.Fatalf("parent (main) relaxed write must succeed at yolo, got err content=%.120q", ev.ToolResult.Content)
			}
			parentWriteOK = true
		}
		// The writable child's out-of-root Write must ERROR — the direct-write
		// child shares the parent's base but must not inherit the relaxed write.
		// (ADR 0079: the outcome is attributed on the tool.RESULT projection.)
		if ev.Type == session.EvSubagentTool && ev.Subagent != nil &&
			ev.Subagent.InnerKind == session.EvToolResult && ev.Subagent.ToolName == "Write" {
			if !ev.Subagent.IsError {
				t.Fatal("writable child out-of-root Write SUCCEEDED at yolo — the direct-write child must not inherit the relaxed parent workspace")
			}
			childWriteDenied = true
		}
	}
	built.Service.FinishRun(sess.ID, run)

	if !parentWriteOK {
		t.Fatal("no EvToolResult for the parent's relaxed Write (call p1)")
	}
	if !childWriteDenied {
		t.Fatal("no subagent.tool Write event for the writable child — the delegation did not run the child's write")
	}
	// The child's write must never have landed: the only content at the target
	// is the parent's own (which wrote first and succeeded).
	data, rerr := os.ReadFile(writeTarget)
	if rerr != nil {
		t.Fatalf("ReadFile(parent-written target): %v", rerr)
	}
	if strings.Contains(string(data), "child escaped write") {
		t.Fatalf("the writable child's out-of-root write LANDED at %q — child relax leak", writeTarget)
	}
}
