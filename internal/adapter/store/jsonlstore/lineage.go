package jsonlstore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	lineageRecordFormat      = "session-lineage-record-jsonl/2"
	lineageEdgeFormat        = "session-lineage-edge-jsonl/2"
	lineagePointFormat       = "session-lineage-point-jsonl/2"
	legacyLineageIndexFormat = "session-lineage-json/1"
)

var (
	errLineagePartitionUnavailable = errors.New("jsonlstore: lineage partition unavailable")
	errLineagePartitionIncomplete  = errors.New("jsonlstore: lineage partition is incomplete")
)

type lineageHeader struct {
	Format string `json:"v"`
}

func lineageToken(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (st *Store) lineageRecordPath(id session.SessionID) string {
	return filepath.Join(st.inventoryCatalogDir(), ".session-lineage-record-"+lineageToken(string(id))+".jsonl")
}

func (st *Store) lineageEdgePath(id session.SessionID, incarnation session.IncarnationID) string {
	return filepath.Join(st.inventoryCatalogDir(), ".session-lineage-edge-"+lineageToken(string(id), string(incarnation))+".jsonl")
}

func (st *Store) lineagePointPath(parent session.SessionID, parentIncarnation session.IncarnationID, child session.SessionID, childIncarnation string) string {
	return filepath.Join(st.inventoryCatalogDir(), ".session-lineage-point-"+lineageToken(string(parent), string(parentIncarnation), string(child), childIncarnation)+".jsonl")
}

func (st *Store) legacyLineageIndexPath() string {
	return filepath.Join(st.inventoryCatalogDir(), ".session-lineage.json")
}

func (st *Store) lineageMigrationPath() string {
	return filepath.Join(st.inventoryCatalogDir(), ".session-lineage-migration.dirty")
}

func (st *Store) legacyLineageUncertain() (bool, error) {
	_, err := os.Stat(st.legacyLineageIndexPath()) //nolint:gosec // adapter-private owner-only path
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("jsonlstore: inspect legacy lineage index: %w", err)
}

func lineageDirtyPath(path string) string { return path + ".dirty" }
func lineageLockPath(path string) string  { return path + ".lock" }

func (*Store) withLineagePartitionLocks(ctx context.Context, paths []string, fn func() error) error {
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	paths = compactStrings(paths)
	locks := make([]*flock.Flock, 0, len(paths))
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Close()
		}
	}()
	for _, path := range paths {
		fl := flock.New(lineageLockPath(path), flock.SetPermissions(0o600))
		locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
		if err != nil {
			_ = fl.Close()
			return fmt.Errorf("jsonlstore: acquire lineage partition lock: %w", err)
		}
		if !locked {
			_ = fl.Close()
			return fmt.Errorf("jsonlstore: acquire lineage partition lock: lock not acquired")
		}
		locks = append(locks, fl)
	}
	return fn()
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func validateLineageRecord(row port.SessionLineageRecord) error {
	if row.ID == "" || !session.IncarnationID(row.Incarnation).Valid() ||
		(row.State != port.SessionLineageRetained && row.State != port.SessionLineagePruned) ||
		session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil ||
		(row.State == port.SessionLineageRetained && !row.DeletedAt.IsZero()) ||
		(row.State == port.SessionLineagePruned && row.DeletedAt.IsZero()) {
		return errors.New("jsonlstore: corrupt lineage partition")
	}
	return nil
}

