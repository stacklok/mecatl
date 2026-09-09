package permstore

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

func rule(tool, pattern string) governance.Rule {
	return governance.Rule{Scope: governance.ScopeUser, Tool: tool, Pattern: pattern, Effect: governance.Allow, Exact: true}
}

func TestRecordAndList(t *testing.T) {
	m := New()
	r := rule("Shell", "git status")
	m.Record("s1", r)
	got := m.Rules("s1")
	if len(got) != 1 || got[0] != r {
		t.Fatalf("expected one learned rule, got %+v", got)
	}
	// Idempotent: recording the identical rule does not grow the set.
	m.Record("s1", r)
	if got := m.Rules("s1"); len(got) != 1 {
		t.Fatalf("expected dedup to keep one rule, got %d", len(got))
	}
	// A distinct rule is added.
	m.Record("s1", rule("Shell", "git diff"))
	if got := m.Rules("s1"); len(got) != 2 {
		t.Fatalf("expected two distinct rules, got %d", len(got))
	}
}

func TestPerSessionKeying(t *testing.T) {
	m := New()
	m.Record("a", rule("Shell", "git status"))
	if got := m.Rules("b"); got != nil {
		t.Fatalf("session b must not see session a's rules, got %+v", got)
	}
	if got := m.Rules("a"); len(got) != 1 {
		t.Fatalf("session a should have its own rule, got %+v", got)
	}
}

func TestRulesReturnsCopy(t *testing.T) {
	m := New()
	m.Record("s1", rule("Shell", "git status"))
	snap := m.Rules("s1")
	snap[0] = rule("Shell", "rm -rf /") // mutate the returned slice
	if got := m.Rules("s1"); got[0].Pattern != "git status" {
		t.Fatalf("Rules must return a copy; internal state was mutated to %q", got[0].Pattern)
	}
}

func TestForgetEviction(t *testing.T) {
	m := New()
	m.Record("s1", rule("Shell", "git status"))
	m.Forget("s1")
	if got := m.Rules("s1"); got != nil {
		t.Fatalf("expected eviction to clear the session, got %+v", got)
	}
	m.Forget("unknown") // idempotent no-op
}

func TestRecordCapsPerSession(t *testing.T) {
	m := New()
	// Record well past the cap with DISTINCT rules (each pattern differs so dedup
	// does not collapse them). The slice must saturate at maxRulesPerSession.
	for i := 0; i < maxRulesPerSession+50; i++ {
		m.Record("s1", rule("Shell", "cmd-"+strconv.Itoa(i)))
	}
	if got := len(m.Rules("s1")); got != maxRulesPerSession {
		t.Fatalf("expected slice capped at %d distinct rules, got %d", maxRulesPerSession, got)
	}
}

func TestRecordDedupAtCap(t *testing.T) {
	m := New()
	// Fill exactly to the cap with distinct rules.
	for i := 0; i < maxRulesPerSession; i++ {
		m.Record("s1", rule("Shell", "cmd-"+strconv.Itoa(i)))
	}
	if got := len(m.Rules("s1")); got != maxRulesPerSession {
		t.Fatalf("setup: expected %d rules, got %d", maxRulesPerSession, got)
	}
	// Re-Record an identical EXISTING rule at the cap: dedup wins over the cap, so
	// the count is unchanged and the rule is still present (idempotent no-op, not a
	// cap-drop that loses the rule).
	existing := rule("Shell", "cmd-0")
	m.Record("s1", existing)
	got := m.Rules("s1")
	if len(got) != maxRulesPerSession {
		t.Fatalf("re-record at cap changed count to %d, want %d", len(got), maxRulesPerSession)
	}
	found := false
	for _, r := range got {
		if r == existing {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("re-recorded existing rule missing after dedup-at-cap")
	}
}

func TestConcurrentAccess(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := session.SessionID("s")
			m.Record(id, rule("Shell", "cmd"))
			_ = m.Rules(id)
			if i%10 == 0 {
				m.Forget(session.SessionID("other"))
			}
		}(i)
	}
	wg.Wait()
	// All records were the same rule; dedup keeps exactly one.
	if got := m.Rules("s"); len(got) != 1 {
		t.Fatalf("expected one deduped rule after concurrent records, got %d", len(got))
	}
}
