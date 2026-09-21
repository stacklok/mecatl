// Package dream implements opt-in background memory consolidation. Planning and
// application are deliberately separate: GeneratePlan only inspects memory and
// consults the model, while ApplyPlan is the mutation boundary. Consolidate keeps
// the original one-call entry point by running those phases serially.
package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const (
	defaultMinEntriesToRun = 5
	manualMinEntriesToRun  = 2
	defaultMaxEntries      = 200
	defaultMaxForgets      = 10
	maxReasonBytes         = 512
	// The former per-entry 2 KiB cap allowed roughly 400 KiB before JSON overhead.
	// A 256 KiB aggregate cap is a conservative nearby bound without truncating a
	// value and presenting the model with a fact different from the stored one.
	defaultMaxInputBytes = 256 * 1024
	// A plan can name at most defaultMaxForgets source keys plus survivors. 64 KiB
	// leaves ample room for that small schema while bounding hostile model output.
	maxPlanOutputBytes = 64 * 1024
	defaultTimeout     = 60 * time.Second
	entriesPrefix      = "Current memory entries:\n"
)

// Config tunes the Consolidator. Zero values select package defaults.
type Config struct {
	Model           string
	Prefix          string
	MaxEntries      int
	MinEntriesToRun int
	// MaxInputBytes bounds the complete user message sent to the planner. Entries
	// that do not fit are skipped; individual values are never truncated.
	MaxInputBytes int
	// MaxForgets caps admitted source retirements. Zero selects the historical
	// default; a negative value disables application while still reporting the
	// normalized proposals as skipped.
	MaxForgets int
	Timeout    time.Duration
}

// Report summarises one consolidation run. The operation accounting fields are
// designed so Planned = Applied + Conflicted + Skipped + Failed.
type Report struct {
	Planned    int
	Applied    int
	Conflicted int
	Skipped    int
	Failed     int

	// Legacy fields remain while callers migrate to operation accounting.
	Kept      int
	Merged    int
	Forgotten int
}

// Plan is an opaque, instance-bound consolidation plan. Its wire operations and
// inspected memory bindings remain private to avoid making model grammar a public
// package contract.
type Plan struct {
	owner      *Consolidator
	candidates map[string]candidate
	operations []supersession
	eligible   int
}

// OperationKind identifies one reviewed consolidation operation family.
type OperationKind string

const (
	// OperationExactDuplicate retires byte-identical sources without rewriting the survivor.
	OperationExactDuplicate OperationKind = "exact_duplicate"
	// OperationSynthesizedReplacement revises the survivor and atomically retires its sources.
	OperationSynthesizedReplacement OperationKind = "synthesized_replacement"
)

// ReviewPlan is a detached, version-free projection for human review. Review
// returns a fresh deep copy, so changing it cannot alter the retained Plan.
type ReviewPlan struct {
	Operations []ReviewOperation
}

// ReviewOperation is one ordered, detached operation shown for approval.
type ReviewOperation struct {
	Kind                   OperationKind
	Survivor               ReviewParticipant
	Sources                []ReviewParticipant
	Replacement            ReviewReplacement
	Reason                 string
	ExactDuplicateEligible bool
}

// ReviewParticipant contains a participant's reviewable content without its version.
type ReviewParticipant struct {
	Key         string
	Value       string
	Description string
}

// ReviewReplacement contains the proposed reviewed survivor content.
type ReviewReplacement struct {
	Value       string
	Description string
}

