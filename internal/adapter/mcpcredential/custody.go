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
	"time"

	"github.com/godbus/dbus/v5"
	keyringapi "github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

// BackendKeyring stores generated keys in the OS keyring; BackendFile uses a protected file.
const (
	BackendKeyring = "keyring" // BackendKeyring stores the generated key in the OS keyring.
	BackendFile    = "file"    // BackendFile stores the generated key in a protected file.
	// NativeNamespace is the credential-store namespace reserved for native MCP
	// custody. It is deliberately distinct from the legacy key_env namespace.
	NativeNamespace = "mecatl-mcp-oauth-native/v1" // #nosec G101 -- public namespace discriminator, not a credential.
	keyringService  = "mecatl.mcp.oauth"
	keyringDomain   = "mecatl/mcp/oauth-key/v1\x00"
	locatorDomain   = "mecatl/mcp/credential-locator/v1\x00"
	markerName      = "mcp-credential-backend.json"
	lockName        = ".mcp-credential-key.lock"
	maxMarkerBytes  = 1024
	maxKeyBytes     = 128
)

// errInvalidMarker is the internal sentinel for a marker that fails shape
// validation; readMarker and readResetMarker both classify against it.
var errInvalidMarker = errors.New("invalid marker")

// Keyring reads and writes the OS credential entry used by MCP custody.
type Keyring interface {
	Get(string, string) (string, error)
	Set(string, string, string) error
	Delete(string, string) error
}

// Detector reports whether the platform keyring is available.
type Detector func(context.Context) (bool, error)

// ConfirmFile confirms the attended fallback to file custody.
type ConfirmFile func(context.Context) (bool, error)

// Selection is the selected custody backend and the key it opened or created.
type Selection struct {
	Backend, Locator string
	Key              []byte
}

// Options controls MCP credential custody selection.
type Options struct {
	Requested, FilePath, Platform string
	Detect                        Detector
	ConfirmFile                   ConfirmFile
	Attended                      bool
	Keyring                       Keyring
}

