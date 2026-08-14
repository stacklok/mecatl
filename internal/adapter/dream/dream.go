// Package dream implements harness pattern 4 (dream / sleep memory
// consolidation): an OPT-IN background service that distills the tiered memory
// built by internal/adapter/memory. It is NOT a tool and NOT part of the agent
// loop — the composition root constructs a Consolidator and either calls
// Consolidate once or drives it on a ticker via RunPeriodically.
//
// # What it does
//
// Consolidate lists the existing memory entries and, if there are enough of
// them, asks a model (any port.LLMProvider) to MERGE near-duplicate entries,
// DROP stale or low-value ones, and tighten phrasing — returning a small set of
// structured operations that the Consolidator then APPLIES to the store via the
// existing MemoryStore.Remember / MemoryStore.Forget seams.
//
// # Conservative by construction (the anti-over-eager-memory contract)
//
// Consolidation must REDUCE noise, never invent facts. Several rules enforce
// this, all checked AFTER the model answers and BEFORE anything is written:
//
//   - No invented keys. The model may only address keys that already exist in
//     the listed input. Any operation naming a key the store did not already
//     hold is dropped. A "merge" writes its combined value under one of the
//     EXISTING input keys (the merge target), never under a brand-new key.
//   - Bounded deletes. At most Config.MaxForgets entries may be forgotten in a
//     single run; once the cap is reached further forgets are ignored. This
//     stops a single bad run from emptying the store.
//   - Prefer keeping. When the plan is empty, unparseable, or the model errors,
//     the run is a NO-OP: the store is left exactly as it was (fail-safe).
//   - Values are traceable. A merge's new value is whatever the model returns
//     for the merge target; the safety guarantee this package makes is about
//     KEYS (no new keys are ever introduced), which is the simplest rule that is
//     easy to test and reason about. See the package tests.
//
// # Fail-safe
//
// On a model stream error, a context cancellation, or output that cannot be
// parsed into a plan, Consolidate returns without mutating the store. A partial
// apply that hits a store error stops and returns the error, but never
// fabricates data.
//
// # Concurrency
//
// A Consolidator holds only its immutable Config and the injected (concurrency
// -safe) collaborators, so it is safe for concurrent use. The underlying
// MemoryStore serialises its own writes.
//
// The package depends only on the standard library and internal packages
// (tool, port, prompt, session). It introduces no new go.mod dependencies.
package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Default tuning constants, applied by New when the corresponding Config field
// is left at its zero value.
const (
	// defaultMinEntriesToRun is the floor below which Consolidate is a no-op:
	// there is nothing worth distilling in a tiny store.
	defaultMinEntriesToRun = 5
	// defaultMaxEntries caps how many entries are shown to the model per run, to
	// keep the request bounded regardless of store size.
	defaultMaxEntries = 200
	// defaultMaxForgets bounds how many entries a single run may delete, so a
	// single bad plan cannot empty the store.
	defaultMaxForgets = 10
	// defaultTimeout bounds a single consolidation model call.
	defaultTimeout = 60 * time.Second
	// maxValueBytes truncates each entry value placed in the prompt, keeping the
	// request small even when individual values are large.
	maxValueBytes = 2048
)

// Config tunes the Consolidator. The zero value is usable: New fills in the
// defaults above for any field left at zero. Supply explicit values to override.
type Config struct {
	// Model is the provider model identifier used for the consolidation call.
	// Empty leaves LLMRequest.Model empty (the provider's default).
	Model string
	// Prefix scopes which entries are consolidated: only keys with this prefix
	// are listed, shown to the model, and eligible for mutation. Empty means the
	// whole store.
	Prefix string
	// MaxEntries caps how many entries are shown to the model per run. 0 selects
	// defaultMaxEntries.
	MaxEntries int
	// MinEntriesToRun is the floor below which a run is a no-op. 0 selects
	// defaultMinEntriesToRun.
	MinEntriesToRun int
	// MaxForgets bounds how many entries a single run may delete. 0 selects
	// defaultMaxForgets. A negative value disables deletion entirely.
	MaxForgets int
	// Timeout caps a single consolidation model call. 0 selects defaultTimeout.
	// It never overrides a shorter caller deadline.
	Timeout time.Duration
}

