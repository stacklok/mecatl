package boatenv

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	pathpkg "path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/tool"
)

type workspace struct {
	sandbox *sandbox
	lockDir string
}

var (
	_ tool.Workspace                 = (*workspace)(nil)
	_ tool.WorkspaceNamespace        = (*workspace)(nil)
	_ tool.AuthorityResourceResolver = (*workspace)(nil)
)

// authorityResolveTimeout bounds the guest round trip AuthorityResourcePath
// makes; the interface carries no context.
const authorityResolveTimeout = 30 * time.Second

// Root is derived from the ref alone, so the server can read it without
// waking the sandbox.
func (w *workspace) Root() string { return "boat:" + w.sandbox.id + ":" + w.sandbox.workdir }

// AuthorityResourcePath derives the physical identity authority policy sees.
// It asks the guest, which resolves symlinks with the same confinement rules
// every file operation uses, so a link cannot aim policy at one file and the
// access at another. Both returned paths are absolute guest paths.
func (w *workspace) AuthorityResourcePath(p string) (target, root string, err error) {
	clean, err := cleanPath(p, true)
	if err != nil {
		return "", "", err
	}
	// Readiness (possibly a resume) is bounded by the provider's ready
	// timeout, not by this call's short resolve budget.
	if err := w.sandbox.ensure(context.Background()); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), authorityResolveTimeout)
	defer cancel()
	out, err := w.helper(ctx, helperRequest{Op: "resolve", Path: clean})
	if err != nil {
		return "", "", classifyHelperError("resolve", clean, err)
	}
	if !pathpkg.IsAbs(out.Target) || !pathpkg.IsAbs(out.Root) {
		return "", "", errors.New("boatenv: helper returned a non-absolute resource identity")
	}
	return out.Target, out.Root, nil
}

func (w *workspace) Read(ctx context.Context, p string) ([]byte, error) {
	data, _, err := w.ReadVersion(ctx, p)
	return data, err
}

// ReadVersion returns content and its version from one guest read. Files
// above the inline limit are snapshotted in the guest and fetched in ranges,
// so the version always describes exactly the bytes returned.
func (w *workspace) ReadVersion(ctx context.Context, p string) ([]byte, tool.FileVersion, error) {
	clean, err := cleanPath(p, false)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "read", Path: clean, InlineLimit: inlineReadLimit, MaxBytes: maxReadBytes})
	if err != nil {
		return nil, tool.FileVersion{}, classifyHelperError("read", clean, err)
	}
	if out.Version == "" {
		return nil, tool.FileVersion{}, errors.New("boatenv: helper returned an unversioned read")
	}
	var data []byte
	if out.Snapshot != "" {
		if data, err = w.fetchSnapshot(ctx, out.Snapshot, out.Size); err != nil {
			return nil, tool.FileVersion{}, err
		}
	} else if data, err = base64.StdEncoding.DecodeString(out.Content); err != nil {
		return nil, tool.FileVersion{}, errors.New("boatenv: helper returned invalid file content")
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != out.Version {
		return nil, tool.FileVersion{}, errors.New("boatenv: file content does not match its version")
	}
	return data, tool.NewFileVersion(out.Version), nil
}

func (w *workspace) Stat(ctx context.Context, p string) (tool.FileInfo, error) {
	clean, err := cleanPath(p, true)
	if err != nil {
		return tool.FileInfo{}, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "stat", Path: clean})
	if err != nil {
		return tool.FileInfo{}, classifyHelperError("stat", clean, err)
	}
	if out.Info == nil {
		return tool.FileInfo{}, errors.New("boatenv: helper omitted file metadata")
	}
	return out.Info.toolFileInfo(), nil
}

func (w *workspace) CreateFile(ctx context.Context, p string, data []byte) (tool.FileVersion, error) {
	clean, err := cleanPath(p, false)
	if err != nil {
		return tool.FileVersion{}, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "create", Path: clean, Content: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return tool.FileVersion{}, classifyHelperError("create", clean, err)
	}
	if out.Version == "" {
		return tool.FileVersion{}, errors.New("boatenv: helper omitted created file version")
	}
	return tool.NewFileVersion(out.Version), nil
}

