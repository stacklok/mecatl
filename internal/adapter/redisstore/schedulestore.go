package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Schedule key layout under the SAME Redis keyspace as the session store. The
// schedule name is the raw string after the prefix (Redis keys are arbitrary
// byte strings, so — unlike jsonlstore's filename-safe safeFilePart — no
// sanitization is needed, the same discipline the session store applies).
const (
	scheduleKeyPrefix     = "mecatl:schedule:"
	scheduleFireKeyPrefix = "mecatl:schedulefire:"

	// scheduleFormat is the per-record format tag written on every schedule and
	// fire record. It versions the encoding so a future incompatible format fails
	// loud on load (the EventLogFormat precedent): an unknown tag is an infra
	// error, never a silent skip.
	scheduleFormat = "redisstore-schedule/1"
)

// Hash fields on a schedule key. The spec is stored as JSON so List/Load return
// the FULL spec; the state fields are separate HASH fields so the Claim Lua
// script can read+advance them atomically in one EVAL (a plain HGETALL + HSET
// would race between replicas; the script is the at-most-once fence).
const (
	fieldSpec              = "spec"
	fieldNextFireAt        = "next_fire_at"
	fieldLastFireAt        = "last_fire_at"
	fieldFireCount         = "fire_count"
	fieldEnabled           = "enabled"
	fieldLastFireSessionID = "last_fire_session_id"
	fieldCreatedAt         = "created_at"
	// fieldOneShotRetryCount is the durable counter of one-shot re-arms (ADR
	// 0059 Phase 2). Absent on pre-Phase-2 records (treated as 0 by the script
	// and scheduleFromHash).
	fieldOneShotRetryCount = "one_shot_retry_count"
	// The in-flight scheduled-fire state fields (issue #386). Absent on
	// pre-#386 records (treated as zero by the scripts and scheduleFromHash).
	// RecordFireStart sets them; RecordFire clears them (HDEL); a fresh
	// Claim/ClaimNow zeroes them (HSET "0" — the nanoStr zero sentinel, so
	// scheduleFromHash's parseNano yields the zero time).
	fieldLastFireStartedAt  = "last_fire_started_at"
	fieldLastFireProgressAt = "last_fire_progress_at"
	fieldFireDeadline       = "fire_deadline"
)

// ErrScheduleNotFound is returned by Load/Delete/Claim/RecordFire/LoadFire when
// no schedule (or fire) exists under the requested name/id. It wraps
// port.ErrScheduleNotFound so a consumer that may not import this adapter can
// distinguish not-found from an infra failure via errors.Is, the same discipline
// the session store applies for ErrNotFound (and the jsonl/mem schedule stores
// apply for their ErrNotFound/ErrScheduleNotFound). It is a SEPARATE sentinel
// from the session-store ErrNotFound so a reader can tell which seam missed.
var ErrScheduleNotFound = fmt.Errorf("redisstore: schedule not found: %w", port.ErrScheduleNotFound)

// claimScript is the AT-MOST-ONCE atomic advance, expressed as a Lua script so
// Redis executes it single-threaded (no client mutex needed — the redisstore.Store
// precedent). A plain HGETALL + HSET would race between replicas: two Claim
// calls could both observe the slot still-due and both advance it. The script
// does the read-check-advance in ONE atomic EVAL, so exactly one Claim wins.
//
// KEYS[1] = mecatl:schedule:<name>
// ARGV[1] = now          (unix nano, string; the LastFireAt to stamp)
// ARGV[2] = nextFire     (unix nano, string; "0" means zero time = no further fire)
// ARGV[3] = pendingSID   (the port.PendingFireSessionID sentinel)
// ARGV[4] = maxFires     (int, string; "0" = forever — passed as an ARG so the
//
//	script checks exhaustion WITHOUT decoding the spec JSON)
//
// Returns:
//   - the bulk string "NOT_FOUND"      if the schedule key does not exist;
//   - the bulk string "NOT_CLAIMABLE"  if the slot is no longer due (a peer
//     already claimed it, it was disabled, or it exhausted MaxFires);
//   - an array {next_fire_at, last_fire_at, fire_count, enabled,
//     last_fire_session_id} describing the ADVANCED state on success.
//
// Both sentinel cases map to ErrScheduleNotFound in Go (the fail-safe
// interpretation the conformance suite pins: a second Claim at the same now does
// NOT re-claim — the slot is gone). redis.NewScript loads the script once and
// reuses EVALSHA on subsequent calls.
//
// PRECISION NOTE: the nfa/now comparison `tonumber(nfa) > now` operates on
// unix-nano values (~1.7e18), which exceed Lua 5.1's 53-bit double mantissa,
// giving ~256ns quantization. This is harmless for cron-grade scheduling (the
// smallest cron granularity is 1s; the tick interval is 30s), but a future
// sub-millisecond schedule would need a string-compare or split high/low
// representation.
var claimScript = redis.NewScript(`
local exists = redis.call('EXISTS', KEYS[1])
if exists == 0 then
  return 'NOT_FOUND'
end
local enabled = redis.call('HGET', KEYS[1], 'enabled')
local nfa = redis.call('HGET', KEYS[1], 'next_fire_at')
local fc = redis.call('HGET', KEYS[1], 'fire_count')
local now = tonumber(ARGV[1])
local nextFire = ARGV[2]
local pending = ARGV[3]
local maxFires = tonumber(ARGV[4])
if enabled == false or enabled == '0' then
  return 'NOT_CLAIMABLE'
end
if nfa == false or nfa == '' or nfa == '0' then
  return 'NOT_CLAIMABLE'
end
if tonumber(nfa) > now then
  return 'NOT_CLAIMABLE'
end
if maxFires > 0 and tonumber(fc) >= maxFires then
  return 'NOT_CLAIMABLE'
end
local newFC = tostring(tonumber(fc) + 1)
local newEnabled = enabled
if nextFire == '0' then
  newEnabled = '0'
end
redis.call('HSET', KEYS[1],
  'last_fire_at', ARGV[1],
  'next_fire_at', nextFire,
  'fire_count', newFC,
  'last_fire_session_id', pending,
  'enabled', newEnabled,
  'last_fire_started_at', '0',
  'last_fire_progress_at', '0',
  'fire_deadline', '0')
return {nextFire, ARGV[1], newFC, newEnabled, pending}
`)

