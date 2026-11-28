package mcpbroker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	diagnosticEventGatewayCall = "gateway_call"
	// diagnosticEventGatewayCallReport is a dispatch and outcome report that
	// failed.
	diagnosticEventGatewayCallReport = "gateway_call_report"
	diagnosticCodeCompleted          = "completed"
)

// gatewayScope is the session tree a gateway call belongs to, read from the
// tool call's context before the attachment operation replaces it, with the
// main agent's run the context carries.
type gatewayScope struct {
	acting, root string
	mainRun      agentcredential.Run
	ok           bool
}

func gatewayScopeFrom(ctx context.Context) gatewayScope {
	acting, hasActing := port.SessionIDFromContext(ctx)
	root, hasRoot := port.RootSessionIDFromContext(ctx)
	mainRun, _ := agentcredential.RunFromContext(ctx)
	return gatewayScope{
		acting: string(acting), root: string(root), mainRun: mainRun,
		ok: hasActing && hasRoot && acting != "" && root != "",
	}
}

// callRun is the run a gateway call names: the main agent's run for a call
// of the root's main agent, or a helper's current delegation and the run
// delegated to it. ok is false when there is none, and the call is refused
// rather than sent with a stand-in.
func (g *gatewayCall) callRun() (run agentcredential.Run, delegationID string, ok bool) {
	if g.scope.acting == g.scope.root {
		return g.scope.mainRun, "", !g.scope.mainRun.IsZero()
	}
	d, held := g.r.gatewayCalls.Credentials.Delegation(g.key)
	return d.Run, d.ID, held && !d.Run.IsZero() && d.ID != ""
}

// operationID identifies one tool call of an agent in a session tree. It is
// stable across retries and resume; the query tool's native call keeps the
// query call's ID, so both name the same operation.
func operationID(root, acting string, call session.ToolCallID) string {
	sum := sha256.Sum256([]byte("mecatl/operation\x00" + root + "\x00" + acting + "\x00" + string(call)))
	return "op_" + base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}

// hashArguments is the argument hash the gateway call's wire is checked
// against.
func hashArguments(args json.RawMessage) (string, error) {
	_, sum, err := argumentsSHA256(args)
	return sum, err
}

// gatewayCall is one model tool call sent through the gateway.
type gatewayCall struct {
	t     *sessionTool
	r     *Runtime
	call  session.ToolCall
	scope gatewayScope
	key   agentcredential.Key
	// agentRun and delegationID are the run the call names and the delegation it
	// acts under, empty for the main agent's run.
	agentRun     agentcredential.Run
	delegationID string
	op           string
	server       string
	tool         string
	args         json.RawMessage
	hash         string
	claim        bool
	// authErr is the identity broker's refusal that ended the call, if any;
	// cause names any other failure before a send.
	authErr *identitybroker.Error
	cause   string
	// attempts are the attempts sent so far, oldest first.
	attempts []mcpadapter.GatewayAttempt
	// brokerSession is the broker session the last authorization was
	// issued in, which its attempt's report names.
	brokerSession string
}

// executeGateway authorizes call with the identity broker and sends it to
// the gateway with a token for this call alone. Every refusal and unknown
// outcome is a tool error; only a cancelled context is a Go error.
func (t *sessionTool) executeGateway(ctx context.Context, scope gatewayScope, call session.ToolCall) (session.ToolResult, error) {
	g := &gatewayCall{t: t, r: t.attachment.runtime, call: call, scope: scope}
	// Each attempt is reported to the identity broker as it ends (send), a
	// cancelled one included: cancelled after its tools/call was written, it
	// reports an unknown outcome; before, a dispatch failure.
	result, code, err := g.run(ctx)
	g.log(ctx, code)
	if err != nil {
		return session.ToolResult{}, err
	}
	if code != diagnosticCodeCompleted && ctx.Err() != nil {
		return session.ToolResult{}, ctx.Err()
	}
	result.CallID = call.ID
	return result, nil
}

