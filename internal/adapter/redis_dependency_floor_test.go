package adapter

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestRedisFollowCapacity_Scenario1_Go127DependencyFloor(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report the test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	var manifests []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root {
			switch entry.Name() {
			case ".git", ".scratch", "node_modules", "vendor":
				return filepath.SkipDir
			}
		}
		if !entry.IsDir() && entry.Name() == "go.mod" {
			manifests = append(manifests, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manifests = append(manifests, filepath.Join(root, "go.work"))
	if len(manifests) < 2 {
		t.Fatalf("found %d Go manifests, want every committed module plus go.work", len(manifests))
	}
	// Manifests declare the Go 1.27 language floor, not a patch floor. These
	// consume published engine versions released with a patch floor, and Go
	// requires a module to declare at least its dependencies' floor. Lower each
	// to go 1.27 once its engine pin moves to a release that declares go 1.27.
	inheritedPatchFloor := map[string]string{
		"go.mod":                     "go 1.27.3", // engine v0.18.0
		"go.work":                    "go 1.27.3", // highest workspace module
		"integration/microvm/go.mod": "go 1.27.3", // engine v0.18.0
		"provider/anthropic/go.mod":  "go 1.27.3", // engine v0.18.0
		"provider/openai/go.mod":     "go 1.27.3", // engine v0.18.0
		"provider/openaichat/go.mod": "go 1.27.3", // engine v0.18.0
		"adapters/go.mod":            "go 1.27.2", // engine pseudo-version of 2026-10-13
	}
	for _, manifest := range manifests {
		body, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(bytes.NewReader(body))
		name := strings.TrimPrefix(manifest, root+string(filepath.Separator))
		found := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "go ") {
				found = true
				want := "go 1.27"
				if floor, ok := inheritedPatchFloor[filepath.ToSlash(name)]; ok {
					want = floor
				}
				if line != want {
					t.Errorf("%s declares %q, want %q", name, line, want)
				}
			}
			if strings.HasPrefix(line, "toolchain ") {
				t.Errorf("%s declares %q; manifests must not pin a toolchain", name, line)
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Errorf("%s has no go directive", manifest)
		}
	}

	rootMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(rootMod)
	for module, version := range map[string]string{
		"github.com/stacklok/toolhive-core":           "v0.0.55",
		"github.com/stacklok/toolhive-core/redisconn": "v0.0.4",
	} {
		if !strings.Contains(text, module+" "+version) {
			t.Errorf("root go.mod does not pin %s %s", module, version)
		}
	}
	for _, module := range []string{
		"github.com/stacklok/toolhive-core",
		"github.com/stacklok/toolhive-core/redisconn",
	} {
		replacement := regexp.MustCompile(`(?m)^\s*(?:replace\s+)?` + regexp.QuoteMeta(module) + `(?:\s+v\S+)?\s*=>`)
		if replacement.MatchString(text) {
			t.Errorf("root go.mod replaces %s; the released module must be consumed directly", module)
		}
	}
}

func TestRedisFollowCapacity_Scenario4_Go127SourceBuildSurfaces(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report the test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	for name, want := range map[string]string{
		"AGENTS.md":    "in Go 1.27",
		"Taskfile.yml": "go            >= 1.27.3",
		"user-docs/_partials/release-archives-and-source.mdx":         "requires Go 1.27.3 or later",
		"user-docs/building/go/demo.md":                               "**Go 1.27.3 or newer**",
		"user-docs/building/go/first-agent.md":                        "Go 1.27.3 or newer",
		"sdk/typescript/examples/slack-bot/docker/mecated.Dockerfile": "FROM golang:1.27.3-bookworm@sha256:",
	} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s does not declare the Go 1.27 source-build floor; want %q", name, want)
		}
	}
}