// Review returns a version-free copy of the exact retained plan bytes. Model-authored
// text that canonicalization would alter is rejected before a Plan is retained.
func (p Plan) Review() ReviewPlan {
	operations := make([]ReviewOperation, 0, len(p.operations))
	for _, operation := range p.operations {
		survivor, ok := p.candidates[operation.Survivor]
		if !ok {
			continue
		}
		kind := operation.Kind
		if kind == "" {
			kind = OperationExactDuplicate
		}
		reviewed := ReviewOperation{
			Kind: kind, Survivor: reviewParticipant(survivor.entry),
			Replacement: ReviewReplacement{Value: operation.Value, Description: operation.Description},
			Reason:      operation.Reason,
		}
		eligible := kind == OperationExactDuplicate
		for _, key := range operation.Superseded {
			source, found := p.candidates[key]
			if !found {
				continue
			}
			reviewed.Sources = append(reviewed.Sources, reviewParticipant(source.entry))
			eligible = eligible && source.entry.Value == survivor.entry.Value && source.entry.Description == survivor.entry.Description
		}
		reviewed.ExactDuplicateEligible = eligible && len(reviewed.Sources) == len(operation.Superseded) && len(reviewed.Sources) > 0
		operations = append(operations, reviewed)
	}
	return ReviewPlan{Operations: operations}
}

func reviewParticipant(entry tool.MemoryEntry) ReviewParticipant {
	return ReviewParticipant{Key: entry.Key, Value: entry.Value, Description: entry.Description}
}

type candidate struct {
	entry   tool.MemoryEntry
	version tool.MemoryVersion
}

// duplicateRetirementStore is the internal atomic mutation capability required
// for unattended consolidation. Implementations compare both bound versions and
// tombstone the source in one transaction.
type duplicateRetirementStore interface {
	tool.MemoryStore
	RetireDuplicate(ctx context.Context, survivorKey string, survivorVersion tool.MemoryVersion, sourceKey string, sourceVersion tool.MemoryVersion) (tool.MemoryRecord, error)
}

// synthesisStore is deliberately adapter-internal. A store either preserves the
// whole-operation transaction capability through wrappers or does not advertise it.
type synthesisStore interface {
	SynthesizeReplacement(ctx context.Context, survivor tool.MemoryEntry, survivorVersion tool.MemoryVersion, sourceKeys []string, sourceVersions []tool.MemoryVersion) (tool.MemoryRecord, error)
}

// SupportsReviewedPlan reports whether store preserves both atomic capabilities
// required to apply every reviewed operation family. It does not inspect or
// mutate the store.
func SupportsReviewedPlan(store tool.MemoryStore) bool {
	if store == nil {
		return false
	}
	_, duplicateOK := store.(duplicateRetirementStore)
	_, synthesisOK := store.(synthesisStore)
	return duplicateOK && synthesisOK
}

type supersession struct {
	Kind        OperationKind
	Survivor    string
	Superseded  []string
	Value       string
	Description string
	Reason      string
}

type exactDuplicateWire struct {
	Survivor   string   `json:"survivor"`
	Superseded []string `json:"superseded"`
	Reason     string   `json:"reason"`
}

type synthesizedReplacementWire struct {
	Survivor    string   `json:"survivor"`
	Superseded  []string `json:"superseded"`
	Value       string   `json:"Value"`
	Description string   `json:"Description"`
	Reason      string   `json:"reason"`
}

type planWire struct {
	ExactDuplicates         []exactDuplicateWire         `json:"exact_duplicates"`
	SynthesizedReplacements []synthesizedReplacementWire `json:"synthesized_replacements"`
}

type planner interface {
	Plan(context.Context, []tool.MemoryEntry) ([]supersession, error)
}

// Consolidator owns the rotating selection cursor and a context-aware gate. The
// gate serializes GeneratePlan, ApplyPlan, and Consolidate for this instance.
type Consolidator struct {
	store   tool.MemoryStore
	planner planner
	cfg     Config
	gate    chan struct{}
	cursor  int
}

// New constructs a Consolidator using llm for plan generation.
func New(store tool.MemoryStore, llm port.LLMProvider, cfg Config) *Consolidator {
	cfg = withDefaults(cfg)
	return &Consolidator{
		store: store, planner: &llmPlanner{llm: llm, model: cfg.Model, maxForgets: planLimit(cfg.MaxForgets)}, cfg: cfg,
		gate: make(chan struct{}, 1),
	}
}

