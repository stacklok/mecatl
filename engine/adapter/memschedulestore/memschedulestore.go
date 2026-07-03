// Package memschedulestore is the in-memory reference port.ScheduleStore
// (scheduled-tasks issue #189, Phase 1b): the offline, clock-injected
// single-process schedule registry the conformance suite validates and that
// composition can wire as the default-store opt-in. It keeps per-schedule
// Spec+State records and per-fire ScheduleFire records in mutex-guarded maps
// and reads `now` from an injected port.Clock, so tests advance a fake clock
// to express "the schedule is now due" / "the slot was missed" without real
// sleeps.
//
// It is the reference for the SAME port.ScheduleStore contract a future JSONL
// store, a gRPC-driver client, and a k8s-backed store implement; the contract
// is the port, the in-memory map is one adapter. The conformance suite
// (engine/adapter/scheduleconformance) is the dual-path contract-unification:
// the Go port is the contract, the wire/disk is one adapter. This is the
// SECOND adapter-validation pattern after leases (leaseconformance).
//
// PARSER-FREE: the store NEVER interprets a cron expression. Claim's nextFire
// is caller-computed (composition, which has the cronparse dependency). The
// store does NOT import engine/adapter/cronparse — that is a deliberate
// layering choice: the store is the durable ground truth, the parser is a
// composition-time helper, and a store backend (e.g. a remote SQL table) need
// not ship a cron parser to satisfy the port.
//
// MISFIRE-FREE: the store does NOT read ScheduleSpec.Misfire. Due returns any
// schedule whose NextFireAt <= now (plus Enabled + MaxFires); the misfire
// policy (fire-once-now vs skip) is a COMPOSITION concern applied at tick time.
package memschedulestore

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ErrNotFound is returned by Load/Delete/Claim/RecordFire/LoadFire when no
// schedule (or fire) exists under the requested name/id. It wraps
// port.ErrScheduleNotFound so a consumer that may not import this adapter (e.g.
// engine/agent) can distinguish not-found from an infra failure via errors.Is,
// the same discipline memstore applies for ErrSessionNotFound.
var ErrNotFound = fmt.Errorf("memschedulestore: schedule not found: %w", port.ErrScheduleNotFound)

// record is a stored schedule's full state: the immutable Spec plus the
// durable firing State. The two halves are separate so a Save can overwrite
// the Spec while preserving the State (the port doc's "State preserved on
// overwrite" rule).
type record struct {
	spec  port.ScheduleSpec
	state port.ScheduleState
}

// Store is a concurrency-safe in-memory ScheduleStore. The Claim path is the
// at-most-once atomic advance: under the mutex it re-checks the slot is still
// due (a peer's Claim may have advanced it between this caller's Due and
// Claim) and, only if still due, advances NextFireAt + LastFireAt, bumps
// FireCount, and stamps port.PendingFireSessionID on LastFireSessionID — all before
// the fire runs (claim-before-fire).
type Store struct {
	mu     sync.Mutex
	scheds map[string]record
	fires  map[string]port.ScheduleFire
}

// compile-time assertion that Store satisfies the port.
var _ port.ScheduleStore = (*Store)(nil)

// New constructs an in-memory ScheduleStore. Every port.ScheduleStore method
// takes `now` as an explicit argument, so the store needs no injected clock of
// its own — time is caller-supplied. (An earlier draft injected a port.Clock
// reserved for a future self-pushing tick helper, but it was never read; it was
// dropped rather than carried as dead constructor surface. If a lookahead phase
// ever needs an internal `now`, re-add the clock then — YAGNI until it exists.)
func New() *Store {
	return &Store{
		scheds: make(map[string]record),
		fires:  make(map[string]port.ScheduleFire),
	}
}

// Save upserts the schedule by Spec.Name. A schedule with the same name is
// overwritten on the Spec half; the State half is PRESERVED on overwrite (a
// Save with a fresh zero State does not reset firing progress — call Delete +
// Save to reset, the port doc says so). A NEW schedule is initialised with
// Enabled=true (a new schedule is active by default; pause it by overwriting
// State.Enabled=false, which Save preserves on re-save).
func (s *Store) Save(_ context.Context, in port.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, existed := s.scheds[in.Spec.Name]
	rec := record{spec: cloneSpec(in.Spec)}
	if existed {
		rec.state = cur.state // preserve firing progress on overwrite
	} else {
		// New schedule: honour an explicit State, but default a zero State to
		// enabled (a freshly-created schedule is active). A caller that wants a
		// new schedule disabled passes a non-zero State with Enabled=false.
		rec.state = in.State
		if in.State.NextFireAt.IsZero() && !in.State.Enabled && in.State.FireCount == 0 {
			rec.state.Enabled = true
		}
	}
	s.scheds[in.Spec.Name] = rec
	return nil
}

