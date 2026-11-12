package dtogen

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const mapViewSource = `
import "github.com/stacklok/mecatl/engine/kernel/value"
type Key string
type Count uint64
type Alias = value.Value
// Record documents map borrowing.
type Record struct {
 Labels map[Key]string
 Counts map[string]Count
 Children map[string]*Child
 Optional *map[Key]*Child
 Native Alias
 Object value.Map
 Maybe *value.Value
 MaybeObject *value.Map
}
type Child struct { Data []byte; Leaves []Leaf }
type Leaf struct { Data []byte }
type RecordView struct { value *Record }
type ChildView struct { value *Child }
type LeafView struct { value *Leaf }
var _ = RecordView{}.Children
`

func TestMapViewsViewOnlyBootstrapAndFreshness(t *testing.T) {
	cfg := primitiveFixture(t, mapViewSource)
	cfg.Types = []string{"RecordView", "ChildView", "LeafView"}
	cfg.Emit = "view"
	writeFixture(t, filepath.Join(cfg.Dir, "registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir engine/owner -types RecordView,ChildView,LeafView -emit view -out "+cfg.Output+"\npackage owner\n")
	root := filepath.Dir(filepath.Dir(cfg.Dir))
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	output := readFixture(t, filepath.Join(cfg.Dir, cfg.Output))
	for _, op := range []string{"CheckCBOR", "AppendCBOR", "ReadCBOR", "Clone", "Equal"} {
		if strings.Contains(output, ") "+op+"(") {
			t.Fatal("view-only emitted value method", op)
		}
	}
	if !strings.Contains(output, "func(x string) string") || !strings.Contains(output, "dtogenViewChildView)") || strings.Contains(output, "&x") {
		t.Fatal("map adapter shape")
	}
	if err := GenerateRepository(context.Background(), root, true); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), "package owner\n"+strings.Replace(mapViewSource, "Record documents map borrowing.", "Record documents updated map borrowing.", 1))
	before := repositoryState(t, root)
	if err := GenerateRepository(context.Background(), root, true); err == nil {
		t.Fatal("source documentation freshness ignored")
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("freshness wrote output")
	}
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	next := readFixture(t, filepath.Join(cfg.Dir, cfg.Output))
	if output == next {
		t.Fatal("stale source fingerprint")
	}
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if next != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
		t.Fatal("nondeterministic map view output")
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), mapViewBehavior)
	runOwner(t, cfg, "")
}

const mapViewBehavior = `package owner
import("testing";"github.com/stacklok/mecatl/engine/kernel/value")
var sink ChildView
func TestMapViews(t *testing.T){
 var z RecordView
 if z.Labels().Len()!=0||z.Counts().Len()!=0||z.Children().Len()!=0||z.Native().Kind()!=value.InvalidKind||z.Object().Len()!=0{t.Fatal("zero")}
 if _,ok:=z.Optional();ok{t.Fatal("zero optional map")};if _,ok:=z.Maybe();ok{t.Fatal("zero optional value")};if _,ok:=z.MaybeObject();ok{t.Fatal("zero optional primitive map")}
 if _,ok:=z.Children().Get("missing");ok{t.Fatal("zero map miss")}
 obj:=value.Map{};native:=value.Null();empty:=map[Key]*Child{}
 r:=Record{Labels:map[Key]string{"empty":""},Counts:map[string]Count{"zero":0},Children:map[string]*Child{"a":{Data:[]byte{1,2},Leaves:[]Leaf{{Data:[]byte{3,4}},{Data:[]byte{5,6}}}},"b":{Data:[]byte{7,8,9}}},Optional:&empty,Native:value.Object(value.Map{"a":value.String("text")}),Object:value.Map{"null":value.Null()},Maybe:&native,MaybeObject:&obj}
 v:=RecordView{value:&r}
 if x,ok:=v.Optional();!ok||x.Len()!=0{t.Fatal("present empty map")}
 if x,ok:=v.Maybe();!ok||x.Kind()!=value.NullKind{t.Fatal("present null")}
 if x,ok:=v.MaybeObject();!ok||x.Len()!=0{t.Fatal("present empty object")}
 if x,ok:=v.Labels().Get("empty");!ok||x!=""{t.Fatal("present empty scalar")}
 if x,ok:=v.Counts().Get("zero");!ok||x!=0{t.Fatal("present zero scalar")}
 if _,ok:=v.Children().Get("absent");ok{t.Fatal("missing")}
 a,ok:=v.Children().Get("a");if !ok||a.Data().At(0)!=1{t.Fatal("child")}
 copy:=a.Leaves().At(0).Data().Copy();copy[0]=99
 if a.Leaves().At(0).Data().At(0)!=3{t.Fatal("leaf copy alias")}
 n:=0;for range v.Children().All(){n++;break};if n!=1{t.Fatal("early stop")}
 if a:=testing.AllocsPerRun(100,func(){
  n:=0;for _,child:=range v.Children().All(){sink=child;n+=child.Data().Len();for _,leaf:=range child.Leaves().All(){n+=leaf.Data().Len()}}
  if n!=9{panic("complete sibling traversal")}
 });a!=0{t.Fatal("boxed copied map entries",a)}
}
`

