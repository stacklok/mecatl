package cborgen

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestOwnerCompositionAndBootstrap exercises actual owner declarations and a
// compiled third-package consumer, not coincidentally compatible handwritten APIs.
func TestOwnerCompositionAndBootstrap(t *testing.T) {
	cfg := executableFixture(t, `
import "github.com/stacklok/mecatl/engine/values"
var Passes int
//cborgen:validate local
// Record composes named owners without expanding private storage.
type Record struct {
 List values.List `+"`cbor:\"l\" cborlimit:\"items=2\"`"+`
 Map values.NamedMap `+"`cbor:\"m\"`"+`
 Capped values.Wrapped `+"`cbor:\"c\" cborlimit:\"bytes=3\"`"+`
 Excluded values.State `+"`cbor:\"-\"`"+`
}
func (Record) local() error {Passes++;return nil}
// CallerBound tightens an imported transparent owner's immediate value.
type CallerBound struct {
 Value values.Wrapped `+"`cbor:\",value\" cborlimit:\"bytes=3\"`"+`
 Cache []byte `+"`cbor:\"-\"`"+`
}
`)
	cfg.Types = []string{"Record", "CallerBound"}
	module := filepath.Dir(cfg.Dir)
	root := filepath.Dir(module)
	child := filepath.Join(module, "values")
	writeFixture(t, filepath.Join(child, "value.go"), `package values
import "errors"
import "github.com/stacklok/mecatl/engine/kernel/codec"
var _ codec.Marshaler=Wrapped{}
var _ codec.Unmarshaler=(*Wrapped)(nil)
var Passes int
//cborgen:validate local
// Text validates once per represented value.
type Text string
func(v Text) local()error {Passes++;if v=="bad" {return errors.New("private payload must not escape")};return nil}
type Inner struct {value Text `+"`cbor:\",value\" cborlimit:\"bytes=5\"`"+`}
//cborgen:validate local
// Wrapped applies an owner cap and local validation.
type Wrapped struct {value Inner `+"`cbor:\",value\" cborlimit:\"bytes=4\"`"+`}
func(Wrapped)local()error{Passes++;return nil}
func Wrap(s string)Wrapped{return Wrapped{value:Inner{value:Text(s)}}}
// InnerBound admits more bytes at its outer field so Inner's five-byte cap is independently observable.
type InnerBound struct {value Inner `+"`cbor:\",value\" cborlimit:\"bytes=8\"`"+`}
func WrapInner(s string)InnerBound{return InnerBound{value:Inner{value:Text(s)}}}
type Bytes []byte
//cborgen:validate local
// Key retains validation even when used as a map key.
type Key string
func(v Key)local()error{Passes++;if v=="" {return errors.New("empty key")};return nil}
type List []Wrapped
type NamedMap map[Key]Bytes
//cborgen:validate local
// State is excluded-only mutable storage.
type State struct {private map[string][]byte}
func(State)local()error{panic("excluded state validator executed")}
type Anchor struct {State State `+"`cbor:\"-\"`"+`}
func NewState()State{return State{private:map[string][]byte{"x":{1}}}}
func(v State)Change(){v.private["x"][0]++}
func(v State)Byte()byte{return v.private["x"][0]}
`)
	for _, owner := range []struct{ dir, pkg, names string }{{cfg.Dir, "owner", "Record,CallerBound"}, {child, "values", "Wrapped,InnerBound,List,NamedMap,Anchor"}} {
		rel, err := filepath.Rel(root, owner.dir)
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(owner.dir, "registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir "+rel+" -types "+owner.names+" -out zz_cbor_generated.go\npackage "+owner.pkg+"\n")
		writeFixture(t, filepath.Join(owner.dir, "trap.go"), "package "+owner.pkg+"\nfunc init(){panic(\"owner init must never execute during generation\")}\n")
	}
	ctx := context.Background()
	// A single-owner invocation resolves its entire declared prerequisite closure.
	if err := Generate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	baseline := repositoryState(t, root)
	outputs := []string{filepath.Join(child, cfg.Output), filepath.Join(cfg.Dir, cfg.Output)}
	for _, corrupt := range []bool{false, true} {
		for _, path := range outputs {
			if corrupt {
				writeFixture(t, path, GeneratedMarker+"\nnot valid Go")
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		before := repositoryState(t, root)
		if err := GenerateRepository(ctx, root, true); err == nil {
			t.Fatal("freshness accepted missing/corrupt owners")
		}
		if !reflect.DeepEqual(before, repositoryState(t, root)) {
			t.Fatal("check changed owner files")
		}
		if err := GenerateRepository(ctx, root, false); err != nil {
			t.Fatal(err)
		}
		for _, path := range outputs {
			data, err := os.ReadFile(path)
			if err != nil || string(data) != baseline[path].data {
				t.Fatal("owner bootstrap changed deterministic output", err)
			}
		}
	}
	if err := GenerateRepository(ctx, root, true); err != nil {
		t.Fatal(err)
	}
	childSource := filepath.Join(child, "value.go")
	original, err := os.ReadFile(childSource)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, childSource, strings.Replace(string(original), `bytes=4`, `bytes=3`, 1))
	before := repositoryState(t, root)
	if err := GenerateRepository(ctx, root, true); err == nil || !strings.Contains(err.Error(), "stale codec output") {
		t.Fatal("changed owner schema accepted", err)
	}
	if !reflect.DeepEqual(before, repositoryState(t, root)) {
		t.Fatal("schema freshness check mutated owner files")
	}
	writeFixture(t, childSource, string(original))
	for _, dir := range []string{cfg.Dir, child} {
		if err := os.Remove(filepath.Join(dir, "trap.go")); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(module, "consumer/consumer_test.go"), `package consumer
import("testing";"bytes";"errors";"reflect";"strings";"github.com/stacklok/mecatl/engine/kernel/codec";"github.com/stacklok/mecatl/engine/owner";"github.com/stacklok/mecatl/engine/values")
var _ codec.Marshaler=owner.Record{}
var _ codec.Unmarshaler=(*owner.Record)(nil)
func policy()codec.Limits{return codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:100,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}}
func record()owner.Record{return owner.Record{List:values.List{values.Wrap("abc")},Map:values.NamedMap{"k":{1}},Capped:values.Wrap("xyz"),Excluded:values.NewState()}}
// assertReadCap uses invalid UTF-8 only beyond the particular cap under test.
// With loose globals and all other caps admitting the length, removing that
// min clamp would produce ErrInvalidData instead of ErrLimitExceeded.
func assertReadCap(t *testing.T,dst codec.Unmarshaler,unchanged func()bool,wire []byte,fieldMax int){
 t.Helper();input:=bytes.Clone(wire);passes:=values.Passes
 r,err:=codec.NewReader(wire,policy());if err!=nil{t.Fatal(err)}
 if err:=dst.ReadCBOR(&r,0,fieldMax);!errors.Is(err,codec.ErrLimitExceeded){t.Fatal("cap must precede UTF-8 scan",err)}
 if !unchanged()||!bytes.Equal(input,wire){t.Fatal("rejected read mutated populated receiver/storage or input")}
 if values.Passes!=passes{t.Fatal("rejected read ran a validator")}
}
func TestTransparentReadCaps(t *testing.T){
 // Every wire value below is constructed independently from CBOR's text header
 // and content bytes, never from a generated encoder or its modified output.
 t.Run("caller field cap 3 below owner caps 4 and 5",func(t *testing.T){
  wire:=[]byte{0x64,'a','b','c',0xff}
  dst:=owner.CallerBound{Value:values.Wrap("old"),Cache:[]byte{1,2}}
  before:=dst.Clone();cache:=&dst.Cache[0]
  assertReadCap(t,&dst,func()bool{return dst.Equal(before)&&&dst.Cache[0]==cache},wire,100)
  out,err:=codec.Decode[owner.CallerBound](wire,policy())
  if !errors.Is(err,codec.ErrLimitExceeded)||!reflect.DeepEqual(out,owner.CallerBound{}){t.Fatal("root caller cap/zero failure",err)}
  exact:=[]byte{0x63,'a','b','c'}
  out,err=codec.Decode[owner.CallerBound](exact,policy())
  if err!=nil||!out.Equal(owner.CallerBound{Value:values.Wrap("abc")}){t.Fatal("exact caller cap",err)}
 })
 t.Run("outer owner cap 4 below inner cap 5",func(t *testing.T){
  wire:=[]byte{0x65,'f','o','u','r',0xff}
  dst:=values.Wrap("old");before:=dst.Clone()
  assertReadCap(t,&dst,func()bool{return dst.Equal(before)},wire,100)
  out,err:=codec.Decode[values.Wrapped](wire,policy())
  if !errors.Is(err,codec.ErrLimitExceeded)||!reflect.DeepEqual(out,values.Wrapped{}){t.Fatal("root outer cap/zero failure",err)}
  exact:=[]byte{0x64,'f','o','u','r'}
  out,err=codec.Decode[values.Wrapped](exact,policy())
  if err!=nil||!out.Equal(values.Wrap("four")){t.Fatal("exact outer cap",err)}
 })
 t.Run("inner owner cap 5 below outer cap 8",func(t *testing.T){
  wire:=[]byte{0x66,'f','i','v','e','s',0xff}
  dst:=values.WrapInner("old");before:=dst.Clone()
  assertReadCap(t,&dst,func()bool{return dst.Equal(before)},wire,100)
  out,err:=codec.Decode[values.InnerBound](wire,policy())
  if !errors.Is(err,codec.ErrLimitExceeded)||!reflect.DeepEqual(out,values.InnerBound{}){t.Fatal("root inner cap/zero failure",err)}
  exact:=[]byte{0x65,'f','i','v','e','s'}
  out,err=codec.Decode[values.InnerBound](exact,policy())
  if err!=nil||!out.Equal(values.WrapInner("fives")){t.Fatal("exact inner cap",err)}
 })
 t.Run("shared reader fieldMax 2 below owner caps",func(t *testing.T){
  dst:=values.Wrap("old");before:=dst.Clone()
  assertReadCap(t,&dst,func()bool{return dst.Equal(before)},[]byte{0x63,'o','k',0xff},2)
  r,err:=codec.NewReader([]byte{0x62,'o','k'},policy());if err!=nil{t.Fatal(err)}
  if err:=dst.ReadCBOR(&r,0,2);err!=nil||!dst.Equal(values.Wrap("ok")){t.Fatal("exact shared fieldMax",err)}
 })
}
func TestComposed(t *testing.T){
 l:=policy();v:=record();values.Passes=0;owner.Passes=0
 b,err:=codec.Encode(v,l);if err!=nil{t.Fatal(err)}
 if values.Passes!=5||owner.Passes!=1{t.Fatal("encode repeated validators",values.Passes,owner.Passes)}
 values.Passes=0;owner.Passes=0
 w,err:=codec.Decode[owner.Record](b,l);if err!=nil{t.Fatal("pointer inference",err)}
 if values.Passes!=5||owner.Passes!=1{t.Fatal("decode repeated validators",values.Passes,owner.Passes)}
 w=v.Clone();w.Excluded.Change();w.Map["k"][0]=9
 if v.Excluded.Byte()!=1||v.Map["k"][0]!=1||v.Equal(w)||values.Passes!=5||owner.Passes!=1{t.Fatal("value methods validated or aliased excluded state")}
 for _,edit:=range []func(*codec.Limits){func(l *codec.Limits){l.MaxNodes=8},func(l *codec.Limits){l.MaxCollectionElements=4},func(l *codec.Limits){l.MaxDepth=1}}{
  small:=l;edit(&small);if _,err:=codec.Encode(v,small);!errors.Is(err,codec.ErrLimitExceeded){t.Fatal("owner reset accounting",err)}
  if _,err:=codec.Decode[owner.Record](b,small);!errors.Is(err,codec.ErrLimitExceeded){t.Fatal("reader reset accounting",err)}
 }
 // Parent collection caps must not leak to strings; the encoded three-byte strings above are valid.
 v.Capped=values.Wrap("four");if _,err:=codec.Encode(v,l);!errors.Is(err,codec.ErrLimitExceeded){t.Fatal("caller cap lost through wrappers",err)}
 budget,err:=codec.NewBudget(l);if err!=nil{t.Fatal(err)}
 if err:=values.Wrap("abc").CheckCBOR(&budget,0,2);!errors.Is(err,codec.ErrLimitExceeded){t.Fatal("root fieldMax not clamped",err)}
 for _,bad:=range []owner.Record{func()owner.Record{v:=record();v.Capped=values.Wrap("bad");return v}(),func()owner.Record{v:=record();v.Map=values.NamedMap{"":{1}};return v}()} {
  if _,err:=codec.Encode(bad,l);!errors.Is(err,codec.ErrInvalidData)||strings.Contains(err.Error(),"private payload"){t.Fatal("semantic classification",err)}
  raw:=bad.AppendCBOR(nil);dst:=record();before:=dst.Clone();r,err:=codec.NewReader(raw,l);if err!=nil{t.Fatal(err)}
  if err:=dst.ReadCBOR(&r,0,100);!errors.Is(err,codec.ErrInvalidData)||!dst.Equal(before){t.Fatal("semantic failure mutated receiver",err)}
 }
 for _,raw:=range [][]byte{b[:len(b)-1],{0xa1,0x61,0x63,0xf6}} {
  dst:=record();before:=dst.Clone();input:=bytes.Clone(raw);r,err:=codec.NewReader(raw,l);if err!=nil{t.Fatal(err)}
  if err:=dst.ReadCBOR(&r,0,100);err==nil||!dst.Equal(before)||!bytes.Equal(raw,input){t.Fatal("structural failure mutated storage",err)}
 }
 dst:=record();before:=dst.Clone();small:=l;small.MaxNodes=8;r,err:=codec.NewReader(b,small);if err!=nil{t.Fatal(err)}
 if err:=dst.ReadCBOR(&r,0,100);!errors.Is(err,codec.ErrLimitExceeded)||!dst.Equal(before){t.Fatal("limit failure mutated receiver",err)}
 trailing:=append(bytes.Clone(b),0);r,err=codec.NewReader(trailing,l);if err!=nil{t.Fatal(err)}
 if err:=dst.ReadCBOR(&r,0,100);err!=nil{t.Fatal("nested read called Done",err)}
 if err:=r.Done();!errors.Is(err,codec.ErrInvalidData){t.Fatal("trailing data",err)}
 if out,err:=codec.Decode[owner.Record](trailing,l);!errors.Is(err,codec.ErrInvalidData)||!reflect.DeepEqual(out,owner.Record{}){t.Fatal("root partial output",err)}
 if err:=dst.CheckCBOR(nil,0,100);!errors.Is(err,codec.ErrInvalidLimits){t.Fatal("nil budget",err)}
 if err:=dst.ReadCBOR(nil,0,100);!errors.Is(err,codec.ErrInvalidLimits){t.Fatal("nil reader",err)}
 var nilReceiver *owner.Record
 if err:=nilReceiver.ReadCBOR(&r,0,100);!errors.Is(err,codec.ErrInvalidData){t.Fatal("nil receiver",err)}
}
`)
	runOwner(t, cfg, "")
}

func TestOwnerDeclarationRejections(t *testing.T) {
	for _, tt := range []struct{ name, source, reason string }{
		{"annotation ambiguous", "//cborgen:validate valid\ntype (Record string; Other string)", "ambiguous or misplaced"},
		{"annotation alias", "type Base string\n//cborgen:validate valid\ntype Alias = Base;type Record struct{V Alias `cbor:\"v\"`}", "not an alias"},
		{"defined pointer chain", "type Alias = *string;type Record Alias", "defined pointer"},
		{"defined pointer", "type Record *string", "defined pointer"},
		{"annotation unknown", "//cborgen:encode encode\ntype Record string", "unknown cborgen"},
		{"annotation duplicate", "//cborgen:validate valid\n//cborgen:validate valid\ntype Record string;func(Record)valid()error{return nil}", "duplicate"},
		{"annotation missing method", "//cborgen:validate valid\ntype Record string", "local value receiver"},
		{"annotation pointer method", "//cborgen:validate valid\ntype Record string;func(*Record)valid()error{return nil}", "value receiver"},
		{"annotation wrong signature", "//cborgen:validate valid\ntype Record string;func(Record)valid(int)error{return nil}", "signature"},
		{"packed byte validator", "//cborgen:validate valid\ntype Byte uint8;func(Byte)valid()error{return nil};type Record []Byte", "packed named-byte"},
		{"transparent mixed", "type Record struct{V string `cbor:\",value\"`;W string `cbor:\"w\"`}", "exactly one persisted"},
		{"transparent scalar cap", "type Record struct{V uint64 `cbor:\",value\" cborlimit:\"bytes=4\"`}", "option or shape"},
		{"named reference removed", "type Record struct{V string `cbor:\"v\" cborlimit:\"bytes=@cap\"`}", "positive decimal"},
		{"reachable method collision", "type Child string;func(Child)Clone()Child{return \"\"};type Record struct{V Child `cbor:\"v\"`}", "collides with generated Clone"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := executableFixture(t, tt.source)
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("got %v; want %q", err, tt.reason)
			}
		})
	}
}

