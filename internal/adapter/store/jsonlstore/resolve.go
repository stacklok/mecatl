package jsonlstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/stacklok/mecatl/engine/session"
)

const (
	canonicalDirName      = "sid-v1"
	sessionTokenPrefix    = "sid-v1-"
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
//	sid-v1-<up to 40 sanitized chars>-<32 hex chars of SHA-256>
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
// scanSnapshotDir, which reads the logical id out of the file's own contents
// anyway and merely needs to confirm the stem belongs to it — re-encoding
// forward proves that exactly as well, which is why decodeSessionToken is gone
// along with the non-canonical-alias hazard that a reversible codec creates.
// The operator workflow in docs/usage/troubleshooting.md already recovers ids
// from file contents and explicitly warns against inferring them from names.
//
// A collision would mean two sessions sharing one family. At 128 bits that is
// unreachable in practice, and canonicalOwnership fails CLOSED on an embedded-id
// mismatch, so even then no session is ever served another's data.
//
// The sessionTokenPrefix is redundant with the sid-v1 DIRECTORY the file lives
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

// familyOrder is the sidecars-before-snapshot order shared by every operation
// that touches a whole session family (migration, deletion): the snapshot
// file is what List/snapshotFiles enumerate, so writing/removing it LAST
// means an operation interrupted partway through still leaves the family
// discoverable (a migration retries; a delete's next sweep retries), whereas
// doing the snapshot first would create a window where the family is
// invisible while a sidecar still exists — a leak no future sweep can find.
var familyOrder = []sessionKind{kindTools, kindEvents, kindSnapshot}

// sidecarKinds is familyOrder without the snapshot — the files a caller must
// remove or move BEFORE it touches the snapshot. It is spelled out rather than
// sliced off familyOrder (`familyOrder[:len(familyOrder)-1]`), because a slice
// expression would silently depend on the snapshot staying LAST: reordering the
// familyOrder literal would then invert the very order its comment calls
// load-bearing, with no compile error and no failing test.
var sidecarKinds = []sessionKind{kindTools, kindEvents}

// sessionResolver owns canonical and legacy paths, ownership checks, and
// write-time migration. Callers serialize migration with the stable family flock.
type sessionResolver struct {
	dir string
}

func (r sessionResolver) canonicalDir() string {
	return filepath.Join(r.dir, canonicalDirName)
}

func (r sessionResolver) canonicalPath(id session.SessionID, kind sessionKind) string {
	return filepath.Join(r.canonicalDir(), encodeSessionToken(id)+kind.suffix())
}

func (r sessionResolver) legacyPath(id session.SessionID, kind sessionKind) string {
	return filepath.Join(r.dir, legacySafeName(id)+kind.suffix())
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

func (sessionResolver) legacyName(id session.SessionID, kind sessionKind) (string, error) {
	name := legacySafeName(id) + kind.suffix()
	if err := validResolverName(name); err != nil {
		return "", err
	}
	return name, nil
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
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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

type currentSnapshot struct {
	Format     string          `json:"v"`
	ModifiedAt time.Time       `json:"modified_at"`
	Metadata   metaSnapshot    `json:"metadata,omitzero"`
	Snapshot   json.RawMessage `json:"snapshot"`
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
			if err == nil && header.Format == currentSnapshotFormat && !header.ModifiedAt.IsZero() && header.Metadata.ID != "" {
				return header, true, true, nil
			}
		case "snapshot":
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
		return currentSnapshot{}, true, fmt.Errorf("jsonlstore: current snapshot exceeds %d bytes", maxScannerTokenSize)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return currentSnapshot{}, true, err
	}
	current, err := decodeCurrentSnapshot(data)
	return current, true, err
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

// readSnapshotLine returns nil, nil only when path does not exist. A present
// empty or unreadable file is an infrastructure error and never triggers fallback.
func readSnapshotLine(root *os.Root, name string) ([]byte, error) {
	f, err := openRegular(root, name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jsonlstore: read session file tail: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: read session file tail: %w", err)
	}
	last, err := readLastLineAt(f, info.Size())
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: read session file tail: %w", err)
	}
	if last == nil {
		return nil, fmt.Errorf("jsonlstore: empty session file: %s", name)
	}
	return last, nil
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
		return currentSnapshot{}, true, fmt.Errorf("jsonlstore: inspect current snapshot: %w", err)
	}
	if embedded != id {
		return currentSnapshot{}, true, fmt.Errorf("jsonlstore: current session id mismatch: stored %q, requested %q", embedded, id)
	}
	if _, err := sessnap.Unmarshal(current.Snapshot); err != nil {
		return currentSnapshot{}, true, fmt.Errorf("jsonlstore: verify current snapshot: %w", err)
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

func (r sessionResolver) readLastLine(name string) ([]byte, error) {
	root, err := r.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := openRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return readLastLineAt(f, info.Size())
}

func (r sessionResolver) readSnapshotLine(name string) ([]byte, error) {
	root, err := r.openRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return readSnapshotLine(root, name)
}

// canonicalOwnership reports whether this session's canonical family exists,
// and fails closed ONLY when the stored snapshot POSITIVELY proves the file
// belongs to another id. It is the ownership question, deliberately separated
// from snapshot VALIDITY, which only loadSnapshot needs.
//
// The distinction is the whole point. Two states used to be conflated:
//
//   - The latest line is TORN or the file is empty — what a crash mid-appendLine
//     leaves, and routine. Ownership is UNPROVEN, not disproven: the path token
//     is injective, so the family is ours whatever the bytes say. Proceed.
//     Treating this as a hard failure is what made one torn line break three
//     unrelated operations — Delete returned an error having removed nothing,
//     violating port.PrunableStore's idempotence and leaving the session
//     permanently unprunable (List skips undecodable files, so nothing ever
//     surfaced it again); EventLog.Read failed for a byte-perfect events file;
//     and every Save/Append/ToolCall failed even though appending a fresh
//     snapshot is precisely what heals the file, Load being last-line-wins.
//   - The embedded id DECODES and differs — reachable only by hand-copying or
//     editing a file into the canonical namespace, since the token is
//     injective. Ownership is disproven, so fail closed everywhere including
//     Delete, which must not destroy another session's data.
//
// The read is a reverse TAIL read bounded by the latest record, never a full
// scan: this runs on every Save, Append and ToolCall, and a snapshot file grows as
// turns x conversation size, so a full scan per appended event was quadratic in
// run length while holding the per-family mutation lock.
func (r sessionResolver) canonicalOwnership(id session.SessionID) (bool, error) {
	currentName, err := r.currentSnapshotName(id)
	if err != nil {
		return false, err
	}
	currentPresent, err := r.exists(currentName)
	if err != nil || currentPresent {
		return currentPresent, err
	}
	name, err := r.canonicalRelativeName(id, kindSnapshot)
	if err != nil {
		return false, err
	}
	present, err := r.exists(name)
	if err != nil || !present {
		return false, err
	}
	last, err := r.readLastLine(name)
	if err != nil || last == nil {
		return true, nil // present, ownership unproven — ours by path
	}
	embedded, err := snapshotIDFromLine(last)
	if err != nil {
		return true, nil // undecodable line — same reasoning
	}
	if embedded != id {
		return true, fmt.Errorf("jsonlstore: canonical session id mismatch: stored %q, requested %q", embedded, id)
	}
	return true, nil
}

// canonicalSnapshot reads the latest canonical snapshot line for a READER. It
// keeps the embedded-id integrity check (the token is injective, so a mismatch
// means the file was tampered with or hand-copied, not that it belongs to
// another session) and leaves the full sessnap decode to Store.Load, which
// unmarshals the same bytes anyway.
func (r sessionResolver) canonicalSnapshot(id session.SessionID) ([]byte, bool, error) {
	name, err := r.canonicalRelativeName(id, kindSnapshot)
	if err != nil {
		return nil, false, err
	}
	line, err := r.readSnapshotLine(name)
	if err != nil || line == nil {
		return nil, false, err
	}
	embedded, err := snapshotIDFromLine(line)
	if err != nil {
		return nil, true, fmt.Errorf("jsonlstore: inspect canonical session file: %w", err)
	}
	if embedded != id {
		return nil, true, fmt.Errorf("jsonlstore: canonical session id mismatch: stored %q, requested %q", embedded, id)
	}
	return line, true, nil
}

// loadSnapshot is read-only. Canonical presence is authoritative; legacy is
// readable only when its latest embedded id exactly matches the requested id.
func (r sessionResolver) loadSnapshot(id session.SessionID) ([]byte, error) {
	current, present, err := r.currentSnapshotFor(id)
	if err != nil || present {
		return current.Snapshot, err
	}
	line, present, err := r.canonicalSnapshot(id)
	if err != nil || present {
		return line, err
	}
	line, ok, err := r.legacySnapshot(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, sessionNotFound(id)
	}
	return line, nil
}

// prepareWrite confirms the canonical family exists, or migrates one verified
// legacy family forward. Absent and mismatched legacy snapshots never authorize
// sidecars.
//
// It checks canonical PRESENCE, not snapshot validity: baseline Save did not
// read the snapshot at all before appending, and a torn latest line must not
// make a session unwritable — appending a fresh snapshot after it is precisely
// what restores the session, since Load takes the last line.
func (st *Store) prepareWrite(id session.SessionID) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	present, err := st.resolver.canonicalOwnership(id)
	if err != nil {
		return err
	}
	if present {
		// A canonical v1 snapshot may be the visible result of an earlier legacy
		// rename whose final directory sync failed. Re-sync both namespaces on the
		// retry before claiming success. Current v2 snapshots have their own
		// replacement boundary and stay on the ordinary fast path.
		currentName, nameErr := st.resolver.currentSnapshotName(id)
		if nameErr != nil {
			return nameErr
		}
		current, existsErr := st.resolver.exists(currentName)
		if existsErr != nil || current || !st.durability.DirectorySync {
			return existsErr
		}
		dirs, openErr := st.openDurableDirectories(st.resolver.dir, st.resolver.canonicalDir())
		if openErr != nil {
			return openErr
		}
		defer dirs.close()
		return dirs.sync()
	}
	line, ok, err := st.resolver.legacySnapshot(id)
	if err != nil || !ok {
		return err
	}
	if _, err := sessnap.Unmarshal(line); err != nil {
		return err
	}
	return st.migrateLegacyFamily(id)
}

// snapshotModifiedAt preserves the v1 file's logical modification time on the
// first lazy promotion. Later v2 saves advance the logical time.
func (r sessionResolver) snapshotModifiedAt(id session.SessionID, now time.Time) (time.Time, error) {
	_, present, err := r.currentSnapshotFor(id)
	if err != nil {
		return time.Time{}, err
	}
	if present {
		return now.UTC(), nil
	}
	name, err := r.canonicalRelativeName(id, kindSnapshot)
	if err != nil {
		return time.Time{}, err
	}
	root, err := r.openRoot()
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return time.Time{}, fmt.Errorf("jsonlstore: %q is not a regular file", name)
		}
		return info.ModTime().UTC(), nil
	}
	if !os.IsNotExist(err) {
		return time.Time{}, err
	}
	return now.UTC(), nil
}

