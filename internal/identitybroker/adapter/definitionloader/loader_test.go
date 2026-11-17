package definitionloader_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/definitionloader"
)

func TestBuiltinRevision(t *testing.T) {
	t.Parallel()
	for buildID, want := range map[string]string{
		"v0.0.51":                  "v0.0.51",
		"v1.20.3":                  "v1.20.3",
		"":                         "dev",
		"dev":                      "dev",
		"dev+0123456789ab":         "dev",
		"dev+0123456789ab.dirty":   "dev",
		"v0.0.51-3-gabc1234":       "dev",
		"v0.0.51-3-gabc1234-dirty": "dev",
		"v0.0.51-dirty":            "dev",
		"abc1234":                  "dev",
		"v01.0.0":                  "dev",
	} {
		if got := definitionloader.BuiltinRevision(buildID); got != want {
			t.Errorf("BuiltinRevision(%q) = %q, want %q", buildID, got, want)
		}
	}
}

// TestLoadBuiltins pins the built-in records. A change to a built-in
// definition changes its digests, so it must be deliberate.
func TestLoadBuiltins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ buildID, revision string }{
		{"v0.0.51", "v0.0.51"},
		{"dev+0123456789ab.dirty", "dev"},
	} {
		records, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{BuildID: tc.buildID})
		if err != nil || len(problems) != 0 {
			t.Fatalf("Load(%q) = problems %v, err %v; want none", tc.buildID, problems, err)
		}
		want := map[string]string{
			"explorer": "50e28fdf176daa2ca5b03d32adc12d0a981b862152a19cac2c429458be6bb1dd",
			"main":     "4e6784a5b89b24fb7a65a929b3d0e1df131adf081bd213b29de7c66458e47bde",
		}
		if len(records) != len(want) {
			t.Fatalf("Load(%q) = %d records, want %d built-ins", tc.buildID, len(records), len(want))
		}
		for _, rec := range records {
			digest, ok := want[rec.Ref.Name]
			if !ok || rec.Ref.Source != definitionloader.SourceSystem || rec.Ref.Revision != tc.revision {
				t.Errorf("record ref = %+v, want system/<built-in> at %q", rec.Ref, tc.revision)
				continue
			}
			p := rec.Provenance
			if p.Profile != definitionloader.ProfileRelease || len(p.Metadata) != 0 {
				t.Errorf("%s provenance profile %q metadata %v, want %q and none", rec.Ref.Name, p.Profile, p.Metadata, definitionloader.ProfileRelease)
			}
			wantDigest := identitybroker.Digest{Algorithm: identitybroker.DigestSHA256, Value: digest}
			if p.SourceDigest != wantDigest || p.DefinitionDigest != wantDigest {
				t.Errorf("%s digests = %+v, %+v; want both %s", rec.Ref.Name, p.SourceDigest, p.DefinitionDigest, digest)
			}
		}
	}
}

func TestLoadGlobalDefinition(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "---\nname: code-reviewer\ndescription: Reviews code\n---\nReview the diff.\n"
	path := writeDef(t, dir, "code-reviewer.md", content)
	modTime := time.Date(2026, 11, 17, 9, 12, 44, 123456789, time.FixedZone("CET", 3600))
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	records, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load = problems %v, err %v; want none", problems, err)
	}
	rec := find(t, records, definitionloader.SourceGlobal, "code-reviewer")

	if want := "2026-11-17T08:12:44.123456789Z"; rec.Ref.Revision != want {
		t.Errorf("revision = %q, want the file's modification time in UTC, %q", rec.Ref.Revision, want)
	}
	p := rec.Provenance
	if p.Profile != definitionloader.ProfileFile || len(p.Metadata) != 1 || p.Metadata["path"] != path {
		t.Errorf("provenance profile %q metadata %v, want %q with path %q", p.Profile, p.Metadata, definitionloader.ProfileFile, path)
	}
	if want := digestOf(content); p.SourceDigest != want {
		t.Errorf("source digest = %+v, want the digest of the bytes read, %+v", p.SourceDigest, want)
	}
	if p.DefinitionDigest == p.SourceDigest {
		t.Error("definition digest equals the source digest; it must digest the accepted definition, not the file bytes")
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("record does not validate: %v", err)
	}
}

