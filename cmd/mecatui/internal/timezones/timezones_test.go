package timezones

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"go/version"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/timezonegen"
)

func TestCatalogIntegrityAndFreshness(t *testing.T) {
	declared := timezonegen.SourceVersion
	regen := timezonegen.Command(declared)
	var catalog timezonegen.Catalog
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		t.Fatalf("invalid catalog: %v; run: %s", err, regen)
	}
	if !version.IsValid(catalog.GoVersion) {
		t.Fatalf("invalid source version %q; run: %s", catalog.GoVersion, regen)
	}
	hash, err := hex.DecodeString(catalog.ArchiveSHA256)
	if err != nil || len(hash) != 32 {
		t.Fatalf("invalid archive fingerprint: %q; run: %s", catalog.ArchiveSHA256, regen)
	}
	if !slices.IsSorted(catalog.Names) || len(catalog.Names) == 0 || !slices.Contains(catalog.Names, "UTC") {
		t.Fatalf("catalog must be sorted, nonempty, and include UTC; run: %s", regen)
	}
	for i, name := range catalog.Names {
		if i > 0 && name == catalog.Names[i-1] {
			t.Fatalf("duplicate %s; run: %s", name, regen)
		}
		if name != "UTC" && (!strings.Contains(name, "/") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "..")) {
			t.Fatalf("invalid identifier shape %q; run: %s", name, regen)
		}
		if _, err := Location(name); err != nil {
			t.Fatalf("catalog name %s not loadable: %v; run: %s", name, err, regen)
		}
	}
	note, err := timezonegen.CheckFreshness(catalog, declared, runtime.Version(), func() (timezonegen.Catalog, error) {
		//nolint:staticcheck // SA1019: go test uses its own toolchain archive, not a potentially different go on PATH.
		source, err := timezonegen.Source(runtime.GOROOT(), declared)
		if err != nil {
			return source, err
		}
		data, err := json.MarshalIndent(source, "", "  ")
		if err == nil && !bytes.Equal(append(data, '\n'), catalogJSON) {
			t.Errorf("catalog serialization drift; run: %s", timezonegen.Command(declared))
		}
		return source, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Log(note)
	}
}

func TestNamesAndValidation(t *testing.T) {
	for _, name := range []string{"UTC", "America/Los_Angeles", "US/Pacific", "Etc/GMT+5", "Asia/Kolkata"} {
		if !Valid(name) {
			t.Errorf("missing valid ID %q", name)
		}
	}
	for _, name := range []string{"", "Local", "PST", "EST", "GMT", "utc", "America/Typo", "America/Los Angeles", " UTC", "../UTC"} {
		if Valid(name) {
			t.Errorf("accepted %q", name)
		}
		if _, err := Location(name); err == nil {
			t.Errorf("loaded invalid %q", name)
		}
	}
	names := Names()
	names[0] = "corrupted"
	if Names()[0] == "corrupted" {
		t.Fatal("Names leaked mutable catalog")
	}
}

func TestArchiveRulesAndRuntimeDST(t *testing.T) {
	//nolint:staticcheck // SA1019: the rule fixture belongs to the test's running toolchain, not an ambient go on PATH.
	archive, err := zip.OpenReader(filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var rules []byte
	for _, entry := range archive.File {
		if entry.Name != "America/New_York" {
			continue
		}
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		rules, err = io.ReadAll(r)
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read=%v close=%v", err, closeErr)
		}
		break
	}
	fixture, err := time.LoadLocationFromTZData("America/New_York", rules)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Location("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []*time.Location{fixture, loaded} {
		for _, tc := range []struct {
			month  time.Month
			offset int
		}{{time.January, -5 * 3600}, {time.July, -4 * 3600}} {
			at := time.Date(2026, tc.month, 15, 9, 0, 0, 0, loc)
			_, offset := at.Zone()
			if offset != tc.offset || at.UTC().Hour() != 9-tc.offset/3600 {
				t.Fatalf("wrong local 9am: %v offset=%d", at, offset)
			}
		}
	}
}
