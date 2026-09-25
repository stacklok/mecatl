package app

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	mcpsource "github.com/stacklok/mecatl/internal/adapter/mcp/source"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	maxMCPReconcileSources       = 8
	maxMCPReconcileServers       = 128
	maxMCPActiveListEntries      = 10_000
	maxMCPCandidatePages         = 256
	maxMCPCandidateBytes         = 16 << 20
	maxMCPRetainedRuntimes       = 4
	maxMCPDiagnosticBytes        = 512
	maxMCPReconcileCycleDuration = 45 * time.Second
	minMCPReconcileCooldown      = time.Second
	minMCPPollInterval           = 25 * time.Second
	maxMCPPollInterval           = 35 * time.Second
)

var errMCPReconcilerClosed = errors.New("MCP source reconciler is closed")

type mcpToolMeta struct {
	Name        string
	Description string
	Schema      string
	ReadOnly    bool
}

type mcpReconcileCandidate struct {
	manager    *mcp.Manager
	configs    []mcp.ServerConfig
	inventory  []mcpsource.SourceInfo
	tools      []mcpToolMeta
	resources  []mcp.Resource
	prompts    []mcp.Prompt
	generation uint64
	pages      int
	bytes      int
	closeOnce  sync.Once
	closed     atomic.Bool
}

func (c *mcpReconcileCandidate) close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.manager != nil {
			_ = c.manager.Close()
		}
	})
}

func (c *mcpReconcileCandidate) isClosed() bool {
	return c != nil && c.closed.Load()
}

type mcpReconcileResult struct {
	candidate   *mcpReconcileCandidate
	inventory   []mcpsource.SourceInfo
	diagnostics []string
	stale       bool
	changed     bool
}

type mcpCandidateBuilder func(context.Context, []mcp.ServerConfig, func()) (*mcpReconcileCandidate, error)

type mcpReconcilerOptions struct {
	sources  []mcpsource.Source
	toolHive bool
	build    mcpCandidateBuilder
	publish  func(old, candidate *mcpReconcileCandidate) bool
	after    func(time.Duration) <-chan time.Time
}

type mcpSourceSnapshot struct {
	configs []mcp.ServerConfig
	info    mcpsource.SourceInfo
}

type mcpReconcileReply struct {
	result mcpReconcileResult
	err    error
}

type mcpSourceReconciler struct {
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup

	sources  []mcpsource.Source
	toolHive bool
	build    mcpCandidateBuilder
	publish  func(old, candidate *mcpReconcileCandidate) bool
	after    func(time.Duration) <-chan time.Time
	trigger  chan struct{}

	mu          sync.Mutex
	waiters     []chan mcpReconcileReply
	lkg         map[string]mcpSourceSnapshot
	current     *mcpReconcileCandidate
	ownsCurrent bool
	status      serveradapter.MCPSourceStatus
	closed      bool

	cyclesDone     atomic.Int64
	nextGen        atomic.Uint64
	preparingGen   atomic.Uint64
	candidateDirty atomic.Bool
}

func newMCPSourceReconciler(opts mcpReconcilerOptions) *mcpSourceReconciler {
	ctx, cancel := context.WithCancel(context.Background())
	if opts.after == nil {
		opts.after = time.After
	}
	r := &mcpSourceReconciler{
		ctx: ctx, cancel: cancel, sources: append([]mcpsource.Source(nil), opts.sources...),
		toolHive: opts.toolHive, build: opts.build, publish: opts.publish, after: opts.after,
		trigger: make(chan struct{}, 1), lkg: make(map[string]mcpSourceSnapshot),
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

func (r *mcpSourceReconciler) polling() bool { return r.toolHive }
func (r *mcpSourceReconciler) cycles() int64 { return r.cyclesDone.Load() }

func (r *mcpSourceReconciler) statusSnapshot() serveradapter.MCPSourceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.status
	status.Sources = cloneMCPInventory(status.Sources)
	return status
}

func (r *mcpSourceReconciler) Reconcile(ctx context.Context) (mcpReconcileResult, error) {
	reply := make(chan mcpReconcileReply, 1)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return mcpReconcileResult{}, errMCPReconcilerClosed
	}
	r.waiters = append(r.waiters, reply)
	r.mu.Unlock()
	r.invalidate()
	select {
	case got := <-reply:
		return got.result, got.err
	case <-ctx.Done():
		return mcpReconcileResult{}, ctx.Err()
	}
}

