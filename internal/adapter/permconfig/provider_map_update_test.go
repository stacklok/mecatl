//go:build linux || darwin

package permconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

func TestUpdateProviderMap_AddReplaceRemovePreservesSettings(t *testing.T) {
	path := providerMapSettings(t, "permissions:\n  deny: [Shell]\nmodels:\n  default_provider: openai\n  default: gpt-5\nproviders:\n  old:\n    base_url: https://old.example/v1\n    default_model: old-model\n    api_flavor: openai-responses\n    auth: {method: none}\nunknown_top_level: preserved\n")

	add := ProviderMapUpdate{Provider: "new", Definition: &ProviderDefinition{
		BaseURL: "https://new.example/v1", DefaultModel: "new-model", APIFlavor: "openai-chat-completions", Auth: ProviderAuth{Method: "api_key"},
	}}
	if state, err := UpdateProviderMap(context.Background(), path, add); err != nil || state != authfile.CommitDurable {
		t.Fatalf("add = (%v, %v), want durable success", state, err)
	}
	if state, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "old", Definition: &ProviderDefinition{
		BaseURL: "https://replaced.example/v1", DefaultModel: "replaced-model", APIFlavor: "anthropic-messages", Auth: ProviderAuth{Method: "none"},
	}}); err != nil || state != authfile.CommitDurable {
		t.Fatalf("replace = (%v, %v), want durable success", state, err)
	}
	if state, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "new"}); err != nil || state != authfile.CommitDurable {
		t.Fatalf("remove = (%v, %v), want durable success", state, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(data)
	for _, want := range []string{"deny: [Shell]", "default_provider: openai", "unknown_top_level: preserved", "old:", "https://replaced.example/v1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("settings missing preserved or replaced content %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "new:") {
		t.Fatalf("removed provider remains in settings:\n%s", text)
	}
	if err := ValidateYAML(data); err != nil {
		t.Fatalf("updated settings are invalid: %v", err)
	}
}

func TestUpdateProviderMap_RemovesSelectedDefaultWithoutRewritingIt(t *testing.T) {
	path := providerMapSettings(t, "models:\n  default_provider: custom\n  default: custom-model\nproviders:\n  custom:\n    base_url: https://custom.example\n    default_model: custom-model\n    api_flavor: openai-responses\n    auth: {method: none}\n")
	if state, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "custom"}); err != nil || state != authfile.CommitDurable {
		t.Fatalf("remove selected default = (%v, %v), want durable success", state, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "default_provider: custom") || strings.Contains(string(data), "custom:\n    base_url") {
		t.Fatalf("removal changed defaults or retained provider:\n%s", data)
	}
	if err := ValidateYAML(data); err != nil {
		t.Fatalf("removal wrote invalid settings: %v\n%s", err, data)
	}
}

func TestUpdateProviderMap_RejectsInvalidOrAmbiguousInput(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		update      ProviderMapUpdate
	}{
		{"invalid provider", "{}\n", ProviderMapUpdate{Provider: "OpenAI", Definition: &ProviderDefinition{}}},
		{"reserved provider", "{}\n", ProviderMapUpdate{Provider: "openai", Definition: &ProviderDefinition{}}},
		{"invalid definition", "{}\n", ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "http://bad.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}}},
		{"ambiguous providers", "providers:\n  custom: {base_url: https://one.example, default_model: one, api_flavor: openai-responses, auth: {method: none}}\n  custom: {base_url: https://two.example, default_model: two, api_flavor: openai-responses, auth: {method: none}}\n", ProviderMapUpdate{Provider: "other", Definition: &ProviderDefinition{BaseURL: "https://other.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := providerMapSettings(t, tc.input)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := UpdateProviderMap(context.Background(), path, tc.update); err == nil {
				t.Fatal("UpdateProviderMap succeeded")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("unsafe update changed settings: %v\n%s", err, after)
			}
		})
	}
}

func TestUpdateProviderMap_ChangedTargetAbortsWithoutLockArtifact(t *testing.T) {
	path := providerMapSettings(t, "providers: {}\n")
	providerMapUpdateTestHook = func(stage string) error {
		if stage == "before-compare" {
			return os.WriteFile(path, []byte("providers: {}\n# changed\n"), 0o600)
		}
		return nil
	}
	t.Cleanup(func() { providerMapUpdateTestHook = nil })

	_, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "https://custom.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}})
	if err == nil || err.Error() != "Configuration changed while this command was running; no changes were made. Review the file and retry." {
		t.Fatalf("error = %v, want changed-target error", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".lock") {
			t.Fatalf("unexpected lock artifact %q", entry.Name())
		}
	}
}

func TestUpdateProviderMap_RejectsUnsafeTargetsAndCancellation(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "target.yaml")
		if err := os.WriteFile(target, []byte("providers: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "settings.yaml")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "https://custom.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}}); err == nil {
			t.Fatal("symlink target accepted")
		}
	})
	t.Run("special file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "settings.yaml")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateProviderMap(context.Background(), path, ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "https://custom.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}}); err == nil {
			t.Fatal("special target accepted")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		path := providerMapSettings(t, "providers: {}\n")
		state, err := UpdateProviderMap(ctx, path, ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "https://custom.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}})
		if state != authfile.CommitNotApplied || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled update = (%v, %v), want not-applied cancellation", state, err)
		}
	})
	t.Run("cancelled before replacement", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		path := providerMapSettings(t, "providers: {}\n")
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		providerMapUpdateTestHook = func(stage string) error {
			if stage == "after-temp-sync" {
				cancel()
			}
			return nil
		}
		t.Cleanup(func() { providerMapUpdateTestHook = nil })
		state, err := UpdateProviderMap(ctx, path, ProviderMapUpdate{Provider: "custom", Definition: &ProviderDefinition{BaseURL: "https://custom.example", DefaultModel: "m", APIFlavor: "openai-responses", Auth: ProviderAuth{Method: "none"}}})
		if state != authfile.CommitNotApplied || !errors.Is(err, context.Canceled) {
			t.Fatalf("late cancellation = (%v, %v), want not-applied cancellation", state, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("late cancellation changed settings: %v\n%s", err, after)
		}
	})
}

func providerMapSettings(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
