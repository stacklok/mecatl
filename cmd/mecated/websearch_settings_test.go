package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonWebSearchSettingsComposition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"settings URL", nil, "operator HTTP backend"},
		{"settings URL with standalone request flags", []string{"--websearch-auth-header=X-Flag", "--websearch-query-param=search"}, "operator HTTP backend"},
		{"empty standalone request flags", []string{"--websearch-auth-header=", "--websearch-query-param="}, "operator HTTP backend"},
		{"explicit URL wins disabled", []string{"--websearch-url=https://flag.example/search"}, "explicit HTTP backend"},
		{"kill switch", []string{"--websearch=off", "--websearch-url=https://flag.example/search"}, "DISABLED by operator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			dir := filepath.Join(home, "mecatl")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			settings := "websearch:\n  url: https://settings.example/search\n  auth_header: X-Settings\n  query_param: lookup\n"
			if strings.HasPrefix(tc.name, "explicit URL") || tc.name == "kill switch" {
				settings = "websearch:\n  enabled: false\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := append([]string{"--mock", "--workspace=" + t.TempDir(), "--no-soul", "--user-model-dir=" + t.TempDir()}, tc.flags...)
			parsed, err := parseFlags(flags)
			if err != nil {
				t.Fatal(err)
			}
			d := &edgeRecordingDiagnostics{}
			cfg := appConfig(parsed, nil, nil, nil, nil, d)
			if tc.name == "settings URL with standalone request flags" && (cfg.WebSearchAuthHeader != "X-Flag" || cfg.WebSearchQueryParam != "search" || !cfg.WebSearchAuthHeaderFlagSet || !cfg.WebSearchQueryParamFlagSet) {
				t.Fatalf("standalone request flags not projected: header=%q param=%q", cfg.WebSearchAuthHeader, cfg.WebSearchQueryParam)
			}
			if tc.name == "empty standalone request flags" && (!cfg.WebSearchAuthHeaderFlagSet || !cfg.WebSearchQueryParamFlagSet) {
				t.Fatal("empty explicit flags lost presence markers")
			}
			if !cfg.PermissionsConventional || len(cfg.PermissionConfigs) != 0 {
				t.Fatalf("conventional discovery lost: %+v", cfg.PermissionConfigs)
			}
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(built.Close)
			found := false
			for _, record := range d.records {
				if strings.Contains(record.message, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("backend %q not selected; diagnostics: %+v", tc.want, d.records)
			}
		})
	}
}