// claimNowScript is the manual-trigger variant of claimScript (the FireNow
// primitive): the SAME atomic advance WITHOUT the next_fire_at <= now due-check.
// It fences at-most-once on last_fire_at == now instead: a prior ClaimNow (or a
// Claim) at this same `now` already advanced last_fire_at, so a second is
// rejected (Claim's fence is "next_fire_at is past now", which a future slot
// fails — ClaimNow cannot use it). The enabled + MaxFires checks still apply.
//
// Returns the same shape as claimScript: "NOT_FOUND" / "NOT_CLAIMABLE" / the
// advanced-state array. See port.ScheduleStore.ClaimNow for the rationale.
var claimNowScript = redis.NewScript(`
local exists = redis.call('EXISTS', KEYS[1])
if exists == 0 then
  return 'NOT_FOUND'
end
local enabled = redis.call('HGET', KEYS[1], 'enabled')
local lfa = redis.call('HGET', KEYS[1], 'last_fire_at')
local fc = redis.call('HGET', KEYS[1], 'fire_count')
local now = ARGV[1]
local nextFire = ARGV[2]
local pending = ARGV[3]
local maxFires = tonumber(ARGV[4])
if enabled == false or enabled == '0' then
  return 'NOT_CLAIMABLE'
end
if maxFires > 0 and tonumber(fc) >= maxFires then
  return 'NOT_CLAIMABLE'
end
-- At-most-once fence WITHOUT the due-check: a prior Claim/ClaimNow at this same
-- now already advanced last_fire_at. Reject so the advance is not repeated.
if lfa == now then
  return 'NOT_CLAIMABLE'
end
local newFC = tostring(tonumber(fc) + 1)
local newEnabled = enabled
if nextFire == '0' then
  newEnabled = '0'
end
redis.call('HSET', KEYS[1],
  'last_fire_at', now,
  'next_fire_at', nextFire,
  'fire_count', newFC,
  'last_fire_session_id', pending,
  'enabled', newEnabled,
  'last_fire_started_at', '0',
  'last_fire_progress_at', '0',
  'fire_deadline', '0')
return {nextFire, now, newFC, newEnabled, pending}
`)

// setEnabledScript atomically checks existence and sets the enabled field in
// ONE EVAL, so a concurrent Delete between the EXISTS and HSET of the old
// two-command path cannot leave a zombie key, and a concurrent Save cannot have
// its enabled field clobbered. Mirrors the claimScript/claimNowScript pattern.
//
// KEYS[1] = mecatl:schedule:<name>
// ARGV[1] = "1" or "0" (the enabled bool as a string)
// Returns "NOT_FOUND" if the key does not exist, "OK" on success.
var setEnabledScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 'NOT_FOUND'
end
redis.call('HSET', KEYS[1], 'enabled', ARGV[1])
return 'OK'
`)

// createScheduleScript is the atomic create-only primitive (review finding 5,
// issue #368): the existence check + the full HSET seed run in ONE EVAL, so two
// concurrent Create calls for the same name cannot both observe absence and
// both write — Redis's single-threaded script execution serializes them, and
// exactly one sees EXISTS==0 and wins. Mirrors setEnabledScript's
// check-then-mutate-in-one-EVAL discipline.
//
// KEYS[1] = mecatl:schedule:<name>
// ARGV = the flat field/value list to HSET on a win (the same fields Save's
//
//	new-schedule branch seeds).
//
// Returns "EXISTS" if the key already exists (create refused, record
// untouched), "OK" on success.
var createScheduleScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 'EXISTS'
end
redis.call('HSET', KEYS[1], unpack(ARGV))
return 'OK'
`)

// reArmOneShotScript is the at-least-once re-arm primitive for a one-shot
// schedule (ADR 0059 Phase 2). Under Redis's single-threaded execution it
// atomically: checks the key exists, re-enables the schedule (enabled=1), sets
// next_fire_at to nextFire, and increments one_shot_retry_count. The atomicity
// (the EVAL) is the re-arm fence: two concurrent re-arms cannot double-increment
// the counter or double-enable. The retry-budget gate is the CALLER's
// responsibility — the script does NOT enforce the budget. One-shot-only.
//
// KEYS[1] = mecatl:schedule:<name>
// ARGV[1] = nextFire (unix nano, string; "0" means zero time = no further fire)
// Returns "NOT_FOUND" if the key does not exist, "OK" on success.
var reArmOneShotScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  return 'NOT_FOUND'
end
local fc = redis.call('HGET', KEYS[1], 'one_shot_retry_count')
if fc == false then fc = '0' end
redis.call('HSET', KEYS[1],
  'enabled', '1',
  'next_fire_at', ARGV[1],
  'one_shot_retry_count', tostring(tonumber(fc) + 1))
return 'OK'
`)

// fireProgressScript is the ATOMIC compare-and-set for advancing an in-flight
// fire record's ProgressAt (review finding M2, issue #386). A non-atomic
// GET-then-SET would race a concurrent terminal RecordFire: the terminal SET
// landing between the GET and the progress SET would revert the record from
// terminal back to in-flight (a phantom in-flight fire). The script closes that
// window by executing the read-check-write in ONE atomic EVAL.
//
// The decode/encode of the JSON fire record is done in GO (the codebase-wide
// discipline — no cjson anywhere; claimScript/claimNowScript pass scalars as ARGV
// and never decode JSON in Lua). The script is a byte-compare CAS: it GETs the
// current record and SETs the new one ONLY when the current bytes are still the
// in-flight bytes Go read (ARGV[1]); if a concurrent terminal RecordFire (or a
// sibling RecordFireProgress) changed the record in between, current != ARGV[1]
// and the CAS aborts (returns 0) — a terminal record is NEVER reverted. The
// "Stop empty" guard is implicit: Go builds ARGV[2] (the candidate new record)
// only from a record it observed in-flight (Stop empty), so the CAS never writes
// a terminal → in-flight revert; the byte-compare additionally guarantees no
// terminal SET landed in the window.
//
// KEYS[1] = mecatl:schedulefire:<fireID>
// ARGV[1] = oldJSON (the in-flight record bytes Go read — the optimistic-lock
//
//	expected value)
//
// ARGV[2] = newJSON (the candidate record with ProgressAt advanced)
// Returns 1 when the record was advanced, 0 when the CAS aborted (a concurrent
// terminal RecordFire / sibling progress won — best-effort; the state alone
// carries the progress, and a terminal record is left untouched).
var fireProgressScript = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if cur == false then
  return 0
end
if cur ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2])
return 1
`)