func TestImportedInterfaceCoincidenceIsNotOptIn(t *testing.T) {
	cfg := executableFixture(t, `import "github.com/stacklok/mecatl/engine/values";type Record struct{V values.Value `+"`cbor:\"v\"`"+`}`)
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "values/value.go"), `package values
import "github.com/stacklok/mecatl/engine/kernel/codec"
type Value string
func(Value)CheckCBOR(*codec.Budget,int,int)error{return nil}
func(Value)AppendCBOR(b []byte)[]byte{return b}
func(*Value)ReadCBOR(*codec.Reader,int,int)error{return nil}
func(v Value)Clone()Value{return v}
func(v Value)Equal(w Value)bool{return v==w}
`)
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "owner go:generate declaration") {
		t.Fatal("handwritten method coincidence opted in", err)
	}
}

func TestOwnerBootstrapDoesNotInventMethods(t *testing.T) {
	for _, tt := range []struct{ name, source, reason string }{
		{"excluded persistence", "type State struct{private []byte};type Record struct{S State `cbor:\"-\"`};var _=State{}.AppendCBOR(nil)", "AppendCBOR undefined"},
		{"defined underlying", "type Base string;type Record Base;var _=Base(\"\").Clone()", "Clone undefined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := executableFixture(t, tt.source)
			if _, err := Load(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("got %v; want %q", err, tt.reason)
			}
		})
	}
}

