// childgc.go is the retention/GC POLICY for persisted child sessions (issue
// #38). The delegation paths persist every child snapshot best-effort
// (WithSubagentStore / WithMemberStore / the Parallel branches) so
// InspectSubagent / InspectMember / resume: work — but nothing ever deleted
// them, so a durable store grew without bound. The MECHANISM (List/Delete)
// is the optional port.PrunableStore seam each store adapter implements; the
// POLICY (what is a child, how old is too old, how many per family, who is
// live) lives HERE, in composition — the stores never learn it.

package app

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// childSessionPrefixes are the delegation families' child-session id
// prefixes, consumed DIRECTLY from the engine's exported id-minting
// convention (engine/agent/childregistry.go — the constants the minting
// sites themselves derive from, so this list cannot drift from what is
// actually minted; TestChildSessionPrefixesMatchEngineConvention pins the
// wiring). ANY id outside these prefixes — every operator/service session —
// is NEVER touched by the child sweeper.
//
// SCOPE CAVEAT: a deployment that overrides a family's prefix
// (agent.WithChildSessionPrefix / WithParallelChildSessionPrefix /
// WithMemberSessionPrefix) mints ids OUTSIDE this list and thereby DE-SCOPES
// them from GC entirely — those children accumulate unswept until a matching
// policy knob exists. mecatl's own composition never overrides the prefixes.
var childSessionPrefixes = []string{
	agent.SubagentSessionPrefix,
	agent.ParallelSessionPrefix,
	agent.TeamSessionPrefix,
}

// scheduleFireSessionPrefixes are the scheduled-task fire-session id prefixes
// (ADR 0059 decision #7 Phase-2). A fire mints a "sched--"-prefixed top-level
// session (the fire id IS the session id), and this family is swept by its OWN
// age pass (sweepScheduleFires, ScheduleFireRetention) — NOT the main pass and
// NOT the child pass. The prefix is a composition-owned constant (the fire path
// mints it in newFireID), not an engine-exported one, so it lives here rather
// than in engine/agent (the engine never mints a sched-- id).
var scheduleFireSessionPrefixes = []string{"sched--"}

// isChildSession reports whether id carries one of the delegation families'
// prefixes, returning the matched prefix (the per-family cap's grouping key).
func isChildSession(id session.SessionID) (family string, ok bool) {
	for _, p := range childSessionPrefixes {
		if strings.HasPrefix(string(id), p) {
			return p, true
		}
	}
	return "", false
}

// isScheduleFireSession reports whether id carries a scheduled-task fire prefix.
func isScheduleFireSession(id session.SessionID) bool {
	for _, p := range scheduleFireSessionPrefixes {
		if strings.HasPrefix(string(id), p) {
			return true
		}
	}
	return false
}

// isMainSession reports whether id is a TOP-LEVEL (operator/service) session —
// i.e. NOT one of the delegation families' child prefixes AND NOT a schedule-
// fire prefix. It is the exact complement of (isChildSession ∪
// isScheduleFireSession), so the child sweep, the schedule-fire sweep, and the
// main sweep partition the inventory with no overlap.
func isMainSession(id session.SessionID) bool {
	if _, ok := isChildSession(id); ok {
		return false
	}
	return !isScheduleFireSession(id)
}

