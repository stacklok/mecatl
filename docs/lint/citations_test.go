package lint

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// This _test.go file owns ALL disk I/O (statFn/readFn/rescue over os.*) so the
// pure CheckCitations core imports no os and gosec sees no file-inclusion taint
// in the production file. Fixtures are map-backed and pure; only the live guard
// (TestRealDesignDocsCitations) touches the real filesystem.

// ---- fixture-backed helpers (pure, no TempDir) -----------------------------

// mapStat builds a statFn over a set of existing paths. Parent directories are
// synthesized so the fixture statFn answers like the real os-backed one (a
// file's directories all exist).
func mapStat(existing map[string]bool) func(string) bool {
	all := map[string]bool{}
	for p, ok := range existing {
		if !ok {
			continue
		}
		all[p] = true
		for d := filepath.Dir(p); d != "." && d != "/"; d = filepath.Dir(d) {
			all[d] = true
		}
	}
	return func(p string) bool { return all[p] }
}

// mapRead builds a readFn over a path->contents map.
func mapRead(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		s, ok := files[p]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(s), nil
	}
}

// mapRescue simulates the basename-rescue walk over a fixture: if exactly one
// known path shares the basename, suggest it.
func mapRescue(known []string) func(string) string {
	return func(base string) string {
		var hits []string
		for _, p := range known {
			if baseName(p) == base {
				hits = append(hits, p)
			}
		}
		if len(hits) == 1 {
			return "did it move to " + hits[0] + "?"
		}
		return ""
	}
}

