package mcpbroker

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolHiveImportsStayBehindApprovedAdapterLeaves(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"internal/adapter/mcp/source/toolhive.go":                     true,
		"internal/adapter/mcpbroker/authenticated_discovery.go":       true,
		"internal/adapter/mcpbroker/toolhive_construction.go":         true,
		"internal/adapter/mcpbroker/toolhive_credential_custody.go":   true,
		"internal/adapter/mcpbroker/toolhive_encrypted_storage.go":    true,
		"internal/adapter/mcpbroker/toolhive_process.go":              true,
		"internal/adapter/mcpbroker/toolhive_protected_storage.go":    true,
		"internal/adapter/mcpbroker/toolhive_recovered_credential.go": true,
		"internal/adapter/toolhivellm/tokensource.go":                 true,
		// RFC 9728 protected-resource discovery (#1055), a separate ToolHive
		// import leaf unrelated to the MCP broker's own construction/discovery.
		"cmd/mecatui/resource_discovery.go":           true,
		"internal/adapter/resourceurl/resourceurl.go": true,
	}
	const toolHiveModule = "github.com/stacklok/toolhive"
	if err := filepath.WalkDir(root, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".scratch" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(sourcePath, ".go") || strings.HasSuffix(sourcePath, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), sourcePath, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			if importPath != toolHiveModule && !strings.HasPrefix(importPath, toolHiveModule+"/") {
				continue
			}
			relative, err := filepath.Rel(root, sourcePath)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !allowed[relative] {
				t.Errorf("ToolHive import escaped approved adapter leaves: %s imports %s", relative, importPath)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
