package redisstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/tool"
)

const (
	workspaceKeyPrefix = "mecatl:workspace:"
	workspaceFormat    = "redis-workspace/1"
	workspaceRoot      = "/workspace"
	workspaceMetaField = "m"
	workspaceData      = "d:"
	workspaceVersion   = "v:"
)

var (
	createWorkspaceFile = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'm') ~= ARGV[1] then return -2 end
if redis.call('HEXISTS', KEYS[1], ARGV[2]) == 1 or redis.call('HEXISTS', KEYS[1], ARGV[3]) == 1 then return 0 end
redis.call('HSET', KEYS[1], ARGV[2], ARGV[4], ARGV[3], ARGV[5])
return 1`)
	replaceWorkspaceFile = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'm') ~= ARGV[1] then return -3 end
local data = redis.call('HGET', KEYS[1], ARGV[2])
local version = redis.call('HGET', KEYS[1], ARGV[3])
if not data and not version then return -1 end
if not data or not version then return -3 end
if version ~= ARGV[4] then return 0 end
redis.call('HSET', KEYS[1], ARGV[2], ARGV[5], ARGV[3], ARGV[6])
return 1`)
	removeWorkspacePath = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'm') ~= ARGV[1] then return -3 end
local data = redis.call('HGET', KEYS[1], ARGV[2])
local version = redis.call('HGET', KEYS[1], ARGV[3])
if data or version then
  if not data or not version then return -3 end
  redis.call('HDEL', KEYS[1], ARGV[2], ARGV[3])
  return 1
end
for _, field in ipairs(redis.call('HKEYS', KEYS[1])) do
  if string.sub(field, 1, string.len(ARGV[4])) == ARGV[4] then return -2 end
end
return -1`)
	copyWorkspaceFile = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'm') ~= ARGV[1] then return -3 end
local data = redis.call('HGET', KEYS[1], ARGV[2])
local version = redis.call('HGET', KEYS[1], ARGV[3])
if not data and not version then return -1 end
if not data or not version then return -3 end
if version ~= ARGV[7] then return -2 end
if redis.call('HEXISTS', KEYS[1], ARGV[4]) == 1 or redis.call('HEXISTS', KEYS[1], ARGV[5]) == 1 then return 0 end
local parent = string.match(string.sub(ARGV[4], 3), '^(.*)/[^/]+$')
while parent do
  if redis.call('HEXISTS', KEYS[1], 'd:' .. parent) == 1 or redis.call('HEXISTS', KEYS[1], 'v:' .. parent) == 1 then return 0 end
  parent = string.match(parent, '^(.*)/[^/]+$')
end
for _, field in ipairs(redis.call('HKEYS', KEYS[1])) do
  if string.sub(field, 1, string.len(ARGV[6])) == ARGV[6] then return 0 end
end
redis.call('HSET', KEYS[1], ARGV[4], data, ARGV[5], version)
return 1`)
	renameWorkspacePath = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'm') ~= ARGV[1] then return -3 end
local fields = redis.call('HGETALL', KEYS[1])
local values = {}
for i = 1, #fields, 2 do values[fields[i]] = fields[i + 1] end
local oldData = 'd:' .. ARGV[2]
local oldVersion = 'v:' .. ARGV[2]
local newData = 'd:' .. ARGV[3]
local newVersion = 'v:' .. ARGV[3]
if values[newData] or values[newVersion] then return 0 end
local parent = string.match(ARGV[3], '^(.*)/[^/]+$')
while parent do
  if values['d:' .. parent] or values['v:' .. parent] then return 0 end
  parent = string.match(parent, '^(.*)/[^/]+$')
end
local newPrefix = ARGV[3] .. '/'
for field, _ in pairs(values) do
  if (string.sub(field, 1, 2) == 'd:' or string.sub(field, 1, 2) == 'v:') and string.sub(field, 3, 2 + string.len(newPrefix)) == newPrefix then return 0 end
end
local data = values[oldData]
local version = values[oldVersion]
if data or version then
  if not data or not version then return -3 end
  redis.call('HSET', KEYS[1], newData, data, newVersion, version)
  redis.call('HDEL', KEYS[1], oldData, oldVersion)
  return 1
end
local oldPrefix = ARGV[2] .. '/'
local moved = 0
for field, _ in pairs(values) do
  local kind = string.sub(field, 1, 2)
  local name = string.sub(field, 3)
  if (kind == 'd:' or kind == 'v:') and string.sub(name, 1, string.len(oldPrefix)) == oldPrefix then
    local target = kind .. ARGV[3] .. string.sub(name, string.len(ARGV[2]) + 1)
    if values[target] then return 0 end
    moved = moved + 1
  end
