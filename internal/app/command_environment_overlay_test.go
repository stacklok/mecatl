package app

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// TestADR_0281_TempOverlayPreservesSecretScrub pins the command-environment
// overlay boundary: each foreground and streaming invocation may receive only
// its temporary-storage values, while the composition-provided secret scrub
// remains in effect. The same overlay must reach both runner paths.
func TestADR_0281_TempOverlayPreservesSecretScrub(t *testing.T) {
	for _, name := range []string{
		"OPENROUTER_API_KEY",    // provider
		"GH_TOKEN",              // GitHub
		"AWS_SECRET_ACCESS_KEY", // cloud
		"GENERIC_API_KEY",       // generic API key
		"GENERIC_TOKEN",         // generic token
	} {
		t.Setenv(name, "MUST-NOT-LEAK")
	}

	cfg := teamCfg(t)
	runner := buildCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a command runner")
	}
	overlayRunner, ok := runner.(tool.CommandEnvironmentRunner)
	if !ok {
		t.Fatal("command runner does not support per-invocation environment overlays")
	}
	streamer, ok := runner.(tool.CommandEnvironmentStreamer)
	if !ok {
		t.Fatal("command runner does not support streaming environment overlays")
	}

	scopes := []struct {
		name    string
		overlay tool.CommandEnvironmentOverlay
		want    map[string]string
	}{
		{
			name: "managed",
			overlay: tool.CommandEnvironmentOverlay{
				TempDir:        "/managed/lease/tmp",
				GoTempDir:      "/managed/lease/tmp",
				TestHomeMarker: "/managed/lease",
			},
			want: map[string]string{
				"TMPDIR":                 "/managed/lease/tmp",
				"GOTMPDIR":               "/managed/lease/tmp",
				"MECATL_TEST_TEMP_LEASE": "/managed/lease",
			},
		},
		{
			name: "system",
			overlay: tool.CommandEnvironmentOverlay{
				TempDir:   "/system/tmp",
				GoTempDir: "/system/tmp",
			},
			want: map[string]string{
				"TMPDIR":   "/system/tmp",
				"GOTMPDIR": "/system/tmp",
			},
		},
	}

	var commonEnvironment string
	for _, scope := range scopes {
		t.Run(scope.name, func(t *testing.T) {
			foreground, err := overlayRunner.RunWithEnvironment(context.Background(), "env", scope.overlay)
			if err != nil {
				t.Fatalf("foreground RunWithEnvironment: %v", err)
			}
			if foreground.ExitCode != 0 {
				t.Fatalf("foreground exit code = %d, stderr = %s", foreground.ExitCode, foreground.Stderr)
			}

			var background bytes.Buffer
			exitCode, err := streamer.RunStreamingWithEnvironment(context.Background(), "env", scope.overlay, &background)
			if err != nil {
				t.Fatalf("background RunStreamingWithEnvironment: %v", err)
			}
			if exitCode != 0 {
				t.Fatalf("background exit code = %d", exitCode)
			}

			assertTempOverlayEnvironment(t, foreground.Stdout, scope.want)
			assertTempOverlayEnvironment(t, background.String(), scope.want)
			for _, output := range []string{foreground.Stdout, background.String()} {
				normalized := withoutTemporaryStorageEnvironment(output)
				if commonEnvironment == "" {
					commonEnvironment = normalized
				} else if normalized != commonEnvironment {
					t.Errorf("non-temporary command environment changed across invocation or scope")
				}
			}
		})
	}
}

func withoutTemporaryStorageEnvironment(output string) string {
	var entries []string
	for _, entry := range strings.Split(strings.TrimSpace(output), "\n") {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "TMPDIR", "GOTMPDIR", "MECATL_TEST_TEMP_LEASE":
			continue
		}
		entries = append(entries, entry)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

func assertTempOverlayEnvironment(t *testing.T, output string, want map[string]string) {
	t.Helper()
	for _, secret := range []string{
		"OPENROUTER_API_KEY", "GH_TOKEN", "AWS_SECRET_ACCESS_KEY", "GENERIC_API_KEY", "GENERIC_TOKEN",
	} {
		if strings.Contains(output, secret+"=") || strings.Contains(output, "MUST-NOT-LEAK") {
			t.Errorf("command environment leaked %q", secret)
		}
	}
	for name, value := range want {
		if !strings.Contains(output, name+"="+value) {
			t.Errorf("command environment missing temporary overlay %s=%q", name, value)
		}
	}
	for _, name := range []string{"TMPDIR", "GOTMPDIR", "MECATL_TEST_TEMP_LEASE"} {
		if _, expected := want[name]; !expected && strings.Contains(output, name+"=") {
			t.Errorf("command environment unexpectedly contains %s", name)
		}
	}
}
