package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

const (
	// gatewayProtocolVersion is the newest MCP revision the gateway paths
	// speak; the SDK sends initialize with it and accepts an older answer.
	// Calls depend on it: on 2026-07-28 the SDK probes server/discover and
	// runs sessionless, and the vMCP answers a call needing URL elicitation
	// with an "input_required" result rather than error -32042, which
	// classify would take as completed. The session-not-found rejection
	// assumes a session, and on 2026-07-28 closing the request's stream, not
	// notifications/cancelled, cancels a call. Listings use the same revision
	// so one gateway sees one protocol. Moving to 2026-07-28 is #2917.
	gatewayProtocolVersion = "2025-11-25"
	gatewayMetaOperation   = "dev.mecatl/operation"
	gatewayMetaAttempt     = "dev.mecatl/attempt"
	gatewayToolsCall       = "tools/call"
	httpsScheme            = "https"
	maxConnectURLBytes     = 2048
	// maxObservedReplyBytes bounds what is kept of the tools/call response
	// while looking for its JSON-RPC reply; past it the reply is not seen,
	// and only a result the SDK returns shows the call completed.
	maxObservedReplyBytes = 8 << 20
	// cancelNotifyWait bounds the wait for the SDK's notifications/cancelled
	// before the session closes; it matches the SDK's own bound.
	cancelNotifyWait = 5 * time.Second
	// gatewaySendMargin is how long before SendBy the token is last
	// presented for the handshake and the tools/call. ToolHive's vMCP checks
	// exp with no leeway, and once exp has passed it answers a token that
	// already started a call with token_expired rather than token_reused,
	// which reads as a plain invalid_token that a caller retries; so the
	// tools/call must reach the vMCP before exp. The embedded vMCP shares
	// Mecatl's clock and is reached in process, so the margin covers only the
	// time from the check to the vMCP's own: well under a millisecond, more
	// under load. 250ms covers that many times over and keeps three quarters
	// of the shortest ID-JAG lifetime the broker accepts (one second).
	gatewaySendMargin = 250 * time.Millisecond
)

// errGatewayRefused is the transport error for a request refused before it
// was written. It never reaches the caller of SendGatewayCall.
var errGatewayRefused = errors.New("mcp: gateway request refused before sending")

// GatewayCallRequest is one authorized tools/call to send to the MCP gateway.
type GatewayCallRequest struct {
	// Endpoint is the gateway's MCP endpoint URL.
	Endpoint string
	// HTTPClient carries every request; its Transport and Timeout are used,
	// and redirects are never followed.
	HTTPClient *http.Client
	// Token is the gateway token, only ever set in the Authorization header.
	Token identitybroker.Token
	// OperationID and AttemptID go into _meta of every JSON-RPC message.
	OperationID, AttemptID string
	// Tool is params.name as the gateway routes it.
	Tool string
	// Arguments is exactly the params.arguments value; empty sends {}.
	Arguments json.RawMessage
	// ArgumentsSHA256 is the authorized hash; the tools/call body is checked
	// against it with HashArguments before it is written.
	ArgumentsSHA256 string
	// SendBy is when the authorization lapses. Every request before the
	// tools/call, and the tools/call itself, is written at least
	// gatewaySendMargin before it, or not at all; the messages of a call that
	// has started are not limited by it.
	SendBy time.Time
	// HashArguments computes the argument hash the authorization was issued
	// for.
	HashArguments func(json.RawMessage) (string, error)

	// hooks lets in-package tests change what is sent and received.
	hooks gatewayHooks
}

type gatewayHooks struct{ sending, receiving mcpsdk.Middleware }

// GatewayAttemptOutcome is what is known about whether an attempt's
// tools/call ran.
type GatewayAttemptOutcome string

const (
	// GatewayAttemptNotSent means the tools/call was never written (a handshake
	// failure or a local refusal).
	GatewayAttemptNotSent GatewayAttemptOutcome = "not_sent"
	// GatewayAttemptRefused means the tools/call was answered with one of the
	// answers that mean it never ran.
	GatewayAttemptRefused GatewayAttemptOutcome = "refused"
	// GatewayAttemptCompleted means the gateway answered the tools/call with a
	// result, or with a JSON-RPC error that means the request was not processed.
	GatewayAttemptCompleted GatewayAttemptOutcome = "completed"
	// GatewayAttemptUnknown means the tools/call was or may have been written, and
	// no answer, an HTTP error, or another JSON-RPC error came back. It may have
	// run.
	GatewayAttemptUnknown GatewayAttemptOutcome = "unknown"
)

