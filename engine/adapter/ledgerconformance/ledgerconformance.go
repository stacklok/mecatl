// Package ledgerconformance provides a shared conformance test suite for the
// tool.ReadLedger interface (ADR 0281). Adapters (the in-memory reference
// memledger, a durable Redis-backed implementation, ...) call Run with a
// factory that constructs a fresh ledger, and the suite exercises only the
// tool.ReadLedger interface.
//
// Importing "testing" in a non-_test.go file is intentional here: this is a
// test-helper package whose sole purpose is to be imported by adapter tests,
// the conventional Go pattern for shared conformance suites (cf. testing/fstest
// and the sibling fsconformance/memconformance packages).
//
// The suite pins the CONTRACT — exact-token round trip, ordinary absence, and
// concurrent access — not the implementation: durability, storage encoding, and
// failure-classification detail beyond "absence vs error" are adapter-internal
// and deliberately NOT asserted here (a durable adapter's own tests cover its
// failure classification, e.g. TestPersistentReadLedgers_Scenario2_*).
package ledgerconformance

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// Run executes the shared ReadLedger conformance table against the ledger
// produced by newLedger. newLedger must return a fresh, isolated ledger each
// call.
func Run(t *testing.T, newLedger func(t *testing.T) tool.ReadLedger) {
	t.Helper()
	ctx := context.Background()

	t.Run("invalid zero version is rejected", func(t *testing.T) {
		l := newLedger(t)
		if err := l.RecordRead(ctx, "invalid.txt", tool.FileVersion{}); !errors.Is(err, tool.ErrInvalidFileVersion) {
			t.Fatalf("RecordRead(zero) error = %v, want ErrInvalidFileVersion", err)
		}
		got, ok, err := l.RecordedVersion(ctx, "invalid.txt")
		if err != nil || ok {
			t.Fatalf("RecordedVersion after rejected zero = (%v, %v, %v), want (zero, false, nil)", got, ok, err)
		}
	})

	t.Run("record and lookup round trip exact token", func(t *testing.T) {
		l := newLedger(t)
		ver := tool.NewFileVersion("exact-token-123")
		if err := l.RecordRead(ctx, "a.txt", ver); err != nil {
			t.Fatalf("RecordRead: %v", err)
		}
		got, ok, err := l.RecordedVersion(ctx, "a.txt")
		if err != nil {
			t.Fatalf("RecordedVersion: %v", err)
		}
		if !ok {
			t.Fatal("RecordedVersion ok = false, want true after RecordRead")
		}
		if !got.Equal(ver) {
			t.Fatalf("RecordedVersion returned a different token than recorded")
		}
	})

	t.Run("unrecorded key is absent, not an error", func(t *testing.T) {
		l := newLedger(t)
		got, ok, err := l.RecordedVersion(ctx, "never.txt")
		if err != nil {
			t.Fatalf("RecordedVersion(never-recorded) err = %v, want nil (absence is not an error)", err)
		}
		if ok {
			t.Fatalf("RecordedVersion(never-recorded) ok = true (version=%v), want false", got)
		}
	})

	t.Run("overwrite replaces the recorded token", func(t *testing.T) {
		l := newLedger(t)
		first := tool.NewFileVersion("v1")
		second := tool.NewFileVersion("v2")
		if err := l.RecordRead(ctx, "k.txt", first); err != nil {
			t.Fatalf("RecordRead #1: %v", err)
		}
		if err := l.RecordRead(ctx, "k.txt", second); err != nil {
			t.Fatalf("RecordRead #2: %v", err)
		}
		got, ok, err := l.RecordedVersion(ctx, "k.txt")
		if err != nil || !ok {
			t.Fatalf("RecordedVersion after overwrite = (ok=%v, err=%v), want (true, nil)", ok, err)
		}
		if !got.Equal(second) {
			t.Fatal("RecordedVersion after overwrite did not return the SECOND recorded token")
		}
	})

	t.Run("distinct keys are independent", func(t *testing.T) {
		l := newLedger(t)
		verA := tool.NewFileVersion("a-token")
		verB := tool.NewFileVersion("b-token")
		if err := l.RecordRead(ctx, "a.txt", verA); err != nil {
			t.Fatalf("RecordRead(a): %v", err)
		}
		if err := l.RecordRead(ctx, "b.txt", verB); err != nil {
			t.Fatalf("RecordRead(b): %v", err)
		}
		gotA, okA, errA := l.RecordedVersion(ctx, "a.txt")
		gotB, okB, errB := l.RecordedVersion(ctx, "b.txt")
		if errA != nil || errB != nil || !okA || !okB {
			t.Fatalf("lookups = (okA=%v errA=%v) (okB=%v errB=%v), want both found with no error", okA, errA, okB, errB)
		}
		if !gotA.Equal(verA) || !gotB.Equal(verB) {
			t.Fatal("distinct keys returned each other's tokens")
		}
	})

	t.Run("concurrent record and lookup is race-free", func(t *testing.T) {
		l := newLedger(t)
		var wg sync.WaitGroup
		const n = 20
		wg.Add(n * 2)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				_ = l.RecordRead(ctx, "race.txt", tool.NewFileVersion("v"))
			}()
			go func() {
				defer wg.Done()
				_, _, _ = l.RecordedVersion(ctx, "race.txt")
			}()
		}
		wg.Wait()
		// The race detector (task test -race) is the real oracle here; assert
		// only that the ledger converged on SOME complete, valid token rather
		// than a torn/partial one.
		got, ok, err := l.RecordedVersion(ctx, "race.txt")
		if err != nil {
			t.Fatalf("RecordedVersion after race: %v", err)
		}
		if ok {
			if _, err := tool.EncodeFileVersion(got); err != nil {
				t.Fatal("RecordedVersion after race returned an invalid (torn) FileVersion")
			}
		}
	})
}
