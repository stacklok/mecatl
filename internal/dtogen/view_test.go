package dtogen

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
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
 Inline []byte ` + "`cbor:\"i\"`" + `
 Source string ` + "`cbor:\"s\"`" + `
 URI string ` + "`cbor:\"u\"`" + `
 Child Child ` + "`cbor:\"c\"`" + `
 Maybe *Child ` + "`cbor:\"m,omitempty\"`" + `
 Children []Child ` + "`cbor:\"cs\"`" + `
 Hidden map[string]string ` + "`cbor:\"-\"`" + `
}
type Child struct { Label string ` + "`cbor:\"l\"`" + `; Blob []byte ` + "`cbor:\"b\"`" + ` }
type RecordView struct { value *Record; scope string }
type ChildView struct { value *Child }
// InlineData gives tag-selected presence, even for empty inline data.
func(v RecordView) InlineData()(views.Bytes,bool){if v.value==nil||v.value.Source!="inline"{return views.Bytes{},false};return views.BorrowBytes(v.value.Inline),true}
// RemoteURI gives tag-selected string presence.
func(v RecordView) RemoteURI()(string,bool){if v.value==nil||v.value.Source!="remote"{return "",false};return v.value.URI,true}
func(v RecordView) Scope()string{return v.scope}
var _ = RecordView{}.Title()
var _ = ChildView{}.Label()
var _ = dtogenViewChildView
`
const viewFixtureMap = `[
 {"view":"RecordView","backing":"value","fields":[
 {"field":"Title","method":"Title"},
 {"field":"Count","method":"Count"},
 {"field":"Data","method":"Data"},
 {"field":"Inline","method":"InlineData","override":"func() (views.Bytes, bool)"},
 {"field":"Source","exclude":true},
 {"field":"URI","method":"RemoteURI","override":"func() (string, bool)"},
 {"field":"Child","method":"Child"},
 {"field":"Maybe","method":"Maybe"},
 {"field":"Children","method":"Children"},
 {"field":"Hidden","exclude":true}],
 "context":[{"method":"Scope","signature":"func() string"}]},
 {"view":"ChildView","backing":"value","fields":[
 {"field":"Label","method":"Label"},{"field":"Blob","method":"Blob"}]}
]`

func viewFixture(t *testing.T, emit string) LoadConfig {
	t.Helper()
	cfg := executableFixture(t, viewFixtureSource)
	cfg.Emit = emit
	cfg.Views = "views.json"
	if emit == "view" {
		cfg.Types = nil
	}
	for _, name := range []string{"bytes.go", "slice.go"} {
		data, err := os.ReadFile("../../engine/kernel/views/" + name)
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "kernel/views", name), string(data))
	}
	writeFixture(t, filepath.Join(cfg.Dir, cfg.Views), viewFixtureMap)
	return cfg
}

