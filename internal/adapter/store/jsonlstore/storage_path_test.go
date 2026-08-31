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
	// Canonical normalization resolves existing ancestors, including platform
	// aliases such as macOS /var -> /private/var.
	physicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("EvalSymlinks(base): %v", err)
	}
	wantRoot := filepath.Join(physicalBase, "store")
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

func TestStoreCanonicalizesExistingSymlinkAncestorWithoutFollowingConfiguredRoot(t *testing.T) {
	base := t.TempDir()
	configured := filepath.Join(base, "store")
	st, err := New(configured)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	physicalBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("EvalSymlinks(base): %v", err)
	}
	want := filepath.Join(physicalBase, "store")
	if st.resolver.dir != want {
		t.Fatalf("canonical store root = %q, want %q", st.resolver.dir, want)
	}
	if _, err := os.Lstat(filepath.Join(st.resolver.dir, canonicalDirName)); err != nil {
		t.Fatalf("canonical directory was not created: %v", err)
	}
}

func TestStoreRejectsEmptyRoot(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(\"\") succeeded, want error")
	}
}
func TestStoreRejectsConfiguredRootAndAncestorSymlinksWithoutTargetMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured func(string, string) string
	}{
		{
			name: "root",
			configured: func(base, target string) string {
				configured := filepath.Join(base, "configured")
				if err := os.Symlink(target, configured); err != nil {
					t.Fatalf("Symlink configured root: %v", err)
				}
				return configured
			},
		},
		{
			name: "ancestor",
			configured: func(base, target string) string {
				ancestor := filepath.Join(base, "configured")
				if err := os.Symlink(target, ancestor); err != nil {
					t.Fatalf("Symlink configured ancestor: %v", err)
				}
				return filepath.Join(ancestor, "store")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			target := filepath.Join(base, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatalf("Mkdir target: %v", err)
			}
			configured := tc.configured(base, target)
			if _, err := New(configured); err == nil {
				t.Fatal("New succeeded through configured symlink")
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatalf("ReadDir target: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("rejected symlink mutated target: %v", entries)
			}
		})
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