// childGCPolicy is the operator-tunable retention policy.
type childGCPolicy struct {
	// retention is the age threshold for the child age pass: a child snapshot
	// whose ModifiedAt is older than now-retention is deleted. <=0 disables the
	// age pass.
	retention time.Duration
	// maxPerFamily is the per-family count cap: within one prefix family the
	// newest maxPerFamily child snapshots survive, the rest are deleted
	// oldest-first. <=0 disables the cap pass.
	maxPerFamily int
	// mainRetention is the age threshold for the MAIN (top-level) age pass: a
	// main snapshot whose ModifiedAt is older than now-mainRetention is deleted.
	// <=0 disables it (issue #79).
	mainRetention time.Duration
	// mainMaxTotal is the GLOBAL count cap over main sessions: the newest
	// mainMaxTotal main snapshots survive, the rest are deleted oldest-first.
	// <=0 disables it. Unlike maxPerFamily this is a single store-wide cap, not
	// per-prefix (issue #79).
	mainMaxTotal int
	// scheduleFireRetention is the age threshold for the SCHEDULE-FIRE age pass
	// (ADR 0059 decision #7 Phase-2): a "sched--"-prefixed fire-session
	// snapshot whose ModifiedAt is older than now-scheduleFireRetention is
	// deleted. <=0 disables it (fire sessions are never swept). It is a peer of
	// mainRetention, partitioning the top-level sessions by family: a sched--
	// session is swept here, NOT by the main pass.
	scheduleFireRetention time.Duration
	// scheduleFireMaxTotal is the GLOBAL count cap over schedule-fire sessions:
	// the newest scheduleFireMaxTotal "sched--" snapshots survive, the rest are
	// deleted oldest-first. <=0 disables it. It is the peer of mainMaxTotal (ADR
	// 0059 decision #7 Phase-2): the age horizon bounds the tail, but a cron
	// firing every minute at a 7d retention accumulates ~10k sessions the horizon
	// never trims from the HEAD, so the count cap is the symmetric bound.
	scheduleFireMaxTotal int
}

// enabled reports whether any pass is active (the all-zero policy is the
// fully-disabled posture).
func (p childGCPolicy) enabled() bool {
	return p.retention > 0 || p.maxPerFamily > 0 || p.mainRetention > 0 || p.mainMaxTotal > 0 ||
		p.scheduleFireRetention > 0 || p.scheduleFireMaxTotal > 0
}

// childGC sweeps child-session snapshots out of a prunable store per the
// policy. Everything is injected (store, clock, liveness, diagnostics) so the
// sweep is deterministic under test.
type childGC struct {
	store         port.PrunableStore
	pager         port.SessionMetadataPager
	deleteSession func(context.Context, session.SessionID) error
	policy        childGCPolicy
	// isLive reports whether a session id has an in-flight run in this
	// process (Service.IsLive). A live id is never deleted, by EITHER pass.
	//
	// INVARIANT — the live-skip does NOT protect engine-spawned children.
	// Service.IsLive reads the TOP-LEVEL run registry only; a mid-run
	// subagent/parallel/team child is driven inside its parent's run and is
	// never registered there, so IsLive("subagent-…") answers false even
	// while that child is executing (pinned by
	// TestServiceIsLiveDoesNotKnowEngineChildren in internal/adapter/server).
	// Engine children are instead protected by AGE HORIZON + SNAPSHOT
	// FRESHNESS: every child persists at its terminal, and a RESUMED child
	// re-persists at resume start (engine/agent/subagent.go), so an
	// in-flight child's snapshot is always younger than any sane retention.
	// The seam stays because it IS protective for API-client-driven sessions
	// that carry a child prefix (StartRun on such an id registers it here).
	isLive func(session.SessionID) bool
	now    func() time.Time
	diag   port.Diagnostics
	// disabled is the sticky kill switch: set when the store signals
	// port.ErrPruneUnsupported (the backend can NEVER enumerate/delete), after
	// which every subsequent sweep is a no-op and the ticker goroutine exits.
	// Only the sweep goroutine (or a single-goroutine test) touches it.
	disabled bool
}

// Ask the store for one complete retention snapshot. Today's v1 pagers form a
// bounded response by scanning their full backend, so a small page size would
// repeat that scan once per page (the interactive picker is fixed separately by
// the indexed-pagination work). The int32 ceiling is also accepted by the remote
// driver protocol; a backend that imposes a smaller page still advances by cursor.
const retentionMetadataPageSize = 1<<31 - 1

