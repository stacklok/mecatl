package ui

// surface_arch_test.go is the structural gate for the issue #555 Phase-2
// surface interface (soul proof-of-pattern): it fails CI if any surface/soul
// /skills/mcp vocabulary declaration is added OUTSIDE surface.go/soul.go/
// skills.go/mcp.go, or if Model acquires a second `surface` field or any
// soulState/skillsState/mcpState field back. It imitates
// approval_arch_test.go — placement-only, additive; it never touches rendered
// output (the soul goldens own that).

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

// surfaceFileHomes is the set of files a declaration carrying the surface/soul
// /skills/mcp vocabulary may live in. view.go's m.modal.Render(...) and
// update.go's m.modal.HandleKey/HandleMsg(...) routing are vocabulary-free call
// sites and need no exception.
var surfaceFileHomes = map[string]bool{
	"surface.go": true,
	"soul.go":    true,
	"skills.go":  true,
	"mcp.go":     true,
}

// surfaceFileCount is the four homes the gate counts — a fifth surface file is
// an explicit decision here, not a silent drift.
const surfaceFileCount = 4

// surfaceToken identifies declarations governed by the surface placement gate.
var surfaceToken = regexp.MustCompile(`^(?:surface|surfaceDeps|soulView|soulNone|soulPanel|soulBodyLines|soulState|soulMaxScroll|clampSoulScroll|soulContentLines|renderSoulPanel|renderSoulMeta|renderSoulBody|soulDisabledNote|soulTrustLabel|skillsView|skillsNone|skillsPanel|skillsDetail|skillsBodyLines|skillsState|filterSkills|cloneSkillGenerations|skillsDisabledNote|skillsEmptyCopy|skillsRowLines|renderSkillsPanel|renderLearnedSkillDetail|openSkills|closeSkills|onSkillsKey|updateSkillsMsg|skillsFilteredRowTotal|syncSkillsFilter|renderSkillsOverlay|mcpView|mcpNone|mcpPanel|mcpResources|mcpResourcePrev|mcpPrompts|mcpPromptArgs|mcpState|argField|renderMCPOverlay|renderMCPPanel|renderResourceList|renderResourcePreview|renderPromptList|renderPromptArgs|mcpStatusLine|mcpPanelFooter|renderGroupsLine|renderRow|hasRequiredArgs|mcpEmptyCopy|mcpDisabledNote|runMCP|runMCPResources|runMCPPrompts|openMCP|closeMCP|onMCPKey|updateMCPMsg|insertIntoInput|joinContents|joinPromptMessages|handlePanelKey|handleResourceKey|handlePromptListKey|handlePromptArgsKey|focusArg|selectPrompt|submitPromptArgs|refreshPanel|mcpInsertResourceMsg)$`)

// TestSurfaceSymbolsLiveInSurfaceFiles walks every non-test ui package file and
// asserts each declaration whose name (decl name or a method's name) carries
// the surface/soul/skills/mcp vocabulary lives in one of the homes.
func TestSurfaceSymbolsLiveInSurfaceFiles(t *testing.T) {
	if len(surfaceFileHomes) != surfaceFileCount {
		t.Fatalf("surfaceFileHomes has %d entries, want surfaceFileCount=%d (a fifth surface file must be an explicit decision here)",
			len(surfaceFileHomes), surfaceFileCount)
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
				names = append(names, d.Name.Name)
			}
			for _, n := range names {
				if n == "" || !surfaceToken.MatchString(n) {
					continue
				}
				if !surfaceFileHomes[file] {
					t.Errorf("surface/soul/skills/mcp-vocabulary declaration %q in non-surface file %s (want surface.go, soul.go, skills.go, or mcp.go)", n, file)
				}
			}
		}
	}
}

// TestModelHasOneModalSurfaceField asserts Model holds the migrating overlay in
// exactly ONE field of the `surface` interface type ("modal"). A second surface
// field is the Phase-3 stack drift this kills.
func TestModelHasOneModalSurfaceField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	var count int
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		// For an interface-typed field reflect Name() reports the interface type
		// name ("surface"); a value struct reports "" and is skipped here (the
		// no-soulState-field gate below asserts value structs by Kind).
		if f.Type.Kind() == reflect.Interface && f.Type.Name() == "surface" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Model has %d surface-interface fields, want exactly 1", count)
	}
}