// scheduleStore is a Redis-backed port.ScheduleStore sharing the parent *Store's
// *redis.Client. It is the MULTI-REPLICA production schedule backend
// (scheduled-tasks issue #189, Phase 1d): the SAME logic as
// memschedulestore/jsonlstore.scheduleStore with Redis persistence + a Lua-script
// atomic Claim. No client-side mutex is needed — Redis serializes commands
// single-threaded, and the Claim script is the cross-replica at-most-once fence
// (where the jsonl mutex was single-process only).
//
// It is parser-free (Claim's nextFire is caller-computed) and misfire-free (Due
// does not read ScheduleSpec.Misfire) — the same discipline as the in-memory and
// jsonl references.
type scheduleStore struct {
	client *redis.Client
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
// 5, issue #368) via createScheduleScript: the existence check + the full
// field seed run in ONE Redis EVAL, so two concurrent Create calls for the
// same name cannot both observe absence — Redis's single-threaded script
// execution is the fence. A name already in use returns ErrScheduleAlreadyExists
// (wrapped) and leaves the existing record untouched.
func (s *scheduleStore) Create(ctx context.Context, in port.Schedule) error {
	specJSON, err := json.Marshal(cloneSpec(in.Spec))
	if err != nil {
		return fmt.Errorf("redisstore: marshal schedule spec: %w", err)
	}
	// Seed the same fields Save's new-schedule branch seeds, including the
	// zero-State-defaults-enabled rule.
	enabled := in.State.Enabled
	if in.State.NextFireAt.IsZero() && !in.State.Enabled && in.State.FireCount == 0 {
		enabled = true
	}
	res, err := createScheduleScript.Run(ctx, s.client, []string{scheduleKey(in.Spec.Name)},
		fieldSpec, specJSON,
		fieldNextFireAt, nanoStr(in.State.NextFireAt),
		fieldLastFireAt, nanoStr(in.State.LastFireAt),
		fieldFireCount, strconv.Itoa(in.State.FireCount),
		fieldEnabled, boolStr(enabled),
		fieldLastFireSessionID, string(in.State.LastFireSessionID),
		fieldCreatedAt, nanoStr(in.Spec.CreatedAt),
		fieldOneShotRetryCount, strconv.Itoa(in.State.OneShotRetryCount),
		fieldLastFireStartedAt, nanoStr(in.State.LastFireStartedAt),
		fieldLastFireProgressAt, nanoStr(in.State.LastFireProgressAt),
		fieldFireDeadline, nanoStr(in.State.FireDeadline),
	).Result()
	if err != nil {
		return fmt.Errorf("redisstore: create schedule %q: %w", in.Spec.Name, err)
	}
	if str, ok := res.(string); ok && str == "EXISTS" {
		return fmt.Errorf("redisstore: %w: %q", port.ErrScheduleAlreadyExists, in.Spec.Name)
	}
	return nil
}

// Save upserts the schedule by Spec.Name. A schedule with the same name is
// overwritten on the Spec half; the State half is PRESERVED on overwrite (a
// Save with a fresh zero State does not reset firing progress — call Delete +
// Save to reset, the port doc says so). A NEW schedule is initialised with
// Enabled=true (a new schedule is active by default; pause it by overwriting
// State.Enabled=false, which Save preserves on re-save). The semantics mirror
// memschedulestore.Save / jsonlstore.scheduleStore.Save byte-for-byte.
//
// On overwrite only the spec + created_at fields are HSET; the state fields
// (next_fire_at, last_fire_at, fire_count, enabled, last_fire_session_id) are
// left untouched so firing progress survives a re-save. On a NEW schedule the
// state fields are seeded from in.State (with the zero-State-defaults-enabled
// rule). Redis serializes the HSET, so no client mutex is required.
func (s *scheduleStore) Save(ctx context.Context, in port.Schedule) error {
	specJSON, err := json.Marshal(cloneSpec(in.Spec))
	if err != nil {
		return fmt.Errorf("redisstore: marshal schedule spec: %w", err)
	}
	key := scheduleKey(in.Spec.Name)
	// EXISTS is atomic w.r.t. other commands; a concurrent Save of the same name
	// is an upsert either way (both HSET the spec), so the existed/not-existed
	// branch only decides whether to seed state fields. A race between two Saves
	// of a NEW schedule could seed state twice — harmless (both write the same
	// in.State-derived fields, and Save is caller-serialised per name per the
	// port concurrency contract).
	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("redisstore: save schedule %q (exists): %w", in.Spec.Name, err)
	}
	if exists == 0 {
		// New schedule: honour an explicit State, but default a zero State to
		// enabled (a freshly-created schedule is active). A caller that wants a
		// new schedule disabled passes a non-zero State with Enabled=false.
		enabled := in.State.Enabled
		if in.State.NextFireAt.IsZero() && !in.State.Enabled && in.State.FireCount == 0 {
			enabled = true
		}
		if err := s.client.HSet(ctx, key,
			fieldSpec, specJSON,
			fieldNextFireAt, nanoStr(in.State.NextFireAt),
			fieldLastFireAt, nanoStr(in.State.LastFireAt),
			fieldFireCount, strconv.Itoa(in.State.FireCount),
			fieldEnabled, boolStr(enabled),
			fieldLastFireSessionID, string(in.State.LastFireSessionID),
			fieldCreatedAt, nanoStr(in.Spec.CreatedAt),
			fieldOneShotRetryCount, strconv.Itoa(in.State.OneShotRetryCount),
			fieldLastFireStartedAt, nanoStr(in.State.LastFireStartedAt),
			fieldLastFireProgressAt, nanoStr(in.State.LastFireProgressAt),
			fieldFireDeadline, nanoStr(in.State.FireDeadline),
		).Err(); err != nil {
			return fmt.Errorf("redisstore: save schedule %q (new): %w", in.Spec.Name, err)
		}
		return nil
	}
	// Overwrite: replace only the spec + created_at, PRESERVE the state fields.
	if err := s.client.HSet(ctx, key,
		fieldSpec, specJSON,
		fieldCreatedAt, nanoStr(in.Spec.CreatedAt),
	).Err(); err != nil {
		return fmt.Errorf("redisstore: save schedule %q (overwrite): %w", in.Spec.Name, err)
	}
	return nil
}