// Load returns the schedule stored under name. The not-found case wraps
// ErrScheduleNotFound. The returned Schedule is a DEEP COPY (the State is a
// struct, the Parts slice is copied) so a caller cannot mutate the store's
// record through the returned reference — the memstore precedent.
func (s *Store) Load(_ context.Context, name string) (port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.scheds[name]
	if !ok {
		return port.Schedule{}, ErrNotFound
	}
	return port.Schedule{Spec: cloneSpec(rec.spec), State: rec.state}, nil
}

// Delete removes the schedule stored under name. It is IDEMPOTENT: deleting an
// unknown name is success (the PrunableStore.Delete discipline).
func (s *Store) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scheds, name)
	return nil
}

// List returns ALL stored schedules, in no guaranteed order, as deep copies.
func (s *Store) List(_ context.Context) ([]port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]port.Schedule, 0, len(s.scheds))
	for _, rec := range s.scheds {
		out = append(out, port.Schedule{Spec: cloneSpec(rec.spec), State: rec.state})
	}
	return out, nil
}

// Due returns the schedules whose NextFireAt <= now AND Enabled AND (when
// MaxFires > 0) FireCount < MaxFires. It is idempotent and side-effect-free;
// it does not advance state. The store does NOT read ScheduleSpec.Misfire —
// the misfire policy is a composition concern.
func (s *Store) Due(_ context.Context, now time.Time) ([]port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]port.Schedule, 0, len(s.scheds))
	for _, rec := range s.scheds {
		if !rec.state.Enabled {
			continue
		}
		if rec.state.NextFireAt.IsZero() || rec.state.NextFireAt.After(now) {
			continue
		}
		if rec.spec.MaxFires > 0 && rec.state.FireCount >= rec.spec.MaxFires {
			continue
		}
		out = append(out, port.Schedule{Spec: cloneSpec(rec.spec), State: rec.state})
	}
	return out, nil
}

// Claim is the AT-MOST-ONCE atomic advance. Under the mutex it re-checks the
// slot is still due (NextFireAt <= now && Enabled && under MaxFires); if NOT
// (a peer's Claim already advanced it, or it was disabled, or it exhausted)
// it returns ErrScheduleNotFound — the slot is gone, the fail-safe
// interpretation the conformance suite pins (a second Claim at the same now
// does NOT re-claim). If still due, it atomically: sets LastFireAt=now,
// advances NextFireAt to nextFire, increments FireCount, stamps
// LastFireSessionID=port.PendingFireSessionID, and (for a zero nextFire: a one-shot or
// an exhausted cron) sets Enabled=false. It returns the claimed Schedule (with
// the advanced State).
func (s *Store) Claim(_ context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.scheds[name]
	if !ok {
		return port.Schedule{}, ErrNotFound
	}
	// Re-check the at-most-once fence: the slot must STILL be due at claim time.
	// A peer's Claim between this caller's Due and Claim advanced NextFireAt
	// past `now`; the slot is gone.
	if !rec.state.Enabled {
		return port.Schedule{}, ErrNotFound
	}
	if rec.state.NextFireAt.IsZero() || rec.state.NextFireAt.After(now) {
		return port.Schedule{}, ErrNotFound
	}
	if rec.spec.MaxFires > 0 && rec.state.FireCount >= rec.spec.MaxFires {
		return port.Schedule{}, ErrNotFound
	}
	// Atomically advance (claim-before-fire).
	rec.state.LastFireAt = now
	rec.state.NextFireAt = nextFire
	rec.state.FireCount++
	rec.state.LastFireSessionID = port.PendingFireSessionID
	if nextFire.IsZero() {
		// A one-shot fired, or a cron whose MaxFires is exhausted: no further
		// fire. The schedule is DONE.
		rec.state.Enabled = false
	}
	s.scheds[name] = rec
	return port.Schedule{Spec: cloneSpec(rec.spec), State: rec.state}, nil
}

