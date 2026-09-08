package fstools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
)

// ledger_failure_test.go pins the Scenario 3 fail-closed acceptance criteria
// (docs/adr/0290): a failed RecordRead, an unavailable/corrupt RecordedVersion
// lookup, and a post-mutation RecordRead failure must all fail closed without
// ever weakening the final ReplaceFile/CreateFile CAS.

var (
	// errSimulatedLedgerRecord is a sentinel a fake ReadLedger returns from
	// RecordRead to simulate a genuine ledger storage/write failure.
	errSimulatedLedgerRecord = errors.New("simulated ledger record failure")
	// errSimulatedLedgerLookup is a sentinel a fake ReadLedger returns from
	// RecordedVersion to simulate an unavailable/corrupt ledger lookup.
	errSimulatedLedgerLookup = errors.New("simulated ledger lookup failure")
)

// ledgerFailureWorkspace wraps a real Workspace so tests can observe whether
// final CAS mutation was reached. Ledger failures are injected separately via
// ledgerFailureLedger, matching the production Environment capability split.
type ledgerFailureWorkspace struct {
	tool.Workspace
	replaceFileCalled bool
	createFileCalled  bool
}

type ledgerFailureLedger struct {
	base tool.ReadLedger

	recordReadErr    error
	recordReadSticky bool
	lookupErr        error
}

func (l *ledgerFailureLedger) RecordRead(ctx context.Context, key string, version tool.FileVersion) error {
	if l.recordReadErr != nil {
		err := l.recordReadErr
		if !l.recordReadSticky {
			l.recordReadErr = nil
		}
		return err
	}
	return l.base.RecordRead(ctx, key, version)
}

func (l *ledgerFailureLedger) RecordedVersion(ctx context.Context, key string) (tool.FileVersion, bool, error) {
	if l.lookupErr != nil {
		return tool.FileVersion{}, false, l.lookupErr
	}
	return l.base.RecordedVersion(ctx, key)
}

func (w *ledgerFailureWorkspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	w.replaceFileCalled = true
	return w.Workspace.ReplaceFile(ctx, path, old, data)
}

func (w *ledgerFailureWorkspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	w.createFileCalled = true
	return w.Workspace.CreateFile(ctx, path, data)
}

// TestPersistentReadLedgers_Scenario3_ReadRecordFailureFailsClosed pins AC3.1:
// if recording a successful Read fails, the tool reports that no new evidence
// was retained. This fixture has no older evidence, so a later mutation is
// refused until a Read is recorded successfully.
func TestPersistentReadLedgers_Scenario3_ReadRecordFailureFailsClosed(t *testing.T) {
	base := memfs.NewWorkspace("/")
	seed(t, base, "a.txt", "hello\n")
	ws := &ledgerFailureWorkspace{Workspace: base}
	ledger := &ledgerFailureLedger{base: ledgerFor(base), recordReadErr: errSimulatedLedgerRecord}

	// The Read itself succeeded (content is readable) but the record failed:
	// the tool must report BOTH facts.
	res := execWithLedger(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws, ledger)
	if !res.IsError {
		t.Fatalf("Read with a failed RecordRead must be a tool error, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hello") && !strings.Contains(res.Content, "read") {
		t.Fatalf("Read-record-failure result should still be informative about the read: %q", res.Content)
	}
	if !strings.Contains(res.Content, "not retained") && !strings.Contains(res.Content, "failed to retain") {
		t.Fatalf("Read-record-failure result must say the evidence was not retained: %q", res.Content)
	}

	// A later Edit is refused: no valid evidence was ever recorded.
	editRes := execWithLedger(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "hello", "new_string": "hi",
	}), ws, ledger)
	if !editRes.IsError {
		t.Fatal("Edit after a failed RecordRead must be refused (no evidence was retained)")
	}

	// Once the ledger failure clears and a Read succeeds, Edit is authorized.
	ledger.recordReadErr = nil
	execWithLedger(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws, ledger)
	editRes = execWithLedger(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "hello", "new_string": "hi",
	}), ws, ledger)
	if editRes.IsError {
		t.Fatalf("Edit after a SUCCESSFUL Read-record should succeed, got: %s", editRes.Content)
	}

	// A failed refresh does not invalidate older matching evidence. The first
	// Read records the current version; the second Read sees unchanged bytes but
	// fails to persist its equivalent token. Normal equality still authorizes
	// the following Edit, and final ReplaceFile CAS remains the race guard.
	base = memfs.NewWorkspace("/")
	seed(t, base, "same.txt", "same\n")
	ws = &ledgerFailureWorkspace{Workspace: base}
	ledger = &ledgerFailureLedger{base: ledgerFor(base)}
	execWithLedger(t, ReadTool{}, call(t, "Read", map[string]any{"path": "same.txt"}), ws, ledger)
	ledger.recordReadErr = errSimulatedLedgerRecord
	failedRefresh := execWithLedger(t, ReadTool{}, call(t, "Read", map[string]any{"path": "same.txt"}), ws, ledger)
	if !failedRefresh.IsError {
		t.Fatal("Read refresh with a failed RecordRead must report the failure")
	}
	editRes = execWithLedger(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "same.txt", "old_string": "same", "new_string": "changed",
	}), ws, ledger)
	if editRes.IsError {
		t.Fatalf("Edit with older matching evidence should succeed, got: %s", editRes.Content)
	}
}

