package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temporary directory: %v", err)
	}
	// On macOS, t.TempDir may be under /var, which symlinks to /private/var.
	// Canonicalize test roots so production's symlink defense is exercised only deliberately.
	return dir
}

func TestLearningSettingsWiringMatchesTransport(t *testing.T) {
	configHome := canonicalTempDir(t)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	local := learningSettingsForConfig(config{transportMode: modeLocal})
	wantPath := filepath.Join(configHome, "mecatl", "settings.yaml")
	if local == nil || local.remote || local.path != wantPath {
		t.Fatalf("embedded learning wiring = %#v, want writable %q", local, wantPath)
	}
	if _, _, _, err := local.Advance(); err != nil {
		t.Fatalf("embedded learning Advance: %v", err)
	}
	before, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}

	remote := learningSettingsForConfig(config{transportMode: modeConnect})
	if remote == nil || !remote.remote || remote.path != "" {
		t.Fatalf("connect learning wiring = %#v, want remote/read-only adapter", remote)
	}
	if _, _, _, err := remote.Advance(); err == nil || !strings.Contains(err.Error(), "server host") {
		t.Fatalf("connect Advance error = %v, want remote-host instruction", err)
	}
	after, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("connect-mode learning adapter modified local settings")
	}
}

func TestOperatorLearningSettingsCyclesSensitivityAndPreservesMode(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	if err := os.WriteFile(path, []byte("# retained\nlearning:\n  mode: review\n  sensitivity: conservative\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := &operatorLearningSettings{path: path}
	from, to, restart, err := settings.AdvanceSensitivity()
	if err != nil {
		t.Fatal(err)
	}
	if from != "Conservative (mode Review, skills evaluated)" || to != "Balanced (mode Review, skills evaluated)" || !strings.Contains(restart, "restart") {
		t.Fatalf("labels = %q %q %q", from, to, restart)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# retained") || !strings.Contains(string(body), "mode: review") || !strings.Contains(string(body), "sensitivity: balanced") {
		t.Fatalf("saved body:\n%s", body)
	}
}

func TestOperatorLearningSettingsPreservesUnrelatedYAMLAndComments(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "# keep me\nposture: trusted\npermissions:\n  deny: [Write]\nlearning:\n  mode: off\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	from, to, restart, err := (&operatorLearningSettings{path: path}).Advance()
	if err != nil {
		t.Fatal(err)
	}
	if from != "Off (sensitivity Balanced, skills evaluated)" || to != "Review (sensitivity Balanced, skills evaluated)" || !strings.Contains(restart, "restart mecatui") {
		t.Fatalf("Advance = %q, %q, %q", from, to, restart)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"# keep me", "posture: trusted", "deny: [Write]", "mode: review"} {
		if !strings.Contains(text, want) {
			t.Errorf("saved YAML missing %q:\n%s", want, text)
		}
	}
}

func TestOperatorLearningSettingsPreservesExplicitEvaluatedActivation(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	body := "learning:\n  mode: review\n  skills:\n    # keep assurance\n    activation: evaluated\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	from, to, _, err := (&operatorLearningSettings{path: path}).Advance()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(from, "skills evaluated") || !strings.Contains(to, "skills evaluated") {
		t.Fatalf("labels = %q -> %q", from, to)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "# keep assurance") || !strings.Contains(string(saved), "activation: evaluated") {
		t.Fatalf("activation/comment not preserved:\n%s", saved)
	}
}

func TestOperatorLearningSettingsConcurrentAdvanceIsSerialized(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	if err := os.WriteFile(path, []byte("learning:\n  mode: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondAttempting := make(chan struct{})
	first := &operatorLearningSettings{path: path, afterRead: func() { <-secondAttempting }}
	second := &operatorLearningSettings{path: path, beforeLock: func() { close(secondAttempting) }}
	errs := make(chan error, 2)
	go func() { _, _, _, err := first.Advance(); errs <- err }()
	go func() { _, _, _, err := second.Advance(); errs <- err }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	doc, err := (&operatorLearningSettings{path: path}).readDocument()
	if err != nil {
		t.Fatal(err)
	}
	mode, err := learningMode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if mode.String() != "auto" {
		t.Fatalf("mode after two concurrent advances = %s, want auto", mode)
	}
}

func TestOperatorLearningSettingsRejectsInvalidYAMLWithoutModification(t *testing.T) {
	cases := map[string]string{
		"non-mapping learning": "learning: off\n",
		"non-string mode":      "learning:\n  mode: [off]\n",
		"invalid mode":         "learning:\n  mode: automatic\n",
		"duplicate learning":   "learning:\n  mode: off\nlearning:\n  mode: auto\n",
		"duplicate mode":       "learning:\n  mode: off\n  mode: auto\n",
		"alias":                "base: &mode off\nlearning:\n  mode: *mode\n",
		"unknown learning key": "learning:\n  mode: off\n  queue: later\n",
		"invalid activation":   "learning:\n  mode: auto\n  skills:\n    activation: pass\n",
		"scalar skills":        "learning:\n  mode: auto\n  skills: validated\n",
		"duplicate activation": "learning:\n  mode: auto\n  skills:\n    activation: validated\n    activation: evaluated\n",
		"multiple documents":   "learning:\n  mode: off\n---\nlearning:\n  mode: auto\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(canonicalTempDir(t), "settings.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := (&operatorLearningSettings{path: path}).Advance(); err == nil {
				t.Fatal("Advance succeeded for invalid YAML")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != body {
				t.Fatalf("invalid file was modified:\n%s", after)
			}
		})
	}
}

func TestOperatorLearningSettingsRejectsSymlinks(t *testing.T) {
	t.Run("settings file", func(t *testing.T) {
		dir := canonicalTempDir(t)
		target := filepath.Join(dir, "target.yaml")
		if err := os.WriteFile(target, []byte("learning:\n  mode: off\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "settings.yaml")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := (&operatorLearningSettings{path: link}).Advance(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Advance error = %v, want symlink rejection", err)
		}
	})
	t.Run("parent directory", func(t *testing.T) {
		base := canonicalTempDir(t)
		realDir := filepath.Join(base, "real")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(base, "linked")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(linkDir, "settings.yaml")
		if _, _, _, err := (&operatorLearningSettings{path: path}).Advance(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Advance error = %v, want parent symlink rejection", err)
		}
		if _, err := os.Stat(filepath.Join(realDir, "settings.yaml")); !os.IsNotExist(err) {
			t.Fatalf("target unexpectedly created: %v", err)
		}
	})
}

func TestOperatorLearningSettingsRejectsSymlinkIntroducedDuringMutation(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "settings.yaml")
	target := filepath.Join(dir, "target.yaml")
	const targetBody = "learning:\n  mode: auto\n"
	if err := os.WriteFile(path, []byte("learning:\n  mode: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(targetBody), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := &operatorLearningSettings{path: path, afterRead: func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}}
	if _, _, _, err := settings.Advance(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Advance error = %v, want symlink rejection", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != targetBody {
		t.Fatalf("symlink target was modified: %q", body)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("settings symlink was replaced: info=%v err=%v", info, err)
	}
}

func TestOperatorLearningSettingsRemoteIsReadOnly(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	body := "learning:\n  mode: off\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := &operatorLearningSettings{path: path, remote: true}
	if _, _, _, err := remote.Advance(); err == nil || !strings.Contains(err.Error(), "server host") {
		t.Fatalf("remote Advance error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Fatal("remote mode modified local settings")
	}
	if got := newOperatorLearningSettings(true); got == nil || !got.remote || got.path != "" {
		t.Fatalf("remote wiring = %#v", got)
	}
}

func TestOperatorLearningSettingsWriteError(t *testing.T) {
	dir := canonicalTempDir(t)
	if _, _, _, err := (&operatorLearningSettings{path: dir}).Advance(); err == nil {
		t.Fatal("Advance should report a read/write error for a directory path")
	}
}