// Resolve selects and opens the root-pinned MCP credential backend.
func Resolve(ctx context.Context, root string, opts Options) (Selection, error) {
	prepareOptions(&opts)
	canonical, err := prepareRoot(root)
	if err != nil {
		return Selection{}, err
	}
	unlock, err := lockRoot(ctx, canonical)
	if err != nil {
		return Selection{}, err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	if err := recoverPublishedMarkerTemp(canonical); err != nil {
		return Selection{}, errors.New("MCP credential backend marker is invalid")
	}
	marker, err := readMarker(canonical)
	if err == nil {
		return resolveMarker(ctx, canonical, marker, opts, "", false)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Selection{}, invalidMarkerError(canonical)
	}

	// Do not hold the root lock while an attended confirmation waits for input.
	// Re-lock and re-check after selection so a concurrent initializer wins.
	unlock()
	unlock = nil
	backend, err := choose(ctx, opts)
	if err != nil {
		return Selection{}, err
	}
	unlock, err = lockRoot(ctx, canonical)
	if err != nil {
		return Selection{}, err
	}
	if err := recoverPublishedMarkerTemp(canonical); err != nil {
		return Selection{}, errors.New("MCP credential backend marker is invalid")
	}
	marker, err = readMarker(canonical)
	if err == nil {
		return resolveMarker(ctx, canonical, marker, opts, backend, true)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Selection{}, invalidMarkerError(canonical)
	}
	return createCredential(ctx, canonical, backend, opts)
}

// invalidMarkerError classifies a readMarker failure that is not a missing
// marker, distinguishing an obsolete prototype-format marker (which points
// the operator to `mcp reset-custody`) from any other invalid state.
func invalidMarkerError(root string) error {
	if isPrototypeMarker(root) {
		return errors.New("MCP credential marker uses an obsolete format; run mcp reset-custody")
	}
	return errors.New("MCP credential backend marker is invalid")
}

// pendingMarkerMismatchError reports a pending-marker identity mismatch and
// points the operator at the recovery path the mismatch itself requires: a
// moved root paired with a stale key artifact cannot be resolved by
// retrying, only by an explicit reset of the root and its artifact together.
func pendingMarkerMismatchError() error {
	return errors.New("MCP credential artifact identity does not match pending marker; run mcp reset-custody")
}

// resetInProgressError reports that a prior reset was interrupted. Resolve
// and Open both refuse a resetting marker rather than treating it as ready
// or attempting recovery themselves: only Reset resumes it.
func resetInProgressError() error {
	return errors.New("MCP credential custody reset is in progress; re-run mcp reset-custody")
}

func prepareOptions(opts *Options) {
	if opts.Requested == "" {
		opts.Requested = "auto"
	}
	if opts.Platform == "" {
		opts.Platform = runtime.GOOS
	}
	if opts.Keyring == nil {
		opts.Keyring = osKeyring{}
	}
}

func resolveMarker(ctx context.Context, root string, marker backendMarker, opts Options, expectedBackend string, checkContext bool) (Selection, error) {
	if marker.State == markerResetting {
		return Selection{}, resetInProgressError()
	}
	if expectedBackend != "" && marker.Backend != expectedBackend {
		return Selection{}, errors.New("MCP credential-store conflicts with the pinned backend")
	}
	if expectedBackend == "" && opts.Requested != "auto" && opts.Requested != marker.Backend {
		return Selection{}, errors.New("MCP credential-store conflicts with the pinned backend")
	}
	locator, err := pinnedLocator(root, marker.Backend, opts.FilePath)
	if err != nil || locatorDigest(marker.Backend, locator) != marker.LocatorSHA256 {
		return Selection{}, errors.New("MCP credential backend locator does not match the pinned root")
	}
	if marker.State == markerPending {
		return recoverPending(ctx, root, marker, opts, checkContext)
	}
	sel, err := openPinned(marker.Backend, locator, opts)
	if err != nil {
		return Selection{}, err
	}
	if keyDigest(sel.Key) != marker.InitSHA256 {
		clear(sel.Key)
		return Selection{}, errors.New("MCP credential artifact identity does not match marker")
	}
	return sel, nil
}

func recoverPending(ctx context.Context, root string, marker backendMarker, opts Options, checkContext bool) (Selection, error) {
	if checkContext {
		if err := ctx.Err(); err != nil {
			return Selection{}, err
		}
	}
	locator, err := pinnedLocator(root, marker.Backend, opts.FilePath)
	if err != nil {
		return Selection{}, err
	}
	key, err := readArtifact(marker.Backend, locator, opts)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Selection{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		// The process may have died after the durable pending marker and before
		// creating the artifact. Restart with a fresh in-memory key, still under
		// the root lock; never adopt an unrelated artifact.
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return Selection{}, errors.New("MCP key generation failed")
		}
		marker.InitSHA256 = keyDigest(key)
		if err := publishMarker(root, marker, true); err != nil {
			clear(key)
			return Selection{}, errors.New("MCP credential backend marker cannot be rewritten")
		}
		if err := createArtifact(marker.Backend, locator, opts, key); err != nil {
			clear(key)
			return Selection{}, err
		}
	} else if keyDigest(key) != marker.InitSHA256 {
		clear(key)
		return Selection{}, pendingMarkerMismatchError()
	}
	if checkContext {
		if err := ctx.Err(); err != nil {
			clear(key)
			return Selection{}, err
		}
	}
	ready := marker
	ready.State = markerReady
	if err := publishMarker(root, ready, true); err != nil {
		clear(key)
		return Selection{}, errors.New("MCP credential backend marker cannot be finalized")
	}
	return Selection{Backend: marker.Backend, Locator: locator, Key: key}, nil
}

