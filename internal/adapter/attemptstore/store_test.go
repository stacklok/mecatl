package attemptstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/attemptconformance"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestStoreConformance(t *testing.T) {
	attemptconformance.Run(t, func(t *testing.T) attemptconformance.Harness {
		t.Helper()
		store, err := New(filepath.Join(t.TempDir(), "attempts"))
		if err != nil {
			t.Fatal(err)
		}
		return attemptconformance.Harness{
			Repository: store,
			SetNow:     func(now time.Time) { store.now = func() time.Time { return now } },
		}
	})
}

func TestStoreReopensCommittedAttempts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempts")
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	partition, create := storeFixture(t, "reopen")
	created, err := store.Create(context.Background(), partition, create)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.Get(context.Background(), partition, created.ID)
	if err != nil || !found || got != created {
		t.Fatalf("reopened Get = %+v, found=%v, err=%v; want %+v", got, found, err, created)
	}
}

func TestConcurrentStoreInstancesPreserveCAS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempts")
	first, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	partition, create := storeFixture(t, "concurrent")
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	first.now = func() time.Time { return now }
	second.now = func() time.Time { return now }
	created, err := first.Create(context.Background(), partition, create)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			_, _, claimErr := store.AcquireClaim(context.Background(), partition, created.ID, created.Version, time.Minute)
			errs <- claimErr
		}(store)
	}
	wg.Wait()
	close(errs)

	succeeded, conflicted := 0, 0
	for claimErr := range errs {
		switch {
		case claimErr == nil:
			succeeded++
		case errors.Is(claimErr, learning.ErrAttemptVersionConflict):
			conflicted++
		default:
			t.Fatalf("concurrent AcquireClaim error = %v", claimErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent CAS outcomes: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestRenameFailurePreservesPriorDocument(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempts")
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	partition, firstCreate := storeFixture(t, "first")
	first, err := store.Create(context.Background(), partition, firstCreate)
	if err != nil {
		t.Fatal(err)
	}
	_, secondCreate := storeFixture(t, "second")
	store.rename = func(string, string) error { return errors.New("injected rename failure") }
	if _, err = store.Create(context.Background(), partition, secondCreate); err == nil {
		t.Fatal("Create succeeded despite rename failure")
	}

	reopened, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.Get(context.Background(), partition, first.ID)
	if err != nil || !found || got != first {
		t.Fatalf("prior record after failed rename = %+v, found=%v, err=%v", got, found, err)
	}
	if _, found, err = reopened.Get(context.Background(), partition, secondCreate.ID); err != nil || found {
		t.Fatalf("uncommitted record found=%v, err=%v", found, err)
	}
}

func TestStoreRejectsSymlinkDocument(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempts")
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.json")
	if err = os.WriteFile(target, []byte(`{"format":"mecatl-attemptstore/1","partitions":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, store.path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	partition, _ := storeFixture(t, "symlink")
	if _, err = store.List(context.Background(), partition, learning.AttemptList{}); err == nil {
		t.Fatal("List followed a symlinked attempt document")
	}
}

func storeFixture(t *testing.T, suffix string) (learning.AttemptPartition, learning.AttemptCreate) {
	t.Helper()
	partition, err := learning.DeriveAttemptPartition("owner-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	digest := learning.CanonicalDigest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	source := learning.AttemptSource{SessionID: session.SessionID("session-" + suffix), RunID: learning.DurableRunID("run-" + suffix), CanonicalDigest: digest}
	prompt := learning.CurrentPromptBinding{Ordinal: 1, Digest: digest, Origin: learning.PromptOriginCurrentPrincipal}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHard, source, prompt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := learning.DeterministicAttemptID("owner-"+suffix, source)
	if err != nil {
		t.Fatal(err)
	}
	return partition, learning.AttemptCreate{ID: id, Provenance: provenance}
}
