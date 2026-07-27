package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// DeliveryQueueFormat is the per-record format tag written on every
// pending-delivery line. It versions the on-disk encoding the same way
// jsonlstore's event-log tag does: an unknown tag on Read is an infra error
// (a forward-incompatible file must fail loud, not silently skip).
const DeliveryQueueFormat = "delivery-json/1"

// deliveryQueueRecord is one .delivery.jsonl line: a format tag plus the
// verbatim DeliveryNote JSON. The note text is ALREADY rendered (fenced and
// clamped by the renderer) when it arrives here; the queue stores it verbatim.
type deliveryQueueRecord struct {
	V    string            `json:"v"`
	Note port.DeliveryNote `json:"note"`
}

// deliveryFileSuffix is the per-session sidecar suffix under the store dir,
// the jsonlstore `.events.jsonl` precedent applied to pending delivery:
//
//	<dir>/<id>.delivery.jsonl — one record per Enqueue (append-only)
//
// Pending (not-yet-delivered) notes are the records in this file NOT present
// in the delivered set. The delivered set is persisted in a sibling ledger
// file so a restart reconstructs both halves.
const (
	deliveryFileSuffix   = ".delivery.jsonl"
	deliveryLedgerSuffix = ".delivery.ledger.json"
	deliveryLedgerFormat = "delivery-ledger/1"
)

// InMemoryDeliveryQueue is the in-memory, concurrency-safe port.DeliveryQueue —
// the no-store-dir default the composition layer wires when the SessionStore is
// memstore (so the delivery seam is never nil). It is the memstore-tier queue:
// it works in-process but does NOT survive a restart (it degrades honestly to
// empty across a restart, byte-identical to the no-delivery path for the
// restarted process — the DURABLE backing is FileDeliveryQueue).
//
// It does NOT round-trip through a serialization (the note text is opaque and
// immutable once enqueued), so notes are stored by value directly.
type InMemoryDeliveryQueue struct {
	mu        sync.Mutex
	cap       int // 0 = unbounded
	diag      port.Diagnostics
	next      map[session.SessionID]uint64
	pending   map[session.SessionID][]port.DeliveryNote
	delivered map[session.SessionID]map[uint64]bool
}

// compile-time assertion that InMemoryDeliveryQueue satisfies the port.
var _ port.DeliveryQueue = (*InMemoryDeliveryQueue)(nil)

// NewInMemoryDeliveryQueue constructs an empty in-memory delivery queue. It is
// the memstore-tier default; for a durable queue use NewFileDeliveryQueue.
func NewInMemoryDeliveryQueue(opts ...DeliveryQueueOption) *InMemoryDeliveryQueue {
	q := &InMemoryDeliveryQueue{
		cap:       0,
		diag:      port.NopDiagnostics{},
		next:      make(map[session.SessionID]uint64),
		pending:   make(map[session.SessionID][]port.DeliveryNote),
		delivered: make(map[session.SessionID]map[uint64]bool),
	}
	for _, opt := range opts {
		opt(q)
	}
	return q
}

// Enqueue appends a note for origin, assigning the next per-session seq. When
// the pending backlog exceeds cap (cap > 0) the OLDEST pending note is dropped
// with a WARN. It is NOT idempotent: each call mints a fresh seq.
func (q *InMemoryDeliveryQueue) Enqueue(_ context.Context, origin session.SessionID, text string) (port.DeliveryNote, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.next[origin]++
	n := port.DeliveryNote{
		Seq:        q.next[origin],
		SessionID:  origin,
		Text:       text,
		EnqueuedAt: time.Now().UTC(),
	}
	q.pending[origin] = append(q.pending[origin], n)
	if q.cap > 0 && len(q.pending[origin]) > q.cap {
		// Drop the OLDEST pending note (index 0). The dropped note's seq stays
		// assigned (it was a real enqueue); it simply never drains. This is
		// the "drop the oldest with a WARN" overload policy.
		dropped := q.pending[origin][0]
		q.pending[origin] = append([]port.DeliveryNote(nil), q.pending[origin][1:]...)
		q.diag.Log(context.Background(), port.LevelWarn,
			"delivery queue: backlog cap exceeded, dropping oldest pending note",
			"session", string(origin), "seq", dropped.Seq, "cap", q.cap, "pending", len(q.pending[origin]))
	}
	return n, nil
}

// Pending returns the origin's pending notes in enqueue order (oldest first).
func (q *InMemoryDeliveryQueue) Pending(_ context.Context, origin session.SessionID) ([]port.DeliveryNote, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	src := q.pending[origin]
	out := make([]port.DeliveryNote, len(src))
	copy(out, src)
	return out, nil
}

