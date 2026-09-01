package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

// queryTokenSecret is a distinctive literal so a leak anywhere is unmistakable.
const queryTokenSecret = "review-query-secret"

// TestRedactErrorStripsEmbeddedURLs is the unit half of the review finding: a
// transport error carries the FULL request URL, independently of the ServerConfig
// URL logged beside it.
//
// The fixture strings are the real shapes, not invented ones — the first is
// verbatim what mcp.Connect produced against a closed loopback listener.
func TestRedactErrorStripsEmbeddedURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		// wantHosts must all survive: redaction has to stay DIAGNOSTIC, or
		// operators lose the ability to debug and will route around it.
		wantHosts []string
	}{
		{
			name:      "the real SDK-wrapped connect failure",
			in:        `mcp: connect to server "notes": calling "initialize": sending "initialize": rejected by transport: Post "http://127.0.0.1:59327/mcp?access_token=` + queryTokenSecret + `": dial tcp 127.0.0.1:59327: connect: connection refused`,
			wantHosts: []string{"127.0.0.1:59327"},
		},
		{
			name:      "bare url.Error form",
			in:        `Get "https://mcp.example/mcp?token=` + queryTokenSecret + `": context deadline exceeded`,
			wantHosts: []string{"mcp.example"},
		},
		{
			name:      "userinfo form",
			in:        `Post "https://user:` + queryTokenSecret + `@mcp.example/mcp": EOF`,
			wantHosts: []string{"mcp.example"},
		},
		{
			name:      "two urls in one message",
			in:        `redirect from https://a.example/mcp?k=` + queryTokenSecret + ` to https://b.example/x?j=` + queryTokenSecret,
			wantHosts: []string{"a.example", "b.example"},
		},
		{
			name:      "fragment form",
			in:        `dial https://mcp.example/mcp#` + queryTokenSecret + ` failed`,
			wantHosts: []string{"mcp.example"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactText(tc.in)
			if strings.Contains(got, queryTokenSecret) {
				t.Fatalf("RedactText left the credential in:\n%s", got)
			}
			for _, host := range tc.wantHosts {
				if !strings.Contains(got, host) {
					t.Fatalf("RedactText dropped host %q, leaving less to diagnose with:\n%s", host, got)
				}
			}
		})
	}

	t.Run("idempotent", func(t *testing.T) {
		in := `Get "https://mcp.example/mcp?token=` + queryTokenSecret + `": EOF`
		once := RedactText(in)
		if twice := RedactText(once); twice != once {
			t.Fatalf("RedactText is not idempotent:\n once: %s\ntwice: %s", once, twice)
		}
	})

	t.Run("nil and empty", func(t *testing.T) {
		if got := RedactError(nil); got != "" {
			t.Fatalf("RedactError(nil) = %q, want empty", got)
		}
		if got := RedactText(""); got != "" {
			t.Fatalf("RedactText(\"\") = %q, want empty", got)
		}
	})

	t.Run("a message with no url is untouched", func(t *testing.T) {
		in := "mcp: server \"notes\" requires a URL"
		if got := RedactText(in); got != in {
			t.Fatalf("RedactText altered a url-free message:\n in: %s\nout: %s", in, got)
		}
	})
}

// TestConnectErrorIsRedactedThroughTheRealClient is the end-to-end half, using the
// REAL Connect path against a genuinely unreachable endpoint (a started-then-closed
// loopback listener, so the dial is a real ECONNREFUSED and no external network is
// touched).
//
// Connect redacts at the SOURCE, so the error it returns is already safe — which is
// what makes every downstream consumer safe by default: NewManager's callback, its
// returned error, and the direct callers that pass this error onward. The test
// proves three things together, because the value of the wrapper is all three at
// once: the returned error is clean, the ORIGINAL is still reachable through
// Unwrap (so the leak shape genuinely existed and this is not a vacuous assertion),
// and errors.Is still sees through the wrapper.
func TestConnectErrorIsRedactedThroughTheRealClient(t *testing.T) {
	srv := httptest.NewServer(nil)
	base := srv.URL
	srv.Close()

	_, err := Connect(context.Background(), ServerConfig{
		Name:        "notes",
		URL:         base + "/mcp?access_token=" + queryTokenSecret,
		Timeout:     ClientConnectTimeout,
		NoRedirects: true,
	}, port.NopDiagnostics{})
	if err == nil {
		t.Fatal("expected a connect failure against a closed listener")
	}

	if strings.Contains(err.Error(), queryTokenSecret) {
		t.Fatalf("Connect returned an error carrying the query token:\n%v", err)
	}

	// The positive control, one layer down: the wrapped original must still carry
	// the token. If it does not, the transport stopped embedding the URL and the
	// assertion above has become vacuous — a change worth knowing about.
	inner := errors.Unwrap(err)
	if inner == nil {
		t.Fatalf("Connect's error has no wrapped original, so this test cannot tell redaction from a transport that never leaked:\n%v", err)
	}
	if !strings.Contains(inner.Error(), queryTokenSecret) {
		t.Fatalf("the unwrapped original no longer carries the token; the leak shape changed and this test proves nothing:\n%v", inner)
	}

	// Still diagnostic.
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("the redacted error dropped the host, leaving nothing to debug:\n%v", err)
	}
}