// run returns the call's result and its diagnostic code. Its error is a
// failed projection of a completed query call.
func (g *gatewayCall) run(ctx context.Context) (session.ToolResult, string, error) {
	fail := func(o gatewayOutcome) (session.ToolResult, string, error) { return o.result(g.call.ID), o.code, nil }
	refused := func(err error) gatewayOutcome {
		if !errors.As(err, &g.authErr) {
			g.cause = failureCause(err)
		}
		return outcomeForAuthorization(err)
	}
	if !g.scope.ok {
		return fail(outcomeIdentityUnavailable)
	}
	g.key = agentcredential.Key{RootSessionID: g.scope.root, AgentSessionID: g.scope.acting}
	g.op = operationID(g.scope.root, g.scope.acting, g.call.ID)
	var ok bool
	if g.agentRun, g.delegationID, ok = g.callRun(); !ok {
		g.cause = "the acting agent has no run"
		return fail(outcomeIdentityUnavailable)
	}
	var err error
	if g.server, g.tool, err = g.r.gateway.Route(g.t.route.backend, g.call.Name); err != nil {
		g.cause = "route: " + err.Error()
		return fail(outcomeInternal)
	}
	// The harness gives a helper the tools of the servers its definition
	// names by backend, and the broker checks the server Route names, so the
	// two must be the same name.
	if g.server != g.t.route.backend {
		g.cause = "route: the gateway named a server other than the tool's backend"
		return fail(outcomeInternal)
	}
	// The argument hash is checked before anything is authorized.
	if g.args, g.hash, err = argumentsSHA256(g.call.Args); err != nil {
		return fail(refused(err))
	}
	defer g.releaseUnlessSent(ctx)

	// Claimed before it is authorized, so a duplicate is refused without a
	// gateway token being issued for it; a call refused authorization was
	// not sent and is released.
	err = g.claimCall(ctx)
	var auth identitybroker.Authorization
	if err == nil {
		auth, err = g.authorize(ctx, "")
	}
	if err != nil {
		return fail(refused(err))
	}
	last := g.send(ctx, auth)
	retried := false
	if last.Rejection == mcpadapter.GatewayRejectionInvalidToken {
		// One retry with a fresh token, reporting the rejected attempt with
		// the call's own fields.
		if auth, err = g.authorize(ctx, last.AttemptID); err != nil {
			return fail(refused(err))
		}
		last, retried = g.send(ctx, auth), true
	}
	if last.Outcome == mcpadapter.GatewayAttemptCompleted {
		g.complete(ctx)
	}
	// A tools/call refused as changed after authorization is a bug: the
	// authorization covers exactly what is sent. It maps to internal, not sent.
	var res session.ToolResult
	code := diagnosticCodeCompleted
	switch {
	case last.Outcome != mcpadapter.GatewayAttemptCompleted:
		o := outcomeForAttempts(g.server, last, retried)
		res, code = o.result(g.call.ID), o.code
	case g.t.queryFilter != "":
		if res, err = mcpadapter.GatewayQueryResult(ctx, g.call.ID, g.server, g.call.Name, g.t.queryFilter, last); err != nil {
			// The call ran; the projection failed. The query tool reports it
			// as a call that may have succeeded.
			return session.ToolResult{}, diagnosticCodeCompleted, err
		}
	default:
		res = mcpadapter.GatewayToolResult(g.call.ID, g.server, g.call.Name, last)
	}
	if last.DeclinedRequests > 0 {
		res = withDeclinedNote(res)
	}
	return res, code, nil
}

