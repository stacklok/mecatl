package dtogen

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Executed in the external-owner fixture after generation, not merely parsed.
const viewMutableCloneBehaviorTest = `package owner
import (
 "bytes"
 "reflect"
 "runtime"
 "testing"
 "example.invalid/payloads/data"
)
var cloneSink data.Child
var copySink []byte
func TestMutableClone(t *testing.T) {
 if got := (RecordView{}).MutableClone(); !reflect.DeepEqual(got, data.Record{}) { t.Fatal("zero record", got) }
 if got := (ScopedChildView{scope:"context only"}).MutableClone(); !reflect.DeepEqual(got, data.Child{}) { t.Fatal("zero child", got) }
 emptyBytes := []byte{}
 emptyChildren := []data.Child{}
 child := data.NewChild([]byte{0xff, 0, 0x80})
 x := data.Record{Title:"provider spelling", Child:child, Maybe:&child,
  Children:[]data.Child{data.NewChild([]byte{3}),data.NewChild([]byte{2})},
  OptionalBytes:&emptyBytes, OptionalChildren:&emptyChildren,
  Lookup:map[string]*data.Child{"child":&child,"nil":nil}, Matrix:[][]byte{nil,{}, {0,0xff}},
  Sibling:[]byte{9}}
 v := RecordView{value:&x,scope:"private context"}
 before := x.Clone()
 a, b := v.MutableClone(), v.MutableClone()
 if !reflect.DeepEqual(a, x) || !reflect.DeepEqual(b, x) { t.Fatal("payload changed (bytes, presence, private omitted state or order)") }
 a.Child.Data[0]++
 a.Child.HiddenBytes()[0]++
 a.Maybe.Data[1]++
 a.Children[0].Data[0]++
 a.Children[1].HiddenBytes()[1]++
 a.Lookup["child"].HiddenBytes()[2]++
 delete(a.Lookup,"nil")
 a.Matrix[2][1]++
 a.Sibling[0]++
 *a.OptionalBytes = append(*a.OptionalBytes, 1)
 *a.OptionalChildren = append(*a.OptionalChildren, data.Child{})
 if !reflect.DeepEqual(x,before) || !reflect.DeepEqual(b,before) { t.Fatal("clone aliases source or another clone") }
 for _, retained := range []data.Record{x,b} {
  if !bytes.Equal(retained.Child.Data,[]byte{0xff,0,0x80}) || !bytes.Equal(retained.Child.HiddenBytes(),[]byte{0xff,0,0x80}) ||
   !bytes.Equal(retained.Maybe.Data,[]byte{0xff,0,0x80}) || !bytes.Equal(retained.Children[0].Data,[]byte{3}) ||
   !bytes.Equal(retained.Children[1].HiddenBytes(),[]byte{0xff,0,0x80}) || !bytes.Equal(retained.Lookup["child"].HiddenBytes(),[]byte{0xff,0,0x80}) ||
   !bytes.Equal(retained.Matrix[2],[]byte{0,0xff}) || !bytes.Equal(retained.Sibling,[]byte{9}) || len(*retained.OptionalBytes)!=0 || len(*retained.OptionalChildren)!=0 {
   t.Fatal("retained leaf changed from independently specified content")
  }
  if _,ok:=retained.Lookup["nil"];!ok { t.Fatal("retained map entry removed") }
 }
 x.Child.Data[0] = 7
 if b.Child.Data[0] != 0xff { t.Fatal("source aliases clone") }
 // Accessors still borrow the original payload; only leaf Copy detaches.
 if v.Child().Data().At(0) != 7 { t.Fatal("navigation stopped borrowing") }
 leaf := v.Child().Data().Copy()
 leaf[0] = 0
 if v.Child().Data().At(0) != 7 || !bytes.Equal(b.Child.Data, []byte{0xff,0,0x80}) { t.Fatal("leaf Copy aliases backing") }
 var nilBytes []byte
 var nilChildren []data.Child
 for _, payload := range []data.Record{
  {}, {OptionalBytes:&nilBytes,OptionalChildren:&nilChildren},
  {OptionalBytes:&emptyBytes,OptionalChildren:&emptyChildren,Children:[]data.Child{},Lookup:map[string]*data.Child{},Sibling:[]byte{},Maybe:&data.Child{}},
 } {
  if got := (RecordView{value:&payload}).MutableClone(); !reflect.DeepEqual(got,payload) { t.Fatal("nil/present-empty changed", got, payload) }
 }
}
func TestMutableClonePopulatedOptionalContainers(t *testing.T) {
 payloadBytes:=[]byte{0xff,0,0x80}
 children:=[]data.Child{data.NewChild([]byte{3}),data.NewChild([]byte{2})}
 x:=data.Record{OptionalBytes:&payloadBytes,OptionalChildren:&children}
 a,b:=(RecordView{value:&x}).MutableClone(),(RecordView{value:&x}).MutableClone()
 (*a.OptionalBytes)[0]=1
 (*a.OptionalChildren)[0].Data[0]=4
 (*a.OptionalChildren)[0].HiddenBytes()[0]=5
 (*a.OptionalChildren)[1]=data.Child{}
 for _,retained:=range []data.Record{x,b} {
  if retained.OptionalBytes==nil || !bytes.Equal(*retained.OptionalBytes,[]byte{0xff,0,0x80}) || retained.OptionalChildren==nil || len(*retained.OptionalChildren)!=2 {
   t.Fatal("optional container presence or bytes changed")
  }
  c:=*retained.OptionalChildren
  if !bytes.Equal(c[0].Data,[]byte{3}) || !bytes.Equal(c[0].HiddenBytes(),[]byte{0xff,0,0x80}) || !bytes.Equal(c[1].Data,[]byte{2}) {
   t.Fatal("optional children share backing or descendants")
  }
 }
}
func TestMutableCloneSelectedContextChild(t *testing.T) {
 x := data.Record{Child:data.NewChild([]byte{1,2,3}),Sibling:make([]byte,1<<20)}
 v := RecordView{value:&x,scope:"enclosing scope"}
 child := v.ScopedChild()
 if child.Scope() != "enclosing scope" { t.Fatal("navigation lost scope") }
 cloneSink = child.MutableClone() // Warm up before measuring only the selected subtree.
 var before, after runtime.MemStats
 runtime.ReadMemStats(&before)
 for i:=0;i<20;i++ { cloneSink=child.MutableClone() }
 runtime.ReadMemStats(&after)
 if n:=after.TotalAlloc-before.TotalAlloc;n>128<<10 { t.Fatal("selected child cloned enclosing/sibling state",n) }
 cloneSink.Data[0]++
 cloneSink.HiddenBytes()[0]++
 if child.Data().At(0)!=1 || x.Child.HiddenBytes()[0]!=0xff { t.Fatal("context child clone aliases backing") }
 if a:=testing.AllocsPerRun(100,func(){
  c:=v.ScopedChild();if c.Scope()!="enclosing scope"||c.Data().At(0)!=1||v.Sibling().Len()!=1<<20 { panic("borrowed navigation") }
 });a!=0 { t.Fatal("borrowed navigation allocated",a) }
 runtime.ReadMemStats(&before)
 for i:=0;i<20;i++ { copySink=child.Data().Copy() }
 runtime.ReadMemStats(&after)
 if n:=after.TotalAlloc-before.TotalAlloc;n>128<<10 { t.Fatal("leaf copy cloned siblings",n) }
 copySink[0]++
 if child.Data().At(0)!=1 { t.Fatal("leaf copy aliases selected child") }
}
`

