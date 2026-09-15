package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
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

func reviewMachinePayload(reviewer ToolReviewer, req ToolReviewRequest, result ToolReviewResult, reviewErr error, disposition string) *session.GuardrailReviewPayload {
	payload := &session.GuardrailReviewPayload{ReviewID: req.ReviewID, Job: string(req.Job), Assessment: string(result.Assessment), Inspection: "complete", Disposition: disposition}
	if reviewErr != nil {
		payload.Inspection = "operational_failure"
		payload.Assessment = string(ReviewUnresolved)
		payload.ReasonCode = "checker_unavailable"
	} else if result.Assessment == ReviewUnresolved {
		payload.ReasonCode = "inspection_unresolved"
	} else if result.Assessment == ReviewProhibited {
		payload.ReasonCode = "authority_crossing"
	}
	if metadata, ok := reviewer.(ReviewMetadataProvider); ok {
		payload.RuleID, payload.RuleOrigin, payload.CheckerProviderID, payload.CheckerModelID = metadata.GuardrailReviewMetadata(req.EffectiveCall.Name, req.Job)
	}
	for _, concern := range result.Concerns {
		payload.Concerns = append(payload.Concerns, session.GuardrailRef{Ref: concern.Ref, Category: concern.Category})
		if concern.SourceRef != "" {
			payload.Sources = append(payload.Sources, session.GuardrailRef{Ref: concern.SourceRef, Category: "source"})
		}
	}
	for _, missing := range result.Missing {
		payload.Sources = append(payload.Sources, session.GuardrailRef{Ref: missing.Ref, Category: missing.Kind})
	}
	return payload
}

type reviewRoot struct {
	mu                sync.Mutex
	facts             []ReviewTrajectoryFact
	bytes             int64
	complete          bool
	maxFacts          int
	maxBytes          int64
	reviewer          ToolReviewer
	preparer          ReviewEvidencePreparer
	details           ReviewDetailSink
	principal         []ReviewPrincipalFact
	principalComplete bool
	held              map[heldResultKey]heldResult
	heldBytes         int64
	rootSessionID     session.SessionID
}

func newReviewRoot(reviewer ToolReviewer, preparer ReviewEvidencePreparer, details ReviewDetailSink) *reviewRoot {
	return &reviewRoot{complete: true, maxFacts: defaultReviewTrajectoryFacts, maxBytes: defaultReviewTrajectoryBytes, reviewer: reviewer, preparer: preparer, details: details}
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
	source       ReviewEvidenceSource
	close        func()
	digest       string
	repeat       bool
}

func (e *Engine) establishReviewPrincipal(r *Run) {
	if e.deps.Role != "" {
		return
	}
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

func (e *Engine) prepareActionReview(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, call session.ToolCall) actionReview {
	e.establishReviewPrincipal(r)
	target, paths := reviewTarget(call)
	facts, complete := r.reviewRoot.principalSnapshot()
	facts = append([]ReviewPrincipalFact(nil), facts...)
	authorizedPaths := make([]string, 0, len(paths))
	authorityComplete := true
	for _, path := range paths {
		if err := e.authorizeReviewEvidenceRead(ctx, r, sess, env, call.ID, path); err != nil {
			authorityComplete = false
			continue
		}
		authorizedPaths = append(authorizedPaths, path)
	}
	deps, depsComplete := snapshotActionDependencies(ctx, env.Workspace(), authorizedPaths)
	depsComplete = depsComplete && authorityComplete
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
		Caller: reviewCaller(e, r, sess), Environment: env.Ref(), Target: target,
		Trajectory: trajectory, TrajectoryComplete: trajectoryComplete,
		Capacity: ReviewCapacity{MaxEvidenceHandles: defaultReviewEvidenceHandles, MaxEvidenceBytes: defaultReviewEvidenceBytes, MaxTrajectoryFacts: defaultReviewTrajectoryFacts, MaxTrajectoryBytes: defaultReviewTrajectoryBytes},
	}
	prepared := e.prepareReviewEvidence(ctx, r, sess, env, req, nil)
	req.Evidence, req.EvidenceComplete = prepared.Evidence, prepared.Complete
	out := actionReview{request: req, dependencies: deps, source: prepared.Source, close: prepared.Close}
	if issuer, ok := r.reviewRoot.reviewer.(ReviewGrantStore); ok {
		out.digest, out.repeat = issuer.GrantDigest(req)
	}
	return out
}

