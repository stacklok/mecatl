package dtogen

import (
	"context"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// primitiveFixture copies only handwritten runtime sources into an isolated module.
// No package initialization is executed, and no workspace or published pin is used.
func primitiveFixture(t *testing.T, source string) LoadConfig {
	t.Helper()
	cfg := executableFixture(t, source)
	for _, pkg := range []string{"value", "views"} {
		entries, err := os.ReadDir("../../engine/kernel/" + pkg)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join("../../engine/kernel", pkg, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "kernel", pkg, entry.Name()), string(data))
		}
	}
	return cfg
}

func TestPrimitiveOwnerCompositionBootstrap(t *testing.T) {
	cfg := primitiveFixture(t, `
import "github.com/stacklok/mecatl/engine/kernel/value"
type Record struct { Value value.Value `+"`cbor:\"v\"`"+`; Map value.Map `+"`cbor:\"m\" cborlimit:\"items=2\"`"+` }
type Parent struct { Record Record `+"`cbor:\"r\"`"+` }
func init(){panic("generator executed consumer code")}
var _ = Record{}.Clone
var _ = Parent{}.Equal
`)
	cfg.Types = []string{"Parent"}
	trap := filepath.Join(filepath.Dir(cfg.Dir), "kernel/value/trap.go")
	writeFixture(t, trap, "package value\nfunc init(){panic(\"generator executed primitive owner code\")}\n")
	for _, prior := range []string{"", GeneratedMarker + "\nnot Go", GeneratedMarker + "\npackage owner\nfunc(Record)Clone()int{return 0}"} {
		if prior != "" {
			writeFixture(t, filepath.Join(cfg.Dir, cfg.Output), prior)
		}
		if err := Generate(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		before := readFixture(t, filepath.Join(cfg.Dir, cfg.Output))
		if strings.Count(before, "propagates value.ErrDepthExceeded") != 4 {
			t.Fatal("direct/transitive parent panic contract missing")
		}
		if strings.Contains(before, "kind ") || strings.Contains(before, "recursive") {
			t.Fatal("expanded private primitive representation")
		}
		if err := Generate(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if before != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
			t.Fatal("nondeterministic primitive output")
		}
	}
	// Signature and provenance failures are validation errors, not interface opt-ins.
	loaded, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	owner := loaded.Package.Imports[valueImportPath]
	saved := owner.Module.Path
	owner.Module.Path = "example.invalid/impostor"
	if _, err := render(loaded); err == nil || !strings.Contains(err.Error(), "provenance mismatch") {
		t.Fatal("impostor module", err)
	}
	owner.Module.Path = saved
	// Primitive owners are handwritten and cannot become generation targets.
	primitive := LoadConfig{Dir: filepath.Join(filepath.Dir(cfg.Dir), "kernel/value"), Types: []string{"Value"}, Output: cfg.Output, Emit: cfg.Emit}
	before := repositoryState(t, filepath.Dir(cfg.Dir))
	if err := Generate(context.Background(), primitive); err == nil {
		t.Fatal("generated colliding primitive methods")
	}
	if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
		t.Fatal("primitive target failure wrote output")
	}
}

