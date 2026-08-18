package ui

// approval_arch_test.go is the structural gate for the Phase-1 approval
// consolidation (issue #555, decision 5 "loud"): it fails CI if any
// approval-vocabulary declaration is added OUTSIDE the four approval_*.go
// files, or if Model acquires a second approval-owned field, so the
// one-file-owns-it invariant the milestone targets stays machine-enforced
// rather than convention-enforced. It is additive — placement-only; it never
// touches rendered output (the frame goldens own that).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// approvalFileHomes is the set of files a declaration carrying the approval
// vocabulary may live in. renderBody's one-line shim (`m.renderApprovalBody()`
// guard) lives in view.go by delegation — it contains NO approval-vocabulary
// identifier, only the single call the delegation pattern names.
var approvalFileHomes = map[string]bool{
	"approval_state.go":   true,
	"approval_surface.go": true,
	"approval_render.go":  true,
	"approval_regions.go": true,
}

// approvalFileSet is the four homes the gate counts — kept in one place so a
// fifth file added later forces an explicit decision here, not a silent drift.
const approvalFileCount = 4

// approvalToken matches the identifier vocabulary the gate guards — the
// MODAL's state and behavior. It is deliberately NARROW (anchored, modal-named
// stems) so shared render vocabulary (clickRegionsForAsk/clickAskVerdict in
// clickgeom.go) and unrelated domains (team.go's task*/team*, the /debug-ask
// builtin) are NOT false-positives. Widening it to catch a new approval-adjacent
// symbol is the visible decision the loud gate is for.
var approvalToken = regexp.MustCompile(`^(?:pendingAsk|approvalState|approvalDeps|approvalAction\w*|approvalAdvance|askQueueIndex|focusVerdict|isChildAsk|isPlanAsk|isDiffCapableAskTool|bashAskArgs|applyPermissionAsk|applyPermissionRetract|onApprovalKey|dispatchClick|advance|markAskResolved|askKnown|resolveAsk|approvalWheel|approvalExpandToggle|openPlanReviewView|clearPlanReview|planAskFingerprint|planReviewLayout|renderPlanReviewView|planButtonsLine|planScrollHint|openAskArgsView|clearAskArgsView|argsAskFingerprint|argsReviewLayout|renderAskArgsView|argsScrollHint|askArgsContent|askArgsTiersDiffer|wrapAskArgs\w*|askArgsMiniViewport|askArgsCardContentWidth|askArgsMiniScrollRange|onAskArgs\w*|onPlanScrollKey|askArgsWheelOverCard|permissionModalBody\w*|renderPermissionModal\w*|approvalButtons\w*|permissionButtonsLine|renderApprovalBody|renderApprovalButtons|approvalButton\w*|buttonRect|askButtonRects|approvalClickRegions|approvalCardRect|clickAt|askButtonAt|buttonGap|planBodyFromArgs|planApprovedProceedText|approvalNotice|planReviewFooterHeight|argsReviewFooterHeight)$`)

// TestApprovalSymbolsLiveInApprovalFiles walks every non-test ui package file
// and asserts each declaration whose name (decl name or a method's receiver
// type) carries the approval vocabulary lives in one of the four homes.
func TestApprovalSymbolsLiveInApprovalFiles(t *testing.T) {
	if len(approvalFileHomes) != approvalFileCount {
		t.Fatalf("approvalFileHomes has %d entries, want approvalFileCount=%d (a fifth approval file must be an explicit decision here)",
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
					t.Errorf("approval-vocabulary declaration %q in non-approval file %s (want one of approval_state/surface/render/regions.go)", n, file)
				}
			}
		}
	}
}

// TestModelHasExactlyOneApprovalField asserts Model holds approval state in
// exactly ONE field of type approvalState — the state-consolidation half of
// the milestone. A second approval-owned Model field is the drift the struct
// was created to kill.
func TestModelHasExactlyOneApprovalField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	// Model is a struct; count fields whose TYPE is approvalState by name
	// (reflect can't name the unexported type portably, so compare the reflect
	// Name()).
	var count int
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Type.Name() == "approvalState" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Model has %d approvalState fields, want exactly 1", count)
	}
}
