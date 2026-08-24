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
