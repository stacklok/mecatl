package identityharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// GatewayMCPPath is the path the gateway stand-in serves MCP at.
	GatewayMCPPath = "/mcp"
	// URLElicitationRequired is the JSON-RPC error code of MCP's URL
	// elicitation, which the gateway stand-in returns for a tools/call on a
	// server the user has not connected.
	URLElicitationRequired = -32042
	// MetaOperation is the _meta key of the operation ID the gateway
	// stand-in records from every JSON-RPC message.
	MetaOperation = "dev.mecatl/operation"
	// MetaAttempt is the _meta key of the attempt ID the gateway stand-in
	// records from every JSON-RPC message.
	MetaAttempt = "dev.mecatl/attempt"
	// ElicitationConnectURL is the URL the elicitation ElicitDuringCall sends
	// asks the user to open.
	ElicitationConnectURL = "https://gateway.example/connect/during-call"

	gatewayProtocolVersion = "2025-11-25"
	maxGatewayRequestBytes = 1 << 20
	sessionHeader          = "Mcp-Session-Id"
	toolsCall              = "tools/call"
	elicitReplyWait        = 5 * time.Second
)

// GatewayOptions configures a gateway stand-in.
type GatewayOptions struct {
	// Verify checks a bearer token; an error answers 401 invalid_token. Nil
	// accepts any bearer token.
	Verify func(token string) error
}

// Gateway stands in for the MCP gateway (the vMCP) for the broker's callers:
// a minimal streamable-HTTP JSON-RPC server for initialize, tools/list, and
// tools/call that needs a bearer token on every request. initialize opens a
// session; a request naming a session the stand-in does not hold gets 404,
// and one naming none is served without a session. A tools/call names a tool
// "server.tool" and answers with a text result naming it; tools/list lists no
// tools. Notifications and replies to the stand-in's own requests get 202.
// It is safe for concurrent use.
type Gateway struct {
	server *httptest.Server
	verify func(string) error

	mu          sync.Mutex
	rejectNext  bool
	rejectAll   bool
	missing     map[string]string // connect URL by server
	elicit      map[string]bool   // servers whose calls elicit during the call
	drop        map[string]bool   // servers whose calls drop the connection
	sessions    map[string]bool
	waiting     map[string]chan struct{} // by the ID of an open elicitation request
	elicitCount int
	calls       []GatewayCall
}

// GatewayCall is a request the gateway stand-in received.
type GatewayCall struct {
	// Method is the JSON-RPC method, empty for a reply, and DELETE for a
	// session termination. Tool is a tools/call's params.name.
	Method, Tool string
	// Status is the HTTP status the gateway answered; 0 when it dropped the
	// connection instead.
	Status int
	// TokenJTI is the bearer token's jti, read without verifying it; empty
	// when the token is absent or not a JWT with one.
	TokenJTI string
	// SessionID is the Mcp-Session-Id the request carried.
	SessionID string
	// Meta holds the MetaOperation and MetaAttempt string values of the
	// message's _meta (params._meta, result._meta, or error.data._meta); nil
	// when it has neither.
	Meta map[string]string
	// Arguments is a tools/call's params.arguments as received.
	Arguments json.RawMessage
	// Reply reports a JSON-RPC response from the client to a request of the
	// stand-in.
	Reply bool
}