// GatewayRejection names why an attempt was not sent or was refused.
type GatewayRejection string

const (
	// GatewayRejectionInvalidToken means 401 with error="invalid_token", on the
	// handshake or the tools/call.
	GatewayRejectionInvalidToken GatewayRejection = "invalid_token"
	// GatewayRejectionTokenReused means 401 with error="invalid_token"
	// naming token_reused, as ToolHive's vMCP answers a call-bound token that
	// has already started a call. This request did not run, but a token is
	// issued for one call alone, so the call it names may already have run
	// with it: the attempt's outcome is unknown, and it is not retried
	// (credentials-and-lifecycle.md, step (9), check 3).
	GatewayRejectionTokenReused GatewayRejection = "token_reused"
	// GatewayRejectionDenied means 403 refusing the request before the
	// gateway ran it: with error="insufficient_scope" (RFC 6750 §3.1), or,
	// as ToolHive's vMCP answers, with a JSON-RPC error of code 403 answering
	// the request's own ID. ToolHive's call binding answers so a tools/call
	// other than the one its token's call entry names, and its
	// authorization a call its policy denies
	// (credentials-and-lifecycle.md, step (9), checks 3 and 4).
	GatewayRejectionDenied GatewayRejection = "denied"
	// GatewayRejectionSessionNotFound means 404 for a request naming a session.
	GatewayRejectionSessionNotFound GatewayRejection = "session_not_found"
	// GatewayRejectionTooLarge means 413: the gateway refused the request
	// because its body is larger than it accepts (RFC 9110 §15.5.14).
	// ToolHive's vMCP answers so, before it parses the body, a request over
	// its own body limit or that of the MCP SDK's handler serving it. Unlike
	// a 403, the status alone suffices: 413 is by definition a refusal to
	// process the request, and the vMCP never relays a backend's HTTP status.
	GatewayRejectionTooLarge GatewayRejection = "request_too_large"
	// GatewayRejectionURLElicitation means -32042 in reply to the tools/call.
	GatewayRejectionURLElicitation GatewayRejection = "url_elicitation_required"
	// GatewayRejectionChanged means the tools/call about to be written differs in
	// name or argument hash from the authorized one.
	GatewayRejectionChanged GatewayRejection = "changed_after_authorization"
	// GatewayRejectionExpired means SendBy, less gatewaySendMargin, passed
	// before the tools/call was written: the tools/call was not sent with the
	// token, and neither was any later handshake request.
	GatewayRejectionExpired GatewayRejection = "authorization_expired"
)

// GatewayAttempt is the result of one SendGatewayCall.
type GatewayAttempt struct {
	AttemptID string
	Outcome   GatewayAttemptOutcome
	// Rejection is set for a refused attempt and for a not-sent one whose
	// reason is known.
	Rejection GatewayRejection
	// ConnectURL is the validated link of a URL elicitation, empty when the
	// gateway sent none or an invalid one.
	ConnectURL string
	// DeclinedRequests counts the requests other than ping the gateway sent
	// during the attempt, all declined.
	DeclinedRequests int
	// HTTPStatus is the status the tools/call got, or the failing handshake
	// request's when the tools/call was not written; 0 for none.
	HTTPStatus int

	result *mcpsdk.CallToolResult
	rpcErr *jsonrpc.Error
}

// Report is what attempt a reports of itself to the identity broker: its
// dispatch, its observed outcome, and its rejection as the cause. A
// tools/call never written is a dispatch failure, which proves it did not
// run, with no outcome. Any other attempt was, or may have been, dispatched:
// a completed call with a result that is not a tool error observed success,
// a refused one or any other completed one failure, and an unknown one
// unknown. A token_reused answer before the write counts as dispatched with
// an unknown outcome: this attempt's tools/call was not written, but its
// token may already have started the call. A tool error counts as a
// completed call's failure under the #2822 shortcut on classify, though the
// vMCP may have sent the call to its backend before failing it. The report
// carries no token, arguments, or result content.
func (a GatewayAttempt) Report() (dispatch identitybroker.DispatchOutcome, outcome identitybroker.ObservedOutcome, cause string) {
	cause = string(a.Rejection)
	switch a.Outcome {
	case GatewayAttemptNotSent:
		return identitybroker.DispatchFailure, "", cause
	case GatewayAttemptCompleted:
		if a.result != nil && !a.result.IsError {
			return identitybroker.DispatchSuccess, identitybroker.ObservedSuccess, cause
		}
		return identitybroker.DispatchSuccess, identitybroker.ObservedFailure, cause
	case GatewayAttemptRefused:
		return identitybroker.DispatchSuccess, identitybroker.ObservedFailure, cause
	default:
		return identitybroker.DispatchSuccess, identitybroker.ObservedUnknown, cause
	}
}

