package ui

import (
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
)

// TestChildIDPrefixesMatchEngine is a tripwire: the ui package mirrors the
// child-session id prefixes (subagentIDPrefix/teamIDPrefix/parallelIDPrefix in
// sessions.go) from engine/agent/childregistry.go's exported constants
// (SubagentSessionPrefix/TeamSessionPrefix/ParallelSessionPrefix) because the
// ui package must not import engine/agent in PRODUCTION code — the id is an
// opaque string on the wire and the prefix is a stable documented contract.
// If the engine renames a prefix, this test fails so the TUI's child-vs-top-
// level classification cannot silently drift.
//
// This is a TEST-ONLY import of engine/agent (AGENTS.md: "Core test files may
// import the engine/adapter/* reference adapters"); production ui code imports
// no engine/... package.
func TestChildIDPrefixesMatchEngine(t *testing.T) {
	cases := []struct {
		name   string
		ui     string
		engine string
	}{
		{"subagent", subagentIDPrefix, agent.SubagentSessionPrefix},
		{"team", teamIDPrefix, agent.TeamSessionPrefix},
		{"parallel", parallelIDPrefix, agent.ParallelSessionPrefix},
	}
	for _, c := range cases {
		if c.ui != c.engine {
			t.Errorf("%s prefix drift: ui = %q, engine/agent = %q — "+
				"a prefix was renamed in the engine; sync sessions.go", c.name, c.ui, c.engine)
		}
	}
}
