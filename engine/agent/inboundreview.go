package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const (
	maxHeldResults     = 32
	maxHeldResultBytes = int64(2 * 1024 * 1024)
	withheldResultText = "tool result withheld by contextual guardrail; the already-produced content was destroyed and the tool was not re-executed"
)

type heldResultKey struct {
	reviewID string
	session  session.SessionID
	call     session.ToolCallID
	env      session.EnvironmentRef
}

type heldResult struct {
	key    heldResultKey
	askID  string
	result session.ToolResult
	bytes  int64
}

type inboundAssessment struct {
	request           ToolReviewRequest
	result            ToolReviewResult
	usage             session.AuxiliaryUsage
	source            ReviewEvidenceSource
	close             func()
	err               error
	applies           bool
	enforce           bool
	principalRevision uint64
}

func reviewPolicy(reviewer ToolReviewer, toolName string, job ReviewJob, operationalFailure bool) (bool, bool) {
	if policy, ok := reviewer.(ReviewPolicyProvider); ok {
		return policy.GuardrailReviewPolicy(toolName, job, operationalFailure)
	}
	// ToolReviewer predated inbound escrow. Reviewers that do not explicitly
	// advertise policy retain action-only behavior rather than unexpectedly
	// inspecting and holding every tool result.
	return job == ReviewJobAction, true
}

func (e *Engine) prepareInboundAssessment(r *Run, sess *session.Session, env tool.Environment, call session.ToolCall, result session.ToolResult) inboundAssessment {
	if r.reviewRoot == nil || r.reviewRoot.reviewer == nil {
		return inboundAssessment{}
	}
	applies, enforce := reviewPolicy(r.reviewRoot.reviewer, call.Name, ReviewJobInbound, false)
	assessment := inboundAssessment{applies: applies, enforce: enforce}
	if !applies {
		return assessment
	}
	e.establishReviewPrincipal(r)
	principal, principalComplete, principalRevision := r.reviewRoot.principalSnapshotWithRevision()
	assessment.principalRevision = principalRevision
	trajectory, trajectoryComplete := r.reviewRoot.snapshot()
	input, _ := json.Marshal(struct {
		Args    json.RawMessage `json:"args"`
		Content string          `json:"content"`
		IsError bool            `json:"is_error"`
	}{Args: call.Args, Content: postHookContent(result), IsError: result.IsError})
	target, _ := reviewTarget(call)
	assessment.request = ToolReviewRequest{
		ReviewID: fmt.Sprintf("%s:%s:inbound", sess.ID, call.ID), Job: ReviewJobInbound,
		Event:          governance.HookEvent{Phase: governance.PhasePostToolUse, Tool: call.Name, Input: input, SessionID: string(sess.ID), CallID: string(call.ID)},
		EffectiveCall:  session.NewToolCall(call.ID, call.Name, append(json.RawMessage(nil), call.Args...)),
		PrincipalFacts: principal, PrincipalFactsComplete: principalComplete,
		Caller: reviewCaller(e, r, sess), Environment: env.Ref(), Target: target,
		Trajectory: trajectory, TrajectoryComplete: trajectoryComplete,
		Capacity: ReviewCapacity{MaxEvidenceHandles: defaultReviewEvidenceHandles, MaxEvidenceBytes: defaultReviewEvidenceBytes, MaxTrajectoryFacts: defaultReviewTrajectoryFacts, MaxTrajectoryBytes: defaultReviewTrajectoryBytes},
	}
	prepared, prepareErr := e.prepareReviewEvidence(r.ctx, r, sess, env, assessment.request, &result)
	assessment.request.Evidence, assessment.request.EvidenceComplete = prepared.Evidence, prepared.Complete
	assessment.source, assessment.close, assessment.err = prepared.Source, prepared.Close, prepareErr
	return assessment
}

func assessInbound(ctx context.Context, r *Run, assessment *inboundAssessment) {
	if assessment == nil || !assessment.applies {
		return
	}
	if assessment.err != nil {
		assessment.result.Assessment = ReviewUnresolved
		recordReviewFailure(r.reviewRoot.reviewer, assessment.result, assessment.err)
		_, assessment.enforce = reviewPolicy(r.reviewRoot.reviewer, assessment.request.EffectiveCall.Name, ReviewJobInbound, true)
		var terminal GuardrailReviewTerminalFailure
		if errors.As(assessment.err, &terminal) && terminal.GuardrailReviewTerminalFailure() {
			assessment.enforce = true
		}
		return
	}
	assessment.result, assessment.usage, assessment.err = r.reviewRoot.reviewer.Review(ctx, assessment.request, assessment.source)
	if assessment.err == nil && !validReviewAssessment(assessment.result.Assessment) {
		assessment.result.Assessment = ReviewUnresolved
		assessment.err = reviewFailure(ReviewFailureInvalidAssessment)
	}
	if assessment.err != nil {
		_, assessment.enforce = reviewPolicy(r.reviewRoot.reviewer, assessment.request.EffectiveCall.Name, ReviewJobInbound, true)
		var terminal GuardrailReviewTerminalFailure
		if errors.As(assessment.err, &terminal) && terminal.GuardrailReviewTerminalFailure() {
			assessment.enforce = true
		}
	}
}

