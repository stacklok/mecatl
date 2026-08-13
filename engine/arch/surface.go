// Package arch holds the whole-graph layering assertions for mecatl's clean
// core. This file carries the ONE non-test export of the package: CorePackages,
// the canonical list of the engine module's compatibility-committed core
// packages. Everything else in the package is _test.go layering proofs.
package arch

// modulePrefix is the mecatl module path prefix shared by CorePackages and the
// layering walk in layering_test.go.
const modulePrefix = "github.com/stacklok/mecatl/"

// CorePackages is the SINGLE SOURCE OF TRUTH for the engine module's eight
// compatibility-committed core packages (engine/COMPATIBILITY.md, ADR 0037),
// as full module-internal import paths. Listed high→low in the layering order:
// domain leaves (session, governance) → domain (tool, prompt) → port → team →
// application (agent).
//
// Two mechanisms consume this list and MUST agree with it, mechanically:
//   - the layering DAG tests in this package (layering_test.go), which enforce
//     the inward-only dependency rule over exactly these packages;
//   - the public-API freshness gate in internal/apicheck, whose guardedPackages
//     set is asserted EQUAL to this list (so a new core package cannot escape
//     the gate and a removed one is noticed).
//
// This file imports NOTHING beyond the package declaration so engine/arch stays
// a trivial, dependency-free importable package — the engine module's dependency
// closure is unaffected by the root module importing it.
var CorePackages = []string{
	modulePrefix + "engine/session",
	modulePrefix + "engine/governance",
	modulePrefix + "engine/learning",
	modulePrefix + "engine/tool",
	modulePrefix + "engine/prompt",
	modulePrefix + "engine/port",
	modulePrefix + "engine/team",
	modulePrefix + "engine/agent",
}
