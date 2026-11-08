package dtogen

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const overrideLeaf = `
//dtogen:override Clone
//dtogen:override Equal
type Leaf struct { Data []byte ` + "`cbor:\"d\"`" + `; Cache int64 ` + "`cbor:\"-\"`" + ` }
var Clones, Equals int
func (v Leaf) Clone() Leaf { Clones++; if v.Data!=nil { data:=make([]byte,len(v.Data));copy(data,v.Data);v.Data=data }; return v }
func (v Leaf) Equal(w Leaf) bool { Equals++;return len(v.Data)==len(w.Data) }
`

func TestWholeTypeOverrides(t *testing.T) {
	for _, selection := range []string{"clone", "equal", "clone,equal", "cbor,clone,equal"} {
		t.Run(selection, func(t *testing.T) {
			source := overrideLeaf
			switch selection {
			case "clone":
				source = strings.Replace(source, "//dtogen:override Equal\n", "", 1)
			case "equal":
				source = strings.Replace(source, "//dtogen:override Clone\n", "", 1)
			}
			cfg := executableFixture(t, source+`
type Record struct { Value Leaf `+"`cbor:\"v\"`"+`; Pointer *Leaf `+"`cbor:\"p\"`"+`; Slice []Leaf `+"`cbor:\"s\"`"+`; Pointers []*Leaf `+"`cbor:\"ps\"`"+` }
`)
			cfg.Emit = selection
			for _, prior := range []string{"", GeneratedMarker + "\ninvalid Go"} {
				out := filepath.Join(cfg.Dir, cfg.Output)
				if prior != "" {
					writeFixture(t, out, prior)
				}
				if err := Generate(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
			}
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("override generation is not deterministic")
			}
			body := `package owner
import "testing"
func TestDispatch(t *testing.T) {
 x:=Leaf{Data:[]byte{1},Cache:1}; y:=Leaf{Data:[]byte{9},Cache:2}
 v:=Record{Value:x,Pointer:&x,Slice:[]Leaf{x},Pointers:[]*Leaf{&x,nil}}
 w:=Record{Value:y,Pointer:&y,Slice:[]Leaf{y},Pointers:[]*Leaf{&y,nil}}
 _=v;_=w
`
			if strings.Contains(selection, "clone") {
				body += `
 Clones=0;z:=x.Clone();if Clones!=1 {t.Fatal("direct Clone override")};z.Data[0]++;if x.Data[0]!=1{t.Fatal("direct alias")}
 Clones=0;c:=v.Clone();if Clones!=4 {t.Fatal("parent Clone bypassed override",Clones)}
 c.Value.Data[0]++;c.Pointer.Data[0]++;c.Slice[0].Data[0]++;c.Pointers[0].Data[0]++
 if x.Data[0]!=1 {t.Fatal("parent storage escaped")}
`
			}
			if strings.Contains(selection, "equal") {
				body += `
 Equals=0;if !x.Equal(y)||Equals!=1 {t.Fatal("direct Equal override")}
 Equals=0;if !v.Equal(w)||Equals!=4 {t.Fatal("parent Equal bypassed override",Equals)}
 w.Pointer=nil;if v.Equal(w){t.Fatal("pointer absence lost")}
`
			}
			body += "}\n"
			writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), body)
			runOwner(t, cfg, "")
		})
	}
}

