package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestCallMcpWithQueryBrokerSupport_Scenario2_DirectCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		result       CallResult
		filter, want string
		fail         bool
	}{
		{"structured wins", CallResult{StructuredContent: json.RawMessage(`{"keep":7}`), Content: []ResourceContents{{Text: `{"keep":9}`}}}, ".keep", "7", false},
		{"first JSON text", CallResult{Content: []ResourceContents{{Text: "not JSON"}, {Text: `{"keep":8}`}, {Text: `{"keep":9}`}}}, ".keep", "8", false},
		{"remote error unchanged", CallResult{IsError: true, Content: []ResourceContents{{Text: "remote failed"}}}, ".", "remote failed", true},
		{"output bounded", CallResult{StructuredContent: json.RawMessage(`{"keep":"` + strings.Repeat("x", 30000) + `"}`)}, ".", "exceeded", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := scriptedCallTool(tc.result)
			args, _ := json.Marshal(map[string]any{"server": "direct", "tool": "list", "args": map[string]any{"page": 1}, "jq_filter": tc.filter})
			result, err := candidate.Execute(t.Context(), session.NewToolCall("direct-query", "CallMcpWithQuery", args), tool.Environment{})
			if err != nil || result.IsError != tc.fail || !strings.Contains(result.Content, tc.want) || result.CallID != "direct-query" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, ok := any(candidate).(tool.AuthorizationRequester); ok {
				t.Fatal("direct query acquired broker authorization behavior")
			}
		})
	}
}
