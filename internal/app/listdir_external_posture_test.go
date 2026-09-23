package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

func listDirEscapeTurns(target string) []mockllm.Turn {
	args, _ := json.Marshal(map[string]string{"path": target})
	return []mockllm.Turn{
		mockllm.ToolCallTurn(session.NewToolCall("list-escape", "ListDir", args)),
		mockllm.TextTurn("done"),
	}
}

type listDirEscapeOutcome struct {
	ask    *session.PendingAsk
	result *session.ToolResult
}

func runListDirEscape(t *testing.T, built *Built, id session.SessionID, verdict session.ApprovalVerdict) listDirEscapeOutcome {
	t.Helper()
	run, err := built.Service.StartRun(context.Background(), id, "list the external directory")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var out listDirEscapeOutcome
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			ask := *ev.Ask
			out.ask = &ask
			run.Approve(ev.Ask.AskID, verdict)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "list-escape" {
			result := *ev.ToolResult
			out.result = &result
		}
	}
	built.Service.FinishRun(id, run)
	return out
}

func TestListDirExternalAbsolutePathFollowsMainPosture(t *testing.T) {
	for _, tc := range []struct {
		name        string
		posture     Posture
		verdict     session.ApprovalVerdict
		wantAsk     bool
		wantSuccess bool
	}{
		{name: "yolo allows", posture: PostureYolo, verdict: session.VerdictAllowOnce, wantSuccess: true},
		{name: "auto allows", posture: PostureAuto, verdict: session.VerdictAllowOnce, wantSuccess: true},
		{name: "trusted asks and approval executes", posture: PostureTrusted, verdict: session.VerdictAllowOnce, wantAsk: true, wantSuccess: true},
		{name: "trusted asks and denial refuses", posture: PostureTrusted, verdict: session.VerdictDeny, wantAsk: true},
		{name: "strict asks and approval executes", posture: PostureStrict, verdict: session.VerdictAllowOnce, wantAsk: true, wantSuccess: true},
		{name: "strict asks and denial refuses", posture: PostureStrict, verdict: session.VerdictDeny, wantAsk: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := setupEscapeFS(t)
			cfg := escapeCfg(t, f, tc.posture, listDirEscapeTurns(f.outside)...)
			built, err := buildIsolated(t, context.Background(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			out := runListDirEscape(t, built, sess.ID, tc.verdict)
			if (out.ask != nil) != tc.wantAsk {
				t.Fatalf("ask present = %v, want %v", out.ask != nil, tc.wantAsk)
			}
			if out.ask != nil && (out.ask.Tool != "ListDir" || !strings.Contains(out.ask.Reason, f.outside)) {
				t.Fatalf("ask = %+v, want ListDir reason naming %q", out.ask, f.outside)
			}
			if out.result == nil {
				t.Fatal("missing ListDir tool result")
			}
			if tc.wantSuccess {
				if out.result.IsError || !strings.Contains(out.result.Content, "secret.txt") {
					t.Fatalf("ListDir result = %+v, want external directory entry", out.result)
				}
			} else if !out.result.IsError || strings.Contains(out.result.Content, "secret.txt") {
				t.Fatalf("denied ListDir result = %+v, want error without external entry", out.result)
			}
		})
	}
}

func TestListDirExternalConfiguredRulesSurviveYolo(t *testing.T) {
	f := setupEscapeFS(t)
	ws, err := osfs.NewWorkspace(f.workspace)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	args, _ := json.Marshal(map[string]string{"path": f.outside})
	call := session.NewToolCall("list", "ListDir", args)
	for _, tc := range []struct {
		name   string
		effect governance.Effect
	}{
		{name: "ask", effect: governance.Ask},
		{name: "deny", effect: governance.Deny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := permpolicy.NewPolicy([]governance.Rule{{Scope: governance.ScopeUser, Tool: "ListDir", Effect: tc.effect}}, nil)
			decision := newEscapePolicy(inner, PostureYolo).Evaluate(context.Background(), "s1", session.ModeDefault, call, ws)
			if decision.Effect != tc.effect {
				t.Fatalf("configured %s became %s at yolo", tc.effect, decision.Effect)
			}
			if tc.effect == governance.Ask && decision.AskProvenance != governance.AskProvenanceConfigured {
				t.Fatal("configured ListDir ask lost ConfiguredAsk provenance")
			}
		})
	}
}

