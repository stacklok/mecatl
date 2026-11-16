package mcpbroker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	diagnosticEventGatewayCall = "gateway_call"
	diagnosticCodeCompleted    = "completed"
)

// gatewayScope is the run a gateway call belongs to, read from the tool
// call's context before the attachment operation replaces it.
type gatewayScope struct {
	acting, root string
	runSerial    int64
	ok           bool
}

func gatewayScopeFrom(ctx context.Context) gatewayScope {
	acting, hasActing := port.SessionIDFromContext(ctx)
	root, hasRoot := port.RootSessionIDFromContext(ctx)
	serial, hasSerial := port.RunSerialFromContext(ctx)
	return gatewayScope{
		acting: string(acting), root: string(root), runSerial: serial,
		ok: hasActing && hasRoot && hasSerial && acting != "" && root != "",
	}
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
	t      *sessionTool
	r      *Runtime
	call   session.ToolCall
	scope  gatewayScope
	key    agentcredential.Key
	op     string
	server string
	tool   string
	args   json.RawMessage
	hash   string
	claim  bool
	// authErr is the identity broker's refusal that ended the call, if any.
	authErr *identitybroker.Error
	// attempts are the attempts sent so far, oldest first.
	attempts []mcpadapter.GatewayAttempt
}

// executeGateway authorizes call with the identity broker and sends it to
// the gateway with a token for this call alone. Every refusal and unknown
// outcome is a tool error; only a cancelled context is a Go error.
func (t *sessionTool) executeGateway(ctx context.Context, scope gatewayScope, call session.ToolCall) (session.ToolResult, error) {
	g := &gatewayCall{t: t, r: t.attachment.runtime, call: call, scope: scope}
	result, code, err := g.run(ctx)
	// Shortcut: the MCP client reports dispatch and outcome to the broker only as a diagnostic, and a call cancelled after its tools/call was written returns the cancellation instead of an unknown-outcome report; removed by #2731 (dispatch and outcome reports).
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
		errors.As(err, &g.authErr)
		return outcomeForAuthorization(err)
	}
	if !g.scope.ok {
		return fail(outcomeIdentityUnavailable)
	}
	g.key = agentcredential.Key{RootSessionID: g.scope.root, AgentSessionID: g.scope.acting}
	g.op = operationID(g.scope.root, g.scope.acting, g.call.ID)
	var err error
	if g.server, g.tool, err = g.r.gateway.Route(g.t.route.backend, g.call.Name); err != nil {
		return fail(outcomeInternal)
	}
	// The argument hash is checked before anything is authorized.
	if g.args, g.hash, err = argumentsSHA256(g.call.Args); err != nil {
		return fail(refused(err))
	}
	defer g.releaseUnlessSent(ctx)

	auth, err := g.authorize(ctx, "")
	if err == nil {
		err = g.claimCall(ctx)
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
	return auth, nil
}

func (g *gatewayCall) request(cred agentcredential.Credential, rejectedAttemptID string) identitybroker.AuthorizeCallRequest {
	// Shortcut: run ID is the session's run serial and run owner the session owner, no delegation, accepted unchecked; removed by #2557 (run and owner checks with delegation).
	return identitybroker.AuthorizeCallRequest{
		SessionID:         cred.BrokerSessionID,
		Credential:        cred.Token,
		OperationID:       g.op,
		RunID:             "run-" + strconv.FormatInt(g.scope.runSerial, 10),
		RunOwner:          cred.Owner,
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
	return a
}

// log records the call's outcome; it never carries a token or arguments. A
// refusal by the identity broker adds its code, reason, and detail, which
// never holds credentials and names refusals, such as AS-1's issuance
// reasons, that the outcome reports only as internal.
func (g *gatewayCall) log(ctx context.Context, code string) {
	ids := make([]string, len(g.attempts))
	outcome := ""
	for i, a := range g.attempts {
		ids[i] = a.AttemptID
		outcome = string(a.Outcome)
	}
	level := port.LevelInfo
	if code != diagnosticCodeCompleted {
		level = port.LevelWarn
	}
	args := []any{"event", diagnosticEventGatewayCall, "code", code, "operation", g.op,
		"attempts", strings.Join(ids, ","), "outcome", outcome,
		"session", string(g.t.attachment.logical.ref.SessionID())}
	if g.authErr != nil {
		args = append(args, "broker_code", string(g.authErr.Code), "broker_reason", string(g.authErr.Reason), "broker_detail", g.authErr.Detail)
	}
	g.r.diag.Log(ctx, level, "mcp broker gateway call", args...)
}
