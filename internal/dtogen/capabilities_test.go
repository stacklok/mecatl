package dtogen

import (
	"context"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestCapabilitySelection(t *testing.T) {
	for _, selection := range []string{"cbor", "clone", "equal", "cbor,clone", "cbor,equal", "clone,equal", "equal,cbor,clone"} {
		t.Run(selection, func(t *testing.T) {
			cfg := executableFixture(t, "type Record struct { Data []byte `cbor:\"d\"`; Cache []byte `cbor:\"-\"` }")
			cfg.Emit = selection
			if err := Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			pkg, err := loadOne(&packages.Config{
				Dir: cfg.Dir, Env: loadEnvironment(),
				Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
			})
			if err != nil {
				t.Fatal(err)
			}
			methods := types.NewMethodSet(types.NewPointer(pkg.Types.Scope().Lookup("Record").Type()))
			want, _ := parseEmit(selection)
			for _, name := range []string{"CheckCBOR", "AppendCBOR", "ReadCBOR", "Clone", "Equal"} {
				if got := methods.Lookup(pkg.Types, name) != nil; got != want.method(name) {
					t.Errorf("%s present=%v", name, got)
				}
			}
			data, err := os.ReadFile(filepath.Join(cfg.Dir, cfg.Output))
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []struct {
				name     string
				selected bool
			}{{"clone", want.clone}, {"equal", want.equal}, {"check", want.cbor}, {"append", want.cbor}, {"read", want.cbor}} {
				if strings.Contains(string(data), ") "+operation.name+"0(") != operation.selected {
					t.Errorf("unexpected helper selection: %s", operation.name)
				}
			}
			runOwner(t, cfg, "")
		})
	}
}

func TestNonCBORWholeState(t *testing.T) {
	cfg := moduleFixture(t, `
// No mapping tags, fixed-width integer policy, serialization policy or codec dependency.
type Child struct { data []byte }
type Record struct {
 Child
 Count int
 Ratio float64
 Number complex128
 Array [2][]byte
 Chain **Child
 Values map[int][]byte
 Ignored []byte `+"`cbor:\"-\" cborlimit:invalid`"+`
 Clone string
}
func (Record) MarshalCBOR()([]byte,error){panic("must not run")}
`)
	cfg.Emit = "equal"
	// An unselected method/field name is ordinary state, not a collision.
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Dir, "owner.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, strings.Replace(string(data), " Clone string", " Label string", 1))
	cfg.Emit = "clone,equal"
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import "testing"
func TestWholeState(t *testing.T) {
 child:=&Child{data:[]byte{1}}
 v:=Record{Child:Child{data:[]byte{2}},Count:3,Ratio:1.5,Number:2i,Array:[2][]byte{{4},{}},Chain:&child,Values:map[int][]byte{5:{6}},Ignored:[]byte{7}}
 w:=v.Clone()
 if !v.Equal(w) || w.Array[1]==nil {t.Fatal("clone changed state/presence")}
 w.data[0]++;w.Array[0][0]++;(**w.Chain).data[0]++;w.Values[5][0]++;w.Ignored[0]++
 if v.data[0]!=2 || v.Array[0][0]!=4 || (**v.Chain).data[0]!=1 || v.Values[5][0]!=6 || v.Ignored[0]!=7 || v.Equal(w) {t.Fatal("mutable state escaped")}
 if !(Record{}).Equal(Record{Values:map[int][]byte{},Ignored:[]byte{}}) {t.Fatal("nil/empty equality")}
}
`)
	runOwner(t, cfg, "")
	before := repositoryState(t, filepath.Dir(cfg.Dir))
	cfg.Emit = "cbor,clone,equal"
	if err := Generate(context.Background(), cfg); err == nil {
		t.Fatal("CBOR accepted non-CBOR shape")
	}
	if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
		t.Fatal("failed CBOR selection changed output")
	}
}

func TestCapabilityErrors(t *testing.T) {
	for _, selection := range []string{"", "view", "clone,clone", "clone,", "cbor, equal"} {
		t.Run(selection, func(t *testing.T) {
			cfg := moduleFixture(t, "type Record struct{}")
			cfg.Emit = selection
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err == nil {
				t.Fatal("invalid selection accepted")
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("invalid selection wrote output")
			}
		})
	}
}

func TestCBOROnlyExcludedNonCBOROwner(t *testing.T) {
	cfg := executableFixture(t, `import "github.com/stacklok/mecatl/engine/cache"
type Record struct { Data []byte `+"`cbor:\"d\"`"+`; Cache cache.State `+"`cbor:\"-\"`"+` }
`)
	cfg.Emit = "cbor"
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "cache/value.go"), `//go:generate go -C ../.. run ./internal/dtogen/cmd/dtogen -dir engine/cache -types State -emit clone,equal -out zz_data_generated.go
package cache
type State struct { private []byte }
`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	runOwner(t, cfg, "")
}

