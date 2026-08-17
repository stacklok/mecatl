package main

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatorModes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	validPath := filepath.Join(dir, "settings.yaml")
	original := []byte("permissions:\n  deny:\n    - Bash(rm *)\nlearning:\n  mode: off\n")
	if err := os.WriteFile(validPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(original)

	if err := run([]string{"existing", "--file", validPath}); err != nil {
		t.Fatalf("existing valid file: %v", err)
	}
	if err := run(proposedArgs(validPath)); err != nil {
		t.Fatalf("proposed valid replacement: %v", err)
	}
	afterData, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if after := sha256.Sum256(afterData); after != before {
		t.Fatal("proposed validation mutated the target")
	}

	missing := filepath.Join(dir, "missing.yaml")
	args := append(proposedArgs(missing), "--allow-missing")
	if err := run(args); err != nil {
		t.Fatalf("proposed missing target: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing target was created or stat failed unexpectedly: %v", err)
	}
}

func TestValidatorCLIPathIsOneArgumentWithoutShellExecution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings $draft;name.yaml")
	original := []byte("learning:\n  mode: off\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(dir, "shell-executed")
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "name.yaml"), []byte("#!/bin/sh\nprintf executed > \"$MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"existing", "--file", path},
		proposedArgs(path),
	} {
		cmd := exec.Command(goPath, append([]string{"run", "validate-settings.go"}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "MARKER="+marker, "draft=")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("validator %v failed: %v\n%s", args[0], err, output)
		}
		if got := strings.TrimSpace(string(output)); got != "valid" {
			t.Fatalf("validator %v output = %q, want valid", args[0], got)
		}
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacter path executed unintended command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings")); !os.IsNotExist(err) {
		t.Fatalf("unintended path was created: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("validator CLI mutated the target")
	}
}

func TestValidatorRejectsUnsafeInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name string
		doc  string
	}{
		{name: "malformed", doc: "learning: [\n"},
		{name: "duplicate learning", doc: "learning:\n  mode: off\nlearning:\n  mode: auto\n"},
		{name: "multiple documents", doc: "learning:\n  mode: off\n---\nlearning:\n  mode: auto\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".yaml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := run(proposedArgs(path)); err == nil {
				t.Fatal("proposed validation unexpectedly succeeded")
			}
			if err := run([]string{"existing", "--file", path}); err == nil {
				t.Fatal("existing validation unexpectedly succeeded")
			}
		})
	}
}

func TestValidatorRejectsInvalidProposedFlags(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{
		{"proposed", "--file", path, "--mode", "automatic"},
		{"proposed", "--file", path, "--mode", "auto", "--sensitivity", "balanced", "--activation", "validated", "--cooldown", "10m", "--window", "30s", "--max-reflections", "8", "--max-tokens", "100000", "--max-reflections-per-principal", "4", "--max-tokens-per-principal", "50000"},
		{"proposed", "--file", path, "--mode", "auto", "--sensitivity", "balanced", "--activation", "validated", "--cooldown", "10m", "--window", "1h", "--max-reflections", "1000000001", "--max-tokens", "100000", "--max-reflections-per-principal", "4", "--max-tokens-per-principal", "50000"},
	}
	for _, args := range cases {
		if err := run(args); err == nil {
			t.Fatalf("invalid flags unexpectedly succeeded: %v", args)
		}
	}
}

func proposedArgs(path string) []string {
	return []string{
		"proposed", "--file", path,
		"--mode", "auto",
		"--sensitivity", "balanced",
		"--activation", "validated",
		"--cooldown", "10m",
		"--window", "1h",
		"--max-reflections", "8",
		"--max-tokens", "100000",
		"--max-reflections-per-principal", "4",
		"--max-tokens-per-principal", "50000",
	}
}
