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

type sessionKind int

const (
	kindSnapshot sessionKind = iota
	kindTools
	kindEvents
)

func (k sessionKind) suffix() string {
	switch k {
	case kindSnapshot:
		return sessionFileSuffix
	case kindTools:
		return toolsFileSuffix
	case kindEvents:
		return eventsFileSuffix
	default:
		panic("jsonlstore: invalid session file kind")
	}
}

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
	line, err = readSnapshotLine(r.legacyPath(id, kindSnapshot))
	if err != nil {
		return nil, err
	}
	if line == nil {
		return nil, sessionNotFound(id)
	}
	owned, err := legacyLineOwnedBy(line, id)
	if err != nil {
		return nil, err
	}
	if !owned {
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
	line, err := readSnapshotLine(r.legacyPath(id, kindSnapshot))
	if err != nil {
		return err
	}
	if line == nil {
		return nil
	}
	owned, err := legacyLineOwnedBy(line, id)
	if err != nil {
		return err
	}
	if !owned {
		return nil
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
	line, err := readSnapshotLine(r.legacyPath(id, kindSnapshot))
	if err != nil {
		return "", false, err
	}
	if line == nil {
		return "", false, nil
	}
	owned, err := legacyLineOwnedBy(line, id)
	if err != nil {
		return "", false, err
	}
	if !owned {
		return "", false, nil
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

func (r sessionResolver) legacyOwned(id session.SessionID) (bool, error) {
	line, err := readSnapshotLine(r.legacyPath(id, kindSnapshot))
	if err != nil || line == nil {
		return false, err
	}
	return legacyLineOwnedBy(line, id)
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
// first and the snapshot last. Missing sources support interrupted retries.
func (r sessionResolver) migrateLegacyFamily(id session.SessionID) error {
	for _, kind := range []sessionKind{kindTools, kindEvents, kindSnapshot} {
		if err := moveLegacyFile(r.legacyPath(id, kind), r.canonicalPath(id, kind)); err != nil {
			return fmt.Errorf("jsonlstore: migrate %s: %w", kind.suffix(), err)
		}
	}
	return nil
}

func moveLegacyFile(src, dst string) error {
	present, err := pathExists(dst)
	if err != nil {
		return err
	}
	if present {
		// Destination already migrated. A source still present alongside it is a
		// genuine clash (guard against silently clobbering already-migrated data
		// via os.Rename's overwrite-on-POSIX behavior); a missing source is the
		// expected state on an interrupted-migration retry.
		if _, statErr := os.Stat(src); statErr == nil { //nolint:gosec // resolver-derived path
			return fmt.Errorf("destination already exists: %q", dst)
		}
		return nil
	}
	if err := os.Rename(src, dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
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
