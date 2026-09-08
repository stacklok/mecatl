// Package apicheck holds the MECHANICAL freshness gate for the engine module's
// public API surface (issue #114, ADR 0037).
//
// # The contract
//
// The seven core packages of the engine module — engine/session, engine/governance,
// engine/tool, engine/prompt, engine/port, engine/team, engine/agent — are the
// importable, COMPATIBILITY-COMMITTED surface (see engine/COMPATIBILITY.md). This
// test enumerates every exported identifier of those packages into a stable,
// human-readable text snapshot and commits it at engine/api/<pkg>.txt. On every
// PR the test re-derives the surface and diffs it against the committed baseline;
// an unflagged change FAILS the build, forcing a deliberate, reviewed baseline
// update plus a CHANGELOG note classified per the compatibility policy.
//
// This mirrors the repo's existing -update golden idiom (see `task test:golden`
// and the mecatui teatest goldens): with -update the baselines are rewritten;
// without it they are read and compared. It is deterministic and OFFLINE — it
// loads packages with go/packages (type info only, no network) and renders the
// surface with go/types object strings, which are stable across Go PATCH versions
// (a Go MINOR bump may reformat them → a one-time `task api:update`).
//
// The tooling deliberately lives in the ROOT module so the engine module's go.mod
// stays tiny (no go/tools dependency travels with the importable core, ADR 0036).
// The committed .txt baselines, however, ship INSIDE the engine module so external
// consumers can read the surface they depend on.
package apicheck

import (
	"flag"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/stacklok/mecatl/engine/arch"
)

// guardedPackages is the source-of-truth list of the engine module's seven
// COMPATIBILITY-COMMITTED core packages, as engine-module import paths.
//
// It is DERIVED from arch.CorePackages (engine/arch/surface.go) — the SINGLE
// source of truth that the layering DAG tests also consume — so the public-API
// gate and the layering tests can never drift. TestGuardedSetMatchesCorePackages
// pins the derivation (a belt-and-braces equality assert), and the engine/arch
// import being a dependency-free, test-support package keeps the root module's
// dependency closure unchanged.
var guardedPackages = arch.CorePackages

// shortName maps a full import path to its baseline filename stem (the last path
// element), e.g. ".../engine/port" → "port".
func shortName(importPath string) string {
	return importPath[strings.LastIndex(importPath, "/")+1:]
}

// updateBaselines reports whether baselines should be rewritten. It reuses the
// -update flag the harness convention already uses (defining our own here would
// not collide since this is a standalone package, but matching the idiom keeps
// `task api:update` identical to `task test:golden`).
func updateBaselines() bool {
	if f := flag.Lookup("update"); f != nil {
		return f.Value.String() == "true"
	}
	return false
}

