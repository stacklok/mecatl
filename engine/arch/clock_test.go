package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoWallClockInEngineCore asserts that no non-test file in the engine core
// reads the wall clock directly via time.Now(). Every wall-clock read in the core
// must flow through the injected port.Clock (the Engine.now() helper for the agent
// loop), so an embedding host can drive the engine deterministically in its tests
// (issue #116 — "the engine is fully clock-injectable").
//
// The reference adapters under engine/adapter/ are EXEMPT: engine/adapter/wallclock
// IS the production port.Clock (its whole job is to call time.Now()), and the
// engine/adapter/*conformance test-support suites assert real-time behaviour. They
// are not part of the clock-injectable core and ship a real clock by definition.
//
// This is the source-text sibling of the import-graph guards in this package: it
// is AST-based (so a time.Now token inside a comment or string never false-trips),
// and it walks the on-disk engine/ tree, mirroring the go/build walkers elsewhere
// here. It runs with cwd == this package dir, so ".." is the engine/ root.
// wallClockReaders are the time-package functions that read the process wall clock
// directly. All are forbidden in the engine core: the core must read wall time only
// through the injected port.Clock (whose sole method is Now()), so an embedding host
// can drive it deterministically. time.Since(x)/time.Until(x) are time.Now()-Sub(x)
// and -Sub respectively — same wall-clock read, so they are covered too even though
// the core has none today (the guard is forward-looking, issue #116).
var wallClockReaders = map[string]bool{"Now": true, "Since": true, "Until": true}

func TestNoWallClockInEngineCore(t *testing.T) {
	const engineRoot = ".." // engine/arch -> engine/

	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(engineRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		slash := filepath.ToSlash(path)
		// Skip the reference/test-support adapters (wallclock IS the real clock;
		// the conformance suites assert real-time behaviour) and all test files.
		if strings.Contains(slash, "/adapter/") || !strings.HasSuffix(slash, ".go") || strings.HasSuffix(slash, "_test.go") {
			return nil
		}

		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !wallClockReaders[sel.Sel.Name] {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "time" {
				return true
			}
			pos := fset.Position(sel.Pos())
			violations = append(violations, pos.String()+" ("+pkg.Name+"."+sel.Sel.Name+")")
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", engineRoot, err)
	}

	if len(violations) > 0 {
		t.Errorf("direct wall-clock read in the engine core (issue #116 — time.Now/Since/Until must flow through the "+
			"injected port.Clock / Engine.now(), so the engine stays clock-injectable):\n\t%s",
			strings.Join(violations, "\n\t"))
	}
}

// TestNoWallClockGuardIsLive is the self-check that the AST matcher above actually
// fires — without it, a refactor that broke the SelectorExpr match (e.g. renamed
// the matched name) would let TestNoWallClockInEngineCore pass vacuously. It feeds
// the same matcher a synthetic source containing time.Now() and asserts a hit.
func TestNoWallClockGuardIsLive(t *testing.T) {
	const src = `package p
import "time"
func f() { _ = time.Now() }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic: %v", err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Now" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("the time.Now() AST matcher failed to fire on synthetic source: TestNoWallClockInEngineCore would pass vacuously")
	}
}
