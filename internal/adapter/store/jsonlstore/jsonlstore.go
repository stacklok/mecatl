// Package jsonlstore implements a versioned current-snapshot port.SessionStore,
// plus append-only port.ToolCallRecorder and port.EventLog sidecars. Save atomically
// replaces one bounded v2 current snapshot; Load also accepts historical v1 JSONL
// snapshots and a successful save lazily promotes that session. ToolCall and Append
// retain their cumulative JSONL audit semantics.
//
// SESSION-FAMILY NAMING. Each session's files share a family stem under
// the owner-only `sid-v1` subdirectory. The reversible token is `sid-v1-` plus
// Raw URL-base64 of the complete opaque valid-UTF-8 session id:
//
//	<dir>/sid-v1/sid-v1-<token>.session.json       (v2 current)
//	<dir>/sid-v1/sid-v1-<token>.session.jsonl      (readable v1 history)
//	<dir>/sid-v1/sid-v1-<token>.tools.jsonl
//	<dir>/sid-v1/sid-v1-<token>.events.jsonl
//
// A pre-rewrite family may still use the lossy legacySafeName stem. The
// sessionResolver in resolve.go is the single authority for canonical/legacy
// paths, ownership checks, and write-time migration. Reads never migrate.
//
// The .events.jsonl log is PARALLEL to (not a superset of) .tools.jsonl: the
// tool log is the structured per-tool AUDIT seam (args, queue/exec timing), the
// event log is the relayed STREAM (reasoning, ask/verdict pairs, delegation
// lifecycle) the server otherwise discards. Neither subsumes the other.
//
// Physical names are confined to filename-safe tokens under the store dir.
package jsonlstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ErrNotFound is returned by Load when no snapshot file exists for the id. It wraps
// port.ErrSessionNotFound so a consumer that may not import this adapter can
// distinguish not-found from an infra failure via errors.Is.
var ErrNotFound = fmt.Errorf("jsonlstore: session not found: %w", port.ErrSessionNotFound)

// SnapshotDurabilityCapability reports which crash-durability primitives the
// current jsonlstore filesystem supports. Atomic replacement without all sync
// steps prevents torn snapshots but does not claim survival across a host crash.
type SnapshotDurabilityCapability struct {
	AtomicReplace bool
	FileSync      bool
	DirectorySync bool
}

// HostCrashSafe reports whether Save can issue all primitives required to make both
// snapshot contents and the replacement directory entry durable before returning nil.
// The underlying filesystem and storage stack must honor successful sync/rename calls.
func (c SnapshotDurabilityCapability) HostCrashSafe() bool {
	return c.AtomicReplace && c.FileSync && c.DirectorySync
}

type snapshotOps struct {
	createTemp   func(string, string) (*os.File, error)
	write        func(*os.File, []byte) (int, error)
	closeFile    func(*os.File) error
	syncFile     func(*os.File) error
	rename       func(string, string) error
	moveRoot     func(*os.Root, string, string) error
	remove       func(string) error
	openDir      func(string) (*os.File, error)
	syncDir      func(*os.File) error
	atomicRename bool
	fileSync     bool
}

type durableDirectorySet struct {
	ops  snapshotOps
	dirs map[string]*os.File
}

func (st *Store) openDurableDirectories(paths ...string) (*durableDirectorySet, error) {
	if !st.durability.DirectorySync {
		return nil, errors.New("jsonlstore: destructive operation requires directory sync")
	}
	set := &durableDirectorySet{ops: st.snapshot, dirs: make(map[string]*os.File, len(paths))}
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, ok := set.dirs[path]; ok {
			continue
		}
		dir, err := st.snapshot.openDir(path) //nolint:gosec // owner-only store directory
		if err != nil {
			set.close()
			return nil, fmt.Errorf("jsonlstore: open directory for durable mutation: %w", err)
		}
		set.dirs[path] = dir
	}
	return set, nil
}

