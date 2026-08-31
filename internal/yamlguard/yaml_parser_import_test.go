package yamlguard

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const goccyYAML = "github.com/goccy/go-yaml"

// TestGoccyYAMLMigration_Scenario7_OnlyGoccyYAMLParserIsDirectlyImported pins
// AC7.1: production files may import goccy, but no other direct YAML parser.
func TestGoccyYAMLMigration_Scenario7_OnlyGoccyYAMLParserIsDirectlyImported(t *testing.T) {
	repoRoot := repositoryRoot(t)
	assertYAMLParserLintRule(t, repoRoot)

	var violations []string
	for _, module := range []string{".", "engine"} {
		filepath.WalkDir(filepath.Join(repoRoot, module), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+".scratch"+string(filepath.Separator)) || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if parseErr != nil {
				t.Errorf("parse imports %s: %v", path, parseErr)
				return nil
			}
			for _, imp := range file.Imports {
				pkg := strings.Trim(imp.Path.Value, "\"")
				if isYAMLParser(pkg) && pkg != goccyYAML && !strings.HasPrefix(pkg, goccyYAML+"/") {
					violations = append(violations, strings.TrimPrefix(path, repoRoot+string(filepath.Separator))+": "+pkg)
				}
			}
			return nil
		})
	}
	if len(violations) != 0 {
		t.Fatalf("direct production YAML parser imports must use %s only:\n%s", goccyYAML, strings.Join(violations, "\n"))
	}
}

func isYAMLParser(pkg string) bool {
	return !strings.HasPrefix(pkg, "github.com/stacklok/mecatl/") && strings.Contains(strings.ToLower(pkg), "yaml")
}

func assertYAMLParserLintRule(t *testing.T, repoRoot string) {
	t.Helper()
	config, err := os.ReadFile(filepath.Join(repoRoot, ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	for _, want := range []string{"yaml-parser-imports:", "go.yaml.in/yaml/v3", "gopkg.in/yaml.v3", "sigs.k8s.io/yaml"} {
		if !strings.Contains(string(config), want) {
			t.Errorf(".golangci.yml YAML parser guard is missing %q", want)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