// Report summarises one Consolidate run. The counts are over the entries that
// were eligible (within Prefix and the MaxEntries window).
type Report struct {
	// Kept is the number of eligible entries left untouched.
	Kept int
	// Merged is the number of entries that were folded into another entry and
	// then forgotten (the duplicates removed by a merge). The surviving merge
	// target is counted under Kept.
	Merged int
	// Forgotten is the number of entries dropped as stale / low-value (not
	// counting merge duplicates, which are counted under Merged).
	Forgotten int
}

// Consolidator distills a MemoryStore by consulting a model and applying a
// bounded, conservative set of merge/forget operations. Construct one with New.
type Consolidator struct {
	store   tool.MemoryStore
	planner planner
	cfg     Config
}

// planner is the adapter-local seam that turns the current entries into a Plan
// by consulting a model. llmPlanner is the production implementation; tests may
// substitute their own to exercise the apply logic without an LLM.
type planner interface {
	// Plan returns the model's proposed operations for the given entries, or an
	// error if the model could not be consulted or its output not parsed. ctx
	// already carries the per-run timeout.
	Plan(ctx context.Context, entries []tool.MemoryEntry) (Plan, error)
}

// New constructs a Consolidator over store, using llm for the consolidation
// call. Zero-valued Config fields are filled with the package defaults. The
// returned Consolidator is safe for concurrent use.
func New(store tool.MemoryStore, llm port.LLMProvider, cfg Config) *Consolidator {
	cfg = withDefaults(cfg)
	return &Consolidator{
		store:   store,
		planner: &llmPlanner{llm: llm, model: cfg.Model},
		cfg:     cfg,
	}
}

// newWithPlanner is the test seam: identical to New but injects a planner in
// place of the LLM-backed one. It is unexported on purpose.
func newWithPlanner(store tool.MemoryStore, p planner, cfg Config) *Consolidator {
	cfg = withDefaults(cfg)
	return &Consolidator{store: store, planner: p, cfg: cfg}
}

func withDefaults(cfg Config) Config {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = defaultMaxEntries
	}
	if cfg.MinEntriesToRun <= 0 {
		cfg.MinEntriesToRun = defaultMinEntriesToRun
	}
	if cfg.MaxForgets == 0 {
		cfg.MaxForgets = defaultMaxForgets
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return cfg
}

// Consolidate runs one distillation pass:
//
//  1. List the eligible entries (within Config.Prefix, capped at MaxEntries).
//  2. If fewer than Config.MinEntriesToRun, return a no-op Report WITHOUT
//     consulting the model or mutating the store.
//  3. Otherwise consult the model under a bounded timeout for a Plan.
//  4. On any model error, context cancellation, or unparseable output, return
//     WITHOUT mutating the store (fail-safe) — the error is returned for the
//     model/ctx cases, and an empty/unusable plan is simply a no-op.
//  5. Apply the validated plan: forget the merge duplicates and the dropped
//     entries (capped at MaxForgets), rewriting any merge target's value first.
//     Only keys that already existed in the input are ever touched.
//
// It returns a Report of what happened and any harness-level error.
func (c *Consolidator) Consolidate(ctx context.Context) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, fmt.Errorf("dream: %w", err)
	}

	entries, err := c.store.List(ctx, c.cfg.Prefix)
	if err != nil {
		return Report{}, fmt.Errorf("dream: list memory: %w", err)
	}

	if len(entries) < c.cfg.MinEntriesToRun {
		// Nothing worth distilling: no model call, no mutation.
		return Report{Kept: len(entries)}, nil
	}

	if len(entries) > c.cfg.MaxEntries {
		entries = entries[:c.cfg.MaxEntries]
	}

	pctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	plan, err := c.planner.Plan(pctx, entries)
	if err != nil {
		// Fail-safe: model error / cancellation / unparseable output → no mutation.
		return Report{}, fmt.Errorf("dream: plan: %w", err)
	}

	return c.apply(ctx, entries, plan)
}

