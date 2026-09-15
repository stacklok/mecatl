package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func TestCommandRunnerConfig_Scenario1_SettingsShellDefault(t *testing.T) {
	res := newWithEnv(Options{Conventional: true}, envWithUserSettings("command_runner:\n  shell: /opt/operator/sh\n"))
	got, err := res.OperatorCommandRunner()
	if err != nil {
		t.Fatalf("OperatorCommandRunner: %v", err)
	}
	if got == nil || got.Shell != "/opt/operator/sh" {
		t.Fatalf("command runner = %+v, want configured shell", got)
	}
}

func TestCommandRunnerConfig_Scenario1_StrictOperatorOnlyValidation(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":   "command_runner:\n  typo: true\n",
		"nested":    "command_runner:\n  environment:\n    typo: true\n",
		"malformed": "command_runner:\n  environment:\n    inherit: [GOOD, bad-name]\n",
		"duplicate": "command_runner:\n  environment:\n    inherit: [GH_TOKEN, GH_TOKEN]\n",
	} {
		t.Run(name, func(t *testing.T) {
			res := newWithEnv(Options{Conventional: true}, envWithUserSettings(body))
			if _, err := res.OperatorCommandRunner(); err == nil {
				t.Fatalf("invalid command_runner accepted")
			}
		})
	}

	res := newWithEnv(Options{Conventional: true}, envWithUserSettings("command_runner:\n  shell: \"\"\n"))
	got, err := res.OperatorCommandRunner()
	if err != nil || got == nil || got.Shell != "" || !got.ShellSet {
		t.Fatalf("empty shell = %+v, %v; want present empty setting", got, err)
	}

	const secret = "project-value-MUST-NOT-LEAK"
	var logs bytes.Buffer
	diag := slogdiag.New(&logs, false, port.LevelDebug)
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "command_runner:\n  shell: "+secret+"\n  environment:\n    inherit: [GH_TOKEN]\n")
	res = newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, envWithUserSettings("command_runner:\n  shell: /bin/operator-sh\n"))
	_ = res.Resolve(context.Background(), ws)
	got, err = res.OperatorCommandRunner()
	if err != nil || got == nil || got.Shell != "/bin/operator-sh" {
		t.Fatalf("project command runner changed operator config: %+v, %v", got, err)
	}
	if text := logs.String(); !strings.Contains(text, "command_runner: IGNORING a project-tier command_runner block") || strings.Contains(text, secret) {
		t.Fatalf("project warning missing or leaked value: %s", text)
	}
}