func createCredential(ctx context.Context, root, backend string, opts Options) (Selection, error) {
	locator, err := pinnedLocator(root, backend, opts.FilePath)
	if err != nil {
		return Selection{}, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return Selection{}, errors.New("MCP key generation failed")
	}
	pending := backendMarker{Version: 1, State: markerPending, StoreNamespace: NativeNamespace, Backend: backend, LocatorSHA256: locatorDigest(backend, locator), InitSHA256: keyDigest(key)}
	// The marker is the durable intent. It is published before the secret
	// artifact so a crash cannot leave an adoptable, unmarked credential.
	if err := publishMarker(root, pending, false); err != nil {
		clear(key)
		return Selection{}, errors.New("MCP credential backend marker cannot be written")
	}
	if err := createArtifact(backend, locator, opts, key); err != nil {
		clear(key)
		return Selection{}, err
	}
	if err := ctx.Err(); err != nil {
		clear(key)
		return Selection{}, err
	}
	ready := pending
	ready.State = markerReady
	if err := publishMarker(root, ready, true); err != nil {
		// Keep the pending marker: the next run can recover the artifact.
		clear(key)
		return Selection{}, errors.New("MCP credential backend marker cannot be finalized")
	}
	return Selection{Backend: backend, Locator: locator, Key: key}, nil
}

// MarkerInspection is the bounded, non-secret result of inspecting custody metadata.
type MarkerInspection string

const (
	// MarkerMissing means the custody marker does not exist.
	MarkerMissing MarkerInspection = "missing"
	// MarkerUnavailable means the custody root or marker cannot be inspected.
	MarkerUnavailable MarkerInspection = "unavailable"
	// MarkerLocked means the custody root is locked.
	MarkerLocked MarkerInspection = "locked"
	// MarkerRecovery means the marker requires recovery.
	MarkerRecovery MarkerInspection = "recovery required"
	// MarkerPresent means the custody marker is valid.
	MarkerPresent MarkerInspection = "present"
)

// InspectMarker checks only the root-pinned custody marker. It never opens a
// keyring, reads a credential, or returns marker contents. The result is safe
// for status/list projections.
func InspectMarker(root string) MarkerInspection {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return MarkerMissing
	}
	canonical, err := prepareExistingRoot(root)
	if err != nil {
		return MarkerUnavailable
	}
	marker, err := readMarker(canonical)
	switch {
	case err == nil && (marker.State == markerPending || marker.State == markerResetting):
		return MarkerRecovery
	case err == nil:
		return MarkerPresent
	case errors.Is(err, os.ErrNotExist):
		return MarkerMissing
	default:
		// readMarker uses the same no-follow, owner-only read path as Resolve.
		// Do not distinguish marker corruption from inaccessible metadata here.
		return MarkerRecovery
	}
}

// Open opens an existing root-pinned MCP credential backend.
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
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	marker, err := readMarker(canonical)
	if err != nil {
		return Selection{}, invalidMarkerError(canonical)
	}
	if marker.State == markerResetting {
		return Selection{}, resetInProgressError()
	}
	if marker.Backend != backend || marker.StoreNamespace != NativeNamespace || marker.LocatorSHA256 == "" {
		return Selection{}, errors.New("MCP credential backend marker is invalid")
	}
	if marker.State == markerPending {
		return Selection{}, errors.New("MCP credential backend requires recovery")
	}
	locator, err := pinnedLocator(canonical, backend, filePath)
	if err != nil || locatorDigest(backend, locator) != marker.LocatorSHA256 {
		return Selection{}, errors.New("MCP credential backend locator does not match the pinned root")
	}
	sel, err := openPinned(backend, locator, Options{FilePath: filePath, Keyring: keyring})
	if err != nil {
		clear(sel.Key)
		return Selection{}, err
	}
	if keyDigest(sel.Key) != marker.InitSHA256 {
		clear(sel.Key)
		return Selection{}, errors.New("MCP credential artifact identity does not match marker")
	}
	return sel, nil
}

