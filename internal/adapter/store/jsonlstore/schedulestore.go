package jsonlstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// scheduleFormat is the per-record format tag written on every schedule and
// fire file. It versions the on-disk encoding so a future incompatible format
// fails loud on load (the eventLogFormat precedent): an unknown tag is an
// infra error, never a silent skip.
const scheduleFormat = "jsonlstore-schedule/1"

// ErrScheduleNotFound is returned by Load/Delete/Claim/RecordFire/LoadFire when
// no schedule (or fire) exists under the requested name/id. It wraps
// port.ErrScheduleNotFound so a consumer that may not import this adapter can
// distinguish not-found from an infra failure via errors.Is, the same
// discipline the session store applies for ErrNotFound.
var ErrScheduleNotFound = fmt.Errorf("jsonlstore: schedule not found: %w", port.ErrScheduleNotFound)

// schedulePrefix / scheduleFirePrefix are the filename prefixes the schedule
// store writes under the SAME dir as the session store:
//
//	<dir>/schedule--<safeName>.json      — one schedule (Spec+State), latest wins
//	<dir>/schedulefire--<safeFireID>.json — one fire record
//
// The two prefixes are deliberately NON-OVERLAPPING: neither is a prefix of the
// other (after "schedule", one continues with "--" and the other with "fire--").
// An earlier scheme used "schedule-" and "schedule-fire-", where the fire prefix
// was a SUB-prefix of the schedule prefix plus a name beginning "fire-": a
// schedule named "fire-foo" produced "schedule-fire-foo.json", COLLIDING with
// fire id "foo"'s "schedule-fire-foo.json" (one silently overwrote the other),
// and the List/Due filters skipped it as a fire record — so a schedule named
// "fire-*" never fired and never listed (review #189). The distinct "schedule--"
// / "schedulefire--" prefixes remove the overlap entirely. This is unreleased, so
// there is no on-disk migration.
//
// A schedule is a single UPSERTED record (NOT append-only like the session
// snapshots): a Save overwrites the file in place, because a schedule is one
// upserted spec+state, not a replayable audit trail. A fire record is write-once
// (RecordFire is idempotent per fire id, so a second write is a no-op).
const (
	schedulePrefix     = "schedule--"
	scheduleFirePrefix = "schedulefire--"
	scheduleSuffix     = ".json"
)

// scheduleRecord is the envelope written to a schedule file: a format tag plus
// the verbatim port.Schedule JSON. The tag lets Load validate the encoding
// version (a forward-incompatible file must fail loud, not silently decode).
type scheduleRecord struct {
	V        string        `json:"v"`
	Schedule port.Schedule `json:"schedule"`
}

// scheduleFireRecord is the envelope written to a fire file: a format tag plus
// the verbatim port.ScheduleFire JSON.
type scheduleFireRecord struct {
	V    string            `json:"v"`
	Fire port.ScheduleFire `json:"fire"`
}

// scheduleStore is a file-backed port.ScheduleStore sharing the parent *Store's
// dir + single-process mutex. It is the single-host production schedule
// backend (scheduled-tasks issue #189, Phase 1d): the SAME logic as
// memschedulestore with file persistence. The mutex is the at-most-once fence
// for the Claim race — this is SINGLE-HOST ONLY (the same scope as the flock
// lease): two replicas pointing at the same dir have NO cross-process fence and
// MUST instead run a leader lease (port.SessionLease on
// port.SchedulerLeaderLeaseID) so at most one replica ticks. The redis backend
// (a SEPARATE follow-up) is the multi-host fence.
//
// It is parser-free (Claim's nextFire is caller-computed) and misfire-free
// (Due does not read ScheduleSpec.Misfire) — the same discipline as the
// in-memory reference.
type scheduleStore struct {
	dir string
	mu  *sync.Mutex // shared with the parent *Store so appends serialize across both stores
}

// compile-time assertion that scheduleStore satisfies the port.
var _ port.ScheduleStore = (*scheduleStore)(nil)

// compile-time assertion that scheduleStore satisfies the OPTIONAL
// ScheduleOneShotReArmer seam (ADR 0059 Phase 2).
var _ port.ScheduleOneShotReArmer = (*scheduleStore)(nil)

