package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// authoritySpecs projects a bound session's catalog view to its carried authority.
// Execution remains independently authorized at execute for stale or forged calls.
func authoritySpecs(specs []tool.ToolSpec, set governance.CapabilitySet, bound bool) []tool.ToolSpec {
	if !bound {
		return specs
	}
	filtered := make([]tool.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if authorityDisclosesTool(spec.Name, set) {
			filtered = append(filtered, spec)
		}
	}
	return filtered
}

// authorityOverlaySpecs adds per-run tools after the carried-authority projection.
func authorityOverlaySpecs(specs []tool.ToolSpec, extras []tool.Tool) []tool.ToolSpec {
	for _, extra := range extras {
		spec := extra.Spec()
		replaced := false
		for i := range specs {
			if specs[i].Name == spec.Name {
				specs[i] = spec
				replaced = true
				break
			}
		}
		if !replaced {
			specs = append(specs, spec)
		}
	}
	return specs
}

func authorityDisclosesTool(name string, set governance.CapabilitySet) bool {
	switch name {
	case tool.ToolSearchName:
		return true
	case callMcpWithQueryToolName:
		return authorityHasMCPTool(set)
	default:
		return set.AllowsTool(name)
	}
}

func authorityHasMCPTool(set governance.CapabilitySet) bool {
	for _, name := range set.Tools {
		if !strings.HasPrefix(name, "mcp__") {
			continue
		}
		server, target, ok := strings.Cut(strings.TrimPrefix(name, "mcp__"), "__")
		if ok && server != "" && target != "" {
			return true
		}
	}
	return false
}

// authorityMatchedSpec preserves ToolSearch's public JSON result shape while
// filtering it to the bound session's carried authority.
type authorityMatchedSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// authorityToolSearch executes the reserved catalog hydration control tool against
// the same carried-authority projection used for the model request.
func authorityToolSearch(call session.ToolCall, cat *tool.Catalog, set governance.CapabilitySet) session.ToolResult {
	var args struct {
		Query string `json:"query"`
	}
	if len(call.Args) > 0 {
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return session.NewToolError(call.ID, fmt.Sprintf("ToolSearch: invalid args: %v", err))
		}
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	matches := make([]authorityMatchedSpec, 0)
	for _, candidate := range cat.Tools() {
		spec := candidate.Spec()
		if spec.Name == tool.ToolSearchName || !authorityDisclosesTool(spec.Name, set) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(spec.Name), query) && !strings.Contains(strings.ToLower(spec.Description), query) {
			continue
		}
		matches = append(matches, authorityMatchedSpec{Name: spec.Name, Description: spec.Description, Schema: spec.Schema})
	}
	body, err := json.Marshal(matches)
	if err != nil {
		return session.NewToolError(call.ID, fmt.Sprintf("ToolSearch: marshal results: %v", err))
	}
	return session.NewToolResult(call.ID, string(body))
}