func newWithPlanner(store tool.MemoryStore, p planner, cfg Config) *Consolidator {
	cfg = withDefaults(cfg)
	return &Consolidator{store: store, planner: p, cfg: cfg, gate: make(chan struct{}, 1)}
}

func withDefaults(cfg Config) Config {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = defaultMaxEntries
	}
	if cfg.MinEntriesToRun <= 0 {
		cfg.MinEntriesToRun = defaultMinEntriesToRun
	}
	if cfg.MaxInputBytes <= 0 {
		cfg.MaxInputBytes = defaultMaxInputBytes
	}
	if cfg.MaxForgets == 0 {
		cfg.MaxForgets = defaultMaxForgets
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return cfg
}

func (c *Consolidator) enter(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("dream: wait: %w", ctx.Err())
	}
}

func (c *Consolidator) leave() { <-c.gate }

// GeneratePlan selects a bounded rotating window, binds convergence-store
// candidates to their exact inspected revisions, and consults the planner when
// the configured scheduled threshold is met. It never mutates the store.
func (c *Consolidator) GeneratePlan(ctx context.Context) (Plan, error) {
	return c.generatePlanWithThreshold(ctx, c.cfg.MinEntriesToRun)
}

// GenerateManualPlan generates an operator-requested review when at least two
// entries are eligible. It shares GeneratePlan's gate, rotating selection, and
// planner while leaving the scheduled MinEntriesToRun threshold unchanged.
func (c *Consolidator) GenerateManualPlan(ctx context.Context) (Plan, error) {
	return c.generatePlanWithThreshold(ctx, manualMinEntriesToRun)
}

func (c *Consolidator) generatePlanWithThreshold(ctx context.Context, minEntries int) (Plan, error) {
	if err := c.enter(ctx); err != nil {
		return Plan{}, err
	}
	defer c.leave()
	return c.generatePlan(ctx, minEntries)
}

