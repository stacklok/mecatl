package jsonlstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	canonicalDirName   = "sid-v1"
	sessionTokenPrefix = "sid-v1-"
)

func encodeSessionToken(id session.SessionID) string {
	return sessionTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeSessionToken(token string) (session.SessionID, error) {
	if !strings.HasPrefix(token, sessionTokenPrefix) {
		return "", fmt.Errorf("jsonlstore: not a session token: %q", token)
	}
	encoded := strings.TrimPrefix(token, sessionTokenPrefix)
	b, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("jsonlstore: decode session token %q: %w", token, err)
	}
	id := session.SessionID(b)
	if encodeSessionToken(id) != token {
		return "", fmt.Errorf("jsonlstore: non-canonical session token %q", token)
	}
	return id, nil
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

// canonicalSnapshot validates both physical ownership and the complete latest
// snapshot. A present canonical file is authoritative even when invalid.
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
	if _, err := sessnap.Unmarshal(line); err != nil {
		return nil, true, err
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

// prepareWrite validates canonical ownership or migrates one verified legacy
// family. Absent and mismatched legacy snapshots never authorize sidecars.
func (r sessionResolver) prepareWrite(id session.SessionID) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	_, present, err := r.canonicalSnapshot(id)
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
// snapshot, when present, must validate before any canonical sidecar is read.
func (r sessionResolver) readablePath(id session.SessionID, kind sessionKind) (string, bool, error) {
	_, snapshotPresent, err := r.canonicalSnapshot(id)
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
		var tokenID session.SessionID
		if canonical {
			stem := strings.TrimSuffix(entry.Name(), sessionFileSuffix)
			tokenID, err = decodeSessionToken(stem)
			if err != nil {
				continue
			}
		}
		path := filepath.Join(dir, entry.Name())
		last, err := readLastLine(path)
		if err != nil {
			continue
		}
		id, err := snapshotIDFromLine(last)
		if err != nil || canonical && tokenID != id {
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
