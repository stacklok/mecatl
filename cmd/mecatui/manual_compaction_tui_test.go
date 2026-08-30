package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

type compositionCompactor struct{}

func (compositionCompactor) CompactSession(context.Context, string) (bool, error) { return true, nil }

// TestMainWiresManualCompaction pins the composition-root half of /compact
// discoverability. ui.TestBuiltinRegistryCapabilityGating pins the other half:
// capability true plus this collaborator exposes the command.
func TestMainWiresManualCompaction(t *testing.T) {
	var deps ui.Deps
	compactor := compositionCompactor{}
	wireManualCompaction(&deps, compactor)
	if deps.Compactor == nil {
		t.Fatal("main left ui.Deps.Compactor nil; /compact cannot be exposed")
	}
	if compacted, err := deps.Compactor.CompactSession(context.Background(), "session"); err != nil || !compacted {
		t.Fatalf("wired compactor result=(%v, %v)", compacted, err)
	}
}

// TestRunCallsWireManualCompaction prevents the composition root from drifting
// away from the /compact collaborator wiring tested above.
func TestRunCallsWireManualCompaction(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var wired bool
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			return true
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			callee, ok := call.Fun.(*ast.Ident)
			if !ok || callee.Name != "wireManualCompaction" {
				return true
			}
			deps, ok := call.Args[0].(*ast.UnaryExpr)
			if !ok || deps.Op != token.AND {
				return true
			}
			depsName, ok := deps.X.(*ast.Ident)
			clientName, clientOK := call.Args[1].(*ast.Ident)
			wired = ok && clientOK && depsName.Name == "deps" && clientName.Name == "cl"
			return !wired
		})
		return !wired
	})
	if !wired {
		t.Fatal("run must invoke wireManualCompaction(&deps, cl) so /compact is available")
	}
}