// StartGateway starts a gateway stand-in on a loopback TLS server that
// HTTPClient trusts, and stops it when t ends.
func (*Harness) StartGateway(t testing.TB, opts GatewayOptions) *Gateway {
	t.Helper()
	g := &Gateway{
		verify:   opts.Verify,
		missing:  make(map[string]string),
		elicit:   make(map[string]bool),
		drop:     make(map[string]bool),
		sessions: make(map[string]bool),
		waiting:  make(map[string]chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+GatewayMCPPath, g.serve)
	mux.HandleFunc("DELETE "+GatewayMCPPath, g.serveDelete)
	g.server = httptest.NewTLSServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

// URL is the gateway's MCP endpoint.
func (g *Gateway) URL() string { return g.server.URL + GatewayMCPPath }

// RejectNextToken answers the next request, initialize included, with 401
// invalid_token whatever its token, as a gateway that no longer accepts it.
func (g *Gateway) RejectNextToken() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rejectNext = true
}

// RejectTokens answers every request with 401 invalid_token while on.
func (g *Gateway) RejectTokens(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rejectAll = on
}

// MissingConnection answers a tools/call on server with a URL elicitation
// to connectURL, as a gateway whose user has not connected that server.
func (g *Gateway) MissingConnection(server, connectURL string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.missing[server] = connectURL
}

// ElicitDuringCall answers a tools/call on server as an event stream that
// first sends a URL-mode elicitation/create request, waits up to 5s for the
// client's reply, and then sends the result.
func (g *Gateway) ElicitDuringCall(server string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.elicit[server] = true
}

// DropAfterRequest makes a tools/call on server read and record the whole
// request, then drop the connection without answering.
func (g *Gateway) DropAfterRequest(server string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.drop[server] = true
}

// ForgetSessions ends every open session, so the next request on one gets 404.
func (g *Gateway) ForgetSessions() {
	g.mu.Lock()
	defer g.mu.Unlock()
	clear(g.sessions)
}

// Calls returns the requests the gateway received, oldest first.
func (g *Gateway) Calls() []GatewayCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	var msg rpcMessage
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayRequestBytes))
	if err != nil || json.Unmarshal(raw, &msg) != nil {
		g.answer(w, GatewayCall{}, http.StatusBadRequest, nil)
		return
	}
	isReply := msg.Method == "" && len(msg.ID) > 0 && (len(msg.Result) > 0 || len(msg.Error) > 0)
	if msg.Method == "" && !isReply {
		g.answer(w, GatewayCall{}, http.StatusBadRequest, nil)
		return
	}
	call := GatewayCall{Method: msg.Method, SessionID: r.Header.Get(sessionHeader), Meta: messageMeta(msg), Reply: isReply}
	if msg.Method == toolsCall {
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		call.Tool, call.Arguments = params.Name, params.Arguments
	}
	if !g.authorize(w, r, &call) || !g.inSession(w, call) {
		return
	}
	if isReply {
		g.deliverReply(msg.ID)
		g.answer(w, call, http.StatusAccepted, nil)
		return
	}
	if msg.ID == nil { // a notification
		g.answer(w, call, http.StatusAccepted, nil)
		return
	}
	g.answerRequest(w, r, call, msg.ID)
}

// answerRequest answers a JSON-RPC request.
func (g *Gateway) answerRequest(w http.ResponseWriter, r *http.Request, call GatewayCall, id json.RawMessage) {
	g.mu.Lock()
	server := serverOf(call.Tool)
	connectURL, missing := g.missing[server]
	elicit, drop := g.elicit[server], g.drop[server]
	g.mu.Unlock()
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	switch {
	case call.Method == "initialize":
		session := randomToken("sess-")
		g.mu.Lock()
		g.sessions[session] = true
		g.mu.Unlock()
		w.Header().Set(sessionHeader, session)
		reply["result"] = map[string]any{
			"protocolVersion": gatewayProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "identityharness-gateway", "version": "test"},
		}
	case call.Method == "tools/list":
		reply["result"] = map[string]any{"tools": []any{}}
	case call.Method == toolsCall && drop:
		g.record(call, 0)
		panic(http.ErrAbortHandler)
	case call.Method == toolsCall && missing:
		reply["error"] = rpcError{
			Code: URLElicitationRequired, Message: "connect " + server + " first",
			Data: map[string]any{"elicitations": []any{map[string]any{
				"mode": "url", "elicitationId": randomToken("elicit-"), "url": connectURL,
				"message": "Connect " + server,
			}}},
		}
	case call.Method == toolsCall && elicit:
		g.elicitDuringCall(w, r, call, id)
		return
	case call.Method == toolsCall:
		reply["result"] = callResult(call.Tool)
	default:
		reply["error"] = rpcError{Code: -32601, Message: "method not found"}
	}
	g.answer(w, call, http.StatusOK, reply)
}

// serveDelete ends the session the request names.
func (g *Gateway) serveDelete(w http.ResponseWriter, r *http.Request) {
	call := GatewayCall{Method: http.MethodDelete, SessionID: r.Header.Get(sessionHeader)}
	if !g.authorize(w, r, &call) {
		return
	}
	if call.SessionID == "" {
		g.answer(w, call, http.StatusBadRequest, nil)
		return
	}
	if !g.inSession(w, call) {
		return
	}
	g.mu.Lock()
	delete(g.sessions, call.SessionID)
	g.mu.Unlock()
	g.answer(w, call, http.StatusNoContent, nil)
}

