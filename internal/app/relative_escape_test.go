package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

func TestYoloRelativeEscapeTools(t *testing.T) {
	f := setupEscapeFS(t)
	rel := "../outside/secret.txt"
	created := "../outside/new.txt"
	calls := []session.ToolCall{
		scenario3Call("read", "Read", map[string]string{"path": rel}),
		scenario3Call("list", "ListDir", map[string]string{"path": "../outside"}),
		scenario3Call("write", "Write", map[string]string{"path": created, "content": "new"}),
		scenario3Call("edit", "Edit", map[string]string{"path": rel, "old_string": f.content, "new_string": "edited"}),
		scenario3Call("alias-edit", "Edit", map[string]string{"path": f.target, "old_string": "edited", "new_string": "edited-again"}),
	}
	turns := make([]mockllm.Turn, 0, len(calls)+1)
	for _, call := range calls {
		turns = append(turns, mockllm.ToolCallTurn(call))
	}
	turns = append(turns, mockllm.TextTurn("done"))
	requests := make(chan port.LLMRequest, len(turns))
	cfg := escapeCfg(t, f, PostureYolo, turns...)
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests <- req })}, turns...)
	}
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "use relative filesystem paths")
	if err != nil {
		t.Fatal(err)
	}
	var results []*session.ToolResult
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			t.Error("yolo relative access asked for approval")
			run.Approve(ev.Ask.AskID, session.VerdictDeny)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results = append(results, ev.ToolResult)
		}
	}
	built.Service.FinishRun(sess.ID, run)
	select {
	case req := <-requests:
		for _, name := range []string{"Read", "ListDir", "Write", "Edit"} {
			found := false
			for _, spec := range req.Tools {
				if spec.Name == name {
					found = strings.Contains(spec.Description, "../") && strings.Contains(spec.Description, "yolo")
				}
			}
			if !found {
				t.Errorf("%s: model request lacks relative escape guidance", name)
			}
		}
	default:
		t.Error("no model request observed")
	}
	if len(results) != len(calls) {
		t.Fatalf("results = %d, want %d", len(results), len(calls))
	}
	for i, result := range results {
		if result.IsError {
			t.Errorf("%s: %s", calls[i].Name, result.Content)
		}
	}
	if !strings.Contains(results[0].Content, f.content) || !strings.Contains(results[1].Content, "secret.txt") {
		t.Fatalf("read/list results: %+v", results[:2])
	}
	for path, want := range map[string]string{f.target: "edited-again", filepath.Join(f.outside, "new.txt"): "new"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Errorf("%s: %q, %v", path, data, err)
		}
	}
}

func TestRelativeEscapeOtherPosturesStayConfined(t *testing.T) {
	for _, posture := range []Posture{PostureAuto, PostureTrusted, PostureStrict} {
		t.Run(posture.String(), func(t *testing.T) {
			f := setupEscapeFS(t)
			built, err := buildIsolated(t, context.Background(), escapeCfg(t, f, posture, readEscapeTurns("../outside/secret.txt")...))
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(context.Background(), sess.ID, "read relative escape")
			if err != nil {
				t.Fatal(err)
			}
			var result *session.ToolResult
			for ev := range run.Events() {
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
					run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
				}
				if ev.Type == session.EvToolResult {
					result = ev.ToolResult
				}
			}
			built.Service.FinishRun(sess.ID, run)
			if result == nil || !result.IsError || !strings.Contains(result.Content, "escape") {
				t.Fatalf("relative escape = %+v", result)
			}
		})
	}
}

