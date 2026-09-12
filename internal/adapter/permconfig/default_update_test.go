package permconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

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
	for _, want := range []string{"# operator settings", "# selected provider", "posture: strict", "aliases:", "slots:", "router:", "allowlist:", "openrouter:", "models:", "default_provider: 'openai'", "default: 'openai/gpt-test'"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output lost %q:\n%s", want, text)
		}
	}

	oldHook := defaultUpdateTestHook
	defer func() { defaultUpdateTestHook = oldHook }()
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
