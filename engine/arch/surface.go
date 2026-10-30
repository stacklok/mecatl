// Package arch holds the whole-graph layering assertions for mecatl's clean
// core. This file carries the ONE non-test export of the package: CorePackages,
// the canonical list of the engine module's compatibility-committed core
// packages. Everything else in the package is _test.go layering proofs.
package arch

// modulePrefix is the mecatl module path prefix shared by CorePackages and the
// layering walk in layering_test.go.
const modulePrefix = "github.com/stacklok/mecatl/"

// CorePackages lists the engine module's compatibility-committed packages by
// full import path. The layering tests and public API snapshot gate must cover
// exactly this set. See engine/COMPATIBILITY.md for the compatibility policy.
var CorePackages = []string{
	modulePrefix + "engine/id",
	modulePrefix + "engine/kernel/history",
	modulePrefix + "engine/session",
	modulePrefix + "engine/governance",
	modulePrefix + "engine/learning",
	modulePrefix + "engine/tool",
	modulePrefix + "engine/prompt",
	modulePrefix + "engine/port",
	modulePrefix + "engine/team",
	modulePrefix + "engine/agent",
}
