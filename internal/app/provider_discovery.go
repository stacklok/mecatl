package app

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

const discoveryCooldown = 10 * time.Second

type discoveryReason uint8

const (
	discoveryBootstrap discoveryReason = iota
	discoveryStartup
	discoveryPicker
	discoveryAdmission
)

type discoveryProvider struct {
	attemptID                                        uint64
	startedAt, completedAt, observedAt, nextEligible time.Time
	inFlight                                         bool
	outcome                                          providerStatus
	observations                                     []modelEntry
	eligible                                         bool
}

type discoveryDefaults struct {
	provider, model string
	autoSelected    bool
}

type discoverySnapshot struct {
	providers  map[string]discoveryProvider
	projection server.ModelSnapshot
	defaults   discoveryDefaults
}

func (v *discoverySnapshot) lookup(pid, model string) (modelEntry, bool) {
	if v != nil {
		for _, m := range v.providers[pid].observations {
			if m.ID == model {
				return m, true
			}
		}
	}
	return modelEntry{}, false
}

type discoveryAttempt struct {
	id                 uint64
	deadline           time.Time
	ctx                context.Context
	cancel             context.CancelFunc
	ready              chan struct{}
	returned, terminal bool
}

// tail serializes candidate acceptance, default healing, publication and shutdown.
// mu protects slots and snapshot commits; no network or remint runs under it.
// Requests never acquire tail, so a cancelled waiter cannot be trapped behind a
// local completion. Public reads are lock-free. Lock order: tail, then mu.
type providerDiscovery struct {
	reg       *providerRegistry
	cfg       Config
	diag      port.Diagnostics
	entries   map[string]providerEntry
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	tail      sync.Mutex
	closed    bool
	attempts  map[string]*discoveryAttempt
	view      atomic.Pointer[discoverySnapshot]
	workers   sync.WaitGroup
	closeOnce sync.Once
	now       func() time.Time
}

func newProviderDiscovery(reg *providerRegistry, cfg Config) *providerDiscovery {
	ctx, cancel := context.WithCancel(syscaller.Context(context.Background(), syscaller.RootModelCatalogRefresh))
	d := &providerDiscovery{reg: reg, cfg: cfg, diag: cfg.diag(), ctx: ctx, cancel: cancel,
		entries: make(map[string]providerEntry), attempts: make(map[string]*discoveryAttempt)}
	d.now = cfg.modelDiscoveryNow
	if d.now == nil {
		d.now = time.Now
	}
	view := &discoverySnapshot{providers: make(map[string]discoveryProvider)}
	for _, pid := range reg.Available() {
		entry, _ := reg.Lookup(pid)
		d.entries[pid] = entry
		view.providers[pid] = discoveryProvider{eligible: entry.lister != nil}
	}
	for pid := range reg.unavailableNative {
		view.providers[pid] = discoveryProvider{outcome: providerStatus{State: statusNotEnrolled, Hint: "run `mecatui providers login " + pid + "`"}}
	}
	d.project(view)
	d.view.Store(view)
	return d
}

func (d *providerDiscovery) snapshot() *discoverySnapshot {
	if d == nil {
		return nil
	}
	return d.view.Load()
}

func (d *providerDiscovery) CurrentModelSnapshot() server.ModelSnapshot {
	view := d.snapshot()
	if view == nil {
		return server.ModelSnapshot{}
	}
	out := server.ModelSnapshot{
		Models:         make([]*mecatlv1.ModelInfo, len(view.projection.Models)),
		ProviderStatus: make([]*mecatlv1.ProviderStatus, len(view.projection.ProviderStatus)),
	}
	for i, m := range view.projection.Models {
		out.Models[i] = proto.CloneOf(m)
	}
	for i, s := range view.projection.ProviderStatus {
		out.ProviderStatus[i] = proto.CloneOf(s)
	}
	return out
}

func (d *providerDiscovery) request(ctx context.Context, pid string, reason discoveryReason) (*discoverySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return d.snapshot(), err
	}
	attempt, err := d.begin(pid, reason)
	if err != nil || attempt == nil {
		return d.snapshot(), err
	}
	select {
	case <-ctx.Done():
		return d.snapshot(), ctx.Err()
	case <-d.ctx.Done():
		return d.snapshot(), context.Canceled
	case <-attempt.ready:
		if d.ctx.Err() != nil {
			return d.snapshot(), context.Canceled
		}
		return d.snapshot(), nil
	}
}