func (w *workspace) ReplaceFile(ctx context.Context, p string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	clean, err := cleanPath(p, false)
	if err != nil {
		return tool.FileVersion{}, err
	}
	// A zero FileVersion is never a wildcard. It still goes to the guest so a
	// missing path reports fs.ErrNotExist and an existing one a version mismatch.
	expected, err := tool.EncodeFileVersion(old)
	expectedValid := err == nil
	out, err := w.helper(ctx, helperRequest{Op: "replace", Path: clean, Expected: expected, ExpectedValid: expectedValid, Content: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return tool.FileVersion{}, classifyHelperError("replace", clean, err)
	}
	if out.Version == "" {
		return tool.FileVersion{}, errors.New("boatenv: helper omitted replacement file version")
	}
	return tool.NewFileVersion(out.Version), nil
}

// Grep matches an RE2 pattern line by line across the selected files. The
// pattern is compiled with Go's regexp first, so syntax errors read exactly
// as on the osfs workspace, then translated into an equivalent Python
// pattern that runs next to the files. Selection, line numbering (split on
// "\n"), binary skipping and budgets all mirror the osfs workspace.
func (w *workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("boatenv: invalid grep pattern: %w", err)
	}
	translated, err := translateRE2(pattern)
	if err != nil {
		return nil, fmt.Errorf("boatenv: grep pattern: %w", err)
	}
	var literal string
	if prefix, _ := re.LiteralPrefix(); prefix != "" && utf8.ValidString(prefix) && !strings.ContainsRune(prefix, utf8.RuneError) {
		literal = base64.StdEncoding.EncodeToString([]byte(prefix))
	}
	files, err := w.grepFiles(ctx, pathGlob)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	out, err := w.helper(ctx, helperRequest{
		Op: "grep", Pattern: translated, Literal: literal, Files: files,
		MaxMatches: grepMatchLimit, MaxFiles: maxGrepFiles, MaxBytes: maxGrepBytes,
	})
	if err != nil {
		var semantic helperSemanticError
		if errors.As(err, &semantic) && semantic.code == "budget" {
			return nil, errors.New(grepSafetyBudgetMessage)
		}
		return nil, classifyHelperError("grep", pathGlob, err)
	}
	matches := make([]tool.GrepMatch, 0, len(out.Matches))
	for _, hit := range out.Matches {
		matches = append(matches, tool.GrepMatch{Path: hit.Path, Line: hit.Line, Text: hit.Text})
	}
	return matches, nil
}

func (w *workspace) ReadDir(ctx context.Context, p string) ([]tool.FileInfo, error) {
	clean, err := cleanPath(p, true)
	if err != nil {
		return nil, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "readdir", Path: clean})
	if err != nil {
		return nil, classifyHelperError("readdir", clean, err)
	}
	entries := make([]tool.FileInfo, 0, len(out.Entries))
	for _, entry := range out.Entries {
		entries = append(entries, entry.toolFileInfo())
	}
	return entries, nil
}

// Remove deletes the named entry itself. A symlink is removed, never the
// file it points at.
func (w *workspace) Remove(ctx context.Context, p string) error {
	clean, err := cleanPath(p, false)
	if err != nil {
		return err
	}
	_, err = w.helper(ctx, helperRequest{Op: "remove", Path: clean})
	return classifyHelperError("remove", clean, err)
}

// Rename moves the named entry itself; a symlink moves as a link.
func (w *workspace) Rename(ctx context.Context, oldPath, newPath string) error {
	oldClean, err := cleanPath(oldPath, false)
	if err != nil {
		return err
	}
	newClean, err := cleanPath(newPath, false)
	if err != nil {
		return err
	}
	_, err = w.helper(ctx, helperRequest{Op: "rename", OldPath: oldClean, NewPath: newClean})
	return classifyHelperError("rename", oldClean, err)
}

func (w *workspace) CopyFile(ctx context.Context, source, destination string) (tool.FileVersion, error) {
	src, err := cleanPath(source, false)
	if err != nil {
		return tool.FileVersion{}, err
	}
	dst, err := cleanPath(destination, false)
	if err != nil {
		return tool.FileVersion{}, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "copy", OldPath: src, NewPath: dst})
	if err != nil {
		return tool.FileVersion{}, classifyHelperError("copy", src, err)
	}
	if out.Version == "" {
		return tool.FileVersion{}, errors.New("boatenv: helper omitted copied file version")
	}
	return tool.NewFileVersion(out.Version), nil
}

func cleanPath(value string, allowRoot bool) (string, error) {
	if strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") {
		return "", errors.New("boatenv: path must stay relative to the Boat workspace")
	}
	clean := pathpkg.Clean(value)
	if value == "" {
		clean = "."
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("boatenv: %q: %w", value, ErrPathEscape)
	}
	if clean == "." && !allowRoot {
		return "", errors.New("boatenv: operation requires a file path")
	}
	return clean, nil
}

type helperInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Perm      uint32 `json:"perm"`
	ModTimeNS int64  `json:"mod_time_ns"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink"`
}

func (i helperInfo) toolFileInfo() tool.FileInfo {
	mode := fs.FileMode(i.Perm & 0o777)
	switch {
	case i.IsSymlink:
		mode |= fs.ModeSymlink
	case i.IsDir:
		mode |= fs.ModeDir
	}
	return tool.FileInfo{Name: i.Name, Size: i.Size, Mode: mode, ModTime: time.Unix(0, i.ModTimeNS), IsDir: i.IsDir && !i.IsSymlink}
}
