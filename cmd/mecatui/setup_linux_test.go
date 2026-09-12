//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestPanelRepair_NativeSetupUsesProductionCommandWiring(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	managed := filepath.Join(configHome, "mecatl")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(managed, "auth.yaml")
	if err := os.WriteFile(authPath, []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(t.TempDir(), "setup-input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("1\ncorp\ny\n4\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "setup-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	host := &setupRouteNativeHost{ids: []string{"corp"}}
	deps := defaultSetupDeps()
	deps.isTerminal = func(int) bool { return true }
	deps.load = func(path string, explicit bool) (setupSnapshot, error) {
		if path != authPath || explicit {
			t.Fatalf("setup load path=%q explicit=%v", path, explicit)
		}
		return setupSnapshot{NativeIDs: []string{"corp"}, NativeDefaults: map[string]string{"corp": "corp/model"}}, nil
	}
	deps.updateKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		t.Fatal("native login crossed API-key writer")
		return "", nil
	}
	res := resolveLLMCommand([]string{"setup"})
	if res.err != nil {
		t.Fatal(res.err)
	}
	services := llmCommandDeps{setup: deps, openNative: func(context.Context, bool, io.Writer) (nativeLLMHost, error) { return host, nil }}
	if err := runLLMCommandWith(res, input, output, io.Discard, services); err != nil {
		t.Fatal(err)
	}
	if strings.Join(host.login, ",") != "corp" || host.closeCall != 1 {
		t.Fatalf("native host login=%v closes=%d", host.login, host.closeCall)
	}

	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	host.login = nil
	res = resolveLLMCommand([]string{"setup", "--auth-file", authPath})
	if res.err != nil {
		t.Fatal(res.err)
	}
	deps.load = func(string, bool) (setupSnapshot, error) {
		return setupSnapshot{NativeIDs: []string{"corp"}}, nil
	}
	services.setup = deps
	if err := runLLMCommandWith(res, input, output, io.Discard, services); err == nil || !strings.Contains(err.Error(), "does not use --auth-file") {
		t.Fatalf("override native handoff = %v", err)
	}
	if len(host.login) != 0 {
		t.Fatalf("override reached native login: %v", host.login)
	}
}

func TestPanelRepair_ProductionStartReparsesChosenAuthAndSavedDefaults(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	managed := filepath.Join(configHome, "mecatl")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(managed, "work-auth.yaml")
	settings := filepath.Join(managed, "settings.yaml")
	if err := os.WriteFile(authPath, []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("models:\n  aliases:\n    fast: gpt-5-mini\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := "saved-key-sentinel"
	if state, err := authfile.UpdateAPIKey(t.Context(), authPath, authfile.APIKeyUpdate{Provider: "openai", APIKey: &key}); state != authfile.CommitDurable || err != nil {
		t.Fatalf("save auth = %q, %v", state, err)
	}
	if state, err := permconfig.UpdateDefaults(t.Context(), settings, permconfig.DefaultUpdate{Provider: "openai", Model: "gpt-5-mini"}); state != authfile.CommitDurable || err != nil {
		t.Fatalf("save defaults = %q, %v", state, err)
	}
	called := 0
	deps := setupDepsForRun(func(argv []string) error {
		called++
		if strings.Join(argv, "\x00") != strings.Join([]string{"mecatui", "--auth-file", authPath}, "\x00") {
			t.Fatalf("startup argv = %q", argv)
		}
		res := resolveInvocation(argv)
		if res.err != nil {
			return res.err
		}
		cfg, err := parseRunConfig(res)
		if err != nil {
			return err
		}
		if cfg.providerKeys.OpenAI != key {
			t.Fatalf("startup did not re-read chosen auth file")
		}
		snapshot, err := loadSetupSnapshot(authPath, true)
		if err != nil {
			return err
		}
		if snapshot.Default != (defaultSelection{Provider: "openai", Model: "gpt-5-mini"}) || snapshot.Aliases["fast"] != "gpt-5-mini" {
			body, _ := os.ReadFile(settings)
			t.Fatalf("startup defaults/aliases = %+v aliases=%v settings=%s", snapshot.Default, snapshot.Aliases, body)
		}
		return nil
	})
	if err := deps.start(authPath); err != nil || called != 1 {
		t.Fatalf("production start wiring: called=%d err=%v", called, err)
	}
}

func TestFinalReview_SetupPreflightsWriterDocumentsBeforeLoadOrPrompt(t *testing.T) {
	cases := map[string]string{
		"malformed": "models: [\n",
		"duplicate": "models: {}\nmodels: {}\n",
		"alias":     "models: &models\n  default: model\ncopy: *models\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			config := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", config)
			managed := filepath.Join(config, "mecatl")
			if err := os.Mkdir(managed, 0o700); err != nil {
				t.Fatal(err)
			}
			authPath := filepath.Join(managed, "auth.yaml")
			if err := os.WriteFile(authPath, []byte("providers: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(managed, "settings.yaml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			input, err := os.CreateTemp(t.TempDir(), "input")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			loaded, read, updated := false, false, false
			deps := defaultSetupDeps()
			deps.isTerminal = func(int) bool { return true }
			deps.load = func(string, bool) (setupSnapshot, error) { loaded = true; return setupSnapshot{}, nil }
			deps.readPassword = func(int) ([]byte, error) { read = true; return nil, nil }
			deps.readSecret = func(context.Context) ([]byte, error) { read = true; return nil, nil }
			deps.updateKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
				updated = true
				return authfile.CommitDurable, nil
			}
			deps.updateDefaults = func(context.Context, string, defaultSelection) (authfile.CommitState, error) {
				updated = true
				return authfile.CommitDurable, nil
			}
			if err := runSetupCommand(t.Context(), authPath, false, input, output, deps); err == nil {
				t.Fatal("invalid writer target reached setup")
			}
			if loaded || read || updated {
				t.Fatalf("preflight crossed interaction: load=%v read=%v update=%v", loaded, read, updated)
			}
		})
	}
}

