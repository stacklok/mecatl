package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/dream"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	dreamReviewTTL            = 10 * time.Minute
	maxDreamReviewRecords     = 64
	maxPendingDreamsPerTarget = 8
)

type dreamReviewState uint8

const (
	dreamGenerating dreamReviewState = iota
	dreamPending
	dreamApplying
	dreamTerminal
)

type retainedDream struct {
	id        string
	target    server.DreamTarget
	expiresAt time.Time
	state     dreamReviewState
	plan      dream.Plan
	review    server.DreamReview
	decision  server.DreamDecision
	receipt   server.DreamReceipt
}

type dreamReviewConfig struct {
	targets    map[server.DreamTarget]*dream.Consolidator
	now        func() time.Time
	newID      func() (string, error)
	ttl        time.Duration
	maxRecords int
	maxPending int
}

// dreamReviewCoordinator owns authoritative plans only in this process. Records
// are bounded, expire lazily, and are discarded on process shutdown by ownership
// of the enclosing Build. It starts no goroutine and has no external cleanup.
type dreamReviewCoordinator struct {
	mu         sync.Mutex
	targets    map[server.DreamTarget]*dream.Consolidator
	records    map[string]*retainedDream
	now        func() time.Time
	newID      func() (string, error)
	ttl        time.Duration
	maxRecords int
	maxPending int
}

var _ server.DreamReviewer = (*dreamReviewCoordinator)(nil)

func newDreamReviewCoordinator(cfg dreamReviewConfig) *dreamReviewCoordinator {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.newID == nil {
		cfg.newID = randomDreamReviewID
	}
	if cfg.ttl <= 0 {
		cfg.ttl = dreamReviewTTL
	}
	if cfg.maxRecords <= 0 {
		cfg.maxRecords = maxDreamReviewRecords
	}
	if cfg.maxPending <= 0 {
		cfg.maxPending = maxPendingDreamsPerTarget
	}
	return &dreamReviewCoordinator{
		targets: cfg.targets, records: make(map[string]*retainedDream), now: cfg.now,
		newID: cfg.newID, ttl: cfg.ttl, maxRecords: cfg.maxRecords, maxPending: cfg.maxPending,
	}
}

func randomDreamReviewID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (c *dreamReviewCoordinator) Generate(ctx context.Context, target server.DreamTarget) (server.DreamReview, error) {
	consolidator := c.targets[target]
	if consolidator == nil {
		return server.DreamReview{}, server.ErrDreamUnavailable
	}

	now := c.now()
	c.mu.Lock()
	c.cleanupLocked(now)
	if len(c.records) >= c.maxRecords || c.pendingLocked(target) >= c.maxPending {
		c.mu.Unlock()
		return server.DreamReview{}, server.ErrDreamCapacity
	}
	id, err := c.uniqueIDLocked()
	if err != nil {
		c.mu.Unlock()
		return server.DreamReview{}, server.ErrDreamGenerateFailed
	}
	record := &retainedDream{id: id, target: target, expiresAt: now.Add(c.ttl), state: dreamGenerating}
	c.records[id] = record
	c.mu.Unlock()

	plan, err := consolidator.GenerateManualPlan(ctx)
	if err != nil {
		c.mu.Lock()
		delete(c.records, id)
		c.mu.Unlock()
		return server.DreamReview{}, server.ErrDreamGenerateFailed
	}
	review := projectDreamReview(id, target, c.now().Add(c.ttl), plan.Review())

	c.mu.Lock()
	record, ok := c.records[id]
	if !ok || record.state != dreamGenerating {
		c.mu.Unlock()
		return server.DreamReview{}, server.ErrDreamNotFound
	}
	record.plan = plan
	record.review = cloneDreamReview(review)
	record.expiresAt = review.ExpiresAt
	record.state = dreamPending
	c.mu.Unlock()
	return cloneDreamReview(review), nil
}

