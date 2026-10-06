// Package workspace implements the authenticated host/guest Workspace RPC adapter.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
)

// Workspace is the host-side mecatl adapter for one authenticated guest mount.
type Workspace struct {
	client   *control.Client
	root     string
	ledgerMu sync.Mutex
	ledger   map[string]tool.FileVersion
}

var _ tool.Workspace = (*Workspace)(nil)

// New constructs a Workspace over an already-authenticated multiplexed guest connection.
func New(client *control.Client, binding control.Binding) (*Workspace, error) {
	return NewAt(client, binding, guestRoot)
}

// NewAt constructs a Workspace affined to one authenticated logical guest root.
func NewAt(client *control.Client, binding control.Binding, root string) (*Workspace, error) {
	if client == nil || !client.IsBoundTo(binding) || root == "" {
		return nil, errors.New("invalid multiplexed microvm workspace configuration")
	}
	return &Workspace{client: client, root: root, ledger: make(map[string]tool.FileVersion)}, nil
}

// Root returns the authenticated guest execution root shared with the bound runner.
func (w *Workspace) Root() string { return w.root }

// Read reads bounded bytes from the guest workspace.
func (w *Workspace) Read(ctx context.Context, path string) ([]byte, error) {
	resp, err := w.call(ctx, request{Operation: opRead, Path: path})
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// ReadVersion returns guest bytes and their opaque authoritative version.
func (w *Workspace) ReadVersion(ctx context.Context, path string) ([]byte, tool.FileVersion, error) {
	resp, err := w.call(ctx, request{Operation: opRead, Path: path})
	if err != nil {
		return nil, tool.FileVersion{}, err
	}
	if !resp.VersionValid {
		return nil, tool.FileVersion{}, errors.New("microvm workspace omitted file version")
	}
	return resp.Data, tool.NewFileVersion(resp.Version), nil
}

// Stat returns guest file metadata without exposing OS-specific values.
func (w *Workspace) Stat(ctx context.Context, path string) (tool.FileInfo, error) {
	resp, err := w.call(ctx, request{Operation: opStat, Path: path})
	if err != nil {
		return tool.FileInfo{}, err
	}
	if resp.Info == nil {
		return tool.FileInfo{}, errors.New("microvm workspace omitted file metadata")
	}
	return tool.FileInfo{
		Name: resp.Info.Name, Size: resp.Info.Size, Mode: resp.Info.Mode,
		ModTime: resp.Info.ModTime, IsDir: resp.Info.IsDir,
	}, nil
}

// CreateFile performs an atomic create-only guest mutation.
func (w *Workspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	resp, err := w.call(ctx, request{Operation: opCreate, Path: path, Data: data})
	if err != nil {
		return tool.FileVersion{}, err
	}
	if !resp.VersionValid {
		return tool.FileVersion{}, errors.New("microvm workspace omitted created file version")
	}
	return tool.NewFileVersion(resp.Version), nil
}

// ReplaceFile conditionally replaces a guest file using its opaque version.
func (w *Workspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	token, err := tool.EncodeFileVersion(old)
	resp, err := w.call(ctx, request{
		Operation: opReplace, Path: path, Data: data,
		Version: token, VersionValid: err == nil,
	})
	if err != nil {
		return tool.FileVersion{}, err
	}
	if !resp.VersionValid {
		return tool.FileVersion{}, errors.New("microvm workspace omitted replacement file version")
	}
	return tool.NewFileVersion(resp.Version), nil
}

// Glob returns bounded, deterministic guest-relative matches.
func (w *Workspace) Glob(ctx context.Context, pattern string) ([]string, error) {
	resp, err := w.call(ctx, request{Operation: opGlob, Pattern: pattern})
	return resp.Paths, err
}

// Grep returns bounded guest-relative regular-expression matches.
func (w *Workspace) Grep(ctx context.Context, pattern, pathGlob string) ([]tool.GrepMatch, error) {
	resp, err := w.call(ctx, request{Operation: opGrep, Pattern: pattern, PathGlob: pathGlob})
	return resp.Matches, err
}

// RecordRead records the exact supplied version without guest I/O.
func (w *Workspace) RecordRead(path string, version tool.FileVersion) {
	key := tool.LedgerKey(w.root, path)
	w.ledgerMu.Lock()
	w.ledger[key] = version
	w.ledgerMu.Unlock()
}

// RecordedVersion performs an I/O-free lookup in the host-side live ledger.
func (w *Workspace) RecordedVersion(path string) (tool.FileVersion, bool) {
	key := tool.LedgerKey(w.root, path)
	w.ledgerMu.Lock()
	version, ok := w.ledger[key]
	w.ledgerMu.Unlock()
	return version, ok
}

func (w *Workspace) call(ctx context.Context, req request) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, err
	}
	var resp response
	if err := w.client.Call(ctx, control.ServiceWorkspace, "call", req, &resp); err != nil {
		var remote *control.RemoteError
		if errors.As(err, &remote) {
			return response{}, remoteError(remote.Code, req.Path)
		}
		return response{}, fmt.Errorf("call microvm workspace service: %w", err)
	}
	if resp.ErrorCode != "" {
		return response{}, remoteError(resp.ErrorCode, req.Path)
	}
	return resp, nil
}

func remoteError(code, path string) error {
	switch code {
	case "version_mismatch":
		return &tool.VersionMismatchError{Path: path}
	case "exists":
		return &fs.PathError{Op: "create", Path: path, Err: fs.ErrExist}
	case "not_found":
		return &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	case "path_escape":
		return errPathEscape
	case "result_bound":
		return errResultBound
	case "binding_mismatch":
		return control.ErrBindingMismatch
	default:
		return errors.New("microvm workspace operation failed")
	}
}