func TestBuiltListDirExternalConfiguredRulesSurviveYolo(t *testing.T) {
	for _, effect := range []string{"ask", "deny"} {
		t.Run(effect, func(t *testing.T) {
			t.Parallel()
			f := setupEscapeFS(t)
			settings := filepath.Join(t.TempDir(), "settings.yaml")
			body := "permissions:\n  " + effect + ":\n    - ListDir\n"
			if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
				t.Fatalf("write permission config: %v", err)
			}
			cfg := escapeCfg(t, f, PostureYolo, listDirEscapeTurns(f.outside)...)
			cfg.PermissionConfigs = []string{settings}
			built, err := buildIsolated(t, context.Background(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			out := runListDirEscape(t, built, sess.ID, session.VerdictDeny)
			if (out.ask != nil) != (effect == "ask") {
				t.Fatalf("configured %s ask present = %v", effect, out.ask != nil)
			}
			if out.result == nil || !out.result.IsError || strings.Contains(out.result.Content, "secret.txt") {
				t.Fatalf("configured %s ListDir result = %+v", effect, out.result)
			}
		})
	}
}

func TestListDirPseudoFSAliasApprovedAskStillRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pseudo-fs aliases require a POSIX filesystem")
	}
	if _, err := os.Stat("/dev"); os.IsNotExist(err) {
		t.Skip("/dev is absent on this host")
	} else if err != nil {
		t.Fatal(err)
	}
	f := setupEscapeFS(t)
	alias := filepath.Join(f.workspace, "pseudolink")
	if err := os.Symlink("/dev", alias); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settings, []byte("permissions:\n  ask:\n    - ListDir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := escapeCfg(t, f, PostureYolo, listDirEscapeTurns(alias)...)
	cfg.PermissionConfigs = []string{settings}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	out := runListDirEscape(t, built, sess.ID, session.VerdictAllowOnce)
	if out.ask == nil || out.ask.AskProvenance != governance.AskProvenanceConfigured {
		t.Fatalf("ask = %+v, want configured ListDir ask before approval", out.ask)
	}
	if out.result == nil || !out.result.IsError || !strings.Contains(out.result.Content, "pseudo-filesystem (/proc, /sys, /dev) is never served") {
		t.Fatalf("approved ListDir result = %+v, want workspace pseudo-fs refusal", out.result)
	}
}

func TestListDirExternalAuthorityDenialStillWins(t *testing.T) {
	f := setupEscapeFS(t)
	policyPath := filepath.Join(t.TempDir(), "authority.cedar")
	if err := os.WriteFile(policyPath, []byte("permit(principal, action, resource);\nforbid(principal, action, resource);\n"), 0o600); err != nil {
		t.Fatalf("write Cedar policy: %v", err)
	}
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "owner", GrantType: session.GrantTypeUser})
	cfg := escapeCfg(t, f, PostureYolo, listDirEscapeTurns(f.outside)...)
	cfg.StoreDir = filepath.Join(t.TempDir(), "sessions")
	cfg.AuthorityEvaluator = "cedar"
	cfg.CedarAuthorityPolicy = policyPath
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "list the external directory")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var result *session.ToolResult
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "list-escape" {
			got := *ev.ToolResult
			result = &got
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if result == nil || !result.IsError || !strings.Contains(result.Content, "denied by authority") || strings.Contains(result.Content, "secret.txt") {
		t.Fatalf("authority-denied ListDir result = %+v", result)
	}
}

func TestBuiltModelAdvertisesExternalAbsoluteListDir(t *testing.T) {
	f := setupEscapeFS(t)
	var mu sync.Mutex
	var description, schema string
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		mu.Lock()
		defer mu.Unlock()
		for _, spec := range req.Tools {
			if spec.Name == "ListDir" {
				description = spec.Description
				schema = string(spec.Schema)
			}
		}
	})}, mockllm.TextTurn("done"))
	cfg := escapeCfg(t, f, PostureYolo)
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider { return provider }
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "inspect a directory")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(description, "absolute") || !strings.Contains(description, "policy-authorized") {
		t.Fatalf("built ListDir description = %q, want policy-authorized absolute-path affordance", description)
	}
	if !strings.Contains(schema, "absolute") {
		t.Fatalf("built ListDir schema = %q, want absolute-path affordance", schema)
	}
}