func (r *mcpSourceReconciler) invalidate() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *mcpSourceReconciler) loop() {
	defer r.wg.Done()
	var poll, cooldown <-chan time.Time
	if r.toolHive {
		poll = r.after(mcpPollDelay())
	}
	for {
		select {
		case <-r.ctx.Done():
			replyMCPWaiters(r.takeWaiters(), mcpReconcileResult{stale: true}, r.ctx.Err())
			return
		case <-poll:
			r.invalidate()
			poll = r.after(mcpPollDelay())
		case <-r.trigger:
			// A continuously dirty server must not reconnect the entire candidate
			// set in a tight loop. Requests during the cooldown share one cycle.
			if cooldown != nil {
				select {
				case <-cooldown:
				case <-r.ctx.Done():
					replyMCPWaiters(r.takeWaiters(), mcpReconcileResult{stale: true}, r.ctx.Err())
					return
				}
			}
			waiters := r.takeWaiters()
			r.mu.Lock()
			r.status.Reconciling = true
			r.mu.Unlock()
			result, err := r.cycle()
			r.publishStatus(result)
			r.cyclesDone.Add(1)
			cooldown = r.after(minMCPReconcileCooldown)
			replyMCPWaiters(waiters, result, err)
		}
	}
}

func (r *mcpSourceReconciler) publishStatus(result mcpReconcileResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Sources = nil
	r.status.Revision = 0
	if r.current != nil {
		r.status.Sources = publishedMCPInventory(r.current.inventory, result.inventory)
		r.status.Revision = r.current.generation
	}
	r.status.Stale = result.stale
	r.status.Reconciling = false
}

func mcpPollDelay() time.Duration {
	span := maxMCPPollInterval - minMCPPollInterval
	if span <= 0 {
		return minMCPPollInterval
	}
	var seed [4]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return minMCPPollInterval + span/2
	}
	jitterMillis := int64(binary.LittleEndian.Uint32(seed[:4])) % span.Milliseconds()
	return minMCPPollInterval + time.Duration(jitterMillis)*time.Millisecond
}

func (r *mcpSourceReconciler) takeWaiters() []chan mcpReconcileReply {
	r.mu.Lock()
	defer r.mu.Unlock()
	waiters := r.waiters
	r.waiters = nil
	return waiters
}

func replyMCPWaiters(waiters []chan mcpReconcileReply, result mcpReconcileResult, err error) {
	for _, waiter := range waiters {
		waiter <- mcpReconcileReply{result: result, err: err}
	}
}

func (r *mcpSourceReconciler) cycle() (mcpReconcileResult, error) {
	cycleCtx, cancel := context.WithTimeout(r.ctx, maxMCPReconcileCycleDuration)
	defer cancel()
	configs, inventory, diagnostics, stale, err := r.resolve(cycleCtx)
	result := mcpReconcileResult{inventory: inventory, diagnostics: diagnostics, stale: stale}
	if err != nil {
		r.mu.Lock()
		result.candidate = r.current
		r.mu.Unlock()
		return result, err
	}
	if stale {
		r.mu.Lock()
		result.candidate = r.current
		r.mu.Unlock()
		return result, nil
	}

	r.mu.Lock()
	current := r.current
	unchanged := current != nil && equalMCPConfigs(current.configs, configs)
	r.mu.Unlock()
	candidateDirty := r.candidateDirty.Swap(false)
	if unchanged && !stale && !candidateDirty {
		result.candidate = current
		return result, nil
	}
	// Consuming a dirty bit is provisional until a complete candidate is
	// accepted (including an equal snapshot). Failures/deferred publication
	// must remain retryable even when the source configuration is unchanged.
	refreshed := false
	defer func() {
		if !refreshed {
			r.candidateDirty.Store(true)
		}
	}()
	if r.build == nil {
		return result, errors.New("MCP source reconciler has no candidate builder")
	}

	generation := r.nextGen.Add(1)
	r.preparingGen.Store(generation)
	defer r.preparingGen.CompareAndSwap(generation, 0)
	dirty := func() {
		r.mu.Lock()
		active := (r.current != nil && r.current.generation == generation) || r.preparingGen.Load() == generation
		r.mu.Unlock()
		if active {
			r.candidateDirty.Store(true)
			r.invalidate()
		}
	}
	candidate, err := r.build(cycleCtx, configs, dirty)
	if err != nil {
		result.stale = true
		result.diagnostics = appendBoundedDiagnostic(result.diagnostics, err.Error())
		r.mu.Lock()
		result.candidate = r.current
		r.mu.Unlock()
		return result, err
	}
	if candidate == nil {
		return result, errors.New("MCP candidate builder returned nil")
	}
	candidate.generation = generation
	candidate.configs = cloneMCPConfigs(configs)
	candidate.inventory = cloneMCPInventory(inventory)
	if err := validateMCPCandidate(candidate); err != nil {
		candidate.close()
		result.stale = true
		result.diagnostics = appendBoundedDiagnostic(result.diagnostics, err.Error())
		return result, err
	}

	r.mu.Lock()
	old := r.current
	r.mu.Unlock()
	if equalMCPCandidate(old, candidate) {
		refreshed = true
		candidate.close()
		result.candidate = old
		return result, nil
	}
	if r.publish != nil {
		if !r.publish(old, candidate) {
			candidate.close()
			result.candidate = old
			result.stale = true
			result.diagnostics = appendBoundedDiagnostic(result.diagnostics, "MCP runtime publication is pending integration")
			return result, nil
		}
		r.mu.Lock()
		r.current = candidate
		r.ownsCurrent = false
		r.mu.Unlock()
	} else {
		r.mu.Lock()
		r.current = candidate
		r.ownsCurrent = true
		r.mu.Unlock()
		// Without a publication hook, preserve the reconciler's original
		// immediate-close behavior for unit embeddings.
		if old != nil {
			old.close()
		}
	}
	refreshed = true
	result.candidate = candidate
	result.changed = true
	return result, nil
}