// readablePath resolves a sidecar without modifying storage. A canonical
// snapshot's PRESENCE decides whether legacy fallback is allowed; its validity
// is irrelevant here — a sidecar (the durable event log especially) must stay
// readable when the snapshot beside it is torn, since the log exists to survive
// exactly the crash that tore it.
func (r sessionResolver) readablePath(id session.SessionID, kind sessionKind) (string, bool, error) {
	snapshotPresent, err := r.canonicalOwnership(id)
	if err != nil {
		return "", false, err
	}
	canonical := r.canonicalPath(id, kind)
	canonicalName, err := r.canonicalRelativeName(id, kind)
	if err != nil {
		return "", false, err
	}
	present, err := r.exists(canonicalName)
	if err != nil {
		return "", false, err
	}
	if present {
		return canonical, true, nil
	}
	if snapshotPresent {
		return "", false, nil
	}
	_, ok, err := r.legacySnapshot(id)
	if err != nil || !ok {
		return "", false, err
	}
	legacy := r.legacyPath(id, kind)
	legacyName, err := r.legacyName(id, kind)
	if err != nil {
		if nameTooLong(err) {
			return "", false, nil
		}
		return "", false, err
	}
	present, err = r.exists(legacyName)
	if nameTooLong(err) {
		return "", false, nil
	}
	return legacy, present, err
}