// compile-time assertion that scheduleStore satisfies the OPTIONAL
// ScheduleCreator seam (review finding 5, issue #368 — atomic create-only).
var _ port.ScheduleCreator = (*scheduleStore)(nil)

// Create atomically creates a NEW schedule under in.Spec.Name (review finding
// 5, issue #368): under the SAME mutex Save/Load already serialize on, it
// checks for an existing record and writes the new one in one lock
// acquisition — unlike the manager's prior two-call Load-then-Save, which
// raced across two separate acquisitions (this store is single-host-only
// anyway — see the type doc — so the mutex alone is sufficient, no OS-level
// O_CREATE|O_EXCL needed). A name already in use returns
// ErrScheduleAlreadyExists (wrapped) and leaves the existing file untouched;
// any OTHER loadLocked error (a corrupt file, an I/O failure) is propagated
// rather than treated as "absent".
func (s *scheduleStore) Create(_ context.Context, in port.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadLocked(in.Spec.Name); err == nil {
		return fmt.Errorf("jsonlstore: %w: %q", port.ErrScheduleAlreadyExists, in.Spec.Name)
	} else if !errors.Is(err, ErrScheduleNotFound) {
		return err
	}
	rec := scheduleRecord{V: scheduleFormat}
	rec.Schedule.Spec = cloneSpec(in.Spec)
	// New schedule: honour an explicit State, but default a zero State to
	// enabled — the same convention Save applies for a brand-new name.
	rec.Schedule.State = in.State
	if in.State.NextFireAt.IsZero() && !in.State.Enabled && in.State.FireCount == 0 {
		rec.Schedule.State.Enabled = true
	}
	return s.writeScheduleLocked(in.Spec.Name, rec)
}

// Save upserts the schedule by Spec.Name. A schedule with the same name is
// overwritten on the Spec half; the State half is PRESERVED on overwrite (a
// Save with a fresh zero State does not reset firing progress — call Delete +
// Save to reset, the port doc says so). A NEW schedule is initialised with
// Enabled=true (a new schedule is active by default; pause it by overwriting
// State.Enabled=false, which Save preserves on re-save). The semantics mirror
// memschedulestore.Save byte-for-byte.
func (s *scheduleStore) Save(_ context.Context, in port.Schedule) error {
	rec := scheduleRecord{V: scheduleFormat}
	rec.Schedule.Spec = cloneSpec(in.Spec)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, err := s.loadLocked(in.Spec.Name); err == nil {
		// Preserve firing progress on overwrite.
		rec.Schedule.State = cur.Schedule.State
	} else {
		// New schedule: honour an explicit State, but default a zero State to
		// enabled (a freshly-created schedule is active). A caller that wants a
		// new schedule disabled passes a non-zero State with Enabled=false.
		rec.Schedule.State = in.State
		if in.State.NextFireAt.IsZero() && !in.State.Enabled && in.State.FireCount == 0 {
			rec.Schedule.State.Enabled = true
		}
	}
	return s.writeScheduleLocked(in.Spec.Name, rec)
}

// SetEnabled atomically sets the schedule's Enabled flag WITHOUT touching any
// other State field (unlike Save, which preserves the State half on a Spec
// overwrite and so cannot mutate Enabled). It is the pause/resume primitive.
// The not-found case wraps ErrScheduleNotFound.
func (s *scheduleStore) SetEnabled(_ context.Context, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadLocked(name)
	if err != nil {
		return err
	}
	rec.Schedule.State.Enabled = enabled
	return s.writeScheduleLocked(name, rec)
}

// Load returns the schedule stored under name. The not-found case wraps
// port.ErrScheduleNotFound.
func (s *scheduleStore) Load(_ context.Context, name string) (port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadLocked(name)
	if err != nil {
		return port.Schedule{}, err
	}
	return port.Schedule{Spec: cloneSpec(rec.Schedule.Spec), State: rec.Schedule.State}, nil
}

// Delete removes the schedule stored under name. It is IDEMPOTENT: deleting an
// unknown name is success (the PrunableStore.Delete discipline).
func (s *scheduleStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.schedulePath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("jsonlstore: delete schedule %q: %w", name, err)
	}
	return nil
}