// SendGatewayCall sends req's tools/call at most once, over a vMCP session
// of its own, and reports what is known about whether it ran. It never
// sends the call again: a caller that retries makes a new attempt.
func SendGatewayCall(ctx context.Context, req GatewayCallRequest) GatewayAttempt {
	attempt := GatewayAttempt{AttemptID: req.AttemptID, Outcome: GatewayAttemptNotSent}
	if req.Endpoint == "" || req.HTTPClient == nil || req.Token.IsZero() || req.OperationID == "" ||
		req.AttemptID == "" || req.Tool == "" || req.ArgumentsSHA256 == "" || req.HashArguments == nil {
		return attempt
	}
	sendBy := req.SendBy.Add(-gatewaySendMargin)
	if !time.Now().Before(sendBy) {
		attempt.Rejection = GatewayRejectionExpired
		return attempt
	}
	wire := newGatewayWire(req, sendBy)
	var declined atomic.Int64
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: clientName, Version: clientVersion}, &mcpsdk.ClientOptions{
		// URL elicitation only: no roots, no sampling, no form elicitation.
		Capabilities: &mcpsdk.ClientCapabilities{Elicitation: &mcpsdk.ElicitationCapabilities{URL: &mcpsdk.URLElicitationCapabilities{}}},
		ElicitationHandler: func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
			return &mcpsdk.ElicitResult{Action: "decline"}, nil
		},
		// The multi-round-trip middleware sends tools/call again when the
		// server asks for input or sheds load.
		MultiRoundTrip: &mcpsdk.MultiRoundTripOptions{Disabled: true},
	})
	client.AddReceivingMiddleware(declineGatewayRequests(&declined))
	if req.hooks.sending != nil {
		client.AddSendingMiddleware(req.hooks.sending)
	}
	if req.hooks.receiving != nil {
		client.AddReceivingMiddleware(req.hooks.receiving)
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint: req.Endpoint,
		HTTPClient: &http.Client{
			Transport:     wire,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       req.HTTPClient.Timeout,
		},
		MaxRetries:           -1, // never resume a call's stream with a GET
		DisableStandaloneSSE: true,
	}

	// Shortcut: each gateway call uses its own vMCP session, and a 404 for the session fails the call; removed by #2564 (vMCP session reuse and a new session on 404).
	// The handshake must finish while the token can still be sent; the SDK
	// detaches the session from this context once connected.
	connectCtx, cancelConnect := context.WithDeadline(ctx, sendBy)
	cs, err := client.Connect(connectCtx, transport, &mcpsdk.ClientSessionOptions{ProtocolVersion: gatewayProtocolVersion})
	cancelConnect()
	var res *mcpsdk.CallToolResult
	var callErr error
	if err == nil {
		var args any // an empty RawMessage would send null
		if len(req.Arguments) > 0 {
			args = req.Arguments
		}
		res, callErr = cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: req.Tool, Arguments: args})
		if sdkSentCancelled(callErr, ctx.Err()) {
			wire.awaitCancelled()
		}
		_ = cs.Close()
	}
	attempt.DeclinedRequests = int(declined.Load())
	return wire.classify(attempt, res, callErr)
}

// declineGatewayRequests declines every request the gateway sends except
// ping, and counts them. Elicitations go on to the ElicitationHandler, which
// declines them. The shortcut is marked on mcpbroker's connection_required
// outcome (#2821).
func declineGatewayRequests(declined *atomic.Int64) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "ping" || strings.HasPrefix(method, "notifications/") {
				return next(ctx, method, req)
			}
			declined.Add(1)
			if method == "elicitation/create" {
				return next(ctx, method, req)
			}
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "declined: requests during a gateway call are not answered"}
		}
	}
}

