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

// compile-time assertion that Store satisfies the OPTIONAL ScheduleOneShotReArmer
// seam (ADR 0059 Phase 2 — the at-least-once one-shot re-arm). A store that does
// not implement it degrades to at-most-once (byte-identical pre-Phase-2).
var _ port.ScheduleOneShotReArmer = (*Store)(nil)

// compile-time assertion that Store satisfies the OPTIONAL ScheduleCreator seam
// (review finding 5, issue #368 — atomic create-only). A store that does not
// implement it degrades to the manager's check-then-Save (byte-identical
// pre-fix TOCTOU-accepted path).
var _ port.ScheduleCreator = (*Store)(nil)

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

// Create atomically creates a NEW schedule under in.Spec.Name (review finding
// 5, issue #368): under the SAME mutex Save/Load/Claim already serialize on,
// it checks for an existing record and creates the new one in one lock
// acquisition — unlike the manager's prior two-call Load-then-Save, which raced
// across two separate acquisitions. A name already in use returns
// ErrScheduleAlreadyExists (wrapped) and leaves the existing record untouched.
func (s *Store) Create(_ context.Context, in port.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.scheds[in.Spec.Name]; exists {
		return fmt.Errorf("memschedulestore: %w: %q", port.ErrScheduleAlreadyExists, in.Spec.Name)
	}
	// New schedule: honour an explicit State, but default a zero State to
	// enabled — the same convention Save applies for a brand-new name.
	rec := record{spec: cloneSpec(in.Spec), state: in.State}
	if in.State.NextFireAt.IsZero() && !in.State.Enabled && in.State.FireCount == 0 {
		rec.state.Enabled = true
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
	// A fresh claim has no in-flight run yet: zero the in-flight fields a prior
	// fire's RecordFireStart may have set (issue #386). RecordFireStart sets them;
	// RecordFire clears them; a new Claim starts from a clean slate.
	rec.state.LastFireStartedAt = time.Time{}
	rec.state.LastFireProgressAt = time.Time{}
	rec.state.FireDeadline = time.Time{}
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
	// A fresh claim has no in-flight run yet (see Claim's comment).
	rec.state.LastFireStartedAt = time.Time{}
	rec.state.LastFireProgressAt = time.Time{}
	rec.state.FireDeadline = time.Time{}
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
//
// It FLIPS the fire terminal and clears the in-flight ScheduleState fields
// (LastFireStartedAt/LastFireProgressAt/FireDeadline) — a recorded (terminal) fire
// has no in-flight run (issue #386). It overwrites any in-flight fire record the
// same f.ID had under RecordFireStart with the terminal one.
func (s *Store) RecordFire(_ context.Context, f port.ScheduleFire) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Idempotent per fire id: a re-record of an already-TERMINAL fire is a no-op.
	// (An in-flight record under the same id is overwritten with the terminal one
	// below — the fire transitions in-flight → terminal.)
	if existing, exists := s.fires[f.ID]; exists && existing.Stop != "" {
		return nil
	}
	rec, ok := s.scheds[f.ScheduleName]
	if !ok {
		return ErrNotFound
	}
	rec.state.LastFireSessionID = f.SessionID
	// Clear the in-flight fields: a terminal fire has no in-flight run.
	rec.state.LastFireStartedAt = time.Time{}
	rec.state.LastFireProgressAt = time.Time{}
	rec.state.FireDeadline = time.Time{}
	s.scheds[f.ScheduleName] = rec
	s.fires[f.ID] = cloneFire(f)
	return nil
}

// RecordFireStart persists the IN-FLIGHT fire (issue #386): the fire's run has
// begun but not yet produced a terminal outcome. It writes the fire record
// (in-flight: Stop empty, StartedAt set) and stamps the schedule's
// LastFireSessionID to the REAL session id (overwriting the pending sentinel
// Claim set) + LastFireStartedAt (and seeds LastFireProgressAt to StartedAt when
// the caller passed a zero ProgressAt) + FireDeadline. It is IDEMPOTENT per fire
// id: a re-record of the same in-flight fire (same StartedAt) is a no-op for the
// in-flight record; a re-record for a fire id that is ALREADY terminal is a
// no-op (a terminal fire is not re-opened). The not-found case wraps
// ErrScheduleNotFound.
func (s *Store) RecordFireStart(_ context.Context, name string, fire port.ScheduleFire) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.scheds[name]
	if !ok {
		return ErrNotFound
	}
	// A fire id that is already terminal is not re-opened.
	if existing, exists := s.fires[fire.ID]; exists && existing.Stop != "" {
		return nil
	}
	// Idempotent per fire id: a re-record of the same in-flight fire (same
	// StartedAt) is a no-op for the in-flight record.
	if existing, exists := s.fires[fire.ID]; exists && existing.StartedAt.Equal(fire.StartedAt) {
		return nil
	}
	rec.state.LastFireSessionID = fire.SessionID
	rec.state.LastFireStartedAt = fire.StartedAt
	if fire.ProgressAt.IsZero() {
		rec.state.LastFireProgressAt = fire.StartedAt
	} else {
		rec.state.LastFireProgressAt = fire.ProgressAt
	}
	rec.state.FireDeadline = fire.Deadline
	s.scheds[name] = rec
	s.fires[fire.ID] = cloneFire(fire)
	return nil
}

