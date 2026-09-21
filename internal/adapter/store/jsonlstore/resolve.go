package jsonlstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	canonicalDirName      = "sid-v2"
	sessionTokenPrefix    = "sid-v2-"
	currentSnapshotSuffix = ".session.json"
	currentSnapshotFormat = "sessnap-current-json/2"
)

// maxTokenPrefix bounds the readable half of a session token. The hash half is
// what makes the token injective, so the prefix exists purely so an operator can
// eyeball a store directory; truncating it loses nothing.
const maxTokenPrefix = 40

// encodeSessionToken maps an opaque session id to a filename-safe stem that is
// BOUNDED and injective, but deliberately NOT reversible:
//
//	sid-v2-<up to 40 sanitized chars>-<32 hex chars of SHA-256>
//
// so the longest filename is 7 + 40 + 1 + 32 + len(".session.jsonl") = 94 bytes
// for an id of ANY length. That constant bound is the point. The previous
// encoding was Raw URL-base64 of the whole id, which inflates 4/3 with no cap
// and so imposed a 175-byte ceiling on session ids (down from 241 under the old
// lossy scheme): past that, every filesystem call returned ENAMETOOLONG, which
// is not os.IsNotExist, so a pre-existing session in that band became
// permanently unwritable after an upgrade and a long provider-supplied child id
// was silently never persisted at all.
//
// Reversibility bought nothing. Its only consumer was a cross-check in
// Current inventory reads the logical id from the snapshot envelope and verifies
// the forward-encoded family token.
// The operator workflow in user-docs/mecatui/sessions.md already recovers ids
// from file contents and explicitly warns against inferring them from names.
//
// A collision would mean two sessions sharing one family. At 128 bits that is
// unreachable in practice, and canonicalOwnership fails CLOSED on an embedded-id
// mismatch, so even then no session is ever served another's data.
//
// The sessionTokenPrefix is redundant with the versioned DIRECTORY the file lives
// in, and that is a deliberate, reviewed decision — it has been proposed for
// removal twice. It is kept so a token identifies its own scheme when it appears
// away from its directory: in a WARN, an operator's `find` output, a support
// bundle, a backup listing. The cost is 7 filename bytes out of a 255-byte
// budget that this encoding leaves 161 bytes of headroom in.
func encodeSessionToken(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	var b strings.Builder
	for _, r := range string(id) {
		if b.Len() >= maxTokenPrefix {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	prefix := b.String()
	if prefix == "" {
		prefix = "id" // an id of only unsanitizable runes, or the empty id
	}
	return sessionTokenPrefix + prefix + "-" + hex.EncodeToString(sum[:16])
}

func validateSessionID(id session.SessionID) error {
	if !utf8.ValidString(string(id)) {
		return fmt.Errorf("jsonlstore: invalid session id: must be valid UTF-8")
	}
	return nil
}

// sessionKind identifies one file in a session family. It is backed by the
// file's own suffix, so the kind->suffix mapping is the type itself — no
// switch, no unrepresentable "invalid kind" state to panic on.
type sessionKind string

const (
	kindSnapshot sessionKind = sessionFileSuffix
	kindTools    sessionKind = toolsFileSuffix
	kindEvents   sessionKind = eventsFileSuffix
)

func (k sessionKind) suffix() string {
	return string(k)
}

// sidecarKinds is the sidecars-before-snapshot deletion order. The current
// snapshot remains inventory authority until both sidecars are gone.
var sidecarKinds = []sessionKind{kindTools, kindEvents}

// sessionResolver owns current paths and old-path detection.
type sessionResolver struct {
	dir string
}

func (r sessionResolver) canonicalDir() string {
	return filepath.Join(r.dir, canonicalDirName)
}

func (r sessionResolver) canonicalPath(id session.SessionID, kind sessionKind) string {
	return filepath.Join(r.canonicalDir(), encodeSessionToken(id)+kind.suffix())
}

func (r sessionResolver) currentSnapshotPath(id session.SessionID) string {
	return filepath.Join(r.canonicalDir(), encodeSessionToken(id)+currentSnapshotSuffix)
}

func validResolverName(name string) error {
	if name == "" || name == "." || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return fmt.Errorf("jsonlstore: invalid resolver filename %q", name)
	}
	return nil
}

func (sessionResolver) canonicalName(id session.SessionID, kind sessionKind) string {
	return encodeSessionToken(id) + kind.suffix()
}

func (r sessionResolver) canonicalRelativeName(id session.SessionID, kind sessionKind) (string, error) {
	name := r.canonicalName(id, kind)
	if err := validResolverName(name); err != nil {
		return "", err
	}
	return filepath.Join(canonicalDirName, name), nil
}

func (sessionResolver) currentSnapshotName(id session.SessionID) (string, error) {
	name := encodeSessionToken(id) + currentSnapshotSuffix
	if err := validResolverName(name); err != nil {
		return "", err
	}
	return filepath.Join(canonicalDirName, name), nil
}

func (r sessionResolver) openRoot() (*os.Root, error) {
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: open store root: %w", err)
	}
	info, err := root.Lstat(canonicalDirName)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("jsonlstore: inspect canonical store dir: %w", err)
	}
	if !info.IsDir() {
		_ = root.Close()
		return nil, fmt.Errorf("jsonlstore: canonical store path %q is not a directory", canonicalDirName)
	}
	return root, nil
}