end
if moved == 0 then return -1 end
for field, value in pairs(values) do
  local kind = string.sub(field, 1, 2)
  local name = string.sub(field, 3)
  if (kind == 'd:' or kind == 'v:') and string.sub(name, 1, string.len(oldPrefix)) == oldPrefix then
    local target = kind .. ARGV[3] .. string.sub(name, string.len(ARGV[2]) + 1)
    redis.call('HSET', KEYS[1], target, value)
    redis.call('HDEL', KEYS[1], field)
  end
end
return 1`)
)

// Workspace is a principal-scoped, shell-less virtual filesystem backed by Redis.
type Workspace struct {
	clients *clientGenerations
	scope   string
}

var _ tool.Workspace = (*Workspace)(nil)
var _ tool.WorkspaceNamespace = (*Workspace)(nil)
var _ tool.AuthorityResourceResolver = (*Workspace)(nil)

// CreateWorkspace creates or opens a principal namespace. Reopening an existing
// namespace validates its format marker; it never repairs malformed state.
func (st *Store) CreateWorkspace(ctx context.Context, scope string) (*Workspace, error) {
	if scope == "" {
		return nil, errors.New("redisstore: empty workspace scope")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return nil, fmt.Errorf("redisstore: create workspace: %w", err)
	}
	defer release()
	key := workspaceKey(scope)
	created, err := client.HSetNX(ctx, key, workspaceMetaField, workspaceFormat).Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: create workspace: %w", err)
	}
	if !created {
		format, err := client.HGet(ctx, key, workspaceMetaField).Result()
		if err != nil || format != workspaceFormat {
			return nil, errors.New("redisstore: workspace namespace is unavailable or corrupt")
		}
	}
	return &Workspace{clients: st.clients, scope: scope}, nil
}

// OpenWorkspace reattaches an existing namespace and fails closed when it is missing.
func (st *Store) OpenWorkspace(ctx context.Context, scope string) (*Workspace, error) {
	if scope == "" {
		return nil, errors.New("redisstore: empty workspace scope")
	}
	w := &Workspace{clients: st.clients, scope: scope}
	client, release, err := st.clients.acquire()
	if err != nil {
		return nil, fmt.Errorf("redisstore: open workspace: %w", err)
	}
	defer release()
	format, err := client.HGet(ctx, workspaceKey(scope), workspaceMetaField).Result()
	if err != nil || format != workspaceFormat {
		return nil, errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
	return w, nil
}

// Root returns the stable virtual root; the principal namespace stays private.
func (*Workspace) Root() string { return workspaceRoot }

// AuthorityResourcePath projects a confined virtual path for policy evaluation.
func (*Workspace) AuthorityResourcePath(p string) (string, string, error) {
	key, err := cleanWorkspacePath(p)
	if err != nil {
		return "", workspaceRoot, err
	}
	return path.Join(workspaceRoot, key), workspaceRoot, nil
}

func (w *Workspace) Read(ctx context.Context, p string) ([]byte, error) {
	data, _, err := w.ReadVersion(ctx, p)
	return data, err
}

// ReadVersion atomically reads file content and its opaque content version.
func (w *Workspace) ReadVersion(ctx context.Context, p string) ([]byte, tool.FileVersion, error) {
	key, err := cleanWorkspacePath(p)
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	client, release, err := w.clients.acquire()
	if err != nil {
		return nil, tool.FileVersion{}, fmt.Errorf("redisstore: read workspace file: %w", err)
	}
	defer release()
	values, err := client.HMGet(ctx, workspaceKey(w.scope), workspaceMetaField, workspaceData+key, workspaceVersion+key).Result()
	if err != nil {
		return nil, tool.FileVersion{}, fmt.Errorf("redisstore: read workspace file: %w", err)
	}
	if len(values) != 3 || values[0] != workspaceFormat {
		return nil, tool.FileVersion{}, errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
	if values[1] == nil && values[2] == nil {
		return nil, tool.FileVersion{}, fmt.Errorf("redisstore: open %q: %w", p, fs.ErrNotExist)
	}
	data, dataOK := values[1].(string)
	version, versionOK := values[2].(string)
	if !dataOK || !versionOK || version != versionToken([]byte(data)) {
		return nil, tool.FileVersion{}, errors.New("redisstore: workspace file record is corrupt")
	}
	return []byte(data), tool.NewFileVersion(version), nil
}

// CreateFile atomically creates a file and refuses an existing path.
func (w *Workspace) CreateFile(ctx context.Context, p string, data []byte) (tool.FileVersion, error) {
	key, err := cleanWorkspacePath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	version := versionToken(data)
	result, err := w.runScript(ctx, createWorkspaceFile, workspaceFormat, workspaceData+key, workspaceVersion+key, data, version)
	if err != nil {
		return tool.FileVersion{}, fmt.Errorf("redisstore: create workspace file: %w", err)
	}
	switch result {
	case 1:
		return tool.NewFileVersion(version), nil
	case 0:
		return tool.FileVersion{}, fmt.Errorf("redisstore: create %q: %w", p, fs.ErrExist)
	default:
		return tool.FileVersion{}, errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
}

// ReplaceFile atomically replaces a file only when its version still matches.
func (w *Workspace) ReplaceFile(ctx context.Context, p string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	key, err := cleanWorkspacePath(p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	oldToken, err := tool.EncodeFileVersion(old)
	if err != nil {
		// Empty is not a valid workspace version token (real versions are SHA-256
		// hex), so it safely reaches the atomic script: a missing file still reports
		// not-exist, while an existing file reports a version mismatch.
		oldToken = ""
	}
	version := versionToken(data)
	result, err := w.runScript(ctx, replaceWorkspaceFile, workspaceFormat, workspaceData+key, workspaceVersion+key, oldToken, data, version)
	if err != nil {
		return tool.FileVersion{}, fmt.Errorf("redisstore: replace workspace file: %w", err)
	}
	switch result {
	case 1:
		return tool.NewFileVersion(version), nil
	case 0:
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: p}
	case -1:
		return tool.FileVersion{}, fmt.Errorf("redisstore: replace %q: %w", p, fs.ErrNotExist)
	default:
		return tool.FileVersion{}, errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
}

// Stat returns metadata for a virtual regular file.
func (w *Workspace) Stat(ctx context.Context, p string) (tool.FileInfo, error) {
	data, _, err := w.ReadVersion(ctx, p)
	if err != nil {
		return tool.FileInfo{}, err
	}
	return tool.FileInfo{Name: path.Base(p), Size: int64(len(data)), Mode: 0o644, ModTime: time.Time{}}, nil
}

// ReadDir returns immediate children of a prefix-derived virtual directory.
func (w *Workspace) ReadDir(ctx context.Context, p string) ([]tool.FileInfo, error) {
	dir, err := cleanWorkspaceDir(p)
	if err != nil {
		return nil, err
	}
	files, err := w.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if dir != "" {
		if _, ok := files[dir]; ok {
			return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrInvalid}
		}
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	entries := make(map[string]tool.FileInfo)
	for name, data := range files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		child, _, nested := strings.Cut(rest, "/")
		if nested {
			entries[child] = tool.FileInfo{Name: child, Mode: fs.ModeDir | 0o755, IsDir: true}
		} else if child != "" {
			entries[child] = tool.FileInfo{Name: child, Size: int64(len(data)), Mode: 0o644}
		}
	}
	if len(entries) == 0 && dir != "" {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: fs.ErrNotExist}
	}
	out := make([]tool.FileInfo, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove atomically removes one regular file and refuses non-recursive removal
// of a prefix-derived directory.
func (w *Workspace) Remove(ctx context.Context, p string) error {
	key, err := cleanWorkspacePath(p)
	if err != nil {
		return err
	}
	if _, err := w.snapshot(ctx); err != nil {
		return err
	}
	result, err := w.runScript(ctx, removeWorkspacePath, workspaceFormat, workspaceData+key, workspaceVersion+key, workspaceData+key+"/")
	if err != nil {
		return fmt.Errorf("redisstore: remove workspace path: %w", err)
	}
	switch result {
	case 1:
		return nil
	case -1:
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
	case -2:
		return &fs.PathError{Op: "remove", Path: p, Err: tool.ErrDirectoryNotEmpty}
	default:
		return errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
}

// Rename atomically moves one file or a complete virtual-directory prefix and
// refuses an existing destination.
func (w *Workspace) Rename(ctx context.Context, oldPath, newPath string) error {
	oldKey, err := cleanWorkspacePath(oldPath)
	if err != nil {
		return err
	}
	newKey, err := cleanWorkspacePath(newPath)
	if err != nil {
		return err
	}
	if strings.HasPrefix(newKey, oldKey+"/") {
		return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrInvalid}
	}
	if _, err := w.snapshot(ctx); err != nil {
		return err
	}
	result, err := w.runScript(ctx, renameWorkspacePath, workspaceFormat, oldKey, newKey)
	if err != nil {
		return fmt.Errorf("redisstore: rename workspace path: %w", err)
	}
	switch result {
	case 1:
		return nil
	case 0:
		return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrExist}
	case -1:
		return &fs.PathError{Op: "rename", Path: oldPath, Err: fs.ErrNotExist}
	default:
		return errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
}

// CopyFile atomically copies one regular file to an absent destination.
func (w *Workspace) CopyFile(ctx context.Context, source, destination string) (tool.FileVersion, error) {
	src, err := cleanWorkspacePath(source)
	if err != nil {
		return tool.FileVersion{}, err
	}
	dst, err := cleanWorkspacePath(destination)
	if err != nil {
		return tool.FileVersion{}, err
	}
	files, err := w.snapshot(ctx)
	if err != nil {
		return tool.FileVersion{}, err
	}
	expected := ""
	if data, ok := files[src]; ok {
		expected = versionToken(data)
	}
	result, err := w.runScript(ctx, copyWorkspaceFile, workspaceFormat, workspaceData+src, workspaceVersion+src, workspaceData+dst, workspaceVersion+dst, workspaceData+dst+"/", expected)
	if err != nil {
		return tool.FileVersion{}, fmt.Errorf("redisstore: copy workspace file: %w", err)
	}
	switch result {
	case 1:
		return tool.NewFileVersion(expected), nil
	case 0:
		return tool.FileVersion{}, &fs.PathError{Op: "copy", Path: destination, Err: fs.ErrExist}
	case -1:
		if _, dirErr := w.ReadDir(ctx, source); dirErr == nil {
			return tool.FileVersion{}, &fs.PathError{Op: "copy", Path: source, Err: fs.ErrInvalid}
		}
		return tool.FileVersion{}, &fs.PathError{Op: "copy", Path: source, Err: fs.ErrNotExist}
	case -2:
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: source}
	default:
		return tool.FileVersion{}, errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
}

// Glob returns sorted paths matching a doublestar pattern.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	files, err := w.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	pat := normalizeWorkspaceGlob(pattern)
	if pat == "" {
		return nil, nil
	}
	out := make([]string, 0)
	for name := range files {
		matched, err := doublestar.Match(pat, name)
		if err != nil {
			return nil, err
		}
		if matched {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Grep searches non-binary files and returns deterministic path/line matches.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("redisstore: invalid grep pattern: %w", err)
	}
	files, err := w.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	pat := normalizeWorkspaceGlob(pathGlob)
	var names []string
	for name := range files {
		if pat != "" {
			matched, matchErr := doublestar.Match(pat, name)
			if matchErr != nil {
				return nil, matchErr
			}
			if !matched {
				continue
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var matches []tool.GrepMatch
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data := files[name]
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, tool.GrepMatch{Path: name, Line: i + 1, Text: line})
			}
		}
	}
	return matches, nil
}

func (w *Workspace) snapshot(ctx context.Context) (map[string][]byte, error) {
	client, release, err := w.clients.acquire()
	if err != nil {
		return nil, fmt.Errorf("redisstore: list workspace: %w", err)
	}
	defer release()
	all, err := client.HGetAll(ctx, workspaceKey(w.scope)).Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: list workspace: %w", err)
	}
	if all[workspaceMetaField] != workspaceFormat {
		return nil, errors.New("redisstore: workspace namespace is unavailable or corrupt")
	}
	files := make(map[string][]byte)
	for field, data := range all {
		if !strings.HasPrefix(field, workspaceData) {
			continue
		}
		name := strings.TrimPrefix(field, workspaceData)
		version, ok := all[workspaceVersion+name]
		if !ok || version != versionToken([]byte(data)) {
			return nil, errors.New("redisstore: workspace file record is corrupt")
		}
		files[name] = []byte(data)
	}
	for field := range all {
		if strings.HasPrefix(field, workspaceVersion) {
			if _, ok := files[strings.TrimPrefix(field, workspaceVersion)]; !ok {
				return nil, errors.New("redisstore: workspace file record is corrupt")
			}
		}
	}
	return files, nil
}

func (w *Workspace) runScript(ctx context.Context, script *redis.Script, args ...any) (int64, error) {
	client, release, err := w.clients.acquire()
	if err != nil {
		return 0, err
	}
	defer release()
	return script.Run(ctx, client, []string{workspaceKey(w.scope)}, args...).Int64()
}

func workspaceKey(scope string) string { return workspaceKeyPrefix + scope }

func versionToken(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cleanWorkspacePath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("redisstore: path escapes workspace: %q", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("redisstore: path escapes workspace: %q", p)
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return "", fmt.Errorf("redisstore: path escapes workspace: %q", p)
	}
	return cleaned, nil
}

func cleanWorkspaceDir(p string) (string, error) {
	if p == "" || p == "." {
		return "", nil
	}
	return cleanWorkspacePath(p)
}

func normalizeWorkspaceGlob(pattern string) string {
	pat := strings.TrimPrefix(pattern, "/")
	for strings.HasPrefix(pat, "./") {
		pat = strings.TrimPrefix(pat, "./")
	}
	if pat == "." {
		return ""
	}
	return pat
}