func (c *dreamReviewCoordinator) Decide(ctx context.Context, id string, decision server.DreamDecision) (server.DreamReceipt, error) {
	now := c.now()
	c.mu.Lock()
	c.cleanupLocked(now)
	record, ok := c.records[id]
	if !ok {
		c.mu.Unlock()
		return server.DreamReceipt{}, server.ErrDreamNotFound
	}
	if record.state == dreamTerminal {
		receipt := record.receipt
		if record.decision != decision {
			c.mu.Unlock()
			return server.DreamReceipt{}, server.ErrDreamTerminalConflict
		}
		c.mu.Unlock()
		if !completeDreamReceipt(receipt) {
			return receipt, server.ErrDreamApplyFailed
		}
		return receipt, nil
	}
	if record.state == dreamApplying {
		err := server.ErrDreamConflict
		if record.decision == decision {
			err = server.ErrDreamInProgress
		}
		c.mu.Unlock()
		return server.DreamReceipt{}, err
	}
	if record.state != dreamPending {
		c.mu.Unlock()
		return server.DreamReceipt{}, server.ErrDreamConflict
	}
	if decision == server.DreamDecisionDismiss {
		receipt := server.DreamReceipt{
			ID: id, Target: record.target, Disposition: decision,
			Planned: record.review.PlannedSourceCount, Skipped: record.review.PlannedSourceCount,
		}
		c.finishLocked(record, decision, receipt, now)
		c.mu.Unlock()
		return receipt, nil
	}
	if decision != server.DreamDecisionApply {
		c.mu.Unlock()
		return server.DreamReceipt{}, server.ErrDreamConflict
	}
	record.state = dreamApplying
	record.decision = decision
	plan, target, planned := record.plan, record.target, record.review.PlannedSourceCount
	consolidator := c.targets[target]
	c.mu.Unlock()

	if consolidator == nil {
		receipt := server.DreamReceipt{ID: id, Target: target, Disposition: decision, Planned: planned, Failed: planned}
		c.mu.Lock()
		record, ok = c.records[id]
		if ok {
			c.finishLocked(record, decision, receipt, c.now())
		}
		c.mu.Unlock()
		return receipt, nil
	}
	report, applyErr := consolidator.ApplyReviewedPlan(ctx, plan)
	receipt := server.DreamReceipt{
		ID: id, Target: target, Disposition: decision, Planned: planned,
		Applied: report.Applied, Conflicted: report.Conflicted, Skipped: report.Skipped, Failed: report.Failed,
	}
	accounted := receipt.Applied + receipt.Conflicted + receipt.Skipped + receipt.Failed
	if accounted < receipt.Planned {
		receipt.Failed += receipt.Planned - accounted
	}
	complete := completeDreamReceipt(receipt)
	c.mu.Lock()
	record, ok = c.records[id]
	if ok {
		c.finishLocked(record, decision, receipt, c.now())
	}
	c.mu.Unlock()
	if applyErr != nil && !complete {
		return receipt, server.ErrDreamApplyFailed
	}
	return receipt, nil
}

func completeDreamReceipt(receipt server.DreamReceipt) bool {
	return receipt.Planned == receipt.Applied+receipt.Conflicted+receipt.Skipped+receipt.Failed
}

func (c *dreamReviewCoordinator) uniqueIDLocked() (string, error) {
	for range 4 {
		id, err := c.newID()
		if err != nil {
			return "", err
		}
		if id != "" {
			if _, exists := c.records[id]; !exists {
				return id, nil
			}
		}
	}
	return "", errors.New("dream review id collision")
}

func (c *dreamReviewCoordinator) pendingLocked(target server.DreamTarget) int {
	count := 0
	for _, record := range c.records {
		if record.target == target && record.state != dreamTerminal {
			count++
		}
	}
	return count
}

func (c *dreamReviewCoordinator) cleanupLocked(now time.Time) {
	for id, record := range c.records {
		if record.state != dreamGenerating && record.state != dreamApplying && !now.Before(record.expiresAt) {
			delete(c.records, id)
		}
	}
}