func lockRoot(ctx context.Context, root string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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

const (
	markerPending   = "pending"
	markerReady     = "ready"
	markerResetting = "resetting"
)

// backendMarker is the durable custody intent. InitSHA256 binds a
// pending/ready marker to its artifact; Retired names the namespace
// directory a resetting marker is retiring to. The two are mutually
// exclusive across states so one marker never carries both meanings.
type backendMarker struct {
	Version        int    `json:"version"`
	State          string `json:"state"`
	StoreNamespace string `json:"store_namespace"`
	Backend        string `json:"backend"`
	LocatorSHA256  string `json:"locator_sha256"`
	InitSHA256     string `json:"init_sha256,omitempty"`
	Retired        string `json:"retired,omitempty"`
}

func recoverPublishedMarkerTemp(root string) error {
	dir, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()

	markerFD, err := unix.Openat(dir, markerName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(markerFD) }()
	var markerStat unix.Stat_t
	if err := unix.Fstat(markerFD, &markerStat); err != nil {
		return err
	}
	if markerStat.Nlink == 1 {
		return nil
	}
	if !privateMarkerLinkStat(markerStat) {
		return errors.New("marker has unsafe link state")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	found := ""
	for _, entry := range entries {
		name := entry.Name()
		if !isMarkerTempName(name) {
			continue
		}
		fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		var tempStat unix.Stat_t
		statErr := unix.Fstat(fd, &tempStat)
		_ = unix.Close(fd)
		if statErr != nil {
			return statErr
		}
		if !privateMarkerLinkStat(tempStat) || tempStat.Dev != markerStat.Dev || tempStat.Ino != markerStat.Ino {
			return errors.New("marker temporary file is not its published sibling")
		}
		if found != "" {
			return errors.New("marker has multiple published temporary siblings")
		}
		found = name
	}
	if found == "" {
		return nil
	}
	if err := unix.Unlinkat(dir, found, 0); err != nil {
		return err
	}
	return unix.Fsync(dir)
}

func privateMarkerLinkStat(st unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Uid == uint32(os.Geteuid()) && st.Nlink == 2 && st.Mode&0o777 == 0o600 //nolint:gosec // euid is the OS-provided owner identity and is compared as a uid.
}

func isMarkerTempName(name string) bool {
	prefix := "." + markerName + "."
	suffix := ".tmp"
	if len(name) != len(prefix)+24+len(suffix) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	for _, b := range name[len(prefix) : len(name)-len(suffix)] {
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return false
		}
	}
	return true
}

func readMarker(root string) (backendMarker, error) {
	b, err := readPrivateFile(filepath.Join(root, markerName), maxMarkerBytes)
	if err != nil {
		return backendMarker{}, err
	}
	marker, err := decodeMarker(b)
	if err != nil {
		return backendMarker{}, errInvalidMarker
	}
	return marker, nil
}

func publishMarker(root string, marker backendMarker, replace bool) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	dir, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()
	for attempt := 0; attempt < 16; attempt++ {
		suffix := make([]byte, 12)
		if _, err := rand.Read(suffix); err != nil {
			return err
		}
		name := "." + markerName + "." + hex.EncodeToString(suffix) + ".tmp"
		fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return err
		}
		ok := false
		defer func() {
			if !ok {
				_ = unix.Unlinkat(dir, name, 0)
			}
		}()
		if err = writeAll(fd, data); err == nil {
			err = unix.Fsync(fd)
		}
		if closeErr := unix.Close(fd); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if replace {
			err = unix.Renameat(dir, name, dir, markerName)
		} else {
			err = unix.Linkat(dir, name, dir, markerName, 0)
		}
		if err != nil {
			return err
		}
		ok = true
		if !replace {
			if err = unix.Unlinkat(dir, name, 0); err != nil {
				return err
			}
		}
		return unix.Fsync(dir)
	}
	return errors.New("MCP credential marker temporary name is unavailable")
}

func locatorDigest(backend, locator string) string {
	h := sha256.Sum256(append([]byte(locatorDomain+NativeNamespace+"\x00"+backend+"\x00"), []byte(locator)...))
	return hex.EncodeToString(h[:])
}

