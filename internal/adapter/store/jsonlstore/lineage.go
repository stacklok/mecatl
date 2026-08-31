package jsonlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const lineageIndexFormat = "session-lineage-json/1"

type lineageIndex struct {
	Format  string                      `json:"v"`
	Records []port.SessionLineageRecord `json:"records"`
}

func (st *Store) lineageIndexPath() string {
	return filepath.Join(st.inventoryCatalogDir(), ".session-lineage.json")
}

func (st *Store) withLineageLock(ctx context.Context, fn func() error) error {
	st.lineageMu.Lock()
	defer st.lineageMu.Unlock()
	fl := flock.New(filepath.Join(st.inventoryCatalogDir(), ".session-lineage.lock"), flock.SetPermissions(0o600))
	locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("jsonlstore: acquire lineage lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("jsonlstore: acquire lineage lock: lock not acquired")
	}
	defer func() { _ = fl.Close() }()
	return fn()
}

func readLineageIndex(path string) (map[string]port.SessionLineageRecord, error) {
	data, err := os.ReadFile(path) //nolint:gosec // adapter-private owner-only path
	if os.IsNotExist(err) {
		return make(map[string]port.SessionLineageRecord), nil
	}
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: read lineage index: %w", err)
	}
	var index lineageIndex
	if err := json.Unmarshal(data, &index); err != nil || index.Format != lineageIndexFormat {
		return nil, fmt.Errorf("jsonlstore: corrupt lineage index")
	}
	rows := make(map[string]port.SessionLineageRecord, len(index.Records))
	for _, row := range index.Records {
		if row.ID == "" || !session.IncarnationID(row.Incarnation).Valid() || (row.State != port.SessionLineageRetained && row.State != port.SessionLineagePruned) ||
			session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil || (row.State == port.SessionLineageRetained && !row.DeletedAt.IsZero()) ||
			(row.State == port.SessionLineagePruned && row.DeletedAt.IsZero()) {
			return nil, fmt.Errorf("jsonlstore: corrupt lineage index")
		}
		key := lineageRecordKey(row.ID, row.Incarnation)
		if _, exists := rows[key]; exists {
			return nil, fmt.Errorf("jsonlstore: corrupt lineage index: duplicate incarnation")
		}
		rows[key] = row
	}
	return rows, nil
}

func lineageRecordKey(id session.SessionID, incarnation string) string {
	return string(id) + "\x00" + incarnation
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

func (st *Store) writeLineageRows(rows map[string]port.SessionLineageRecord) error {
	ordered := make([]port.SessionLineageRecord, 0, len(rows))
	for _, row := range rows {
		ordered = append(ordered, row)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Incarnation < ordered[j].Incarnation
	})
	data, err := json.Marshal(lineageIndex{Format: lineageIndexFormat, Records: ordered})
	if err != nil {
		return fmt.Errorf("jsonlstore: encode lineage index: %w", err)
	}
	return st.writeInventoryFileWithPattern(st.lineageIndexPath(), data, ".session-lineage-*")
}

func (st *Store) updateLineageLocked(record port.SessionLineageRecord) error {
	rows, err := readLineageIndex(st.lineageIndexPath())
	if err != nil {
		return err
	}
	for key, old := range rows {
		if old.ID == record.ID && old.State == port.SessionLineageRetained && old.Incarnation != record.Incarnation {
			old.State = port.SessionLineagePruned
			old.DeletedAt = time.Now().UTC()
			rows[key] = old
		}
	}
	rows[lineageRecordKey(record.ID, record.Incarnation)] = record
	return st.writeLineageRows(rows)
}

func retainedLineageRecord(s *session.Session) port.SessionLineageRecord {
	return port.SessionLineageRecord{ID: s.ID, Kind: s.Kind, Relationship: s.Relationship, OwnerScope: session.PrincipalScopeHash(s.Owner), Incarnation: string(s.Incarnation()), State: port.SessionLineageRetained}
}

func (st *Store) pruneLineageLocked(id session.SessionID) error {
	rows, err := readLineageIndex(st.lineageIndexPath())
	if err != nil {
		return err
	}
	if _, err := st.reconcileLineageLocked(rows); err != nil {
		return err
	}
	var key string
	var row port.SessionLineageRecord
	for candidateKey, candidate := range rows {
		if candidate.ID == id && candidate.State == port.SessionLineageRetained {
			key, row = candidateKey, candidate
			break
		}
	}
	if key == "" {
		return nil
	}
	row.State = port.SessionLineagePruned
	row.DeletedAt = time.Now().UTC()
	rows[key] = row
	return st.writeLineageRows(rows)
}

func (st *Store) reconcileLineageLocked(rows map[string]port.SessionLineageRecord) (bool, error) {
	inventory, err := st.rebuildInventoryRows()
	if err != nil {
		return false, err
	}
	present := make(map[session.SessionID]string, len(inventory))
	changed := false
	for _, meta := range inventory {
		current, ok, readErr := st.resolver.currentSnapshotFor(meta.ID)
		if readErr != nil || !ok {
			// Legacy snapshots have no crash window with this index; retain their row.
			continue
		}
		sess, decodeErr := sessnap.Unmarshal(current.Snapshot)
		if decodeErr != nil {
			return false, decodeErr
		}
		fresh := retainedLineageRecord(sess)
		present[meta.ID] = fresh.Incarnation
		key := lineageRecordKey(fresh.ID, fresh.Incarnation)
		if old, exists := rows[key]; !exists {
			rows[key] = fresh
			changed = true
		} else if old.State == port.SessionLineageRetained && !reflect.DeepEqual(old, fresh) {
			rows[key] = fresh
			changed = true
		}
	}
	for key, row := range rows {
		if currentIncarnation, ok := present[row.ID]; row.State == port.SessionLineageRetained && (!ok || currentIncarnation != row.Incarnation) {
			row.State = port.SessionLineagePruned
			row.DeletedAt = time.Now().UTC()
			rows[key] = row
			changed = true
		}
	}
	return changed, nil
}

// ReadSessionLineage returns deterministic direct edges and repairs index/snapshot
// crash boundaries before answering.
func (st *Store) ReadSessionLineage(ctx context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	if err := port.ValidateSessionLineageQuery(query); err != nil {
		return port.SessionLineageResult{}, err
	}
	var result port.SessionLineageResult
	err := st.withLineageLock(ctx, func() error {
		rows, err := readLineageIndex(st.lineageIndexPath())
		if err != nil {
			return err
		}
		changed, err := st.reconcileLineageLocked(rows)
		if err != nil {
			return fmt.Errorf("jsonlstore: reconcile lineage: %w", err)
		}
		if changed {
			if err := st.writeLineageRows(rows); err != nil {
				return err
			}
		}
		for _, row := range rows {
			rel := row.Relationship
			if row.ID == query.RootID ||
				rel.ParentSessionID == query.RootID && rel.ParentIncarnation == query.RootIncarnation ||
				rel.OriginSessionID == query.RootID && rel.OriginIncarnation == query.RootIncarnation ||
				rel.DebugTargetID == query.RootID && rel.DebugTargetIncarnation == query.RootIncarnation {
				result.Records = append(result.Records, row)
			}
		}
		sort.Slice(result.Records, func(i, j int) bool {
			return lineageResultLess(result.Records[i], result.Records[j], query.RootID)
		})
		if len(result.Records) > query.Limit {
			result.Records = result.Records[:query.Limit]
			result.Truncated = true
		}
		return nil
	})
	return result, err
}
