package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/internal/app"
)

// The child owns a real embedded server; the parent observes the process stderr
// independently of the terminal and the server's shutdown.
func TestMecatuiExitHandoff_Scenario1_EmbeddedComposition(t *testing.T) {
	if os.Getenv("MECATUI_TEST_HANDOFF_COMPOSITION") == "" {
		checkEmbeddedCompositionChild(t, "available")
		return
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	userModelDir := os.Getenv("MECATUI_TEST_HANDOFF_USER_MODEL_DIR")
	workspace := os.Getenv("MECATUI_TEST_HANDOFF_WORKSPACE")
	if configHome == "" || runtimeDir == "" || userModelDir == "" || workspace == "" {
		t.Fatal("embedded composition requires synthetic test environment")
	}
	settingsDir := filepath.Join(configHome, "mecatl")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.yaml"), []byte("execution:\n  default_placement: "+app.PlacementHostLocal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := filepath.Glob(filepath.Join(runtimeDir, "mecatui-*", "mecated.sock"))
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool, len(before))
	for _, socket := range before {
		known[socket] = true
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	err = runWithOptions([]string{"mecatui", "--mock", "--quiet", "--no-store", "--no-memory", "--no-user-model", "--user-model-dir=" + userModelDir, "--no-soul", "--no-skills", "--no-commands", "--workspace=" + workspace}, runOptions{runProgram: func(_ context.Context, m ui.Model) (tea.Model, error) {
		sockets, err := filepath.Glob(filepath.Join(runtimeDir, "mecatui-*", "mecated.sock"))
		var fresh []string
		for _, socket := range sockets {
			if !known[socket] {
				fresh = append(fresh, socket)
			}
		}
		if err != nil || len(fresh) != 1 {
			t.Fatalf("embedded sockets=%v err=%v", fresh, err)
		}
		target := "unix://" + fresh[0]
		cl, err := client.Dial(client.DialConfig{Server: target})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cl.Close() }()
		first, _, _, err := cl.CreateSession(ctx, client.ModeFromString("default"), client.ModelSelection{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cl.RenameSession(ctx, first, "first title"); err != nil {
			t.Fatal(err)
		}
		m0, _ := m.Update(client.SessionReadyMsg{SessionID: first})
		m = m0.(ui.Model)
		final := seedStartupResumeSession(ctx, t, target, "")
		if final == first {
			t.Fatal("seed must switch sessions")
		}
		if _, err := cl.RenameSession(ctx, final, "final title"); err != nil {
			t.Fatal(err)
		}
		finalRecord, err := json.Marshal(final)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stderr.WriteString("handoff-child-final-id=" + string(finalRecord) + "\n")
		m0, _ = m.Update(client.SessionReadyMsg{SessionID: final})
		m = m0.(ui.Model)
		if m.ActiveSessionID() != final {
			t.Fatalf("active=%q final=%q", m.ActiveSessionID(), final)
		}
		if os.Getenv("MECATUI_TEST_HANDOFF_COMPOSITION") == "missing" {
			m0, _ = m.Update(client.SessionReadyMsg{SessionID: "missing-final"})
			m = m0.(ui.Model)
		}
		// Run an alternate-screen program before returning the actual UI model.
		if _, err := tea.NewProgram(handoffTestModel{}, tea.WithInput(nil), tea.WithOutput(os.Stderr)).Run(); err != nil {
			t.Fatal(err)
		}
		return m, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	sockets, err := filepath.Glob(filepath.Join(runtimeDir, "mecatui-*", "mecated.sock"))
	for _, socket := range sockets {
		if !known[socket] {
			t.Fatalf("embedded server not cleaned: %v %v", sockets, err)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stderr.WriteString("handoff-child-cleaned\n")
}

func compositionChildEnv(t *testing.T, scenario string) []string {
	t.Helper()
	root := compositionScratchRoot(t)
	paths := map[string]string{
		"HOME":                                filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":                     filepath.Join(root, "config"),
		"XDG_STATE_HOME":                      filepath.Join(root, "state"),
		"XDG_DATA_HOME":                       filepath.Join(root, "data"),
		"XDG_RUNTIME_DIR":                     filepath.Join(root, "runtime"),
		"MECATUI_TEST_HANDOFF_USER_MODEL_DIR": filepath.Join(root, "user-model"),
		"MECATUI_TEST_HANDOFF_WORKSPACE":      filepath.Join(root, "workspace"),
	}
	for name, dir := range paths {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	return []string{
		"HOME=" + paths["HOME"],
		"XDG_CONFIG_HOME=" + paths["XDG_CONFIG_HOME"],
		"XDG_STATE_HOME=" + paths["XDG_STATE_HOME"],
		"XDG_DATA_HOME=" + paths["XDG_DATA_HOME"],
		"XDG_RUNTIME_DIR=" + paths["XDG_RUNTIME_DIR"],
		"MECATUI_TEST_HANDOFF_USER_MODEL_DIR=" + paths["MECATUI_TEST_HANDOFF_USER_MODEL_DIR"],
		"MECATUI_TEST_HANDOFF_WORKSPACE=" + paths["MECATUI_TEST_HANDOFF_WORKSPACE"],
		"MECATUI_TEST_HANDOFF_COMPOSITION=" + scenario,
		"PATH=" + os.Getenv("PATH"),
	}
}

func compositionScratchRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			scratch := filepath.Join(dir, ".scratch")
			if err := os.MkdirAll(scratch, 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(scratch, "mh-")
			if err != nil {
				t.Fatal(err)
			}
			if len(filepath.Join(root, "runtime", "mecatui-0123456789", "mecated.sock")) < 100 {
				t.Cleanup(func() { _ = os.RemoveAll(root) })
				return root
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("no ancestor checkout has a short enough .scratch directory for embedded socket")
	return ""
}

func checkEmbeddedCompositionChild(t *testing.T, scenario string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestMecatuiExitHandoff_Scenario1_EmbeddedComposition$")
	cmd.Env = compositionChildEnv(t, scenario)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("embedded child: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "PASS\n" {
		t.Fatalf("stdout changed by program: %q", stdout.String())
	}
	got := stderr.String()
	line := strings.Index(got, finalSessionHandoffPrefix)
	if line < 1 || got[line-1] != '\n' || strings.Contains(got, "hosting an embedded mecated at") || strings.Count(got, finalSessionHandoffPrefix) != 1 || strings.LastIndex(got[:line], "\x1b[?1049l") < 0 || !strings.Contains(got[line:], "handoff-child-cleaned\n") {
		t.Fatalf("handoff must follow teardown with a separating line and no startup socket address: %q", got)
	}
	fields := strings.SplitN(got[line:], "\n", 2)
	var id string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(fields[0], finalSessionHandoffPrefix)), &id); err != nil || id == "" {
		t.Fatalf("invalid ID: %q: %v", fields[0], err)
	}
	const finalIDPrefix = "handoff-child-final-id="
	finalLine := strings.Index(got, finalIDPrefix)
	if finalLine < 0 {
		t.Fatalf("missing seeded final ID: %q", got)
	}
	var finalID string
	if err := json.Unmarshal([]byte(strings.SplitN(got[finalLine+len(finalIDPrefix):], "\n", 2)[0]), &finalID); err != nil {
		t.Fatalf("invalid seeded final ID: %v", err)
	}
	if scenario != "missing" && id != finalID {
		t.Fatalf("handoff ID = %q, want seeded final ID %q", id, finalID)
	}
	if !strings.Contains(got[line:], "Resume: mecatui --resume '"+id+"'\n") || !strings.Contains(got[line:], "Or: mecatui --resume-latest (may select a different chat)\n") {
		t.Fatalf("final ID command missing: %q", got[line:])
	}
	if scenario == "missing" {
		if id != "missing-final" || strings.Contains(got[line:], "Session:") || strings.Contains(got[line:], "Model calls:") || strings.Contains(got[line:], "Tokens (main):") {
			t.Fatalf("failed lookup retained stale summary: %q", got[line:])
		}
	} else if !strings.Contains(got[line:], "Session: final title\nModel calls: 1\nTokens (main): 0 input, 0 output\n") || strings.Contains(got[line:], "first title") {
		t.Fatalf("wrong final session snapshot: %q", got[line:])
	}
}