func (s *durableDirectorySet) sync() error {
	paths := make([]string, 0, len(s.dirs))
	for path := range s.dirs {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var syncErr error
	for _, path := range paths {
		if err := s.ops.syncDir(s.dirs[path]); err != nil {
			syncErr = errors.Join(syncErr, fmt.Errorf("jsonlstore: sync directory %q: %w", path, err))
		}
	}
	return syncErr
}

func (s *durableDirectorySet) close() {
	for _, dir := range s.dirs {
		_ = dir.Close()
	}
}

func defaultSnapshotOps() snapshotOps {
	return snapshotOps{
		createTemp: os.CreateTemp,
		write:      (*os.File).Write,
		closeFile:  (*os.File).Close,
		syncFile:   (*os.File).Sync,
		rename:     os.Rename,
		moveRoot:   (*os.Root).Rename,
		remove:     os.Remove,
		openDir:    os.Open,
		syncDir:    (*os.File).Sync,
		// Go's os.Rename is an atomic replacement when source and destination
		// are on the same supported filesystem; CreateTemp below guarantees that.
		atomicRename: true,
		fileSync:     true,
	}
}

// Store is a current-snapshot SessionStore with append-only tool/event sidecars.
type Store struct {
	// resolver is the single authority for canonical and legacy family paths
	// (including the plain root dir, resolver.dir — Store has no separate
	// copy of it).
	resolver sessionResolver
	// mu is confined to the sibling schedule store. Session-family mutations
	// coordinate by their stable cross-process flock identity instead.
	mu                        sync.Mutex
	inventoryMu               sync.Mutex
	snapshot                  snapshotOps
	durability                SnapshotDurabilityCapability
	tempOwner                 string
	tempGeneration            atomic.Uint64
	toolCallLockTimeout       time.Duration
	inventoryWorkObserver     func(inventoryWorkKind)
	snapshotFamilyLockBlocked func()
}

// compile-time assertions that Store satisfies both ports plus the optional
// retention seam and the durable event log.
var (
	_ port.SessionStore     = (*Store)(nil)
	_ port.SessionCreator   = (*Store)(nil)
	_ port.ToolCallRecorder = (*Store)(nil)
	_ port.PrunableStore    = (*Store)(nil)
	_ port.EventLog         = (*Store)(nil)
)

// New constructs a Store writing under dir, creating dir if needed. The dir is
// created at mode 0700: the store holds raw conversation transcripts (session
// snapshots, tool-call args/results, and the relayed event stream) in plaintext,
// so it is owner-only by construction.
func New(dir string) (*Store, error) {
	return newStoreWithSnapshotOps(dir, defaultSnapshotOps())
}

// normalizeStoreRoot makes dir absolute and lexically clean. It deliberately
// does NOT resolve symlinks: the store root is the path the operator configured,
// so a deployment that points the store at a symlinked volume keeps seeing that
// path in its own paths, locks and diagnostics. Every path the resolver derives
// is rooted here, so swapping in filepath.EvalSymlinks would silently rewrite
// them all (on macOS, where /var is a symlink to /private/var, for every store).
func normalizeStoreRoot(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("jsonlstore: store dir is empty")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("jsonlstore: normalize dir: %w", err)
	}
	return root, nil
}

// createDurableDirectoryHierarchy publishes every adapter-created directory before
// its children are used. An unsupported directory sync is reflected later by the
// durability capability probe; any other sync failure makes construction fail.
func createDurableDirectoryHierarchy(path string, mode os.FileMode, ops snapshotOps) error {
	var missing []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("jsonlstore: path %q is not a directory", current)
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("jsonlstore: no existing parent for %q", path)
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		created := missing[i]
		if err := os.Mkdir(created, mode); err != nil {
			if !os.IsExist(err) {
				return err
			}
			info, statErr := os.Stat(created)
			if statErr != nil || !info.IsDir() {
				return errors.Join(err, statErr)
			}
		}
		for _, dirPath := range []string{created, filepath.Dir(created)} {
			dir, err := ops.openDir(dirPath)
			if err != nil {
				return err
			}
			syncErr := ops.syncDir(dir)
			closeErr := dir.Close()
			if syncErr != nil && !syncUnsupported(syncErr) {
				return syncErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return nil
}

func newStoreWithSnapshotOps(dir string, ops snapshotOps) (*Store, error) {
	root, err := normalizeStoreRoot(dir)
	if err != nil {
		return nil, err
	}
	if err := createDurableDirectoryHierarchy(root, 0o700, ops); err != nil {
		return nil, fmt.Errorf("jsonlstore: create store dir: %w", err)
	}
	resolver := sessionResolver{dir: root}
	if err := createDurableDirectoryHierarchy(resolver.canonicalDir(), 0o700, ops); err != nil {
		return nil, fmt.Errorf("jsonlstore: create canonical dir: %w", err)
	}
	if err := createDurableDirectoryHierarchy(filepath.Join(resolver.canonicalDir(), inventoryCatalogDirName), 0o700, ops); err != nil {
		return nil, fmt.Errorf("jsonlstore: create inventory catalog dir: %w", err)
	}
	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return nil, fmt.Errorf("jsonlstore: create snapshot temp owner: %w", err)
	}
	atomicReplace, err := atomicReplaceSupported(resolver.canonicalDir(), ops)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: probe atomic replacement: %w", err)
	}
	fileSync, err := fileSyncSupported(resolver.canonicalDir(), ops)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: probe file sync: %w", err)
	}
	directorySync, err := directorySyncSupported(resolver.canonicalDir(), ops)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: probe directory sync: %w", err)
	}
	durability := SnapshotDurabilityCapability{
		AtomicReplace: atomicReplace,
		FileSync:      fileSync,
		DirectorySync: directorySync,
	}
	st := &Store{
		resolver: resolver, snapshot: ops, durability: durability,
		tempOwner: hex.EncodeToString(ownerBytes), toolCallLockTimeout: toolCallFamilyLockTimeout,
	}
	if err := st.reapSnapshotTempsAtStartup(); err != nil {
		return nil, err
	}
	return st, nil
}

