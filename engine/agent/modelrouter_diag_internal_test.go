package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// modelrouter_diag_internal_test.go pins the per-miss observability INFO (issue #287):
// when a plain delegation's classification MISSES, the dispatch-path routeTask closure
// logs exactly one INFO naming the REASON — metadata only, never the task prompt or the
// classifier output (gauntlet #7).

// TestRouteTaskMissLogsReason drives the production parentCaps.routeTask closure with a
// router that misses, and asserts exactly one miss INFO carrying the reason attr. It
// ALSO plants a sentinel in the task prompt and asserts the log NEVER contains it — the
// reason is a harness constant, the untrusted prompt must not leak into diagnostics.
func TestRouteTaskMissLogsReason(t *testing.T) {
	const sentinel = "SENSITIVE-INJECTION-CANARY-8827"
	diag := newInternalCapturingDiag()

	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			// Miss with a SPECIFIC reason so the INFO's reason attr is assertable.
			return ModelRouteResult{Reason: RouterMissUnknownCategory}
		}},
	})
	// A Run carrying the breaker AND the recording diag (the dispatch closure logs through
	// r.diag). max well above 1 so a single call cannot trip the breaker-open INFO.
	run := &Run{
		router:   &modelRouterBreaker{max: defaultModelRouterMaxMisses},
		children: newChildRunRegistry(),
		diag:     diag,
	}
	caps := mainEngine.parentCaps(run, nil, 0)
	if caps.routeDecision == nil {
		t.Fatal("routeTask must be wired when SubagentModelRouter is set")
	}

	// Drive ONE plain delegation classification whose task prompt embeds the sentinel.
	caps.routeDecision(context.Background(), "please route this task: "+sentinel)

	records := diag.snapshot()
	var missLines int
	for _, r := range records {
		if strings.Contains(r.msg, "classification MISSED") {
			missLines++
			if got := r.attrs["reason"]; got != RouterMissUnknownCategory {
				t.Fatalf("miss INFO reason attr = %v, want %q", got, RouterMissUnknownCategory)
			}
		}
	}
	if missLines != 1 {
		t.Fatalf("want exactly one miss INFO line, got %d (records: %+v)", missLines, records)
	}

	// ADVERSARIAL (gauntlet #7): NO log line — message OR any attribute value — may carry
	// the untrusted task-prompt sentinel. The reason is a harness constant only.
	for _, r := range records {
		if strings.Contains(r.msg, sentinel) {
			t.Fatalf("log message leaked the untrusted task prompt: %q", r.msg)
		}
		for k, v := range r.attrs {
			if strings.Contains(fmt.Sprint(v), sentinel) {
				t.Fatalf("log attr %q leaked the untrusted task prompt: %v", k, v)
			}
		}
	}
}

// TestWritableRouterMissLogsReasonAndFallsBack (issue #285 × #287): a PLAIN writable
// delegation whose classification MISSES logs the WP2 per-miss INFO (reason attr) AND
// falls back CLEANLY to the default writable explorer — the router is never load-bearing,
// even for a writable call.
func TestWritableRouterMissLogsReasonAndFallsBack(t *testing.T) {
	diag := newInternalCapturingDiag()
	// The router closure misses with a specific reason (the WP2 dispatch INFO surfaces it).
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Reason: RouterMissBadVerdict}
		}},
	})
	run := &Run{
		router:   &modelRouterBreaker{max: defaultModelRouterMaxMisses},
		children: newChildRunRegistry(),
		diag:     diag,
	}
	caps := mainEngine.parentCaps(run, nil, 0)

	tl := writableRouterTool(true) // writable factory wired ⇒ a writable delegation DOES route
	res, err := tl.ExecuteWithParent(context.Background(),
		session.NewToolCall("p1", "Subagent", json.RawMessage(`{"prompt":"implement it","mode":"read-write"}`)),
		memEnv("/ws"), nil, caps)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a writable router miss must complete on the default writable engine, got error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "WRITABLE-DEFAULT") {
		t.Fatalf("a writable router miss must fall back to the default writable explorer; got %q", res.Content)
	}
	var found bool
	for _, r := range diag.snapshot() {
		if strings.Contains(r.msg, "classification MISSED") {
			found = true
			if got := r.attrs["reason"]; got != RouterMissBadVerdict {
				t.Fatalf("writable miss INFO reason = %v, want %q", got, RouterMissBadVerdict)
			}
		}
	}
	if !found {
		t.Fatal("a writable router miss must log the WP2 per-miss INFO")
	}
}

