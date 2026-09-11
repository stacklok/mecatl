package permconfig

import (
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
)

// findRule returns the first rule matching tool+pattern, or nil.
func findRule(rules []governance.Rule, tool, pattern string) *governance.Rule {
	for i := range rules {
		if rules[i].Tool == tool && rules[i].Pattern == pattern {
			return &rules[i]
		}
	}
	return nil
}

// Fail-safe row 1: a WebFetch(domain:...) ALLOW is DEMOTED to Ask and reported.
func TestClaudeImportWebFetchDomainAllowDemotedToAsk(t *testing.T) {
	data := []byte(`{"permissions":{"allow":["WebFetch(domain:example.com)"]}}`)
	var report Report
	rules, err := importClaude(data, governance.ScopeSharedProject, &report)
	if err != nil {
		t.Fatalf("importClaude: %v", err)
	}
	r := findRule(rules, "WebFetch", "domain:example.com")
	if r == nil {
		t.Fatalf("WebFetch rule missing: %+v", rules)
		return
	}
	if r.Effect != governance.Ask {
		t.Fatalf("WebFetch domain allow must be DEMOTED to Ask, got %v", r.Effect)
	}
	if len(report.Demoted) != 1 {
		t.Fatalf("expected 1 demotion reported, got %+v", report.Demoted)
	}
}

// Issue #26: a bare WebSearch ALLOW imports VERBATIM as Allow — NO demotion (its
// outbound payload is a query string, lower-risk than WebFetch's arbitrary-URL
// fetch, and egress is provider-gated). This was considered and consciously not
// demoted (see importClaudeBucket's fail-safe table).
func TestClaudeImportWebSearchAllowNotDemoted(t *testing.T) {
	data := []byte(`{"permissions":{"allow":["WebSearch"]}}`)
	var report Report
	rules, err := importClaude(data, governance.ScopeSharedProject, &report)
	if err != nil {
		t.Fatalf("importClaude: %v", err)
	}
	r := findRule(rules, "WebSearch", "")
	if r == nil {
		t.Fatalf("WebSearch rule missing: %+v", rules)
		return
	}
	if r.Effect != governance.Allow {
		t.Fatalf("bare WebSearch allow must import VERBATIM as Allow (no demotion), got %v", r.Effect)
	}
	if len(report.Demoted) != 0 {
		t.Fatalf("WebSearch must NOT be demoted, got %+v", report.Demoted)
	}
}

// Fail-safe row 2: a Read(~/...) rule is kept but reported INERT ("~" unexpanded).
func TestClaudeImportTildeLeftInert(t *testing.T) {
	data := []byte(`{"permissions":{"allow":["Read(~/.zshrc)"]}}`)
	var report Report
	rules, err := importClaude(data, governance.ScopeUser, &report)
	if err != nil {
		t.Fatalf("importClaude: %v", err)
	}
	r := findRule(rules, "Read", "~/.zshrc")
	if r == nil {
		t.Fatalf("Read rule missing: %+v", rules)
		return
	}
	if r.Pattern != "~/.zshrc" {
		t.Fatalf("the \"~\" must be left UNEXPANDED, got %q", r.Pattern)
	}
	if len(report.Inert) != 1 {
		t.Fatalf("expected 1 inert rule reported, got %+v", report.Inert)
	}
}

// Fail-safe row 3: an unparseable spec is DROPPED + reported.
func TestClaudeImportUnparseableDropped(t *testing.T) {
	data := []byte(`{"permissions":{"allow":["Shell(unterminated","",  "Read"]}}`)
	var report Report
	rules, err := importClaude(data, governance.ScopeUser, &report)
	if err != nil {
		t.Fatalf("importClaude: %v", err)
	}
	// "Read" survives; "Shell(unterminated" and "" are dropped.
	if findRule(rules, "Read", "") == nil {
		t.Fatalf("the valid Read rule should survive: %+v", rules)
	}
	if len(report.Dropped) != 2 {
		t.Fatalf("expected 2 dropped specs, got %+v", report.Dropped)
	}
}

// Fail-safe NEVER widens: a deny/ask bucket is imported verbatim (no demotion),
// and an allow is never escalated to a deny/ask except the WebFetch demotion.
func TestClaudeImportNeverWidens(t *testing.T) {
	data := []byte(`{"permissions":{"deny":["Shell(rm:*)"],"ask":["Shell(git push:*)"],"allow":["Shell(go test:*)"]}}`)
	var report Report
	rules, err := importClaude(data, governance.ScopeSharedProject, &report)
	if err != nil {
		t.Fatalf("importClaude: %v", err)
	}
	if r := findRule(rules, "Shell", "rm*"); r == nil || r.Effect != governance.Deny {
		t.Fatalf("deny must import verbatim: %+v", rules)
	}
	if r := findRule(rules, "Shell", "git push*"); r == nil || r.Effect != governance.Ask {
		t.Fatalf("ask must import verbatim: %+v", rules)
	}
	if r := findRule(rules, "Shell", "go test*"); r == nil || r.Effect != governance.Allow {
		t.Fatalf("a non-WebFetch allow must stay an allow: %+v", rules)
	}
	if !report.Empty() {
		t.Fatalf("this config has no lossy rows; report should be empty, got %+v", report)
	}
}

func TestClaudeImportEmptyAndMalformed(t *testing.T) {
	var report Report
	if rules, err := importClaude(nil, governance.ScopeUser, &report); err != nil || rules != nil {
		t.Fatalf("empty input: rules=%v err=%v", rules, err)
	}
	if _, err := importClaude([]byte("{not json"), governance.ScopeUser, &report); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
