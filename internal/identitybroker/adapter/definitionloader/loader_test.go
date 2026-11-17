package definitionloader_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/tool"
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
		defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{BuildID: tc.buildID})
		if err != nil || len(problems) != 0 {
			t.Fatalf("Load(%q) = problems %v, err %v; want none", tc.buildID, problems, err)
		}
		want := map[string]string{
			identitybroker.BuiltinExplorer: "50e28fdf176daa2ca5b03d32adc12d0a981b862152a19cac2c429458be6bb1dd",
			identitybroker.BuiltinMain:     "4e6784a5b89b24fb7a65a929b3d0e1df131adf081bd213b29de7c66458e47bde",
		}
		if len(defs) != len(want) {
			t.Fatalf("Load(%q) = %d definitions, want %d built-ins", tc.buildID, len(defs), len(want))
		}
		for _, d := range defs {
			rec := d.Record
			digest, ok := want[rec.Ref.Name]
			if !ok || rec.Ref.Source != identitybroker.SourceSystem || rec.Ref.Revision != tc.revision {
				t.Errorf("record ref = %+v, want system/<built-in> at %q", rec.Ref, tc.revision)
				continue
			}
			if d.Def.Name != rec.Ref.Name || d.Def.Description == "" {
				t.Errorf("%s accepted definition = %+v, want the built-in the record names", rec.Ref.Name, d.Def)
			}
			p := rec.Provenance
			if p.Profile != definitionloader.ProfileRelease || len(p.Metadata) != 0 {
				t.Errorf("%s provenance profile %q metadata %v, want %q and none", rec.Ref.Name, p.Profile, p.Metadata, definitionloader.ProfileRelease)
			}
			wantDigest := sha256Value(digest)
			if p.SourceDigest != wantDigest || p.DefinitionDigest != wantDigest {
				t.Errorf("%s digests = %+v, %+v; want both %s", rec.Ref.Name, p.SourceDigest, p.DefinitionDigest, digest)
			}
		}
	}
}