func TestViewMutableCloneSelection(t *testing.T) {
	for _, tc := range []struct {
		name, emit, roots, declaration string
		want                           bool
	}{
		{"clone owner", "clone", "Record", "", true},
		{"equal owner", "equal", "Record", "", false},
		{"no owner", "", "", "", false},
		{"outside named closure", "clone", "Other", "", false},
		{"method alone", "", "", "func(v Record) Clone() Record { return v }", false},
		{"outside closure with method", "clone", "Other", "func(v Record) Clone() Record { return v }", false},
		{"explicit override", "clone", "Record", "//dtogen:override Clone\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := viewFixture(t, "view")
			cfg.Types = []string{"RecordView"}
			module := filepath.Dir(cfg.Dir)
			dir := filepath.Join(module, "data")
			declaration := tc.declaration
			if tc.name == "explicit override" {
				declaration += "type Record struct { Data []byte }; var Calls int; func(v Record) Clone() Record { Calls++; v.Data=append([]byte(nil),v.Data...); return v }"
			} else {
				declaration = "type Record struct { Data []byte }; " + declaration
			}
			writeFixture(t, filepath.Join(dir, "data.go"), "package data\n"+declaration+"\ntype Other struct{}\n")
			if tc.emit != "" {
				writeFixture(t, filepath.Join(dir, "registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir engine/data -types "+tc.roots+" -emit "+tc.emit+" -out zz_data_generated.go\npackage data\n")
			}
			writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), "package owner\nimport \"github.com/stacklok/mecatl/engine/data\"\ntype RecordView struct{value *data.Record}\n")
			if err := Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			body := `package owner
import("reflect";"testing";"github.com/stacklok/mecatl/engine/data")
func TestCapability(t *testing.T){
 _,ok:=reflect.TypeFor[RecordView]().MethodByName("MutableClone")
 if ok!=WANT { t.Fatal("capability selection",ok) }
 _=data.Record{}
 CHECK
}
`
			body = strings.ReplaceAll(body, "WANT", map[bool]string{true: "true", false: "false"}[tc.want])
			check := ""
			if tc.want {
				check = `x:=data.Record{Data:[]byte{1}};v:=RecordView{value:&x};a:=v.MutableClone();a.Data[0]++;if x.Data[0]!=1 { t.Fatal("alias") }`
			}
			if tc.name == "explicit override" {
				check += `;if data.Calls!=1 { t.Fatal("declared Clone override not called",data.Calls) };_=(RecordView{}).MutableClone();if data.Calls!=1 { t.Fatal("zero view called override") }`
			}
			writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), strings.ReplaceAll(body, "CHECK", check))
			runOwner(t, cfg, "")
		})
	}
}

