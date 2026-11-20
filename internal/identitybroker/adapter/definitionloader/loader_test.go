package definitionloader_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/agentdef"
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
		// Published main builds each get their own revision.
		"v0.0.53-main.812.1.gabcdef12":       "v0.0.53-main.812.1.gabcdef12",
		"v0.0.53-main.812.1.gabcdef12-dirty": "dev",
		"v0.0.53-main.812.1.gABCDEF12":       "dev",
		"v0.0.53-main.812.1.gabcdef1":        "dev",
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
		recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{BuildID: tc.buildID})
		if err != nil || len(problems) != 0 {
			t.Fatalf("Load(%q) = problems %v, err %v; want none", tc.buildID, problems, err)
		}
		// Pinned: the digests of the built-ins' canonical forms.
		want := map[string]string{
			identitybroker.BuiltinExplorer: "d0070ba5a91151e6e8d5027241ec2bf3a8010ab4edd00a85cf8edd48b763eb3a",
			identitybroker.BuiltinMain:     "1bdf8e2e3ad0d79f833e86406226c7c5b4a7e1af6a4e2a059fa75b940521b63a",
			identitybroker.BuiltinBranch:   "64ec85aea8d2520653c62cc2e9a5f02888473ae990ae8370db70c661075dab1a",
			identitybroker.BuiltinMember:   "e72923070f34e6b3e4e76d6364059cd5edf1fa654ede7c090a0c4aaa5443aca3",
		}
		if len(recs) != len(want) {
			t.Fatalf("Load(%q) = %d records, want %d built-ins", tc.buildID, len(recs), len(want))
		}
		for _, rec := range recs {
			digest, ok := want[rec.Ref.Name]
			if !ok || rec.Ref.Source != identitybroker.SourceSystem || rec.Ref.Revision != tc.revision {
				t.Errorf("record ref = %+v, want system/<built-in> at %q", rec.Ref, tc.revision)
				continue
			}
			p := rec.Provenance
			if p.Profile != definitionloader.ProfileRelease || len(p.Metadata) != 1 || p.Metadata[definitionloader.MetadataDefinitionForm] != wantForm {
				t.Errorf("%s provenance profile %q metadata %v, want %q with form %q only", rec.Ref.Name, p.Profile, p.Metadata, definitionloader.ProfileRelease, wantForm)
			}
			wantDigest := sha256Value(digest)
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

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load = problems %v, err %v; want none", problems, err)
	}
	rec := find(t, recs, identitybroker.SourceGlobal, "code-reviewer")

	// The time zone case is pinned by TestFileRevision: os.Stat reports the
	// time in the process's zone, which is UTC on CI.
	if want := "2026-11-17T08:12:44.123456789Z"; rec.Ref.Revision != want {
		t.Errorf("revision = %q, want the file's modification time in UTC, %q", rec.Ref.Revision, want)
	}
	p := rec.Provenance
	if p.Profile != definitionloader.ProfileFile || len(p.Metadata) != 2 || p.Metadata["path"] != path || p.Metadata[definitionloader.MetadataDefinitionForm] != wantForm {
		t.Errorf("provenance profile %q metadata %v, want %q with path %q and form %q", p.Profile, p.Metadata, definitionloader.ProfileFile, path, wantForm)
	}
	if want := digestOf(content); p.SourceDigest != want {
		t.Errorf("source digest = %+v, want the digest of the bytes read, %+v", p.SourceDigest, want)
	}
	// Pinned: the digest of the parsed definition's canonical form. A change
	// to the form must be deliberate (agentdef.FormVersion).
	if want := digestOf(`{"name":"code-reviewer","description":"Reviews code","body":"Review the diff."}`); p.DefinitionDigest != want {
		t.Errorf("definition digest = %s, want %s", p.DefinitionDigest.Value, want.Value)
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
		recs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return find(t, recs, identitybroker.SourceGlobal, "a").Provenance
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

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	find(t, recs, identitybroker.SourceGlobal, "good")
	for _, rec := range recs {
		if rec.Ref.Name == "Code Reviewer" {
			t.Errorf("loaded %+v; a name outside the SPIFFE segment grammar must be left out", rec.Ref)
		}
	}
	if !hasProblemAt(problems, noFrontmatter, true) {
		t.Errorf("problems = %+v, want %s reported as dropped", problems, noFrontmatter)
	}
	if !hasProblemAt(problems, badName, true) {
		t.Errorf("problems = %+v, want %s reported as dropped", problems, badName)
	}
}

