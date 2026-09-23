package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRunBoundsInventoryIsComplete is the issue-#90 structural drift guard for the
// composition tier: the run-bound default consts declared in internal/app must
// match the inventory below EXACTLY, and the companion AST scan
// (TestRunBoundsInventoryHasNoUnlistedConsts) fails when a NEW default* run-bound
// const appears that is not inventoried. This checks the code inventory, not a
// duplicate Markdown table. Update the owning API/config reference when relevant.
//
// The engine/agent tier has its own guard (engine/agent/runbounds_drift_test.go);
// the split is required by the layering rule (engine/ cannot import internal/ or
// os, so it cannot AST-scan). internal/app can, so the un-listed-const detection
// lives ONLY here.
//
// NOTE: defaultCompactionRatio is declared in BOTH internal/app/build.go AND
// engine/agent/loop.go (same value, 0.8). Both are pinned (here and in the engine
// guard) so the two cannot drift apart; consolidating the declaration is a
// deferred non-goal of issue #90.
//
// NOTE: the three deployment caps (deploymentMaxTurns / deploymentMaxToolCalls /
// deploymentMaxConsecutiveFailures) use the `deployment` prefix, NOT `default`, so
// they are deliberately INVISIBLE to the AST scan in
// TestRunBoundsInventoryHasNoUnlistedConsts (which keys on default*/Default*). They
// are pinned by VALUE here only; the AST scan covers the remaining default* consts.
func TestRunBoundsInventoryIsComplete(t *testing.T) {
	t.Parallel()

	type bound struct {
		name string
		val  any
	}
	want := []bound{
		{"deploymentMaxTurns", 2000},
		{"deploymentMaxToolCalls", 8000},
		{"deploymentMaxConsecutiveFailures", 5},
		{"defaultCompactionRatio", 0.8}, // ALSO in engine/agent/loop.go — keep in sync
		{"defaultCompactionTargetRatio", 0.6},
		{"defaultContextWindowTokens", 128_000},
	}

	for _, b := range want {
		var got any
		switch b.name {
		case "deploymentMaxTurns":
			got = deploymentMaxTurns
		case "deploymentMaxToolCalls":
			got = deploymentMaxToolCalls
		case "deploymentMaxConsecutiveFailures":
			got = deploymentMaxConsecutiveFailures
		case "defaultCompactionRatio":
			got = defaultCompactionRatio
		case "defaultCompactionTargetRatio":
			got = defaultCompactionTargetRatio
		case "defaultContextWindowTokens":
			got = defaultContextWindowTokens
		default:
			t.Errorf("inventory references unknown const %q — update the switch", b.name)
			continue
		}
		if got != b.val {
			t.Errorf("run-bound const %q = %v, want %v — update the inventory and any affected API/config reference",
				b.name, got, b.val)
		}
	}
}

// TestRunBoundsInventoryHasNoUnlistedConsts is the OTHER half of the guard: it
// AST-scans internal/app for `const default…` / `const Default…` declarations and
// fails if any run-bound-looking default const is NOT in the inventory above. This
// catches an unlisted declaration that a pure value-pinning test cannot detect.
// The engine/agent tier references known identifiers but does not AST-scan for
// additional declarations.
//
// A const is considered "run-bound-looking" if its name begins with default/Default
// AND it is declared in one of the run-bounds-bearing files. To avoid false
// positives on unrelated default* consts, the allowed declaration files are listed
// explicitly; a const in a NEW file must be added to runBoundsFiles or excluded via
// the ignore set.
func TestRunBoundsInventoryHasNoUnlistedConsts(t *testing.T) {
	t.Parallel()

	// Files in internal/app that declare run-bound defaults. A new file declaring a
	// run-bound default* const must be added here.
	runBoundsFiles := []string{
		"build.go",
		"guardrails.go",
	}

	// The closed set of run-bound default* const names this package owns, i.e. the
	// consts the AST scan below can SEE (it keys on the default*/Default* prefix).
	// The three deployment caps (deploymentMaxTurns / deploymentMaxToolCalls /
	// deploymentMaxConsecutiveFailures) use the `deployment` prefix and are therefore
	// NOT detected by the scan; they are pinned by value in
	// TestRunBoundsInventoryIsComplete only. Keep this set in sync with the default*
	// arm of the switch in that test.
	listed := map[string]bool{
		"defaultCompactionRatio":       true,
		"defaultCompactionTargetRatio": true,
		"defaultContextWindowTokens":   true,
	}

	dir := "."                     // internal/app
	found := map[string][]string{} // const name → files declaring it
	for _, f := range runBoundsFiles {
		path := filepath.Join(dir, f)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, data, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					nm := name.Name
					if !strings.HasPrefix(nm, "default") && !strings.HasPrefix(nm, "Default") {
						continue
					}
					found[nm] = append(found[nm], f)
				}
			}
			return true
		})
	}

	var unlisted []string
	for name := range found {
		if !listed[name] {
			unlisted = append(unlisted, name)
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("internal/app declares run-bound default* const(s) not in the inventory: %v — add a row to TestRunBoundsInventoryIsComplete and update the owning API/config reference, or extend runBoundsFiles if a new file is intentional", unlisted)
	}

	// Also assert every listed const was actually found declared (catches a stale
	// inventory entry after a const is removed).
	for name := range listed {
		if _, ok := found[name]; !ok {
			t.Errorf("inventory lists %q but no default* const declaration was found in %v — remove the stale inventory row and update any affected API/config reference", name, runBoundsFiles)
		}
	}
}
