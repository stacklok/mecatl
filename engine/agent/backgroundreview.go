package agent

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// backgroundReviewDrainCap bounds how long run end waits for in-flight yolo
// advisory reviews before cancelling them, and backgroundReviewDrainGrace how
// long it then waits for cancelled reviews to unwind. Vars only as TEST seams,
// like childDrainCap.
var (
	backgroundReviewDrainCap   = 10 * time.Second
	backgroundReviewDrainGrace = 1 * time.Second
)

// backgroundReviews is a run's set of opted-in yolo advisory reviews. Reviews
// start on the dispatching goroutine after their request is snapshotted, run
// the checker off the dispatch path, and publish only before drain seals the set,
// so no advisory event follows the run's terminal event.
type backgroundReviews struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	sealed bool
}

// context returns the set's review context, detached from the run's
// cancellation so a review outlives the tool call it observes until drain.
func (b *backgroundReviews) context(parent context.Context) (context.Context, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	if b.ctx == nil {
		b.ctx, b.cancel = context.WithCancel(context.WithoutCancel(parent))
	}
	return b.ctx, true
}

func (b *backgroundReviews) start(fn func()) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		fn()
	}()
	return true
}

// publish runs fn unless drain has sealed the set or cancelled its context.
func (b *backgroundReviews) publish(fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sealed || b.ctx.Err() != nil {
		return
	}
	fn()
}

// drain stops new reviews, waits up to the cap for in-flight ones, cancels the
// rest, and seals publication. It reports whether any review failed to unwind.
func (b *backgroundReviews) drain() bool {
	b.mu.Lock()
	b.closed = true
	cancel := b.cancel
	b.mu.Unlock()
	abandoned := false
	if cancel != nil {
		if !waitGroupWithin(&b.wg, backgroundReviewDrainCap) {
			cancel()
			abandoned = !waitGroupWithin(&b.wg, backgroundReviewDrainGrace)
		}
		cancel()
	}
	b.mu.Lock()
	b.sealed = true
	b.mu.Unlock()
	return abandoned
}

func waitGroupWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (r *Run) drainBackgroundReviews(ctx context.Context) {
	if r.backgroundReviews.drain() {
		r.diag.Log(ctx, port.LevelWarn, "abandoning yolo advisory guardrail reviews that did not stop within the run-end drain cap",
			"cap", (backgroundReviewDrainCap + backgroundReviewDrainGrace).String())
	}
}

func advisoryDisposition(result ToolReviewResult, err error, clean string) string {
	if err != nil || result.Assessment != ReviewAcceptable {
		return "pass_advisory"
	}
	return clean
}

// startBackgroundActionReview reviews an opted-in yolo action without delaying
// it. The call has already been authorized and proceeds regardless of verdict.
func (e *Engine) startBackgroundActionReview(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall) {
	if r.reviewRoot == nil || !yoloBackgroundReview(r.reviewRoot.reviewer, sess.Mode, call.Name, ReviewJobAction) {
		return
	}
	bg, ok := r.backgroundReviews.context(ctx)
	if !ok {
		return
	}
	assessment := e.prepareActionAssessment(bg, r, sess, env, call)
	sessID := sess.ID
	started := r.backgroundReviews.start(func() {
		if assessment.action.close != nil {
			defer assessment.action.close()
		}
		assessAction(bg, r, &assessment)
		r.enqueueAuxiliaryUsageWhileActive(bg, RemapAuxiliaryUsage(bg, r.diag, session.UsageKindGuardrail, assessment.usage))
		result := assessment.result
		decision := string(result.Assessment)
		if assessment.err != nil {
			decision = "unresolved"
			result.Assessment = ReviewUnresolved
		}
		r.backgroundReviews.publish(func() {
			r.reviewRoot.record(reviewFact(call, assessment.action.request.Target, decision))
			r.publishActionDetail(bg, sessID, assessment.action.request.ReviewID, assessment.action.request, result, assessment.err)
			disposition := advisoryDisposition(result, assessment.err, "execute")
			e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Hook: &session.HookPayload{Phase: string(governance.PhasePreToolUse), Tool: call.Name, Decision: session.HookInfo, CallID: call.ID, Guardrail: reviewMachinePayload(r.reviewRoot.reviewer, assessment.action.request, result, assessment.err, disposition)}})
		})
	})
	if !started {
		assessment.cancel()
		if assessment.action.close != nil {
			assessment.action.close()
		}
	}
}

// startBackgroundInboundReview inspects an opted-in yolo result that has
// already been released to the model; the verdict is advisory only.
func (e *Engine) startBackgroundInboundReview(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, result session.ToolResult) {
	if r.reviewRoot == nil || !yoloBackgroundReview(r.reviewRoot.reviewer, sess.Mode, call.Name, ReviewJobInbound) {
		return
	}
	bg, ok := r.backgroundReviews.context(ctx)
	if !ok {
		return
	}
	assessment := inboundAssessment{applies: true, mode: sess.Mode}
	e.prepareInboundRequest(bg, r, sess, env, call, result, &assessment)
	sessID := sess.ID
	started := r.backgroundReviews.start(func() {
		if assessment.close != nil {
			defer assessment.close()
		}
		assessInbound(bg, r, &assessment)
		r.enqueueAuxiliaryUsageWhileActive(bg, RemapAuxiliaryUsage(bg, r.diag, session.UsageKindGuardrail, assessment.usage))
		r.backgroundReviews.publish(func() {
			r.publishInboundDetail(bg, sessID, assessment)
			e.emitInboundReview(r, turnIdx, call, assessment, advisoryDisposition(assessment.result, assessment.err, "release_result"))
		})
	})
	if !started {
		assessment.cancel()
		if assessment.close != nil {
			assessment.close()
		}
	}
}
