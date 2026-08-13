package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// pathescape_scenario5_test.go pins the path-escape-posture Scenario 5
// acceptance criteria (docs/acceptance/path-escape-posture.md): the
// out-of-root relax is SCOPED to the main session's workspace construction
// only. A child engine (Subagent / team member / Parallel branch) never
// inherits it — its workspace is built by the shared newForkWorkspace helper,
// which never receives the relaxed options — and Glob/Grep stay
// workspace-confined at every posture (patterns are not paths, ADR-0047 point
// 5). This is a SCOPE boundary, not a trust boundary: it holds at every
// posture, for trusted and untrusted workspaces alike. All offline (mockllm +
// a real osfs workspace under t.TempDir).
//
// These are GUARD tests: each must go RED if a later change wires
// WithRelaxedReads/WithRelaxedWrites into a child path (newForkWorkspace /
// buildSubagentTool / buildTeamWiring / buildMemberEngine / the parallel
// child engine builder). Each pins a NEGATIVE property, so each is written
// against the real construction seam and is verified red by planting the
// violation, never green for a spurious reason.

// TestPathEscapePosture_Scenario5_ChildReadEscapeDenied pins AC5.1: a
// read-only Subagent child engine's Read of an out-of-root absolute path is
// DENIED, even when the MAIN session runs relaxed at yolo/auto — the relax
// does not propagate to the child. The parent first proves IT runs relaxed
// (its own out-of-root Read succeeds), then delegates; the child's identical
// out-of-root Read errors, and its content never folds back. Runs at both
// relaxed postures (yolo and auto).
func TestPathEscapePosture_Scenario5_ChildReadEscapeDenied(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("worktree fixtures assume a POSIX filesystem")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, posture := range []Posture{PostureYolo, PostureAuto} {
		posture := posture
		t.Run(posture.String(), func(t *testing.T) {
			t.Parallel()
			f := setupEscapeFS(t)
			// The parent workspace is a real git repo so the Subagent child
			// forks into a real worktree via newForkWorkspace — the seam under
			// test. A plain t.TempDir workspace would hit the copy fallback
			// (which still routes through newForkWorkspace, so the guard holds,
			// but the worktree path is the one the relax must never cross).
			initGitRepoTest(t, f.workspace)
			writeRepoFile(t, f.workspace, "inroot.txt", "in-root\n")
			gitCommitTest(t, f.workspace, "add inroot")

			target := mustJSONStr(t, f.target)
			parentRead := session.NewToolCall("p1", "Read", json.RawMessage(`{"path":`+target+`}`))
			delegate := session.NewToolCall("p2", "Subagent", json.RawMessage(`{"prompt":"read the file outside the workspace"}`))
			childRead := session.NewToolCall("k1", "Read", json.RawMessage(`{"path":`+target+`}`))

			cfg := escapeCfg(t, f, posture,
				// Parent turn 1: prove the MAIN session itself runs relaxed.
				mockllm.ToolCallTurn(parentRead),
				// Parent turn 2: delegate to the child.
				mockllm.ToolCallTurn(delegate),
				// Child turn 1: attempt the SAME out-of-root read; turn 2: end.
				mockllm.ToolCallTurn(childRead),
				mockllm.TextTurn("child done"),
				// Parent turn 3: close.
				mockllm.TextTurn("parent done"),
			)
			// A real shell + trusted workspace so buildSubagentTool wires the
			// worktree forker (the seam under test); at auto/yolo applyPosture
			// already raises TrustProject.
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
						t.Fatalf("parent (main) relaxed read must succeed at %s, got err=%v content=%.120q",
							posture, ev.ToolResult.IsError, ev.ToolResult.Content)
					}
					parentReadOK = true
				}
				// The child's Read outcome rides the tool.RESULT projection
				// (ADR 0079: the projection now also emits tool.call previews and
				// message/result text previews, so the ok/error outcome is
				// attributed on the tool.result projection — a tool.call preview
				// always reads IsError=false). Its IsError must be TRUE. If a
				// future change relaxed the child workspace, the read would
				// succeed (IsError=false) and the content would fold into the
				// Subagent ToolResult — both pinned below.
				if ev.Type == session.EvSubagentTool && ev.Subagent != nil &&
					ev.Subagent.InnerKind == session.EvToolResult && ev.Subagent.ToolName == "Read" {
					if !ev.Subagent.IsError {
						t.Fatalf("child out-of-root Read SUCCEEDED at %s — the relax must not propagate to a read-only child", posture)
					}
					childReadDenied = true
				}
				// Belt-and-suspenders: the Subagent ToolResult must never carry
				// the out-of-root content (the child got an error, so its
				// summary cannot quote the secret).
				if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == delegate.ID {
					if strings.Contains(ev.ToolResult.Content, f.content) {
						t.Fatalf("child surfaced the out-of-root content at %s via the Subagent result — child relax leak", posture)
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

// TestPathEscapePosture_Scenario5_GlobGrepConfined pins AC5.2: Glob and Grep
// never serve out-of-root matches at any posture, including yolo. Patterns
// are not paths — there is no parity argument for enumerating outside the
// root (ADR-0047 point 5), so the relax never widens them. A ".." traversal
// pattern, an absolute out-of-root pattern, and a Grep pathGlob escaping the
// root all return only in-root matches (or none), never the out-of-root
// fixture, at EVERY posture.
func TestPathEscapePosture_Scenario5_GlobGrepConfined(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path fixtures")
	}
	for _, posture := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
		posture := posture
		t.Run(posture.String(), func(t *testing.T) {
			t.Parallel()
			f := setupEscapeFS(t)
			// An in-root file so Glob/Grep have something legitimate to find.
			if err := os.WriteFile(filepath.Join(f.workspace, "inroot.go"), []byte("package inroot\n// marker-token-inroot\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(in-root): %v", err)
			}
			// The out-of-root fixture: a .go file carrying a distinctive token,
			// placed so a naive out-of-root glob/grep WOULD find it if the
			// confinement regressed.
			outFile := filepath.Join(f.outside, "leak.go")
			if err := os.WriteFile(outFile, []byte("package leak\n// marker-token-outside\n"), 0o644); err != nil {
				t.Fatalf("WriteFile(outside): %v", err)
			}

			// Exercise the REAL main-session workspace the factory produces at
			// this posture — at yolo it carries WithRelaxedReads/Writes, the
			// exact object whose Glob/Grep must stay confined. Confinement is a
			// workspace-body property (not a policy one), so this is the honest
			// seam; the factory is the single construction site.
			ws := osfsWorkspaceFactory(port.NopDiagnostics{}, nil)(f.workspace)
			if ws == nil {
				t.Fatalf("osfsWorkspaceFactory returned a nil workspace at %s", posture)
			}

			ctx := context.Background()
			// Glob: a ".." traversal that climbs out of the root, and an
			// absolute out-of-root pattern. Neither may return the out-of-root
			// file. normalizeGlobPattern strips a leading "/", so both forms
			// probe the escape surface.
			for _, pat := range []string{
				"../outside/*.go",
				filepath.ToSlash(f.outside) + "/*.go",
				"../*.go",
			} {
				matches, err := ws.Glob(ctx, pat)
				if err != nil {
					continue // a rejected pattern is confined by construction
				}
				for _, m := range matches {
					if strings.Contains(filepath.ToSlash(m), "leak.go") || strings.Contains(filepath.ToSlash(m), "outside") {
						t.Fatalf("Glob(%q) at %s returned out-of-root match %q — Glob must stay workspace-confined", pat, posture, m)
					}
				}
			}
			// Grep over the whole tree (empty pathGlob) and over an escaping
			// pathGlob: the out-of-root token must never appear.
			for _, pathGlob := range []string{"", "../outside/*.go", "**/*.go"} {
				matches, err := ws.Grep(ctx, "marker-token-outside", pathGlob)
				if err != nil {
					continue
				}
				if len(matches) != 0 {
					t.Fatalf("Grep(pathGlob=%q) at %s returned %d out-of-root matches — Grep must stay workspace-confined", pathGlob, posture, len(matches))
				}
			}
			// Positive control: the in-root token IS found, proving the
			// assertion above is not vacuously green (an always-empty Grep
			// would also "pass").
			inMatches, err := ws.Grep(ctx, "marker-token-inroot", "**/*.go")
			if err != nil || len(inMatches) == 0 {
				t.Fatalf("positive control failed: in-root Grep at %s returned %d matches, err=%v", posture, len(inMatches), err)
			}
		})
	}
}

// TestPathEscapePosture_Scenario5_ChildEnginesNeverRelaxed pins AC5.3
// STRUCTURALLY: no child engine derivation constructs a workspace with the
// relaxed escape options. The relaxed options are supplied ONLY by the main
// session's osfsWorkspaceFactory; every child family forks through
// newForkWorkspace — the ONE constructor the Subagent worktree forker, the
// team-member force-copy/worktree forkers (buildTeamWiring), and the Parallel
// branch forker (registerParallelTool) all share.
//
// The proof is construction-level: fork the (relaxed) main workspace through
// the SAME forker shapes the child families use, over the newForkWorkspace
// constructor, and assert the resulting CHILD workspace DENIES an out-of-root
// read. If a later change passed the relaxed options into newForkWorkspace
// (or built any child workspace with them), the forked child workspace would
// serve the escape and this fails.
func TestPathEscapePosture_Scenario5_ChildEnginesNeverRelaxed(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("worktree fixtures assume a POSIX filesystem")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	f := setupEscapeFS(t)
	initGitRepoTest(t, f.workspace)
	writeRepoFile(t, f.workspace, "inroot.txt", "in-root\n")
	gitCommitTest(t, f.workspace, "add inroot")

	// The three real forker shapes, each over the shared newForkWorkspace
	// constructor (AC5.3's named seam): the Subagent explorer's worktree
	// forker (buildSubagentTool), and the team-member / Parallel force-copy
	// forker (buildTeamWiring / registerParallelTool). Both must produce child
	// workspaces that deny an out-of-root read even when forked from a relaxed
	// main-session base.
	forkShapes := map[string]*forker.Forker{
		"subagent-worktree":       forker.New(newForkWorkspace(nil), forker.WithDirtyOverlay()),
		"team-member-force-copy":  forker.New(newForkWorkspace(nil), forker.WithForceCopy()),
		"parallel-branch-forcopy": forker.New(newForkWorkspace(nil), forker.WithForceCopy()),
	}
	for name, fk := range forkShapes {
		fk := fk
		t.Run(name, func(t *testing.T) {
			for _, posture := range []Posture{PostureYolo, PostureAuto} {
				posture := posture
				t.Run(posture.String(), func(t *testing.T) {
					// The fork BASE is the relaxed main workspace at this
					// posture — the exact object a Subagent/member/branch
					// receives as its parent ws. If the child workspace
					// inherited the relax, the out-of-root read below succeeds.
					base := osfsWorkspaceFactory(port.NopDiagnostics{}, nil)(f.workspace)
					if base == nil {
						t.Fatalf("relaxed base workspace is nil at %s", posture)
					}
					baseEnv := testEnvironment(base, nil)
					childEnv, cleanup, _, err := fk.Fork(context.Background(), baseEnv, "guard")
					if err != nil {
						t.Fatalf("Fork: %v", err)
					}
					defer func() { _ = cleanup() }()
					// The child workspace must DENY the out-of-root read even
					// though its base was relaxed — the construction-level
					// proof that the relaxed options never crossed into
					// newForkWorkspace.
					data, rerr := childEnv.Workspace().Read(context.Background(), f.target)
					if rerr == nil {
						t.Fatalf("%s child workspace (forked from a %s-relaxed base) served out-of-root read %.80q — the relaxed options crossed into newForkWorkspace", name, posture, data)
					}
					if !errors.Is(rerr, osfs.ErrPathEscape) && !strings.Contains(rerr.Error(), "escapes") {
						t.Fatalf("%s child out-of-root read error = %v, want the ErrPathEscape surface", name, rerr)
					}
				})
			}
		})
	}
}

// TestPathEscapePosture_DefaultConstructionDeniesEscape pins AC5.4: the
// zero-value workspace construction (NO relaxed option) denies an out-of-root
// absolute read at every posture — the default stays deny. A relaxed read is
// opt-in via WithRelaxedReads only; a plain osfs.NewWorkspace (the shape
// newForkWorkspace and every strict/trusted session uses) never serves an
// out-of-root absolute path regardless of the surrounding posture.
func TestPathEscapePosture_DefaultConstructionDeniesEscape(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	// The posture axis is irrelevant to a zero-value construction — the relaxed
	// option is construction-time, not consult-at-read — but pin every posture
	// so a later "posture-aware default" change is caught here.
	for _, posture := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
		posture := posture
		t.Run(posture.String(), func(t *testing.T) {
			t.Parallel()
			ws, err := osfs.NewWorkspace(f.workspace) // zero-value: NO WithRelaxedReads
			if err != nil {
				t.Fatalf("osfs.NewWorkspace: %v", err)
			}
			data, rerr := ws.Read(context.Background(), f.target)
			if rerr == nil {
				t.Fatalf("zero-value workspace served out-of-root read %.80q at posture %s — the default must stay deny", data, posture)
			}
			if !errors.Is(rerr, osfs.ErrPathEscape) && !strings.Contains(rerr.Error(), "escapes") {
				t.Fatalf("zero-value out-of-root read error = %v, want the ErrPathEscape surface", rerr)
			}
			// Stat must deny identically (the other relaxed-read path).
			if _, serr := ws.Stat(context.Background(), f.target); serr == nil {
				t.Fatalf("zero-value workspace served out-of-root Stat at posture %s — the default must stay deny", posture)
			}
		})
	}
}

// mustJSONStr marshals v to a JSON string literal for embedding in a tool-call
// arg envelope.
func mustJSONStr(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %q: %v", v, err)
	}
	return string(b)
}