// MarkDelivered records seq as delivered, removing it from the pending set.
// Idempotent: a re-mark of an already-delivered or unknown seq is a no-op.
func (q *InMemoryDeliveryQueue) MarkDelivered(_ context.Context, origin session.SessionID, seq uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delivered := q.delivered[origin]
	if delivered == nil {
		delivered = make(map[uint64]bool)
		q.delivered[origin] = delivered
	}
	if delivered[seq] {
		return nil // idempotent re-mark
	}
	delivered[seq] = true
	pending := q.pending[origin]
	for i, n := range pending {
		if n.Seq == seq {
			q.pending[origin] = append(pending[:i], pending[i+1:]...)
			break
		}
	}
	return nil
}

// Close is a no-op for the in-memory queue (kept for the port.DeliveryQueue
// durability-tier symmetry with FileDeliveryQueue.Close, so a caller can defer
// either uniformly).
func (*InMemoryDeliveryQueue) Close() {}

// FileDeliveryQueue is a DURABLE, file-backed port.DeliveryQueue writing a
// per-session `.delivery.jsonl` sidecar under a configured dir (the jsonlstore
// `.events.jsonl` precedent). It survives a process restart: a note queued
// before a restart drains after it (the persist-in-snapshot List 2 decision).
//
// Layout under dir:
//
//	<dir>/<id>.delivery.jsonl       — append-only, one record per Enqueue
//	<dir>/<id>.delivery.ledger.json  — the delivered-seq ledger (upserted)
//
// The two files together reconstruct the pending set on restart: the .jsonl is
// the append-only enqueue log, the .ledger.json is the set of seqs already
// drained. Pending = enqueue log − delivered ledger.
//
// It is a composition-owned adapter (NOT a jsonlstore package sibling) so it
// does not collide with the shared store package's evolution; it shares the
// SAME discipline (format-tagged records, filename sanitization, atomic upsert
// for the ledger, append-only for the log). SINGLE-HOST: the mutex is the
// fence; a multi-host deployment needs the leader lease (the schedule store
// precedent) — the queue is process-affinity-routed like the session store.
type FileDeliveryQueue struct {
	dir  string
	cap  int
	diag port.Diagnostics
	mu   sync.Mutex // serializes appends/reads across files
}

// compile-time assertion that FileDeliveryQueue satisfies the port.
var _ port.DeliveryQueue = (*FileDeliveryQueue)(nil)

// NewFileDeliveryQueue constructs a durable, file-backed delivery queue under
// dir, creating dir if needed (mode 0700: it holds rendered note text — owner
// only, matching the jsonlstore dir). It returns an error only if dir cannot be
// created.
func NewFileDeliveryQueue(dir string, opts ...DeliveryQueueOption) (*FileDeliveryQueue, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("delivery queue: create dir: %w", err)
	}
	q := &FileDeliveryQueue{
		dir:  dir,
		cap:  0,
		diag: port.NopDiagnostics{},
	}
	for _, opt := range opts {
		opt(q)
	}
	return q, nil
}

// Close is a no-op (files are opened per call, never held), kept for the
// durability-tier symmetry with the in-memory queue and the defer idiom.
func (*FileDeliveryQueue) Close() {}

// Enqueue appends a note for origin, assigning the next per-session seq. The
// note is durably appended to the per-session `.delivery.jsonl` before
// Enqueue returns (nil error = on stable storage, the port contract). When the
// pending backlog exceeds cap (cap > 0) the OLDEST pending note is dropped
// with a WARN — "dropped" here means recorded in the delivered ledger so it
// will not drain (its enqueue line stays in the append-only log for audit, but
// it is no longer pending).
func (q *FileDeliveryQueue) Enqueue(ctx context.Context, origin session.SessionID, text string) (port.DeliveryNote, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	seq := q.nextSeqLocked(origin)
	n := port.DeliveryNote{
		Seq:        seq,
		SessionID:  origin,
		Text:       text,
		EnqueuedAt: time.Now().UTC(),
	}
	line, err := json.Marshal(deliveryQueueRecord{V: DeliveryQueueFormat, Note: n})
	if err != nil {
		return port.DeliveryNote{}, fmt.Errorf("delivery queue: marshal note: %w", err)
	}
	if err := deliveryAppendLine(q.deliveryPath(origin), line); err != nil {
		return port.DeliveryNote{}, fmt.Errorf("delivery queue: append: %w", err)
	}
	// Backlog cap: drop the OLDEST pending note (with a WARN) rather than grow
	// unboundedly. "Drop" = record it in the delivered ledger so it is no
	// longer pending (the enqueue line stays in the append-only log). This is
	// the durable analogue of the in-memory drop.
	if q.cap > 0 {
		pending, err := q.pendingLocked(origin)
		if err != nil {
			// A read fault here must not fail the enqueue (the note IS
			// recorded); the cap is best-effort overload protection. WARN and
			// proceed without a drop.
			q.diag.Log(ctx, port.LevelWarn,
				"delivery queue: backlog cap check read failed (note recorded, cap not enforced this enqueue)",
				"session", string(origin), "err", err.Error())
		} else if len(pending) > q.cap {
			// pending exceeds the cap after this enqueue → drop the OLDEST
			// pending note. The just-enqueued note is the newest, so it is
			// never the one dropped. After the drop len == cap (the bound).
			drop := pending[0]
			if err := q.markDeliveredLocked(origin, drop.Seq); err != nil {
				q.diag.Log(ctx, port.LevelWarn,
					"delivery queue: backlog drop write failed (note recorded, cap not enforced)",
					"session", string(origin), "seq", drop.Seq, "err", err.Error())
			} else {
				q.diag.Log(ctx, port.LevelWarn,
					"delivery queue: backlog cap exceeded, dropping oldest pending note",
					"session", string(origin), "seq", drop.Seq, "cap", q.cap)
			}
		}
	}
	return n, nil
}

