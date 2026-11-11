package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driverSQLite "modernc.org/sqlite"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestPhysicalConnectionPolicyAndReplacement(t *testing.T) {
	b := newBackend(t, t.TempDir())
	id := history.NewHistoryID()
	appendRoot(t, b, id)
	db := storedDatabase(t, b, id)
	check := func() {
		t.Helper()
		for _, p := range []struct{ key, want string }{{"journal_mode", "wal"}, {"synchronous", "2"}, {"fullfsync", "1"}, {"checkpoint_fullfsync", "1"}, {"wal_autocheckpoint", "1000"}, {"busy_timeout", "5000"}, {"locking_mode", "normal"}, {"read_uncommitted", "0"}} {
			var got string
			if err := db.QueryRow("PRAGMA " + p.key).Scan(&got); err != nil || got != p.want {
				t.Fatalf("%s=%s: %v", p.key, got, err)
			}
		}
		if s := db.Stats(); s.OpenConnections != 1 || s.Idle != 1 || s.MaxOpenConnections != 1 {
			t.Fatalf("stats: %+v", s)
		}
	}
	check()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil && !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal(err)
	}
	check()
}

func TestPoolAndWriterContentionUseCallerBudget(t *testing.T) {
	root := t.TempDir()
	b := newBackend(t, root)
	id := history.NewHistoryID()
	first := appendRoot(t, b, id)
	other := newBackend(t, root)
	if _, err := other.Observe(t.Context(), id, 0); err != nil {
		t.Fatal(err)
	}
	writer := storedDatabase(t, other, id)
	tx, err := writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	deadlines := make(chan time.Time, 1)
	installWrapped(t, b, id, wrappedConnector{begin: func(ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("BeginTx context has no deadline")
			return
		}
		select {
		case deadlines <- deadline:
		default:
		}
	}})
	db := storedDatabase(t, b, id)
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 160*time.Millisecond)
	defer cancel()
	wantDeadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("caller context has no deadline")
	}
	result := make(chan error, 1)
	start := time.Now()
	go func() {
		o, err := b.Append(ctx, id, first, proposal(2))
		if o != (history.Observation{}) {
			result <- errors.New("nonzero failure evidence")
			return
		}
		result <- err
	}()
	// Establish that the operation is actually waiting in the pool, not sleeping
	// before it starts. Releasing it leaves only the original remaining SQL budget.
	for db.Stats().WaitCount == 0 {
		if time.Since(start) > time.Second {
			t.Fatal("pool wait not reached")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait: %v", err)
	}
	select {
	case gotDeadline := <-deadlines:
		if gotDeadline != wantDeadline {
			t.Fatalf("driver BeginTx deadline restarted: got %v, want %v", gotDeadline, wantDeadline)
		}
	default:
		t.Fatal("driver BeginTx was not called")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("caller budget exceeded: %s", elapsed)
	}
	// Reads remain deferred and can see an established WAL snapshot while another
	// connection has writer authority. Another history also progresses.
	if o, err := b.Observe(t.Context(), id, 1); err != nil || o.Head != first {
		t.Fatalf("reader took write authority: %+v %v", o, err)
	}
	appendRoot(t, b, history.NewHistoryID())
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Append(t.Context(), id, first, proposal(2)); err != nil {
		t.Fatal(err)
	}
}

// receiptConnector/receiptConn preserve the real driver's context-aware methods;
// only transaction COMMIT is intercepted to deterministically test cancellation
// and lost acknowledgments at the adapter boundary, not simulate media failures.
type receiptConnector struct {
	driver.Connector
	before, after func()
	failure       error
	commits       *atomic.Int32
}

func (r receiptConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := r.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return receiptConn{Conn: c, ConnBeginTx: c.(driver.ConnBeginTx), ConnPrepareContext: c.(driver.ConnPrepareContext), ExecerContext: c.(driver.ExecerContext), QueryerContext: c.(driver.QueryerContext), Pinger: c.(driver.Pinger), r: r}, nil
}

type receiptConn struct {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	r receiptConnector
}

func (c receiptConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	tx, err := c.ConnBeginTx.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return receiptTx{Tx: tx, r: c.r}, nil
}

type receiptTx struct {
	driver.Tx
	r receiptConnector
}

func (tx receiptTx) Commit() error {
	tx.r.commits.Add(1)
	if tx.r.before != nil {
		tx.r.before()
	}
	err := tx.Tx.Commit()
	if err != nil {
		return err
	}
	if tx.r.after != nil {
		tx.r.after()
	}
	return tx.r.failure
}

func installReceipt(t *testing.T, b *Backend, id history.HistoryID, r receiptConnector) {
	t.Helper()
	old := storedDatabase(t, b, id)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(b.root, id.String(), databaseName)}
	u.RawQuery = url.Values{"mode": {"rw"}, "_txlock": {"immediate"}}.Encode()
	base, err := driverSQLite.NewConnector(u.String())
	if err != nil {
		t.Fatal(err)
	}
	r.Connector = connector{Connector: base}
	db := sql.OpenDB(r)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	b.handles[id].db = db
}

