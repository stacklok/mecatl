package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// TestValidateSkillDraftConfig pins the STRUCTURAL trust boundary: the quarantine
// must live outside the workspace root (so the model's workspace-confined Write/Edit
// cannot reach it) and be disjoint from every active skills dir. The previous
// approach (governance deny rules on raw model paths) was removed: it matched
// absolute deny patterns against the workspace-RELATIVE paths the tools actually
// use, so it never fired — a false boundary. Structural confinement replaces it.
func TestValidateSkillDraftConfig(t *testing.T) {
	t.Run("disabled yields no error", func(t *testing.T) {
		if err := validateSkillDraftConfig(Config{Workspace: t.TempDir()}); err != nil {
			t.Fatalf("disabled draft must not error: %v", err)
		}
	})

	t.Run("quarantine inside the workspace is fatal", func(t *testing.T) {
		ws := t.TempDir()
		cfg := Config{Workspace: ws, SkillsDraftDir: filepath.Join(ws, "quarantine")}
		if err := validateSkillDraftConfig(cfg); err == nil {
			t.Fatal("expected a fatal error when the quarantine is inside the workspace")
		}
	})

	t.Run("quarantine overlapping an active skills dir is fatal", func(t *testing.T) {
		base := t.TempDir()
		ws := filepath.Join(base, "ws")
		shared := filepath.Join(base, "shared")
		cfg := Config{
			Workspace:      ws,
			SkillsDirs:     []string{shared},
			SkillsDraftDir: filepath.Join(shared, "quarantine"), // outside ws, but inside an active dir
		}
		if err := validateSkillDraftConfig(cfg); err == nil {
			t.Fatal("expected a fatal error when the quarantine overlaps an active skills dir")
		}
	})

	t.Run("outside-workspace, disjoint quarantine is accepted", func(t *testing.T) {
		base := t.TempDir()
		cfg := Config{
			Workspace:      filepath.Join(base, "ws"),
			SkillsDirs:     []string{filepath.Join(base, "active")},
			SkillsDraftDir: filepath.Join(base, "quarantine"),
		}
		if err := validateSkillDraftConfig(cfg); err != nil {
			t.Fatalf("a valid out-of-workspace, disjoint quarantine must be accepted: %v", err)
		}
	})
}

// TestValidateSkillDraftConfigSymlinkedWorkspace pins the fix for the symlink
// divergence: validation must canonicalize the workspace the SAME way the osfs
// Workspace does (abs + EvalSymlinks), not with filepath.Abs alone. Here the
// workspace is a symlink whose target is an ANCESTOR of the quarantine — with plain
// Abs the two look disjoint (false PASS) and the model's Write/Edit could reach the
// quarantine through the resolved os.Root; with EvalSymlinks the quarantine is
// correctly seen as inside the workspace and rejected.
func TestValidateSkillDraftConfigSymlinkedWorkspace(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	quar := filepath.Join(realDir, "quar")
	if err := os.MkdirAll(quar, 0o755); err != nil {
		t.Fatal(err)
	}
	wslink := filepath.Join(base, "wslink")
	if err := os.Symlink(realDir, wslink); err != nil {
		t.Skipf("symlinks unsupported on this platform/filesystem: %v", err)
	}
	cfg := Config{Workspace: wslink, SkillsDraftDir: quar}
	if err := validateSkillDraftConfig(cfg); err == nil {
		t.Fatal("quarantine inside the symlink-resolved workspace must be fatal; " +
			"validation must use EvalSymlinks like the enforcement layer, not filepath.Abs")
	}
}

func TestDefaultRulesSkillDraftAsks(t *testing.T) {
	policy := permpolicy.NewPolicy(defaultRules(), nil)
	got := policy.Evaluate(context.Background(), "s1", session.ModeDefault,
		session.NewToolCall("id", skills.DraftToolName, json.RawMessage(`{}`)), nil)
	if got.Effect != governance.Ask {
		t.Fatalf("SkillDraft should default to Ask, got %v", got.Effect)
	}
}