func TestCheckCitations_Fixtures(t *testing.T) {
	// A fixture repo: these paths "exist". Broken citations live ONLY here, in
	// the test, never in a real doc — that is what makes the live guard
	// mutation-proof (gutting CheckCitations cannot be hidden behind a real
	// dead citation).
	existing := map[string]bool{
		"engine/agent/loop.go":               true,
		"internal/adapter/server/service.go": true,
		"contracts/proto/x.proto":            true,
	}
	files := map[string]string{
		"engine/agent/loop.go": "package agent\nfunc Run() {}\ntype Engine struct{}\n",
	}
	// For rescue: the moved file now lives under engine/, cited at internal/.
	knownForRescue := []string{"engine/agent/loop.go"}

	stat := mapStat(existing)
	read := mapRead(files)
	rescue := mapRescue(knownForRescue)

	tests := []struct {
		name     string
		doc      string
		md       string
		wantKind *Kind // nil => expect zero problems
		wantSym  string
		wantSugg bool
	}{
		// ---- POSITIVE: must produce ZERO problems ----
		{
			name: "real path with line number",
			md:   "see `engine/agent/loop.go:677` for the loop",
		},
		{
			name: "real path with line range suffix",
			md:   "see `engine/agent/loop.go:677-690` for the loop body",
		},
		{
			name: "real path with line list suffix",
			md:   "see `engine/agent/loop.go:677,690` for the two anchors",
		},
		{
			name: "real path with symbol pairing",
			md:   "the loop entry is `engine/agent/loop.go` (`Run`)",
		},
		{
			name: "bare symbol back-reference (no slash)",
			md:   "mutate through `Save`, never poke directly",
		},
		{
			name: "json tag prose (no slash)",
			md:   "the field is `omitempty` and additive",
		},
		{
			name: "command prose (no slash)",
			md:   "run `task test` to verify",
		},
		{
			name: "bare basename with line number (no slash)",
			md:   "consults nothing before reopening (`service.go:868`)",
		},
		{
			name: "relative dot path is ignored",
			md:   "a local helper `./rel.go` here",
		},
		{
			name: "illustrative asset name with inline ignore marker",
			md:   "addressed by `references/api.md` in the skill namespace <!-- lint:not-a-citation -->",
		},
		{
			name: "sh extension is not a known code extension",
			md:   "`scripts/run.sh` is executable",
		},
		{
			name: "real path with proto extension",
			md:   "the contract `contracts/proto/x.proto` is source of truth",
		},
		// ---- NEGATIVE: must produce a Problem (proves the oracle bites) ----
		{
			name:     "missing file",
			md:       "see `engine/agent/ghost.go` which is gone",
			wantKind: kindPtr(MissingFile),
		},
		{
			name:     "missing symbol",
			md:       "the loop entry `engine/agent/loop.go` (`NoSuchSymbol`)",
			wantKind: kindPtr(MissingSymbol),
			wantSym:  "NoSuchSymbol",
		},
		{
			name:     "moved file triggers basename rescue",
			md:       "the loop lives at `internal/agent/loop.go` now",
			wantKind: kindPtr(MissingFile),
			wantSugg: true,
		},
		{
			// The marker is load-bearing: the SAME slash-path WITHOUT it flags.
			name:     "slash-path without ignore marker flags",
			md:       "addressed by `references/api.md` in the namespace",
			wantKind: kindPtr(MissingFile),
		},
		{
			// Proving-negative for the anchored marker (fix #4): a mistyped
			// marker (pluralized "citaton") must NOT suppress; the dead citation
			// is still flagged, fail-safe.
			name:     "mistyped ignore marker still flags",
			md:       "addressed by `references/api.md` <!-- lint:not-a-citaton: typo -->",
			wantKind: kindPtr(MissingFile),
		},
		{
			// A pluralized-but-otherwise-exact near-miss likewise must not match.
			name:     "pluralized ignore marker still flags",
			md:       "addressed by `references/api.md` <!-- lint:not-a-citations -->",
			wantKind: kindPtr(MissingFile),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckCitations(tc.doc, tc.md, stat, read, rescue)
			if tc.wantKind == nil {
				if len(got) != 0 {
					t.Fatalf("expected zero problems, got %d: %v", len(got), got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected exactly one problem, got %d: %v", len(got), got)
			}
			p := got[0]
			if p.Kind != *tc.wantKind {
				t.Errorf("kind = %v, want %v", p.Kind, *tc.wantKind)
			}
			if tc.wantSym != "" && p.Symbol != tc.wantSym {
				t.Errorf("symbol = %q, want %q", p.Symbol, tc.wantSym)
			}
			if tc.wantSugg && p.Suggestion == "" {
				t.Errorf("expected a basename-rescue suggestion, got none (%v)", p)
			}
			if p.Error() == "" {
				t.Errorf("Problem.Error() should be non-empty and actionable")
			}
		})
	}
}

func kindPtr(k Kind) *Kind { return &k }

// TestMultipleCitationsOnOneLine pins that BOTH spans on a line are scanned, not
// just the first: a live citation next to a dead one must still surface the dead
// one. The fixtures elsewhere are all single-citation with a len==1 assertion;
// this is the multi-span case, so it asserts the dead one is reported rather
// than asserting an exact count.
func TestMultipleCitationsOnOneLine(t *testing.T) {
	stat := mapStat(map[string]bool{"engine/agent/loop.go": true})
	read := mapRead(map[string]string{})

	md := "the loop is `engine/agent/loop.go` but `engine/agent/ghost.go` is gone"
	got := CheckCitations("MULTI", md, stat, read, nil)

	var sawGhost bool
	for _, p := range got {
		if p.Path == "engine/agent/loop.go" {
			t.Errorf("the LIVE citation must not be flagged, got %v", p)
		}
		if p.Path == "engine/agent/ghost.go" && p.Kind == MissingFile {
			sawGhost = true
		}
	}
	if !sawGhost {
		t.Fatalf("the dead second citation must be flagged; got %v", got)
	}
}

// TestSymbolPairingNearMiss pins the documented anti-heuristic guarantee: two
// adjacent code spans that are NOT the `path` (`Symbol`) shape (the gap is a
// bare space, not " (", and there is no closing paren) must NOT be misread as a
// symbol claim. The file resolves, so a false symbol read would surface a
// spurious MissingSymbol; the correct behavior is zero problems.
func TestSymbolPairingNearMiss(t *testing.T) {
	stat := mapStat(map[string]bool{"engine/agent/loop.go": true})
	// The file does NOT contain "Run", so IF the near-miss were misread as a
	// symbol pairing it would produce a MissingSymbol.
	read := mapRead(map[string]string{"engine/agent/loop.go": "package agent\n"})

	md := "the loop entry is `engine/agent/loop.go` `Run` in prose"
	got := CheckCitations("NEARMISS", md, stat, read, nil)
	if len(got) != 0 {
		t.Fatalf("adjacent non-paired spans must not be read as a symbol claim, got %v", got)
	}
}

// TestGrammarRejectsProse feeds the known false-positive strings and asserts the
// slash-rule, the known-extension rule, and the inline ignore marker govern them
// as documented. If any of those exemptions is removed, one of these flips to a
// Problem and the test fails.
func TestGrammarRejectsProse(t *testing.T) {
	stat := mapStat(map[string]bool{
		"engine/agent/loop.go": true,
	})
	read := mapRead(map[string]string{})

	// Each of these must yield zero problems by the slash-rule, the known-ext
	// rule, or the explicit inline ignore marker.
	proseSpans := []string{
		"poke `Save` directly",                                  // no slash -> ignored
		"the `omitempty` tag",                                   // no slash -> ignored
		"`task test` to verify",                                 // no slash -> ignored
		"reopening (`service.go:868`)",                          // no slash, has :NN -> ignored
		"a `./rel.go` import",                                   // leading dot, but also no real file; the dot prefix is fine prose
		"`scripts/run.sh` is executable",                        // .sh not a known code ext -> not matched
		"`references/api.md` here <!-- lint:not-a-citation -->", // explicit ignore marker
	}
	for _, span := range proseSpans {
		got := CheckCitations("PROSE", span, stat, read, nil)
		if len(got) != 0 {
			t.Errorf("prose %q should yield zero problems, got %v", span, got)
		}
	}

	// And a genuine repo-rooted dead citation MUST still be caught (the rules
	// reject prose, not real citations).
	got := CheckCitations("PROSE", "`engine/agent/ghost.go` is gone", stat, read, nil)
	if len(got) != 1 || got[0].Kind != MissingFile {
		t.Fatalf("a dead repo citation must still be flagged, got %v", got)
	}
}

// ---- live guard over the real design docs ----------------------------------

// repoRoot derives the repo root from this test file's location. docs/lint is
// two levels below the root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file = <root>/docs/lint/citations_test.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// osStat reports whether a repo-root-relative path exists (file OR dir).
func osStat(root string) func(string) bool {
	return func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	}
}

