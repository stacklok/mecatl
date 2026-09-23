package boatenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	// maxTreeEntries bounds one guest listing; a pattern whose static base
	// holds more entries must be narrowed.
	maxTreeEntries = 200_000

	// Grep budgets, matching the osfs workspace so the same search behaves the
	// same on either backend.
	maxGrepFiles   = 10_000
	maxGrepBytes   = 64 << 20
	grepMatchLimit = 201
)

const grepSafetyBudgetMessage = "grep search exceeds the workspace safety budget; narrow the path (for example, internal/**/*.go)"

// normalizeGlobPattern matches the osfs and memfs leniency: strip a leading
// "/" and any leading "./"; an empty or "." pattern matches nothing.
func normalizeGlobPattern(pattern string) string {
	pat := strings.TrimPrefix(pattern, "/")
	for strings.HasPrefix(pat, "./") {
		pat = pat[2:]
	}
	pat = strings.TrimPrefix(pat, "/")
	if pat == "." {
		return ""
	}
	return pat
}

// checkGlobPattern rejects malformed patterns and patterns that climb out of
// the workspace before any guest call.
func checkGlobPattern(pat string) error {
	if strings.ContainsRune(pat, '\x00') {
		return errors.New("boatenv: glob pattern contains NUL")
	}
	if !doublestar.ValidatePattern(pat) {
		return doublestar.ErrBadPattern
	}
	for _, part := range strings.Split(pat, "/") {
		if part == ".." {
			return fmt.Errorf("boatenv: glob pattern %q: %w", pat, ErrPathEscape)
		}
	}
	return nil
}

// tree fetches the lstat listing under base, depth levels deep (0 lists
// everything below base).
func (w *workspace) tree(ctx context.Context, base string, depth int) (*listingFS, error) {
	if base == "." {
		base = ""
	}
	out, err := w.helper(ctx, helperRequest{Op: "tree", Base: base, MaxEntries: maxTreeEntries, MaxDepth: depth})
	if err != nil {
		var semantic helperSemanticError
		if errors.As(err, &semantic) && semantic.code == "too_many" {
			return nil, fmt.Errorf("boatenv: workspace listing under %q exceeds %d entries; narrow the pattern", base, maxTreeEntries)
		}
		return nil, classifyHelperError("glob", base, err)
	}
	return newListingFS(out.Tree), nil
}

// globWalk visits root-relative matches with the same traversal options and
// filters as the osfs workspace: never descend through symlinks, drop
// symlink leaves, and never surface the root itself.
func (w *workspace) globWalk(ctx context.Context, pat string, visit func(string, fs.DirEntry) error) error {
	base, rest := doublestar.SplitPattern(pat)
	// Only a globstar can match below the pattern's own depth, so list no
	// deeper than the pattern reaches.
	depth := 0
	if !strings.Contains(rest, "**") {
		depth = strings.Count(rest, "/") + 1
	}
	listing, err := w.tree(ctx, base, depth)
	if err != nil {
		return err
	}
	return doublestar.GlobWalk(listing, pat, func(p string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 || p == "." {
			return nil
		}
		return visit(p, entry)
	}, doublestar.WithFailOnIOErrors(), doublestar.WithNoFollow())
}

// Glob returns sorted workspace-relative paths matching the doublestar
// pattern, exactly as the osfs workspace does.
func (w *workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	pat := normalizeGlobPattern(pattern)
	if pat == "" {
		return nil, nil
	}
	if err := checkGlobPattern(pat); err != nil {
		return nil, err
	}
	var matches []string
	if err := w.globWalk(ctx, pat, func(p string, _ fs.DirEntry) error {
		matches = append(matches, p)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// grepFiles selects Grep's candidate files in the order the osfs workspace
// visits them: a WalkDir-ordered pass over regular files when there is no
// path glob, otherwise the glob walk's regular-file matches.
func (w *workspace) grepFiles(ctx context.Context, pathGlob string) ([]string, error) {
	if pathGlob == "" {
		listing, err := w.tree(ctx, "", 0)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, e := range listing.order {
			if e.Type == "f" {
				files = append(files, e.Path)
			}
		}
		return files, nil
	}
	pat := normalizeGlobPattern(pathGlob)
	if pat == "" {
		return nil, nil
	}
	if err := checkGlobPattern(pat); err != nil {
		return nil, err
	}
	var files []string
	err := w.globWalk(ctx, pat, func(p string, entry fs.DirEntry) error {
		if entry.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

// listingFS is a read-only fs.FS over a guest tree listing. Directory
// entries carry lstat types, so doublestar's WithNoFollow sees symlinks as
// symlinks, exactly as it does over an os.Root.
type listingFS struct {
	order    []treeEntry
	byPath   map[string]treeEntry
	children map[string][]string
}

var (
	_ fs.ReadDirFS = (*listingFS)(nil)
	_ fs.StatFS    = (*listingFS)(nil)
)

func newListingFS(entries []treeEntry) *listingFS {
	l := &listingFS{order: entries, byPath: make(map[string]treeEntry, len(entries)), children: map[string][]string{}}
	for _, e := range entries {
		l.byPath[e.Path] = e
		parent := pathpkg.Dir(e.Path)
		l.children[parent] = append(l.children[parent], pathpkg.Base(e.Path))
	}
	return l
}

func (*listingFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
}

func (l *listingFS) Stat(name string) (fs.FileInfo, error) {
	if name == "." {
		return listingInfo{entry: treeEntry{Path: ".", Type: "d"}}, nil
	}
	e, ok := l.byPath[name]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	if e.Type == "l" {
		// Stat follows an in-workspace link; a dangling or escaping one does
		// not exist, as on an os.Root.
		if e.Target == "" {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}
		e.Type = e.Target
	}
	return listingInfo{entry: e}, nil
}

func (l *listingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		e, ok := l.byPath[name]
		if !ok || e.Type != "d" {
			return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
		}
	}
	names := l.children[name]
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		p := n
		if name != "." {
			p = name + "/" + n
		}
		out = append(out, fs.FileInfoToDirEntry(listingInfo{entry: l.byPath[p]}))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

type listingInfo struct{ entry treeEntry }

func (i listingInfo) Name() string       { return pathpkg.Base(i.entry.Path) }
func (i listingInfo) Size() int64        { return i.entry.Size }
func (i listingInfo) ModTime() time.Time { return time.Unix(0, i.entry.MTime) }
func (i listingInfo) IsDir() bool        { return i.entry.Type == "d" }
func (listingInfo) Sys() any             { return nil }

func (i listingInfo) Mode() fs.FileMode {
	mode := fs.FileMode(i.entry.Perm & 0o777)
	switch i.entry.Type {
	case "d":
		mode |= fs.ModeDir
	case "l":
		mode |= fs.ModeSymlink
	case "o":
		mode |= fs.ModeIrregular
	}
	return mode
}
