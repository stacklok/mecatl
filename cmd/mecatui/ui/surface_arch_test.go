package ui

// surface_arch_test.go is the structural gate for the issue #555 Phase-2
// surface interface (soul proof-of-pattern): it fails CI if any surface/soul
// vocabulary declaration is added OUTSIDE surface.go/soul.go, or if Model
// acquires a second `surface` field or any `soulState` field back. It imitates
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
// vocabulary may live in. view.go's m.modal.Render(...) and update.go's
// m.modal.HandleKey/HandleMsg(...) routing are vocabulary-free call sites and
// need no exception.
var surfaceFileHomes = map[string]bool{
	"surface.go": true,
	"soul.go":    true,
}

// surfaceFileCount is the two homes the gate counts — a third surface file is
// an explicit decision here, not a silent drift.
const surfaceFileCount = 2

// surfaceToken matches the soul + surface identifier vocabulary the gate guards:
// every soul declaration, plus the interface/deps struct. Anchored so plain
// "surface"-substring incidental names are not false-positives; widening it to
// catch a new migrated-in symbol is the visible decision the loud gate is for.
var surfaceToken = regexp.MustCompile(`^(?:surface|surfaceDeps|soulView|soulNone|soulPanel|soulBodyLines|soulState|soulMaxScroll|clampSoulScroll|soulContentLines|renderSoulPanel|renderSoulMeta|renderSoulBody|soulDisabledNote|soulTrustLabel)$`)

// TestSurfaceSymbolsLiveInSurfaceFiles walks every non-test ui package file and
// asserts each declaration whose name (decl name or a method's name) carries
// the surface/soul vocabulary lives in one of the two homes.
func TestSurfaceSymbolsLiveInSurfaceFiles(t *testing.T) {
	if len(surfaceFileHomes) != surfaceFileCount {
		t.Fatalf("surfaceFileHomes has %d entries, want surfaceFileCount=%d (a third surface file must be an explicit decision here)",
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
					t.Errorf("surface/soul-vocabulary declaration %q in non-surface file %s (want surface.go or soul.go)", n, file)
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
