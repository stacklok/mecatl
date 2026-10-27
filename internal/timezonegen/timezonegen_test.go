package timezonegen

import (
	"strings"
	"testing"
)

func TestDeclaredVersionAndRuntimeEnforcement(t *testing.T) {
	declared, err := DeclaredVersion([]byte("module example\n\ngo 1.27.2\n"))
	if err != nil || declared != "go1.27.2" {
		t.Fatalf("declared=%q err=%v", declared, err)
	}
	for _, mod := range []string{"module example", "go invalid", "// go 1.27.2"} {
		if _, err := DeclaredVersion([]byte(mod)); err == nil {
			t.Fatalf("accepted %q", mod)
		}
	}
	if err := RequireVersion(declared, declared); err != nil {
		t.Fatal(err)
	}
	for _, active := range []string{"go1.28.0", "go1.27.1", "devel go1.28-abc"} {
		if err := RequireVersion(declared, active); err == nil || !strings.Contains(err.Error(), Command(declared)) {
			t.Fatalf("runtime %s: %v", active, err)
		}
	}
}

func TestFreshnessDecisions(t *testing.T) {
	fresh := Catalog{GoVersion: "go1.27.2", ArchiveSHA256: strings.Repeat("a", 64), Names: []string{"America/New_York", "UTC"}}
	for _, tc := range []struct {
		name     string
		catalog  Catalog
		declared string
		active   string
		wantErr  bool
		wantRead bool
		wantNote bool
	}{
		{"matching source", fresh, "go1.27.2", "go1.27.2", false, true, false},
		{"newer local", fresh, "go1.27.2", "go1.28.0", false, false, true},
		{"declared bump stale CI", fresh, "go1.28.0", "go1.28.0", true, false, false},
		{"declared bump newer local", fresh, "go1.28.0", "go1.29.0", false, false, true},
		{"missing provenance", Catalog{Names: fresh.Names}, "go1.27.2", "go1.27.2", true, false, false},
		{"bad fingerprint", Catalog{GoVersion: fresh.GoVersion, ArchiveSHA256: "bad", Names: fresh.Names}, "go1.27.2", "go1.27.2", true, true, false},
		{"missing name", Catalog{GoVersion: fresh.GoVersion, ArchiveSHA256: fresh.ArchiveSHA256, Names: []string{"UTC"}}, "go1.27.2", "go1.27.2", true, true, false},
		{"older runtime", fresh, "go1.27.2", "go1.27.1", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := false
			note, err := CheckFreshness(tc.catalog, tc.declared, tc.active, func() (Catalog, error) {
				read = true
				return fresh, nil
			})
			if (err != nil) != tc.wantErr || read != tc.wantRead || (note != "") != tc.wantNote {
				t.Fatalf("note=%q err=%v read=%v", note, err, read)
			}
			if err != nil && !strings.Contains(err.Error(), Command(tc.declared)) {
				t.Fatalf("not actionable: %v", err)
			}
			if note != "" && !strings.Contains(note, Command(tc.declared)) {
				t.Fatalf("not actionable: %s", note)
			}
		})
	}
}