// apply validates the plan against the known input keys and mutates the store
// conservatively. It never introduces a key absent from entries, and forgets at
// most Config.MaxForgets entries.
//
//nolint:gocyclo // lifecycle/base-only branches keep one visible apply transaction
func (c *Consolidator) apply(ctx context.Context, entries []tool.MemoryEntry, plan Plan) (Report, error) {
	rewrite, fromMerge := c.stagePlan(entries, plan)
	ctx = tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterSystem, Origin: tool.MemoryOriginConsolidation})
	attribution, _ := tool.MemoryAttributionFromContext(ctx)
	for key, value := range rewrite {
		if err := tool.ValidateMemoryContentWrite(key, value, "", attribution); err != nil {
			return Report{}, fmt.Errorf("dream: validate rewrite %q: %w", key, err)
		}
	}

	// Tally the merged vs. dropped split from the staged set.
	var mergedCount, droppedCount int
	for _, viaMerge := range fromMerge {
		if viaMerge {
			mergedCount++
		} else {
			droppedCount++
		}
	}

	// Lifecycle stores use per-key CAS; base-only stores retain the compatible legacy path.
	if lifecycle, ok := c.store.(tool.MemoryLifecycleStore); ok {
		convergence, ok := c.store.(tool.MemoryConvergenceStore)
		if !ok {
			return Report{}, fmt.Errorf("dream: lifecycle store lacks atomic convergence writes")
		}
		expected := map[string]tool.MemoryVersion{}
		original := map[string]tool.MemoryEntry{}
		for _, entry := range entries {
			original[entry.Key] = entry
		}
		for key := range rewrite {
			record, found, err := lifecycle.Inspect(ctx, key)
			base := original[key]
			if err != nil || !found || record.Current.Value != base.Value || record.Current.Description != base.Description {
				if err == nil {
					err = errors.New("memory changed since planning")
				}
				return Report{}, fmt.Errorf("dream: inspect rewrite %q: %w", key, err)
			}
			expected[key] = record.Current.Version
		}
		for key := range fromMerge {
			record, found, err := lifecycle.Inspect(ctx, key)
			base := original[key]
			if err != nil || !found || record.Current.Value != base.Value || record.Current.Description != base.Description {
				if err == nil {
					err = errors.New("memory changed since planning")
				}
				return Report{}, fmt.Errorf("dream: inspect forget %q: %w", key, err)
			}
			expected[key] = record.Current.Version
		}
		for key, val := range rewrite {
			if _, err := convergence.RememberIfCurrent(ctx, tool.MemoryEntry{Key: key, Value: val}, tool.MemoryCurrent{Exists: true, Version: expected[key]}); err != nil {
				return Report{}, fmt.Errorf("dream: rewrite %q: %w", key, err)
			}
		}
		for key := range fromMerge {
			if _, err := lifecycle.ForgetVersioned(ctx, key, expected[key]); err != nil {
				return Report{}, fmt.Errorf("dream: forget %q: %w", key, err)
			}
		}
	} else {
		for key, val := range rewrite {
			if err := c.store.RememberEntry(ctx, tool.MemoryEntry{Key: key, Value: val}); err != nil {
				return Report{}, fmt.Errorf("dream: rewrite %q: %w", key, err)
			}
		}
		for key := range fromMerge {
			if err := c.store.Forget(ctx, key); err != nil {
				return Report{}, fmt.Errorf("dream: forget %q: %w", key, err)
			}
		}
	}

	kept := len(entries) - len(fromMerge)
	return Report{Kept: kept, Merged: mergedCount, Forgotten: droppedCount}, nil
}

