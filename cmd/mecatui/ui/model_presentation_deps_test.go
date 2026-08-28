package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func normalizeTestModelDeps(deps Deps) Deps {
	if deps.emojiCapable == nil {
		deps.emojiCapable = func() bool { return false }
	}
	if deps.kittyCapable == nil {
		deps.kittyCapable = func() bool { return false }
	}
	if deps.scrollKeysMarking == nil {
		deps.scrollKeysMarking = func() string { return "pgup/pgdn" }
	}
	return deps
}

func newTestModelFromDeps(deps Deps) Model {
	return New(normalizeTestModelDeps(deps))
}

func TestNormalizeTestModelDepsIsDeterministic(t *testing.T) {
	deps := normalizeTestModelDeps(Deps{})
	if deps.emojiCapable() || deps.kittyCapable() {
		t.Fatal("test presentation capabilities should be disabled")
	}
	if got := deps.scrollKeysMarking(); got != "pgup/pgdn" {
		t.Fatalf("test scroll-key marking = %q, want %q", got, "pgup/pgdn")
	}
}

func TestNormalizeTestModelDepsPreservesOverrides(t *testing.T) {
	deps := normalizeTestModelDeps(Deps{
		emojiCapable:      func() bool { return true },
		kittyCapable:      func() bool { return true },
		scrollKeysMarking: func() string { return "custom" },
	})
	if !deps.emojiCapable() || !deps.kittyCapable() {
		t.Fatal("test dependency normalization replaced capability overrides")
	}
	if got := deps.scrollKeysMarking(); got != "custom" {
		t.Fatalf("test scroll-key marking = %q, want %q", got, "custom")
	}
}

// These tests intentionally call New directly to cover production nil/default
// normalization and dependency call timing.
func TestNewNormalizesPresentationDependencies(t *testing.T) {
	m := New(Deps{})
	if m.deps.emojiCapable == nil || m.deps.kittyCapable == nil || m.deps.scrollKeysMarking == nil {
		t.Fatal("New left a presentation dependency nil")
	}
	if got := m.deps.scrollKeysMarking(); got == "" {
		t.Fatal("default scroll-key marking is empty")
	}
}

func TestNewUsesEnvironmentBackedPresentationDefaults(t *testing.T) {
	const helper = "MECATUI_PRESENTATION_DEFAULTS_HELPER"
	if scenario := os.Getenv(helper); scenario != "" {
		m := New(Deps{})
		wantEnabled := scenario == "enabled"
		if m.emojiOK != wantEnabled {
			t.Fatalf("New environment-backed emoji result = %v, want %v", m.emojiOK, wantEnabled)
		}
		if got := m.deps.kittyCapable(); got != wantEnabled {
			t.Fatalf("New environment-backed Kitty result = %v, want %v", got, wantEnabled)
		}
		wantMarking := "pgup/pgdn"
		if wantEnabled {
			wantMarking = "fn+↑/fn+↓ (pgup/pgdn)"
		}
		if got := m.deps.scrollKeysMarking(); got != wantMarking {
			t.Fatalf("New default scroll-key marking = %q, want %q", got, wantMarking)
		}
		return
	}

	cases := []struct {
		name     string
		scenario string
		noEmoji  string
		noKitty  string
		platform string
	}{
		{name: "forced capabilities on Mac", scenario: "enabled", noEmoji: "0", noKitty: "0", platform: "mac"},
		{name: "disabled capabilities on PC", scenario: "disabled", noEmoji: "1", noKitty: "1", platform: "pc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestNewUsesEnvironmentBackedPresentationDefaults$")
			cmd.Env = controlledTestEnv(os.Environ(),
				helper+"="+tc.scenario,
				"MECATUI_FORCE_EMOJI=1",
				"MECATUI_NO_EMOJI="+tc.noEmoji,
				"MECATUI_FORCE_KITTY=1",
				"MECATUI_NO_KITTY="+tc.noKitty,
				"MECATUI_TEST_PLATFORM="+tc.platform,
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("presentation-default helper failed: %v\n%s", err, output)
			}
		})
	}
}

func controlledTestEnv(base []string, overrides ...string) []string {
	replaced := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		key, _, _ := strings.Cut(entry, "=")
		replaced[key] = struct{}{}
	}

	env := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := replaced[key]; !ok {
			env = append(env, entry)
		}
	}
	return append(env, overrides...)
}

func TestNewPresentationDependencyTiming(t *testing.T) {
	emojiCalls, kittyCalls, markingCalls := 0, 0, 0
	m := New(Deps{
		emojiCapable: func() bool {
			emojiCalls++
			return true
		},
		kittyCapable: func() bool {
			kittyCalls++
			return false
		},
		scrollKeysMarking: func() string {
			markingCalls++
			return "test-scroll"
		},
	})
	if emojiCalls != 1 || !m.emojiOK {
		t.Fatalf("emoji detector calls = %d, emojiOK = %v; want one call and true", emojiCalls, m.emojiOK)
	}
	if kittyCalls != 0 {
		t.Fatalf("Kitty detector called during New: %d", kittyCalls)
	}
	if markingCalls != 1 {
		t.Fatalf("scroll marking calls during New = %d, want 1", markingCalls)
	}
	if got := m.helpKeyMarkings().scroll; got != "test-scroll" {
		t.Fatalf("model help scroll marking = %q, want %q", got, "test-scroll")
	}
	if markingCalls != 2 {
		t.Fatalf("scroll marking calls after model help = %d, want 2", markingCalls)
	}

	m.phase = phaseIdle
	m.width, m.height = 100, 40
	m.vp.SetHeight(30)
	_ = m.maybeKittyTransmit()
	if kittyCalls != 1 {
		t.Fatalf("Kitty detector calls after transmit check = %d, want 1", kittyCalls)
	}
}
