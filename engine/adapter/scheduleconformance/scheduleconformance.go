// Package scheduleconformance provides a shared conformance test suite for the
// port.ScheduleStore interface (scheduled-tasks issue #189, Phase 1b). Adapters
// (the in-memory reference memschedulestore, a future JSONL store, the gRPC
// driver client over bufconn, the k8s-backed store) call Run with a factory
// that constructs a fresh store, and the suite exercises only the
// port.ScheduleStore interface through the port's value types.
//
// Importing "testing" in a non-_test.go file is intentional here: this is a
// test-helper package whose sole purpose is to be imported by adapter tests,
// the conventional Go pattern for shared conformance suites (cf. the sibling
// leaseconformance/storeconformance/eventlogconformance packages).
//
// The suite pins the CONTRACT, not the implementation: how the store persists
// its records (a map, a JSONL sidecar, a CRD, a remote table) is
// adapter-internal and deliberately NOT asserted here. It is the SAME suite the
// reference memschedulestore and a future gRPC driver client (over bufconn)
// both pass — the dual-path contract-unification: the Go port is the contract,
// the wire is one adapter. This is the SECOND adapter-validation pattern after
// leases (leaseconformance), and mirrors its discipline byte-for-byte.
//
// TIME: the suite never sleeps. Every port.ScheduleStore method takes `now` as
// an explicit argument, so the suite expresses "the schedule is now due" / "the
// slot was missed" by passing explicit time.Time values to Due/Claim — no clock,
// no advance callback.
//
// CRON: the store is parser-free (Claim's nextFire is caller-computed), but the
// suite is a test-helper package and IS allowed to import engine/adapter/cronparse
// to compute the next-fire values it hands to Claim. The memschedulestore
// reference adapter does NOT import cronparse — the suite's use of it here does
// not bind the adapter.
//
// MISFIRE: the store does NOT implement misfire policy — MisfireSkip /
// MisfireFireOnceNow are read by COMPOSITION at tick time from ScheduleSpec.Misfire,
// not by the store. The store's Due returns any schedule whose NextFireAt <= now
// regardless of Misfire; the policy decides whether composition calls Claim. The
// suite therefore does NOT test MisfireSkip at the store level (it is not a
// store concern). It DOES test the MisfireFireOnceNow STORE-level consequence:
// a past-due slot, when Claimed, advances NextFireAt to the NEXT future fire
// (computed from `now`, not the stale past NextFireAt) — the catch-up semantics.
package scheduleconformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/cronparse"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// sampleCron is a cron expression the suite uses for recurring-schedule cases:
// every minute, on the minute. It is parse-safe and gives short, predictable
// next-fire intervals the suite asserts against.
const sampleCron = "* * * * *"