// SetEnabled atomically sets the schedule's Enabled flag WITHOUT touching any
// other State field (unlike Save, which preserves the State half on a Spec
// overwrite and so cannot mutate Enabled). It is the pause/resume primitive.
// The not-found case (key missing) wraps ErrScheduleNotFound. The
// existence-check + HSET run atomically in ONE Lua EVAL (setEnabledScript) so a
// concurrent Delete cannot leave a zombie key and a concurrent Save cannot have
// its enabled clobbered — the same atomic-discipline the Claim scripts uphold.
func (s *scheduleStore) SetEnabled(ctx context.Context, name string, enabled bool) error {
	key := scheduleKey(name)
	res, err := setEnabledScript.Run(ctx, s.client, []string{key}, boolStr(enabled)).Result()
	if err != nil {
		return fmt.Errorf("redisstore: set enabled %q: %w", name, err)
	}
	if s, ok := res.(string); ok && s == "NOT_FOUND" {
		return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
	}
	return nil
}

// ReArmOneShot is the at-least-once re-arm primitive for a one-shot schedule
// (ADR 0059 Phase 2). It EVALs reArmOneShotScript, which — under Redis's
// single-threaded execution — atomically re-enables the schedule (enabled=1),
// sets next_fire_at to nextFire, and increments one_shot_retry_count. The
// atomicity (the EVAL) is the re-arm fence: two concurrent re-arms cannot
// double-increment or double-enable (the concurrent-claim test proves it). The
// not-found case (key missing → NOT_FOUND) wraps ErrScheduleNotFound. The
// retry-budget gate is the CALLER's responsibility. One-shot-only.
func (s *scheduleStore) ReArmOneShot(ctx context.Context, name string, nextFire time.Time) error {
	key := scheduleKey(name)
	res, err := reArmOneShotScript.Run(ctx, s.client, []string{key}, nanoStr(nextFire)).Result()
	if err != nil {
		return fmt.Errorf("redisstore: re-arm one-shot %q: %w", name, err)
	}
	if s, ok := res.(string); ok && s == "NOT_FOUND" {
		return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
	}
	return nil
}

// Load returns the schedule stored under name. The not-found case (key missing,
// redis.Nil on HGETALL of a non-existent key returns an empty map rather than
// Nil, so an empty map is treated as not-found) wraps port.ErrScheduleNotFound.
func (s *scheduleStore) Load(ctx context.Context, name string) (port.Schedule, error) {
	fields, err := s.client.HGetAll(ctx, scheduleKey(name)).Result()
	if err != nil {
		return port.Schedule{}, fmt.Errorf("redisstore: load schedule %q: %w", name, err)
	}
	if len(fields) == 0 {
		return port.Schedule{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
	}
	return scheduleFromHash(fields)
}

// Delete removes the schedule stored under name. It is IDEMPOTENT: DEL on a
// missing key succeeds (the PrunableStore.Delete discipline). It does NOT delete
// the schedule's fire records (retention is the caller's concern via the
// existing PrunableStore) — matching memschedulestore / jsonlstore.
func (s *scheduleStore) Delete(ctx context.Context, name string) error {
	if err := s.client.Del(ctx, scheduleKey(name)).Err(); err != nil {
		return fmt.Errorf("redisstore: delete schedule %q: %w", name, err)
	}
	return nil
}

// List returns ALL stored schedules, in no guaranteed order. It SCANs the
// keyspace for schedule keys (MATCH mecatl:schedule:*, the production-safe
// cursor-based pattern — never KEYS), then HGETALLs each. The
// mecatl:schedulefire: prefix does NOT collide with mecatl:schedule: (the
// character after "schedule" is "f" vs ":", so the glob excludes fire keys). A
// corrupt entry (missing spec, unparseable JSON) is skipped best-effort rather
// than failing the whole inventory.
func (s *scheduleStore) List(ctx context.Context) ([]port.Schedule, error) {
	var out []port.Schedule
	scan := s.client.Scan(ctx, 0, scheduleKeyPrefix+"*", 0).Iterator()
	for scan.Next(ctx) {
		fields, err := s.client.HGetAll(ctx, scan.Val()).Result()
		if err != nil {
			continue
		}
		if len(fields) == 0 {
			continue
		}
		sc, err := scheduleFromHash(fields)
		if err != nil {
			continue
		}
		out = append(out, sc)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("redisstore: list schedules: %w", err)
	}
	return out, nil
}

// Due returns the schedules whose NextFireAt <= now AND Enabled AND (when
// MaxFires > 0) FireCount < MaxFires. It is idempotent and side-effect-free; it
// does not advance state. The store does NOT read ScheduleSpec.Misfire — the
// misfire policy is a composition concern. It SCANs + filters, the same shape as
// List.
func (s *scheduleStore) Due(ctx context.Context, now time.Time) ([]port.Schedule, error) {
	nowNano := now.UnixNano()
	out := make([]port.Schedule, 0)
	scan := s.client.Scan(ctx, 0, scheduleKeyPrefix+"*", 0).Iterator()
	for scan.Next(ctx) {
		fields, err := s.client.HGetAll(ctx, scan.Val()).Result()
		if err != nil {
			continue
		}
		if len(fields) == 0 {
			continue
		}
		sc, err := scheduleFromHash(fields)
		if err != nil {
			continue
		}
		if !sc.State.Enabled {
			continue
		}
		if sc.State.NextFireAt.IsZero() || sc.State.NextFireAt.UnixNano() > nowNano {
			continue
		}
		if sc.Spec.MaxFires > 0 && sc.State.FireCount >= sc.Spec.MaxFires {
			continue
		}
		out = append(out, sc)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("redisstore: due schedules: %w", err)
	}
	return out, nil
}

// Claim is the AT-MOST-ONCE atomic advance. It EVALs claimScript, which — under
// Redis's single-threaded execution — re-checks the slot is still due
// (Enabled && next_fire_at <= now && under MaxFires) and, only if so, atomically
// advances LastFireAt=now, NextFireAt=nextFire, FireCount++, LastFireSessionID=
// pending, and (for a zero nextFire: a one-shot or an exhausted cron) Enabled=
// false. If the slot is NOT due (a peer's Claim already advanced it, it was
// disabled, or it exhausted) the script returns the NOT_CLAIMABLE sentinel,
// which maps to ErrScheduleNotFound — the fail-safe interpretation the
// conformance suite pins (a second Claim at the same now does NOT re-claim).
// The not-found case (key missing → NOT_FOUND sentinel) likewise wraps
// ErrScheduleNotFound.
//
// MULTI-REPLICA: the Lua CAS is the cross-replica fence — no client mutex, no
// leader lease required for at-most-once (the jsonl mutex gave only
// single-process). This is the property the dedicated concurrent-claim test
// (-race) proves.
//
// The Spec is HGET'd separately to obtain MaxFires (passed as an ARG so the
// script checks exhaustion without decoding the spec JSON) and to construct the
// returned Schedule. The state advance itself is atomic in the script; the
// returned Spec is the pre-Claim HGET (a concurrent Save changing the Spec
// between HGET and EVAL preserves state by contract, so only the Spec half could
// be stale — an operator race, not an at-most-once correctness issue).
func (s *scheduleStore) Claim(ctx context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	key := scheduleKey(name)
	specJSON, err := s.client.HGet(ctx, key, fieldSpec).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return port.Schedule{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
		}
		return port.Schedule{}, fmt.Errorf("redisstore: claim schedule %q (spec): %w", name, err)
	}
	var spec port.ScheduleSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return port.Schedule{}, fmt.Errorf("redisstore: claim schedule %q (decode spec): %w", name, err)
	}
	// nextFire is passed as "0" for the zero time (the sentinel the script treats
	// as "no further fire" → disable), NOT nextFire.UnixNano() — a zero time.Time
	// has a large NEGATIVE UnixNano, which would not match the "0" branch.
	res, err := claimScript.Run(ctx, s.client,
		[]string{key},
		now.UnixNano(), nanoStr(nextFire), string(port.PendingFireSessionID), spec.MaxFires,
	).Result()
	if err != nil {
		return port.Schedule{}, fmt.Errorf("redisstore: claim schedule %q: %w", name, err)
	}
	switch v := res.(type) {
	case string:
		// NOT_FOUND or NOT_CLAIMABLE — both map to ErrScheduleNotFound (the slot
		// is gone: never existed, or a peer already claimed it).
		return port.Schedule{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
	case []interface{}:
		state, err := stateFromClaimResult(v)
		if err != nil {
			return port.Schedule{}, fmt.Errorf("redisstore: claim schedule %q (result): %w", name, err)
		}
		return port.Schedule{Spec: cloneSpec(spec), State: state}, nil
	default:
		return port.Schedule{}, fmt.Errorf("redisstore: claim schedule %q: unexpected script result type %T", name, res)
	}
}

