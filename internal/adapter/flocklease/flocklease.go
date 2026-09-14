// Package flocklease is the single-host port.SessionLease implementation. It
// combines the generic expiry contract with immediate same-host crash detection:
// a stable per-session flock serializes record transitions only, while each lease
// generation retains its own liveness flock for the generation's lifetime.
//
// Layout per session id <safeID> under the lease dir:
//   - <safeID>.lock             — stable, operation-scoped transition lock.
//   - <safeID>.<token>.live     — generation-specific retained liveness lock.
//   - <safeID>.lease.json       — atomic {owner, token, expiry} record.
package flocklease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Lease is a single-host port.SessionLease over a lease directory.
type Lease struct {
	dir   string
	ttl   time.Duration
	clock port.Clock

	mu           sync.Mutex
	held         map[generation]*heldLease
	cleanup      map[session.SessionID][]*flock.Flock
	cleanupPaths map[session.SessionID][]string

	gatesMu sync.Mutex
	gates   map[session.SessionID]*sessionGate
}

type sessionGate struct {
	mu   sync.Mutex
	refs int
}

type generation struct {
	id    session.SessionID
	token uint64
}

type heldLease struct {
	fl    *flock.Flock
	lease port.Lease
}

var _ port.SessionLease = (*Lease)(nil)

// New constructs a single-host lease rooted at dir. A non-positive ttl defaults
// to 30 seconds.
func New(dir string, ttl time.Duration, clock port.Clock) (*Lease, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("flocklease: New requires a non-empty dir")
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("flocklease: create dir %q: %w", dir, err)
	}
	return &Lease{
		dir:          dir,
		ttl:          ttl,
		clock:        clock,
		held:         make(map[generation]*heldLease),
		cleanup:      make(map[session.SessionID][]*flock.Flock),
		cleanupPaths: make(map[session.SessionID][]string),
		gates:        make(map[session.SessionID]*sessionGate),
	}, nil
}

type record struct {
	Owner  string    `json:"owner"`
	Token  uint64    `json:"token"`
	Expiry time.Time `json:"expiry"`
}

// Acquire serializes the record transition under the stable lock. An unexpired
// record is held only while its generation lock is live; a crashed holder can be
// replaced immediately. An expired record can always be replaced, even if its old
// process remains alive.
func (l *Lease) Acquire(ctx context.Context, id session.SessionID, owner string) (out port.Lease, err error) {
	unlock, err := l.lockSession(ctx, id)
	if err != nil {
		return out, err
	}
	defer unlock()
	l.retryCleanup(id)

	stable, err := l.lockStable(ctx, id)
	if err != nil {
		return out, err
	}
	committed := false
	defer l.finishStable(id, stable, &err, &committed)

	cur, err := l.readRecord(id)
	if err != nil {
		return out, err
	}
	now := l.clock.Now()
	currentGen := generation{id: id, token: cur.Token}
	if cur.Owner == owner && now.Before(cur.Expiry) {
		if held := l.getHeld(currentGen); held != nil {
			out, err = l.writeRecord(id, owner, cur.Token, now)
			if err != nil {
				return port.Lease{}, err
			}
			held.lease = out
			committed = true
			return out, nil
		}
	}
	if cur.Owner != "" && now.Before(cur.Expiry) {
		live, probeErr := l.generationLive(id, cur.Token)
		if probeErr != nil {
			return out, probeErr
		}
		if live {
			return out, port.ErrLeaseHeld
		}
	}
	if cur.Token == math.MaxUint64 {
		return out, fmt.Errorf("flocklease: fencing token exhausted for %q", id)
	}

	// Closing a locally retained predecessor is pre-commit: failure must stop the
	// takeover because the caller still tracks that active generation.
	if err := l.relinquish(currentGen); err != nil {
		return out, err
	}

	token := cur.Token + 1
	gen := generation{id: id, token: token}
	live := flock.New(l.generationPath(id, token))
	locked, lockErr := live.TryLock()
	if lockErr != nil {
		l.keepForCleanup(id, live)
		return out, fmt.Errorf("flocklease: acquire generation lock %q: %w", live.Path(), lockErr)
	}
	if !locked {
		l.keepForCleanup(id, live)
		return out, fmt.Errorf("flocklease: generation lock %q unexpectedly held", live.Path())
	}

	out, err = l.writeRecord(id, owner, token, now)
	if err != nil {
		l.keepForCleanup(id, live)
		return port.Lease{}, err
	}
	l.setHeld(gen, &heldLease{fl: live, lease: out})
	committed = true
	if cur.Token != 0 {
		l.removeObsolete(id, l.generationPath(id, cur.Token))
	}
	return out, nil
}