// register the -update flag if no other test in this package has. Standalone
// package, so we own it.
var _ = func() bool {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite the committed engine/api/*.txt baselines")
	}
	return true
}()

// apiDir resolves the absolute path to engine/api, robustly from the package's
// own location (internal/apicheck → ../../engine/api).
func apiDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// wd is .../mecatl/internal/apicheck; engine/api is two levels up.
	dir := filepath.Clean(filepath.Join(wd, "..", "..", "engine", "api"))
	return dir
}

// loadPackages loads the guarded packages with full type information under the
// workspace. Offline: no build, no network — go/packages reads types only.
func loadPackages(t *testing.T) map[string]*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedImports |
			packages.NeedDeps,
		Tests: false,
	}
	loaded, err := packages.Load(cfg, guardedPackages...)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	byPath := make(map[string]*packages.Package, len(loaded))
	var loadErrs []string
	for _, p := range loaded {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %s", p.PkgPath, e))
		}
		byPath[p.PkgPath] = p
	}
	if len(loadErrs) > 0 {
		t.Fatalf("package load errors:\n%s", strings.Join(loadErrs, "\n"))
	}
	for _, want := range guardedPackages {
		if byPath[want] == nil {
			t.Fatalf("guarded package %q did not load — is the go.work workspace intact?", want)
		}
	}
	return byPath
}

// dumpPackage renders the exported API of pkg as a sorted, stable, human-readable
// text block. Each top-level exported object becomes a line via
// types.ObjectString with a package-local qualifier (so other packages are
// referred to by their package name, never a file position). For exported named
// types we additionally enumerate exported struct fields and exported methods
// (value + pointer receiver), each on its own sorted, indented line. No file
// positions, no doc comments, no unexported symbols — pure type identity.
func dumpPackage(pkg *packages.Package) string {
	scope := pkg.Types.Scope()
	thisPkg := pkg.Types

	// qualifier: drop our own package name, name others by package name only.
	qual := func(p *types.Package) string {
		if p == thisPkg {
			return ""
		}
		return p.Name()
	}

	var lines []string
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		lines = append(lines, renderObject(obj, qual))

		// For named types, enumerate exported fields + methods so a change to a
		// type's shape (a new field, a removed method) is caught even though the
		// top-level ObjectString of a struct already includes its fields. We add
		// methods explicitly because ObjectString of a defined type does NOT list
		// its method set.
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}

		var sub []string
		// Exported methods on the named type (value + pointer receiver).
		mset := types.NewMethodSet(types.NewPointer(named))
		for i := 0; i < mset.Len(); i++ {
			m := mset.At(i).Obj()
			if !m.Exported() {
				continue
			}
			sub = append(sub, "    method "+types.ObjectString(m, qual))
		}
		// Exported interface methods (explicit method set of an interface).
		if iface, ok := named.Underlying().(*types.Interface); ok {
			for i := 0; i < iface.NumMethods(); i++ {
				m := iface.Method(i)
				if !m.Exported() {
					continue
				}
				sub = append(sub, "    ifacemethod "+types.ObjectString(m, qual))
			}
		}
		sort.Strings(sub)
		// Dedup (a value+pointer method can appear once; interface methods are
		// distinct prefixes so no cross-collision).
		sub = dedup(sub)
		lines = append(lines, sub...)
	}

	sort.Strings(lines)
	lines = dedup(lines)
	return strings.Join(lines, "\n") + "\n"
}

// renderObject renders one top-level exported object to its baseline line. It is
// types.ObjectString for most objects, with two deliberate refinements:
//
//   - CONSTS append their exact VALUE (obj.Val().ExactString(), deterministic),
//     because types.ObjectString prints the TYPE only — so a wire-protocol enum
//     value change (e.g. EvApproval "approval" → "approved", or a Stop* string)
//     would otherwise be invisible, which is a real break for an external
//     consumer reading the constant off the wire.
//   - exported STRUCT types are rendered from their EXPORTED FIELDS ONLY, so
//     internal field churn (a private mutex, map, or sub-struct) does not force a
//     spurious baseline update / CHANGELOG note, and private layout never ships.
//     This mirrors how exported methods are already filtered.
//
// All other objects fall through to types.ObjectString unchanged. Deterministic.
func renderObject(obj types.Object, qual types.Qualifier) string {
	switch o := obj.(type) {
	case *types.Const:
		return types.ObjectString(o, qual) + " = " + o.Val().ExactString()
	case *types.TypeName:
		if named, ok := o.Type().(*types.Named); ok {
			if st, ok := named.Underlying().(*types.Struct); ok {
				return renderStructType(o, named, st, qual)
			}
		}
	}
	return types.ObjectString(obj, qual)
}

// renderStructType renders an exported struct type as a `type Name struct{...}`
// line containing ONLY its exported fields, in declaration order (deterministic),
// reusing go/types' own field/tag formatting for each kept field so the rendering
// matches types.ObjectString byte-for-byte minus the unexported fields.
func renderStructType(tn *types.TypeName, named *types.Named, st *types.Struct, qual types.Qualifier) string {
	var fields []*types.Var
	var tags []string
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		fields = append(fields, f)
		tags = append(tags, st.Tag(i))
	}
	exported := types.NewStruct(fields, tags)
	prefix := "type " + tn.Name()
	if tp := named.TypeParams(); tp != nil && tp.Len() > 0 {
		prefix += renderTypeParams(tp, qual)
	}
	return prefix + " " + types.TypeString(exported, qual)
}

// renderTypeParams renders a generic type's parameter list, e.g. "[T any]",
// using go/types formatting so it matches ObjectString's rendering.
func renderTypeParams(tp *types.TypeParamList, qual types.Qualifier) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < tp.Len(); i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		p := tp.At(i)
		b.WriteString(p.Obj().Name())
		b.WriteByte(' ')
		b.WriteString(types.TypeString(p.Constraint(), qual))
	}
	b.WriteByte(']')
	return b.String()
}

func dedup(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// TestPublicAPIUnchanged is the gate. With -update it (re)writes every baseline;
// without it it compares each package's live surface to the committed baseline
// and fails on any drift.
func TestPublicAPIUnchanged(t *testing.T) {
	t.Parallel()
	pkgs := loadPackages(t)
	dir := apiDir(t)

	if updateBaselines() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	for _, importPath := range guardedPackages {
		pkg := pkgs[importPath]
		got := dumpPackage(pkg)
		path := filepath.Join(dir, shortName(importPath)+".txt")

		if updateBaselines() {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil { //nolint:gosec // committed text baseline
				t.Fatalf("write baseline %s: %v", path, err)
			}
			continue
		}

		wantBytes, err := os.ReadFile(path) //nolint:gosec // committed text baseline
		if err != nil {
			t.Errorf("read baseline %s: %v\nengine public API baseline missing: run `task api:update`, commit %s, and add an engine/CHANGELOG.md entry classified per engine/COMPATIBILITY.md (Added=minor, Changed/Removed=breaking).",
				path, err, path)
			continue
		}
		want := string(wantBytes)
		if got != want {
			t.Errorf("engine public API changed for %s: run `task api:update`, commit engine/api/%s.txt, and add an engine/CHANGELOG.md entry classified per engine/COMPATIBILITY.md (Added=minor, Changed/Removed=breaking).\n%s",
				importPath, shortName(importPath), unifiedish(want, got))
		}
	}
}

// TestGuardedSetMatchesBaselineDir is the DRIFT GUARD. The set of .txt files in
// engine/api must equal exactly the guarded-package set: a baseline with no
// guarded package (stray) OR a guarded package with no baseline (escape) both
// fail. This is what stops a newly-added core package from silently escaping the
// gate and notices a removed one. (Skipped under -update, which is allowed to be
// creating the files for the first time.)
func TestGuardedSetMatchesBaselineDir(t *testing.T) {
	t.Parallel()
	if updateBaselines() {
		t.Skip("-update is (re)writing baselines; the set check runs in the read path")
	}
	dir := apiDir(t)

	want := make(map[string]bool, len(guardedPackages))
	for _, p := range guardedPackages {
		want[shortName(p)+".txt"] = true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read engine/api dir %s: %v", dir, err)
	}
	got := make(map[string]bool)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".txt") {
			continue // README.md and any non-.txt are not baselines
		}
		got[n] = true
	}

	for n := range want {
		if !got[n] {
			t.Errorf("guarded package baseline %s is MISSING from engine/api — run `task api:update` and commit it (a core package must not escape the gate)", n)
		}
	}
	for n := range got {
		if !want[n] {
			t.Errorf("stray baseline engine/api/%s has no guarded package — remove it, or add the package to arch.CorePackages in engine/arch/surface.go (which guardedPackages derives from)", n)
		}
	}
}

// TestGuardedSetMatchesCorePackages makes the COMPATIBILITY.md claim TRUE: the
// gate's guarded set is MECHANICALLY EQUAL to engine/arch's CorePackages — the
// single source of truth the layering DAG tests also consume. guardedPackages is
// derived from arch.CorePackages directly (so this is belt-and-braces), but the
// explicit assertion documents the contract and fails loudly if a future edit
// re-introduces a hand-maintained copy that drifts.
func TestGuardedSetMatchesCorePackages(t *testing.T) {
	t.Parallel()
	if !reflect.DeepEqual(guardedPackages, arch.CorePackages) {
		t.Fatalf("guardedPackages drifted from engine/arch.CorePackages:\n guarded = %v\n core    = %v\n"+
			"the public-API gate MUST guard exactly the layering core (engine/COMPATIBILITY.md); keep guardedPackages derived from arch.CorePackages",
			guardedPackages, arch.CorePackages)
	}
}

func TestScalableReflectionEvidence_Scenario8_EngineAPIAndLayeringGates(t *testing.T) {
	const learningPath = "github.com/stacklok/mecatl/engine/learning"
	if !slices.Contains(arch.CorePackages, learningPath) {
		t.Fatalf("engine/learning is absent from the shared API/layering core: %v", arch.CorePackages)
	}

	pkg := loadPackages(t)[learningPath]
	if updateBaselines() {
		return
	}
	baselinePath := filepath.Join(apiDir(t), "learning.txt")
	baselineBytes, err := os.ReadFile(baselinePath) //nolint:gosec // committed text baseline
	if err != nil {
		t.Fatalf("read learning API baseline: %v", err)
	}
	baseline := string(baselineBytes)
	if got := dumpPackage(pkg); got != baseline {
		t.Fatalf("engine/learning API snapshot is stale:\n%s", unifiedish(baseline, got))
	}
	for _, declaration := range []string{
		"func MaterializeEvidence(ctx context.Context, req MaterializationRequest) (Materialization, error)",
		"type MaterializationManifest struct",
		"type EvidenceRef struct",
		"type Input struct",
	} {
		if !strings.Contains(baseline, declaration) {
			t.Errorf("engine/learning API snapshot is missing %q", declaration)
		}
	}

	changelogPath := filepath.Clean(filepath.Join(apiDir(t), "..", "CHANGELOG.md"))
	changelogBytes, err := os.ReadFile(changelogPath) //nolint:gosec // committed documentation
	if err != nil {
		t.Fatalf("read engine changelog: %v", err)
	}
	changelog := string(changelogBytes)
	changedStart := strings.Index(changelog, "### Changed\n")
	if changedStart < 0 {
		t.Fatal("engine changelog has no Changed section")
	}
	changed := changelog[changedStart:]
	if next := strings.Index(changed[len("### Changed\n"):], "\n### "); next >= 0 {
		changed = changed[:len("### Changed\n")+next]
	}
	if !strings.Contains(changed, "Versioned bounded reflection evidence materialization") ||
		!strings.Contains(changed, "Input") || !strings.Contains(changed, "EvidenceRef") {
		t.Error("engine changelog Changed section does not classify the reflection Input/EvidenceRef compatibility changes")
	}
}

// unifiedish renders a compact line-oriented diff (added/removed only) so a
// reviewer sees WHAT changed in the surface, not a wall of two full dumps.
func unifiedish(want, got string) string {
	wantLines := strings.Split(strings.TrimRight(want, "\n"), "\n")
	gotLines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	wantSet := make(map[string]bool, len(wantLines))
	for _, l := range wantLines {
		wantSet[l] = true
	}
	gotSet := make(map[string]bool, len(gotLines))
	for _, l := range gotLines {
		gotSet[l] = true
	}
	var b strings.Builder
	b.WriteString("--- surface diff (committed vs current) ---\n")
	for _, l := range wantLines {
		if !gotSet[l] {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range gotLines {
		if !wantSet[l] {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}
