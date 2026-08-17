package jsonlstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	inventoryCatalogFormat   = "session-inventory-json/1"
	inventoryCatalogFileName = ".session-inventory.json"
)

type inventoryCatalog struct {
	Format      string                      `json:"v"`
	Fingerprint string                      `json:"fingerprint"`
	Rows        []port.SessionDiscoveryMeta `json:"rows"`
}

func (st *Store) inventoryCatalogPath() string {
	return filepath.Join(st.resolver.canonicalDir(), inventoryCatalogFileName)
}

// inventoryFingerprint observes only directory-entry metadata for authoritative
// snapshot files. It deliberately excludes sidecars, locks, temporaries, and the
// derivative catalog itself. A ready catalog read therefore never opens or
// decodes a transcript while still noticing ordinary same-directory changes made
// by another Store process (atomic replacement, creation, removal, or promotion).
func (st *Store) inventoryFingerprint() (string, error) {
	var records []string
	for _, candidate := range []struct {
		dir       string
		prefix    string
		canonical bool
	}{
		{dir: st.resolver.dir, prefix: "legacy/"},
		{dir: st.resolver.canonicalDir(), prefix: "canonical/", canonical: true},
	} {
		entries, err := os.ReadDir(candidate.dir)
		if err != nil {
			return "", fmt.Errorf("jsonlstore: fingerprint inventory directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !inventorySnapshotName(entry.Name(), candidate.canonical) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return "", fmt.Errorf("jsonlstore: fingerprint inventory entry: %w", err)
			}
			records = append(records, fmt.Sprintf("%s%s\x00%d\x00%d\x00%d", candidate.prefix, entry.Name(), info.Size(), info.ModTime().UnixNano(), info.Mode()))
		}
	}
	sort.Strings(records)
	h := sha256.New()
	for _, record := range records {
		_, _ = h.Write([]byte(record))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func inventorySnapshotName(name string, canonical bool) bool {
	if strings.HasSuffix(name, sessionFileSuffix) {
		return true
	}
	return canonical && strings.HasSuffix(name, currentSnapshotSuffix)
}

func (st *Store) readInventoryCatalog(fingerprint string) ([]port.SessionDiscoveryMeta, bool) {
	data, err := os.ReadFile(st.inventoryCatalogPath()) //nolint:gosec // adapter-private owner-only path
	if err != nil {
		return nil, false
	}
	var catalog inventoryCatalog
	if json.Unmarshal(data, &catalog) != nil || catalog.Format != inventoryCatalogFormat || catalog.Fingerprint != fingerprint || !validInventoryRows(catalog.Rows) {
		return nil, false
	}
	return append([]port.SessionDiscoveryMeta(nil), catalog.Rows...), true
}

func validInventoryRows(rows []port.SessionDiscoveryMeta) bool {
	seen := make(map[session.SessionID]struct{}, len(rows))
	for _, row := range rows {
		if row.ID == "" || validateSessionID(row.ID) != nil || row.ModifiedAt.IsZero() {
			return false
		}
		if _, duplicate := seen[row.ID]; duplicate {
			return false
		}
		seen[row.ID] = struct{}{}
		if row.State == "" {
			if row.Turns != 0 || row.ModelID != "" || !row.CreatedAt.IsZero() || row.Title != "" ||
				row.TitleProvenance != "" || row.Owner != nil || row.Workspace != "" || row.Kind != "" ||
				row.Relationship != (session.SessionRelationship{}) {
				return false
			}
			continue
		}
		if !knownStates[row.State] || session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil {
			return false
		}
	}
	return true
}

func (st *Store) writeInventoryCatalog(fingerprint string, rows []port.SessionDiscoveryMeta) error {
	data, err := json.Marshal(inventoryCatalog{Format: inventoryCatalogFormat, Fingerprint: fingerprint, Rows: rows})
	if err != nil {
		return fmt.Errorf("jsonlstore: encode inventory catalog: %w", err)
	}
	tmp, err := os.CreateTemp(st.resolver.canonicalDir(), ".session-inventory-*") //nolint:gosec // owner-only store dir
	if err != nil {
		return fmt.Errorf("jsonlstore: create inventory catalog temporary: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("jsonlstore: chmod inventory catalog temporary: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("jsonlstore: write inventory catalog: %w", err)
	}
	if st.durability.FileSync {
		if err := tmp.Sync(); err != nil {
			return fmt.Errorf("jsonlstore: sync inventory catalog: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("jsonlstore: close inventory catalog: %w", err)
	}
	if err := os.Rename(tmpPath, st.inventoryCatalogPath()); err != nil {
		return fmt.Errorf("jsonlstore: replace inventory catalog: %w", err)
	}
	if !st.durability.DirectorySync {
		return nil
	}
	dir, err := os.Open(st.resolver.canonicalDir()) //nolint:gosec // adapter-private owner-only path
	if err != nil {
		return fmt.Errorf("jsonlstore: open inventory catalog directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("jsonlstore: sync inventory catalog directory: %w", err)
	}
	return nil
}
