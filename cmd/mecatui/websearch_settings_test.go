package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/app"
)

func TestEmbeddedWebSearchSettingsComposition(t *testing.T) {
	for _, tc := range []struct {
		name, search, want string
	}{
		{"generic URL", "url: https://settings.example/search", "WebSearch ENABLED with operator HTTP backend (settings.yaml)"},
		{"disabled", "enabled: false\n  url: https://settings.example/search", "WebSearch DISABLED by operator"},
		{"SearXNG", "searxng:\n    url: https://searx.example/search", "WebSearch ENABLED with SearXNG backend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SEARXNG_URL", "https://conflicting-searx.example/search")
			writeIsolatedExecutionSettings(t, app.PlacementHostLocal)
			path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "mecatl", "settings.yaml")
			if err := os.WriteFile(path, []byte("execution:\n  default_placement: host-local\nwebsearch:\n  "+tc.search+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(t.TempDir(), "diagnostics.log")
			started := false
			err := runWithOptions([]string{"mecatui", "--mock", "--diagnostics-log=" + logPath, "--no-store", "--no-memory", "--no-soul", "--no-skills", "--no-commands", "--user-model-dir=" + t.TempDir(), "--workspace=" + t.TempDir()}, runOptions{
				beforeEmbeddedStart: func(cfg app.Config) error {
					if !cfg.PermissionsConventional || len(cfg.PermissionConfigs) != 0 || cfg.WebSearchURL != "" || cfg.SearXNGURL != "" || cfg.WebSearchOff {
						t.Fatalf("embedded settings must resolve in shared Build and ignore SEARXNG_URL, not in cmd: %+v", cfg.PermissionConfigs)
					}
					return nil
				},
				runProgram: func(_ context.Context, model ui.Model) (tea.Model, error) {
					started = true
					return model, nil
				},
			})
			if err != nil || !started {
				t.Fatalf("embedded startup: started=%v err=%v", started, err)
			}
			logs, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(logs), tc.want) {
				t.Fatalf("embedded backend did not use conventional settings: %s", logs)
			}
		})
	}
}