func openRegular(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("jsonlstore: %q is not a regular file", name)
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("jsonlstore: %q is not a regular file", name)
	}
	return f, nil
}

func rootPathExists(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("jsonlstore: %q is not a regular file", name)
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// currentSnapshot carries the derivative activity projection beside its canonical
// snapshot payload. It is deliberately absent from sessnap.Snapshot.
type currentSnapshot struct {
	Format     string                `json:"v"`
	ModifiedAt time.Time             `json:"modified_at"`
	Metadata   metaSnapshot          `json:"metadata,omitzero"`
	Activity   session.ActivityState `json:"activity,omitempty"`
	Snapshot   json.RawMessage       `json:"snapshot"`
}

func decodeCurrentSnapshot(data []byte) (currentSnapshot, error) {
	var current currentSnapshot
	if err := json.Unmarshal(data, &current); err != nil {
		return currentSnapshot{}, fmt.Errorf("jsonlstore: decode current snapshot: %w", err)
	}
	if current.Format != currentSnapshotFormat {
		return currentSnapshot{}, fmt.Errorf("jsonlstore: unsupported current snapshot format %q", current.Format)
	}
	if current.ModifiedAt.IsZero() {
		return currentSnapshot{}, fmt.Errorf("jsonlstore: current snapshot carries no modified_at")
	}
	if len(current.Snapshot) == 0 {
		return currentSnapshot{}, fmt.Errorf("jsonlstore: current snapshot carries no payload")
	}
	return current, nil
}

// readCurrentSnapshotHeader reads the bounded metadata prefix written before the
// snapshot payload. The boolean pair is (file present, header present). Older v2
// envelopes have no metadata field and deliberately fall back to the compatibility
// reader; current envelopes return before the decoder reaches conversation bytes.
func readCurrentSnapshotHeader(root *os.Root, name string) (currentSnapshot, bool, bool, error) {
	f, err := openRegular(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			return currentSnapshot{}, false, false, nil
		}
		return currentSnapshot{}, false, false, err
	}
	defer func() { _ = f.Close() }()

	decoder := json.NewDecoder(io.LimitReader(f, maxScannerTokenSize))
	token, err := decoder.Token()
	if err != nil {
		return currentSnapshot{}, true, false, err
	}
	if token != json.Delim('{') {
		return currentSnapshot{}, true, false, fmt.Errorf("jsonlstore: invalid current snapshot header")
	}
	var header currentSnapshot
	hasMetadata := false
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return currentSnapshot{}, true, false, err
		}
		key, ok := token.(string)
		if !ok {
			return currentSnapshot{}, true, false, fmt.Errorf("jsonlstore: invalid current snapshot header")
		}
		switch key {
		case "v":
			err = decoder.Decode(&header.Format)
		case "modified_at":
			err = decoder.Decode(&header.ModifiedAt)
		case "metadata":
			err = decoder.Decode(&header.Metadata)
			hasMetadata = err == nil && header.Format == currentSnapshotFormat && !header.ModifiedAt.IsZero() && header.Metadata.ID != ""
		case "activity":
			err = decoder.Decode(&header.Activity)
			if err == nil && hasMetadata {
				return header, true, true, nil
			}
		case "snapshot":
			if hasMetadata {
				return header, true, true, nil
			}
			return currentSnapshot{}, true, false, nil
		default:
			var ignored json.RawMessage
			err = decoder.Decode(&ignored)
		}
		if err != nil {
			return currentSnapshot{}, true, false, err
		}
	}
	return currentSnapshot{}, true, false, nil
}

