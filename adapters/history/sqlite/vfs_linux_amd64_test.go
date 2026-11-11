//go:build linux && amd64

package sqlite

// Qualification-only native Unix VFS callbacks for sqlite v1.62.0/libc v1.80.1.
// Registration is process-global, so these run ONLY in disposable sub-executables.
// Native locking, shared memory and all non-target I/O remain unchanged. This is
// process/I/O-error evidence, not simulated power loss or a production VFS option.

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"unsafe"

	"modernc.org/libc"
	driverSQLite "modernc.org/sqlite"
	s "modernc.org/sqlite/lib"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

var vfsFault struct {
	parent                   uintptr
	mode                     string
	armed, hit, firstPayload bool
	cancel                   context.CancelFunc
	files                    map[uintptr]vfsFile
}

type vfsFile struct {
	original, copy uintptr
	flags          int32
}

func vfsFunction(f any) uintptr { return (*[2]uintptr)(unsafe.Pointer(&f))[1] }

// vfsMemory borrows a native address through libc's C-memory view. All addresses
// here come from SQLite/libc, never Go allocations, and outlive each callback.
func vfsMemory(address uintptr) unsafe.Pointer {
	return unsafe.Pointer(unsafe.SliceData(libc.GoBytes(address, 1)))
}
func vfsAlloc(tls *libc.TLS, n uintptr) uintptr {
	p := libc.Xcalloc(tls, 1, uint64(n))
	if p == 0 {
		panic("VFS allocation failed")
	}
	return p
}
func vfsOpen(tls *libc.TLS, _, name, file uintptr, flags int32, out uintptr) int32 {
	p := (*s.Tsqlite3_vfs)(vfsMemory(vfsFault.parent)).FxOpen
	rc := (*(*func(*libc.TLS, uintptr, uintptr, uintptr, int32, uintptr) int32)(unsafe.Pointer(&p)))(tls, vfsFault.parent, name, file, flags, out)
	if rc != s.SQLITE_OK {
		return rc
	}
	old := (*s.Tsqlite3_file)(vfsMemory(file)).FpMethods
	cp := vfsAlloc(tls, unsafe.Sizeof(s.Tsqlite3_io_methods{}))
	methods := (*s.Tsqlite3_io_methods)(vfsMemory(cp))
	*methods = *(*s.Tsqlite3_io_methods)(vfsMemory(old))
	methods.FxSync = vfsFunction(vfsSync)
	methods.FxClose = vfsFunction(vfsClose)
	methods.FxRead = vfsFunction(vfsRead)
	vfsFault.files[file] = vfsFile{old, cp, flags}
	(*s.Tsqlite3_file)(vfsMemory(file)).FpMethods = cp
	return rc
}
func vfsSync(tls *libc.TLS, file uintptr, flags int32) int32 {
	state := vfsFault.files[file]
	wal := state.flags&s.SQLITE_OPEN_WAL != 0
	db := state.flags&s.SQLITE_OPEN_MAIN_DB != 0
	hit := vfsFault.armed && ((wal && (vfsFault.mode == "wal-sync" || vfsFault.mode == "wal-post-sync" || vfsFault.mode == "commit-cancel")) || (db && (vfsFault.mode == "checkpoint-sync" || vfsFault.mode == "auto-checkpoint-sync")))
	if hit {
		vfsFault.armed = false
		vfsFault.hit = true
		if vfsFault.mode == "commit-cancel" {
			vfsFault.cancel()
		} else if vfsFault.mode != "wal-post-sync" {
			return s.SQLITE_IOERR_FSYNC
		}
	}
	p := (*s.Tsqlite3_io_methods)(vfsMemory(state.original)).FxSync
	rc := (*(*func(*libc.TLS, uintptr, int32) int32)(unsafe.Pointer(&p)))(tls, file, flags)
	if hit && vfsFault.mode == "wal-post-sync" && rc == s.SQLITE_OK {
		return s.SQLITE_IOERR_FSYNC
	}
	return rc
}
func vfsRead(tls *libc.TLS, file, buf uintptr, n int32, offset int64) int32 {
	state := vfsFault.files[file]
	if vfsFault.mode == "late-read" && vfsFault.armed && state.flags&s.SQLITE_OPEN_MAIN_DB != 0 {
		vfsFault.armed = false
		vfsFault.hit = true
		return s.SQLITE_IOERR_READ
	}
	p := (*s.Tsqlite3_io_methods)(vfsMemory(state.original)).FxRead
	return (*(*func(*libc.TLS, uintptr, uintptr, int32, int64) int32)(unsafe.Pointer(&p)))(tls, file, buf, n, offset)
}
func vfsClose(tls *libc.TLS, file uintptr) int32 {
	state := vfsFault.files[file]
	p := (*s.Tsqlite3_io_methods)(vfsMemory(state.original)).FxClose
	(*s.Tsqlite3_file)(vfsMemory(file)).FpMethods = state.original
	rc := (*(*func(*libc.TLS, uintptr) int32)(unsafe.Pointer(&p)))(tls, file)
	libc.Xfree(tls, state.copy)
	delete(vfsFault.files, file)
	return rc
}
func installFaultVFS(t *testing.T) {
	t.Helper()
	if driverSQLite.FdatasyncEnabled() {
		t.Fatal("unqualified process-global sync policy")
	}
	tls := libc.NewTLS()
	name, err := libc.CString("unix")
	if err != nil {
		t.Fatal(err)
	}
	vfsFault.parent = s.Xsqlite3_vfs_find(tls, name)
	libc.Xfree(tls, name)
	if vfsFault.parent == 0 {
		t.Fatal("native unix VFS unavailable")
	}
	v := vfsAlloc(tls, unsafe.Sizeof(s.Tsqlite3_vfs{}))
	vfs := (*s.Tsqlite3_vfs)(vfsMemory(v))
	*vfs = *(*s.Tsqlite3_vfs)(vfsMemory(vfsFault.parent))
	vfs.FpNext = 0
	vfs.FzName, err = libc.CString("adapter-fault-qualification")
	if err != nil {
		t.Fatal(err)
	}
	vfs.FxOpen = vfsFunction(vfsOpen)
	vfsFault.files = make(map[uintptr]vfsFile)
	// Only the child changes the default; production DSNs/connectors are untouched.
	if rc := s.Xsqlite3_vfs_register(tls, v, 1); rc != s.SQLITE_OK {
		t.Fatalf("register VFS: %d", rc)
	}
	t.Cleanup(func() {
		if len(vfsFault.files) != 0 {
			t.Errorf("VFS wrappers leaked: %d", len(vfsFault.files))
		}
		s.Xsqlite3_vfs_register(tls, vfsFault.parent, 1)
		s.Xsqlite3_vfs_unregister(tls, v)
		libc.Xfree(tls, vfs.FzName)
		libc.Xfree(tls, v)
		tls.Close()
	})
}

