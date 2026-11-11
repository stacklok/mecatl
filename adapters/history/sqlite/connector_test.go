package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestCanceledPhysicalSetupCanRetry(t *testing.T) {
	root := t.TempDir()
	b := newBackend(t, root)
	id := history.NewHistoryID()
	ref := appendRoot(t, b, id)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	owner, err := openDatabase(filepath.Join(root, id.String(), databaseName), false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	execSQL(t, owner, `PRAGMA locking_mode=EXCLUSIVE`)
	tx, err := owner.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	reader := newBackend(t, root)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	if o, err := reader.Observe(ctx, id, 1); !errors.Is(err, context.DeadlineExceeded) || o != (history.Observation{}) {
		t.Fatalf("setup wait: %+v %v", o, err)
	}
	if len(reader.handles) != 0 {
		t.Fatal("failed setup retained a handle")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = owner.Close(); err != nil {
		t.Fatal(err)
	}
	if o, err := reader.Observe(t.Context(), id, 1); err != nil || o.Head != ref {
		t.Fatalf("retry: %+v %v", o, err)
	}
}

func TestOperationBudget(t *testing.T) {
	b := newBackend(t, t.TempDir())
	id := history.NewHistoryID()
	start := time.Now()
	ctx, done, err := b.admit(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(start.Add(4900*time.Millisecond)) || deadline.After(time.Now().Add(5*time.Second)) {
		t.Fatalf("default deadline: %v", deadline)
	}
	earlier, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	early, finish, err := b.admit(earlier, id)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	want, _ := earlier.Deadline()
	got, _ := early.Deadline()
	if got != want {
		t.Fatalf("raised caller deadline: %v vs %v", got, want)
	}
}