// TestLoadDropsNonUTF8Definition plants an invalid byte in each part of a
// file. In the frontmatter, the YAML parser reads it as U+FFFD; in the body,
// JSON would encode it so. Either way two different files could share a
// DefinitionDigest, so the definition is left out instead. A file holding a
// literal U+FFFD is valid UTF-8 and loads.
func TestLoadDropsNonUTF8Definition(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"body":         "---\nname: bad\ndescription: x\n---\na\xffb\n",
		"model":        "---\nname: bad\ndescription: x\nmodel: a\xffb\n---\n",
		"hook command": "---\nname: bad\ndescription: x\nhooks:\n  Stop: a\xfeb\n---\n",
		"header name":  "---\nname: bad\ndescription: x\nmcpServers:\n  - name: s\n    url: https://x\n    headers:\n      K\xff: v\n---\n",
		"unknown key":  "---\nname: bad\ndescription: x\nfuture: a\xffb\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeDef(t, dir, "bad.md", content)
			recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if n := countGlobal(recs, "bad"); n != 0 {
				t.Errorf("loaded global/bad from a file that is not valid UTF-8")
			}
			if !hasProblemAt(problems, path, true) {
				t.Errorf("problems = %+v, want %s reported as dropped", problems, path)
			}
		})
	}

	dir := t.TempDir()
	writeDef(t, dir, "ok.md", "---\nname: ok\ndescription: x\nmodel: a\uFFFDb\n---\n")
	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load = problems %v, err %v; want none", problems, err)
	}
	find(t, recs, identitybroker.SourceGlobal, "ok")
}

// TestLoadReportsUnreadableFiles plants entries that are named like
// definitions but cannot be read as files: a dangling symlink and a symlink to
// a directory. Each is reported as dropped at its path, and loading continues.
// A symlink to a definition file loads, with the target's bytes.
func TestLoadReportsUnreadableFiles(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs extra privileges on Windows")
	}
	dir, elsewhere := t.TempDir(), t.TempDir()
	writeDef(t, dir, "good.md", "---\nname: good\ndescription: x\n---\n")
	dangling := filepath.Join(dir, "dangling.md")
	if err := os.Symlink(filepath.Join(elsewhere, "missing.md"), dangling); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	toDir := filepath.Join(dir, "todir.md")
	if err := os.Symlink(elsewhere, toDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	content := "---\nname: linked\ndescription: x\n---\n"
	target := writeDef(t, elsewhere, "target.md", content)
	if err := os.Symlink(target, filepath.Join(dir, "linked.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	find(t, recs, identitybroker.SourceGlobal, "good")
	if find(t, recs, identitybroker.SourceGlobal, "linked").Provenance.SourceDigest != digestOf(content) {
		t.Error("a symlinked definition's SourceDigest does not cover its target's bytes")
	}
	for _, path := range []string{dangling, toDir} {
		if !hasProblemAt(problems, path, true) {
			t.Errorf("problems = %+v, want %s reported as dropped", problems, path)
		}
	}
	if len(problems) != 2 {
		t.Errorf("problems = %+v, want only the two unreadable entries", problems)
	}
}

// TestLoadReportsAdjustedDefinition plants a definition the parser keeps but
// adjusts: it loads, it is reported as not dropped, and its DefinitionDigest
// covers the adjusted definition.
func TestLoadReportsAdjustedDefinition(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	long := strings.Repeat("d", agentdef.MaxDescriptionBytes+100)
	path := writeDef(t, dir, "long.md", "---\nname: long\ndescription: "+long+"\n---\n")

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := find(t, recs, identitybroker.SourceGlobal, "long")
	if !hasProblemAt(problems, path, false) {
		t.Errorf("problems = %+v, want %s reported as adjusted, not dropped", problems, path)
	}
	if hasProblemAt(problems, path, true) {
		t.Errorf("problems = %+v, report %s as dropped though it loaded", problems, path)
	}
	// The parser cuts an ASCII description to the cap and ends it with a
	// three-byte ellipsis.
	want := long[:agentdef.MaxDescriptionBytes-len("…")] + "…"
	if rec.Provenance.DefinitionDigest != digestOf(`{"name":"long","description":"`+want+`"}`) {
		t.Error("DefinitionDigest does not cover the parser's truncated description")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rec.Provenance.SourceDigest != digestOf(string(raw)) {
		t.Error("SourceDigest does not cover the bytes read")
	}

	// A file that already holds the truncated text parses to the same accepted
	// definition: the same DefinitionDigest from different bytes.
	other := t.TempDir()
	writeDef(t, other, "long.md", "---\nname: long\ndescription: "+want+"\n---\n")
	pre, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{other}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := find(t, pre, identitybroker.SourceGlobal, "long")
	if p.Provenance.DefinitionDigest != rec.Provenance.DefinitionDigest {
		t.Error("DefinitionDigest does not cover the adjusted definition")
	}
	if p.Provenance.SourceDigest == rec.Provenance.SourceDigest {
		t.Error("SourceDigest should differ: the files' bytes differ")
	}
}

// TestLoadFailsOnUnreadableDirectory covers Load's one failure: a directory
// that exists but cannot be read. It returns no records, built-ins included,
// and keeps the problems found before the failure.
func TestLoadFailsOnUnreadableDirectory(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("root, and Windows, read a directory regardless of its permission bits")
	}
	readable, unreadable := t.TempDir(), t.TempDir()
	broken := writeDef(t, readable, "broken.md", "no frontmatter here\n")
	writeDef(t, unreadable, "a.md", "---\nname: a\ndescription: A\n---\n")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o700) })

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{readable, unreadable}})
	if err == nil {
		t.Fatalf("Load = %d records, nil error; want an error for the unreadable directory", len(recs))
	}
	if recs != nil {
		t.Errorf("Load returned %d records with its error, want none", len(recs))
	}
	if !hasProblemAt(problems, broken, true) {
		t.Errorf("problems = %+v, want the problem found before the failure kept", problems)
	}
}

