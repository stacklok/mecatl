package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

func TestHarnessProjectPolicyIgnoredValueFreeWithoutDroppingDeny(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{Kinds: harnessEmptyKinds()})
	cfg.PermissionsConventional = true
	cfg.AllowAllTools = true
	cfg.GuardrailsDisabled = true
	cfg.TrustProject = true
	if err := os.MkdirAll(filepath.Join(cfg.Workspace, ".mecatl"), 0o700); err != nil {
		t.Fatal(err)
	}
	const privateValue = "PRIVATE-CONTEXT-VALUE"
	body := "harness_context: " + privateValue + "\npermissions:\n  deny: [Write]\n"
	if err := os.WriteFile(filepath.Join(cfg.Workspace, ".mecatl/settings.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	diag := &kvDiag{}
	cfg.Diagnostics = diag
	cfg.MockProvider = mockllm.New(mockllm.ToolCallTurn(session.ToolCall{ID: "denied", Name: "Write", Args: json.RawMessage(`{"path":"forbidden.txt","content":"no"}`)}), mockllm.TextTurn("done"))
	b, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	id := harnessCreate(t, b, t.Context())
	events := harnessRun(t, b, t.Context(), id, "attempt write")
	denied := false
	for _, event := range events {
		if event.ToolResult != nil && event.ToolResult.CallID == "denied" {
			denied = event.ToolResult.IsError
		}
	}
	if !denied {
		t.Fatal("ignoring project harness policy dropped configured project deny")
	}
	diag.mu.Lock()
	defer diag.mu.Unlock()
	all := fmt.Sprint(diag.msgs, diag.args)
	if !strings.Contains(all, "harness_context: IGNORING") {
		t.Fatal("missing operator-only warning")
	}
	if strings.Contains(all, privateValue) {
		t.Fatal("project harness policy value leaked through diagnostics")
	}
}
