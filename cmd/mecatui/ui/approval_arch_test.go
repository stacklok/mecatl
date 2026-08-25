package ui

// approval_arch_test.go is the structural gate for the approval surface migration
// (issue #555): it fails CI if approval-vocabulary declarations leave the two
// approval files, if Model acquires a second approval-owned field, or if the
// surface regains a broad renderer.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// approvalFileHomes is the two production homes for approval behavior: Model-side
// integration remains in approval.go; approval-local state, rendering, layout,
// and hit behavior live in approval_surface.go. Generic geometry is deliberately
// excluded from this approval-specific structural gate.
var approvalFileHomes = map[string]bool{
	"approval.go":         true,
	"approval_surface.go": true,
}

// approvalFileCount makes a third production home an explicit decision.
const approvalFileCount = 2

// approvalToken matches the identifier vocabulary the gate guards — the
// modal's state and behavior. It deliberately excludes shared generic geometry
// (for example cellRect in geom.go and ClickableRegion in hit_regions.go) and unrelated
// domains. Widening it to catch a new approval-adjacent symbol is the visible
// decision the loud gate is for.
var approvalToken = regexp.MustCompile(`^(?:approvalRender|newApprovalRender|pendingAsk|approvalResolvedIntent|approvalRetractedIntent|setExpandToolsIntent|approvalQueueOutcome|approvalAdvance|approvalFooterProjection|askQueueIndex|visibleApprovalVerdicts|isChildAsk|isPlanAsk|isDiffCapableAskTool|bashAskArgs|applyPermissionAsk|applyPermissionRetract|onApprovalKey|dispatchClick|advance|markAskResolved|askKnown|resolveAsk|approvalExpandToggle|openPlan|clearPlanReview|planAskFingerprint|planReviewLayout|planLayout|planButtonsLine|planScrollHint|openArgs|clearAskArgsView|argsAskFingerprint|argsReviewLayout|argsLayout|argsScrollHint|askArgsContent|askArgsTiersDiffer|wrapAskArgs\w*|wrapApprovalReason|askArgsMiniViewport|askArgsCardContentWidth|askArgsMiniScrollRange|onAskArgs\w*|onPlanScrollKey|permissionModalBody\w*|renderPermissionModal\w*|approvalButtons\w*|permissionButtonsLine|renderApprovalBody|renderApprovalButtons|approvalButton\w*|askButtonRects|buttonGap|planBodyFromArgs|planApprovedProceedText|approvalNotice|planReviewFooterHeight|argsReviewFooterHeight)$`)

// TestApprovalSymbolsLiveInApprovalFiles walks every non-test ui package file
// and asserts each declaration whose name (decl name or a method's receiver
// type) carries the approval vocabulary lives in one of the two homes.
func TestApprovalSymbolsLiveInApprovalFiles(t *testing.T) {
	assertApprovalDeclarationsLiveInApprovalFiles(t)
}

// TestApprovalSurfaceHasNoModelIntegration ensures that Model-side construction,
// lifecycle, effects, and phase policy stay in approval.go. The surface receives
// surfaceDeps plus its own inputs, so its state and rendering remain independent of Model.
func TestApprovalSurfaceHasNoModelIntegration(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "approval_surface.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		// viewport.Model is approval-local view state, not the UI Model. Skip its
		// selector before visiting Model so the unqualified-Model guard remains
		// precise.
		if selector, ok := n.(*ast.SelectorExpr); ok && selector.Sel.Name == "Model" {
			return false
		}
		ident, ok := n.(*ast.Ident)
		if ok && ident.Name == "Model" {
			t.Error("approval_surface.go references Model; Model-side approval integration belongs in approval.go")
		}
		return true
	})
}

func TestApprovalIntentUsesSealedSurfaceProtocol(t *testing.T) {
	for _, file := range []string{"approval.go", "approval_surface.go", "update.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "approvalSurfaceIntent") {
			t.Fatalf("%s retains the approval-only intent marker", file)
		}
	}
	body, err := os.ReadFile("approval.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "applyApprovalSurfaceIntent(intent surfaceIntent)") {
		t.Fatal("approval intent handler must accept the sealed surfaceIntent protocol")
	}
}

func assertApprovalDeclarationsLiveInApprovalFiles(t *testing.T) {
	if len(approvalFileHomes) != approvalFileCount {
		t.Fatalf("approvalFileHomes has %d entries, want approvalFileCount=%d (a sixth approval file must be an explicit decision here)",
			len(approvalFileHomes), approvalFileCount)
	}
	fset := token.NewFileSet()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range matches {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			var names []string
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names = append(names, s.Name.Name)
					case *ast.ValueSpec:
						for _, id := range s.Names {
							names = append(names, id.Name)
						}
					}
				}
			case *ast.FuncDecl:
				// A method is flagged when the METHOD name itself is approval-
				// vocabulary. The receiver type name (e.g. Model) is NOT separately
				// asserted: an approval method on a non-approval receiver drags the
				// vocabulary back onto Model, but the method-name match already
				// catches that (renderApprovalBody is the one delegation exception,
				// and its name is caught by the same rule — see below).
				names = append(names, d.Name.Name)
			}
			for _, n := range names {
				if n == "" || !approvalToken.MatchString(n) {
					continue
				}
				// The delegation shim exception: view.go's renderApprovalBody is
				// the single-arm dispatch renderBody owns; it carries no approval
				// state. The onApprovalKey Model shim in approval_surface.go is
				// the key delegation (named identically — allowed home).
				if file == "view.go" && n == "renderApprovalBody" {
					continue
				}
				if !approvalFileHomes[file] {
					t.Errorf("approval-vocabulary declaration %q in non-approval file %s (want approval.go or approval_surface.go)", n, file)
				}
			}
		}
	}
}

// TestModelHasNoApprovalStateField keeps approval ephemeral: its state belongs
// only to a dynamically-open approvalSurface, never to Model.
func TestModelHasNoApprovalStateField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Type.Name() == "approvalState" || f.Name == "approval" {
			t.Errorf("Model owns approval state through %q; want dynamic surface only", f.Name)
		}
	}
}

// TestApprovalSurfaceUsesNarrowRenderPrimitive prevents a broad *renderer from
// leaking back into approval state. The two callbacks are the only shared render
// behavior approval needs: Edit/Write diffs and plan Markdown.
func TestApprovalSurfaceUsesNarrowRenderPrimitive(t *testing.T) {
	surface := reflect.TypeOf(approvalSurface{})
	field, ok := surface.FieldByName("render")
	if !ok {
		t.Fatal("approvalSurface has no approval render primitive")
	}
	if got := field.Type.Name(); got != "approvalRender" {
		t.Fatalf("approvalSurface render field = %q, want approvalRender", got)
	}
	primitive := reflect.TypeOf(approvalRender{})
	if primitive.NumField() != 2 {
		t.Fatalf("approvalRender has %d fields, want only diff and markdown", primitive.NumField())
	}
	for _, name := range []string{"diff", "markdown"} {
		f, ok := primitive.FieldByName(name)
		if !ok || f.Type.Kind() != reflect.Func {
			t.Errorf("approvalRender.%s = %v, want function", name, f.Type)
		}
	}
}