func (e *Engine) authorizeReviewEvidenceRead(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, callID session.ToolCallID, path string) error {
	available := false
	if e.deps.Catalog != nil {
		for _, candidate := range e.deps.Catalog.Available(sess.Mode) {
			if candidate.Spec().Name == "Read" {
				available = true
				break
			}
		}
	}
	if !available {
		return errors.New("review evidence Read is outside the originating session catalog")
	}
	args, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	if err != nil {
		return err
	}
	decision := e.permissionDecision(ctx, sess, env, session.NewToolCall(callID, "Read", args))
	if decision.Effect != governance.Allow {
		return errors.New("review evidence Read is not allowed by the originating session permission policy")
	}
	if denied, checked := e.authorizeExecution(ctx, r, sess, env, sess.Counters.Turns-1, session.NewToolCall(callID, "Read", args)); checked {
		return errors.New(denied.Content)
	}
	return nil
}

func (e *Engine) prepareReviewEvidence(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, req ToolReviewRequest, result *session.ToolResult) PreparedReviewEvidence {
	if r.reviewRoot.preparer == nil {
		return PreparedReviewEvidence{}
	}
	prepared, err := r.reviewRoot.preparer.PrepareReviewEvidence(ctx, ReviewEvidencePreparation{
		Request: req, Environment: env, Owner: sess.Owner.Clone(), Result: result,
		Authorize: func(authCtx context.Context, call session.ToolCall) error {
			paths := tool.LocalFileOperands(call.Name, call.Args)
			if call.Name != "Read" || len(paths) != 1 {
				return fmt.Errorf("unsupported review evidence authorization call %q", call.Name)
			}
			return e.authorizeReviewEvidenceRead(authCtx, r, sess, env, call.ID, paths[0])
		},
	})
	if err != nil {
		if prepared.Close != nil {
			prepared.Close()
		}
		return PreparedReviewEvidence{}
	}
	if prepared.Close == nil {
		prepared.Close = func() {}
	} else {
		closeEvidence := prepared.Close
		var once sync.Once
		prepared.Close = func() { once.Do(closeEvidence) }
	}
	return prepared
}

func reviewCaller(e *Engine, r *Run, sess *session.Session) ReviewCaller {
	role := e.deps.Role
	if role == "" {
		role = "main"
	}
	caps := make([]string, 0)
	if e.deps.Catalog != nil {
		specs := e.deps.Catalog.Specs(sess.Mode)
		if authority, bound := sess.BoundAuthority(); bound {
			specs = authoritySpecs(specs, authority.CapabilitySet, true)
		}
		for _, spec := range specs {
			caps = append(caps, spec.Name)
		}
	}
	return ReviewCaller{Role: role, Isolated: r.req.reviewIsolated, Capabilities: caps}
}

func reviewTarget(call session.ToolCall) (ReviewTarget, []string) {
	paths := tool.LocalFileOperands(call.Name, call.Args)
	if len(paths) > 0 {
		return ReviewTarget{Kind: "workspace", Display: paths[0], DestinationID: paths[0]}, paths
	}
	if call.Name == "WebFetch" {
		var args struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(call.Args, &args) == nil && args.URL != "" {
			return ReviewTarget{Kind: "external", Display: args.URL, DestinationID: args.URL}, nil
		}
	}
	return ReviewTarget{Kind: "tool"}, nil
}

