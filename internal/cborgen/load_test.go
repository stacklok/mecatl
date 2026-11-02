package cborgen

import (
	"context"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func isolateGoEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"GOWORK": "off", "GOENV": "off", "GOFLAGS": "-mod=readonly",
		"GOPACKAGESDRIVER": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off",
	} {
		t.Setenv(key, value)
	}
}

func moduleFixture(t *testing.T, source string) LoadConfig {
	t.Helper()
	isolateGoEnvironment(t)
	parent := t.TempDir()
	// A nested module must win over the parent module (and the harness's pinned
	// published engine dependency). No require/replace or engine import is needed.
	writeFixture(t, filepath.Join(parent, "go.mod"), "module example.invalid/parent\n\ngo 1.27.3\n")
	module := filepath.Join(parent, "engine")
	writeFixture(t, filepath.Join(module, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\n")
	writeFixture(t, filepath.Join(module, "kernel/codec/limits.go"), "package codec\ntype Limits struct { MaxBytes int }\n")
	dir := filepath.Join(module, "owner")
	writeFixture(t, filepath.Join(dir, "owner.go"), "package owner\n"+source)
	return LoadConfig{Dir: dir, Types: []string{"Record"}, Output: "zz_cbor_generated.go"}
}

const callers = `
import "github.com/stacklok/mecatl/engine/kernel/codec"
type Record struct { Value string ` + "`cbor:\"value\"`" + ` }
// Loading must never initialize the owner package.
func init() { panic("cborgen executed an owner initialization trap") }
func handwritten(v Record, limits codec.Limits) (Record, error) {
 if err := CheckRecord(v, limits); err != nil { return Record{}, err }
 data, err := EncodeRecord(v.Clone(), limits)
 if err != nil { return Record{}, err }
 other, err := DecodeRecord(data, limits)
 if !other.Equal(v) { return Record{}, err }
 return other, err
}
var _ func(Record, codec.Limits) error = CheckRecord
var _ func(Record, codec.Limits) ([]byte, error) = EncodeRecord
var _ func([]byte, codec.Limits) (Record, error) = DecodeRecord
var _ func(Record) Record = Record.Clone
var _ func(Record, Record) bool = Record.Equal
`

func TestLoadRegenerationSeam(t *testing.T) {
	for name, prior := range map[string]string{
		"missing":       "",
		"broken syntax": GeneratedMarker + "\nthis is not Go; import (",
		"broken types":  GeneratedMarker + "\npackage owner\nvar broken MissingType\n",
		"stale signatures": GeneratedMarker + `
package owner
func CheckRecord() {}
func (v Record) Clone() int { return 0 }
func (v Record) Equal() {}
`,
	} {
		t.Run(name, func(t *testing.T) {
			config := moduleFixture(t, callers)
			config.Types = []string{"Record", "Record"}
			out := filepath.Join(config.Dir, config.Output)
			if prior != "" {
				writeFixture(t, out, prior)
			}
			loaded, err := Load(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Roots) != 1 || loaded.Roots[0].Obj().Name() != "Record" {
				t.Fatalf("roots: %v", loaded.Roots)
			}
			if loaded.Package.Module.Dir != filepath.Dir(config.Dir) {
				t.Fatalf("wrong containing module: %v", loaded.Package.Module)
			}
			if loaded.Package.Types.Path() != "github.com/stacklok/mecatl/engine/owner" || loaded.Output != out {
				t.Fatalf("wrong loaded package/output: %s / %s", loaded.Package.Types.Path(), loaded.Output)
			}
			for _, method := range []string{"Clone", "Equal"} {
				if types.NewMethodSet(loaded.Roots[0]).Lookup(loaded.Package.Types, method) == nil {
					t.Fatalf("missing method %s", method)
				}
			}
			data, err := os.ReadFile(out)
			if prior == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("load created output: %q, %v", data, err)
				}
			} else if err != nil || string(data) != prior {
				t.Fatalf("load changed prior output: %q, %v", data, err)
			}
			// Stand in for the later emitter only in this disposable module. A
			// normal load, with NO overlay, must accept the replacement and all
			// handwritten function and method callers. These are not codec bodies.
			writeFixture(t, out, GeneratedMarker+`
package owner
import "github.com/stacklok/mecatl/engine/kernel/codec"
func CheckRecord(Record, codec.Limits) error { return nil }
func EncodeRecord(Record, codec.Limits) ([]byte, error) { return nil, nil }
func DecodeRecord([]byte, codec.Limits) (Record, error) { return Record{}, nil }
func (v Record) Clone() Record { return v }
func (v Record) Equal(other Record) bool { return v == other }
`)
			if _, err := loadOne(&packages.Config{
				Dir: config.Dir, Env: loadEnvironment(), BuildFlags: []string{"-mod=readonly"},
				Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
			}); err != nil {
				t.Fatalf("replacement without overlay: %v", err)
			}
		})
	}
}

