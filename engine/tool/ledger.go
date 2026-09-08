package tool

import (
	"context"
	"errors"
)

// ErrLedgerUnavailable is a sentinel a ReadLedger implementation MAY wrap when
// its RecordedVersion lookup cannot be trusted to answer — the storage is
// unreachable, timed out, or the stored record is corrupt/undecodable. It is
// DISTINCT from absence (no entry for the key): callers (the built-in Edit and
// existing-file Write tools) MUST fail closed on it, never treating an
// unavailable/corrupt lookup as an unrecorded-but-otherwise-authorized read.
// Wrapping it is optional — any non-nil error from RecordedVersion carries the
// same fail-closed obligation — but implementations that KNOW they hit this
// exact class of failure should wrap it so callers/tests can classify with
// errors.Is.
var ErrLedgerUnavailable = errors.New("tool: read ledger unavailable")

// ReadLedger is the session-bound, storage-selectable read-before-write
// evidence capability (ADR 0294). Environment composes exactly one ReadLedger
// with one independently selected Workspace content backend. Two Environments
// over the SAME Workspace may carry ISOLATED ReadLedgers (session scoping happens
// at ledger construction/selection, not by inspecting the key), and a durable
// ledger (for example Redis-backed) may accompany content stored elsewhere.
//
// Operations are context-aware and distinguish exactly three outcomes:
//
//  1. a valid recorded FileVersion (ok=true, err=nil);
//  2. no entry for key — normal absence (ok=false, err=nil); and
//  3. a storage, decode, or corruption error (err non-nil) — DISTINCT from
//     absence; callers must fail closed rather than treating it as case 2.
//
// A ReadLedger stores the EXACT opaque token a caller supplies (the version a
// corresponding version-bearing read minted) and performs NO file-content I/O
// and NO physical-alias resolution of its own. The file tools apply the existing
// I/O-free LedgerKey normalization using their Environment's Workspace root
// BEFORE calling into the ledger, so an implementation receives an
// already-normalized key and need not (and must not) reach back into a
// filesystem to interpret it.
//
// Conformance: engine/adapter/ledgerconformance is the shared behavioral suite
// every implementation must pass (the in-memory reference adapter
// engine/adapter/memledger runs it; a durable adapter runs it over its client).
type ReadLedger interface {
	// RecordRead stores version under the already-normalized key, performing
	// NO file-content I/O. err is non-nil ONLY on a genuine storage/write
	// failure; the caller (a file tool) must then report that the read's
	// evidence was not retained, so a later mutation on the same key is
	// refused until a Read successfully records again.
	RecordRead(ctx context.Context, key string, version FileVersion) error

	// RecordedVersion returns the version previously recorded for the
	// already-normalized key, performing NO file-content I/O.
	//
	//   - (version, true, nil): a valid recorded version was found.
	//   - (zero, false, nil): key was never recorded — normal absence.
	//   - (zero, false, err): the lookup is UNAVAILABLE or the stored record
	//     is CORRUPT. This is DISTINCT from absence; callers MUST fail
	//     closed on it (never treat it as an unrecorded-but-authorized read).
	RecordedVersion(ctx context.Context, key string) (version FileVersion, ok bool, err error)
}