// Run executes the shared ScheduleStore conformance table against the store
// produced by newStore. newStore returns a fresh, isolated store. Every
// port.ScheduleStore method takes `now` as an explicit argument, so the suite
// controls time DIRECTLY by passing time.Time values to Due/Claim — there is no
// clock to advance, and the factory needs no advance callback.
func Run(t *testing.T, newStore func(t *testing.T) port.ScheduleStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("save/load round-trips spec and state", func(t *testing.T) {
		s := newStore(t)
		in := port.Schedule{
			Spec: port.ScheduleSpec{
				Name:      "conf-sched-rt",
				Prompt:    "rotate the keys",
				Profile:   "no-fs",
				Workspace: "/srv",
				MaxFires:  3,
				Mutating:  true,
				CreatedAt: time.Unix(1_700_000_000, 0),
			},
			State: port.ScheduleState{
				NextFireAt: time.Unix(1_700_000_060, 0),
				Enabled:    true,
			},
		}
		if err := s.Save(ctx, in); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := s.Load(ctx, in.Spec.Name)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		assertScheduleEqual(t, "round-trip", got, in)
	})

	t.Run("save preserves existing firing state on overwrite", func(t *testing.T) {
		// The port doc: "the State half is preserved on overwrite (a Save with
		// a fresh State does not reset firing progress — call Delete + Save to
		// reset)." A re-Save with a zero State MUST NOT clobber prior progress.
		s := newStore(t)
		const name = "conf-sched-preserve"
		first := port.Schedule{
			Spec: port.ScheduleSpec{Name: name, Prompt: "v1", Trigger: port.TriggerSpec{OneShot: time.Unix(1_700_000_060, 0)}},
			State: port.ScheduleState{
				NextFireAt:        time.Unix(1_700_000_060, 0),
				LastFireAt:        time.Unix(1_700_000_000, 0),
				FireCount:         2,
				Enabled:           true,
				LastFireSessionID: "sess-1",
			},
		}
		if err := s.Save(ctx, first); err != nil {
			t.Fatalf("Save #1: %v", err)
		}
		// Overwrite with a fresh (zero) State — only the Spec changes.
		overwrite := port.Schedule{
			Spec: port.ScheduleSpec{
				Name:     name,
				Prompt:   "v2",
				Trigger:  port.TriggerSpec{OneShot: time.Unix(1_700_000_120, 0)},
				Mutating: true,
			},
			State: port.ScheduleState{}, // deliberately zero — must NOT reset.
		}
		if err := s.Save(ctx, overwrite); err != nil {
			t.Fatalf("Save #2 (overwrite): %v", err)
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Spec.Prompt != "v2" {
			t.Errorf("Spec.Prompt = %q, want %q (Spec overwritten)", got.Spec.Prompt, "v2")
		}
		if got.State.FireCount != 2 {
			t.Errorf("State.FireCount = %d, want 2 (State preserved on overwrite)", got.State.FireCount)
		}
		if got.State.LastFireSessionID != "sess-1" {
			t.Errorf("State.LastFireSessionID = %q, want %q (preserved)", got.State.LastFireSessionID, "sess-1")
		}
		if !got.State.NextFireAt.Equal(time.Unix(1_700_000_060, 0)) {
			t.Errorf("State.NextFireAt = %v, want preserved %v", got.State.NextFireAt, time.Unix(1_700_000_060, 0))
		}
		if !got.State.Enabled {
			t.Errorf("State.Enabled = false, want true (preserved)")
		}
	})

	t.Run("new schedule defaults to enabled; explicit disabled honored", func(t *testing.T) {
		// The port contract: a NEW schedule saved with a zero State is active by
		// default (State.Enabled defaulted true), but an explicit disabled State
		// (Enabled=false with any non-zero State field) is honored verbatim. All
		// backends must agree on this create-time default — it is the behavior most
		// likely to diverge, so pin it here rather than only in each adapter's own
		// tests.
		s := newStore(t)

		// (a) zero State on a new schedule → enabled by default.
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: "conf-new-default", Prompt: "p", Trigger: port.TriggerSpec{OneShot: time.Unix(1_700_000_060, 0)}},
			State: port.ScheduleState{}, // zero — the create-seam default applies.
		}); err != nil {
			t.Fatalf("Save (default): %v", err)
		}
		got, err := s.Load(ctx, "conf-new-default")
		if err != nil {
			t.Fatalf("Load (default): %v", err)
		}
		if !got.State.Enabled {
			t.Errorf("new schedule with zero State: Enabled = false, want true (create default)")
		}

		// (b) explicit disabled State on a new schedule → honored (not flipped to
		// enabled). A non-zero NextFireAt disambiguates "caller set the State" from
		// "zero State".
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: "conf-new-disabled", Prompt: "p", Trigger: port.TriggerSpec{OneShot: time.Unix(1_700_000_060, 0)}},
			State: port.ScheduleState{NextFireAt: time.Unix(1_700_000_060, 0), Enabled: false},
		}); err != nil {
			t.Fatalf("Save (disabled): %v", err)
		}
		got, err = s.Load(ctx, "conf-new-disabled")
		if err != nil {
			t.Fatalf("Load (disabled): %v", err)
		}
		if got.State.Enabled {
			t.Errorf("new schedule with explicit disabled State: Enabled = true, want false (honored)")
		}
	})

	t.Run("load not-found wraps ErrScheduleNotFound", func(t *testing.T) {
		s := newStore(t)
		_, err := s.Load(ctx, "conf-sched-missing")
		if !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("Load(unknown) = %v, want ErrScheduleNotFound", err)
		}
	})

	t.Run("delete is idempotent", func(t *testing.T) {
		s := newStore(t)
		const name = "conf-sched-del"
		if err := s.Save(ctx, port.Schedule{Spec: port.ScheduleSpec{Name: name, Prompt: "x", Trigger: port.TriggerSpec{OneShot: time.Unix(1, 0)}}}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := s.Delete(ctx, name); err != nil {
			t.Fatalf("Delete #1: %v", err)
		}
		// Deleting an already-removed (or never-existed) name is success.
		if err := s.Delete(ctx, name); err != nil {
			t.Fatalf("Delete #2 (idempotent): %v", err)
		}
		if err := s.Delete(ctx, "conf-sched-never"); err != nil {
			t.Fatalf("Delete(unknown): %v", err)
		}
	})

	t.Run("list returns all saved schedules", func(t *testing.T) {
		s := newStore(t)
		names := []string{"conf-sched-list-a", "conf-sched-list-b", "conf-sched-list-c"}
		for i, n := range names {
			if err := s.Save(ctx, port.Schedule{
				Spec: port.ScheduleSpec{Name: n, Prompt: "p", Trigger: port.TriggerSpec{OneShot: time.Unix(int64(i+1), 0)}},
			}); err != nil {
				t.Fatalf("Save %q: %v", n, err)
			}
		}
		got, err := s.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != len(names) {
			t.Fatalf("List returned %d schedules, want %d", len(got), len(names))
		}
		gotNames := make(map[string]struct{}, len(got))
		for _, sc := range got {
			gotNames[sc.Spec.Name] = struct{}{}
		}
		for _, n := range names {
			if _, ok := gotNames[n]; !ok {
				t.Errorf("List missing %q", n)
			}
		}
	})

	t.Run("due filters on NextFireAt and Enabled and MaxFires", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		// due: NextFireAt in the past, enabled, no MaxFires cap.
		due := port.Schedule{Spec: port.ScheduleSpec{Name: "conf-sched-due", Prompt: "p", Trigger: port.TriggerSpec{OneShot: now.Add(-time.Second)}}}
		due.State.NextFireAt = now.Add(-time.Second)
		due.State.Enabled = true
		if err := s.Save(ctx, due); err != nil {
			t.Fatalf("Save due: %v", err)
		}
		// not-due-future: NextFireAt in the future.
		future := port.Schedule{Spec: port.ScheduleSpec{Name: "conf-sched-future", Prompt: "p", Trigger: port.TriggerSpec{OneShot: now.Add(time.Hour)}}}
		future.State.NextFireAt = now.Add(time.Hour)
		future.State.Enabled = true
		if err := s.Save(ctx, future); err != nil {
			t.Fatalf("Save future: %v", err)
		}
		// disabled: NextFireAt past but Enabled=false (must be excluded).
		disabled := port.Schedule{Spec: port.ScheduleSpec{Name: "conf-sched-disabled", Prompt: "p", Trigger: port.TriggerSpec{OneShot: now.Add(-time.Second)}}}
		disabled.State.NextFireAt = now.Add(-time.Second)
		disabled.State.Enabled = false
		if err := s.Save(ctx, disabled); err != nil {
			t.Fatalf("Save disabled: %v", err)
		}
		// exhausted: NextFireAt past, Enabled=true, but FireCount >= MaxFires.
		exhausted := port.Schedule{Spec: port.ScheduleSpec{Name: "conf-sched-exhausted", Prompt: "p", Trigger: port.TriggerSpec{OneShot: now.Add(-time.Second)}, MaxFires: 2}}
		exhausted.State.NextFireAt = now.Add(-time.Second)
		exhausted.State.Enabled = true
		exhausted.State.FireCount = 2
		if err := s.Save(ctx, exhausted); err != nil {
			t.Fatalf("Save exhausted: %v", err)
		}

		got, err := s.Due(ctx, now)
		if err != nil {
			t.Fatalf("Due: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Due returned %d schedules, want 1 (only the past+enabled+under-cap one)", len(got))
		}
		if got[0].Spec.Name != "conf-sched-due" {
			t.Errorf("Due returned %q, want %q", got[0].Spec.Name, "conf-sched-due")
		}
	})

	t.Run("claim is the at-most-once atomic advance", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-claim"
		// A recurring cron: next fire after `now` is one minute on.
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		sc := port.Schedule{
			Spec: port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{
				NextFireAt: now, // due now
				Enabled:    true,
			},
		}
		if err := s.Save(ctx, sc); err != nil {
			t.Fatalf("Save: %v", err)
		}

		claimed, err := s.Claim(ctx, name, now, next)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if !claimed.State.LastFireAt.Equal(now) {
			t.Errorf("LastFireAt = %v, want %v", claimed.State.LastFireAt, now)
		}
		if !claimed.State.NextFireAt.Equal(next) {
			t.Errorf("NextFireAt = %v, want %v (advanced to next cron fire)", claimed.State.NextFireAt, next)
		}
		if claimed.State.FireCount != 1 {
			t.Errorf("FireCount = %d, want 1 (incremented)", claimed.State.FireCount)
		}
		if claimed.State.LastFireSessionID == "" {
			t.Errorf("LastFireSessionID = empty, want a non-empty sentinel-pending value Claim set")
		}
		if !claimed.State.Enabled {
			t.Errorf("Enabled = false, want true (recurring cron stays enabled)")
		}

		// The persisted state reflects the claim (Claim is durable, not a
		// transient return value).
		persisted, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after Claim: %v", err)
		}
		if !persisted.State.NextFireAt.Equal(next) {
			t.Errorf("persisted NextFireAt = %v, want %v", persisted.State.NextFireAt, next)
		}
		if persisted.State.FireCount != 1 {
			t.Errorf("persisted FireCount = %d, want 1", persisted.State.FireCount)
		}
	})

	t.Run("claim excludes the slot from due", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-claim-due"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		sc := port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}
		if err := s.Save(ctx, sc); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		got, err := s.Due(ctx, now)
		if err != nil {
			t.Fatalf("Due after Claim: %v", err)
		}
		for _, d := range got {
			if d.Spec.Name == name {
				t.Errorf("Due still returns %q after Claim (NextFireAt must have advanced past now)", name)
			}
		}
	})

	t.Run("claim is at-most-once: second claim at same now errors not-found", func(t *testing.T) {
		// THE AT-MOST-ONCE PROOF. Two callers Claim the same name
		// "simultaneously" (the suite serializes them but both pass the SAME
		// now/nextFire). The SECOND Claim must observe the FIRST's advance and
		// NOT re-claim: the slot's NextFireAt is now past `now`, so the slot is
		// no longer due, so a second Claim returns ErrScheduleNotFound (the
		// fail-safe interpretation — the slot is gone). This is the at-most-once
		// guarantee at the store level: there is no owner/claim-holder field,
		// the durable NextFireAt advance IS the fence.
		//
		// The port doc is silent on a second Claim's return value (it says a
		// peer's Due won't re-return the slot, not what a second Claim does).
		// The suite picks the fail-safe interpretation: second claim errors
		// (wrapping ErrScheduleNotFound). An adapter that returned the
		// already-advanced state without erroring would be a re-claim bug (it
		// would let a peer believe it won the slot). Documented here so all
		// adapters agree.
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-atmostonce"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim #1: %v", err)
		}
		_, err = s.Claim(ctx, name, now, next) // same now/next — the slot is gone
		if !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("Claim #2 at same now = %v, want ErrScheduleNotFound (the slot is no longer due — at-most-once)", err)
		}
	})

	t.Run("claim not-found wraps ErrScheduleNotFound", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		_, err := s.Claim(ctx, "conf-sched-claim-missing", now, now.Add(time.Minute))
		if !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("Claim(unknown) = %v, want ErrScheduleNotFound", err)
		}
	})

	t.Run("record fire updates last session and is idempotent", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-record"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		claimed, err := s.Claim(ctx, name, now, next)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if claimed.State.LastFireSessionID == "" {
			t.Fatalf("Claim set LastFireSessionID to empty, want a non-empty sentinel-pending value")
		}
		fire := port.ScheduleFire{
			ID:           "fire-1",
			ScheduleName: name,
			SessionID:    "real-session-1",
			FiredAt:      now,
			Stop:         session.StopEndTurn,
		}
		if err := s.RecordFire(ctx, fire); err != nil {
			t.Fatalf("RecordFire: %v", err)
		}
		// LastFireSessionID must be the REAL session id now, not the sentinel.
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.State.LastFireSessionID != "real-session-1" {
			t.Errorf("LastFireSessionID = %q, want %q (RecordFire overwrote the sentinel)", got.State.LastFireSessionID, "real-session-1")
		}
		// Idempotent per fire id: a second RecordFire with the same f.ID is a no-op.
		fire2 := fire
		fire2.Stop = session.StopError // would-be mutation if not idempotent
		fire2.Err = "transient"
		if err := s.RecordFire(ctx, fire2); err != nil {
			t.Fatalf("RecordFire #2 (idempotent): %v", err)
		}
		got2, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after idempotent record: %v", err)
		}
		if got2.State.LastFireSessionID != "real-session-1" {
			t.Errorf("LastFireSessionID after re-record = %q, want unchanged %q", got2.State.LastFireSessionID, "real-session-1")
		}
		// The fire record itself is retrievable.
		loaded, err := s.LoadFire(ctx, fire.ID)
		if err != nil {
			t.Fatalf("LoadFire: %v", err)
		}
		if loaded.ID != fire.ID || loaded.SessionID != fire.SessionID || loaded.Stop != session.StopEndTurn {
			t.Errorf("LoadFire = %+v, want the original fire (StopCompleted, not the idempotent re-record's StopError)", loaded)
		}
	})

	// Issue #386 — the in-flight scheduled-fire state model (RecordFireStart /
	// RecordFireProgress) and its interaction with Claim/RecordFire. The store
	// tracks an in-flight fire between RecordFireStart (the run began) and
	// RecordFire (the run produced a terminal outcome); a fresh Claim zeros the
	// in-flight fields, RecordFireStart sets them, RecordFire clears them.
	t.Run("record fire start persists in-flight state", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-firestart"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		// A Claim alone leaves the in-flight fields zero (the crash-after-Claim
		// state — LastFireSessionID is the pending sentinel, no run started).
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after Claim: %v", err)
		}
		if !got.State.LastFireStartedAt.IsZero() {
			t.Errorf("LastFireStartedAt after Claim = %v, want zero (Claim alone has no run start)", got.State.LastFireStartedAt)
		}
		if got.State.LastFireSessionID != port.PendingFireSessionID {
			t.Errorf("LastFireSessionID after Claim = %q, want %q (pending sentinel)", got.State.LastFireSessionID, port.PendingFireSessionID)
		}

		// RecordFireStart: the run actually began. LastFireSessionID becomes the
		// REAL session id, LastFireStartedAt is set, and an in-flight fire record
		// is visible via ListFires (Stop empty).
		start := now.Add(time.Second)
		deadline := start.Add(5 * time.Minute)
		fire := port.ScheduleFire{
			ID:           "fire-inflight",
			ScheduleName: name,
			SessionID:    "real-session-1",
			FiredAt:      now,
			StartedAt:    start,
			Deadline:     deadline,
		}
		if err := s.RecordFireStart(ctx, name, fire); err != nil {
			t.Fatalf("RecordFireStart: %v", err)
		}
		got, err = s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after RecordFireStart: %v", err)
		}
		if got.State.LastFireSessionID != "real-session-1" {
			t.Errorf("LastFireSessionID = %q, want %q (real session id)", got.State.LastFireSessionID, "real-session-1")
		}
		if !got.State.LastFireStartedAt.Equal(start) {
			t.Errorf("LastFireStartedAt = %v, want %v", got.State.LastFireStartedAt, start)
		}
		// Progress seeded to the start instant (caller passed a zero ProgressAt).
		if !got.State.LastFireProgressAt.Equal(start) {
			t.Errorf("LastFireProgressAt = %v, want seeded %v", got.State.LastFireProgressAt, start)
		}
		if !got.State.FireDeadline.Equal(deadline) {
			t.Errorf("FireDeadline = %v, want %v", got.State.FireDeadline, deadline)
		}
		// The in-flight fire record is visible via ListFires with Stop empty.
		fires, err := s.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires after RecordFireStart: %v", err)
		}
		if len(fires) != 1 || fires[0].ID != "fire-inflight" || fires[0].Stop != "" {
			t.Errorf("ListFires = %+v, want one in-flight fire (Stop empty)", fires)
		}
		if !fires[0].StartedAt.Equal(start) {
			t.Errorf("in-flight fire StartedAt = %v, want %v", fires[0].StartedAt, start)
		}
	})

	t.Run("record fire start is idempotent per fire id", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-firestart-idem"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		start := now.Add(time.Second)
		fire := port.ScheduleFire{
			ID:           "fire-idem",
			ScheduleName: name,
			SessionID:    "real-session-idem",
			FiredAt:      now,
			StartedAt:    start,
		}
		if err := s.RecordFireStart(ctx, name, fire); err != nil {
			t.Fatalf("RecordFireStart #1: %v", err)
		}
		// A second RecordFireStart with the SAME StartedAt is a no-op: the state
		// must not change and there must still be exactly one fire record.
		if err := s.RecordFireStart(ctx, name, fire); err != nil {
			t.Fatalf("RecordFireStart #2 (idempotent): %v", err)
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !got.State.LastFireStartedAt.Equal(start) {
			t.Errorf("LastFireStartedAt = %v, want unchanged %v", got.State.LastFireStartedAt, start)
		}
		fires, err := s.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires: %v", err)
		}
		if len(fires) != 1 {
			t.Errorf("ListFires = %d records, want 1 (idempotent)", len(fires))
		}
	})

	t.Run("record fire progress advances last progress", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-fireprogress"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		start := now.Add(time.Second)
		if err := s.RecordFireStart(ctx, name, port.ScheduleFire{
			ID:           "fire-progress",
			ScheduleName: name,
			SessionID:    "real-session-progress",
			FiredAt:      now,
			StartedAt:    start,
		}); err != nil {
			t.Fatalf("RecordFireStart: %v", err)
		}
		// Progress advances from the seeded start instant.
		p1 := start.Add(10 * time.Second)
		if err := s.RecordFireProgress(ctx, name, "fire-progress", p1); err != nil {
			t.Fatalf("RecordFireProgress #1: %v", err)
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !got.State.LastFireProgressAt.Equal(p1) {
			t.Errorf("LastFireProgressAt = %v, want %v", got.State.LastFireProgressAt, p1)
		}
		// An EARLIER progress instant does NOT rewind (reordered/delayed update).
		if err := s.RecordFireProgress(ctx, name, "fire-progress", start.Add(5*time.Second)); err != nil {
			t.Fatalf("RecordFireProgress (stale): %v", err)
		}
		got, err = s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after stale progress: %v", err)
		}
		if !got.State.LastFireProgressAt.Equal(p1) {
			t.Errorf("LastFireProgressAt after stale update = %v, want unchanged %v", got.State.LastFireProgressAt, p1)
		}
		// A LATER instant advances further.
		p2 := p1.Add(time.Minute)
		if err := s.RecordFireProgress(ctx, name, "fire-progress", p2); err != nil {
			t.Fatalf("RecordFireProgress #2: %v", err)
		}
		got, err = s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after progress #2: %v", err)
		}
		if !got.State.LastFireProgressAt.Equal(p2) {
			t.Errorf("LastFireProgressAt = %v, want %v", got.State.LastFireProgressAt, p2)
		}
		// The in-flight fire record's ProgressAt advances too.
		fires, err := s.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires: %v", err)
		}
		if len(fires) != 1 || !fires[0].ProgressAt.Equal(p2) {
			t.Errorf("in-flight fire ProgressAt = %v, want %v", fires[0].ProgressAt, p2)
		}
	})

	// Review finding M1: RecordFireProgress targets the SINGLE in-flight fire by
	// its known id (fireID), NOT by scanning every fire record for the schedule's
	// in-flight one. A progress write for fireID A must NOT touch a different
	// in-flight fire record B under the same schedule (the old scan found "the
	// first in-flight fire for the schedule", so a stale/wrong fireID would
	// advance whichever record the scan hit first — now the store addresses the
	// record by key, so a non-matching fireID is a best-effort no-op for the
	// record, and a matching fireID advances ONLY it).
	t.Run("record fire progress targets the named fire only", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-fireprogress-targeted"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		start := now.Add(time.Second)
		// Two in-flight fire records under the same schedule (a record the caller
		// RecordFireStart'd, and a second it did NOT — a phantom the scan would
		// have hit). RecordFireProgress must address ONLY the named fire id.
		if err := s.RecordFireStart(ctx, name, port.ScheduleFire{
			ID: "fire-target-A", ScheduleName: name, SessionID: "sess-A",
			FiredAt: now, StartedAt: start,
		}); err != nil {
			t.Fatalf("RecordFireStart A: %v", err)
		}
		if err := s.RecordFireStart(ctx, name, port.ScheduleFire{
			ID: "fire-target-B", ScheduleName: name, SessionID: "sess-B",
			FiredAt: now, StartedAt: start.Add(time.Millisecond),
		}); err != nil {
			t.Fatalf("RecordFireStart B: %v", err)
		}
		pA := start.Add(10 * time.Second)
		if err := s.RecordFireProgress(ctx, name, "fire-target-A", pA); err != nil {
			t.Fatalf("RecordFireProgress A: %v", err)
		}
		byID := map[string]port.ScheduleFire{}
		fires, err := s.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires: %v", err)
		}
		for _, f := range fires {
			byID[f.ID] = f
		}
		a, ok := byID["fire-target-A"]
		if !ok {
			t.Fatalf("missing fire-target-A in ListFires")
		}
		if !a.ProgressAt.Equal(pA) {
			t.Errorf("fire-target-A ProgressAt = %v, want %v (the targeted fire advances)", a.ProgressAt, pA)
		}
		b, ok := byID["fire-target-B"]
		if !ok {
			t.Fatalf("missing fire-target-B in ListFires")
		}
		if !b.ProgressAt.IsZero() {
			t.Errorf("fire-target-B ProgressAt = %v, want zero (a non-targeted fire must NOT advance — RecordFireProgress addresses only fireID)", b.ProgressAt)
		}
		// A progress write with a fireID that has NO record is a best-effort
		// success (the state alone carries it); it must NOT error.
		if err := s.RecordFireProgress(ctx, name, "fire-nonexistent", start.Add(20*time.Second)); err != nil {
			t.Errorf("RecordFireProgress(unknown fireID) = %v, want nil (best-effort: a missing record is a no-op success)", err)
		}
	})

	// Review finding M2: a progress write to an ALREADY-TERMINAL fire is a NO-OP
	// for the record — it MUST NOT revert the record from terminal back to
	// in-flight. The pre-fix non-atomic GET-then-SET could race a concurrent
	// terminal RecordFire (a cross-replica race in redisstore), reverting a
	// terminal record to a phantom in-flight one. The store now addresses the
	// record by key and guards the write on the record being in-flight (Stop
	// empty), so a terminal record is untouched.
	t.Run("record fire progress does not revert a terminal fire", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-fireprogress-terminal"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		start := now.Add(time.Second)
		if err := s.RecordFireStart(ctx, name, port.ScheduleFire{
			ID: "fire-terminal-prog", ScheduleName: name, SessionID: "sess-terminal-prog",
			FiredAt: now, StartedAt: start,
		}); err != nil {
			t.Fatalf("RecordFireStart: %v", err)
		}
		// Flip the fire terminal.
		terminal := port.ScheduleFire{
			ID: "fire-terminal-prog", ScheduleName: name, SessionID: "sess-terminal-prog",
			FiredAt: now, StartedAt: start, Stop: session.StopEndTurn,
		}
		if err := s.RecordFire(ctx, terminal); err != nil {
			t.Fatalf("RecordFire (terminal): %v", err)
		}
		// A progress write to the now-terminal fire is a no-op for the record: it
		// must NOT revert Stop to empty (in-flight) or advance ProgressAt.
		later := start.Add(time.Hour)
		if err := s.RecordFireProgress(ctx, name, "fire-terminal-prog", later); err != nil {
			t.Fatalf("RecordFireProgress on terminal fire: %v", err)
		}
		loaded, err := s.LoadFire(ctx, "fire-terminal-prog")
		if err != nil {
			t.Fatalf("LoadFire: %v", err)
		}
		if loaded.Stop != session.StopEndTurn {
			t.Errorf("terminal fire reverted to in-flight: Stop = %q, want %q (a progress write must NOT revert a terminal record)", loaded.Stop, session.StopEndTurn)
		}
		if !loaded.ProgressAt.IsZero() {
			t.Errorf("terminal fire ProgressAt = %v, want zero (a progress write must not advance a terminal record)", loaded.ProgressAt)
		}
		// The state's LastFireProgressAt was CLEARED by RecordFire (a terminal fire
		// has no in-flight run); a progress write to a terminal fire must NOT
		// re-stamp it (it would resurrect in-flight state). The state advance is
		// monotonic on the STATE field, but the terminal RecordFire cleared it to
		// zero — a later progress must not re-set it for a fire that is already
		// terminal. The store's contract: progress on a terminal fire record is a
		// no-op; the STATE field is advanced best-effort (the caller serialises
		// same-name calls, so a progress after RecordFire does not happen in
		// practice — this asserts the record half, the load-bearing M2 invariant).
	})

	t.Run("record fire clears in-flight fields and flips terminal", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-fireclear"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		start := now.Add(time.Second)
		deadline := start.Add(5 * time.Minute)
		if err := s.RecordFireStart(ctx, name, port.ScheduleFire{
			ID:           "fire-clear",
			ScheduleName: name,
			SessionID:    "real-session-clear",
			FiredAt:      now,
			StartedAt:    start,
			Deadline:     deadline,
		}); err != nil {
			t.Fatalf("RecordFireStart: %v", err)
		}
		// RecordFire flips the fire terminal and clears the in-flight state fields.
		terminal := port.ScheduleFire{
			ID:           "fire-clear",
			ScheduleName: name,
			SessionID:    "real-session-clear",
			FiredAt:      now,
			Stop:         session.StopEndTurn,
		}
		if err := s.RecordFire(ctx, terminal); err != nil {
			t.Fatalf("RecordFire: %v", err)
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after RecordFire: %v", err)
		}
		if !got.State.LastFireStartedAt.IsZero() {
			t.Errorf("LastFireStartedAt = %v, want zero (cleared on terminal record)", got.State.LastFireStartedAt)
		}
		if !got.State.LastFireProgressAt.IsZero() {
			t.Errorf("LastFireProgressAt = %v, want zero (cleared on terminal record)", got.State.LastFireProgressAt)
		}
		if !got.State.FireDeadline.IsZero() {
			t.Errorf("FireDeadline = %v, want zero (cleared on terminal record)", got.State.FireDeadline)
		}
		if got.State.LastFireSessionID != "real-session-clear" {
			t.Errorf("LastFireSessionID = %q, want the real session id", got.State.LastFireSessionID)
		}
		// The fire record is now terminal (Stop set).
		loaded, err := s.LoadFire(ctx, "fire-clear")
		if err != nil {
			t.Fatalf("LoadFire: %v", err)
		}
		if loaded.Stop != session.StopEndTurn {
			t.Errorf("LoadFire Stop = %q, want %q (terminal)", loaded.Stop, session.StopEndTurn)
		}
	})

	t.Run("claim alone leaves last fire started at zero (crash after claim)", func(t *testing.T) {
		// The crash-after-Claim state: Claim advanced the slot and stamped the
		// pending sentinel, but RecordFireStart never ran (the process died
		// between Claim and the run start). The in-flight fields MUST be zero so
		// a watchdog/recovery layer can distinguish "no run started" from "run
		// started but no progress".
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-crash-after-claim"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !got.State.LastFireStartedAt.IsZero() {
			t.Errorf("LastFireStartedAt = %v, want zero (no run started)", got.State.LastFireStartedAt)
		}
		if !got.State.LastFireProgressAt.IsZero() {
			t.Errorf("LastFireProgressAt = %v, want zero (no run started)", got.State.LastFireProgressAt)
		}
		if !got.State.FireDeadline.IsZero() {
			t.Errorf("FireDeadline = %v, want zero (no run started)", got.State.FireDeadline)
		}
		if got.State.LastFireSessionID != port.PendingFireSessionID {
			t.Errorf("LastFireSessionID = %q, want %q (pending sentinel — RecordFireStart never ran)", got.State.LastFireSessionID, port.PendingFireSessionID)
		}
	})

	t.Run("record fire not-found wraps ErrScheduleNotFound", func(t *testing.T) {
		s := newStore(t)
		fire := port.ScheduleFire{
			ID:           "fire-orphan",
			ScheduleName: "conf-sched-deleted-before-record",
			SessionID:    "sess",
		}
		if err := s.RecordFire(ctx, fire); err != nil {
			// A fire against a schedule that was never saved: the schedule is
			// not-found. The suite asserts the wrap regardless of whether the
			// adapter checks the schedule up front or lazily.
			if !errors.Is(err, port.ErrScheduleNotFound) {
				t.Fatalf("RecordFire(unknown schedule) = %v, want ErrScheduleNotFound", err)
			}
		}
		// LoadFire on an unknown fire id wraps ErrScheduleNotFound.
		if _, err := s.LoadFire(ctx, "conf-sched-fire-missing"); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("LoadFire(unknown) = %v, want ErrScheduleNotFound", err)
		}
	})

	t.Run("list fires: empty for existing schedule, populated after record, not-found for unknown", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-listfires"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}

		// (a) An existing schedule with NO fires returns a successful EMPTY slice
		// (not an error, not nil).
		got, err := s.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires on existing schedule with no fires: %v (want nil err)", err)
		}
		if got == nil {
			t.Fatal("ListFires on existing schedule with no fires = nil, want a non-nil empty slice")
		}
		if len(got) != 0 {
			t.Fatalf("ListFires on existing schedule with no fires = %d records, want 0", len(got))
		}

		// (b) After RecordFire, ListFires returns the recorded fire(s).
		if _, err := s.Claim(ctx, name, now, next); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		fire1 := port.ScheduleFire{
			ID:           "listfires-1",
			ScheduleName: name,
			SessionID:    "sess-listfires-1",
			FiredAt:      now,
			Stop:         session.StopEndTurn,
		}
		fire2 := port.ScheduleFire{
			ID:           "listfires-2",
			ScheduleName: name,
			SessionID:    "sess-listfires-2",
			FiredAt:      now,
			Stop:         session.StopError,
			Err:          "boom",
		}
		if err := s.RecordFire(ctx, fire1); err != nil {
			t.Fatalf("RecordFire #1: %v", err)
		}
		if err := s.RecordFire(ctx, fire2); err != nil {
			t.Fatalf("RecordFire #2: %v", err)
		}
		got, err = s.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires after RecordFire: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ListFires returned %d records, want 2", len(got))
		}
		// Both recorded fires must be present (order is not guaranteed, so collect
		// by id and assert membership).
		byID := make(map[string]port.ScheduleFire, len(got))
		for _, f := range got {
			byID[f.ID] = f
		}
		if f, ok := byID["listfires-1"]; !ok {
			t.Errorf("ListFires missing %q", "listfires-1")
		} else if f.SessionID != "sess-listfires-1" || f.Stop != session.StopEndTurn {
			t.Errorf("ListFires %q = %+v, want the recorded fire", "listfires-1", f)
		}
		if f, ok := byID["listfires-2"]; !ok {
			t.Errorf("ListFires missing %q", "listfires-2")
		} else if f.Stop != session.StopError || f.Err != "boom" {
			t.Errorf("ListFires %q = %+v, want the recorded fire", "listfires-2", f)
		}

		// (c) A second schedule's fires do NOT bleed into the first's list (the
		// foreign-key filter holds).
		const other = "conf-sched-listfires-other"
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: other, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save other: %v", err)
		}
		if _, err := s.Claim(ctx, other, now, next); err != nil {
			t.Fatalf("Claim other: %v", err)
		}
		if err := s.RecordFire(ctx, port.ScheduleFire{
			ID: "listfires-other-1", ScheduleName: other, SessionID: "sess-other", FiredAt: now, Stop: session.StopEndTurn,
		}); err != nil {
			t.Fatalf("RecordFire other: %v", err)
		}
		got, err = s.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires after other schedule recorded: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("ListFires for %q returned %d records, want 2 (other schedule's fire bled in)", name, len(got))
		}

		// (d) ListFires on an UNKNOWN schedule wraps ErrScheduleNotFound.
		if _, err := s.ListFires(ctx, "conf-sched-listfires-missing"); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("ListFires(unknown schedule) = %v, want ErrScheduleNotFound", err)
		}
	})

	t.Run("set enabled flips Enabled without touching other state", func(t *testing.T) {
		// SetEnabled is the pause/resume primitive: it flips Enabled and ONLY
		// Enabled — unlike Save (which preserves the existing State half on a
		// Spec overwrite and so CANNOT mutate Enabled). The other State fields
		// (NextFireAt/LastFireAt/FireCount/LastFireSessionID) must be unchanged.
		s := newStore(t)
		const name = "conf-sched-setenabled"
		seed := port.Schedule{
			Spec: port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{
				NextFireAt:        time.Unix(1_700_000_060, 0),
				LastFireAt:        time.Unix(1_700_000_000, 0),
				FireCount:         3,
				Enabled:           true,
				LastFireSessionID: "sess-pre",
			},
		}
		if err := s.Save(ctx, seed); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// Pause: SetEnabled false.
		if err := s.SetEnabled(ctx, name, false); err != nil {
			t.Fatalf("SetEnabled(false): %v", err)
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after SetEnabled(false): %v", err)
		}
		if got.State.Enabled {
			t.Errorf("Enabled = true, want false (paused)")
		}
		if !got.State.NextFireAt.Equal(seed.State.NextFireAt) {
			t.Errorf("NextFireAt = %v, want unchanged %v", got.State.NextFireAt, seed.State.NextFireAt)
		}
		if !got.State.LastFireAt.Equal(seed.State.LastFireAt) {
			t.Errorf("LastFireAt = %v, want unchanged %v", got.State.LastFireAt, seed.State.LastFireAt)
		}
		if got.State.FireCount != seed.State.FireCount {
			t.Errorf("FireCount = %d, want unchanged %d", got.State.FireCount, seed.State.FireCount)
		}
		if got.State.LastFireSessionID != seed.State.LastFireSessionID {
			t.Errorf("LastFireSessionID = %q, want unchanged %q", got.State.LastFireSessionID, seed.State.LastFireSessionID)
		}
		// Resume: SetEnabled true.
		if err := s.SetEnabled(ctx, name, true); err != nil {
			t.Fatalf("SetEnabled(true): %v", err)
		}
		got, err = s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after SetEnabled(true): %v", err)
		}
		if !got.State.Enabled {
			t.Errorf("Enabled = false, want true (resumed)")
		}
		// SetEnabled on an unknown name wraps ErrScheduleNotFound.
		if err := s.SetEnabled(ctx, "conf-sched-setenabled-missing", false); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("SetEnabled(unknown) = %v, want ErrScheduleNotFound", err)
		}
	})

	t.Run("max fires exhaustion disables on the final claim", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-maxfires"
		next1, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire #1: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}, MaxFires: 2},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// Fire #1.
		c1, err := s.Claim(ctx, name, now, next1)
		if err != nil {
			t.Fatalf("Claim #1: %v", err)
		}
		if c1.State.FireCount != 1 || !c1.State.Enabled {
			t.Fatalf("Claim #1 state = %+v, want FireCount=1 Enabled=true", c1.State)
		}
		// Advance the clock to next1 (the new NextFireAt) so the slot is due again.
		now2 := next1
		// Fire #2 — the FINAL fire (MaxFires=2). A cron whose FireCount reaches
		// MaxFires is DONE: Claim sets Enabled=false and zeroes NextFireAt. The
		// caller passes nextFire=zero for the terminal claim (the store is
		// parser-free; composition computes "no further fire" and passes zero).
		c2, err := s.Claim(ctx, name, now2, time.Time{})
		if err != nil {
			t.Fatalf("Claim #2 (final): %v", err)
		}
		if c2.State.FireCount != 2 {
			t.Errorf("FireCount = %d, want 2", c2.State.FireCount)
		}
		if c2.State.Enabled {
			t.Errorf("Enabled = true, want false (MaxFires exhausted)")
		}
		if !c2.State.NextFireAt.IsZero() {
			t.Errorf("NextFireAt = %v, want zero (no further fire)", c2.State.NextFireAt)
		}
		// Due at any later time must NOT return the exhausted schedule.
		got, err := s.Due(ctx, now2.Add(time.Hour))
		if err != nil {
			t.Fatalf("Due after exhaustion: %v", err)
		}
		for _, d := range got {
			if d.Spec.Name == name {
				t.Errorf("Due returned exhausted schedule %q (Enabled=false, must be excluded)", name)
			}
		}
	})

	t.Run("one-shot fires once then disables", func(t *testing.T) {
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-oneshot"
		// A one-shot: the caller passes nextFire=zero (no further fire). Claim
		// sets Enabled=false and zeroes NextFireAt.
		if err := s.Save(ctx, port.Schedule{
			Spec: port.ScheduleSpec{
				Name:     name,
				Prompt:   "once",
				Trigger:  port.TriggerSpec{OneShot: now},
				Mutating: true,
			},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		claimed, err := s.Claim(ctx, name, now, time.Time{})
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if claimed.State.FireCount != 1 {
			t.Errorf("FireCount = %d, want 1", claimed.State.FireCount)
		}
		if claimed.State.Enabled {
			t.Errorf("Enabled = true, want false (one-shot fired)")
		}
		if !claimed.State.NextFireAt.IsZero() {
			t.Errorf("NextFireAt = %v, want zero (one-shot done)", claimed.State.NextFireAt)
		}
		// A later Due does not return it.
		got, err := s.Due(ctx, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("Due: %v", err)
		}
		for _, d := range got {
			if d.Spec.Name == name {
				t.Errorf("Due returned fired one-shot %q (must be excluded)", name)
			}
		}
	})

	t.Run("misfire fire-once-now advances to a future next fire", func(t *testing.T) {
		// The MisfireFireOnceNow STORE-level consequence: a past-due slot, when
		// Claimed, advances NextFireAt to the NEXT future fire computed from
		// `now` (NOT from the stale past NextFireAt). The caller hands the
		// cronparse-from-now nextFire to Claim; the suite asserts the new
		// NextFireAt is strictly after `now`.
		s := newStore(t)
		stale := time.Unix(1_700_000_000, 0)
		// `now` is an hour past the stale NextFireAt — the slot was missed. The
		// suite passes explicit time.Time values: we save with a past NextFireAt
		// directly and Claim at the post-gap `now`.
		now := stale.Add(time.Hour)
		const name = "conf-sched-misfire"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: stale, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// Due at the post-gap now still returns it (the store does not read
		// Misfire — MisfireSkip is a composition concern, not a store concern).
		due, err := s.Due(ctx, now)
		if err != nil {
			t.Fatalf("Due: %v", err)
		}
		var found bool
		for _, d := range due {
			if d.Spec.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Due did not return the past-due slot (the store does not implement MisfireSkip)")
		}
		claimed, err := s.Claim(ctx, name, now, next)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if !claimed.State.NextFireAt.After(now) {
			t.Errorf("NextFireAt after misfire-claim = %v, want strictly after now %v (advanced from `now`, not the stale past NextFireAt)", claimed.State.NextFireAt, now)
		}
		if !claimed.State.LastFireAt.Equal(now) {
			t.Errorf("LastFireAt = %v, want now %v", claimed.State.LastFireAt, now)
		}
	})

	t.Run("claim now bypasses due-check", func(t *testing.T) {
		// ClaimNow is the FireNow primitive: the SAME atomic advance as Claim but
		// WITHOUT the NextFireAt <= now due-check — a manual trigger fires
		// regardless of whether the slot is due, while still claiming atomically
		// for at-most-once. The Enabled + MaxFires checks still apply. The
		// at-most-once fence (no due-check) is LastFireAt == now: a second
		// ClaimNow at the same now is rejected (the advance already happened).
		s := newStore(t)
		now := time.Unix(1_700_000_000, 0)
		const name = "conf-sched-claimnow"
		next, err := cronparse.NextFire(sampleCron, now, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire: %v", err)
		}
		// A FUTURE NextFireAt — Claim would reject this (not due), ClaimNow must
		// accept it (the manual trigger bypasses the cadence).
		future := now.Add(time.Hour)
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: future, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}

		// (a) ClaimNow on a future-due schedule succeeds and advances State.
		claimed, err := s.ClaimNow(ctx, name, now, next)
		if err != nil {
			t.Fatalf("ClaimNow on future-due schedule: %v", err)
		}
		if !claimed.State.LastFireAt.Equal(now) {
			t.Errorf("LastFireAt = %v, want now %v", claimed.State.LastFireAt, now)
		}
		if !claimed.State.NextFireAt.Equal(next) {
			t.Errorf("NextFireAt = %v, want %v (advanced to next cron fire)", claimed.State.NextFireAt, next)
		}
		if claimed.State.FireCount != 1 {
			t.Errorf("FireCount = %d, want 1 (incremented)", claimed.State.FireCount)
		}
		if claimed.State.LastFireSessionID != port.PendingFireSessionID {
			t.Errorf("LastFireSessionID = %q, want %q (the pending sentinel)", claimed.State.LastFireSessionID, port.PendingFireSessionID)
		}
		if !claimed.State.Enabled {
			t.Errorf("Enabled = false, want true (recurring cron stays enabled)")
		}
		// The persisted state reflects the advance (ClaimNow is durable, not a
		// transient return value) — the same discipline as Claim.
		persisted, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after ClaimNow: %v", err)
		}
		if !persisted.State.NextFireAt.Equal(next) {
			t.Errorf("persisted NextFireAt = %v, want %v", persisted.State.NextFireAt, next)
		}
		if persisted.State.FireCount != 1 {
			t.Errorf("persisted FireCount = %d, want 1", persisted.State.FireCount)
		}

		// (b) At-most-once: a SECOND ClaimNow at the same now is rejected — the
		// advance already happened. ErrScheduleNotFound is the fail-safe "the
		// slot is gone" interpretation (the same shape Claim's second-call
		// contract pins).
		_, err = s.ClaimNow(ctx, name, now, next) // same now/next — already advanced
		if !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("ClaimNow #2 at same now = %v, want ErrScheduleNotFound (the advance already happened — at-most-once)", err)
		}

		// (c) ClaimNow at a LATER now succeeds (crash-recoverability — no wedge).
		// The fence is LastFireAt == now, so a new now passes (the stale
		// LastFireAt != the new now). This is the self-heal property: a hard
		// crash between ClaimNow and RecordFire does NOT wedge the schedule.
		later := now.Add(2 * time.Hour)
		next2, err := cronparse.NextFire(sampleCron, later, time.UTC)
		if err != nil {
			t.Fatalf("cronparse.NextFire #2: %v", err)
		}
		if _, err := s.ClaimNow(ctx, name, later, next2); err != nil {
			t.Fatalf("ClaimNow at later now = %v, want success (crash-recoverable: a new now passes the LastFireAt fence)", err)
		}

		// (d) ClaimNow on a DISABLED schedule → ErrScheduleNotFound (the Enabled
		// check still applies — a paused schedule cannot be force-fired).
		const disabled = "conf-sched-claimnow-disabled"
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: disabled, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: future, Enabled: false},
		}); err != nil {
			t.Fatalf("Save disabled: %v", err)
		}
		if _, err := s.ClaimNow(ctx, disabled, now, next); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("ClaimNow on disabled = %v, want ErrScheduleNotFound (Enabled check still applies)", err)
		}

		// (e) ClaimNow on an unknown name → ErrScheduleNotFound.
		if _, err := s.ClaimNow(ctx, "conf-sched-claimnow-missing", now, next); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("ClaimNow(unknown) = %v, want ErrScheduleNotFound", err)
		}

		// (f) ClaimNow on a MaxFires-exhausted schedule → ErrScheduleNotFound (the
		// MaxFires check still applies — an exhausted schedule cannot be
		// force-fired).
		const exhausted = "conf-sched-claimnow-exhausted"
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: exhausted, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}, MaxFires: 1},
			State: port.ScheduleState{NextFireAt: future, Enabled: true, FireCount: 1},
		}); err != nil {
			t.Fatalf("Save exhausted: %v", err)
		}
		if _, err := s.ClaimNow(ctx, exhausted, now, next); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("ClaimNow on exhausted = %v, want ErrScheduleNotFound (MaxFires check still applies)", err)
		}
	})

	// Cross-primitive at-most-once: Claim then ClaimNow at the same now (and
	// ClaimNow then Claim) must BOTH reject the second as ErrScheduleNotFound —
	// the two primitives share the same atomic fence so a slot claimed by one
	// is gone for the other (no TOCTOU between the tick loop's Claim and a
	// manual FireNow's ClaimNow).
	t.Run("cross-primitive claim/claimnow at-most-once", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		now := time.Unix(1_700_000_000, 0).UTC()
		future := now.Add(time.Hour)
		next := future.Add(time.Minute)

		// (a) Claim then ClaimNow at the same now → ErrScheduleNotFound.
		const a = "conf-sched-cross-claim-then-claimnow"
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: a, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save a: %v", err)
		}
		if _, err := s.Claim(ctx, a, now, next); err != nil {
			t.Fatalf("Claim a: %v", err)
		}
		if _, err := s.ClaimNow(ctx, a, now, next); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("ClaimNow after Claim at same now = %v, want ErrScheduleNotFound (cross-primitive at-most-once)", err)
		}

		// (b) ClaimNow then Claim at the same now → ErrScheduleNotFound.
		const b = "conf-sched-cross-claimnow-then-claim"
		if err := s.Save(ctx, port.Schedule{
			Spec:  port.ScheduleSpec{Name: b, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleCron}},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save b: %v", err)
		}
		if _, err := s.ClaimNow(ctx, b, now, next); err != nil {
			t.Fatalf("ClaimNow b: %v", err)
		}
		if _, err := s.Claim(ctx, b, now, next); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("Claim after ClaimNow at same now = %v, want ErrScheduleNotFound (cross-primitive at-most-once)", err)
		}
	})

	// ScheduleOneShotReArmer is an OPTIONAL interface (type-asserted on the
	// store, like PrunableStore). A store that does not implement it degrades to
	// at-most-once (byte-identical pre-Phase-2); a store that DOES implement it
	// must make ReArmOneShot atomic (two concurrent re-arms don't
	// double-increment OneShotRetryCount or double-enable). The suite runs this
	// only against stores that opt in.
	t.Run("rearm one-shot is atomic and increments the counter", func(t *testing.T) {
		s := newStore(t)
		reArmer, ok := s.(port.ScheduleOneShotReArmer)
		if !ok {
			t.Skip("store does not implement ScheduleOneShotReArmer (at-most-once — byte-identical pre-Phase-2)")
		}
		const name = "conf-sched-rearm"
		now := time.Unix(1_700_000_000, 0)
		if err := s.Save(ctx, port.Schedule{
			Spec: port.ScheduleSpec{
				Name:              name,
				Prompt:            "once",
				Trigger:           port.TriggerSpec{OneShot: now},
				Mutating:          true,
				OneShotRetry:      true,
				OneShotMaxRetries: 3,
			},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// Claim the one-shot (Claim disables it — the at-most-once advance).
		if _, err := s.Claim(ctx, name, now, time.Time{}); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		// ReArmOneShot re-enables + advances NextFireAt + increments the counter.
		nextFire := now.Add(time.Minute)
		if err := reArmer.ReArmOneShot(ctx, name, nextFire); err != nil {
			t.Fatalf("ReArmOneShot: %v", err)
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after ReArmOneShot: %v", err)
		}
		if !got.State.Enabled {
			t.Errorf("Enabled = false, want true (ReArmOneShot re-enabled)")
		}
		if !got.State.NextFireAt.Equal(nextFire) {
			t.Errorf("NextFireAt = %v, want %v (ReArmOneShot advanced)", got.State.NextFireAt, nextFire)
		}
		if got.State.OneShotRetryCount != 1 {
			t.Errorf("OneShotRetryCount = %d, want 1 (incremented)", got.State.OneShotRetryCount)
		}
		// ReArmOneShot on an unknown name wraps ErrScheduleNotFound.
		if err := reArmer.ReArmOneShot(ctx, "conf-sched-rearm-missing", nextFire); !errors.Is(err, port.ErrScheduleNotFound) {
			t.Fatalf("ReArmOneShot(unknown) = %v, want ErrScheduleNotFound", err)
		}
	})

	t.Run("rearm one-shot concurrent calls do not double-increment", func(t *testing.T) {
		// THE ATOMICITY PROOF. Two callers ReArmOneShot the same name
		// "simultaneously". The store MUST serialize them so the counter advances
		// by exactly 2 (one per call), NOT a torn double-increment that loses an
		// update or double-counts. This is the at-most-once fence for the RE-ARM:
		// a concurrent re-arm must not double-enable or double-increment. Run with
		// -race to surface a torn update.
		s := newStore(t)
		reArmer, ok := s.(port.ScheduleOneShotReArmer)
		if !ok {
			t.Skip("store does not implement ScheduleOneShotReArmer (at-most-once — byte-identical pre-Phase-2)")
		}
		const name = "conf-sched-rearm-concurrent"
		now := time.Unix(1_700_000_000, 0)
		if err := s.Save(ctx, port.Schedule{
			Spec: port.ScheduleSpec{
				Name:              name,
				Prompt:            "once",
				Trigger:           port.TriggerSpec{OneShot: now},
				Mutating:          true,
				OneShotRetry:      true,
				OneShotMaxRetries: 10,
			},
			State: port.ScheduleState{NextFireAt: now, Enabled: true},
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := s.Claim(ctx, name, now, time.Time{}); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		// Two concurrent re-arms. Each must succeed and increment the counter by 1;
		// the final counter must be exactly 2 (no lost updates, no
		// double-increments).
		nextFire := now.Add(time.Minute)
		done := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() { done <- reArmer.ReArmOneShot(ctx, name, nextFire) }()
		}
		for i := 0; i < 2; i++ {
			if err := <-done; err != nil {
				t.Fatalf("ReArmOneShot %d: %v", i, err)
			}
		}
		got, err := s.Load(ctx, name)
		if err != nil {
			t.Fatalf("Load after concurrent ReArmOneShot: %v", err)
		}
		if got.State.OneShotRetryCount != 2 {
			t.Errorf("OneShotRetryCount = %d, want 2 (two concurrent re-arms must each increment once — atomicity)", got.State.OneShotRetryCount)
		}
		if !got.State.Enabled {
			t.Errorf("Enabled = false, want true (re-enabled)")
		}
	})
}

// assertScheduleEqual compares the spec + state fields the suite cares about
// (it does NOT reach into adapter-internal layout — only the port value object).
func assertScheduleEqual(t *testing.T, label string, got, want port.Schedule) {
	t.Helper()
	if got.Spec.Name != want.Spec.Name {
		t.Errorf("%s: Spec.Name = %q, want %q", label, got.Spec.Name, want.Spec.Name)
	}
	if got.Spec.Prompt != want.Spec.Prompt {
		t.Errorf("%s: Spec.Prompt = %q, want %q", label, got.Spec.Prompt, want.Spec.Prompt)
	}
	if got.Spec.Profile != want.Spec.Profile {
		t.Errorf("%s: Spec.Profile = %q, want %q", label, got.Spec.Profile, want.Spec.Profile)
	}
	if got.Spec.Workspace != want.Spec.Workspace {
		t.Errorf("%s: Spec.Workspace = %q, want %q", label, got.Spec.Workspace, want.Spec.Workspace)
	}
	if got.Spec.MaxFires != want.Spec.MaxFires {
		t.Errorf("%s: Spec.MaxFires = %d, want %d", label, got.Spec.MaxFires, want.Spec.MaxFires)
	}
	if got.Spec.Mutating != want.Spec.Mutating {
		t.Errorf("%s: Spec.Mutating = %v, want %v", label, got.Spec.Mutating, want.Spec.Mutating)
	}
	if !got.Spec.CreatedAt.Equal(want.Spec.CreatedAt) {
		t.Errorf("%s: Spec.CreatedAt = %v, want %v", label, got.Spec.CreatedAt, want.Spec.CreatedAt)
	}
	if !got.State.NextFireAt.Equal(want.State.NextFireAt) {
		t.Errorf("%s: State.NextFireAt = %v, want %v", label, got.State.NextFireAt, want.State.NextFireAt)
	}
	if got.State.Enabled != want.State.Enabled {
		t.Errorf("%s: State.Enabled = %v, want %v", label, got.State.Enabled, want.State.Enabled)
	}
}