// ClaimNow is the manual-trigger variant of Claim (the FireNow primitive). It
// EVALs claimNowScript, which performs the SAME atomic advance as claimScript
// WITHOUT the next_fire_at <= now due-check — it claims the slot regardless of
// whether it is due. The Enabled + MaxFires checks still apply. The at-most-once
// fence is last_fire_at == now (a prior Claim/ClaimNow at this same now already
// advanced it). The not-found case (key missing → NOT_FOUND) and the
// not-claimable case (disabled / exhausted / already-advanced → NOT_CLAIMABLE)
// both wrap ErrScheduleNotFound. See port.ScheduleStore.ClaimNow.
func (s *scheduleStore) ClaimNow(ctx context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	key := scheduleKey(name)
	specJSON, err := s.client.HGet(ctx, key, fieldSpec).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return port.Schedule{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
		}
		return port.Schedule{}, fmt.Errorf("redisstore: claim-now schedule %q (spec): %w", name, err)
	}
	var spec port.ScheduleSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return port.Schedule{}, fmt.Errorf("redisstore: claim-now schedule %q (decode spec): %w", name, err)
	}
	res, err := claimNowScript.Run(ctx, s.client,
		[]string{key},
		nanoStr(now), nanoStr(nextFire), string(port.PendingFireSessionID), spec.MaxFires,
	).Result()
	if err != nil {
		return port.Schedule{}, fmt.Errorf("redisstore: claim-now schedule %q: %w", name, err)
	}
	switch v := res.(type) {
	case string:
		// NOT_FOUND or NOT_CLAIMABLE — both map to ErrScheduleNotFound (the slot
		// is gone: never existed, disabled, exhausted, or already-advanced).
		return port.Schedule{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
	case []interface{}:
		state, err := stateFromClaimResult(v)
		if err != nil {
			return port.Schedule{}, fmt.Errorf("redisstore: claim-now schedule %q (result): %w", name, err)
		}
		return port.Schedule{Spec: cloneSpec(spec), State: state}, nil
	default:
		return port.Schedule{}, fmt.Errorf("redisstore: claim-now schedule %q: unexpected script result type %T", name, res)
	}
}