func snapshotActionDependencies(ctx context.Context, ws tool.Workspace, paths []string) ([]actionDependency, bool) {
	if len(paths) == 0 {
		return nil, false
	}
	reader, ok := ws.(tool.BoundedWorkspaceReader)
	if !ok {
		return nil, false
	}
	deps := make([]actionDependency, 0, len(paths))
	for _, path := range paths {
		_, version, err := reader.ReadVersionBounded(ctx, path, defaultReviewEvidenceBytes)
		if errors.Is(err, fs.ErrNotExist) {
			deps = append(deps, actionDependency{path: path})
			continue
		}
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

func (e *Engine) revalidateAuthorizedActionDependencies(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, callID session.ToolCallID, dependencies []actionDependency) bool {
	for _, dependency := range dependencies {
		if err := e.authorizeReviewEvidenceRead(ctx, r, sess, env, callID, dependency.path); err != nil {
			return false
		}
	}
	return revalidateActionDependencies(ctx, env.Workspace(), dependencies)
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

func (r *Run) publishReviewDetail(ctx context.Context, detail ReviewDetail) {
	if r.reviewRoot == nil || r.reviewRoot.details == nil || r.reviewRoot.rootSessionID == "" {
		return
	}
	detail.RootSessionID = r.reviewRoot.rootSessionID
	r.reviewRoot.details.PublishReviewDetail(ctx, detail)
}

func reviewConcernDisplay(concern ReviewConcern) string {
	if rationale := strings.TrimSpace(concern.Rationale); rationale != "" {
		return rationale
	}
	switch concern.Category {
	case "authority_crossing":
		return "The proposed action may cross the caller's established authority."
	case "redirection", "prompt_injection":
		return "The reviewed content may redirect the agent across its authority boundary."
	case "exfiltration", "exfil":
		return "The proposed action may send data outside the established authorization."
	default:
		return "The reviewer identified a potential authority-boundary concern."
	}
}

func reviewSourceDisplay(req ToolReviewRequest, sourceRef string) string {
	switch sourceRef {
	case "call":
		return fmt.Sprintf("effective call %s (%s)", req.EffectiveCall.ID, req.EffectiveCall.Name)
	case "":
		return ""
	}
	for _, meta := range req.Evidence {
		if meta.Handle == sourceRef {
			if meta.Display != "" {
				return meta.Display
			}
			return "review evidence " + meta.Kind
		}
	}
	for _, fact := range req.PrincipalFacts {
		if fact.Ref == sourceRef {
			return fmt.Sprintf("principal fact %s (%s)", fact.Kind, fact.Ref)
		}
	}
	for _, fact := range req.Trajectory {
		if fact.Ref == sourceRef {
			return fmt.Sprintf("prior %s %s action (%s)", fact.Direction, fact.DataClass, fact.Ref)
		}
	}
	return ""
}

func (r *Run) publishActionDetail(ctx context.Context, sessionID session.SessionID, reviewID string, req ToolReviewRequest, result ToolReviewResult, reviewErr error) {
	if r.reviewRoot.details == nil {
		return
	}
	next := "No human action is required."
	if reviewErr != nil {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessionID, ReviewID: reviewID, Concern: "Inspection could not complete; no unsafe finding was inferred.", NextAction: "Run once or cancel when enforcement requires a human decision."})
		return
	}
	if result.Assessment == ReviewAcceptable {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessionID, ReviewID: reviewID, Concern: "Inspection completed; no attempted authority crossing was found.", NextAction: next})
		return
	}
	if result.Assessment == ReviewUnresolved {
		next = "Run once or cancel; missing evidence is not itself an unsafe finding."
	}
	for _, concern := range result.Concerns {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessionID, ReviewID: reviewID, Concern: reviewConcernDisplay(concern), SourceDisplay: reviewSourceDisplay(req, concern.SourceRef), NextAction: "Run once, approve this exact repeatable action for this session when available, or cancel."})
	}
	for _, missing := range result.Missing {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessionID, ReviewID: reviewID, Concern: "Inspection was unresolved because required evidence was unavailable.", SourceDisplay: missing.Ref, NextAction: next})
	}
	if len(result.Concerns) == 0 && len(result.Missing) == 0 {
		r.publishReviewDetail(ctx, ReviewDetail{SessionID: sessionID, ReviewID: reviewID, Concern: "Inspection was unresolved without a specific unsafe finding.", NextAction: next})
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
	if issuer, ok := r.reviewRoot.reviewer.(ReviewGrantStore); ok && action.repeat {
		assessment.grantHit = issuer.AllowsGrant(action.digest)
	}
	return assessment
}

