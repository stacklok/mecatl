package cliconfig

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// newMCPFlagSet builds a quiet ContinueOnError FlagSet with --mcp-server
// registered via RegisterMCPServerFlag, mirroring how the three mains use it.
func newMCPFlagSet(t *testing.T) (*flag.FlagSet, *MCPServerList) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, RegisterMCPServerFlag(fs, "")
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
	if len(list.Servers()) != 2 {
		t.Errorf("Servers() len = %d, want 2", len(list.Servers()))
	}
}

// TestMCPServerListBearerRequiresHTTPS pins the cleartext-token guard
// (CWE-319): when a MCP_<NAME>_TOKEN is present (a bearer WILL be attached),
// the URL must be https — or http to an explicit loopback host. Without a
// token the URL is not gated (unchanged behavior; the operator may point a
// tokenless dev server anywhere).
func TestMCPServerListBearerRequiresHTTPS(t *testing.T) {
	t.Run("token + http non-loopback is rejected", func(t *testing.T) {
		t.Setenv("MCP_VMCP_TOKEN", "s3cr3t")
		fs, _ := newMCPFlagSet(t)
		err := fs.Parse([]string{"--mcp-server", "vmcp=http://vmcp.internal/mcp"})
		if err == nil {
			t.Fatal("bearer over plaintext http off-host: want error, got nil")
		}
		if !strings.Contains(err.Error(), "https") {
			t.Errorf("error = %q, want it to demand https", err)
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
		if got := list.Servers(); len(got) != 1 || got[0].Headers["Authorization"] != "Bearer s3cr3t" {
			t.Errorf("servers = %v, want the bearer attached", got)
		}
	})

	t.Run("no token leaves http non-loopback allowed (unchanged)", func(t *testing.T) {
		fs, list := newMCPFlagSet(t)
		if err := fs.Parse([]string{"--mcp-server", "vmcp=http://vmcp.internal/mcp"}); err != nil {
			t.Fatalf("tokenless http must stay allowed: %v", err)
		}
		if got := list.Servers(); len(got) != 1 || got[0].Headers != nil {
			t.Errorf("servers = %v, want one tokenless entry", got)
		}
	})
}

// TestMCPServerListServersNilSafe mirrors KeyValueList.AsMap's nil-receiver
// discipline: a config struct built without RegisterMCPServerFlag yields nil.
func TestMCPServerListServersNilSafe(t *testing.T) {
	var list *MCPServerList
	if got := list.Servers(); got != nil {
		t.Errorf("nil receiver Servers() = %v, want nil", got)
	}
}

// TestRegisterMCPServerFlagHelp proves the registration uses the canonical
// flag name and, absent an override, the shared default help text — the one
// mecated carried before the extraction — so the three mains cannot drift.
func TestRegisterMCPServerFlagHelp(t *testing.T) {
	fs, _ := newMCPFlagSet(t)
	fl := fs.Lookup("mcp-server")
	if fl == nil {
		t.Fatal("--mcp-server not registered")
	}
	if fl.Usage != DefaultMCPServerFlagHelp {
		t.Errorf("help = %q, want DefaultMCPServerFlagHelp", fl.Usage)
	}

	fs2 := flag.NewFlagSet("test", flag.ContinueOnError)
	RegisterMCPServerFlag(fs2, "custom wording")
	if got := fs2.Lookup("mcp-server").Usage; got != "custom wording" {
		t.Errorf("help override = %q, want custom wording", got)
	}
}
