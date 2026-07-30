package ui

// A source-level sweep asserting a banned substring never appears in any ui
// renderer string literal. Behavioural tests can only cover the surfaces they
// know about; this sweep fails loud the moment ANY ui source file re-introduces
// the banned phrase (a new renderer, a copy-pasted note, …). Test files are
// exempt — a test may need to NAME the banned string (as this one does).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func assertNoBannedRenderString(t *testing.T, banned string) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob ui sources: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if fileHasStringLiteral(t, f, banned) {
			t.Errorf("banned string %q re-introduced in %s", banned, f)
		}
	}
}

// fileHasStringLiteral reports whether the named file contains a string literal
// (interpreted or raw) whose VALUE contains banned. It parses the AST rather than
// grepping so a doc comment mentioning the phrase is not a false positive — the
// ban is on what the TUI RENDERS, not on what a comment explains.
func fileHasStringLiteral(t *testing.T, path, banned string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		if strings.Contains(s, banned) {
			found = true
			return false
		}
		return true
	})
	return found
}