func TestImportedOwnerCapabilityRequirements(t *testing.T) {
	for _, tc := range []struct{ consumer, owner, missing string }{
		{"clone", "equal", "Clone"}, {"equal", "clone", "Equal"},
	} {
		t.Run(tc.consumer, func(t *testing.T) {
			cfg := moduleFixture(t, `import "github.com/stacklok/mecatl/engine/values";type Record struct { Value values.Value }`)
			cfg.Emit = tc.consumer
			module := filepath.Dir(cfg.Dir)
			writeFixture(t, filepath.Join(module, "values/value.go"), "//go:generate go -C ../.. run ./internal/dtogen/cmd/dtogen -dir engine/values -types Value -emit "+tc.owner+" -out zz_data_generated.go\npackage values\ntype Value struct { private []byte }\n")
			before := repositoryState(t, module)
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "does not emit "+tc.missing) {
				t.Fatal("imported capability was inferred instead of declared", err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, module)) {
				t.Fatal("capability failure installed a prerequisite")
			}
		})
	}
}

func TestNonCBORExternalOwnerBootstrap(t *testing.T) {
	cfg := moduleFixture(t, `import "example.invalid/payloads/data"
type Record struct { Value data.Value; Pointer *data.Value; Slice []data.Value }
var _ = Record{}.Clone().Equal(Record{})
`)
	cfg.Emit = "clone,equal"
	module := filepath.Dir(cfg.Dir)
	root := filepath.Dir(module)
	writeFixture(t, filepath.Join(module, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\n\nrequire example.invalid/payloads v0.0.0\nreplace example.invalid/payloads => ../payloads\n")
	writeFixture(t, filepath.Join(root, "payloads/go.mod"), "module example.invalid/payloads\n\ngo 1.27.3\n")
	owner := filepath.Join(root, "payloads/data")
	writeFixture(t, filepath.Join(owner, "value.go"), `//go:generate go -C ../.. run ./internal/dtogen/cmd/dtogen -dir payloads/data -types Value -emit clone,equal -out zz_data_generated.go
package data
type Value struct { private []byte }
func New()Value{return Value{private:[]byte{1}}}
func(v Value)Change(){v.private[0]++}
func(v Value)Byte()byte{return v.private[0]}
var _ = Value{}.Clone().Equal(Value{})
func init(){panic("load must not execute owner")}
`)
	outputs := []string{filepath.Join(owner, "zz_data_generated.go"), filepath.Join(cfg.Dir, cfg.Output)}
	for _, prior := range []string{"", GeneratedMarker + "\nnot Go syntax"} {
		for _, path := range outputs {
			if prior == "" {
				continue
			}
			writeFixture(t, path, prior)
		}
		if err := Generate(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	before := repositoryState(t, root)
	// Requiring an unavailable capability must not invent imported method stubs.
	path := filepath.Join(owner, "value.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, strings.Replace(string(source), "-emit clone,equal", "-emit equal", 1))
	if err := Generate(context.Background(), cfg); err == nil {
		t.Fatal("unavailable owner clone accepted")
	}
	for _, output := range outputs {
		data, err := os.ReadFile(output)
		if err != nil || string(data) != before[output].data {
			t.Fatal("owner failure changed artifacts", err)
		}
	}
	writeFixture(t, path, strings.Replace(string(source), `func init(){panic("load must not execute owner")}`, "", 1))
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("testing";"example.invalid/payloads/data")
func TestComposition(t *testing.T){x:=data.New();v:=Record{Value:x,Pointer:&x,Slice:[]data.Value{x}};w:=v.Clone();if !v.Equal(w){t.Fatal("equality")};w.Value.Change();w.Pointer.Change();w.Slice[0].Change();if v.Value.Byte()!=1||v.Pointer.Byte()!=1||v.Slice[0].Byte()!=1||v.Equal(w){t.Fatal("owner storage escaped")}}
`)
	runOwner(t, cfg, "")
}
