package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestParseFlagsMCPServer covers the factory MCP wiring (issue #341): the shared
// --mcp-server flag (cliconfig.MCPServerList) is registered on mecatequi, the
// MCP_<NAME>_TOKEN convention resolves a bearer header, repeats accumulate, bad
// shapes are rejected at parse — and appConfig threads the list onto
// app.Config.MCPServers so app.Build's static MCP source sees it.
func TestParseFlagsMCPServer(t *testing.T) {
	t.Run("name=URL with token env maps onto app.Config.MCPServers", func(t *testing.T) {
		t.Setenv("MCP_TEQUITL_TOKEN", "run-token")
		f, err := parseFlags([]string{
			"--prompt", "x",
			"--mcp-server", "tequitl=https://tequitl.internal/mcp",
			"--mcp-server", "vmcp=https://vmcp.internal/mcp",
		})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		cfg := appConfig(f, newDiagnostics())
		if len(cfg.MCPServers) != 2 {
			t.Fatalf("app.Config.MCPServers len = %d, want 2", len(cfg.MCPServers))
		}
		if cfg.MCPServers[0].Name != "tequitl" || cfg.MCPServers[0].URL != "https://tequitl.internal/mcp" {
			t.Errorf("first server = %+v", cfg.MCPServers[0])
		}
		if auth := cfg.MCPServers[0].Headers["Authorization"]; auth != "Bearer run-token" {
			t.Errorf("tequitl Authorization = %q, want the MCP_TEQUITL_TOKEN bearer", auth)
		}
		if cfg.MCPServers[1].Headers != nil {
			t.Errorf("vmcp Headers = %v, want nil (token optional)", cfg.MCPServers[1].Headers)
		}
	})

	t.Run("bad shape is a parse error", func(t *testing.T) {
		if _, err := parseFlags([]string{"--prompt", "x", "--mcp-server", "noequals"}); err == nil {
			t.Fatal("parseFlags with a malformed --mcp-server: want error, got nil")
		}
	})

	t.Run("unset leaves MCPServers empty", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if got := appConfig(f, newDiagnostics()).MCPServers; len(got) != 0 {
			t.Errorf("MCPServers = %v, want empty", got)
		}
	})
}

// TestParseFlagsSummaryCompact covers the explicit stdout-compact summary mode
// (issue #341): an EXPLICIT --out-summary=- selects the compact single-line
// emit; the unset default (which also resolves to "-") and an explicit file
// path both keep the indented default.
func TestParseFlagsSummaryCompact(t *testing.T) {
	t.Run("default (unset) is NOT compact", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.summaryCompact {
			t.Error("the unset default must keep the indented summary (behavior unchanged)")
		}
	})

	t.Run("explicit --out-summary=- IS compact", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--out-summary", "-"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if !f.summaryCompact {
			t.Error("an explicit --out-summary=- must select the compact single-line mode")
		}
	})

	t.Run("explicit file path is NOT compact", func(t *testing.T) {
		f, err := parseFlags([]string{"--prompt", "x", "--out-summary", "/tmp/sum.json"})
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}
		if f.summaryCompact {
			t.Error("an explicit file path must keep the indented summary")
		}
	})
}

// TestEmitSummaryCompactSingleLine pins the compact emit shape: exactly ONE
// line, no indentation, parsing back to the complete Summary. The indented
// default stays multi-line (the unchanged-default half).
func TestEmitSummaryCompactSingleLine(t *testing.T) {
	sum := Summary{
		SchemaVersion: SummarySchemaVersion,
		SessionID:     "sess-1",
		StopReason:    "end_turn",
		NonEmptyDiff:  true,
		DiffBytes:     42,
		FinalText:     "done\nwith a newline",
	}

	t.Run("compact is one line and round-trips", func(t *testing.T) {
		var buf bytes.Buffer
		f := flags{outSummary: "-", summaryCompact: true}
		if err := emitSummary(f, &buf, sum); err != nil {
			t.Fatalf("emitSummary: %v", err)
		}
		out := buf.String()
		if !strings.HasSuffix(out, "\n") {
			t.Fatalf("compact summary must end with a newline; got %q", out)
		}
		body := strings.TrimSuffix(out, "\n")
		if strings.Contains(body, "\n") {
			t.Fatalf("compact summary must be a SINGLE line; got %q", out)
		}
		var got Summary
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("compact summary line does not parse: %v\n%s", err, body)
		}
		if got != sum {
			t.Errorf("round-trip mismatch: got %+v, want %+v", got, sum)
		}
	})

	t.Run("default stays indented multi-line", func(t *testing.T) {
		var buf bytes.Buffer
		f := flags{outSummary: "-"}
		if err := emitSummary(f, &buf, sum); err != nil {
			t.Fatalf("emitSummary: %v", err)
		}
		if !strings.Contains(buf.String(), "\n  \"") {
			t.Errorf("default summary must remain indented JSON; got %q", buf.String())
		}
	})
}

// TestRealMainCompactSummaryIsFinalStdoutLine drives the WHOLE main path with
// an explicit --out-summary=- over the mock provider and asserts the contract a
// log-tailing scheduler relies on: the LAST stdout line parses as the COMPLETE
// Summary JSON, and nothing follows it.
func TestRealMainCompactSummaryIsFinalStdoutLine(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)

	var stdout, stderr bytes.Buffer
	code := realMain([]string{
		"--prompt", "summarise the repo",
		"--mock",
		"--workspace", repo,
		"--out-summary", "-",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("realMain = %d, want 0\nstderr=%s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("stdout must end with the summary's trailing newline; got %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	last := lines[len(lines)-1]
	var sum Summary
	if err := json.Unmarshal([]byte(last), &sum); err != nil {
		t.Fatalf("last stdout line is not the complete Summary JSON: %v\nline=%q\nstdout=%q", err, last, out)
	}
	if sum.SchemaVersion != SummarySchemaVersion || sum.SessionID == "" || sum.StopReason == "" {
		t.Errorf("last-line Summary incomplete: %+v", sum)
	}
}

// TestRealMainDefaultSummaryStdoutIndented pins the unchanged default: with
// --out-summary UNSET (resolving to stdout), the summary stays indented JSON.
func TestRealMainDefaultSummaryStdoutIndented(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)

	var stdout, stderr bytes.Buffer
	code := realMain([]string{
		"--prompt", "summarise the repo",
		"--mock",
		"--workspace", repo,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("realMain = %d, want 0\nstderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "\n  \"") {
		t.Errorf("default stdout summary must remain indented JSON; got %q", stdout.String())
	}
	var sum Summary
	if err := json.Unmarshal(stdout.Bytes(), &sum); err != nil {
		t.Fatalf("default stdout summary must still parse as one JSON object: %v", err)
	}
}
