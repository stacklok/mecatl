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
		{"defined", "type Other contentid.CID; type Record struct { V Other `cbor:\"v\"` }", "inaccessible field"},
		{"pointer defined", "type Other contentid.CID; type Record struct { V *Other `cbor:\"v\"` }", "inaccessible field"},
		{"bytes limit", "type Record struct { V contentid.CID `cbor:\"v\" cborlimit:\"bytes=37\"` }", "invalid budget option or shape"},
		{"alias limit", "type Alias = contentid.CID; type Record struct { V Alias `cbor:\"v\" cborlimit:\"bytes=37\"` }", "invalid budget option or shape"},
		{"pointer limit", "type Record struct { V *contentid.CID `cbor:\"v,omitempty\" cborlimit:\"bytes=@link\"` }", "invalid budget option or shape"},
		{"items limit", "type Record struct { V contentid.CID `cbor:\"v\" cborlimit:\"items=1\"` }", "invalid budget option or shape"},
		{"optional value", "type Record struct { V contentid.CID `cbor:\"v,omitempty\"` }", "omitempty requires pointer"},
		{"array", "type Record struct { V [1]contentid.CID `cbor:\"v\"` }", "fixed byte arrays"},
		{"map key", "type Record struct { V map[contentid.CID]bool `cbor:\"v\"` }", "string underlying"},
		{"import collision", "type Record struct { V contentid.CID `cbor:\"v\"` };var cborgenType2 bool", "collides with generated cborgenType2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := executableFixture(t, "import \"github.com/stacklok/mecatl/engine/kernel/contentid\";"+tt.source)
			prior := GeneratedMarker + "\nbroken old output"
			path := filepath.Join(cfg.Dir, cfg.Output)
			writeFixture(t, path, prior)
			if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("got %v; want %q", err, tt.reason)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != prior {
				t.Fatal("failed generation changed output", err)
			}
		})
	}
}

func TestCIDIdentityAndCompiledOwner(t *testing.T) {
	cfg := executableFixture(t, `
import v "github.com/stacklok/mecatl/engine/kernel/contentid"
import "github.com/stacklok/mecatl/engine/lookalike"
type Alias = v.CID
type AliasChain = Alias
type CID string
type Wrapped struct { Link AliasChain `+"`cbor:\"l\"`"+` }
type Record struct {
 V AliasChain `+"`cbor:\"v\"`"+`
 P *Alias `+"`cbor:\"p,omitempty\"`"+`
 X *Alias `+"`cbor:\"-\"`"+`
 S CID `+"`cbor:\"s\"`"+`
 W Wrapped `+"`cbor:\"w\"`"+`
 L lookalike.CID `+"`cbor:\"l\"`"+`
}
func init(){panic("owner initialization executed")}
`)
	trapPath := filepath.Join(cfg.Dir, "owner.go")
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "lookalike/cid.go"), "package lookalike\ntype CID struct { Flag bool `cbor:\"f\"` }\n")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	first := assertHelperNamespace(t, cfg, false)
	if strings.Count(first, "return b.CheckCID(v, d)") != 1 || strings.Count(first, "return r.CID(d)") != 1 {
		t.Fatal("aliases did not share the exact CID leaf")
	}
	// Regeneration must recover broken output without executing owner init.
	writeFixture(t, filepath.Join(cfg.Dir, cfg.Output), GeneratedMarker+"\nnot Go")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if got := assertHelperNamespace(t, cfg, false); got != first {
		t.Fatal("CID regeneration is not deterministic")
	}
	owner, err := os.ReadFile(trapPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, trapPath, strings.Replace(string(owner), `func init(){panic("owner initialization executed")}`, "", 1))
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("testing";"bytes";"github.com/stacklok/mecatl/engine/kernel/codec";"github.com/stacklok/mecatl/engine/kernel/contentid")
func TestCID(t *testing.T){
 l:=codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:100,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}
 c:=contentid.Sum([]byte("fixture"));v:=Record{V:c,P:&c,S:"ordinary",W:Wrapped{Link:c}}
 b,e:=EncodeRecord(v,l);if e!=nil||CheckRecord(v,l)!=nil{t.Fatal(e)}
 w,e:=DecodeRecord(b,l);if e!=nil||!v.Equal(w){t.Fatal("roundtrip",e)}
 if !bytes.Contains(b,[]byte{0x68,'o','r','d','i','n','a','r','y'})||!bytes.Contains(b,[]byte{0x61,'l',0xa1,0x61,'f',0xf4}){t.Fatal("non-CID shapes treated as links")}
 v.X=&c;w=v.Clone();if !v.Equal(w)||w.P==v.P||w.X==v.X{t.Fatal("pointer clone")};*w.P=contentid.CID{};if *v.P!=c||v.Equal(w){t.Fatal("aliased clone")}
 var z Record;if !z.Equal(z.Clone()){t.Fatal("zero clone/equal")}
}
`)
	runOwner(t, cfg, "")
}

func TestCIDAliasRootRejected(t *testing.T) {
	cfg := executableFixture(t, `import "github.com/stacklok/mecatl/engine/kernel/contentid";type Alias = contentid.CID`)
	cfg.Types = []string{"Alias"}
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatal("CID alias became a root schema", err)
	}
}

func TestCIDLookalikePrivateStorageRejected(t *testing.T) {
	cfg := executableFixture(t, `import "github.com/stacklok/mecatl/engine/lookalike";type Record struct { V lookalike.CID `+"`cbor:\"-\"`"+` }`)
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "lookalike/cid.go"), "package lookalike\ntype CID struct { binary [36]byte }\n")
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "inaccessible field") {
		t.Fatal("look-alike received CID treatment", err)
	}
}
