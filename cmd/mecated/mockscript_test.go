package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
)

func TestLoadMockScriptCompilesTextAndToolTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script.json")
	body := `{
  "turns": [
    {"tool_calls":[{"id":"write-1","name":"Write","args":{"path":"proof.txt","content":"ok\n"}}]},
    {"text":"continued after the tool"}
  ]
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	provider, err := loadMockScript(path)
	if err != nil {
		t.Fatalf("loadMockScript: %v", err)
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

func TestLoadMockScriptRejectsAmbiguousAndUnknownTurns(t *testing.T) {
	for name, body := range map[string]string{
		"both response shapes": `{"turns":[{"text":"x","tool_calls":[{"id":"1","name":"Write","args":{}}]}]}`,
		"missing response":     `{"turns":[{"delay_ms":1}]}`,
		"unknown field":        `{"turns":[{"text":"x","surprise":true}]}`,
		"excessive delay":      `{"turns":[{"delay_ms":30001,"text":"x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "script.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadMockScript(path); err == nil || !strings.Contains(err.Error(), "--mock-script") {
				t.Fatalf("loadMockScript error = %v, want flag-scoped validation error", err)
			}
		})
	}
}

func TestMockScriptFlagIsAdditiveToCannedMock(t *testing.T) {
	canned, err := parseFlags([]string{"--mock"})
	if err != nil {
		t.Fatal(err)
	}
	if !canned.useMock || canned.mockScript != "" || canned.mockProvider != nil {
		t.Fatalf("--mock changed: useMock=%v script=%q provider=%T", canned.useMock, canned.mockScript, canned.mockProvider)
	}

	scripted, err := parseFlags([]string{"--mock-script", "/tmp/script.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !scripted.useMock || scripted.mockScript != "/tmp/script.json" {
		t.Fatalf("script flag = useMock=%v path=%q", scripted.useMock, scripted.mockScript)
	}
	provider := mockllm.New(mockllm.TextTurn("scripted"))
	scripted.mockProvider = provider
	if got := appConfig(scripted, nil, nil, nil, nil, nil).MockProvider; got != provider {
		t.Fatalf("appConfig MockProvider = %T, want the loaded script provider", got)
	}
}
