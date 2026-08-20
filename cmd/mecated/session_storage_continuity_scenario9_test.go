package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

const scenario9OperationsDoc = "../../user-docs/building/deployment/session-storage-operations.md"

func TestSessionStorageContinuity_Scenario9_ServiceExamplesExecuteConfiguredArgs(t *testing.T) {
	body := readScenario9OperationsDoc(t)

	systemd := scenario9Block(t, body, "scenario9-systemd")
	if strings.Count(systemd, "ExecStart=") != 1 {
		t.Fatalf("systemd example has %d ExecStart directives, want 1", strings.Count(systemd, "ExecStart="))
	}
	systemdArgs := strings.Fields(strings.TrimPrefix(lineWithPrefix(t, systemd, "ExecStart="), "ExecStart="))
	wantSystemd := []string{
		"/usr/local/bin/mecated", "serve",
		"--permission-config", "/home/operator/.config/mecatl/settings.yaml",
		"--store-dir", "/home/operator/.local/state/mecatl/sessions",
	}
	projectedSystemd := make([]string, len(systemdArgs))
	for i, arg := range systemdArgs {
		projectedSystemd[i] = strings.ReplaceAll(arg, "%h", "/home/operator")
	}
	if !reflect.DeepEqual(projectedSystemd, wantSystemd) {
		t.Fatalf("systemd argv = %#v, want %#v", projectedSystemd, wantSystemd)
	}
	assertScenario9CLIArgs(t, projectedSystemd, wantSystemd[3], wantSystemd[5])

	launchdArgs := parseLaunchdProgramArguments(t, scenario9Block(t, body, "scenario9-launchd"))
	wantLaunchd := []string{
		"/usr/local/bin/mecated", "serve",
		"--permission-config", "/Users/operator/Library/Application Support/mecatl/settings.yaml",
		"--store-dir", "/Users/operator/Library/Application Support/mecatl/sessions",
	}
	for i := range launchdArgs {
		launchdArgs[i] = strings.ReplaceAll(launchdArgs[i], "/Users/USERNAME", "/Users/operator")
	}
	if !reflect.DeepEqual(launchdArgs, wantLaunchd) {
		t.Fatalf("launchd ProgramArguments = %#v, want %#v", launchdArgs, wantLaunchd)
	}
	assertScenario9CLIArgs(t, launchdArgs, wantLaunchd[3], wantLaunchd[5])

	settingsPath := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(settingsPath, []byte(scenario9Block(t, body, "scenario9-retention")), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := permconfig.New(permconfig.Options{ExplicitFiles: []string{settingsPath}})
	retention, err := resolver.OperatorRetention()
	if err != nil {
		t.Fatalf("parse documented retention settings: %v", err)
	}
	if retention == nil || retention.Version != 1 || retention.Child.MaxAge != "168h" || retention.SweepCadence != "1h" || retention.AcknowledgeMainDeletion {
		t.Fatalf("documented retention policy parsed unexpectedly: %+v", retention)
	}
}

func TestSessionStorageContinuity_Scenario9_NoUnsafeDeletionRecipe(t *testing.T) {
	body := readScenario9OperationsDoc(t)
	for _, want := range []string{
		"Do not use cron", "`find`", "filesystem globs", "daemon-owned retention",
		"Embedded mecatui", "`mecatui connect`", "management capability",
		"effective policy", "dry run", "before apply",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("operations guide missing %q", want)
		}
	}
	code := strings.ToLower(scenario9FencedCode(body))
	for _, forbidden := range []string{"rm -", "find ", "-delete", "crontab", "curl -x delete", "cleanup/apply"} {
		if strings.Contains(code, forbidden) {
			t.Errorf("operations guide contains destructive command recipe %q", forbidden)
		}
	}
}

func TestSessionStorageContinuity_Scenario9_BackupMigrationRunbook(t *testing.T) {
	body := readScenario9OperationsDoc(t)
	for _, want := range []string{
		"plaintext", "0700", "0600", "free space", "temporary-space estimate",
		"unsupported backend", "restore to a new directory", "read-only validation",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Errorf("operations guide missing runbook requirement %q", want)
		}
	}
	assertScenario9Order(t, body,
		"1. **Stop and quiesce.**",
		"2. **Back up.**",
		"3. **Forecast and plan.**",
		"4. **Migrate or apply.**",
		"5. **Verify.**",
		"6. **Start.**",
	)
}

func assertScenario9CLIArgs(t *testing.T, argv []string, wantConfig, wantStore string) {
	t.Helper()
	if len(argv) < 2 || argv[1] != "serve" {
		t.Fatalf("service argv does not invoke mecated serve: %#v", argv)
	}
	cfg, err := parseFlags(argv[2:])
	if err != nil {
		t.Fatalf("parse documented service argv: %v", err)
	}
	if cfg.storeDir != wantStore || len(cfg.permissionConfigs) != 1 || cfg.permissionConfigs[0] != wantConfig {
		t.Fatalf("parsed config/store paths = %#v / %q, want [%q] / %q", cfg.permissionConfigs, cfg.storeDir, wantConfig, wantStore)
	}
}

func readScenario9OperationsDoc(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(scenario9OperationsDoc)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func scenario9Block(t *testing.T, body, marker string) string {
	t.Helper()
	start := strings.Index(body, "{/* "+marker+" */}")
	if start < 0 {
		t.Fatalf("missing marker %q", marker)
	}
	fence := strings.Index(body[start:], "```")
	if fence < 0 {
		t.Fatalf("missing fenced example after %q", marker)
	}
	start += fence
	start = strings.Index(body[start:], "\n") + start + 1
	end := strings.Index(body[start:], "```")
	if end < 0 {
		t.Fatalf("unterminated fenced example after %q", marker)
	}
	return strings.TrimSpace(body[start : start+end])
}

func lineWithPrefix(t *testing.T, body, prefix string) string {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("missing line with prefix %q", prefix)
	return ""
}

func parseLaunchdProgramArguments(t *testing.T, body string) []string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(body))
	var args []string
	wantArray, inArray := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			if len(args) == 0 {
				t.Fatalf("parse launchd plist: %v", err)
			}
			return args
		}
		switch v := tok.(type) {
		case xml.StartElement:
			if v.Name.Local == "key" {
				var key string
				if err := dec.DecodeElement(&key, &v); err != nil {
					t.Fatal(err)
				}
				wantArray = key == "ProgramArguments"
			} else if v.Name.Local == "array" && wantArray {
				inArray, wantArray = true, false
			} else if v.Name.Local == "string" && inArray {
				var arg string
				if err := dec.DecodeElement(&arg, &v); err != nil {
					t.Fatal(err)
				}
				args = append(args, arg)
			}
		case xml.EndElement:
			if v.Name.Local == "array" && inArray {
				return args
			}
		}
	}
}

func scenario9FencedCode(body string) string {
	var code strings.Builder
	inFence := false
	for part := range strings.SplitSeq(body, "```") {
		if inFence {
			code.WriteString(part)
			code.WriteByte('\n')
		}
		inFence = !inFence
	}
	return code.String()
}

func assertScenario9Order(t *testing.T, body string, steps ...string) {
	t.Helper()
	last := -1
	for _, step := range steps {
		next := strings.Index(body, step)
		if next <= last {
			t.Fatalf("runbook step %q is missing or out of order", step)
		}
		last = next
	}
}
