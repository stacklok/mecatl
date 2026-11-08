package dtogen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestViewExternalOwnerClosureAndFreshness(t *testing.T) {
	cfg := viewFixture(t, "view")
	module := filepath.Dir(cfg.Dir)
	root := filepath.Dir(module)
	writeFixture(t, filepath.Join(module, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\n\nrequire (\nexample.invalid/payloads v0.0.0\ngithub.com/jcalabro/atmos v0.8.0\n)\nreplace example.invalid/payloads => ../payloads\n")
	source := filepath.Join(root, "payloads/data/value.go")
	writeFixture(t, filepath.Join(root, "payloads/go.mod"), "module example.invalid/payloads\n\ngo 1.27.3\n")
	writeFixture(t, source, `//go:generate go -C ../.. run ./internal/dtogen/cmd/dtogen -dir payloads/data -types Record -emit clone,equal -out zz_data_generated.go
package data
// Record is external backing. Provisional: retain provider vocabulary.
type Record struct {
 // Title is optional provider spelling, not normalized.
 Title string
 Child Child
 Maybe *Child
 Children []Child
}
type Child struct { Data []byte; Hidden struct { private []byte } }
var _ = Record{}.Clone().Equal(Record{})
`)
	writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), `package owner
import "example.invalid/payloads/data"
type RecordView struct { value *data.Record; scope string }
type ChildView struct { value *data.Child }
func(v RecordView) Scope()string{return v.scope}
var _ = RecordView{}.Title()
var _ = RecordView{}.Children().At
`)
	mapping := `[
 {"view":"RecordView","backing":"value","fields":[{"field":"Title","method":"Title"},{"field":"Child","method":"Child"},{"field":"Maybe","method":"Maybe"},{"field":"Children","method":"Children"}],"context":[{"method":"Scope","signature":"func() string"}]},
 {"view":"ChildView","backing":"value","fields":[{"field":"Data","method":"Data"},{"field":"Hidden","exclude":true}]}]`
	writeFixture(t, filepath.Join(cfg.Dir, cfg.Views), mapping)
	writeFixture(t, filepath.Join(cfg.Dir, "registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir engine/owner -emit view -views views.json -out "+cfg.Output+"\npackage owner\n")
	outputs := []string{filepath.Join(root, "payloads/data/zz_data_generated.go"), filepath.Join(cfg.Dir, cfg.Output)}
	ctx := context.Background()
	for _, prior := range []string{"", GeneratedMarker + "\nnot valid Go", GeneratedMarker + "\npackage data\nvar broken MissingType"} {
		if prior != "" {
			for _, path := range outputs {
				writeFixture(t, path, prior)
			}
		}
		// Traps prove the loader does not run either owner, even in a fresh closure.
		for _, dir := range []string{cfg.Dir, filepath.Dir(source)} {
			writeFixture(t, filepath.Join(dir, "trap.go"), "package "+map[bool]string{true: "owner", false: "data"}[dir == cfg.Dir]+"\nfunc init(){panic(\"owner executed\")}\n")
		}
		if err := Generate(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{cfg.Dir, filepath.Dir(source)} {
			if err := os.Remove(filepath.Join(dir, "trap.go")); err != nil {
				t.Fatal(err)
			}
		}
		if err := GenerateRepository(ctx, root, true); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(readFixture(t, outputs[1]), "Provisional: retain provider vocabulary") {
		t.Fatal("external docs lost")
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("testing";"example.invalid/payloads/data")
func TestExternal(t *testing.T){x:=data.Record{Title:"external",Child:data.Child{Data:[]byte{1}},Maybe:&data.Child{},Children:[]data.Child{{Data:[]byte{2}}}};v:=RecordView{value:&x};if v.Title()!="external"||v.Child().Data().At(0)!=1||v.Children().At(0).Data().At(0)!=2{t.Fatal("projection")};if _,ok:=v.Maybe();!ok{t.Fatal("presence")};x=data.Record{};if v.Title()!=""{t.Fatal("view unexpectedly cloned")}}
`)
	runOwner(t, cfg, "")
	// Both ordinary freshness and the structural assertion notice a new field.
	oldSource := readFixture(t, source)
	writeFixture(t, source, strings.Replace(oldSource, "Title string", "Title string; Added bool", 1))
	cmd := exec.Command("go", "test", "-mod=readonly", "./owner", "-run", "^$")
	cmd.Dir = module
	cmd.Env = loadEnvironment()
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "cannot convert") {
		t.Fatalf("stale shape compiled: %v\n%s", err, output)
	}
	before := repositoryState(t, root)
	if err := Generate(ctx, cfg); err == nil || !strings.Contains(err.Error(), "uncovered backing field") {
		t.Fatal("new field accepted", err)
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("invalid closure changed artifacts")
	}
	writeFixture(t, source, oldSource)
	// Every stale check must be nonmutating, including mapping and owner docs.
	for _, change := range []struct{ name, path, from, to string }{
		{"field docs", source, "Title is optional provider spelling", "Title is revised provider spelling"},
		{"type docs", source, "Provisional: retain provider vocabulary", "Provisional: revisit provider vocabulary"},
		{"map", filepath.Join(cfg.Dir, cfg.Views), `"method":"Child"`, `"method":"RequiredChild"`},
	} {
		t.Run(change.name, func(t *testing.T) {
			old := readFixture(t, change.path)
			writeFixture(t, change.path, strings.Replace(old, change.from, change.to, 1))
			before := repositoryState(t, root)
			if err := GenerateRepository(ctx, root, true); err == nil || !strings.Contains(err.Error(), "stale data output") {
				t.Fatal("expected stale data bytes", err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, root)) {
				t.Fatal("check wrote files")
			}
			// Map changes may require handwritten caller updates; docs-only regenerate.
			if change.name != "map" {
				if err := Generate(ctx, cfg); err != nil {
					t.Fatal(err)
				}
				if err := GenerateRepository(ctx, root, true); err != nil {
					t.Fatal(err)
				}
			}
			writeFixture(t, change.path, old)
			if err := Generate(ctx, cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
	registry := filepath.Join(cfg.Dir, "registry.go")
	declaration := readFixture(t, registry)
	writeFixture(t, registry, "package owner\n")
	before = repositoryState(t, root)
	if err := GenerateRepository(ctx, root, true); err == nil || !strings.Contains(err.Error(), "extra data output") {
		t.Fatal("removed view owner accepted", err)
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("inventory check wrote files")
	}
	writeFixture(t, registry, declaration)
	// A failure in the rendered selected closure preserves BOTH earlier owners.
	writeFixture(t, source, strings.Replace(oldSource, "Title string", "Title string; Added bool", 1))
	before = repositoryState(t, root)
	if err := GenerateRepository(ctx, root, false); err == nil {
		t.Fatal("invalid view closure accepted")
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("partial install after view mapping failure")
	}
}

func TestCLIViewOnly(t *testing.T) {
	binary := buildCLI(t)
	cfg := viewFixture(t, "view")
	cmd := exec.Command(binary, "-dir", cfg.Dir, "-emit", "view", "-views", cfg.Views, "-out", cfg.Output)
	cmd.Dir = t.TempDir()
	cmd.Env = loadEnvironment()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("view-only CLI: %s %v", out, err)
	}
	runOwner(t, cfg, "")
}

func TestViewConfigurationErrors(t *testing.T) {
	for _, tc := range []struct {
		emit, views string
		types       []string
	}{
		{"view", "", nil}, {"clone", "views.json", []string{"Record"}}, {"view", "../views.json", nil}, {"view", "views.json", []string{"Record"}},
	} {
		t.Run(tc.emit+tc.views, func(t *testing.T) {
			cfg := viewFixture(t, "view")
			cfg.Emit = tc.emit
			cfg.Views = tc.views
			cfg.Types = tc.types
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err == nil {
				t.Fatal("invalid config accepted")
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("invalid config wrote output")
			}
		})
	}
}
