package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
)

func TestYoloRelativeEscapeAuthorityUsesPhysicalTarget(t *testing.T) {
	f := setupEscapeFS(t)
	neighbor := filepath.Join(f.outside, "neighbor.txt")
	if err := os.WriteFile(neighbor, []byte("allowed-neighbor"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(t.TempDir(), "authority.cedar")
	policy := `permit(principal, action, resource);
forbid(principal, action, resource) when { resource.path like "` + filepath.ToSlash(f.target) + `" };`
	if err := os.WriteFile(policyPath, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := escapeCfg(t, f, PostureYolo,
		mockllm.ToolCallTurn(scenario3Call("blocked-read", "Read", map[string]string{"path": "../outside/secret.txt"})),
		mockllm.ToolCallTurn(scenario3Call("allowed-read", "Read", map[string]string{"path": "../outside/neighbor.txt"})),
		mockllm.ToolCallTurn(scenario3Call("blocked-write", "Write", map[string]string{"path": "../outside/secret.txt", "content": "bad"})),
		mockllm.TextTurn("done"))
	cfg.AuthorityEvaluator = "cedar"
	cfg.CedarAuthorityPolicy = policyPath
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "inspect and edit sibling")
	if err != nil {
		t.Fatal(err)
	}
	results := make(map[string]*session.ToolResult)
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			t.Error("unexpected approval request")
			run.Approve(ev.Ask.AskID, session.VerdictDeny)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results[string(ev.ToolResult.CallID)] = ev.ToolResult
		}
	}
	built.Service.FinishRun(sess.ID, run)
	for _, id := range []string{"blocked-read", "blocked-write"} {
		result := results[id]
		if result == nil || !result.IsError || !strings.Contains(result.Content, "denied by authority") {
			t.Errorf("%s = %+v, want authority denial", id, result)
		}
	}
	if result := results["allowed-read"]; result == nil || result.IsError || !strings.Contains(result.Content, "allowed-neighbor") {
		t.Errorf("allowed neighbor = %+v", result)
	}
	if data, err := os.ReadFile(f.target); err != nil || string(data) != f.content {
		t.Errorf("denied write modified target: %q, %v", data, err)
	}
}