func (c *Consolidator) generatePlan(ctx context.Context, minEntries int) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, fmt.Errorf("dream: %w", err)
	}
	entries, err := c.store.List(ctx, c.cfg.Prefix)
	if err != nil {
		return Plan{}, fmt.Errorf("dream: list memory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	plan := Plan{owner: c, candidates: make(map[string]candidate), eligible: len(entries)}
	if len(entries) < minEntries {
		return plan, nil
	}

	selected, bindings, err := c.selectEntries(ctx, entries)
	if err != nil {
		return Plan{}, err
	}
	plan.candidates = bindings
	plan.eligible = len(selected)
	if len(selected) == 0 {
		return plan, nil
	}

	pctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	operations, err := c.planner.Plan(pctx, selected)
	if err != nil {
		return Plan{}, fmt.Errorf("dream: plan: %w", err)
	}
	if err := validateModelText(operations); err != nil {
		return Plan{}, errors.New("dream: invalid plan output")
	}
	if err := validateOperations(operations, bindings); err != nil {
		return Plan{}, errors.New("dream: invalid plan output")
	}
	plan.operations = operations
	return plan, nil
}

func validateModelText(operations []supersession) error {
	for _, operation := range operations {
		for _, value := range []string{operation.Value, operation.Description, operation.Reason} {
			if tool.CanonicalMemoryText(value) != value {
				return errors.New("non-canonical model text")
			}
		}
	}
	return nil
}

func validateOperations(operations []supersession, candidates map[string]candidate) error {
	roles := make(map[string]bool)
	for _, operation := range operations {
		if operation.Kind != OperationExactDuplicate && operation.Kind != OperationSynthesizedReplacement && operation.Kind != "" {
			return errors.New("unknown operation kind")
		}
		if _, ok := candidates[operation.Survivor]; !ok {
			return errors.New("unknown survivor")
		}
		if operation.Kind == OperationSynthesizedReplacement {
			survivor := candidates[operation.Survivor]
			if operation.Value == survivor.entry.Value && operation.Description == survivor.entry.Description {
				return errors.New("synthesized replacement is unchanged")
			}
		}
		if sourceRole, assigned := roles[operation.Survivor]; assigned && !sourceRole {
			return errors.New("cross-role key")
		}
		if _, repeated := roles[operation.Survivor]; repeated {
			return errors.New("repeated survivor")
		}
		roles[operation.Survivor] = true
		if len(operation.Superseded) == 0 || len(operation.Reason) > maxReasonBytes {
			return errors.New("invalid operation")
		}
		for _, key := range operation.Superseded {
			if key == operation.Survivor {
				return errors.New("same survivor and source")
			}
			if _, ok := candidates[key]; !ok {
				return errors.New("unknown source")
			}
			if _, assigned := roles[key]; assigned {
				return errors.New("repeated or cross-role source")
			}
			roles[key] = false
		}
	}
	return nil
}

// selectEntries rotates over the sorted active set. For an unchanged set where
// every entry fits individually, repeated calls examine every entry before reuse;
// insertion/removal may reposition the process-local cursor, and restart resets
// it. Cursor progress is based on examined rather than selected entries, so one
// oversized entry cannot pin selection.
func (c *Consolidator) selectEntries(ctx context.Context, entries []tool.MemoryEntry) ([]tool.MemoryEntry, map[string]candidate, error) {
	start := c.cursor % len(entries)
	selected := make([]tool.MemoryEntry, 0, min(c.cfg.MaxEntries, len(entries)))
	bindings := make(map[string]candidate)
	messageBytes := len(entriesPrefix) + 2 // JSON array brackets.
	examined := 0
	lastSelectedAdvance := 0
	defer func() {
		advance := examined
		// If the aggregate cap forced a full scan to select only a prefix, resume
		// after the last selected entry rather than wrapping to the same start.
		if examined == len(entries) && len(selected) > 0 {
			advance = lastSelectedAdvance
		}
		c.cursor = (start + advance) % len(entries)
	}()

	for examined < len(entries) && len(selected) < c.cfg.MaxEntries {
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("dream: select memory: %w", err)
		}
		entry := entries[(start+examined)%len(entries)]
		examined++
		record, found, err := c.store.Inspect(ctx, entry.Key)
		if err != nil {
			return nil, nil, fmt.Errorf("dream: inspect candidate: %w", err)
		}
		if !found || record.Current.Status != tool.MemoryStatusActive {
			continue
		}
		entry = tool.MemoryEntry{Key: record.Current.Key, Value: record.Current.Value, Description: record.Current.Description, UpdatedAt: record.Current.UpdatedAt}
		binding := candidate{entry: entry, version: record.Current.Version}
		raw, err := json.Marshal(entryWireOf(entry))
		if err != nil {
			return nil, nil, errors.New("dream: encode candidate")
		}
		extra := len(raw)
		if len(selected) > 0 {
			extra++
		}
		if messageBytes+extra > c.cfg.MaxInputBytes {
			continue
		}
		messageBytes += extra
		selected = append(selected, entry)
		lastSelectedAdvance = examined
		bindings[entry.Key] = binding
	}
	return selected, bindings, nil
}

type normalizedSupersession struct {
	kind        OperationKind
	survivor    candidate
	sources     []candidate
	replacement tool.MemoryEntry
	reason      string
}

// ApplyPlan is the explicit mutation boundary. Only a store with atomic duplicate
// retirement can apply a version-bound plan; every other store reports normalized
// proposals as skipped without invoking any mutation method.
func (c *Consolidator) ApplyPlan(ctx context.Context, plan Plan) (Report, error) {
	if err := c.enter(ctx); err != nil {
		return Report{}, err
	}
	defer c.leave()
	return c.applyPlan(ctx, plan)
}

