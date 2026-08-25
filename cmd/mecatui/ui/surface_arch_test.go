package ui

// surface_arch_test.go is the structural gate for the issue #555 Phase-2
// surface interface (soul proof-of-pattern): it fails CI if any surface/soul
// /skills/mcp/sessions/models vocabulary is added outside its declared homes, or if
// Model acquires a second `surface` field or any soulState/skillsState/mcpState/
// sessionsState/modelsState field back. It imitates approval_arch_test.go —
// placement-only, additive; it never touches rendered output (the soul goldens
// own that).

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
// /skills/mcp/sessions vocabulary may live in. view.go's m.modal.Render(...) and
// update.go's m.modal.HandleKey/HandleMsg(...) routing are vocabulary-free call
// sites and need no exception.
var surfaceFileHomes = map[string]bool{
	"surface.go":          true,
	"soul.go":             true,
	"skills.go":           true,
	"mcp.go":              true,
	"sessions.go":         true,
	"sessions_surface.go": true,
	"models.go":           true,
	"models_catalog.go":   true,
	"models_surface.go":   true,
}

// surfaceFileCount is the explicit homes the gate counts — another surface file
// is an explicit decision here, not a silent drift.
const surfaceFileCount = 9

// surfaceToken identifies declarations governed by the surface placement gate.
var surfaceToken = regexp.MustCompile(`^(?:surface|surfaceDeps|session[A-Za-z0-9]*|soulView|soulNone|soulPanel|soulBodyLines|soulState|soulMaxScroll|clampSoulScroll|soulContentLines|renderSoulPanel|renderSoulMeta|renderSoulBody|soulDisabledNote|soulTrustLabel|skillsView|skillsNone|skillsPanel|skillsDetail|skillsBodyLines|skillsState|filterSkills|cloneSkillGenerations|skillsDisabledNote|skillsEmptyCopy|skillsRowLines|renderSkillsPanel|renderLearnedSkillDetail|openSkills|closeSkills|onSkillsKey|updateSkillsMsg|skillsFilteredRowTotal|syncSkillsFilter|renderSkillsOverlay|mcpView|mcpNone|mcpPanel|mcpResources|mcpResourcePrev|mcpPrompts|mcpPromptArgs|mcpState|argField|renderMCPOverlay|renderMCPPanel|renderResourceList|renderResourcePreview|renderPromptList|renderPromptArgs|mcpStatusLine|mcpPanelFooter|renderGroupsLine|renderRow|hasRequiredArgs|mcpEmptyCopy|mcpDisabledNote|runMCP|runMCPResources|runMCPPrompts|openMCP|closeMCP|onMCPKey|updateMCPMsg|insertIntoInput|joinContents|joinPromptMessages|handlePanelKey|handleResourceKey|handlePromptListKey|handlePromptArgsKey|focusArg|selectPrompt|submitPromptArgs|refreshPanel|modelsView|modelsNone|modelsPanel|modelsChrome|modelsMinRows|modelsState|modelsCatalogIntent|modelsSelectIntent|modelsGlobalDefaultIntent|modelsRowBudgetFor|renderModelsPanel|filterModels|clampModelsCursor|modelsDisabledNote|modelsErrorHint|modelsGatewayEmptyNote|modelsEmptyCopy|promotedStatus|providerStatusLine|renderProviderStatusLines|modelsPositionLabel|modelRowText|modelLabel|modelCapSegments|openModels|configProvenanceProviderSet|availableNotDefaultStatus)$`)

