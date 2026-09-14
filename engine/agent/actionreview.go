package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const (
	defaultReviewEvidenceHandles = 16
	defaultReviewEvidenceBytes   = int64(400_000)
	defaultReviewTrajectoryFacts = 256
	defaultReviewTrajectoryBytes = int64(128_000)
	reviewDirectionAuthorization = "authorization"
	reviewClassGenuineApproval   = "genuine_approval"
)

// reviewGrantIssuer is an optional composition capability implemented by the
// contextual reviewer. It keeps keyed repeat-grant state outside the engine while
// leaving ToolReviewer as the public review protocol.
type reviewGrantIssuer interface {
	GrantDigest(ToolReviewRequest) (string, bool)
	AllowsGrant(string) bool
	ArmGrant(string, string)
}

type reviewRoot struct {
	mu                sync.Mutex
	facts             []ReviewTrajectoryFact
	bytes             int64
	complete          bool
	maxFacts          int
	maxBytes          int64
	reviewer          ToolReviewer
	details           ReviewDetailSink
	principal         []ReviewPrincipalFact
	principalComplete bool
}

func newReviewRoot(reviewer ToolReviewer, details ReviewDetailSink) *reviewRoot {
	return &reviewRoot{complete: true, maxFacts: defaultReviewTrajectoryFacts, maxBytes: defaultReviewTrajectoryBytes, reviewer: reviewer, details: details}
}

func (r *reviewRoot) snapshot() ([]ReviewTrajectoryFact, bool) {
	if r == nil {
		return nil, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReviewTrajectoryFact(nil), r.facts...), r.complete
}

func (r *reviewRoot) principalSnapshot() ([]ReviewPrincipalFact, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReviewPrincipalFact(nil), r.principal...), r.principalComplete
}

func (r *reviewRoot) establishPrincipal(facts []ReviewPrincipalFact, complete bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.principal != nil || r.principalComplete {
		return
	}
	r.principal = append([]ReviewPrincipalFact(nil), facts...)
	r.principalComplete = complete
}

func (r *reviewRoot) record(f ReviewTrajectoryFact) {
	if r == nil {
		return
	}
	n := int64(len(f.Call) + len(f.Ref) + len(f.Direction) + len(f.DataClass) + len(f.TargetID) + len(f.Decision))
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, old := range r.facts {
		if old == f {
			return
		}
	}
	if !r.complete {
		return
	}
	if len(r.facts) >= r.maxFacts-1 || n > r.maxBytes-r.bytes {
		r.complete = false
		compact := ReviewTrajectoryFact{Direction: f.Direction, DataClass: f.DataClass, TargetID: f.TargetID, Decision: f.Decision}
		compactBytes := int64(len(compact.Direction) + len(compact.DataClass) + len(compact.TargetID) + len(compact.Decision))
		if len(r.facts) < r.maxFacts && compactBytes <= r.maxBytes-r.bytes {
			r.facts = append(r.facts, compact)
			r.bytes += compactBytes
		}
		return
	}
	r.facts = append(r.facts, f)
	r.bytes += n
}

type actionDependency struct {
	path    string
	exists  bool
	version string
}

type actionReview struct {
	request      ToolReviewRequest
	dependencies []actionDependency
	digest       string
	repeat       bool
}