func TestPrimitiveOwnerInvalidSignaturesNoWrite(t *testing.T) {
	for _, test := range []struct{ name, old, new string }{
		{"CheckCBOR", "CheckCBOR(b *codec.Budget, depth, fieldMax int)", "CheckCBOR(b *codec.Budget, depth, fieldMax int64)"},
		{"AppendCBOR", "AppendCBOR(dst []byte) []byte", "AppendCBOR(dst []byte, extra ...byte) []byte"},
		{"ReadCBOR", "ReadCBOR(r *codec.Reader, depth, fieldMax int)", "ReadCBOR(r *codec.Reader, depth, fieldMax int64)"},
		{"Clone", "Clone() Value", "Clone(extra ...int) Value"},
		{"Equal", "Equal(other Value) bool", "Equal(other Value, extra ...int) bool"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := primitiveFixture(t, `import "github.com/stacklok/mecatl/engine/kernel/value"; type Record struct{ V value.Value `+"`cbor:\"v\"`"+` }`)
			if err := Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			// Modify signatures that remain valid Go, so the exact owner check, rather
			// than a syntax error, is responsible for rejecting the operation contract.
			path := filepath.Join(filepath.Dir(cfg.Dir), "kernel/value/value.go")
			data := readFixture(t, path)
			if !strings.Contains(data, test.old) {
				t.Fatalf("missing source signature %s", test.old)
			}
			modified := strings.Replace(data, test.old, test.new, 1)
			// Depth arguments still have int bodies; convert at the narrow delegated call.
			if test.name == "CheckCBOR" {
				modified = strings.Replace(modified, "v.check(b, depth, fieldMax, 0)", "v.check(b, int(depth), int(fieldMax), 0)", 1)
			}
			if test.name == "ReadCBOR" {
				modified = strings.Replace(modified, "read(r, depth, fieldMax, 0)", "read(r, int(depth), int(fieldMax), 0)", 1)
			}
			if test.name == "CheckCBOR" || test.name == "AppendCBOR" {
				modified = strings.Replace(modified, "codec.Check(v, limits)", "codec.Check(Map{}, limits)", 1)
			}
			writeFixture(t, path, modified)
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "invalid "+test.name+" signature") {
				t.Fatal("invalid primitive method accepted or wrong diagnostic", err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("validation failure wrote artifacts")
			}
		})
	}
}

// TestPrimitiveOwnerLoadedContractRejection mutates the typed load result rather
// than rewriting method bodies to keep malformed signatures valid Go source.
func TestPrimitiveOwnerLoadedContractRejection(t *testing.T) {
	for _, name := range []string{"Value", "Map"} {
		t.Run(name, func(t *testing.T) {
			cfg := primitiveFixture(t, `import "github.com/stacklok/mecatl/engine/kernel/value"; type Record struct{ V value.`+name+" `cbor:\"v\"` }")
			if err := Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			owner := loaded.Package.Imports[valueImportPath]
			original := owner.Types.Scope().Lookup(name).Type().(*types.Named)
			root := loaded.Roots[0]
			originalFields := root.Underlying().(*types.Struct)
			files := owner.CompiledGoFiles
			foreignPos := owner.Fset.AddFile(filepath.Join(cfg.Dir, "impostor.go"), -1, 1).Pos(0)
			for _, tc := range []struct{ name, method, change, want string }{
				{"valid contract control", "", "", ""},
				{"source file", "", "source", "primitive owner source mismatch"},
				{"method source", "Clone", "provenance", "primitive method provenance mismatch"},
				{"check parameters", "CheckCBOR", "parameter", "invalid CheckCBOR signature"},
				{"append parameters", "AppendCBOR", "parameter", "invalid AppendCBOR signature"},
				{"read parameters", "ReadCBOR", "parameter", "invalid ReadCBOR signature"},
				{"clone parameters", "Clone", "parameter", "invalid Clone signature"},
				{"equal parameters", "Equal", "parameter", "invalid Equal signature"},
				{"missing method", "Equal", "missing", "missing Equal"},
				{"value receiver", "Clone", "receiver", "invalid Clone signature"},
				{"pointer receiver", "ReadCBOR", "receiver", "invalid ReadCBOR signature"},
				{"result", "Clone", "result", "invalid Clone signature"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					mutated := types.NewNamed(types.NewTypeName(original.Obj().Pos(), owner.Types, name, nil), original.Underlying(), nil)
					for i := 0; i < original.NumMethods(); i++ {
						fn := original.Method(i)
						if fn.Name() == tc.method && tc.change == "missing" {
							continue
						}
						sig := fn.Type().(*types.Signature)
						remap := func(typ types.Type) types.Type {
							if types.Identical(typ, original) {
								return mutated
							}
							if types.Identical(typ, types.NewPointer(original)) {
								return types.NewPointer(mutated)
							}
							return typ
						}
						recv := remap(sig.Recv().Type())
						params := make([]*types.Var, sig.Params().Len())
						for j := range params {
							params[j] = types.NewVar(token.NoPos, owner.Types, "", remap(sig.Params().At(j).Type()))
						}
						results := make([]*types.Var, sig.Results().Len())
						for j := range results {
							results[j] = types.NewVar(token.NoPos, owner.Types, "", remap(sig.Results().At(j).Type()))
						}
						pos := fn.Pos()
						if fn.Name() == tc.method {
							switch tc.change {
							case "provenance":
								pos = foreignPos
							case "parameter":
								params = append(params, types.NewVar(token.NoPos, owner.Types, "", types.Typ[types.String]))
							case "receiver":
								if _, pointer := recv.(*types.Pointer); pointer {
									recv = mutated
								} else {
									recv = types.NewPointer(mutated)
								}
							case "result":
								results[0] = types.NewVar(token.NoPos, owner.Types, "", types.Typ[types.String])
							}
						}
						methodSig := types.NewSignatureType(types.NewVar(token.NoPos, owner.Types, "", recv), nil, nil, types.NewTuple(params...), types.NewTuple(results...), sig.Variadic())
						mutated.AddMethod(types.NewFunc(pos, owner.Types, fn.Name(), methodSig))
					}
					field := originalFields.Field(0)
					root.SetUnderlying(types.NewStruct([]*types.Var{types.NewField(field.Pos(), field.Pkg(), field.Name(), mutated, false)}, []string{originalFields.Tag(0)}))
					if tc.change == "source" {
						owner.CompiledGoFiles = []string{filepath.Join(cfg.Dir, "impostor.go")}
					}
					defer func() {
						root.SetUnderlying(originalFields)
						owner.CompiledGoFiles = files
					}()
					before := repositoryState(t, filepath.Dir(cfg.Dir))
					_, err := render(loaded)
					if tc.want == "" {
						if err != nil {
							t.Fatalf("valid contract control: %v", err)
						}
					} else if err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("got %v, want %s", err, tc.want)
					}
					if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
						t.Fatal("contract rejection wrote artifacts")
					}
				})
			}
		})
	}
}