func TestYoloRelativeEscapeConfiguredDeny(t *testing.T) {
	f := setupEscapeFS(t)
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policy, []byte("permissions:\n  deny:\n    - 'Read'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := escapeCfg(t, f, PostureYolo, readEscapeTurns("../outside/secret.txt")...)
	cfg.PermissionConfigs = []string{policy}
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := runOneTurn(t, built, sess.ID)
	if result == nil || !result.IsError || strings.Contains(result.Content, f.content) {
		t.Fatalf("configured deny result: %+v", result)
	}
}

func TestYoloRelativeEscapeConfiguredAsk(t *testing.T) {
	f := setupEscapeFS(t)
	policy := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policy, []byte("permissions:\n  ask:\n    - 'Read'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := escapeCfg(t, f, PostureYolo, readEscapeTurns("../outside/secret.txt")...)
	cfg.PermissionConfigs = []string{policy}
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "read relative escape")
	if err != nil {
		t.Fatal(err)
	}
	var result *session.ToolResult
	asked := false
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			asked = true
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvToolResult {
			result = ev.ToolResult
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if !asked || result == nil || result.IsError || !strings.Contains(result.Content, f.content) {
		t.Fatalf("configured ask = %v, result = %+v", asked, result)
	}
}

func TestYoloRelativeEscapeServiceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(t *testing.T, f escapeFixture) string
	}{
		{"pseudo-fs", func(t *testing.T, f escapeFixture) string {
			if runtime.GOOS != "linux" {
				t.Skip("Linux pseudo-fs fixture")
			}
			path, err := filepath.Rel(f.workspace, "/proc/uptime")
			if err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"nested symlink", func(t *testing.T, f escapeFixture) string {
			if err := os.Symlink(f.workspace, filepath.Join(f.outside, "link")); err != nil {
				t.Fatal(err)
			}
			return "../outside/link/../secret.txt"
		}},
		{"in-root alias", func(t *testing.T, f escapeFixture) string {
			if err := os.Symlink(f.outside, filepath.Join(f.workspace, "alias")); err != nil {
				t.Fatal(err)
			}
			return "alias/secret.txt"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupEscapeFS(t)
			path := tc.path(t, f)
			built, err := buildIsolated(t, context.Background(), escapeCfg(t, f, PostureYolo, readEscapeTurns(path)...))
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			result, _ := runOneTurn(t, built, sess.ID)
			if result == nil || !result.IsError || strings.Contains(result.Content, f.content) {
				t.Fatalf("refused path %q: %+v", path, result)
			}
			if tc.name == "pseudo-fs" && !strings.Contains(result.Content, "pseudo-filesystem") {
				t.Fatalf("relative pseudo-fs denial = %+v, want hard-deny reason", result)
			}
		})
	}
}

func TestYoloRelativeEscapeBoundaries(t *testing.T) {
	f := setupEscapeFS(t)
	ws := osfsWorkspaceFactory(port.NopDiagnostics{}, PostureYolo)(f.workspace)
	if ws == nil {
		t.Fatal("missing workspace")
	}
	child := childWorkspaceView(ws)
	if err := os.WriteFile(filepath.Join(f.workspace, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ws.Read(t.Context(), "../workspace/inside.txt"); err != nil || string(data) != "inside" {
		t.Fatalf("relative re-entry: %q, %v", data, err)
	}
	if _, err := child.Read(t.Context(), "../workspace/inside.txt"); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("child relative re-entry: %v", err)
	}
	if _, err := child.Read(t.Context(), "../outside/secret.txt"); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("child relative read: %v", err)
	}
	if _, err := child.CreateFile(t.Context(), "../outside/new.txt", []byte("bad")); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("child relative write: %v", err)
	}
	if runtime.GOOS == "linux" {
		path, err := filepath.Rel(f.workspace, "/proc/self/environ")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ws.Read(t.Context(), path); !errors.Is(err, osfs.ErrPathEscape) {
			t.Fatalf("pseudo-fs relative read: %v", err)
		}
	}
	if err := os.Symlink(f.outside, filepath.Join(f.workspace, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Read(t.Context(), "alias/secret.txt"); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("in-root symlink read: %v", err)
	}
	if err := os.Symlink(f.workspace, filepath.Join(f.outside, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Read(t.Context(), "../outside/link/../secret.txt"); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("nested symlink traversal: %v", err)
	}
	if _, err := ws.CreateFile(t.Context(), "../outside/link/../new.txt", []byte("bad")); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("nested symlink write: %v", err)
	}
	if _, _, err := ws.(interface {
		AuthorityResourcePath(string) (string, string, error)
	}).AuthorityResourcePath("../outside/link/../secret.txt"); !errors.Is(err, osfs.ErrPathEscape) {
		t.Fatalf("nested symlink authority: %v", err)
	}
}
