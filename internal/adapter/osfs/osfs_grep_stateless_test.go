package osfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

func TestGrepPreservesLineAndRegexpSemantics(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	longLine := append(bytes.Repeat([]byte("x"), 128<<10), []byte(" needle")...)
	files := map[string][]byte{
		"01-literals.txt": []byte("alpha needle omega\nALPHA NEEDLE\nclass7\nleft-or-right\n"),
		"02-unicode.txt":  []byte("valid replacement rune: �\ninvalid replacement rune: \xff\n雪 needle\n"),
		"03-empty.txt":    nil,
		"04-trailing.txt": []byte("tail\n"),
		"05-crlf.txt":     []byte("carriage\r\nnext\r\n"),
		"06-long.txt":     append(append([]byte(nil), longLine...), '\n'),
		"07-binary.txt":   []byte("needle\x00hidden\n"),
	}
	writeGrepFixture(t, root, files)

	tests := []struct {
		name    string
		pattern string
		want    []tool.GrepMatch
	}{
		{name: "literal hit", pattern: "needle", want: []tool.GrepMatch{{Path: "01-literals.txt", Line: 1, Text: "alpha needle omega"}, {Path: "02-unicode.txt", Line: 3, Text: "雪 needle"}, {Path: "06-long.txt", Line: 1, Text: string(longLine)}}},
		{name: "literal miss", pattern: "definitely absent"},
		{name: "anchored", pattern: "^alpha", want: []tool.GrepMatch{{Path: "01-literals.txt", Line: 1, Text: "alpha needle omega"}}},
		{name: "case folded", pattern: "(?i)needle", want: []tool.GrepMatch{{Path: "01-literals.txt", Line: 1, Text: "alpha needle omega"}, {Path: "01-literals.txt", Line: 2, Text: "ALPHA NEEDLE"}, {Path: "02-unicode.txt", Line: 3, Text: "雪 needle"}, {Path: "06-long.txt", Line: 1, Text: string(longLine)}}},
		{name: "character class", pattern: `class[0-9]`, want: []tool.GrepMatch{{Path: "01-literals.txt", Line: 3, Text: "class7"}}},
		{name: "alternation", pattern: `left|right`, want: []tool.GrepMatch{{Path: "01-literals.txt", Line: 4, Text: "left-or-right"}}},
		{name: "non-prefix", pattern: `[a-z]+ needle`, want: []tool.GrepMatch{{Path: "01-literals.txt", Line: 1, Text: "alpha needle omega"}, {Path: "06-long.txt", Line: 1, Text: string(longLine)}}},
		{name: "valid and invalid RuneError", pattern: "�", want: []tool.GrepMatch{{Path: "02-unicode.txt", Line: 1, Text: "valid replacement rune: �"}, {Path: "02-unicode.txt", Line: 2, Text: "invalid replacement rune: \xff"}}},
		{name: "empty and trailing lines", pattern: `^$`, want: []tool.GrepMatch{{Path: "01-literals.txt", Line: 5, Text: ""}, {Path: "02-unicode.txt", Line: 4, Text: ""}, {Path: "03-empty.txt", Line: 1, Text: ""}, {Path: "04-trailing.txt", Line: 2, Text: ""}, {Path: "05-crlf.txt", Line: 3, Text: ""}, {Path: "06-long.txt", Line: 2, Text: ""}}},
		{name: "CRLF preserves CR", pattern: `\r$`, want: []tool.GrepMatch{{Path: "05-crlf.txt", Line: 1, Text: "carriage\r"}, {Path: "05-crlf.txt", Line: 2, Text: "next\r"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ws.Grep(context.Background(), tc.pattern, "*.txt")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Grep(%q) = %#v, want %#v", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestGrepMatchesPreoptimizationReferenceOnGeneratedCorpus(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string][]byte)
	for file := 0; file < 72; file++ {
		var body bytes.Buffer
		for line := 0; line < 37; line++ {
			fmt.Fprintf(&body, "file=%02d line=%02d alpha-%c value=%d", file, line, 'a'+rune((file+line)%26), file*line)
			if (file+line)%19 == 0 {
				body.WriteString(" rare-needle")
			}
			if line != 36 || file%3 != 0 {
				body.WriteByte('\n')
			}
		}
		if file == 17 {
			body.WriteString("invalid=\xff\n")
		}
		if file == 33 {
			body.WriteString("binary\x00rare-needle\n")
		}
		files[fmt.Sprintf("group-%02d/file-%02d.txt", file%6, file)] = body.Bytes()
	}
	files["empty.txt"] = nil
	writeGrepFixture(t, root, files)

	queries := []struct {
		pattern    string
		glob       string
		selectPath func(string) bool
	}{
		{pattern: "rare-needle", glob: "**/*.txt", selectPath: func(path string) bool { return strings.Contains(path, "/") }},
		{pattern: "absent literal prefix", glob: "", selectPath: func(string) bool { return true }},
		{pattern: `^file=0[0-9]`, glob: "group-01/*.txt", selectPath: func(path string) bool { return strings.HasPrefix(path, "group-01/") }},
		{pattern: `(?i)ALPHA-[M-P]`, glob: "./group-02/*.txt", selectPath: func(path string) bool { return strings.HasPrefix(path, "group-02/") }},
		{pattern: `value=(0|17|34)$`, glob: "/group-03/*.txt", selectPath: func(path string) bool { return strings.HasPrefix(path, "group-03/") }},
		{pattern: `[a-z]+-[a-z].*rare`, glob: "**", selectPath: func(string) bool { return true }},
		{pattern: "�", glob: "**/*.txt", selectPath: func(path string) bool { return strings.Contains(path, "/") }},
		{pattern: `^$`, glob: "", selectPath: func(string) bool { return true }},
		{pattern: `(`, glob: "", selectPath: func(string) bool { return true }},
	}
	for _, query := range queries {
		t.Run(query.pattern+"/"+query.glob, func(t *testing.T) {
			got, gotErr := ws.Grep(context.Background(), query.pattern, query.glob)
			want, wantErr := referenceGrep(query.pattern, files, query.selectPath)
			if !sameError(gotErr, wantErr) {
				t.Fatalf("error = %v, reference = %v", gotErr, wantErr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("matches differ:\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestGrepFindsActualIgnoredNestedWorktree(t *testing.T) {
	root := t.TempDir()
	initIgnoredWorktree(t, root, 1)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, glob := range []string{
		".scratch/worktrees/target/internal/**/*.go",
		"./.scratch/worktrees/target/internal/**/*.go",
		"/.scratch/worktrees/target/internal/**/*.go",
	} {
		got, err := ws.Grep(context.Background(), "ignored-worktree-needle", glob)
		if err != nil {
			t.Fatalf("Grep(%q): %v", glob, err)
		}
		if want := []tool.GrepMatch{{Path: ".scratch/worktrees/target/internal/deep/proof-000.go", Line: 1, Text: "package deep // ignored-worktree-needle"}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Grep(%q) = %#v, want %#v", glob, got, want)
		}
	}
}

func TestGrepCancellationDuringLineScan(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(context.Background(), "many.txt", []byte(strings.Repeat("matchable line\n", 10_000))); err != nil {
		t.Fatal(err)
	}
	ctx := &grepCountingCancelContext{Context: context.Background(), cancelAt: 20}
	_, err = ws.Grep(ctx, ".", "*.txt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Grep error = %v, want cancellation during line scan", err)
	}
}

func TestGrepNegativeLiteralPrefilterHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(context.Background(), "haystack.txt", []byte(strings.Repeat("haystack\n", 1_000))); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "haystack.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := &grepCountingCancelContext{Context: context.Background(), cancelAt: 2}
	search := grepSearch{
		ctx:      ctx,
		re:       regexp.MustCompile("absent-needle"),
		literal:  []byte("absent-needle"),
		maxFiles: maxGrepFiles,
		maxBytes: maxGrepBytes,
	}
	if err := search.scan(ws, "haystack.txt", info); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan error = %v, want cancellation checked after the whole-file prefilter", err)
	}
}

func TestGrepPreservesSortedOrderAndMatchCap(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.txt", "a.txt"} {
		if err := ws.Write(context.Background(), name, []byte(strings.Repeat("hit\n", 150))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ws.Grep(context.Background(), "hit", "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != grepMatchLimit {
		t.Fatalf("matches = %d, want cap %d", len(got), grepMatchLimit)
	}
	if got[0] != (tool.GrepMatch{Path: "a.txt", Line: 1, Text: "hit"}) || got[149] != (tool.GrepMatch{Path: "a.txt", Line: 150, Text: "hit"}) || got[150] != (tool.GrepMatch{Path: "b.txt", Line: 1, Text: "hit"}) || got[200] != (tool.GrepMatch{Path: "b.txt", Line: 51, Text: "hit"}) {
		t.Fatalf("ordered cap boundaries = first:%#v a-last:%#v b-first:%#v last:%#v", got[0], got[149], got[150], got[200])
	}
}

type grepCountingCancelContext struct {
	context.Context
	checks   int
	cancelAt int
}

func (c *grepCountingCancelContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func referenceGrep(pattern string, files map[string][]byte, selectPath func(string) bool) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("osfs: invalid grep pattern: %w", err)
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var matches []tool.GrepMatch
	for _, path := range paths {
		data := files[path]
		if !selectPath(path) || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		for lineNo, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, tool.GrepMatch{Path: path, Line: lineNo + 1, Text: line})
				if len(matches) == grepMatchLimit {
					return matches, nil
				}
			}
		}
	}
	return matches, nil
}

func sameError(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Error() == b.Error()
}

func writeGrepFixture(t testing.TB, root string, files map[string][]byte) {
	t.Helper()
	for path, data := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("MkdirAll(%q): %v", path, err)
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
}

func initIgnoredWorktree(t testing.TB, root string, files int) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for linked-worktree coverage")
	}
	home := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + filepath.Join(home, "xdg"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_AUTHOR_NAME=grep fixture",
			"GIT_AUTHOR_EMAIL=grep@example.invalid",
			"GIT_COMMITTER_NAME=grep fixture",
			"GIT_COMMITTER_EMAIL=grep@example.invalid",
		}
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("git %v: %v: %s", args, runErr, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".scratch/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("init")
	runGit("add", ".gitignore")
	runGit("commit", "-m", "fixture")
	target := filepath.Join(root, ".scratch", "worktrees", "target")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit("worktree", "add", "-b", "grep-target", target)
	for i := 0; i < files; i++ {
		path := filepath.Join(target, "internal", "deep", fmt.Sprintf("proof-%03d.go", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("package deep // ignored-worktree-needle\nvar Value%d = %q\n", i, strings.Repeat("payload ", 100))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkWorkspaceGrep(b *testing.B) {
	root := b.TempDir()
	files := make(map[string][]byte)
	for file := 0; file < 360; file++ {
		var body bytes.Buffer
		for line := 0; line < 120; line++ {
			fmt.Fprintf(&body, "record=%04d line=%03d ordinary payload alpha beta gamma delta\n", file, line)
		}
		if file%97 == 0 {
			body.WriteString("rare literal stateless-needle appears here\n")
		}
		if file == 359 {
			body.WriteString(strings.Repeat("L", 128<<10))
			body.WriteString(" longline-token \xff\n")
		}
		files[fmt.Sprintf("group-%02d/file-%04d.txt", file%12, file)] = body.Bytes()
	}
	writeGrepFixture(b, root, files)
	initIgnoredWorktree(b, root, 48)
	ws, err := NewWorkspace(root)
	if err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name, pattern, glob string
	}{
		{name: "literal-missing", pattern: "absent-stateless-literal", glob: "**/*.txt"},
		{name: "literal-rare", pattern: "stateless-needle", glob: "**/*.txt"},
		{name: "broad-capped", pattern: `record=[0-9]+`, glob: "**/*.txt"},
		{name: "short-literal", pattern: "alpha", glob: "**/*.txt"},
		{name: "nonprefix", pattern: `[a-z]+ beta`, glob: "**/*.txt"},
		{name: "casefold", pattern: `(?i)ALPHA`, glob: "**/*.txt"},
		{name: "alternation", pattern: `alpha|omega`, glob: "**/*.txt"},
		{name: "longline-invalid-utf8", pattern: `longline-token.�`, glob: "**/*.txt"},
		{name: "ignored-nested-worktree", pattern: "ignored-worktree-needle", glob: ".scratch/worktrees/target/internal/**/*.go"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ws.Grep(context.Background(), tc.pattern, tc.glob); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
