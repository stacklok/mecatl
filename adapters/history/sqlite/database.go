package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

const databaseName = "history.sqlite"
const applicationID = 0x4d484953 // MHIS
const schemaVersion = 1
const identitySchema = `CREATE TABLE identity (singleton INTEGER PRIMARY KEY CHECK(singleton=1), history TEXT NOT NULL CHECK(length(history)>0)) STRICT`
const commitsSchema = `CREATE TABLE commits (seq INTEGER PRIMARY KEY CHECK(seq>0), cid BLOB NOT NULL CHECK(length(cid)=36), payload BLOB NOT NULL CHECK(length(payload)>0)) STRICT`

// attach gates every first attachment, including existing-path reads, on a stable
// lock outside the published directory. Final-name existence is not readiness.
// Missing reads return before creating even a lock file.
func (b *Backend) attach(ctx context.Context, id history.HistoryID, create bool) (db *sql.DB, err error) {
	dir := filepath.Join(b.root, id.String())
	if _, err = os.Stat(dir); errors.Is(err, os.ErrNotExist) && !create { // #nosec G703 -- dir combines the trusted storage root with a typed HistoryID filename.
		return nil, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fault(err)
	}
	lock := flock.New(filepath.Join(b.root, id.String()+".lock"))
	defer func() {
		err = errors.Join(err, fault(lock.Close()))
		if err != nil && db != nil {
			err = errors.Join(err, fault(db.Close()))
			db = nil
		}
	}()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, fault(err)
	}
	if !locked {
		return nil, fault(ctx.Err())
	}
	if _, err = os.Stat(dir); errors.Is(err, os.ErrNotExist) { // #nosec G703 -- dir combines the trusted storage root with a typed HistoryID filename.
		if !create {
			return nil, nil
		}
		if err = b.publish(ctx, id, dir); err != nil {
			return nil, fault(err)
		}
	} else if err != nil {
		return nil, fault(err)
	}
	db, err = openDatabase(filepath.Join(dir, databaseName), false)
	if err != nil {
		return nil, fault(err)
	}
	if err = validateDatabase(ctx, db, id); err != nil {
		return db, fault(err)
	}
	// A publisher may have died after rename but before parent synchronization.
	if err = syncDir(dir); err != nil {
		return db, fault(err)
	}
	if err = syncDir(b.root); err != nil {
		return db, fault(err)
	}
	if err = ctx.Err(); err != nil {
		return db, err
	}
	return db, nil
}

// publish prepares an owned temporary sibling, closes SQLite before synchronizing
// its files, then publishes once. An existing final namespace is never replaced.
// All cooperating attachers hold the same stable flock, including after a crash.
func (b *Backend) publish(ctx context.Context, id history.HistoryID, final string) (err error) {
	temp, err := os.MkdirTemp(b.root, ".init-"+id.String()+"-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temp)) }() // #nosec G703 -- temp is a private sibling created beneath the trusted storage root.
	path := filepath.Join(temp, databaseName)
	db, err := openDatabase(path, true)
	if err != nil {
		return err
	}
	_, initErr := db.ExecContext(ctx, `PRAGMA application_id=1296582995; PRAGMA user_version=1; `+identitySchema+`; `+commitsSchema)
	if initErr == nil {
		_, initErr = db.ExecContext(ctx, `INSERT INTO identity VALUES(1,?)`, id.String())
	}
	if err = errors.Join(initErr, db.Close()); err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil { // #nosec G703 -- path is the fixed database filename beneath a private temporary directory created under the trusted root.
		return err
	}
	// Closing the sole connection checkpoints/removes WAL normally; synchronize
	// every remaining file rather than assuming a particular close/checkpoint path.
	files, err := os.ReadDir(temp)
	if err != nil {
		return err
	}
	for _, entry := range files {
		f, e := os.OpenFile(filepath.Join(temp, entry.Name()), os.O_RDWR, 0) // #nosec G703 -- temp is privately created beneath the trusted root and ReadDir names are local basenames.
		if e != nil {
			return e
		}
		if e = errors.Join(f.Sync(), f.Close()); e != nil {
			return e
		}
	}
	if err = syncDir(temp); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = os.Lstat(final); !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- final combines the trusted storage root with a typed HistoryID filename.
		if err != nil {
			return err
		}
		return history.ErrCorrupt
	}
	if err = os.Rename(temp, final); err != nil { // #nosec G703 -- both paths are private storage entries beneath the trusted root; final uses a typed HistoryID.
		return err
	}
	if b.afterPublish != nil {
		if err = b.afterPublish(); err != nil {
			return err
		}
	}
	return syncDir(b.root)
}

// validateDatabase recognizes only this exact schema and bounded identity. It
// never scans commits or runs integrity_check. Metadata lengths are checked in
// SQLite before the driver can allocate the governed text/blob values.
func validateDatabase(ctx context.Context, db *sql.DB, id history.HistoryID) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range []struct {
		query string
		want  int
	}{{"PRAGMA application_id", applicationID}, {"PRAGMA user_version", schemaVersion}} {
		var n int
		if err = tx.QueryRowContext(ctx, p.query).Scan(&n); err != nil {
			return err
		}
		if n != p.want {
			return history.ErrCorrupt
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&count); err != nil {
		return err
	}
	if count != 2 {
		return history.ErrCorrupt
	}
	for _, s := range []struct{ name, sql string }{{"identity", identitySchema}, {"commits", commitsSchema}} {
		var ok bool
		if err = tx.QueryRowContext(ctx, `SELECT type='table' AND tbl_name=? AND typeof(sql)='text' AND length(CAST(sql AS BLOB))=? FROM sqlite_schema WHERE name=?`, s.name, len(s.sql), s.name).Scan(&ok); err != nil {
			return errors.Join(history.ErrCorrupt, err)
		}
		if !ok {
			return history.ErrCorrupt
		}
		var text string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE name=?`, s.name).Scan(&text); err != nil {
			return err
		}
		if text != s.sql {
			return history.ErrCorrupt
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM identity`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return history.ErrCorrupt
	}
	var ok bool
	if err = tx.QueryRowContext(ctx, `SELECT typeof(singleton)='integer' AND singleton=1 AND typeof(history)='text' AND length(CAST(history AS BLOB))=? FROM identity`, len(id.String())).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return history.ErrCorrupt
	}
	var identity string
	if err = tx.QueryRowContext(ctx, `SELECT history FROM identity WHERE singleton=1`).Scan(&identity); err != nil {
		return err
	}
	if identity != id.String() {
		return history.ErrCorrupt
	}
	return tx.Commit()
}
