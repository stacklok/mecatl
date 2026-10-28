package timezonegen

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
)

func TestSourceRecordsArchiveProvenanceAndNames(t *testing.T) {
	// A UTC TZif v1 file: one local-time type, no transitions, "UTC" abbreviation.
	rules := make([]byte, 54)
	copy(rules, "TZif")
	binary.BigEndian.PutUint32(rules[36:40], 1)
	binary.BigEndian.PutUint32(rules[40:44], 4)
	copy(rules[50:], "UTC\x00")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"Region/Zulu", "Region/Alpha", "Region/Alpha", "UTC", "bare", "directory/", "invalid/zone"} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "directory/" {
			continue
		}
		data := rules
		if name == "invalid/zone" {
			data = []byte("invalid TZif")
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "lib", "time", "zoneinfo.zip")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Source(root, runtime.Version())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive.Bytes())
	if catalog.GoVersion != runtime.Version() || catalog.ArchiveSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("incorrect source provenance: %+v", catalog)
	}
	if want := []string{"Region/Alpha", "Region/Zulu", "UTC"}; !slices.Equal(catalog.Names, want) {
		t.Fatalf("names = %v, want %v", catalog.Names, want)
	}
	again, err := Source(root, runtime.Version())
	if err != nil || !reflect.DeepEqual(catalog, again) {
		t.Fatalf("generation is not deterministic: %+v, %v", again, err)
	}
}

func TestSourceRejectsMissingOrInvalidArchive(t *testing.T) {
	root := t.TempDir()
	if _, err := Source(root, runtime.Version()); err == nil {
		t.Fatal("missing source archive accepted")
	}
	path := filepath.Join(root, "lib", "time", "zoneinfo.zip")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Source(root, runtime.Version()); err == nil {
		t.Fatal("invalid source archive accepted")
	}
}