// List returns ALL stored schedules, in no guaranteed order.
func (s *scheduleStore) List(_ context.Context) ([]port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: list schedule dir: %w", err)
	}
	var out []port.Schedule
	for _, e := range entries {
		// The "schedule--" and "schedulefire--" prefixes are non-overlapping, so a
		// fire record never matches the schedule prefix — no fire-skip needed.
		if e.IsDir() || !strings.HasPrefix(e.Name(), schedulePrefix) || !strings.HasSuffix(e.Name(), scheduleSuffix) {
			continue
		}
		rec, err := readScheduleFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			// A corrupt file is skipped, not fatal (best-effort listing).
			continue
		}
		out = append(out, port.Schedule{Spec: cloneSpec(rec.Schedule.Spec), State: rec.Schedule.State})
	}
	return out, nil
}

// Due returns the schedules whose NextFireAt <= now AND Enabled AND (when
// MaxFires > 0) FireCount < MaxFires. It is idempotent and side-effect-free;
// it does not advance state. The store does NOT read ScheduleSpec.Misfire —
// the misfire policy is a composition concern.
func (s *scheduleStore) Due(_ context.Context, now time.Time) ([]port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: list schedule dir: %w", err)
	}
	out := make([]port.Schedule, 0)
	for _, e := range entries {
		// Non-overlapping prefixes (see List) — no fire-skip needed.
		if e.IsDir() || !strings.HasPrefix(e.Name(), schedulePrefix) || !strings.HasSuffix(e.Name(), scheduleSuffix) {
			continue
		}
		rec, err := readScheduleFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		st := rec.Schedule.State
		spec := rec.Schedule.Spec
		if !st.Enabled {
			continue
		}
		if st.NextFireAt.IsZero() || st.NextFireAt.After(now) {
			continue
		}
		if spec.MaxFires > 0 && st.FireCount >= spec.MaxFires {
			continue
		}
		out = append(out, port.Schedule{Spec: cloneSpec(spec), State: st})
	}
	return out, nil
}

// Claim is the AT-MOST-ONCE atomic advance. Under the shared mutex it re-checks
// the slot is still due (NextFireAt <= now && Enabled && under MaxFires); if
// NOT (a peer's Claim already advanced it, or it was disabled, or it exhausted)
// it returns ErrScheduleNotFound — the slot is gone, the fail-safe
// interpretation the conformance suite pins (a second Claim at the same now
// does NOT re-claim). If still due, it atomically: sets LastFireAt=now,
// advances NextFireAt to nextFire, increments FireCount, stamps
// LastFireSessionID=port.PendingFireSessionID, and (for a zero nextFire: a one-shot or
// an exhausted cron) sets Enabled=false. It returns the claimed Schedule (with
// the advanced State), and the durable file reflects the advance (Claim is
// durable, not a transient return value).
//
// SINGLE-HOST: the mutex is the fence. Two replicas pointing at the same dir
// have NO cross-process fence and MUST run a leader lease (the redis backend is
// the multi-host fence). This is the same scope as the flock lease.
func (s *scheduleStore) Claim(_ context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadLocked(name)
	if err != nil {
		return port.Schedule{}, err
	}
	st := rec.Schedule.State
	spec := rec.Schedule.Spec
	// Re-check the at-most-once fence: the slot must STILL be due at claim time.
	if !st.Enabled {
		return port.Schedule{}, ErrScheduleNotFound
	}
	if st.NextFireAt.IsZero() || st.NextFireAt.After(now) {
		return port.Schedule{}, ErrScheduleNotFound
	}
	if spec.MaxFires > 0 && st.FireCount >= spec.MaxFires {
		return port.Schedule{}, ErrScheduleNotFound
	}
	// Atomically advance (claim-before-fire).
	st.LastFireAt = now
	st.NextFireAt = nextFire
	st.FireCount++
	st.LastFireSessionID = port.PendingFireSessionID
	// A fresh claim has no in-flight run yet: zero the in-flight fields a prior
	// fire's RecordFireStart may have set (issue #386). RecordFireStart sets them;
	// RecordFire clears them; a new Claim starts from a clean slate.
	st.LastFireStartedAt = time.Time{}
	st.LastFireProgressAt = time.Time{}
	st.FireDeadline = time.Time{}
	if nextFire.IsZero() {
		// A one-shot fired, or a cron whose MaxFires is exhausted: no further
		// fire. The schedule is DONE.
		st.Enabled = false
	}
	rec.Schedule.State = st
	if err := s.writeScheduleLocked(name, rec); err != nil {
		return port.Schedule{}, err
	}
	return port.Schedule{Spec: cloneSpec(spec), State: st}, nil
}