func legacyLineOwnedBy(line []byte, id session.SessionID) (bool, error) {
	embedded, err := snapshotIDFromLine(line)
	if err != nil {
		return false, fmt.Errorf("jsonlstore: inspect legacy session file: %w", err)
	}
	return embedded == id, nil
}

// nameTooLong reports whether err is the filesystem refusing a path component
// as over-long. For a LEGACY probe that is equivalent to absence and must be
// treated as such: legacySafeName is 1:1 with the id, so if the id is too long
// to name, the pre-rewrite scheme could never have created that file either —
// there is nothing there to find. Returning the raw error instead re-imposed the
// very ceiling the bounded canonical token removed, because prepareWrite probes
// for a legacy family on every write regardless of the id's length. Caught by
// storeconformance's long-id case, not by any jsonlstore test.
func nameTooLong(err error) bool {
	return errors.Is(err, syscall.ENAMETOOLONG)
}

// legacySnapshot returns the legacy snapshot line only when its own latest
// embedded id equals id. There are THREE outcomes, and the third must never be
// folded into the other two:
//
//   - (line, true, nil)  — the legacy family is ours; the line is its snapshot.
//   - (nil, false, nil)  — absent, OR owned by a different session that the
//     lossy stem collided with. Callers treat these two identically: a clean
//     not-found for reads, a clean no-op for migration.
//   - (nil, false, err)  — we could not TELL (unreadable, empty, undecodable).
//     This is an infrastructure error and callers MUST propagate it. Treating
//     it as "not ours" would fail OPEN on the ownership proof that keeps one
//     session's data from being served for another — do not simplify a caller
//     to `if !ok { return nil }`.
func (r sessionResolver) legacySnapshot(id session.SessionID) ([]byte, bool, error) {
	name, err := r.legacyName(id, kindSnapshot)
	if nameTooLong(err) {
		return nil, false, nil // unnameable under the 1:1 legacy scheme ⇒ absent
	}
	if err != nil {
		return nil, false, err
	}
	line, err := r.readSnapshotLine(name)
	if nameTooLong(err) {
		return nil, false, nil // unnameable under the 1:1 legacy scheme ⇒ absent
	}
	if err != nil || line == nil {
		return nil, false, err
	}
	owned, err := legacyLineOwnedBy(line, id)
	if err != nil || !owned {
		return nil, false, err
	}
	return line, true, nil
}