// RecordFireStart persists the IN-FLIGHT fire (issue #386): the fire's run has
// begun but not yet produced a terminal outcome. It stores the fire record
// (in-flight: Stop empty, StartedAt set) and stamps the schedule's
// LastFireSessionID to the REAL session id (overwriting the pending sentinel
// Claim set) + LastFireStartedAt (and seeds LastFireProgressAt to StartedAt when
// the caller passed a zero ProgressAt) + FireDeadline. It is IDEMPOTENT per
// fire id: a re-record of the same in-flight fire (same StartedAt) is a no-op
// for the in-flight record; a re-record for a fire id that is ALREADY terminal
// is a no-op (a terminal fire is not re-opened). The not-found case (the
// schedule was deleted between Claim and RecordFireStart) wraps
// port.ErrScheduleNotFound. The semantics mirror memschedulestore.RecordFireStart
// byte-for-byte, adapted to Redis's single-threaded execution (no client mutex).
func (s *scheduleStore) RecordFireStart(ctx context.Context, name string, fire port.ScheduleFire) error {
	key := scheduleKey(name)
	// The schedule must exist (the not-found case — the schedule was deleted
	// between Claim and RecordFireStart). HGET the spec as a liveness probe.
	if specRaw, err := s.client.HGet(ctx, key, fieldSpec).Result(); err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
		}
		return fmt.Errorf("redisstore: record fire start %q (schedule): %w", fire.ID, err)
	} else if specRaw == "" {
		return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
	}
	fireKey := scheduleFireKey(fire.ID)
	// A fire id that is already terminal is not re-opened; and a re-record of the
	// same in-flight fire (same StartedAt) is a no-op. Read the existing fire
	// record to distinguish (best-effort under Redis's single-threaded execution).
	if raw, err := s.client.Get(ctx, fireKey).Bytes(); err == nil {
		var existing scheduleFireRecord
		if json.Unmarshal(raw, &existing) == nil && existing.V == scheduleFormat {
			if existing.Fire.Stop != "" {
				return nil // already terminal — not re-opened
			}
			if existing.Fire.StartedAt.Equal(fire.StartedAt) {
				return nil // same in-flight fire — idempotent
			}
		}
	} else if !errors.Is(err, redis.Nil) {
		return fmt.Errorf("redisstore: record fire start %q (load existing): %w", fire.ID, err)
	}
	// Stamp the schedule's in-flight state fields BEFORE latching the fire key
	// (the RecordFire ordering precedent: the fire key is the idempotency latch,
	// so a failed pre-latch write leaves the latch unset and a retry re-runs it).
	progressAt := fire.ProgressAt
	if progressAt.IsZero() {
		progressAt = fire.StartedAt
	}
	if err := s.client.HSet(ctx, key,
		fieldLastFireSessionID, string(fire.SessionID),
		fieldLastFireStartedAt, nanoStr(fire.StartedAt),
		fieldLastFireProgressAt, nanoStr(progressAt),
		fieldFireDeadline, nanoStr(fire.Deadline),
	).Err(); err != nil {
		return fmt.Errorf("redisstore: record fire start %q (update state): %w", fire.ID, err)
	}
	// Latch the in-flight fire record. SET (not SETNX): a prior in-flight record
	// under the same id with a DIFFERENT StartedAt (a re-RecordFireStart after a
	// retry that re-derived StartedAt) is overwritten — the in-flight record is
	// mutable until RecordFire flips it terminal.
	inFlight := fire
	inFlight.Stop = "" // an in-flight fire has no terminal outcome
	fireJSON, err := json.Marshal(scheduleFireRecord{V: scheduleFormat, Fire: inFlight})
	if err != nil {
		return fmt.Errorf("redisstore: marshal schedule fire start: %w", err)
	}
	if err := s.client.Set(ctx, fireKey, fireJSON, 0).Err(); err != nil {
		return fmt.Errorf("redisstore: record fire start %q (set): %w", fire.ID, err)
	}
	return nil
}

// RecordFireProgress advances the in-flight fire's last-observed-progress
// instant (issue #386). It updates LastFireProgressAt on the state and ProgressAt
// on the in-flight fire record (when `at` is after the stored value — an earlier
// `at` is ignored so a reordered update cannot rewind progress). It is
// best-effort/idempotent: a missing in-flight fire record records on the state
// alone; a not-found schedule wraps port.ErrScheduleNotFound; a terminal fire is
// untouched. The semantics mirror memschedulestore.RecordFireProgress
// byte-for-byte, adapted to Redis's single-threaded execution.
//
// fireID targets the SINGLE in-flight fire record by its known key
// (scheduleFireKey(fireID)) directly — NO SCAN of the fire keyspace (review
// finding M1: scanning every fire key on every turn boundary is O(N) in the fire
// population and contends the keyspace). The fire-record write is ATOMIC via the
// fireProgressScript Lua CAS (review finding M2): a concurrent terminal
// RecordFire's SET landing between the progress read and write CANNOT revert the
// record from terminal back to in-flight — the CAS aborts when the record
// changed (a terminal RecordFire landed), so a terminal record is never
// reverted.
func (s *scheduleStore) RecordFireProgress(ctx context.Context, name string, fireID string, at time.Time) error {
	key := scheduleKey(name)
	// The schedule must exist (the not-found case).
	if specRaw, err := s.client.HGet(ctx, key, fieldSpec).Result(); err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
		}
		return fmt.Errorf("redisstore: record fire progress %q (schedule): %w", name, err)
	} else if specRaw == "" {
		return fmt.Errorf("%w: %q", ErrScheduleNotFound, name)
	}
	if !at.IsZero() {
		// Advance the state's LastFireProgressAt only when `at` is after the stored
		// value (monotonic; never rewinds). Read-then-conditionally-write is safe
		// here because the tick loop is single-threaded per schedule (the port
		// concurrency contract: same-name calls are serialised by the caller).
		if cur, err := s.client.HGet(ctx, key, fieldLastFireProgressAt).Result(); err == nil || errors.Is(err, redis.Nil) {
			stored := parseNano(cur) // parseNano handles "" (redis.Nil) as zero
			if at.After(stored) {
				if err := s.client.HSet(ctx, key, fieldLastFireProgressAt, nanoStr(at)).Err(); err != nil {
					return fmt.Errorf("redisstore: record fire progress %q (update state): %w", name, err)
				}
			}
		} else {
			return fmt.Errorf("redisstore: record fire progress %q (read state): %w", name, err)
		}
	}
	// Advance the in-flight fire record's ProgressAt by its known key (no SCAN).
	// Atomic via fireProgressScript (review finding M2): the CAS aborts when the
	// record changed between the read and the write (a concurrent terminal
	// RecordFire landed), so a terminal record is never reverted to in-flight.
	return s.advanceInFlightFireProgress(ctx, fireID, at)
}

