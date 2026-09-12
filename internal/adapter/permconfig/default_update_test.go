//go:build linux

package permconfig

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

func TestFinalReview_DefaultWriterRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.yaml")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct {
		state authfile.CommitState
		err   error
	}, 1)
	go func() {
		state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "openai", Model: "gpt"})
		done <- struct {
			state authfile.CommitState
			err   error
		}{state, err}
	}()
	select {
	case got := <-done:
		if got.state != authfile.CommitNotApplied || got.err == nil {
			t.Fatalf("FIFO update = %q, %v", got.state, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO update blocked waiting for a writer")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("models: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "openai", Model: "gpt"})
	if state != authfile.CommitDurable || err != nil {
		t.Fatalf("lock was not released after FIFO rejection: %q, %v", state, err)
	}
}

func TestFinalReview_SettingsConventionalParentLinkIsSyncedBeforeCommit(t *testing.T) {
	for _, faultPhase := range []string{"before-created-parent-sync", "before-created-parent-close"} {
		t.Run(faultPhase, func(t *testing.T) {
			config := t.TempDir()
			if err := os.Chmod(config, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_CONFIG_HOME", config)
			path := filepath.Join(config, UserSettingsRelPath)
			oldHook := defaultUpdateTestHook
			defaultUpdateTestHook = func(phase string) error {
				if phase == faultPhase {
					return errors.New("injected parent durability failure")
				}
				return nil
			}
			t.Cleanup(func() { defaultUpdateTestHook = oldHook })
			state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "openai", Model: "gpt"})
			if state != authfile.CommitNotApplied || err == nil {
				t.Fatalf("created-parent fault = %q, %v", state, err)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("target committed after parent fault: %v", statErr)
			}
		})
	}
}

func TestFinalReview_SettingsConventionalParentCreationRejectsUnsafeRaceAndMissingBase(t *testing.T) {
	t.Run("unsafe pre-existing child", func(t *testing.T) {
		config := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", config)
		if err := os.Mkdir(filepath.Join(config, "mecatl"), 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(config, UserSettingsRelPath)
		state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "openai", Model: "gpt"})
		if state != authfile.CommitNotApplied || err == nil {
			t.Fatalf("unsafe creation race = %q, %v", state, err)
		}
	})
	t.Run("missing base is not recursively created", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "missing", "config")
		t.Setenv("XDG_CONFIG_HOME", base)
		path := filepath.Join(base, UserSettingsRelPath)
		state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "openai", Model: "gpt"})
		if state != authfile.CommitNotApplied || err == nil {
			t.Fatalf("missing base = %q, %v", state, err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "missing")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("writer recursively created config ancestors: %v", statErr)
		}
	})
}

func TestUpdateDefaultsStaysAnchoredWhenParentPathIsReplaced(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "mecatl")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "settings.yaml")
	if err := os.WriteFile(path, []byte("models:\n  default_provider: old\n  default: old/model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "mecatl-original")
	oldHook := defaultUpdateTestHook
	defer func() { defaultUpdateTestHook = oldHook }()
	defaultUpdateTestHook = func(phase string) error {
		if phase != "after-lock" {
			return nil
		}
		if err := os.Rename(parent, moved); err != nil {
			return err
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("models:\n  default_provider: decoy\n  default: decoy/model\n"), 0o600)
	}

	state, err := UpdateDefaults(context.Background(), path, DefaultUpdate{Provider: "openai", Model: "openai/new"})
	if state != authfile.CommitDurable || err != nil {
		t.Fatalf("UpdateDefaults = %q, %v", state, err)
	}
	original, err := os.ReadFile(filepath.Join(moved, "settings.yaml"))
	if err != nil || !strings.Contains(string(original), "default: 'openai/new'") {
		t.Fatalf("anchored target was not updated: %q, %v", original, err)
	}
	decoy, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(decoy), "default: decoy/model") {
		t.Fatalf("replacement parent was modified or synced as target: %q, %v", decoy, err)
	}
}

func TestPanelRepair_CooperatingDefaultWritersUseLatestFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte("posture: strict\nmodels:\n  default_provider: old\n  default: old/model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstLocked := make(chan struct{})
	secondAtLock := make(chan struct{})
	releaseFirst := make(chan struct{})
	var beforeLocks atomic.Int32
	var afterRenames atomic.Int32
	oldHook := defaultUpdateTestHook
	defaultUpdateTestHook = func(phase string) error {
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
		case "after-rename":
			if afterRenames.Add(1) == 1 {
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				return os.WriteFile(path, append(body, []byte("first_writer_note: preserved\n")...), 0o600)
			}
		}
		return nil
	}
	defer func() { defaultUpdateTestHook = oldHook }()

	type result struct {
		state authfile.CommitState
		err   error
	}
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "openai", Model: "openai/first"})
		firstDone <- result{state, err}
	}()
	<-firstLocked
	go func() {
		state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "anthropic", Model: "anthropic/latest"})
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
		if got.state != authfile.CommitDurable || got.err != nil {
			t.Fatalf("%s writer = %q, %v", name, got.state, got.err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"default_provider: 'anthropic'", "default: 'anthropic/latest'", "first_writer_note: preserved", "posture: strict"} {
		if !strings.Contains(text, want) {
			t.Fatalf("latest settings lost %q:\n%s", want, text)
		}
	}
}