func keyDigest(key []byte) string {
	digest := sha256.Sum256(append([]byte(locatorDomain+NativeNamespace+"\x00artifact\x00"), key...))
	return hex.EncodeToString(digest[:])
}

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
			o.Detect = detectSecretService
		}
		ok, err := o.Detect(ctx)
		if err != nil {
			return "", err
		}
		if ok {
			return BackendKeyring, nil
		}
		if !o.Attended || o.ConfirmFile == nil {
			return "", errors.New("MCP keyring is unavailable; choose --credential-store=file")
		}
		confirmed, err := o.ConfirmFile(ctx)
		if err != nil {
			return "", err
		}
		if !confirmed {
			return "", errors.New("MCP keyring declined; choose --credential-store=file")
		}
		return BackendFile, nil
	default:
		return "", errors.New("MCP credential-store must be auto, keyring, or file")
	}
}
func detectSecretService(ctx context.Context) (bool, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
	}
	bus, err := dbus.SessionBusPrivateNoAutoStartup(dbus.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	defer func() { _ = bus.Close() }()
	var owner string
	call := bus.Object("org.freedesktop.DBus", dbus.ObjectPath("/org/freedesktop/DBus")).CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, "org.freedesktop.secrets")
	if err := call.Store(&owner); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return owner != "", nil
}

func readArtifact(backend, locator string, o Options) ([]byte, error) {
	if backend == BackendKeyring {
		value, err := o.Keyring.Get(keyringService, locator)
		if errors.Is(err, keyringapi.ErrNotFound) {
			return nil, os.ErrNotExist
		}
		if err != nil {
			return nil, errors.New("MCP keyring is unavailable")
		}
		key, err := base64.RawStdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			clear(key)
			return nil, errors.New("MCP keyring entry is invalid")
		}
		return key, nil
	}
	b, err := readPrivateFile(locator, maxKeyBytes)
	if err != nil {
		return nil, err
	}
	return decodeKey(b)
}

func createArtifact(backend, locator string, o Options, key []byte) error {
	if backend == BackendKeyring {
		value, err := o.Keyring.Get(keyringService, locator)
		if err == nil {
			_ = value
			return errors.New("MCP credential artifact already exists without a pending marker")
		}
		if !errors.Is(err, keyringapi.ErrNotFound) {
			return errors.New("MCP keyring is unavailable")
		}
		if err := o.Keyring.Set(keyringService, locator, base64.RawStdEncoding.EncodeToString(key)); err != nil {
			return errors.New("MCP keyring is unavailable")
		}
		return nil
	}
	if err := secureMkdirAll(filepath.Dir(locator)); err != nil {
		return errors.New("MCP file credential-key directory cannot be created")
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(key))
	fd, err := unix.Open(locator, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("MCP credential artifact already exists without a pending marker")
		}
		return errors.New("MCP file credential key cannot be created")
	}
	if err = verifyPrivateFD(fd); err == nil {
		err = writeAll(fd, encoded)
	}
	if err == nil {
		err = unix.Fsync(fd)
	}
	if closeErr := unix.Close(fd); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("MCP file credential key cannot be written")
	}
	return nil
}

func openPinned(backend, locator string, o Options) (Selection, error) {
	if backend == BackendKeyring {
		value, err := o.Keyring.Get(keyringService, locator)
		if err != nil {
			return Selection{}, errors.New("MCP keyring is unavailable")
		}
		key, err := base64.RawStdEncoding.DecodeString(value)
		if err != nil || len(key) != 32 {
			clear(key)
			return Selection{}, errors.New("MCP keyring entry is invalid")
		}
		return Selection{Backend: backend, Locator: locator, Key: key}, nil
	}
	key, err := readFileKey(locator)
	return Selection{Backend: backend, Locator: locator, Key: key}, err
}

