package agentfs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// staticSource is a test AgentSource that returns a fixed set of defs and skips.
type staticSource struct {
	discovered []Discovered
	skips      []SkipError
	err        error
}

func (s staticSource) Agents(context.Context) ([]Discovered, []SkipError, error) {
	return s.discovered, s.skips, s.err
}

// TestMultiSourcePrecedenceShadows asserts earlier-source-wins on a name
// collision, with a shadow diagnostic for the dropped lower-precedence def
// (the diagnostic carries the shadowed AND kept entries' Detail locators).
func TestMultiSourcePrecedenceShadows(t *testing.T) {
	high := staticSource{discovered: []Discovered{
		{Def: AgentDef{Name: "rev", Description: "high"}, Detail: "explicit: /high/rev.md"},
	}}
	low := staticSource{discovered: []Discovered{
		{Def: AgentDef{Name: "rev", Description: "low"}, Detail: "user(xdg): /low/rev.md"},
		{Def: AgentDef{Name: "only-low", Description: "L"}, Detail: "user(xdg): /low/only.md"},
	}}

	defs, skips, err := NewMultiSource(high, low).Agents(t.Context())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("want 2 defs (rev + only-low), got %d: %+v", len(defs), defs)
	}
	// rev kept from the high-precedence source.
	for _, d := range defs {
		if d.Def.Name == "rev" && d.Def.Description != "high" {
			t.Fatalf("rev should be the high-precedence def, got %q", d.Def.Description)
		}
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "shadowed") {
		t.Fatalf("want 1 shadow skip, got %v", skips)
	}
	// The shadow diagnostic is located by the SHADOWED entry's detail and names
	// the KEPT entry's detail (the messages otherwise verbatim from before).
	if skips[0].Path != "user(xdg): /low/rev.md" || !strings.Contains(skips[0].Reason, `kept "explicit: /high/rev.md"`) {
		t.Fatalf("shadow skip detail mismatch: %+v", skips[0])
	}
	// A shadow is a DROP (the lower-precedence def is excluded), so it carries
	// Fatal==true — the composition root logs it as "agent def dropped".
	if !skips[0].Fatal {
		t.Fatalf("shadow skip should be Fatal (dropped), got %+v", skips[0])
	}
}

func TestMultiSourceFatalErrorPropagates(t *testing.T) {
	boom := errors.New("io fault")
	_, _, err := NewMultiSource(staticSource{err: boom}).Agents(t.Context())
	if !errors.Is(err, boom) {
		t.Fatalf("want propagated fatal error, got %v", err)
	}
}

func TestMultiSourceNilEntriesIgnored(t *testing.T) {
	defs, _, err := NewMultiSource(nil, staticSource{discovered: []Discovered{{Def: AgentDef{Name: "a", Description: "d"}}}}, nil).Agents(t.Context())
	if err != nil || len(defs) != 1 {
		t.Fatalf("nil sources should be skipped, got %v err=%v", defs, err)
	}
}