// Pending returns the origin's pending notes in enqueue order (the enqueue log
// minus the delivered ledger). A miss returns an empty slice (absence is data).
func (q *FileDeliveryQueue) Pending(_ context.Context, origin session.SessionID) ([]port.DeliveryNote, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pendingLocked(origin)
}

// MarkDelivered records seq as delivered for origin by adding it to the
// delivered ledger (persisted). Idempotent: a re-mark is a no-op success.
func (q *FileDeliveryQueue) MarkDelivered(_ context.Context, origin session.SessionID, seq uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.markDeliveredLocked(origin, seq)
}

// pendingLocked reconstructs the pending set: read the enqueue log, subtract
// the delivered ledger. Caller holds mu.
func (q *FileDeliveryQueue) pendingLocked(origin session.SessionID) ([]port.DeliveryNote, error) {
	delivered, err := q.readLedgerLocked(origin)
	if err != nil {
		return nil, err
	}
	out := make([]port.DeliveryNote, 0)
	f, err := os.Open(q.deliveryPath(origin)) //nolint:gosec // path is sanitized via deliveryPath
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // miss → empty (absence is data)
		}
		return nil, fmt.Errorf("delivery queue: open log: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var rec deliveryQueueRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, fmt.Errorf("delivery queue: decode record: %w", err)
		}
		if rec.V != DeliveryQueueFormat {
			return nil, fmt.Errorf("delivery queue: unknown format %q (want %q)", rec.V, DeliveryQueueFormat)
		}
		if delivered[rec.Note.Seq] {
			continue
		}
		out = append(out, rec.Note)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("delivery queue: scan log: %w", err)
	}
	return out, nil
}

// nextSeqLocked returns the next monotonic seq for origin. It derives from the
// durable state (the max seq in the enqueue log) so a restart does not re-mint
// seq 1 — the ledger continues. Caller holds mu.
func (q *FileDeliveryQueue) nextSeqLocked(origin session.SessionID) uint64 {
	top, err := q.maxSeqLocked(origin)
	if err != nil || top == 0 {
		// No prior enqueues (or an unreadable log): start at 1.
		return 1
	}
	return top + 1
}

// maxSeqLocked scans the enqueue log for the highest seq. Caller holds mu.
func (q *FileDeliveryQueue) maxSeqLocked(origin session.SessionID) (uint64, error) {
	var top uint64
	f, err := os.Open(q.deliveryPath(origin)) //nolint:gosec // path is sanitized
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var rec deliveryQueueRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			return 0, err
		}
		if rec.Note.Seq > top {
			top = rec.Note.Seq
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return top, nil
}

// markDeliveredLocked adds seq to the origin's delivered ledger (persisted).
// Idempotent. Caller holds mu.
func (q *FileDeliveryQueue) markDeliveredLocked(origin session.SessionID, seq uint64) error {
	delivered, err := q.readLedgerLocked(origin)
	if err != nil {
		return err
	}
	if delivered[seq] {
		return nil // idempotent
	}
	delivered[seq] = true
	return q.writeLedgerLocked(origin, delivered)
}