// GatewayToolResult maps a completed attempt to the model-facing result as
// remoteTool does; tool is the name the model called. An attempt that did
// not complete gives a tool error.
func GatewayToolResult(callID session.ToolCallID, server, tool string, a GatewayAttempt) session.ToolResult {
	switch {
	case a.Outcome != GatewayAttemptCompleted:
		return session.NewToolError(callID, "mcp call failed: the gateway call did not complete")
	case a.rpcErr != nil:
		return session.NewToolError(callID, fmt.Sprintf("mcp call failed: %v", a.rpcErr))
	case a.result == nil:
		return session.NewToolError(callID, "mcp call failed: the gateway call did not complete")
	}
	// Shortcut: a gateway result is not checked against the output schema its listing declares; removed by #2920 (check gateway tool results against their listed output schema).
	return mapCallToolResult(callID, a.result, nil, tool, server)
}

// GatewayQueryResult projects a completed attempt's result through filter
// as CallMcpWithQuery does.
func GatewayQueryResult(ctx context.Context, callID session.ToolCallID, server, tool, filter string, a GatewayAttempt) (session.ToolResult, error) {
	if a.Outcome != GatewayAttemptCompleted || a.rpcErr != nil || a.result == nil {
		return GatewayToolResult(callID, server, tool, a), nil
	}
	return FilterCallResult(ctx, callID, server, tool, filter, rawCallResult(server, tool, a.result))
}

// gatewayWire is one attempt's HTTP transport. It authorizes every request,
// puts the attempt's _meta into every JSON-RPC message, refuses a tools/call
// other than the authorized one or a second one, and records what the
// gateway answered the tools/call.
type gatewayWire struct {
	base            http.RoundTripper
	authorization   string
	meta            map[string]json.RawMessage
	tool            string
	argumentsSHA256 string
	hash            func(json.RawMessage) (string, error)
	// sendBy is SendBy less gatewaySendMargin: no request is written after it
	// until the tools/call has started.
	sendBy time.Time

	cancelled  chan struct{}
	cancelOnce sync.Once

	mu          sync.Mutex
	refusal     GatewayRejection
	handshake   gatewayAnswer // the first refusing answer before the tools/call
	callStarted bool
	callWritten bool // the tools/call was or may have been written
	call        gatewayAnswer
	callID      json.RawMessage
	reply       *gatewayReply // the JSON-RPC response to callID, as received
}

type gatewayAnswer struct {
	status    int
	rejection GatewayRejection
}

type gatewayReply struct{ err *jsonrpc.Error }

func newGatewayWire(req GatewayCallRequest, sendBy time.Time) *gatewayWire {
	base := req.HTTPClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	quote := func(s string) json.RawMessage { b, _ := json.Marshal(s); return b }
	return &gatewayWire{
		base:            base,
		authorization:   "Bearer " + req.Token.Value(),
		meta:            map[string]json.RawMessage{gatewayMetaOperation: quote(req.OperationID), gatewayMetaAttempt: quote(req.AttemptID)},
		tool:            req.Tool,
		argumentsSHA256: req.ArgumentsSHA256,
		hash:            req.HashArguments,
		sendBy:          sendBy,
		cancelled:       make(chan struct{}),
	}
}

func (w *gatewayWire) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}
	// Until the tools/call has started, the token is presented only before
	// sendBy: roundTripOther checks a request other than the tools/call, and
	// startCall the tools/call itself, just before it is written. Nothing is
	// written before those checks.
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", w.authorization)
	var msg gatewayMessage
	if req.Method == http.MethodPost && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if msg, err = withMeta(body, w.meta); err != nil {
			return nil, fmt.Errorf("%w: %w", errGatewayRefused, err)
		}
		out.Body = io.NopCloser(bytes.NewReader(msg.body))
		out.ContentLength = int64(len(msg.body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(msg.body)), nil }
	}
	if msg.method != gatewayToolsCall {
		return w.roundTripOther(out, msg.method)
	}
	if !w.authorized(msg.params) {
		return nil, w.refuse(GatewayRejectionChanged)
	}
	if err := w.startCall(msg.id); err != nil {
		return nil, err
	}

	// Only a failure while net/http was still obtaining a connection proves
	// that no byte of the tools/call was written (see dispatchRecordingTransport).
	var getConn, gotConn atomic.Bool
	traced := out.WithContext(httptrace.WithClientTrace(out.Context(), &httptrace.ClientTrace{
		GetConn: func(string) { getConn.Store(true) },
		GotConn: func(httptrace.GotConnInfo) { gotConn.Store(true) },
	}))
	resp, err := w.base.RoundTrip(traced)
	// The answer, a 403's body included, is read before taking the lock, so a
	// slow body never holds up the call's cancellation.
	var answer gatewayAnswer
	if err == nil {
		answer = callAnswerOf(out, resp, msg.id)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.callWritten = !getConn.Load() || gotConn.Load()
		return nil, err
	}
	w.callWritten = true
	w.call = answer
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.Body != nil {
		resp.Body = &replyWatcher{
			body: resp.Body, wire: w,
			stream: strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"),
		}
	}
	return resp, nil
}

