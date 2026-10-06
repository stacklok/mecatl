package app

import (
	"reflect"
	"sort"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/modelhook"
)

func TestADR_0363_ContextualGuardrails_Scenario3_DefaultCoverage(t *testing.T) {
	rules, ok := compileGuardrailRules(Config{}, defaultGuardrailSpecs)
	if !ok {
		t.Fatal("default guardrail rules did not compile")
	}
	tools := []string{"Shell", "Read", "ListDir", "Grep", "Glob", "WebSearch", "WebFetch", "mcp__server__tool", "FetchMcpResource", "CallMcpWithQuery", "Edit", "Write", "Copy", "Move", "Remove", "Subagent", "Parallel", "Team"}
	var pre, post []string
	for _, name := range tools {
		if _, matched := modelhook.ResolveRule(rules, name, modelhook.PhasePre); matched {
			pre = append(pre, name)
		}
		if _, matched := modelhook.ResolveRule(rules, name, modelhook.PhasePost); matched {
			post = append(post, name)
		}
	}
	sort.Strings(pre)
	sort.Strings(post)
	wantPre := []string{"CallMcpWithQuery", "Copy", "Edit", "Move", "Parallel", "Remove", "Shell", "Subagent", "Team", "WebFetch", "WebSearch", "Write", "mcp__server__tool"}
	wantPost := []string{"CallMcpWithQuery", "FetchMcpResource", "Glob", "Grep", "ListDir", "Read", "Shell", "WebFetch", "WebSearch", "mcp__server__tool"}
	sort.Strings(wantPre)
	sort.Strings(wantPost)
	if !reflect.DeepEqual(pre, wantPre) || !reflect.DeepEqual(post, wantPost) {
		t.Fatalf("coverage pre=%v post=%v; want pre=%v post=%v", pre, post, wantPre, wantPost)
	}
}