func (r sessionResolver) legacyOwned(id session.SessionID) (bool, error) {
	_, ok, err := r.legacySnapshot(id)
	return ok, err
}

type snapshotFile struct {
	id             session.SessionID
	last           []byte
	metadata       *metaSnapshot
	modified       time.Time
	estimatedBytes int64
	priority       int // legacy v1 < canonical v1 < current v2
}

// snapshotFiles reads logical ids from snapshots, deduplicates all readable
// generations, and returns bytewise-id order.
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
	if err := scanSnapshotDir(root, false, byID); err != nil {
		return nil, err
	}
	if err := scanSnapshotDir(canonicalRoot, true, byID); err != nil {
		return nil, err
	}
	if err := scanCurrentSnapshotDir(canonicalRoot, byID); err != nil {
		return nil, err
	}
	out := slices.Collect(maps.Values(byID))
	slices.SortFunc(out, func(a, b snapshotFile) int { return strings.Compare(string(a.id), string(b.id)) })
	return out, nil
}

func scanSnapshotDir(root *os.Root, canonical bool, byID map[session.SessionID]snapshotFile) error {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("jsonlstore: list store dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionFileSuffix) {
			continue
		}
		last, err := readSnapshotLine(root, entry.Name())
		if err != nil || last == nil {
			continue
		}
		id, err := snapshotIDFromLine(last)
		if err != nil {
			continue
		}
		// The logical id always comes from the file's own contents. For a
		// canonical entry, confirm the physical stem belongs to that id by
		// re-encoding FORWARD — the token is injective, so this is exactly as
		// strong as decoding the stem would be, and it needs no reversible codec.
		// A stem that does not match (hand-planted, or written by an older
		// encoding) is skipped rather than trusted.
		if canonical && strings.TrimSuffix(entry.Name(), sessionFileSuffix) != encodeSessionToken(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		priority := 0
		if canonical {
			priority = 1
		}
		candidate := snapshotFile{id: id, last: last, modified: info.ModTime(), estimatedBytes: info.Size(), priority: priority}
		current, exists := byID[id]
		if !exists || candidate.priority > current.priority ||
			candidate.priority == current.priority && candidate.modified.After(current.modified) {
			byID[id] = candidate
		}
	}
	return nil
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
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		header, present, hasHeader, err := readCurrentSnapshotHeader(root, entry.Name())
		if err != nil || !present {
			continue
		}
		if hasHeader {
			id := header.Metadata.ID
			if strings.TrimSuffix(entry.Name(), currentSnapshotSuffix) != encodeSessionToken(id) {
				continue
			}
			m := header.Metadata
			byID[id] = snapshotFile{id: id, metadata: &m, modified: header.ModifiedAt, estimatedBytes: info.Size(), priority: 2}
			continue
		}

		current, present, err := readCurrentSnapshot(root, entry.Name())
		if err != nil || !present {
			continue
		}
		id := current.Metadata.ID
		if id == "" { // compatibility with v2 snapshots written before inventory headers
			id, err = snapshotIDFromLine(current.Snapshot)
		}
		if err != nil || strings.TrimSuffix(entry.Name(), currentSnapshotSuffix) != encodeSessionToken(id) {
			continue
		}
		var metadata *metaSnapshot
		if current.Metadata.ID != "" {
			m := current.Metadata
			metadata = &m
		}
		byID[id] = snapshotFile{id: id, last: current.Snapshot, metadata: metadata, modified: current.ModifiedAt, estimatedBytes: info.Size(), priority: 2}
	}
	return nil
}