// TestPersistentReadLedgers_Scenario3_LookupFailurePreventsMutation pins
// AC3.2: an unavailable or corrupt ledger lookup refuses Edit and
// existing-file Write BEFORE ReplaceFile is ever called; it must never be
// treated as an unrecorded-but-otherwise-authorized read.
func TestPersistentReadLedgers_Scenario3_LookupFailurePreventsMutation(t *testing.T) {
	t.Run("Edit", func(t *testing.T) {
		base := memfs.NewWorkspace("/")
		seed(t, base, "a.txt", "hello\n")
		exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)

		ws := &ledgerFailureWorkspace{Workspace: base}
		ledger := &ledgerFailureLedger{base: ledgerFor(base), lookupErr: errSimulatedLedgerLookup}
		res := execWithLedger(t, EditTool{}, call(t, "Edit", map[string]any{
			"path": "a.txt", "old_string": "hello", "new_string": "hi",
		}), ws, ledger)
		if !res.IsError {
			t.Fatal("Edit with an unavailable ledger lookup must be refused")
		}
		if ws.replaceFileCalled {
			t.Fatal("Edit must refuse BEFORE ReplaceFile is ever called on a lookup failure")
		}
	})

	t.Run("Write", func(t *testing.T) {
		base := memfs.NewWorkspace("/")
		seed(t, base, "a.txt", "old\n")
		exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)

		ws := &ledgerFailureWorkspace{Workspace: base}
		ledger := &ledgerFailureLedger{base: ledgerFor(base), lookupErr: errSimulatedLedgerLookup}
		res := execWithLedger(t, WriteTool{}, call(t, "Write", map[string]any{
			"path": "a.txt", "content": "new\n",
		}), ws, ledger)
		if !res.IsError {
			t.Fatal("Write with an unavailable ledger lookup must be refused")
		}
		if ws.replaceFileCalled {
			t.Fatal("Write must refuse BEFORE ReplaceFile is ever called on a lookup failure")
		}
	})
}

// TestPersistentReadLedgers_Scenario3_CreateOnlyUnchanged pins AC3.5: new-file
// Write remains create-only and does not require a ledger entry (no prior Read
// needed); concurrent creators still produce exactly one winner (the loser
// gets a model-visible create-conflict refusal, and the winner's content
// survives).
func TestPersistentReadLedgers_Scenario3_CreateOnlyUnchanged(t *testing.T) {
	// No prior Read is needed for a brand-new file, even with an armed ledger
	// wrapper that would fail any RecordedVersion lookup — proving Write's
	// create path never consults the ledger before CreateFile.
	base := memfs.NewWorkspace("/")
	ws := &ledgerFailureWorkspace{Workspace: base}
	ledger := &ledgerFailureLedger{base: ledgerFor(base), lookupErr: errSimulatedLedgerLookup}
	res := execWithLedger(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "new.txt", "content": "fresh\n",
	}), ws, ledger)
	if res.IsError {
		t.Fatalf("Write of a new file must not consult the ledger lookup, got: %s", res.Content)
	}
	data, err := base.Read(context.Background(), "new.txt")
	if err != nil || string(data) != "fresh\n" {
		t.Fatalf("new file content = (%q, %v), want (\"fresh\\n\", nil)", data, err)
	}

	// Concurrent creators: exactly one winner (mirrors
	// TestWriteCreateOnlyRejectsConcurrentCreate's proof under the revised seam).
	baseConcurrent := memfs.NewWorkspace("/")
	cws := &createConflictWorkspace{Workspace: baseConcurrent}
	res = exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "race.txt", "content": "agent\n",
	}), cws)
	if !res.IsError || !strings.Contains(res.Content, "already exists") {
		t.Fatalf("concurrent create result = (error=%v, content=%q), want model-visible create refusal", res.IsError, res.Content)
	}
	final, err := baseConcurrent.Read(context.Background(), "race.txt")
	if err != nil {
		t.Fatalf("Read final file: %v", err)
	}
	if string(final) != "concurrent\n" {
		t.Fatalf("final file = %q, want the concurrent create preserved (exactly one winner)", final)
	}
}

