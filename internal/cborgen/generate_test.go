package cborgen

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaRejections(t *testing.T) {
	for _, tt := range []struct{ name, source, reason string }{
		{"int", "type Record struct { V int `cbor:\"v\"` }", "fixed-width"},
		{"uint", "type Record struct { V uint `cbor:\"v\"` }", "fixed-width"},
		{"uintptr", "type Record struct { V uintptr `cbor:\"v\"` }", "fixed-width"},
		{"float", "type Record struct { V float64 `cbor:\"v\"` }", "unsupported scalar"},
		{"interface", "type Record struct { V any `cbor:\"v\"` }", "unsupported shape"},
		{"channel", "type Record struct { V chan bool `cbor:\"v\"` }", "unsupported shape"},
		{"function", "type Record struct { V func() `cbor:\"v\"` }", "unsupported shape"},
		{"array", "type Record struct { V [2]string `cbor:\"v\"` }", "fixed byte arrays"},
		{"key", "type Record struct { V map[uint8]string `cbor:\"v\"` }", "string underlying"},
		{"cycle", "type Record struct { V *Record `cbor:\"v,omitempty\"` }", "recursive"},
		{"excluded cycle", "type Record struct { V []Record `cbor:\"-\"` }", "recursive"},
		{"excluded unsupported", "type Record struct { V func() `cbor:\"-\"` }", "unsupported shape"},
		{"double pointer", "type Record struct { V **string `cbor:\"v\"` }", "double-pointer"},
		{"embedding", "type Child struct{};type Record struct { Child }", "embedding"},
		{"missing tag", "type Record struct { V string }", "missing explicit"},
		{"empty name", "type Record struct { V string `cbor:\",omitempty\"` }", "invalid wire name"},
		{"unknown option", "type Record struct { V string `cbor:\"v,keyasint\"` }", "unknown or repeated"},
		{"repeated option", "type Record struct { V *string `cbor:\"v,omitempty,omitempty\"` }", "unknown or repeated"},
		{"duplicate name", "type Record struct { V string `cbor:\"x\"`;W bool `cbor:\"x\"` }", "duplicate wire"},
		{"duplicate tag", "type Record struct { V string `cbor:\"x\" cbor:\"y\"` }", "duplicate cbor tag"},
		{"duplicate budget tag", "type Record struct { V string `cbor:\"x\" cborlimit:\"bytes=1\" cborlimit:\"bytes=2\"` }", "duplicate cborlimit tag"},
		{"malformed tag", "type Record struct { V string `cbor:x` }", "malformed struct tag"},
		{"invalid UTF8 name", "type Record struct { V string `cbor:\"\\xff\"` }", "invalid wire name"},
		{"optional scalar", "type Record struct { V string `cbor:\"v,omitempty\"` }", "requires pointer"},
		{"wrong cap", "type Record struct { V string `cbor:\"v\" cborlimit:\"items=1\"` }", "option or shape"},
		{"struct cap", "type Record struct { V struct{} `cbor:\"v\" cborlimit:\"items=1\"` }", "option or shape"},
		{"excluded cap", "type Record struct { V string `cbor:\"-\" cborlimit:\"bytes=1\"` }", "budget on excluded"},
		{"excluded subtree cap", "type Child struct { V string `cborlimit:\"bytes=1\"` };type Record struct { V Child `cbor:\"-\"` }", "budget in excluded subtree"},
		{"excluded subtree malformed cap", "type Child struct { V string `cborlimit:bytes=1` };type Record struct { V Child `cbor:\"-\"` }", "malformed struct tag value"},
		{"zero cap", "type Record struct { V string `cbor:\"v\" cborlimit:\"bytes=0\"` }", "nonpositive"},
		{"negative cap", "type Record struct { V string `cbor:\"v\" cborlimit:\"bytes=-1\"` }", "positive decimal"},
		{"overflow cap", "type Record struct { V string `cbor:\"v\" cborlimit:\"bytes=18446744073709551616\"` }", "overflowing"},
		{"expression", "type Record struct { V string `cbor:\"v\" cborlimit:\"bytes=1+2\"` }", "positive decimal"},
		{"empty reference", "type Record struct { V string `cbor:\"v\" cborlimit:\"bytes=@\"` }", "positive decimal"},
		{"bad reference", "type Record struct { V string `cbor:\"v\" cborlimit:\"bytes=@x-y\"` }", "positive decimal"},
		{"repeated cap", "type Record struct { V string `cbor:\"v\" cborlimit:\"bytes=1,bytes=2\"` }", "positive decimal"},
		{"marshal", "type Record struct{};func (Record) MarshalCBOR() ([]byte,error) {return nil,nil}", "custom marshal"},
		{"pointer cbor hook", "type Child string;func (*Child) UnmarshalCBOR([]byte) error{return nil};type Record struct { V Child `cbor:\"v\"` }", "custom marshal"},
		{"pointer streaming cbor hook", "import \"io\";type Child string;func (*Child) UnmarshalCBOR(io.Reader) error{return nil};type Record struct { V Child `cbor:\"v\"` }", "custom marshal"},
		{"aliased streaming cbor reader", "import \"io\";type Reader = io.Reader;type Child string;func (*Child) UnmarshalCBOR(Reader) error{return nil};type Record struct { V Child `cbor:\"v\"` }", "custom marshal"},
		{"aliased streaming cbor writer", "import \"io\";type Writer = io.Writer;type Child string;func (Child) MarshalCBOR(Writer) error{return nil};type Record struct { V Child `cbor:\"v\"` }", "custom marshal"},
		{"pointer text hook", "type Child string;func (*Child) UnmarshalText([]byte) error{return nil};type Record struct { V Child `cbor:\"v\"` }", "custom marshal"},
		{"binary hook", "type Child []byte;func (Child) MarshalBinary() ([]byte,error){return nil,nil};type Record struct { V Child `cbor:\"v\"` }", "custom marshal"},
		{"generic reachable", "type Child[T any] struct {V T};type Record struct { V Child[bool] `cbor:\"v\"` }", "generic types"},
		{"generic alias reachable", "type Child[T any] = []T;type Record struct { V Child[bool] `cbor:\"v\"` }", "generic types"},
		{"generic alias chain", "type Generic[T any] = []T;type Plain = Generic[bool];type Record struct { V Plain `cbor:\"v\"` }", "generic types"},
		{"shadowed primitive", "type string uint64;type Record struct{}", "shadows predeclared string"},
		{"shadowed builtin", "func len(any) int {panic(\"must never run\")};type Record struct{}", "shadows predeclared len"},
		{"private implementation collision", "type Record struct{};var cborgen int", "collides with generated cborgen"},
		{"private alias collision", "type v []byte;type Record struct { V v `cbor:\"v\"` };var cborgenValue1 int", "collides with generated cborgenValue1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := moduleFixture(t, tt.source)
			prior := GeneratedMarker + "\nbroken old output"
			path := filepath.Join(cfg.Dir, cfg.Output)
			writeFixture(t, path, prior)
			err := Generate(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("got %v; want %q", err, tt.reason)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != prior {
				t.Fatal("generation failure changed prior output", err)
			}
		})
	}
}