// readLedgerLocked reads the delivered-seq ledger for origin. Caller holds mu.
func (q *FileDeliveryQueue) readLedgerLocked(origin session.SessionID) (map[uint64]bool, error) {
	b, err := os.ReadFile(q.ledgerPath(origin)) //nolint:gosec // path is sanitized
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[uint64]bool), nil // miss → empty (no deliveries yet)
		}
		return nil, fmt.Errorf("delivery queue: read ledger: %w", err)
	}
	var rec struct {
		V         string   `json:"v"`
		Sequences []uint64 `json:"sequences"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("delivery queue: decode ledger: %w", err)
	}
	if rec.V != deliveryLedgerFormat {
		return nil, fmt.Errorf("delivery queue: unknown ledger format %q (want %q)", rec.V, deliveryLedgerFormat)
	}
	out := make(map[uint64]bool, len(rec.Sequences))
	for _, s := range rec.Sequences {
		out[s] = true
	}
	return out, nil
}

// writeLedgerLocked atomically writes the delivered-seq ledger (caller holds
// mu). Atomic: temp file + rename, so a crash mid-write leaves the prior ledger
// intact (the jsonlstore writeFileAtomic precedent).
func (q *FileDeliveryQueue) writeLedgerLocked(origin session.SessionID, delivered map[uint64]bool) error {
	seqs := make([]uint64, 0, len(delivered))
	for s := range delivered {
		seqs = append(seqs, s)
	}
	rec := struct {
		V         string   `json:"v"`
		Sequences []uint64 `json:"sequences"`
	}{V: deliveryLedgerFormat, Sequences: seqs}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("delivery queue: marshal ledger: %w", err)
	}
	return deliveryWriteAtomic(q.ledgerPath(origin), b)
}

// deliveryPath / ledgerPath map an origin session id to a filename-safe path
// under the queue's dir (the safeName discipline, mirroring jsonlstore).
func (q *FileDeliveryQueue) deliveryPath(id session.SessionID) string {
	return filepath.Join(q.dir, deliverySafeName(id)+deliveryFileSuffix)
}

func (q *FileDeliveryQueue) ledgerPath(id session.SessionID) string {
	return filepath.Join(q.dir, deliverySafeName(id)+deliveryLedgerSuffix)
}

// DeliveryQueueOption configures a delivery queue at construction.
type DeliveryQueueOption func(queueConfig)

type queueConfig interface {
	setCap(int)
	setDiag(port.Diagnostics)
}

func (q *InMemoryDeliveryQueue) setCap(c int) { q.cap = c }
func (q *InMemoryDeliveryQueue) setDiag(d port.Diagnostics) {
	if d != nil {
		q.diag = d
	}
}
func (q *FileDeliveryQueue) setCap(c int) { q.cap = c }
func (q *FileDeliveryQueue) setDiag(d port.Diagnostics) {
	if d != nil {
		q.diag = d
	}
}

// WithDeliveryBacklogCap bounds the pending backlog per origin: when the
// pending count exceeds cap, the OLDEST pending note is dropped with a WARN
// rather than growing unboundedly. 0 (the default) means UNBOUNDED (no drop).
func WithDeliveryBacklogCap(backlogCap int) DeliveryQueueOption {
	return func(q queueConfig) { q.setCap(backlogCap) }
}

// WithDeliveryDiagnostics injects the Diagnostics sink the backlog-drop WARN
// rides. Nil defaults to NopDiagnostics (the WARN is dropped — an operator who
// wires a durable queue but no diagnostics sees silent drops, the same posture
// as the rest of the composition).
func WithDeliveryDiagnostics(d port.Diagnostics) DeliveryQueueOption {
	return func(q queueConfig) { q.setDiag(d) }
}

// deliveryAppendLine appends b followed by a newline to the file at path,
// opening for append (creating if needed). Each line is a complete JSON record.
// Mirrors jsonlstore.appendLine.
func deliveryAppendLine(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path is sanitized
	if err != nil {
		return fmt.Errorf("open for append: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("append: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close after append: %w", err)
	}
	return nil
}

// deliveryWriteAtomic writes b to path via a temp file + rename (atomic on
// POSIX rename(2)). Mode 0o600: owner-only, matching the 0o700 dir (the file
// holds the rendered note text). Mirrors jsonlstore.writeFileAtomic.
func deliveryWriteAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*") //nolint:gosec // temp in the same dir for same-FS rename
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // best-effort cleanup if rename failed
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// deliverySafeName maps a SessionID to a filename-safe token so it cannot
// traverse out of the dir. Any rune that is not alphanumeric, '-', '_' or '.'
// becomes '_'; a leading '.' is neutralized. Mirrors jsonlstore.safeName.
func deliverySafeName(id session.SessionID) string {
	s := string(id)
	if s == "" {
		return "_empty_"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if strings.HasPrefix(out, ".") {
		out = "_" + out[1:]
	}
	return out
}
