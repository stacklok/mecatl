package boatenv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	pathpkg "path"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

type workspace struct {
	client    *apiClient
	sandboxID string
	workdir   string
}

var (
	_ tool.Workspace                 = (*workspace)(nil)
	_ tool.WorkspaceNamespace        = (*workspace)(nil)
	_ tool.AuthorityResourceResolver = (*workspace)(nil)
)

// authorityResolveTimeout bounds the guest round trip AuthorityResourcePath
// makes; the interface carries no context.
const authorityResolveTimeout = 30 * time.Second

func (w *workspace) Root() string { return "boat:" + w.sandboxID + ":" + w.workdir }

// AuthorityResourcePath derives the physical identity authority policy sees.
// It asks the guest, which resolves symlinks with the same confinement rules
// every file operation uses, so a link cannot aim policy at one file and the
// access at another. Both returned paths are absolute guest paths.
func (w *workspace) AuthorityResourcePath(p string) (target, root string, err error) {
	clean, err := cleanPath(p, true)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), authorityResolveTimeout)
	defer cancel()
	out, err := w.helper(ctx, helperRequest{Op: "resolve", Path: clean})
	if err != nil {
		return "", "", classifyHelperError("resolve", clean, out, err)
	}
	if !pathpkg.IsAbs(out.Target) || !pathpkg.IsAbs(out.Root) {
		return "", "", errors.New("boatenv: helper returned a non-absolute resource identity")
	}
	return out.Target, out.Root, nil
}

func (w *workspace) Read(ctx context.Context, p string) ([]byte, error) {
	clean, err := cleanPath(p, false)
	if err != nil {
		return nil, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "read", Path: clean})
	if err != nil {
		return nil, classifyHelperError("read", clean, out, err)
	}
	data, err := base64.StdEncoding.DecodeString(out.Content)
	if err != nil {
		return nil, errors.New("boatenv: helper returned invalid file content")
	}
	return data, nil
}

func (w *workspace) ReadVersion(ctx context.Context, p string) ([]byte, tool.FileVersion, error) {
	clean, err := cleanPath(p, false)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "read", Path: clean})
	if err != nil {
		return nil, tool.FileVersion{}, classifyHelperError("read", clean, out, err)
	}
	data, err := base64.StdEncoding.DecodeString(out.Content)
	if err != nil || out.Version == "" {
		return nil, tool.FileVersion{}, errors.New("boatenv: helper returned invalid versioned content")
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
		return tool.FileInfo{}, classifyHelperError("stat", clean, out, err)
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
		return tool.FileVersion{}, classifyHelperError("create", clean, out, err)
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
		return tool.FileVersion{}, classifyHelperError("replace", clean, out, err)
	}
	if out.Version == "" {
		return tool.FileVersion{}, errors.New("boatenv: helper omitted replacement file version")
	}
	return tool.NewFileVersion(out.Version), nil
}

func (w *workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	clean, err := cleanPattern(pattern)
	if err != nil {
		return nil, err
	}
	out, err := w.helper(ctx, helperRequest{Op: "glob", Pattern: clean})
	if err != nil {
		return nil, classifyHelperError("glob", clean, out, err)
	}
	return out.Paths, nil
}

func (w *workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	if _, err := compilePatternGuard(pattern); err != nil {
		return nil, err
	}
	if pathGlob != "" {
		var err error
		pathGlob, err = cleanPattern(pathGlob)
		if err != nil {
			return nil, err
		}
	}
	out, err := w.helper(ctx, helperRequest{Op: "grep", Pattern: pattern, PathGlob: pathGlob})
	if err != nil {
		return nil, classifyHelperError("grep", pathGlob, out, err)
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
		return nil, classifyHelperError("readdir", clean, out, err)
	}
	entries := make([]tool.FileInfo, 0, len(out.Entries))
	for _, entry := range out.Entries {
		entries = append(entries, entry.toolFileInfo())
	}
	return entries, nil
}

