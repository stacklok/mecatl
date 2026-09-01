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
// It asserts the two things that matter separately: the raw error DOES carry the
// token (so the test is not vacuously passing against a shape that never leaked),
// and RedactError removes it.
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

	// The positive control. If this stops holding, the leak shape changed and the
	// assertion below has become vacuous.
	if !strings.Contains(err.Error(), queryTokenSecret) {
		t.Fatalf("the raw connect error no longer carries the query token, so this test proves nothing; error was:\n%v", err)
	}
	if got := RedactError(err); strings.Contains(got, queryTokenSecret) {
		t.Fatalf("RedactError left the token in a real connect error:\n%s", got)
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