// Renew extends the exact current, locally retained generation, identified by
// matching owner+token in the durable record — read under the same stable
// transition lock Acquire/Release use to serialize takeovers. A record whose
// owner or token no longer match is definitive loss (a genuine competitor took
// over): the local generation handle is relinquished and ErrLeaseHeld is
// returned. Bare EXPIRY with the record still naming the caller at the
// caller's token is NOT, by itself, loss: on a single host that can only be
// true if nobody else raced an Acquire/takeover in the interim (issue #1333 —
// a process suspended past the TTL, e.g. laptop sleep, must not lose the
// lease to a competitor that never ran). Renew reclaims it with a fresh expiry
// instead, keeping the token unchanged. `held == nil` still hard-fails
// regardless of the record: it means a PRIOR Renew already declared loss and
// tore down local state.
func (l *Lease) Renew(ctx context.Context, in port.Lease) (out port.Lease, err error) {
	unlock, err := l.lockSession(ctx, in.SessionID)
	if err != nil {
		return out, err
	}
	defer unlock()
	l.retryCleanup(in.SessionID)

	stable, err := l.lockStable(ctx, in.SessionID)
	if err != nil {
		return out, err
	}
	committed := false
	defer l.finishStable(in.SessionID, stable, &err, &committed)

	gen := generation{id: in.SessionID, token: in.Token}
	held := l.getHeld(gen)
	cur, readErr := l.readRecord(in.SessionID)
	if readErr != nil {
		return out, readErr
	}
	if held == nil || cur.Owner != in.Owner || cur.Token != in.Token {
		closeErr := l.relinquish(gen)
		return out, errors.Join(port.ErrLeaseHeld, closeErr)
	}
	now := l.clock.Now()
	out, err = l.writeRecord(in.SessionID, in.Owner, in.Token, now)
	if err != nil {
		return port.Lease{}, err
	}
	held.lease = out
	committed = true
	return out, nil
}

// Release tombstones and closes only the exact current generation. A stale
// release cannot alter or unlock a successor. If writing or closing fails, the
// retained handle remains tracked so a later call can retry cleanup.
func (l *Lease) Release(ctx context.Context, in port.Lease) (err error) {
	unlock, err := l.lockSession(ctx, in.SessionID)
	if err != nil {
		return err
	}
	defer unlock()
	l.retryCleanup(in.SessionID)

	stable, err := l.lockStable(ctx, in.SessionID)
	if err != nil {
		return err
	}
	committed := false
	defer l.finishStable(in.SessionID, stable, &err, &committed)

	gen := generation{id: in.SessionID, token: in.Token}
	held := l.getHeld(gen)
	if held == nil || held.lease.Owner != in.Owner {
		return nil
	}
	cur, err := l.readRecord(in.SessionID)
	if err != nil {
		return err
	}
	if cur.Owner != in.Owner || cur.Token != in.Token {
		return l.relinquish(gen)
	}
	if _, err = l.writeRecord(in.SessionID, "", in.Token, l.clock.Now().Add(-l.ttl)); err != nil {
		return err
	}
	committed = true
	l.removeObsolete(in.SessionID, l.generationPath(in.SessionID, in.Token))
	// The tombstone is already committed. If Close fails, retain the generation
	// handle for retry but report success: returning an error would tell the caller
	// it owns no lease while this adapter still tracks an active generation.
	_ = l.relinquish(gen)
	return nil
}

func (l *Lease) lockSession(ctx context.Context, id session.SessionID) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.gatesMu.Lock()
	gate := l.gates[id]
	if gate == nil {
		gate = &sessionGate{}
		l.gates[id] = gate
	}
	gate.refs++
	l.gatesMu.Unlock()

	gate.mu.Lock()
	if err := ctx.Err(); err != nil {
		gate.mu.Unlock()
		l.releaseGate(id, gate)
		return nil, err
	}
	return func() {
		gate.mu.Unlock()
		l.releaseGate(id, gate)
	}, nil
}

func (l *Lease) releaseGate(id session.SessionID, gate *sessionGate) {
	l.gatesMu.Lock()
	gate.refs--
	if gate.refs == 0 && l.gates[id] == gate {
		delete(l.gates, id)
	}
	l.gatesMu.Unlock()
}

func (l *Lease) lockStable(ctx context.Context, id session.SessionID) (*flock.Flock, error) {
	fl := flock.New(l.lockPath(id))
	locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		l.keepForCleanup(id, fl)
		return nil, fmt.Errorf("flocklease: acquire transition lock %q: %w", fl.Path(), err)
	}
	if !locked {
		l.keepForCleanup(id, fl)
		return nil, ctx.Err()
	}
	return fl, nil
}