func TestViewMutableCloneInspectionOnlyLocal(t *testing.T) {
	cfg := viewFixture(t, "view")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("reflect";"testing")
func TestInspectionOnly(t *testing.T) {
 for _,typ:=range []reflect.Type{reflect.TypeFor[RecordView](),reflect.TypeFor[ChildView]()} {
  if _,ok:=typ.MethodByName("MutableClone");ok { t.Fatal("view-only local acquired clone capability",typ) }
 }
}
`)
	runOwner(t, cfg, "")
}

func TestViewMutableCloneCollisions(t *testing.T) {
	for _, tc := range []struct{ name, field, method string }{
		{"value method", "", "func(RecordView) MutableClone() data.Record { return data.Record{} }"},
		{"pointer method", "", "func(*RecordView) MutableClone() data.Record { return data.Record{} }"},
		{"untagged DTO field", "; MutableClone string", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := viewFixture(t, "view")
			cfg.Types = []string{"RecordView"}
			dir := filepath.Join(filepath.Dir(cfg.Dir), "data")
			writeFixture(t, filepath.Join(dir, "data.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir engine/data -types Record -emit clone -out zz_data_generated.go\npackage data\ntype Record struct{Data []byte"+tc.field+"}\n")
			writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), "package owner\nimport \"github.com/stacklok/mecatl/engine/data\"\ntype RecordView struct{value *data.Record}\n"+tc.method+"\n")
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			err := Generate(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "collid") || !strings.Contains(err.Error(), "MutableClone") {
				t.Fatalf("expected MutableClone collision: %v", err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("collision installed artifacts")
			}
		})
	}
}