func pinnedLocator(root, backend, filePath string) (string, error) {
	if backend == BackendKeyring {
		return keyringAccount(root), nil
	}
	if backend == BackendFile {
		return fileLocator(root, filePath)
	}
	return "", errors.New("MCP credential backend is invalid")
}
func keyringAccount(root string) string {
	h := sha256.Sum256(append([]byte(keyringDomain), []byte(root)...))
	return hex.EncodeToString(h[:])
}
func fileLocator(_ string, path string) (string, error) {
	if path == "" {
		return "", errors.New("MCP file credential key path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil || filepath.Clean(abs) != abs {
		return "", errors.New("MCP file credential key path is invalid")
	}
	return canonicalPath(abs)
}

func readFileKey(path string) ([]byte, error) {
	b, err := readPrivateFile(path, maxKeyBytes)
	if err != nil {
		return nil, errors.New("MCP file credential key cannot be read")
	}
	return decodeKey(b)
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
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("file is too large")
	}
	return b, nil
}
func verifyPrivateFD(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0o777 != 0o600 { //nolint:gosec // euid is the OS-provided owner identity and is compared as a uid.
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
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&0o777 != 0o700 { //nolint:gosec // euid is the OS-provided owner identity and is compared as a uid.
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

// isPrototypeMarker reports whether root contains the exact pre-recovery
// marker shape. It reads metadata only and never opens custody artifacts.
func isPrototypeMarker(root string) bool {
	_, legacy, _, err := readResetMarker(root)
	return err == nil && legacy
}

// Reset retires the native credential namespace and wrapping-key artifact
// for root. It never re-initializes custody, and never touches the legacy
// namespace, operator settings, or a key_env profile sharing the root: the
// next Resolve creates fresh custody through the existing pending/ready
// protocol.
//
// Reset first rewrites the marker in place to a durable "resetting" intent
// that records the retired-directory name and the already-proven artifact
// locator. Every later step tolerates its own prior completion, so an
// interruption at any point is recovered by simply re-running Reset: it
// resumes from the resetting marker rather than inferring progress from how
// many retired directories exist on disk.
func Reset(ctx context.Context, root, backend, filePath string, keyring Keyring) error {
	if keyring == nil {
		keyring = osKeyring{}
	}
	canonical, err := prepareExistingRoot(root)
	if err != nil {
		return err
	}
	unlock, err := lockRoot(ctx, canonical)
	if err != nil {
		return err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()

	marker, legacy, raw, err := readResetMarker(canonical)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("MCP custody reset found no native custody to reset")
	}
	if err != nil {
		return errors.New("MCP custody reset requires an identifiable native marker")
	}
	if marker.Backend != backend {
		return errors.New("MCP custody reset backend does not match the selected profile")
	}
	locator, err := pinnedLocator(canonical, backend, filePath)
	if err != nil {
		return err
	}

	if marker.State == markerResetting {
		// Resuming: the digest was already proven when the intent was
		// written. Re-check it against the current pinned locator so a
		// profile edit between runs is still caught, rather than trusting a
		// resetting marker forever.
		if locatorDigest(backend, locator) != marker.LocatorSHA256 {
			return errors.New("MCP custody reset refused an unproven artifact")
		}
	} else {
		if legacy {
			if legacyLocatorDigest(locator) != marker.LocatorSHA256 {
				return errors.New("MCP custody reset refused an unproven artifact")
			}
		} else if locatorDigest(backend, locator) != marker.LocatorSHA256 {
			return errors.New("MCP custody reset refused an unproven artifact")
		}
		intent := backendMarker{
			Version:        1,
			State:          markerResetting,
			StoreNamespace: NativeNamespace,
			Backend:        backend,
			LocatorSHA256:  locatorDigest(backend, locator),
			Retired:        newRetiredNamespaceName(raw),
		}
		// The marker already exists on disk in every case Reset reaches this
		// point (current pending/ready or the exact legacy shape), so this
		// always replaces it, never a first-ever creation.
		if err := publishMarker(canonical, intent, true); err != nil {
			return errors.New("MCP custody reset could not record its intent")
		}
		marker = intent
	}

	// A prototype marker predates namespace separation, so there is usually
	// no native namespace directory here; retireNativeNamespace treats that
	// as already-retired and just records the retired directory. If one
	// exists anyway, this reset is discarding all native custody under the
	// root regardless, so folding it in is in scope, not a new risk.
	retired := filepath.Join(canonical, marker.Retired)
	if err := retireNativeNamespace(canonical, retired, NativeNamespace); err != nil {
		return err
	}
	if err := deleteArtifact(backend, locator, keyring); err != nil {
		return err
	}
	if err := removeMarker(canonical); err != nil {
		return errors.New("MCP custody reset could not retire its marker")
	}
	return nil
}

// readResetMarker reads the raw marker file and classifies it as a current
// marker (pending, ready, or resetting) or the exact legacy prototype shape.
func readResetMarker(root string) (backendMarker, bool, []byte, error) {
	raw, err := readPrivateFile(filepath.Join(root, markerName), maxMarkerBytes)
	if err != nil {
		return backendMarker{}, false, nil, err
	}
	if marker, err := decodeMarker(raw); err == nil {
		return marker, false, raw, nil
	}
	if marker, err := decodeLegacyMarker(raw); err == nil {
		return marker, true, raw, nil
	}
	return backendMarker{}, false, nil, errInvalidMarker
}

// decodeStrictJSON decodes raw into v, rejecting unknown fields and any
// trailing content.
func decodeStrictJSON(raw []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var extra any
	return dec.Decode(v) == nil && dec.Decode(&extra) == io.EOF
}

// isHexSHA256 reports whether s is exactly a lowercase- or uppercase-safe
// hex-encoded SHA-256 digest.
func isHexSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func decodeMarker(raw []byte) (backendMarker, error) {
	var marker backendMarker
	if !decodeStrictJSON(raw, &marker) {
		return backendMarker{}, errInvalidMarker
	}
	if marker.Version != 1 || marker.StoreNamespace != NativeNamespace || (marker.Backend != BackendKeyring && marker.Backend != BackendFile) || !isHexSHA256(marker.LocatorSHA256) {
		return backendMarker{}, errInvalidMarker
	}
	switch marker.State {
	case markerPending, markerReady:
		if marker.Retired != "" || !isHexSHA256(marker.InitSHA256) {
			return backendMarker{}, errInvalidMarker
		}
	case markerResetting:
		if marker.InitSHA256 != "" || !isValidRetiredName(marker.Retired) {
			return backendMarker{}, errInvalidMarker
		}
	default:
		return backendMarker{}, errInvalidMarker
	}
	return marker, nil
}

// legacyMarker is the exact three-field prototype marker shape from before
// namespace separation and pending/ready recovery existed.
type legacyMarker struct {
	Version int    `json:"version"`
	Backend string `json:"backend"`
	Locator string `json:"locator_sha256"`
}

func decodeLegacyMarker(raw []byte) (backendMarker, error) {
	var old legacyMarker
	if !decodeStrictJSON(raw, &old) {
		return backendMarker{}, errInvalidMarker
	}
	if old.Version != 1 || (old.Backend != BackendKeyring && old.Backend != BackendFile) || !isHexSHA256(old.Locator) {
		return backendMarker{}, errInvalidMarker
	}
	return backendMarker{Version: 1, Backend: old.Backend, LocatorSHA256: old.Locator}, nil
}

func legacyLocatorDigest(locator string) string {
	digest := sha256.Sum256([]byte(locator))
	return hex.EncodeToString(digest[:])
}

// retiredNamespacePrefix is the fixed, non-secret prefix every retired
// native namespace directory name starts with.
func retiredNamespacePrefix() string {
	return credentialstore.NamespacePhysicalName(NativeNamespace) + ".reset-"
}

// newRetiredNamespaceName derives the retired directory name from the
// marker bytes being retired, so concurrent or repeated resets of
// differently-shaped markers cannot collide.
func newRetiredNamespaceName(seed []byte) string {
	digest := sha256.Sum256(seed)
	return retiredNamespacePrefix() + hex.EncodeToString(digest[:8])
}

// isValidRetiredName reports whether name is exactly one retired-namespace
// directory name: the fixed prefix plus a 16-hex-digit suffix, one path
// component, so a marker can never point a mutating step outside the root.
func isValidRetiredName(name string) bool {
	suffix, ok := strings.CutPrefix(name, retiredNamespacePrefix())
	if !ok || len(suffix) != 16 {
		return false
	}
	for _, b := range []byte(suffix) {
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return false
		}
	}
	return true
}

// isPrivateDir reports whether info describes an owner-only, non-symlink
// directory. It is the single source of truth for the retired/native custody
// safety check; callers must not re-derive it.
func isPrivateDir(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() && info.Mode().Perm() == 0o700
}

// retireNativeNamespace renames the native namespace directory to retired.
// Both a fresh rename and a resumed reset are handled: a missing native
// directory with an existing retired one is treated as already done.
func retireNativeNamespace(root, retired, namespace string) error {
	native := filepath.Join(root, credentialstore.NamespacePhysicalName(namespace))
	nativeInfo, nativeErr := os.Lstat(native)
	retiredInfo, retiredErr := os.Lstat(retired)
	if err := validateRetiredNamespaceState(nativeInfo, nativeErr, retiredInfo, retiredErr); err != nil {
		return err
	}
	dir, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("MCP custody reset could not inspect the custody root")
	}
	defer func() { _ = unix.Close(dir) }()
	if nativeErr == nil {
		if err := unix.Renameat(dir, filepath.Base(native), dir, filepath.Base(retired)); err != nil {
			return errors.New("MCP custody reset could not retire native custody")
		}
		return unix.Fsync(dir)
	}
	return ensureRetiredNamespace(dir, filepath.Base(retired), retiredInfo)
}

func validateRetiredNamespaceState(nativeInfo os.FileInfo, nativeErr error, retiredInfo os.FileInfo, retiredErr error) error {
	if nativeErr == nil && retiredErr == nil {
		return errors.New("MCP custody reset found conflicting retired custody")
	}
	if nativeErr != nil && !errors.Is(nativeErr, os.ErrNotExist) {
		return errors.New("MCP custody reset could not inspect native custody")
	}
	if retiredErr != nil && !errors.Is(retiredErr, os.ErrNotExist) {
		return errors.New("MCP custody reset could not inspect retired custody")
	}
	if nativeErr == nil && !isPrivateDir(nativeInfo) {
		return errors.New("MCP custody reset found unsafe native custody")
	}
	if retiredErr == nil && !isPrivateDir(retiredInfo) {
		return errors.New("MCP custody reset found unsafe retired custody")
	}
	return nil
}

// ensureRetiredNamespace records retired as existing under the already-open,
// no-follow root directory fd, so the mutating step never re-resolves a bare
// path an attacker could have swapped after the caller's safety check.
func ensureRetiredNamespace(dir int, retired string, info os.FileInfo) error {
	if info != nil {
		return nil
	}
	if err := unix.Mkdirat(dir, retired, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return errors.New("MCP custody reset could not record retired custody")
	}
	return unix.Fsync(dir)
}

func deleteArtifact(backend, locator string, keyring Keyring) error {
	if backend == BackendKeyring {
		err := keyring.Delete(keyringService, locator)
		if err != nil && !errors.Is(err, keyringapi.ErrNotFound) {
			return errors.New("MCP custody reset could not remove the keyring artifact")
		}
		return nil
	}
	dir, err := unix.Open(filepath.Dir(locator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("MCP custody reset could not inspect the file artifact directory")
	}
	defer func() { _ = unix.Close(dir) }()
	if err := unlinkPrivateFile(dir, filepath.Base(locator)); err != nil {
		return errors.New("MCP custody reset could not remove the file artifact")
	}
	return nil
}

func removeMarker(root string) error {
	dir, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()
	return unlinkPrivateFile(dir, markerName)
}

// unlinkPrivateFile removes name from the already-open, no-follow directory
// dir after verifying it is a private (owner-only, single-link, regular)
// file, then fsyncs the directory. A missing file counts as already
// removed, so every caller is safe to repeat.
func unlinkPrivateFile(dir int, name string) error {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || verifyPrivateFD(fd) != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return errors.New("found an unsafe file")
	}
	_ = unix.Close(fd)
	if err := unix.Unlinkat(dir, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return errors.New("could not remove file")
	}
	return unix.Fsync(dir)
}

type osKeyring struct{}

func (osKeyring) Get(s, a string) (string, error) { return keyringapi.Get(s, a) }
func (osKeyring) Set(s, a, v string) error        { return keyringapi.Set(s, a, v) }
func (osKeyring) Delete(s, a string) error        { return keyringapi.Delete(s, a) }
