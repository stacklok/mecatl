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
type Child struct { Data []byte; Hidden struct { private []byte } `+"`view:\"-\"`"+` }
var _ = Record{}.Clone().Equal(Record{})
`)
	writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), `package owner
import "example.invalid/payloads/data"
type RecordView struct { value *data.Record; scope string }
type ChildView struct { value *data.Child }
type ExtraView struct { value *data.Record; scope string }
func(v RecordView) Scope()string{return v.scope}
var _ = RecordView{}.Title()
var _ = RecordView{}.Children().At
`)
	writeFixture(t, filepath.Join(cfg.Dir, "registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir engine/owner -emit view -types RecordView,ChildView -out "+cfg.Output+"\npackage owner\n")
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
	if err := GenerateRepository(ctx, root, true); err == nil || !strings.Contains(err.Error(), "stale data output") {
		t.Fatal("new field was not stale", err)
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("check changed artifacts")
	}
	if err := Generate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFixture(t, outputs[1]), "func (v RecordView) Added() bool") {
		t.Fatal("new supported field lacks default getter")
	}
	addedTest := filepath.Join(cfg.Dir, "added_test.go")
	writeFixture(t, addedTest, `package owner
 import("testing";"example.invalid/payloads/data")
 func TestNewFieldDefault(t *testing.T){v:=RecordView{value:&data.Record{Added:true}};if !v.Added()||(RecordView{}).Added(){t.Fatal("default getter value/zero")}}
 `)
	runOwner(t, cfg, "")
	if err := os.Remove(addedTest); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, source, oldSource)
	if err := Generate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// Every stale check must be nonmutating, including policy, layout and owner docs.
	for _, change := range []struct{ name, path, from, to string }{
		{"field docs", source, "Title is optional provider spelling", "Title is revised provider spelling"},
		{"type docs", source, "Provisional: retain provider vocabulary", "Provisional: revisit provider vocabulary"},
		{"policy", source, "Maybe *Child", "Maybe *Child `view:\"-\"`"},
		{"layout", filepath.Join(cfg.Dir, "owner.go"), "scope string", "scope string; extra uint64"},
		{"selection", filepath.Join(cfg.Dir, "registry.go"), "RecordView,ChildView", "RecordView,ChildView,ExtraView"},
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
			// Direct generation retains its explicit selection; repository selection is source-owned.
			if change.name != "selection" {
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
	writeFixture(t, source, strings.Replace(oldSource, "Title string", "Title map[string]string", 1))
	before = repositoryState(t, root)
	if err := GenerateRepository(ctx, root, false); err == nil {
		t.Fatal("invalid view closure accepted")
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("partial install after view policy failure")
	}
}

func TestCLIViewOnly(t *testing.T) {
	binary := buildCLI(t)
	cfg := viewFixture(t, "view")
	cmd := exec.Command(binary, "-dir", cfg.Dir, "-emit", "view", "-types", strings.Join(cfg.Types, ","), "-out", cfg.Output)
	cmd.Dir = t.TempDir()
	cmd.Env = loadEnvironment()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("view-only CLI: %s %v", out, err)
	}
	for _, args := range [][]string{
		{"-dir", cfg.Dir, "-emit", "view", "-out", cfg.Output},
		{"-dir", cfg.Dir, "-emit", "view", "-types", "RecordView,ChildView", "-views", "views.json", "-out", cfg.Output},
	} {
		before := repositoryState(t, filepath.Dir(cfg.Dir))
		cmd := exec.Command(binary, args...)
		cmd.Env = loadEnvironment()
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("obsolete/missing selection accepted: %s", out)
		}
		if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
			t.Fatal("invalid CLI wrote files")
		}
	}
	runOwner(t, cfg, "")
}

func TestViewConfigurationErrors(t *testing.T) {
	for _, tc := range []struct {
		emit  string
		types []string
	}{
		{"view", nil}, {"view,clone", []string{"RecordView"}}, {"cbor,view", []string{"RecordView"}}, {"view,equal", []string{"RecordView"}}, {"view", []string{"Missing"}}, {"view", []string{"Record"}},
	} {
		t.Run(tc.emit+strings.Join(tc.types, ","), func(t *testing.T) {
			cfg := viewFixture(t, "view")
			cfg.Emit = tc.emit
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
