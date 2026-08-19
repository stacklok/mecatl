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
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/sessionretention"
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
	store           port.PrunableStore
	pager           port.SessionMetadataPager
	deleteCandidate func(context.Context, port.SessionDiscoveryMeta) error
	policy          childGCPolicy
	// isLive reports whether a top-level or engine-owned child session is active
	// in this process (Service.IsLive). A live id is never planned by age or cap,
	// and deletion rechecks the same predicate under the run-entry lock.
	isLive func(session.SessionID) bool
	now    func() time.Time
	diag   port.Diagnostics
	// disabled is the sticky kill switch: set when the store signals
	// port.ErrPruneUnsupported (the backend can NEVER enumerate/delete), after
	// which every subsequent sweep is a no-op and the ticker goroutine exits.
	// Only the sweep goroutine (or a single-goroutine test) touches it.
	disabled bool
	// failed records whether the most recent sweep hit a transient list/delete
	// failure, for Build-owned health projection. It is worker-confined.
	failed bool
	// unavailable is set when the mandatory maintenance-exclusion seam becomes
	// runtime-unsupported. The worker settles health unavailable and exits.
	unavailable bool
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if g.pager == nil {
		return nil, port.ErrSessionMetadataPagingUnsupported
	}
	var (
		out    []port.SessionDiscoveryMeta
		cursor *port.SessionMetadataCursor
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
	g.failed = false
	if g.disabled {
		return 0, 0
	}
	entries, err := g.retentionMetadata(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, 0
		}
		if errors.Is(err, port.ErrPruneUnsupported) || errors.Is(err, port.ErrSessionMetadataPagingUnsupported) {
			g.disabled = true
			g.diag.Log(ctx, port.LevelInfo, "session GC: store does not support durable retention metadata; disabling session GC", "err", err)
			return 0, 0
		}
		g.failed = true
		g.diag.Log(ctx, port.LevelWarn, "session GC: metadata list failed; skipping sweep", "err", err)
		return 0, 0
	}

	live := make(map[session.SessionID]bool)
	for _, meta := range entries {
		if g.isLive(meta.ID) {
			live[meta.ID] = true
		}
	}
	plan := sessionretention.Plan(entries, sessionretention.Policy{
		MainMaxAge: g.policy.mainRetention, MainMaxCount: g.policy.mainMaxTotal,
		ChildMaxAge: g.policy.retention, ChildMaxCount: g.policy.maxPerFamily,
		ScheduledMaxAge: g.policy.scheduleFireRetention, ScheduledMaxCount: g.policy.scheduleFireMaxTotal,
	}, sessionretention.Scope{}, sessionretention.RuntimeProtection{Live: live}, g.now())

	var errs deleteErrors
	for _, item := range plan.Eligible {
		if ctx.Err() != nil {
			break
		}
		if g.remove(ctx, item.Metadata, &errs) {
			deleted++
		}
		if g.unavailable {
			break
		}
	}
	retained = len(entries) - deleted

	if errs.count > 0 {
		g.failed = true
		g.diag.Log(ctx, port.LevelWarn, "session GC: some deletes failed (retained; retried next sweep)",
			"failed", errs.count, "first_err", errs.first)
	}
	if deleted > 0 {
		g.diag.Log(ctx, port.LevelInfo, "session GC: swept",
			"deleted", deleted, "retained", retained,
			"protected_unknown", plan.Protected.ByReason[sessionretention.ProtectedUnknown],
			"protected_state", plan.Protected.ByReason[sessionretention.ProtectedState])
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
func (g *childGC) remove(ctx context.Context, candidate port.SessionDiscoveryMeta, errs *deleteErrors) bool {
	deleteSession := func(ctx context.Context, candidate port.SessionDiscoveryMeta) error {
		return g.store.Delete(ctx, candidate.ID)
	}
	if g.deleteCandidate != nil {
		deleteSession = g.deleteCandidate
	}
	if err := deleteSession(ctx, candidate); err != nil {
		if errors.Is(err, server.ErrMaintenanceExclusionUnavailable) {
			g.unavailable = true
			return false
		}
		errs.count++
		if errs.first == nil {
			errs.first = err
		}
		return false
	}
	return true
}

// startChildGC wires the child-session retention sweeper: a no-op cleanup (with
// one build-once INFO, the startMemoryConsolidation idiom) when the policy is
// fully disabled or the store is not prunable; otherwise one startup sweep plus
// a ticker every cfg.ChildGCInterval (0 = startup-only), all on one owned
// background goroutine. The returned idempotent cleanup cancels the worker and
// joins it; Build runs that cleanup before closing Service or the store, so no
// sweep can outlive its dependencies.
func startChildGC(parent context.Context, cfg Config, store port.SessionStore, isLive func(session.SessionID) bool, deleters ...func(context.Context, port.SessionDiscoveryMeta) error) func() {
	noop := func() {}
	// The sweeper has no caller: it runs as the explicit system principal
	// (ADR 0204 decision 7), never an absent one.
	parent = syscaller.Context(parent, syscaller.RootChildGC)
	policy := childGCPolicy{
		retention:             cfg.ChildRetention,
		maxPerFamily:          cfg.ChildRetentionMaxPerFamily,
		mainRetention:         cfg.MainRetention,
		mainMaxTotal:          cfg.MainRetentionMaxTotal,
		scheduleFireRetention: cfg.ScheduleFireRetention,
		scheduleFireMaxTotal:  cfg.ScheduleFireRetentionMaxTotal,
	}
	if !policy.enabled() {
		cfg.diag().Log(parent, port.LevelInfo, "session GC DISABLED (no child retention/cap and no main retention/cap)")
		return noop
	}
	if cfg.maintenanceMutationAvailable != nil && !cfg.maintenanceMutationAvailable() {
		cfg.storageMaintenance.disableSweep()
		cfg.diag().Log(parent, port.LevelInfo, "session GC unavailable (maintenance exclusion is unavailable); store is never swept")
		return noop
	}
	prunable, ok := store.(port.PrunableStore)
	if !ok {
		cfg.diag().Log(parent, port.LevelInfo, "session GC unavailable (session store is not prunable); store is never swept")
		return noop
	}
	pager, ok := store.(port.SessionMetadataPager)
	if !ok {
		cfg.diag().Log(parent, port.LevelInfo, "session GC unavailable (session store has no durable metadata pager); store is never swept")
		return noop
	}
	if _, ok := store.(port.ConditionalPrunableStore); !ok && len(deleters) > 0 {
		cfg.diag().Log(parent, port.LevelInfo, "session GC unavailable (session store has no atomic conditional delete); store is never swept")
		return noop
	}
	var deleteCandidate func(context.Context, port.SessionDiscoveryMeta) error
	if len(deleters) > 0 {
		deleteCandidate = deleters[0]
	}
	gc := &childGC{
		store:           prunable,
		pager:           pager,
		deleteCandidate: deleteCandidate,
		policy:          policy,
		isLive:          isLive,
		now:             time.Now,
		diag:            cfg.diag(),
	}
	cfg.diag().Log(parent, port.LevelInfo, "session GC ENABLED",
		"child_retention", cfg.ChildRetention, "child_max_per_family", cfg.ChildRetentionMaxPerFamily,
		"main_retention", cfg.MainRetention, "main_max_total", cfg.MainRetentionMaxTotal,
		"schedule_fire_retention", cfg.ScheduleFireRetention,
		"schedule_fire_max_total", cfg.ScheduleFireRetentionMaxTotal,
		"interval", cfg.ChildGCInterval)
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runChildGCWorker(ctx, cfg, gc)
	}()
	return sync.OnceFunc(func() {
		cancel()
		wg.Wait()
	})
}

func runChildGCWorker(ctx context.Context, cfg Config, gc *childGC) {
	defer cfg.storageMaintenance.stopSweepSchedule()
	runSweep := func() {
		if cfg.maintenanceMutationAvailable != nil && !cfg.maintenanceMutationAvailable() {
			gc.unavailable = true
			cfg.storageMaintenance.disableSweep()
			return
		}
		cfg.storageMaintenance.beginSweep()
		defer func() {
			if ctx.Err() != nil {
				cfg.storageMaintenance.stopSweepSchedule()
				return
			}
			if gc.disabled || gc.unavailable {
				cfg.storageMaintenance.disableSweep()
				return
			}
			if gc.failed {
				cfg.storageMaintenance.failSweep(time.Now(), cfg.ChildGCInterval)
				return
			}
			cfg.storageMaintenance.finishSweep(time.Now(), cfg.ChildGCInterval)
		}()
		gc.sweep(ctx)
	}
	runSweep()
	if cfg.ChildGCInterval <= 0 || gc.disabled || gc.unavailable || ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(cfg.ChildGCInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runSweep()
			if gc.disabled || gc.unavailable {
				return
			}
		}
	}
}