func (c *Consolidator) applyPlan(ctx context.Context, plan Plan) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, fmt.Errorf("dream: %w", err)
	}
	if plan.owner != c {
		return Report{}, errors.New("dream: plan belongs to another consolidator")
	}

	operations := c.normalize(plan, false)
	report := Report{Kept: plan.eligible}
	for _, operation := range operations {
		report.Planned += len(operation.sources)
	}
	retirement, ok := c.store.(duplicateRetirementStore)
	if !ok || c.cfg.MaxForgets < 0 {
		report.Skipped = report.Planned
		return report, nil
	}

	ctx = tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{
		Writer: tool.MemoryWriterSystem,
		Origin: tool.MemoryOriginConsolidation,
	})
	var failures []error
	for _, operation := range operations {
		result, err := applySupersession(ctx, retirement, operation)
		report.Applied += result.Applied
		report.Conflicted += result.Conflicted
		report.Skipped += result.Skipped
		report.Failed += result.Failed
		if err != nil {
			failures = append(failures, err)
		}
	}
	report.Merged = report.Applied
	report.Kept -= report.Applied
	return report, errors.Join(failures...)
}

// ApplyReviewedPlan applies the retained, instance-bound plan after explicit human
// approval. Synthesis operations are atomic per operation; independent operations
// continue after a conflict or failure.
func (c *Consolidator) ApplyReviewedPlan(ctx context.Context, plan Plan) (Report, error) {
	if err := c.enter(ctx); err != nil {
		return Report{}, err
	}
	defer c.leave()
	if err := ctx.Err(); err != nil {
		return Report{}, fmt.Errorf("dream: %w", err)
	}
	if plan.owner != c {
		return Report{}, errors.New("dream: plan belongs to another consolidator")
	}
	operations := c.normalize(plan, true)
	report := Report{Kept: plan.eligible}
	for _, operation := range operations {
		report.Planned += len(operation.sources)
	}
	if c.cfg.MaxForgets < 0 {
		report.Skipped = report.Planned
		return report, nil
	}
	ctx = tool.WithMemoryAttribution(ctx, tool.MemoryAttribution{Writer: tool.MemoryWriterSystem, Origin: tool.MemoryOriginConsolidation})
	retirement, duplicateOK := c.store.(duplicateRetirementStore)
	synthesis, synthesisOK := c.store.(synthesisStore)
	var failures []error
	for _, operation := range operations {
		if operation.kind != OperationSynthesizedReplacement {
			if !duplicateOK {
				report.Skipped += len(operation.sources)
				continue
			}
			result, err := applySupersession(ctx, retirement, operation)
			report.Applied += result.Applied
			report.Conflicted += result.Conflicted
			report.Skipped += result.Skipped
			report.Failed += result.Failed
			if err != nil {
				failures = append(failures, err)
			}
			continue
		}
		count := len(operation.sources)
		if !synthesisOK {
			report.Skipped += count
			continue
		}
		keys := make([]string, count)
		versions := make([]tool.MemoryVersion, count)
		for i, source := range operation.sources {
			keys[i], versions[i] = source.entry.Key, source.version
		}
		_, err := synthesis.SynthesizeReplacement(ctx, operation.replacement, operation.survivor.version, keys, versions)
		if err == nil {
			report.Applied += count
			continue
		}
		if isVersionConflict(err) || errors.Is(err, tool.ErrMemoryNotFound) {
			report.Conflicted += count
		} else {
			report.Failed += count
			failures = append(failures, fmt.Errorf("dream: apply synthesized replacement: %w", err))
		}
	}
	report.Merged = report.Applied
	report.Kept -= report.Applied
	return report, errors.Join(failures...)
}

func applySupersession(ctx context.Context, store duplicateRetirementStore, operation normalizedSupersession) (Report, error) {
	var (
		report   Report
		failures []error
	)
	for _, source := range operation.sources {
		if source.entry.Value != operation.survivor.entry.Value ||
			source.entry.Description != operation.survivor.entry.Description {
			report.Skipped++
			continue
		}
		if _, err := store.RetireDuplicate(ctx,
			operation.survivor.entry.Key, operation.survivor.version,
			source.entry.Key, source.version,
		); err != nil {
			if isVersionConflict(err) || errors.Is(err, tool.ErrMemoryNotFound) {
				report.Conflicted++
			} else {
				report.Failed++
				failures = append(failures, fmt.Errorf("dream: retire duplicate source: %w", err))
			}
			continue
		}
		report.Applied++
	}
	return report, errors.Join(failures...)
}

