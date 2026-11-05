package cborgen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIDSchemaRejections(t *testing.T) {
	for _, tt := range []struct{ name, source, reason string }{
		{"wrong length", "type Record struct { binary [35]byte `cbor:\",cid\"` }", "cid requires [36]byte"},
		{"slice", "type Record struct { binary []byte `cbor:\",cid\"` }", "cid requires [36]byte"},
		{"named element", "type Byte uint8;type Record struct { binary [36]Byte `cbor:\",cid\"` }", "cid requires [36]byte"},
		{"named array", "type Binary [36]byte;type Record struct { binary Binary `cbor:\",cid\"` }", "cid requires [36]byte"},
		{"cap", "type Record struct { binary [36]byte `cbor:\",cid\" cborlimit:\"bytes=36\"` }", "cid requires [36]byte"},
		{"other field", "type Record struct { binary [36]byte `cbor:\",cid\"`; Other bool `cbor:\"o\"` }", "exactly one persisted"},
		{"two representations", "type Record struct { A [36]byte `cbor:\",cid\"`; B string `cbor:\",value\"` }", "exactly one representation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := executableFixture(t, tt.source)
			prior := GeneratedMarker + "\nbroken old output"
			path := filepath.Join(cfg.Dir, cfg.Output)
			writeFixture(t, path, prior)
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("got %v; want %q", err, tt.reason)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != prior {
				t.Fatal("failure changed output", err)
			}
		})
	}
}

func TestCIDIdentityAndCompiledOwner(t *testing.T) {
	cfg := executableFixture(t, `
import "github.com/stacklok/mecatl/engine/kernel/contentid"
//cborgen:validate valid
// Link owns an explicit fixed-profile representation.
type Link struct { binary [36]byte `+"`cbor:\",cid\"`"+`; cache []byte `+"`cbor:\"-\"`"+` }
func (v Link) valid() error {_,err:=contentid.ParseBinary(v.binary[:]);return err}
type Alias = Link
type Record struct { V Alias `+"`cbor:\"v\"`"+`; P *Link `+"`cbor:\"p,omitempty\"`"+`; X *Link `+"`cbor:\"-\"`"+` }
func init(){panic("owner initialization executed")}
`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	first := assertHelperNamespace(t, cfg, false)
	if strings.Count(first, "return b.CheckCIDLink(d)") != 1 || strings.Count(first, "r.CIDLink(d)") != 1 {
		t.Fatal("missing raw link composition")
	}
	writeFixture(t, filepath.Join(cfg.Dir, cfg.Output), GeneratedMarker+"\nnot Go")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := assertHelperNamespace(t, cfg, false); got != first {
		t.Fatal("nondeterministic regeneration")
	}
	path := filepath.Join(cfg.Dir, "owner.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, strings.Replace(string(source), `func init(){panic("owner initialization executed")}`, "", 1))
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("testing";"bytes";"errors";"github.com/stacklok/mecatl/engine/kernel/codec";"github.com/stacklok/mecatl/engine/kernel/contentid")
func TestCID(t *testing.T){
 l:=codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:100,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}
 c:=Link{binary:contentid.Sum([]byte("fixture")).Binary()};v:=Record{V:c,P:&c}
 b,e:=codec.Encode(v,l);if e!=nil||codec.Check(v,l)!=nil{t.Fatal(e)}
 w,e:=codec.Decode[Record](b,l);if e!=nil||!v.Equal(w){t.Fatal("inferred consumer",e)}
 if !bytes.Contains(b,append([]byte{0xd8,42,0x58,37,0},c.binary[:]...)){t.Fatal("framing")}
 v.X=&Link{cache:[]byte{1}};w=v.Clone();w.X.cache[0]=2
 if v.X.cache[0]!=1||v.Equal(w)||v.P==w.P{t.Fatal("excluded state clone")}
 if _,e:=codec.Encode(Record{},l);!errors.Is(e,codec.ErrInvalidData){t.Fatal("profile validator",e)}
 r,e:=codec.NewReader([]byte{0xa1,0x61,0x76,0xf6},l);if e!=nil{t.Fatal(e)}
 before:=v.Clone();if e:=v.ReadCBOR(&r,0,100);e==nil||!v.Equal(before){t.Fatal("transactional receiver",e)}
}
`)
	runOwner(t, cfg, "")
}

func TestCIDLookalikePrivateStorageRejected(t *testing.T) {
	cfg := executableFixture(t, `import "github.com/stacklok/mecatl/engine/lookalike";type Record struct { V lookalike.CID `+"`cbor:\"-\"`"+` }`)
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "lookalike/cid.go"), "package lookalike\ntype CID struct { binary [36]byte }\n")
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "owner go:generate declaration") {
		t.Fatal("unregistered lookalike accepted", err)
	}
}
