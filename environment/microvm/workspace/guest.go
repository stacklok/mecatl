package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

var (
	errPathEscape  = errors.New("microvm workspace path escapes guest root")
	errResultBound = errors.New("microvm workspace result exceeds bound")
)

// Guest serves the confined filesystem side of the authenticated guest protocol.
type Guest struct {
	root *osRoot
	mu   sync.Mutex
}

// osRoot is the subset of os.Root used by the guest service.
type osRoot struct {
	rootFS *os.Root
}

var (
	// ErrAssignedRootUnavailable means the authenticated guest root could not be opened.
	// It deliberately carries no backend detail because callers project it across trust boundaries.
	ErrAssignedRootUnavailable = errors.New("microvm assigned root is unavailable")
)

// NewGuest opens root as the guest's /workspace mount.
func NewGuest(root string, binding control.Binding) (*Guest, error) {
	if root == "" || binding.Owner == "" || binding.SessionID == "" || binding.EnvironmentID == "" || binding.Ref == "" || binding.Generation == 0 {
		return nil, fmt.Errorf("microvm workspace root or binding is invalid")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, ErrAssignedRootUnavailable
	}
	return &Guest{root: &osRoot{rootFS: r}}, nil
}

// Handler returns the workspace service for the shared guest multiplexer.
func (g *Guest) Handler() control.Handler {
	return func(ctx context.Context, method string, payload json.RawMessage, _ func(any) error) (any, string, error) {
		if err := ctx.Err(); err != nil {
			return nil, "cancelled", err
		}
		if method != "call" {
			return nil, "unsupported_method", errors.New("unsupported microvm workspace method")
		}
		var req request
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, "malformed", control.ErrMalformedFrame
		}
		resp := g.handle(req)
		return resp, "", nil
	}
}

// Close releases the guest's confined workspace root.
func (g *Guest) Close() error { return g.root.rootFS.Close() }

func (g *Guest) handle(req request) response {
	var resp response
	var err error
	switch req.Operation {
	case opRead:
		resp.Data, resp.Version, err = g.readVersion(req.Path)
		resp.VersionValid = err == nil
	case opStat:
		var info wireFileInfo
		info, err = g.stat(req.Path)
		resp.Info = &info
	case opCreate:
		resp.Version, err = g.create(req.Path, req.Data)
		resp.VersionValid = err == nil
	case opReplace:
		resp.Version, err = g.replace(req.Path, req.Version, req.VersionValid, req.Data)
		resp.VersionValid = err == nil
	case opGlob:
		resp.Paths, err = g.glob(req.Pattern)
	case opGrep:
		resp.Matches, err = g.grep(req.Pattern, req.PathGlob)
	default:
		err = errors.New("unsupported microvm workspace operation")
	}
	if err != nil {
		return errorResponse(err)
	}
	return resp
}

func confined(path string) (string, error) {
	if path == "" || !filepath.IsLocal(filepath.FromSlash(path)) {
		return "", errPathEscape
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))), nil
}

func (g *Guest) read(path string) ([]byte, error) {
	rel, err := confined(path)
	if err != nil {
		return nil, err
	}
	file, err := g.root.rootFS.Open(rel)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxContentSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > maxContentSize {
		return nil, errResultBound
	}
	return data, nil
}

func (g *Guest) readVersion(path string) ([]byte, string, error) {
	data, err := g.read(path)
	if err != nil {
		return nil, "", err
	}
	return data, version(data), nil
}

func version(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (g *Guest) stat(path string) (wireFileInfo, error) {
	rel, err := confined(path)
	if err != nil {
		return wireFileInfo{}, err
	}
	info, err := g.root.rootFS.Stat(rel)
	if err != nil {
		return wireFileInfo{}, err
	}
	return wireFileInfo{Name: info.Name(), Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime(), IsDir: info.IsDir()}, nil
}

func (g *Guest) create(path string, data []byte) (string, error) {
	if len(data) > maxContentSize {
		return "", errResultBound
	}
	rel, err := confined(path)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if dir := filepath.Dir(rel); dir != "." {
		if err := g.root.rootFS.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	file, err := g.root.rootFS.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = g.root.rootFS.Remove(rel)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = g.root.rootFS.Remove(rel)
		return "", err
	}
	return version(data), nil
}

func (g *Guest) replace(path, old string, oldValid bool, data []byte) (string, error) {
	if len(data) > maxContentSize {
		return "", errResultBound
	}
	rel, err := confined(path)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current, err := g.read(rel)
	if err != nil {
		return "", err
	}
	if !oldValid || version(current) != old {
		return "", &tool.VersionMismatchError{Path: path}
	}

	temp, file, err := g.createReplaceTemp(rel)
	if err != nil {
		return "", err
	}
	cleanup := func() { _ = g.root.rootFS.Remove(temp) }
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return "", err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", err
	}
	latest, err := g.read(rel)
	if err != nil {
		cleanup()
		return "", err
	}
	if version(latest) != old {
		cleanup()
		return "", &tool.VersionMismatchError{Path: path}
	}
	if err := g.root.rootFS.Rename(temp, rel); err != nil {
		cleanup()
		return "", err
	}
	return version(data), nil
}

func (g *Guest) createReplaceTemp(path string) (string, *os.File, error) {
	for range 8 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", nil, err
		}
		temp := path + ".mecatl-replace-" + hex.EncodeToString(suffix[:])
		file, err := g.root.rootFS.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return temp, file, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("microvm workspace could not allocate replacement file")
}

func (g *Guest) glob(pattern string) ([]string, error) {
	pattern, err := confined(pattern)
	if err != nil {
		return nil, err
	}
	paths, err := doublestar.Glob(g.root.rootFS.FS(), pattern, doublestar.WithFilesOnly())
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) > maxResults {
		return nil, errResultBound
	}
	return paths, nil
}

func (g *Guest) grep(pattern, pathGlob string) ([]tool.GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if pathGlob != "" {
		pathGlob, err = confined(pathGlob)
		if err != nil {
			return nil, err
		}
	}
	var matches []tool.GrepMatch
	err = fs.WalkDir(g.root.rootFS.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if pathGlob != "" {
			matched, matchErr := doublestar.Match(pathGlob, path)
			if matchErr != nil || !matched {
				return matchErr
			}
		}
		data, readErr := g.read(path)
		if readErr != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, tool.GrepMatch{Path: path, Line: i + 1, Text: line})
				if len(matches) > maxResults {
					return errResultBound
				}
			}
		}
		return nil
	})
	return matches, err
}
