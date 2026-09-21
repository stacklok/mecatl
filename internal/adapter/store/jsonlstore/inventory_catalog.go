package jsonlstore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	inventoryCatalogFormat        = "session-inventory-json/3"
	inventoryCatalogDirName       = ".session-inventory"
	inventoryCatalogFileName      = "manifest.json"
	inventoryCatalogLockName      = "catalog.lock"
	inventoryGenerationMarkerName = "generation"
	inventoryGlobalScope          = "all"
)

type inventoryWorkKind uint8

const (
	inventoryWorkCatalogRow inventoryWorkKind = iota + 1
	inventoryWorkSnapshotRead
	inventoryWorkRebuild
)

type inventoryCatalogScope struct {
	File  string `json:"file"`
	Count int    `json:"count"`
}

type inventoryCatalogSource struct {
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
	Mode       uint32 `json:"mode"`
}

type inventoryCatalog struct {
	Format      string                            `json:"v"`
	Fingerprint string                            `json:"fingerprint"`
	Generation  string                            `json:"generation"`
	Sources     map[string]inventoryCatalogSource `json:"v1_sources"`
	Scopes      map[string]inventoryCatalogScope  `json:"scopes"`
}

func (st *Store) observeInventoryWork(kind inventoryWorkKind) {
	if st.inventoryWorkObserver != nil {
		st.inventoryWorkObserver(kind)
	}
}

func (st *Store) inventoryCatalogDir() string {
	return filepath.Join(st.resolver.canonicalDir(), inventoryCatalogDirName)
}

func (st *Store) inventoryCatalogPath() string {
	return filepath.Join(st.inventoryCatalogDir(), inventoryCatalogFileName)
}

func (st *Store) inventoryGenerationMarkerPath() string {
	return filepath.Join(st.inventoryCatalogDir(), inventoryGenerationMarkerName)
}

func (st *Store) inventoryCatalogRelativePath(path string) (string, error) {
	name, err := filepath.Rel(st.inventoryCatalogDir(), path)
	if err != nil || name == "." || filepath.Base(name) != name {
		return "", fmt.Errorf("jsonlstore: inventory catalog path %q is not a direct descendant", path)
	}
	return name, nil
}

func (st *Store) inventoryCatalogRoot() (*os.Root, error) {
	root, err := os.OpenRoot(st.inventoryCatalogDir())
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: open inventory catalog root: %w", err)
	}
	return root, nil
}

func (st *Store) advanceInventoryGeneration() error {
	generation := fmt.Sprintf("%s-%016x\n", st.tempOwner, st.tempGeneration.Add(1))
	return st.writeInventoryFileWithPattern(st.inventoryGenerationMarkerPath(), []byte(generation), ".inventory-generation-*")
}

func (st *Store) withInventoryCatalogLock(ctx context.Context, fn func() error) error {
	fl := flock.New(filepath.Join(st.inventoryCatalogDir(), inventoryCatalogLockName), flock.SetPermissions(0o600))
	locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("jsonlstore: acquire inventory catalog lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("jsonlstore: acquire inventory catalog lock: lock not acquired")
	}
	defer func() { _ = fl.Close() }()
	return fn()
}

// inventoryFingerprint observes O(1) directory metadata for the current
// snapshot namespace plus the durable generation marker advanced by current
// writers. Older namespaces are not observed.
func (st *Store) inventoryFingerprint() (string, error) {
	h := sha256.New()
	dir := st.resolver.canonicalDir()
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("jsonlstore: fingerprint inventory directory: %w", err)
	}
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", dir, info.ModTime().UnixNano(), info.Size(), info.Mode())
	marker, err := os.ReadFile(st.inventoryGenerationMarkerPath()) //nolint:gosec // adapter-private owner-only path
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("jsonlstore: read inventory generation marker: %w", err)
	}
	_, _ = h.Write(marker)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (st *Store) inventorySources() (map[string]inventoryCatalogSource, error) {
	return map[string]inventoryCatalogSource{}, nil
}

