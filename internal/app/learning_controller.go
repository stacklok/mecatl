package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

const (
	learningCompletedTTL          = 24 * time.Hour
	learningCompletedMax          = 1024
	learningCooldownMax           = 1024
	defaultReflectionOutputTokens = 4096
)

type learningReservation struct {
	at        time.Time
	tokens    int
	principal string
}
type learningCompleted struct {
	digest string
	at     time.Time
}

type automaticAdmissionController struct {
	mu             sync.Mutex
	cfg            LearningAutomaticConfig
	now            func() time.Time
	reservations   []learningReservation
	cooldowns      map[string]time.Time
	completed      map[string]time.Time
	completedOrder []learningCompleted
	emit           func(learning.Activity)
}

func newAutomaticAdmissionController(cfg LearningAutomaticConfig, emit func(learning.Activity)) *automaticAdmissionController {
	return &automaticAdmissionController{cfg: cfg, now: time.Now, cooldowns: make(map[string]time.Time), completed: make(map[string]time.Time), emit: emit}
}

func (c *automaticAdmissionController) activity(kind learning.ActivityKind, reason learning.AdmissionReason, sensitivity learning.Sensitivity, count int64) {
	if c != nil && c.emit != nil {
		c.emit(learning.Activity{Kind: kind, Reason: reason, Sensitivity: sensitivity, Count: count})
	}
}

func automaticTrajectoryDigest(principal string, in learning.Input) (string, error) {
	tr := in.Trajectory
	messages := tr.Messages
	if tr.Current.Valid(len(messages)) {
		messages = messages[tr.Current.Start:tr.Current.End]
	}
	canonical := learning.NewTrajectory(tr.SessionID, "", tr.Stop, tr.Usage, messages)
	canonical.Kind, canonical.Counters = tr.Kind, tr.Counters
	canonical.Current = learning.MessageSpan{Start: 0, End: len(messages)}
	projected, err := learning.ProjectInput(learning.NewInput(canonical, nil, nil, nil))
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Principal string `json:"principal"`
		Kind      string `json:"kind"`
		Counters  any    `json:"counters"`
		Usage     any    `json:"usage"`
		Evidence  any    `json:"evidence"`
	}{principal, string(tr.Kind), tr.Counters, tr.Usage, projected})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (c *automaticAdmissionController) pruneLocked(now time.Time) {
	cutoff := now.Add(-c.cfg.Window)
	kept := c.reservations[:0]
	for _, item := range c.reservations {
		if !item.at.Before(cutoff) {
			kept = append(kept, item)
		}
	}
	c.reservations = kept
	for principal, until := range c.cooldowns {
		if !now.Before(until) {
			delete(c.cooldowns, principal)
		}
	}
	completedCutoff := now.Add(-learningCompletedTTL)
	keptCompleted := c.completedOrder[:0]
	for _, item := range c.completedOrder {
		if item.at.Before(completedCutoff) {
			if c.completed[item.digest].Equal(item.at) {
				delete(c.completed, item.digest)
			}
			continue
		}
		keptCompleted = append(keptCompleted, item)
	}
	c.completedOrder = keptCompleted
}

// reserve runs only after coordinator capacity and in-flight duplicate checks.
// Every accepted reservation remains consumed regardless of the job outcome.
func (c *automaticAdmissionController) reserve(principal, digest string, tokens int, class learning.AdmissionClass, sensitivity learning.Sensitivity) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.pruneLocked(now)
	if _, ok := c.completed[digest]; ok {
		c.activity(learning.ActivityDuplicate, learning.ReasonDuplicate, sensitivity, 1)
		return false
	}
	if class != learning.AdmissionHard && c.cfg.Cooldown > 0 {
		if until := c.cooldowns[principal]; now.Before(until) {
			c.activity(learning.ActivityRateLimited, learning.ReasonRateLimit, sensitivity, 1)
			return false
		}
	}
	globalCount, globalTokens, principalCount, principalTokens := 0, 0, 0, 0
	for _, item := range c.reservations {
		globalCount++
		globalTokens += item.tokens
		if item.principal == principal {
			principalCount++
			principalTokens += item.tokens
		}
	}
	if c.cfg.MaxReflections == 0 || c.cfg.MaxTokens == 0 || c.cfg.MaxReflectionsPerPrincipal == 0 || c.cfg.MaxTokensPerPrincipal == 0 ||
		globalCount >= c.cfg.MaxReflections || globalTokens+tokens > c.cfg.MaxTokens ||
		principalCount >= c.cfg.MaxReflectionsPerPrincipal || principalTokens+tokens > c.cfg.MaxTokensPerPrincipal {
		c.activity(learning.ActivityRateLimited, learning.ReasonRateLimit, sensitivity, 1)
		return false
	}
	c.reservations = append(c.reservations, learningReservation{at: now, tokens: tokens, principal: principal})
	if class != learning.AdmissionHard && c.cfg.Cooldown > 0 {
		c.setCooldownLocked(principal, now.Add(c.cfg.Cooldown))
	}
	reason := learning.ReasonWeightedThreshold
	if class == learning.AdmissionHard {
		reason = learning.ReasonHardTrigger
	}
	c.activity(learning.ActivityReservedTokens, reason, sensitivity, int64(tokens))
	return true
}

func (c *automaticAdmissionController) setCooldownLocked(principal string, until time.Time) {
	if len(c.cooldowns) >= learningCooldownMax {
		oldestPrincipal := ""
		var oldest time.Time
		for candidate, candidateUntil := range c.cooldowns {
			if oldestPrincipal == "" || candidateUntil.Before(oldest) || candidateUntil.Equal(oldest) && candidate < oldestPrincipal {
				oldestPrincipal, oldest = candidate, candidateUntil
			}
		}
		delete(c.cooldowns, oldestPrincipal)
	}
	c.cooldowns[principal] = until
}

func (c *automaticAdmissionController) complete(digest string) {
	if c == nil || digest == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.pruneLocked(now)
	c.completed[digest] = now
	c.completedOrder = append(c.completedOrder, learningCompleted{digest: digest, at: now})
	for len(c.completedOrder) > learningCompletedMax {
		old := c.completedOrder[0]
		c.completedOrder = c.completedOrder[1:]
		if c.completed[old.digest].Equal(old.at) {
			delete(c.completed, old.digest)
		}
	}
}
