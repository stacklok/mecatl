package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// capturingDiagnostics is a port.Diagnostics test double that records every
// emitted message (and its level). With binds attributes but, for the purposes
// of the build-once guard, records onto the SAME shared store so a child sink's
// emissions are still counted against the parent's tally. It is concurrency-safe.
type capturingDiagnostics struct {
	mu      *sync.Mutex
	msgs    *[]string
	records *[]string
	bound   []any
}

func newCapturingDiagnostics() *capturingDiagnostics {
	return &capturingDiagnostics{mu: &sync.Mutex{}, msgs: &[]string{}, records: &[]string{}}
}

func (c *capturingDiagnostics) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	// A nil store (zero value used only as a non-nil sink, e.g. in
	// TestSubproviderChildTelemetryOff) drops the record like a Nop.
	if c.mu == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.msgs = append(*c.msgs, msg)
	*c.records = append(*c.records, fmt.Sprint(append(append([]any{msg}, c.bound...), args...)...))
}

func (c *capturingDiagnostics) With(args ...any) port.Diagnostics {
	return &capturingDiagnostics{mu: c.mu, msgs: c.msgs, records: c.records, bound: append(append([]any{}, c.bound...), args...)}
}

func (c *capturingDiagnostics) capturedStrings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), (*c.records)...)
}

func (c *capturingDiagnostics) countContaining(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range *c.msgs {
		if strings.Contains(m, substr) {
			n++
		}
	}
	return n
}

// TestBuildConfigFactsLogOnceAcrossChildDerivations is the relocation guard: the
// build-once composition facts (token counter / compaction strategy / slash
// commands) must be emitted EXACTLY ONCE through the injected Diagnostics — at
// composition — NOT once per session/child-engine derivation.
//
// It drives the relocated top-level emit (logBuildConfigFacts, what Build calls
// once) PLUS N child-engine derivations via childEngineDepsForProvider (the
// team-member / subagent / Half-B path). Against the OLD per-builder logging the
// facts went through slog, not Diagnostics, so the capturing sink would see ZERO
// — the test FAILS (0 != 1). After the relocation the sink sees exactly one of
// each — the test PASSES. It would ALSO fail if the relocation were wrongly put
// back inside the per-derivation builders via Diagnostics (then N+1 emissions).
func TestBuildConfigFactsLogOnceAcrossChildDerivations(t *testing.T) {
	diag := newCapturingDiagnostics()
	cfg := Config{
		Model:      "gpt-5",
		Tokenizer:  "heuristic",
		Compaction: "heuristic",
		// A configured shell on an UNTRUSTED workspace (TrustProject false, the zero
		// value): the issue-#40 untrusted-shell INFO must join the build-once facts —
		// fired here, never by the per-session/per-child derivations below.
		Shell:       "/bin/sh",
		Diagnostics: diag,
	}
	if cfg.Diagnostics == nil { // mirror Build's default; here it is already set.
		cfg.Diagnostics = port.NopDiagnostics{}
	}

	provider := mockllm.New()

	// The ONE composition-time emit Build performs (after cfg.Model is resolved).
	logBuildConfigFacts(cfg)

	// Now derive the MAIN engine deps and N child engine deps — the per-session and
	// per-child paths that USED to re-log each fact. None of these may emit a fact.
	_ = engineDepsForProvider(cfg, provider, cfg.Model, func() int { return defaultContextWindowTokens },
		nil, nil, nil, nil, nil)
	const nChildren = 5
	for range nChildren {
		_ = childEngineDepsForProvider(cfg, "", provider, cfg.Model, func() int { return defaultContextWindowTokens },
			tool.NewCatalog(), promptConfig(cfg, ""), nil)
	}

	// Each build-once fact family must appear EXACTLY ONCE in total.
	families := map[string]string{
		"token counter":               "token counter:",
		"compaction strategy":         "compaction strategy:",
		"slash commands":              "slash commands",
		"untrusted-shell (issue #40)": "shell DISABLED (untrusted workspace)",
	}
	for name, substr := range families {
		if got := diag.countContaining(substr); got != 1 {
			t.Errorf("build-once fact %q emitted %d times via Diagnostics, want exactly 1 "+
				"(it must fire once at composition, not per session/child derivation)", name, got)
		}
	}
}