// stagePlan validates plan against the known input keys and returns the changes
// to apply: rewrite maps each surviving merge target to its tightened value, and
// fromMerge maps each key staged for deletion to whether it was a merge
// duplicate (true) or a standalone stale/low-value drop (false). It enforces all
// the conservative safety rules — no invented keys, never drop a survivor, no
// double-forget, and the global MaxForgets cap — so apply only has to mutate.
func (c *Consolidator) stagePlan(entries []tool.MemoryEntry, plan Plan) (rewrite map[string]string, fromMerge map[string]bool) {
	known := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		known[e.Key] = struct{}{}
	}

	maxForgets := c.cfg.MaxForgets
	if maxForgets < 0 {
		maxForgets = 0
	}

	rewrite = make(map[string]string)
	fromMerge = make(map[string]bool)

	// stage records key for deletion, honouring every safety rule. It returns
	// false (no-op) when the key is invented, is a surviving target, is already
	// staged, or the global cap is reached.
	stage := func(key string, viaMerge bool) bool {
		if _, ok := known[key]; !ok {
			return false
		}
		if _, isTarget := rewrite[key]; isTarget {
			return false
		}
		if _, already := fromMerge[key]; already {
			return false
		}
		if len(fromMerge) >= maxForgets {
			return false
		}
		fromMerge[key] = viaMerge
		return true
	}

	// Process merges first so a key chosen as a survivor is never forgotten.
	for _, m := range plan.Merges {
		target := strings.TrimSpace(m.Into)
		if _, ok := known[target]; !ok { // invented-key guard on the survivor
			continue
		}
		var folded int
		for _, src := range m.From {
			src = strings.TrimSpace(src)
			if src == "" || src == target {
				continue
			}
			if stage(src, true) {
				folded++
			}
		}
		if folded == 0 {
			continue
		}
		// Adopt the model's tightened value only when provided; otherwise keep the
		// existing value untouched.
		if strings.TrimSpace(m.Value) != "" {
			rewrite[target] = m.Value
		}
	}

	// Then standalone forgets (stale / low-value), sharing the same cap.
	for _, k := range plan.Forgets {
		stage(strings.TrimSpace(k), false)
	}

	return rewrite, fromMerge
}

