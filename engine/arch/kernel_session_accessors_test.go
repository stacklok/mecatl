package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestKernelSessionConstantTimeAccessors protects capture and selected-context
// lookup independently of allocation counts: a nonallocating history scan is still
// O(n). These four accessors own direct handle reads, not traversal or delegation.
// Keep this guard scoped to their owning files; it is not a call-graph analyzer.
func TestKernelSessionConstantTimeAccessors(t *testing.T) {
	for _, owner := range []struct {
		file, receiver string
		methods        []string
	}{
		{"session.go", "Session", []string{"Snapshot"}},
		{"snapshot.go", "Snapshot", []string{"Instructions", "ToolCatalog", "ModelBinding"}},
	} {
		t.Run(owner.file, func(t *testing.T) {
			path := filepath.Join("..", "kernel", "session", owner.file)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, violation := range kernelAccessorViolations(f, owner.receiver, owner.methods...) {
				t.Error(violation)
			}
		})
	}
}

// kernelAccessorViolations requires each named concrete method exactly once and
// rejects loops and all calls in its body. Fields, conditionals and handle literals
// remain unrestricted; even a helper call must be reviewed as a boundary change
// because it could hide traversal. Other methods are intentionally outside scope.
func kernelAccessorViolations(f *ast.File, receiver string, methods ...string) []string {
	counts := make(map[string]int, len(methods))
	for _, method := range methods {
		counts[method] = 0
	}
	var violations []string
	for _, decl := range f.Decls {
		method, ok := decl.(*ast.FuncDecl)
		if !ok || method.Recv == nil || len(method.Recv.List) != 1 {
			continue
		}
		typ := method.Recv.List[0].Type
		if pointer, ok := typ.(*ast.StarExpr); ok {
			typ = pointer.X
		}
		name, ok := typ.(*ast.Ident)
		if !ok || name.Name != receiver {
			continue
		}
		if _, wanted := counts[method.Name.Name]; !wanted {
			continue
		}
		counts[method.Name.Name]++
		if method.Body == nil {
			violations = append(violations, receiver+"."+method.Name.Name+": missing body")
			continue
		}
		ast.Inspect(method.Body, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.ForStmt, *ast.RangeStmt, *ast.CallExpr:
				violations = append(violations, fmt.Sprintf("%s.%s: %T violates direct constant-time handle access", receiver, method.Name.Name, n))
			}
			return true
		})
	}
	for _, method := range methods {
		if counts[method] != 1 {
			violations = append(violations, fmt.Sprintf("%s.%s: found %d methods, want exactly one", receiver, method, counts[method]))
		}
	}
	return violations
}

func TestKernelSessionConstantTimeAccessorGuardControls(t *testing.T) {
	for _, tc := range []struct {
		name, receiver, method, source string
		want                           int
	}{
		{"direct capture", "Session", "Snapshot", `func (s *Session) Snapshot() Snapshot { return s.state.view }`, 0},
		{"conditional handle", "Snapshot", "Instructions", `func (v Snapshot) Instructions() (InstructionsView, bool) {
			if v.instructions != nil { return InstructionsView{record: v.instructions, scope: v.historyID}, true }
			return InstructionsView{}, false
		}`, 0},
		{"unrelated traversal", "Session", "Snapshot", `func (s *Session) Snapshot() Snapshot { return s.state.view }
		func (s *Session) other() { scan(s.state.view.records) }`, 0},
		{"nonallocating range scan", "Session", "Snapshot", `func (s *Session) Snapshot() Snapshot {
			for _, r := range s.state.view.records { _ = r }
			return s.state.view
		}`, 1},
		{"nonallocating indexed scan", "Snapshot", "Instructions", `func (v Snapshot) Instructions() (InstructionsView, bool) {
			for i := 0; i < 100; i++ { _ = v.records[i] }
			return InstructionsView{record: v.instructions}, true
		}`, 1},
		{"delegated traversal", "Snapshot", "Instructions", `func (v Snapshot) Instructions() (InstructionsView, bool) { return v.scanInstructions() }`, 1},
		{"missing method", "Session", "Snapshot", `func (s *Session) other() {}`, 1},
		{"wrong owner", "Session", "Snapshot", `func (s *Other) Snapshot() Snapshot { return s.view }`, 1},
		{"duplicate method", "Session", "Snapshot", `func (s *Session) Snapshot() Snapshot { return s.state.view }
		func (s *Session) Snapshot() Snapshot { return s.state.view }`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "control.go", "package session\n"+tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := kernelAccessorViolations(f, tc.receiver, tc.method); len(got) != tc.want {
				t.Fatalf("violations = %v; want %d", got, tc.want)
			}
		})
	}
}
