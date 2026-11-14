package identityharness

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// GatewayMCPPath is the path the gateway stand-in serves MCP at.
	GatewayMCPPath = "/mcp"
	// URLElicitationRequired is the JSON-RPC error code of MCP's URL
	// elicitation, which the gateway stand-in returns for a tools/call on a
	// server the user has not connected.
	URLElicitationRequired = -32042

	gatewayProtocolVersion = "2025-06-18"
	maxGatewayRequestBytes = 1 << 20
)

// GatewayOptions configures a gateway stand-in.
type GatewayOptions struct {
	// Verify checks a bearer token; an error answers 401 invalid_token. Nil
	// accepts any bearer token.
	Verify func(token string) error
}

// Gateway stands in for the MCP gateway (the vMCP) for the broker's callers:
// a minimal JSON-RPC server for initialize, tools/list, and tools/call that
// needs a bearer token on every request. A tools/call names a tool
// "server.tool" and answers with a text result naming it; tools/list lists no
// tools. It is safe for concurrent use.
type Gateway struct {
	server *httptest.Server
	verify func(string) error

	mu         sync.Mutex
	rejectNext bool
	rejectAll  bool
	missing    map[string]string // connect URL by server
	calls      []GatewayCall
}

// GatewayCall is a request the gateway stand-in received.
type GatewayCall struct {
	// Method is the JSON-RPC method and Tool a tools/call's params.name.
	Method, Tool string
	// Status is the HTTP status the gateway answered.
	Status int
	// TokenJTI is the bearer token's jti, read without verifying it; empty
	// when the token is absent or not a JWT with one.
	TokenJTI string
}

// StartGateway starts a gateway stand-in on a loopback TLS server that
// HTTPClient trusts, and stops it when t ends.
func (*Harness) StartGateway(t testing.TB, opts GatewayOptions) *Gateway {
	t.Helper()
	g := &Gateway{verify: opts.Verify, missing: make(map[string]string)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+GatewayMCPPath, g.serve)
	g.server = httptest.NewTLSServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

// URL is the gateway's MCP endpoint.
func (g *Gateway) URL() string { return g.server.URL + GatewayMCPPath }

// RejectNextToken answers the next request with 401 invalid_token whatever
// its token, as a gateway that no longer accepts it.
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

// Calls returns the requests the gateway received, oldest first.
func (g *Gateway) Calls() []GatewayCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayRequestBytes))
	if err != nil || json.Unmarshal(raw, &req) != nil || req.Method == "" {
		g.answer(w, GatewayCall{}, http.StatusBadRequest, nil)
		return
	}
	call := GatewayCall{Method: req.Method}
	if req.Method == "tools/call" {
		call.Tool = req.Params.Name
	}
	token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !hasBearer || token == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		g.answer(w, call, http.StatusUnauthorized, nil)
		return
	}
	call.TokenJTI = unverifiedJTI(token)
	g.mu.Lock()
	reject := g.rejectAll || g.rejectNext
	g.rejectNext = false
	connectURL, missing := g.missing[serverOf(call.Tool)]
	g.mu.Unlock()
	if reject || (g.verify != nil && g.verify(token) != nil) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		g.answer(w, call, http.StatusUnauthorized, nil)
		return
	}
	if req.ID == nil { // a notification
		g.answer(w, call, http.StatusAccepted, nil)
		return
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	switch {
	case req.Method == "initialize":
		reply["result"] = map[string]any{
			"protocolVersion": gatewayProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "identityharness-gateway", "version": "test"},
		}
	case req.Method == "tools/list":
		reply["result"] = map[string]any{"tools": []any{}}
	case req.Method == "tools/call" && missing:
		reply["error"] = rpcError{
			Code: URLElicitationRequired, Message: "connect " + serverOf(call.Tool) + " first",
			Data: map[string]any{"elicitations": []any{map[string]any{
				"mode": "url", "elicitationId": randomToken("elicit-"), "url": connectURL,
				"message": "Connect " + serverOf(call.Tool),
			}}},
		}
	case req.Method == "tools/call":
		reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "called " + call.Tool}}}
	default:
		reply["error"] = rpcError{Code: -32601, Message: "method not found"}
	}
	g.answer(w, call, http.StatusOK, reply)
}

// answer records call with status and writes status with body as JSON, or
// no body when body is nil.
func (g *Gateway) answer(w http.ResponseWriter, call GatewayCall, status int, body any) {
	call.Status = status
	g.mu.Lock()
	g.calls = append(g.calls, call)
	g.mu.Unlock()
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
