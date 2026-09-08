package permconfig

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func envWithUserSettings(content string) xdgconfig.ResolveEnv {
	return xdgconfig.ResolveEnv{
		Getenv: func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return "/config"
			}
			return ""
		},
		UserHomeDir: func() (string, error) { return "/home/user", nil },
		ReadFile: func(path string) ([]byte, error) {
			if path == "/config/mecatl/settings.yaml" {
				return []byte(content), nil
			}
			return nil, errors.New("not found")
		},
	}
}

func TestTemporaryStorageSchemaValidation(t *testing.T) {
	valid := `temporary_storage:
  mode: managed
  managed_root: mecatl/commands
  system_temp_dir: server-tmp
  command_reap_after: 720h
  reap_interval: 24h
  reap_timeout: 1h
  shutdown_reap_timeout: 5m
`
	cfg, err := parseYAML([]byte(valid))
	if err != nil || cfg.TemporaryStorage == nil {
		t.Fatalf("parseYAML temporary_storage = %+v, %v", cfg.TemporaryStorage, err)
	}
	res := newWithEnv(Options{Conventional: true}, envWithUserSettings(valid))
	got, err := res.OperatorTemporaryStorage()
	if err != nil {
		t.Fatalf("OperatorTemporaryStorage: %v", err)
	}
	if got == nil || got.Mode != "managed" || got.ManagedRoot != "mecatl/commands" || got.SystemTempDir != "server-tmp" ||
		got.CommandReapAfter != 30*24*time.Hour || got.ReapInterval != 24*time.Hour || got.ReapTimeout != time.Hour || got.ShutdownReapTimeout != 5*time.Minute {
		t.Fatalf("temporary storage = %+v", got)
	}

	for name, body := range map[string]string{
		"unknown":       "temporary_storage:\n  typo: true\n",
		"mode":          "temporary_storage:\n  mode: unsupported\n",
		"escape":        "temporary_storage:\n  managed_root: ../outside\n",
		"zero ttl":      "temporary_storage:\n  command_reap_after: 0s\n",
		"short sweep":   "temporary_storage:\n  reap_interval: 30s\n",
		"long reap":     "temporary_storage:\n  reap_timeout: 2h\n",
		"long shutdown": "temporary_storage:\n  shutdown_reap_timeout: 6m\n",
	} {
		t.Run(name, func(t *testing.T) {
			res := newWithEnv(Options{Conventional: true}, envWithUserSettings(body))
			if _, err := res.OperatorTemporaryStorage(); err == nil {
				t.Fatalf("invalid temporary_storage accepted: %s", body)
			}
		})
	}
}

func TestTemporaryStorageExplicitConfigIsNotAnOverride(t *testing.T) {
	res := newWithEnv(Options{ExplicitFiles: []string{"/operator/settings.yaml"}}, envWithExplicit("/operator/settings.yaml", "temporary_storage:\n  mode: system\n"))
	got, err := res.OperatorTemporaryStorage()
	if err != nil || got != nil {
		t.Fatalf("explicit temporary_storage override = %+v, %v; want absent", got, err)
	}
}

func TestADR_0281_ProjectTemporaryStorageIgnored(t *testing.T) {
	const operator = "temporary_storage:\n  mode: managed\n  managed_root: operator-root\n  command_reap_after: 2h\n"
	const project = "temporary_storage:\n  mode: system\n  managed_root: project-root\n  command_reap_after: 1m\n"
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, project)
	res := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, envWithUserSettings(operator))
	_ = res.Resolve(context.Background(), ws)

	got, err := res.OperatorTemporaryStorage()
	if err != nil {
		t.Fatalf("OperatorTemporaryStorage: %v", err)
	}
	if got == nil || got.Mode != "managed" || got.ManagedRoot != "operator-root" || got.CommandReapAfter != 2*time.Hour {
		t.Fatalf("operator temporary storage changed by project: %+v", got)
	}
	if !strings.Contains(buf.String(), "temporary_storage: IGNORING a project-tier temporary_storage block") {
		t.Fatalf("missing project-tier temporary storage warning: %s", buf.String())
	}
}