// TestLoadBuiltinsAreCopies shows a caller that changes a returned built-in
// does not change what the next Load returns.
func TestLoadBuiltinsAreCopies(t *testing.T) {
	t.Parallel()
	first, _, err := definitionloader.Load(t.Context(), definitionloader.Config{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	main := find(t, first, identitybroker.SourceSystem, identitybroker.BuiltinMain)
	want := main.Def.Description
	for i := range first {
		first[i].Def.Description = "changed by a caller"
	}
	second, _, err := definitionloader.Load(t.Context(), definitionloader.Config{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := find(t, second, identitybroker.SourceSystem, identitybroker.BuiltinMain).Def.Description; got != want {
		t.Errorf("second Load built-in description = %q, want %q", got, want)
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

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load = problems %v, err %v; want none", problems, err)
	}
	d := find(t, defs, identitybroker.SourceGlobal, "code-reviewer")
	rec := d.Record

	// The time zone case is pinned by TestFileRevision: os.Stat reports the
	// time in the process's zone, which is UTC on CI.
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
	// Pinned: the digest of the accepted definition, with its admission tier
	// (Origin) left out. A change to the accepted form must be deliberate.
	if want := sha256Value("44b2c216d4d3a3c6d8b5b2337fb9c9a14acd733d1304ec099ac049b7fe5d20b8"); p.DefinitionDigest != want {
		t.Errorf("definition digest = %s, want %s", p.DefinitionDigest.Value, want.Value)
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("record does not validate: %v", err)
	}

	// The accepted definition comes back with the record, so the harness builds
	// the agent from the same read the broker recorded.
	if d.Def.Name != "code-reviewer" || d.Def.Description != "Reviews code" || !strings.Contains(d.Def.Body, "Review the diff.") {
		t.Errorf("accepted definition = %+v, want the parsed code-reviewer", d.Def)
	}
	if d.Def.Origin != tool.AgentOriginExplicit {
		t.Errorf("accepted definition origin = %q, want %q", d.Def.Origin, tool.AgentOriginExplicit)
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
		defs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return find(t, defs, identitybroker.SourceGlobal, "a").Record.Provenance
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

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	find(t, defs, identitybroker.SourceGlobal, "good")
	for _, d := range defs {
		if d.Record.Ref.Name == "Code Reviewer" {
			t.Errorf("loaded %+v; a name outside the SPIFFE segment grammar must be left out", d.Record.Ref)
		}
	}
	if !hasProblem(problems, noFrontmatter, true) {
		t.Errorf("problems = %+v, want %s reported as dropped", problems, noFrontmatter)
	}
	if !hasProblemAt(problems, badName, true) {
		t.Errorf("problems = %+v, want %s reported as dropped at its bare path", problems, badName)
	}
}

// TestLoadDropsNonUTF8Definition plants a body with an invalid byte. JSON
// would encode it as U+FFFD, so two different files could share a
// DefinitionDigest; the definition is left out instead.
func TestLoadDropsNonUTF8Definition(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeDef(t, dir, "bad.md", "---\nname: bad\ndescription: x\n---\na\xffb\n")

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range defs {
		if d.Record.Ref.Source == identitybroker.SourceGlobal {
			t.Errorf("loaded %+v from a file that is not valid UTF-8", d.Record.Ref)
		}
	}
	if !hasProblemAt(problems, path, true) {
		t.Errorf("problems = %+v, want %s reported as dropped", problems, path)
	}
}

// TestLoadReportsAdjustedDefinition plants a definition the parser keeps but
// adjusts: it loads, it is reported as not dropped, and its record covers the
// adjusted definition the harness builds from.
func TestLoadReportsAdjustedDefinition(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	long := strings.Repeat("d", tool.MaxAgentDescriptionBytes+100)
	path := writeDef(t, dir, "long.md", "---\nname: long\ndescription: "+long+"\n---\n")

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := find(t, defs, identitybroker.SourceGlobal, "long")
	if !hasProblem(problems, path, false) {
		t.Errorf("problems = %+v, want %s reported as adjusted, not dropped", problems, path)
	}
	if hasProblem(problems, path, true) {
		t.Errorf("problems = %+v, report %s as dropped though it loaded", problems, path)
	}
	want := agentfs.TruncateRunes(long, tool.MaxAgentDescriptionBytes)
	if d.Def.Description != want {
		t.Errorf("accepted description = %d bytes ending %q, want the parser's truncated form", len(d.Def.Description), d.Def.Description[max(0, len(d.Def.Description)-8):])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if d.Record.Provenance.SourceDigest != digestOf(string(raw)) {
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
	if p.Record.Provenance.DefinitionDigest != d.Record.Provenance.DefinitionDigest {
		t.Error("DefinitionDigest does not cover the adjusted definition")
	}
	if p.Record.Provenance.SourceDigest == d.Record.Provenance.SourceDigest {
		t.Error("SourceDigest should differ: the files' bytes differ")
	}
}

// TestLoadFailsOnUnreadableDirectory covers Load's one failure: a directory
// that exists but cannot be read. It returns no definitions, built-ins
// included, and keeps the problems found before the failure.
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

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{readable, unreadable}})
	if err == nil {
		t.Fatalf("Load = %d definitions, nil error; want an error for the unreadable directory", len(defs))
	}
	if defs != nil {
		t.Errorf("Load returned %d definitions with its error, want none", len(defs))
	}
	if !hasProblem(problems, broken, true) {
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

	defs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, d := range defs {
		got = append(got, d.Record.Ref.Source+"/"+d.Record.Ref.Name)
	}
	want := []string{"global/alpha", "global/main", "global/zeta", "system/explorer", "system/main"}
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

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, d := range defs {
		if d.Record.Ref.Source == identitybroker.SourceGlobal {
			t.Errorf("loaded %+v from a non-UTF-8 path", d.Record.Ref)
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

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{high, low}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	n := 0
	for _, x := range defs {
		if x.Record.Ref.Source == identitybroker.SourceGlobal && x.Record.Ref.Name == "a" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("loaded %d copies of global/a, want exactly 1", n)
	}
	d := find(t, defs, identitybroker.SourceGlobal, "a")
	if got := d.Record.Provenance.Metadata["path"]; got != filepath.Join(high, "a.md") || d.Def.Description != "High" {
		t.Errorf("loaded a from %q (description %q), want the earlier directory's file", got, d.Def.Description)
	}
	// The shadowed definition is reported by its display locator
	// ("explicit: <path>"), so this check matches a substring.
	if !hasProblem(problems, shadowed, true) {
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

	defs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{explicit}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	find(t, defs, identitybroker.SourceGlobal, "chosen")
	for _, d := range defs {
		if strings.HasPrefix(d.Record.Ref.Name, "poisoned") {
			t.Errorf("loaded %+v from a user-tier location; only explicit directories are loaded", d.Record.Ref)
		}
	}
}

// TestLoadFailsOnMissingDirectory shows a configured directory that does not
// exist, or is a file, fails the load: a mistyped path or an unmounted volume
// shows at startup. An empty entry is skipped, as the harness skips it.
func TestLoadFailsOnMissingDirectory(t *testing.T) {
	t.Parallel()
	present := t.TempDir()
	writeDef(t, present, "a.md", "---\nname: a\ndescription: A\n---\n")
	file := writeDef(t, t.TempDir(), "not-a-dir.md", "x")
	for name, missing := range map[string]string{
		"absent": filepath.Join(t.TempDir(), "absent"),
		"file":   file,
	} {
		defs, _, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{present, missing}})
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("%s: Load error = %v, want one naming %s", name, err, missing)
		}
		if defs != nil {
			t.Errorf("%s: Load returned %d definitions with its error, want none", name, len(defs))
		}
	}

	defs, problems, err := definitionloader.Load(t.Context(), definitionloader.Config{Dirs: []string{"", present}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load with an empty entry = problems %v, err %v; want none", problems, err)
	}
	find(t, defs, identitybroker.SourceGlobal, "a")
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

func find(t *testing.T, defs []definitionloader.Definition, source, name string) definitionloader.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Record.Ref.Source == source && d.Record.Ref.Name == name {
			return d
		}
	}
	t.Fatalf("no definition %s/%s in %+v", source, name, defs)
	return definitionloader.Definition{}
}

// hasProblem reports a problem whose location contains path, which also
// matches a display locator such as "explicit: <path>".
func hasProblem(problems []definitionloader.Problem, path string, dropped bool) bool {
	for _, p := range problems {
		if p.Dropped == dropped && strings.Contains(p.Location, path) {
			return true
		}
	}
	return false
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