func (w *workspace) Remove(ctx context.Context, p string) error {
	clean, err := cleanPath(p, false)
	if err != nil {
		return err
	}
	out, err := w.helper(ctx, helperRequest{Op: "remove", Path: clean})
	return classifyHelperError("remove", clean, out, err)
}

func (w *workspace) Rename(ctx context.Context, oldPath, newPath string) error {
	oldClean, err := cleanPath(oldPath, false)
	if err != nil {
		return err
	}
	newClean, err := cleanPath(newPath, false)
	if err != nil {
		return err
	}
	out, err := w.helper(ctx, helperRequest{Op: "rename", OldPath: oldClean, NewPath: newClean})
	return classifyHelperError("rename", oldClean, out, err)
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
		return tool.FileVersion{}, classifyHelperError("copy", src, out, err)
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
		return "", errors.New("boatenv: path escapes the Boat workspace")
	}
	if clean == "." && !allowRoot {
		return "", errors.New("boatenv: operation requires a file path")
	}
	return clean, nil
}

func cleanPattern(pattern string) (string, error) {
	if pattern == "" {
		return "", errors.New("boatenv: glob pattern is required")
	}
	if strings.ContainsRune(pattern, '\x00') || strings.HasPrefix(pattern, "/") {
		return "", errors.New("boatenv: glob pattern must stay relative to the Boat workspace")
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == ".." {
			return "", errors.New("boatenv: glob pattern escapes the Boat workspace")
		}
	}
	return pattern, nil
}

func compilePatternGuard(pattern string) (string, error) {
	if strings.ContainsRune(pattern, '\x00') {
		return "", errors.New("boatenv: grep pattern contains NUL")
	}
	return pattern, nil
}

