package cborgen

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateReachableVocabulary(t *testing.T) {
	cfg := executableFixture(t, `
import "github.com/stacklok/mecatl/engine/values"
type Alias = values.Text
type Signed int32
type Flag bool
type Fixed [2]values.Octet
type List []map[values.Key]*values.Detail
type NamedMap map[values.Key]List
type Hidden struct { private map[string][]byte }
type Record struct {
 Text Alias `+"`cbor:\"text\"`"+`
 Flag Flag `+"`cbor:\"flag\"`"+`
 Fixed Fixed `+"`cbor:\"fixed\" cborlimit:\"bytes=2\"`"+`
 Signed Signed `+"`cbor:\"signed\"`"+`
 Maps NamedMap `+"`cbor:\"maps\"`"+`
 Anonymous struct { Bytes []values.Octet `+"`cbor:\"bytes\"`"+` } `+"`cbor:\"anonymous\"`"+`
 Excluded Hidden `+"`cbor:\"-\"`"+`
}
type Empty struct{}
type v []byte
type cap map[string]v
type limits struct { P cap `+"`cbor:\"p\"`"+` }
type Excluded struct { state Hidden `+"`cbor:\"-\"`"+` }
`)
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "values/value.go"), `package values
type Octet uint8
type Text string
type Key string
type Detail struct { Bytes []Octet `+"`cbor:\"bytes\"`"+` }
`)
	cfg.Types = []string{"Record", "Empty", "Excluded", "limits"}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("testing";"github.com/stacklok/mecatl/engine/kernel/codec";"github.com/stacklok/mecatl/engine/values")
func TestVocabulary(t *testing.T){
 l:=codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:100,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}
 v:=Record{Text:"é",Flag:true,Fixed:Fixed{1,255},Signed:-2147483648,Maps:NamedMap{"outer":{{"inner":{Bytes:[]values.Octet{0,255}}}}}}
 v.Anonymous.Bytes=[]values.Octet{1,2}
 data,err:=EncodeRecord(v,l);if err!=nil{t.Fatal(err)};w,err:=DecodeRecord(data,l);if err!=nil||!v.Equal(w){t.Fatal("roundtrip",w,err)}
 v.Excluded.private=map[string][]byte{"x":{1}};w=v.Clone();w.Excluded.private["x"][0]=2;w.Maps["outer"][0]["inner"].Bytes[0]=9
 if v.Excluded.private["x"][0]!=1||v.Maps["outer"][0]["inner"].Bytes[0]!=0||v.Equal(w){t.Fatal("nested mutable graph")}
 x:=Excluded{state:Hidden{private:map[string][]byte{"x":{1}}}};y:=x.Clone();y.state.private["x"][0]=2;if x.Equal(y){t.Fatal("private excluded state")}
 b,err:=EncodeExcluded(x,l);if err!=nil||len(b)!=1||b[0]!=0xa0{t.Fatal("all excluded",b,err)}
 if _,err:=DecodeEmpty([]byte{0xa0},l);err!=nil{t.Fatal("empty",err)}
 shadow:=limits{P:cap{"x":{1,2}}};b,err=Encodelimits(shadow,l);if err!=nil{t.Fatal(err)}
 decoded,err:=Decodelimits(b,l);if err!=nil||!decoded.Equal(shadow)||!decoded.Clone().Equal(shadow){t.Fatal("helper local names shadowed owner types",err)}
}
`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	runOwner(t, cfg, "")
}

func TestImportedInaccessibleNamedTypeRejected(t *testing.T) {
	cfg := moduleFixture(t, "import \"github.com/stacklok/mecatl/engine/values\";type Record struct { V values.Value `cbor:\"v\"` }")
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "values/value.go"), "package values\ntype hidden uint8\ntype Value struct { Bytes []hidden `cbor:\"bytes\"` }")
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "inaccessible imported named type") {
		t.Fatalf("got %v; want inaccessible imported named type", err)
	}
}
