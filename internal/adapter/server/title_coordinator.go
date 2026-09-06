package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	titleCoordinatorWorkers = 2
	titleCoordinatorQueue   = 64
	titleReconcileLimit     = 64
	titleRetryBackoff       = 5 * time.Second
)

// titleCoordinator is deliberately Service-owned: title generation is not an
// agent run and may neither hold a chat run's lock nor alter its accounting.
type titleCoordinator struct {
	svc       *Service
	generator SessionTitleGenerator
	ctx       context.Context
	cancel    context.CancelFunc
	queue     chan session.SessionID
	wg        sync.WaitGroup
	retryWG   sync.WaitGroup
	queued    sync.Map
	attemptID atomic.Uint64
}

func buildTitleCoordinator(svc *Service, cfg Config) *titleCoordinator {
	if cfg.TitleGenerator == nil && cfg.TitleGeneratorForSession == nil {
		return nil
	}
	return newTitleCoordinator(svc, cfg.TitleGenerator)
}

func newTitleCoordinator(svc *Service, generator SessionTitleGenerator) *titleCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	c := &titleCoordinator{svc: svc, generator: generator, ctx: ctx, cancel: cancel, queue: make(chan session.SessionID, titleCoordinatorQueue)}
	for range titleCoordinatorWorkers {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-c.queue:
					c.drive(id)
					c.queued.Delete(id)
				}
			}
		}()
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.svc.reconcilePendingTitles(ctx, c)
	}()
	return c
}

func (c *titleCoordinator) Submit(id session.SessionID) {
	c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation submitted")
	if _, loaded := c.queued.LoadOrStore(id, struct{}{}); loaded {
		c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation submission skipped", "reason", "already_queued")
		return
	}
	select {
	case <-c.ctx.Done():
		c.queued.Delete(id)
		c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation submission skipped", "reason", "coordinator_stopped")
	case c.queue <- id:
		c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation admitted")
	default:
		// A full queue changes no durable state. The next eligible exchange can retry.
		c.queued.Delete(id)
		c.diagnostics(id).Log(context.Background(), port.LevelWarn, "session title generation submission skipped", "reason", "queue_full")
	}
}

func (c *titleCoordinator) diagnostics(id session.SessionID) port.Diagnostics {
	return c.svc.cfg.Diagnostics.With("session", string(id), "operation", "session_title")
}

func (c *titleCoordinator) Close() {
	c.cancel()
	c.wg.Wait()
	c.retryWG.Wait()
}

func (s *Service) submitTitleGeneration(id session.SessionID) {
	if s.titleCoordinator == nil {
		s.cfg.Diagnostics.With("session", string(id), "operation", "session_title").Log(context.Background(), port.LevelDebug, "session title generation submission skipped", "reason", "no_coordinator")
		return
	}
	s.titleCoordinator.Submit(id)
}

// reconcilePendingTitles admits a bounded set of completed snapshots stranded
// before terminal relay persistence was wired. It deliberately skips any attempt
// record: an empty outcome may have crossed the durable claim before a crash, so
// retrying it could bill a second provider call.
func (s *Service) reconcilePendingTitles(parent context.Context, coordinator *titleCoordinator) {
	if coordinator == nil {
		return
	}
	pager, ok := s.cfg.Store.(port.SessionMetadataPager)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	page, err := pager.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: titleReconcileLimit})
	if err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session title reconciliation failed", "operation", "session_title", "reason", "metadata_page_failed")
		return
	}
	for _, meta := range page.Sessions {
		if meta.State != session.StateCompleted {
			continue
		}
		sess, err := s.cfg.Store.Load(ctx, meta.ID)
		if err != nil || sess == nil || sess.TitleGeneration != session.TitleGenerationPending || sess.TitleProvenance == session.TitleProvenanceOperator || len(sess.TitleSourcePrompts()) == 0 || len(sess.TitleAttempts()) != 0 {
			continue
		}
		coordinator.Submit(sess.ID)
	}
}