// roundTripOther sends a request other than the tools/call and notes a
// refusing answer to the handshake.
func (w *gatewayWire) roundTripOther(out *http.Request, method string) (*http.Response, error) {
	w.mu.Lock()
	cancelHandshake := method == "notifications/cancelled" && !w.callStarted
	w.mu.Unlock()
	if cancelHandshake {
		// Initialize must not be cancelled. The SDK sends this notification
		// on a detached context; waiting for it can stall failed-handshake cleanup.
		// It is refused whatever the time, so it records no refusal: after a
		// hung handshake it can arrive past sendBy.
		return nil, errGatewayRefused
	}
	w.mu.Lock()
	callStarted := w.callStarted
	w.mu.Unlock()
	if !callStarted && !time.Now().Before(w.sendBy) {
		return nil, w.refuse(GatewayRejectionExpired)
	}
	resp, err := w.base.RoundTrip(out)
	if method == "notifications/cancelled" {
		w.cancelOnce.Do(func() { close(w.cancelled) })
	}
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	if !w.callStarted && w.handshake.status == 0 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		w.handshake = answerOf(out, resp)
	}
	w.mu.Unlock()
	return resp, nil
}

// authorized reports whether a tools/call's params name the authorized tool
// and carry arguments with the authorized hash.
func (w *gatewayWire) authorized(params map[string]json.RawMessage) bool {
	var name string
	if json.Unmarshal(params["name"], &name) != nil || name != w.tool {
		return false
	}
	sum, err := w.hash(params["arguments"])
	return err == nil && sum == w.argumentsSHA256
}

// startCall marks the tools/call with id as started, just before it is
// written, unless a tools/call has already started or sendBy has passed. The
// deadline is checked here, after the argument hash and with nothing left
// before the write: a tools/call refused here was not written, so nothing was
// sent for this call with the token, and the attempt is not sent.
func (w *gatewayWire) startCall(id json.RawMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.callStarted:
		return errGatewayRefused
	case !time.Now().Before(w.sendBy):
		w.refuseLocked(GatewayRejectionExpired)
		return errGatewayRefused
	}
	w.callStarted, w.callID = true, id
	return nil
}

// refuse records r as why the attempt was not sent, unless an earlier
// refusal or a refusing handshake answer already says why: the first one
// wins. The SDK closes a failed handshake's session with a DELETE, which the
// send-by deadline may refuse after the gateway answered, say, invalid_token.
func (w *gatewayWire) refuse(r GatewayRejection) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refuseLocked(r)
	return errGatewayRefused
}

// refuseLocked is refuse with w.mu held.
func (w *gatewayWire) refuseLocked(r GatewayRejection) {
	if w.refusal == "" && w.handshake.status == 0 {
		w.refusal = r
	}
}

// sdkSentCancelled reports whether the SDK sent notifications/cancelled for
// the call: it does so exactly when CallTool returns the context's error.
func sdkSentCancelled(callErr, ctxErr error) bool {
	return ctxErr != nil && errors.Is(callErr, ctxErr)
}

// awaitCancelled waits, bounded, for the SDK's notifications/cancelled after
// a written tools/call, so closing the session does not race it.
func (w *gatewayWire) awaitCancelled() {
	w.mu.Lock()
	written := w.callWritten
	w.mu.Unlock()
	if !written {
		return
	}
	timer := time.NewTimer(cancelNotifyWait)
	defer timer.Stop()
	select {
	case <-w.cancelled:
	case <-timer.C:
	}
}

// observeReply records msg if it is the JSON-RPC response to the tools/call.
func (w *gatewayWire) observeReply(msg []byte) {
	var r struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *jsonrpc.Error  `json:"error"`
	}
	if json.Unmarshal(msg, &r) != nil || r.Method != "" || (len(r.Result) == 0 && r.Error == nil) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reply == nil && sameID(r.ID, w.callID) {
		w.reply = &gatewayReply{err: r.Error}
	}
}