//nolint:gocyclo // Validation remains fail-closed at each decode boundary.
func readLineagePartition(
	path, format string,
	limit int,
	checkDirty bool,
	accept func(port.SessionLineageRecord) bool,
	less func(port.SessionLineageRecord, port.SessionLineageRecord) bool,
) ([]port.SessionLineageRecord, bool, error) {
	if checkDirty {
		_, err := os.Stat(lineageDirtyPath(path)) //nolint:gosec // adapter-private owner-only path
		if err == nil {
			return nil, false, errLineagePartitionIncomplete
		} else if !os.IsNotExist(err) {
			return nil, false, fmt.Errorf("jsonlstore: inspect lineage partition: %w", err)
		}
	}
	f, err := os.Open(path) //nolint:gosec // adapter-private owner-only path
	if os.IsNotExist(err) {
		return nil, false, errLineagePartitionUnavailable
	}
	if err != nil {
		return nil, false, fmt.Errorf("jsonlstore: open lineage partition: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(bufio.NewReader(f))
	var header lineageHeader
	if err := dec.Decode(&header); err != nil || header.Format != format {
		return nil, false, errors.New("jsonlstore: corrupt lineage partition")
	}
	rows := make([]port.SessionLineageRecord, 0, min(limit, 8))
	var previous port.SessionLineageRecord
	havePrevious := false
	validate := func(row port.SessionLineageRecord) bool {
		if validateLineageRecord(row) != nil || accept != nil && !accept(row) || havePrevious && less != nil && !less(previous, row) {
			return false
		}
		previous = row
		havePrevious = true
		return true
	}
	for len(rows) < limit {
		var row port.SessionLineageRecord
		if err := dec.Decode(&row); errors.Is(err, io.EOF) {
			return rows, false, nil
		} else if err != nil || !validate(row) {
			return nil, false, errors.New("jsonlstore: corrupt lineage partition")
		}
		rows = append(rows, row)
	}
	var sentinel port.SessionLineageRecord
	if err := dec.Decode(&sentinel); errors.Is(err, io.EOF) {
		return rows, false, nil
	} else if err != nil || !validate(sentinel) {
		return nil, false, errors.New("jsonlstore: corrupt lineage partition")
	}
	return rows, true, nil
}

func readWholeLineagePartition(path, format string) ([]port.SessionLineageRecord, error) {
	rows, _, err := readLineagePartition(path, format, int(^uint(0)>>1)-1, false, nil, nil)
	if errors.Is(err, errLineagePartitionUnavailable) {
		return nil, nil
	}
	return rows, err
}

func (st *Store) writeLineagePartition(path, format string, rows []port.SessionLineageRecord) error {
	if st.lineagePartitionWriteObserver != nil {
		st.lineagePartitionWriteObserver(path)
	}
	var data strings.Builder
	enc := json.NewEncoder(&data)
	if err := enc.Encode(lineageHeader{Format: format}); err != nil {
		return err
	}
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("jsonlstore: encode lineage partition: %w", err)
		}
	}
	return st.writeInventoryFileWithPattern(path, []byte(data.String()), ".session-lineage-partition-*")
}