func (c *titleCoordinator) drive(id session.SessionID) {
	c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation claim started")
	sources, attemptID, selector, claimed, reason := c.claim(id)
	if !claimed {
		c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation claim skipped", "reason", reason)
		return
	}
	c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation claimed", "attempt", attemptID)
	generator := c.generator
	if c.svc.cfg.TitleGeneratorForSession != nil {
		generator = c.svc.cfg.TitleGeneratorForSession(selector)
	}
	if generator == nil {
		result := TitleGenerationResult{Outcome: session.TitleAttemptInterrupted, FailureClass: titleFailureProvider, FailureStage: titleStageEstablishment}
		c.diagnostics(id).Log(context.Background(), port.LevelWarn, "session title generator unavailable", "attempt", attemptID, "provider", selector.ProviderID, "model", selector.ModelID)
		c.logCompletion(id, attemptID, result)
		c.commit(id, attemptID, result)
		return
	}
	c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generator selected", "attempt", attemptID, "provider", selector.ProviderID, "model", selector.ModelID)
	result := generator.Generate(c.ctx, sources)
	c.logCompletion(id, attemptID, result)
	c.commit(id, attemptID, result)
}

// claim persists an incomplete attempt before provider I/O. Thus a process death
// after the save has an explicit unknown outcome rather than risking rebilling it.
func (c *titleCoordinator) claim(id session.SessionID) ([]string, string, ProviderSelector, bool, string) {
	unlock := c.svc.runEntryMu.lock(id)
	defer unlock()
	release, err := c.svc.acquireMutationLease(c.ctx, id)
	if err != nil {
		return nil, "", ProviderSelector{}, false, "mutation_lease_unavailable"
	}
	defer release()
	sess, err := c.svc.cfg.Store.Load(c.ctx, id)
	if err != nil {
		return nil, "", ProviderSelector{}, false, "session_load_failed"
	}
	if sess == nil || sess.TitleGeneration != session.TitleGenerationPending || sess.TitleProvenance == session.TitleProvenanceOperator {
		return nil, "", ProviderSelector{}, false, "ineligible"
	}
	attempts := sess.TitleAttempts()
	if len(attempts) > 0 && attempts[len(attempts)-1].Outcome == "" {
		attempts[len(attempts)-1].Outcome = session.TitleAttemptInterrupted
		sess.ApplyTitleGeneration(session.TitleGenerationExhausted, attempts)
		c.persistTitle(c.ctx, sess)
		return nil, "", ProviderSelector{}, false, "incomplete_attempt"
	}
	sources := sess.TitleSourcePrompts()
	if len(sources) == 0 {
		return nil, "", ProviderSelector{}, false, "no_sources"
	}
	attemptID := c.nextAttemptID(attempts)
	sess.ApplyTitleGeneration(sess.TitleGeneration, append(attempts, session.TitleAttempt{ID: attemptID}))
	if !c.persistTitle(c.ctx, sess) {
		return nil, "", ProviderSelector{}, false, "claim_persist_failed"
	}
	return sources, attemptID, ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID}, true, "claimed"
}

func (c *titleCoordinator) nextAttemptID(attempts []session.TitleAttempt) string {
	var persisted uint64
	for _, attempt := range attempts {
		var n uint64
		if _, err := fmt.Sscanf(attempt.ID, "title-%d", &n); err == nil && fmt.Sprintf("title-%d", n) == attempt.ID && n > persisted {
			persisted = n
		}
	}
	for {
		current := c.attemptID.Load()
		next := persisted + 1
		if next <= current {
			next = current + 1
		}
		if c.attemptID.CompareAndSwap(current, next) {
			return fmt.Sprintf("title-%d", next)
		}
	}
}

func (c *titleCoordinator) logCompletion(id session.SessionID, attemptID string, result TitleGenerationResult) {
	level := port.LevelDebug
	if result.Outcome == session.TitleAttemptFailed || result.Outcome == session.TitleAttemptInterrupted {
		level = port.LevelWarn
	}
	args := []any{
		"attempt", attemptID,
		"outcome", string(result.Outcome),
		"provider", result.ProviderID,
		"model", result.ModelID,
		"input_tokens", result.Usage.InputTokens,
		"output_tokens", result.Usage.OutputTokens,
		"cache_read_tokens", result.Usage.CacheReadTokens,
		"cache_write_tokens", result.Usage.CacheWriteTokens,
	}
	class, stage := result.FailureClass, result.FailureStage
	if (result.Outcome == session.TitleAttemptFailed || result.Outcome == session.TitleAttemptInterrupted) && class == "" {
		class, stage = titleFailureClassFor(result.Err), titleStageUnknown
	}
	if class != "" {
		args = append(args, "failure_class", string(class), "failure_stage", string(stage))
	}
	c.diagnostics(id).Log(context.Background(), level, "session title generator completed", args...)
}

