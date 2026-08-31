package permconfig

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func sourceMatrixWorkspace(t *testing.T, shared, local string) *memfs.Workspace {
	t.Helper()
	ws := memfs.NewWorkspace("/repo")
	for path, content := range map[string]string{
		projectFileMecatl:      shared,
		projectFileMecatlLocal: local,
	} {
		if err := ws.Write(context.Background(), path, []byte(content)); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	return ws
}

func TestGoccyYAMLMigration_Scenario1_SourceMatrixSafeErrorsDiagnosticsAndPaths(t *testing.T) {
	t.Parallel()

	const (
		cliPath    = "/operator/AKIAIOSFODNN7EXAMPLE.yaml"
		configHome = "/cfg/AKIAIOSFODNN7EXAMPLE"
		yamlSecret = "yaml-derived-secret-should-never-appear"
		malformed  = "credentials: yaml-derived-secret-should-never-appear\n  broken: [\n"
	)
	userPath := configHome + "/" + UserSettingsRelPath
	var logs bytes.Buffer
	env := xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile: func(path string) ([]byte, error) {
			if path == cliPath || path == userPath {
				return []byte(malformed), nil
			}
			return nil, errors.New("not found")
		},
	}
	r := newWithEnv(Options{
		Conventional:  true,
		ExplicitFiles: []string{cliPath},
		Diagnostics:   slogdiag.New(&logs, true, port.LevelDebug),
	}, env)
	ws := sourceMatrixWorkspace(t, malformed, malformed)
	if rules := r.Resolve(context.Background(), ws); len(rules) != 0 {
		t.Fatalf("malformed files yielded rules: %+v", rules)
	}

	if err := ValidateYAML([]byte(malformed)); err == nil {
		t.Fatal("malformed YAML was accepted")
	} else if text := err.Error(); strings.Contains(text, yamlSecret) || strings.Contains(text, "credentials:") || strings.Contains(text, "broken:") {
		t.Fatalf("returned parse error leaked YAML-derived content: %q", text)
	}

	output := logs.String()
	for _, path := range []string{cliPath, userPath, projectFileMecatl, projectFileMecatlLocal, "/repo"} {
		if !strings.Contains(output, path) {
			t.Fatalf("diagnostics did not retain permitted source identifier %q: %s", path, output)
		}
	}
	if strings.Contains(output, yamlSecret) {
		t.Fatalf("diagnostics leaked YAML-derived content %q: %s", yamlSecret, output)
	}
	if strings.Contains(output, "credentials:") || strings.Contains(output, "broken:") {
		t.Fatalf("diagnostics leaked YAML key or source snippet: %s", output)
	}
}

func TestSafePermconfigSchemaErrorDoesNotClassifyDecoderText(t *testing.T) {
	data, err := os.ReadFile("permconfig.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "err.Error()") || strings.Contains(string(data), "safePermconfigSchemaLine") {
		t.Fatal("permission-config schema diagnostics must use structured section and token context, not decoder text")
	}
}

func TestGoccyYAMLMigration_SchemaSourceMatrixErrorsAreValueFree(t *testing.T) {
	t.Parallel()

	const (
		cliPath    = "/operator/AKIAIOSFODNN7EXAMPLE.yaml"
		configHome = "/cfg/AKIAIOSFODNN7EXAMPLE"
		secretKey  = "yaml-derived-secret-key"
		secret     = "yaml-derived-secret-value"
		schema     = "permissions:\n  yaml-derived-secret-key: yaml-derived-secret-value\n"
	)
	userPath := configHome + "/" + UserSettingsRelPath
	var logs bytes.Buffer
	env := xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile: func(path string) ([]byte, error) {
			if path == cliPath || path == userPath {
				return []byte(schema), nil
			}
			return nil, errors.New("not found")
		},
	}
	r := newWithEnv(Options{
		Conventional: true, ExplicitFiles: []string{cliPath},
		Diagnostics: slogdiag.New(&logs, true, port.LevelDebug),
	}, env)
	if rules := r.Resolve(context.Background(), sourceMatrixWorkspace(t, schema, schema)); len(rules) != 0 {
		t.Fatalf("schema-invalid files yielded rules: %+v", rules)
	}

	err := ValidateYAML([]byte(schema))
	if err == nil {
		t.Fatal("schema-invalid YAML was accepted")
	}
	if got, want := err.Error(), "invalid permission config schema at permissions (line 1)"; got != want {
		t.Fatalf("ValidateYAML error = %q, want %q", got, want)
	}
	output := logs.String()
	for _, permitted := range []string{cliPath, userPath, projectFileMecatl, projectFileMecatlLocal, "/repo", "invalid permission config schema at permissions"} {
		if !strings.Contains(output, permitted) {
			t.Fatalf("diagnostics omitted %q: %s", permitted, output)
		}
	}
	for _, forbidden := range []string{secretKey, secret} {
		if strings.Contains(err.Error(), forbidden) || strings.Contains(output, forbidden) {
			t.Fatalf("schema diagnostic leaked YAML-derived content %q: error=%q logs=%s", forbidden, err, output)
		}
	}
}
