package identitybroker

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func TestCeilingAllowsExactPairs(t *testing.T) {
	c := NewCeiling(CeilingServer{Server: "github", ExceptTools: []string{"merge_pr"}}, CeilingServer{Server: "calendar"})
	for _, tc := range []struct {
		server, tool string
		want         bool
	}{
		{"github", "read_pr", true},
		{"github", "merge_pr", false},
		{"calendar", "merge_pr", true}, // the same tool name on another server is its own pair
		{"slack", "read_pr", false},    // a server the ceiling does not grant
	} {
		if got := c.Allows(tc.server, tc.tool); got != tc.want {
			t.Errorf("allows(%q, %q) = %v, want %v", tc.server, tc.tool, got, tc.want)
		}
	}
	if (Ceiling{}).Allows("github", "read_pr") || NewCeiling().allowsServer("github") {
		t.Fatal("an empty ceiling allows a call")
	}
	if got := c.String(); got != "calendar; github except merge_pr" {
		t.Fatalf("String = %q", got)
	}
	if got := (Ceiling{}).String(); got != "none" {
		t.Fatalf("empty String = %q", got)
	}
}

// TestCeilingMatchesToolNamesExactly keeps tool names as given, as the
// harness matches a definition's exclusions: case, surrounding spaces, dots,
// and underscores are part of the name.
func TestCeilingMatchesToolNamesExactly(t *testing.T) {
	c := NewCeiling(CeilingServer{Server: " github ", ExceptTools: []string{" merge", "Delete", "pr.merge", "_x"}})
	for _, tc := range []struct {
		tool string
		want bool
	}{
		{"merge", true}, // " merge" is another name
		{" merge", false},
		{"delete", true}, // case matters
		{"Delete", false},
		{"pr.merge", false},
		{"pr", true},
		{"_x", false},
		{"x", true},
	} {
		if got := c.Allows("github", tc.tool); got != tc.want {
			t.Errorf("Allows(github, %q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

// TestCeilingStringIsUnambiguous quotes a tool name holding a separator, so
// a recorded ceiling reads back one way.
func TestCeilingStringIsUnambiguous(t *testing.T) {
	c := NewCeiling(
		CeilingServer{Server: "github", ExceptTools: []string{"a,b", "c; calendar", "x except y", "pr.merge"}},
		CeilingServer{Server: "calendar"},
	)
	want := `calendar; github except "a,b","c; calendar",pr.merge,"x except y"`
	if got := c.String(); got != want {
		t.Fatalf("String = %s, want %s", got, want)
	}
}

// TestCheckCeilingRefusalHidesTheCeiling keeps the ceiling, which can hold
// a parent's exclusions, out of the refusal the caller sees.
func TestCheckCeilingRefusalHidesTheCeiling(t *testing.T) {
	c := NewCeiling(CeilingServer{Server: "github", ExceptTools: []string{"secret_parent_exclusion"}})
	err := checkCeiling(c, MethodToolsCall, "github", "secret_parent_exclusion")
	if err == nil || strings.Contains(err.Error(), "secret_parent_exclusion") || strings.Contains(err.Error(), "github") {
		t.Fatalf("refusal = %v, want one that names no part of the ceiling", err)
	}
}

func TestCheckCeilingListsOnlyGrantedServers(t *testing.T) {
	c := NewCeiling(CeilingServer{Server: "github", ExceptTools: []string{"merge_pr"}})
	if err := checkCeiling(c, MethodToolsList, "github", ""); err != nil {
		t.Fatalf("listing a granted server: %v", err)
	}
	if err := checkCeiling(c, MethodToolsList, "calendar", ""); err == nil {
		t.Fatal("listing a server the ceiling does not grant was allowed")
	}
	if err := checkCeiling(c, MethodToolsCall, "github", "merge_pr"); err == nil {
		t.Fatal("an excluded tool was allowed")
	}
	if err := checkCeiling(c, MethodToolsCall, "github", "read_pr"); err != nil {
		t.Fatalf("a granted tool: %v", err)
	}
	if err := checkCeiling(c, "resources/read", "github", "read_pr"); err == nil {
		t.Fatal("an unknown method was allowed")
	}
}

// TestCeilingIntersectionIsWithinBoth is the design's property test: an
// effective ceiling, a definition's intersected with a parent's, allows a
// call only when both do, and every call both allow.
func TestCeilingIntersectionIsWithinBoth(t *testing.T) {
	servers := []string{"github", "calendar", "slack"}
	tools := []string{"read", "write", "merge", "delete"}
	r := rand.New(rand.NewPCG(1, 2))
	random := func() Ceiling {
		var grants []CeilingServer
		for _, s := range servers {
			if r.IntN(3) == 0 {
				continue
			}
			g := CeilingServer{Server: s}
			for _, tool := range tools {
				if r.IntN(3) == 0 {
					g.ExceptTools = append(g.ExceptTools, tool)
				}
			}
			grants = append(grants, g)
		}
		return NewCeiling(grants...)
	}
	for range 500 {
		definition, parent := random(), random()
		effective := definition.intersect(parent)
		for _, s := range append(servers, "other") {
			for _, tool := range append(tools, "other") {
				want := definition.Allows(s, tool) && parent.Allows(s, tool)
				if got := effective.Allows(s, tool); got != want {
					t.Fatalf("definition %s, parent %s: effective %s allows(%s, %s) = %v, want %v", definition, parent, effective, s, tool, got, want)
				}
			}
		}
		if !effective.equal(parent.intersect(definition)) {
			t.Fatalf("intersection of %s and %s is not symmetric", definition, parent)
		}
	}
}
