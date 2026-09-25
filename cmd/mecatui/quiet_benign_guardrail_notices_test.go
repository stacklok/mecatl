package main

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

func TestQuietBenignGuardrailNotices_Scenario3_ClientSetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings, err := readClientSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.HookNotices.ShowBenign {
		t.Fatal("missing hook_notices must default show_benign to false")
	}
	writeSettings(t, "mecatui", "hook_notices: {}\n")
	settings, err = readClientSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.HookNotices.ShowBenign {
		t.Fatal("missing show_benign must default to false")
	}
	writeSettings(t, "mecatui", "hook_notices:\n  show_benign: true\n")
	settings, err = readClientSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.HookNotices.ShowBenign {
		t.Fatal("show_benign: true was not parsed")
	}
	var deps ui.Deps
	applyClientPresentationSettings(settings, &deps)
	if !deps.ShowBenignHookNotices {
		t.Fatal("show_benign: true was not applied to ui dependencies")
	}
}

func TestQuietBenignGuardrailNotices_Scenario3_StrictConfigOwnership(t *testing.T) {
	for name, body := range map[string]string{
		"unknown member": "hook_notices:\n  show_benign: false\n  secret_member: DO-NOT-ECHO\n",
		"non boolean":    "hook_notices:\n  show_benign: DO-NOT-ECHO\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			writeSettings(t, "mecatui", body)
			_, err := readClientSettings()
			if err == nil {
				t.Fatal("invalid hook_notices setting was accepted")
			}
			if strings.Contains(err.Error(), "DO-NOT-ECHO") {
				t.Fatalf("configuration value leaked: %v", err)
			}
		})
	}
}

func TestQuietBenignGuardrailNotices_Scenario3_DeprecatedMecatlSettingsIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeSettings(t, "mecatl", "hook_notices:\n  show_benign: true\n")
	settings, err := readClientSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.HookNotices.ShowBenign {
		t.Fatal("deprecated mecatl settings enabled client hook notice policy")
	}
	var deps ui.Deps
	applyClientPresentationSettings(settings, &deps)
	if deps.ShowBenignHookNotices {
		t.Fatal("deprecated mecatl settings reached ui dependencies")
	}
}