func atomicReplaceSupported(path string, ops snapshotOps) (bool, error) {
	if !ops.atomicRename {
		return false, nil
	}
	source, err := ops.createTemp(path, ".snapshot-rename-probe-*")
	if err != nil {
		return probeResult(err)
	}
	sourcePath := source.Name()
	_ = source.Close()
	defer func() { _ = os.Remove(sourcePath) }()

	target, err := ops.createTemp(path, ".snapshot-rename-target-*")
	if err != nil {
		return probeResult(err)
	}
	targetPath := target.Name()
	_ = target.Close()
	defer func() { _ = os.Remove(targetPath) }()

	return probeResult(ops.rename(sourcePath, targetPath))
}

func fileSyncSupported(path string, ops snapshotOps) (bool, error) {
	if !ops.fileSync {
		return false, nil
	}
	probe, err := ops.createTemp(path, ".snapshot-sync-probe-*")
	if err != nil {
		return probeResult(err)
	}
	probePath := probe.Name()
	defer func() {
		_ = probe.Close()
		_ = os.Remove(probePath)
	}()
	return probeResult(ops.syncFile(probe))
}

func directorySyncSupported(path string, ops snapshotOps) (bool, error) {
	dir, err := ops.openDir(path)
	if err != nil {
		return probeResult(err)
	}
	defer func() { _ = dir.Close() }()
	return probeResult(ops.syncDir(dir))
}

func probeResult(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if syncUnsupported(err) {
		return false, nil
	}
	return false, err
}

func syncUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EINVAL)
}

// SnapshotDurability reports the filesystem primitives this Store verified at
// construction. A false field is an explicit weaker guarantee, not a Save error.
func (st *Store) SnapshotDurability() SnapshotDurabilityCapability {
	return st.durability
}

const snapshotTempMarker = ".tmp-v1-"

func snapshotTempPattern(snapshotPath, owner string, generation uint64) string {
	return filepath.Base(snapshotPath) + snapshotTempMarker + owner + "-" + fmt.Sprintf("%016x", generation) + "-*"
}

func snapshotFamilyLockPath(snapshotPath string) string {
	return strings.TrimSuffix(snapshotPath, currentSnapshotSuffix) + ".family.lock"
}

func snapshotPathFromTempName(dir, name string) (string, bool) {
	marker := strings.LastIndex(name, snapshotTempMarker)
	if marker < 0 {
		return "", false
	}
	base := name[:marker]
	if !strings.HasPrefix(base, sessionTokenPrefix) || !strings.HasSuffix(base, currentSnapshotSuffix) {
		return "", false
	}
	parts := strings.Split(name[marker+len(snapshotTempMarker):], "-")
	if len(parts) != 3 || len(parts[0]) != 32 || len(parts[1]) != 16 || parts[2] == "" {
		return "", false
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return "", false
	}
	if _, err := strconv.ParseUint(parts[1], 16, 64); err != nil {
		return "", false
	}
	return filepath.Join(dir, base), true
}

const toolCallFamilyLockTimeout = 5 * time.Second

