package app

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
)

// TestRoutableAgentNamesMatrix pins the composition-side routable-set rules (issue #286):
// a def is routable iff it expressed NO model intent AND does not switch provider AND has no
// inline MCP. ANY non-empty def.Model (inherit / built-in alias / concrete / unknown alias)
// pins it; a provider switch away from the parent or an inline MCP server excludes it; an
// unknown provider falls back to the parent (still routable); a reference-only MCP is fine.
func TestRoutableAgentNamesMatrix(t *testing.T) {
	defs := []agents.AgentDef{
		{Name: "unpinned"},                                // routable
		{Name: "inherit-pin", Model: "inherit"},           // pinned (explicit inherit)
		{Name: "alias-pin", Model: "sonnet"},              // pinned (built-in alias)
		{Name: "concrete-pin", Model: "gpt-4o"},           // pinned (concrete id)
		{Name: "unknown-alias-pin", Model: "zzz-unknown"}, // pinned (any non-empty)
		{Name: "switched", Provider: "other"},             // excluded (known provider switch)
		{Name: "unknown-provider", Provider: "ghost"},     // routable (unknown ⇒ falls back to parent)
		{Name: "inline-mcp", MCPServers: []tool.AgentMCPServer{{Name: "x", URL: "http://h/mcp"}}}, // excluded (inline MCP)
		{Name: "reference-mcp", MCPServers: []tool.AgentMCPServer{{Name: "github"}}},              // routable (reference-only MCP)
	}
	reg := agents.NewRegistry(defs)
	// Parent provider = providerMock; "other" is a KNOWN second provider (a real switch).
	provReg := twoProviderReg(nil, providerMock, "parent-model", nil, "other")

	got := routableAgentNames(provReg, reg, providerMock)
	want := []string{"reference-mcp", "unknown-provider", "unpinned"} // sorted
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routableAgentNames = %v, want %v", got, want)
	}
	pinnedWant := []string{"alias-pin", "concrete-pin", "inherit-pin", "unknown-alias-pin"}
	if pinnedGot := pinnedAgentNames(reg); !reflect.DeepEqual(pinnedGot, pinnedWant) {
		t.Fatalf("pinnedAgentNames = %v, want %v", pinnedGot, pinnedWant)
	}
}

// TestRoutableAgentNamesByteIdenticalDefaults pins the no-op paths: a nil registry, and a
// registry of only PINNED defs, both yield nil (no def routes — byte-identical to pre-#286).
func TestRoutableAgentNamesByteIdenticalDefaults(t *testing.T) {
	if got := routableAgentNames(nil, nil, providerMock); got != nil {
		t.Fatalf("nil registry must yield nil; got %v", got)
	}
	if got := pinnedAgentNames(nil); got != nil {
		t.Fatalf("nil registry must yield no pinned names; got %v", got)
	}
	pinnedOnly := agents.NewRegistry([]agents.AgentDef{
		{Name: "a", Model: "inherit"},
		{Name: "b", Model: "gpt-4o"},
	})
	if got := routableAgentNames(regForTest(nil, providerMock, "m"), pinnedOnly, providerMock); got != nil {
		t.Fatalf("a registry of only pinned defs must yield nil; got %v", got)
	}
	if got, want := pinnedAgentNames(pinnedOnly), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pinnedAgentNames = %v, want %v", got, want)
	}
}
