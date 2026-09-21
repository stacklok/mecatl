package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ledgerKeyPrefix mirrors sessionKeyPrefix/eventsKeyPrefix/toolsKeyPrefix: the
// session id is the raw string after the prefix, so two distinct session ids
// always produce two distinct Redis keys (concatenating a fixed prefix with
// the WHOLE remainder is injective — no sanitization is needed, the same
// discipline the session/event/tool-call keys already apply).
const ledgerKeyPrefix = storeKeyPrefix + "ledger:"

// ledgerFormat is the per-entry format tag written on every ledger hash
// field's value. It versions the encoding so a future incompatible format (or
// a hand-corrupted value) fails loud on lookup — the EventLogFormat/
// scheduleFormat precedent — rather than a silent misdecode.
const ledgerFormat = "redisstore-ledger/1"

// ledgerRecord is the JSON value stored in one hash field: a format tag plus
// the caller's exact opaque FileVersion token. It performs NO file-content
// I/O and carries no path/session identity of its own — that identity lives
// entirely in the Redis key (session) and hash field (normalized path), the
// two INDEPENDENT axes that make (session, path) addressing injective (ADR
// 0290): concatenating them into one delimited string is exactly what this
// scheme avoids, so an adversarial separator or shared prefix in either value
// can never make two distinct pairs address the same hash field.
type ledgerRecord struct {
	V string `json:"v"`
	T string `json:"t"`
}

// redisLedger is a tool.ReadLedger bound to ONE session scope, backed by the
// Store's shared Redis client (the same client-generation/security/key-prefix
// conventions the session store, event log, and schedule store already use).
// Every operation is a single atomic HSET/HGET against ledgerKey(id) — Redis
// serializes commands single-threaded, so no client-side mutex is needed (the
// Store-wide precedent). It performs NO file-content I/O and exposes no
// file-content operation: exactly the two tool.ReadLedger methods.
type redisLedger struct {
	clients *clientGenerations
	id      session.SessionID
}

// compile-time assertion that redisLedger satisfies the port.
var _ tool.ReadLedger = (*redisLedger)(nil)

// ReadLedger returns a tool.ReadLedger bound to one session scope, sharing
// this Store's Redis client. Independently constructed handles for the SAME
// session id (including from separate *Store instances/process — see New) all
// read and write the same durable Redis hash, so a version recorded through
// one handle is visible after reopening another (ADR 0294 Scenario 2).
func (st *Store) ReadLedger(id session.SessionID) tool.ReadLedger {
	return &redisLedger{clients: st.clients, id: id}
}

// DeleteReadLedger removes every entry for the session's read-ledger scope
// (the whole Redis hash). Canonical session deletion already removes this key
// atomically with the other session sidecars; this method supports an explicit
// ledger-only reset. It is idempotent — deleting an already-empty or never-used
// scope is a no-op.
func (st *Store) DeleteReadLedger(ctx context.Context, id session.SessionID) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return fmt.Errorf("redisstore: delete read ledger %q: %w: %w", id, tool.ErrLedgerUnavailable, err)
	}
	defer release()
	if err := client.Del(ctx, ledgerKey(id)).Err(); err != nil {
		return fmt.Errorf("redisstore: delete read ledger %q: %w: %w", id, tool.ErrLedgerUnavailable, err)
	}
	return nil
}

// RecordRead stores the EXACT opaque token version carries under the
// already-normalized key, as this session's hash field. It performs NO
// file-content I/O. err is non-nil ONLY on a genuine storage failure —
// acquiring a live client, or the HSET itself (an unreachable/timed-out
// broker) — wrapped in tool.ErrLedgerUnavailable so a caller can classify it
// with errors.Is, distinct from an ordinary successful record.
func (l *redisLedger) RecordRead(ctx context.Context, key string, version tool.FileVersion) error {
	token, err := tool.EncodeFileVersion(version)
	if err != nil {
		return fmt.Errorf("redisstore: encode read ledger entry: %w", err)
	}
	data, err := json.Marshal(ledgerRecord{V: ledgerFormat, T: token})
	if err != nil {
		return fmt.Errorf("redisstore: encode read ledger entry: %w", err)
	}
	client, release, err := l.clients.acquire()
	if err != nil {
		return fmt.Errorf("redisstore: record read ledger entry: %w: %w", tool.ErrLedgerUnavailable, err)
	}
	defer release()
	if err := client.HSet(ctx, ledgerKey(l.id), key, data).Err(); err != nil {
		return fmt.Errorf("redisstore: record read ledger entry: %w: %w", tool.ErrLedgerUnavailable, err)
	}
	return nil
}

// RecordedVersion returns the version previously recorded for the
// already-normalized key, performing NO file-content I/O. It distinguishes
// three outcomes:
//
//   - (version, true, nil): a valid recorded version was found.
//   - (zero, false, nil): the field is absent from the hash — ordinary,
//     normal absence (Redis HGET on a missing key or field, both reported by
//     go-redis as redis.Nil).
//   - (zero, false, err wrapping tool.ErrLedgerUnavailable): the lookup could
//     not be trusted — acquiring a live client failed, the HGET itself failed
//     (timeout, unreachable broker), the stored value is not valid JSON, or it
//     carries an unrecognized format tag. This is DISTINCT from absence;
//     callers MUST fail closed on it.
func (l *redisLedger) RecordedVersion(ctx context.Context, key string) (tool.FileVersion, bool, error) {
	client, release, err := l.clients.acquire()
	if err != nil {
		return tool.FileVersion{}, false, fmt.Errorf("redisstore: lookup read ledger entry: %w: %w", tool.ErrLedgerUnavailable, err)
	}
	defer release()
	raw, err := client.HGet(ctx, ledgerKey(l.id), key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return tool.FileVersion{}, false, nil
		}
		return tool.FileVersion{}, false, fmt.Errorf("redisstore: lookup read ledger entry: %w: %w", tool.ErrLedgerUnavailable, err)
	}
	var rec struct {
		V string  `json:"v"`
		T *string `json:"t"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return tool.FileVersion{}, false, fmt.Errorf("redisstore: decode read ledger entry: %w: %w", tool.ErrLedgerUnavailable, err)
	}
	if rec.V != ledgerFormat {
		return tool.FileVersion{}, false, fmt.Errorf("redisstore: unknown read ledger format %q (want %q): %w", rec.V, ledgerFormat, tool.ErrLedgerUnavailable)
	}
	if rec.T == nil {
		return tool.FileVersion{}, false, fmt.Errorf("redisstore: read ledger entry has no string token: %w", tool.ErrLedgerUnavailable)
	}
	return tool.DecodeFileVersion(*rec.T), true, nil
}

func ledgerKey(id session.SessionID) string { return ledgerKeyPrefix + string(id) }
