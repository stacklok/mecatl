package dtogen

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRecursivePublicValidationUsesImmediateCodecHooks(t *testing.T) {
	cfg := executableFixture(t, `
import "errors"
var visits []string
//dtogen:validate validateLocal
type Child struct { Data []byte `+"`cbor:\"d\"`"+` }
func (v Child) validateLocal() error { visits=append(visits,"child");return nil }
func (v Child) Validate() error { visits=append(visits,"public child");return v.validateLocal() }
//dtogen:validate validateLocal
type Record struct { Children []Child `+"`cbor:\"c\"`"+`; Bad bool `+"`cbor:\"b\"`"+` }
func (v Record) validateLocal() error {
 visits=append(visits,"parent")
 if v.Bad {return errors.New("invalid parent")};return nil
}
func (v Record) Validate() error {
 visits=append(visits,"public parent")
 if err:=v.validateLocal();err!=nil{return err}
 for _,c:=range v.Children {if err:=c.Validate();err!=nil{return err}};return nil
}
`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(cfg.Dir, "validation_test.go"), `package owner
import("errors";"reflect";"testing";"github.com/stacklok/mecatl/engine/kernel/codec")
func TestValidationPaths(t *testing.T) {
 l:=codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:4,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}
 v:=Record{Children:[]Child{{Data:[]byte{1}},{Data:[]byte{2}}}}
 visits=nil
 if err:=v.Validate();err!=nil||!reflect.DeepEqual(visits,[]string{"public parent","parent","public child","child","public child","child"}) {t.Fatal("public recursive control",visits,err)}
 visits=nil
 b,err:=codec.Encode(v,l)
 if err!=nil||!reflect.DeepEqual(visits,[]string{"child","child","parent"}) {t.Fatal("encode must validate each node once, child first",visits,err)}
 visits=nil
 if got,err:=codec.Decode[Record](b,l);err!=nil||!got.Equal(v)||!reflect.DeepEqual(visits,[]string{"child","child","parent"}) {t.Fatal("decode must validate each node once, child first",visits,err)}
 v.Bad=true;v.Children[1].Data=make([]byte,5)
 visits=nil
 if out,err:=codec.Encode(v,l);out!=nil||!errors.Is(err,codec.ErrLimitExceeded)||!reflect.DeepEqual(visits,[]string{"child"}) {t.Fatal("encode bounds before local hooks",visits,err)}
 visits=nil
 if got,err:=codec.Decode[Record](v.AppendCBOR(nil),l);!reflect.DeepEqual(got,Record{})||!errors.Is(err,codec.ErrLimitExceeded)||!reflect.DeepEqual(visits,[]string{"child"}) {t.Fatal("decode bounds before local hooks",visits,err)}
 l.MaxByteStringBytes=5
 visits=nil
 if out,err:=codec.Encode(v,l);out!=nil||!errors.Is(err,codec.ErrInvalidData)||!reflect.DeepEqual(visits,[]string{"child","child","parent"}) {t.Fatal("encode invalid local hook",visits,err)}
 visits=nil
 if got,err:=codec.Decode[Record](v.AppendCBOR(nil),l);!reflect.DeepEqual(got,Record{})||!errors.Is(err,codec.ErrInvalidData)||!reflect.DeepEqual(visits,[]string{"child","child","parent"}) {t.Fatal("decode invalid local hook",visits,err)}
}
`)
	runOwner(t, cfg, "")
}
