package jsonlstore

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Reader opens an existing JSONL store without initializing, repairing, or
// mutating it. It intentionally does not implement any writing port.
type Reader struct{ store *Store }

// OpenReader requires an existing current store namespace. Use New to prepare
// a store and its derivative indexes before opening it read-only.
func OpenReader(dir string) (*Reader, error) {
	root, err := normalizeStoreRoot(dir)
	if err != nil {
		return nil, err
	}
	resolver := sessionResolver{dir: root}
	for _, path := range []string{root, resolver.canonicalDir()} {
		if err := validateAdapterDirectory(path); err != nil {
			return nil, fmt.Errorf("jsonlstore: open read-only store: %w", err)
		}
	}
	catalogDir := filepath.Join(resolver.canonicalDir(), inventoryCatalogDirName)
	if err := validateAdapterDirectory(catalogDir); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("jsonlstore: open read-only inventory directory: %w", err)
	}
	return &Reader{store: &Store{resolver: resolver, readOnly: true}}, nil
}

// Lock the confined descriptor returned by openRegular. gofrs/flock supports
// read-only open flags but accepts paths, not existing descriptors. Flock is
// available in syscall on the supported Linux and Darwin targets.
func withExistingSharedLock(ctx context.Context, path string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("jsonlstore: open lock directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	f, err := openRegular(root, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("jsonlstore: open existing lock %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
			return fn()
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return fmt.Errorf("jsonlstore: acquire shared lock %q: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// List enumerates the current snapshots.
func (r *Reader) List(ctx context.Context) ([]port.StoredSession, error) {
	files, err := r.store.resolver.snapshotFilesContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.StoredSession, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, port.StoredSession{ID: file.id, ModifiedAt: file.modified})
	}
	return out, ctx.Err()
}

// MetaList reads metadata from the current prepared catalog.
func (r *Reader) MetaList(ctx context.Context) ([]port.SessionMeta, error) {
	return r.store.MetaList(ctx)
}

// PageSessionMetadata reads a bounded page from the current prepared catalog.
func (r *Reader) PageSessionMetadata(ctx context.Context, req port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	// Retain a row ordinal alongside the byte cursor so EOF can be checked
	// against the manifest count without scanning earlier pages.
	consumed := 0
	if req.Cursor != nil {
		cursor := *req.Cursor
		position, ordinal, ok := strings.Cut(cursor.Continuation, ":")
		var err error
		consumed, err = strconv.Atoi(ordinal)
		if !ok || err != nil || consumed <= 0 {
			return port.SessionMetadataPage{}, port.ErrSessionMetadataCursorRestart
		}
		cursor.Continuation = position
		req.Cursor = &cursor
	}
	page, err := r.store.PageSessionMetadata(ctx, req)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	if consumed > page.TotalCount || len(page.Sessions) > page.TotalCount-consumed {
		return port.SessionMetadataPage{}, fmt.Errorf("jsonlstore: read-only inventory scope unavailable: excess rows")
	}
	consumed += len(page.Sessions)
	if req.Limit > 0 && (page.NextCursor == nil) != (consumed == page.TotalCount) {
		return port.SessionMetadataPage{}, fmt.Errorf("jsonlstore: read-only inventory scope unavailable: row count mismatch")
	}
	if page.NextCursor != nil {
		page.NextCursor.Continuation += ":" + strconv.Itoa(consumed)
	}
	return page, nil
}

// SupportsSessionActivityProjection reports support for saved activity metadata.
func (*Reader) SupportsSessionActivityProjection() bool { return true }

// Load reads the saved snapshot under an existing family lock.
func (r *Reader) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return r.store.Load(ctx, id)
}

// Read yields committed events without changing the log.
func (r *Reader) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return r.store.Read(ctx, id)
}

// ReadAfter reads after a cursor and optionally follows new events.
func (r *Reader) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return r.store.ReadAfter(ctx, id, after, opts)
}

// ReadSessionLineage reads selected existing partitions under shared locks.
func (r *Reader) ReadSessionLineage(ctx context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	if err := port.ValidateSessionLineageQuery(query); err != nil {
		return port.SessionLineageResult{}, err
	}
	paths := []string{r.store.lineageRecordPath(query.RootID)}
	if query.RecordID != "" {
		paths = append(paths, r.store.lineagePointPath(query.RootID, query.RootIncarnation, query.RecordID, string(query.RecordIncarnation)), r.store.lineageRecordPath(query.RecordID))
	} else {
		paths = append(paths, r.store.lineageEdgePath(query.RootID, query.RootIncarnation))
	}
	sort.Strings(paths)
	paths = compactStrings(paths)
	var result port.SessionLineageResult
	var acquire func(int) error
	acquire = func(i int) error {
		if i == len(paths) {
			var err error
			result, err = r.store.ReadSessionLineage(ctx, query)
			return err
		}
		return withExistingSharedLock(ctx, lineageLockPath(paths[i]), func() error {
			root, err := os.OpenRoot(filepath.Dir(paths[i]))
			if err != nil {
				return fmt.Errorf("jsonlstore: open lineage partition directory: %w", err)
			}
			defer func() { _ = root.Close() }()
			f, err := openRegular(root, filepath.Base(paths[i]))
			if err != nil {
				return fmt.Errorf("jsonlstore: inspect read-only lineage partition: %w", err)
			}
			_ = f.Close()
			return acquire(i + 1)
		})
	}
	err := acquire(0)
	return result, err
}

var (
	_ port.MetaLister           = (*Reader)(nil)
	_ port.SessionMetadataPager = (*Reader)(nil)
	_ port.SessionLineageReader = (*Reader)(nil)
)
