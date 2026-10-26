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

// backgroundReviewDrainGrace bounds how long run end waits for cancelled yolo
// advisory reviews to unwind. A var only as a TEST seam, like childDrainCap.
var backgroundReviewDrainGrace = 1 * time.Second

// backgroundReviews is a run's set of opted-in yolo advisory reviews. Each
// review snapshots its request on the dispatching goroutine, then runs the
// checker off the dispatch path on a context detached from the run's own
// cancellation. Run end cancels them; their advisory events go out through the
// child registry's sealed emit, so none follows the run's terminal event.
type backgroundReviews struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	ctx    context.Context //nolint:containedctx // run-scoped review lifetime, cancelled at run end
	cancel context.CancelFunc
	closed bool
}

func (b *backgroundReviews) init(parent context.Context) {
	b.ctx, b.cancel = context.WithCancel(context.WithoutCancel(parent))
}

func (b *backgroundReviews) start(review func()) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.ctx == nil {
		return false
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		review()
	}()
	return true
}

// drain stops new reviews, cancels in-flight ones, and waits up to the grace for
// them to unwind. It is idempotent and reports whether every review unwound.
func (b *backgroundReviews) drain() bool {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	if b.cancel == nil {
		return true
	}
	b.cancel()
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(backgroundReviewDrainGrace)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (r *Run) drainBackgroundReviews(ctx context.Context) {
	if !r.backgroundReviews.drain() {
		r.diag.Log(ctx, port.LevelWarn, "abandoning yolo advisory guardrail reviews that did not stop within the run-end grace",
			"grace", backgroundReviewDrainGrace.String())
	}
}

func advisoryDisposition(result ToolReviewResult, err error, clean string) string {
	if err != nil || result.Assessment != ReviewAcceptable {
		return "pass_advisory"
	}
	return clean
}

// launchBackgroundReview runs assess off the dispatch path, records its checker
// usage, and publishes the advisory event built by publish unless run end
// cancelled the review first. release always runs once.
func (r *Run) launchBackgroundReview(assess func() session.AuxiliaryUsage, publish func() session.Event, release func()) {
	ctx := r.backgroundReviews.ctx
	if !r.backgroundReviews.start(func() {
		defer release()
		usage := assess()
		r.enqueueAuxiliaryUsageWhileActive(ctx, RemapAuxiliaryUsage(ctx, r.diag, session.UsageKindGuardrail, usage))
		if ctx.Err() == nil {
			r.children.safeEmit(publish())
		}
	}) {
		release()
	}
}

// startBackgroundActionReview reviews an opted-in yolo action without delaying
// it. The call has already been authorized and proceeds regardless of verdict.
func (e *Engine) startBackgroundActionReview(r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall) {
	if r.reviewRoot == nil || r.backgroundReviews.ctx == nil || !yoloBackgroundReview(r.reviewRoot.reviewer, sess.Mode, call.Name, ReviewJobAction) {
		return
	}
	bg, sessID := r.backgroundReviews.ctx, sess.ID
	a := e.prepareActionAssessment(bg, r, sess, env, call)
	r.launchBackgroundReview(func() session.AuxiliaryUsage {
		assessAction(bg, r, &a)
		return a.usage
	}, func() session.Event {
		result, decision := a.result, string(a.result.Assessment)
		if a.err != nil {
			result.Assessment, decision = ReviewUnresolved, "unresolved"
		}
		r.reviewRoot.record(reviewFact(call, a.action.request.Target, decision))
		r.publishActionDetail(bg, sessID, a.action.request.ReviewID, a.action.request, result, a.err)
		guardrail := reviewMachinePayload(r.reviewRoot.reviewer, a.action.request, result, a.err, advisoryDisposition(result, a.err, "execute"))
		return session.Event{Type: session.EvHook, Turn: turnIdx, Hook: &session.HookPayload{Phase: string(governance.PhasePreToolUse), Tool: call.Name, Decision: session.HookInfo, CallID: call.ID, Guardrail: guardrail}}
	}, func() {
		a.cancel()
		if a.action.close != nil {
			a.action.close()
		}
	})
}

// startBackgroundInboundReview inspects an opted-in yolo result that has
// already been released to the model; the verdict is advisory only.
func (e *Engine) startBackgroundInboundReview(r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, result session.ToolResult) {
	if r.reviewRoot == nil || r.backgroundReviews.ctx == nil || !yoloBackgroundReview(r.reviewRoot.reviewer, sess.Mode, call.Name, ReviewJobInbound) {
		return
	}
	bg, sessID := r.backgroundReviews.ctx, sess.ID
	a := inboundAssessment{applies: true, mode: sess.Mode}
	e.prepareInboundRequest(bg, r, sess, env, call, result, &a)
	r.launchBackgroundReview(func() session.AuxiliaryUsage {
		assessInbound(bg, r, &a)
		return a.usage
	}, func() session.Event {
		r.publishInboundDetail(bg, sessID, a)
		return inboundReviewEvent(r, turnIdx, call, a, advisoryDisposition(a.result, a.err, "release_result"))
	}, func() {
		a.cancel()
		if a.close != nil {
			a.close()
		}
	})
}
