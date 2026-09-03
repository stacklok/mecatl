// Package flocklease is the single-host port.SessionLease: cross-process
// single-writer enforcement for sessions sharing one machine (one store
// directory), backed by gofrs/flock advisory locks plus a small per-session
// record file. It is the lease analogue of the memory store's flock discipline
// (internal/adapter/memory/store.go), kept a SEPARATE package on purpose — the
// memory store's "one *Store per dir" invariant is about ONE sentinel guarding
// ONE document, whereas a per-session lease needs ONE lock file PER session id,
// so the two do not share a sentinel.
//
// flock gives single-host exclusion AND free crash recovery: a successful
// Acquire retains the flock fd for that held lease's lifetime, and when its
// process dies the OS releases the flock so a survivor can take over immediately.
// The JSON record preserves owner/token/expiry metadata and durable fencing-token
// monotonicity; its TTL never overrides a live local kernel hold. Single-host is
// the honest guarantee; the multi-host story is the k8s/driver backends.
//
// Layout per session id <safeID> under the lease dir:
//   - <safeID>.lock        — the STABLE flock sentinel, never renamed.
//   - <safeID>.lease.json  — {owner, token, expiry}, rewritten atomically under
//     the lock (temp + rename).
//
// <safeID> is a collision-free encoding (a sanitized prefix + a hash suffix) so
// two distinct ids never share a lock file and thus never falsely contend.
//
// INVARIANT — at most ONE *Lease per dir per process (the composition
// discipline), mirroring the memory store. Per-id sentinels mean distinct
// sessions never cross-contend. A successful Acquire retains its flock fd until
// the exact owner/token is released; that kernel hold, not the JSON expiry, is
// the single-host liveness authority.
package flocklease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Lease is a single-host port.SessionLease over a lease directory. Each session
// id gets its own flock sentinel and record file. Successful acquisitions retain
// the flock handle until an exact-token Release.
type Lease struct {
	dir   string
	ttl   time.Duration
	clock port.Clock

	mu   sync.Mutex
	held map[session.SessionID]*heldLease
}

type heldLease struct {
	fl    *flock.Flock
	lease port.Lease
}

// compile-time assertion that *Lease satisfies the port.
var _ port.SessionLease = (*Lease)(nil)

// New constructs a single-host lease rooted at dir, creating dir (and parents)
// if absent. ttl controls the expiry metadata and renew schedule; the retained
// kernel flock remains the local liveness authority. clock computes record
// expiry. A non-positive ttl defaults to 30s.
func New(dir string, ttl time.Duration, clock port.Clock) (*Lease, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("flocklease: New requires a non-empty dir")
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	// 0o700: a lease dir reveals which sessions are active; no reason to be
	// group/other-readable.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("flocklease: create dir %q: %w", dir, err)
	}
	return &Lease{dir: dir, ttl: ttl, clock: clock, held: make(map[session.SessionID]*heldLease)}, nil
}

// record is the on-disk lease state for one session id. A RELEASED lease keeps
// the record as a tombstone (empty Owner, retained Token) rather than deleting
// the file, so the per-id fencing token stays monotone across release AND across
// a process restart — a fresh takeover reads the last token and bumps past it.
// An empty Owner means "not currently held".
type record struct {
	Owner  string    `json:"owner"`
	Token  uint64    `json:"token"`
	Expiry time.Time `json:"expiry"`
}