// authorize asks the identity broker for this call's gateway token. On
// svid_expired it renews the agent credential and asks once more, reporting
// the same rejected attempt.
func (g *gatewayCall) authorize(ctx context.Context, rejectedAttemptID string) (identitybroker.Authorization, error) {
	cred, err := g.r.gatewayCalls.Credentials.Credential(ctx, g.key, false)
	if err != nil {
		return identitybroker.Authorization{}, err
	}
	auth, err := g.r.gatewayCalls.Authorizer.AuthorizeCall(ctx, g.request(cred, rejectedAttemptID))
	if errors.Is(err, identitybroker.ReasonError(identitybroker.ReasonSVIDExpired, "")) {
		if cred, err = g.r.gatewayCalls.Credentials.Credential(ctx, g.key, true); err != nil {
			return identitybroker.Authorization{}, err
		}
		auth, err = g.r.gatewayCalls.Authorizer.AuthorizeCall(ctx, g.request(cred, rejectedAttemptID))
	}
	if err != nil {
		return identitybroker.Authorization{}, err
	}
	if auth.GatewayToken.IsZero() || auth.AttemptID == "" {
		return identitybroker.Authorization{}, errors.New("mcpbroker: authorization carries no gateway token or attempt")
	}
	g.brokerSession = cred.BrokerSessionID
	return auth, nil
}

func (g *gatewayCall) request(cred agentcredential.Credential, rejectedAttemptID string) identitybroker.AuthorizeCallRequest {
	return identitybroker.AuthorizeCallRequest{
		SessionID:         cred.BrokerSessionID,
		Credential:        cred.Token,
		OperationID:       g.op,
		RunID:             g.agentRun.ID,
		DelegationID:      g.delegationID,
		RunOwner:          g.agentRun.Owner,
		Method:            identitybroker.MethodToolsCall,
		Server:            g.server,
		Tool:              g.tool,
		ArgumentsSHA256:   g.hash,
		RejectedAttemptID: rejectedAttemptID,
	}
}

func (g *gatewayCall) claimCall(ctx context.Context) error {
	if err := g.r.gatewayCalls.SendOnce.Claim(ctx, g.scope.root, g.op, g.hash); err != nil {
		return err
	}
	g.claim = true
	return nil
}

func (g *gatewayCall) release(ctx context.Context) {
	if g.claim {
		_ = g.r.gatewayCalls.SendOnce.Release(context.WithoutCancel(ctx), g.scope.root, g.op, g.hash)
		g.claim = false
	}
}

// complete marks the claim completed, so the record may drop it to make room.
func (g *gatewayCall) complete(ctx context.Context) {
	if g.claim {
		_ = g.r.gatewayCalls.SendOnce.Complete(context.WithoutCancel(ctx), g.scope.root, g.op, g.hash)
	}
}

// releaseUnlessSent releases the claim if no attempt may have run: every
// attempt was not sent or refused.
func (g *gatewayCall) releaseUnlessSent(ctx context.Context) {
	for _, a := range g.attempts {
		if a.Outcome != mcpadapter.GatewayAttemptNotSent && a.Outcome != mcpadapter.GatewayAttemptRefused {
			return
		}
	}
	g.release(ctx)
}

func (g *gatewayCall) send(ctx context.Context, auth identitybroker.Authorization) mcpadapter.GatewayAttempt {
	gw := g.r.gateway
	// Shortcut: the request carries no run ID in trace headers; removed by #2566 (audit records and delivery, which passes the run ID in trace headers).
	a := g.r.sendGateway(ctx, mcpadapter.GatewayCallRequest{
		Endpoint:        gw.Endpoint(),
		HTTPClient:      gw.HTTPClient(),
		Token:           auth.GatewayToken,
		OperationID:     g.op,
		AttemptID:       auth.AttemptID,
		Tool:            g.tool,
		Arguments:       g.args,
		ArgumentsSHA256: g.hash,
		SendBy:          auth.ExpiresAt,
		HashArguments:   hashArguments,
	})
	g.attempts = append(g.attempts, a)
	g.report(ctx, a)
	return a
}

const (
	// reportTries bounds the tries of one attempt's report, and
	// reportTryTimeout each try.
	reportTries      = 3
	reportTryTimeout = 2 * time.Second
	// reportRetryWait is the wait before the first retry; it doubles for
	// each one after.
	reportRetryWait = 50 * time.Millisecond
)