// migrateLegacyFamily preserves bytes and append order by renaming sidecars
// first and the snapshot last (familyOrder). Missing sources support
// interrupted retries.
func (st *Store) migrateLegacyFamily(id session.SessionID) error {
	dirs, err := st.openDurableDirectories(st.resolver.dir, st.resolver.canonicalDir())
	if err != nil {
		return err
	}
	defer dirs.close()
	root, err := st.resolver.openRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	move := func(kind sessionKind) error {
		src, err := st.resolver.legacyName(id, kind)
		if err != nil {
			return fmt.Errorf("jsonlstore: migrate %s: %w", kind.suffix(), err)
		}
		dst, err := st.resolver.canonicalRelativeName(id, kind)
		if err != nil {
			return fmt.Errorf("jsonlstore: migrate %s: %w", kind.suffix(), err)
		}
		if err := moveLegacyFile(st.snapshot.moveRoot, root, src, dst); err != nil {
			return fmt.Errorf("jsonlstore: migrate %s: %w", kind.suffix(), err)
		}
		return nil
	}
	for _, kind := range sidecarKinds {
		if err := move(kind); err != nil {
			return errors.Join(err, dirs.sync())
		}
	}
	if err := dirs.sync(); err != nil {
		return err
	}
	if err := move(kindSnapshot); err != nil {
		return errors.Join(err, dirs.sync())
	}
	return dirs.sync()
}

// moveLegacyFile renames one legacy family file onto its canonical path.
//
// The source lstat is NOT a redundant round-trip that Rename's own ENOENT
// could replace — it is load-bearing twice, and both uses are easy to lose:
//
//   - It must come FIRST, so an absent source returns nil REGARDLESS of the
//     destination. That is the interrupted-migration retry shape (a previous
//     attempt already moved this file), pinned by
//     TestInterruptedMigrationRetriesAfterSidecarAlreadyMoved. Checking the
//     destination first turns that retry into a spurious clash error.
//   - IsRegular refuses a non-regular source instead of renaming it into the
//     canonical namespace. os.Rename happily moves a directory, after which
//     every Load/Save/Append for that id fails with EISDIR forever while List
//     silently omits it; a FIFO is worse — readSnapshotLine's os.Open blocks
//     indefinitely waiting for a writer while the family lock is held, blocking
//     every same-family mutation.
//
// A present destination with a present regular source is a genuine clash:
// error rather than let os.Rename silently clobber already-migrated data.
func moveLegacyFile(move func(*os.Root, string, string) error, root *os.Root, src, dst string) error {
	info, err := root.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source %q is not a regular file", src)
	}
	present, err := rootPathExists(root, dst)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("destination already exists: %q", dst)
	}
	return move(root, src, dst)
}

func sessionNotFound(id session.SessionID) error {
	return fmt.Errorf("%w: %q", ErrNotFound, id)
}
