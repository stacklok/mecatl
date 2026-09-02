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
					c.queued.Delete(id)
					c.drive(id)
				}
			}
		}()
	}
	return c
}

func (c *titleCoordinator) Submit(id session.SessionID) {
	if _, loaded := c.queued.LoadOrStore(id, struct{}{}); loaded {
		return
	}
	select {
	case <-c.ctx.Done():
		c.queued.Delete(id)
	case c.queue <- id:
	default:
		// A full queue changes no durable state. The next eligible exchange can retry.
		c.queued.Delete(id)
	}
}

func (c *titleCoordinator) Close() {
	c.cancel()
	c.wg.Wait()
}

func (s *Service) submitTitleGeneration(id session.SessionID) {
	if s.titleCoordinator != nil {
		s.titleCoordinator.Submit(id)
	}
}

func (c *titleCoordinator) drive(id session.SessionID) {
	sources, attemptID, selector, claimed := c.claim(id)
	if !claimed {
		return
	}
	generator := c.generator
	if c.svc.cfg.TitleGeneratorForSession != nil {
		generator = c.svc.cfg.TitleGeneratorForSession(selector)
	}
	if generator == nil {
		c.commit(id, attemptID, TitleGenerationResult{Outcome: session.TitleAttemptInterrupted})
		return
	}
	result := generator.Generate(c.ctx, sources)
	c.commit(id, attemptID, result)
}

// claim persists an incomplete attempt before provider I/O. Thus a process death
// after the save has an explicit unknown outcome rather than risking rebilling it.
func (c *titleCoordinator) claim(id session.SessionID) ([]string, string, ProviderSelector, bool) {
	unlock := c.svc.runEntryMu.lock(id)
	defer unlock()
	release, err := c.svc.acquireMutationLease(c.ctx, id)
	if err != nil {
		return nil, "", ProviderSelector{}, false
	}
	defer release()
	sess, err := c.svc.cfg.Store.Load(c.ctx, id)
	if err != nil || sess == nil || sess.TitleGeneration != session.TitleGenerationPending || sess.TitleProvenance == session.TitleProvenanceOperator {
		return nil, "", ProviderSelector{}, false
	}
	attempts := sess.TitleAttempts()
	if len(attempts) > 0 && attempts[len(attempts)-1].Outcome == "" {
		attempts[len(attempts)-1].Outcome = session.TitleAttemptInterrupted
		sess.RestoreTitleMetadata(session.TitleGenerationExhausted, sess.TitleSourcePrompts(), attempts, sess.AuxiliaryUsage())
		c.persistTitle(c.ctx, sess)
		return nil, "", ProviderSelector{}, false
	}
	sources := sess.TitleSourcePrompts()
	if len(sources) == 0 {
		return nil, "", ProviderSelector{}, false
	}
	attemptID := fmt.Sprintf("title-%d", c.attemptID.Add(1))
	sess.RecordTitleAttempt(session.TitleAttempt{ID: attemptID, CreatedAt: c.svc.cfg.Now()})
	if !c.persistTitle(c.ctx, sess) {
		return nil, "", ProviderSelector{}, false
	}
	return sources, attemptID, ProviderSelector{ProviderID: sess.ProviderID, ModelID: sess.ModelID}, true
}

func (c *titleCoordinator) commit(id session.SessionID, attemptID string, result TitleGenerationResult) {
	unlock := c.svc.runEntryMu.lock(id)
	defer unlock()
	release, err := c.svc.acquireMutationLease(c.ctx, id)
	if err != nil {
		return
	}
	defer release()
	sess, err := c.svc.cfg.Store.Load(c.ctx, id)
	if err != nil || sess == nil || sess.TitleGeneration != session.TitleGenerationPending || sess.TitleProvenance == session.TitleProvenanceOperator {
		return
	}
	attempts := sess.TitleAttempts()
	if len(attempts) == 0 || attempts[len(attempts)-1].ID != attemptID || attempts[len(attempts)-1].Outcome != "" {
		return
	}
	attempts[len(attempts)-1].Outcome = result.Outcome
	sess.RestoreTitleMetadata(sess.TitleGeneration, sess.TitleSourcePrompts(), attempts, sess.AuxiliaryUsage())
	sess.RecordAuxiliaryUsage(session.AuxiliaryUsage{Operation: session.AuxiliaryOperationSessionTitle, ProviderID: result.ProviderID, ModelID: result.ModelID, Usage: result.Usage, RecordedAt: c.svc.cfg.Now(), Outcome: result.Outcome})

	switch result.Outcome {
	case session.TitleAttemptSucceeded:
		if sess.SetGeneratedTitle(result.Title) != nil {
			sess.SetTitleGeneration(session.TitleGenerationExhausted)
		}
	case session.TitleAttemptDeferred:
		if len(sess.TitleSourcePrompts()) >= 3 {
			sess.SetTitleGeneration(session.TitleGenerationExhausted)
		}
	case session.TitleAttemptFailed:
		if len(attempts) < 2 && result.Retryable {
			sess.SetTitleGeneration(session.TitleGenerationPending)
			if c.persistTitle(c.ctx, sess) {
				go c.retry(id)
			}
			return
		}
		sess.SetTitleGeneration(session.TitleGenerationExhausted)
	default:
		sess.SetTitleGeneration(session.TitleGenerationExhausted)
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
		s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "session title event append failed", "session", string(sess.ID), "error", err)
	}
	s.PublishSessionEvent(sess.ID, ev)
}