// TestRedactErrorValuePreservesSentinels pins the property that makes source-level
// redaction safe to adopt: callers branch on sentinels, and wrapping must not break
// that. internal/app's MCP login flow and the operator MCP callback both do
// errors.Is against ErrOAuthLoginRequired / ErrOAuthUnavailable.
func TestRedactErrorValuePreservesSentinels(t *testing.T) {
	leaky := fmt.Errorf(`connect https://mcp.example/mcp?access_token=%s: %w`, queryTokenSecret, ErrOAuthLoginRequired)
	wrapped := RedactErrorValue(leaky)

	if strings.Contains(wrapped.Error(), queryTokenSecret) {
		t.Fatalf("RedactErrorValue left the token in:\n%v", wrapped)
	}
	if !errors.Is(wrapped, ErrOAuthLoginRequired) {
		t.Fatal("RedactErrorValue broke errors.Is: callers that branch on OAuth sentinels would silently take the wrong path")
	}

	// An error with nothing to redact is returned AS-IS, so the common path adds no
	// wrapper at all.
	plain := errors.New("mcp: server \"notes\" requires a URL")
	if got := RedactErrorValue(plain); got != plain {
		t.Fatalf("RedactErrorValue wrapped an error with no URL in it: %T", got)
	}
	if RedactErrorValue(nil) != nil {
		t.Fatal("RedactErrorValue(nil) must be nil")
	}
}

// TestNewManagerCallbackReceivesSafeValues pins the SECOND credential channel: the
// ServerConfig handed back to onError.
//
// Connect's error redaction cannot reach it, because the config is the caller's own
// value travelling back. A callback logging sc.URL leaks a query token even when
// the error beside it is clean, and one logging sc.Headers leaks a bearer outright —
// which is exactly what the inline agent-MCP callback did. So NewManager replaces it
// with a safe view.
func TestNewManagerCallbackReceivesSafeValues(t *testing.T) {
	srv := httptest.NewServer(nil)
	base := srv.URL
	srv.Close()

	const headerSecret = "Bearer zzz-callback-header-secret"
	var gotCfg ServerConfig
	var gotErr error
	var called int

	_, err := NewManager(context.Background(), []ServerConfig{{
		Name:        "notes",
		URL:         base + "/mcp?access_token=" + queryTokenSecret,
		Headers:     map[string]string{"Authorization": headerSecret},
		Timeout:     ClientConnectTimeout,
		NoRedirects: true,
	}}, func(cfg ServerConfig, cerr error) {
		called++
		gotCfg, gotErr = cfg, cerr
	}, port.NopDiagnostics{})
	if err == nil {
		t.Fatal("expected NewManager to report that every server failed")
	}
	if called != 1 {
		t.Fatalf("onError called %d times, want 1", called)
	}

	if strings.Contains(gotCfg.URL, queryTokenSecret) {
		t.Fatalf("the callback config carries the query token: %q", gotCfg.URL)
	}
	if gotCfg.Headers != nil {
		t.Fatalf("the callback config carries headers, which are secret-shaped: %v", gotCfg.Headers)
	}
	if gotErr != nil && strings.Contains(gotErr.Error(), queryTokenSecret) {
		t.Fatalf("the callback error carries the query token:\n%v", gotErr)
	}
	// The name survives — it is what a callback actually needs.
	if gotCfg.Name != "notes" {
		t.Fatalf("callback config lost the server name: %+v", gotCfg)
	}
	// ...and so does enough of the URL to diagnose with.
	if !strings.Contains(gotCfg.URL, "127.0.0.1") {
		t.Fatalf("callback config dropped the host: %q", gotCfg.URL)
	}

	// The RETURNED error is clean too (that is the second reported log line).
	if strings.Contains(err.Error(), queryTokenSecret) {
		t.Fatalf("NewManager's returned error carries the query token:\n%v", err)
	}
	if strings.Contains(err.Error(), headerSecret) {
		t.Fatalf("NewManager's returned error carries the header secret:\n%v", err)
	}
}