func (e *Engine) prepareActionReview(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, call session.ToolCall) actionReview {
	target, paths := reviewTarget(call)
	if e.deps.Role == "" {
		principal := make([]ReviewPrincipalFact, 0, len(r.fragments)+1)
		if r.currentPrompt != nil {
			principal = append(principal, ReviewPrincipalFact{Kind: "genuine_user_task", Ref: "current-user", Statement: r.currentPrompt.Text, PositiveVerdict: true})
		}
		for i, fragment := range r.fragments {
			provenance := prompt.InstructionProvenanceUnknown
			if i < len(r.fragmentManifest) {
				provenance = r.fragmentManifest[i].Provenance
			}
			positive := provenance == prompt.InstructionProvenanceProject || provenance == prompt.InstructionProvenanceRules
			principal = append(principal, ReviewPrincipalFact{Kind: "admitted_" + provenance + "_instruction", Ref: fmt.Sprintf("instruction-%d", i), Statement: fragment.Text, PositiveVerdict: positive})
		}
		r.reviewRoot.establishPrincipal(principal, r.currentPrompt != nil)
	}
	facts, complete := r.reviewRoot.principalSnapshot()
	facts = append([]ReviewPrincipalFact(nil), facts...)
	deps, depsComplete := snapshotActionDependencies(ctx, env.Workspace(), paths)
	for i, dep := range deps {
		state := "absent"
		if dep.exists {
			state = dep.version
		}
		facts = append(facts, ReviewPrincipalFact{Kind: "target_dependency_version", Ref: fmt.Sprintf("dependency-%d", i), Statement: dep.path + "\x00" + state})
	}
	if !depsComplete {
		facts = append(facts, ReviewPrincipalFact{Kind: "repeat_dependency_incomplete", Ref: "repeat-scope", Statement: "Not every dynamic script, configuration, or target dependency can be version-bound; Run once remains available but repeat approval is unavailable."})
	}
	trajectory, trajectoryComplete := r.reviewRoot.snapshot()
	effectiveArgs := append(json.RawMessage(nil), call.Args...)
	eventInput := append(json.RawMessage(nil), call.Args...)
	effectiveCall := session.NewToolCall(call.ID, call.Name, effectiveArgs)
	req := ToolReviewRequest{
		ReviewID: fmt.Sprintf("%s:%s:action", sess.ID, call.ID), Job: ReviewJobAction,
		Event:         governance.HookEvent{Phase: governance.PhasePreToolUse, Tool: call.Name, Input: eventInput, SessionID: string(sess.ID), CallID: string(call.ID)},
		EffectiveCall: effectiveCall, PrincipalFacts: facts, PrincipalFactsComplete: complete,
		Caller: reviewCaller(e, r), Environment: env.Ref(), Target: target,
		EvidenceComplete: true, Trajectory: trajectory, TrajectoryComplete: trajectoryComplete,
		Capacity: ReviewCapacity{MaxEvidenceHandles: defaultReviewEvidenceHandles, MaxEvidenceBytes: defaultReviewEvidenceBytes, MaxTrajectoryFacts: defaultReviewTrajectoryFacts, MaxTrajectoryBytes: defaultReviewTrajectoryBytes},
	}
	out := actionReview{request: req, dependencies: deps}
	if issuer, ok := r.reviewRoot.reviewer.(reviewGrantIssuer); ok {
		out.digest, out.repeat = issuer.GrantDigest(req)
	}
	return out
}

func reviewCaller(e *Engine, r *Run) ReviewCaller {
	role := e.deps.Role
	if role == "" {
		role = "main"
	}
	caps := make([]string, 0)
	if e.deps.Catalog != nil {
		for _, spec := range e.deps.Catalog.Specs(session.ModeDefault) {
			caps = append(caps, spec.Name)
		}
	}
	return ReviewCaller{Role: role, Isolated: r.req.reviewIsolated, Capabilities: caps}
}

func reviewTarget(call session.ToolCall) (ReviewTarget, []string) {
	var args map[string]json.RawMessage
	_ = json.Unmarshal(call.Args, &args)
	keys := []string{"path", "destination", "source", "url", "uri", "target"}
	var paths []string
	var display string
	for _, key := range keys {
		var value string
		if raw := args[key]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != "" {
			if display == "" {
				display = value
			}
			if key == "path" || key == "destination" || key == "source" {
				paths = append(paths, value)
			}
		}
	}
	kind := "tool"
	if len(paths) > 0 {
		kind = "workspace"
	} else if display != "" {
		kind = "external"
	}
	return ReviewTarget{Kind: kind, Display: display, DestinationID: display}, paths
}

