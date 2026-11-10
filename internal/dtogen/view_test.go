package dtogen

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const viewFixtureSource = `
import "github.com/stacklok/mecatl/engine/kernel/views"
// Record retains provider data.
// Provisional: revisit tags after use.
type Record struct {
 // Title is provider-owned text.
 Title string ` + "`cbor:\"t\"`" + `
 Count *uint64 ` + "`cbor:\"n,omitempty\"`" + `
 Data []byte ` + "`cbor:\"d\"`" + `
 InlineData []byte ` + "`cbor:\"i\" view:\"manual\"`" + `
 Source string ` + "`cbor:\"s\" view:\"-\"`" + `
 RemoteURI string ` + "`cbor:\"u\" view:\"manual\"`" + `
 Child Child ` + "`cbor:\"c\"`" + `
 Maybe *Child ` + "`cbor:\"m,omitempty\"`" + `
 Children []Child ` + "`cbor:\"cs\"`" + `
 Hidden map[string]string ` + "`cbor:\"-\" view:\"-\"`" + `
}
type Child struct { Label string ` + "`cbor:\"l\"`" + `; Blob []byte ` + "`cbor:\"b\"`" + ` }
type RecordView struct { value *Record; scope string }
type ChildView struct { value *Child }
// InlineData gives tag-selected presence, even for empty inline data.
func(v RecordView) InlineData()(views.Bytes,bool){if v.value==nil||v.value.Source!="inline"{return views.Bytes{},false};return views.BorrowBytes(v.value.InlineData),true}
// RemoteURI gives tag-selected string presence.
func(v RecordView) RemoteURI()(string,bool){if v.value==nil||v.value.Source!="remote"{return "",false};return v.value.RemoteURI,true}
func(v RecordView) Scope()string{return v.scope}
var _ = RecordView{}.Title()
var _ = ChildView{}.Label()
var _ = dtogenViewChildView
`

func viewFixture(t *testing.T, emit string) LoadConfig {
	t.Helper()
	cfg := executableFixture(t, viewFixtureSource)
	cfg.Emit = emit
	cfg.Types = []string{"RecordView", "ChildView"}
	for _, name := range []string{"bytes.go", "slice.go"} {
		data, err := os.ReadFile("../../engine/kernel/views/" + name)
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "kernel/views", name), string(data))
	}
	return cfg
}