// TestLoadDefinitionDigestFollowsAcceptedDefinition shows the two digests
// answer different questions: formatting that parses to the same definition
// changes only the source digest, and a content change changes both.
func TestLoadDefinitionDigestFollowsAcceptedDefinition(t *testing.T) {
	t.Parallel()
	load := func(content string) identitybroker.Provenance {
		dir := t.TempDir()
		writeDef(t, dir, "a.md", content)
		records, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return find(t, records, definitionloader.SourceGlobal, "a").Provenance
	}
	base := load("---\nname: a\ndescription: Agent A\n---\nBody\n")
	reformatted := load("---\n# a comment\ndescription:   Agent A\nname: a\nunknown: ignored\n---\nBody\n")
	changed := load("---\nname: a\ndescription: Agent A\n---\nAnother body\n")

	if reformatted.SourceDigest == base.SourceDigest {
		t.Error("reformatted file kept the source digest; it must follow the bytes read")
	}
	if reformatted.DefinitionDigest != base.DefinitionDigest {
		t.Error("reformatted file changed the definition digest; it must follow the accepted definition only")
	}
	if changed.DefinitionDigest == base.DefinitionDigest {
		t.Error("a changed body kept the definition digest")
	}
}

func TestLoadSkipsAndReportsBadDefinitions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDef(t, dir, "good.md", "---\nname: good\ndescription: Good\n---\n")
	noFrontmatter := writeDef(t, dir, "broken.md", "no frontmatter here\n")
	badName := writeDef(t, dir, "spaced.md", "---\nname: Code Reviewer\ndescription: Name has a space\n---\n")

	records, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	find(t, records, definitionloader.SourceGlobal, "good")
	for _, rec := range records {
		if rec.Ref.Name == "Code Reviewer" {
			t.Errorf("loaded %+v; a name outside the SPIFFE segment grammar must be left out", rec.Ref)
		}
	}
	for _, path := range []string{noFrontmatter, badName} {
		if !hasDropped(problems, path) {
			t.Errorf("problems = %+v, want %s reported as dropped", problems, path)
		}
	}
}

func TestLoadEarlierDirectoryWins(t *testing.T) {
	t.Parallel()
	high, low := t.TempDir(), t.TempDir()
	writeDef(t, high, "a.md", "---\nname: a\ndescription: High\n---\n")
	shadowed := writeDef(t, low, "a.md", "---\nname: a\ndescription: Low\n---\n")

	records, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{high, low}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := find(t, records, definitionloader.SourceGlobal, "a")
	if got := rec.Provenance.Metadata["path"]; got != filepath.Join(high, "a.md") {
		t.Errorf("loaded a from %q, want the earlier directory's file", got)
	}
	if !hasDropped(problems, shadowed) {
		t.Errorf("problems = %+v, want the shadowed %s reported", problems, shadowed)
	}
}

// TestLoadReadsOnlyExplicitDirectories plants definitions in the user tier's
// conventional locations and checks none is loaded. It changes the process
// environment, so it does not run in parallel.
func TestLoadReadsOnlyExplicitDirectories(t *testing.T) {
	home, config := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	writeDef(t, filepath.Join(home, ".claude", "agents"), "poisoned-home.md", "---\nname: poisoned-home\ndescription: x\n---\n")
	writeDef(t, filepath.Join(config, "mecatl", "agents"), "poisoned-xdg.md", "---\nname: poisoned-xdg\ndescription: x\n---\n")
	explicit := t.TempDir()
	writeDef(t, explicit, "chosen.md", "---\nname: chosen\ndescription: x\n---\n")

	records, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{explicit}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	find(t, records, definitionloader.SourceGlobal, "chosen")
	for _, rec := range records {
		if strings.HasPrefix(rec.Ref.Name, "poisoned") {
			t.Errorf("loaded %+v from a user-tier location; only explicit directories are loaded", rec.Ref)
		}
	}
}

func TestLoadMissingDirectoryContributesNothing(t *testing.T) {
	t.Parallel()
	records, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{filepath.Join(t.TempDir(), "absent")}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load = problems %v, err %v; want none", problems, err)
	}
	for _, rec := range records {
		if rec.Ref.Source != definitionloader.SourceSystem {
			t.Errorf("loaded %+v from a missing directory", rec.Ref)
		}
	}
}

func writeDef(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func find(t *testing.T, records []identitybroker.DefinitionRecord, source, name string) identitybroker.DefinitionRecord {
	t.Helper()
	for _, rec := range records {
		if rec.Ref.Source == source && rec.Ref.Name == name {
			return rec
		}
	}
	t.Fatalf("no record for %s/%s in %+v", source, name, records)
	return identitybroker.DefinitionRecord{}
}

func hasDropped(problems []definitionloader.Problem, path string) bool {
	for _, p := range problems {
		if p.Dropped && strings.Contains(p.Location, path) {
			return true
		}
	}
	return false
}

func digestOf(s string) identitybroker.Digest {
	sum := sha256.Sum256([]byte(s))
	return identitybroker.Digest{Algorithm: identitybroker.DigestSHA256, Value: hex.EncodeToString(sum[:])}
}