func isVersionConflict(err error) bool {
	var conflict *tool.MemoryVersionConflictError
	return errors.As(err, &conflict)
}

// normalize admits source retirements in model order. Invalid references and
// repeats are ignored rather than allowed to perturb later valid operations.
// Role assignment occurs only when a source is admitted, preserving the first
// valid occurrence while ensuring no accepted key is both survivor and source.
func (c *Consolidator) normalize(plan Plan, includeSynthesis bool) []normalizedSupersession {
	limit := c.cfg.MaxForgets
	if limit < 0 {
		limit = int(^uint(0) >> 1)
	}
	roles := make(map[string]bool) // true survivor, false source
	admitted := 0
	out := make([]normalizedSupersession, 0, len(plan.operations))
	for _, proposed := range plan.operations {
		kind := proposed.Kind
		if kind == "" { // Preserve pre-family plans created by in-package callers.
			kind = OperationExactDuplicate
		}
		if kind == OperationSynthesizedReplacement && !includeSynthesis || kind != OperationExactDuplicate && kind != OperationSynthesizedReplacement {
			continue
		}
		survivorKey := proposed.Survivor
		survivor, known := plan.candidates[survivorKey]
		if !known {
			continue
		}
		if survivorRole, assigned := roles[survivorKey]; assigned && !survivorRole {
			continue
		}
		normalized := normalizedSupersession{
			kind: kind, survivor: survivor, reason: proposed.Reason,
			replacement: tool.MemoryEntry{Key: survivorKey, Value: proposed.Value, Description: proposed.Description},
		}
		if kind == OperationSynthesizedReplacement && proposed.Value == survivor.entry.Value && proposed.Description == survivor.entry.Description {
			continue
		}
		for _, sourceKey := range proposed.Superseded {
			if admitSource(plan.candidates, roles, survivorKey, sourceKey, limit, &admitted, &normalized) {
				break
			}
		}
		if len(normalized.sources) > 0 {
			out = append(out, normalized)
		}
		if admitted == limit {
			break
		}
	}
	return out
}

func admitSource(candidates map[string]candidate, roles map[string]bool, survivorKey, sourceKey string, limit int, admitted *int, operation *normalizedSupersession) bool {
	if *admitted == limit {
		return true
	}
	source, known := candidates[sourceKey]
	if !known || sourceKey == survivorKey {
		return false
	}
	if _, assigned := roles[sourceKey]; assigned {
		return false
	}
	roles[survivorKey] = true
	roles[sourceKey] = false
	operation.sources = append(operation.sources, source)
	*admitted++
	return *admitted == limit
}

// Consolidate preserves the original one-call API without recursively acquiring
// the instance gate.
func (c *Consolidator) Consolidate(ctx context.Context) (Report, error) {
	if err := c.enter(ctx); err != nil {
		return Report{}, err
	}
	defer c.leave()
	plan, err := c.generatePlan(ctx, c.cfg.MinEntriesToRun)
	if err != nil {
		return Report{}, err
	}
	return c.applyPlan(ctx, plan)
}

// RunPeriodically preserves the original error-only callback API.
func (c *Consolidator) RunPeriodically(ctx context.Context, interval time.Duration, onError func(error)) error {
	return c.RunPeriodicallyWithReport(ctx, interval, func(_ Report, err error) {
		if err != nil && onError != nil {
			onError(err)
		}
	})
}

// RunPeriodicallyWithReport drives Consolidate on a ticker and reports every
// completed attempt, including partial reports accompanied by an error.
func (c *Consolidator) RunPeriodicallyWithReport(ctx context.Context, interval time.Duration, onReport func(Report, error)) error {
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
			report, err := c.Consolidate(ctx)
			if onReport != nil {
				onReport(report, err)
			}
		}
	}
}