func TestViewCapabilitiesAndBootstrap(t *testing.T) {
	for _, emit := range []string{"view", "view,clone", "equal,view", "view,cbor", "clone,equal,view", "cbor,clone,view", "cbor,equal,view", "clone,equal,cbor,view"} {
		t.Run(emit, func(t *testing.T) {
			cfg := viewFixture(t, emit)
			// A single literal owner declaration feeds direct and repository generation.
			roots := ""
			if len(cfg.Types) > 0 {
				roots = " -types Record"
			}
			writeFixture(t, filepath.Join(cfg.Dir, "registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir engine/owner"+roots+" -emit "+emit+" -views views.json -out "+cfg.Output+"\npackage owner\n")
			// Bootstrap overlays and determinism share the same path for every selection.
			// Cover both the view-only and maximal closure, not their Cartesian product.
			bootstrap := emit == "view" || emit == "clone,equal,cbor,view"
			priors := []string{""}
			if bootstrap {
				priors = append(priors, GeneratedMarker+"\nnot Go", GeneratedMarker+"\npackage owner\nfunc(RecordView) Title() int{return 0}")
			}
			for _, prior := range priors {
				if prior != "" {
					writeFixture(t, filepath.Join(cfg.Dir, cfg.Output), prior)
				}
				if err := Generate(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				before := readFixture(t, filepath.Join(cfg.Dir, cfg.Output))
				if !strings.Contains(before, "Provisional: revisit tags") || !strings.Contains(before, "Title is provider-owned text") || !strings.Contains(before, "O(1)") {
					t.Fatal("source/accessor docs lost")
				}
				if strings.Contains(before, "func (v RecordView) InlineData") {
					t.Fatal("override body replaced")
				}
				want, err := parseEmit(emit)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"CheckCBOR", "AppendCBOR", "ReadCBOR", "Clone", "Equal"} {
					receiver := "Record"
					if name == "ReadCBOR" {
						receiver = "*Record"
					}
					if got := strings.Contains(before, "func (v "+receiver+") "+name+"("); got != want.method(name) {
						t.Errorf("%s present=%v", name, got)
					}
				}
				if bootstrap {
					if err := Generate(context.Background(), cfg); err != nil {
						t.Fatal(err)
					}
					if before != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
						t.Fatal("nondeterministic output")
					}
				}
			}
			root := filepath.Dir(filepath.Dir(cfg.Dir))
			if err := GenerateRepository(context.Background(), root, true); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), viewBehaviorTest)
			runOwner(t, cfg, "")
		})
	}
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
 n:=uint64(0);r:=Record{Title:"hi",Count:&n,Data:[]byte{},Source:"inline",Inline:[]byte{},Child:Child{Label:"one",Blob:[]byte{1}},Maybe:&Child{},Children:[]Child{{Label:"two",Blob:[]byte{2}},{Label:"three"}}}
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
 Existing views.Bytes
}
type Child struct { Label Label }
type RecordView struct { value *Record }
type ChildView struct { value *Child }
`)
	writeFixture(t, filepath.Join(cfg.Dir, cfg.Views), `[
 {"view":"RecordView","backing":"value","fields":[
 {"field":"Active","method":"Active"},{"field":"Ratio","method":"Ratio"},
 {"field":"Token","method":"Token"},{"field":"Bytes","method":"Bytes"},
 {"field":"Children","method":"Children"},{"field":"Existing","exclude":true}]},
 {"view":"ChildView","backing":"value","fields":[{"field":"Label","method":"Label"}]}]`)
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

func TestViewInvalidMappingNoReplacement(t *testing.T) {
	// Subtests are sequential and restore their mutation before the next case.
	cfg := viewFixture(t, "view")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	prior := readFixture(t, filepath.Join(cfg.Dir, cfg.Output))
	for _, tc := range []struct {
		name, from, to, want string
		source               bool
	}{
		{"uncovered", `,\n {"field":"Hidden","exclude":true}`, "", "uncovered", false},
		{"unknown", "\"field\":\"Hidden\"", "\"field\":\"Missing\"", "unknown field", false},
		{"duplicate field", "\"field\":\"Hidden\"", "\"field\":\"Title\"", "duplicate field", false},
		{"duplicate method", "\"method\":\"Count\"", "\"method\":\"Title\"", "duplicate method", false},
		{"signature expression", "func() (views.Bytes, bool)", "func() (views.Bytes, bool) { panic(0) }", "exact declared signature", false},
		{"invalid signature", "func() (views.Bytes, bool)", "func() Missing", "invalid declared signature", false},
		{"pointer receiver", "func(v RecordView) InlineData", "func(v *RecordView) InlineData", "missing/stale", true},
		{"unclassified", "func(v RecordView) Scope()string{return v.scope}", "func(v RecordView) Scope()string{return v.scope};func(RecordView) Extra(){}", "unclassified", true},
		{"duplicate context", `"context":[{"method":"Scope","signature":"func() string"}]`, `"context":[{"method":"Scope","signature":"func() string"},{"method":"Scope","signature":"func() string"}]`, "duplicate context", false},
		{"duplicate view", `"view":"ChildView"`, `"view":"RecordView"`, "duplicate view", false},
		{"override signature", "func() (views.Bytes, bool)", "func() views.Bytes", "exact declared signature", false},
		{"missing override", "\"method\":\"InlineData\"", "\"method\":\"Absent\"", "missing/stale", false},
		{"collision", "\"override\":\"func() (views.Bytes, bool)\"", "\"override\":\"\"", "collision", false},
		{"stale context", "\"method\":\"Scope\"", "\"method\":\"Gone\"", "missing/stale", false},
		{"unknown JSON", "\"view\":\"RecordView\"", "\"viwe\":\"RecordView\"", "unknown field", false},
		{"unsupported", "\"field\":\"Hidden\",\"exclude\":true", "\"field\":\"Hidden\",\"method\":\"Hidden\"", "unsupported view shape", false},
		{"context child", "type ChildView struct { value *Child }", "type ChildView struct { value *Child; scope string }", "no context-free", true},
		{"import collision", "type ChildView struct", "type dtogenViews struct{};type ChildView struct", "collides", true},
		{"constructor collision", "type ChildView struct", "var dtogenViewChildView int;type ChildView struct", "collides", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(cfg.Dir, cfg.Views)
			if tc.source {
				path = filepath.Join(cfg.Dir, "owner.go")
			}
			if prior != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
				t.Fatal("baseline artifact changed before mutation")
			}
			source := readFixture(t, path)
			t.Cleanup(func() { writeFixture(t, path, source) })
			from := strings.ReplaceAll(tc.from, `\n`, "\n")
			if !strings.Contains(source, from) {
				t.Fatal("bad test replacement", from)
			}
			writeFixture(t, path, strings.Replace(source, from, tc.to, 1))
			err := Generate(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if prior != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
				t.Fatal("failure replaced artifact")
			}
		})
	}
}