// classify decides the attempt's outcome. Only the answers that mean "never
// ran" refuse a written tools/call: those answerOf and callAnswerOf name, and
// -32042 in reply to the tools/call itself. A request the gateway sent during
// the call does not.
func (w *gatewayWire) classify(a GatewayAttempt, res *mcpsdk.CallToolResult, callErr error) GatewayAttempt {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.callWritten {
		a.Outcome, a.Rejection, a.HTTPStatus = GatewayAttemptNotSent, w.refusal, w.handshake.status
		if a.Rejection == "" {
			a.Rejection = w.handshake.rejection
		}
		if a.Rejection == GatewayRejectionTokenReused {
			a.Outcome = GatewayAttemptUnknown
		}
		return a
	}
	a.HTTPStatus = w.call.status
	switch {
	case w.call.rejection == GatewayRejectionTokenReused:
		a.Outcome, a.Rejection = GatewayAttemptUnknown, w.call.rejection
	case w.call.rejection != "":
		a.Outcome, a.Rejection = GatewayAttemptRefused, w.call.rejection
	case w.reply != nil && w.reply.err != nil && w.reply.err.Code == mcpsdk.CodeURLElicitationRequired:
		a.Outcome, a.Rejection = GatewayAttemptRefused, GatewayRejectionURLElicitation
		a.ConnectURL = connectLink(w.reply.err.Data)
	case w.reply != nil && w.reply.err != nil && notProcessed(w.reply.err.Code):
		a.Outcome, a.rpcErr = GatewayAttemptCompleted, w.reply.err
	case w.reply != nil && w.reply.err != nil: // such as -32603: the tool may have run
		a.Outcome = GatewayAttemptUnknown
	case callErr == nil && res != nil:
		// The SDK returns a result only from the call's own reply, so this
		// holds even when the reply was too large to observe.
		// Shortcut: a tool error result counts as completed, though the vMCP also returns backend failures after it sent the call (timeout, dropped connection) as tool errors; removed by #2822 (the gateway says whether it sent the call to the backend).
		a.Outcome, a.result = GatewayAttemptCompleted, res
	default: // no reply reached the caller (dropped, timed out, an HTTP error, or cancelled): it may have run
		a.Outcome = GatewayAttemptUnknown
	}
	return a
}

// notProcessed reports whether code is a JSON-RPC 2.0 error that rejects the
// request itself (parse error, invalid request, method not found, or invalid
// params), so the tool did not run.
func notProcessed(code int64) bool {
	return code == jsonrpc.CodeParseError || code == jsonrpc.CodeInvalidRequest ||
		code == jsonrpc.CodeMethodNotFound || code == jsonrpc.CodeInvalidParams
}

// answerOf is the status of resp and, when it means the request never ran,
// why: 401 invalid_token (token_reused apart), 403 insufficient_scope, 404
// for a request naming a session, or 413.
func answerOf(req *http.Request, resp *http.Response) gatewayAnswer {
	a := gatewayAnswer{status: resp.StatusCode}
	switch {
	case resp.StatusCode == http.StatusUnauthorized && slices.ContainsFunc(resp.Header.Values("WWW-Authenticate"), isTokenReusedChallenge):
		a.rejection = GatewayRejectionTokenReused
	case resp.StatusCode == http.StatusUnauthorized && slices.ContainsFunc(resp.Header.Values("WWW-Authenticate"), isInvalidTokenChallenge):
		a.rejection = GatewayRejectionInvalidToken
	case resp.StatusCode == http.StatusForbidden && slices.ContainsFunc(resp.Header.Values("WWW-Authenticate"), isInsufficientScopeChallenge):
		a.rejection = GatewayRejectionDenied
	case resp.StatusCode == http.StatusNotFound && req.Header.Get("Mcp-Session-Id") != "":
		a.rejection = GatewayRejectionSessionNotFound
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		a.rejection = GatewayRejectionTooLarge
	}
	return a
}

// callAnswerOf is answerOf for the tools/call with ID id, which also tells
// a 403 that ToolHive's vMCP answered before running it (deniedBefore).
func callAnswerOf(req *http.Request, resp *http.Response, id json.RawMessage) gatewayAnswer {
	a := answerOf(req, resp)
	if a.rejection == "" && resp.StatusCode == http.StatusForbidden && deniedBefore(resp, id) {
		a.rejection = GatewayRejectionDenied
	}
	return a
}

// isInvalidTokenChallenge reports a WWW-Authenticate value carrying a Bearer
// challenge with error="invalid_token" (RFC 6750 §3). A value that does not
// parse is not one.
func isInvalidTokenChallenge(header string) bool { return hasBearerError(header, "invalid_token") }