// retentionMetadata reads the durable taxonomy used by automatic retention.
// PrunableStore.List intentionally carries only id+mtime, which is insufficient
// to distinguish an explicit main session from an unclassified legacy record.
// A paging failure therefore aborts the sweep rather than falling back to ID
// prefix inference.
func (g *childGC) retentionMetadata(ctx context.Context) ([]port.SessionDiscoveryMeta, error) {
	if g.pager == nil {
		return nil, port.ErrSessionMetadataPagingUnsupported
	}
	var (
		out    []port.SessionDiscoveryMeta
		cursor *port.SessionMetadataCursor
	)
	for {
		page, err := g.pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{
			Limit: retentionMetadataPageSize, Cursor: cursor,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Sessions...)
		if page.NextCursor == nil {
			return out, nil
		}
		if len(page.Sessions) == 0 || cursor != nil &&
			page.NextCursor.ModifiedAt.Equal(cursor.ModifiedAt) && page.NextCursor.ID == cursor.ID {
			return nil, errors.New("session GC: metadata pager made no progress")
		}
		next := *page.NextCursor
		cursor = &next
	}
}

type retentionClass uint8

const (
	retentionProtectedUnknown retentionClass = iota
	retentionProtectedState
	retentionMain
	retentionScheduled
	retentionSubagent
	retentionParallel
	retentionTeam
)

// classifyRetention requires positive, valid durable metadata. In particular,
// an unprefixed id is not evidence that a legacy record is a main chat.
func classifyRetention(meta port.SessionDiscoveryMeta) retentionClass {
	if err := session.ValidateSessionMetadata(meta.Kind, meta.Relationship); err != nil {
		return retentionProtectedUnknown
	}
	switch meta.State {
	case session.StateRunning, session.StateAwaiting:
		return retentionProtectedState
	case session.StateIdle, session.StateCompleted, session.StateFailed, session.StateCancelled:
		// These are terminal/idle candidates; family classification follows below.
	default:
		return retentionProtectedUnknown
	}
	switch meta.Kind {
	case session.SessionKindMain:
		// Reserved delegation/fire prefixes contradict a main classification. Fail
		// closed instead of granting main-retention semantics to malformed metadata.
		if !isMainSession(meta.ID) {
			return retentionProtectedUnknown
		}
		return retentionMain
	case session.SessionKindScheduled:
		return retentionScheduled
	case session.SessionKindSubagent:
		return retentionSubagent
	case session.SessionKindParallelBranch:
		return retentionParallel
	case session.SessionKindTeamMember:
		return retentionTeam
	default:
		return retentionProtectedUnknown
	}
}