func lineageResultLess(a, b port.SessionLineageRecord, root session.SessionID) bool {
	if (a.ID == root) != (b.ID == root) {
		return a.ID == root
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	if a.State != b.State {
		return a.State == port.SessionLineageRetained
	}
	return a.Incarnation < b.Incarnation
}

func retainedLineageRecord(s *session.Session) port.SessionLineageRecord {
	return port.SessionLineageRecord{ID: s.ID, Kind: s.Kind, Relationship: s.Relationship, OwnerScope: session.PrincipalScopeHash(s.Owner), Incarnation: string(s.Incarnation()), State: port.SessionLineageRetained}
}

func lineageSubjects(row port.SessionLineageRecord) [][2]string {
	rel := row.Relationship
	var subjects [][2]string
	if rel.ParentSessionID != "" {
		subjects = append(subjects, [2]string{string(rel.ParentSessionID), string(rel.ParentIncarnation)})
	}
	if rel.OriginSessionID != "" {
		subjects = append(subjects, [2]string{string(rel.OriginSessionID), string(rel.OriginIncarnation)})
	}
	if rel.DebugTargetID != "" {
		subjects = append(subjects, [2]string{string(rel.DebugTargetID), string(rel.DebugTargetIncarnation)})
	}
	return subjects
}

func lineageReferences(row port.SessionLineageRecord, id session.SessionID, incarnation session.IncarnationID) bool {
	for _, subject := range lineageSubjects(row) {
		if subject[0] == string(id) && subject[1] == string(incarnation) {
			return true
		}
	}
	return false
}

func (st *Store) lineageAffectedPaths(record port.SessionLineageRecord, old []port.SessionLineageRecord) []string {
	paths := []string{st.lineageRecordPath(record.ID), st.lineageEdgePath(record.ID, session.IncarnationID(record.Incarnation))}
	for _, row := range old {
		for _, subject := range lineageSubjects(row) {
			parent, incarnation := session.SessionID(subject[0]), session.IncarnationID(subject[1])
			paths = append(paths, st.lineageEdgePath(parent, incarnation), st.lineagePointPath(parent, incarnation, row.ID, row.Incarnation))
		}
	}
	for _, subject := range lineageSubjects(record) {
		parent, incarnation := session.SessionID(subject[0]), session.IncarnationID(subject[1])
		paths = append(paths, st.lineageEdgePath(parent, incarnation), st.lineagePointPath(parent, incarnation, record.ID, record.Incarnation))
	}
	return paths
}

type lineageDirtyManifest struct {
	Format string                      `json:"v"`
	ID     session.SessionID           `json:"id"`
	Paths  []string                    `json:"paths"`
	Old    []port.SessionLineageRecord `json:"old"`
}

const lineageDirtyFormat = "session-lineage-dirty-json/1"

func (st *Store) markLineageDirty(id session.SessionID, recordPath string, old []port.SessionLineageRecord, paths []string) error {
	paths = compactStringsSorted(paths)
	manifest := lineageDirtyManifest{Format: lineageDirtyFormat, ID: id, Old: old, Paths: make([]string, 0, len(paths))}
	for _, path := range paths {
		manifest.Paths = append(manifest.Paths, filepath.Base(path))
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("jsonlstore: encode lineage dirty manifest: %w", err)
	}
	if err := st.writeInventoryFileWithPattern(lineageDirtyPath(recordPath), append(data, '\n'), ".session-lineage-dirty-*"); err != nil {
		return err
	}
	for _, path := range paths {
		if path == recordPath {
			continue
		}
		if err := st.writeInventoryFileWithPattern(lineageDirtyPath(path), []byte("incomplete\n"), ".session-lineage-dirty-*"); err != nil {
			return err
		}
	}
	return nil
}

func compactStringsSorted(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	return compactStrings(values)
}

func (st *Store) clearLineageDirty(paths []string) error {
	root, err := st.inventoryCatalogRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, path := range compactStringsSorted(paths) {
		name, err := st.inventoryCatalogRelativePath(lineageDirtyPath(path))
		if err != nil {
			return err
		}
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("jsonlstore: clear lineage dirty marker: %w", err)
		}
	}
	dir, err := os.Open(st.inventoryCatalogDir()) //nolint:gosec // adapter-private owner-only path
	if err != nil {
		return fmt.Errorf("jsonlstore: open lineage marker directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("jsonlstore: sync cleared lineage markers: %w", err)
	}
	return nil
}

func upsertLineageRow(rows []port.SessionLineageRecord, record port.SessionLineageRecord) []port.SessionLineageRecord {
	for i := range rows {
		if rows[i].ID == record.ID && rows[i].Incarnation == record.Incarnation {
			rows[i] = record
			return rows
		}
	}
	return append(rows, record)
}

func (st *Store) publishLineageRecord(record port.SessionLineageRecord, old []port.SessionLineageRecord, affectedPaths []string) error {
	now := time.Now().UTC()
	recordRows := append([]port.SessionLineageRecord(nil), old...)
	for i := range recordRows {
		if recordRows[i].State == port.SessionLineageRetained && recordRows[i].Incarnation != record.Incarnation {
			recordRows[i].State = port.SessionLineagePruned
			recordRows[i].DeletedAt = now
		}
	}
	recordRows = upsertLineageRow(recordRows, record)
	sort.Slice(recordRows, func(i, j int) bool { return lineageResultLess(recordRows[i], recordRows[j], record.ID) })
	if err := st.writeLineagePartition(st.lineageRecordPath(record.ID), lineageRecordFormat, recordRows); err != nil {
		return err
	}

	for _, path := range compactStringsSorted(affectedPaths) {
		if path == st.lineageRecordPath(record.ID) {
			continue
		}
		if strings.HasPrefix(filepath.Base(path), ".session-lineage-point-") {
			points := make([]port.SessionLineageRecord, 0, 1)
			for _, row := range recordRows {
				for _, subject := range lineageSubjects(row) {
					if path == st.lineagePointPath(session.SessionID(subject[0]), session.IncarnationID(subject[1]), row.ID, row.Incarnation) {
						points = append(points, row)
						break
					}
				}
			}
			if err := st.writeLineagePartition(path, lineagePointFormat, points); err != nil {
				return err
			}
			continue
		}
		edges, err := readWholeLineagePartition(path, lineageEdgeFormat)
		if err != nil {
			return err
		}
		filtered := make([]port.SessionLineageRecord, 0, len(edges))
		for _, edge := range edges {
			if edge.ID != record.ID {
				filtered = append(filtered, edge)
			}
		}
		for _, row := range recordRows {
			for _, subject := range lineageSubjects(row) {
				if path == st.lineageEdgePath(session.SessionID(subject[0]), session.IncarnationID(subject[1])) {
					filtered = upsertLineageRow(filtered, row)
					break
				}
			}
		}
		sort.Slice(filtered, func(i, j int) bool { return lineageResultLess(filtered[i], filtered[j], "") })
		if reflect.DeepEqual(edges, filtered) {
			continue
		}
		if err := st.writeLineagePartition(path, lineageEdgeFormat, filtered); err != nil {
			return err
		}
	}
	return nil
}

func (st *Store) readLineageDirtyManifest(recordPath string, id session.SessionID) (lineageDirtyManifest, bool, error) {
	data, err := os.ReadFile(lineageDirtyPath(recordPath)) //nolint:gosec // adapter-private owner-only path
	if os.IsNotExist(err) {
		return lineageDirtyManifest{}, false, nil
	}
	if err != nil {
		return lineageDirtyManifest{}, false, fmt.Errorf("jsonlstore: read lineage dirty manifest: %w", err)
	}
	var manifest lineageDirtyManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Format != lineageDirtyFormat || manifest.ID != id || len(manifest.Paths) == 0 {
		return lineageDirtyManifest{}, true, errors.New("jsonlstore: corrupt lineage dirty manifest")
	}
	catalog := st.inventoryCatalogDir()
	seenRecord := false
	for i, name := range manifest.Paths {
		if filepath.Base(name) != name || (!strings.HasPrefix(name, ".session-lineage-record-") && !strings.HasPrefix(name, ".session-lineage-edge-") && !strings.HasPrefix(name, ".session-lineage-point-")) || !strings.HasSuffix(name, ".jsonl") {
			return lineageDirtyManifest{}, true, errors.New("jsonlstore: corrupt lineage dirty manifest path")
		}
		manifest.Paths[i] = filepath.Join(catalog, name)
		seenRecord = seenRecord || manifest.Paths[i] == recordPath
	}
	if !seenRecord {
		return lineageDirtyManifest{}, true, errors.New("jsonlstore: lineage dirty manifest omits record partition")
	}
	for _, row := range manifest.Old {
		if row.ID != id || validateLineageRecord(row) != nil {
			return lineageDirtyManifest{}, true, errors.New("jsonlstore: corrupt lineage dirty manifest record")
		}
	}
	return manifest, true, nil
}

func (st *Store) recoverDirtyLineage(ctx context.Context, id session.SessionID) error {
	recordPath := st.lineageRecordPath(id)
	manifest, dirty, err := st.readLineageDirtyManifest(recordPath, id)
	if err != nil || !dirty {
		return err
	}
	return st.withLineagePartitionLocks(ctx, manifest.Paths, func() error {
		// Re-read under the partition locks: another process may have completed recovery.
		manifest, dirty, err = st.readLineageDirtyManifest(recordPath, id)
		if err != nil || !dirty {
			return err
		}
		current, present, err := st.resolver.currentSnapshotFor(id)
		if err != nil {
			return fmt.Errorf("jsonlstore: recover lineage from current snapshot: %w", err)
		}
		var recovered port.SessionLineageRecord
		if present {
			sess, err := sessnap.Unmarshal(current.Snapshot)
			if err != nil {
				return fmt.Errorf("jsonlstore: recover lineage snapshot: %w", err)
			}
			recovered = retainedLineageRecord(sess)
		} else {
			found := false
			for _, row := range manifest.Old {
				if row.State == port.SessionLineageRetained {
					if found {
						return errors.New("jsonlstore: cannot prove deleted lineage incarnation")
					}
					recovered = row
					found = true
				}
			}
			if !found {
				return errors.New("jsonlstore: cannot prove deleted lineage record")
			}
			recovered.State = port.SessionLineagePruned
			recovered.DeletedAt = time.Now().UTC()
		}
		covered := make(map[string]struct{}, len(manifest.Paths))
		for _, path := range manifest.Paths {
			covered[path] = struct{}{}
		}
		for _, path := range st.lineageAffectedPaths(recovered, manifest.Old) {
			if _, ok := covered[path]; !ok {
				return errors.New("jsonlstore: lineage dirty manifest does not cover recovered snapshot")
			}
		}
		if err := st.publishLineageRecord(recovered, manifest.Old, manifest.Paths); err != nil {
			return err
		}
		legacy, err := st.legacyLineageUncertain()
		if err != nil {
			return err
		}
		if legacy {
			return errLineagePartitionIncomplete
		}
		return st.clearLineageDirty(manifest.Paths)
	})
}

func (st *Store) mutateLineage(ctx context.Context, record port.SessionLineageRecord, publishSnapshot func() error) error {
	if err := st.recoverDirtyLineage(ctx, record.ID); err != nil {
		return err
	}
	legacyUncertain, err := st.legacyLineageUncertain()
	if err != nil {
		return err
	}
	recordPath := st.lineageRecordPath(record.ID)
	old, err := readWholeLineagePartition(recordPath, lineageRecordFormat)
	if err != nil {
		return err
	}
	paths := st.lineageAffectedPaths(record, old)
	return st.withLineagePartitionLocks(ctx, paths, func() error {
		if dirty, err := anyLineageDirty(paths); err != nil {
			return err
		} else if dirty {
			return errLineagePartitionIncomplete
		}
		if err := st.markLineageDirty(record.ID, recordPath, old, paths); err != nil {
			return err
		}
		if err := publishSnapshot(); err != nil {
			return err
		}
		if err := st.publishLineageRecord(record, old, paths); err != nil {
			return err
		}
		if legacyUncertain {
			return nil
		}
		return st.clearLineageDirty(paths)
	})
}

func anyLineageDirty(paths []string) (bool, error) {
	for _, path := range compactStringsSorted(paths) {
		_, err := os.Stat(lineageDirtyPath(path)) //nolint:gosec // adapter-private owner-only path
		if err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func (st *Store) pruneLineage(ctx context.Context, id session.SessionID, deleteSnapshot func() error) error {
	if err := st.recoverDirtyLineage(ctx, id); err != nil {
		return err
	}
	legacyUncertain, err := st.legacyLineageUncertain()
	if err != nil {
		return err
	}
	recordPath := st.lineageRecordPath(id)
	old, err := readWholeLineagePartition(recordPath, lineageRecordFormat)
	if err != nil {
		return err
	}
	var retained *port.SessionLineageRecord
	for i := range old {
		if old[i].State == port.SessionLineageRetained {
			if retained != nil {
				return errors.New("jsonlstore: multiple retained lineage incarnations")
			}
			row := old[i]
			retained = &row
		}
	}
	if retained == nil {
		if len(old) == 0 {
			return deleteSnapshot()
		}
		current, present, err := st.resolver.currentSnapshotFor(id)
		if err != nil {
			return fmt.Errorf("jsonlstore: reconstruct lineage before delete: %w", err)
		}
		if !present {
			return deleteSnapshot()
		}
		sess, err := sessnap.Unmarshal(current.Snapshot)
		if err != nil {
			return fmt.Errorf("jsonlstore: reconstruct lineage snapshot before delete: %w", err)
		}
		row := retainedLineageRecord(sess)
		retained = &row
	}
	paths := st.lineageAffectedPaths(*retained, old)
	return st.withLineagePartitionLocks(ctx, paths, func() error {
		if dirty, err := anyLineageDirty(paths); err != nil {
			return err
		} else if dirty {
			return errLineagePartitionIncomplete
		}
		if err := st.markLineageDirty(id, recordPath, old, paths); err != nil {
			return err
		}
		retained.State = port.SessionLineagePruned
		retained.DeletedAt = time.Now().UTC()
		if err := st.publishLineageRecord(*retained, old, paths); err != nil {
			return err
		}
		if err := deleteSnapshot(); err != nil {
			return err
		}
		if legacyUncertain {
			return nil
		}
		return st.clearLineageDirty(paths)
	})
}

type legacyLineageIndex struct {
	Format  string                      `json:"v"`
	Records []port.SessionLineageRecord `json:"records"`
}

// MigrateLegacyLineage is an explicit, bounded maintenance operation for the
// pre-partition global index. Callers must quiesce session mutations while it
// runs. maxWork bounds legacy records plus v2 partition files and rows examined;
// exceeding it leaves the legacy index in place and targeted queries fail closed.
//
//nolint:gocyclo // Explicit migration validates every legacy and v2 partition boundary.
func (st *Store) MigrateLegacyLineage(ctx context.Context, maxWork int) error {
	if maxWork <= 0 {
		return errors.New("jsonlstore: lineage migration max work must be positive")
	}
	legacyPath := st.legacyLineageIndexPath()
	info, err := os.Stat(legacyPath) //nolint:gosec // adapter-private owner-only path
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("jsonlstore: inspect legacy lineage index: %w", err)
	}
	if info.Size() > maxScannerTokenSize {
		return fmt.Errorf("jsonlstore: legacy lineage index exceeds %d bytes", maxScannerTokenSize)
	}
	data, err := os.ReadFile(legacyPath) //nolint:gosec // explicit adapter-private maintenance input
	if err != nil {
		return fmt.Errorf("jsonlstore: read legacy lineage index: %w", err)
	}
	var legacy legacyLineageIndex
	if err := json.Unmarshal(data, &legacy); err != nil || legacy.Format != legacyLineageIndexFormat {
		return errors.New("jsonlstore: corrupt legacy lineage index")
	}
	work := len(legacy.Records)
	if work > maxWork {
		return fmt.Errorf("jsonlstore: lineage migration exceeds max work %d", maxWork)
	}
	if err := st.writeInventoryFileWithPattern(st.lineageMigrationPath(), []byte("incomplete\n"), ".session-lineage-migration-*"); err != nil {
		return fmt.Errorf("jsonlstore: publish lineage migration marker: %w", err)
	}
	byID := make(map[session.SessionID][]port.SessionLineageRecord)
	for _, row := range legacy.Records {
		if err := validateLineageRecord(row); err != nil {
			return errors.New("jsonlstore: corrupt legacy lineage index record")
		}
		for _, existing := range byID[row.ID] {
			if existing.Incarnation == row.Incarnation {
				return errors.New("jsonlstore: duplicate legacy lineage incarnation")
			}
		}
		byID[row.ID] = append(byID[row.ID], row)
	}

	entries, err := os.ReadDir(st.inventoryCatalogDir())
	if err != nil {
		return fmt.Errorf("jsonlstore: scan lineage partitions for migration: %w", err)
	}
	// First settle interrupted v2 mutations from their exact snapshots. The
	// legacy index intentionally keeps their markers dirty until final publish.
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".session-lineage-record-") || !strings.HasSuffix(name, ".jsonl.dirty") {
			continue
		}
		work++
		if work > maxWork {
			return fmt.Errorf("jsonlstore: lineage migration exceeds max work %d", maxWork)
		}
		body, err := os.ReadFile(filepath.Join(st.inventoryCatalogDir(), name)) //nolint:gosec // adapter-private maintenance file
		if err != nil {
			return err
		}
		var manifest lineageDirtyManifest
		if err := json.Unmarshal(body, &manifest); err != nil || manifest.ID == "" {
			return errors.New("jsonlstore: corrupt lineage dirty manifest during migration")
		}
		if err := st.recoverDirtyLineage(ctx, manifest.ID); err != nil && !errors.Is(err, errLineagePartitionIncomplete) {
			return err
		}
	}

	entries, err = os.ReadDir(st.inventoryCatalogDir())
	if err != nil {
		return fmt.Errorf("jsonlstore: rescan lineage partitions for migration: %w", err)
	}
	var edgePaths, pointPaths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := filepath.Join(st.inventoryCatalogDir(), name)
		switch {
		case strings.HasPrefix(name, ".session-lineage-record-"):
			rows, err := readWholeLineagePartition(path, lineageRecordFormat)
			if err != nil {
				return err
			}
			work += 1 + len(rows)
			if work > maxWork {
				return fmt.Errorf("jsonlstore: lineage migration exceeds max work %d", maxWork)
			}
			for _, row := range rows {
				byID[row.ID] = upsertLineageRow(byID[row.ID], row)
			}
		case strings.HasPrefix(name, ".session-lineage-edge-"):
			work++
			if work > maxWork {
				return fmt.Errorf("jsonlstore: lineage migration exceeds max work %d", maxWork)
			}
			edgePaths = append(edgePaths, path)
		case strings.HasPrefix(name, ".session-lineage-point-"):
			work++
			if work > maxWork {
				return fmt.Errorf("jsonlstore: lineage migration exceeds max work %d", maxWork)
			}
			pointPaths = append(pointPaths, path)
		}
	}

	var paths []string
	desiredEdges := make(map[string][]port.SessionLineageRecord)
	desiredPoints := make(map[string][]port.SessionLineageRecord)
	for id, rows := range byID {
		retained := 0
		for _, row := range rows {
			if row.State == port.SessionLineageRetained {
				retained++
			}
			paths = append(paths, st.lineageEdgePath(row.ID, session.IncarnationID(row.Incarnation)))
			for _, subject := range lineageSubjects(row) {
				parent, incarnation := session.SessionID(subject[0]), session.IncarnationID(subject[1])
				path := st.lineageEdgePath(parent, incarnation)
				desiredEdges[path] = upsertLineageRow(desiredEdges[path], row)
				paths = append(paths, path)
				pointPath := st.lineagePointPath(parent, incarnation, row.ID, row.Incarnation)
				desiredPoints[pointPath] = []port.SessionLineageRecord{row}
				paths = append(paths, pointPath)
			}
		}
		if retained > 1 {
			return fmt.Errorf("jsonlstore: multiple retained lineage incarnations for %q", id)
		}
		paths = append(paths, st.lineageRecordPath(id))
	}
	paths = append(paths, edgePaths...)
	paths = append(paths, pointPaths...)
	paths = compactStringsSorted(paths)
	return st.withLineagePartitionLocks(ctx, paths, func() error {
		for id, rows := range byID {
			sort.Slice(rows, func(i, j int) bool { return lineageResultLess(rows[i], rows[j], id) })
			if err := st.writeLineagePartition(st.lineageRecordPath(id), lineageRecordFormat, rows); err != nil {
				return err
			}
		}
		for _, path := range paths {
			switch {
			case strings.HasPrefix(filepath.Base(path), ".session-lineage-edge-"):
				rows := desiredEdges[path]
				sort.Slice(rows, func(i, j int) bool { return lineageResultLess(rows[i], rows[j], "") })
				if err := st.writeLineagePartition(path, lineageEdgeFormat, rows); err != nil {
					return err
				}
			case strings.HasPrefix(filepath.Base(path), ".session-lineage-point-"):
				if err := st.writeLineagePartition(path, lineagePointFormat, desiredPoints[path]); err != nil {
					return err
				}
			}
		}
		if err := st.clearLineageDirty(paths); err != nil {
			return err
		}
		root, err := st.inventoryCatalogRoot()
		if err != nil {
			return err
		}
		for _, path := range []string{st.lineageMigrationPath(), legacyPath} {
			name, nameErr := st.inventoryCatalogRelativePath(path)
			if nameErr != nil {
				err = nameErr
				break
			}
			if removeErr := root.Remove(name); removeErr != nil && !os.IsNotExist(removeErr) {
				err = removeErr
				break
			}
		}
		_ = root.Close()
		if err != nil {
			return fmt.Errorf("jsonlstore: finalize legacy lineage migration: %w", err)
		}
		dir, err := os.Open(st.inventoryCatalogDir()) //nolint:gosec // adapter-private owner-only path
		if err != nil {
			return err
		}
		err = dir.Sync()
		_ = dir.Close()
		return err
	})
}