func TestCommitOutcomeAndLateCancellation(t *testing.T) {
	for _, mode := range []string{"before commit", "late cancel", "lost receipt"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			b := newBackend(t, root)
			id := history.NewHistoryID()
			first := appendRoot(t, b, id)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var commits atomic.Int32
			r := receiptConnector{commits: &commits}
			lost := errors.New("lost receipt")
			switch mode {
			case "before commit":
				r.before = cancel
			case "late cancel":
				r.after = cancel
			case "lost receipt":
				r.failure = lost
			}
			installReceipt(t, b, id, r)
			o, err := b.Append(ctx, id, first, proposal(2))
			if commits.Load() != 1 {
				t.Fatalf("resubmitted commit: %d", commits.Load())
			}
			switch mode {
			case "before commit":
				if err == nil || o != (history.Observation{}) {
					t.Fatalf("before commit: %+v %v", o, err)
				}
			case "late cancel":
				if err != nil || o.Head != proposal(2).Ref || ctx.Err() == nil {
					t.Fatalf("late cancellation masked success: %+v %v", o, err)
				}
			case "lost receipt":
				if !errors.Is(err, lost) || o != (history.Observation{}) {
					t.Fatalf("receipt: %+v %v", o, err)
				}
			}
			// Reopen independently; a lost receipt is resolved through actual accepted
			// bytes, not inferred from the operation error.
			other := newBackend(t, root)
			observed, err := other.Observe(t.Context(), id, 2)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "before commit" {
				if observed.Head != first || observed.Commit != (history.CommitRef{}) {
					t.Fatalf("canceled write persisted: %+v", observed)
				}
			} else if observed.Head != proposal(2).Ref || observed.Commit != proposal(2).Ref {
				t.Fatalf("missing committed write: %+v", observed)
			}
		})
	}
}

func TestOperationsRaceClose(t *testing.T) {
	b := newBackend(t, t.TempDir())
	id := history.NewHistoryID()
	appendRoot(t, b, id)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			<-start
			for range 10 {
				if _, err := b.Observe(t.Context(), id, 1); err != nil && !errors.Is(err, os.ErrClosed) {
					t.Error(err)
				}
			}
		})
	}
	wg.Go(func() {
		<-start
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	close(start)
	wg.Wait()
}

// wrappedConnector keeps the real SQLite connection and its context-aware
// optional interfaces while tests observe transaction start and cleanup.
type wrappedConnector struct {
	driver.Connector
	begin func(context.Context)
	close func() error
}

func (c wrappedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return wrappedConn{
		Conn:               conn,
		ConnBeginTx:        conn.(driver.ConnBeginTx),
		ConnPrepareContext: conn.(driver.ConnPrepareContext),
		ExecerContext:      conn.(driver.ExecerContext),
		QueryerContext:     conn.(driver.QueryerContext),
		Pinger:             conn.(driver.Pinger),
		SessionResetter:    conn.(driver.SessionResetter),
		Validator:          conn.(driver.Validator),
		begin:              c.begin,
		close:              c.close,
	}, nil
}

type wrappedConn struct {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
	begin func(context.Context)
	close func() error
}

func (c wrappedConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	if c.begin != nil {
		c.begin(ctx)
	}
	return c.ConnBeginTx.BeginTx(ctx, options)
}

func (c wrappedConn) Close() error {
	return errors.Join(c.Conn.Close(), func() error {
		if c.close == nil {
			return nil
		}
		return c.close()
	}())
}

func installWrapped(t *testing.T, b *Backend, id history.HistoryID, wrapped wrappedConnector) {
	t.Helper()
	old := storedDatabase(t, b, id)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(b.root, id.String(), databaseName)}
	u.RawQuery = url.Values{"mode": {"rw"}, "_txlock": {"immediate"}}.Encode()
	base, err := driverSQLite.NewConnector(u.String())
	if err != nil {
		t.Fatal(err)
	}
	wrapped.Connector = connector{Connector: base}
	db := sql.OpenDB(wrapped)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	b.handles[id].db = db
}

func TestCloseRetainsAllCleanupFailures(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })

	firstID, secondID := history.NewHistoryID(), history.NewHistoryID()
	appendRoot(t, b, firstID)
	appendRoot(t, b, secondID)
	firstFailure := errors.New("first close failure")
	secondFailure := errors.New("second close failure")
	var firstClosed, secondClosed atomic.Int32
	installWrapped(t, b, firstID, wrappedConnector{close: func() error {
		firstClosed.Add(1)
		return firstFailure
	}})
	installWrapped(t, b, secondID, wrappedConnector{close: func() error {
		secondClosed.Add(1)
		return secondFailure
	}})
	for _, id := range []history.HistoryID{firstID, secondID} {
		if _, err := b.Observe(t.Context(), id, 1); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, 11)
	var wg sync.WaitGroup
	for range cap(results) {
		wg.Go(func() {
			<-start
			results <- b.Close()
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var recorded error
	for err := range results {
		if !errors.Is(err, firstFailure) || !errors.Is(err, secondFailure) {
			t.Fatalf("missing cleanup failure: %v", err)
		}
		if recorded == nil {
			recorded = err
		} else if err != recorded {
			t.Fatalf("Close did not return its recorded failure: %v vs %v", err, recorded)
		}
	}
	if firstClosed.Load() != 1 || secondClosed.Load() != 1 {
		t.Fatalf("physical closes: first=%d second=%d", firstClosed.Load(), secondClosed.Load())
	}
	if err := b.Close(); err != recorded {
		t.Fatalf("repeated Close did not return recorded failure: %v vs %v", err, recorded)
	}
	if o, err := b.Observe(t.Context(), firstID, 1); !errors.Is(err, os.ErrClosed) || o != (history.Observation{}) {
		t.Fatalf("operation admitted after Close: %+v %v", o, err)
	}
}
