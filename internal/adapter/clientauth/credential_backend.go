//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package clientauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// CredentialStoreMode is the login-only backend selector.
type CredentialStoreMode string

// Supported login-only selectors.
const (
	CredentialStoreAuto    CredentialStoreMode = "auto"
	CredentialStoreKeyring CredentialStoreMode = "keyring"
	CredentialStoreFile    CredentialStoreMode = "file"
)

// CredentialBackend is the durable, closed backend vocabulary.
type CredentialBackend string

// Supported durable backends.
const (
	CredentialBackendKeyring CredentialBackend = "keyring"
	CredentialBackendFile    CredentialBackend = "file"
)

// CredentialStoreSelection reports the root's authoritative backend.
type CredentialStoreSelection struct {
	Backend     CredentialBackend
	NewlyPinned bool
}

const backendMarker = "clientauth-credential-backend.json"

var errBackendState = errors.New("clientauth: credential backend state is invalid or unsafe")

// ResolveCredentialStore selects and pins a backend before interactive OAuth.
func ResolveCredentialStore(ctx context.Context, root string, requested CredentialStoreMode) (CredentialStoreSelection, error) {
	return resolveCredentialStore(ctx, root, requested, true)
}

func resolveCredentialStore(ctx context.Context, root string, requested CredentialStoreMode, create bool) (CredentialStoreSelection, error) {
	if requested != CredentialStoreAuto && requested != CredentialStoreKeyring && requested != CredentialStoreFile {
		return CredentialStoreSelection{}, errors.New("clientauth: credential-store must be auto, keyring, or file")
	}
	if err := ctx.Err(); err != nil {
		return CredentialStoreSelection{}, err
	}
	root, err := privateBackendRoot(root, create)
	if err != nil {
		return CredentialStoreSelection{}, err
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return CredentialStoreSelection{}, errBackendState
	}
	defer func() { _ = handle.Close() }()
	// Share the existing root key lock, validating rather than repairing it.
	lockFile, err := handle.OpenFile(keyringLockName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		err = lockFile.Close()
	} else if errors.Is(err, os.ErrExist) {
		err = nil
	}
	if err != nil {
		return CredentialStoreSelection{}, errBackendState
	}
	if _, err := privateBackendFile(handle, keyringLockName); err != nil {
		return CredentialStoreSelection{}, err
	}
	lock := flock.New(filepath.Join(root, keyringLockName), flock.SetPermissions(0600))
	defer func() { _ = lock.Close() }()
	locked, err := lock.TryLockContext(ctx, keyringLockRetry)
	if err != nil || !locked {
		if ctx.Err() != nil {
			return CredentialStoreSelection{}, ctx.Err()
		}
		return CredentialStoreSelection{}, errBackendState
	}
	lockedInfo, err := lock.Stat()
	if err != nil {
		return CredentialStoreSelection{}, errBackendState
	}
	rootedInfo, err := privateBackendFile(handle, keyringLockName)
	if err != nil || !os.SameFile(lockedInfo, rootedInfo) {
		return CredentialStoreSelection{}, errBackendState
	}
	return resolveLockedCredentialStore(ctx, handle, root, requested, create)
}

func resolveLockedCredentialStore(ctx context.Context, handle *os.Root, root string, requested CredentialStoreMode, create bool) (CredentialStoreSelection, error) {
	data, err := readBackendFile(handle, backendMarker)
	if err == nil {
		var marker struct {
			Version int               `json:"version"`
			Backend CredentialBackend `json:"backend"`
		}
		if strictBackendJSON(data, &marker) != nil || marker.Version != 1 || !validBackend(marker.Backend) {
			return CredentialStoreSelection{}, errBackendState
		}
		if requested != CredentialStoreAuto && string(requested) != string(marker.Backend) {
			return CredentialStoreSelection{}, errors.New("clientauth: credential-store conflicts with the pinned backend")
		}
		return CredentialStoreSelection{Backend: marker.Backend}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return CredentialStoreSelection{}, err
	}
	legacy, err := legacyBackendEvidence(handle, root)
	if err != nil {
		return CredentialStoreSelection{}, err
	}
	if !create && !legacy {
		return CredentialStoreSelection{}, credentialstore.ErrNotFound
	}
	backend := CredentialBackendKeyring
	if !legacy {
		backend, err = chooseFreshCredentialBackend(ctx, requested, runtime.GOOS, detectLinuxSecretService)
		if err != nil {
			return CredentialStoreSelection{}, err
		}
	}
	if requested != CredentialStoreAuto && string(requested) != string(backend) {
		return CredentialStoreSelection{}, errors.New("clientauth: credential-store conflicts with legacy keyring enrollment")
	}
	if err := ctx.Err(); err != nil {
		return CredentialStoreSelection{}, err
	}
	data, _ = json.Marshal(struct {
		Version int               `json:"version"`
		Backend CredentialBackend `json:"backend"`
	}{1, backend})
	if err := writeBackendMarker(handle, root, data); err != nil {
		return CredentialStoreSelection{}, err
	}
	return CredentialStoreSelection{Backend: backend, NewlyPinned: true}, nil
}

func chooseFreshCredentialBackend(ctx context.Context, requested CredentialStoreMode, platform string, detect func(context.Context) (secretServiceState, error)) (CredentialBackend, error) {
	if requested == CredentialStoreFile {
		return CredentialBackendFile, nil
	}
	if requested == CredentialStoreAuto && platform == "linux" {
		state, err := detect(ctx)
		if err != nil {
			return "", err
		}
		switch state {
		case secretServiceAbsent:
			return CredentialBackendFile, nil
		case secretServicePresent:
			return CredentialBackendKeyring, nil
		default:
			return "", errSecretServiceDetection
		}
	}
	return CredentialBackendKeyring, nil
}