// modelsSymbolHomes pins the deliberate three-way /models split: Model-owned
// installation and effects, durable catalog reduction, and dynamic surface behavior.
// It is stricter than surfaceFileHomes so moving a symbol between those homes is an
// explicit architecture decision rather than an accidental broad allowlist.
var modelsSymbolHomes = map[string]string{
	"openModels":  "models.go",
	"chooseModel": "models.go", "modelSwitchNote": "models.go", "restartOnModel": "models.go", "restartOnModelCmd": "models.go",
	"restartOnModelWithCarryover": "models.go", "carryoverCmd": "models.go", "switchEffort": "models.go", "switchEffortCmd": "models.go",
	"restartFailedMsg": "models.go", "modelSelLabel": "models.go", "saveSelectionCmd": "models.go", "selectionSavedMsg": "models.go",
	"setGlobalDefault": "models.go", "saveGlobalDefaultCmd": "models.go",
	"modelCatalog": "models_catalog.go", "reduceModelCatalog": "models_catalog.go", "updateModelsMsg": "models_catalog.go", "applyModelsCatalog": "models_catalog.go",
	"reconcileSelection": "models_catalog.go", "liveModelLabel": "models_catalog.go", "modelProvenanceLine": "models_catalog.go", "modelProvenance": "models_catalog.go",
	"statusAutoSelected": "models_catalog.go", "configProvenanceProviderSet": "models_catalog.go", "availableNotDefaultStatus": "models_catalog.go",
	"modelsView": "models_surface.go", "modelsNone": "models_surface.go", "modelsPanel": "models_surface.go",
	"modelsChrome": "models_surface.go", "modelsMinRows": "models_surface.go", "modelsState": "models_surface.go",
	"modelsCatalogIntent": "models_surface.go", "modelsSelectIntent": "models_surface.go", "modelsGlobalDefaultIntent": "models_surface.go",
	"modelsRowBudgetFor": "models_surface.go", "renderModelsPanel": "models_surface.go", "filterModels": "models_surface.go", "clampModelsCursor": "models_surface.go",
	"modelsDisabledNote": "models_surface.go", "modelsErrorHint": "models_surface.go", "modelsGatewayEmptyNote": "models_surface.go", "modelsEmptyCopy": "models_surface.go",
	"promotedStatus": "models_surface.go", "providerStatusLine": "models_surface.go", "renderProviderStatusLines": "models_surface.go",
	"modelsPositionLabel": "models_surface.go", "modelRowText": "models_surface.go", "modelLabel": "models_surface.go", "modelCapSegments": "models_surface.go",
}

// TestSurfaceSymbolsLiveInSurfaceFiles walks every non-test ui package file and
// asserts each declaration whose name (decl name or a method's name) carries
// the surface/soul/skills/mcp/sessions vocabulary lives in one of the homes.
func TestSurfaceSymbolsLiveInSurfaceFiles(t *testing.T) {
	if len(surfaceFileHomes) != surfaceFileCount {
		t.Fatalf("surfaceFileHomes has %d entries, want surfaceFileCount=%d (an additional surface file must be an explicit decision here)",
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
				if n == "" {
					continue
				}
				_, isModelsSymbol := modelsSymbolHomes[n]
				if !surfaceToken.MatchString(n) && !isModelsSymbol {
					continue
				}
				if !surfaceFileHomes[file] {
					t.Errorf("surface/soul/skills/mcp/sessions-vocabulary declaration %q in non-surface file %s (want surface.go, soul.go, skills.go, mcp.go, or sessions.go)", n, file)
				}
				if want, ok := modelsSymbolHomes[n]; ok && file != want {
					t.Errorf("/models declaration %q lives in %s, want %s", n, file, want)
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
// approval surface's ephemeral state).
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

// TestModelHasNoSessionsStateField asserts Model holds NO sessionsState field —
// the picker and transcript views live only in m.modal. A pre-declared
// m.sessions field would split the /sessions surface state from its dynamic
// Open lifetime and is tombstone drift.
func TestModelHasNoSessionsStateField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	var count int
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Type.Name() == "sessionsState" {
			count++
		}
	}
	if count != 0 {
		t.Errorf("Model has %d sessionsState fields, want exactly 0 (/sessions state lives only in m.modal)", count)
	}
}

// TestModelHasNoModelsStateField keeps the picker lifetime inside m.modal.
func TestModelHasNoModelsStateField(t *testing.T) {
	st := reflect.TypeOf(Model{})
	for i := 0; i < st.NumField(); i++ {
		if st.Field(i).Type.Name() == "modelsState" {
			t.Error("Model has a modelsState field; /models state lives only in m.modal")
		}
	}
}

// TestSurfaceDepsIsAmbientOnly reflects over the SHARED surfaceDeps struct and
// asserts it carries ONLY the ambient base fields (theme/keys/marks/caps/ctx)
// plus the Model-owned reference hit allocator, and NONE of the archived deps-per-call wideners (width/lifecycle/nextEpoch/
// focusInput — surface-SPECIFIC collaborators live as fields on the surface's
// own state struct, set next to deps in the same Open literal, per
// docs/design/surface-migration-plan.md §4 decision 8). ctx is ambient: any
// modal that talks to the server needs the parent context, so it belongs in
// the shared base, not on each surface. This is the anti-regression guard for
// the deps-on-state redesign: a deps-per-call widening fails here the moment a
// non-ambient field lands.
func TestSurfaceDepsIsAmbientOnly(t *testing.T) {
	st := reflect.TypeOf(surfaceDeps{})
	want := []string{"theme", "keys", "marks", "caps", "ctx", "hits"}
	if st.NumField() != len(want) {
		t.Errorf("surfaceDeps has %d fields, want exactly %d (theme/keys/marks/caps/ctx/hits only)", st.NumField(), len(want))
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