// sweep runs one age pass then one per-family cap pass and returns the
// (deleted, retained) child counts. It is BEST-EFFORT throughout: a metadata
// listing failure aborts the sweep with one WARN; Delete failures are tallied
// into one WARN per sweep (the entry stays retained and the next sweep retries);
// nothing is ever fatal. It logs ONE INFO summary when anything was deleted
// and stays silent otherwise (a found-nothing sweep is the steady state).
// A store that signals port.ErrPruneUnsupported (the backend can NEVER
// enumerate — e.g. a remote driver answering UNIMPLEMENTED) gets ONE INFO and
// stickily disables all further sweeping — never a recurring WARN.
func (g *childGC) sweep(ctx context.Context) (deleted, retained int) {
	if g.disabled {
		return 0, 0
	}
	entries, err := g.retentionMetadata(ctx)
	if err != nil {
		if errors.Is(err, port.ErrPruneUnsupported) || errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
			g.disabled = true
			g.diag.Log(ctx, port.LevelInfo, "session GC: store does not support durable retention metadata; disabling session GC", "err", err)
			return 0, 0
		}
		g.diag.Log(ctx, port.LevelWarn, "session GC: metadata list failed; skipping sweep", "err", err)
		return 0, 0
	}

	byFamily := map[string][]port.StoredSession{
		agent.SubagentSessionPrefix: {},
		agent.ParallelSessionPrefix: {},
		agent.TeamSessionPrefix:     {},
	}
	var mains, fires []port.StoredSession
	var protectedUnknown, protectedState int
	for _, meta := range entries {
		e := port.StoredSession{ID: meta.ID, ModifiedAt: meta.ModifiedAt}
		switch classifyRetention(meta) {
		case retentionMain:
			mains = append(mains, e)
		case retentionScheduled:
			fires = append(fires, e)
		case retentionSubagent:
			byFamily[agent.SubagentSessionPrefix] = append(byFamily[agent.SubagentSessionPrefix], e)
		case retentionParallel:
			byFamily[agent.ParallelSessionPrefix] = append(byFamily[agent.ParallelSessionPrefix], e)
		case retentionTeam:
			byFamily[agent.TeamSessionPrefix] = append(byFamily[agent.TeamSessionPrefix], e)
		case retentionProtectedState:
			protectedState++
		default:
			protectedUnknown++
		}
	}

	var errs deleteErrors
	for _, kids := range byFamily {
		deleted += g.sweepPartition(ctx, kids, g.policy.retention, g.policy.maxPerFamily, &errs)
		retained += len(kids)
	}

	fireDeleted := g.sweepPartition(ctx, fires, g.policy.scheduleFireRetention, g.policy.scheduleFireMaxTotal, &errs)
	deleted += fireDeleted
	retained += len(fires)

	mainDeleted := g.sweepPartition(ctx, mains, g.policy.mainRetention, g.policy.mainMaxTotal, &errs)
	deleted += mainDeleted
	retained += len(mains)
	retained += protectedUnknown + protectedState
	retained -= deleted

	if errs.count > 0 {
		g.diag.Log(ctx, port.LevelWarn, "session GC: some deletes failed (retained; retried next sweep)",
			"failed", errs.count, "first_err", errs.first)
	}
	if deleted > 0 {
		g.diag.Log(ctx, port.LevelInfo, "session GC: swept",
			"deleted", deleted, "retained", retained,
			"protected_unknown", protectedUnknown, "protected_state", protectedState)
	}
	return deleted, retained
}

// deleteErrors tallies a sweep's failed deletes into ONE WARN (first error
// kept as the sample).
type deleteErrors struct {
	count int
	first error
}

// remove best-effort-deletes one id, tallying a failure into errs.
func (g *childGC) remove(ctx context.Context, id session.SessionID, errs *deleteErrors) bool {
	deleteSession := g.store.Delete
	if g.deleteSession != nil {
		deleteSession = g.deleteSession
	}
	if err := deleteSession(ctx, id); err != nil {
		errs.count++
		if errs.first == nil {
			errs.first = err
		}
		return false
	}
	return true
}

// sweepPartition runs the age pass then a count-cap pass over ONE partition of
// the inventory (a child family, the mains, or the schedule fires) and returns
// how many it deleted. It is the SINGLE retention algorithm the three partitions
// share (issue #38 / #79 / ADR 0059 Phase-2): they differ ONLY in the two scalar
// knobs, so the sweep logic lives here and each call site passes its own
// retention + cap:
//
//   - sort oldest-first (ModifiedAt, then ID tiebreak) — store List order is
//     unspecified (map iteration), and the tiebreak makes cap eviction and age
//     ordering DETERMINISTIC (coarse file mtimes / batch saves collide);
//   - age pass (retention>0): delete entries older than now-retention, skipping
//     LIVE ids, best-effort (a failed delete stays retained, retried next sweep);
//   - cap pass (maxTotal>0): evict the oldest NON-live survivors down to maxTotal
//     (a live id keeps its slot; the next-oldest non-live is evicted in its stead).
//
// Both passes self-disable on a zero knob, so a fully-disabled partition (both
// knobs <=0) returns 0 with no deletes — the historical "never touched" posture.
// The cap is over the partition passed in: for the mains and the fires that is a
// single store-wide set; for a child family it is per-prefix (the caller loops
// per family).
func (g *childGC) sweepPartition(ctx context.Context, entries []port.StoredSession, retention time.Duration, maxTotal int, errs *deleteErrors) (deleted int) {
	slices.SortStableFunc(entries, func(a, b port.StoredSession) int {
		return cmp.Or(a.ModifiedAt.Compare(b.ModifiedAt), cmp.Compare(a.ID, b.ID))
	})

	survivors := entries
	if retention > 0 {
		survivors = entries[:0]
		cutoff := g.now().Add(-retention)
		for _, e := range entries {
			if e.ModifiedAt.Before(cutoff) && !g.isLive(e.ID) && g.remove(ctx, e.ID, errs) {
				deleted++
				continue
			}
			survivors = append(survivors, e)
		}
	}

	if maxTotal <= 0 || len(survivors) <= maxTotal {
		return deleted
	}
	over := len(survivors) - maxTotal
	for _, e := range survivors {
		if over == 0 {
			break
		}
		if g.isLive(e.ID) {
			// A live id keeps its slot; the next-oldest NON-live id is deleted
			// in its stead (the loop keeps scanning).
			continue
		}
		if g.remove(ctx, e.ID, errs) {
			deleted++
		}
		over--
	}
	return deleted
}

