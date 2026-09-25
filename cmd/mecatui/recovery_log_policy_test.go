package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func TestServerProviderRecovery_Scenario7_HostLogDeliveryPolicy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mecatui.log")
	opened := openDiagLogWriter(stateEnv(root), false, path)
	t.Cleanup(func() { _ = opened.Closer.Close() })
	diag := slogdiag.NewText(opened.Writer)
	composed := embeddedConfig(config{}, diag)
	composed.Diagnostics.Log(context.Background(), port.LevelInfo, "recovery decision", "decision", "recovered")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "recovery decision") {
		t.Fatalf("embedded TUI file did not receive recovery log: %q", body)
	}

	quietPath := filepath.Join(root, "quiet.log")
	quiet := openDiagLogWriter(stateEnv(root), true, quietPath)
	t.Cleanup(func() { _ = quiet.Closer.Close() })
	slogdiag.NewText(quiet.Writer).Log(context.Background(), port.LevelInfo, "must be discarded")
	if _, err := os.Stat(quietPath); !os.IsNotExist(err) {
		t.Fatalf("quiet diagnostics created a log: %v", err)
	}
}
