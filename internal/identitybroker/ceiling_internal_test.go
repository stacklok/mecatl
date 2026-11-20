package identitybroker

import (
	"math/rand/v2"
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
		if got := c.allows(tc.server, tc.tool); got != tc.want {
			t.Errorf("allows(%q, %q) = %v, want %v", tc.server, tc.tool, got, tc.want)
		}
	}
	if (Ceiling{}).allows("github", "read_pr") || NewCeiling().allowsServer("github") {
		t.Fatal("an empty ceiling allows a call")
	}
	if got := c.String(); got != "calendar; github except merge_pr" {
		t.Fatalf("String = %q", got)
	}
	if got := (Ceiling{}).String(); got != "none" {
		t.Fatalf("empty String = %q", got)
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
				want := definition.allows(s, tool) && parent.allows(s, tool)
				if got := effective.allows(s, tool); got != want {
					t.Fatalf("definition %s, parent %s: effective %s allows(%s, %s) = %v, want %v", definition, parent, effective, s, tool, got, want)
				}
			}
		}
		if !effective.equal(parent.intersect(definition)) {
			t.Fatalf("intersection of %s and %s is not symmetric", definition, parent)
		}
	}
}