func (d *providerDiscovery) begin(pid string, reason discoveryReason) (*discoveryAttempt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, context.Canceled
	}
	view := d.snapshot()
	state, exists := view.providers[pid]
	entry := d.entries[pid]
	if !exists || !state.eligible || (reason == discoveryStartup && entry.nativeEndpoint) {
		return nil, nil
	}
	attempt := d.attempts[pid]
	if attempt != nil {
		if !attempt.terminal {
			return attempt, nil
		}
		if !attempt.returned {
			return nil, nil
		}
	}
	if (reason == discoveryStartup && state.attemptID != 0) || d.now().Before(state.nextEligible) {
		return nil, nil
	}
	budget := liveModelRefreshTimeout
	if reason == discoveryBootstrap {
		if isToolhiveProvider(pid) {
			budget = toolhiveProbeTimeout
		}
		if pid == providerOpenAICodex {
			budget = openAICodexBootstrapTimeout
		}
	}
	started := d.now()
	deadline := time.Now().Add(budget)
	fetchCtx, cancel := context.WithDeadline(d.ctx, deadline)
	attempt = &discoveryAttempt{id: state.attemptID + 1, deadline: deadline, ctx: fetchCtx, cancel: cancel, ready: make(chan struct{})}
	d.attempts[pid] = attempt
	next := *view
	next.providers = maps.Clone(view.providers)
	state.attemptID, state.startedAt, state.inFlight = attempt.id, started, true
	next.providers[pid] = state
	d.view.Store(&next)
	d.workers.Add(1)
	go d.fetch(pid, entry.lister, attempt)
	return attempt, nil
}

func (d *providerDiscovery) fetch(pid string, lister modelLister, attempt *discoveryAttempt) {
	defer d.workers.Done()
	deadlineDone := make(chan struct{})
	stop := context.AfterFunc(attempt.ctx, func() {
		defer close(deadlineDone)
		d.complete(pid, attempt, nil, attempt.ctx.Err())
	})
	models, err := lister.ListModels(attempt.ctx)
	d.mu.Lock()
	attempt.returned = true
	d.mu.Unlock()
	d.complete(pid, attempt, models, err)
	if !stop() {
		<-deadlineDone
	}
	attempt.cancel()
}

func (d *providerDiscovery) complete(pid string, attempt *discoveryAttempt, models []modelEntry, err error) {
	d.tail.Lock()
	d.mu.Lock()
	if d.closed || d.attempts[pid] != attempt || attempt.terminal {
		d.mu.Unlock()
		d.tail.Unlock()
		return
	}
	if !time.Now().Before(attempt.deadline) || attempt.ctx.Err() != nil {
		err = context.DeadlineExceeded
	}
	d.mu.Unlock()
	view := d.snapshot()
	next := &discoverySnapshot{providers: maps.Clone(view.providers)}
	state := next.providers[pid]
	state.inFlight = false
	switch {
	case err != nil:
		state.outcome.State = classifyLiveListError(err)
	case len(models) == 0:
		state.outcome.State = statusEmpty
	default:
		state.outcome.State = statusOK
		state.observations = cloneModelEntries(models)
		state.observedAt = d.now()
	}
	state.outcome.Hint = statusHintFor(d.entries[pid], state.outcome.State)
	next.providers[pid] = state
	var healed *diagFact
	if err == nil && len(models) > 0 {
		healed = d.reg.healDefaultModelCandidate(pid, next)
	}
	d.project(next)
	// Cooldown starts at publication, not lister return or the beginning of the
	// local completion tail. The candidate is reserved while tail is held.
	d.mu.Lock()
	// A request may start another provider while this local tail is reserved.
	// Preserve those attempt fields. Only tails change observations/outcomes,
	// so their public projections are unchanged and need no re-projection here.
	for other, current := range d.snapshot().providers {
		if other != pid {
			next.providers[other] = current
		}
	}
	state.completedAt = d.now()
	state.nextEligible = state.completedAt.Add(discoveryCooldown)
	next.providers[pid] = state
	d.view.Store(next)
	attempt.terminal = true
	close(attempt.ready)
	d.mu.Unlock()
	d.tail.Unlock()
	attempt.cancel()
	// Sink delivery may block; publication and waiter notification must not.
	// Delivery remains on the owned worker, which Close joins.
	if healed != nil {
		d.diag.Log(context.Background(), healed.level, healed.msg, healed.args...)
	}
	if err != nil {
		d.diag.Log(d.ctx, port.LevelDebug, "live model listing failed; retaining last-good metadata or catalog floor", "provider", pid, "state", state.outcome.State)
	}
}