// payloadFaultConnector arms native read failure only after the first payload
// was actually fetched. It does not manufacture the SQLite read error itself.
type payloadFaultConnector struct{ driver.Connector }

func (c payloadFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return payloadFaultConn{receiptConn: receiptConn{Conn: conn, ConnBeginTx: conn.(driver.ConnBeginTx), ConnPrepareContext: conn.(driver.ConnPrepareContext), ExecerContext: conn.(driver.ExecerContext), QueryerContext: conn.(driver.QueryerContext), Pinger: conn.(driver.Pinger)}}, nil
}

type payloadFaultConn struct{ receiptConn }

func (c payloadFaultConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.ConnBeginTx.BeginTx(ctx, o)
}
func (c payloadFaultConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.QueryerContext.QueryContext(ctx, q, args)
	if err != nil {
		return nil, err
	}
	if q == `SELECT payload FROM commits WHERE seq=?` && len(args) == 1 && args[0].Value == int64(1) {
		return payloadFaultRows{Rows: rows}, nil
	}
	return rows, nil
}

type payloadFaultRows struct{ driver.Rows }

func (r payloadFaultRows) Close() error {
	err := r.Rows.Close()
	if err == nil {
		vfsFault.firstPayload = true
		vfsFault.armed = true
	}
	return err
}