// TestLoadSortsAndKeepsBuiltins pins the order Load returns: by source, then
// name. The built-ins stay when directories are given, and a global definition
// named like a built-in sits beside it rather than replacing it.
func TestLoadSortsAndKeepsBuiltins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"zeta", "main", "alpha"} {
		writeDef(t, dir, name+".md", "---\nname: "+name+"\ndescription: x\n---\n")
	}

	recs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, rec := range recs {
		got = append(got, rec.Ref.Source+"/"+rec.Ref.Name)
	}
	want := []string{"global/alpha", "global/main", "global/zeta", "system/branch", "system/explorer", "system/main", "system/member"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Load order = %v, want %v", got, want)
	}
}

// TestLoadDropsNonUTF8Path plants a definition under a directory whose name is
// not valid UTF-8. Its path cannot go in the provenance metadata, so it is
// left out and reported. File systems that refuse such names skip the test.
func TestLoadDropsNonUTF8Path(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "bad\xff")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("file system refuses a non-UTF-8 name: %v", err)
	}
	path := writeDef(t, dir, "a.md", "---\nname: a\ndescription: A\n---\n")

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, rec := range recs {
		if rec.Ref.Source == identitybroker.SourceGlobal {
			t.Errorf("loaded %+v from a non-UTF-8 path", rec.Ref)
		}
	}
	if !hasProblemAt(problems, path, true) {
		t.Errorf("problems = %+v, want %q reported as dropped", problems, path)
	}
}

func TestLoadEarlierDirectoryWins(t *testing.T) {
	t.Parallel()
	high, low := t.TempDir(), t.TempDir()
	writeDef(t, high, "a.md", "---\nname: a\ndescription: High\n---\n")
	shadowed := writeDef(t, low, "a.md", "---\nname: a\ndescription: Low\n---\n")

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{high, low}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := countGlobal(recs, "a"); n != 1 {
		t.Errorf("loaded %d copies of global/a, want exactly 1", n)
	}
	rec := find(t, recs, identitybroker.SourceGlobal, "a")
	if got := rec.Provenance.Metadata["path"]; got != filepath.Join(high, "a.md") {
		t.Errorf("loaded a from %q, want the earlier directory's file", got)
	}
	if want := digestOf(`{"name":"a","description":"High"}`); rec.Provenance.DefinitionDigest != want {
		t.Error("global/a does not record the earlier directory's definition")
	}
	if !hasProblemAt(problems, shadowed, true) {
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

	recs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{explicit}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	find(t, recs, identitybroker.SourceGlobal, "chosen")
	for _, rec := range recs {
		if strings.HasPrefix(rec.Ref.Name, "poisoned") {
			t.Errorf("loaded %+v from a user-tier location; only explicit directories are loaded", rec.Ref)
		}
	}
}

// TestLoadFailsOnMissingDirectory shows a configured directory that does not
// exist, or is a file, fails the load: a mistyped path or an unmounted volume
// shows at startup. A blank entry is skipped, as the harness skips it.
func TestLoadFailsOnMissingDirectory(t *testing.T) {
	t.Parallel()
	present := t.TempDir()
	writeDef(t, present, "a.md", "---\nname: a\ndescription: A\n---\n")
	file := writeDef(t, t.TempDir(), "not-a-dir.md", "x")
	for name, missing := range map[string]string{
		"absent": filepath.Join(t.TempDir(), "absent"),
		"file":   file,
	} {
		recs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{present, missing}})
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("%s: Load error = %v, want one naming %s", name, err, missing)
		}
		if recs != nil {
			t.Errorf("%s: Load returned %d records with its error, want none", name, len(recs))
		}
	}

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{"", "  ", present}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load with blank entries = problems %v, err %v; want none", problems, err)
	}
	find(t, recs, identitybroker.SourceGlobal, "a")
}