func (c *dreamReviewCoordinator) finishLocked(record *retainedDream, decision server.DreamDecision, receipt server.DreamReceipt, now time.Time) {
	record.state = dreamTerminal
	record.decision = decision
	record.receipt = receipt
	record.expiresAt = now.Add(c.ttl)
	record.target = ""
	record.plan = dream.Plan{}
	record.review = server.DreamReview{}
}

func projectDreamReview(id string, target server.DreamTarget, expiresAt time.Time, review dream.ReviewPlan) server.DreamReview {
	out := server.DreamReview{ID: id, Target: target, ExpiresAt: expiresAt, PlannedOperations: len(review.Operations)}
	out.Operations = make([]server.DreamOperation, len(review.Operations))
	for i, operation := range review.Operations {
		projected := server.DreamOperation{
			Kind: string(operation.Kind), Survivor: projectDreamParticipant(operation.Survivor),
			Replacement: server.DreamReplacement{Value: operation.Replacement.Value, Description: operation.Replacement.Description},
			Reason:      operation.Reason, ExactDuplicateEligible: operation.ExactDuplicateEligible,
			Sources: make([]server.DreamParticipant, len(operation.Sources)),
		}
		for j, source := range operation.Sources {
			projected.Sources[j] = projectDreamParticipant(source)
		}
		out.PlannedSourceCount += len(projected.Sources)
		out.Operations[i] = projected
	}
	return out
}

func projectDreamParticipant(participant dream.ReviewParticipant) server.DreamParticipant {
	return server.DreamParticipant{Key: participant.Key, Value: participant.Value, Description: participant.Description}
}

func cloneDreamReview(in server.DreamReview) server.DreamReview {
	out := in
	out.Operations = make([]server.DreamOperation, len(in.Operations))
	for i, operation := range in.Operations {
		out.Operations[i] = operation
		out.Operations[i].Sources = append([]server.DreamParticipant(nil), operation.Sources...)
	}
	return out
}

func buildDreamReview(cfg Config, assets catalogAssets, providerPresent bool) (server.DreamReviewer, server.DreamCapabilities) {
	caps := server.DreamCapabilities{Targets: make(map[server.DreamTarget]server.DreamTargetCapability, 2)}
	if cfg.OwnershipEnforced {
		caps.UnavailableReason = "manual dreaming is unavailable while ownership enforcement is enabled"
		caps.Targets[server.DreamTargetProjectMemory] = server.DreamTargetCapability{UnavailableReason: caps.UnavailableReason}
		caps.Targets[server.DreamTargetUserModel] = server.DreamTargetCapability{UnavailableReason: caps.UnavailableReason}
		return nil, caps
	}
	targets := make(map[server.DreamTarget]*dream.Consolidator, 2)
	add := func(target server.DreamTarget, storePresent, capable bool, consolidator *dream.Consolidator) {
		capability := server.DreamTargetCapability{}
		switch {
		case !storePresent:
			capability.UnavailableReason = "target store is unavailable"
		case !providerPresent:
			capability.UnavailableReason = "dream planner is unavailable"
		case !capable:
			capability.UnavailableReason = "target store lacks reviewed atomic consolidation"
		case consolidator == nil:
			capability.UnavailableReason = "manual dream coordinator is unavailable"
		default:
			capability.Generate, capability.Decide = true, true
			targets[target] = consolidator
		}
		caps.Targets[target] = capability
	}
	add(server.DreamTargetProjectMemory, assets.memStore != nil, dream.SupportsReviewedPlan(assets.memStore), assets.memoryDream)
	add(server.DreamTargetUserModel, assets.userModelStore != nil, dream.SupportsReviewedPlan(assets.userModelStore), assets.userModelDream)
	caps.Generate, caps.Decide = len(targets) > 0, len(targets) > 0
	if len(targets) == 0 {
		caps.UnavailableReason = "no manual dream target is available"
		return nil, caps
	}
	return newDreamReviewCoordinator(dreamReviewConfig{targets: targets}), caps
}