// advanceInFlightFireProgress advances the single in-flight fire record's
// ProgressAt (keyed by fireID) when `at` is after the stored value. Best-effort:
// a missing record (redis.Nil — no prior RecordFireStart) is a no-op success (the
// state alone carries the progress); a TERMINAL record is untouched. The write is
// ATOMIC via fireProgressScript (review finding M2): a concurrent terminal
// RecordFire's SET landing between this read and write CANNOT revert the record
// from terminal back to in-flight — the CAS (byte-compare on the record Go read)
// aborts when the record changed, so a terminal record is never reverted.
func (s *scheduleStore) advanceInFlightFireProgress(ctx context.Context, fireID string, at time.Time) error {
	fireKey := scheduleFireKey(fireID)
	raw, err := s.client.Get(ctx, fireKey).Bytes()
	if err != nil {
		// A missing record (no prior RecordFireStart) is best-effort: the state
		// alone carries the progress.
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return fmt.Errorf("redisstore: record fire progress %q (load fire): %w", fireID, err)
	}
	var rec scheduleFireRecord
	if json.Unmarshal(raw, &rec) != nil || rec.V != scheduleFormat {
		return nil // best-effort: a corrupt record is skipped (the state alone carries it)
	}
	// A terminal fire is untouched: the progress write must NEVER revert a
	// terminal record back to in-flight (review finding M2).
	if rec.Fire.Stop != "" {
		return nil
	}
	if at.IsZero() || !at.After(rec.Fire.ProgressAt) {
		return nil // not advancing (zero or stale `at`)
	}
	rec.Fire.ProgressAt = at
	newJSON, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("redisstore: marshal schedule fire progress: %w", err)
	}
	// Atomic CAS: SET only when the record is still the in-flight bytes Go read
	// (a concurrent terminal RecordFire that landed in between changes the bytes,
	// so the CAS aborts — the terminal record is NOT reverted). A concurrent
	// RecordFireProgress that won first changes the bytes too, so a loser aborts
	// (its `at` is lost; the next progress event re-reads and advances —
	// best-effort, monotonic).
	res, err := fireProgressScript.Run(ctx, s.client, []string{fireKey}, string(raw), string(newJSON)).Result()
	if err != nil {
		return fmt.Errorf("redisstore: record fire progress %q (cas): %w", fireID, err)
	}
	_ = res // 1 = advanced, 0 = raced (terminal RecordFire / sibling progress won) — best-effort
	return nil
}

// RecordFire records the outcome of a fire (f) and updates the schedule's
// LastFireSessionID to f.SessionID (overwriting the port.PendingFireSessionID
// value Claim set). It is IDEMPOTENT per fire id: recording the same f.ID twice
// is a no-op (a re-record of an already-TERMINAL fire returns nil without
// mutating state; an in-flight record under the same id is overwritten with the
// terminal one — the fire transitions in-flight → terminal). The not-found case
// (the schedule was deleted between Claim and RecordFire) wraps
// port.ErrScheduleNotFound.
//
// It FLIPS the fire terminal and clears the in-flight ScheduleState fields
// (LastFireStartedAt/LastFireProgressAt/FireDeadline) — a recorded (terminal)
// fire has no in-flight run (issue #386). The semantics mirror
// memschedulestore.RecordFire byte-for-byte, adapted to Redis's single-threaded
// execution.
func (s *scheduleStore) RecordFire(ctx context.Context, f port.ScheduleFire) error {
	fireJSON, err := json.Marshal(scheduleFireRecord{V: scheduleFormat, Fire: f})
	if err != nil {
		return fmt.Errorf("redisstore: marshal schedule fire: %w", err)
	}
	fireKey := scheduleFireKey(f.ID)
	// Idempotent per fire id: a re-record of an already-TERMINAL fire is a no-op.
	// (An in-flight record under the same id is overwritten with the terminal one
	// below — the fire transitions in-flight → terminal.)
	if raw, err := s.client.Get(ctx, fireKey).Bytes(); err == nil {
		var existing scheduleFireRecord
		if json.Unmarshal(raw, &existing) == nil && existing.V == scheduleFormat && existing.Fire.Stop != "" {
			return nil // already terminal — idempotent no-op
		}
	} else if !errors.Is(err, redis.Nil) {
		return fmt.Errorf("redisstore: record fire %q (load existing): %w", f.ID, err)
	}
	// The schedule must still exist (it may have been deleted between Claim and
	// RecordFire). HGET the spec as a liveness probe.
	if specRaw, err := s.client.HGet(ctx, scheduleKey(f.ScheduleName), fieldSpec).Result(); err != nil {
		if errors.Is(err, redis.Nil) {
			return fmt.Errorf("%w: %q", ErrScheduleNotFound, f.ScheduleName)
		}
		return fmt.Errorf("redisstore: record fire %q (schedule): %w", f.ID, err)
	} else if specRaw == "" {
		return fmt.Errorf("%w: %q", ErrScheduleNotFound, f.ScheduleName)
	}
	// Stamp the real session id over the port.PendingFireSessionID value Claim
	// set AND clear the in-flight fields (a terminal fire has no in-flight run) —
	// BEFORE latching the fire key. Ordering matters: the fire key (SET below) is
	// the idempotency latch, so it MUST be the last write. If the stamp ran AFTER
	// the latch and then failed transiently, a retry would short-circuit at the
	// terminal-record check above (the latch is set with a terminal Stop) and
	// never re-run the stamp — wedging LastFireSessionID at PendingFireSessionID
	// permanently (review #189). With the stamp first, a failed stamp leaves the
	// latch unset so a retry re-runs it; a failed latch after a successful stamp
	// simply re-stamps the same value (idempotent) and re-latches.
	if err := s.client.HSet(ctx, scheduleKey(f.ScheduleName), fieldLastFireSessionID, string(f.SessionID)).Err(); err != nil {
		return fmt.Errorf("redisstore: record fire %q (update session): %w", f.ID, err)
	}
	if err := s.client.HDel(ctx, scheduleKey(f.ScheduleName),
		fieldLastFireStartedAt, fieldLastFireProgressAt, fieldFireDeadline,
	).Err(); err != nil {
		return fmt.Errorf("redisstore: record fire %q (clear in-flight): %w", f.ID, err)
	}
	// Latch the terminal fire record (SET overwrites any prior in-flight record
	// under the same id — the in-flight → terminal transition). Two concurrent
	// RecordFire of the same fire race past the terminal-record check, but both
	// write the identical terminal record, so the result is not consulted.
	if err := s.client.Set(ctx, fireKey, fireJSON, 0).Err(); err != nil {
		return fmt.Errorf("redisstore: record fire %q (set): %w", f.ID, err)
	}
	return nil
}

// LoadFire returns the fire record stored under fireID. The not-found case
// (redis.Nil on GET) wraps port.ErrScheduleNotFound.
func (s *scheduleStore) LoadFire(ctx context.Context, fireID string) (port.ScheduleFire, error) {
	raw, err := s.client.Get(ctx, scheduleFireKey(fireID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return port.ScheduleFire{}, fmt.Errorf("%w: %q", ErrScheduleNotFound, fireID)
		}
		return port.ScheduleFire{}, fmt.Errorf("redisstore: load fire %q: %w", fireID, err)
	}
	var rec scheduleFireRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return port.ScheduleFire{}, fmt.Errorf("redisstore: decode fire %q: %w", fireID, err)
	}
	if rec.V != scheduleFormat {
		return port.ScheduleFire{}, fmt.Errorf("redisstore: unknown schedule-fire format %q (want %q)", rec.V, scheduleFormat)
	}
	return rec.Fire, nil
}