func inventoryGeneration(fingerprint string, sources map[string]inventoryCatalogSource) string {
	h := sha256.New()
	_, _ = h.Write([]byte(fingerprint))
	keys := make([]string, 0, len(sources))
	for key := range sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		source := sources[key]
		_, _ = fmt.Fprintf(h, "\n%s\x00%d\x00%d\x00%d", key, source.Size, source.ModifiedAt, source.Mode)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func inventoryOwnerScope(owner *session.Principal) string {
	if owner == nil {
		return "owner:none"
	}
	sum := session.PrincipalScopeHash(owner)
	return "owner:" + hex.EncodeToString(sum[:])
}

func inventoryScopeFile(scope, generation string) string {
	label := "all"
	if scope != inventoryGlobalScope {
		label = strings.TrimPrefix(scope, "owner:")
	}
	return ".session-inventory-" + generation + "-" + label + ".jsonl"
}

func (st *Store) readInventoryManifest(fingerprint string) (inventoryCatalog, bool) {
	data, err := os.ReadFile(st.inventoryCatalogPath()) //nolint:gosec // adapter-private owner-only path
	if err != nil {
		return inventoryCatalog{}, false
	}
	var catalog inventoryCatalog
	if json.Unmarshal(data, &catalog) != nil || catalog.Format != inventoryCatalogFormat ||
		catalog.Fingerprint != fingerprint || catalog.Sources == nil || len(catalog.Sources) != 0 || catalog.Scopes == nil ||
		catalog.Generation != inventoryGeneration(fingerprint, catalog.Sources) {
		return inventoryCatalog{}, false
	}
	for scope, entry := range catalog.Scopes {
		if scope == "" || entry.File != inventoryScopeFile(scope, catalog.Generation) || entry.Count < 0 || filepath.Base(entry.File) != entry.File {
			return inventoryCatalog{}, false
		}
	}
	return catalog, true
}

func (st *Store) readInventoryCatalog(fingerprint string) ([]port.SessionDiscoveryMeta, bool) {
	catalog, ok := st.readInventoryManifest(fingerprint)
	if !ok {
		return nil, false
	}
	scope, ok := catalog.Scopes[inventoryGlobalScope]
	if !ok {
		return nil, false
	}
	rows, err := st.readInventoryScope(scope)
	if err != nil || !validInventoryRows(rows) {
		return nil, false
	}
	return rows, true
}

func (st *Store) readInventoryScope(scope inventoryCatalogScope) ([]port.SessionDiscoveryMeta, error) {
	f, err := os.Open(filepath.Join(st.inventoryCatalogDir(), scope.File)) //nolint:gosec // validated adapter-private basename
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rows := make([]port.SessionDiscoveryMeta, 0, scope.Count)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenSize)
	for scanner.Scan() {
		var row port.SessionDiscoveryMeta
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		row.Activity = session.ValidActivity(row.Activity)
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(rows) != scope.Count {
		return nil, fmt.Errorf("jsonlstore: inventory scope count %d, want %d", len(rows), scope.Count)
	}
	return rows, nil
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
				row.TitleProvenance != "" || row.Owner != nil || row.EnvironmentRef != (session.EnvironmentRef{}) || row.Kind != "" ||
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

func sortInventoryRows(rows []port.SessionDiscoveryMeta) {
	slices.SortFunc(rows, port.CompareSessionMetadataOrder)
}

func (st *Store) writeInventoryCatalog(fingerprint string, sources map[string]inventoryCatalogSource, rows []port.SessionDiscoveryMeta) error {
	generation := inventoryGeneration(fingerprint, sources)
	global := append([]port.SessionDiscoveryMeta(nil), rows...)
	sortInventoryRows(global)
	grouped := map[string][]port.SessionDiscoveryMeta{inventoryGlobalScope: global}
	for _, row := range global {
		if row.Owner != nil {
			scope := inventoryOwnerScope(row.Owner)
			grouped[scope] = append(grouped[scope], row)
		}
	}
	manifest := inventoryCatalog{
		Format: inventoryCatalogFormat, Fingerprint: fingerprint, Generation: generation, Sources: sources,
		Scopes: make(map[string]inventoryCatalogScope, len(grouped)),
	}
	for scope, scopeRows := range grouped {
		file := inventoryScopeFile(scope, generation)
		var data []byte
		for _, row := range scopeRows {
			encoded, err := json.Marshal(row)
			if err != nil {
				return fmt.Errorf("jsonlstore: encode inventory row: %w", err)
			}
			data = append(data, encoded...)
			data = append(data, '\n')
		}
		if err := st.writeInventoryFile(filepath.Join(st.inventoryCatalogDir(), file), data); err != nil {
			return err
		}
		manifest.Scopes[scope] = inventoryCatalogScope{File: file, Count: len(scopeRows)}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("jsonlstore: encode inventory catalog: %w", err)
	}
	return st.writeInventoryFile(st.inventoryCatalogPath(), data)
}

func (st *Store) reconcileInventoryArtifacts(generation string) error {
	entries, err := os.ReadDir(st.inventoryCatalogDir())
	if err != nil {
		return fmt.Errorf("jsonlstore: scan inventory catalog artifacts: %w", err)
	}
	root, err := st.inventoryCatalogRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	activePrefix := ".session-inventory-" + generation + "-"
	for _, entry := range entries {
		name := entry.Name()
		if name == inventoryCatalogFileName || name == inventoryCatalogLockName || strings.HasPrefix(name, activePrefix) ||
			!strings.HasPrefix(name, ".session-inventory-") {
			continue
		}
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("jsonlstore: remove obsolete inventory artifact %q: %w", name, err)
		}
	}
	return nil
}

func (st *Store) writeInventoryFile(path string, data []byte) error {
	return st.writeInventoryFileWithPattern(path, data, ".session-inventory-*")
}

func (st *Store) writeInventoryFileWithPattern(path string, data []byte, pattern string) error {
	if _, err := st.inventoryCatalogRelativePath(path); err != nil {
		return err
	}
	root, err := st.inventoryCatalogRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// defaultSnapshotOps, NOT st.snapshot: st.snapshot is the store's
	// fault-injectable ops used by crash-safety tests for snapshot writes.
	return replaceCurrentSnapshot(path, data, time.Time{}, pattern, defaultSnapshotOps(), st.durability, root)
}
