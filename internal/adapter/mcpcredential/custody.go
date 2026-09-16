// Package mcpcredential provides the small, root-pinned custody seam used by
// local MCP onboarding. It deliberately contains no OAuth or client policy.
package mcpcredential

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	keyringapi "github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"
)

const (
	BackendKeyring = "keyring"
	BackendFile    = "file"
	keyringService = "mecatl.mcp.oauth"
	keyringDomain  = "mecatl/mcp/oauth-key/v1\x00"
	markerName     = "mcp-credential-backend.json"
	lockName       = ".mcp-credential-key.lock"
	maxMarkerBytes = 1024
	maxKeyBytes    = 128
)

type Keyring interface {
	Get(string, string) (string, error)
	Set(string, string, string) error
}
type Detector func(context.Context) (bool, error)

type Selection struct {
	Backend, Locator string
	Key              []byte
}
type Options struct {
	Requested, FilePath, Platform string
	Detect                        Detector
	Keyring                       Keyring
}

func Resolve(ctx context.Context, root string, opts Options) (Selection, error) {
	if opts.Requested == "" {
		opts.Requested = "auto"
	}
	if opts.Platform == "" {
		opts.Platform = runtime.GOOS
	}
	if opts.Keyring == nil {
		opts.Keyring = osKeyring{}
	}
	canonical, err := prepareRoot(root)
	if err != nil {
		return Selection{}, err
	}
	unlock, err := lockRoot(ctx, canonical)
	if err != nil {
		return Selection{}, err
	}
	defer unlock()
	marker, err := readMarker(canonical)
	if err == nil {
		if opts.Requested != "auto" && opts.Requested != marker.Backend {
			return Selection{}, errors.New("MCP credential-store conflicts with the pinned backend")
		}
		return openPinned(canonical, marker.Backend, opts)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Selection{}, errors.New("MCP credential backend marker is invalid")
	}
	backend, err := choose(ctx, opts)
	if err != nil {
		return Selection{}, err
	}
	sel, err := openNew(canonical, backend, opts)
	if err != nil {
		return Selection{}, err
	}
	if err := writeMarker(canonical, backendMarker{Version: 1, Backend: backend, LocatorSHA256: digest(sel.Locator)}); err != nil {
		clear(sel.Key)
		return Selection{}, errors.New("MCP credential backend marker cannot be written")
	}
	return sel, nil
}

func Open(ctx context.Context, root, backend, filePath string, keyring Keyring) (Selection, error) {
	if keyring == nil {
		keyring = osKeyring{}
	}
	canonical, err := prepareExistingRoot(root)
	if err != nil {
		return Selection{}, err
	}
	unlock, err := lockRoot(ctx, canonical)
	if err != nil {
		return Selection{}, err
	}
	defer unlock()
	marker, err := readMarker(canonical)
	if err != nil || marker.Backend != backend || marker.LocatorSHA256 == "" {
		return Selection{}, errors.New("MCP credential backend marker is invalid")
	}
	sel, err := openPinned(canonical, backend, Options{FilePath: filePath, Keyring: keyring})
	if err != nil || digest(sel.Locator) != marker.LocatorSHA256 {
		clear(sel.Key)
		return Selection{}, errors.New("MCP credential backend locator does not match the pinned root")
	}
	return sel, nil
}

func lockRoot(ctx context.Context, root string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.New("MCP credential root is locked")
	}
	fd, err := unix.Open(filepath.Join(root, lockName), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil || verifyPrivateFD(fd) != nil || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return nil, errors.New("MCP credential root is locked")
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
}

type backendMarker struct {
	Version       int    `json:"version"`
	Backend       string `json:"backend"`
	LocatorSHA256 string `json:"locator_sha256"`
}

func readMarker(root string) (backendMarker, error) {
	var marker backendMarker
	b, err := readPrivateFile(filepath.Join(root, markerName), maxMarkerBytes)
	if err != nil {
		return marker, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&marker) != nil || dec.Decode(&extra) != io.EOF || marker.Version != 1 || (marker.Backend != BackendKeyring && marker.Backend != BackendFile) || len(marker.LocatorSHA256) != sha256.Size*2 {
		return marker, errors.New("invalid marker")
	}
	if _, err := hex.DecodeString(marker.LocatorSHA256); err != nil {
		return marker, errors.New("invalid marker")
	}
	return marker, nil
}

