package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
func storedDatabase(t *testing.T, b *Backend, id history.HistoryID) *sql.DB {
	t.Helper()
	db, err := b.database(t.Context(), id, false)
	if err != nil || db == nil {
		t.Fatalf("database: %v", err)
	}
	return db
}

func TestSchemaAndIdentityFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		args      []any
	}{
		{"version", `PRAGMA user_version=2`, nil},
		{"application", `PRAGMA application_id=0`, nil},
		{"identity", `UPDATE identity SET history=?`, []any{history.NewHistoryID().String()}},
		{"missing identity", `DELETE FROM identity`, nil},
		{"missing table", `DROP TABLE commits`, nil},
		{"extra index", `CREATE INDEX unexpected ON commits(cid)`, nil},
		{"non-strict", `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=replace(sql,' STRICT','') WHERE name='commits'`, nil},
		{"different constraint", `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=replace(sql,'seq>0','seq>1') WHERE name='commits'`, nil},
		{"huge identity", `UPDATE identity SET history=hex(zeroblob(8388608))`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b := newBackend(t, root)
			id := history.NewHistoryID()
			ref := appendRoot(t, b, id)
			execSQL(t, storedDatabase(t, b, id), tc.sql, tc.args...)
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			b = newBackend(t, root)
			if o, err := b.Observe(t.Context(), id, 1); err == nil || o != (history.Observation{}) {
				t.Fatalf("observe: %+v %v", o, err)
			}
			if p, err := b.Read(t.Context(), id, readRequest(ref)); err == nil || p != nil {
				t.Fatalf("read: %+v %v", p, err)
			}
			if o, err := b.Append(t.Context(), id, ref, proposal(2)); err == nil || o != (history.Observation{}) {
				t.Fatalf("append: %+v %v", o, err)
			}
			if len(b.handles) != 0 {
				t.Fatal("invalid handle retained")
			}
		})
	}
}

func TestMalformedIndexedEvidence(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"CID length", `UPDATE commits SET cid=zeroblob(16777216) WHERE seq=2`},
		{"CID profile", `UPDATE commits SET cid=zeroblob(36) WHERE seq=2`},
		{"empty payload", `UPDATE commits SET payload=x'' WHERE seq=2`},
		{"hole", `DELETE FROM commits WHERE seq=1`},
		{"nonpositive", `UPDATE commits SET seq=0 WHERE seq=2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackend(t, t.TempDir())
			id := history.NewHistoryID()
			first := appendRoot(t, b, id)
			second := proposal(2)
			if _, err := b.Append(t.Context(), id, first, second); err != nil {
				t.Fatal(err)
			}
			db := storedDatabase(t, b, id)
			execSQL(t, db, `PRAGMA ignore_check_constraints=ON`)
			execSQL(t, db, tc.sql)
			if o, err := b.Observe(t.Context(), id, 2); err == nil || o != (history.Observation{}) {
				t.Fatalf("observe: %+v %v", o, err)
			}
			if page, err := b.Read(t.Context(), id, readRequest(second.Ref)); err == nil || page != nil {
				t.Fatalf("read: %+v %v", page, err)
			}
		})
	}
}

// corruptType writes values a STRICT table normally excludes, then restores the
// exact schema. This represents corrupt stored evidence, not a supported writer.
func corruptType(t *testing.T, root string, id history.HistoryID, sqlText string) {
	t.Helper()
	path := filepath.Join(root, id.String(), databaseName)
	db, err := openDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=replace(sql,' STRICT','')`)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, `PRAGMA ignore_check_constraints=ON`)
	execSQL(t, db, sqlText)
	execSQL(t, db, `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=sql || ' STRICT'`)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTypeAndLengthPreflightAllocation(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		openFault bool
	}{
		{"CID TEXT", `UPDATE commits SET cid=CAST(zeroblob(16777216) AS TEXT)`, false},
		{"payload TEXT", `UPDATE commits SET payload=CAST(zeroblob(16777216) AS TEXT)`, false},
		{"CID BLOB", `UPDATE commits SET cid=zeroblob(16777216)`, false},
		{"payload BLOB", `UPDATE commits SET payload=zeroblob(16777216)`, false},
		{"identity BLOB", `UPDATE identity SET history=zeroblob(16777216)`, true},
		{"identity TEXT", `UPDATE identity SET history=CAST(zeroblob(16777216) AS TEXT)`, true},
		{"schema SQL", `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=sql || ' /*' || hex(zeroblob(8388608)) || '*/' WHERE name='commits'`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			b := newBackend(t, root)
			id := history.NewHistoryID()
			ref := appendRoot(t, b, id)
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			corruptType(t, root, id, tc.sql)
			b = newBackend(t, root)
			if !tc.openFault {
				// Warm schema/connection allocations outside the operation measurement.
				if _, err := b.database(t.Context(), id, false); err != nil {
					t.Fatal(err)
				}
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			page, err := b.Read(t.Context(), id, readRequest(ref))
			runtime.ReadMemStats(&after)
			if err == nil || page != nil {
				t.Fatalf("read: %+v %v", page, err)
			}
			if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
				t.Fatalf("preflight allocated %d", n)
			}
			if strings.Contains(err.Error(), "000000") {
				t.Fatal("error leaked content")
			}
		})
	}
}

func TestReadFaultDiscardsPrefixAndChecksEndpoints(t *testing.T) {
	b := newBackend(t, t.TempDir())
	id := history.NewHistoryID()
	first := appendRoot(t, b, id)
	second := proposal(2)
	if _, err := b.Append(t.Context(), id, first, second); err != nil {
		t.Fatal(err)
	}
	db := storedDatabase(t, b, id)
	execSQL(t, db, `PRAGMA ignore_check_constraints=ON; UPDATE commits SET cid=zeroblob(36) WHERE seq=2`)
	r := readRequest(second.Ref)
	r.MaxCommits = 1
	if page, err := b.Read(t.Context(), id, r); !errors.Is(err, history.ErrCorrupt) || page != nil {
		t.Fatalf("prefix hid invalid endpoint: %+v %v", page, err)
	}
	r.After = second.Ref
	if page, err := b.Read(t.Context(), id, r); !errors.Is(err, history.ErrCorrupt) || page != nil {
		t.Fatalf("EOF hid endpoint: %+v %v", page, err)
	}
}

func TestPolicyOrderingBeforePayloadFetch(t *testing.T) {
	b := newBackend(t, t.TempDir())
	id := history.NewHistoryID()
	first := appendRoot(t, b, id)
	second := proposal(2)
	second.Bytes = []byte{0x43, 1, 2, 3}
	if _, err := b.Append(t.Context(), id, first, second); err != nil {
		t.Fatal(err)
	}
	r := readRequest(second.Ref)
	r.MaxCommitBytes = 3
	r.MaxBytes = 2
	if page, err := b.Read(context.Background(), id, r); !errors.Is(err, history.ErrInvalid) || page != nil {
		t.Fatalf("per-commit cap must precede prefix budget: %+v %v", page, err)
	}
}