// ClaimNow is the manual-trigger variant of Claim (the FireNow primitive). It
// performs the SAME atomic advance as Claim but does NOT enforce the
// NextFireAt <= now due-check — it claims the slot regardless of whether it is
// due. The Enabled + MaxFires checks STILL apply. The at-most-once fence is
// LastFireAt == now (a prior advance at this same now already happened). See
// port.ScheduleStore.ClaimNow for the rationale.
func (s *scheduleStore) ClaimNow(_ context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadLocked(name)
	if err != nil {
		return port.Schedule{}, err
	}
	st := rec.Schedule.State
	spec := rec.Schedule.Spec
	if !st.Enabled {
		return port.Schedule{}, ErrScheduleNotFound
	}
	if spec.MaxFires > 0 && st.FireCount >= spec.MaxFires {
		return port.Schedule{}, ErrScheduleNotFound
	}
	// At-most-once fence (no due-check): a prior Claim/ClaimNow at this same now
	// already advanced LastFireAt.
	if st.LastFireAt.Equal(now) {
		return port.Schedule{}, ErrScheduleNotFound
	}
	st.LastFireAt = now
	st.NextFireAt = nextFire
	st.FireCount++
	st.LastFireSessionID = port.PendingFireSessionID
	// A fresh claim has no in-flight run yet (see Claim's comment).
	st.LastFireStartedAt = time.Time{}
	st.LastFireProgressAt = time.Time{}
	st.FireDeadline = time.Time{}
	if nextFire.IsZero() {
		st.Enabled = false
	}
	rec.Schedule.State = st
	if err := s.writeScheduleLocked(name, rec); err != nil {
		return port.Schedule{}, err
	}
	return port.Schedule{Spec: cloneSpec(spec), State: st}, nil
}

// RecordFireStart persists the IN-FLIGHT fire (issue #386): the fire's run has
// begun but not yet produced a terminal outcome. It writes the fire record
// (in-flight: Stop empty, StartedAt set) and stamps the schedule's
// LastFireSessionID to the REAL session id (overwriting the pending sentinel
// Claim set) + LastFireStartedAt (and seeds LastFireProgressAt to StartedAt
// when the caller passed a zero ProgressAt) + FireDeadline. It is IDEMPOTENT
// per fire id: a re-record of the same in-flight fire (same StartedAt) is a
// no-op for the in-flight record; a re-record for a fire id that is ALREADY
// terminal is a no-op (a terminal fire is not re-opened). The not-found case
// (the schedule was deleted between Claim and RecordFireStart) wraps
// port.ErrScheduleNotFound. The semantics mirror memschedulestore.RecordFireStart
// byte-for-byte.
func (s *scheduleStore) RecordFireStart(_ context.Context, name string, fire port.ScheduleFire) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadLocked(name)
	if err != nil {
		return err
	}
	// A fire id that is already terminal is not re-opened.
	if existing, err := s.readFireFileLocked(fire.ID); err == nil && existing.Stop != "" {
		return nil
	}
	// Idempotent per fire id: a re-record of the same in-flight fire (same
	// StartedAt) is a no-op for the in-flight record.
	if existing, err := s.readFireFileLocked(fire.ID); err == nil && existing.StartedAt.Equal(fire.StartedAt) {
		return nil
	}
	rec.Schedule.State.LastFireSessionID = fire.SessionID
	rec.Schedule.State.LastFireStartedAt = fire.StartedAt
	if fire.ProgressAt.IsZero() {
		rec.Schedule.State.LastFireProgressAt = fire.StartedAt
	} else {
		rec.Schedule.State.LastFireProgressAt = fire.ProgressAt
	}
	rec.Schedule.State.FireDeadline = fire.Deadline
	if err := s.writeScheduleLocked(name, rec); err != nil {
		return err
	}
	inFlight := fire
	inFlight.Stop = "" // an in-flight fire has no terminal outcome
	return s.writeFireLocked(inFlight)
}