func assessAction(ctx context.Context, r *Run, assessment *actionReviewAssessment) {
	if assessment.grantHit {
		return
	}
	assessment.result, assessment.err = r.reviewRoot.reviewer.Review(ctx, assessment.action.request, assessment.action.source)
	if assessment.err == nil && assessment.result.Assessment != ReviewAcceptable && assessment.result.Assessment != ReviewProhibited && assessment.result.Assessment != ReviewUnresolved {
		assessment.result.Assessment = ReviewUnresolved
		assessment.err = errors.New("tool reviewer returned an invalid or empty assessment")
	}
}

func (e *Engine) resolveActionAssessment(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, auth *permissionAuthorization, assessment actionReviewAssessment) (session.ToolResult, bool, bool, bool) {
	if assessment.action.close != nil {
		defer assessment.action.close()
	}
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
	_, enforce := reviewPolicy(r.reviewRoot.reviewer, call.Name, ReviewJobAction, assessment.err != nil)
	if assessment.err != nil {
		var terminal interface{ GuardrailReviewTerminalFailure() bool }
		if errors.As(assessment.err, &terminal) && terminal.GuardrailReviewTerminalFailure() {
			enforce = true
		}
	}
	r.reviewRoot.record(reviewFact(call, assessment.action.request.Target, decision))
	r.publishActionDetail(ctx, sess.ID, assessment.action.request.ReviewID, assessment.action.request, result, assessment.err)
	disposition := "execute"
	if result.Assessment != ReviewAcceptable && !enforce {
		disposition = "pass_advisory"
	}
	if result.Assessment != ReviewAcceptable && enforce && e.deps.Interactive {
		disposition = "ask_action"
	}
	if result.Assessment != ReviewAcceptable && enforce && !e.deps.Interactive {
		disposition = "deny"
	}
	e.emit(r, session.Event{Type: session.EvHook, Turn: turnIdx, Hook: &session.HookPayload{Phase: string(governance.PhasePreToolUse), Tool: call.Name, Decision: session.HookInfo, CallID: call.ID, Guardrail: reviewMachinePayload(r.reviewRoot.reviewer, assessment.action.request, result, assessment.err, disposition)}})
	if result.Assessment == ReviewAcceptable || !enforce {
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
	return e.resolveActionAsk(ctx, r, sess, env, turnIdx, call, auth, assessment, reason)
}

func (e *Engine) resolveActionAsk(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, auth *permissionAuthorization, assessment actionReviewAssessment, reason string) (session.ToolResult, bool, bool, bool) {
	ask := session.PendingAsk{
		AskID: r.issueAskID(sess.ID, sess.Counters.ToolCalls, call.ID), Tool: call.Name, Args: call.Args,
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
	allowed, cancelled, staleReason := e.reauthorizeAction(ctx, r, sess, env, turnIdx, call, auth)
	if cancelled {
		return session.ToolResult{}, true, false, false
	}
	if !allowed || !revalidateActionDependencies(ctx, env.Workspace(), assessment.action.dependencies) {
		if staleReason == "" {
			staleReason = "contextual guardrail approval became stale before execution"
		}
		res := session.NewToolError(call.ID, staleReason)
		e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
		return res, false, false, false
	}
	r.reviewRoot.record(reviewFact(call, assessment.action.request.Target, "approved"))
	return session.ToolResult{}, false, true, answer.verdict == session.VerdictAllowAlways
}

func (e *Engine) reauthorizeAction(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, auth *permissionAuthorization) (allowed, cancelled bool, reason string) {
	if auth == nil || !auth.matches(call, env.Ref()) {
		return false, false, "contextual guardrail permission binding became stale before execution"
	}
	if !auth.authorityStillValid(sess, call, env) {
		return false, false, "contextual guardrail authority binding became stale before execution"
	}
	current := e.permissionDecision(ctx, sess, env, call)
	if current == auth.decision {
		return true, false, ""
	}
	if current.Effect == governance.Allow {
		auth.decision = current
		return true, false, ""
	}
	if current.Effect == governance.Deny {
		return false, false, current.Reason
	}

	decision, cancelled := e.authorizeDecision(ctx, r, sess, turnIdx, call, current)
	if cancelled {
		return false, true, ""
	}
	if decision.Effect != governance.Allow {
		return false, false, decision.Reason
	}
	postApproval := e.permissionDecision(ctx, sess, env, call)
	if postApproval == current || postApproval.Effect == governance.Allow {
		auth.decision = postApproval
		return true, false, ""
	}
	return false, false, "permission policy changed while approval was pending"
}

func (e *Engine) reviewAction(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, auth *permissionAuthorization, t tool.Tool, enqueue time.Time) (session.ToolResult, *dispatchPark, bool, bool) {
	return e.reviewActionWithTail(ctx, r, sess, env, turnIdx, call, auth, false, func() (session.ToolResult, *dispatchPark, bool) {
		return e.postPreToolUse(ctx, r, sess, env, turnIdx, call, t, auth, enqueue)
	})
}

func (e *Engine) reviewActionWithTail(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, turnIdx int, call session.ToolCall, auth *permissionAuthorization, revalidate bool, tail func() (session.ToolResult, *dispatchPark, bool)) (session.ToolResult, *dispatchPark, bool, bool) {
	if r.reviewRoot == nil || r.reviewRoot.reviewer == nil {
		return session.ToolResult{}, nil, false, true
	}
	if applies, _ := reviewPolicy(r.reviewRoot.reviewer, call.Name, ReviewJobAction, false); !applies {
		return session.ToolResult{}, nil, false, true
	}
	assessment := e.prepareActionAssessment(ctx, r, sess, env, call)
	assessAction(ctx, r, &assessment)
	if ctx.Err() != nil {
		if assessment.action.close != nil {
			assessment.action.close()
		}
		return session.ToolResult{}, nil, true, false
	}
	res, cancelled, proceed, armGrant := e.resolveActionAssessment(ctx, r, sess, env, turnIdx, call, auth, assessment)
	if cancelled || !proceed {
		return res, nil, cancelled, false
	}
	if revalidate {
		allowed, reauthCancelled, reason := e.reauthorizeAction(ctx, r, sess, env, turnIdx, call, auth)
		if reauthCancelled {
			return session.ToolResult{}, nil, true, false
		}
		if !allowed || !e.revalidateAuthorizedActionDependencies(ctx, r, sess, env, call.ID, assessment.action.dependencies) {
			if reason == "" {
				reason = "contextual guardrail binding became stale before execution"
			}
			res = session.NewToolError(call.ID, reason)
			e.emit(r, session.Event{Type: session.EvToolResult, Turn: turnIdx, ToolResult: ptr(res)})
			return res, nil, false, false
		}
	}
	res, park, cancelled := tail()
	issuer, hasIssuer := r.reviewRoot.reviewer.(ReviewGrantStore)
	if armGrant {
		e.armPostActionGrant(ctx, r, sess, env, call, session.VerdictAllowAlways, assessment.action.repeat, issuer, hasIssuer, res, park, cancelled)
	}
	return res, park, cancelled, false
}

func (e *Engine) armPostActionGrant(ctx context.Context, r *Run, sess *session.Session, env tool.Environment, call session.ToolCall, verdict session.ApprovalVerdict, repeat bool, issuer ReviewGrantStore, hasIssuer bool, result session.ToolResult, park *dispatchPark, cancelled bool) {
	if verdict != session.VerdictAllowAlways || !repeat || !hasIssuer || park != nil || cancelled || result.IsError {
		return
	}
	post := e.prepareActionReview(ctx, r, sess, env, call)
	if post.close != nil {
		defer post.close()
	}
	if post.repeat {
		issuer.ArmGrant(post.digest, post.request.Event.SessionID)
	}
}