// RunPeriodically drives Consolidate on a ticker every interval until ctx is
// cancelled, returning ctx.Err() when it stops. A non-positive interval is an
// error. Per-run errors are passed to onError (if non-nil) and otherwise
// ignored, so a single failed run does not stop the loop; the trigger policy
// (whether to run at all, and how often) belongs to the composition root.
func (c *Consolidator) RunPeriodically(ctx context.Context, interval time.Duration, onError func(error)) error {
	if interval <= 0 {
		return fmt.Errorf("dream: RunPeriodically requires a positive interval, got %v", interval)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := c.Consolidate(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

// --- plan types ------------------------------------------------------------

// Plan is the structured set of operations the model proposes. It is the parsed
// form of the model's JSON answer. Unknown or invented keys in it are rejected
// during apply.
type Plan struct {
	// Merges folds the From keys into the Into key, optionally rewriting Into's
	// value.
	Merges []Merge `json:"merges"`
	// Forgets lists keys to drop as stale or low-value.
	Forgets []string `json:"forgets"`
}

// Merge folds one or more near-duplicate source entries (From) into a single
// surviving entry (Into), optionally replacing Into's value with a tightened
// Value. Into and every From must already exist in the store.
type Merge struct {
	// Into is the surviving key (must be an existing key).
	Into string `json:"into"`
	// From are the duplicate keys to fold in and forget (each must already exist).
	From []string `json:"from"`
	// Value, if non-empty, is the tightened replacement value for Into.
	Value string `json:"value"`
}

// --- LLM-backed planner ----------------------------------------------------

// systemPrompt instructs the model to act as a conservative memory consolidator
// that only ever references existing keys.
const systemPrompt = `You are a conservative memory consolidator for an AI coding agent.
You are given the agent's current cross-session memory as a JSON array of
entries, each with a "key", a "value", and "updated_at". Your job is to REDUCE
noise WITHOUT inventing anything:

  - MERGE entries that are near-duplicates or clearly about the same fact into a
    single surviving entry. Choose ONE of the existing keys as the survivor.
  - DROP entries that are stale, superseded, or low-value.
  - You MAY tighten the surviving entry's wording, but you must NOT introduce any
    fact that is not already present in the inputs.

Hard rules:
  - Only ever reference keys that appear in the input. NEVER invent a new key.
  - Prefer keeping over deleting when unsure. When in doubt, leave it alone.
  - It is perfectly fine to propose no changes.

Respond with a single JSON object and nothing else:
{"merges":[{"into":"<existing key>","from":["<existing key>",...],"value":"<tightened value, optional>"}],
 "forgets":["<existing key>",...]}`

// llmPlanner consults a port.LLMProvider and parses its answer into a Plan. It
// holds no mutable state and is safe for concurrent use.
type llmPlanner struct {
	llm   port.LLMProvider
	model string
}

// Plan builds a small, bounded request describing the current entries, streams
// the model's answer, and parses the proposed operations. ctx already carries
// the per-run timeout set by Consolidate. An unparseable answer is an error so
// the caller can treat it fail-safe.
func (l *llmPlanner) Plan(ctx context.Context, entries []tool.MemoryEntry) (Plan, error) {
	req := port.LLMRequest{
		System:   prompt.Layered{StablePrefix: systemPrompt},
		Messages: []session.Message{session.NewUserMessage(renderEntries(entries))},
		Model:    l.model,
	}

	seq, err := l.llm.Stream(ctx, req)
	if err != nil {
		return Plan{}, fmt.Errorf("stream not established: %w", err)
	}

	var b strings.Builder
	for chunk, cerr := range seq {
		if cerr != nil {
			return Plan{}, fmt.Errorf("stream error: %w", cerr)
		}
		if chunk.Kind == port.ChunkText {
			b.WriteString(chunk.Text)
		}
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, fmt.Errorf("context: %w", err)
	}

	plan, ok := parsePlan(b.String())
	if !ok {
		return Plan{}, fmt.Errorf("unparseable plan %q", strings.TrimSpace(b.String()))
	}
	return plan, nil
}

// renderEntries produces the bounded user message: the entries as a compact
// JSON array, each value truncated to maxValueBytes.
func renderEntries(entries []tool.MemoryEntry) string {
	type wire struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		UpdatedAt string `json:"updated_at"`
	}
	out := make([]wire, 0, len(entries))
	for _, e := range entries {
		v := e.Value
		if len(v) > maxValueBytes {
			v = v[:maxValueBytes] + "…[truncated]"
		}
		out = append(out, wire{Key: e.Key, Value: v, UpdatedAt: e.UpdatedAt.UTC().Format(time.RFC3339)})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		// Should not happen for plain strings; fall back to an empty array.
		return "[]"
	}
	return "Current memory entries:\n" + string(raw)
}

// parsePlan extracts a Plan from the model's answer, tolerating surrounding
// prose by locating the first balanced object. It returns ok=false on empty or
// unparseable input so the caller fails safe. A well-formed object with no
// operations is a valid (empty) plan.
func parsePlan(s string) (Plan, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Plan{}, false
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return Plan{}, false
	}
	var plan Plan
	if err := json.Unmarshal([]byte(s[start:end+1]), &plan); err != nil {
		return Plan{}, false
	}
	return plan, true
}

// Compile-time assertions.
var _ planner = (*llmPlanner)(nil)