// RecordFireProgress advances the in-flight fire's last-observed-progress
// instant (issue #386). It updates LastFireProgressAt on the state and ProgressAt
// on the in-flight fire record (when `at` is after the stored value — an earlier
// `at` is ignored so a reordered update cannot rewind progress). It is
// best-effort/idempotent: a missing in-flight fire record records on the state
// alone; a not-found schedule wraps port.ErrScheduleNotFound; a terminal fire is
// untouched. The semantics mirror memschedulestore.RecordFireProgress
// byte-for-byte.
//
// fireID targets the SINGLE in-flight fire record by its known file directly
// (review finding M1): there is NO os.ReadDir scan of every fire file under the
// store mutex — the caller (the fire loop) has the fireID its RecordFireStart
// wrote, so the record is addressed by its key. A terminal fire record (Stop
// non-empty) is left untouched: the progress write MUST NOT revert a terminal
// record back to in-flight (review finding M2).
func (s *scheduleStore) RecordFireProgress(_ context.Context, name string, fireID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadLocked(name)
	if err != nil {
		return err
	}
	if !at.IsZero() && at.After(rec.Schedule.State.LastFireProgressAt) {
		rec.Schedule.State.LastFireProgressAt = at
		if err := s.writeScheduleLocked(name, rec); err != nil {
			return err
		}
	}
	// Advance the in-flight fire record's ProgressAt directly by its known id
	// (no directory scan). Best-effort: a missing record is fine (the state alone
	// carries it); a TERMINAL fire record (Stop non-empty) is untouched — the
	// progress write must never revert a terminal record to in-flight (review
	// finding M2).
	fire, err := s.readFireFileLocked(fireID)
	if err != nil {
		if errors.Is(err, ErrScheduleNotFound) {
			return nil // best-effort: no in-flight record; the state alone carries it
		}
		return err
	}
	if fire.Stop != "" {
		return nil // terminal fire: untouched (never revert terminal → in-flight)
	}
	if !at.IsZero() && at.After(fire.ProgressAt) {
		fire.ProgressAt = at
		if err := s.writeFireLocked(fire); err != nil {
			return err
		}
	}
	return nil
}

// RecordFire records the outcome of a fire (f) and updates the schedule's
// LastFireSessionID to f.SessionID (overwriting the port.PendingFireSessionID value
// Claim set). It is IDEMPOTENT per fire id: recording the same f.ID twice is a
// no-op (the second call returns nil without mutating state). The not-found
// case (the schedule was deleted between Claim and RecordFire) wraps
// port.ErrScheduleNotFound.
//
// It FLIPS the fire terminal and clears the in-flight ScheduleState fields
// (LastFireStartedAt/LastFireProgressAt/FireDeadline) — a recorded (terminal)
// fire has no in-flight run (issue #386). It overwrites any in-flight fire record
// the same f.ID had under RecordFireStart with the terminal one. The semantics
// mirror memschedulestore.RecordFire byte-for-byte.
func (s *scheduleStore) RecordFire(_ context.Context, f port.ScheduleFire) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Idempotent per fire id: a re-record of an already-TERMINAL fire is a no-op.
	// (An in-flight record under the same id is overwritten with the terminal one
	// below — the fire transitions in-flight → terminal.)
	if existing, err := s.readFireFileLocked(f.ID); err == nil && existing.Stop != "" {
		return nil
	}
	rec, err := s.loadLocked(f.ScheduleName)
	if err != nil {
		return err
	}
	rec.Schedule.State.LastFireSessionID = f.SessionID
	// Clear the in-flight fields: a terminal fire has no in-flight run.
	rec.Schedule.State.LastFireStartedAt = time.Time{}
	rec.Schedule.State.LastFireProgressAt = time.Time{}
	rec.Schedule.State.FireDeadline = time.Time{}
	if err := s.writeScheduleLocked(f.ScheduleName, rec); err != nil {
		return err
	}
	return s.writeFireLocked(f)
}