// TestBuildEmitsConfigFactsExactlyOnce is the PRODUCTION-WIRING guard: it drives
// the real app.Build (offline, mockllm) with a capturing port.Diagnostics and
// asserts each of the three build-once fact families is emitted EXACTLY ONCE
// through the injected sink. Unlike TestBuildConfigFactsLogOnceAcrossChildDerivations
// — which calls logBuildConfigFacts directly and so cannot notice the Build call
// site being deleted — this test FAILS if the logBuildConfigFacts(cfg) call in
// Build is removed (the facts would silently drop to 0 in production). It is the
// assertion that actually protects the Build wiring.
func TestBuildEmitsConfigFactsExactlyOnce(t *testing.T) {
	diag := newCapturingDiagnostics()
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace: t.TempDir(),
		Model:     "mock",
		UseMock:   true,
		// Shell configured + TrustProject false (zero value): the REAL Build must
		// emit the issue-#40 untrusted-shell INFO exactly once too.
		Shell:       "/bin/sh",
		Diagnostics: diag,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	families := map[string]string{
		"token counter":               "token counter:",
		"compaction strategy":         "compaction strategy:",
		"slash commands":              "slash commands",
		"untrusted-shell (issue #40)": "shell DISABLED (untrusted workspace)",
	}
	for name, substr := range families {
		if got := diag.countContaining(substr); got != 1 {
			t.Errorf("Build emitted build-once fact %q %d times via the injected Diagnostics, "+
				"want exactly 1. A count of 0 means the logBuildConfigFacts(cfg) call in Build "+
				"was removed; >1 means it fires per derivation.", name, got)
		}
	}
}

// TestBuildNarratesFamilyFactsExactlyOnceAcrossSessions extends the build-once
// guard PAST the factory (QA mutant M4 — TestBuildEmitsConfigFactsExactlyOnce
// stops before any per-session engine is built): after a real Build, it creates a
// SELECTOR session through the wired SessionEngine factory — the path that runs
// assembleCatalog with narrate=false — and asserts each tool-family narration
// line still appears EXACTLY ONCE. Dropping the narrate gate (or flipping it to
// true on the per-session path) re-creates the N×-duplication regression class
// CLAUDE.md records as having fired before (the per-derivation re-logging bug):
// the count would become 2 here, and once-per-session/new in production.
func TestBuildNarratesFamilyFactsExactlyOnceAcrossSessions(t *testing.T) {
	diag := newCapturingDiagnostics()
	built, err := buildIsolated(t, context.Background(), Config{
		Workspace:      t.TempDir(),
		Model:          "mock",
		UseMock:        true,
		MemoryDir:      t.TempDir(),
		UserModelDir:   t.TempDir(),
		EnableParallel: true,
		EnableTeams:    true,
		// Shell + untrusted (TrustProject false): the per-session assembly below
		// re-runs the trust-gated runner builders (buildSandboxedCommandRunner via
		// buildTeamWiring/buildSubagentTool), which must stay log-FREE — the
		// untrusted-shell INFO is a build-once fact.
		Shell:       "/bin/sh",
		Diagnostics: diag,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	// A non-zero selector forces a PER-SESSION engine — the factory invokes
	// assembleCatalog a second time (narrate=false), which must add ZERO new
	// family narration lines.
	if _, err := built.Service.CreateSessionWithProvider(context.Background(),
		session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: providerMock}); err != nil {
		t.Fatalf("CreateSessionWithProvider(selector): %v", err)
	}

	families := []string{
		"memory tools ENABLED",
		"user-model tools ENABLED",
		"Parallel tool ENABLED",
		"Team tool ENABLED",
		"SkillDraft tool DISABLED",
		"shell DISABLED (untrusted workspace)",
	}
	for _, substr := range families {
		if got := diag.countContaining(substr); got != 1 {
			t.Errorf("family narration %q emitted %d times via the injected Diagnostics, want exactly 1 "+
				"(0 = the build-time narrate dropped; >1 = the per-session assembly narrates too — the N×-duplication class)", substr, got)
		}
	}
}

func TestLogGuardrailsPostureAutoWithoutCheckerIsUnsupervised(t *testing.T) {
	diag := newCapturingDiagnostics()
	logGuardrailsPosture(Config{Posture: PostureAuto, Diagnostics: diag})
	records := strings.Join(diag.capturedStrings(), "\n")
	if !strings.Contains(records, "UNSUPERVISED") || !strings.Contains(records, "does not enable") {
		t.Fatalf("auto/no-checker posture diagnostic = %q", records)
	}
}
