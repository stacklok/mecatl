package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// hitDispatchSurface is an offline surface fixture. Its Render method deliberately
// mints its local behaviour from the shared Model-owned allocator so the tests
// exercise the same reference seam that production surfaces use.
type hitDispatchSurface struct {
	deps     surfaceDeps
	hits     map[HitID]string
	received []surfaceHitMsg
	hitMsgs  int
}

func (s *hitDispatchSurface) Render(_, _ int) (string, []ClickableRegion) {
	id := s.deps.hits.allocate()
	s.hits = map[HitID]string{id: "activate"}
	return "hit target", []ClickableRegion{{rect: cellRect{x0: 2, x1: 5, y0: 1, y1: 2}, hit: id}}
}

func (*hitDispatchSurface) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	return nil, true, false
}

func (s *hitDispatchSurface) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	hit, ok := msg.(surfaceHitMsg)
	if !ok {
		return nil, false, false
	}
	s.hitMsgs++
	if _, ok := s.hits[hit.ID]; !ok {
		return nil, true, false
	}
	s.received = append(s.received, hit)
	return nil, true, false
}

func (*hitDispatchSurface) HandleWheel(tea.MouseWheelMsg) (tea.Cmd, bool) { return nil, true }
func (*hitDispatchSurface) Close()                                        {}

func hitDispatchModel(t *testing.T) (Model, *hitDispatchSurface) {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.deps.NoAltScreen = false
	m.width, m.height = 80, 24
	m.vp.SetHeight(18)
	s := &hitDispatchSurface{deps: (&m).surfaceDeps()}
	m.modal = s
	return m, s
}

func TestSurfaceApprovalMigration_Scenario3_RealViewPersistsFreshFrameHitCaches(t *testing.T) {
	m, surface := hitDispatchModel(t)

	_ = m.View()
	if len(m.hits.frame) != 1 {
		t.Fatalf("first View frame has %d hit regions, want 1", len(m.hits.frame))
	}
	first := m.hits.frame[0]
	if _, ok := surface.hits[first.id]; !ok {
		t.Fatal("first View frame ID is not in the surface-local behaviour cache")
	}

	_ = m.View()
	if len(m.hits.frame) != 1 {
		t.Fatalf("second View frame has %d hit regions, want 1", len(m.hits.frame))
	}
	second := m.hits.frame[0]
	if second.id == first.id {
		t.Fatalf("second View reused stale hit ID %d", second.id)
	}
	if _, ok := surface.hits[first.id]; ok {
		t.Fatalf("surface retained stale hit ID %d after a fresh View", first.id)
	}
	if _, ok := surface.hits[second.id]; !ok {
		t.Fatal("second View frame ID is not in the fresh surface-local behaviour cache")
	}
}

func TestSurfaceApprovalMigration_Scenario3_HitCoordinatesAndMessageRouting(t *testing.T) {
	m, surface := hitDispatchModel(t)
	_ = m.View()
	region := m.hits.frame[0]

	globalX, globalY := m.metrics.localToGlobal(region.rect.x0+1, region.rect.y0)
	mm, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: globalX, Y: globalY})
	m = mm.(Model)
	if len(surface.received) != 1 {
		t.Fatalf("surface received %d hit messages, want 1", len(surface.received))
	}
	hit := surface.received[0]
	if hit.ID != region.id {
		t.Errorf("routed hit ID = %d, want %d", hit.ID, region.id)
	}
	if hit.X != 1 || hit.Y != 0 {
		t.Errorf("surface-local hit coordinates = (%d,%d), want (1,0)", hit.X, hit.Y)
	}
	if m.modal != surface {
		t.Fatal("generic hit routing changed the active surface")
	}
}

