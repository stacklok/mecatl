package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// The top-level `output-economy:` key was REMOVED (ADR 0089, the clean break
// superseding ADR 0086's parse-compat shim). parseYAML is deliberately lenient
// at the top level (plain
// yaml.Unmarshal), so the removed key gets a TARGETED named rejection that rides
// the existing invalid-file path (WARN + skip). These tests pin the error text
// at every tier that parses through parseYAML.

const removedOutputEconomyYAML = "output-economy: terse\n"

func TestParseYAMLOutputEconomyIsUnknownKey(t *testing.T) {
	_, err := parseYAML([]byte(removedOutputEconomyYAML))
	if err == nil {
		t.Fatal("parseYAML(output-economy: terse) = nil error; want a named unknown-key error")
	}
	if !strings.Contains(err.Error(), "output-economy") || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("parseYAML err = %q, want it to name the output-economy key as unknown", err)
	}
}

// A NESTED `output-economy:` under a section must NOT trip the top-level-only
// rejection — the flat-struct probe has no path to it, so a section key of the
// same name stays the lenient decode's business (the strict permissions:
// subtree reports unknown section keys through its own path).
func TestNestedOutputEconomyDoesNotTripTopLevelRejection(t *testing.T) {
	if err := rejectRemovedTopLevelKeys([]byte("permissions:\n  output-economy: terse\n")); err != nil {
		t.Fatalf("nested output-economy tripped the top-level rejection: %v", err)
	}
	// Malformed YAML falls through (nil) — the real decode reports it.
	if err := rejectRemovedTopLevelKeys([]byte("output-economy: [unclosed\n")); err != nil {
		t.Fatalf("malformed YAML should fall through to the real decode, got: %v", err)
	}
}

// Operator tier: an explicit (CLI) settings.yaml carrying the removed key is
// skipped with the invalid-file WARN whose error names the key precisely.
func TestOperatorOutputEconomyIsUnknownKey(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)
	env := envWithExplicit("/etc/mecatl/economy.yaml", removedOutputEconomyYAML)
	r := newWithEnv(Options{
		ExplicitFiles: []string{"/etc/mecatl/economy.yaml"},
		Diagnostics:   diag,
	}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	_ = r.Resolve(context.Background(), &countingWS{Workspace: memfs.NewWorkspace("/repo")})
	log := buf.String()
	if !strings.Contains(log, "output-economy") || !strings.Contains(log, "unknown key") {
		t.Fatalf("expected the invalid-file WARN to name output-economy as an unknown key; got:\n%s", log)
	}
}

// Project tier: a project .mecatl/settings.yaml carrying the removed key hits
// the same parseYAML and the same named rejection.
func TestProjectOutputEconomyIsUnknownKey(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, removedOutputEconomyYAML)

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	log := buf.String()
	if !strings.Contains(log, "output-economy") || !strings.Contains(log, "unknown key") {
		t.Fatalf("expected the invalid-file WARN to name output-economy as an unknown key; got:\n%s", log)
	}
}