const systemPrompt = `You are a conservative memory consolidator for an AI coding agent.
You receive complete active memory entries as JSON. Propose either exact duplicates or
synthesized replacements. Both choose one EXISTING survivor key and EXISTING source
keys. Exact duplicates contain no replacement content. Synthesized replacements contain
the complete proposed Value and Description. Never propose a standalone deletion.
Prefer no operation when uncertain.

Respond with one bare JSON object and nothing else:
{"exact_duplicates":[{"survivor":"<existing key>","superseded":["<existing key>"],"reason":"<brief reason>"}],"synthesized_replacements":[{"survivor":"<existing key>","superseded":["<existing key>"],"Value":"<replacement>","Description":"<replacement description>","reason":"<brief reason>"}]}`

type llmPlanner struct {
	llm        port.LLMProvider
	model      string
	maxForgets int
}

func (l *llmPlanner) Plan(ctx context.Context, entries []tool.MemoryEntry) ([]supersession, error) {
	req := port.LLMRequest{
		System:   prompt.Layered{StablePrefix: systemPrompt},
		Messages: []session.Message{session.NewUserMessage(renderEntries(entries))},
		Model:    l.model,
	}
	seq, err := l.llm.Stream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("stream not established: %w", err)
	}
	var b strings.Builder
	for chunk, streamErr := range seq {
		if streamErr != nil {
			return nil, fmt.Errorf("stream error: %w", streamErr)
		}
		if chunk.Kind == port.ChunkText {
			if len(chunk.Text) > maxPlanOutputBytes-b.Len() {
				return nil, errors.New("invalid plan output")
			}
			b.WriteString(chunk.Text)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context: %w", err)
	}
	wire, err := parsePlan(b.String(), l.maxForgets)
	if err != nil {
		// Never include model output (including unknown field names) in errors.
		return nil, errors.New("invalid plan output")
	}
	operations := make([]supersession, 0, len(wire.ExactDuplicates)+len(wire.SynthesizedReplacements))
	for _, operation := range wire.ExactDuplicates {
		operations = append(operations, supersession{Kind: OperationExactDuplicate, Survivor: operation.Survivor, Superseded: operation.Superseded, Reason: operation.Reason})
	}
	for _, operation := range wire.SynthesizedReplacements {
		operations = append(operations, supersession{Kind: OperationSynthesizedReplacement, Survivor: operation.Survivor, Superseded: operation.Superseded, Value: operation.Value, Description: operation.Description, Reason: operation.Reason})
	}
	return operations, nil
}

type entryWire struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updated_at"`
}

func entryWireOf(entry tool.MemoryEntry) entryWire {
	return entryWire{Key: entry.Key, Value: entry.Value, Description: entry.Description, UpdatedAt: entry.UpdatedAt.UTC().Format(time.RFC3339)}
}

func renderEntries(entries []tool.MemoryEntry) string {
	out := make([]entryWire, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entryWireOf(entry))
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return entriesPrefix + "[]"
	}
	return entriesPrefix + string(raw)
}

func parsePlan(output string, configuredLimit ...int) (planWire, error) {
	maxForgets := defaultMaxForgets
	if len(configuredLimit) > 0 {
		maxForgets = configuredLimit[0]
	}
	if len(output) > maxPlanOutputBytes {
		return planWire{}, errors.New("oversized")
	}
	if strings.TrimSpace(output) == "" {
		return planWire{}, errors.New("empty")
	}
	if err := validateJSONObject(output); err != nil {
		return planWire{}, err
	}
	if err := validateExactPlanMembers(output); err != nil {
		return planWire{}, err
	}
	dec := json.NewDecoder(strings.NewReader(output))
	dec.DisallowUnknownFields()
	var wire planWire
	if err := dec.Decode(&wire); err != nil {
		return planWire{}, err
	}
	if err := validateDecodedPlan(wire, maxForgets); err != nil {
		return planWire{}, err
	}
	if err := requireEOF(dec); err != nil {
		return planWire{}, err
	}
	return wire, nil
}