// TestLoadReadsOnlyDefinitionFiles pins which entries of a directory count:
// files directly in it with the .md extension. Within a directory, the first
// file in name order wins a name and a later one is reported.
func TestLoadReadsOnlyDefinitionFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDef(t, dir, "a.md", "---\nname: dup\ndescription: First\n---\n")
	second := writeDef(t, dir, "b.md", "---\nname: dup\ndescription: Second\n---\n")
	writeDef(t, dir, "notes.txt", "---\nname: text\ndescription: x\n---\n")
	writeDef(t, filepath.Join(dir, "nested"), "deep.md", "---\nname: deep\ndescription: x\n---\n")
	if err := os.Mkdir(filepath.Join(dir, "dir.md"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := countGlobal(recs, "dup"); n != 1 {
		t.Fatalf("loaded %d copies of global/dup, want exactly 1", n)
	}
	if want := digestOf(`{"name":"dup","description":"First"}`); find(t, recs, identitybroker.SourceGlobal, "dup").Provenance.DefinitionDigest != want {
		t.Error("global/dup does not record the first file's definition")
	}
	if !hasProblemAt(problems, second, true) {
		t.Errorf("problems = %+v, want %s reported as a dropped duplicate", problems, second)
	}
	for _, name := range []string{"text", "deep"} {
		if n := countGlobal(recs, name); n != 0 {
			t.Errorf("loaded global/%s; only .md files directly in the directory count", name)
		}
	}
	if len(problems) != 1 {
		t.Errorf("problems = %+v, want only the duplicate", problems)
	}
}

// TestLoadEarlierDirectoryHoldsNameWhenRecordFails plants a definition the
// earlier directory wins but whose record fails. The harness runs that
// definition under the name, so the later directory's must not be recorded
// for it.
func TestLoadEarlierDirectoryHoldsNameWhenRecordFails(t *testing.T) {
	t.Parallel()
	high, low := t.TempDir(), t.TempDir()
	bad := writeDef(t, high, "a.md", "---\nname: a\ndescription: High\n---\nx\xffy\n")
	shadowed := writeDef(t, low, "a.md", "---\nname: a\ndescription: Low\n---\n")

	recs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{high, low}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n := countGlobal(recs, "a"); n != 0 {
		t.Errorf("loaded global/a %d times; the later directory's definition must not take the name", n)
	}
	if !hasProblemAt(problems, bad, true) || !hasProblemAt(problems, shadowed, true) {
		t.Errorf("problems = %+v, want both %s and %s reported as dropped", problems, bad, shadowed)
	}
}

// TestLoadStopsWhenCanceled shows a canceled context stops the load before it
// reads a directory.
func TestLoadStopsWhenCanceled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDef(t, dir, "a.md", "---\nname: a\ndescription: A\n---\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	recs, _, err := definitionloader.Load(ctx, definitionloader.Config{Dirs: []string{dir}})
	if !errors.Is(err, context.Canceled) || recs != nil {
		t.Errorf("Load = %d records, err %v; want none and context.Canceled", len(recs), err)
	}
}

// wantForm pins the canonical form version records name. The pinned digests
// in these tests are of this form, so changing agentdef.FormVersion must
// change them too.
const wantForm = "agentdef-canonical/v1"

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

func find(t *testing.T, recs []identitybroker.DefinitionRecord, source, name string) identitybroker.DefinitionRecord {
	t.Helper()
	for _, rec := range recs {
		if rec.Ref.Source == source && rec.Ref.Name == name {
			return rec
		}
	}
	t.Fatalf("no record %s/%s in %+v", source, name, recs)
	return identitybroker.DefinitionRecord{}
}

func countGlobal(recs []identitybroker.DefinitionRecord, name string) int {
	n := 0
	for _, rec := range recs {
		if rec.Ref.Source == identitybroker.SourceGlobal && rec.Ref.Name == name {
			n++
		}
	}
	return n
}

// hasProblemAt reports a problem located exactly at path.
func hasProblemAt(problems []definitionloader.Problem, path string, dropped bool) bool {
	for _, p := range problems {
		if p.Dropped == dropped && p.Location == path {
			return true
		}
	}
	return false
}

func digestOf(s string) identitybroker.Digest {
	sum := sha256.Sum256([]byte(s))
	return sha256Value(hex.EncodeToString(sum[:]))
}

func sha256Value(v string) identitybroker.Digest {
	return identitybroker.Digest{Algorithm: identitybroker.DigestSHA256, Value: v}
}
