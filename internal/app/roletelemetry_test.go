package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
)

// roleFamilySet is the CLOSED set of family labels roleFamily may emit. It must
// stay in lockstep with the telemetry adapter's Role* constants (asserted in
// TestRoleFamilyMatchesTelemetryConstants below) and mcpperf's roleFamilies
// allowlist (asserted in that package's security tests).
var roleFamilySet = map[string]bool{
	"main": true, "subagent": true, "member": true, "parallel": true,
	"usermodel": true, "child": true,
}

// TestRoleFamilyMapping pins the CLOSED role→family mapping, including the
// cardinality guard: a def name, member name, model id, or session id embedded
// in the raw role must NEVER appear in the output — every input collapses onto
// one of the six family values, with "child" as the fail-safe bucket.
func TestRoleFamilyMapping(t *testing.T) {
	cases := []struct {
		role string
		want string
	}{
		{"", "main"},
		{"task", "subagent"},
		{"task:code-reviewer", "subagent"},
		{"task:model=gpt-5-mini", "subagent"},
		{"member:explorer", "member"},
		{"member:security-auditor", "member"},
		{"parallel", "parallel"},
		// The judge is part of the Parallel fan-out's cost story.
		{"parallel-judge", "parallel"},
		{"usermodel-review", "usermodel"},
		// Anything unrecognised lands in the bounded fallback bucket. There is
		// no "fork" family: fork/fork-judge are session-id prefixes, never a
		// Deps.Role, so a hypothetical role of that shape is just unrecognised.
		{"fork", "child"},
		{"fork-judge", "child"},
		{"some-future-role", "child"},
		{"sess-7f3a1b", "child"},
	}
	for _, tc := range cases {
		if got := roleFamily(tc.role); got != tc.want {
			t.Errorf("roleFamily(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}

	// Cardinality guard: name-bearing roles must never leak their embedded name
	// into the label value, and EVERY output must be a member of the closed set.
	leaky := []struct {
		role     string
		embedded string
	}{
		{"task:super-secret-def", "super-secret-def"},
		{"task:model=acme/private-model", "acme"},
		{"member:alice", "alice"},
		{"member:internal-sec-team", "internal-sec-team"},
		{"sess-deadbeef0123", "deadbeef"},
	}
	for _, tc := range leaky {
		got := roleFamily(tc.role)
		if strings.Contains(got, tc.embedded) {
			t.Errorf("roleFamily(%q) = %q leaked the embedded name %q into the label value", tc.role, got, tc.embedded)
		}
		if !roleFamilySet[got] {
			t.Errorf("roleFamily(%q) = %q is outside the closed family set", tc.role, got)
		}
	}
}

// TestRoleFamilyMatchesTelemetryConstants pins roleFamily's output vocabulary
// against the telemetry adapter's exported Role* constants, so the two closed
// sets (composed in different packages by design — internal/app does not import
// telemetry in production code) cannot drift apart.
func TestRoleFamilyMatchesTelemetryConstants(t *testing.T) {
	pairs := map[string]string{
		roleFamily(""):                 telemetry.RoleMain,
		roleFamily("task"):             telemetry.RoleSubagent,
		roleFamily("member:x"):         telemetry.RoleMember,
		roleFamily("parallel-judge"):   telemetry.RoleParallel,
		roleFamily("usermodel-review"): telemetry.RoleUserModel,
		roleFamily("anything-else"):    telemetry.RoleChild,
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("roleFamily output %q != telemetry constant %q", got, want)
		}
	}
}

// fakeScopedSink/fakeScopedRecorder are distinguishable fakes a test scoper
// returns, carrying the family they were minted for.
type fakeScopedSink struct{ family string }

func (*fakeScopedSink) Emit(context.Context, session.Event) {}

type fakeScopedRecorder struct{ family string }

func (*fakeScopedRecorder) ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
}

// TestChildEngineDepsRoleScopedTelemetryWhenScoperSet proves a wired
// MetricsRoleScoper supplies the child Deps' Sink/ToolCallRecorder, keyed on
// the BOUNDED family (never the raw role), on BOTH child deps builders.
func TestChildEngineDepsRoleScopedTelemetryWhenScoperSet(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "mock"}
	cfg.MetricsRoleScoper = func(family string) (port.EventSink, port.ToolCallRecorder) {
		return &fakeScopedSink{family: family}, &fakeScopedRecorder{family: family}
	}
	provider := mockllm.New()

	cases := []struct {
		role string
		want string
	}{
		{"task", "subagent"},
		{"member:explorer", "member"},
		{"parallel", "parallel"},
	}
	for _, tc := range cases {
		deps := childEngineDepsForProvider(cfg, tc.role, provider, cfg.Model, func() int { return defaultContextWindowTokens }, tool.NewCatalog(), promptConfig(cfg, ""), nil)
		sink, ok := deps.Sink.(*fakeScopedSink)
		if !ok {
			t.Fatalf("role %q: Deps.Sink = %T, want the scoper's sink", tc.role, deps.Sink)
		}
		if sink.family != tc.want {
			t.Errorf("role %q: scoper sink family = %q, want %q (the bounded family, not the raw role)", tc.role, sink.family, tc.want)
		}
		rec, ok := deps.ToolCallRecorder.(*fakeScopedRecorder)
		if !ok {
			t.Fatalf("role %q: Deps.ToolCallRecorder = %T, want the scoper's recorder", tc.role, deps.ToolCallRecorder)
		}
		if rec.family != tc.want {
			t.Errorf("role %q: scoper recorder family = %q, want %q", tc.role, rec.family, tc.want)
		}
	}

	// The DEFAULT-provider child shape (childEngineDeps: parallel-judge and the
	// usermodel-review child) is wired equivalently — the two builders must not
	// drift (the same posture rule as the Clock inheritance test).
	defDeps := childEngineDeps(cfg, "usermodel-review", provider, tool.NewCatalog(), cfg.Model, fixedDefaultWindow, promptConfig(cfg, ""), nil)
	sink, ok := defDeps.Sink.(*fakeScopedSink)
	if !ok || sink.family != "usermodel" {
		t.Errorf("childEngineDeps(usermodel-review) Sink = %T/%+v, want the scoper's sink with family usermodel", defDeps.Sink, defDeps.Sink)
	}
	if rec, ok := defDeps.ToolCallRecorder.(*fakeScopedRecorder); !ok || rec.family != "usermodel" {
		t.Errorf("childEngineDeps(usermodel-review) ToolCallRecorder = %T, want the scoper's recorder with family usermodel", defDeps.ToolCallRecorder)
	}
}

