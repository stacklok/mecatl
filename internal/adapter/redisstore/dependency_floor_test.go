package redisstore

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRedisFollowCapacity_Scenario1_Go127DependencyFloor(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report the test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
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
	for _, manifest := range manifests {
		body, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(bytes.NewReader(body))
		found := false
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "go ") {
				found = true
				if scanner.Text() != "go 1.27" {
					t.Errorf("%s declares %q, want go 1.27", strings.TrimPrefix(manifest, root+string(filepath.Separator)), scanner.Text())
				}
				break
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
		"github.com/stacklok/toolhive-core":           "v0.0.46",
		"github.com/stacklok/toolhive-core/redisconn": "v0.0.2",
	} {
		if !strings.Contains(text, module+" "+version) {
			t.Errorf("root go.mod does not pin %s %s", module, version)
		}
	}
	if strings.Contains(text, "replace github.com/stacklok/toolhive-core/redisconn") {
		t.Error("root go.mod replaces redisconn; the released module must be consumed directly")
	}
}
