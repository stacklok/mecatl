package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestTokenSourceInjectsBearerAndPreservesHTTPClient(t *testing.T) {
	var gotAuthorization string
	base := &tokenRoundTripper{roundTrip: func(req *http.Request) (*http.Response, error) {
		gotAuthorization = req.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    req,
		}, nil
	}}
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	supplied := &http.Client{
		Transport:     base,
		CheckRedirect: redirect,
		Timeout:       7 * time.Second,
	}

	client := newMCPHTTPClient(ServerConfig{
		URL:         "https://mcp.example/mcp",
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "session-token"}),
		HTTPClient:  supplied,
	}, nil)

	resp, err := client.Get("https://mcp.example/mcp")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if gotAuthorization != "Bearer session-token" {
		t.Fatalf("Authorization = %q, want Bearer session-token", gotAuthorization)
	}
	if client.Timeout != supplied.Timeout || client.CheckRedirect == nil {
		t.Fatalf("supplied client behavior not preserved: timeout=%v redirect=%v", client.Timeout, client.CheckRedirect != nil)
	}
	if supplied.Transport != base {
		t.Fatal("newMCPHTTPClient mutated the supplied client transport")
	}
	transport, ok := client.Transport.(*bearerRoundTripper)
	if !ok {
		t.Fatalf("transport = %T, want *bearerRoundTripper", client.Transport)
	}
	if transport.base != base {
		t.Fatalf("token transport base = %T, want supplied transport", transport.base)
	}
}

func TestTokenSourceIsNotSentOnCrossOriginRedirect(t *testing.T) {
	var redirectedAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer session-token" {
			t.Errorf("origin Authorization = %q, want Bearer session-token", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()

	client := newMCPHTTPClient(ServerConfig{
		URL:         origin.URL,
		TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "session-token"}),
	}, nil)
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if redirectedAuthorization != "" {
		t.Fatalf("cross-origin Authorization = %q, want empty", redirectedAuthorization)
	}
}

func TestBearerTokenSourceRejectsInvalidTokens(t *testing.T) {
	tests := []struct {
		name  string
		token *oauth2.Token
	}{
		{name: "nil"},
		{name: "empty access token", token: &oauth2.Token{TokenType: "Bearer"}},
		{name: "non bearer", token: &oauth2.Token{AccessToken: "token", TokenType: "MAC"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := bearerTokenSource{source: oauth2.StaticTokenSource(tt.token)}
			if _, err := source.Token(); err == nil || !strings.Contains(err.Error(), "non-bearer token") {
				t.Fatalf("Token error = %v, want non-bearer token error", err)
			}
		})
	}
}

func TestTokenSourceConflicts(t *testing.T) {
	source := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token", TokenType: "Bearer"})
	tests := []struct {
		name string
		cfg  ServerConfig
		want string
	}{
		{
			name: "credential header",
			cfg: ServerConfig{
				TokenSource: source,
				Headers:     map[string]string{"proxy-authorization": "Basic secret"},
			},
			want: "static credential headers and token source are mutually exclusive",
		},
		{
			name: "direct OAuth",
			cfg:  ServerConfig{TokenSource: source, OAuth: &OAuthOptions{}},
			want: "token source and OAuth are mutually exclusive",
		},
		{
			name: "custom HTTP client with direct OAuth",
			cfg:  ServerConfig{HTTPClient: &http.Client{}, OAuth: &OAuthOptions{}},
			want: "custom HTTP client and OAuth are mutually exclusive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := prepareOAuthServerConfig(context.Background(), tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("prepareOAuthServerConfig error = %v, want %q", err, tt.want)
			}
		})
	}

	cfg := ServerConfig{
		URL:         "https://mcp.example/mcp",
		TokenSource: source,
		Headers:     map[string]string{"X-Trace-ID": "trace"},
	}
	if _, _, err := prepareOAuthServerConfig(context.Background(), cfg); err != nil {
		t.Fatalf("non-credential headers conflict with TokenSource: %v", err)
	}
}

func TestTokenSourceRequiresSecureResourceURL(t *testing.T) {
	source := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token", TokenType: "Bearer"})
	for _, tc := range []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "https", url: "https://mcp.example/mcp"},
		{name: "loopback http", url: "http://127.0.0.1:8080/mcp"},
		{name: "plaintext remote", url: "http://mcp.example/mcp", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := prepareOAuthServerConfig(context.Background(), ServerConfig{URL: tc.url, TokenSource: source})
			if tc.wantErr && err == nil {
				t.Fatal("prepareOAuthServerConfig error = nil, want rejection")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("prepareOAuthServerConfig error = %v, want nil", err)
			}
		})
	}
}

func TestBearerTokenSourcePropagatesError(t *testing.T) {
	want := errors.New("token unavailable")
	source := bearerTokenSource{source: tokenSourceFunc(func() (*oauth2.Token, error) {
		return nil, want
	})}
	if _, err := source.Token(); !errors.Is(err, want) {
		t.Fatalf("Token error = %v, want %v", err, want)
	}
}

type tokenRoundTripper struct {
	roundTrip func(*http.Request) (*http.Response, error)
}

func (t *tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.roundTrip(req)
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }
