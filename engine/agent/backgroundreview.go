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

// maxBackgroundReviews caps concurrent advisory checker runs per run, bounding
// provider load, cost, and held evidence on read-heavy runs.
const maxBackgroundReviews = 4

// backgroundReviews is a run's set of opted-in yolo advisory reviews. Each
// review snapshots its request and evidence on the dispatching goroutine, then
// runs the checker off the dispatch path on a context detached from the run's
// own cancellation. At most maxBackgroundReviews run at once; a review over the
// cap is skipped. Advisory verdicts never feed the shared trajectory, so
// enforcing reviews of delegated children see the same facts in dispatch order. Run end cancels them; their advisory events go out through the
// child registry's sealed emit, so none follows the run's terminal event. The
// context is created on first use, so a run without background reviews pays
// no allocations for the set.
type backgroundReviews struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	ctx    context.Context //nolint:containedctx // run-scoped review lifetime, cancelled at run end
	cancel context.CancelFunc
	closed bool
	active int
}

// reserve claims a review slot and returns the review context, creating it
// from parent on first use. It reports false once run end has begun draining
// or when maxBackgroundReviews are already running; a true return must be
// followed by start, which releases the slot.
func (b *backgroundReviews) reserve(parent context.Context) (context.Context, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.active >= maxBackgroundReviews {
		return nil, false
	}
	b.active++
	if b.ctx == nil {
		if parent == nil {
			parent = context.Background()
		}
		b.ctx, b.cancel = context.WithCancel(context.WithoutCancel(parent))
	}
	return b.ctx, true
}

// start runs a reserved review and releases its slot when it ends. It reports
// false, releasing the slot at once, when run end began after the reservation.
func (b *backgroundReviews) start(review func()) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		b.active--
		return false
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.releaseSlot()
		review()
	}()
	return true
}

func (b *backgroundReviews) releaseSlot() {
	b.mu.Lock()
	b.active--
	b.mu.Unlock()
}

// drain stops new reviews, cancels in-flight ones, and waits up to the grace for
// them to unwind. It is idempotent and reports whether every review unwound.
func (b *backgroundReviews) drain() bool {
	b.mu.Lock()
	b.closed = true
	cancel := b.cancel
	b.mu.Unlock()
	if cancel == nil {
		return true
	}
	cancel()
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
func (r *Run) launchBackgroundReview(ctx context.Context, assess func() session.AuxiliaryUsage, publish func() session.Event, release func()) {
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
	if r.reviewRoot == nil || !yoloBackgroundReview(r.reviewRoot.reviewer, sess.Mode, call.Name, ReviewJobAction) {
		return
	}
	bg, ok := r.backgroundReviews.reserve(r.ctx)
	if !ok {
		return
	}
	sessID := sess.ID
	a := e.prepareActionAssessment(bg, r, sess, env, call, true)
	r.launchBackgroundReview(bg, func() session.AuxiliaryUsage {
		assessAction(bg, r, &a)
		return a.usage
	}, func() session.Event {
		result := a.result
		if a.err != nil {
			result.Assessment = ReviewUnresolved
		}
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
	if r.reviewRoot == nil || !yoloBackgroundReview(r.reviewRoot.reviewer, sess.Mode, call.Name, ReviewJobInbound) {
		return
	}
	bg, ok := r.backgroundReviews.reserve(r.ctx)
	if !ok {
		return
	}
	sessID := sess.ID
	a := inboundAssessment{applies: true, mode: sess.Mode, snapshot: true}
	e.prepareInboundRequest(bg, r, sess, env, call, result, &a)
	r.launchBackgroundReview(bg, func() session.AuxiliaryUsage {
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