func writeMarker(root string, marker backendMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	dir, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	name := "." + markerName + ".new"
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(dir, name, 0)
	if err = writeAll(fd, data); err == nil {
		err = unix.Fsync(fd)
	}
	if closeErr := unix.Close(fd); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = unix.Linkat(dir, name, dir, markerName, 0); err != nil {
		return err
	}
	if err = unix.Unlinkat(dir, name, 0); err != nil {
		return err
	}
	return unix.Fsync(dir)
}

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func choose(ctx context.Context, o Options) (string, error) {
	switch o.Requested {
	case BackendFile:
		return BackendFile, nil
	case BackendKeyring:
		return BackendKeyring, nil
	case "auto":
		if o.Platform != "linux" {
			return BackendKeyring, nil
		}
		if o.Detect == nil {
			return "", errors.New("MCP keyring availability is unproven; choose --credential-store=file")
		}
		ok, err := o.Detect(ctx)
		if err != nil {
			return "", err
		}
		if ok {
			return BackendKeyring, nil
		}
		return BackendFile, nil
	default:
		return "", errors.New("MCP credential-store must be auto, keyring, or file")
	}
}
func openNew(root, backend string, o Options) (Selection, error) {
	if backend == BackendKeyring {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return Selection{}, errors.New("MCP key generation failed")
		}
		loc := keyringAccount(root)
		encoded := base64.RawStdEncoding.EncodeToString(key)
		if err := o.Keyring.Set(keyringService, loc, encoded); err != nil {
			clear(key)
			return Selection{}, errors.New("MCP keyring is unavailable")
		}
		return Selection{Backend: backend, Locator: loc, Key: key}, nil
	}
	path, err := fileLocator(root, o.FilePath)
	if err != nil {
		return Selection{}, err
	}
	key, err := readOrCreateFileKey(path)
	return Selection{Backend: backend, Locator: path, Key: key}, err
}
func openPinned(root, backend string, o Options) (Selection, error) {
	if backend == BackendKeyring {
		loc := keyringAccount(root)
		value, err := o.Keyring.Get(keyringService, loc)
		if err != nil {
			return Selection{}, errors.New("MCP keyring is unavailable")
		}
		key, err := base64.RawStdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			clear(key)
			return Selection{}, errors.New("MCP keyring entry is invalid")
		}
		return Selection{Backend: backend, Locator: loc, Key: key}, nil
	}
	path, err := fileLocator(root, o.FilePath)
	if err != nil {
		return Selection{}, err
	}
	key, err := readOrCreateFileKey(path)
	return Selection{Backend: backend, Locator: path, Key: key}, err
}
func keyringAccount(root string) string {
	h := sha256.Sum256(append([]byte(keyringDomain), []byte(root)...))
	return hex.EncodeToString(h[:])
}
func fileLocator(root, path string) (string, error) {
	if path == "" {
		return "", errors.New("MCP file credential key path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil || filepath.Clean(abs) != abs {
		return "", errors.New("MCP file credential key path is invalid")
	}
	return canonicalPath(abs)
}

func readOrCreateFileKey(path string) ([]byte, error) {
	b, err := readPrivateFile(path, maxKeyBytes)
	if err == nil {
		return decodeKey(b)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("MCP file credential key cannot be read")
	}
	if err := secureMkdirAll(filepath.Dir(path)); err != nil {
		return nil, errors.New("MCP file credential-key directory cannot be created")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, errors.New("MCP key generation failed")
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(key))
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if errors.Is(err, unix.EEXIST) {
		clear(key)
		b, readErr := readPrivateFile(path, maxKeyBytes)
		if readErr != nil {
			return nil, errors.New("MCP file credential key cannot be read")
		}
		return decodeKey(b)
	}
	if err != nil {
		clear(key)
		return nil, errors.New("MCP file credential key cannot be created")
	}
	if stErr := verifyPrivateFD(fd); stErr != nil {
		_ = unix.Close(fd)
		clear(key)
		return nil, errors.New("MCP file credential key cannot be created")
	}
	if err = writeAll(fd, encoded); err == nil {
		err = unix.Fsync(fd)
	}
	if closeErr := unix.Close(fd); err == nil {
		err = closeErr
	}
	if err != nil {
		clear(key)
		return nil, errors.New("MCP file credential key cannot be written")
	}
	return key, nil
}
func writeAll(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func decodeKey(b []byte) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(string(b))
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != string(b) {
		clear(key)
		return nil, errors.New("MCP file credential key is invalid")
	}
	return key, nil
}
func readPrivateFile(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := verifyPrivateFD(fd); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("file is too large")
	}
	return b, nil
}
func verifyPrivateFD(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || uint32(st.Uid) != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0o777 != 0o600 {
		return errors.New("not owner-only regular file")
	}
	return nil
}

func prepareRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("MCP credential root must be absolute")
	}
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("MCP credential root is unavailable")
	}
	root, err := canonicalPath(root)
	if err != nil {
		return "", errors.New("MCP credential root is unavailable")
	}
	if err := secureMkdirAll(root); err != nil {
		return "", errors.New("MCP credential root cannot be created")
	}
	return prepareExistingRoot(root)
}
func prepareExistingRoot(root string) (string, error) {
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("MCP credential root is unavailable")
	}
	root, err := canonicalPath(root)
	if err != nil {
		return "", errors.New("MCP credential root is unavailable")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", errors.New("MCP credential root is unavailable")
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || uint32(st.Uid) != uint32(os.Geteuid()) || st.Mode&0o777 != 0o700 {
		return "", errors.New("MCP credential root must be owner-only")
	}
	return filepath.Clean(root), nil
}

func canonicalPath(path string) (string, error) {
	path = filepath.Clean(path)
	var missing []string
	for probe := path; ; probe = filepath.Dir(probe) {
		info, err := os.Lstat(probe)
		if err == nil {
			if !info.IsDir() {
				if probe != path || info.Mode()&os.ModeSymlink != 0 {
					return "", errors.New("path ancestor is not a directory")
				}
				missing = append(missing, filepath.Base(probe))
				continue
			}
			base, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				base = filepath.Join(base, missing[i])
			}
			return base, nil
		}
		if !errors.Is(err, os.ErrNotExist) || probe == string(filepath.Separator) {
			return "", err
		}
		missing = append(missing, filepath.Base(probe))
	}
}

// secureMkdirAll creates and traverses each path component through a no-follow directory handle.
func secureMkdirAll(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		if err := unix.Mkdirat(fd, part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

type osKeyring struct{}

func (osKeyring) Get(s, a string) (string, error) { return keyringapi.Get(s, a) }
func (osKeyring) Set(s, a, v string) error        { return keyringapi.Set(s, a, v) }
