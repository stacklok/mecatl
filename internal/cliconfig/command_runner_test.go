package cliconfig

import (
	"os"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestCommandRunnerConfig_Scenario1_CLIAndDisablePrecedence(t *testing.T) {
	resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{testCommandRunnerSettings(t, "command_runner:\n  shell: /settings/sh\n")}})

	got, err := ResolveCommandRunnerShell(resolver, "/bin/sh", false)
	if err != nil || got != "/settings/sh" {
		t.Fatalf("settings shell = %q, %v", got, err)
	}
	got, err = ResolveCommandRunnerShell(resolver, "", true)
	if err != nil || got != "" {
		t.Fatalf("explicit empty shell = %q, %v", got, err)
	}
	got, err = ResolveCommandRunnerShell(resolver, "/cli/sh", true)
	if err != nil || got != "/cli/sh" {
		t.Fatalf("explicit shell = %q, %v", got, err)
	}

	for _, noShell := range []bool{false, true} {
		created := CommandRunnerEnabled(got, noShell)
		if created == noShell {
			t.Fatalf("CommandRunnerEnabled(shell, noShell=%v) = %v", noShell, created)
		}
	}
}

func testCommandRunnerSettings(t *testing.T, body string) string {
	t.Helper()
	path := t.TempDir() + "/settings.yaml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