func (c *titleCoordinator) commit(id session.SessionID, attemptID string, result TitleGenerationResult) {
	unlock := c.svc.runEntryMu.lock(id)
	defer unlock()
	release, err := c.svc.acquireMutationLease(c.ctx, id)
	if err != nil {
		c.diagnostics(id).Log(context.Background(), port.LevelWarn, "session title generation commit lost", "attempt", attemptID, "reason", "mutation_lease_unavailable")
		return
	}
	defer release()
	sess, err := c.svc.cfg.Store.Load(c.ctx, id)
	if err != nil {
		c.diagnostics(id).Log(context.Background(), port.LevelWarn, "session title generation commit lost", "attempt", attemptID, "reason", "session_load_failed")
		return
	}
	if sess == nil || sess.TitleGeneration != session.TitleGenerationPending || sess.TitleProvenance == session.TitleProvenanceOperator {
		c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation commit lost", "attempt", attemptID, "reason", "conditional_state_changed")
		return
	}
	attempts := sess.TitleAttempts()
	if len(attempts) == 0 || attempts[len(attempts)-1].ID != attemptID || attempts[len(attempts)-1].Outcome != "" {
		c.diagnostics(id).Log(context.Background(), port.LevelDebug, "session title generation commit lost", "attempt", attemptID, "reason", "conditional_attempt_changed")
		return
	}
	attempts[len(attempts)-1].Outcome = result.Outcome
	generation := sess.TitleGeneration
	retry := false
	switch result.Outcome {
	case session.TitleAttemptSucceeded:
		if sess.SetGeneratedTitle(result.Title) != nil {
			generation = session.TitleGenerationExhausted
		} else {
			generation = session.TitleGenerationGenerated
		}
	case session.TitleAttemptDeferred:
		if len(sess.TitleSourcePrompts()) >= 3 {
			generation = session.TitleGenerationExhausted
		}
	case session.TitleAttemptFailed:
		if len(attempts) < 2 && result.Retryable {
			retry = true
		} else {
			generation = session.TitleGenerationExhausted
		}
	default:
		generation = session.TitleGenerationExhausted
	}
	sess.ApplyTitleGeneration(generation, attempts)
	sess.RecordTokenUsage(session.UsageKindSessionTitle, result.ProviderID, result.ModelID, result.Usage)
	if retry {
		c.retryWG.Add(1)
		if c.persistTitle(c.ctx, sess) {
			go func() {
				defer c.retryWG.Done()
				c.retry(id)
			}()
		} else {
			c.retryWG.Done()
		}
		return
	}
	c.persistTitle(c.ctx, sess)
}

func (c *titleCoordinator) retry(id session.SessionID) {
	timer := time.NewTimer(titleRetryBackoff)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
	case <-timer.C:
		c.Submit(id)
	}
}

// persistTitle maintains the required durability order: snapshot first, then
// durable event, then best-effort live publication. Payload construction is
// source-free and all externally visible strings are UTF-8 repaired by mapper.
func (c *titleCoordinator) persistTitle(ctx context.Context, sess *session.Session) bool {
	if err := c.svc.cfg.Store.Save(ctx, sess); err != nil {
		return false
	}
	c.svc.publishTitle(ctx, sess)
	return true
}

func (s *Service) publishTitle(ctx context.Context, sess *session.Session) {
	payload := titlePayload(sess)
	ev := session.Event{Type: session.EvSessionTitle, Title: &payload}
	if err := s.appendEvent(context.WithoutCancel(ctx), sess.ID, ev); err != nil {
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session title event append failed", "session", string(sess.ID), "operation", "session_title", "reason", "event_append_failed")
	}
	s.PublishSessionEvent(sess.ID, ev)
}