func withSnapshotFamilyLock(ctx context.Context, snapshotPath string, blocked func(), fn func() error) error {
	fl := flock.New(snapshotFamilyLockPath(snapshotPath), flock.SetPermissions(0o600))
	locked, err := fl.TryLock()
	if err == nil && !locked {
		if blocked != nil {
			blocked()
		}
		locked, err = fl.TryLockContext(ctx, 10*time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("jsonlstore: acquire snapshot family lock %q: %w", fl.Path(), err)
	}
	if !locked {
		return fmt.Errorf("jsonlstore: acquire snapshot family lock %q: lock not acquired", fl.Path())
	}
	defer func() { _ = fl.Close() }()
	return fn()
}

func (st *Store) withSnapshotFamilyLock(ctx context.Context, snapshotPath string, fn func() error) error {
	return withSnapshotFamilyLock(ctx, snapshotPath, st.snapshotFamilyLockBlocked, fn)
}

func reapSnapshotTemps(snapshotPath string) error {
	entries, err := os.ReadDir(filepath.Dir(snapshotPath))
	if err != nil {
		return fmt.Errorf("jsonlstore: scan snapshot temporaries: %w", err)
	}
	for _, entry := range entries {
		ownedPath, ok := snapshotPathFromTempName(filepath.Dir(snapshotPath), entry.Name())
		if !ok || ownedPath != snapshotPath {
			continue
		}
		if err := os.Remove(filepath.Join(filepath.Dir(snapshotPath), entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("jsonlstore: reap snapshot temporary %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func (st *Store) reapSnapshotTempsAtStartup() error {
	entries, err := os.ReadDir(st.resolver.canonicalDir())
	if err != nil {
		return fmt.Errorf("jsonlstore: scan snapshot directory at startup: %w", err)
	}
	families := make(map[string]struct{})
	for _, entry := range entries {
		if path, ok := snapshotPathFromTempName(st.resolver.canonicalDir(), entry.Name()); ok {
			families[path] = struct{}{}
		}
	}
	for path := range families {
		fl := flock.New(snapshotFamilyLockPath(path), flock.SetPermissions(0o600))
		locked, lockErr := fl.TryLock()
		if lockErr != nil {
			_ = fl.Close()
			return fmt.Errorf("jsonlstore: inspect snapshot family lock %q: %w", fl.Path(), lockErr)
		}
		if !locked {
			_ = fl.Close()
			continue
		}
		if err := reapSnapshotTemps(path); err != nil {
			_ = fl.Close()
			return err
		}
		_ = fl.Close()
	}
	return nil
}

// Save atomically replaces the adapter-private v2 current snapshot. Existing
// v1 JSONL snapshots remain readable and are promoted lazily on the next save;
// their file modification time becomes the v2 logical modification time.
func (st *Store) Save(ctx context.Context, s *session.Session) error {
	if s == nil {
		return sessnap.ErrNilSession
	}
	if err := validateSessionID(s.ID); err != nil {
		return err
	}
	payload, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	path := st.resolver.currentSnapshotPath(s.ID)
	return st.withSnapshotFamilyLock(ctx, path, func() error {
		if err := st.advanceInventoryGeneration(); err != nil {
			return err
		}
		if err := reapSnapshotTemps(path); err != nil {
			return err
		}
		if err := st.prepareWrite(s.ID); err != nil {
			return err
		}
		modifiedAt, err := st.resolver.snapshotModifiedAt(s.ID, time.Now())
		if err != nil {
			return fmt.Errorf("jsonlstore: resolve snapshot modification time: %w", err)
		}
		data, err := json.Marshal(currentSnapshot{
			Format: currentSnapshotFormat, ModifiedAt: modifiedAt, Metadata: metaSnapshotFromSession(s), Snapshot: payload,
		})
		if err != nil {
			return fmt.Errorf("jsonlstore: marshal current snapshot: %w", err)
		}
		generation := st.tempGeneration.Add(1)
		pattern := snapshotTempPattern(path, st.tempOwner, generation)
		return replaceCurrentSnapshot(path, data, modifiedAt, pattern, st.snapshot, st.durability, nil)
	})
}

// Create atomically publishes the adapter-private v2 snapshot only when no
// authoritative current, canonical-v1, or matching legacy snapshot exists.
// Every supported writer takes the same stable family lock, so the presence
// check and final rename form one cross-process create-once operation.
func (st *Store) Create(ctx context.Context, s *session.Session) error {
	if s == nil {
		return sessnap.ErrNilSession
	}
	if err := validateSessionID(s.ID); err != nil {
		return err
	}
	payload, err := sessnap.Marshal(s)
	if err != nil {
		return err
	}
	path := st.resolver.currentSnapshotPath(s.ID)
	return st.withSnapshotFamilyLock(ctx, path, func() error {
		present, err := st.resolver.canonicalOwnership(s.ID)
		if err != nil {
			return err
		}
		if !present {
			_, present, err = st.resolver.legacySnapshot(s.ID)
			if err != nil {
				return err
			}
		}
		if present {
			return fmt.Errorf("jsonlstore: create %q: %w", s.ID, port.ErrSessionAlreadyExists)
		}

		modifiedAt := time.Now().UTC()
		data, err := json.Marshal(currentSnapshot{
			Format: currentSnapshotFormat, ModifiedAt: modifiedAt, Metadata: metaSnapshotFromSession(s), Snapshot: payload,
		})
		if err != nil {
			return fmt.Errorf("jsonlstore: marshal current snapshot: %w", err)
		}
		if err := st.advanceInventoryGeneration(); err != nil {
			return err
		}
		generation := st.tempGeneration.Add(1)
		pattern := snapshotTempPattern(path, st.tempOwner, generation)
		return replaceCurrentSnapshot(path, data, modifiedAt, pattern, st.snapshot, st.durability, nil)
	})
}

// replaceCurrentSnapshot durably replaces the file at path with data via
// create-temp-in-same-dir -> write -> optional stamp/fsync -> rename ->
// optional directory fsync. tempPattern is the os.CreateTemp glob pattern
// (callers own its naming scheme — e.g. snapshotTempPattern's owner/generation
// stem, or a caller-private literal pattern); a zero modifiedAt skips the
// mtime stamp. root, when non-nil, confines the rename/cleanup step to that
// already-open directory root instead of the raw path (defense-in-depth for
// a caller whose target directory warrants it); it is opened and closed by
// the caller, never here.
func replaceCurrentSnapshot(
	path string,
	data []byte,
	modifiedAt time.Time,
	tempPattern string,
	ops snapshotOps,
	durability SnapshotDurabilityCapability,
	root *os.Root,
) (retErr error) {
	tmp, err := ops.createTemp(filepath.Dir(path), tempPattern) //nolint:gosec // owner-only store dir
	if err != nil {
		return fmt.Errorf("jsonlstore: create snapshot temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			if root != nil {
				_ = root.Remove(filepath.Base(tmpPath))
			} else {
				_ = os.Remove(tmpPath)
			}
		}
	}()
	written, err := ops.write(tmp, data)
	if err != nil {
		return fmt.Errorf("jsonlstore: write snapshot temp: %w", err)
	}
	if written != len(data) {
		return fmt.Errorf("jsonlstore: write snapshot temp: wrote %d of %d bytes: %w", written, len(data), io.ErrShortWrite)
	}
	if !modifiedAt.IsZero() {
		if err := os.Chtimes(tmpPath, modifiedAt, modifiedAt); err != nil {
			return fmt.Errorf("jsonlstore: stamp snapshot temp: %w", err)
		}
	}
	if durability.FileSync {
		if err := ops.syncFile(tmp); err != nil {
			return fmt.Errorf("jsonlstore: sync snapshot temp: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("jsonlstore: close snapshot temp: %w", err)
	}
	if root != nil {
		if err := root.Rename(filepath.Base(tmpPath), filepath.Base(path)); err != nil {
			return fmt.Errorf("jsonlstore: replace current snapshot: %w", err)
		}
	} else if err := ops.rename(tmpPath, path); err != nil {
		return fmt.Errorf("jsonlstore: replace current snapshot: %w", err)
	}
	if !durability.DirectorySync {
		return nil
	}
	dir, err := ops.openDir(filepath.Dir(path)) //nolint:gosec // owner-only store dir
	if err != nil {
		return fmt.Errorf("jsonlstore: open snapshot directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := ops.syncDir(dir); err != nil {
		return fmt.Errorf("jsonlstore: sync snapshot directory: %w", err)
	}
	return nil
}

// Load reads the authoritative snapshot without modifying storage. Canonical
// presence prevents fallback; legacy is accepted only when its latest embedded
// id exactly matches the requested id.
func (st *Store) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	var line []byte
	err := st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
		var err error
		line, err = st.resolver.loadSnapshot(id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return sessnap.Unmarshal(line)
}

// maxScannerTokenSize is also the reverse reader's latest-record ceiling.
const maxScannerTokenSize = 16 * 1024 * 1024

// newScanner builds a bufio.Scanner over f with the generous buffer a snapshot
// or event-log line can need.
func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxScannerTokenSize)
	return sc
}

// sessionFileSuffix / toolsFileSuffix are the per-session file suffixes under
// dir (see the package doc layout).
const (
	sessionFileSuffix = ".session.jsonl"
	toolsFileSuffix   = ".tools.jsonl"
	eventsFileSuffix  = ".events.jsonl"
)

// EventLogFormat is the per-record format tag written on every event-log line.
// It versions the on-disk encoding so the language-neutral driver wire (3c) and
// any future format change can be distinguished; Read rejects an unknown tag as
// an infra error (a forward-incompatible log must fail loud, not silently skip).
//
// It is EXPORTED so the gRPC driver wire (`internal/adapter/grpcdriver.EventLogFormat`)
// can be pinned EQUAL to it by a test: the wire payload is exactly this record's
// "ev" bytes (json.Marshal of a session.Event), so the two tags MUST agree or a
// log written by one path is unreadable by the other (the one-codec claim). The
// unexported alias keeps the in-file call sites terse.
const EventLogFormat = "eventlog-json/1"

// eventLogFormat is the in-file alias of EventLogFormat (keeps the existing call
// sites terse; the two are the one constant).
const eventLogFormat = EventLogFormat

// eventLogRecord is one .events.jsonl line: a format tag plus the verbatim
// session.Event JSON. The event is stored as already-redacted JSON (the relay is
// the redaction boundary); the tag lets Read validate the encoding version.
type eventLogRecord struct {
	V  string          `json:"v"`
	Ev json.RawMessage `json:"ev"`
}

// List returns one row per logical session id. IDs come from latest snapshots,
// never filenames; canonical files win when canonical and legacy coexist.
//
// COST: ids are decoded from each session file's latest snapshot line rather
// than from filenames (a filename is not invertible back to the id, and now
// there are two directories — canonical and legacy — to reconcile), so List
// costs one directory read per dir plus one reverse TAIL read per snapshot
// file. The reader grows its EOF window only to the latest record, never scanning
// older snapshot history. Fine for a retention sweep on a startup/hourly cadence;
// indexed inventory is a separate concern.
//
// List enumerates v2 current snapshots and historical *.session.jsonl files;
// sidecars are never inventory authority. That keeps sidecars-before-snapshot
// removal load-bearing (see familyOrder in resolve.go): a sidecar without any
// session snapshot is invisible here and can never be swept.
func (st *Store) List(_ context.Context) ([]port.StoredSession, error) {
	files, err := st.resolver.snapshotFiles()
	if err != nil {
		return nil, err
	}
	out := make([]port.StoredSession, 0, len(files))
	for _, file := range files {
		out = append(out, port.StoredSession{ID: file.id, ModifiedAt: file.modified})
	}
	return out, nil
}

// Delete removes canonical sidecars before the canonical snapshot, and a
// legacy family in the same order — REMOVAL ORDER is load-bearing, see
// familyOrder's doc comment (resolve.go): the snapshot file is what List
// enumerates, so removing it last means a partial failure leaves the family
// still VISIBLE (the next retention sweep retries it), where the reverse
// order would leave an invisible orphaned sidecar no sweep could ever find.
// With two families (canonical + legacy) this now has to hold TWICE per
// call: canonical sidecars before the canonical snapshot, AND — only when
// the legacy snapshot's embedded id proves it belongs to this session —
// legacy sidecars before the legacy snapshot. A legacy family that fails
// ownership (mismatch or absent) is left untouched; that mismatch and
// absence are both idempotent success.
//
// The canonical family is removed on PRESENCE alone, never on the snapshot
// parsing: the token is injective, so the file is ours whatever it contains,
// and gating removal on validity made a torn snapshot line permanently
// unprunable — every retention sweep re-failed on it while List, which skips
// undecodable files, never surfaced it. port.PrunableStore requires that a
// Delete either remove or be idempotent success, so an unreadable snapshot
// must not be a third outcome.
func (st *Store) Delete(ctx context.Context, id session.SessionID) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	return st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
		return st.deleteSessionFamilyLocked(id)
	})
}

// DeleteSessionIfUnchanged holds the family lock across durable metadata
// revalidation and sidecar-first/snapshot-last deletion.
func (st *Store) DeleteSessionIfUnchanged(ctx context.Context, expected port.SessionDiscoveryMeta) (bool, error) {
	if err := validateSessionID(expected.ID); err != nil {
		return false, err
	}
	deleted := false
	err := st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(expected.ID), func() error {
		rows, err := st.rebuildInventoryRows()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == expected.ID && port.SessionDiscoveryMetaEqual(row, expected) {
				if err := st.deleteSessionFamilyLocked(expected.ID); err != nil {
					return err
				}
				deleted = true
				break
			}
		}
		return nil
	})
	return deleted, err
}