func (r *Run) publishInboundDetail(ctx context.Context, sessID session.SessionID, assessment inboundAssessment) {
	if r.reviewRoot.details == nil {
		return
	}
	if assessment.err != nil {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessID, ReviewID: assessment.request.ReviewID, Concern: reviewFailureConcern(reviewFailureCode(assessment.err)), NextAction: "Release this already-produced result once or cancel. The tool is not run again."})
		return
	}
	if assessment.result.Assessment == ReviewAcceptable {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessID, ReviewID: assessment.request.ReviewID, Concern: "Inspection completed; no attempted redirection or authority crossing was found.", NextAction: "The existing result is released without running the tool again."})
		return
	}
	if len(assessment.result.Concerns) == 0 && len(assessment.result.Missing) == 0 {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessID, ReviewID: assessment.request.ReviewID, Concern: "Inspection was unresolved without a specific unsafe finding.", NextAction: "Release this already-produced result once or cancel. The tool is not run again."})
		return
	}
	for _, concern := range assessment.result.Concerns {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessID, ReviewID: assessment.request.ReviewID, Concern: reviewConcernDisplay(concern), SourceDisplay: reviewSourceDisplay(assessment.request, concern.SourceRef), NextAction: "Release this already-produced result once or cancel. The tool is not run again."})
	}
	for _, missing := range assessment.result.Missing {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessID, ReviewID: assessment.request.ReviewID, Concern: "Inspection was unresolved because required evidence was unavailable.", SourceDisplay: missing.Ref, NextAction: "Release this already-produced result once or cancel. The tool is not run again."})
	}
}

func inboundNeedsHold(a inboundAssessment) bool {
	if !a.applies || !a.enforce {
		return false
	}
	return a.err != nil || a.result.Assessment != ReviewAcceptable
}

func heldResultSize(result session.ToolResult) int64 {
	n := int64(len(result.Content))
	for _, part := range result.Parts {
		n += int64(len(part.MIMEType) + len(part.Data) + len(part.URL) + len(part.Text) + len(part.Name) + len(part.Title) + len(part.Description) + len(part.LastModified))
		for _, audience := range part.Audience {
			n += int64(len(audience))
		}
	}
	return n
}

func cloneToolResult(result session.ToolResult) session.ToolResult {
	out := result
	out.Parts = append([]session.Content(nil), result.Parts...)
	for i := range out.Parts {
		out.Parts[i].Data = append([]byte(nil), result.Parts[i].Data...)
		out.Parts[i].Audience = append([]string(nil), result.Parts[i].Audience...)
	}
	return out
}

func (root *reviewRoot) holdResult(key heldResultKey, result session.ToolResult) bool {
	size := heldResultSize(result)
	root.mu.Lock()
	defer root.mu.Unlock()
	if size > maxHeldResultBytes-root.heldBytes || len(root.held) >= maxHeldResults {
		return false
	}
	if root.held == nil {
		root.held = make(map[heldResultKey]heldResult)
	}
	if _, exists := root.held[key]; exists {
		return false
	}
	root.held[key] = heldResult{key: key, result: cloneToolResult(result), bytes: size}
	root.heldBytes += size
	return true
}

func (root *reviewRoot) bindHeldAsk(key heldResultKey, askID string) bool {
	root.mu.Lock()
	defer root.mu.Unlock()
	held, ok := root.held[key]
	if !ok || held.askID != "" {
		return false
	}
	held.askID = askID
	root.held[key] = held
	return true
}

func (root *reviewRoot) consumeHeld(key heldResultKey, askID string) (session.ToolResult, bool) {
	root.mu.Lock()
	defer root.mu.Unlock()
	held, ok := root.held[key]
	if !ok || held.askID != askID || held.key != key {
		return session.ToolResult{}, false
	}
	delete(root.held, key)
	root.heldBytes -= held.bytes
	return held.result, true
}