// authorize records the token's jti in call and reports whether the request
// may proceed; otherwise it has answered 401.
func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request, call *GatewayCall) bool {
	token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !hasBearer || token == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		g.answer(w, *call, http.StatusUnauthorized, nil)
		return false
	}
	call.TokenJTI = unverifiedJTI(token)
	g.mu.Lock()
	reject := g.rejectAll || g.rejectNext
	g.rejectNext = false
	g.mu.Unlock()
	if reject || (g.verify != nil && g.verify(token) != nil) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		g.answer(w, *call, http.StatusUnauthorized, nil)
		return false
	}
	return true
}

// inSession reports whether call names no session or one the stand-in holds;
// otherwise it has answered 404.
func (g *Gateway) inSession(w http.ResponseWriter, call GatewayCall) bool {
	if call.SessionID == "" {
		return true
	}
	g.mu.Lock()
	held := g.sessions[call.SessionID]
	g.mu.Unlock()
	if !held {
		g.answer(w, call, http.StatusNotFound, nil)
	}
	return held
}

// elicitDuringCall answers a tools/call as an event stream: an
// elicitation/create request, then, after the client's reply or 5s, the
// result.
func (g *Gateway) elicitDuringCall(w http.ResponseWriter, r *http.Request, call GatewayCall, id json.RawMessage) {
	replied := make(chan struct{})
	g.mu.Lock()
	g.elicitCount++
	elicitID := fmt.Sprintf("gw-elicit-%d", g.elicitCount)
	g.waiting[elicitID] = replied
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.waiting, elicitID)
		g.mu.Unlock()
	}()

	g.record(call, http.StatusOK)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	writeEvent(w, map[string]any{
		"jsonrpc": "2.0", "id": elicitID, "method": "elicitation/create",
		"params": map[string]any{
			"mode": "url", "elicitationId": randomToken("elicit-"), "url": ElicitationConnectURL,
			"message": "Open the link to continue",
		},
	})
	select {
	case <-replied:
	case <-r.Context().Done():
		return
	case <-time.After(elicitReplyWait):
	}
	writeEvent(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": callResult(call.Tool)})
}

// deliverReply wakes the open elicitation request id answers, if any.
func (g *Gateway) deliverReply(id json.RawMessage) {
	var key string
	if json.Unmarshal(id, &key) != nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch, ok := g.waiting[key]; ok {
		close(ch)
		delete(g.waiting, key)
	}
}

// writeEvent writes msg as one server-sent event and flushes it.
func writeEvent(w http.ResponseWriter, msg any) {
	data, _ := json.Marshal(msg)
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func callResult(tool string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": "called " + tool}}}
}

// messageMeta returns the MetaOperation and MetaAttempt values of msg's
// _meta, nil when it has neither.
func messageMeta(msg rpcMessage) map[string]string {
	var holder struct {
		Meta map[string]any `json:"_meta"`
	}
	switch {
	case msg.Method != "":
		_ = json.Unmarshal(msg.Params, &holder)
	case len(msg.Result) > 0:
		_ = json.Unmarshal(msg.Result, &holder)
	default:
		var e struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(msg.Error, &e) == nil {
			_ = json.Unmarshal(e.Data, &holder)
		}
	}
	var meta map[string]string
	for _, key := range []string{MetaOperation, MetaAttempt} {
		if v, ok := holder.Meta[key].(string); ok {
			if meta == nil {
				meta = make(map[string]string)
			}
			meta[key] = v
		}
	}
	return meta
}

// record appends call with status.
func (g *Gateway) record(call GatewayCall, status int) {
	call.Status = status
	g.mu.Lock()
	g.calls = append(g.calls, call)
	g.mu.Unlock()
}

// answer records call with status and writes status with body as JSON, or
// no body when body is nil.
func (g *Gateway) answer(w http.ResponseWriter, call GatewayCall, status int, body any) {
	g.record(call, status)
	if body == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// serverOf is the server part of a "server.tool" name.
func serverOf(tool string) string {
	server, _, _ := strings.Cut(tool, ".")
	return server
}

// HasJSONRPCResult reports whether body, an MCP response as JSON or as a
// server-sent event stream, carries a JSON-RPC result.
func HasJSONRPCResult(body []byte) bool {
	payloads := [][]byte{body}
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		payloads = nil
		for line := range bytes.Lines(body) {
			if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				payloads = append(payloads, data)
			}
		}
	}
	for _, p := range payloads {
		var msg struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(p, &msg) == nil && len(msg.Result) > 0 && string(msg.Result) != "null" {
			return true
		}
	}
	return false
}

// unverifiedJTI is token's jti, read without verifying it.
func unverifiedJTI(token string) string {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return ""
	}
	jti, _ := claims["jti"].(string)
	return jti
}