// report reports attempt a's dispatch and observed outcome to the identity
// broker. It runs even when ctx is cancelled, retries a report that failed
// for a reason that may pass, and never fails the call: a report still
// failing is only logged. It carries references and fixed values only.
//
// Shortcut: a report still lost after its retries goes undetected until reconciliation compares reported dispatches with the vMCP's tool calls; removed by #2848.
func (g *gatewayCall) report(ctx context.Context, a mcpadapter.GatewayAttempt) {
	ctx = context.WithoutCancel(ctx)
	dispatch, outcome, cause := a.Report()
	req := identitybroker.ReportCallRequest{
		SessionID: g.brokerSession, OperationID: g.op, AttemptID: a.AttemptID,
		Dispatch: dispatch, Outcome: outcome, Cause: cause,
	}
	var err error
	wait := reportRetryWait
	tries := 0
	for tries < reportTries {
		if tries > 0 {
			time.Sleep(wait)
			wait *= 2
		}
		tries++
		tryCtx, cancel := context.WithTimeout(ctx, reportTryTimeout)
		err = g.r.gatewayCalls.Reporter.ReportCall(tryCtx, req)
		cancel()
		if err == nil || !retryableReport(err) {
			break
		}
	}
	if err == nil {
		return
	}
	args := []any{"event", diagnosticEventGatewayCallReport, "operation", g.op, "attempt", a.AttemptID,
		"dispatch", string(dispatch), "outcome", string(outcome), "tries", tries,
		"session", string(g.t.attachment.logical.ref.SessionID())}
	var brokerErr *identitybroker.Error
	if errors.As(err, &brokerErr) {
		args = append(args, "broker_code", string(brokerErr.Code), "broker_reason", string(brokerErr.Reason))
	} else {
		args = append(args, "cause", failureCause(err))
	}
	g.r.diag.Log(ctx, port.LevelWarn, "mcp broker gateway call report failed", args...)
}

// retryableReport reports whether a failed report may succeed if sent
// again: the broker was unavailable, the report lost a race, or a try ran
// out of time.
func retryableReport(err error) bool {
	var brokerErr *identitybroker.Error
	if errors.As(err, &brokerErr) {
		return brokerErr.Code == identitybroker.CodeUnavailable || brokerErr.Code == identitybroker.CodeAborted
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// failureCause names a failure before a send that is not the identity
// broker's refusal, from fixed text only: an error from the arguments could
// quote them.
func failureCause(err error) string {
	for _, known := range []error{
		agentcredential.ErrNoCredential, agentcredential.ErrDropped,
		agentcredential.ErrOtherAgent, agentcredential.ErrExpired,
		ErrAlreadySent, errSendOnceFull, errNotIJSON, errCaseFoldedMembers, errArgumentsTooLarge,
		context.DeadlineExceeded, context.Canceled,
	} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "unclassified"
}

// log records the call's outcome; it never carries a token or arguments. A
// refusal by the identity broker adds its code, reason, and detail, which
// never holds credentials and names refusals, such as AS-1's issuance
// reasons, that the outcome reports only as internal; any other failure
// before a send adds its cause, and the last attempt its HTTP status.
func (g *gatewayCall) log(ctx context.Context, code string) {
	ids := make([]string, len(g.attempts))
	outcome, status := "", 0
	for i, a := range g.attempts {
		ids[i] = a.AttemptID
		outcome, status = string(a.Outcome), a.HTTPStatus
	}
	level := port.LevelInfo
	if code != diagnosticCodeCompleted {
		level = port.LevelWarn
	}
	args := []any{"event", diagnosticEventGatewayCall, "code", code, "operation", g.op,
		"attempts", strings.Join(ids, ","), "outcome", outcome, "http_status", status,
		"session", string(g.t.attachment.logical.ref.SessionID())}
	if g.cause != "" {
		args = append(args, "cause", g.cause)
	}
	if g.authErr != nil {
		args = append(args, "broker_code", string(g.authErr.Code), "broker_reason", string(g.authErr.Reason), "broker_detail", g.authErr.Detail)
	}
	g.r.diag.Log(ctx, level, "mcp broker gateway call", args...)
}