// TestPersistentReadLedgers_Scenario3_PostMutationRecordFailure pins AC3.6: if
// persisting the new version after a successful create or replace fails, the
// tool reports BOTH the successful mutation and the record failure without
// rollback. In these fixtures the content changed, so older evidence is stale
// and the next existing-file mutation is refused by normal version equality.
func TestPersistentReadLedgers_Scenario3_PostMutationRecordFailure(t *testing.T) {
	t.Run("Edit", func(t *testing.T) {
		base := memfs.NewWorkspace("/")
		seed(t, base, "a.txt", "hello\n")
		exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)

		ws := &ledgerFailureWorkspace{Workspace: base}
		ledger := &ledgerFailureLedger{base: ledgerFor(base), recordReadErr: errSimulatedLedgerRecord}

		res := execWithLedger(t, EditTool{}, call(t, "Edit", map[string]any{
			"path": "a.txt", "old_string": "hello", "new_string": "hi",
		}), ws, ledger)
		if !res.IsError {
			t.Fatalf("Edit must report the record failure as an error result, got: %s", res.Content)
		}
		// The mutation ALREADY SUCCEEDED — no rollback.
		data, err := base.Read(context.Background(), "a.txt")
		if err != nil || string(data) != "hi\n" {
			t.Fatalf("file content after post-mutation record failure = (%q, %v), want (\"hi\\n\", nil) — no rollback", data, err)
		}
		if !strings.Contains(res.Content, "edited") && !strings.Contains(res.Content, "replaced") {
			t.Fatalf("result must report the successful edit despite the record failure: %q", res.Content)
		}
		if !strings.Contains(res.Content, "not retained") && !strings.Contains(res.Content, "failed to retain") {
			t.Fatalf("result must report the record failure: %q", res.Content)
		}

		// The next existing-file mutation is refused: no valid evidence exists.
		next := execWithLedger(t, EditTool{}, call(t, "Edit", map[string]any{
			"path": "a.txt", "old_string": "hi", "new_string": "bye",
		}), ws, ledger)
		if !next.IsError {
			t.Fatal("the next Edit must be refused until another successful Read records evidence")
		}
	})

	t.Run("Write overwrite", func(t *testing.T) {
		base := memfs.NewWorkspace("/")
		seed(t, base, "a.txt", "old\n")
		exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)

		ws := &ledgerFailureWorkspace{Workspace: base}
		ledger := &ledgerFailureLedger{base: ledgerFor(base), recordReadErr: errSimulatedLedgerRecord}
		res := execWithLedger(t, WriteTool{}, call(t, "Write", map[string]any{
			"path": "a.txt", "content": "new\n",
		}), ws, ledger)
		if !res.IsError {
			t.Fatalf("Write overwrite must report the record failure as an error result, got: %s", res.Content)
		}
		data, err := base.Read(context.Background(), "a.txt")
		if err != nil || string(data) != "new\n" {
			t.Fatalf("file content after post-mutation record failure = (%q, %v), want (\"new\\n\", nil) — no rollback", data, err)
		}
		if !strings.Contains(res.Content, "overwrote") {
			t.Fatalf("result must report the successful overwrite despite the record failure: %q", res.Content)
		}

		next := execWithLedger(t, WriteTool{}, call(t, "Write", map[string]any{
			"path": "a.txt", "content": "again\n",
		}), ws, ledger)
		if !next.IsError {
			t.Fatal("the next Write overwrite must be refused until another successful Read records evidence")
		}
	})

	t.Run("Write create", func(t *testing.T) {
		base := memfs.NewWorkspace("/")
		ws := &ledgerFailureWorkspace{Workspace: base}
		ledger := &ledgerFailureLedger{base: ledgerFor(base), recordReadErr: errSimulatedLedgerRecord}
		res := execWithLedger(t, WriteTool{}, call(t, "Write", map[string]any{
			"path": "new.txt", "content": "fresh\n",
		}), ws, ledger)
		if !res.IsError {
			t.Fatalf("Write create must report the record failure as an error result, got: %s", res.Content)
		}
		data, err := base.Read(context.Background(), "new.txt")
		if err != nil || string(data) != "fresh\n" {
			t.Fatalf("file content after post-create record failure = (%q, %v), want (\"fresh\\n\", nil) — no rollback", data, err)
		}
		if !strings.Contains(res.Content, "wrote") {
			t.Fatalf("result must report the successful create despite the record failure: %q", res.Content)
		}

		// A later Edit on this now-existing file is refused: no valid evidence.
		next := execWithLedger(t, EditTool{}, call(t, "Edit", map[string]any{
			"path": "new.txt", "old_string": "fresh", "new_string": "stale",
		}), ws, ledger)
		if !next.IsError {
			t.Fatal("Edit of the newly-created file must be refused until a successful Read records evidence")
		}
	})
}
