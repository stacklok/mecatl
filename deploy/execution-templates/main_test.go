package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
)

const reviewedRecipe = `recipes:
  reviewed-toolchain:
    maxEnvironments: 1000
    display:
      name: Reviewed toolchain
    image: registry.example.com/toolchain@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    storageClass: standard
    storageSize: 2Gi
    cpuRequest: 100m
    memoryRequest: 128Mi
    cpuLimit: '1'
    memoryLimit: 1Gi
    ephemeralStorageRequest: 64Mi
    ephemeralStorageLimit: 1Gi
    tmpSizeLimit: 256Mi
    runtimeClassName: reviewed-runtime
    maxFileBytes: 5242880
    maxCommandBytes: 1048576
    maxCommandDuration: 5m
`

func TestOfflineRecipeAuthoring(t *testing.T) {
	var baseline string
	for _, tc := range []struct {
		name, old, replacement string
		changed                bool
	}{
		{name: "baseline"},
		{name: "quota", old: "maxEnvironments: 1000", replacement: "maxEnvironments: 1"},
		{name: "display", old: "name: Reviewed toolchain", replacement: "name: Renamed toolchain"},
		{name: "recipe", old: "maxCommandDuration: 5m", replacement: "maxCommandDuration: 4m", changed: true},
		{name: "image", old: strings.Repeat("a", 64), replacement: strings.Repeat("b", 64), changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input, output := filepath.Join(dir, "recipe.yaml"), filepath.Join(dir, "templates.yaml")
			body := reviewedRecipe
			if tc.old != "" {
				body = strings.Replace(body, tc.old, tc.replacement, 1)
			}
			if err := os.WriteFile(input, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := run([]string{input, output}, io.Discard); err != nil {
				t.Fatal(err)
			}
			if err := run([]string{"-validate", output}, io.Discard); err != nil {
				t.Fatal(err)
			}
			profiles, err := executioncontroller.LoadTemplates(output)
			if err != nil {
				t.Fatal(err)
			}
			revision := profiles.DefaultRevision("reviewed-toolchain")
			if baseline == "" {
				baseline = revision
			}
			if (revision != baseline) != tc.changed {
				t.Fatalf("revision=%s baseline=%s changed=%v", revision, baseline, tc.changed)
			}
			before, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if err := run([]string{input, output}, io.Discard); err == nil {
				t.Fatal("overwrote existing output")
			}
			after, err := os.ReadFile(output)
			if err != nil || string(after) != string(before) {
				t.Fatal("existing output changed")
			}
			// Keep the published key but change the recipe: validation must reject it.
			tampered := strings.Replace(string(before), "storageSize: 2Gi", "storageSize: 3Gi", 1)
			if tampered == string(before) {
				t.Fatal("recipe mutation did not apply")
			}
			if err := os.WriteFile(output, []byte(tampered), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := run([]string{"-validate", output}, io.Discard); err == nil || !strings.Contains(err.Error(), "canonical execution definition") {
				t.Fatalf("changed recipe accepted or wrong failure: %v", err)
			}
		})
	}
}

func TestInvalidRecipeDoesNotPublish(t *testing.T) {
	for _, tc := range []struct{ old, replacement string }{
		{"maxEnvironments: 1000", "maxEnvironments: 0"},
		{"maxFileBytes:", "unknownField:"},
		{"maxCommandDuration: 5m", "maxCommandDuration: invalid"},
		{"@sha256:" + strings.Repeat("a", 64), ":latest"},
		{"name: Reviewed toolchain", "unknownDisplay: invalid"},
	} {
		dir := t.TempDir()
		input, output := filepath.Join(dir, "recipe.yaml"), filepath.Join(dir, "templates.yaml")
		if err := os.WriteFile(input, []byte(strings.Replace(reviewedRecipe, tc.old, tc.replacement, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{input, output}, io.Discard); err == nil {
			t.Fatalf("invalid recipe accepted: %s", tc.replacement)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("invalid recipe published output: %v", err)
		}
	}
}