func snapshotActionDependencies(ctx context.Context, ws tool.Workspace, paths []string) ([]actionDependency, bool) {
	if len(paths) == 0 {
		return nil, false
	}
	deps := make([]actionDependency, 0, len(paths))
	for _, path := range paths {
		info, err := ws.Stat(ctx, path)
		if errors.Is(err, fs.ErrNotExist) {
			deps = append(deps, actionDependency{path: path})
			continue
		}
		if err != nil || info.IsDir || info.Size > defaultReviewEvidenceBytes {
			return deps, false
		}
		_, version, err := ws.ReadVersion(ctx, path)
		if err != nil {
			return deps, false
		}
		encoded, err := tool.EncodeFileVersion(version)
		if err != nil {
			return deps, false
		}
		deps = append(deps, actionDependency{path: path, exists: true, version: encoded})
	}
	return deps, true
}

func revalidateActionDependencies(ctx context.Context, ws tool.Workspace, want []actionDependency) bool {
	if len(want) == 0 {
		return true
	}
	paths := make([]string, len(want))
	for i := range want {
		paths[i] = want[i].path
	}
	got, complete := snapshotActionDependencies(ctx, ws, paths)
	if !complete || len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func reviewFact(call session.ToolCall, target ReviewTarget, decision string) ReviewTrajectoryFact {
	class := "action"
	direction := "local"
	switch call.Name {
	case "Read", "ListDir", "Grep", "Glob":
		class = "sensitive_read"
	case "WebSearch", "WebFetch", "CallMcpWithQuery":
		class, direction = "external_send", "outbound"
	case "Subagent", "Parallel", "Team":
		class, direction = "delegation", "worker"
	}
	return ReviewTrajectoryFact{Call: call.ID, Ref: string(call.ID), Direction: direction, DataClass: class, TargetID: target.DestinationID, Decision: decision}
}

func (r *Run) publishActionConcerns(ctx context.Context, sessionID session.SessionID, reviewID string, concerns []ReviewConcern) {
	if r.reviewRoot.details == nil {
		return
	}
	for _, concern := range concerns {
		r.reviewRoot.details.PublishReviewDetail(ctx, ReviewDetail{SessionID: sessionID, ReviewID: reviewID, Concern: concern.Rationale, SourceDisplay: concern.SourceRef, NextAction: "Run once, approve this exact repeatable action for this session when available, or cancel."})
	}
}

type actionReviewAssessment struct {
	action   actionReview
	result   ToolReviewResult
	err      error
	grantHit bool
}

func (e *Engine) prepareActionAssessment(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, call session.ToolCall) actionReviewAssessment {
	action := e.prepareActionReview(ctx, r, sess, env, call)
	assessment := actionReviewAssessment{action: action}
	if issuer, ok := r.reviewRoot.reviewer.(reviewGrantIssuer); ok && action.repeat {
		assessment.grantHit = issuer.AllowsGrant(action.digest)
	}
	return assessment
}

func assessAction(ctx context.Context, r *Run, assessment *actionReviewAssessment) {
	if assessment.grantHit {
		return
	}
	assessment.result, assessment.err = r.reviewRoot.reviewer.Review(ctx, assessment.action.request, nil)
}

func (e *Engine) resolveActionAssessment(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, assessment actionReviewAssessment) (session.ToolResult, bool, bool, bool) {
	if assessment.grantHit {
		r.reviewRoot.record(reviewFact(call, assessment.action.request.Target, "repeat_grant"))
		return session.ToolResult{}, false, true, false
	}
	result := assessment.result
	decision := string(result.Assessment)
	if assessment.err != nil {
		decision = "unresolved"
		result.Assessment = ReviewUnresolved
	}
	r.reviewRoot.record(reviewFact(call, assessment.action.request.Target, decision))
	r.publishActionConcerns(ctx, sess.ID, assessment.action.request.ReviewID, result.Concerns)
	if result.Assessment == ReviewAcceptable {
		return session.ToolResult{}, false, true, false
	}
	reason := "contextual guardrail could not approve this action"
	if result.Assessment == ReviewProhibited {
		reason = "contextual guardrail requires human approval for this action"
	}
	if !e.deps.Interactive {
		res := session.NewToolError(call.ID, reason)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false, false, false
	}
	ask := session.PendingAsk{
		AskID: newAskID(sess.ID, sess.Counters.ToolCalls, call.ID, r.askDiscriminator), Tool: call.Name, Args: call.Args,
		Reason: reason, Call: call.ID, Origin: session.ApprovalOriginHookGuardrail,
		Guardrail: &session.GuardrailPendingScope{ReviewID: assessment.action.request.ReviewID, Kind: session.GuardrailApprovalAction, GrantDigest: assessment.action.digest, SessionOnly: true, RepeatAvailable: assessment.action.repeat},
	}
	answer, ok, paused := e.surfaceAsk(ctx, r, sess, turnIdx, ask)
	if !paused {
		res := session.NewToolError(call.ID, "internal: cannot pause for contextual guardrail approval")
		return res, false, false, false
	}
	if !ok {
		return session.ToolResult{}, true, false, false
	}
	if answer.verdict != session.VerdictAllowOnce && answer.verdict != session.VerdictAllowAlways {
		res := session.NewToolError(call.ID, reason)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		r.reviewRoot.record(reviewFact(call, assessment.action.request.Target, "denied"))
		return res, false, false, false
	}
	permission := e.permissionDecision(ctx, sess, env, call)
	if env.Ref() != assessment.action.request.Environment || permission.Effect == governance.Deny ||
		!revalidateActionDependencies(ctx, env.Workspace(), assessment.action.dependencies) {
		res := session.NewToolError(call.ID, "contextual guardrail approval became stale before execution")
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false, false, false
	}
	r.reviewRoot.record(reviewFact(call, assessment.action.request.Target, "approved"))
	return session.ToolResult{}, false, true, answer.verdict == session.VerdictAllowAlways
}

func (e *Engine) reviewAction(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, t tool.Tool, enqueue time.Time) (session.ToolResult, *dispatchPark, bool, bool) {
	if r.reviewRoot == nil || r.reviewRoot.reviewer == nil {
		return session.ToolResult{}, nil, false, true
	}
	assessment := e.prepareActionAssessment(ctx, r, sess, env, call)
	assessAction(ctx, r, &assessment)
	if ctx.Err() != nil {
		return session.ToolResult{}, nil, true, false
	}
	res, cancelled, proceed, armGrant := e.resolveActionAssessment(ctx, r, sess, env, turnIdx, call, assessment)
	if cancelled || !proceed {
		return res, nil, cancelled, false
	}
	res, park, cancelled := e.postPreToolUse(ctx, r, sess, env, turnIdx, call, t, enqueue)
	issuer, hasIssuer := r.reviewRoot.reviewer.(reviewGrantIssuer)
	if armGrant {
		e.armPostActionGrant(ctx, r, sess, env, call, session.VerdictAllowAlways, assessment.action.repeat, issuer, hasIssuer, res, park, cancelled)
	}
	return res, park, cancelled, false
}

func (e *Engine) armPostActionGrant(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, call session.ToolCall, verdict session.ApprovalVerdict, repeat bool, issuer reviewGrantIssuer, hasIssuer bool, result session.ToolResult, park *dispatchPark, cancelled bool) {
	if verdict != session.VerdictAllowAlways || !repeat || !hasIssuer || park != nil || cancelled || result.IsError {
		return
	}
	post := e.prepareActionReview(ctx, r, sess, env, call)
	if post.repeat {
		issuer.ArmGrant(post.digest, post.request.Event.SessionID)
	}
}
