package mockscript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

func TestLoadCompilesTextAndToolTurns(t *testing.T) {
	path := writeScript(t, `{
		"turns": [
			{"tool_calls":[{"id":"write-1","name":"Write","args":{"path":"proof.txt","content":"ok\\n"}}]},
			{"text":"continued after the tool"}
		]
	}`)

	provider, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	first, err := provider.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	var toolName string
	for chunk, streamErr := range first {
		if streamErr != nil {
			t.Fatalf("first stream error: %v", streamErr)
		}
		if chunk.ToolCall != nil {
			toolName = chunk.ToolCall.Name
		}
	}
	if toolName != "Write" {
		t.Fatalf("first turn tool = %q, want Write", toolName)
	}

	second, err := provider.Stream(context.Background(), port.LLMRequest{})
	if err != nil {
		t.Fatalf("second Stream: %v", err)
	}
	var text string
	for chunk, streamErr := range second {
		if streamErr != nil {
			t.Fatalf("second stream error: %v", streamErr)
		}
		text += chunk.Text
	}
	if text != "continued after the tool" {
		t.Fatalf("second turn text = %q", text)
	}
}

func TestLoadRejectsInvalidOrOversizedScript(t *testing.T) {
	for name, body := range map[string]string{
		"both response shapes": `{"turns":[{"text":"x","tool_calls":[{"id":"1","name":"Write","args":{}}]}]}`,
		"unknown field":        `{"turns":[{"text":"x","surprise":true}]}`,
		"trailing value":       `{"turns":[{"text":"x"}]} {}`,
		"oversized":            `{"turns":[{"text":"` + strings.Repeat("x", maxScriptBytes) + `"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeScript(t, body)); err == nil || !strings.Contains(err.Error(), "--mock-script") {
				t.Fatalf("Load error = %v, want flag-scoped validation error", err)
			}
		})
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