// startChildGC wires the child-session retention sweeper: a no-op (with one
// build-once INFO, the startMemoryConsolidation idiom) when the policy is
// fully disabled or the store is not prunable; otherwise one startup sweep
// plus a ticker every cfg.ChildGCInterval (0 = startup-only), all on one
// background goroutine sharing ctx (so the loop exits on shutdown). The
// liveness predicate is the Service's in-flight run registry, threaded in by
// Build AFTER the Service exists.
func startChildGC(ctx context.Context, cfg Config, store port.SessionStore, isLive func(session.SessionID) bool, deleters ...func(context.Context, session.SessionID) error) {
	// The sweeper has no caller: it runs as the explicit system principal
	// (ADR 0204 decision 7), never an absent one.
	ctx = syscaller.Context(ctx, syscaller.RootChildGC)
	policy := childGCPolicy{
		retention:             cfg.ChildRetention,
		maxPerFamily:          cfg.ChildRetentionMaxPerFamily,
		mainRetention:         cfg.MainRetention,
		mainMaxTotal:          cfg.MainRetentionMaxTotal,
		scheduleFireRetention: cfg.ScheduleFireRetention,
		scheduleFireMaxTotal:  cfg.ScheduleFireRetentionMaxTotal,
	}
	if !policy.enabled() {
		cfg.diag().Log(ctx, port.LevelInfo, "session GC DISABLED (no child retention/cap and no main retention/cap)")
		return
	}
	prunable, ok := store.(port.PrunableStore)
	if !ok {
		cfg.diag().Log(ctx, port.LevelInfo, "session GC unavailable (session store is not prunable); store is never swept")
		return
	}
	pager, ok := store.(port.SessionMetadataPager)
	if !ok {
		cfg.diag().Log(ctx, port.LevelInfo, "session GC unavailable (session store has no durable metadata pager); store is never swept")
		return
	}
	var deleteSession func(context.Context, session.SessionID) error
	if len(deleters) > 0 {
		deleteSession = deleters[0]
	}
	gc := &childGC{
		store:         prunable,
		pager:         pager,
		deleteSession: deleteSession,
		policy:        policy,
		isLive:        isLive,
		now:           time.Now,
		diag:          cfg.diag(),
	}
	cfg.diag().Log(ctx, port.LevelInfo, "session GC ENABLED",
		"child_retention", cfg.ChildRetention, "child_max_per_family", cfg.ChildRetentionMaxPerFamily,
		"main_retention", cfg.MainRetention, "main_max_total", cfg.MainRetentionMaxTotal,
		"schedule_fire_retention", cfg.ScheduleFireRetention,
		"schedule_fire_max_total", cfg.ScheduleFireRetentionMaxTotal,
		"interval", cfg.ChildGCInterval)
	go func() {
		gc.sweep(ctx)
		if cfg.ChildGCInterval <= 0 || gc.disabled {
			return // startup-only, or the store can never be swept
		}
		ticker := time.NewTicker(cfg.ChildGCInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				gc.sweep(ctx)
				if gc.disabled {
					return // sticky: the store signalled ErrPruneUnsupported
				}
			}
		}
	}()
}