func TestOverrideErrorsPreserveArtifacts(t *testing.T) {
	// Only the invalid source and selection vary; reuse the valid baseline.
	baseline := moduleFixture(t, "type Record struct{}")
	baseline.Emit = "clone,equal"
	if err := Generate(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(baseline.Dir, "owner.go")
	source := readFixture(t, path)
	artifact := readFixture(t, filepath.Join(baseline.Dir, baseline.Output))
	for _, tc := range []struct{ name, source, emit, want string }{
		{"missing", "//dtogen:override Clone\ntype Record struct{}", "clone", "requires a handwritten method"},
		{"pointer receiver", "//dtogen:override Clone\ntype Record struct{};func(*Record)Clone()Record{return Record{}}", "clone", "signature"},
		{"wrong clone result", "//dtogen:override Clone\ntype Record struct{};func(Record)Clone()*Record{return nil}", "clone", "signature"},
		{"clone argument", "//dtogen:override Clone\ntype Record struct{};func(v Record)Clone(int)Record{return v}", "clone", "signature"},
		{"equal pointer", "//dtogen:override Equal\ntype Record struct{};func(*Record)Equal(Record)bool{return true}", "equal", "signature"},
		{"equal variadic", "//dtogen:override Equal\ntype Record struct{};func(Record)Equal(...Record)bool{return true}", "equal", "signature"},
		{"equal argument", "//dtogen:override Equal\ntype Record struct{};func(Record)Equal(*Record)bool{return true}", "equal", "signature"},
		{"equal result", "//dtogen:override Equal\ntype Record struct{};func(Record)Equal(Record)int{return 0}", "equal", "signature"},
		{"equal extra result", "//dtogen:override Equal\ntype Record struct{};func(Record)Equal(Record)(bool,error){return true,nil}", "equal", "signature"},
		{"promoted", "type Base struct{};func(Base)Clone()Record{return Record{}}\n//dtogen:override Clone\ntype Record struct{Base}", "clone", "collides with generated Clone"},
		{"duplicate", "//dtogen:override Clone\n//dtogen:override Clone\ntype Record struct{}", "clone", "duplicate"},
		{"stale target", "type Record struct{}\n//dtogen:override Clone\ntype Unused struct{}", "clone", "stale"},
		{"alias", "//dtogen:override Clone\ntype Record = string", "clone", "not an alias"},
		{"misplaced", "type Record struct{}\n//dtogen:override Clone\nfunc f(){}", "clone", "misplaced"},
		{"ambiguous", "//dtogen:override Clone\ntype (Record struct{}; Other struct{})", "clone", "misplaced"},
		{"unknown", "//dtogen:override Copy\ntype Record struct{}", "clone", "only Clone or Equal"},
		{"CBOR", "//dtogen:override CheckCBOR\ntype Record struct{}", "cbor", "only Clone or Equal"},
		{"unselected", "//dtogen:override Clone\ntype Record struct{};func(v Record)Clone()Record{return v}", "equal", "selected -emit"},
		{"unrelated collision", "//dtogen:override Clone\ntype Record struct{};func(v Record)Clone()Record{return v};func(Record)Equal(Record)bool{return true}", "clone,equal", "collides with generated Equal"},
		{"unsupported graph", "//dtogen:override Clone\ntype Record struct{Err error};func(v Record)Clone()Record{return v}", "clone", "unsupported shape"},
		{"broken implementation", "//dtogen:override Clone\ntype Record struct{};func(Record)Clone()Record{return Missing}", "clone", "undefined: Missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseline
			if artifact != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
				t.Fatal("baseline artifact changed before mutation")
			}
			t.Cleanup(func() { writeFixture(t, path, source) })
			writeFixture(t, path, "package owner\n"+tc.source)
			cfg.Emit = tc.emit
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %q", err, tc.want)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("invalid override changed artifacts")
			}
		})
	}
}

