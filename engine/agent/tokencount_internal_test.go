package agent

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type exactStringCounter struct{}

func (exactStringCounter) Count(text string) int               { return len(text) }
func (exactStringCounter) CountMessages([]session.Message) int { return 0 }

func TestRequestTokenEstimateCountsProviderTokensAndOpaqueReplay(t *testing.T) {
	counter := HeuristicTokenCounter{CharsPerToken: 1}
	req := port.LLMRequest{Messages: []port.LLMMessage{
		{Role: session.RoleAssistant, ToolCalls: []port.LLMToolCall{{ID: "pair", ItemID: "item", Name: "Read", Args: []byte(`{}`)}}, Replay: &session.ProviderReplay{Format: "opaque", Version: 3, Data: []byte{0, 255, 1}}},
		{Role: session.RoleTool, ToolResult: &port.LLMToolResult{CallID: "pair", Content: "body"}},
	}}
	want := 2*perMessageOverhead + perToolCallOverhead + len("pairitemRead{}") + len("opaque") + 3 + len("pairbody")
	if got := estimateRequestTokens(counter, req); got != want {
		t.Fatalf("DTO estimate=%d, want %d", got, want)
	}
}

func TestEstimateRequestTokensOptimizedCountersPreserveAccounting(t *testing.T) {
	req := port.LLMRequest{
		System: prompt.Layered{StablePrefix: "stable", VolatileSuffix: "volatile"},
		Tools:  []tool.ToolSpec{{Name: "Tool", Description: "description", Schema: []byte(`{"type":"object"}`)}},
	}
	want := len(req.System.Render()) + perToolSpecOverhead +
		len(req.Tools[0].Name) + len(req.Tools[0].Description) + len(req.Tools[0].Schema)
	if got := estimateRequestTokens(exactStringCounter{}, req); got != want {
		t.Fatalf("fallback estimate = %d, want %d", got, want)
	}
	if got := estimateRequestTokens(HeuristicTokenCounter{CharsPerToken: 1}, req); got != want {
		t.Fatalf("optimized heuristic estimate = %d, want %d", got, want)
	}
}

func TestHeuristicRequestEstimateDoesNotAllocateForSchemaOrLayeredSystem(t *testing.T) {
	req := port.LLMRequest{
		System: prompt.Layered{StablePrefix: "stable", VolatileSuffix: "volatile"},
		Tools:  []tool.ToolSpec{{Name: "Tool", Description: "description", Schema: []byte(`{"type":"object"}`)}},
	}
	counter := HeuristicTokenCounter{}
	if allocs := testing.AllocsPerRun(100, func() {
		_ = estimateRequestTokens(counter, req)
	}); allocs != 0 {
		t.Fatalf("estimateRequestTokens allocations = %v, want 0", allocs)
	}
}