func (root *reviewRoot) dropHeld(key heldResultKey) {
	root.mu.Lock()
	defer root.mu.Unlock()
	held, ok := root.held[key]
	if ok {
		delete(root.held, key)
		root.heldBytes -= held.bytes
	}
}

func (root *reviewRoot) clearHeld() {
	root.mu.Lock()
	defer root.mu.Unlock()
	clear(root.held)
	root.heldBytes = 0
}

func (e *Engine) emitInboundReview(r *Run, turnIdx int, call session.ToolCall, assessment inboundAssessment, disposition string) {
	if !assessment.applies {
		return
	}
	machine := reviewMachinePayload(r.reviewRoot.reviewer, assessment.request, assessment.result, assessment.err, disposition)
	e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Hook: &session.HookPayload{Phase: string(governance.PhasePostToolUse), Tool: call.Name, Decision: session.HookInfo, CallID: call.ID, Guardrail: machine}})
}

func (e *Engine) resolveInbound(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, result session.ToolResult, assessment inboundAssessment) (session.ToolResult, bool) {
	r.recordAuxiliaryUsageWhileActive(ctx, sess, session.UsageKindGuardrail, assessment.usage)
	if assessment.close != nil {
		defer assessment.close()
	}
	if !assessment.applies {
		return result, false
	}
	if !r.reviewRoot.principalRevisionIs(assessment.principalRevision) {
		e.emitInboundReview(r, turnIdx, call, assessment, "withhold_result")
		return session.NewToolError(call.ID, withheldResultText+": root instructions changed during review; retry the action for a fresh assessment"), false
	}
	r.publishInboundDetail(ctx, sess.ID, assessment)
	if !inboundNeedsHold(assessment) {
		disposition := "release_result"
		if !assessment.enforce && (assessment.err != nil || assessment.result.Assessment != ReviewAcceptable) {
			disposition = "pass_advisory"
		}
		e.emitInboundReview(r, turnIdx, call, assessment, disposition)
		return result, false
	}
	key := heldResultKey{reviewID: assessment.request.ReviewID, session: sess.ID, call: call.ID, env: env.Ref()}
	if !r.reviewRoot.holdResult(key, result) {
		e.emitInboundReview(r, turnIdx, call, assessment, "deny")
		return session.NewToolError(call.ID, withheldResultText+": private hold capacity unavailable"), false
	}
	r.reviewRoot.record(reviewFact(call, assessment.request.Target, "withheld"))
	if !e.deps.Interactive {
		r.reviewRoot.dropHeld(key)
		e.emitInboundReview(r, turnIdx, call, assessment, "withhold_result")
		return session.NewToolError(call.ID, withheldResultText), false
	}
	ask := session.PendingAsk{
		AskID: r.issueAskID(sess.ID, sess.Counters.ToolCalls, call.ID), Tool: call.Name,
		Reason: "contextual guardrail withheld this already-produced result pending Release once or Deny", Call: call.ID,
		Origin:    session.ApprovalOriginHookGuardrail,
		Guardrail: &session.GuardrailPendingScope{ReviewID: assessment.request.ReviewID, Kind: session.GuardrailApprovalResultRelease, SessionOnly: true},
	}
	if !r.reviewRoot.bindHeldAsk(key, ask.AskID) {
		r.reviewRoot.dropHeld(key)
		return session.NewToolError(call.ID, withheldResultText+": live hold binding unavailable"), false
	}
	answer, ok, paused := e.surfaceAsk(ctx, r, sess, turnIdx, ask)
	if !paused || !ok {
		r.reviewRoot.dropHeld(key)
		return session.NewToolError(call.ID, withheldResultText+": release decision was cancelled"), true
	}
	if !r.reviewRoot.principalRevisionIs(assessment.principalRevision) {
		r.reviewRoot.dropHeld(key)
		return session.NewToolError(call.ID, withheldResultText+": root instructions changed while release approval was pending; retry the action for a fresh assessment"), false
	}
	if answer.verdict == session.VerdictAllowOnce {
		released, found := r.reviewRoot.consumeHeld(key, ask.AskID)
		if !found {
			return session.NewToolError(call.ID, withheldResultText+": held result is unavailable"), false
		}
		r.reviewRoot.record(reviewFact(call, assessment.request.Target, "released"))
		e.emitInboundReview(r, turnIdx, call, assessment, "release_result")
		return released, false
	}
	r.reviewRoot.dropHeld(key)
	r.reviewRoot.record(reviewFact(call, assessment.request.Target, "denied"))
	e.emitInboundReview(r, turnIdx, call, assessment, "deny")
	return session.NewToolError(call.ID, withheldResultText), false
}