func TestPanelRepair_DefaultWriterErrorsNeverContainSecret(t *testing.T) {
	const secret = "SYNTHETIC-SETTINGS-SECRET"
	for _, phase := range []string{"before-lock", "after-lock", "after-temp-sync", "before-compare", "after-rename"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "settings.yaml")
			if err := os.WriteFile(path, []byte("private_note: "+secret+"\nmodels:\n  default_provider: old\n  default: old/model\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			oldHook := defaultUpdateTestHook
			defaultUpdateTestHook = func(got string) error {
				if got == phase {
					return errors.New("injected safe fault")
				}
				return nil
			}
			t.Cleanup(func() { defaultUpdateTestHook = oldHook })
			state, err := UpdateDefaults(t.Context(), path, DefaultUpdate{Provider: "openai", Model: "gpt-5-mini"})
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("phase %s = %q, %v", phase, state, err)
			}
			if phase == "after-rename" && state != authfile.CommitReplacementAppliedDurabilityUnknown {
				t.Fatalf("post-rename state = %q", state)
			}
			if phase != "after-rename" && state != authfile.CommitNotApplied {
				t.Fatalf("pre-rename state = %q", state)
			}
		})
	}
}

func TestMecatuiLocalProviderSetup_Scenario4_NarrowDefaultMutation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.yaml")
	const original = "# operator settings\nposture: strict\nmodels:\n  # selected provider\n  default_provider: old\n  default: old/model\n  aliases:\n    fast: openai/fast\n  slots:\n    compaction: tiny\n  router:\n    classifier-slot: cheap\n    default-category: small\n    categories:\n      - name: small\n        description: small tasks\n        model: openai/fast\n  allowlist: [openai/fast]\nopenrouter:\n  models:\n    openrouter/model:\n      order: [a, b]\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := UpdateDefaults(context.Background(), path, DefaultUpdate{Provider: "openai", Model: "openai/gpt-test"})
	if err != nil || state != authfile.CommitDurable {
		t.Fatalf("UpdateDefaults = %q, %v", state, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	parsed, err := parseYAML(got)
	if err != nil || parsed.Models == nil || parsed.Models.DefaultProvider != "openai" || parsed.Models.Default != "openai/gpt-test" {
		t.Fatalf("updated defaults are not nested under models: %+v, %v\n%s", parsed.Models, err, text)
	}
	for _, want := range []string{"# operator settings", "# selected provider", "posture: strict", "aliases:", "slots:", "router:", "allowlist:", "openrouter:", "models:", "default_provider: 'openai'", "default: 'openai/gpt-test'"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output lost %q:\n%s", want, text)
		}
	}

	oldHook := defaultUpdateTestHook
	defer func() { defaultUpdateTestHook = oldHook }()
	for _, phase := range []string{"after-lock", "after-temp-sync"} {
		t.Run("cancel-"+phase, func(t *testing.T) {
			cancelDir := t.TempDir()
			if err := os.Chmod(cancelDir, 0o700); err != nil {
				t.Fatal(err)
			}
			cancelPath := filepath.Join(cancelDir, "settings.yaml")
			original := []byte("models:\n  default_provider: old\n  default: old/model\n")
			if err := os.WriteFile(cancelPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defaultUpdateTestHook = func(got string) error {
				if got == phase {
					cancel()
				}
				return nil
			}
			state, err := UpdateDefaults(ctx, cancelPath, DefaultUpdate{Provider: "openai", Model: "openai/new"})
			if state != authfile.CommitNotApplied || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled update = %q, %v", state, err)
			}
			got, readErr := os.ReadFile(cancelPath)
			if readErr != nil || !bytes.Equal(got, original) {
				t.Fatalf("cancelled update changed target: %q, %v", got, readErr)
			}
		})
	}
	defaultUpdateTestHook = func(phase string) error {
		if phase == "before-compare" {
			return os.WriteFile(path, []byte("models: {}\n"), 0o600)
		}
		return nil
	}
	state, err = UpdateDefaults(context.Background(), path, DefaultUpdate{Provider: "anthropic", Model: "anthropic/test"})
	if state != authfile.CommitNotApplied || err == nil {
		t.Fatalf("mismatch = %q, %v", state, err)
	}

	defaultUpdateTestHook = func(phase string) error {
		if phase == "after-rename" {
			return errors.New("injected directory sync failure")
		}
		return nil
	}
	state, err = UpdateDefaults(context.Background(), path, DefaultUpdate{Provider: "anthropic", Model: "anthropic/test"})
	if state != authfile.CommitReplacementAppliedDurabilityUnknown || err == nil {
		t.Fatalf("post-rename failure = %q, %v", state, err)
	}
}