func TestGenerateOutputFailurePreservesArtifact(t *testing.T) {
	cfg := moduleFixture(t, "type Record struct{}")
	path := filepath.Join(cfg.Dir, cfg.Output)
	prior := GeneratedMarker + "\nbroken old output"
	writeFixture(t, path, prior)
	loaded, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := render(loaded); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(cfg.Dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	// Directory permissions forbid replacement but leave the existing file writable.
	// Direct destination writes would succeed here and destroy the prior artifact.
	probe, err := os.CreateTemp(cfg.Dir, ".permission-probe-*")
	if err == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("filesystem permits creation despite read-only directory permissions")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Generate(context.Background(), cfg); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("generation error = %v; want output permission failure", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != prior {
		t.Fatal("output failure changed prior artifact", err)
	}
	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".cborgen-") {
			t.Fatalf("temporary artifact leaked: %s", entry.Name())
		}
	}
}

func TestImportedPrivateFieldRejected(t *testing.T) {
	cfg := moduleFixture(t, "import \"github.com/stacklok/mecatl/engine/hidden\";type Record struct { V hidden.Value `cbor:\"-\"` }")
	writeFixture(t, filepath.Join(filepath.Dir(cfg.Dir), "hidden/value.go"), "package hidden\ntype Value struct { private []byte }")
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "owner go:generate declaration") {
		t.Fatal(err)
	}
}