// Acquire grants the lease when its kernel flock is free. A successful acquire
// retains the flock handle until exact-token Release. The record expiry remains
// useful metadata, but cannot let a live local contender bypass the kernel hold.
func (l *Lease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	if err := ctx.Err(); err != nil {
		return port.Lease{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	if held := l.held[id]; held != nil {
		if held.lease.Owner != owner && now.Before(held.lease.Expiry) {
			return port.Lease{}, port.ErrLeaseHeld
		}
		token := held.lease.Token
		if held.lease.Owner != owner {
			// SessionLease conformance permits an expired in-adapter handoff. The
			// same retained kernel handle remains authoritative against other
			// processes throughout the record update.
			token++
		}
		out, err := l.writeRecord(id, owner, token, now)
		if err != nil {
			return port.Lease{}, err
		}
		held.lease = out
		return out, nil
	}

	fl := flock.New(l.lockPath(id))
	locked, err := fl.TryLock()
	if err != nil {
		_ = fl.Close()
		return port.Lease{}, fmt.Errorf("flocklease: acquire lock %q: %w", fl.Path(), err)
	}
	if !locked {
		_ = fl.Close()
		return port.Lease{}, port.ErrLeaseHeld
	}

	cur, _, err := l.readRecord(id)
	if err != nil {
		_ = fl.Close()
		return port.Lease{}, err
	}
	out, err := l.writeRecord(id, owner, cur.Token+1, now)
	if err != nil {
		_ = fl.Close()
		return port.Lease{}, err
	}
	l.held[id] = &heldLease{fl: fl, lease: out}
	return out, nil
}

// Renew extends the exact locally-held lease, retaining its kernel flock and
// token. JSON expiry does not constitute local lease loss while that flock lives.
func (l *Lease) Renew(ctx context.Context, in port.Lease) (port.Lease, error) {
	if err := ctx.Err(); err != nil {
		return port.Lease{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	held := l.held[in.SessionID]
	if held == nil || held.lease.Owner != in.Owner || held.lease.Token != in.Token {
		return port.Lease{}, port.ErrLeaseHeld
	}
	out, err := l.writeRecord(in.SessionID, in.Owner, in.Token, l.clock.Now())
	if err != nil {
		return port.Lease{}, err
	}
	held.lease = out
	return out, nil
}

// Release tombstones and unlocks only an exact lease held by this adapter.
// Mismatched, absent, and stale releases are idempotent no-ops and cannot touch a
// successor's handle. On a tombstone error the handle is still closed so failures
// cannot leak descriptors; the next acquirer advances from the durable record.
func (l *Lease) Release(ctx context.Context, in port.Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	held := l.held[in.SessionID]
	if held == nil || held.lease.Owner != in.Owner || held.lease.Token != in.Token {
		return nil
	}
	_, writeErr := l.writeRecord(in.SessionID, "", in.Token, l.clock.Now().Add(-l.ttl))
	closeErr := held.fl.Close()
	delete(l.held, in.SessionID)
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("flocklease: release lock %q: %w", held.fl.Path(), closeErr)
	}
	return nil
}

// readRecord loads the session's lease record. A missing file is (zero, false,
// nil) — absence is data, not an error. Caller holds the flock.
func (l *Lease) readRecord(id session.SessionID) (record, bool, error) {
	b, err := os.ReadFile(l.recordPath(id)) //nolint:gosec // path is sanitized via recordPath
	if os.IsNotExist(err) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, fmt.Errorf("flocklease: read record %q: %w", id, err)
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return record{}, false, fmt.Errorf("flocklease: decode record %q: %w", id, err)
	}
	return r, true, nil
}

// writeRecord atomically (temp + rename) writes the record and returns the
// resulting port.Lease. Caller holds the flock.
func (l *Lease) writeRecord(id session.SessionID, owner string, token uint64, now time.Time) (port.Lease, error) {
	expiry := now.Add(l.ttl)
	r := record{Owner: owner, Token: token, Expiry: expiry}
	b, err := json.Marshal(r)
	if err != nil {
		return port.Lease{}, fmt.Errorf("flocklease: encode record %q: %w", id, err)
	}
	tmp, err := os.CreateTemp(l.dir, ".lease-*.json.tmp")
	if err != nil {
		return port.Lease{}, fmt.Errorf("flocklease: create temp record: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return port.Lease{}, fmt.Errorf("flocklease: write temp record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return port.Lease{}, fmt.Errorf("flocklease: close temp record: %w", err)
	}
	if err := os.Rename(tmpName, l.recordPath(id)); err != nil {
		_ = os.Remove(tmpName)
		return port.Lease{}, fmt.Errorf("flocklease: commit record %q: %w", id, err)
	}
	return port.Lease{SessionID: id, Owner: owner, Token: token, Expiry: expiry}, nil
}

func (l *Lease) lockPath(id session.SessionID) string {
	return filepath.Join(l.dir, safeName(id)+".lock")
}

func (l *Lease) recordPath(id session.SessionID) string {
	return filepath.Join(l.dir, safeName(id)+".lease.json")
}

// safeName encodes a session id into a collision-free, path-safe filename stem:
// a sanitized human-readable prefix (so an operator can eyeball the dir) plus a
// hash suffix that makes a collision negligible rather than structural (the
// suffix is 64 bits of the digest, so ~2^-64 per pair) — two distinct ids
// practically never collide onto one lock file and thus never falsely contend.
// jsonlstore's canonical session files now use this SAME shape (a capped
// sanitized prefix plus a hash suffix, with 128 bits rather than 64); its
// sanitized-ONLY legacySafeName survives there just for pre-rewrite files and
// schedule/fire names, and is fine for a store whose Load re-reads the id from
// the file's own contents — but a lease has no contents to check, so it must
// NEVER conflate two sessions.
func safeName(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	suffix := hex.EncodeToString(sum[:8]) // 16 hex chars: collision-resistant.

	s := string(id)
	var b strings.Builder
	const maxPrefix = 40
	for _, r := range s {
		if b.Len() >= maxPrefix {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	prefix := b.String()
	if prefix == "" {
		prefix = "id"
	}
	return prefix + "-" + suffix
}
