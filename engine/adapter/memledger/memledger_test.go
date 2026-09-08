package memledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/ledgerconformance"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestMemledgerConformance runs the shared ReadLedger conformance table
// against the in-memory reference ledger.
func TestMemledgerConformance(t *testing.T) {
	ledgerconformance.Run(t, func(*testing.T) tool.ReadLedger {
		return memledger.New()
	})
}

func TestPersistentReadLedgers_InvalidVersionRejected(t *testing.T) {
	ledger := memledger.New()
	if err := ledger.RecordRead(context.Background(), "invalid.txt", tool.FileVersion{}); !errors.Is(err, tool.ErrInvalidFileVersion) {
		t.Fatalf("RecordRead(zero) error = %v, want ErrInvalidFileVersion", err)
	}
	if _, ok, err := ledger.RecordedVersion(context.Background(), "invalid.txt"); err != nil || ok {
		t.Fatalf("RecordedVersion after rejected zero = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

// TestInvariant_persistent_read_ledger_exact_version pins AC1.1 (ADR 0294):
// recording a read stores the EXACT opaque FileVersion supplied by the
// corresponding version-bearing read, and a lookup returns that SAME valid
// token — without touching file content. This is ADR-0208's no-I/O evidence
// contract, restated for the storage-independent ledger seam: the ledger never
// re-derives a version, it only stores and returns the caller's token
// byte-for-byte (compared via FileVersion.Equal, since the token is opaque).
func TestInvariant_persistent_read_ledger_exact_version(t *testing.T) {
	ctx := context.Background()
	l := memledger.New()

	// The exact token a version-bearing read (ReadVersion) would have minted.
	want := tool.NewFileVersion("sha256:deadbeef-exact-opaque-token")
	if err := l.RecordRead(ctx, "dir/file.txt", want); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}

	got, ok, err := l.RecordedVersion(ctx, "dir/file.txt")
	if err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	}
	if !ok {
		t.Fatal("RecordedVersion ok = false, want true after RecordRead")
	}
	if !got.Equal(want) {
		t.Fatalf("RecordedVersion returned a token that does not equal the recorded one")
	}
	gotToken, gotErr := tool.EncodeFileVersion(got)
	wantToken, wantErr := tool.EncodeFileVersion(want)
	if gotErr != nil || wantErr != nil || gotToken != wantToken {
		t.Fatalf("RecordedVersion encoded token = (%q, err=%v), want EXACTLY the recorded (%q, err=%v)",
			gotToken, gotErr, wantToken, wantErr)
	}
}

// TestInvariant_persistent_read_ledger_lexical_key pins AC1.2 (ADR 0294):
// relative and ordinary in-root absolute spellings of the same file converge to
// ONE ledger entry through the EXISTING I/O-free tool.LedgerKey normalization —
// record by one spelling, look up by the other, with no filesystem inspection.
// A physical symlink alias is deliberately NOT exercised here (tool.LedgerKey's
// own test suite, engine/tool/ledgerkey_test.go, documents that it may
// conservatively miss); this test pins only that the composed
// ledger-key-then-ReadLedger path preserves the lexical convergence tool.LedgerKey
// already guarantees.
func TestInvariant_persistent_read_ledger_lexical_key(t *testing.T) {
	ctx := context.Background()
	l := memledger.New()

	const root = "/ws"
	rel := "dir/file.txt"
	abs := root + "/dir/file.txt"
	absDirty := root + "/deep/../dir/file.txt"

	relKey := tool.LedgerKey(root, rel)
	absKey := tool.LedgerKey(root, abs)
	absDirtyKey := tool.LedgerKey(root, absDirty)
	if relKey != absKey || relKey != absDirtyKey {
		t.Fatalf("tool.LedgerKey did not converge relative/absolute/dirty-absolute forms: %q, %q, %q", relKey, absKey, absDirtyKey)
	}

	want := tool.NewFileVersion("lexical-convergence-token")
	// Record by the RELATIVE key, look up by the ABSOLUTE key (post-LedgerKey
	// normalization, as a Workspace adapter would apply before calling the
	// ledger).
	if err := l.RecordRead(ctx, relKey, want); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	got, ok, err := l.RecordedVersion(ctx, absKey)
	if err != nil {
		t.Fatalf("RecordedVersion(absKey): %v", err)
	}
	if !ok || !got.Equal(want) {
		t.Fatalf("record by relative / lookup by absolute did not converge (ok=%v)", ok)
	}

	// And the reverse direction: record by absolute, look up by relative.
	l2 := memledger.New()
	if err := l2.RecordRead(ctx, absKey, want); err != nil {
		t.Fatalf("RecordRead(absKey): %v", err)
	}
	got, ok, err = l2.RecordedVersion(ctx, relKey)
	if err != nil {
		t.Fatalf("RecordedVersion(relKey): %v", err)
	}
	if !ok || !got.Equal(want) {
		t.Fatalf("record by absolute / lookup by relative did not converge (ok=%v)", ok)
	}
}