// TestModelHasNoSoulStateField asserts Model holds NO soulState field — the
// dynamic-Open decision means the soul state lives ONLY in m.modal; a
// pre-declared m.soul field is the tombstone drift this kills. Matches by
// reflect Type.Name() (the same idiom approval_arch_test.go uses for
// approvalState).
func TestModelHasNoSoulStateField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	var count int
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Type.Name() == "soulState" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("Model has %d soulState fields, want exactly 0 (soul state lives only in m.modal)", count)
	}
}

// TestModelHasNoSkillsStateField asserts Model holds NO skillsState field — the
// dynamic-Open decision means the skills state lives ONLY in m.modal; a
// pre-declared m.skills field is the tombstone drift this kills. The Model fields
// skillsEpoch and skillChangeLast STAY and are NOT chased by this gate — each
// for its own distinct reason: skillsEpoch is the RPC nonce (correlation must
// survive surface close/reopen, so the epoch lives on the Model, not the
// surface); skillChangeLast is the status-receipt cursor, Model-owned because
// the SkillChangesMsg receipt must fire when NO surface is open (the surface's
// HandleMsg deliberately passes it through handled=false). Matches by reflect
// Type.Name() (mirrors TestModelHasNoSoulStateField).
func TestModelHasNoSkillsStateField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	var count int
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Type.Name() == "skillsState" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("Model has %d skillsState fields, want exactly 0 (skills state lives only in m.modal)", count)
	}
}

// TestModelHasNoMCPStateField asserts Model holds NO mcpState field — the
// dynamic-Open decision means the MCP state lives ONLY in m.modal; a
// pre-declared m.mcp field is the tombstone drift this kills. The MCP surface
// keeps NO Model-owned companion field (unlike skills' skillsEpoch: the MCP RPCs
// are single-shot with no cross-close correlation nonce to preserve). Matches by
// reflect Type.Name() (mirrors TestModelHasNoSkillsStateField).
func TestModelHasNoMCPStateField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	var count int
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Type.Name() == "mcpState" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("Model has %d mcpState fields, want exactly 0 (MCP state lives only in m.modal)", count)
	}
}

// TestSurfaceDepsIsAmbientOnly reflects over the SHARED surfaceDeps struct and
// asserts it carries ONLY the ambient base fields (theme/keys/marks/caps/ctx)
// and NONE of the archived deps-per-call wideners (width/lifecycle/nextEpoch/
// focusInput — surface-SPECIFIC collaborators live as fields on the surface's
// own state struct, set next to deps in the same Open literal, per
// docs/design/surface-migration-plan.md §4 decision 8). ctx is ambient: any
// modal that talks to the server needs the parent context, so it belongs in
// the shared base, not on each surface. This is the anti-regression guard for
// the deps-on-state redesign: a deps-per-call widening fails here the moment a
// non-ambient field lands.
func TestSurfaceDepsIsAmbientOnly(t *testing.T) {
	st := reflect.TypeOf(surfaceDeps{})
	want := []string{"theme", "keys", "marks", "caps", "ctx"}
	if st.NumField() != len(want) {
		t.Errorf("surfaceDeps has %d fields, want exactly %d (theme/keys/marks/caps/ctx only)", st.NumField(), len(want))
	}
	banned := []string{"width", "lifecycle", "nextEpoch", "focusInput"}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		for _, b := range banned {
			if f.Name == b {
				t.Errorf("surfaceDeps carries the archived widener %q — surface-specific collaborators belong on the surface state struct, not the shared deps", b)
			}
		}
		found := false
		for _, w := range want {
			if f.Name == w {
				found = true
			}
		}
		if !found {
			t.Errorf("surfaceDeps field %q is not one of the ambient base fields %v", f.Name, want)
		}
	}
}