// TestRouteTaskMissEmptyReasonFallsBackToEmptyModel pins the "empty-model" fallback: a
// router that reports ok but a BLANK model (a defensive path with no reason of its own)
// still logs a miss INFO, tagged "empty-model" rather than an empty reason attr.
func TestRouteTaskMissEmptyReasonFallsBackToEmptyModel(t *testing.T) {
	diag := newInternalCapturingDiag()
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Category: "some-category", Model: "  ", OK: true} // ok, but blank model, no reason
		}},
	})
	run := &Run{
		router:   &modelRouterBreaker{max: defaultModelRouterMaxMisses},
		children: newChildRunRegistry(),
		diag:     diag,
	}
	caps := mainEngine.parentCaps(run, nil, 0)

	caps.routeDecision(context.Background(), "task")

	var found bool
	for _, r := range diag.snapshot() {
		if strings.Contains(r.msg, "classification MISSED") {
			found = true
			if got := r.attrs["reason"]; got != "empty-model" {
				t.Fatalf("blank-model miss reason attr = %v, want %q", got, "empty-model")
			}
		}
	}
	if !found {
		t.Fatal("a blank routed model must still log a miss INFO")
	}
}

// TestRouterBreakerOpenSkipStaysSilent pins that the per-miss INFO fires ONLY for a real
// classification attempt — the breaker-OPEN skip path emits NO per-miss line. Driving
// misses PAST the breaker max, the "classification MISSED" INFO count must equal exactly
// `max` (the calls that actually consulted the classifier), never max+skips: once the
// breaker opens, the closure returns early (silent) before logRouterMissReason. A
// regression that logged on the skip path would over-count here.
func TestRouterBreakerOpenSkipStaysSilent(t *testing.T) {
	diag := newInternalCapturingDiag()
	mainEngine := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  allowAllInt(),
		Model:   "main",
		SubagentModelRouter: &SubagentModelRouter{Backend: "llm", Route: func(context.Context, string) ModelRouteResult {
			return ModelRouteResult{Reason: RouterMissBadVerdict} // always miss
		}},
	})
	run := &Run{
		router:   &modelRouterBreaker{max: defaultModelRouterMaxMisses},
		children: newChildRunRegistry(),
		diag:     diag,
	}
	caps := mainEngine.parentCaps(run, nil, 0)

	// Call well PAST the breaker max: the first `max` calls consult the classifier (each
	// logs one per-miss INFO), the breaker opens, and the remaining calls SKIP it silently.
	for i := 0; i < defaultModelRouterMaxMisses+4; i++ {
		caps.routeDecision(context.Background(), "task")
	}

	var missLines int
	for _, r := range diag.snapshot() {
		if strings.Contains(r.msg, "classification MISSED") {
			missLines++
		}
	}
	if missLines != defaultModelRouterMaxMisses {
		t.Fatalf("per-miss INFO count = %d, want exactly %d (the breaker-open skip path must stay silent, never max+skips)",
			missLines, defaultModelRouterMaxMisses)
	}
}

func TestRouteDecisionDiagnosticsSanitizeHostileCallbackMetadata(t *testing.T) {
	const forbidden = "private-task-marker"
	diag := newInternalCapturingDiag()
	mainEngine := NewEngine(Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allowAllInt(), Model: "main",
		SubagentModelRouter: &SubagentModelRouter{
			Backend: "jev", ClassifierModel: "classifier\u202emodel\u200b",
			Route: func(context.Context, string) ModelRouteResult {
				return ModelRouteResult{
					Category: "candidate\u202e\x1b[31m",
					Model:    "target\u200b\nmodel",
					Reason:   "opaque SDK error: " + forbidden + "\u202e\x1b[2J",
				}
			},
		},
	})
	run := &Run{router: &modelRouterBreaker{max: defaultModelRouterMaxMisses}, children: newChildRunRegistry(), diag: diag}
	caps := mainEngine.parentCaps(run, nil, 0)
	got := caps.routeConfigured(context.Background(), forbidden)
	if got.decision == nil || got.reason != routingReasonGeneric {
		t.Fatalf("canonical miss = reason %q decision %+v", got.reason, got.decision)
	}
	for _, record := range diag.snapshot() {
		for key, value := range record.attrs {
			text := fmt.Sprint(value)
			if strings.Contains(text, forbidden) || strings.ContainsAny(text, "\x1b\n") || strings.ContainsRune(text, '\u202e') || strings.ContainsRune(text, '\u200b') {
				t.Fatalf("diagnostic %q leaked hostile %s metadata: %q", record.msg, key, text)
			}
		}
	}
}
