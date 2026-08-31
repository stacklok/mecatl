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
func TestOperatorLearningSettingsConcurrentAdvanceAndSensitivityAreSerialized(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	if err := os.WriteFile(path, []byte("learning:\n  mode: off\n  sensitivity: balanced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstRead := make(chan struct{})
	secondAttempting := make(chan struct{})
	advance := &operatorLearningSettings{path: path, afterRead: func() {
		close(firstRead)
		<-secondAttempting
	}}
	sensitivity := &operatorLearningSettings{path: path, beforeLock: func() {
		<-firstRead
		close(secondAttempting)
	}}
	errs := make(chan error, 2)
	go func() { _, _, _, err := advance.Advance(); errs <- err }()
	go func() { _, _, _, err := sensitivity.AdvanceSensitivity(); errs <- err }()
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
	sensitivityValue, err := learningSensitivity(doc)
	if err != nil {
		t.Fatal(err)
	}
	if mode.String() != "review" || sensitivityValue.String() != "eager" {
		t.Fatalf("settings after concurrent updates = mode %s, sensitivity %s; want review and eager", mode, sensitivityValue)
	}
}

func TestOperatorLearningSettingsAdvanceSensitivityNormalizesAbsentMode(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	body := "posture: trusted\nlearning:\n  sensitivity: conservative\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := (&operatorLearningSettings{path: path}).AdvanceSensitivity(); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"posture: trusted", "mode: off", "sensitivity: balanced"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("saved YAML missing %q:\n%s", want, saved)
		}
	}
}

func TestGoccyYAMLMigration_Scenario4_LearningEditorWriteSafetyUnchanged(t *testing.T) {
	operations := map[string]func(*operatorLearningSettings) (string, string, string, error){
		"Advance":            (*operatorLearningSettings).Advance,
		"AdvanceSensitivity": (*operatorLearningSettings).AdvanceSensitivity,
	}
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
	for operationName, operation := range operations {
		for name, body := range cases {
			t.Run(operationName+"/"+name, func(t *testing.T) {
				path := filepath.Join(canonicalTempDir(t), "settings.yaml")
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				from, to, restart, err := operation(&operatorLearningSettings{path: path})
				if err == nil || !strings.HasPrefix(err.Error(), "parse operator settings:") {
					t.Fatalf("%s error = %v, want settings parse error", operationName, err)
				}
				if from != "" || to != "" || restart != "" {
					t.Fatalf("%s success labels = %q, %q, %q, want empty", operationName, from, to, restart)
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

func TestGoccyYAMLMigration_Scenario4_LearningEditorPreservationFixtures(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("learning_settings.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "go.yaml.in/yaml/v3") || !strings.Contains(string(source), "github.com/goccy/go-yaml") {
		t.Fatal("learning settings editor must use the goccy AST document path")
	}

	cases := []struct {
		name     string
		fixture  string
		expected string
		edit     func(*operatorLearningSettings) error
	}{
		{
			name:     "mode preserves top-level comments order and flow style",
			fixture:  "settings-preserve-top-level.yaml",
			expected: "settings-preserve-top-level.mode.yaml",
			edit: func(settings *operatorLearningSettings) error {
				_, _, _, err := settings.Advance()
				return err
			},
		},
		{
			name:     "sensitivity preserves learning flow style",
			fixture:  "settings-preserve-learning.yaml",
			expected: "settings-preserve-learning.sensitivity.yaml",
			edit: func(settings *operatorLearningSettings) error {
				_, _, _, err := settings.AdvanceSensitivity()
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(canonicalTempDir(t), "settings.yaml")
			input, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", tc.expected))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, input, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := tc.edit(&operatorLearningSettings{path: path}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("edited document mismatch (-want +got):\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}

func TestOperatorLearningSettingsAcceptsIntegerAutomaticLimits(t *testing.T) {
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	body := "learning:\n  mode: off\n  automatic:\n    cooldown: 1m\n    window: 1h\n    max_reflections: 1\n    max_tokens: 1\n    max_reflections_per_principal: 1\n    max_tokens_per_principal: 1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := (&operatorLearningSettings{path: path}).Advance(); err != nil {
		t.Fatalf("Advance with integer automatic limits: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "max_tokens: 1") {
		t.Fatalf("integer automatic limit was not preserved:\n%s", got)
	}
}

func TestOperatorLearningSettingsValidationErrorsDoNotLeakMappingKeys(t *testing.T) {
	const secret = "super-secret-mapping-key"
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown learning key",
			body: "learning:\n  " + secret + ": super-secret-value\n",
			want: "parse operator settings: unknown learning key",
		},
		{
			name: "invalid learning value",
			body: "learning:\n  mode: super-secret-value\n",
			want: "parse operator settings: invalid learning.mode",
		},
		{
			name: "unknown automatic key",
			body: "learning:\n  automatic:\n    " + secret + ": super-secret-value\n",
			want: "parse operator settings: unknown learning.automatic key",
		},
		{
			name: "duplicate automatic key",
			body: "learning:\n  automatic:\n    max_tokens: 1\n    max_tokens: 2\n",
			want: "parse operator settings: invalid YAML",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(canonicalTempDir(t), "settings.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, _, err := (&operatorLearningSettings{path: path}).Advance()
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("Advance error = %v, want prefix %q", err, tc.want)
			}
			for _, forbidden := range []string{secret, "super-secret-value"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("validation error leaked YAML-derived content %q: %s", forbidden, err)
				}
			}
		})
	}
}

func TestOperatorLearningSettingsMalformedSyntaxIncludesSafeLocation(t *testing.T) {
	const secret = "MECATUI_LEARNING_SECRET"
	path := filepath.Join(canonicalTempDir(t), "settings.yaml")
	if err := os.WriteFile(path, []byte("learning: ["+secret), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := (&operatorLearningSettings{path: path}).readDocument()
	if err == nil {
		t.Fatal("malformed settings unexpectedly parsed")
	}
	if !strings.Contains(err.Error(), "parse operator settings: invalid YAML at line ") || !strings.Contains(err.Error(), ", column ") {
		t.Fatalf("readDocument error = %q, want safe line and column", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("readDocument error leaked YAML content: %q", err)
	}
}
