package app

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// attrCapturingDiag captures the message AND every attribute of every diagnostic.
//
// It exists because the package's other capturingDiag (askadjudicator_test.go)
// takes `_ ...any` and DISCARDS attributes. That is fine for the build-once
// message assertions it was written for, and useless here: the credential leak
// this test guards lives in the `err` ATTRIBUTE, not the message, so a test using
// that helper would pass while the token flowed straight through. Capturing
// everything is the whole point — a redaction test must see what the sink sees.
type attrCapturingDiag struct {
	mu    sync.Mutex
	lines []string
}

func (d *attrCapturingDiag) Log(_ context.Context, level port.Level, msg string, attrs ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, fmt.Sprintf("%v %s %v", level, msg, attrs))
}

func (d *attrCapturingDiag) With(attrs ...any) port.Diagnostics {
	d.mu.Lock()
	d.lines = append(d.lines, fmt.Sprintf("with %v", attrs))
	d.mu.Unlock()
	return d
}

func (d *attrCapturingDiag) dump() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.lines, "\n")
}

// TestClientMCPUnreachableWARNRedactsQueryCredentials is the review finding on
// PR #903, end to end through the REAL session-engine factory.
//
// The factory redacted sc.URL but logged `err` unchanged, and net/http embeds the
// complete request URL — query string included — in a connection error, which the
// MCP SDK then formats into its own message text. So a token in the URL reached the
// operator's diagnostics through the error even though the `url` attribute beside
// it was clean. Redacting one of two channels is not redacting.
//
// Both WARN sites are covered: the per-server onError callback, and the
// every-server-failed aggregate.
//
// Offline: the endpoint is a started-then-closed loopback listener, so the dial is
// a genuine ECONNREFUSED and no external network is touched.
func TestClientMCPUnreachableWARNRedactsQueryCredentials(t *testing.T) {
	const secret = "review-query-secret"

	deadURL := func(t *testing.T) string {
		t.Helper()
		srv := httptest.NewServer(nil)
		url := srv.URL
		srv.Close()
		return url
	}

	factoryWithDiag := func(diag port.Diagnostics) server.SessionEngineFactory {
		llm := mockllm.New(mockllm.TextTurn("ok"))
		reg := regForTest(llm, providerOpenAI, "gpt-5")
		return sessionEngineFactory(Config{Model: "gpt-5", Diagnostics: diag}, reg, llm, memstore.New(),
			permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
			prompt.RootAssembler{}, catalogAssets{}, nil)
	}

	t.Run("every server unreachable: both WARN sites", func(t *testing.T) {
		diag := &attrCapturingDiag{}
		res, err := factoryWithDiag(diag)(context.Background(), server.ProviderSelector{},
			[]mcp.ServerConfig{{
				Name:        "notes",
				URL:         deadURL(t) + "/mcp?access_token=" + secret,
				Timeout:     mcp.ClientConnectTimeout,
				NoRedirects: true,
			}}, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory must still build a core-only engine: %v", err)
		}
		defer func() { _ = res.Close() }()

		got := diag.dump()

		// Positive control FIRST: if the WARNs never fired, the leak assertion below
		// would pass vacuously and this test would guard nothing.
		if !strings.Contains(got, "unreachable") {
			t.Fatalf("the per-server unreachable WARN never fired, so this test proves nothing:\n%s", got)
		}
		if !strings.Contains(got, "no servers connected") {
			t.Fatalf("the all-failed WARN never fired, so the second site is unguarded:\n%s", got)
		}

		if strings.Contains(got, secret) {
			t.Fatalf("a query-string credential reached the operator diagnostics:\n%s", got)
		}
		// No PREFIX of the token either — a clamped or partially-rendered URL must
		// not leave a usable fragment behind.
		for n := 6; n < len(secret); n++ {
			if strings.Contains(got, secret[:n]) {
				t.Fatalf("a %d-character prefix of the credential survived:\n%s", n, got)
			}
		}
		// Still diagnostic: the operator can tell WHICH server and WHERE it pointed.
		if !strings.Contains(got, "notes") {
			t.Fatalf("diagnostics no longer name the server:\n%s", got)
		}
		if !strings.Contains(got, "127.0.0.1") {
			t.Fatalf("diagnostics no longer carry the host, leaving nothing to debug:\n%s", got)
		}
	})

	t.Run("partial failure: the surviving server does not mask the leak", func(t *testing.T) {
		// One reachable, one not. The onError site fires for the failing server while
		// the aggregate WARN does not, so this reaches the first site alone.
		diag := &attrCapturingDiag{}
		res, err := factoryWithDiag(diag)(context.Background(), server.ProviderSelector{},
			[]mcp.ServerConfig{
				{Name: "live", URL: newMCPTestServer(t), Timeout: mcp.ClientConnectTimeout},
				{Name: "dead", URL: deadURL(t) + "/mcp?access_token=" + secret, Timeout: mcp.ClientConnectTimeout, NoRedirects: true},
			}, server.ProfileDefault, "", session.ModeDefault)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer func() { _ = res.Close() }()

		got := diag.dump()
		if !strings.Contains(got, "unreachable") {
			t.Fatalf("the per-server WARN never fired:\n%s", got)
		}
		if strings.Contains(got, secret) {
			t.Fatalf("a query-string credential reached the operator diagnostics on the partial path:\n%s", got)
		}
	})
}