func TestViewBootstrap(t *testing.T) {
	cfg := viewFixture(t, "view")
	// A single literal owner declaration feeds direct and repository generation.
	writeFixture(t, filepath.Join(cfg.Dir, "registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir engine/owner -types RecordView,ChildView -emit view -out "+cfg.Output+"\npackage owner\n")
	for _, prior := range []string{"", GeneratedMarker + "\nnot Go", GeneratedMarker + "\npackage owner\nfunc(RecordView) Title() int{return 0}"} {
		if prior != "" {
			writeFixture(t, filepath.Join(cfg.Dir, cfg.Output), prior)
		}
		state := repositoryState(t, filepath.Dir(cfg.Dir))
		if err := GenerateRepository(context.Background(), filepath.Dir(filepath.Dir(cfg.Dir)), true); err == nil {
			t.Fatal("missing/broken/stale view output passed check")
		}
		if !reflect.DeepEqual(state, repositoryState(t, filepath.Dir(cfg.Dir))) {
			t.Fatal("check wrote bootstrap artifacts")
		}
		if err := Generate(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		before := readFixture(t, filepath.Join(cfg.Dir, cfg.Output))
		if !strings.Contains(before, "Provisional: revisit tags") || !strings.Contains(before, "Title is provider-owned text") || !strings.Contains(before, "O(1)") {
			t.Fatal("source/accessor docs lost")
		}
		if strings.Contains(before, "func (v RecordView) InlineData") {
			t.Fatal("manual body replaced")
		}
		for _, name := range []string{"CheckCBOR", "AppendCBOR", "ReadCBOR", "Clone", "Equal"} {
			if strings.Contains(before, ") "+name+"(") {
				t.Errorf("view-only emitted %s", name)
			}
		}
		if err := Generate(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if before != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
			t.Fatal("nondeterministic output")
		}
	}
	root := filepath.Dir(filepath.Dir(cfg.Dir))
	if err := GenerateRepository(context.Background(), root, true); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), viewBehaviorTest)
	runOwner(t, cfg, "")
}
func readFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const viewBehaviorTest = `package owner
import "testing"
func TestViews(t *testing.T){
 var z RecordView
 if z.Title()!=""||z.Data().Len()!=0||z.Child().Label()!=""||z.Children().Len()!=0{t.Fatal("zero")}
 if _,ok:=z.Count();ok{t.Fatal("zero optional scalar")};if _,ok:=z.Maybe();ok{t.Fatal("zero child")};if _,ok:=z.InlineData();ok{t.Fatal("zero inline")}
 n:=uint64(0);r:=Record{Title:"hi",Count:&n,Data:[]byte{},Source:"inline",InlineData:[]byte{},Child:Child{Label:"one",Blob:[]byte{1}},Maybe:&Child{},Children:[]Child{{Label:"two",Blob:[]byte{2}},{Label:"three"}}}
 v:=RecordView{value:&r,scope:"scope"}
 if n,ok:=v.Count();!ok||n!=0{t.Fatal("present zero")};if child,ok:=v.Maybe();!ok||child.Label()!=""{t.Fatal("present empty child")}
 if b,ok:=v.InlineData();!ok||b.Copy()==nil{t.Fatal("present empty inline")};if _,ok:=v.RemoteURI();ok{t.Fatal("tag presence")}
 if v.Data().Copy()==nil||z.Data().Copy()!=nil{t.Fatal("bytes presence")}
 if v.Scope()!="scope"||v.Child().Label()!="one"||v.Children().At(0).Label()!="two"{t.Fatal("navigation")}
 out:=v.Child().Blob().Copy();out[0]++;if v.Child().Blob().At(0)!=1{t.Fatal("mutable export")}
 sum:=0;for i,c:=range v.Children().All(){sum+=i+len(c.Label())};if sum!=9{t.Fatal("iteration",sum)}
 if a:=testing.AllocsPerRun(100,func(){
  got:=len(v.Title())+len(v.Scope())+v.Data().Len()
  count,ok:=v.Count();if !ok{panic("count")};got+=int(count)
  child:=v.Child();got+=len(child.Label())+int(child.Blob().At(0))
  maybe,ok:=v.Maybe();if !ok{panic("child")};got+=len(maybe.Label())+maybe.Blob().Len()
  inline,ok:=v.InlineData();if !ok{panic("inline")};got+=inline.Len();if _,ok:=v.RemoteURI();ok{panic("remote")}
  if v.Children().Len()!=2||v.Children().At(0).Blob().At(0)!=2{panic("indexed collection")}
  for i,c:=range v.Children().All(){got+=i+len(c.Label());for _,b:=range c.Blob().All(){got+=int(b)}}
  if got!=22{panic("complete traversal")}
 });a!=0{t.Fatal("allocations",a)}
 r.Source="remote";if uri,ok:=v.RemoteURI();!ok||uri!=""{t.Fatal("present empty tagged string")};if _,ok:=v.InlineData();ok{t.Fatal("borrowed tag change")}
}
`