func TestLoadDoesNotHideHandwrittenErrors(t *testing.T) {
	for _, test := range []struct{ name, extra, reason string }{
		{"unrelated function", "func broken() { var n int = \"bad\"; _ = n }", "cannot use"},
		{"schema field", "", "MissingSchemaType"},
		{"method caller", "var _ string = Record{}.Clone()", "cannot use"},
		{"function caller", "var _ = CheckRecord(Record{})", "not enough arguments"},
		{"old root caller", "var _ = CheckRemoved", "undefined: CheckRemoved"},
		{"syntax", "func broken( {", "expected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := callers + "\n" + test.extra
			if test.name == "schema field" {
				source = strings.Replace(source, "Value string", "Value MissingSchemaType", 1)
			}
			config := moduleFixture(t, source)
			prior := GeneratedMarker + "\nnot valid Go"
			out := filepath.Join(config.Dir, config.Output)
			writeFixture(t, out, prior)
			if _, err := Load(context.Background(), config); err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("got %v; want handwritten diagnostic %q", err, test.reason)
			}
			data, err := os.ReadFile(out)
			if err != nil || string(data) != prior {
				t.Fatalf("failure changed prior output: %q, %v", data, err)
			}
		})
	}
}

func TestLoadRootGuardsAndCollisions(t *testing.T) {
	for _, test := range []struct{ name, source, reason string }{
		{"missing", "type Other struct{}", "not a locally declared type"},
		{"variable", "var Record int", "not a locally declared type"},
		{"alias", "type Local struct{}; type Record = Local", "is an alias"},
		{"imported alias", "import \"net/url\"; type Record = url.URL", "is an alias"},
		{"generic", "type Record[T any] struct { Value T }", "is generic"},
		{"scalar", "type Record uint64", "must be a struct"},
		{"function", "type Record struct{}; func CheckRecord() {}", "collides with generated CheckRecord"},
		{"variable name", "type Record struct{}; var EncodeRecord int", "collides with generated EncodeRecord"},
		{"type name", "type Record struct{}; type DecodeRecord int", "collides with generated DecodeRecord"},
		{"clone", "type Record struct{}; func (Record) Clone() Record { return Record{} }", "collides with generated Clone"},
		{"pointer equal", "type Record struct{}; func (*Record) Equal(Record) bool { return true }", "collides with generated Equal"},
		{"field clone", "type Record struct { Clone string }", "field Clone collides"},
		{"field equal", "type Record struct { Equal bool }", "field Equal collides"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := moduleFixture(t, test.source)
			_, err := Load(context.Background(), config)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("got %v; want reason %q", err, test.reason)
			}
		})
	}
}

func TestLoadOnlyOverlaysOwnedOutput(t *testing.T) {
	config := moduleFixture(t, callers)
	writeFixture(t, filepath.Join(config.Dir, "zz_other_generated.go"), GeneratedMarker+"\npackage owner\nvar unrelated MissingOtherType\n")
	_, err := Load(context.Background(), config)
	if err == nil || !strings.Contains(err.Error(), "MissingOtherType") {
		t.Fatalf("sibling generated-file diagnostic lost: %v", err)
	}
}

func TestLoadActiveFilesAndSortedRoots(t *testing.T) {
	config := moduleFixture(t, callers+"\ntype Zulu struct{}\n")
	config.Types = []string{"Zulu", "Record", "Zulu"}
	writeFixture(t, filepath.Join(config.Dir, "aaa_ignored.go"), "//go:build ignore\n\npackage different\nnot valid Go\n")
	loaded, err := Load(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Roots) != 2 || loaded.Roots[0].Obj().Name() != "Record" || loaded.Roots[1].Obj().Name() != "Zulu" {
		t.Fatalf("unsorted roots: %v", loaded.Roots)
	}
}