func TestOverrideScalarContainers(t *testing.T) {
	cfg := moduleFixture(t, `
//dtogen:override Clone
//dtogen:override Equal
type Byte uint8
var clones, equals int
func(v Byte)Clone()Byte{clones++;return v}
func(v Byte)Equal(w Byte)bool{equals++;return true}
type Record struct { Bytes []Byte; Array [2]Byte; Map map[string]Byte }
`)
	cfg.Emit = "clone,equal"
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import "testing"
func TestContainers(t *testing.T){
 v:=Record{Bytes:[]Byte{1,2},Array:[2]Byte{3,4},Map:map[string]Byte{"x":5}}
 w:=Record{Bytes:[]Byte{9,8},Array:[2]Byte{7,6},Map:map[string]Byte{"x":0}}
 clones=0;c:=v.Clone();if clones!=5{t.Fatal("scalar clone bypass",clones)}
 c.Bytes[0]++;c.Map["x"]++;if v.Bytes[0]!=1||v.Map["x"]!=5{t.Fatal("storage escaped")}
 equals=0;if !v.Equal(w)||equals!=5{t.Fatal("scalar equal bypass",equals)}
}
`)
	runOwner(t, cfg, "")
}

func TestImportedNestedOverrides(t *testing.T) {
	cfg := moduleFixture(t, `import "example.invalid/payloads/middle"
type Record struct { Value middle.Value; Pointer *middle.Value; Slice []middle.Value }
var _=Record{}.Clone().Equal(Record{})
`)
	cfg.Emit = "clone,equal"
	module := filepath.Dir(cfg.Dir)
	root := filepath.Dir(module)
	writeFixture(t, filepath.Join(module, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\nrequire example.invalid/payloads v0.0.0\nreplace example.invalid/payloads => ../payloads\n")
	writeFixture(t, filepath.Join(root, "payloads/go.mod"), "module example.invalid/payloads\n\ngo 1.27.3\n")
	leafDir := filepath.Join(root, "payloads/leaf")
	middleDir := filepath.Join(root, "payloads/middle")
	leafSource := "//go:generate go -C ../.. run ./internal/dtogen/cmd/dtogen -dir payloads/leaf -types Leaf -emit clone,equal -out zz_data_generated.go\npackage leaf\n" + overrideLeaf
	writeFixture(t, filepath.Join(leafDir, "leaf.go"), leafSource+`func init(){panic("must not execute owner")}`)
	writeFixture(t, filepath.Join(middleDir, "middle.go"), `//go:generate go -C ../.. run ./internal/dtogen/cmd/dtogen -dir payloads/middle -types Value -emit clone,equal -out zz_data_generated.go
package middle
import "example.invalid/payloads/leaf"
type Value struct { Value leaf.Leaf; Pointer *leaf.Leaf; Slice []leaf.Leaf }
var _=Value{}.Clone().Equal(Value{})
`)
	outputs := []string{filepath.Join(leafDir, "zz_data_generated.go"), filepath.Join(middleDir, "zz_data_generated.go"), filepath.Join(cfg.Dir, cfg.Output)}
	for _, prior := range []string{"", GeneratedMarker + "\ninvalid Go"} {
		for _, out := range outputs {
			if prior != "" {
				writeFixture(t, out, prior)
			}
		}
		if err := Generate(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	// A bad prerequisite override must preserve every already-rendered artifact.
	writeFixture(t, filepath.Join(leafDir, "leaf.go"), strings.Replace(leafSource, "Clone() Leaf", "Clone(_ int) Leaf", 1))
	before := repositoryState(t, root)
	if err := Generate(context.Background(), cfg); err == nil {
		t.Fatal("invalid imported override accepted")
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("invalid prerequisite replaced artifacts")
	}
	writeFixture(t, filepath.Join(leafDir, "leaf.go"), leafSource)
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("testing";"example.invalid/payloads/leaf";"example.invalid/payloads/middle")
func TestNestedDispatch(t *testing.T){
 x:=leaf.Leaf{Data:[]byte{1}};y:=leaf.Leaf{Data:[]byte{9},Cache:5}
 a:=middle.Value{Value:x,Pointer:&x,Slice:[]leaf.Leaf{x}}
 b:=middle.Value{Value:y,Pointer:&y,Slice:[]leaf.Leaf{y}}
 leaf.Clones=0;_=a.Clone();if leaf.Clones!=3{t.Fatal("middle clone",leaf.Clones)}
 leaf.Equals=0;if !a.Equal(b)||leaf.Equals!=3{t.Fatal("middle equal",leaf.Equals)}
 v:=Record{Value:a,Pointer:&a,Slice:[]middle.Value{a}};w:=Record{Value:b,Pointer:&b,Slice:[]middle.Value{b}}
 leaf.Clones=0;c:=v.Clone();if leaf.Clones!=9{t.Fatal("nested clone",leaf.Clones)}
 c.Value.Value.Data[0]++;c.Pointer.Pointer.Data[0]++;c.Slice[0].Slice[0].Data[0]++
 if x.Data[0]!=1{t.Fatal("nested storage escaped")}
 leaf.Equals=0;if !v.Equal(w)||leaf.Equals!=9{t.Fatal("nested equal",leaf.Equals)}
}
`)
	runOwner(t, cfg, "")
	data, err := os.ReadFile(outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "func (v Leaf) Clone") || strings.Contains(string(data), "func (v Leaf) Equal") || strings.Contains(string(data), ") clone0(") || strings.Contains(string(data), ") equal0(") {
		t.Fatal("override received a generated default method/body")
	}
}