func readCurrentSnapshot(root *os.Root, name string) (currentSnapshot, bool, error) {
	f, err := openRegular(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			return currentSnapshot{}, false, nil
		}
		return currentSnapshot{}, false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return currentSnapshot{}, true, err
	}
	if info.Size() > maxScannerTokenSize+lastLineSeekWindow {
		return currentSnapshot{}, true, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("jsonlstore: current snapshot exceeds %d bytes", maxScannerTokenSize))
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return currentSnapshot{}, true, err
	}
	current, err := decodeCurrentSnapshot(data)
	if err != nil {
		return currentSnapshot{}, true, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, err)
	}
	return current, true, nil
}

func snapshotIDFromLine(line []byte) (session.SessionID, error) {
	var head struct {
		ID *session.SessionID `json:"id"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return "", fmt.Errorf("jsonlstore: decode snapshot id: %w", err)
	}
	if head.ID == nil {
		return "", fmt.Errorf("jsonlstore: snapshot carries no id")
	}
	if err := validateSessionID(*head.ID); err != nil {
		return "", err
	}
	return *head.ID, nil
}

// currentSnapshotFor reads and verifies the v2 snapshot. Presence is
// authoritative: an invalid v2 file fails loudly and never falls back to v1.
func (r sessionResolver) currentSnapshotFor(id session.SessionID) (currentSnapshot, bool, error) {
	name, err := r.currentSnapshotName(id)
	if err != nil {
		return currentSnapshot{}, false, err
	}
	root, err := r.openRoot()
	if err != nil {
		return currentSnapshot{}, false, err
	}
	defer func() { _ = root.Close() }()
	current, present, err := readCurrentSnapshot(root, name)
	if err != nil || !present {
		return current, present, err
	}
	embedded, err := snapshotIDFromLine(current.Snapshot)
	if err != nil {
		return currentSnapshot{}, true, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("jsonlstore: inspect current snapshot: %w", err))
	}
	if embedded != id {
		return currentSnapshot{}, true, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("jsonlstore: current session id mismatch: stored %q, requested %q", embedded, id))
	}
	if _, err := sessnap.Unmarshal(current.Snapshot); err != nil {
		return currentSnapshot{}, true, port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("jsonlstore: verify current snapshot: %w", err))
	}
	return current, true, nil
}

func (r sessionResolver) exists(name string) (bool, error) {
	root, err := r.openRoot()
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	return rootPathExists(root, name)
}

func (r sessionResolver) currentSnapshotExists(id session.SessionID) (bool, error) {
	name, err := r.currentSnapshotName(id)
	if err != nil {
		return false, err
	}
	return r.exists(name)
}

func (r sessionResolver) loadSnapshot(id session.SessionID) ([]byte, error) {
	current, present, err := r.currentSnapshotFor(id)
	if err != nil || present {
		return current.Snapshot, err
	}
	return nil, sessionNotFound(id)
}

func (st *Store) prepareWrite(id session.SessionID) error {
	return validateSessionID(id)
}

func (r sessionResolver) snapshotModifiedAt(_ session.SessionID, now time.Time) (time.Time, error) {
	return now.UTC(), nil
}

// readablePath resolves only the current family's canonical sidecar.
func (r sessionResolver) readablePath(id session.SessionID, kind sessionKind) (string, bool, error) {
	name, err := r.canonicalRelativeName(id, kind)
	if err != nil {
		return "", false, err
	}
	present, err := r.exists(name)
	return r.canonicalPath(id, kind), present, err
}

type snapshotFile struct {
	id             session.SessionID
	last           []byte
	metadata       *metaSnapshot
	activity       session.ActivityState
	modified       time.Time
	estimatedBytes int64
	priority       int
}

// snapshotFiles enumerates only current snapshots.
func (r sessionResolver) snapshotFiles() ([]snapshotFile, error) {
	root, err := r.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	canonicalRoot, err := root.OpenRoot(canonicalDirName)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: open canonical store dir: %w", err)
	}
	defer func() { _ = canonicalRoot.Close() }()
	byID := make(map[session.SessionID]snapshotFile)
	if err := scanCurrentSnapshotDir(canonicalRoot, byID); err != nil {
		return nil, err
	}
	out := slices.Collect(maps.Values(byID))
	slices.SortFunc(out, func(a, b snapshotFile) int { return strings.Compare(string(a.id), string(b.id)) })
	return out, nil
}

func scanCurrentSnapshotDir(root *os.Root, byID map[session.SessionID]snapshotFile) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("jsonlstore: list store dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), currentSnapshotSuffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("jsonlstore: inspect current snapshot %q: %w", entry.Name(), err)
		}
		header, present, hasHeader, err := readCurrentSnapshotHeader(root, entry.Name())
		if err != nil {
			return fmt.Errorf("jsonlstore: inspect current snapshot %q: %w", entry.Name(), err)
		}
		if !present {
			return fmt.Errorf("jsonlstore: current snapshot %q disappeared during inventory", entry.Name())
		}
		if hasHeader {
			id := header.Metadata.ID
			if strings.TrimSuffix(entry.Name(), currentSnapshotSuffix) != encodeSessionToken(id) {
				return fmt.Errorf("jsonlstore: current snapshot %q has mismatched session id %q", entry.Name(), id)
			}
			m := header.Metadata
			byID[id] = snapshotFile{id: id, metadata: &m, activity: header.Activity, modified: header.ModifiedAt, estimatedBytes: info.Size(), priority: 2}
			continue
		}
		current, present, err := readCurrentSnapshot(root, entry.Name())
		if err != nil {
			return fmt.Errorf("jsonlstore: read current snapshot %q: %w", entry.Name(), err)
		}
		if !present {
			return fmt.Errorf("jsonlstore: current snapshot %q disappeared during inventory", entry.Name())
		}
		id := current.Metadata.ID
		if id == "" {
			id, err = snapshotIDFromLine(current.Snapshot)
		}
		if err != nil {
			return fmt.Errorf("jsonlstore: inspect current snapshot %q: %w", entry.Name(), err)
		}
		if strings.TrimSuffix(entry.Name(), currentSnapshotSuffix) != encodeSessionToken(id) {
			return fmt.Errorf("jsonlstore: current snapshot %q has mismatched session id %q", entry.Name(), id)
		}
		var metadata *metaSnapshot
		if current.Metadata.ID != "" {
			m := current.Metadata
			metadata = &m
		}
		byID[id] = snapshotFile{id: id, last: current.Snapshot, metadata: metadata, activity: current.Activity, modified: current.ModifiedAt, estimatedBytes: info.Size(), priority: 2}
	}
	return nil
}

func sessionNotFound(id session.SessionID) error {
	return fmt.Errorf("%w: %q", ErrNotFound, id)
}
