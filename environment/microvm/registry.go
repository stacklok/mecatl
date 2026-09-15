package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gofrs/flock"
)

// FileRegistry is the daemon's durable generation-fenced environment registry.
// Each operation takes an inter-process lock and reloads the file, so a restarted
// daemon and two daemons sharing the state directory observe one authoritative set.
type FileRegistry struct {
	path string
	lock *flock.Flock
}

type registryDocument struct {
	Version      int                 `json:"version"`
	Environments []EnvironmentRecord `json:"environments"`
}

// OpenFileRegistry opens a registry. The document is created by the first Save;
// opening never invents or provisions an environment.
func OpenFileRegistry(path string) (*FileRegistry, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("microvm registry path must be absolute and clean")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create microvm registry directory: %w", err)
	}
	return &FileRegistry{path: path, lock: flock.New(path + ".lock")}, nil
}

// Save atomically creates or updates one exact generation. An environment ID can
// never be overwritten by a different generation, including by a racing daemon.
func (r *FileRegistry) Save(ctx context.Context, record EnvironmentRecord) error {
	if err := validateRecordIdentity(record); err != nil {
		return err
	}
	return r.withLock(ctx, func(document *registryDocument) error {
		for i := range document.Environments {
			if document.Environments[i].EnvironmentID != record.EnvironmentID {
				continue
			}
			current := document.Environments[i]
			if current.Generation != record.Generation || current.Ref != record.Ref || !validRecordTransition(current, record) {
				return ErrEnvironmentStale
			}
			document.Environments[i] = cloneEnvironmentRecord(record)
			return r.write(*document)
		}
		document.Environments = append(document.Environments, cloneEnvironmentRecord(record))
		return r.write(*document)
	})
}

// Lookup returns one exact durable record.
func (r *FileRegistry) Lookup(ctx context.Context, environmentID string) (EnvironmentRecord, error) {
	var result EnvironmentRecord
	err := r.withLock(ctx, func(document *registryDocument) error {
		for _, record := range document.Environments {
			if record.EnvironmentID == environmentID {
				result = cloneEnvironmentRecord(record)
				return nil
			}
		}
		return ErrEnvironmentUnknown
	})
	return result, err
}

// List returns a stable snapshot of every durable record, including tombstones.
func (r *FileRegistry) List(ctx context.Context) ([]EnvironmentRecord, error) {
	var result []EnvironmentRecord
	err := r.withLock(ctx, func(document *registryDocument) error {
		result = make([]EnvironmentRecord, len(document.Environments))
		for i, record := range document.Environments {
			result[i] = cloneEnvironmentRecord(record)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].EnvironmentID < result[j].EnvironmentID })
		return nil
	})
	return result, err
}

func (r *FileRegistry) withLock(ctx context.Context, fn func(*registryDocument) error) error {
	if r == nil || r.lock == nil {
		return errors.New("microvm registry is not configured")
	}
	locked, err := r.lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock microvm registry: %w", err)
	}
	if !locked {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		return errors.New("microvm registry lock was not acquired")
	}
	defer func() { _ = r.lock.Unlock() }()
	document, err := r.read()
	if err != nil {
		return err
	}
	return fn(&document)
}

func (r *FileRegistry) read() (registryDocument, error) {
	file, err := os.Open(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return registryDocument{Version: 1}, nil
	}
	if err != nil {
		return registryDocument{}, fmt.Errorf("open microvm registry: %w", err)
	}
	var document registryDocument
	decoder := json.NewDecoder(io.LimitReader(file, 16<<20))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&document)
	closeErr := file.Close()
	if decodeErr != nil {
		return registryDocument{}, fmt.Errorf("decode microvm registry: %w", decodeErr)
	}
	if closeErr != nil {
		return registryDocument{}, fmt.Errorf("close microvm registry: %w", closeErr)
	}
	if document.Version != 1 {
		return registryDocument{}, fmt.Errorf("unsupported microvm registry version %d", document.Version)
	}
	return document, nil
}

func (r *FileRegistry) write(document registryDocument) error {
	document.Version = 1
	data, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode microvm registry: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".registry-*")
	if err != nil {
		return fmt.Errorf("create microvm registry transaction: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure microvm registry transaction: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write microvm registry transaction: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync microvm registry transaction: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close microvm registry transaction: %w", err)
	}
	if err := os.Rename(tmpName, r.path); err != nil {
		return fmt.Errorf("commit microvm registry transaction: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open microvm registry directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync microvm registry directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close microvm registry directory: %w", err)
	}
	return nil
}

func validRecordTransition(current, next EnvironmentRecord) bool {
	rank := func(state EnvironmentState) int {
		switch state {
		case EnvironmentProvisioning:
			return 0
		case EnvironmentReady:
			return 1
		case EnvironmentDeleting, EnvironmentCleanupPending:
			return 2
		case EnvironmentDestroyed:
			return 3
		default:
			return -1
		}
	}
	currentRank, nextRank := rank(current.State), rank(next.State)
	if currentRank < 0 || nextRank < currentRank || (current.Tombstone && !next.Tombstone) {
		return false
	}
	if current.ParentRef != next.ParentRef || current.ForkBase != next.ForkBase {
		return false
	}
	if current.VMDeleted && !next.VMDeleted || current.WorktreeDeleted && !next.WorktreeDeleted {
		return false
	}
	return true
}

func validateRecordIdentity(record EnvironmentRecord) error {
	environmentID, generation, err := parseEnvironmentRef(record.Ref)
	if err != nil || environmentID != record.EnvironmentID || generation != record.Generation {
		return ErrInvalidEnvironmentRef
	}
	parentSet := record.ParentRef != (EnvironmentRef{})
	if parentSet != (record.ForkBase != "") {
		return ErrInvalidEnvironmentRef
	}
	if parentSet {
		if record.ParentRef == record.Ref {
			return ErrInvalidEnvironmentRef
		}
		if _, _, err := parseEnvironmentRef(record.ParentRef); err != nil {
			return ErrInvalidEnvironmentRef
		}
	}
	return nil
}