// finishStable distinguishes failures before and after the durable record
// transition. Before commit, a Close failure is returned fail-closed. After
// commit, the operation succeeds and the handle is retained for cleanup: the
// caller must never receive an error while an active committed generation is
// only known to this adapter.
func (l *Lease) finishStable(id session.SessionID, fl *flock.Flock, result *error, committed *bool) {
	if err := fl.Close(); err != nil {
		l.addCleanup(id, fl)
		if !*committed {
			*result = errors.Join(*result, fmt.Errorf("flocklease: close transition lock %q: %w", fl.Path(), err))
		}
	}
}

// generationLive probes a recorded generation while the stable transition lock
// is held. A free lock means its process crashed. The probe is always closed; a
// close failure is retained and makes the transition fail closed.
func (l *Lease) generationLive(id session.SessionID, token uint64) (bool, error) {
	probe := flock.New(l.generationPath(id, token))
	locked, err := probe.TryLock()
	if err != nil {
		l.keepForCleanup(id, probe)
		return false, fmt.Errorf("flocklease: probe generation lock %q: %w", probe.Path(), err)
	}
	if !locked {
		l.keepForCleanup(id, probe)
		return true, nil
	}
	if err := probe.Close(); err != nil {
		l.addCleanup(id, probe)
		return false, fmt.Errorf("flocklease: close generation probe %q: %w", probe.Path(), err)
	}
	return false, nil
}

func (l *Lease) getHeld(gen generation) *heldLease {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held[gen]
}

func (l *Lease) setHeld(gen generation, held *heldLease) {
	l.mu.Lock()
	l.held[gen] = held
	l.mu.Unlock()
}

func (l *Lease) relinquish(gen generation) error {
	held := l.getHeld(gen)
	if held == nil {
		return nil
	}
	if err := held.fl.Close(); err != nil {
		return fmt.Errorf("flocklease: close generation lock %q: %w", held.fl.Path(), err)
	}
	l.mu.Lock()
	if l.held[gen] == held {
		delete(l.held, gen)
	}
	l.mu.Unlock()
	return nil
}

func (l *Lease) addCleanup(id session.SessionID, fl *flock.Flock) {
	l.mu.Lock()
	l.cleanup[id] = append(l.cleanup[id], fl)
	l.mu.Unlock()
}

func (l *Lease) keepForCleanup(id session.SessionID, fl *flock.Flock) {
	if err := fl.Close(); err != nil {
		l.addCleanup(id, fl)
	}
}

func (l *Lease) removeObsolete(id session.SessionID, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		l.mu.Lock()
		l.cleanupPaths[id] = append(l.cleanupPaths[id], path)
		l.mu.Unlock()
	}
}

// retryCleanup handles only this session's deferred resources. Combined with the
// per-session gate, a slow or broken session cannot delay another session's lease
// transition, and completed gate entries are removed rather than retained by id.
func (l *Lease) retryCleanup(id session.SessionID) {
	l.mu.Lock()
	handles := l.cleanup[id]
	paths := l.cleanupPaths[id]
	delete(l.cleanup, id)
	delete(l.cleanupPaths, id)
	l.mu.Unlock()

	pendingHandles := handles[:0]
	for _, fl := range handles {
		if err := fl.Close(); err != nil {
			pendingHandles = append(pendingHandles, fl)
		}
	}
	pendingPaths := paths[:0]
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			pendingPaths = append(pendingPaths, path)
		}
	}
	if len(pendingHandles) == 0 && len(pendingPaths) == 0 {
		return
	}
	l.mu.Lock()
	l.cleanup[id] = append(l.cleanup[id], pendingHandles...)
	l.cleanupPaths[id] = append(l.cleanupPaths[id], pendingPaths...)
	l.mu.Unlock()
}

func (l *Lease) readRecord(id session.SessionID) (record, error) {
	b, err := os.ReadFile(l.recordPath(id)) //nolint:gosec // path is sanitized via recordPath
	if os.IsNotExist(err) {
		return record{}, nil
	}
	if err != nil {
		return record{}, fmt.Errorf("flocklease: read record %q: %w", id, err)
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return record{}, fmt.Errorf("flocklease: decode record %q: %w", id, err)
	}
	return r, nil
}

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

func (l *Lease) generationPath(id session.SessionID, token uint64) string {
	return filepath.Join(l.dir, safeName(id)+"."+strconv.FormatUint(token, 10)+".live")
}

func (l *Lease) recordPath(id session.SessionID) string {
	return filepath.Join(l.dir, safeName(id)+".lease.json")
}

func safeName(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	suffix := hex.EncodeToString(sum[:8])
	s := string(id)
	var b strings.Builder
	const maxPrefix = 40
	for _, r := range s {
		if b.Len() >= maxPrefix {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
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