func TestSurfaceApprovalMigration_Scenario3_StaleHitsFailClosed(t *testing.T) {
	m, surface := hitDispatchModel(t)
	_ = m.View()
	stale := m.hits.frame[0].id
	_ = m.View() // supersedes stale and surface-local IDs

	beforeSelection := m.sel
	beforeReceived := len(surface.received)
	beforeHitMsgs := surface.hitMsgs
	for _, id := range []HitID{stale, HitID(999999)} {
		mm, _ := m.Update(surfaceHitMsg{ID: id, X: 0, Y: 0})
		m = mm.(Model)
	}
	if surface.hitMsgs != beforeHitMsgs {
		t.Fatalf("stale/unknown hits reached surface routing: got %d messages, want %d", surface.hitMsgs, beforeHitMsgs)
	}
	if len(surface.received) != beforeReceived {
		t.Fatalf("stale/unknown hits reached surface: got %d messages, want %d", len(surface.received), beforeReceived)
	}
	if m.sel != beforeSelection {
		t.Fatal("stale/unknown hits changed underlying transcript selection")
	}
	if m.modal != surface {
		t.Fatal("stale/unknown hits changed focus or closed the surface")
	}

	m.modal = nil
	mm, _ := m.Update(surfaceHitMsg{ID: m.hits.frame[0].id, X: 0, Y: 0})
	m = mm.(Model)
	if m.modal != nil || len(surface.received) != beforeReceived || m.sel != beforeSelection {
		t.Fatal("closed-surface hit did not fail closed")
	}
}

func TestApprovalSurfaceStructuralBoundary(t *testing.T) {
	assertApprovalDeclarationsLiveInApprovalFiles(t)

	// This companion pin covers the shared seams: geometry owns primitives and
	// parent-owned metrics, while hit regions retain only local opaque routing state.
	if approvalFileHomes["geom.go"] || approvalFileHomes["hit_regions.go"] {
		t.Fatal("generic geometry and hit regions must not be approval-owned production homes")
	}
	region := reflect.TypeOf(ClickableRegion{})
	if region.NumField() != 2 {
		t.Fatalf("ClickableRegion has %d fields, want rect and opaque hit ID", region.NumField())
	}
	hit, ok := region.FieldByName("hit")
	if !ok || hit.Type != reflect.TypeOf(HitID(0)) {
		t.Fatal("ClickableRegion must carry an opaque HitID")
	}
	if _, ok := region.FieldByName("action"); ok {
		t.Fatal("ClickableRegion must not carry approval behaviour")
	}

	files := surfaceDeclarationFiles(t)
	for _, name := range []string{"HitID", "ClickableRegion", "surfaceHitMsg", "renderedHitRegion", "hitIDAllocator", "hitRegions"} {
		if got := files[name]; got != "hit_regions.go" {
			t.Errorf("shared hit declaration %s lives in %q, want hit_regions.go", name, got)
		}
	}
	for _, name := range []string{"cellRect", "renderedSurfaceMetrics"} {
		if got := files[name]; got != "geom.go" {
			t.Errorf("shared geometry declaration %s lives in %q, want geom.go", name, got)
		}
	}
	if got := files["approvalSurface"]; got != "approval_surface.go" {
		t.Errorf("approval surface declaration lives in %q, want approval_surface.go", got)
	}

	model := reflect.TypeOf(Model{})
	for i := 0; i < model.NumField(); i++ {
		field := model.Field(i)
		if field.Name == "approval" || field.Type.Name() == "approvalState" {
			t.Errorf("Model owns approval state through %q; want dynamic surface only", field.Name)
		}
	}
	field, ok := model.FieldByName("hits")
	if !ok || field.Type != reflect.TypeOf((*hitRegions)(nil)) {
		t.Fatal("Model must retain only the generic shared hit-region cache")
	}
	field, ok = model.FieldByName("metrics")
	if !ok || field.Type != reflect.TypeOf((*renderedSurfaceMetrics)(nil)) {
		t.Fatal("Model must retain parent-owned rendered surface metrics")
	}
	for i := 0; i < reflect.TypeOf(renderedHitRegion{}).NumField(); i++ {
		if reflect.TypeOf(renderedHitRegion{}).Field(i).Type.Implements(reflect.TypeOf((*surface)(nil)).Elem()) {
			t.Fatal("hit regions must not retain a surface owner")
		}
	}
}

func surfaceDeclarationFiles(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	declared := make(map[string]string)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					declared[spec.Name.Name] = file
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						declared[name.Name] = file
					}
				}
			}
		}
	}
	return declared
}
