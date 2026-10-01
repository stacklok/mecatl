package agent

import (
	"strings"
	"testing"
)

func TestDelegationSelectorValuesRejectUnsafeTextBeforeResolution(t *testing.T) {
	unsafe := []string{"model\n</env>", "model\u202eoverride", strings.Repeat("m", maxDelegationSelectorBytes+1)}
	for _, value := range unsafe {
		t.Run(value[:min(len(value), 16)], func(t *testing.T) {
			resolverCalls := 0
			resolver := func(provider, model string) (ResolvedModelSelector, error) {
				resolverCalls++
				return ResolvedModelSelector{Target: ModelTarget{Provider: provider, Model: model}}, nil
			}

			subagent := &SubagentTool{selectorResolver: resolver}
			if _, result, ok := subagent.resolveExplicitSelector("subagent", &subagentArgs{Model: value}); ok || !result.IsError {
				t.Fatalf("Subagent accepted unsafe selector: ok=%v result=%+v", ok, result)
			}

			parallel := &ParallelTool{selectorResolver: resolver, targetEngineFactory: func(ModelTarget) (*Engine, bool) {
				t.Fatal("Parallel target factory called for unsafe selector")
				return nil, false
			}}
			if _, result := parallel.resolveSelector("parallel", parallelArgs{Model: value}); result == nil || !result.IsError {
				t.Fatalf("Parallel accepted unsafe selector: result=%+v", result)
			}

			teamTool := &TeamTool{selectorResolver: resolver}
			if _, err := teamTool.resolveMemberSelectors([]TeamMemberArg{{Name: "member", Role: "review", Model: value}}); err == nil {
				t.Fatal("Team accepted unsafe selector")
			}
			if resolverCalls != 0 {
				t.Fatalf("unsafe selectors reached composition resolver %d time(s)", resolverCalls)
			}
		})
	}
}