func (st *Store) missingLineagePoint(query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	rows, _, err := readLineagePartition(
		st.lineageRecordPath(query.RecordID), lineageRecordFormat, 1, true,
		func(row port.SessionLineageRecord) bool { return row.ID == query.RecordID },
		func(a, b port.SessionLineageRecord) bool { return lineageResultLess(a, b, query.RecordID) },
	)
	if errors.Is(err, errLineagePartitionUnavailable) {
		return port.SessionLineageResult{}, nil
	}
	if err != nil {
		return port.SessionLineageResult{}, err
	}
	if len(rows) == 1 && rows[0].Incarnation == string(query.RecordIncarnation) && lineageReferences(rows[0], query.RootID, query.RootIncarnation) {
		return port.SessionLineageResult{}, errLineagePartitionIncomplete
	}
	return port.SessionLineageResult{}, nil
}

// ReadSessionLineage reads only the selected root record and exact-incarnation
// direct-edge partitions. It never locks, reconciles, or writes.
func (st *Store) ReadSessionLineage(_ context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	if err := port.ValidateSessionLineageQuery(query); err != nil {
		return port.SessionLineageResult{}, err
	}
	if _, err := os.Stat(st.lineageMigrationPath()); err == nil {
		return port.SessionLineageResult{}, errLineagePartitionIncomplete
	} else if !os.IsNotExist(err) {
		return port.SessionLineageResult{}, fmt.Errorf("jsonlstore: inspect lineage migration state: %w", err)
	}
	if query.RecordID != "" {
		path := st.lineagePointPath(query.RootID, query.RootIncarnation, query.RecordID, string(query.RecordIncarnation))
		rows, more, err := readLineagePartition(path, lineagePointFormat, 1, true,
			func(row port.SessionLineageRecord) bool {
				return row.ID == query.RecordID && row.Incarnation == string(query.RecordIncarnation) && lineageReferences(row, query.RootID, query.RootIncarnation)
			}, nil)
		if errors.Is(err, errLineagePartitionUnavailable) {
			return st.missingLineagePoint(query)
		}
		if err != nil {
			return port.SessionLineageResult{}, err
		}
		if more || len(rows) != 1 {
			return st.missingLineagePoint(query)
		}
		return port.SessionLineageResult{Records: rows}, nil
	}
	// The root and edge partitions have distinct ordering contracts.
	records, moreRecords, err := readLineagePartition(
		st.lineageRecordPath(query.RootID), lineageRecordFormat, query.Limit, true,
		func(row port.SessionLineageRecord) bool { return row.ID == query.RootID },
		func(a, b port.SessionLineageRecord) bool { return lineageResultLess(a, b, query.RootID) },
	)
	if err != nil {
		return port.SessionLineageResult{}, err
	}
	rows := records
	more := moreRecords
	if !more {
		remaining := query.Limit - len(rows)
		edges, moreEdges, err := readLineagePartition(
			st.lineageEdgePath(query.RootID, query.RootIncarnation), lineageEdgeFormat, remaining, true,
			func(row port.SessionLineageRecord) bool {
				return lineageReferences(row, query.RootID, query.RootIncarnation)
			},
			func(a, b port.SessionLineageRecord) bool { return lineageResultLess(a, b, "") },
		)
		if err != nil {
			return port.SessionLineageResult{}, err
		}
		rows = append(rows, edges...)
		more = moreEdges
	}
	if len(rows) > query.Limit {
		rows = rows[:query.Limit]
		more = true
	}
	return port.SessionLineageResult{Records: rows, Truncated: more}, nil
}