func TestPrimitiveLookalikesAndRecursionRejected(t *testing.T) {
	for _, test := range []struct{ name, source, want string }{
		{"same methods different owner", `import "github.com/stacklok/mecatl/engine/other"; type Record struct { V other.Value ` + "`cbor:\"v\"`" + ` }`, "requires an owner"},
		{"unknown name in exact package", `import "github.com/stacklok/mecatl/engine/kernel/value"; type Record struct { V value.Other ` + "`cbor:\"v\"`" + ` }`, "requires an owner"},
		{"local recursion", `type Record struct { V *Record ` + "`cbor:\"v\"`" + ` }`, "recursive type graph"},
		{"generic recursion", `type Link[T any] struct { Next *Link[T] };type Record struct { V Link[int] ` + "`cbor:\"v\"`" + ` }`, "generic types unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := primitiveFixture(t, test.source)
			// An arbitrary custom implementation must never acquire the primitive seam.
			data := `package other
import "github.com/stacklok/mecatl/engine/kernel/codec"
type Value struct{}
func(Value)CheckCBOR(*codec.Budget,int,int)error{return nil}
func(Value)AppendCBOR(b []byte)[]byte{return b}
func(*Value)ReadCBOR(*codec.Reader,int,int)error{return nil}
func(v Value)Clone()Value{return v}
func(Value)Equal(Value)bool{return true}
`
			writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "other/value.go"), data)
			writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "kernel/value/other.go"), "package value\ntype Other struct{}\n")
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatal("unexpected result", err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("failed generation wrote output")
			}
		})
	}
}