// LoadFire returns the fire record stored under fireID. The not-found case
// wraps port.ErrScheduleNotFound.
func (s *scheduleStore) LoadFire(_ context.Context, fireID string) (port.ScheduleFire, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readFireFileLocked(fireID)
}

// ListFires returns the fire records for a schedule, in no guaranteed order. The
// not-found case for the SCHEDULE wraps ErrScheduleNotFound; an empty fire list
// for an existing schedule is a successful empty slice (not an error). It scans
// the fire files (the schedulefire-- prefix) and filters by ScheduleName — a
// fire record carries its ScheduleName foreign key, so it is locatable without a
// per-schedule fire index.
func (s *scheduleStore) ListFires(_ context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The schedule must exist (the not-found-for-the-schedule contract).
	if _, err := s.loadLocked(scheduleName); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("jsonlstore: list fires dir: %w", err)
	}
	out := make([]port.ScheduleFire, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), scheduleFirePrefix) || !strings.HasSuffix(e.Name(), scheduleSuffix) {
			continue
		}
		// Read the fire file by its full path (readFireFileLocked re-encodes a
		// fireID via safeFilePart, which would double-encode the filename-derived
		// id; reading the file directly avoids that).
		rec, err := readScheduleFireFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue // best-effort: a corrupt fire file is skipped, not fatal.
		}
		if rec.Fire.ScheduleName != scheduleName {
			continue
		}
		out = append(out, rec.Fire)
	}
	return out, nil
}

// loadLocked reads the schedule file for name (caller holds mu). The not-found
// case wraps port.ErrScheduleNotFound.
func (s *scheduleStore) loadLocked(name string) (scheduleRecord, error) {
	rec, err := readScheduleFile(s.schedulePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return scheduleRecord{}, ErrScheduleNotFound
		}
		return scheduleRecord{}, err
	}
	return rec, nil
}

// writeScheduleLocked atomically writes the schedule record (caller holds mu).
// Atomic: write to a temp file then rename, so a crash mid-write leaves either
// the old or the new record, never a truncated half-record (the session store
// is append-only and need not; a schedule is a single upserted record, so it
// must). Mode 0o600: owner-only, matching the 0o700 dir (the file holds the
// schedule spec incl. the prompt text).
func (s *scheduleStore) writeScheduleLocked(name string, rec scheduleRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("jsonlstore: marshal schedule: %w", err)
	}
	return writeFileAtomic(s.schedulePath(name), b)
}

// writeFireLocked writes the fire record (caller holds mu). Write-once: the
// idempotent guard in RecordFire prevents a second write.
func (s *scheduleStore) writeFireLocked(f port.ScheduleFire) error {
	b, err := json.Marshal(scheduleFireRecord{V: scheduleFormat, Fire: f})
	if err != nil {
		return fmt.Errorf("jsonlstore: marshal schedule fire: %w", err)
	}
	return writeFileAtomic(s.firePath(f.ID), b)
}

// readFireFileLocked reads the fire record for fireID (caller holds mu). The
// not-found case wraps port.ErrScheduleNotFound.
func (s *scheduleStore) readFireFileLocked(fireID string) (port.ScheduleFire, error) {
	b, err := os.ReadFile(s.firePath(fireID)) //nolint:gosec // path is sanitized via firePath
	if err != nil {
		if os.IsNotExist(err) {
			return port.ScheduleFire{}, ErrScheduleNotFound
		}
		return port.ScheduleFire{}, fmt.Errorf("jsonlstore: read fire file: %w", err)
	}
	var rec scheduleFireRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return port.ScheduleFire{}, fmt.Errorf("jsonlstore: decode fire: %w", err)
	}
	if rec.V != scheduleFormat {
		return port.ScheduleFire{}, fmt.Errorf("jsonlstore: unknown schedule-fire format %q (want %q)", rec.V, scheduleFormat)
	}
	return rec.Fire, nil
}

