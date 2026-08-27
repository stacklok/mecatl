package jsonlstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreNormalizesRootAndConfinesInventoryNames(t *testing.T) {
	base := t.TempDir()
	st, err := New(filepath.Join(base, "nested", "..", "store"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Lexical normalization only — symlinks in the operator's path survive, so the
	// expectation must not be run through filepath.EvalSymlinks.
	wantRoot := filepath.Join(base, "store")
	if st.resolver.dir != wantRoot {
		t.Fatalf("normalized store root = %q, want %q", st.resolver.dir, wantRoot)
	}

	catalogDir := st.inventoryCatalogDir()
	temporary, err := os.CreateTemp(catalogDir, ".inventory-test-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(temporary.Name()) })
	if got, err := st.inventoryCatalogRelativePath(temporary.Name()); err != nil || got != filepath.Base(temporary.Name()) {
		t.Fatalf("temporary inventory basename = %q, %v; want %q, nil", got, err, filepath.Base(temporary.Name()))
	}

	for _, path := range []string{
		filepath.Join(catalogDir, "nested", "artifact"),
		filepath.Join(base, "outside"),
	} {
		if _, err := st.inventoryCatalogRelativePath(path); err == nil {
			t.Errorf("inventoryCatalogRelativePath(%q) succeeded outside catalog direct descendants", path)
		}
	}
}

func TestStoreRejectsEmptyRoot(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(\"\") succeeded, want error")
	}
}

func TestStorePreservesConfiguredRootSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir target: %v", err)
	}
	configured := filepath.Join(base, "configured")
	if err := os.Symlink(target, configured); err != nil {
		t.Fatalf("Symlink configured root: %v", err)
	}
	st, err := New(configured)
	if err != nil {
		t.Fatalf("New through configured root symlink: %v", err)
	}
	if st.resolver.dir != configured {
		t.Fatalf("configured root = %q, want %q", st.resolver.dir, configured)
	}
}

func TestStoreRejectsAdapterOwnedDirectorySymlinks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "canonical",
			setup: func(t *testing.T, root string) {
				t.Helper()
				target := filepath.Join(root, "canonical-target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatalf("Mkdir target: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(root, canonicalDirName)); err != nil {
					t.Fatalf("Symlink canonical dir: %v", err)
				}
			},
		},
		{
			name: "inventory catalog",
			setup: func(t *testing.T, root string) {
				t.Helper()
				canonical := filepath.Join(root, canonicalDirName)
				if err := os.Mkdir(canonical, 0o700); err != nil {
					t.Fatalf("Mkdir canonical dir: %v", err)
				}
				target := filepath.Join(root, "catalog-target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatalf("Mkdir target: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(canonical, inventoryCatalogDirName)); err != nil {
					t.Fatalf("Symlink inventory catalog dir: %v", err)
				}
			},
		},
		{
			name: "migration registry",
			setup: func(t *testing.T, root string) {
				t.Helper()
				canonical := filepath.Join(root, canonicalDirName)
				if err := os.Mkdir(canonical, 0o700); err != nil {
					t.Fatalf("Mkdir canonical dir: %v", err)
				}
				target := filepath.Join(root, "migration-target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatalf("Mkdir target: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(canonical, migrationJobsDir)); err != nil {
					t.Fatalf("Symlink migration registry: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			if _, err := New(root); err == nil {
				t.Fatal("New succeeded with an adapter-owned directory symlink")
			}
		})
	}
}
