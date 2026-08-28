package platform

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCurrentOverridePrecedenceAndFallback(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		override string
		want     Platform
	}{
		{name: "mac override wins on linux", goos: "linux", override: "mac", want: Mac},
		{name: "pc override wins on darwin", goos: "darwin", override: "pc", want: PC},
		{name: "invalid override falls back to darwin", goos: "darwin", override: "other", want: Mac},
		{name: "empty override falls back to non-darwin", goos: "linux", want: PC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := current(tc.goos, func(string) string { return tc.override })
			if got != tc.want {
				t.Fatalf("current(%q, override %q) = %v, want %v", tc.goos, tc.override, got, tc.want)
			}
		})
	}
}

func TestCurrentUsesEnvironmentOverride(t *testing.T) {
	const helper = "MECATUI_PLATFORM_CURRENT_HELPER"
	if want := os.Getenv(helper); want != "" {
		if got := Current().String(); got != want {
			t.Fatalf("Current() = %q, want environment override %q", got, want)
		}
		return
	}

	for _, want := range []string{"mac", "pc"} {
		t.Run(want, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCurrentUsesEnvironmentOverride$")
			cmd.Env = controlledTestEnv(os.Environ(),
				helper+"="+want,
				"MECATUI_TEST_PLATFORM="+want,
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Current helper failed: %v\n%s", err, output)
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

func TestPlatformString(t *testing.T) {
	if got := PC.String(); got != "pc" {
		t.Errorf("PC.String() = %q, want %q", got, "pc")
	}
	if got := Mac.String(); got != "mac" {
		t.Errorf("Mac.String() = %q, want %q", got, "mac")
	}
}

func TestScrollKeysMarkingNonMac(t *testing.T) {
	if got := scrollKeysMarking(PC); got != "pgup/pgdn" {
		t.Fatalf("scrollKeysMarking(PC) = %q, want %q", got, "pgup/pgdn")
	}
}

func TestScrollKeysMarkingMac(t *testing.T) {
	got := scrollKeysMarking(Mac)
	if !strings.Contains(got, "pgup/pgdn") {
		t.Errorf("scrollKeysMarking(Mac) = %q, want substring %q", got, "pgup/pgdn")
	}
	if !strings.Contains(got, "fn+↑") {
		t.Errorf("scrollKeysMarking(Mac) = %q, want substring %q", got, "fn+↑")
	}
}
