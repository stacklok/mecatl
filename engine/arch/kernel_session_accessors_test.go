package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestKernelSessionConstantTimeAccessors protects capture, selected-context and
// identity lookup independently of allocation counts: a nonallocating history scan
// is still O(n). These accessors own direct handle reads, not traversal or delegation.
// Keep this guard scoped to their owning files; it is not a call-graph analyzer.
func TestKernelSessionConstantTimeAccessors(t *testing.T) {
	for _, owner := range []struct {
		file, receiver string
		methods        []string
	}{
		{"session.go", "Session", []string{"Snapshot"}},
		{"record_view.go", "RecordView", []string{"Timestamp", "Commit", "ModelRequest", "ModelResponse", "InvocationFailure", "RunEnded"}},
		{"commit_metadata.go", "CommitMetadata", []string{"Ref", "Timestamp", "InstanceID"}},
		{"snapshot.go", "Snapshot", []string{"RootInstructions", "UserInstructions", "ToolCatalog", "ModelBinding", "UserInputByID"}},
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
// rejects loops and calls in its body, except len and integer conversions in the
// identity lookup and time.Unix(...).UTC() in timestamp getters. Fields,
// conditionals and handle literals remain unrestricted;
// helper calls require review because they could hide traversal. Other methods
// are intentionally outside scope.
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
			if call, ok := n.(*ast.CallExpr); ok && receiver == "Snapshot" && method.Name.Name == "UserInputByID" {
				if name, ok := call.Fun.(*ast.Ident); ok && (name.Name == "len" || name.Name == "int" || name.Name == "uint64") {
					return true
				}
			}
			if call, ok := n.(*ast.CallExpr); ok && (receiver == "RecordView" || receiver == "CommitMetadata") && method.Name.Name == "Timestamp" {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "time" && selector.Sel.Name == "Unix" {
						return true
					}
					if _, ok := selector.X.(*ast.CallExpr); ok && selector.Sel.Name == "UTC" {
						// Inspect the receiver call too: only time.Unix is permitted.
						return true
					}
				}
			}
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
		{"conditional handle", "Snapshot", "RootInstructions", `func (v Snapshot) RootInstructions() (RootInstructionsView, bool) {
			if v.instructions != nil { return RootInstructionsView{record: v.instructions, scope: v.historyID}, true }
			return RootInstructionsView{}, false
		}`, 0},
		{"unrelated traversal", "Session", "Snapshot", `func (s *Session) Snapshot() Snapshot { return s.state.view }
		func (s *Session) other() { scan(s.state.view.records) }`, 0},
		{"nonallocating range scan", "Session", "Snapshot", `func (s *Session) Snapshot() Snapshot {
			for _, r := range s.state.view.records { _ = r }
			return s.state.view
		}`, 1},
		{"nonallocating indexed scan", "Snapshot", "RootInstructions", `func (v Snapshot) RootInstructions() (RootInstructionsView, bool) {
			for i := 0; i < 100; i++ { _ = v.records[i] }
			return RootInstructionsView{record: v.instructions}, true
		}`, 1},
		{"delegated traversal", "Snapshot", "RootInstructions", `func (v Snapshot) RootInstructions() (RootInstructionsView, bool) { return v.scanRootInstructions() }`, 1},
		{"bounded identity lookup", "Snapshot", "UserInputByID", `func (v Snapshot) UserInputByID(id UserInputID) (UserInputView, bool) {
			if id.ordinal == 0 || id.ordinal > uint64(len(v.inputs)) { return UserInputView{}, false }
			return UserInputView{record: v.records[v.inputs[int(id.ordinal-1)]]}, true
		}`, 0},
		{"identity scan", "Snapshot", "UserInputByID", `func (v Snapshot) UserInputByID(id UserInputID) (UserInputView, bool) {
			for _, r := range v.records { _ = r }
			return UserInputView{}, false
		}`, 1},
		{"delegated identity lookup", "Snapshot", "UserInputByID", `func (v Snapshot) UserInputByID(id UserInputID) (UserInputView, bool) { return v.findInput(id) }`, 1},
		{"identity map allocation", "Snapshot", "UserInputByID", `func (v Snapshot) UserInputByID(id UserInputID) (UserInputView, bool) {
			_ = make(map[UserInputID]int)
			return UserInputView{}, false
		}`, 1},
		{"record timestamp conversion", "RecordView", "Timestamp", `func (v RecordView) Timestamp() (time.Time, bool) {
			if v.record == nil { return time.Time{}, false }
			return time.Unix(0, v.record.value.Timestamp).UTC(), true
		}`, 0},
		{"commit timestamp conversion", "CommitMetadata", "Timestamp", `func (v CommitMetadata) Timestamp() time.Time {
			if v == (CommitMetadata{}) { return time.Time{} }
			return time.Unix(0, v.timestamp).UTC()
		}`, 0},
		{"timestamp scan", "RecordView", "Timestamp", `func (v RecordView) Timestamp() (time.Time, bool) {
			for _, r := range v.records { _ = r }
			return time.Unix(0, v.record.value.Timestamp).UTC(), true
		}`, 1},
		{"delegated timestamp", "CommitMetadata", "Timestamp", `func (v CommitMetadata) Timestamp() time.Time { return v.scanTimestamp().UTC() }`, 1},
		{"timestamp conversion argument scan", "RecordView", "Timestamp", `func (v RecordView) Timestamp() (time.Time, bool) { return time.Unix(0, v.scanTimestamp()).UTC(), true }`, 1},
		{"unrelated time call", "CommitMetadata", "Timestamp", `func (v CommitMetadata) Timestamp() time.Time { return time.Now().UTC() }`, 1},
		{"conversion outside timestamp", "CommitMetadata", "Ref", `func (v CommitMetadata) Ref() time.Time { return time.Unix(0, v.timestamp).UTC() }`, 2},
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
