package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestBuildOperatorContextWindowsFeedEchoListAndPerSessionFactory(t *testing.T) {
	const (
		model          = "gpt-5"
		operatorWindow = 321_000
		cliWindow      = 111_000
	)
	for _, tc := range []struct {
		name string
		cli  int
		want int64
	}{
		{name: "operator YAML", want: operatorWindow},
		{name: "CLI wins", cli: cliWindow, want: cliWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			projectDir := filepath.Join(workspace, ".mecatl")
			if err := os.MkdirAll(projectDir, 0o700); err != nil {
				t.Fatalf("mkdir project config: %v", err)
			}
			project := "models:\n  default: gpt-5\n  context_windows:\n    openai:\n      gpt-5: 999000\n"
			if err := os.WriteFile(filepath.Join(projectDir, "settings.yaml"), []byte(project), 0o600); err != nil {
				t.Fatalf("write project config: %v", err)
			}
			operator := writeOperatorSettingsFile(t, "models:\n  aliases:\n    routed: gpt-5\n  default: routed\n  allowlist: [gpt-5]\n  context_windows:\n    openai:\n      gpt-5: 321000\n")

			built, err := buildIsolated(t, context.Background(), Config{
				Workspace:               workspace,
				NoSoul:                  true,
				TrustProject:            true,
				PermissionsConventional: true,
				permConfigEnv:           isolatedPermConfigEnv(t),
				PermissionConfigs:       []string{operator},
				ContextWindowOverride:   tc.cli,
				envDetector:             fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-x"}),
				liveModelHTTPClient:     offlineHTTPClient(),
				providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
					return mockllm.New(mockllm.TextTurn("ok"))
				},
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()

			shared, err := built.Service.CreateSession(context.Background(), session.ModeDefault, defaultLimits())
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			resolved := built.Service.ResolvedModel(shared.ID)
			if resolved.ProviderID != providerOpenAI || resolved.ModelID != model || resolved.ContextWindow != tc.want {
				t.Fatalf("shared resolved model = %+v, want openai/%s window %d", resolved, model, tc.want)
			}

			var listed int64
			for _, entry := range built.Service.ListModels(context.Background()) {
				if entry.GetProviderId() == providerOpenAI && entry.GetId() == model {
					listed = entry.GetContextLimit()
					break
				}
			}
			if listed != tc.want {
				t.Fatalf("model-list context_limit = %d, want %d", listed, tc.want)
			}

			selected, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, defaultLimits(),
				server.ProviderSelector{ProviderID: providerOpenAI, ModelID: model})
			if err != nil {
				t.Fatalf("CreateSessionWithProvider: %v", err)
			}
			if got := built.Service.ResolvedModel(selected.ID).ContextWindow; got != tc.want {
				t.Fatalf("per-session resolved window = %d, want %d", got, tc.want)
			}
		})
	}
}