func (r *mcpSourceReconciler) resolve(ctx context.Context) ([]mcp.ServerConfig, []mcpsource.SourceInfo, []string, bool, error) {
	if len(r.sources) > maxMCPReconcileSources {
		return nil, nil, nil, true, fmt.Errorf("MCP source count exceeds limit %d", maxMCPReconcileSources)
	}
	var inventory []mcpsource.SourceInfo
	var diagnostics []string
	var all []struct {
		config mcp.ServerConfig
		source string
	}
	stale := false
	for _, src := range r.sources {
		cfgs, skips, err := src.Servers(ctx)
		info := sourceInfo(src)
		for _, skip := range skips {
			msg := skip.Error()
			info.Diagnostics = appendBoundedDiagnostic(info.Diagnostics, msg)
			diagnostics = appendBoundedDiagnostic(diagnostics, msg)
		}
		if err != nil {
			stale = true
			msg := "source consultation failed: " + err.Error()
			info.Diagnostics = appendBoundedDiagnostic(info.Diagnostics, msg)
			diagnostics = appendBoundedDiagnostic(diagnostics, msg)
			if prior, ok := r.lkg[src.Name()]; ok {
				cfgs = cloneMCPConfigs(prior.configs)
				info.Servers = append([]mcpsource.ServerInfo(nil), prior.info.Servers...)
			}
		} else {
			cfgs = cloneMCPConfigs(cfgs)
			info.Servers = serverInfos(cfgs, info.Group)
			r.lkg[src.Name()] = mcpSourceSnapshot{configs: cloneMCPConfigs(cfgs), info: info}
		}
		inventory = append(inventory, info)
		for _, cfg := range cfgs {
			all = append(all, struct {
				config mcp.ServerConfig
				source string
			}{cfg, src.Name()})
		}
	}
	if len(all) > maxMCPReconcileServers {
		return nil, inventory, diagnostics, true, fmt.Errorf("MCP server count exceeds limit %d", maxMCPReconcileServers)
	}
	winner := make(map[string]string, len(all))
	merged := make([]mcp.ServerConfig, 0, len(all))
	for _, item := range all {
		if prior, exists := winner[item.config.Name]; exists {
			diagnostics = appendBoundedDiagnostic(diagnostics, fmt.Sprintf("server %q shadowed by higher-precedence source %q", item.config.Name, prior))
			continue
		}
		winner[item.config.Name] = item.source
		merged = append(merged, item.config)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return merged, inventory, diagnostics, stale, nil
}

func (r *mcpSourceReconciler) Close() {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.cancel()
		r.wg.Wait()
		r.mu.Lock()
		current := r.current
		owned := r.ownsCurrent
		r.current = nil
		r.ownsCurrent = false
		r.mu.Unlock()
		if owned {
			current.close()
		}
	})
}

func sourceInfo(src mcpsource.Source) mcpsource.SourceInfo {
	info := mcpsource.SourceInfo{Name: src.Name(), Enabled: true}
	switch {
	case src.Name() == "static":
		info.Kind = "static"
	case strings.HasPrefix(src.Name(), "toolhive("):
		info.Kind = "toolhive"
		info.Group = strings.TrimSuffix(strings.TrimPrefix(src.Name(), "toolhive("), ")")
	}
	return info
}

func serverInfos(configs []mcp.ServerConfig, group string) []mcpsource.ServerInfo {
	out := make([]mcpsource.ServerInfo, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, mcpsource.ServerInfo{Name: cfg.Name, URL: cfg.URL, Transport: "streamable-http", Group: group})
	}
	return out
}

func cloneMCPConfigs(in []mcp.ServerConfig) []mcp.ServerConfig {
	out := make([]mcp.ServerConfig, len(in))
	for i, cfg := range in {
		out[i] = cfg
		if cfg.Headers != nil {
			out[i].Headers = make(map[string]string, len(cfg.Headers))
			for k, v := range cfg.Headers {
				out[i].Headers[k] = v
			}
		}
		if cfg.OAuth != nil {
			oauth := *cfg.OAuth
			oauth.Presenter = nil // serving reconciliation never launches consent
			out[i].OAuth = &oauth
		}
	}
	return out
}

func withArtifactResults(in []mcp.ServerConfig, enabled bool) []mcp.ServerConfig {
	out := cloneMCPConfigs(in)
	for i := range out {
		out[i].ArtifactResults = enabled
	}
	return out
}

func equalMCPConfigs(a, b []mcp.ServerConfig) bool {
	return reflect.DeepEqual(comparableMCPConfigs(a), comparableMCPConfigs(b))
}

func comparableMCPConfigs(in []mcp.ServerConfig) []mcp.ServerConfig {
	out := cloneMCPConfigs(in)
	for i := range out {
		out[i].ListChanged = nil
		out[i].CandidateBudget = nil
		out[i].TokenSource = nil
		out[i].HTTPClient = nil
		if out[i].OAuth != nil {
			oauth := *out[i].OAuth
			oauth.Presenter = nil
			oauth.CredentialStore = nil
			oauth.CredentialReader = nil
			out[i].OAuth = &oauth
		}
	}
	return out
}