func validBackend(backend CredentialBackend) bool {
	return backend == CredentialBackendKeyring || backend == CredentialBackendFile
}

// OpenCredentialStore opens the selected creating store, never choosing a backend.
func OpenCredentialStore(ctx context.Context, root string, backend CredentialBackend) (credentialstore.Store, error) {
	if !validBackend(backend) {
		return nil, errBackendState
	}
	selection, err := resolveCredentialStore(ctx, root, CredentialStoreMode(backend), false)
	if err != nil {
		return nil, err
	}
	return openSelectedCredentialStore(ctx, root, selection.Backend, false)
}

// OpenExistingCredentialStore uses the existing pin, or pins valid legacy evidence.
func OpenExistingCredentialStore(ctx context.Context, root string) (credentialstore.Store, CredentialBackend, error) {
	selection, err := resolveCredentialStore(ctx, root, CredentialStoreAuto, false)
	if err != nil {
		return nil, "", err
	}
	store, err := openSelectedCredentialStore(ctx, root, selection.Backend, true)
	return store, selection.Backend, err
}

func openSelectedCredentialStore(ctx context.Context, root string, backend CredentialBackend, existing bool) (credentialstore.Store, error) {
	if backend == CredentialBackendFile {
		var store *credentialstore.PlainFileStore
		var err error
		if existing {
			store, err = credentialstore.OpenExistingPlainFile(root, credentialNamespace)
		} else {
			store, err = credentialstore.NewPlainFile(root, credentialNamespace)
		}
		if err != nil {
			return nil, err
		}
		return store, nil
	}
	var keys *KeyringProvider
	var err error
	if existing {
		keys, err = NewExistingKeyringProvider(root)
	} else {
		keys, err = NewKeyringProvider(root)
	}
	if err != nil {
		return nil, err
	}
	var store *credentialstore.EncryptedFileStore
	if existing {
		store, err = OpenExistingStore(ctx, root, keys)
	} else {
		store, err = OpenStore(ctx, root, keys)
	}
	if err != nil {
		return nil, err
	}
	return store, nil
}

func privateBackendRoot(root string, create bool) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errBackendState
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", errBackendState
		}
		info, err = os.Lstat(root)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", errBackendState, err)
	}
	if !privateBackendInfo(info, true) {
		return "", errBackendState
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errBackendState
	}
	return canonical, nil
}

func privateBackendInfo(info os.FileInfo, dir bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Geteuid()) {
		return false
	}
	if dir {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && reflect.ValueOf(stat.Nlink).Uint() == 1
}

func privateBackendFile(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !privateBackendInfo(info, false) {
		return nil, errBackendState
	}
	return info, nil
}

func readBackendFile(root *os.Root, name string) ([]byte, error) {
	info, err := privateBackendFile(root, name)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, errBackendState
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !privateBackendInfo(opened, false) || !os.SameFile(info, opened) {
		return nil, errBackendState
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errBackendState
	}
	return data, nil
}

func writeBackendMarker(root *os.Root, path string, data []byte) error {
	temp, err := os.CreateTemp(path, ".clientauth-backend-*")
	if err != nil {
		return errBackendState
	}
	name := filepath.Base(temp.Name())
	defer func() { _ = temp.Close(); _ = root.Remove(name) }()
	if _, err := temp.Write(data); err != nil {
		return errBackendState
	}
	if err := temp.Sync(); err != nil {
		return errBackendState
	}
	if err := temp.Close(); err != nil {
		return errBackendState
	}
	if err := root.Rename(name, backendMarker); err != nil {
		return errBackendState
	}
	dir, err := root.Open(".")
	if err != nil {
		return errBackendState
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return errBackendState
	}
	return nil
}

func strictBackendJSON(data []byte, out any) error {
	// Reject duplicate keys recursively, including those hidden in legacy rows.
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSONValue(d); err != nil {
		return errBackendState
	}
	if _, err := d.Token(); err != io.EOF {
		return errBackendState
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errBackendState
	}
	return nil
}

func uniqueJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errBackendState
			}
			seen[name] = true
		}
		if err := uniqueJSONValue(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func legacyBackendEvidence(root *os.Root, path string) (bool, error) {
	data, err := readBackendFile(root, "clientauth-connections.json")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errBackendState
	}
	var raw registryRawFile
	if strictBackendJSON(data, &raw) != nil || raw.Version != 1 || raw.Connections == nil {
		return false, errBackendState
	}
	registry := &Registry{path: filepath.Join(path, "clientauth-connections.json"), root: path, existingOnly: true}
	rows, err := registry.readRows()
	if err != nil {
		return false, errBackendState
	}
	for _, row := range rows {
		if !row.valid {
			return false, errBackendState
		}
		// Every alias must reach exactly this row; ambiguous evidence is not enrollment.
		found, err := registry.Find(row.connection.Identity.Target)
		if err != nil || !found.Identity.Equal(row.connection.Identity) {
			return false, errBackendState
		}
		if row.connection.ResourceURL != "" {
			found, err = registry.Find(row.connection.ResourceURL)
			if err != nil || !found.Identity.Equal(row.connection.Identity) {
				return false, errBackendState
			}
		}
	}
	return len(rows) > 0, nil
}
