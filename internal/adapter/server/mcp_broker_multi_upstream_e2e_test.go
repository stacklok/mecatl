package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stacklok/mecatl/engine/tool"
)

// These protocol fixtures are shared by the remote SessionService regressions.
type multiUpstreamOIDC struct {
	name, clientID              string
	server                      *httptest.Server
	key                         *rsa.PrivateKey
	mu                          sync.Mutex
	nonce                       string
	deny, revokeRefresh         bool
	tokenTTL, refreshTokenTTL   int
	subject                     string
	initialTokens, refreshes    int
	accessToken, refreshedToken string
	authorizeEntered            chan struct{}
	authorizeRelease            <-chan struct{}
	enteredOnce                 sync.Once
}

func newMultiUpstreamOIDC(t *testing.T, name string) *multiUpstreamOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &multiUpstreamOIDC{name: name, clientID: name + "-client", key: key, accessToken: name + "-token", refreshedToken: name + "-refreshed"}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}
func (f *multiUpstreamOIDC) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeTestJSON(w, map[string]any{"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/jwks", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/authorize":
		f.mu.Lock()
		f.nonce = r.URL.Query().Get("nonce")
		deny := f.deny
		entered, release := f.authorizeEntered, f.authorizeRelease
		f.mu.Unlock()
		if entered != nil {
			f.enteredOnce.Do(func() { close(entered) })
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		query := url.Values{"state": {r.URL.Query().Get("state")}}
		if deny {
			query.Set("error", "access_denied")
		} else {
			query.Set("code", f.name+"-code")
		}
		http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?"+query.Encode(), http.StatusFound)
	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.mu.Lock()
			f.refreshes++
			revoked, ttl := f.revokeRefresh, f.refreshTokenTTL
			f.mu.Unlock()
			if revoked {
				w.WriteHeader(http.StatusBadRequest)
				writeTestJSON(w, map[string]any{"error": "invalid_grant"})
				return
			}
			if ttl == 0 {
				ttl = 3600
			}
			writeTestJSON(w, map[string]any{"access_token": f.refreshedToken, "refresh_token": f.name + "-refresh", "token_type": "Bearer", "expires_in": ttl})
			return
		}
		f.mu.Lock()
		f.initialTokens++
		nonce := f.nonce
		ttl, subject := f.tokenTTL, f.subject
		f.mu.Unlock()
		if ttl == 0 {
			ttl = 3600
		}
		if subject == "" {
			subject = "same-test-user"
		}
		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": f.server.URL, "sub": subject, "aud": f.clientID, "nonce": nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
		token.Header["kid"] = f.name + "-key"
		signed, err := token.SignedString(f.key)
		if err != nil {
			http.Error(w, "sign token", http.StatusInternalServerError)
			return
		}
		writeTestJSON(w, map[string]any{"access_token": f.accessToken, "refresh_token": f.name + "-refresh", "id_token": signed, "token_type": "Bearer", "expires_in": ttl})
	case "/jwks":
		writeTestJSON(w, map[string]any{"keys": []map[string]any{{"kty": "RSA", "use": "sig", "kid": f.name + "-key", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})}}})
	default:
		http.NotFound(w, r)
	}
}
func (f *multiUpstreamOIDC) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.initialTokens, f.refreshes
}

type multiUpstreamMCP struct {
	name            string
	server          *httptest.Server
	mu              sync.Mutex
	reject, revoked bool
	headers         []string
	toolCalls       int
	onCall          func()
}

func newMultiUpstreamMCP(t *testing.T, name string) *multiUpstreamMCP {
	t.Helper()
	f := &multiUpstreamMCP{name: name}
	upstream := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "v1"}, nil)
	mcpsdk.AddTool(upstream, &mcpsdk.Tool{Name: "whoami", Description: "report backend"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
		f.mu.Lock()
		f.toolCalls++
		onCall := f.onCall
		f.mu.Unlock()
		if onCall != nil {
			onCall()
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: name}}}, nil, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return upstream }, &mcpsdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.headers = append(f.headers, r.Header.Get("Authorization"))
		reject, revoked := f.reject, f.revoked
		f.mu.Unlock()
		if revoked {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "fixture grant revoked", http.StatusUnauthorized)
			return
		}
		if reject {
			http.Error(w, "deterministic authenticated discovery failure", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *multiUpstreamMCP) snapshot() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.headers...), f.toolCalls
}
func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func toolFromFixture(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Spec().Name == name {
			return candidate
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}
func fixtureToolNames(tools []tool.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, candidate := range tools {
		names = append(names, candidate.Spec().Name)
	}
	return names
}
func assertOnlyBearer(t *testing.T, headers []string, allowed ...string) {
	t.Helper()
	for _, header := range headers {
		if !containsString(allowed, header) {
			t.Fatal("unexpected upstream authorization")
		}
	}
}
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