func validateDecodedPlan(wire planWire, maxForgets int) error {
	if wire.ExactDuplicates == nil || wire.SynthesizedReplacements == nil {
		return errors.New("missing operation family")
	}
	limit := planLimit(maxForgets)
	if len(wire.ExactDuplicates)+len(wire.SynthesizedReplacements) > limit {
		return errors.New("too many operations")
	}
	roles := make(map[string]bool)
	retirements := 0
	for _, operation := range wire.ExactDuplicates {
		if operation.Survivor == "" || len(operation.Superseded) == 0 || operation.Reason == "" || len(operation.Reason) > maxReasonBytes {
			return errors.New("incomplete exact duplicate")
		}
		if err := validateOperationRoles(roles, operation.Survivor, operation.Superseded); err != nil {
			return err
		}
		retirements += len(operation.Superseded)
	}
	for _, operation := range wire.SynthesizedReplacements {
		if operation.Survivor == "" || len(operation.Superseded) == 0 || operation.Reason == "" || len(operation.Reason) > maxReasonBytes {
			return errors.New("incomplete synthesized replacement")
		}
		if err := validateOperationRoles(roles, operation.Survivor, operation.Superseded); err != nil {
			return err
		}
		retirements += len(operation.Superseded)
	}
	if retirements > limit {
		return errors.New("too many source retirements")
	}
	return nil
}

func validateOperationRoles(roles map[string]bool, survivor string, sources []string) error {
	if _, assigned := roles[survivor]; assigned {
		return errors.New("repeated or cross-role survivor")
	}
	roles[survivor] = true
	for _, source := range sources {
		if source == survivor {
			return errors.New("same survivor and source")
		}
		if _, assigned := roles[source]; assigned {
			return errors.New("repeated or cross-role source")
		}
		roles[source] = false
	}
	return nil
}

func planLimit(maxForgets int) int {
	if maxForgets <= 0 {
		return defaultMaxForgets
	}
	return maxForgets
}

func validateExactPlanMembers(output string) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &top); err != nil {
		return err
	}
	if len(top) != 2 || top["exact_duplicates"] == nil || top["synthesized_replacements"] == nil {
		return errors.New("invalid top-level members")
	}
	for family, expected := range map[string]map[string]struct{}{
		"exact_duplicates":         {"survivor": {}, "superseded": {}, "reason": {}},
		"synthesized_replacements": {"survivor": {}, "superseded": {}, "Value": {}, "Description": {}, "reason": {}},
	} {
		var operations []json.RawMessage
		if err := json.Unmarshal(top[family], &operations); err != nil {
			return err
		}
		for _, operation := range operations {
			var members map[string]json.RawMessage
			if err := json.Unmarshal(operation, &members); err != nil {
				return err
			}
			if len(members) != len(expected) {
				return errors.New("invalid operation members")
			}
			for member := range expected {
				if members[member] == nil {
					return errors.New("invalid operation members")
				}
			}
		}
	}
	return nil
}

// validateJSONObject rejects duplicate keys at every object depth before normal
// decoding (encoding/json otherwise silently accepts the last duplicate).
func validateJSONObject(output string) error {
	dec := json.NewDecoder(strings.NewReader(output))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errors.New("top-level value is not an object")
	}
	if err := consumeObject(dec); err != nil {
		return err
	}
	return requireEOF(dec)
}

func consumeObject(dec *json.Decoder) error {
	seen := make(map[string]struct{})
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate object key")
		}
		seen[key] = struct{}{}
		if err := consumeValue(dec); err != nil {
			return err
		}
	}
	_, err := dec.Token() // closing brace
	return err
}

func consumeValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return consumeObject(dec)
	case '[':
		for dec.More() {
			if err := consumeValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	default:
		return errors.New("unexpected delimiter")
	}
}

func requireEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

var _ planner = (*llmPlanner)(nil)
