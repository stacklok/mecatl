package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

func TestMecatedMCPWarningSharedFactory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MECATL_LIFECYCLE_LEGACY", "")
	cfg, err := parseFlagsMode(modeServe, nil)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	ac := appConfig(cfg, nil, nil, nil, nil, slogdiag.NewText(&logs))
	if any(ac.MCPProfileLoader) != any(ac.MCPAuthorityLoader) {
		t.Fatal("factory constructed separate loader lifetimes")
	}
	operator := &permconfig.MCPSection{Servers: []permconfig.MCPServerProfile{{Name: "server", URL: "https://mcp.example/mcp", Auth: permconfig.MCPAuthProfile{Mode: "static_bearer", StaticBearer: &permconfig.MCPStaticBearerProfile{TokenEnv: "MECATL_LIFECYCLE_LEGACY"}}}}}
	if _, _, err := ac.MCPProfileLoader.Load(operator); !errors.Is(err, cliconfig.ErrMCPProfileSecret) {
		t.Fatalf("direct secret error = %v", err)
	}
	if _, err := ac.MCPAuthorityLoader.LoadAuthority(operator, ac.MCPAuthorityDefault, ac.MCPBrokerSupported); !errors.Is(err, cliconfig.ErrMCPProfileSecret) {
		t.Fatalf("authority secret error = %v", err)
	}
	if strings.Count(logs.String(), "deprecated") != 1 {
		t.Fatal("factory warning missing or duplicated")
	}
}

func TestMCPDirectLifecycleStaticSourceSelection(t *testing.T) {
	for _, settingsFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "conventional", true: "same settings file"}[settingsFile], func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			dir := filepath.Join(xdg, "mecatl")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "auth.yaml")
			prefix := ""
			if settingsFile {
				if err := os.WriteFile(path, []byte("mcp: [poisoned-unselected-default"), 0o600); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(t.TempDir(), "selected.yaml")
				prefix = "credential_store:\n  api_key:\n    file: " + path + "\n"
			}
			if err := os.WriteFile(path, []byte("mcp:\n  lifecycle_test:\n    token: selected-direct-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(t.TempDir(), "settings.yaml")
			body := prefix + "mcp:\n  mode: global\n  servers:\n    - name: selected\n      url: https://mcp.example/mcp\n      auth:\n        mode: static_bearer\n        static_bearer: {token_ref: lifecycle_test}\n"
			if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			profiles, err := loadMCPLoginProfilesSelected([]string{settings}, "selected")
			if err != nil {
				t.Fatal(err)
			}
			defer profiles.Close()
			if profiles.Servers[0].Headers["Authorization"] != "Bearer selected-direct-token" {
				t.Fatal("direct lifecycle lost selected credential-store metadata")
			}
		})
	}
}

func TestMCPDirectLifecycleOAuthSelectionDoesNotReadStaticFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MECATL_LOGIN_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("MECATL_LOGIN_CREDENTIAL", base64.StdEncoding.EncodeToString([]byte("opaque")))
	poisoned := filepath.Join(t.TempDir(), "poisoned.yaml")
	if err := os.WriteFile(poisoned, []byte("mcp: [private-poisoned-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	oauthBody := "credential_store:\n  api_key:\n    file: " + poisoned + "\n" + mcpLoginOAuthYAML("oauth", "environment", "")
	body := oauthBody + "    - name: unrelated\n      url: https://mcp.example/mcp\n      auth:\n        mode: static_bearer\n        static_bearer: {token_ref: absent}\n"
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	originalEnv := xdgconfig.OSEnv
	t.Cleanup(func() { xdgconfig.OSEnv = originalEnv })
	authReads := 0
	xdgconfig.OSEnv.ReadFile = func(path string) ([]byte, error) {
		if path == poisoned {
			authReads++
		}
		return originalEnv.ReadFile(path)
	}
	profiles, err := loadMCPLoginProfilesSelected([]string{settings}, "oauth")
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if len(profiles.Servers) != 1 || profiles.Servers[0].OAuth == nil || authReads != 0 {
		t.Fatal("targeted OAuth lifecycle acquired the unrelated static file")
	}
	// OAuth-only effective settings must also avoid static acquisition.
	if err := os.WriteFile(settings, []byte(oauthBody), 0o600); err != nil {
		t.Fatal(err)
	}
	oauthOnly, err := loadMCPLoginProfilesSelected([]string{settings}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer oauthOnly.Close()
	if authReads != 0 {
		t.Fatal("OAuth-only lifecycle acquired the static file")
	}
	// Positive control uses the same lifecycle path and actual acquisition seam.
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadMCPLoginProfilesSelected([]string{settings}, "unrelated")
	if !errors.Is(err, cliconfig.ErrMCPProfileStore) || authReads != 1 {
		t.Fatalf("static positive control: err=%v reads=%d", err, authReads)
	}
}

func TestMCPDirectLifecycleDerivedEnvironmentToleratesUnusedFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MECATL_MCP_TOKEN_LIFECYCLE_TEST", "derived-direct-token")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	body := "credential_store:\n  api_key:\n    file: " + filepath.Join(t.TempDir(), "missing.yaml") + "\nmcp:\n  mode: global\n  servers:\n    - name: selected\n      url: https://mcp.example/mcp\n      auth:\n        mode: static_bearer\n        static_bearer: {token_ref: lifecycle_test}\n"
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := loadMCPLoginProfilesSelected([]string{settings}, "selected")
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	if profiles.Servers[0].Headers["Authorization"] != "Bearer derived-direct-token" {
		t.Fatal("derived environment was not selected")
	}
}

func TestMCPLoginLegacyWarningSurvivesMissingSecret(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MECATL_LIFECYCLE_LEGACY", "")
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	body := "mcp:\n  mode: global\n  servers:\n    - name: selected\n      url: https://mcp.example/mcp\n      auth:\n        mode: static_bearer\n        static_bearer: {token_env: MECATL_LIFECYCLE_LEGACY}\n"
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	_, err := loadMCPLoginProfilesWithCustodyMode([]string{settings}, "selected", false, slogdiag.NewText(&logs))
	if !errors.Is(err, cliconfig.ErrMCPProfileSecret) || strings.Count(logs.String(), "deprecated") != 1 {
		t.Fatal("direct failure lost injected startup warning")
	}
}
