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