func TestAdapterVFSBoundaries(t *testing.T) {
	for _, mode := range []string{"wal-sync", "wal-post-sync", "commit-cancel", "checkpoint-sync", "auto-checkpoint-sync", "late-read"} {
		t.Run(mode, func(t *testing.T) {
			p := startAdapterProcess(t, t.TempDir(), history.NewHistoryID(), mode, "TestAdapterVFSHelper")
			if got := p.line(t); got != "qualified" {
				t.Fatalf("child boundary failed: %s", got)
			}
			p.wait(t)
		})
	}
}
func TestAdapterVFSHelper(t *testing.T) {
	mode := os.Getenv("MECATL_HISTORY_CHILD")
	if mode == "" {
		t.Skip("sub-executable only")
	}
	vfsFault.mode = mode
	installFaultVFS(t)
	root := os.Getenv("MECATL_HISTORY_ROOT")
	id, err := history.ParseHistoryID(os.Getenv("MECATL_HISTORY_ID"))
	if err != nil {
		t.Fatal(err)
	}
	b := newBackend(t, root)
	first := appendRoot(t, b, id)
	db := storedDatabase(t, b, id)
	var version string
	if err = db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil || version != "3.54.1" {
		t.Fatalf("unqualified SQLite: %s %v", version, err)
	}
	if mode == "late-read" {
		qualifyLateRead(t, b, id, first)
		fmt.Println("qualified")
		return
	}
	// Arm immediately before real driver COMMIT, after WAL header priming.
	var commits atomic.Int32
	target := false
	installReceipt(t, b, id, receiptConnector{commits: &commits, before: func() {
		if target && mode != "checkpoint-sync" {
			vfsFault.armed = true
		}
	}})
	db = storedDatabase(t, b, id)
	execSQL(t, db, `PRAGMA wal_checkpoint(TRUNCATE)`)
	second := proposal(2)
	if _, err = b.Append(t.Context(), id, first, second); err != nil {
		t.Fatal(err)
	}
	c := proposal(3)
	if mode == "auto-checkpoint-sync" {
		c = largeProposal(3, 5<<20)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	vfsFault.cancel = cancel
	target = true
	outcome, operationErr := b.Append(ctx, id, second.Ref, c)
	if commits.Load() != 2 {
		t.Fatalf("adapter retried write: %d", commits.Load())
	}
	if mode == "checkpoint-sync" {
		if operationErr != nil || outcome.Head != c.Ref {
			t.Fatalf("append: %+v %v", outcome, operationErr)
		}
		vfsFault.armed = true
		_, operationErr = db.ExecContext(t.Context(), `PRAGMA wal_checkpoint(FULL)`)
	}
	vfsFault.armed = false
	if !vfsFault.hit {
		t.Fatal("native fault boundary not reached")
	}
	var native *driverSQLite.Error
	switch mode {
	case "wal-sync", "wal-post-sync":
		if outcome != (history.Observation{}) || !errors.As(operationErr, &native) || native.Code() != s.SQLITE_IOERR_FSYNC {
			t.Fatalf("sync error: %+v %v", outcome, operationErr)
		}
	case "checkpoint-sync":
		if !errors.As(operationErr, &native) || native.Code() != s.SQLITE_IOERR_FSYNC {
			t.Fatalf("checkpoint error: %v", operationErr)
		}
	case "auto-checkpoint-sync", "commit-cancel":
		// SQLite may suppress automatic checkpoint errors after an accepted WAL
		// commit. The adapter must not reclassify that successful COMMIT as failure.
		if operationErr != nil || outcome.Head != c.Ref || outcome.Commit != c.Ref {
			t.Fatalf("accepted WAL commit: %+v %v", outcome, operationErr)
		}
		if mode == "commit-cancel" && ctx.Err() == nil {
			t.Fatal("caller was not canceled at native sync")
		}
	default:
		t.Fatal("unknown VFS mode")
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if len(vfsFault.files) != 0 {
		t.Fatal("native files leaked after operation failure/Close")
	}
	b = newBackend(t, root)
	// These injected WAL errors reject the row in this driver version; errors in
	// general still have unknown acceptance. Receipt loss is qualified separately.
	want := c
	if mode == "wal-sync" || mode == "wal-post-sync" {
		want = second
	}
	observed, err := b.Observe(t.Context(), id, 3)
	if err != nil || observed.Head != want.Ref {
		t.Fatalf("reopen: %+v %v", observed, err)
	}
	request := readRequest(want.Ref)
	request.MaxBytes = 6 << 20
	request.MaxCommitBytes = 6 << 20
	page, err := b.Read(t.Context(), id, request)
	if err != nil || len(page) != int(want.Ref.Sequence) || !bytes.Equal(page[len(page)-1].Bytes, want.Bytes) {
		t.Fatalf("reopened page: count=%d err=%v", len(page), err)
	}
	var integrity string
	if err = storedDatabase(t, b, id).QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %s %v", integrity, err)
	}
	next := proposal(want.Ref.Sequence + 1)
	if o, e := b.Append(t.Context(), id, want.Ref, next); e != nil || o.Head != next.Ref {
		t.Fatalf("failed operation retained lock: %+v %v", o, e)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if len(vfsFault.files) != 0 {
		t.Fatal("native file wrappers leaked")
	}
	fmt.Println("qualified")
}

func qualifyLateRead(t *testing.T, b *Backend, id history.HistoryID, first history.CommitRef) {
	// Put the second payload on overflow pages, then close/checkpoint and reopen
	// with a tiny cache, so its fetch reaches xRead after the first payload scan.
	db := storedDatabase(t, b, id)
	two := largeProposal(2, 256<<10)
	if _, err := b.Append(t.Context(), id, first, two); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(b.root, id.String(), databaseName)}
	u.RawQuery = url.Values{"mode": {"rw"}, "_txlock": {"immediate"}}.Encode()
	base, err := driverSQLite.NewConnector(u.String())
	if err != nil {
		t.Fatal(err)
	}
	replacement := sql.OpenDB(payloadFaultConnector{Connector: connector{Connector: base}})
	replacement.SetMaxOpenConns(1)
	replacement.SetMaxIdleConns(1)
	b.handles[id].db = replacement
	execSQL(t, replacement, `PRAGMA cache_size=1; PRAGMA mmap_size=0`)
	request := readRequest(two.Ref)
	request.MaxBytes = 1 << 20
	request.MaxCommitBytes = 1 << 20
	page, err := b.Read(t.Context(), id, request)
	var native *driverSQLite.Error
	if !vfsFault.firstPayload || !vfsFault.hit || page != nil || !errors.As(err, &native) || native.Code() != s.SQLITE_IOERR_READ {
		t.Fatalf("late native read failure: first=%t hit=%t count=%d err=%v", vfsFault.firstPayload, vfsFault.hit, len(page), err)
	}
	vfsFault.mode = ""
	vfsFault.armed = false
	// No failed transaction/pool/lock may remain after discarding the partial page.
	page, err = b.Read(t.Context(), id, request)
	if err != nil || len(page) != 2 || page[0].Ref != first || !bytes.Equal(page[0].Bytes, proposal(1).Bytes) || page[1].Ref != two.Ref || !bytes.Equal(page[1].Bytes, two.Bytes) {
		t.Fatalf("read retry: count=%d err=%v", len(page), err)
	}
	next := proposal(3)
	if o, e := b.Append(t.Context(), id, two.Ref, next); e != nil || o.Head != next.Ref {
		t.Fatalf("failure leaked write authority: %+v %v", o, e)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if len(vfsFault.files) != 0 {
		t.Fatal("native files retained after Close")
	}
}