// dumpDiag flattens the package's existing recordingDiag (reconnect_test.go) into
// one searchable string, so a leak assertion covers messages, keys, and values
// alike rather than only the field a test thought to check.
func dumpDiag(d *recordingDiag) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var b strings.Builder
	for _, r := range d.records {
		fmt.Fprintf(&b, "%v %s %v\n", r.level, r.msg, r.args)
	}
	return b.String()
}

// TestPackageDiagnosticsAreRedactedAtTheSink pins the STRUCTURAL half of the fix.
//
// Four sites inside this package log a transport error (resource listing, prompt
// listing, list refresh, reconnect), and every one can carry a credential-bearing
// URL. Rather than dress each call, Connect and NewManager wrap the sink — so a log
// site added later is safe by default rather than leaking by default. This test
// drives the wrapper directly, because the four sites need conditions
// (a server that connects then fails listing, a dropped session) that an offline
// unit test cannot all reach.
func TestPackageDiagnosticsAreRedactedAtTheSink(t *testing.T) {
	rec := &recordingDiag{}
	wrapped := redactDiagnostics(rec)

	leaky := errors.New(`Post "https://mcp.example/mcp?access_token=` + queryTokenSecret + `": EOF`)
	wrapped.Log(context.Background(), port.LevelWarn, "mcp server reconnect failed", "server", "notes", "err", leaky)
	wrapped.Log(context.Background(), port.LevelWarn,
		`listing failed for https://mcp.example/mcp?k=`+queryTokenSecret, "server", "notes")
	wrapped.With("url", "https://mcp.example/mcp?k="+queryTokenSecret).
		Log(context.Background(), port.LevelWarn, "after With", "n", 3)

	if strings.Contains(dumpDiag(rec), queryTokenSecret) {
		t.Fatalf("the sink wrapper let a credential through:\n%s", dumpDiag(rec))
	}
	// Still useful: the host and the message survive.
	if !strings.Contains(dumpDiag(rec), "mcp.example") {
		t.Fatalf("the wrapper scrubbed too much; nothing left to diagnose:\n%s", dumpDiag(rec))
	}
	if !strings.Contains(dumpDiag(rec), "reconnect failed") {
		t.Fatalf("the wrapper dropped the message:\n%s", dumpDiag(rec))
	}

	// Non-string, non-error attributes pass through untouched.
	rec2 := &recordingDiag{}
	redactDiagnostics(rec2).Log(context.Background(), port.LevelInfo, "counts", "n", 7, "ok", true)
	if !strings.Contains(dumpDiag(rec2), "7") || !strings.Contains(dumpDiag(rec2), "true") {
		t.Fatalf("typed attributes were mangled:\n%s", dumpDiag(rec2))
	}

	// And wrapping is not doubled on the NewManager -> Connect path.
	if inner, ok := wrapped.(redactingDiagnostics); !ok {
		t.Fatal("redactDiagnostics did not wrap")
	} else if _, doubled := inner.inner.(redactingDiagnostics); doubled {
		t.Fatal("redactDiagnostics double-wrapped")
	}
	if again := redactDiagnostics(wrapped); again != wrapped {
		t.Fatal("redactDiagnostics re-wrapped an already-wrapped sink")
	}
}