func TestLoadDependencyErrors(t *testing.T) {
	config := moduleFixture(t, "import \"github.com/stacklok/mecatl/engine/broken\"\ntype Record struct { Value broken.Value }")
	writeFixture(t, filepath.Join(filepath.Dir(config.Dir), "broken/value.go"), "package broken\ntype Value MissingDependencyType\n")
	_, err := Load(context.Background(), config)
	if err == nil || !strings.Contains(err.Error(), "MissingDependencyType") {
		t.Fatalf("dependency diagnostic lost: %v", err)
	}
}

func TestLoadOutputProtection(t *testing.T) {
	for _, test := range []struct{ output, reason string }{
		{"../outside.go", "package-local"}, {"/outside.go", "package-local"},
		{"", "package-local"}, {"output.txt", "package-local"},
		{"output_test.go", "package-local"}, {"_output.go", "package-local"}, {".output.go", "package-local"},
	} {
		t.Run(test.output, func(t *testing.T) {
			config := moduleFixture(t, "type Record struct{}")
			config.Output = test.output
			if _, err := Load(context.Background(), config); err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("got %v; want %s", err, test.reason)
			}
		})
	}
	for _, kind := range []string{"unmarked", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			config := moduleFixture(t, "type Record struct{}")
			out := filepath.Join(config.Dir, config.Output)
			switch kind {
			case "unmarked":
				writeFixture(t, out, "package owner\n// handwritten\n")
			case "symlink":
				target := filepath.Join(t.TempDir(), "target.go")
				writeFixture(t, target, GeneratedMarker+"\npackage owner\n")
				if err := os.Symlink(target, out); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(context.Background(), config); err == nil {
				t.Fatal("unsafe output accepted")
			}
		})
	}
}

func TestLoadBuildContext(t *testing.T) {
	config := moduleFixture(t, callers)
	poison := filepath.Join(t.TempDir(), "poison")
	writeFixture(t, poison, "not valid Go configuration")
	t.Setenv("GOWORK", poison) // Only the containing-module choice overrides ambient context.
	t.Setenv("GOFLAGS", "-mod=readonly -tags=codecfixture")
	writeFixture(t, filepath.Join(config.Dir, "owner.go"), "//go:build codecfixture\n\npackage owner\n"+callers)
	writeFixture(t, filepath.Join(config.Dir, "aaa_inactive.go"), "//go:build !codecfixture\n\npackage inactive\nnot valid Go\n")
	writeFixture(t, filepath.Join(config.Dir, "tagged.go"), "//go:build codecfixture\n\npackage owner\ntype Tagged struct {}\n")
	config.Types = append(config.Types, "Tagged")
	if _, err := Load(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-definitely-invalid-go-flag")
	if _, err := Load(context.Background(), config); err == nil || !strings.Contains(err.Error(), "definitely-invalid-go-flag") {
		t.Fatalf("build flag diagnostic hidden: %v", err)
	}
}

func TestLoadEnvironmentPreservesChecksumPolicy(t *testing.T) {
	for _, value := range []string{"sum.golang.org", "sum.golang.org https://sum.golang.org"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GOSUMDB", value)
			if !slices.Contains(loadEnvironment(), "GOSUMDB="+value) {
				t.Fatal("loader replaced explicit checksum policy")
			}
		})
	}
	t.Setenv("GOSUMDB", "") // Register restoration before testing an absent setting.
	if err := os.Unsetenv("GOSUMDB"); err != nil {
		t.Fatal(err)
	}
	for _, setting := range loadEnvironment() {
		if strings.HasPrefix(setting, "GOSUMDB=") {
			t.Fatalf("loader replaced default checksum policy: %s", setting)
		}
	}
}

func TestTestEnvironmentControlsPoisonedAmbient(t *testing.T) {
	for _, key := range []string{"GOWORK", "GOENV", "GOFLAGS", "GOPACKAGESDRIVER"} {
		t.Setenv(key, "/invalid/ambient")
	}
	config := moduleFixture(t, callers) // Test setup, not production, isolates ambient Go settings.
	if _, err := Load(context.Background(), config); err != nil {
		t.Fatal(err)
	}
}
