package jsonlstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const migrationJobsDir = "migration-jobs"

var _ port.SessionMigrationStore = (*Store)(nil)

// InspectSessionMigration performs a read-only physical inventory. It never
// creates a catalog, job record, lock, quarantine, or replacement file.
func (st *Store) InspectSessionMigration(ctx context.Context) (port.SessionMigrationInspection, error) {
	if err := ctx.Err(); err != nil {
		return port.SessionMigrationInspection{}, err
	}
	inspection := port.SessionMigrationInspection{Available: true}
	type entry struct {
		path string
		info os.FileInfo
	}
	var v1 []entry
	var generationParts []string
	for _, dir := range []string{st.resolver.dir, st.resolver.canonicalDir()} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return port.SessionMigrationInspection{}, fmt.Errorf("jsonlstore: inspect migration storage: %w", err)
		}
		for _, item := range entries {
			if item.IsDir() || !isSessionStorageFile(item.Name()) {
				continue
			}
			path := filepath.Join(dir, item.Name())
			info, err := os.Lstat(path)
			if err != nil {
				return port.SessionMigrationInspection{}, fmt.Errorf("jsonlstore: inspect migration entry: %w", err)
			}
			inspection.CurrentBytes += info.Size()
			generationParts = append(generationParts, item.Name()+":"+fmt.Sprint(info.Size())+":"+fmt.Sprint(info.ModTime().UnixNano()))
			if !info.Mode().IsRegular() {
				inspection.InvalidFamilies++
				continue
			}
			switch {
			case strings.HasSuffix(item.Name(), sessionFileSuffix):
				inspection.V1Families++
				v1 = append(v1, entry{path: path, info: info})
			case strings.HasSuffix(item.Name(), currentSnapshotSuffix):
				inspection.V2Families++
				if !validMigrationCurrent(path, item.Name()) {
					inspection.InvalidFamilies++
				}
			}
		}
	}
	sort.Strings(generationParts)
	generation := sha256.Sum256([]byte(strings.Join(generationParts, "\n")))
	inspection.Generation = hex.EncodeToString(generation[:16])

	seen := make(map[session.SessionID]struct{}, len(v1))
	for _, item := range v1 {
		family, payloadSize, err := migrationFamily(item.path, item.info)
		if err != nil {
			inspection.InvalidFamilies++
			continue
		}
		if _, duplicate := seen[family.ID]; duplicate {
			inspection.SkippedFamilies++
			continue
		}
		seen[family.ID] = struct{}{}
		inspection.Families = append(inspection.Families, family)
		reclaim := item.info.Size() - payloadSize
		if reclaim > 0 {
			inspection.ReclaimableBytes += reclaim
		}
		if payloadSize > inspection.TemporaryBytes {
			inspection.TemporaryBytes = payloadSize
		}
	}
	return inspection, nil
}

func validMigrationCurrent(path, name string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from the owner-only store scan
	if err != nil {
		return false
	}
	current, err := decodeCurrentSnapshot(data)
	if err != nil {
		return false
	}
	sess, err := sessnap.Unmarshal(current.Snapshot)
	if err != nil || sess.ID == "" || name != encodeSessionToken(sess.ID)+currentSnapshotSuffix {
		return false
	}
	return current.Metadata.ID == "" || current.Metadata.ID == sess.ID
}

func migrationFamily(path string, info os.FileInfo) (port.SessionMigrationFamily, int64, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from the owner-only store scan
	if err != nil {
		return port.SessionMigrationFamily{}, 0, err
	}
	defer func() { _ = f.Close() }()
	line, err := readLastLineAt(f, info.Size())
	if err != nil {
		return port.SessionMigrationFamily{}, 0, err
	}
	sess, err := sessnap.Unmarshal(line)
	if err != nil {
		return port.SessionMigrationFamily{}, 0, err
	}
	if sess.ID == "" {
		return port.SessionMigrationFamily{}, 0, errors.New("snapshot carries no session id")
	}
	fingerprint := migrationFingerprint(line, info)
	ownerKey := migrationOwnerKey(sess.Owner)
	handleSum := sha256.Sum256([]byte(sess.ID))
	family := port.SessionMigrationFamily{
		ID: sess.ID, Handle: hex.EncodeToString(handleSum[:8]), Fingerprint: fingerprint,
		OwnerKey: ownerKey, Kind: sess.Kind, State: sess.State, Bytes: info.Size(),
	}
	envelope, err := json.Marshal(currentSnapshot{
		Format: currentSnapshotFormat, ModifiedAt: info.ModTime(), Metadata: metaSnapshotFromSession(sess), Snapshot: line,
	})
	if err != nil {
		return port.SessionMigrationFamily{}, 0, err
	}
	return family, int64(len(envelope)), nil
}

