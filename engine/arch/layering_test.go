// Package arch holds the whole-graph layering assertions for mecatl's clean
// core. These tests are the DAG half of the machine-enforced layering rule
// (CLAUDE.md, "The layering rule"); the per-file half is the depguard allowlist
// in .golangci.yml. depguard catches a DIRECT heavy-adapter import in any one
// core file; these tests catch what depguard cannot express: import CYCLES among
// the core packages and the TRANSITIVE direction of dependencies (a core package
// must not reach an adapter, contracts/gen, an LLM SDK, or grpc through ANY chain).
//
// Everything here is hermetic and offline: imports are resolved from the on-disk
// module via go/build.Default — no network, no go.mod change, mirroring the
// existing go/build walker in engine/prompt/layering_test.go and the import
// tripwire in engine/port/diagnostics_imports_test.go.
package arch

import (
	"errors"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corePackages is the seven-package clean core whose inward-only dependency rule
// these tests enforce. It is an alias for the package-exported CorePackages
// (surface.go) — the SINGLE source of truth shared with internal/apicheck's
// public-API gate — so the layering tests and the gate can never drift.
var corePackages = CorePackages

// forbiddenPrefixes are the "outward / heavy" dependency families a core package
// must never reach, even transitively. Hitting any of these is the canonical
// layering violation: domain/port/agent depend on adapters via INJECTED ports,
// never by importing them. On top of these named prefixes, the walk enforces the
// SELF-CONTAINMENT rule directly (violatesSelfContainment): any module-internal
// import that is not under engine/ — the heavy adapters, the composition root,
// contracts/gen, everything outside the importable core — is a violation, so the
// engine tree can become its own module without dragging the host repo along.
var forbiddenPrefixes = []string{
	modulePrefix + "engine/adapter/",         // the in-tree reference adapters (test-only; never production core)
	"github.com/openai/openai-go",            // OpenAI SDK
	"github.com/anthropics/anthropic-sdk-go", // Anthropic SDK
	"google.golang.org/grpc",                 // gRPC runtime
}

// enginePrefix is the only module-internal subtree a core package may import
// from (and, per forbiddenPrefixes, not its adapter/ branch in production code).
const enginePrefix = modulePrefix + "engine/"

// violatesSelfContainment reports whether imp is a module-internal import that
// escapes the engine/ subtree — the rule that keeps the core extractable as a
// standalone module (no reach into the host repo's adapters, composition root,
// or generated contracts through ANY chain).
func violatesSelfContainment(imp string) bool {
	return strings.HasPrefix(imp, modulePrefix) && !strings.HasPrefix(imp, enginePrefix)
}

// coreImportRule is one row of the direction table: the EXACT set of mecatl-core
// packages and external (non-stdlib) packages a given core tier may DIRECTLY
// import. "Exact" is load-bearing — this is what makes the table catch a
// wrong-DIRECTION but non-cyclic, non-adapter intra-core edge (e.g. session
// importing tool, or tool reaching backwards into prompt). depguard's strict
// per-file allow-lists already catch this; the DAG layer owns direction
// symmetrically so neither mechanism is the sole guardian.
//
// The sets MUST match the verified import map in CLAUDE.md's "layering rule"
// exactly — loosening any row defeats the test's purpose.
type coreImportRule struct {
	pkg             string          // the core package under test
	allowedCore     map[string]bool // exact mecatl-core packages it may import
	allowedExternal map[string]bool // exact non-stdlib external packages it may import
	desc            string          // human rule cited on failure
}

// coreImportRules is the direction table covering ALL seven tiers. Governance
// remains session-free; session may carry governance capability values. Every
// higher tier names only the lower tiers it legitimately depends on — never
// sideways, never upward.
var coreImportRules = []coreImportRule{
	{
		pkg: modulePrefix + "engine/session",
		allowedCore: map[string]bool{
			modulePrefix + "engine/governance": true,
		},
		desc: "session may import governance's capability value + stdlib; governance stays session-free",
	},
	{
		pkg:             modulePrefix + "engine/governance",
		allowedCore:     map[string]bool{},
		allowedExternal: map[string]bool{"mvdan.cc/sh/v3/syntax": true},
		desc:            "governance is the session-free domain leaf: it may import stdlib plus mvdan.cc/sh/v3/syntax for private Shell compatibility classification",
	},
	{
		pkg: modulePrefix + "engine/learning",
		allowedCore: map[string]bool{
			modulePrefix + "engine/governance": true,
			modulePrefix + "engine/session":    true,
			modulePrefix + "engine/tool":       true,
		},
		desc: "learning may import governance's canonical untrusted framing, session, tool's canonical memory security helpers, and stdlib",
	},
	{
		pkg: modulePrefix + "engine/tool",
		allowedCore: map[string]bool{
			modulePrefix + "engine/session": true,
		},
		desc: "tool may import only session + stdlib (it holds the FileSystem/Workspace interfaces)",
	},
	{
		pkg: modulePrefix + "engine/prompt",
		allowedCore: map[string]bool{
			modulePrefix + "engine/session": true,
			modulePrefix + "engine/tool":    true,
		},
		desc: "prompt may import only session + tool + stdlib (NOT port — prompt's ports live in prompt)",
	},
	{
		pkg: modulePrefix + "engine/team",
		allowedCore: map[string]bool{
			modulePrefix + "engine/session": true,
		},
		desc: "team may import only session + stdlib (the agent-team value types)",
	},
	{
		pkg: modulePrefix + "engine/port",
		allowedCore: map[string]bool{
			modulePrefix + "engine/session":    true,
			modulePrefix + "engine/governance": true,
			modulePrefix + "engine/prompt":     true,
			modulePrefix + "engine/tool":       true,
		},
		desc: "port may import only domain (session, governance, prompt, tool) + stdlib — never team, agent, or anything outward",
	},
	{
		pkg: modulePrefix + "engine/agent",
		allowedCore: map[string]bool{
			modulePrefix + "engine/session":    true,
			modulePrefix + "engine/learning":   true,
			modulePrefix + "engine/governance": true,
			modulePrefix + "engine/prompt":     true,
			modulePrefix + "engine/tool":       true,
			modulePrefix + "engine/port":       true,
			modulePrefix + "engine/team":       true,
		},
		allowedExternal: map[string]bool{
			"golang.org/x/sync/errgroup": true,
		},
		desc: "agent may import only domain + port + team + stdlib + golang.org/x/sync/errgroup — adapters are INJECTED, never imported",
	},
}

// directNonTestImports returns the NON-TEST imports (bp.Imports, never
// bp.TestImports) of a single mecatl package, resolved from the on-disk module.
// A package that fails to resolve (should not happen for our own packages) fails
// the test loudly rather than silently skipping.
func directNonTestImports(t *testing.T, pkg string) []string {
	t.Helper()
	bp, err := build.Import(pkg, "", 0)
	if err != nil {
		t.Fatalf("resolve %s from on-disk module: %v", pkg, err)
	}
	return bp.Imports
}

// walkTransitiveNonTestImports collects the transitive closure of NON-TEST
// imports reachable from root, following only mecatl-internal edges (stdlib and
// third-party leaves are recorded but not descended into — they cannot loop back
// into our core). visit is called once per edge (from -> imp) so callers can
// assert on every edge in the graph, not just the node set.
func walkTransitiveNonTestImports(t *testing.T, root string, visit func(from, imp string)) {
	t.Helper()
	seen := map[string]bool{}
	var walk func(pkg string)
	walk = func(pkg string) {
		if seen[pkg] {
			return
		}
		seen[pkg] = true
		bp, err := build.Import(pkg, "", 0)
		if err != nil {
			return // stdlib / third-party leaf we cannot resolve here; not our concern
		}
		for _, imp := range bp.Imports {
			visit(pkg, imp)
			if strings.HasPrefix(imp, modulePrefix) {
				walk(imp)
			}
		}
	}
	walk(root)
}

// TestTablesNonEmpty is a guard against an accidental edit silently neutering the
// walk into a no-op: if corePackages, forbiddenPrefixes, or coreImportRules were
// emptied, the other tests would pass vacuously. Fail loudly instead.
func TestTablesNonEmpty(t *testing.T) {
	if len(corePackages) == 0 {
		t.Fatal("corePackages is empty: the layering walk would be a no-op — every other test in this file passes vacuously")
	}
	if len(forbiddenPrefixes) == 0 {
		t.Fatal("forbiddenPrefixes is empty: TestNoCoreImportsAdapter would never fire — restore the adapter/SDK/grpc prefixes")
	}
	if len(coreImportRules) != len(corePackages) {
		t.Fatalf("coreImportRules covers %d tiers but corePackages has %d: the direction table must cover ALL seven core tiers",
			len(coreImportRules), len(corePackages))
	}
}

// TestNoCoreImportsAdapter walks each core package's transitive non-test imports
// and fails on any edge into an adapter (engine/adapter/ or anything outside
// engine/ — which covers the heavy adapters, the composition root, and
// contracts/gen), an LLM SDK, or grpc. This is the "dependencies point inward
// only" direction check across the WHOLE graph (depguard only sees one file's
// direct imports), plus the self-containment rule that keeps engine/ extractable
// as its own module.
func TestNoCoreImportsAdapter(t *testing.T) {
	for _, core := range corePackages {
		t.Run(shortName(core), func(t *testing.T) {
			walkTransitiveNonTestImports(t, core, func(from, imp string) {
				if violatesSelfContainment(imp) {
					t.Errorf("layering violation (CLAUDE.md \"dependencies point inward only\"): "+
						"core package %s transitively imports %s, which is outside the self-contained engine/ tree"+
						"\n\t...via edge %s -> %s",
						shortName(core), imp, shortName(from), shortName(imp))
				}
				for _, bad := range forbiddenPrefixes {
					if strings.HasPrefix(imp, bad) {
						t.Errorf("layering violation (CLAUDE.md \"dependencies point inward only\"): "+
							"core package %s transitively imports %s\n\t...via edge %s -> %s",
							shortName(core), imp, shortName(from), shortName(imp))
					}
				}
			})
		})
	}
}

func TestNoEngineAdapterImportsAgent(t *testing.T) {
	const adapterRoot = "../adapter"
	const agentImport = modulePrefix + "engine/agent"

	packages := 0
	err := filepath.WalkDir(adapterRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		bp, importErr := build.ImportDir(path, 0)
		if importErr != nil {
			var noGo *build.NoGoError
			if errors.As(importErr, &noGo) {
				return nil
			}
			return importErr
		}
		packages++
		for _, imp := range bp.Imports {
			if imp == agentImport {
				t.Errorf("reference adapter %s imports engine/agent in production code; engine/adapter/* may depend only on inward domain/port seams", filepath.ToSlash(path))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk reference adapters: %v", err)
	}
	if packages == 0 {
		t.Fatal("no engine/adapter packages found; the adapter-to-agent boundary check is vacuous")
	}
}

// TestCoreImportDirection is the SYMMETRIC direction check: for every one of the
// seven core tiers, its DIRECT non-test imports must be a subset of that tier's
// exact allow-set (table above). This closes the gap that TestNoCoreImportsAdapter
// + TestNoCyclesAmongCore leave open — a wrong-direction but non-cyclic,
// non-adapter intra-core edge (session→tool, tool→prompt backwards, …) is neither
// a forbidden prefix nor a cycle, so only an exact per-tier subset assertion
// catches it. (CLAUDE.md layering rule; matches the verified import map exactly.)
func TestCoreImportDirection(t *testing.T) {
	for _, rule := range coreImportRules {
		t.Run(shortName(rule.pkg), func(t *testing.T) {
			assertDirectImportsAllowed(t, rule.pkg, rule.allowedCore, rule.allowedExternal, rule.desc)
		})
	}
}

// TestViolatesSelfContainmentSynthetic pins the self-containment classifier on
// synthetic inputs, independent of the (clean) live graph — a broken classifier
// would otherwise let TestNoCoreImportsAdapter pass vacuously on that axis.
func TestViolatesSelfContainmentSynthetic(t *testing.T) {
	cases := []struct {
		imp  string
		want bool
	}{
		{modulePrefix + "engine/session", false},            // core: allowed
		{modulePrefix + "engine/adapter/memfs", false},      // engine subtree (the named adapter prefix handles production use)
		{modulePrefix + "contracts/gen/go/mecatl/v1", true}, // generated contracts: outside engine/
		{"github.com/bmatcuk/doublestar/v4", false},         // third-party: not module-internal
		{"golang.org/x/sync/errgroup", false},               // third-party: not module-internal
	}
	for _, c := range cases {
		if got := violatesSelfContainment(c.imp); got != c.want {
			t.Errorf("violatesSelfContainment(%q) = %v, want %v", c.imp, got, c.want)
		}
	}
	// Any module-internal path outside engine/ must trip, whatever it is named.
	if !violatesSelfContainment(modulePrefix + "x/y") {
		t.Error("a module-internal import outside engine/ must violate self-containment")
	}
}

// TestNoCyclesAmongCore builds the directed import graph RESTRICTED to the seven
// core packages and asserts it is acyclic. This is precisely the property the
// per-file depguard rules CANNOT express: depguard sees each file in isolation,
// so it can never observe that A -> B -> A closes a loop. A cycle here would mean
// two core tiers mutually depend, collapsing the layering.
func TestNoCyclesAmongCore(t *testing.T) {
	core := map[string]bool{}
	for _, p := range corePackages {
		core[p] = true
	}

	// Adjacency restricted to core->core edges, from direct non-test imports.
	adj := map[string][]string{}
	for _, p := range corePackages {
		for _, imp := range directNonTestImports(t, p) {
			if core[imp] {
				adj[p] = append(adj[p], imp)
			}
		}
	}

	if cycle := findCycle(corePackages, adj); cycle != nil {
		short := make([]string, len(cycle))
		for i, n := range cycle {
			short[i] = shortName(n)
		}
		t.Fatalf("import cycle among core packages (CLAUDE.md \"dependencies point inward only\" "+
			"forbids cycles; depguard cannot detect this — that is why this DAG test exists): %s",
			strings.Join(short, " -> "))
	}
}

// findCycle runs three-colour DFS over the directed graph (nodes, adj) and
// returns one cycle as an ordered node slice (with the closing node repeated at
// the end), or nil if the graph is acyclic. It is extracted from
// TestNoCyclesAmongCore so the live (acyclic) graph is no longer the ONLY thing
// exercising the detector — TestFindCycleSynthetic feeds it injected graphs so a
// broken DFS is caught even though the real graph stays acyclic.
func findCycle(nodes []string, adj map[string][]string) []string {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := map[string]int{}
	var stack []string

	var dfs func(node string) []string
	dfs = func(node string) []string {
		color[node] = grey
		stack = append(stack, node)
		for _, next := range adj[node] {
			switch color[next] {
			case grey:
				// Found a back-edge: extract the cycle from the stack.
				start := 0
				for i, n := range stack {
					if n == next {
						start = i
						break
					}
				}
				return append(append([]string{}, stack[start:]...), next)
			case white:
				if c := dfs(next); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[node] = black
		return nil
	}

	for _, n := range nodes {
		if color[n] == white {
			if c := dfs(n); c != nil {
				return c
			}
		}
	}
	return nil
}

// TestFindCycleSynthetic is the permanent regression guard for the cycle DFS,
// independent of the (acyclic) live graph: a broken detector would still let
// TestNoCyclesAmongCore pass. It feeds findCycle synthetic graphs — a 3-node
// cycle, a 2-node cycle, and an acyclic graph (no false positive).
func TestFindCycleSynthetic(t *testing.T) {
	t.Run("three-node cycle A->B->C->A", func(t *testing.T) {
		adj := map[string][]string{
			"A": {"B"},
			"B": {"C"},
			"C": {"A"},
		}
		if findCycle([]string{"A", "B", "C"}, adj) == nil {
			t.Fatal("findCycle missed a 3-node cycle A->B->C->A")
		}
	})
	t.Run("two-node cycle A<->B", func(t *testing.T) {
		adj := map[string][]string{
			"A": {"B"},
			"B": {"A"},
		}
		if findCycle([]string{"A", "B"}, adj) == nil {
			t.Fatal("findCycle missed a 2-node cycle A<->B")
		}
	})
	t.Run("acyclic DAG (no false positive)", func(t *testing.T) {
		// A diamond: A->B, A->C, B->D, C->D. Reachable twice but acyclic.
		adj := map[string][]string{
			"A": {"B", "C"},
			"B": {"D"},
			"C": {"D"},
			"D": {},
		}
		if c := findCycle([]string{"A", "B", "C", "D"}, adj); c != nil {
			t.Fatalf("findCycle reported a false cycle on an acyclic diamond: %v", c)
		}
	})
}

// assertDirectImportsAllowed fails for any direct non-test import of pkg that is
// neither stdlib (no dot before the first slash → not a domain-qualified path),
// nor in allowedCore, nor in allowedExternal.
func assertDirectImportsAllowed(t *testing.T, pkg string, allowedCore, allowedExternal map[string]bool, rule string) {
	t.Helper()
	for _, imp := range directNonTestImports(t, pkg) {
		if isStdlib(imp) {
			continue
		}
		if allowedCore[imp] || allowedExternal[imp] {
			continue
		}
		t.Errorf("layering violation (CLAUDE.md layering rule): %s imports disallowed package %s.\n\t%s",
			shortName(pkg), imp, rule)
	}
}

// isStdlib reports whether an import path is a standard-library package. Stdlib
// paths never contain a dot in their first path segment (no domain), whereas
// every module path does (github.com/..., golang.org/...).
func isStdlib(imp string) bool {
	slash := strings.IndexByte(imp, '/')
	first := imp
	if slash >= 0 {
		first = imp[:slash]
	}
	return !strings.Contains(first, ".")
}

// shortName trims the module prefix for readable failure messages.
func shortName(pkg string) string {
	return strings.TrimPrefix(pkg, modulePrefix)
}
