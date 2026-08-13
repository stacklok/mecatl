package jsonlstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	canonicalDirName   = "sid-v1"
	sessionTokenPrefix = "sid-v1-"
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
// write-time migration. Store.mu serializes migration and writes.
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
func readSnapshotLine(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // resolver-derived path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("jsonlstore: open session file: %w", err)
	}
	defer func() { _ = f.Close() }()
	last, err := scanLastNonBlankLine(f)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: scan session file: %w", err)
	}
	if last == nil {
		return nil, fmt.Errorf("jsonlstore: empty session file: %s", path)
	}
	return last, nil
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
// The read is a TAIL read (readLastLine's window), never a full scan: this runs
// on every Save, Append and ToolCall, and a snapshot file grows as
// turns x conversation size, so a full scan per appended event was quadratic in
// run length while holding Store.mu.
func (r sessionResolver) canonicalOwnership(id session.SessionID) (bool, error) {
	path := r.canonicalPath(id, kindSnapshot)
	present, err := pathExists(path)
	if err != nil || !present {
		return false, err
	}
	last, err := readLastLine(path)
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
	line, err := readSnapshotLine(r.canonicalPath(id, kindSnapshot))
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
func (r sessionResolver) prepareWrite(id session.SessionID) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	present, err := r.canonicalOwnership(id)
	if err != nil || present {
		return err
	}
	line, ok, err := r.legacySnapshot(id)
	if err != nil || !ok {
		return err
	}
	if _, err := sessnap.Unmarshal(line); err != nil {
		return err
	}
	return r.migrateLegacyFamily(id)
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
	present, err := pathExists(canonical)
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
	present, err = pathExists(legacy)
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
	line, err := readSnapshotLine(r.legacyPath(id, kindSnapshot))
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
	id        session.SessionID
	last      []byte
	modified  time.Time
	canonical bool
}

// snapshotFiles reads logical ids from snapshots, deduplicates canonical and
// legacy families, and returns bytewise-id order.
func (r sessionResolver) snapshotFiles() ([]snapshotFile, error) {
	byID := make(map[session.SessionID]snapshotFile)
	if err := scanSnapshotDir(r.dir, false, byID); err != nil {
		return nil, err
	}
	if err := scanSnapshotDir(r.canonicalDir(), true, byID); err != nil {
		return nil, err
	}
	out := slices.Collect(maps.Values(byID))
	slices.SortFunc(out, func(a, b snapshotFile) int { return strings.Compare(string(a.id), string(b.id)) })
	return out, nil
}

func scanSnapshotDir(dir string, canonical bool, byID map[session.SessionID]snapshotFile) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("jsonlstore: list store dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionFileSuffix) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		last, err := readLastLine(path)
		if err != nil {
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
		candidate := snapshotFile{id: id, last: last, modified: info.ModTime(), canonical: canonical}
		current, exists := byID[id]
		if !exists || candidate.canonical && !current.canonical ||
			candidate.canonical == current.canonical && candidate.modified.After(current.modified) {
			byID[id] = candidate
		}
	}
	return nil
}

// migrateLegacyFamily preserves bytes and append order by renaming sidecars
// first and the snapshot last (familyOrder). Missing sources support
// interrupted retries.
func (r sessionResolver) migrateLegacyFamily(id session.SessionID) error {
	for _, kind := range familyOrder {
		if err := moveLegacyFile(r.legacyPath(id, kind), r.canonicalPath(id, kind)); err != nil {
			return fmt.Errorf("jsonlstore: migrate %s: %w", kind.suffix(), err)
		}
	}
	return nil
}

// moveLegacyFile renames one legacy family file onto its canonical path.
//
// The os.Stat(src) is NOT a redundant round-trip that os.Rename's own ENOENT
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
//     indefinitely waiting for a writer while Store.mu is held, deadlocking
//     the whole store.
//
// A present destination with a present regular source is a genuine clash:
// error rather than let os.Rename silently clobber already-migrated data.
func moveLegacyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source %q is not a regular file", src)
	}
	present, err := pathExists(dst)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("destination already exists: %q", dst)
	}
	return os.Rename(src, dst)
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func sessionNotFound(id session.SessionID) error {
	return fmt.Errorf("%w: %q", ErrNotFound, id)
}