// schedulePath / firePath map a schedule name / fire id to a filename-safe path
// under the store dir, so neither can traverse out of dir (the safeName
// discipline). A separate sanitizer accepts a bare string (schedule names and
// fire ids are caller-chosen strings, not session.SessionID).
func (s *scheduleStore) schedulePath(name string) string {
	return filepath.Join(s.dir, schedulePrefix+safeFilePart(name)+scheduleSuffix)
}

func (s *scheduleStore) firePath(fireID string) string {
	return filepath.Join(s.dir, scheduleFirePrefix+safeFilePart(fireID)+scheduleSuffix)
}

// readScheduleFile reads + validates a schedule file. A missing file is an
// os.IsNotExist error the caller maps to ErrScheduleNotFound.
func readScheduleFile(path string) (scheduleRecord, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is sanitized via schedulePath
	if err != nil {
		return scheduleRecord{}, err
	}
	var rec scheduleRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return scheduleRecord{}, fmt.Errorf("jsonlstore: decode schedule: %w", err)
	}
	if rec.V != scheduleFormat {
		return scheduleRecord{}, fmt.Errorf("jsonlstore: unknown schedule format %q (want %q)", rec.V, scheduleFormat)
	}
	return rec, nil
}

// readScheduleFireFile reads + validates a fire file by its full path (the
// path-based companion to readFireFileLocked, which takes a bare fireID and
// re-encodes it via safeFilePart). Used by ListFires, which scans fire files by
// filename and must not double-encode the filename-derived id.
func readScheduleFireFile(path string) (scheduleFireRecord, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is sanitized via firePath
	if err != nil {
		return scheduleFireRecord{}, err
	}
	var rec scheduleFireRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return scheduleFireRecord{}, fmt.Errorf("jsonlstore: decode fire: %w", err)
	}
	if rec.V != scheduleFormat {
		return scheduleFireRecord{}, fmt.Errorf("jsonlstore: unknown schedule-fire format %q (want %q)", rec.V, scheduleFormat)
	}
	return rec, nil
}

// writeFileAtomic writes b to path via a temp file + rename (atomic on POSIX
// rename(2)): a crash mid-write leaves the prior file intact, never a
// truncated half-record. Mode 0o600: owner-only, matching the 0o700 dir.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*") //nolint:gosec // temp in the same dir for same-FS rename
	if err != nil {
		return fmt.Errorf("jsonlstore: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // best-effort cleanup if rename failed
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("jsonlstore: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("jsonlstore: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("jsonlstore: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("jsonlstore: rename temp: %w", err)
	}
	return nil
}

// safeFilePart sanitizes a string-typed key (a schedule name or fire id) for
// use as a filename. Schedules deliberately retain the legacy lossy naming
// scheme; the reversible session-family codec must not migrate schedule files.
func safeFilePart(s string) string {
	return legacySafeName(session.SessionID(s))
}

// cloneSpec returns a copy of spec whose Parts slice AND Owner pointer are
// independent of the stored one (so a caller cannot mutate the store's record
// through the returned Schedule). ScheduleSpec is otherwise a struct of values.
// Mirrors memschedulestore.cloneSpec.
func cloneSpec(spec port.ScheduleSpec) port.ScheduleSpec {
	out := spec
	if spec.Parts != nil {
		out.Parts = make([]session.Content, len(spec.Parts))
		copy(out.Parts, spec.Parts)
	}
	out.Owner = spec.Owner.Clone()
	return out
}

// ReArmOneShot is the at-least-once re-arm primitive for a one-shot schedule
// (ADR 0059 Phase 2). It atomically (under the shared mutex): re-enables the
// schedule (Enabled=true), sets NextFireAt to nextFire, and increments
// OneShotRetryCount, then writes the record atomically (temp file + rename). The
// not-found case wraps ErrScheduleNotFound. The retry-budget gate is the CALLER's
// responsibility. One-shot-only.
func (s *scheduleStore) ReArmOneShot(_ context.Context, name string, nextFire time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.loadLocked(name)
	if err != nil {
		return err
	}
	rec.Schedule.State.Enabled = true
	rec.Schedule.State.NextFireAt = nextFire
	rec.Schedule.State.OneShotRetryCount++
	return s.writeScheduleLocked(name, rec)
}