type helperRequest struct {
	Op       string `json:"op"`
	Path     string `json:"path,omitempty"`
	OldPath  string `json:"old_path,omitempty"`
	NewPath  string `json:"new_path,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	PathGlob string `json:"path_glob,omitempty"`
	Content  string `json:"content,omitempty"`
	Expected string `json:"expected,omitempty"`
	// ExpectedValid distinguishes a real (possibly empty) version token from
	// the zero FileVersion, which must match nothing.
	ExpectedValid bool `json:"expected_valid"`
}

type helperResponse struct {
	OK      bool          `json:"ok"`
	Code    string        `json:"code,omitempty"`
	Content string        `json:"content,omitempty"`
	Version string        `json:"version,omitempty"`
	Info    *helperInfo   `json:"info,omitempty"`
	Paths   []string      `json:"paths,omitempty"`
	Matches []helperMatch `json:"matches,omitempty"`
	Entries []helperInfo  `json:"entries,omitempty"`
	Target  string        `json:"target,omitempty"`
	Root    string        `json:"root,omitempty"`
}

type helperInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Perm      uint32 `json:"perm"`
	ModTimeNS int64  `json:"mod_time_ns"`
	IsDir     bool   `json:"is_dir"`
}

func (i helperInfo) toolFileInfo() tool.FileInfo {
	mode := fs.FileMode(i.Perm & 0o777)
	if i.IsDir {
		mode |= fs.ModeDir
	}
	return tool.FileInfo{Name: i.Name, Size: i.Size, Mode: mode, ModTime: time.Unix(0, i.ModTimeNS), IsDir: i.IsDir}
}

type helperMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (w *workspace) helper(ctx context.Context, req helperRequest) (helperResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return helperResponse{}, fmt.Errorf("boatenv: encode helper request: %w", err)
	}
	program := base64.StdEncoding.EncodeToString([]byte(pythonHelper))
	input := base64.StdEncoding.EncodeToString(payload)
	command := "python3 -c 'import base64;exec(base64.b64decode(\"" + program + "\"))' '" + input + "'"
	result, err := w.client.runCommand(ctx, w.sandboxID, w.workdir, command)
	if err != nil {
		return helperResponse{}, err
	}
	if result.ExitCode != 0 {
		return helperResponse{}, fmt.Errorf("boatenv: filesystem helper failed with exit code %d", result.ExitCode)
	}
	var out helperResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &out); err != nil {
		return helperResponse{}, errors.New("boatenv: filesystem helper returned invalid JSON")
	}
	if !out.OK {
		return out, helperSemanticError{code: out.Code}
	}
	return out, nil
}

type helperSemanticError struct{ code string }

func (e helperSemanticError) Error() string {
	return "boatenv: filesystem helper rejected operation: " + e.code
}

func classifyHelperError(op, p string, out helperResponse, err error) error {
	if err == nil {
		return nil
	}
	var semantic helperSemanticError
	if !errors.As(err, &semantic) {
		return err
	}
	switch out.Code {
	case "not_found":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, fs.ErrNotExist)
	case "exists":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, fs.ErrExist)
	case "version_mismatch":
		return &tool.VersionMismatchError{Path: p}
	case "not_empty":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, tool.ErrDirectoryNotEmpty)
	case "unsupported":
		return fmt.Errorf("boatenv: %s %q: %w", op, p, tool.ErrFileOperationUnsupported)
	default:
		return fmt.Errorf("boatenv: %s %q rejected by remote workspace", op, p)
	}
}

const pythonHelper = `
import base64, errno, fcntl, hashlib, json, os, pathlib, re, stat, sys, tempfile

def emit(value):
    print(json.dumps(value, separators=(",", ":")))
    raise SystemExit(0)

def fail(code):
    emit({"ok": False, "code": code})

def version(data):
    return hashlib.sha256(data).hexdigest()

req = json.loads(base64.b64decode(sys.argv[1]))
root = pathlib.Path(os.getcwd()).resolve()
root_s = str(root)

def confined(rel, allow_root=True):
    if not isinstance(rel, str) or "\x00" in rel or rel.startswith("/"):
        fail("invalid")
    parts = pathlib.PurePosixPath(rel or ".").parts
    if ".." in parts:
        fail("invalid")
    candidate = root.joinpath(*parts).resolve(strict=False)
    try:
        if os.path.commonpath([root_s, str(candidate)]) != root_s:
            fail("invalid")
    except ValueError:
        fail("invalid")
    if not allow_root and candidate == root:
        fail("invalid")
    return candidate

def info(path, name=None):
    st = path.stat()
    return {"name": name if name is not None else path.name, "size": st.st_size,
            "perm": stat.S_IMODE(st.st_mode), "mod_time_ns": st.st_mtime_ns,
            "is_dir": stat.S_ISDIR(st.st_mode)}

def locked():
    key = hashlib.sha256(root_s.encode()).hexdigest()
    f = open("/tmp/mecatl-boat-" + key + ".lock", "a+b")
    fcntl.flock(f.fileno(), fcntl.LOCK_EX)
    return f

op = req.get("op", "")
try:
    if op == "read":
        p = confined(req.get("path", ""), False)
        data = p.read_bytes()
        emit({"ok": True, "content": base64.b64encode(data).decode(), "version": version(data)})

    if op == "resolve":
        p = confined(req.get("path", "."), True)
        emit({"ok": True, "target": str(p), "root": root_s})

    if op == "stat":
        p = confined(req.get("path", "."), True)
        emit({"ok": True, "info": info(p, "." if p == root else p.name)})

    if op == "create":
        p = confined(req.get("path", ""), False)
        data = base64.b64decode(req.get("content", ""))
        with locked():
            p.parent.mkdir(parents=True, exist_ok=True)
            fd = os.open(str(p), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
            try:
                with os.fdopen(fd, "wb") as f:
                    f.write(data)
            except Exception:
                try: os.unlink(p)
                except OSError: pass
                raise
        emit({"ok": True, "version": version(data)})

    if op == "replace":
        p = confined(req.get("path", ""), False)
        data = base64.b64decode(req.get("content", ""))
        expected = req.get("expected", "")
        with locked():
            if not p.exists(): fail("not_found")
            if not p.is_file(): fail("unsupported")
            current = p.read_bytes()
            if not req.get("expected_valid") or version(current) != expected: fail("version_mismatch")
            old_mode = stat.S_IMODE(p.stat().st_mode)
            p.parent.mkdir(parents=True, exist_ok=True)
            fd, tmp = tempfile.mkstemp(prefix=".mecatl-write-", dir=str(p.parent))
            try:
                with os.fdopen(fd, "wb") as f:
                    f.write(data)
                    f.flush()
                    os.fsync(f.fileno())
                os.chmod(tmp, old_mode)
                os.replace(tmp, p)
            finally:
                try: os.unlink(tmp)
                except FileNotFoundError: pass
        emit({"ok": True, "version": version(data)})

    if op == "glob":
        pattern = req.get("pattern", "")
        if pattern.startswith("/") or ".." in pathlib.PurePosixPath(pattern).parts: fail("invalid")
        paths = []
        for raw in root.glob(pattern):
            p = raw.resolve(strict=False)
            try:
                if os.path.commonpath([root_s, str(p)]) != root_s: continue
            except ValueError:
                continue
            rel = p.relative_to(root).as_posix()
            if rel != ".": paths.append(rel)
        emit({"ok": True, "paths": sorted(set(paths))})

    if op == "grep":
        try: regex = re.compile(req.get("pattern", ""))
        except re.error: fail("invalid")
        pattern = req.get("path_glob") or "**/*"
        if pattern.startswith("/") or ".." in pathlib.PurePosixPath(pattern).parts: fail("invalid")
        hits = []
        for raw in root.glob(pattern):
            p = raw.resolve(strict=False)
            try:
                if os.path.commonpath([root_s, str(p)]) != root_s or not p.is_file(): continue
            except (ValueError, OSError):
                continue
            try: text = p.read_text(encoding="utf-8", errors="replace")
            except OSError: continue
            rel = p.relative_to(root).as_posix()
            for n, line in enumerate(text.splitlines(), 1):
                if regex.search(line):
                    hits.append({"path": rel, "line": n, "text": line})
                    if len(hits) >= 1000: emit({"ok": True, "matches": hits})
        emit({"ok": True, "matches": hits})

    if op == "readdir":
        p = confined(req.get("path", "."), True)
        if not p.exists(): fail("not_found")
        if not p.is_dir(): fail("unsupported")
        entries = [info(child, child.name) for child in sorted(p.iterdir(), key=lambda x: x.name)]
        emit({"ok": True, "entries": entries})

    if op == "remove":
        p = confined(req.get("path", ""), False)
        with locked():
            if not p.exists() and not p.is_symlink(): fail("not_found")
            if p.is_dir():
                try: p.rmdir()
                except OSError as e:
                    if e.errno in (errno.ENOTEMPTY, errno.EEXIST): fail("not_empty")
                    raise
            else: p.unlink()
        emit({"ok": True})

    if op == "rename":
        src = confined(req.get("old_path", ""), False)
        dst = confined(req.get("new_path", ""), False)
        with locked():
            if not src.exists() and not src.is_symlink(): fail("not_found")
            if dst.exists() or dst.is_symlink(): fail("exists")
            dst.parent.mkdir(parents=True, exist_ok=True)
            os.rename(src, dst)
        emit({"ok": True})

    if op == "copy":
        src = confined(req.get("old_path", ""), False)
        dst = confined(req.get("new_path", ""), False)
        with locked():
            if not src.exists(): fail("not_found")
            if not src.is_file(): fail("unsupported")
            if dst.exists() or dst.is_symlink(): fail("exists")
            dst.parent.mkdir(parents=True, exist_ok=True)
            data = src.read_bytes()
            fd = os.open(str(dst), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
            with os.fdopen(fd, "wb") as f: f.write(data)
        emit({"ok": True, "version": version(data)})

    fail("unsupported")
except FileNotFoundError:
    fail("not_found")
except FileExistsError:
    fail("exists")
except PermissionError:
    fail("unsupported")
except OSError as e:
    if e.errno in (errno.ENOTEMPTY, errno.EEXIST): fail("not_empty")
    fail("unsupported")
`