// TestRedactingSinkIsWiredAtTheEntryPoints pins that the decoration is APPLIED,
// not merely that it works.
//
// TestPackageDiagnosticsAreRedactedAtTheSink drives redactDiagnostics directly, so
// it keeps passing if someone deletes the wrap from Connect/NewManager — which is
// the mutation that reopens all four in-package leak sites. Reaching those sites
// through real behaviour needs conditions an offline test cannot stage (a server
// that connects then fails listing, a dropped session mid-reconnect), so this
// asserts the wiring white-box instead: the *Server that Connect builds must hold
// a redacting sink, since every in-package log line goes through it.
func TestRedactingSinkIsWiredAtTheEntryPoints(t *testing.T) {
	// The package's existing fake MCP server (mcp_test.go); gotAuth is unused here.
	var auth string
	url := newTestServer(t, &auth)

	t.Run("Connect wraps the sink it stores on the Server", func(t *testing.T) {
		srv, err := Connect(context.Background(), ServerConfig{
			Name: "notes", URL: url, Timeout: ClientConnectTimeout,
		}, &recordingDiag{})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() { _ = srv.Close() }()

		if _, ok := srv.diag.(redactingDiagnostics); !ok {
			t.Fatalf("Server.diag is %T, not a redacting sink: every in-package log site (resource listing, prompt listing, list refresh, reconnect) can carry a credential-bearing URL and now leaks", srv.diag)
		}
	})

	t.Run("NewManager wraps too", func(t *testing.T) {
		mgr, err := NewManager(context.Background(), []ServerConfig{{
			Name: "notes", URL: url, Timeout: ClientConnectTimeout,
		}}, nil, &recordingDiag{})
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		defer func() { _ = mgr.Close() }()

		servers := mgr.Servers()
		if len(servers) != 1 {
			t.Fatalf("got %d connected servers, want 1", len(servers))
		}
		if _, ok := servers[0].diag.(redactingDiagnostics); !ok {
			t.Fatalf("Server.diag is %T, not a redacting sink", servers[0].diag)
		}
	})

	t.Run("a nil sink is still safe", func(t *testing.T) {
		if got := redactDiagnostics(nil); got == nil {
			t.Fatal("redactDiagnostics(nil) returned nil; the package would panic on its first log")
		}
	})
}

// TestClampErrRedactsBeforeClamping pins the ORDER inside clampErr.
//
// Clamping first can cut the middle of a query string and leave a PARTIAL
// credential in the retained prefix — which the sink wrapper then cannot recognise
// as a URL to scrub, because the truncated remnant no longer parses as one. The
// fixture puts the URL late enough in the message that a 200-byte clamp would land
// inside the token.
func TestClampErrRedactsBeforeClamping(t *testing.T) {
	// The fixture must make the 200-byte clamp land INSIDE the token, not before
	// it: a token entirely past the cut is removed by truncation alone, and the
	// test would then pass with the redaction removed. Size the padding so the
	// token starts 10 bytes before the clamp, leaving "review-que" behind.
	const clampLen = 200
	head := ` Post "https://mcp.example/mcp?access_token=`
	pad := strings.Repeat("x", clampLen-10-len(head))
	err := fmt.Errorf("%s%s%s\": EOF", pad, head, queryTokenSecret)

	// Prove the fixture actually straddles the clamp, or this test is vacuous.
	rawUnredacted := err.Error()
	if len(rawUnredacted) <= clampLen {
		t.Fatalf("fixture is too short to exercise the clamp (%d bytes)", len(rawUnredacted))
	}
	if !strings.Contains(rawUnredacted[:clampLen], queryTokenSecret[:6]) {
		t.Fatalf("fixture does not straddle the clamp: a truncation-only implementation would pass.\nfirst %d bytes: %s", clampLen, rawUnredacted[:clampLen])
	}
	if strings.Contains(rawUnredacted[:clampLen], queryTokenSecret) {
		t.Fatal("fixture puts the WHOLE token before the clamp; widen the padding so the cut lands mid-token")
	}

	got := clampErr(err)
	if strings.Contains(got, queryTokenSecret) {
		t.Fatalf("clampErr leaked the whole token:\n%s", got)
	}
	// The sharper assertion: no PREFIX of the token survives either.
	for n := 6; n < len(queryTokenSecret); n++ {
		if strings.Contains(got, queryTokenSecret[:n]) {
			t.Fatalf("clampErr left a %d-char prefix of the token (clamped mid-query):\n%s", n, got)
		}
	}
}