func TestMapViewsUnsupportedShapesNoWrite(t *testing.T) {
	for _, test := range []struct{ name, shape, extra, want string }{
		{"by-value DTO", "map[string]Child", "", "unsupported map element"},
		{"context-bearing pointer DTO", "map[string]*Child", "; scope string", "no context-free child view"},
		{"byte payload", "map[string][]byte", "", "unsupported map element"},
		{"pointer scalar", "map[string]*string", "", "no context-free child view"},
		{"nonstring key", "map[int]string", "", "string-underlying keys"},
		{"nested map", "map[string]map[string]string", "", "unsupported map element"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := primitiveFixture(t, `type Record struct { Data map[string]string };type Child struct { Data []byte };type RecordView struct {value *Record};type ChildView struct{value *Child}`)
			cfg.Types = []string{"RecordView", "ChildView"}
			cfg.Emit = "view"
			if err := Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), "package owner\ntype Record struct { Data "+test.shape+" };type Child struct { Data []byte };type RecordView struct {value *Record};type ChildView struct{value *Child"+test.extra+"}")
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatal("unsupported shape", err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("invalid map view wrote output")
			}
		})
	}
}

func TestPrimitiveImportedOwnerPrerequisiteClosure(t *testing.T) {
	cfg := primitiveFixture(t, `import "github.com/stacklok/mecatl/engine/middle";type Record struct { V middle.Payload `+"`cbor:\"v\"`"+` }`)
	path := filepath.Join(filepath.Dir(cfg.Dir), "middle/value.go")
	writeFixture(t, path, `package middle
import "github.com/stacklok/mecatl/engine/kernel/value"
type Payload struct { V value.Value `+"`cbor:\"v\"`"+` }
func init(){panic("generation executed prerequisite owner")}
`)
	middle := registerFixtureOwner(t, cfg, "middle", "Payload")
	// Bootstrap a missing prerequisite while its handwritten consumer calls it.
	writeFixture(t, filepath.Join(middle.Dir, middle.Output), GeneratedMarker+"\nnot Go")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if output := readFixture(t, filepath.Join(cfg.Dir, cfg.Output)); strings.Count(output, "propagates value.ErrDepthExceeded") != 2 {
		t.Fatal("imported transitive panic docs missing")
	}
	// The prerequisite has changed (and would regenerate), but invalid consumer
	// policy must leave every selected output untouched.
	source := readFixture(t, path)
	writeFixture(t, path, strings.Replace(source, "V value.Value", "V value.Value; Cache []byte "+"`cbor:\"-\"`", 1))
	writeFixture(t, filepath.Join(cfg.Dir, "owner.go"), `package owner
import "github.com/stacklok/mecatl/engine/middle"
type Record struct { V middle.Payload `+"`cbor:\"v\"`"+`; Invalid chan int `+"`cbor:\"-\"`"+` }
`)
	before := repositoryState(t, filepath.Dir(cfg.Dir))
	if err := Generate(context.Background(), cfg); err == nil {
		t.Fatal("unsupported consumer accepted")
	}
	if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
		t.Fatal("failure installed prerequisite before closure validation")
	}
}
