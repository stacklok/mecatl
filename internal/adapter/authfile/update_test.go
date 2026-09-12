//go:build linux

package authfile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
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
	key := "new-secret"
	for _, update := range []APIKeyUpdate{{Provider: "custom-oauth", APIKey: &key}, {Provider: "custom-oauth"}} {
		path := privateFile(t, "providers:\n  custom-oauth:\n    oauth:\n      access_token: oauth-secret\n      account_id: acct\n")
		before, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		state, err := UpdateAPIKey(context.Background(), path, update)
		after, afterErr := os.ReadFile(path)
		if state != CommitNotApplied || err == nil || afterErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("OAuth-shaped target mutation: state=%q err=%v unchanged=%v read=%v", state, err, bytes.Equal(before, after), afterErr)
		}
		if strings.Contains(err.Error(), "oauth-secret") {
			t.Fatal("OAuth material reached error")
		}
	}
	const original = "# credentials\nproviders:\n  openai:\n    api_key: old\n  openai-codex:\n    oauth:\n      access_token: oauth-secret\n      account_id: acct\n      expires_at: '2030-01-01T00:00:00Z'\n"
	path := privateFile(t, original)
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

func TestPanelRepair_AuthFileCanonicalParentsAndLeaves(t *testing.T) {
	key := "new"
	t.Run("symlinked ancestor resolves to private parent", func(t *testing.T) {
		base := t.TempDir()
		realParent := filepath.Join(base, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(base, "alias")
		if err := os.Symlink(realParent, alias); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(alias, "auth.yaml")
		state, err := UpdateAPIKey(t.Context(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
		if state != CommitDurable || err != nil {
			t.Fatalf("canonical parent = %q, %v", state, err)
		}
	})
	t.Run("leaf and lock symlinks rejected", func(t *testing.T) {
		for _, leaf := range []string{"auth.yaml", "auth.yaml.lock"} {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("providers: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, leaf)); err != nil {
				t.Fatal(err)
			}
			state, err := UpdateAPIKey(t.Context(), filepath.Join(dir, "auth.yaml"), APIKeyUpdate{Provider: "openai", APIKey: &key})
			if state != CommitNotApplied || err == nil {
				t.Fatalf("symlink %s = %q, %v", leaf, state, err)
			}
		}
	})
	t.Run("conventional path through XDG alias", func(t *testing.T) {
		base := t.TempDir()
		realConfig := filepath.Join(base, "real-config")
		if err := os.Mkdir(realConfig, 0o700); err != nil {
			t.Fatal(err)
		}
		aliasConfig := filepath.Join(base, "config-alias")
		if err := os.Symlink(realConfig, aliasConfig); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", aliasConfig)
		path := DefaultPath(xdgconfig.OSEnv)
		state, err := UpdateAPIKey(t.Context(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
		if state != CommitDurable || err != nil {
			t.Fatalf("conventional create through alias = %q, %v", state, err)
		}
		physicalParent := filepath.Join(realConfig, "mecatl")
		info, err := os.Stat(physicalParent)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("created physical parent = %v, %v", info, err)
		}
		if _, err := os.Stat(filepath.Join(physicalParent, "auth.yaml")); err != nil {
			t.Fatalf("conventional physical auth file: %v", err)
		}
	})
}

func TestPanelRepair_AuthFileCooperativeContention(t *testing.T) {
	path := privateFile(t, "providers:\n  openai:\n    api_key: old\n")
	firstKey, latestKey := "first", "latest"
	locked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	oldHook := updateTestHook
	updateTestHook = func(phase string) error {
		if phase == "after-lock" {
			once.Do(func() { close(locked); <-release })
		}
		return nil
	}
	defer func() { updateTestHook = oldHook }()
	firstDone := make(chan error, 1)
	go func() {
		_, err := UpdateAPIKey(context.Background(), path, APIKeyUpdate{Provider: "openai", APIKey: &firstKey})
		firstDone <- err
	}()
	<-locked
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	state, err := UpdateAPIKey(ctx, path, APIKeyUpdate{Provider: "openai", APIKey: &latestKey})
	if state != CommitNotApplied || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("contended cancellation = %q, %v elapsed=%v", state, err, time.Since(start))
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	state, err = UpdateAPIKey(t.Context(), path, APIKeyUpdate{Provider: "openai", APIKey: &latestKey})
	if state != CommitDurable || err != nil {
		t.Fatalf("latest writer = %q, %v", state, err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(got), "api_key: 'latest'") {
		t.Fatalf("latest content = %q, %v", got, readErr)
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

func TestPanelRepair_CooperatingAuthWritersPreserveBothUpdates(t *testing.T) {
	path := privateFile(t, "providers: {}\n")
	firstKey, secondKey := "first-secret", "second-secret"
	firstLocked := make(chan struct{})
	secondAtLock := make(chan struct{})
	releaseFirst := make(chan struct{})
	var beforeLocks atomic.Int32
	oldHook := updateTestHook
	updateTestHook = func(phase string) error {
		switch phase {
		case "before-lock":
			if beforeLocks.Add(1) == 2 {
				close(secondAtLock)
			}
		case "after-lock":
			if beforeLocks.Load() == 1 {
				close(firstLocked)
				<-releaseFirst
			}
		}
		return nil
	}
	defer func() { updateTestHook = oldHook }()

	type result struct {
		state CommitState
		err   error
	}
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		state, err := UpdateAPIKey(t.Context(), path, APIKeyUpdate{Provider: "openai", APIKey: &firstKey})
		firstDone <- result{state, err}
	}()
	<-firstLocked
	go func() {
		state, err := UpdateAPIKey(t.Context(), path, APIKeyUpdate{Provider: "anthropic", APIKey: &secondKey})
		secondDone <- result{state, err}
	}()
	<-secondAtLock
	select {
	case got := <-secondDone:
		t.Fatalf("second writer passed held cooperative lock: %+v", got)
	default:
	}
	close(releaseFirst)
	for name, ch := range map[string]<-chan result{"first": firstDone, "second": secondDone} {
		got := <-ch
		if got.state != CommitDurable || got.err != nil {
			t.Fatalf("%s writer = %q, %v", name, got.state, got.err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "api_key: 'first-secret'") || !strings.Contains(string(body), "api_key: 'second-secret'") {
		t.Fatalf("cooperating updates were lost: %q, %v", body, err)
	}
}

func TestPanelRepair_AuthWriterErrorsNeverContainSecret(t *testing.T) {
	const secret = "SYNTHETIC-FAULT-SECRET"
	for _, phase := range []string{"before-lock", "after-lock", "after-temp-sync", "before-compare", "after-rename"} {
		t.Run(phase, func(t *testing.T) {
			path := privateFile(t, "providers:\n  openai:\n    api_key: old\n")
			oldHook := updateTestHook
			updateTestHook = func(got string) error {
				if got == phase {
					return errors.New("injected safe fault")
				}
				return nil
			}
			t.Cleanup(func() { updateTestHook = oldHook })
			key := secret
			state, err := UpdateAPIKey(t.Context(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("phase %s = %q, %v", phase, state, err)
			}
			if phase == "after-rename" && state != CommitReplacementAppliedDurabilityUnknown {
				t.Fatalf("post-rename state = %q", state)
			}
			if phase != "after-rename" && state != CommitNotApplied {
				t.Fatalf("pre-rename state = %q", state)
			}
		})
	}
	t.Run("target-mismatch", func(t *testing.T) {
		path := privateFile(t, "providers:\n  openai:\n    api_key: old\n")
		oldHook := updateTestHook
		updateTestHook = func(phase string) error {
			if phase == "before-compare" {
				return os.WriteFile(path, []byte("providers:\n  openai:\n    api_key: "+secret+"\n"), 0o600)
			}
			return nil
		}
		t.Cleanup(func() { updateTestHook = oldHook })
		key := secret
		state, err := UpdateAPIKey(t.Context(), path, APIKeyUpdate{Provider: "openai", APIKey: &key})
		if state != CommitNotApplied || err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("mismatch = %q, %v", state, err)
		}
	})
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