// RecordFireProgress advances the in-flight fire's last-observed-progress
// instant (issue #386). It updates LastFireProgressAt on the state and ProgressAt
// on the in-flight fire record (when `at` is after the stored value — an earlier
// `at` is ignored so a reordered update cannot rewind progress). It is
// best-effort/idempotent: a missing in-flight fire record records on the state
// alone; a not-found schedule wraps ErrScheduleNotFound; a terminal fire is
// untouched. fireID targets the single in-flight fire record by its known id
// directly (review finding M1 — no scan); a terminal fire record is untouched
// (review finding M2 — never revert terminal → in-flight).
func (s *Store) RecordFireProgress(_ context.Context, name string, fireID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.scheds[name]
	if !ok {
		return ErrNotFound
	}
	if !at.IsZero() && at.After(rec.state.LastFireProgressAt) {
		rec.state.LastFireProgressAt = at
		s.scheds[name] = rec
	}
	// Advance the in-flight fire record's ProgressAt directly by its known id
	// (no scan). Best-effort: a missing record is fine (the state alone carries
	// it); a TERMINAL fire record (Stop non-empty) is untouched — never revert
	// terminal → in-flight (review finding M2).
	f, ok := s.fires[fireID]
	if !ok {
		return nil // best-effort: no in-flight record; the state alone carries it
	}
	if f.Stop != "" {
		return nil // terminal fire: untouched
	}
	if !at.IsZero() && at.After(f.ProgressAt) {
		f.ProgressAt = at
		s.fires[fireID] = f
	}
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

// ReArmOneShot is the at-least-once re-arm primitive for a one-shot schedule
// (ADR 0059 Phase 2). It atomically: re-enables the schedule (Enabled=true),
// sets NextFireAt to nextFire, and increments OneShotRetryCount. The atomicity
// (the single mutex) is the re-arm fence: two concurrent re-arms cannot
// double-increment the counter or double-enable. The not-found case wraps
// ErrScheduleNotFound. The retry-budget gate (OneShotRetryCount <
// OneShotMaxRetries) is the CALLER's responsibility — the store does NOT enforce
// the budget, it only atomically advances the counter. One-shot-only; the caller
// never calls this on a cron schedule.
func (s *Store) ReArmOneShot(_ context.Context, name string, nextFire time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.scheds[name]
	if !ok {
		return ErrNotFound
	}
	rec.state.Enabled = true
	rec.state.NextFireAt = nextFire
	rec.state.OneShotRetryCount++
	s.scheds[name] = rec
	return nil
}

// cloneSpec returns a copy of spec whose Parts slice AND Owner pointer are
// independent of the stored one (so a caller cannot mutate the store's record
// through the returned Schedule). ScheduleSpec is otherwise a struct of values.
func cloneSpec(spec port.ScheduleSpec) port.ScheduleSpec {
	out := spec
	if spec.Parts != nil {
		out.Parts = make([]session.Content, len(spec.Parts))
		copy(out.Parts, spec.Parts)
	}
	out.Owner = spec.Owner.Clone()
	return out
}

// cloneFire returns a copy of f (ScheduleFire is a struct of values; this is
// for symmetry with cloneSpec so a future slice field is isolated by
// construction).
func cloneFire(f port.ScheduleFire) port.ScheduleFire {
	return f
}
