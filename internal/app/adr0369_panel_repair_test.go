package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func TestAliasCommandBuildStartup(t *testing.T) {
	t.Run("unmatched CLI provider fails Build", func(t *testing.T) {
		built, err := buildIsolated(t, t.Context(), Config{
			UseMock: true, NoSoul: true,
			ModelAliasTargets: ModelAliases{"fast": {ProviderID: providerMock}},
		})
		if err == nil {
			built.Close()
			t.Fatal("Build accepted --model-alias-provider without a same-tier model")
		}
		if strings.Contains(err.Error(), "fast") || len(err.Error()) > 512 {
			t.Fatalf("startup error exposed alias details or was unbounded: %v", err)
		}
	})

	t.Run("CLI scalar replaces complete YAML pair", func(t *testing.T) {
		operator := writeOperatorSettingsFile(t, `models:
  default: fast
  aliases:
    fast:
      provider: poison-provider
      model: poison-model
`)
		built, err := buildIsolated(t, t.Context(), Config{
			UseMock: true, NoSoul: true, PermissionConfigs: []string{operator},
			ModelAliases:      map[string]string{"fast": "cli-model"},
			ModelAliasTargets: ModelAliases{"fast": {Model: "cli-model"}},
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, defaultLimits())
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		got := built.Service.ResolvedModel(sess.ID)
		if got.ProviderID != providerMock || got.ModelID != "cli-model" {
			t.Fatalf("resolved CLI replacement = %+v, want mock/cli-model", got)
		}
	})
}

func TestADR_0369_Scenario1_ProjectModelsDisabled(t *testing.T) {
	const project = `models:
  default: poison
  aliases:
    poison:
      provider: mock
      model: poison-model
  slots:
    plan: poison
`
	for _, trust := range []bool{false, true} {
		name := map[bool]string{false: "untrusted", true: "trusted"}[trust]
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			writeProjectFile(t, workspace+"/.mecatl/settings.yaml", project)
			var logs bytes.Buffer
			diag := slogdiag.New(&logs, false, port.LevelDebug)
			provider := mockllm.New(
				mockllm.ToolCallTurn(session.NewToolCall("read", "Read", []byte(`{"file_path":".mecatl/settings.yaml"}`))),
				mockllm.TextTurn("done"),
			)
			built, err := buildIsolated(t, context.Background(), Config{
				Workspace: workspace, MockProvider: provider, NoSoul: true, Model: "safe-model", UserModelDir: t.TempDir(),
				TrustProject: trust, PermissionsConventional: true,
				permConfigEnv: isolatedPermConfigEnv(t), Diagnostics: diag,
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			for _, mode := range []session.PermissionMode{session.ModeDefault, session.ModePlan} {
				sess, err := built.Service.CreateSession(t.Context(), mode, defaultLimits())
				if err != nil {
					t.Fatalf("CreateSession(%s): %v", mode, err)
				}
				got := built.Service.ResolvedModel(sess.ID)
				if got.ProviderID != providerMock || got.ModelID != "safe-model" {
					t.Fatalf("project models affected %s session: %+v", mode, got)
				}
				if mode == session.ModeDefault {
					run, err := built.Service.StartRun(t.Context(), sess.ID, "read settings")
					if err != nil {
						t.Fatalf("StartRun: %v", err)
					}
					for range run.Events() {
					}
					built.Service.FinishRun(sess.ID, run)
				}
			}
			log := logs.String()
			if strings.Count(log, "IGNORING project-tier models block") != 1 {
				t.Fatalf("project models warning count != 1: %s", log)
			}
			for _, forbidden := range []string{"poison-model", "plan: poison"} {
				if strings.Contains(log, forbidden) {
					t.Fatalf("warning leaked ignored value %q: %s", forbidden, log)
				}
			}
		})
	}
}
