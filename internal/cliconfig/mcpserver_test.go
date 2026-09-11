package cliconfig

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// newMCPFlagSet builds a quiet ContinueOnError FlagSet with --mcp-server (and
// its --mcp-server-insecure-http companion) registered via
// RegisterMCPServerFlag, mirroring how the three mains use it.
func newMCPFlagSet(t *testing.T) (*flag.FlagSet, *MCPServerList) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, RegisterMCPServerFlag(fs, "")
}

// mustFinalize runs the post-parse finalize step (the deferred token-bearing
// scheme gate) and fails the test on error — the happy-path helper mirroring
// the call every main makes right after flag.Parse.
func mustFinalize(t *testing.T, list *MCPServerList) {
	t.Helper()
	if err := list.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

// TestMCPServerListParsesNameURL covers the happy path: one name=URL entry
// yields one ServerConfig with the name and URL split at the FIRST '=' (a URL
// may itself contain '='), and — with no MCP_<NAME>_TOKEN in the environment —
// NO Headers at all (token optional; the absence must not mint an empty
// Authorization header).
func TestMCPServerListParsesNameURL(t *testing.T) {
	fs, list := newMCPFlagSet(t)
	if err := fs.Parse([]string{"--mcp-server", "tequitl=http://127.0.0.1:9100/mcp?tenant=a=b"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFinalize(t, list)
	got := list.Servers()
	if len(got) != 1 {
		t.Fatalf("Servers() len = %d, want 1", len(got))
	}
	if got[0].Name != "tequitl" {
		t.Errorf("Name = %q, want tequitl", got[0].Name)
	}
	if got[0].URL != "http://127.0.0.1:9100/mcp?tenant=a=b" {
		t.Errorf("URL = %q (must split at the FIRST '=')", got[0].URL)
	}
	if got[0].Headers != nil {
		t.Errorf("Headers = %v, want nil when MCP_TEQUITL_TOKEN is unset", got[0].Headers)
	}
}

// TestMCPServerListRepeatable proves --mcp-server is repeatable and preserves
// the order of occurrence (the connect order the operator wrote).
func TestMCPServerListRepeatable(t *testing.T) {
	fs, list := newMCPFlagSet(t)
	err := fs.Parse([]string{
		"--mcp-server", "vmcp=https://vmcp.internal/mcp",
		"--mcp-server", "tequitl=https://tequitl.internal/mcp",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFinalize(t, list)
	got := list.Servers()
	if len(got) != 2 {
		t.Fatalf("Servers() len = %d, want 2", len(got))
	}
	if got[0].Name != "vmcp" || got[1].Name != "tequitl" {
		t.Errorf("order not preserved: got %q then %q", got[0].Name, got[1].Name)
	}
}

// TestMCPServerListRejectsBadShapes covers the malformed inputs: no '=', an
// empty name, an empty URL, and a bare '='. Each must fail parse with the
// mecated-shaped "want name=URL" message.
func TestMCPServerListRejectsBadShapes(t *testing.T) {
	for _, bad := range []string{"", "noequals", "=http://x", "name=", "="} {
		t.Run("bad "+bad, func(t *testing.T) {
			fs, _ := newMCPFlagSet(t)
			err := fs.Parse([]string{"--mcp-server", bad})
			if err == nil {
				t.Fatalf("parse(%q): want error, got nil", bad)
			}
			if !strings.Contains(err.Error(), "want name=URL") {
				t.Errorf("parse(%q) error = %q, want the name=URL guidance", bad, err)
			}
		})
	}
}

// TestMCPServerListTokenFromEnv proves the MCP_<NAME>_TOKEN convention: the
// name is upper-cased into the env var, and a present token becomes an
// "Authorization: Bearer <token>" header on that server ONLY.
func TestMCPServerListTokenFromEnv(t *testing.T) {
	t.Setenv("MCP_TEQUITL_TOKEN", "s3cr3t")
	fs, list := newMCPFlagSet(t)
	err := fs.Parse([]string{
		"--mcp-server", "tequitl=https://tequitl.internal/mcp",
		"--mcp-server", "vmcp=https://vmcp.internal/mcp",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFinalize(t, list)
	got := list.Servers()
	if len(got) != 2 {
		t.Fatalf("Servers() len = %d, want 2", len(got))
	}
	if auth := got[0].Headers["Authorization"]; auth != "Bearer s3cr3t" {
		t.Errorf("tequitl Authorization = %q, want %q", auth, "Bearer s3cr3t")
	}
	if got[1].Headers != nil {
		t.Errorf("vmcp Headers = %v, want nil (no MCP_VMCP_TOKEN set)", got[1].Headers)
	}
}

// TestMCPServerListRejectsInvalidNames pins the name charset (CWE-178 hardening,
// panel finding): the name derives the MCP_<NAME>_TOKEN env var, so it must
// match ^[A-Za-z0-9_]+$. A hyphenated name like my-svc would derive the
// UNSETTABLE env var MCP_MY-SVC_TOKEN and silently connect unauthenticated;
// a Unicode letter (ı) case-folds surprisingly. Both are rejected at parse.
func TestMCPServerListRejectsInvalidNames(t *testing.T) {
	for _, bad := range []string{
		"my-svc=https://x.internal/mcp",
		"my svc=https://x.internal/mcp",
		"svc.1=https://x.internal/mcp",
		"ıvmcp=https://x.internal/mcp",
		"a/b=https://x.internal/mcp",
	} {
		t.Run(bad, func(t *testing.T) {
			fs, _ := newMCPFlagSet(t)
			err := fs.Parse([]string{"--mcp-server", bad})
			if err == nil {
				t.Fatalf("parse(%q): want error, got nil", bad)
			}
			if !strings.Contains(err.Error(), "A-Za-z0-9_") {
				t.Errorf("parse(%q) error = %q, want the name-charset guidance", bad, err)
			}
		})
	}

	// Underscores and digits stay legal (they map cleanly into an env name).
	fs, list := newMCPFlagSet(t)
	if err := fs.Parse([]string{"--mcp-server", "task_graph2=https://x.internal/mcp"}); err != nil {
		t.Fatalf("parse(task_graph2=...): %v", err)
	}
	mustFinalize(t, list)
	if got := list.Servers(); len(got) != 1 || got[0].Name != "task_graph2" {
		t.Errorf("Servers() = %v, want the task_graph2 entry", got)
	}
}

// TestMCPServerListRejectsEnvNameCollisions pins the case-insensitive
// uniqueness rule (CWE-178): vmcp, VMCP, and vMcp all derive MCP_VMCP_TOKEN,
// so a second entry whose upper-cased name collides with an earlier one is
// rejected rather than silently sharing (or stealing) the first one's token.
func TestMCPServerListRejectsEnvNameCollisions(t *testing.T) {
	for _, second := range []string{"vmcp", "VMCP", "vMcp"} {
		t.Run("vmcp then "+second, func(t *testing.T) {
			fs, _ := newMCPFlagSet(t)
			err := fs.Parse([]string{
				"--mcp-server", "vmcp=https://a.internal/mcp",
				"--mcp-server", second + "=https://b.internal/mcp",
			})
			if err == nil {
				t.Fatalf("second entry %q: want a collision error, got nil", second)
			}
			if !strings.Contains(err.Error(), "MCP_VMCP_TOKEN") {
				t.Errorf("collision error = %q, want it to name MCP_VMCP_TOKEN", err)
			}
		})
	}

	// Distinct env names stay legal.
	fs, list := newMCPFlagSet(t)
	err := fs.Parse([]string{
		"--mcp-server", "vmcp=https://a.internal/mcp",
		"--mcp-server", "tequitl=https://b.internal/mcp",
	})
	if err != nil {
		t.Fatalf("distinct names must parse: %v", err)
	}
	mustFinalize(t, list)
	if len(list.Servers()) != 2 {
		t.Errorf("Servers() len = %d, want 2", len(list.Servers()))
	}
}

// TestMCPServerListBearerRequiresHTTPS pins the cleartext-token guard
// (CWE-319): when a MCP_<NAME>_TOKEN is present (a bearer WILL be attached),
// the URL must be https — or http to an explicit loopback host. Without a
// token the URL is not gated (unchanged behavior; the operator may point a
// tokenless dev server anywhere).
//
// Since issue #358 the gate fires in the post-parse Finalize step (so the
// --mcp-server-insecure-http opt-in is order-independent), NOT inside Set —
// the DEFAULT POSTURE is unchanged: a token-bearing http non-loopback URL
// without the relaxation still fails, just at Finalize instead of Parse.
// Every main calls Finalize inside parseFlags, so the operator-visible
// behavior (parseFlags errors) is identical.
func TestMCPServerListBearerRequiresHTTPS(t *testing.T) {
	t.Run("token + http non-loopback is rejected at Finalize", func(t *testing.T) {
		t.Setenv("MCP_VMCP_TOKEN", "s3cr3t")
		fs, list := newMCPFlagSet(t)
		if err := fs.Parse([]string{"--mcp-server", "vmcp=http://vmcp.internal/mcp"}); err != nil {
			t.Fatalf("parse must succeed (the gate is deferred to Finalize): %v", err)
		}
		err := list.Finalize()
		if err == nil {
			t.Fatal("bearer over plaintext http off-host: want Finalize error, got nil")
		}
		if !strings.Contains(err.Error(), "https") {
			t.Errorf("error = %q, want it to demand https", err)
		}
		if !strings.Contains(err.Error(), "--mcp-server-insecure-http") {
			t.Errorf("error = %q, want it to name the explicit --mcp-server-insecure-http opt-in", err)
		}
	})

	t.Run("token + http loopback is allowed", func(t *testing.T) {
		t.Setenv("MCP_VMCP_TOKEN", "s3cr3t")
		for _, u := range []string{"http://127.0.0.1:9100/mcp", "http://localhost:9100/mcp", "http://[::1]:9100/mcp"} {
			fs, list := newMCPFlagSet(t)
			if err := fs.Parse([]string{"--mcp-server", "vmcp=" + u}); err != nil {
				t.Errorf("loopback %q must be allowed: %v", u, err)
				continue
			}
			mustFinalize(t, list)
			if got := list.Servers(); len(got) != 1 || got[0].Headers["Authorization"] != "Bearer s3cr3t" {
				t.Errorf("loopback %q: servers = %v, want the bearer attached", u, got)
			}
		}
	})

	t.Run("token + https is allowed", func(t *testing.T) {
		t.Setenv("MCP_VMCP_TOKEN", "s3cr3t")
		fs, list := newMCPFlagSet(t)
		if err := fs.Parse([]string{"--mcp-server", "vmcp=https://vmcp.internal/mcp"}); err != nil {
			t.Fatalf("https with token must parse: %v", err)
		}
		mustFinalize(t, list)
		if got := list.Servers(); len(got) != 1 || got[0].Headers["Authorization"] != "Bearer s3cr3t" {
			t.Errorf("servers = %v, want the bearer attached", got)
		}
	})

	t.Run("no token leaves http non-loopback allowed (unchanged)", func(t *testing.T) {
		fs, list := newMCPFlagSet(t)
		if err := fs.Parse([]string{"--mcp-server", "vmcp=http://vmcp.internal/mcp"}); err != nil {
			t.Fatalf("tokenless http must stay allowed: %v", err)
		}
		mustFinalize(t, list)
		if got := list.Servers(); len(got) != 1 || got[0].Headers != nil {
			t.Errorf("servers = %v, want one tokenless entry", got)
		}
	})
}

// TestMCPServerInsecureHTTPOrderIndependent is the issue-#358 contract
// addition (from titlani#40's devils-advocate pass): the scheme gate must NOT
// fire inside Set (argv order), so `--mcp-server-insecure-http tequitl`
// relaxes `--mcp-server tequitl=http://…` REGARDLESS of which flag comes
// first on argv. Both orders are pinned; both must yield the SAME parsed
// servers with the bearer attached.
func TestMCPServerInsecureHTTPOrderIndependent(t *testing.T) {
	orders := map[string][]string{
		"relaxation BEFORE the server": {
			"--mcp-server-insecure-http", "tequitl",
			"--mcp-server", "tequitl=http://tequitl.internal/mcp",
		},
		"relaxation AFTER the server": {
			"--mcp-server", "tequitl=http://tequitl.internal/mcp",
			"--mcp-server-insecure-http", "tequitl",
		},
	}
	for name, argv := range orders {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MCP_TEQUITL_TOKEN", "s3cr3t")
			fs, list := newMCPFlagSet(t)
			if err := fs.Parse(argv); err != nil {
				t.Fatalf("parse: %v", err)
			}
			mustFinalize(t, list)
			got := list.Servers()
			if len(got) != 1 {
				t.Fatalf("Servers() len = %d, want 1", len(got))
			}
			if got[0].URL != "http://tequitl.internal/mcp" {
				t.Errorf("URL = %q, want the plain-http URL preserved", got[0].URL)
			}
			if auth := got[0].Headers["Authorization"]; auth != "Bearer s3cr3t" {
				t.Errorf("Authorization = %q, want the bearer attached under the relaxation", auth)
			}
		})
	}
}

// TestMCPServerInsecureHTTPScopeIsPerName plants the per-name scope invariant:
// a relaxation for server A must NOT relax server B. Two token-bearing plain
// http servers, one relaxation — Finalize must still reject, and the error
// must name the UNrelaxed server (vmcp), not the relaxed one.
func TestMCPServerInsecureHTTPScopeIsPerName(t *testing.T) {
	t.Setenv("MCP_TEQUITL_TOKEN", "tok-a")
	t.Setenv("MCP_VMCP_TOKEN", "tok-b")
	fs, list := newMCPFlagSet(t)
	err := fs.Parse([]string{
		"--mcp-server", "tequitl=http://tequitl.internal/mcp",
		"--mcp-server", "vmcp=http://vmcp.internal/mcp",
		"--mcp-server-insecure-http", "tequitl",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = list.Finalize()
	if err == nil {
		t.Fatal("vmcp (token + http off-host, NOT relaxed) must still fail Finalize")
	}
	if !strings.Contains(err.Error(), "vmcp") {
		t.Errorf("error = %q, want it to name the unrelaxed server vmcp", err)
	}
	if strings.Contains(err.Error(), "tequitl=") {
		t.Errorf("error = %q, must not blame the relaxed server tequitl", err)
	}
}

// TestMCPServerInsecureHTTPUnknownNameRejected: a relaxation naming a server
// with no matching --mcp-server registration is a validation error — in
// EITHER argv order (the unknown-name check is also part of the deferred
// finalize, so it cannot depend on ordering).
func TestMCPServerInsecureHTTPUnknownNameRejected(t *testing.T) {
	for name, argv := range map[string][]string{
		"no servers at all": {"--mcp-server-insecure-http", "ghost"},
		"other servers only": {
			"--mcp-server", "vmcp=https://vmcp.internal/mcp",
			"--mcp-server-insecure-http", "ghost",
		},
	} {
		t.Run(name, func(t *testing.T) {
			fs, list := newMCPFlagSet(t)
			if err := fs.Parse(argv); err != nil {
				t.Fatalf("parse: %v", err)
			}
			err := list.Finalize()
			if err == nil {
				t.Fatal("relaxation for an unregistered name: want Finalize error, got nil")
			}
			if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "--mcp-server") {
				t.Errorf("error = %q, want it to name ghost and point at --mcp-server", err)
			}
		})
	}
}

// TestMCPServerInsecureHTTPStaleAcknowledgmentIsLoud: a relaxation naming a
// server whose URL is NOT plain http to a non-loopback host — https, http to
// loopback, or a non-http scheme — is an ERROR, never silently inert. A stale
// acknowledgment (the endpoint moved to https, the operator forgot to drop
// the flag) must be loud so the flag list keeps matching reality.
func TestMCPServerInsecureHTTPStaleAcknowledgmentIsLoud(t *testing.T) {
	for name, url := range map[string]string{
		"https server":    "https://tequitl.internal/mcp",
		"loopback http":   "http://127.0.0.1:9100/mcp",
		"localhost http":  "http://localhost:9100/mcp",
		"non-http scheme": "ftp://tequitl.internal/mcp",
	} {
		t.Run(name, func(t *testing.T) {
			fs, list := newMCPFlagSet(t)
			err := fs.Parse([]string{
				"--mcp-server", "tequitl=" + url,
				"--mcp-server-insecure-http", "tequitl",
			})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = list.Finalize()
			if err == nil {
				t.Fatalf("relaxation over %s: want a stale-acknowledgment error, got nil", url)
			}
			if !strings.Contains(err.Error(), "tequitl") {
				t.Errorf("error = %q, want it to name the server", err)
			}
		})
	}
}

// TestMCPServerInsecureHTTPHTTPSValidationUnchanged: the relaxation covers
// ONLY the http scheme for the named server — everything else about the
// server list validates unchanged. A token-bearing entry with a non-http(s)
// scheme still fails Finalize even when named by a relaxation (covered by the
// stale test above), and a DIFFERENT server's https token path is untouched.
func TestMCPServerInsecureHTTPHTTPSValidationUnchanged(t *testing.T) {
	t.Setenv("MCP_TEQUITL_TOKEN", "tok-a")
	t.Setenv("MCP_VMCP_TOKEN", "tok-b")
	fs, list := newMCPFlagSet(t)
	err := fs.Parse([]string{
		"--mcp-server", "tequitl=http://tequitl.internal/mcp",
		"--mcp-server", "vmcp=https://vmcp.internal/mcp",
		"--mcp-server-insecure-http", "tequitl",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFinalize(t, list)
	got := list.Servers()
	if len(got) != 2 {
		t.Fatalf("Servers() len = %d, want 2", len(got))
	}
	if auth := got[1].Headers["Authorization"]; auth != "Bearer tok-b" {
		t.Errorf("vmcp (https) Authorization = %q — the relaxation must not disturb the https path", auth)
	}
	if auth := got[0].Headers["Authorization"]; auth != "Bearer tok-a" {
		t.Errorf("tequitl (relaxed http) Authorization = %q, want the bearer attached", auth)
	}
}

// TestMCPServerInsecureHTTPNameValidation: the relaxation flag runs the SAME
// name validation as --mcp-server itself (the #347 charset — the name matches
// a registered server, so a shape a server may never have is rejected at
// parse), and a case-insensitive duplicate relaxation is rejected loudly.
func TestMCPServerInsecureHTTPNameValidation(t *testing.T) {
	t.Run("charset enforced", func(t *testing.T) {
		for _, bad := range []string{"my-svc", "my svc", "svc.1", "ıvmcp", "a/b", ""} {
			fs, _ := newMCPFlagSet(t)
			err := fs.Parse([]string{"--mcp-server-insecure-http", bad})
			if err == nil {
				t.Errorf("parse(%q): want a charset error, got nil", bad)
				continue
			}
			if !strings.Contains(err.Error(), "A-Za-z0-9_") {
				t.Errorf("parse(%q) error = %q, want the name-charset guidance", bad, err)
			}
		}
	})

	t.Run("duplicate relaxations rejected (case-insensitive)", func(t *testing.T) {
		for _, second := range []string{"tequitl", "TEQUITL", "tEqUiTl"} {
			fs, _ := newMCPFlagSet(t)
			err := fs.Parse([]string{
				"--mcp-server-insecure-http", "tequitl",
				"--mcp-server-insecure-http", second,
			})
			if err == nil {
				t.Errorf("duplicate relaxation %q: want error, got nil", second)
			}
		}
	})
}

// TestMCPServerInsecureHTTPNameMatchesCaseInsensitively: the server name is
// case-insensitively unique (it derives MCP_<NAME>_TOKEN), so the relaxation
// matches it case-insensitively too — TEQUITL relaxes tequitl.
func TestMCPServerInsecureHTTPNameMatchesCaseInsensitively(t *testing.T) {
	t.Setenv("MCP_TEQUITL_TOKEN", "s3cr3t")
	fs, list := newMCPFlagSet(t)
	err := fs.Parse([]string{
		"--mcp-server", "tequitl=http://tequitl.internal/mcp",
		"--mcp-server-insecure-http", "TEQUITL",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFinalize(t, list)
	if got := list.Servers(); len(got) != 1 || got[0].Headers["Authorization"] != "Bearer s3cr3t" {
		t.Errorf("servers = %v, want the bearer attached under the case-folded relaxation", got)
	}
}

// TestMCPServerInsecureHTTPTokenlessServerAllowed: a relaxation naming a
// TOKENLESS plain-http off-host server is accepted — the acknowledgment
// matches the URL's real shape (cleartext off-host), and the entry simply has
// no bearer to protect. Only an acknowledgment that CONTRADICTS the URL shape
// (https/loopback/non-http) is stale.
func TestMCPServerInsecureHTTPTokenlessServerAllowed(t *testing.T) {
	fs, list := newMCPFlagSet(t)
	err := fs.Parse([]string{
		"--mcp-server", "tequitl=http://tequitl.internal/mcp",
		"--mcp-server-insecure-http", "tequitl",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mustFinalize(t, list)
	if got := list.Servers(); len(got) != 1 || got[0].Headers != nil {
		t.Errorf("servers = %v, want one tokenless entry", got)
	}
}

// TestMCPServerListServersPanicsWithoutFinalize pins the fail-closed guard:
// Servers() before a successful Finalize would hand out configs the deferred
// token-bearing scheme gate never vetted, so it panics (a programmer error in
// a main that forgot the post-parse call — never an operator error path).
func TestMCPServerListServersPanicsWithoutFinalize(t *testing.T) {
	t.Setenv("MCP_VMCP_TOKEN", "s3cr3t")
	fs, list := newMCPFlagSet(t)
	if err := fs.Parse([]string{"--mcp-server", "vmcp=http://vmcp.internal/mcp"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Error("Servers() without Finalize: want a panic, got none")
		}
	}()
	list.Servers()
}

// TestMCPServerListServersNilSafe mirrors KeyValueList.AsMap's nil-receiver
// discipline: a config struct built without RegisterMCPServerFlag yields nil
// (no panic — there is nothing the gate could have missed), and Finalize on
// the nil receiver is a no-op nil.
func TestMCPServerListServersNilSafe(t *testing.T) {
	var list *MCPServerList
	if err := list.Finalize(); err != nil {
		t.Errorf("nil receiver Finalize() = %v, want nil", err)
	}
	if got := list.Servers(); got != nil {
		t.Errorf("nil receiver Servers() = %v, want nil", got)
	}
}

// TestMCPServerListDisableNotificationsEnv pins the ADR 0326 GET-hostile
// gateway opt-out: MCP_<NAME>_DISABLE_NOTIFICATIONS (the same name-derived env
// convention as the token) sets ServerConfig.DisableNotifications on that
// server only. Unset = default (stream on); a value that does not parse as a
// bool is a fail-loud Finalize error, never a silent default.
func TestMCPServerListDisableNotificationsEnv(t *testing.T) {
	t.Run("set true disables the stream on that server only", func(t *testing.T) {
		t.Setenv("MCP_VMCP_DISABLE_NOTIFICATIONS", "true")
		fs, list := newMCPFlagSet(t)
		err := fs.Parse([]string{
			"--mcp-server", "vmcp=https://vmcp.internal/mcp",
			"--mcp-server", "tequitl=https://tequitl.internal/mcp",
		})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		mustFinalize(t, list)
		got := list.Servers()
		if len(got) != 2 {
			t.Fatalf("Servers() len = %d, want 2", len(got))
		}
		if !got[0].DisableNotifications {
			t.Errorf("vmcp DisableNotifications = false, want true (MCP_VMCP_DISABLE_NOTIFICATIONS=true)")
		}
		if got[1].DisableNotifications {
			t.Errorf("tequitl DisableNotifications = true, want false (env var is per-server)")
		}
	})

	t.Run("explicit false keeps the stream on", func(t *testing.T) {
		t.Setenv("MCP_VMCP_DISABLE_NOTIFICATIONS", "false")
		fs, list := newMCPFlagSet(t)
		if err := fs.Parse([]string{"--mcp-server", "vmcp=https://vmcp.internal/mcp"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		mustFinalize(t, list)
		if got := list.Servers(); len(got) != 1 || got[0].DisableNotifications {
			t.Errorf("Servers() = %+v, want one entry with DisableNotifications false", got)
		}
	})

	t.Run("unparseable value fails Finalize loudly", func(t *testing.T) {
		t.Setenv("MCP_VMCP_DISABLE_NOTIFICATIONS", "ture")
		fs, list := newMCPFlagSet(t)
		if err := fs.Parse([]string{"--mcp-server", "vmcp=https://vmcp.internal/mcp"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		err := list.Finalize()
		if err == nil {
			t.Fatal("unparseable bool: want Finalize error, got nil (a typo must not silently keep the churny stream)")
		}
		if !strings.Contains(err.Error(), "MCP_VMCP_DISABLE_NOTIFICATIONS") {
			t.Errorf("error = %q, want it to name MCP_VMCP_DISABLE_NOTIFICATIONS", err)
		}
	})

	t.Run("unset defaults to stream on", func(t *testing.T) {
		fs, list := newMCPFlagSet(t)
		if err := fs.Parse([]string{"--mcp-server", "vmcp=https://vmcp.internal/mcp"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		mustFinalize(t, list)
		if got := list.Servers(); len(got) != 1 || got[0].DisableNotifications {
			t.Errorf("Servers() = %+v, want the default (DisableNotifications false)", got)
		}
	})
}

// TestRegisterMCPServerFlagHelp proves the registration uses the canonical
// flag names and, absent an override, the shared default help texts — the one
// mecated carried before the extraction — so the three mains cannot drift.
// --mcp-server-insecure-http rides the SAME registration call, so a main
// cannot register the server flag without its opt-in companion, and its help
// must state the cleartext acknowledgment plainly.
func TestRegisterMCPServerFlagHelp(t *testing.T) {
	fs, _ := newMCPFlagSet(t)
	fl := fs.Lookup("mcp-server")
	if fl == nil {
		t.Fatal("--mcp-server not registered")
	}
	if fl.Usage != DefaultMCPServerFlagHelp {
		t.Errorf("help = %q, want DefaultMCPServerFlagHelp", fl.Usage)
	}

	ins := fs.Lookup("mcp-server-insecure-http")
	if ins == nil {
		t.Fatal("--mcp-server-insecure-http not registered alongside --mcp-server")
	}
	if ins.Usage != DefaultMCPServerInsecureHTTPFlagHelp {
		t.Errorf("insecure help = %q, want DefaultMCPServerInsecureHTTPFlagHelp", ins.Usage)
	}
	for _, want := range []string{"cleartext", "network"} {
		if !strings.Contains(strings.ToLower(ins.Usage), want) {
			t.Errorf("insecure help must state the acknowledgment plainly (missing %q): %q", want, ins.Usage)
		}
	}

	fs2 := flag.NewFlagSet("test", flag.ContinueOnError)
	RegisterMCPServerFlag(fs2, "custom wording")
	if got := fs2.Lookup("mcp-server").Usage; got != "custom wording" {
		t.Errorf("help override = %q, want custom wording", got)
	}
}