func (st *Store) deleteSessionFamilyLocked(id session.SessionID) error {
	dirs, err := st.openDurableDirectories(st.resolver.dir, st.resolver.canonicalDir())
	if err != nil {
		return err
	}
	defer dirs.close()
	if err := st.advanceInventoryGeneration(); err != nil {
		return err
	}
	canonicalOwned, err := st.resolver.canonicalOwnership(id)
	if err != nil {
		return err
	}
	legacyOwned, err := st.resolver.legacyOwned(id)
	if err != nil {
		return err
	}

	mutated := false
	remove := func(path, label string) error {
		removed, err := removeSessionFile(st.snapshot.remove, path)
		mutated = mutated || removed
		if err != nil {
			if mutated {
				err = errors.Join(err, dirs.sync())
			}
			return fmt.Errorf("jsonlstore: delete %s %q: %w", label, id, err)
		}
		return nil
	}
	for _, kind := range sidecarKinds {
		if err := remove(st.resolver.canonicalPath(id, kind), kind.suffix()); err != nil {
			return err
		}
		if legacyOwned {
			if err := remove(st.resolver.legacyPath(id, kind), "legacy "+kind.suffix()); err != nil {
				return err
			}
		}
	}
	if err := dirs.sync(); err != nil {
		return err
	}

	mutated = false
	if canonicalOwned {
		if err := remove(st.resolver.canonicalPath(id, kindSnapshot), "v1"); err != nil {
			return err
		}
		if err := remove(st.resolver.currentSnapshotPath(id), "current"); err != nil {
			return err
		}
	}
	if legacyOwned {
		if err := remove(st.resolver.legacyPath(id, kindSnapshot), "legacy"); err != nil {
			return err
		}
	}
	return dirs.sync()
}