func TestRegisterSkillDraftGating(t *testing.T) {
	t.Run("disabled when no draft dir", func(t *testing.T) {
		cat := tool.NewCatalog()
		registerSkillDraft(context.Background(), Config{Diagnostics: port.NopDiagnostics{}}, cat, nil, true)
		if _, ok := cat.Lookup(skills.DraftToolName); ok {
			t.Error("SkillDraft must NOT be registered without a skills-draft dir")
		}
	})
	t.Run("registered when draft dir set", func(t *testing.T) {
		cat := tool.NewCatalog()
		registerSkillDraft(context.Background(), Config{SkillsDraftDir: t.TempDir(), SkillsDraftThreshold: 0.5, Diagnostics: port.NopDiagnostics{}}, cat, nil, true)
		if _, ok := cat.Lookup(skills.DraftToolName); !ok {
			t.Error("SkillDraft must be registered when a skills-draft dir is set")
		}
	})
}

func TestDirsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"/a/b", "/a/b", true},
		{"/a", "/a/b", true},
		{"/a/b", "/a", true},
		{"/a/b", "/a/c", false},
		{"/a", "/ab", false},
	}
	for _, tc := range cases {
		if got := dirsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("dirsOverlap(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCloudNativeLearning_Scenario1_DirectSkillDraftRemainsInactive(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prompt   string
		admitted bool
	}{
		{name: "admitted procedure request", prompt: "create a skill from this procedure", admitted: true},
		{name: "rejected procedure request", prompt: "do not create a skill from this procedure", admitted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := osfsWSForTest(t, t.TempDir()).Root()
			var activitiesMu sync.Mutex
			var activities []learning.Activity
			turns := []mockllm.Turn{
				mockllm.ToolCallTurn(session.NewToolCall("draft", skills.DraftToolName, []byte(`{"name":"direct-procedure","description":"Use this procedure when testing direct drafts.","body":"1. Run the procedure.\nDone when: complete."}`))),
				mockllm.TextTurn("Draft complete."),
			}
			if tc.admitted {
				turns = append(turns, mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
			}
			provider := mockllm.New(turns...)
			built, err := buildIsolated(t, context.Background(), Config{
				Model: "test-model", Workspace: workspace, TrustProject: true, NoSoul: true,
				SkillsDraftDir: t.TempDir(), LearningMode: learning.Review,
				UserModelDir: t.TempDir(), MemoryDir: t.TempDir(),
				LearningMetricsEmitter: func(activity learning.Activity) {
					activitiesMu.Lock()
					defer activitiesMu.Unlock()
					activities = append(activities, activity)
				},
				envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "test-key"}),
				liveModelHTTPClient: offlineHTTPClient(),
				providerConstructor: func(Config, string, string, string) port.LLMProvider { return provider },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()

			ctx := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: "alice", GrantType: session.GrantTypeUser})
			sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(ctx, sess.ID, tc.prompt)
			if err != nil {
				t.Fatal(err)
			}
			for event := range run.Events() {
				if event.Type == session.EvPermissionAsk && event.Ask != nil {
					run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
				}
				if event.Type == session.EvResult && event.Result != nil && event.Result.Stop == session.StopError {
					t.Fatalf("run failed: %s", event.Result.Error)
				}
			}

			listed, err := built.Service.ListLearnedSkills(ctx, &mecatlv1.ListLearnedSkillsRequest{Project: workspace})
			if err != nil {
				t.Fatal(err)
			}
			if len(listed.GetSkills()) != 1 {
				t.Fatalf("direct draft count = %d, want 1: %+v", len(listed.GetSkills()), listed.GetSkills())
			}
			if got := learning.SkillState(listed.GetSkills()[0].GetState()); got != learning.SkillDraft {
				t.Fatalf("direct draft state = %q, want inactive draft", got)
			}

			activitiesMu.Lock()
			defer activitiesMu.Unlock()
			wantKind, wantReason := learning.ActivitySkipped, learning.ReasonBelowThreshold
			if tc.admitted {
				wantKind, wantReason = learning.ActivityAdmitted, learning.ReasonHardTrigger
			}
			for _, activity := range activities {
				if activity.Kind == wantKind && activity.Reason == wantReason {
					return
				}
			}
			t.Fatalf("activities = %+v, want %s/%s", activities, wantKind, wantReason)
		})
	}
}
