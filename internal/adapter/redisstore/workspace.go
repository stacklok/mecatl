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
)

// Workspace is a principal-scoped, shell-less virtual filesystem backed by Redis.
type Workspace struct {
	clients *clientGenerations
	scope   string
}

var _ tool.Workspace = (*Workspace)(nil)
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
