// Package flocklease is the single-host port.SessionLease: cross-process
// single-writer enforcement for sessions sharing one machine (one store
// directory), backed by gofrs/flock advisory locks plus a small per-session
// record file. It is the lease analogue of the memory store's flock discipline
// (internal/adapter/memory/store.go), kept a SEPARATE package on purpose — the
// memory store's "one *Store per dir" invariant is about ONE sentinel guarding
// ONE document, whereas a per-session lease needs ONE lock file PER session id,
// so the two do not share a sentinel.
//
// flock gives single-host exclusion AND free crash recovery: when a holder
// process dies, the OS releases its flock, so a survivor can take over without
// waiting for the TTL. flock has no TTL of its own, so the lease's
// owner/token/expiry semantics live in a JSON record file written under the held
// lock; the TTL covers the cross-HOST case the flock cannot (two machines over a
// shared NFS/EFS mount, where flock semantics are unreliable). Single-host is the
// honest guarantee; the multi-host story is the k8s/driver backends.
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
// sessions never cross-contend; the per-id flock fd is created per operation and
// closed before return, so there is no long-lived handle to self-deadlock.
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
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	// lockRetryDelay is how often a contended flock acquire re-probes while
	// waiting (mirrors the memory store).
	lockRetryDelay = 5 * time.Millisecond
	// lockTimeout bounds any single acquire so a stuck/crashed holder cannot
	// deadlock a run indefinitely.
	lockTimeout = 5 * time.Second
)

// Lease is a single-host port.SessionLease over a lease directory. Each session
// id gets its own flock sentinel and record file; the per-id flock is acquired
// and released within a single Acquire/Renew/Release call.
type Lease struct {
	dir   string
	ttl   time.Duration
	clock port.Clock
}

// compile-time assertion that *Lease satisfies the port.
var _ port.SessionLease = (*Lease)(nil)

// New constructs a single-host lease rooted at dir, creating dir (and parents)
// if absent. ttl is the lease lifetime (the cross-host fallback bound; flock
// covers the single-host crash case for free). clock is the wall clock the
// expiry is computed against. A non-positive ttl defaults to 30s.
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
	return &Lease{dir: dir, ttl: ttl, clock: clock}, nil
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

// held reports whether the record represents a current hold (a non-empty owner).
func (r record) held() bool { return r.Owner != "" }

// Acquire grants the lease when free/expired/same-owner, writing a fresh record
// under the exclusive flock; otherwise it returns ErrLeaseHeld. A takeover bumps
// the token strictly past the prior record's; a same-owner re-acquire keeps it.
func (l *Lease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	now := l.clock.Now()
	var out port.Lease
	err := l.withLock(ctx, id, func() error {
		cur, present, err := l.readRecord(id)
		if err != nil {
			return err
		}
		switch {
		case !present, !cur.held(), !now.Before(cur.Expiry):
			// free (no record), released (tombstone), or expired → takeover,
			// strictly-greater token (cur.Token is 0 when absent).
			out, err = l.writeRecord(id, owner, cur.Token+1, now)
			return err
		case cur.Owner == owner:
			out, err = l.writeRecord(id, owner, cur.Token, now)
			return err
		default:
			return port.ErrLeaseHeld
		}
	})
	if err != nil {
		return port.Lease{}, err
	}
	return out, nil
}

// Renew extends a held lease (owner + token match, unexpired) under the lock,
// keeping the token and refreshing the expiry; else ErrLeaseHeld (loss signal).
func (l *Lease) Renew(ctx context.Context, in port.Lease) (port.Lease, error) {
	now := l.clock.Now()
	var out port.Lease
	err := l.withLock(ctx, in.SessionID, func() error {
		cur, present, err := l.readRecord(in.SessionID)
		if err != nil {
			return err
		}
		if !present || cur.Owner != in.Owner || cur.Token != in.Token || !now.Before(cur.Expiry) {
			return port.ErrLeaseHeld
		}
		out, err = l.writeRecord(in.SessionID, in.Owner, in.Token, now)
		return err
	})
	if err != nil {
		return port.Lease{}, err
	}
	return out, nil
}

// Release drops the caller's own hold (owner + token match) by writing a
// tombstone record under the lock; idempotent (a mismatch or absent record is a
// no-op success). The tombstone keeps the fencing token monotone across release.
func (l *Lease) Release(ctx context.Context, in port.Lease) error {
	now := l.clock.Now()
	return l.withLock(ctx, in.SessionID, func() error {
		cur, present, err := l.readRecord(in.SessionID)
		if err != nil {
			return err
		}
		if !present || cur.Owner != in.Owner || cur.Token != in.Token {
			return nil // not our hold; idempotent no-op.
		}
		// Write a TOMBSTONE (empty owner, retained token, already-past expiry)
		// rather than deleting the file, so the per-id token stays monotone across
		// release. The token survives so a future takeover bumps strictly past it.
		_, err = l.writeRecord(in.SessionID, "", in.Token, now.Add(-l.ttl))
		return err
	})
}

// withLock acquires the per-id EXCLUSIVE flock (bounded by lockTimeout / ctx),
// runs fn, and releases the lock and closes the fd before return on every path.
// A fresh *flock.Flock per call means there is no shared fd to race in-process.
func (l *Lease) withLock(ctx context.Context, id session.SessionID, fn func() error) error {
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	fl := flock.New(l.lockPath(id))
	locked, err := fl.TryLockContext(lockCtx, lockRetryDelay)
	if err != nil {
		return fmt.Errorf("flocklease: acquire lock %q: %w", fl.Path(), err)
	}
	if !locked {
		return fmt.Errorf("flocklease: could not acquire lock %q within %s (held by another process?)", fl.Path(), lockTimeout)
	}
	defer func() { _ = fl.Close() }() // Close releases the flock and the fd.
	return fn()
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