// TestChildEngineDepsNilTelemetryWhenNoScoper pins the no-perf path: with no
// MetricsRoleScoper, both child deps builders leave Sink/ToolCallRecorder nil —
// byte-identical to the pre-feature unmetered child shape — even when the MAIN
// pair is wired on cfg.
func TestChildEngineDepsNilTelemetryWhenNoScoper(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), Model: "mock"}
	// A main-engine pair on cfg must NOT bleed into children (the double-count
	// hazard the old nil-forcing comment documented).
	cfg.Sink = &fakeScopedSink{family: "main"}
	cfg.ToolCallRecorder = &fakeScopedRecorder{family: "main"}
	provider := mockllm.New()

	deps := childEngineDepsForProvider(cfg, "task", provider, cfg.Model, func() int { return defaultContextWindowTokens }, tool.NewCatalog(), promptConfig(cfg, ""), nil)
	if deps.Sink != nil {
		t.Errorf("childEngineDepsForProvider Deps.Sink = %T, want nil without a scoper", deps.Sink)
	}
	if deps.ToolCallRecorder != nil {
		t.Errorf("childEngineDepsForProvider Deps.ToolCallRecorder = %T, want nil without a scoper", deps.ToolCallRecorder)
	}

	defDeps := childEngineDeps(cfg, "parallel-judge", provider, tool.NewCatalog(), cfg.Model, fixedDefaultWindow, promptConfig(cfg, ""), nil)
	if defDeps.Sink != nil || defDeps.ToolCallRecorder != nil {
		t.Errorf("childEngineDeps Sink/ToolCallRecorder = %T/%T, want nil/nil without a scoper", defDeps.Sink, defDeps.ToolCallRecorder)
	}
}

// --- integration: real composition + mockllm + real telemetry.Metrics ---