func cloneModelEntries(models []modelEntry) []modelEntry {
	out := slices.Clone(models)
	for i := range out {
		out[i].InputModalities = slices.Clone(models[i].InputModalities)
	}
	return out
}

func (d *providerDiscovery) project(view *discoverySnapshot) {
	view.defaults = discoveryDefaults{provider: d.reg.Default(), model: d.reg.ResolvedDefaultModel(), autoSelected: d.reg.DefaultModelAutoSelected()}
	for _, pid := range d.reg.Available() {
		if pid == providerMock {
			continue
		}
		models := view.providers[pid].observations
		if len(models) == 0 {
			models = providerInventoryFloor(d.reg, pid)
		} else {
			models = mergeCustomProviderFloor(d.reg, pid, models)
		}
		for _, m := range models {
			view.projection.Models = append(view.projection.Models, projectModelEntry(d.reg, d.cfg, view, pid, m))
		}
	}
	sortModelInfos(view.projection.Models)
	view.projection.ProviderStatus = providerStatusProto(d.reg, view)
}

func (d *providerDiscovery) refresh(ctx context.Context, reason discoveryReason) {
	ctx, cancel := context.WithTimeout(ctx, liveModelRefreshTimeout)
	defer cancel()
	var waits sync.WaitGroup
	for pid, entry := range d.entries {
		if entry.lister == nil || (reason == discoveryStartup && entry.nativeEndpoint) {
			continue
		}
		waits.Go(func() { _, _ = d.request(ctx, pid, reason) })
	}
	waits.Wait()
}

func (d *providerDiscovery) start(runSync bool, delay time.Duration) {
	eligible := false
	for _, entry := range d.entries {
		eligible = eligible || entry.lister != nil && !entry.nativeEndpoint
	}
	if !eligible {
		return
	}
	if runSync {
		d.refresh(d.ctx, discoveryStartup)
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.workers.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.workers.Done()
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-d.ctx.Done():
				return
			}
		}
		d.refresh(d.ctx, discoveryStartup)
	}()
}

func (d *providerDiscovery) Close() {
	if d == nil {
		return
	}
	d.closeOnce.Do(func() {
		d.tail.Lock()
		d.mu.Lock()
		d.closed = true
		d.cancel()
		for _, attempt := range d.attempts {
			attempt.cancel()
			if !attempt.terminal {
				attempt.terminal = true
				close(attempt.ready)
			}
		}
		d.mu.Unlock()
		d.tail.Unlock()
		d.workers.Wait()
	})
}

type windowSource string

const (
	windowGlobal   windowSource = "global_override"
	windowConfig   windowSource = "exact_config"
	windowLive     windowSource = "live"
	windowCatalog  windowSource = "catalog"
	windowFallback windowSource = "policy_fallback"
	windowUnknown  windowSource = "unknown"
)

type windowResolution struct {
	tokens     int
	source     windowSource
	admissible bool
}

func resolveModelWindow(cfg Config, view *discoverySnapshot, pid, model string) windowResolution {
	if cfg.ContextWindowOverride > 0 {
		return windowResolution{cfg.ContextWindowOverride, windowGlobal, true}
	}
	if n := cfg.contextWindows[pid][model]; n > 0 {
		return windowResolution{n, windowConfig, true}
	}
	if m, ok := view.lookup(pid, model); ok && m.ContextLimit > 0 {
		return windowResolution{clampLive(m.ContextLimit, maxLiveContextLimit), windowLive, true}
	}
	if n := catalogContextWindow(metadataCatalogProviderID(pid), model); n > 0 {
		return windowResolution{n, windowCatalog, true}
	}
	if view == nil || !view.providers[pid].eligible || view.providers[pid].outcome.State == statusOK {
		return windowResolution{defaultContextWindowTokens, windowFallback, true}
	}
	return windowResolution{0, windowUnknown, false}
}