func removeSessionFile(remove func(string) error, path string) (bool, error) {
	if err := remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// toolCallRecord is the structured line written by ToolCall. It is a flat,
// self-describing record for offline replay/analysis.
type toolCallRecord struct {
	Type         string             `json:"type"` // always "tool_call"
	Time         time.Time          `json:"time"`
	SessionID    session.SessionID  `json:"session_id"`
	CallID       session.ToolCallID `json:"call_id"`
	Tool         string             `json:"tool"`
	Args         json.RawMessage    `json:"args,omitempty"`
	Result       string             `json:"result"`
	IsError      bool               `json:"is_error"`
	QueuedMicros int64              `json:"queued_micros"`
	TookMicros   int64              `json:"took_micros"`
}

// ToolCall appends a structured tool-call record to the per-session tool log,
// including both the dispatch queue time (queued) and the execution wall time
// (took) in microseconds. It satisfies port.ToolCallRecorder. Errors are intentionally
// swallowed (the port has no error return) but the record is best-effort durable.
// Because the port carries no caller context, lock acquisition is capped at five
// seconds; on timeout the best-effort record is dropped rather than blocking a run.
func (st *Store) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	rec := toolCallRecord{
		Type:         "tool_call",
		Time:         time.Now().UTC(),
		SessionID:    id,
		CallID:       call.ID,
		Tool:         call.Name,
		Args:         call.Args,
		Result:       result.Content,
		IsError:      result.IsError,
		QueuedMicros: queued.Microseconds(),
		TookMicros:   took.Microseconds(),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), st.toolCallLockTimeout)
	defer cancel()
	_ = st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
		if err := st.prepareWrite(id); err != nil {
			return err
		}
		return st.appendLine(st.resolver.canonicalPath(id, kindTools), line)
	})
}