// ListFires returns the fire records for a schedule, in no guaranteed order. The
// not-found case for the SCHEDULE wraps port.ErrScheduleNotFound; an empty fire
// list for an existing schedule is a successful empty slice (not an error). It
// SCANs the fire keyspace (MATCH mecatl:schedulefire:*, the production-safe
// cursor-based pattern — never KEYS) and filters by ScheduleName — a fire record
// carries its ScheduleName foreign key, so it is locatable without a
// per-schedule fire index. A corrupt entry (unparseable JSON) is skipped
// best-effort rather than failing the whole list.
func (s *scheduleStore) ListFires(ctx context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	// The schedule must exist (the not-found-for-the-schedule contract). HGETALL
	// of a missing key returns an empty map, so an empty map is treated as
	// not-found (the same discipline Load applies).
	fields, err := s.client.HGetAll(ctx, scheduleKey(scheduleName)).Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: list fires %q (schedule): %w", scheduleName, err)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrScheduleNotFound, scheduleName)
	}
	out := make([]port.ScheduleFire, 0)
	scan := s.client.Scan(ctx, 0, scheduleFireKeyPrefix+"*", 0).Iterator()
	for scan.Next(ctx) {
		raw, err := s.client.Get(ctx, scan.Val()).Bytes()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue // raced away between SCAN and GET — skip.
			}
			continue // best-effort: a corrupt/unreadable fire is skipped, not fatal.
		}
		var rec scheduleFireRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		if rec.V != scheduleFormat {
			continue
		}
		if rec.Fire.ScheduleName != scheduleName {
			continue
		}
		out = append(out, rec.Fire)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("redisstore: list fires %q: %w", scheduleName, err)
	}
	return out, nil
}

// scheduleFireRecord is the envelope stored at a fire key: a format tag plus the
// verbatim port.ScheduleFire JSON. It mirrors the jsonlstore envelope shape so
// the two stores are codec-siblings (and a forward-incompatible record fails
// loud on load, not silently decodes).
type scheduleFireRecord struct {
	V    string            `json:"v"`
	Fire port.ScheduleFire `json:"fire"`
}

// scheduleFromHash reconstructs a port.Schedule from a HGETALL result map. A
// missing/empty spec field is an error (a corrupt/partial entry).
func scheduleFromHash(fields map[string]string) (port.Schedule, error) {
	specJSON, ok := fields[fieldSpec]
	if !ok || specJSON == "" {
		return port.Schedule{}, fmt.Errorf("redisstore: schedule hash missing spec field")
	}
	var spec port.ScheduleSpec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return port.Schedule{}, fmt.Errorf("redisstore: decode schedule spec: %w", err)
	}
	return port.Schedule{
		Spec: cloneSpec(spec),
		State: port.ScheduleState{
			NextFireAt:         parseNano(fields[fieldNextFireAt]),
			LastFireAt:         parseNano(fields[fieldLastFireAt]),
			FireCount:          parseIntOr(fields[fieldFireCount], 0),
			Enabled:            fields[fieldEnabled] == "1",
			LastFireSessionID:  session.SessionID(fields[fieldLastFireSessionID]),
			OneShotRetryCount:  parseIntOr(fields[fieldOneShotRetryCount], 0),
			LastFireStartedAt:  parseNano(fields[fieldLastFireStartedAt]),
			LastFireProgressAt: parseNano(fields[fieldLastFireProgressAt]),
			FireDeadline:       parseNano(fields[fieldFireDeadline]),
		},
	}, nil
}

// stateFromClaimResult parses the array returned by claimScript on success:
// {next_fire_at, last_fire_at, fire_count, enabled, last_fire_session_id}, each
// a bulk string.
func stateFromClaimResult(v []interface{}) (port.ScheduleState, error) {
	if len(v) != 5 {
		return port.ScheduleState{}, fmt.Errorf("expected 5 fields, got %d", len(v))
	}
	return port.ScheduleState{
		NextFireAt:        parseNano(toString(v[0])),
		LastFireAt:        parseNano(toString(v[1])),
		FireCount:         parseIntOr(toString(v[2]), 0),
		Enabled:           toString(v[3]) == "1",
		LastFireSessionID: session.SessionID(toString(v[4])),
	}, nil
}

// scheduleKey / scheduleFireKey map a schedule name / fire id to its Redis key.
// Redis keys are opaque byte strings, so — unlike jsonlstore's filename-safe
// safeFilePart — no sanitization is needed (the session-store precedent).
func scheduleKey(name string) string       { return scheduleKeyPrefix + name }
func scheduleFireKey(fireID string) string { return scheduleFireKeyPrefix + fireID }

// nanoStr renders a time as a unix-nano string, using "0" for the zero time
// (the sentinel the Claim script treats as "no further fire"). It is the
// inverse of parseNano.
func nanoStr(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

// parseNano parses a unix-nano string back to a time, treating "" and "0" as the
// zero time (the sentinel nanoStr writes for a zero time).
func parseNano(s string) time.Time {
	if s == "" || s == "0" {
		return time.Time{}
	}
	ns, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// parseIntOr parses a base-10 int, returning fallback on any error.
func parseIntOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// boolStr renders a bool as the "0"/"1" the Claim script checks.
func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// toString coerces a go-redis script-result element (a bulk string) to a Go
// string. A nil element (e.g. a missing HASH field the script read as false)
// becomes "".
func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// cloneSpec returns a copy of spec whose Parts slice is independent of the
// stored one (so a caller cannot mutate the store's record through the returned
// Schedule). ScheduleSpec is otherwise a struct of values. Mirrors
// memschedulestore.cloneSpec / jsonlstore.cloneSpec.
func cloneSpec(spec port.ScheduleSpec) port.ScheduleSpec {
	out := spec
	if spec.Parts != nil {
		out.Parts = make([]session.Content, len(spec.Parts))
		copy(out.Parts, spec.Parts)
	}
	return out
}