// ClaimNow is the manual-trigger variant of Claim (the FireNow primitive). It
// performs the SAME atomic advance as Claim but does NOT enforce the
// NextFireAt <= now due-check — it claims the slot regardless of whether it is
// due (a manual fire bypasses the cadence but still claims atomically for
// at-most-once). The Enabled + MaxFires checks STILL apply. The at-most-once
// fence is LastFireAt == now: a second ClaimNow at the same now (or a ClaimNow
// racing a tick-loop Claim at the same now) is rejected — the advance already
// happened. See port.ScheduleStore.ClaimNow for the crash-recoverability rationale.
func (s *Store) ClaimNow(_ context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.scheds[name]
	if !ok {
		return port.Schedule{}, ErrNotFound
	}
	if !rec.state.Enabled {
		return port.Schedule{}, ErrNotFound
	}
	if rec.spec.MaxFires > 0 && rec.state.FireCount >= rec.spec.MaxFires {
		return port.Schedule{}, ErrNotFound
	}
	// At-most-once fence WITHOUT the due-check: a prior ClaimNow (or a Claim) at
	// this same `now` already advanced LastFireAt to `now`. Reject so the advance
	// is not repeated (Claim's fence is "NextFireAt is past now", which a future
	// slot fails — ClaimNow cannot use it).
	if rec.state.LastFireAt.Equal(now) {
		return port.Schedule{}, ErrNotFound
	}
	// Atomically advance (claim-before-fire).
	rec.state.LastFireAt = now
	rec.state.NextFireAt = nextFire
	rec.state.FireCount++
	rec.state.LastFireSessionID = port.PendingFireSessionID
	if nextFire.IsZero() {
		rec.state.Enabled = false
	}
	s.scheds[name] = rec
	return port.Schedule{Spec: cloneSpec(rec.spec), State: rec.state}, nil
}

// SetEnabled atomically sets the schedule's Enabled flag WITHOUT touching any
// other State field (unlike Save, which preserves the State half on a Spec
// overwrite and so cannot mutate Enabled). It is the pause/resume primitive.
// The not-found case wraps ErrScheduleNotFound.
func (s *Store) SetEnabled(_ context.Context, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.scheds[name]
	if !ok {
		return ErrNotFound
	}
	rec.state.Enabled = enabled
	s.scheds[name] = rec
	return nil
}

// RecordFire records the outcome of a fire (f) and updates the schedule's
// LastFireSessionID to f.SessionID (overwriting the port.PendingFireSessionID value
// Claim set). It is IDEMPOTENT per fire id: recording the same f.ID twice is a
// no-op (the second call returns nil without mutating state). The not-found
// case (the schedule was deleted between Claim and RecordFire) wraps
// ErrScheduleNotFound.
func (s *Store) RecordFire(_ context.Context, f port.ScheduleFire) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Idempotent per fire id: a re-record of an already-stored fire is a no-op.
	if _, exists := s.fires[f.ID]; exists {
		return nil
	}
	rec, ok := s.scheds[f.ScheduleName]
	if !ok {
		return ErrNotFound
	}
	rec.state.LastFireSessionID = f.SessionID
	s.scheds[f.ScheduleName] = rec
	s.fires[f.ID] = cloneFire(f)
	return nil
}

// LoadFire returns the fire record stored under fireID. The not-found case
// wraps ErrScheduleNotFound.
func (s *Store) LoadFire(_ context.Context, fireID string) (port.ScheduleFire, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.fires[fireID]
	if !ok {
		return port.ScheduleFire{}, ErrNotFound
	}
	return cloneFire(f), nil
}

// ListFires returns the fire records for a schedule, in no guaranteed order. The
// not-found case for the SCHEDULE wraps ErrScheduleNotFound; an empty fire list
// for an existing schedule is a successful empty slice (not an error).
func (s *Store) ListFires(_ context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.scheds[scheduleName]; !ok {
		return nil, ErrNotFound
	}
	out := make([]port.ScheduleFire, 0)
	for _, f := range s.fires {
		if f.ScheduleName != scheduleName {
			continue
		}
		out = append(out, cloneFire(f))
	}
	return out, nil
}

// cloneSpec returns a copy of spec whose Parts slice is independent of the
// stored one (so a caller cannot mutate the store's record through the
// returned Schedule). ScheduleSpec is otherwise a struct of values.
func cloneSpec(spec port.ScheduleSpec) port.ScheduleSpec {
	out := spec
	if spec.Parts != nil {
		out.Parts = make([]session.Content, len(spec.Parts))
		copy(out.Parts, spec.Parts)
	}
	return out
}

// cloneFire returns a copy of f (ScheduleFire is a struct of values; this is
// for symmetry with cloneSpec so a future slice field is isolated by
// construction).
func cloneFire(f port.ScheduleFire) port.ScheduleFire {
	return f
}