func TestImportedPackedValidatorRejected(t *testing.T) {
	cfg := executableFixture(t, `import "github.com/stacklok/mecatl/engine/values";type Record struct{V []values.Byte `+"`cbor:\"v\"`"+`}`)
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "values/value.go"), `package values
//cborgen:validate valid
type Byte uint8
func(Byte)valid()error{return nil}
`)
	registerFixtureOwner(t, cfg, "values", "Byte")
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "packed named-byte") {
		t.Fatal("packed imported byte bypassed validator", err)
	}
}

func TestImportedOwnerMustMatchLoadedSource(t *testing.T) {
	cfg := moduleFixture(t, `import "github.com/stacklok/mecatl/engine/values";type Record struct{V values.Value `+"`cbor:\"v\"`"+`}`)
	module := filepath.Dir(cfg.Dir)
	root := filepath.Dir(module)
	writeFixture(t, filepath.Join(module, "go.mod"), "module example.invalid/consumer\n\ngo 1.27.3\n\nrequire github.com/stacklok/mecatl/engine v0.0.0\nreplace github.com/stacklok/mecatl/engine => ../upstream\n")
	for _, dir := range []string{"registered", "upstream"} {
		writeFixture(t, filepath.Join(root, dir, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\n")
		writeFixture(t, filepath.Join(root, dir, "values/value.go"), "package values\ntype Value string\n")
	}
	writeFixture(t, filepath.Join(root, "upstream/kernel/codec/codec.go"), "package codec\ntype Budget struct{}\ntype Reader struct{}\n")
	writeFixture(t, filepath.Join(root, "registered/values/registry.go"), "//go:generate go -C ../.. run "+generatorCommand+" -dir registered/values -types Value -out zz_cbor_generated.go\npackage values\n")
	if _, err := Load(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "not the imported source package") {
		t.Fatal("unrelated owner source granted provenance", err)
	}
}

func TestFormerRootHelperNamesAvailable(t *testing.T) {
	cfg := executableFixture(t, `type Record string
var CheckRecord,EncodeRecord,DecodeRecord bool
`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	runOwner(t, cfg, "")
}
