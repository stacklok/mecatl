package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

const validLearningPatch = `learning:
  mode: review
  sensitivity: balanced
  skills:
    activation: validated
  automatic:
    cooldown: 10m
    window: 1h
    max_reflections: 8
    max_tokens: 100000
    max_reflections_per_principal: 4
    max_tokens_per_principal: 50000
`

func TestGoccyYAMLMigration_Scenario4_LearningPatchPreservesUnrelatedDocument(t *testing.T) {
	t.Parallel()
	base := []byte("permissions:\n  deny:\n    - Shell(rm *)\nlearning:\n  mode: off\n")
	proposed, err := validateSettingsInMemory(base, []byte(validLearningPatch))
	if err != nil {
		t.Fatalf("replace learning: %v", err)
	}
	got := string(proposed)
	if !strings.Contains(got, "Shell(rm *)") || !strings.Contains(got, "mode: review") || strings.Contains(got, "mode: off") {
		t.Fatalf("replacement did not preserve unrelated settings and replace learning:\n%s", got)
	}
	if _, err := validateSettingsInMemory([]byte("permissions:\n  deny: [Read]\n"), nil); err != nil {
		t.Fatalf("valid existing settings: %v", err)
	}
	if _, err := validateSettingsInMemory(nil, []byte(validLearningPatch)); err != nil {
		t.Fatalf("missing base plus patch: %v", err)
	}

	fixture, err := os.ReadFile(filepath.Join("..", "mecatui", "testdata", "settings-preserve-top-level.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "mecatui", "testdata", "settings-preserve-top-level.mode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	proposed, err = validateSettingsInMemory(fixture, []byte("learning:\n  mode: review\n  sensitivity: balanced\n  skills:\n    # preserve this activation comment\n    activation: evaluated\n"))
	if err != nil {
		t.Fatalf("patch preservation fixture: %v", err)
	}
	if string(proposed) != string(want) {
		t.Fatalf("preservation fixture mismatch (-want +got):\nwant:\n%s\ngot:\n%s", want, proposed)
	}
}

func TestGoccyYAMLMigration_Scenario2_ConfigValidationSafeDocumentContract(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ base, patch []byte }{
		"malformed base":       {base: []byte("learning: [\n")},
		"nonmapping base":      {base: []byte("- learning\n")},
		"anchor base":          {base: []byte("learning: &policy {}\n")},
		"alias base":           {base: []byte("base: &policy {}\nlearning: *policy\n")},
		"duplicate base":       {base: []byte("learning: {}\nlearning: {}\n")},
		"multidocument base":   {base: []byte("learning: {}\n---\nlearning: {}\n")},
		"patch extra key":      {patch: []byte("learning: {}\npermissions: {}\n")},
		"patch nonmapping":     {patch: []byte("learning: off\n")},
		"patch alias":          {patch: []byte("learning: &policy\n  mode: off\n")},
		"patch unknown":        {patch: []byte("learning:\n  mystery: true\n")},
		"patch out of range":   {patch: []byte("learning:\n  automatic:\n    window: 30s\n")},
		"patch duplicate":      {patch: []byte("learning: {}\nlearning: {}\n")},
		"patch multidocument":  {patch: []byte("learning: {}\n---\nlearning: {}\n")},
		"empty learning patch": {patch: []byte{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.base == nil {
				tc.base = []byte("permissions: {}\n")
			}
			if _, err := validateSettingsInMemory(tc.base, tc.patch); err == nil {
				t.Fatal("unsafe document unexpectedly validated")
			}
		})
	}

}

func TestConfigValidateMalformedSyntaxIncludesSafeLocation(t *testing.T) {
	const secret = "MECATED_CONFIG_SECRET"
	_, err := validateSettingsInMemory([]byte("learning: ["+secret), nil)
	if err == nil {
		t.Fatal("malformed settings unexpectedly validated")
	}
	if !strings.Contains(err.Error(), "malformed YAML document at line ") || !strings.Contains(err.Error(), ", column ") {
		t.Fatalf("validateSettingsInMemory error = %q, want safe line and column", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validateSettingsInMemory error leaked YAML content: %q", err)
	}
}

func TestGoccyYAMLMigration_SemanticMatrixConfigValidate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "engine", "testdata", "semantic-matrix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Cases []struct {
			Name     string            `yaml:"name"`
			Document string            `yaml:"document"`
			Readers  map[string]string `yaml:"readers"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	for _, tc := range matrix.Cases {
		outcome, ok := tc.Readers["configvalidate"]
		if !ok {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			_, err := validateSettingsInMemory([]byte(tc.Document), nil)
			if accepted, want := err == nil, outcome == "accept"; accepted != want {
				t.Fatalf("validateSettingsInMemory() accepted=%v, want %v (error=%v)", accepted, want, err)
			}
		})
	}
}

func TestGoccyYAMLMigration_Scenario2_ConfigValidateADR0225Safety(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "settings $draft; name.yaml")
	patchPath := filepath.Join(dir, "learning patch.yaml")
	base := []byte("permissions:\n  deny:\n    - Shell(rm *)\nlearning:\n  mode: off\n")
	if err := os.WriteFile(basePath, base, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patchPath, []byte(validLearningPatch), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeBase := sha256.Sum256(base)
	patchBytes, _ := os.ReadFile(patchPath)
	beforePatch := sha256.Sum256(patchBytes)
	var out bytes.Buffer
	if err := runConfigValidate([]string{"--file", basePath, "--learning-patch", patchPath}, &out); err != nil {
		t.Fatalf("validate path with spaces/metacharacters: %v", err)
	}
	if out.String() != "valid\n" {
		t.Fatalf("output = %q", out.String())
	}
	afterBase, _ := os.ReadFile(basePath)
	afterPatch, _ := os.ReadFile(patchPath)
	if sha256.Sum256(afterBase) != beforeBase || sha256.Sum256(afterPatch) != beforePatch {
		t.Fatal("validation mutated a file")
	}

	missing := filepath.Join(dir, "new settings.yaml")
	out.Reset()
	if err := runConfigValidate([]string{"--file", missing, "--learning-patch", patchPath}, &out); err != nil {
		t.Fatalf("missing base preflight: %v", err)
	}
	if out.String() != "valid (new file)\n" {
		t.Fatalf("new-file output = %q", out.String())
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing base was created: %v", err)
	}

	link := filepath.Join(dir, "settings-link.yaml")
	if err := os.Symlink(basePath, link); err == nil {
		out.Reset()
		if err := runConfigValidate([]string{"--file", link}, &out); err == nil {
			t.Fatal("final-component symlink unexpectedly accepted")
		}
		if out.Len() != 0 {
			t.Fatalf("symlink validation wrote output: %q", out.String())
		}
	}
}

func TestRunConfigValidateDefaultPathAndMissingBehavior(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path := filepath.Join(xdg, "mecatl", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("learning:\n  mode: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfigValidate(nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("default path: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := runConfigValidate(nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing default error = %v", err)
	}
	patch := filepath.Join(xdg, "patch.yaml")
	if err := os.WriteFile(patch, []byte(validLearningPatch), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigValidate([]string{"--learning-patch", patch}, &out); err != nil {
		t.Fatalf("missing default with explicit patch: %v", err)
	}
	if out.String() != "valid (new file)\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunConfigValidateBoundsFilesAndSanitizesErrors(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.yaml")
	if err := os.WriteFile(large, bytes.Repeat([]byte("x"), maxSettingsConfigBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfigValidate([]string{"--file", large}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized error = %v", err)
	}
	base := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(base, []byte("learning: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfigValidate([]string{"--file", base, "--learning-patch", large}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized patch error = %v", err)
	}
	secret := "SUPER_SECRET_VALUE_123"
	malformed := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(malformed, []byte("learning: ["+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runConfigValidate([]string{"--file", malformed}, &out)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
		t.Fatalf("unsanitized result: err=%v out=%q", err, out.String())
	}
	if !strings.Contains(err.Error(), "line ") || !strings.Contains(err.Error(), "column ") {
		t.Fatalf("syntax error = %q, want value-free line and column", err)
	}
}

func TestRunConfigValidateSyntaxLocationIsValueFree(t *testing.T) {
	const sentinel = "CONFIGVALIDATE_SECRET_SENTINEL"
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte("learning: ["+sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runConfigValidate([]string{"--file", path}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("malformed settings unexpectedly validated")
	}
	message := err.Error()
	if !strings.Contains(message, "line ") || !strings.Contains(message, "column ") {
		t.Fatalf("syntax error = %q, want line and column", message)
	}
	if strings.Contains(message, sentinel) {
		t.Fatalf("syntax error leaked YAML content: %q", message)
	}
}

func TestRunConfigValidateRejectsUnsafePathsAndArguments(t *testing.T) {
	dir := t.TempDir()
	if err := runConfigValidate([]string{"--file", dir}, &bytes.Buffer{}); err == nil {
		t.Fatal("directory unexpectedly accepted")
	}
	target := filepath.Join(dir, "settings.yaml")
	link := filepath.Join(dir, "settings-link.yaml")
	if err := os.WriteFile(target, []byte("learning: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err == nil {
		if err := runConfigValidate([]string{"--file", link}, &bytes.Buffer{}); err == nil {
			t.Fatal("symlink unexpectedly accepted")
		}
	}
	for _, args := range [][]string{{"--unknown"}, {"--file", target, "extra"}} {
		if err := runConfigValidate(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("arguments unexpectedly accepted: %v", args)
		}
	}
}

func TestReadConfigFileUsesNoFollowNonblockingRegularDescriptor(t *testing.T) {
	dir := t.TempDir()
	normal := filepath.Join(dir, "settings.yaml")
	want := []byte("learning:\n  mode: off\n")
	if err := os.WriteFile(normal, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, missing, err := readConfigFile(normal, false, false)
	if err != nil || missing || !bytes.Equal(got, want) {
		t.Fatalf("normal read = %q, missing=%v, err=%v", got, missing, err)
	}

	target := filepath.Join(dir, "target.yaml")
	secret := "SUPER_SECRET_VALUE_123"
	if err := os.WriteFile(target, []byte("learning: ["+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, learningPatch := range []bool{false, true} {
		if _, _, err := readConfigFile(link, false, learningPatch); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("symlink read (learningPatch=%v) = %v", learningPatch, err)
		}
	}

	fifo := filepath.Join(dir, "settings.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, learningPatch := range []bool{false, true} {
		result := make(chan error, 1)
		go func() {
			_, _, err := readConfigFile(fifo, false, learningPatch)
			result <- err
		}()
		select {
		case err := <-result:
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("FIFO read (learningPatch=%v) = %v", learningPatch, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("FIFO read blocked (learningPatch=%v)", learningPatch)
		}
	}
}

func TestResolveConfigValidateAndHelp(t *testing.T) {
	res := resolveCommand([]string{"mecated", "config", "validate", "--help"})
	if res.err != nil || !res.handled || res.run == nil {
		t.Fatalf("resolution = %+v", res)
	}
	var out bytes.Buffer
	if err := res.run(strings.NewReader(""), &out, &bytes.Buffer{}); err == nil {
		t.Fatal("help should return flag.ErrHelp")
	}
	for _, want := range []string{"mecated config validate", "-file", "-learning-patch"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q:\n%s", want, out.String())
		}
	}
}