func TestViewOptionalContainersAndImportReuse(t *testing.T) {
	cfg := viewFixture(t, "view")
	writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), `package owner
import "github.com/stacklok/mecatl/engine/kernel/views"
type Label string
// Record borrows provider values. Provisional: revisit the provider vocabulary.
type Record struct {
 Active bool
 Ratio float64
 // Token preserves provider spelling.
 Token Label
 Bytes *[]byte
 Children *[]Child
 Existing views.Bytes `+"`view:\"-\"`"+`
}
type Child struct { Label Label }
type RecordView struct { value *Record }
type ChildView struct { value *Child }
`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "generated.go", readFixture(t, filepath.Join(cfg.Dir, cfg.Output)), parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Token" {
			found = true
			if fn.Doc == nil || !strings.Contains(fn.Doc.Text(), "Provisional:") || !strings.Contains(fn.Doc.Text(), "Token preserves provider spelling") || !strings.Contains(fn.Doc.Text(), "O(1)") {
				t.Fatal("public accessor lost source/accessor documentation")
			}
		}
	}
	if !found {
		t.Fatal("missing public accessor")
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import "testing"
func TestOptionalContainers(t *testing.T){
 var z RecordView
 if z.Active()||z.Ratio()!=0||z.Token()!=""{t.Fatal("zero scalars")};if _,ok:=z.Bytes();ok{t.Fatal("zero bytes")};if _,ok:=z.Children();ok{t.Fatal("zero collection")}
 b:=[]byte{};cs:=[]Child{{Label:"child"}};r:=Record{Active:true,Ratio:1.5,Token:"token",Bytes:&b,Children:&cs};v:=dtogenViewRecordView(&r)
 if !v.Active()||v.Ratio()!=1.5||v.Token()!="token"{t.Fatal("named scalars")}
 if b,ok:=v.Bytes();!ok||b.Copy()==nil{t.Fatal("present empty bytes")};if cs,ok:=v.Children();!ok||cs.At(0).Label()!="child"{t.Fatal("present children")}
 var nilBytes []byte;r.Bytes=&nilBytes;if b,ok:=v.Bytes();!ok||b.Copy()!=nil{t.Fatal("present nil bytes")}
 if n:=testing.AllocsPerRun(100,func(){cs,ok:=v.Children();if !ok||cs.At(0).Label()!="child"||!v.Active()||v.Ratio()!=1.5||v.Token()!="token"{panic("traversal")}});n!=0{t.Fatal("allocations",n)}
}
`)
	runOwner(t, cfg, "")
}

func TestViewShapeNameability(t *testing.T) {
	local := types.NewPackage("example.invalid/view", "view")
	external := types.NewPackage("example.invalid/backing", "backing")
	private := types.NewNamed(types.NewTypeName(token.NoPos, external, "private", nil), types.NewStruct(nil, nil), nil)
	privateFields := types.NewStruct([]*types.Var{types.NewField(token.NoPos, external, "private", types.Typ[types.String], false)}, nil)
	public := types.NewNamed(types.NewTypeName(token.NoPos, external, "Public", nil), privateFields, nil)
	alias := types.NewAlias(types.NewTypeName(token.NoPos, external, "PublicAlias", nil), private)
	noargs := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	privateMethods := types.NewInterfaceType([]*types.Func{types.NewFunc(token.NoPos, external, "sealed", noargs)}, nil).Complete()
	hiddenResult := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, external, "", private)), false)
	for _, tc := range []struct {
		name string
		typ  types.Type
		pkg  *types.Package
		want bool
	}{
		{"external private field", privateFields, local, false},
		{"owner private field", privateFields, external, true},
		{"named public type", public, local, true},
		{"public alias of private type", alias, local, true},
		{"private named type", private, local, false},
		{"owner named type", private, external, true},
		{"external private interface method", privateMethods, local, false},
		{"owner private interface method", privateMethods, external, true},
		{"function signature", hiddenResult, local, false},
		{"channel", types.NewChan(types.SendRecv, private), local, false},
		{"map", types.NewMap(types.Typ[types.String], private), local, false},
		{"array", types.NewArray(public, 2), local, true},
		{"pointer slice", types.NewPointer(types.NewSlice(public)), local, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := viewShapeNameable(tc.typ, tc.pkg); got != tc.want {
				t.Fatalf("nameable=%v want %v", got, tc.want)
			}
		})
	}
}

func TestViewInvalidPolicyNoReplacement(t *testing.T) {
	cfg := viewFixture(t, "view")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to, want string }{
		{"unknown", `view:"-"`, `view:"unknown"`, "unknown view tag"},
		{"empty", `view:"-"`, `view:""`, "unknown view tag"},
		{"contradictory", `view:"-"`, `view:"-,manual"`, "unknown view tag"},
		{"duplicate", `view:"-"`, `view:"-" view:"manual"`, "duplicate view tag"},
		{"malformed", `view:"-"`, `view:manual`, "malformed struct tag"},
		{"pointer receiver", "func(v RecordView) InlineData", "func(v *RecordView) InlineData", "value receiver"},
		{"getter parameters", "InlineData()(views.Bytes,bool)", "InlineData(n int)(views.Bytes,bool)", "no parameters"},
		{"missing manual", "func(v RecordView) InlineData", "func(v RecordView) Absent", "missing directly declared"},
		{"manual formerly generated", `cbor:"t"`, `cbor:"t" view:"manual"`, "missing directly declared"},
		{"unsafe pointer", "type Record struct {", "import \"unsafe\"\ntype Record struct { Dangerous unsafe.Pointer", "unsupported scalar"},
		{"collision", `view:"manual"`, `json:"inline"`, "collision"},
		{"unsupported", `Hidden map[string]string ` + "`cbor:\"-\" view:\"-\"`", "Hidden map[string]Child", "unsupported map element"},
		{"context required", "type ChildView struct { value *Child }", "type ChildView struct { value *Child; scope string }", "no context-free"},
		{"context optional", "Child Child", "Child string", "no context-free"},
		{"import collision", "type ChildView struct", "type dtogenViews struct{};type ChildView struct", "collides"},
		{"constructor collision", "type ChildView struct", "var dtogenViewChildView int;type ChildView struct", "collides"},
		{"backing", "value *Record", "data *Record", "private value *DTO"},
		{"public context", "scope string", "ScopeField string", "private nonembedded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(cfg.Dir, "owner.go")
			source := readFixture(t, path)
			t.Cleanup(func() { writeFixture(t, path, source) })
			if !strings.Contains(source, tc.from) {
				t.Fatal("bad replacement", tc.from)
			}
			changed := strings.Replace(source, tc.from, tc.to, 1)
			if tc.name == "context optional" {
				changed = strings.Replace(changed, "value *Child }", "value *Child; scope string }", 1)
			}
			writeFixture(t, path, changed)
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			err := Generate(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("failure changed artifacts")
			}
		})
	}
}