func TestFinalReview_SetupPreflightRejectsMalformedAuthBeforeLoadOrPrompt(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	managed := filepath.Join(config, "mecatl")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(managed, "auth.yaml")
	if err := os.WriteFile(authPath, []byte("providers: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "settings.yaml"), []byte("models: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	loaded, interacted := false, false
	deps := defaultSetupDeps()
	deps.isTerminal = func(int) bool { return true }
	deps.load = func(string, bool) (setupSnapshot, error) { loaded = true; return setupSnapshot{}, nil }
	deps.readPassword = func(int) ([]byte, error) { interacted = true; return nil, nil }
	deps.updateKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		interacted = true
		return authfile.CommitDurable, nil
	}
	if err := runSetupCommand(t.Context(), authPath, false, input, output, deps); err == nil {
		t.Fatal("malformed auth target reached setup")
	}
	if loaded || interacted {
		t.Fatalf("auth preflight crossed interaction: load=%v interacted=%v", loaded, interacted)
	}
}

func TestFinalReview_SetupPreflightRejectsTargetAliasBeforeLoadOrPrompt(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	managed := filepath.Join(config, "mecatl")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(managed, "auth.yaml")
	settings := filepath.Join(managed, "settings.yaml")
	if err := os.WriteFile(authPath, []byte("providers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(authPath, settings); err != nil {
		t.Fatal(err)
	}
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	loaded, interacted := false, false
	deps := defaultSetupDeps()
	deps.isTerminal = func(int) bool { return true }
	deps.load = func(string, bool) (setupSnapshot, error) { loaded = true; return setupSnapshot{}, nil }
	deps.readPassword = func(int) ([]byte, error) { interacted = true; return nil, nil }
	deps.updateKey = func(context.Context, string, authfile.APIKeyUpdate) (authfile.CommitState, error) {
		interacted = true
		return authfile.CommitDurable, nil
	}
	if err := runSetupCommand(t.Context(), authPath, false, input, output, deps); err == nil {
		t.Fatal("physical target alias accepted")
	}
	if loaded || interacted {
		t.Fatalf("alias preflight crossed interaction: load=%v interacted=%v", loaded, interacted)
	}
}

func TestFinalReview_SetupPreflightRejectsFIFOWithoutBlocking(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	managed := filepath.Join(config, "mecatl")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(managed, "auth.yaml")
	if err := unix.Mkfifo(authPath, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- preflightSetupDocuments(authPath, filepath.Join(managed, "settings.yaml")) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as auth target")
		}
	case <-time.After(time.Second):
		t.Fatal("setup preflight blocked on FIFO")
	}
}

func TestFinalReview_SetupPreflightDoesNotCreateConventionalParent(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	managed := filepath.Join(config, "mecatl")
	if err := preflightSetupDocuments(filepath.Join(managed, "auth.yaml"), filepath.Join(managed, "settings.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created managed directory: %v", err)
	}
}