// tokenReusedReason is how ToolHive's vMCP names a call-bound token that has
// already started a call in an invalid_token challenge's error_description.
// The match depends on ToolHive's description text, which is not a stable
// contract: a machine-readable reason for call-binding refusals is requested
// in stacklok/toolhive#7362. Until it exists, a change to that text would make a spent
// token look like any rejected one and be retried with a new token.
const tokenReusedReason = "(token_reused)" // #nosec G101 -- a reason code, not a credential.

// isTokenReusedChallenge reports a WWW-Authenticate value carrying a Bearer
// invalid_token challenge whose error_description names token_reused.
func isTokenReusedChallenge(header string) bool {
	challenges, err := parseDirectChallenges(header)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(challenges, func(c directChallenge) bool {
		return strings.EqualFold(c.scheme, "Bearer") && c.params["error"] == "invalid_token" &&
			strings.Contains(c.params["error_description"], tokenReusedReason)
	})
}

// jsonRPCCodeDenied is the JSON-RPC error code ToolHive's vMCP answers a
// request it refuses before dispatch with, alongside HTTP 403.
const jsonRPCCodeDenied = 403

// maxDenialBody bounds how much of a 403 answer deniedBefore reads.
const maxDenialBody = 64 << 10

// deniedBefore reports whether resp, a 403 answering the request with ID id,
// carries a JSON-RPC error of code jsonRPCCodeDenied for that ID: ToolHive's
// vMCP refused the request before running it. It restores the body it
// reads, so the caller still sees all of it.
func deniedBefore(resp *http.Response, id json.RawMessage) bool {
	if resp.Body == nil || len(id) == 0 {
		return false
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, maxDenialBody))
	resp.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(head), resp.Body), Closer: resp.Body}
	if err != nil {
		return false
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code int64 `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(head, &reply) != nil {
		return false
	}
	return reply.JSONRPC == "2.0" && reply.Error != nil && reply.Error.Code == jsonRPCCodeDenied && sameID(reply.ID, id)
}

// readCloser reads from Reader and closes Closer.
type readCloser struct {
	io.Reader
	io.Closer
}

// isInsufficientScopeChallenge reports a WWW-Authenticate value carrying a
// Bearer challenge with error="insufficient_scope" (RFC 6750 §3.1). A value
// that does not parse is not one.
func isInsufficientScopeChallenge(header string) bool {
	return hasBearerError(header, "insufficient_scope")
}

// hasBearerError reports a WWW-Authenticate value carrying a Bearer
// challenge whose error parameter is code.
func hasBearerError(header, code string) bool {
	challenges, err := parseDirectChallenges(header)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(challenges, func(c directChallenge) bool {
		return strings.EqualFold(c.scheme, "Bearer") && c.params["error"] == code
	})
}

// connectLink returns the link of the first URL-mode elicitation in a
// -32042 error's data, or "" when it is not an absolute https URL without
// user information, at most 2048 bytes, made only of the characters RFC 3986
// allows in a URI, and unchanged when parsed again. The link reaches the
// model, so a backend must not be able to add words to it.
// TODO(#2537): decide which origin the link must also be on (the vMCP's or AS-2's issuer's).
func connectLink(data json.RawMessage) string {
	type elicitation struct {
		Mode string `json:"mode"`
		URL  string `json:"url"`
	}
	var d struct {
		Elicitations []elicitation `json:"elicitations"`
	}
	if json.Unmarshal(data, &d) != nil {
		return ""
	}
	i := slices.IndexFunc(d.Elicitations, func(e elicitation) bool { return e.Mode == "url" })
	if i < 0 {
		return ""
	}
	link := d.Elicitations[i].URL
	if link == "" || len(link) > maxConnectURLBytes || strings.ContainsFunc(link, notURIChar) {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != httpsScheme || u.Host == "" || u.Opaque != "" || u.User != nil || u.String() != link {
		return ""
	}
	return link
}

// notURIChar reports whether r is outside RFC 3986's unreserved, reserved,
// and percent characters.
func notURIChar(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return false
	default:
		return !strings.ContainsRune("-._~:/?#[]@!$&'()*+,;=%", r)
	}
}

// gatewayMessage is a JSON-RPC message with the attempt's _meta added.
type gatewayMessage struct {
	body   []byte
	method string
	id     json.RawMessage
	params map[string]json.RawMessage
}

// withMeta adds meta to body's _meta: params._meta for a request or
// notification, result._meta for a result, error.data._meta for an error.
// Every other value, params.arguments included, keeps its bytes.
func withMeta(body []byte, meta map[string]json.RawMessage) (gatewayMessage, error) {
	top, err := jsonObject(body)
	if err != nil {
		return gatewayMessage{}, err
	}
	var msg gatewayMessage
	msg.id = top["id"]
	switch {
	case top["method"] != nil:
		if err := json.Unmarshal(top["method"], &msg.method); err != nil {
			return gatewayMessage{}, err
		}
		if msg.params, err = jsonObject(top["params"]); err != nil {
			return gatewayMessage{}, err
		}
		if msg.params["_meta"], err = mergeMeta(msg.params["_meta"], meta); err != nil {
			return gatewayMessage{}, err
		}
		top["params"] = encodeObject(msg.params)
	case top["result"] != nil:
		if top["result"], err = setMeta(top["result"], meta); err != nil {
			return gatewayMessage{}, err
		}
	case top["error"] != nil:
		e, err := jsonObject(top["error"])
		if err != nil {
			return gatewayMessage{}, err
		}
		if e["data"], err = setMeta(e["data"], meta); err != nil {
			return gatewayMessage{}, err
		}
		top["error"] = encodeObject(e)
	default:
		return gatewayMessage{}, errors.New("not a JSON-RPC message")
	}
	msg.body = encodeObject(top)
	return msg, nil
}

// setMeta adds meta to the _meta of the object raw (absent or null is {}).
func setMeta(raw json.RawMessage, meta map[string]json.RawMessage) (json.RawMessage, error) {
	obj, err := jsonObject(raw)
	if err != nil {
		return nil, err
	}
	if obj["_meta"], err = mergeMeta(obj["_meta"], meta); err != nil {
		return nil, err
	}
	return encodeObject(obj), nil
}

func mergeMeta(existing json.RawMessage, meta map[string]json.RawMessage) (json.RawMessage, error) {
	obj, err := jsonObject(existing)
	if err != nil {
		return nil, err
	}
	for k, v := range meta {
		obj[k] = v
	}
	return encodeObject(obj), nil
}

// jsonObject decodes an object's members without re-encoding them; absent
// or null is an empty object.
func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return obj, nil
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// encodeObject writes obj with its member values verbatim.
func encodeObject(obj map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, _ := json.Marshal(k)
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(obj[k])
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func sameID(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	return len(a) > 0 && json.Compact(&ca, a) == nil && json.Compact(&cb, b) == nil && bytes.Equal(ca.Bytes(), cb.Bytes())
}

// replyWatcher passes the tools/call response through to the SDK and
// records the JSON-RPC response to the call as it is read, before the SDK
// can deliver it.
type replyWatcher struct {
	body     io.ReadCloser
	wire     *gatewayWire
	stream   bool // text/event-stream; otherwise one JSON message
	buf      []byte
	event    []byte // the current event's name
	data     []byte // the current event's data
	isData   bool
	overflow bool
}

func (r *replyWatcher) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if !r.overflow && n > 0 {
		r.buf = append(r.buf, p[:n]...)
		if r.stream {
			r.scanEvents()
		}
		if len(r.buf)+len(r.data) > maxObservedReplyBytes {
			r.overflow, r.buf, r.data = true, nil, nil
		}
	}
	if err == io.EOF && !r.overflow {
		if r.stream {
			// As the SDK does, a last line and event left unterminated at EOF
			// still count.
			r.buf = append(r.buf, '\n', '\n')
			r.scanEvents()
		} else {
			r.wire.observeReply(r.buf)
		}
	}
	return n, err
}

func (r *replyWatcher) Close() error { return r.body.Close() }

// scanEvents consumes complete lines of the event stream, dispatching each
// event's data at its blank line.
func (r *replyWatcher) scanEvents() {
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			return
		}
		line := bytes.TrimSuffix(r.buf[:i], []byte("\r"))
		r.buf = r.buf[i+1:]
		switch {
		case len(line) == 0:
			// The SDK, too, reads only unnamed and "message" events.
			if r.isData && (len(r.event) == 0 || string(r.event) == "message") {
				r.wire.observeReply(r.data)
			}
			r.event, r.data, r.isData = r.event[:0], r.data[:0], false
		case bytes.HasPrefix(line, []byte("event:")):
			r.event = append(r.event[:0], bytes.TrimSpace(bytes.TrimPrefix(line, []byte("event:")))...)
		case bytes.HasPrefix(line, []byte("data:")):
			value := bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))
			if r.isData {
				r.data = append(r.data, '\n')
			}
			r.data, r.isData = append(r.data, value...), true
		}
	}
}