func cloneMCPInventory(in []mcpsource.SourceInfo) []mcpsource.SourceInfo {
	out := make([]mcpsource.SourceInfo, len(in))
	for i, info := range in {
		out[i] = info
		out[i].Servers = append([]mcpsource.ServerInfo(nil), info.Servers...)
		out[i].Diagnostics = append([]string(nil), info.Diagnostics...)
	}
	return out
}

func publishedMCPInventory(published, observed []mcpsource.SourceInfo) []mcpsource.SourceInfo {
	inventory := cloneMCPInventory(published)
	for i := range inventory {
		for _, observation := range observed {
			if inventory[i].Name == observation.Name {
				inventory[i].Diagnostics = append([]string(nil), observation.Diagnostics...)
				break
			}
		}
	}
	return inventory
}

func appendBoundedDiagnostic(dst []string, raw string) []string {
	safe := mcp.RedactText(strings.Join(strings.Fields(raw), " "))
	if len(safe) > maxMCPDiagnosticBytes {
		safe = safe[:maxMCPDiagnosticBytes]
		for !utf8.ValidString(safe) {
			safe = safe[:len(safe)-1]
		}
	}
	return append(dst, safe)
}

func validateMCPCandidate(c *mcpReconcileCandidate) error {
	entries := len(c.tools) + len(c.resources) + len(c.prompts)
	if entries > maxMCPActiveListEntries {
		return fmt.Errorf("MCP active list entries exceed limit %d", maxMCPActiveListEntries)
	}
	if c.pages > maxMCPCandidatePages {
		return fmt.Errorf("MCP candidate pages exceed limit %d", maxMCPCandidatePages)
	}
	if c.bytes > maxMCPCandidateBytes {
		return fmt.Errorf("MCP candidate bytes exceed limit %d", maxMCPCandidateBytes)
	}
	return nil
}

func equalMCPCandidate(a, b *mcpReconcileCandidate) bool {
	if a == nil || b == nil {
		return a == b
	}
	return equalMCPConfigs(a.configs, b.configs) && reflect.DeepEqual(a.tools, b.tools) && reflect.DeepEqual(a.resources, b.resources) && reflect.DeepEqual(a.prompts, b.prompts) && reflect.DeepEqual(a.inventory, b.inventory)
}

func toolMetadata(tools []tool.Tool) []mcpToolMeta {
	out := make([]mcpToolMeta, 0, len(tools))
	for _, candidate := range tools {
		spec := candidate.Spec()
		out = append(out, mcpToolMeta{Name: spec.Name, Description: spec.Description, Schema: string(spec.Schema), ReadOnly: candidate.ReadOnly()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func buildMCPReconcileCandidate(diagCfg Config) mcpCandidateBuilder {
	return func(ctx context.Context, configs []mcp.ServerConfig, changed func()) (*mcpReconcileCandidate, error) {
		bounded := cloneMCPConfigs(configs)
		budget := mcp.NewCandidateListBudget(maxMCPActiveListEntries, maxMCPCandidatePages, maxMCPCandidateBytes)
		defer budget.Seal()
		for i := range bounded {
			bounded[i].ListChanged = changed
			bounded[i].CandidateBudget = budget
			bounded[i].ArtifactResults = diagCfg.artifacts != nil
		}
		mgr, err := mcp.NewCompleteManager(ctx, bounded, diagCfg.diag())
		if err != nil {
			return nil, err
		}
		candidate := &mcpReconcileCandidate{manager: mgr, configs: cloneMCPConfigs(configs)}
		candidate.tools = toolMetadata(mgr.Tools())
		candidate.resources, err = mgr.ListResources(ctx, "")
		if err != nil {
			candidate.close()
			return nil, err
		}
		candidate.prompts, err = mgr.ListPrompts(ctx, "")
		if err != nil {
			candidate.close()
			return nil, err
		}
		_, candidate.pages, candidate.bytes = budget.Stats()
		return candidate, nil
	}
}