// newRoleScopedMetrics builds a REAL telemetry.Metrics whose series render on a
// prometheus registry (the same exporter pipeline production uses), so the
// integration tests can gather and assert role-labelled series.
func newRoleScopedMetrics(t *testing.T) (*telemetry.Metrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	exp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		t.Fatalf("prometheus exporter: %v", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	m, err := telemetry.NewMetrics(mp)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return m, reg
}

// driveSubagentWithRoleMetrics assembles the real composition path — a child
// engine built by buildChildEngine over a cfg carrying a MetricsRoleScoper fed
// by the REAL telemetry adapter, wrapped in the Subagent tool and driven by a
// parent engine whose own pair is the role="main" view — then returns the
// gathered metric families.
func driveSubagentWithRoleMetrics(t *testing.T) []*dto.MetricFamily {
	t.Helper()
	metrics, reg := newRoleScopedMetrics(t)

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hello role metrics\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := Config{Workspace: ws, Model: "mock"}
	// Mirror the cmd wiring EXACTLY: the main pair is the role="main" view on
	// cfg.Sink/cfg.ToolCallRecorder, the scoper hands children their own views.
	// Wiring cfg's main pair is what makes the canonical leak DETECTABLE: a
	// regression that drops the childTelemetryFor override in
	// childEngineDepsForProvider makes the child inherit cfg's main pair, so the
	// leak manifests as main-count INFLATION in the no-double-count test below —
	// with a nil cfg.Sink it would just make the child invisible instead.
	mainScoped := metrics.WithRole(telemetry.RoleMain)
	cfg.Sink = mainScoped
	cfg.ToolCallRecorder = mainScoped
	cfg.MetricsRoleScoper = func(family string) (port.EventSink, port.ToolCallRecorder) {
		scoped := metrics.WithRole(family)
		return scoped, scoped
	}

	// The child reads a file (a real tool call on the subagent series) and ends
	// with non-zero usage so tokens{role="subagent"} is observable.
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "r1", Name: "Read", Args: json.RawMessage(`{"path":"hello.txt"}`)}),
		mockllm.ChunksTurn(
			mockllm.TextChunk("child done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 100, OutputTokens: 40}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	childEng := buildChildEngine(cfg, nil, childProvider, "", cfg.Model, nil)
	task := agent.NewSubagentTool(childEng)

	// The parent calls Subagent once and ends with its OWN distinct usage, so the
	// no-double-count assertion can separate main from child token counts.
	parentProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "t1", Name: "Subagent", Args: json.RawMessage(`{"prompt":"read hello.txt and report"}`)}),
		mockllm.ChunksTurn(
			mockllm.TextChunk("parent done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 7, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)
	// Role "" routes the parent through the SAME scoper onto the "main" family —
	// the uniform-label property the cmd wiring establishes with WithRole(RoleMain).
	parentEng := newChildEngine(cfg, "", parentProvider, parentCat, cfg.Model, fixedDefaultWindow, promptConfig(cfg, cfg.gitStatus))

	sess := session.New("parent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws, Revision: "in-tree-v1"}, session.Limits{MaxTurns: 5}, time.Now())
	parentWS := osfsWSForTest(t, ws)
	run := parentEng.Run(context.Background(), sess, testEnvironment(parentWS, nil), agent.RunRequest{Text: "go"})
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "t1" && ev.ToolResult.IsError {
			t.Fatalf("Subagent tool result is an error: %q", ev.ToolResult.Content)
		}
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return families
}

// counterValue sums the counter values of every series in the named family
// whose labels include all the given pairs. Returns -1 when the family is
// absent and 0 when no series matches.
func counterValue(families []*dto.MetricFamily, name string, labels map[string]string) float64 {
	var mf *dto.MetricFamily
	for _, f := range families {
		if f.GetName() == name {
			mf = f
			break
		}
	}
	if mf == nil {
		return -1
	}
	sum := 0.0
	for _, m := range mf.GetMetric() {
		got := map[string]string{}
		for _, lp := range m.GetLabel() {
			got[lp.GetName()] = lp.GetValue()
		}
		matches := true
		for k, v := range labels {
			if got[k] != v {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		switch {
		case m.GetCounter() != nil:
			sum += m.GetCounter().GetValue()
		case m.GetUntyped() != nil:
			sum += m.GetUntyped().GetValue()
		}
	}
	return sum
}

// TestChildDriveRecordsRoleTaggedMetrics drives a Subagent child through the
// real composition (buildChildEngine + the Subagent tool + a real
// telemetry.Metrics behind the scoper) and asserts the child's activity lands
// on role="subagent" series: a non-zero tool_calls_total and non-zero tokens.
func TestChildDriveRecordsRoleTaggedMetrics(t *testing.T) {
	families := driveSubagentWithRoleMetrics(t)

	if got := counterValue(families, "mecatl_tool_calls_total", map[string]string{"role": "subagent", "tool": "Read"}); got != 1 {
		t.Errorf("tool_calls_total{role=subagent,tool=Read} = %v, want 1", got)
	}
	if got := counterValue(families, "mecatl_tokens_total", map[string]string{"role": "subagent", "kind": "input"}); got != 100 {
		t.Errorf("tokens{role=subagent,kind=input} = %v, want 100", got)
	}
	if got := counterValue(families, "mecatl_tokens_total", map[string]string{"role": "subagent", "kind": "output"}); got != 40 {
		t.Errorf("tokens{role=subagent,kind=output} = %v, want 40", got)
	}
}

// TestChildMetricsNoDoubleCountAgainstMain proves child activity NEVER folds
// into the role="main" series: after the same Subagent drive, main's token and
// tool-call counts reflect ONLY the parent's own activity.
func TestChildMetricsNoDoubleCountAgainstMain(t *testing.T) {
	families := driveSubagentWithRoleMetrics(t)

	// Main tokens are exactly the parent's usage — adding the child's 100/40
	// here is the double-count regression this test guards.
	if got := counterValue(families, "mecatl_tokens_total", map[string]string{"role": "main", "kind": "input"}); got != 7 {
		t.Errorf("tokens{role=main,kind=input} = %v, want 7 (child tokens must not double into main)", got)
	}
	if got := counterValue(families, "mecatl_tokens_total", map[string]string{"role": "main", "kind": "output"}); got != 3 {
		t.Errorf("tokens{role=main,kind=output} = %v, want 3", got)
	}
	// Main saw exactly one tool call (the Subagent call itself); the child's
	// Read must not appear on the main series.
	if got := counterValue(families, "mecatl_tool_calls_total", map[string]string{"role": "main"}); got != 1 {
		t.Errorf("tool_calls_total{role=main} = %v, want 1 (the Subagent call only)", got)
	}
	if got := counterValue(families, "mecatl_tool_calls_total", map[string]string{"role": "main", "tool": "Read"}); got != 0 {
		t.Errorf("tool_calls_total{role=main,tool=Read} = %v, want 0 (the child's Read must stay on the subagent series)", got)
	}
}