// Append records one relayed event under id as a format-tagged JSON line on the
// per-session event log. It satisfies port.EventLog. The event is marshalled to
// its session.Event JSON verbatim (already redacted at the relay) and wrapped in
// the {"v":"eventlog-json/1","ev":...} envelope so Read can validate the format.
// It uses the same stable per-family cross-process mutation identity as
// Save/Delete/ToolCall. A nil return means the newline-committed record has
// been file-synced and its containing directory has been synced on every append.
func (st *Store) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("jsonlstore: marshal event: %w", err)
	}
	line, err := json.Marshal(eventLogRecord{V: eventLogFormat, Ev: evJSON})
	if err != nil {
		return fmt.Errorf("jsonlstore: marshal event record: %w", err)
	}
	return st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
		if err := st.prepareWrite(id); err != nil {
			return err
		}
		return st.appendLine(st.resolver.canonicalPath(id, kindEvents), line)
	})
}

// Read scans the per-session event log and yields every recorded event in append
// order (cumulative — NOT latest-line-wins like the snapshot read). It satisfies
// port.EventLog. A MISS (no event file) yields an EMPTY sequence: absence is data.
// A genuine fault — an undecodable newline-terminated record, an unknown format
// tag, or an I/O error — is yielded as the error on a zero-value event and the
// consumer stops. Only an unterminated final record is ignored: newline is the
// event-record commit marker.
func (st *Store) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		if err := validateSessionID(id); err != nil {
			yield(session.Event{}, err)
			return
		}
		var (
			f            *os.File
			completeSize int64
		)
		err := st.withSnapshotFamilyLock(ctx, st.resolver.currentSnapshotPath(id), func() error {
			path, present, err := st.resolver.readablePath(id, kindEvents)
			if err != nil || !present {
				return err
			}
			var root *os.Root
			var name string
			switch path {
			case st.resolver.canonicalPath(id, kindEvents):
				root, err = os.OpenRoot(st.resolver.canonicalDir())
				name = filepath.Base(path)
			case st.resolver.legacyPath(id, kindEvents):
				root, err = os.OpenRoot(st.resolver.dir)
				name, _ = st.resolver.legacyName(id, kindEvents)
			default:
				return errors.New("event path escaped store roots")
			}
			if err != nil {
				return fmt.Errorf("open event root: %w", err)
			}
			defer func() { _ = root.Close() }()
			f, err = openRegular(root, name)
			if os.IsNotExist(err) {
				f = nil
				return nil
			}
			if err != nil {
				return fmt.Errorf("open event file: %w", err)
			}
			completeSize, err = completeRecordSize(f)
			if err != nil {
				_ = f.Close()
				f = nil
				return fmt.Errorf("inspect event file tail: %w", err)
			}
			return nil
		})
		if err != nil {
			yield(session.Event{}, fmt.Errorf("jsonlstore: capture event file: %w", err))
			return
		}
		if f == nil {
			return
		}
		defer func() { _ = f.Close() }()

		sc := newScanner(io.NewSectionReader(f, 0, completeSize))
		for sc.Scan() {
			b := sc.Bytes()
			var rec eventLogRecord
			if err := json.Unmarshal(b, &rec); err != nil {
				yield(session.Event{}, fmt.Errorf("jsonlstore: decode event record: %w", err))
				return
			}
			if rec.V != eventLogFormat {
				yield(session.Event{}, fmt.Errorf("jsonlstore: unknown event-log format %q (want %q)", rec.V, eventLogFormat))
				return
			}
			var ev session.Event
			if err := json.Unmarshal(rec.Ev, &ev); err != nil {
				yield(session.Event{}, fmt.Errorf("jsonlstore: decode event: %w", err))
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
		if err := sc.Err(); err != nil {
			yield(session.Event{}, fmt.Errorf("jsonlstore: scan event file: %w", err))
		}
	}
}

