package dtogen

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyArtifactMigrationIsNarrow(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\n")
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"id/zz_cbor_generated.go", true},
		{"kernel/codec/internal/fixture/zz_cbor_generated.go", true},
		{"kernel/codec/internal/fixture/value/zz_cbor_generated.go", true},
		{"kernel/contentid/zz_cbor_generated.go", true},
		{"kernel/history/id/zz_cbor_generated.go", true},
		{"kernel/history/format/zz_cbor_generated.go", true},
		{"kernel/session/format/zz_cbor_generated.go", true},
		{"owner/zz_cbor_generated.go", false},
		{"kernel/session/format/other.go", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path := filepath.Join(root, tc.path)
			prior := legacyGeneratedMarker + "\nnot valid Go"
			writeFixture(t, path, prior)
			data, err := readOutput(path)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v: %v", tc.allowed, err)
			}
			if tc.allowed && string(data) != prior {
				t.Fatal("read changed legacy bytes")
			}
			if err := writeArtifact(path, []byte(GeneratedMarker+"\npackage owner\n")); (err == nil) != tc.allowed {
				t.Fatal("installation broadened migration", err)
			}
		})
	}
	// A familiar suffix in a different module is not the old owner.
	writeFixture(t, filepath.Join(root, "go.mod"), "module example.invalid/other\n\ngo 1.27.3\n")
	path := filepath.Join(root, "id/zz_cbor_generated.go")
	writeFixture(t, path, legacyGeneratedMarker+"\npackage id\n")
	if _, err := readOutput(path); err == nil {
		t.Fatal("wrong module accepted legacy marker")
	}
	writeFixture(t, filepath.Join(root, "go.mod"), "module github.com/stacklok/mecatl/engine\n\ngo 1.27.3\n")
	writeFixture(t, path, "package id\n// "+legacyGeneratedMarker+"\n")
	if _, err := readOutput(path); err == nil {
		t.Fatal("unmarked familiar path accepted")
	}
}

func TestLegacyInventoryAndDeclarations(t *testing.T) {
	for _, marker := range []string{legacyGeneratedMarker, GeneratedMarker} {
		root := t.TempDir()
		writeFixture(t, filepath.Join(root, "removed/orphan.go"), marker+"\npackage removed\n")
		before := repositoryState(t, root)
		if err := GenerateRepository(context.Background(), root, true); err == nil || !strings.Contains(err.Error(), "extra data output") {
			t.Fatal("orphan accepted", err)
		}
		if !reflect.DeepEqual(before, repositoryState(t, root)) {
			t.Fatal("check mutated orphan")
		}
	}
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "owner/doc.go"), "//go:generate go -C .. run ./internal/cborgen/cmd/cborgen -dir owner -types Record -out zz_cbor_generated.go\npackage owner\n")
	if _, err := discoverTargets(root); err == nil || !strings.Contains(err.Error(), "obsolete cborgen declaration") {
		t.Fatal("old declaration silently ignored", err)
	}
	cfg := moduleFixture(t, "//cborgen:validate local\ntype Record string\nfunc(Record)local()error{return nil}")
	if _, err := Load(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "obsolete cborgen directive") {
		t.Fatal("old validator silently ignored", err)
	}
}

func TestClosureFailurePreservesEveryArtifact(t *testing.T) {
	cfg := moduleFixture(t, `import("github.com/stacklok/mecatl/engine/first";"github.com/stacklok/mecatl/engine/last")
type Record struct { A first.Value; Z last.Value }
`)
	cfg.Emit = "clone,equal"
	module := filepath.Dir(cfg.Dir)
	for _, pkg := range []string{"first", "last"} {
		writeFixture(t, filepath.Join(module, pkg, "value.go"), "//go:generate go -C ../.. run ./internal/dtogen/cmd/dtogen -dir engine/"+pkg+" -types Value -emit clone,equal -out zz_data_generated.go\npackage "+pkg+"\ntype Value struct { Data []byte }\n")
	}
	if err := Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	before := repositoryState(t, module)
	writeFixture(t, filepath.Join(module, "first/change.go"), "package first\ntype Extra []byte\n")
	path := filepath.Join(module, "first/value.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, strings.Replace(string(data), "Data []byte", "Data []byte; More Extra", 1))
	writeFixture(t, filepath.Join(module, "last/collision.go"), "package last\nvar dtogen int\n")
	if err := Generate(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "collides with generated dtogen") {
		t.Fatal("late render failure missing", err)
	}
	for path, prior := range before {
		if !strings.HasSuffix(path, "generated.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != prior.data {
			t.Fatalf("failure replaced %s: %v", path, err)
		}
	}
}
