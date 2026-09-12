//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package authfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "auth.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestADR_0332_AuthFileTargetedPreservation(t *testing.T) {
	const original = "# credentials\nproviders:\n  openai:\n    api_key: old\n  openai-codex:\n    oauth:\n      access_token: oauth-secret\n      account_id: acct\n      expires_at: '2030-01-01T00:00:00Z'\n"
	path := privateFile(t, original)
	key := "new-secret"
	state, err := UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if err != nil || state != CommitDurable {
		t.Fatalf("UpdateAPIKey = %q, %v", state, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, preserved := range []string{"# credentials", "openai-codex:", "access_token: oauth-secret", "account_id: acct"} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("output lost %q:\n%s", preserved, text)
		}
	}
	if !strings.Contains(text, "api_key: 'new-secret'") || strings.Contains(text, "api_key: old") {
		t.Fatalf("target was not narrowly replaced:\n%s", text)
	}

	for name, body := range map[string]string{
		"duplicate": "providers:\n  openai:\n    api_key: one\n  openai:\n    api_key: two\n",
		"alias":     "providers:\n  openai: &key\n    api_key: one\n  anthropic: *key\n",
		"unknown":   "providers:\n  openai:\n    api_key: one\n    extra: nope\n",
	} {
		t.Run(name, func(t *testing.T) {
			bad := privateFile(t, body)
			state, err := UpdateAPIKey(context.Background(), bad, APIKeyUpdate{Provider: "openai", APIKey: &key})
			if state != CommitNotApplied || err == nil {
				t.Fatalf("UpdateAPIKey = %q, %v", state, err)
			}
		})
	}
}

func TestADR_0332_AuthFileOwnershipAndModeGate(t *testing.T) {
	key := "secret"
	path := privateFile(t, "providers: {}\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if state != CommitNotApplied || err == nil {
		t.Fatalf("loose target = %q, %v", state, err)
	}
	if mode := mustMode(t, path); mode != 0o644 {
		t.Fatalf("writer changed mode to %o", mode)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "auth.yaml")
	state, err = UpdateAPIKey(context.Background(), missing, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if state != CommitNotApplied || err == nil {
		t.Fatalf("loose parent = %q, %v", state, err)
	}
}

func TestADR_0332_AuthFileCommitProtocol(t *testing.T) {
	path := privateFile(t, "providers:\n  openai:\n    api_key: old\n")
	key := "new"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state, err := UpdateAPIKey(ctx, path, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if state != CommitNotApplied || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update = %q, %v", state, err)
	}

	oldHook := updateTestHook
	defer func() { updateTestHook = oldHook }()
	for _, phase := range []string{"after-lock", "after-temp-sync"} {
		t.Run("cancel-"+phase, func(t *testing.T) {
			cancelPath := privateFile(t, "providers:\n  openai:\n    api_key: old\n")
			cancelCtx, cancelUpdate := context.WithCancel(context.Background())
			updateTestHook = func(got string) error {
				if got == phase {
					cancelUpdate()
				}
				return nil
			}
			state, err := UpdateAPIKey(cancelCtx, cancelPath, APIKeyUpdate{Provider: "openai", APIKey: &key})
			if state != CommitNotApplied || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled update = %q, %v", state, err)
			}
			got, readErr := os.ReadFile(cancelPath)
			if readErr != nil || strings.Contains(string(got), "api_key: 'new'") {
				t.Fatalf("cancelled update changed target: %q, %v", got, readErr)
			}
		})
	}
	updateTestHook = func(phase string) error {
		if phase == "before-compare" {
			return os.WriteFile(path, []byte("providers: {}\n"), 0o600)
		}
		return nil
	}
	state, err = UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if state != CommitNotApplied || err == nil {
		t.Fatalf("mismatch update = %q, %v", state, err)
	}

	updateTestHook = func(phase string) error {
		if phase == "after-rename" {
			return errors.New("injected directory sync failure")
		}
		return nil
	}
	state, err = UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if state != CommitReplacementAppliedDurabilityUnknown || err == nil {
		t.Fatalf("post-rename failure = %q, %v", state, err)
	}
}

func TestMecatuiLocalProviderSetup_Scenario3_UnchangedAndSecretSafe(t *testing.T) {
	path := privateFile(t, "providers:\n  openai:\n    api_key: same\n  openai-codex:\n    oauth:\n      access_token: oauth-only\n      account_id: acct\n")
	key := "same"
	state, err := UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai\nFORGED", APIKey: &key})
	if state != CommitNotApplied || err == nil || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "\n") {
		t.Fatalf("unsafe invalid-provider result = %q, %q", state, err)
	}
	state, err = UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
	if state != CommitNoop || err != nil {
		t.Fatalf("same value = %q, %v", state, err)
	}
	missing := privateFile(t, "providers: {}\n")
	state, err = UpdateAPIKey(context.Background(), missing, APIKeyUpdate{Provider: "openai"})
	if state != CommitNoop || err != nil {
		t.Fatalf("absent removal = %q, %v", state, err)
	}

	settings := filepath.Join(filepath.Dir(path), "settings.yaml")
	if err := os.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDistinctFiles(path, settings); err != nil {
		t.Fatalf("distinct files: %v", err)
	}
	if err := ValidateDistinctFiles(path, path); err == nil {
		t.Fatal("same physical file was accepted")
	}
	alias := filepath.Join(filepath.Dir(path), "auth-hardlink.yaml")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDistinctFiles(path, alias); err == nil {
		t.Fatal("hard-linked physical file was accepted")
	}

	for _, reserved := range []string{"mock", "openai-codex", "toolhive"} {
		state, err := UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: reserved, APIKey: &key})
		if state != CommitNotApplied || err == nil {
			t.Fatalf("reserved provider %q = %q, %v", reserved, state, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(got), "api_key: same") {
		t.Fatalf("reserved update changed auth file: %q, %v", got, err)
	}
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