func migrationFingerprint(line []byte, info os.FileInfo) string {
	h := sha256.New()
	_, _ = h.Write(line)
	_, _ = io.WriteString(h, fmt.Sprintf("\x00%d\x00%d", info.Size(), info.ModTime().UnixNano()))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func migrationOwnerKey(owner *session.Principal) string {
	if owner == nil {
		return ""
	}
	sum := session.PrincipalScopeHash(owner)
	return hex.EncodeToString(sum[:16])
}

// MigrateSessionFamily holds the stable family flock across revalidation,
// promotion, v2 verification, and v1 removal. Expected item outcomes return a
// closed reason code; operational failures are wrapped for the server boundary to
// sanitize as backend_failure.
//
//nolint:gocyclo // the linear crash-safety transaction keeps every fail-closed checkpoint explicit.
func (st *Store) MigrateSessionFamily(ctx context.Context, expected port.SessionMigrationFamily) (string, error) {
	releaseOwnership, err := st.holdMigrationAcquisition(ctx, "")
	if err != nil {
		return "", err
	}
	defer releaseOwnership()
	if err := validateSessionID(expected.ID); err != nil {
		return "invalid_snapshot", nil
	}
	path := st.resolver.currentSnapshotPath(expected.ID)
	var reason string
	err = st.withSnapshotFamilyLock(ctx, path, func() error {
		dirs, err := st.openDurableDirectories(st.resolver.dir, st.resolver.canonicalDir())
		if err != nil {
			return err
		}
		defer dirs.close()
		v1Path, info, line, found, err := st.migrationSource(expected.ID)
		if err != nil {
			reason = "invalid_snapshot"
			return nil
		}
		if !found {
			// A previous attempt may have committed and removed v1 before its job
			// checkpoint. A readable matching v2 is idempotent success after the
			// canonical directory is synced again, which converges a retry after a
			// failed final removal sync.
			if st.verifiedCurrent(expected.ID, nil) {
				return dirs.sync()
			}
			reason = "changed"
			return nil
		}
		sess, err := sessnap.Unmarshal(line)
		if err != nil || sess.ID != expected.ID {
			reason = "invalid_snapshot"
			return nil
		}
		if migrationFingerprint(line, info) != expected.Fingerprint || migrationOwnerKey(sess.Owner) != expected.OwnerKey ||
			sess.Kind != expected.Kind || sess.State != expected.State {
			reason = "changed"
			return nil
		}
		currentExists := false
		if currentInfo, statErr := os.Stat(path); statErr == nil {
			currentExists = currentInfo.Mode().IsRegular()
			if !currentExists || !st.verifiedCurrent(expected.ID, line) {
				reason = "changed"
				return nil
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		// A v2 file left by a prior rename whose directory sync failed is not
		// durable authority yet. Establish it before removing the verified v1.
		if currentExists {
			if err := dirs.sync(); err != nil {
				return err
			}
		}
		if err := st.advanceInventoryGeneration(); err != nil {
			return err
		}
		if err := st.prepareWrite(expected.ID); err != nil {
			return err
		}
		if !currentExists && v1Path == st.resolver.legacyPath(expected.ID, kindSnapshot) {
			v1Path = st.resolver.canonicalPath(expected.ID, kindSnapshot)
		}
		if !currentExists {
			data, err := json.Marshal(currentSnapshot{
				Format: currentSnapshotFormat, ModifiedAt: info.ModTime(), Metadata: metaSnapshotFromSession(sess), Snapshot: line,
			})
			if err != nil {
				reason = "invalid_snapshot"
				return nil
			}
			pattern := snapshotTempPattern(path, st.tempOwner, st.tempGeneration.Add(1))
			if err := replaceCurrentSnapshot(path, data, info.ModTime(), pattern, st.snapshot, st.durability, nil); err != nil {
				if errors.Is(err, syscall.ENOSPC) {
					reason = "insufficient_space"
					return nil
				}
				return err
			}
		}
		if !st.verifiedCurrent(expected.ID, line) {
			reason = "verification_failed"
			return nil
		}
		removed, err := removeSessionFile(st.snapshot.remove, v1Path)
		if err != nil {
			return err
		}
		if removed {
			return dirs.sync()
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("jsonlstore: migrate session family: %w", err)
	}
	return reason, nil
}

func (st *Store) migrationSource(id session.SessionID) (string, os.FileInfo, []byte, bool, error) {
	for _, path := range []string{st.resolver.canonicalPath(id, kindSnapshot), st.resolver.legacyPath(id, kindSnapshot)} {
		f, err := os.Open(path) //nolint:gosec // confined store path
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", nil, nil, false, err
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			if statErr != nil {
				return "", nil, nil, false, statErr
			}
			return "", nil, nil, false, errors.New("migration source is not regular")
		}
		line, readErr := readLastLineAt(f, info.Size())
		_ = f.Close()
		if readErr != nil {
			return "", nil, nil, false, readErr
		}
		sess, decodeErr := sessnap.Unmarshal(line)
		if decodeErr != nil || sess.ID != id {
			return "", nil, nil, false, errors.New("migration source ownership unproven")
		}
		return path, info, line, true, nil
	}
	return "", nil, nil, false, nil
}

func (st *Store) verifiedCurrent(id session.SessionID, want []byte) bool {
	data, err := os.ReadFile(st.resolver.currentSnapshotPath(id)) //nolint:gosec // confined store path
	if err != nil {
		return false
	}
	current, err := decodeCurrentSnapshot(data)
	if err != nil {
		return false
	}
	sess, err := sessnap.Unmarshal(current.Snapshot)
	if err != nil || sess.ID != id {
		return false
	}
	return want == nil || string(current.Snapshot) == string(want)
}

func (st *Store) migrationJobDir() string {
	return filepath.Join(st.resolver.canonicalDir(), migrationJobsDir)
}

func (st *Store) migrationJobPath(id string) (string, error) {
	if len(id) != 32 {
		return "", errors.New("invalid migration job handle")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", errors.New("invalid migration job handle")
	}
	return filepath.Join(st.migrationJobDir(), id+".json"), nil
}

type migrationAcquisitionContextKey struct{}

type migrationAcquisition struct {
	mu     sync.Mutex
	store  *Store
	id     string
	active bool
}

func (st *Store) holdMigrationAcquisition(ctx context.Context, id string) (func(), error) {
	acquisition, ok := ctx.Value(migrationAcquisitionContextKey{}).(*migrationAcquisition)
	if !ok || acquisition == nil {
		return nil, errors.New("jsonlstore: migration job lock acquisition not bound")
	}
	acquisition.mu.Lock()
	if !acquisition.active || acquisition.store != st || id != "" && acquisition.id != id {
		acquisition.mu.Unlock()
		return nil, errors.New("jsonlstore: migration job lock acquisition not bound")
	}
	return acquisition.mu.Unlock, nil
}

// AcquireSessionMigrationJob holds one stable cross-process job exclusion until
// the returned release function is called and binds that acquisition to the
// returned context.
func (st *Store) AcquireSessionMigrationJob(ctx context.Context, id string) (context.Context, func() error, error) {
	path, err := st.migrationJobPath(id)
	if err != nil {
		return nil, nil, err
	}
	if err := validateAdapterDirectory(st.migrationJobDir()); err != nil {
		return nil, nil, fmt.Errorf("jsonlstore: validate migration registry: %w", err)
	}
	fl := flock.New(path+".lock", flock.SetPermissions(0o600))
	locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		_ = fl.Close()
		return nil, nil, fmt.Errorf("jsonlstore: acquire migration job lock: %w", err)
	}
	if !locked {
		_ = fl.Close()
		return nil, nil, errors.New("jsonlstore: migration job lock not acquired")
	}
	acquisition := &migrationAcquisition{store: st, id: id, active: true}
	bound := context.WithValue(ctx, migrationAcquisitionContextKey{}, acquisition)
	var once sync.Once
	var releaseErr error
	return bound, func() error {
		once.Do(func() {
			acquisition.mu.Lock()
			acquisition.active = false
			releaseErr = fl.Close()
			acquisition.mu.Unlock()
		})
		return releaseErr
	}, nil
}

// CheckSessionMigrationJobOwnership rejects contexts without the active exact
// acquisition used by this store.
func (st *Store) CheckSessionMigrationJobOwnership(ctx context.Context) error {
	release, err := st.holdMigrationAcquisition(ctx, "")
	if err != nil {
		return errors.New("jsonlstore: migration job lock lost")
	}
	release()
	return nil
}

// SaveSessionMigrationJob atomically checkpoints one sanitized durable job record.
func (st *Store) SaveSessionMigrationJob(ctx context.Context, job port.SessionMigrationJob) error {
	releaseOwnership, err := st.holdMigrationAcquisition(ctx, job.ID)
	if err != nil {
		return err
	}
	defer releaseOwnership()
	if !st.durability.HostCrashSafe() {
		return errors.New("jsonlstore: migration checkpoint requires atomic replacement, file sync, and directory sync")
	}
	path, err := st.migrationJobPath(job.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("jsonlstore: encode migration job: %w", err)
	}
	pattern := snapshotTempPattern(path, st.tempOwner, st.tempGeneration.Add(1))
	return replaceCurrentSnapshot(path, data, time.Now(), pattern, st.snapshot, st.durability, nil)
}

// LoadSessionMigrationJob reloads one validated durable job record by opaque handle.
func (st *Store) LoadSessionMigrationJob(_ context.Context, id string) (port.SessionMigrationJob, error) {
	path, err := st.migrationJobPath(id)
	if err != nil {
		return port.SessionMigrationJob{}, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // validated handle under owner-only registry
	if err != nil {
		return port.SessionMigrationJob{}, err
	}
	var job port.SessionMigrationJob
	if err := json.Unmarshal(data, &job); err != nil {
		return port.SessionMigrationJob{}, fmt.Errorf("jsonlstore: decode migration job: %w", err)
	}
	if job.ID != id {
		return port.SessionMigrationJob{}, errors.New("jsonlstore: migration job identity mismatch")
	}
	return job, nil
}
