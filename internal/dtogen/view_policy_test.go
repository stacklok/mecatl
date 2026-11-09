package dtogen

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestViewContextChildShapes(t *testing.T) {
	for _, shape := range []string{"Child", "*Child", "[]Child", "*[]Child", "[]*Child"} {
		t.Run(shape, func(t *testing.T) {
			cfg := viewFixture(t, "view")
			path := filepath.Join(cfg.Dir, "owner.go")
			source := `package owner
 type Record struct { Item ` + shape + ` }
 type Child struct { Label string }
 type RecordView struct { value *Record }
 type ChildView struct { value *Child; scope string }
 `
			writeFixture(t, path, source)
			// Even a broken preexisting output must survive a policy failure.
			writeFixture(t, filepath.Join(cfg.Dir, cfg.Output), GeneratedMarker+"\nnot Go")
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "no context-free") {
				t.Fatalf("context child accepted: %v", err)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("failed context policy changed artifacts")
			}
			// Manual navigation may choose its semantic shape and retain scope.
			writeFixture(t, path, strings.Replace(source, "Item "+shape, "Item "+shape+" `view:\"manual\"`", 1)+`
 func(v RecordView) Item() ChildView { return ChildView{scope:"retained"} }
 `)
			if err := Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestViewExplicitSelectionAndAmbiguity(t *testing.T) {
	cfg := viewFixture(t, "view")
	path := filepath.Join(cfg.Dir, "owner.go")
	source := readFixture(t, path)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		types       []string
		extra, want string
	}{
		{"unselected child", []string{"RecordView"}, "", "no context-free"},
		{"ambiguous", []string{"RecordView", "ChildView", "OtherView"}, "type OtherView struct {value *Child}", "ambiguous context-free"},
		{"shared manual policy", []string{"RecordView", "ChildView", "OtherView"}, "type OtherView struct {value *Record; scope string}", "missing directly declared"},
		{"embedded", []string{"RecordView", "ChildView"}, "", "private nonembedded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := source + "\n" + tc.extra
			if tc.name == "embedded" {
				changed = strings.Replace(changed, "scope string", "ChildView", 1)
			}
			writeFixture(t, path, changed)
			selected := cfg
			selected.Types = tc.types
			before := repositoryState(t, filepath.Dir(cfg.Dir))
			if err := Generate(context.Background(), selected); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if !reflect.DeepEqual(before, repositoryState(t, filepath.Dir(cfg.Dir))) {
				t.Fatal("failure changed artifacts")
			}
		})
	}
	// One context-free and one contextual handle share a DTO without ambiguity.
	writeFixture(t, path, source+`
 type ScopedChildView struct {value *Child; scope string}
 func(v ScopedChildView) ID() string {return v.scope}
 `)
	cfg.Types = []string{"ScopedChildView", "RecordView", "ChildView", "RecordView"}
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	before := readFixture(t, filepath.Join(cfg.Dir, cfg.Output))
	cfg.Types = []string{"ChildView", "RecordView", "ScopedChildView"}
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if before != readFixture(t, filepath.Join(cfg.Dir, cfg.Output)) {
		t.Fatal("selection order/repetition changed output")
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
 import "testing"
 func TestScopedPair(t *testing.T) { r:=Record{Child:Child{Label:"child"}};v:=RecordView{value:&r};s:=ScopedChildView{value:&r.Child,scope:"scope"};if v.Child().Label()!=s.Label()||s.ID()!="scope"{t.Fatal("adapter/context pair")}}
 `)
	runOwner(t, cfg, "")
}

func TestViewTagsDoNotChangeValueCapabilities(t *testing.T) {
	cfg := executableFixture(t, `type Record struct {
 Data []byte `+"`cbor:\"d\" view:\"manual\"`"+`
 Hidden []byte `+"`cbor:\"h\" view:\"-\"`"+`
 Future string `+"`cbor:\"f\" view:\"future\" view:\"-\"`"+`
 }`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
 import("testing";"github.com/stacklok/mecatl/engine/kernel/codec")
 func TestIndependent(t *testing.T){
 v:=Record{Data:[]byte{1},Hidden:[]byte{2},Future:"kept"};w:=v.Clone();if !w.Equal(v){t.Fatal("equal")};w.Hidden[0]++;w.Data[0]++;if v.Data[0]!=1||v.Hidden[0]!=2||v.Equal(w){t.Fatal("ownership")}
 l:=codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:100,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}
 data,err:=codec.Encode(v,l);if err!=nil{t.Fatal(err)};out,err:=codec.Decode[Record](data,l);if err!=nil||!out.Equal(v){t.Fatal("codec",err)}
 }
 `)
	runOwner(t, cfg, "")
}
