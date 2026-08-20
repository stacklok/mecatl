package governance

import (
	"errors"
	"strings"
)

// ErrDelegationDepthExhausted reports an attempt to derive a child after all
// remaining delegation hops have been consumed.
var ErrDelegationDepthExhausted = errors.New("governance: delegation depth exhausted")

// MCPResourceCapability returns the reserved opaque capability for resources
// exposed by one named MCP server. It is a capability only, never a catalog tool.
func MCPResourceCapability(server string) string {
	return "mcp_resource__" + strings.TrimSpace(server)
}

// CapabilitySet is the authority carried by a run. It is a plain value: tool
// names are exact capabilities, RemainingDelegationDepth is the number of child
// derivations still available, and the posture flags constrain execution.
//
// The zero value is a valid empty set. A zero remaining depth means no further
// delegation is allowed, while it remains a valid requirement in Contains.
type CapabilitySet struct {
	Tools                    []string
	RemainingDelegationDepth int
	FileSystem               bool
	DirectWrite              bool
}

// Narrow returns the capability set allowed by both inputs. It only removes
// tools, lowers depth, and turns posture flags off; it never widens authority.
func Narrow(left, right CapabilitySet) CapabilitySet {
	return CapabilitySet{
		Tools:                    intersectTools(left.Tools, right.Tools),
		RemainingDelegationDepth: minDepth(left.RemainingDelegationDepth, right.RemainingDelegationDepth),
		FileSystem:               left.FileSystem && right.FileSystem,
		DirectWrite:              left.DirectWrite && right.DirectWrite,
	}
}

// ConsumeDelegationHop derives the child set after consuming one remaining
// delegation hop. It rejects exhaustion instead of silently clamping depth.
func ConsumeDelegationHop(set CapabilitySet) (CapabilitySet, error) {
	if set.RemainingDelegationDepth <= 0 {
		return CapabilitySet{}, ErrDelegationDepthExhausted
	}
	set.Tools = append([]string(nil), set.Tools...)
	set.RemainingDelegationDepth--
	return set, nil
}

// Contains reports whether set includes every capability required by other.
func (set CapabilitySet) Contains(other CapabilitySet) bool {
	return set.RemainingDelegationDepth >= other.RemainingDelegationDepth &&
		(!other.FileSystem || set.FileSystem) &&
		(!other.DirectWrite || set.DirectWrite) &&
		containsTools(set.Tools, other.Tools)
}

// AllowsTool reports whether name is an exact member of the capability set.
func (set CapabilitySet) AllowsTool(name string) bool {
	return containsTools(set.Tools, []string{name})
}

func containsTools(available, required []string) bool {
	availableSet := make(map[string]struct{}, len(available))
	for _, tool := range available {
		availableSet[tool] = struct{}{}
	}
	for _, tool := range required {
		if _, ok := availableSet[tool]; !ok {
			return false
		}
	}
	return true
}

func intersectTools(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, tool := range right {
		rightSet[tool] = struct{}{}
	}
	seen := make(map[string]struct{}, len(left))
	tools := make([]string, 0, len(left))
	for _, tool := range left {
		if _, ok := rightSet[tool]; !ok {
			continue
		}
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		tools = append(tools, tool)
	}
	return tools
}

func minDepth(left, right int) int {
	if left < right {
		return left
	}
	return right
}