// osRead reads a repo-root-relative path.
func osRead(root string) func(string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	}
}

// osRescue walks the repo lazily to find a single file matching basename,
// suggesting it ("did it move to ...?"). Called ONLY on a MissingFile, so the
// walk cost is paid only when a citation is already dead.
func osRescue(root string) func(string) string {
	return func(base string) string {
		var hits []string
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // best-effort rescue; skip unreadable subtrees
			}
			name := d.Name()
			if d.IsDir() {
				// Skip noise that cannot hold a cited source file.
				// Skip .claude too: it holds git worktrees with the OLD
				// internal/ layout, which would create phantom rescue hits.
				if name == ".git" || name == ".claude" || name == ".worktrees" || name == ".scratch" || name == "bin" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if name == base {
				rel, rerr := filepath.Rel(root, path)
				if rerr == nil {
					hits = append(hits, filepath.ToSlash(rel))
				}
			}
			return nil
		})
		if len(hits) == 1 {
			return "did it move to " + hits[0] + "?"
		}
		return ""
	}
}

func TestOSRescueSkipsScratchAndWorktrees(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		filepath.Join("engine", "agent", "moved.go"),
		filepath.Join(".worktrees", "poison", "moved.go"),
		filepath.Join(".scratch", "poison", "moved.go"),
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := osRescue(root)("moved.go"), "did it move to engine/agent/moved.go?"; got != want {
		t.Fatalf("rescue = %q, want %q", got, want)
	}
}

func TestOSRescueDoesNotSuggestFilesOnlyInSkippedDirs(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		filepath.Join(".worktrees", "poison", "moved.go"),
		filepath.Join(".scratch", "poison", "moved.go"),
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := osRescue(root)("moved.go"); got != "" {
		t.Fatalf("rescue = %q, want no suggestion", got)
	}
}

// TestRealDesignDocsCitations runs CheckCitations over every docs/design/*.md
// against the real filesystem and fails with the aggregated dead-citation list.
// This is the live anti-drift guard. To widen scope (e.g. include docs/*.md or
// CLAUDE.md), add globs to the patterns slice below.
func TestRealDesignDocsCitations(t *testing.T) {
	root := repoRoot(t)
	stat := osStat(root)
	read := osRead(root)
	rescue := osRescue(root)

	// Scope: docs/design/*.md and the architecture guide (the living reference,
	// citation-heavy). Widen by appending globs here.
	patterns := []string{
		filepath.Join(root, "docs", "design", "*.md"),
		filepath.Join(root, "docs", "adr", "*.md"),
		filepath.Join(root, "docs", "architecture.md"),
		filepath.Join(root, "docs", "architecture", "*.md"),
	}

	var docs []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			t.Fatalf("glob %q: %v", pat, err)
		}
		docs = append(docs, matches...)
	}
	if len(docs) == 0 {
		t.Fatalf("no design docs matched %v (wrong repo root?)", patterns)
	}
	sort.Strings(docs)

	var all []Problem
	for _, doc := range docs {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		rel, _ := filepath.Rel(root, doc)
		all = append(all, CheckCitations(rel, string(data), stat, read, rescue)...)
	}

	if len(all) > 0 {
		var b strings.Builder
		b.WriteString("dead citations in docs/design/*.md (fix the doc, not this test); " +
			"see docs/design/README.md for the citation convention:\n")
		for _, p := range all {
			b.WriteString("  - " + p.Error() + "\n")
		}
		t.Fatal(b.String())
	}
}
