package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

// Backend owns retained database handles. Construct with New; the zero value is
// unusable. Do not copy it. Each history uses at most one physical connection.
// Close must be called to release handles; clean shutdown is not needed for
// previously acknowledged commits to survive process loss.
type Backend struct {
	root     string
	mu       sync.Mutex
	handles  map[history.HistoryID]*attachment
	active   sync.WaitGroup
	closing  bool
	closed   chan struct{}
	closeErr error
	// afterPublish runs under the initialization lock after rename, before sync.
	// Tests use it to pause the publication boundary, never to weaken admission.
	afterPublish func() error
}

// attachment serializes only first opens of one history. Failure removes the
// slot so a subsequent caller can retry with its own context.
type attachment struct {
	ready chan struct{}
	db    *sql.DB
}

var _ history.Backend = (*Backend)(nil)

// New prepares and stabilizes a dedicated private storage root and its ancestor
// directory entries. The root must be trusted and application-owned. Histories
// are opened lazily; constructor I/O has no caller cancellation contract.
func New(root string) (*Backend, error) {
	if root == "" {
		return nil, history.ErrInvalid
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fault(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil { // #nosec G703 -- root is an explicitly trusted, application-owned storage path supplied by the caller.
		return nil, fault(err)
	}
	for dir := root; ; dir = filepath.Dir(dir) {
		if err = syncDir(dir); err != nil {
			return nil, fault(err)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return &Backend{root: root, handles: make(map[history.HistoryID]*attachment), closed: make(chan struct{})}, nil
}

// Close permanently stops admission, drains admitted operations, and closes all
// retained handles. Concurrent and repeated calls wait for the same cleanup and
// return the same error. No backend-wide mutex is held during filesystem/SQL I/O.
func (b *Backend) Close() error {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		<-b.closed
		return b.closeErr
	}
	b.closing = true
	b.mu.Unlock()
	b.active.Wait()
	var err error
	for _, h := range b.handles {
		err = errors.Join(err, h.db.Close())
	}
	b.closeErr = fault(err)
	close(b.closed)
	return b.closeErr
}

// admit counts operations before Close can start waiting. The same five-second
// budget covers per-history opening, flock, pool and SQLite contention, not an
// unconditional hard bound on native filesystem I/O.
func (b *Backend) admit(ctx context.Context, id history.HistoryID) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if id == (history.HistoryID{}) {
		return nil, nil, history.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		cancel()
		return nil, nil, os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	b.active.Add(1)
	return ctx, func() { cancel(); b.active.Done() }, nil
}

func (b *Backend) database(ctx context.Context, id history.HistoryID, create bool) (*sql.DB, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		h := b.handles[id]
		if h != nil {
			b.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-h.ready:
			}
			if h.db != nil {
				return h.db, nil
			}
			continue
		}
		h = &attachment{ready: make(chan struct{})}
		b.handles[id] = h
		b.mu.Unlock()
		db, err := b.attach(ctx, id, create)
		b.mu.Lock()
		h.db = db
		if db == nil {
			delete(b.handles, id)
		}
		close(h.ready)
		b.mu.Unlock()
		return db, err
	}
}

// storageError retains causes for errors.Is/As without rendering driver errors
// that could contain stored schema or payload text.
type storageError struct{ cause error }

func (storageError) Error() string   { return "sqlite history storage failure" }
func (e storageError) Unwrap() error { return e.cause }
func fault(err error) error {
	if err == nil {
		return nil
	}
	return storageError{err}
}

func syncDir(path string) error {
	f, err := os.Open(path) // #nosec G703 -- paths derive from the trusted storage root and typed HistoryID filenames.
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