// executableFixture supplies the real runtime source and a pinned offline module,
// not a fake runtime API. The root tool itself has no engine import/dependency.
func executableFixture(t *testing.T, source string) LoadConfig {
	t.Helper()
	cfg := moduleFixture(t, source)
	module := filepath.Dir(cfg.Dir)
	for _, pkg := range []string{"codec", "contentid"} {
		runtimeDir, err := filepath.Abs("../../engine/kernel/" + pkg)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(runtimeDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(runtimeDir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(module, "kernel", pkg, entry.Name()), string(data))
		}
	}
	writeFixture(t, filepath.Join(module, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\n\nrequire github.com/jcalabro/atmos v0.8.0\n")
	sum, err := os.ReadFile("../../engine/go.sum")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(module, "go.sum"), string(sum))
	return cfg
}

func registerFixtureOwner(t *testing.T, cfg LoadConfig, pkg string, names ...string) LoadConfig {
	t.Helper()
	root := filepath.Dir(filepath.Dir(cfg.Dir))
	dir := filepath.Join(filepath.Dir(cfg.Dir), pkg)
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	back, err := filepath.Rel(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "value.go"))
	if err != nil {
		t.Fatal(err)
	}
	packageName := strings.Fields(string(data))[1]
	writeFixture(t, filepath.Join(dir, "registry.go"), "//go:generate go -C "+back+" run "+generatorCommand+" -dir "+rel+" -types "+strings.Join(names, ",")+" -out zz_cbor_generated.go\npackage "+packageName+"\n")
	owner := LoadConfig{Dir: dir, Types: names, Output: "zz_cbor_generated.go"}
	if err := Generate(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	return owner
}

func runOwner(t *testing.T, cfg LoadConfig, arch string) {
	t.Helper()
	args := []string{"test", "-mod=readonly", "./..."}
	if arch != "" {
		// Cross-compile without assuming the host can execute another ISA.
		args = []string{"test", "-mod=readonly", "-c", "-o", filepath.Join(t.TempDir(), "owner.test"), "./owner"}
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = filepath.Dir(cfg.Dir)
	cmd.Env = loadEnvironment()
	if arch != "" {
		cmd.Env = append(cmd.Env, "GOARCH="+arch)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compiled owner: %v\n%s", err, out)
	}
}

func TestGenerateExecutableOwnerAndDeterminism(t *testing.T) {
	cfg := executableFixture(t, `
import "github.com/stacklok/mecatl/engine/kernel/codec"
type Byte uint8
type Bytes []Byte
type Ptr = *Bytes
type Record struct {
 V map[Key][]*Child `+"`cbor:\"v\" cborlimit:\"items=5\"`"+`
 P Ptr `+"`cbor:\"p\" cborlimit:\"bytes=18446744073709551615\"`"+`
 X struct { Data []byte } `+"`cbor:\"-\"`"+`
}
type Key string
type Child struct { S string `+"`cbor:\"s\" cborlimit:\"bytes=5\"`"+` }
func (Child) MarshalText(prefix string) string { panic("custom MarshalText invoked") }
func (Child) Validate() error { panic("custom Validate invoked") }
type Other struct { V bool `+"`cbor:\"v\"`"+` }
func wrappers(v Record,l codec.Limits) bool {b,e:=codec.Encode(v.Clone(),l);if e!=nil{return false};w,e:=codec.Decode[Record](b,l);return e==nil&&codec.Check(w,l)==nil&&v.Equal(w)}
`)
	cfg.Types = []string{"Record", "Other", "Record"}
	writeFixture(t, filepath.Join(cfg.Dir, "owner_test.go"), `package owner
import("testing";"github.com/stacklok/mecatl/engine/kernel/codec")
func TestOwner(t *testing.T){
 l:=codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:100,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}
 b:=Bytes{1,2};v:=Record{P:&b,V:map[Key][]*Child{"x":{{S:"a"}}}}
 if !wrappers(v,l){t.Fatal("real generated entry points")}
 v.X.Data=[]byte{1};w:=v.Clone();w.X.Data[0]=2;(*w.P)[0]=9;w.V["x"][0].S="b"
 if v.X.Data[0]!=1||(*v.P)[0]!=1||v.V["x"][0].S!="a"||v.Equal(w){t.Fatal("clone alias/equal")}
 if _,e:=codec.Encode(Other{},l);e!=nil{t.Fatal(e)}
}
`)
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.Dir, cfg.Output)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(first), "NamedLimit") || strings.Contains(string(first), "func EncodeRecord") {
		t.Fatal("obsolete root helpers or named policy emitted")
	}
	runOwner(t, cfg, "")
	ownerPath := filepath.Join(cfg.Dir, "owner.go")
	owner, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	declaration := "type Other struct { V bool `cbor:\"v\"` }\n"
	if strings.Count(string(owner), declaration) != 1 {
		t.Fatal("missing declaration-order control")
	}
	reordered := strings.Replace(string(owner), declaration, "", 1)
	reordered = strings.Replace(reordered, "type Byte uint8", declaration+"type Byte uint8", 1)
	writeFixture(t, ownerPath, reordered)
	cfg.Types = []string{"Other", "Record"}
	t.Setenv("GOOS", "linux")
	t.Setenv("GOARCH", "386")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil || string(first) != string(second) {
		t.Fatal("generation depends on roots/architecture", err)
	}
	runOwner(t, cfg, "386")
	// Both function and method handwritten wrappers survive broken owned output.
	writeFixture(t, path, GeneratedMarker+"\nsyntactically broken")
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	runOwner(t, cfg, "386")
}