// ScheduleStore returns a port.ScheduleStore backed by the SAME directory as
// the session store (a sibling struct sharing the dir + the single-process
// mutex). Composition discovers it via type-assertion on this accessor — NOT
// by asserting the *Store itself implements port.ScheduleStore (the schedule
// store is a separate concern; the accessor keeps session-store and
// schedule-store methods from bloating one struct, the way PrunableStore is
// discovered on the store itself but here the schedule store is a sibling
// struct, not the session store). A caller that does not need schedules never
// calls this; the byte-identical default is no schedules.
func (st *Store) ScheduleStore() port.ScheduleStore {
	return &scheduleStore{dir: st.resolver.dir, mu: &st.mu}
}

// completeRecordSize returns the byte offset immediately after the final newline.
// A suffix after that offset is an uncommitted record fragment.
func completeRecordSize(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	const blockSize int64 = 32 * 1024
	for end := info.Size(); end > 0; {
		start := max(int64(0), end-blockSize)
		buf := make([]byte, end-start)
		n, err := f.ReadAt(buf, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if idx := bytes.LastIndexByte(buf[:n], '\n'); idx >= 0 {
			return start + int64(idx) + 1, nil
		}
		end = start
	}
	return 0, nil
}

func (st *Store) openCanonicalSidecarForAppend(path string) (*os.Root, *os.File, error) {
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(st.resolver.canonicalDir()) {
		return nil, nil, fmt.Errorf("jsonlstore: append path is outside canonical directory")
	}
	name := filepath.Base(path)
	if err := validResolverName(name); err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(st.resolver.canonicalDir())
	if err != nil {
		return nil, nil, fmt.Errorf("jsonlstore: open sidecar root: %w", err)
	}
	entry, lstatErr := root.Lstat(name)
	if lstatErr == nil && !entry.Mode().IsRegular() {
		_ = root.Close()
		return nil, nil, fmt.Errorf("jsonlstore: sidecar %q is not a regular file", name)
	}
	if lstatErr != nil && !os.IsNotExist(lstatErr) {
		_ = root.Close()
		return nil, nil, fmt.Errorf("jsonlstore: inspect sidecar for append: %w", lstatErr)
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NONBLOCK, 0o600)
	if errors.Is(err, os.ErrExist) {
		f, err = root.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		_ = root.Close()
		return nil, nil, fmt.Errorf("jsonlstore: open for append: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		_ = root.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("jsonlstore: stat for append: %w", err)
		}
		return nil, nil, fmt.Errorf("jsonlstore: sidecar %q is not a regular file", name)
	}
	return root, f, nil
}

// appendLine appends one newline-committed record. The caller holds the stable
// session-family lock, so truncating an uncommitted EOF fragment and appending
// the replacement is atomic with respect to every supported writer.
func (st *Store) appendLine(path string, b []byte) error {
	if !st.durability.DirectorySync {
		return errors.New("jsonlstore: sidecar append requires directory sync")
	}
	root, f, err := st.openCanonicalSidecarForAppend(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	end, err := completeRecordSize(f)
	if err != nil {
		return fmt.Errorf("jsonlstore: inspect tail for append: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("jsonlstore: stat for append: %w", err)
	}
	if end != info.Size() {
		if err := f.Truncate(end); err != nil {
			return fmt.Errorf("jsonlstore: truncate torn tail: %w", err)
		}
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		return fmt.Errorf("jsonlstore: seek for append: %w", err)
	}
	record := append(append(make([]byte, 0, len(b)+1), b...), '\n')
	written, err := st.snapshot.write(f, record)
	if err != nil {
		return fmt.Errorf("jsonlstore: append: %w", err)
	}
	if written != len(record) {
		return fmt.Errorf("jsonlstore: append: wrote %d of %d bytes: %w", written, len(record), io.ErrShortWrite)
	}
	if err := st.snapshot.syncFile(f); err != nil {
		return fmt.Errorf("jsonlstore: sync appended record: %w", err)
	}
	if err := st.snapshot.closeFile(f); err != nil {
		return fmt.Errorf("jsonlstore: close after append: %w", err)
	}
	closed = true
	dir, err := st.snapshot.openDir(st.resolver.canonicalDir()) //nolint:gosec // owner-only store directory
	if err != nil {
		return fmt.Errorf("jsonlstore: open sidecar directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := st.snapshot.syncDir(dir); err != nil {
		return fmt.Errorf("jsonlstore: sync sidecar directory: %w", err)
	}
	return nil
}

// legacySafeName maps a SessionID to the pre-v1 filename-safe token. It is
// intentionally LOSSY and retained only for legacy session-family discovery and
// schedule-store compatibility. Any rune that is not alphanumeric, '-', '_' or
// '.' becomes '_'. A leading '.' is also neutralized.
func legacySafeName(id session.SessionID) string {
	s := string(id)
	if s == "" {
		return "_empty_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if strings.HasPrefix(out, ".") {
		out = "_" + out[1:]
	}
	return out
}
