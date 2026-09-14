# Product Metrics — Activation/Retention/Reliability Extension Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the already-shipped `internal/adapter/productmetrics` pipeline (PR #1278) to answer the product team's activation/time-to-value/retention/reliability questions: reinstate a per-install identity (accepting the cardinality cost, now that it's been sized and understood), and add `had_tool_call`, tool category/outcome, `run_duration`, `tool_calls_per_run`, and `time_to_first_value`.

**Architecture:** A small, additive `engine/port` capability (`RunAwareToolCallRecorder`, mirroring the existing `HookApprovalLearner` optional-interface precedent) lets the already-shipped `Recorder` correlate a tool call to the run that made it — the one piece of engine-layer work this requires. Everything else is confined to `internal/adapter/productmetrics`, `internal/cliconfig`, and a new Helm template for `mecak8s`'s install-id provisioning.

**Tech Stack:** Go 1.26, the existing `internal/adapter/productmetrics` package (Tasks 1-15 of the prior plan, already merged), Helm (`deploy/helm/mecak8s/`).

**Spec:** This plan's own context section below records every design decision reached in conversation; there is no separate written design doc for this increment — the conversation that produced it is the record.

## Global Constraints

- **No new free-text/PII surface.** Tool category comes from a structural `mcp__` prefix check (confirmed via `internal/adapter/mcp/tool.go:89`'s `"mcp__" + server + "__" + toolName` construction) — never a maintained allowlist, never the raw MCP server/tool name. Everything else stays a bounded enum or a count/duration, per the existing package-wide guard test discipline (Task 6 of the prior plan).
- **`install.id` is now a deliberate, accepted exception** to "every attribute is bounded" — reinstate it as a resource attribute (undoing the earlier removal), now that its cardinality cost has been sized (~$1,930/month at 100K installs, worst-case 24/7 uptime, full catalog, on the actual AMP pricing model) and accepted.
- **The engine port change must be purely additive.** `RunAwareToolCallRecorder` is a NEW, STANDALONE interface (not embedding `ToolCallRecorder`), type-asserted at the one dispatch call site — mirrors `engine/port/hookrunner.go`'s `HookApprovalLearner` exactly. No existing `ToolCallRecorder` implementer (the operator `internal/adapter/telemetry.Metrics`/`RoleMetrics`, `jsonlstore`, `redisstore`, etc.) needs to change at all. This is an `Added` (minor) change per `engine/COMPATIBILITY.md` — run `task api:update` and add an `engine/CHANGELOG.md` entry.
- **`task lint && task test` must stay green after every task.** `task api:check` (part of `task test`) must pass after the engine port change.

---

### Task 1: Engine port extension — `RunAwareToolCallRecorder`

**Files:**
- Modify: `engine/port/log.go` (add the new interface, do NOT touch `ToolCallRecorder`)
- Modify: `engine/agent/dispatch.go` (the one call site, `execute`, currently around line 1293)
- Test: `engine/agent/run_aware_tool_call_recorder_test.go` (new)
- Modify: `engine/CHANGELOG.md`, run `task api:update` to regenerate `engine/api/*.txt`

**Interfaces:**
- Produces: `type RunAwareToolCallRecorder interface { ToolCallForRun(runID string, id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) }` in `engine/port`.

- [ ] **Step 1: Write the failing test**

```go
package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// runAwareFakeRecorder implements BOTH port.ToolCallRecorder and the new
// port.RunAwareToolCallRecorder, recording which method the dispatcher chose.
type runAwareFakeRecorder struct {
	plainCalls    int
	runAwareCalls int
	lastRunID     string
}

func (f *runAwareFakeRecorder) ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
	f.plainCalls++
}

func (f *runAwareFakeRecorder) ToolCallForRun(runID string, _ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	f.runAwareCalls++
	f.lastRunID = runID
}

// TestExecutePrefersRunAwareToolCallRecorderWhenImplemented pins that the
// dispatcher, at its one ToolCallRecorder call site, calls ToolCallForRun
// (never both) when the injected recorder implements it, passing the SAME
// RunID the enclosing Run already carries — and falls back to the plain
// ToolCall for a recorder that does not implement the richer interface
// (every existing ToolCallRecorder implementer is unaffected).
func TestExecutePrefersRunAwareToolCallRecorderWhenImplemented(t *testing.T) {
	rec := &runAwareFakeRecorder{}
	eng, sess := newTestEngineWithToolCallRecorder(t, rec) // see Step 3 for this test helper's real signature, sourced from an existing dispatch_test.go helper
	runID := driveOneToolCallingTurn(t, eng, sess)          // helper: drives a turn that calls a tool at least once

	if rec.plainCalls != 0 {
		t.Errorf("plainCalls = %d, want 0 (RunAwareToolCallRecorder must be preferred)", rec.plainCalls)
	}
	if rec.runAwareCalls == 0 {
		t.Fatal("runAwareCalls = 0, want at least 1")
	}
	if rec.lastRunID != runID {
		t.Errorf("lastRunID = %q, want %q (the enclosing Run's own id)", rec.lastRunID, runID)
	}
}

func TestExecuteFallsBackToPlainToolCallRecorder(t *testing.T) {
	// A recorder implementing ONLY port.ToolCallRecorder (not the richer
	// interface) must keep working exactly as before — confirmed via the
	// EXISTING plain-ToolCallRecorder test fixture already in this package
	// (find it by name in dispatch_test.go and reuse it directly rather than
	// inventing a new one).
}
```

Note to implementer: `newTestEngineWithToolCallRecorder`/`driveOneToolCallingTurn` are placeholder helper NAMES — before writing this file, grep `engine/agent/*_test.go` for the EXISTING test harness this package already uses to build a test `*Engine` and drive a tool-calling turn (there is one; every dispatch test in this package uses it), and write these two tests using the REAL existing helpers/fixtures, not new ones. `TestExecuteFallsBackToPlainToolCallRecorder`'s body is intentionally left for you to fill in using that same real harness with a recorder implementing only the base interface — assert its existing plain-`ToolCall` path still fires exactly as it does today (this is a regression guard, not new behavior).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd engine && go test ./agent/... -run TestExecutePrefersRunAwareToolCallRecorderWhenImplemented -v`
Expected: FAIL — `port.RunAwareToolCallRecorder` undefined, or the dispatcher doesn't yet type-assert for it.

- [ ] **Step 3: Add the port interface**

In `engine/port/log.go`, immediately after the existing `ToolCallRecorder` interface, add:

```go
// RunAwareToolCallRecorder is an OPTIONAL capability a ToolCallRecorder may
// ALSO implement to additionally receive the RunID of the run that made the
// call (the same opaque per-run correlation id carried on session.Event.RunID,
// ADR 0249) — the one thing ToolCall's signature cannot express, since a
// SessionID can span many sequential runs over a session's lifetime and
// ToolCall alone gives no way to tell which run a given call belongs to.
//
// The engine TYPE-ASSERTS this interface on Deps.ToolCallRecorder and calls
// ToolCallForRun INSTEAD OF ToolCall (never both) when implemented — so a
// recorder that implements only the base ToolCallRecorder is wholly
// unaffected (no method added to ToolCallRecorder: that would be a breaking
// change, mirroring the HookApprovalLearner precedent in hookrunner.go).
type RunAwareToolCallRecorder interface {
	// ToolCallForRun is ToolCall's signature plus the leading runID — the
	// same value the enclosing Run stamps onto every session.Event.RunID it
	// emits. Consumers that need to correlate a tool call to the run that
	// made it (e.g. "did this run have at least one successful tool call")
	// use this instead of ToolCall.
	ToolCallForRun(runID string, id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration)
}
```

- [ ] **Step 4: Wire the type-assertion at the dispatch call site**

In `engine/agent/dispatch.go`, replace:

```go
	if e.deps.ToolCallRecorder != nil {
		e.deps.ToolCallRecorder.ToolCall(sess.ID, c, res, queued, dur)
	}
```

with:

```go
	if e.deps.ToolCallRecorder != nil {
		if aware, ok := e.deps.ToolCallRecorder.(port.RunAwareToolCallRecorder); ok {
			aware.ToolCallForRun(r.RunID(), sess.ID, c, res, queued, dur)
		} else {
			e.deps.ToolCallRecorder.ToolCall(sess.ID, c, res, queued, dur)
		}
	}
```

(`r` is the enclosing `execute` method's existing `*Run` parameter — already in scope two lines below this call site where `e.emit(r, ...)` is called; `port` is already imported in this file — confirm, and add the import if for some reason it is not.)

- [ ] **Step 5: Run test to verify it passes**

Run: `cd engine && go test ./agent/... -run 'TestExecutePrefersRunAwareToolCallRecorderWhenImplemented|TestExecuteFallsBackToPlainToolCallRecorder' -v`
Expected: PASS (both tests)

- [ ] **Step 6: Run the full engine test suite to catch any regression**

Run: `cd engine && go test ./... -race`
Expected: PASS, no regressions (this is an additive interface change; every existing `ToolCallRecorder` consumer's tests should be untouched).

- [ ] **Step 7: Update the engine API compatibility surface**

Run: `task api:update` (regenerates `engine/api/*.txt`). Then add an entry to `engine/CHANGELOG.md` classified as `Added` (minor) per `engine/COMPATIBILITY.md`'s rules, e.g.:

```markdown
## Unreleased
### Added
- `port.RunAwareToolCallRecorder`: an optional `ToolCallRecorder` extension
  that additionally receives the calling run's `RunID`, letting a consumer
  correlate a tool call to the run that made it. Purely additive — no
  existing `ToolCallRecorder` implementer is affected.
```

- [ ] **Step 8: Run `task api:check` and repo-wide `task lint`**

Run: `task api:check` (should now pass against the regenerated `engine/api/*.txt`) and `task lint` (0 issues expected).

- [ ] **Step 9: Commit**

```bash
git add engine/port/log.go engine/agent/dispatch.go engine/agent/run_aware_tool_call_recorder_test.go engine/CHANGELOG.md engine/api/
git commit -m "feat(engine): add the optional RunAwareToolCallRecorder port capability"
```

---

### Task 2: Reinstate `install.id`

**Files:**
- Modify: `internal/adapter/productmetrics/config.go` (restore `Config.InstallID`)
- Modify: `internal/adapter/productmetrics/provider.go` (restore the resource attribute)
- Modify: `internal/adapter/productmetrics/provider_test.go` (restore the test's `InstallID` field)
- Modify: `internal/cliconfig/productmetrics.go` (thread `installID` back into `Config`)
- Modify: `internal/adapter/productmetrics/installid.go` (doc comment: remove the "deliberately never threaded" language — it's threaded again now)
- Test: existing `installid_test.go` unaffected (the persistence mechanism itself never changed)

**Interfaces:**
- Produces: `Config.InstallID string` restored; `NewProvider`'s resource attributes include `"mecatl.install.id": cfg.InstallID` again.

- [ ] **Step 1: Restore `Config.InstallID`**

In `internal/adapter/productmetrics/config.go`, restore the field:

```go
// Config configures a Provider/Recorder pair for one process.
type Config struct {
	// Binary identifies which of the four entry points this process is.
	Binary Binary
	// Version is the mecatl build version (resource attribute service.version).
	Version string
	// InstallID is this process's persisted (or externally-provisioned, for
	// mecak8s — see Task 7) anonymous install identifier. Reinstated as a
	// resource attribute after being sized and accepted: ~$1,930/month at
	// 100K installs under worst-case 24/7 uptime on the actual AMP pricing
	// model (see the ADR's updated cost-analysis section, Task 8).
	InstallID string
}
```

- [ ] **Step 2: Write the failing test (provider_test.go)**

Restore the `InstallID` field to the existing `TestNewProviderExportsToConfiguredEndpoint` test's `Config{...}` literal:

```go
	p, err := NewProvider(context.Background(), Config{
		Binary:    BinaryMecated,
		Version:   "test",
		InstallID: "11111111-1111-1111-1111-111111111111",
	})
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go build ./...`
Expected: FAIL — `unknown field InstallID` (Config doesn't have it back yet if you did Step 2 before Step 1 — do Step 1 first; this ordering note exists so the two steps are both concrete, not because there is a real red-green gap here — `Config`'s field addition and the test asserting it stay in the SAME commit).

- [ ] **Step 4: Restore the resource attribute in `provider.go`**

In `internal/adapter/productmetrics/provider.go`, restore:

```go
	composite, err := providers.NewCompositeProvider(ctx,
		providers.WithServiceName("mecatl"),
		providers.WithServiceVersion(cfg.Version),
		providers.WithOTLPEndpoint(endpoint),
		providers.WithMetricsEnabled(true),
		providers.WithInsecure(strings.HasPrefix(endpoint, "http://")),
		providers.WithHeaders(map[string]string{headerKeyName: bakedKey}),
		providers.WithCustomAttributes(map[string]string{
			"mecatl.install.id": cfg.InstallID,
			"mecatl.binary":     string(cfg.Binary),
		}),
	)
```

removing the "Deliberately NOT included" doc comment above it (or rewriting it — see Step 5).

- [ ] **Step 5: Rewrite the doc comment explaining the reinstated decision**

Replace the comment block above the `NewCompositeProvider` call with:

```go
	// mecatl.install.id is a per-install random UUID, deliberately attached
	// as a resource attribute (so it flattens onto every instrument this
	// provider exports). This was removed once (see git history) over
	// unbounded-cardinality concerns on the Prometheus-remote-write
	// destination (stacklok/infra#5604), then reinstated after the actual
	// cost was sized against real AMP pricing and accepted — see the ADR's
	// cost-analysis section for the numbers. mecak8s provisions this value
	// differently (a stable per-Helm-release ConfigMap, not this package's
	// local install-id file — see internal/cliconfig's mecak8s wiring and
	// deploy/helm/mecak8s/templates/install-id-configmap.yaml), since a
	// pod-local file would mint a new id on every pod restart.
```

- [ ] **Step 6: Restore threading in `internal/cliconfig/productmetrics.go`**

Change:

```go
	// LoadOrCreateInstallIDDefault still runs (and persists its file) purely
	// to detect first-run for the disclosure notice below — the returned id
	// value itself is deliberately discarded, never threaded to NewProvider:
	// see provider.go's doc comment on why a per-install identifier must
	// never become a Prometheus-remote-write label.
	_, firstRun, err := productmetrics.LoadOrCreateInstallIDDefault()
	if err != nil {
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: install id: %w", err)
	}

	provider, err := productmetrics.NewProvider(ctx, productmetrics.Config{
		Binary:  binary,
		Version: version,
	})
```

to:

```go
	// LoadOrCreateInstallIDDefault persists (or reads back) this process's
	// local install-id file and reports firstRun for the disclosure notice
	// below. Reinstated as a real, exported resource attribute (see
	// provider.go's doc comment) after its cardinality cost was sized and
	// accepted.
	installID, firstRun, err := productmetrics.LoadOrCreateInstallIDDefault()
	if err != nil {
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: install id: %w", err)
	}

	provider, err := productmetrics.NewProvider(ctx, productmetrics.Config{
		Binary:    binary,
		Version:   version,
		InstallID: installID,
	})
```

Note: `BuildProductMetrics`'s signature does NOT change in this task — mecak8s's alternate provisioning (Task 7) overrides `installID` BEFORE calling `BuildProductMetrics` by having its OWN caller read the env-var-provided id and pass it through a new, distinct code path added in Task 7; this task only restores the DEFAULT (local-file) path all four binaries currently share.

- [ ] **Step 7: Update `installid.go`'s doc comment**

In `internal/adapter/productmetrics/installid.go`, remove the paragraph beginning "The returned id is deliberately never threaded into any exported metric attribute..." (added when `install.id` was removed) — replace with:

```go
// The returned id IS threaded into an exported resource attribute (see
// provider.go) — this package makes no attempt to keep the id local-only;
// that was a prior, now-reverted design (see git history / the ADR's
// cost-analysis section for why it was reinstated).
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd internal/adapter/productmetrics && go test ./... -race -v` and `cd ../../cliconfig && go test ./... -race -v`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/adapter/productmetrics/config.go internal/adapter/productmetrics/provider.go \
        internal/adapter/productmetrics/provider_test.go internal/adapter/productmetrics/installid.go \
        internal/cliconfig/productmetrics.go
git commit -m "feat(productmetrics): reinstate mecatl.install.id after sizing its cardinality cost"
```

---

### Task 3: Per-run tracking — `had_tool_call`, tool category + outcome

**Files:**
- Modify: `internal/adapter/productmetrics/metrics.go` (unify per-run tracking; add `had_tool_call` attribute; add the `attrCategory`/`attrOutcome` keys)
- Modify: `internal/adapter/productmetrics/toolcall.go` (implement `ToolCallForRun`, category/outcome derivation, per-run tallying)
- Modify: `internal/adapter/productmetrics/metrics_test.go`, `toolcall_test.go`
- Modify: `internal/adapter/productmetrics/bounded_test.go` (extend the allowlist + drive `ToolCallForRun` with sensitive markers)

**Interfaces:**
- Consumes: `port.RunAwareToolCallRecorder` (Task 1).
- Produces: `Recorder.ToolCallForRun(...)` (satisfies the new interface); `mecatl.product.runs_completed`'s new `had_tool_call` attribute; `mecatl.product.tool_calls`'s new `category`/`outcome` attributes.

- [ ] **Step 1: Write the failing tests**

```go
// In toolcall_test.go, alongside the existing TestRecorderToolCallCountsWithoutIdentity:

func TestRecorderToolCallForRunCategorizesBuiltinsByName(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: false}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{IsError: true}, 0, time.Millisecond)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumPoint(t, agg, "category", "Bash"); got != 1 {
		t.Errorf("tool_calls{category=Bash} = %d, want 1", got)
	}
	if got := sumPoint(t, agg, "category", "Read"); got != 1 {
		t.Errorf("tool_calls{category=Read} = %d, want 1", got)
	}
}

func TestRecorderToolCallForRunBucketsMCPToolsUnderOneCategory(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "mcp__github__list_issues"}, session.ToolResult{}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "mcp__slack__post_message"}, session.ToolResult{}, 0, time.Millisecond)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumPoint(t, agg, "category", "mcp"); got != 2 {
		t.Errorf("tool_calls{category=mcp} = %d, want 2 (both MCP-server tools bucketed together)", got)
	}
	// The real server/tool names must never appear as an attribute value.
	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			sum, ok := md.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				iter := dp.Attributes.Iter()
				for iter.Next() {
					kv := iter.Attribute()
					if kv.Value.AsString() == "github" || kv.Value.AsString() == "list_issues" {
						t.Fatalf("MCP server/tool name leaked as an attribute value: %s=%s", kv.Key, kv.Value.AsString())
					}
				}
			}
		}
	}
}

func TestRecorderToolCallForRunRecordsOutcome(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: false}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: true}, 0, time.Millisecond)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumPoint(t, agg, "outcome", "success"); got != 1 {
		t.Errorf("tool_calls{outcome=success} = %d, want 1", got)
	}
	if got := sumPoint(t, agg, "outcome", "error"); got != 1 {
		t.Errorf("tool_calls{outcome=error} = %d, want 1", got)
	}
}
```

```go
// In metrics_test.go, alongside the existing runs_completed tests:

func TestRecorderRunsCompletedHadToolCallTrueWhenASuccessfulToolCallOccurred(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{IsError: false}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-1",
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, "had_tool_call", "true"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=true} = %d, want 1", got)
	}
}

func TestRecorderRunsCompletedHadToolCallFalseWithNoToolCall(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-2",
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, "had_tool_call", "false"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=false} = %d, want 1", got)
	}
}

func TestRecorderRunsCompletedHadToolCallFalseWhenOnlyToolCallErrored(t *testing.T) {
	// A tool call that ERRORED does not count toward had_tool_call — the
	// product definition requires at least one SUCCESSFUL tool/action.
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-3", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: true}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-3",
		Result: &session.ResultPayload{Stop: session.StopError},
	})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, "had_tool_call", "false"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=false} = %d, want 1 (the only tool call errored)", got)
	}
}

func TestRecorderPerRunStateIsIsolatedAcrossConcurrentRuns(t *testing.T) {
	// Two runs interleaved (a real possibility: Team/Parallel fan-out, or
	// two concurrent client sessions on one process) must not leak state
	// into each other.
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-a", session.SessionID("s1"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-b", Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-a", Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, "had_tool_call", "false"); got != 1 {
		t.Errorf("run-b's had_tool_call = %d points at false, want exactly 1 (run-a's tool call must not leak into run-b)", got)
	}
	if got := sumPoint(t, agg, "had_tool_call", "true"); got != 1 {
		t.Errorf("run-a's had_tool_call = %d points at true, want exactly 1", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd internal/adapter/productmetrics && go build ./...`
Expected: FAIL — `ToolCallForRun` undefined, `had_tool_call`/`category`/`outcome` attributes don't exist yet.

- [ ] **Step 3: Unify per-run tracking in `metrics.go`**

Replace the existing `runFamiliesUsed`/`usedFamilies` per-run tracking (added in the prior plan's review-fix commit) with a single, richer per-run record covering everything this task and Task 4 need:

```go
// perRunState tracks, per LIVE run (keyed by session.Event.RunID / the same
// id ToolCallForRun receives), the bounded facts this package derives across
// the Emit/ToolCallForRun boundary. Cleared on EvResult so it stays bounded
// to concurrently-live runs, never growing across a process's lifetime.
type perRunState struct {
	subagentSeen  bool
	teamSeen      bool
	hadToolCall   bool
	toolCallCount int64
	startedAt     time.Time
}

// runs guards concurrent access to the live-run map — the SAME discipline
// the prior subagent/team dedup fix already established, now extended to
// cover had_tool_call/tool_calls_per_run/run_duration too.
type runs struct {
	mu    sync.Mutex
	byRun map[string]*perRunState
}

func newRuns() *runs { return &runs{byRun: make(map[string]*perRunState)} }

// get returns (creating if absent) the live perRunState for runID. An empty
// runID (no run context) returns a throwaway, never-shared state — matching
// the prior code's "always counts" fallback for the no-run-context case.
func (r *runs) get(runID string) *perRunState {
	if runID == "" {
		return &perRunState{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.byRun[runID]
	if !ok {
		st = &perRunState{}
		r.byRun[runID] = st
	}
	return st
}

// clear drops runID's live state at EvResult, returning the state that was
// there (or a zero-value one if none existed — e.g. a run with no tool
// calls and no delegation family use).
func (r *runs) clear(runID string) *perRunState {
	if runID == "" {
		return &perRunState{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.byRun[runID]
	if !ok {
		return &perRunState{}
	}
	delete(r.byRun, runID)
	return st
}
```

Replace the `Recorder` struct's `mu sync.Mutex` + `runFamiliesUsed map[string]usedFamilies` fields with a single `perRun *runs` field, and update `NewRecorder` to initialize it: `r.perRun = newRuns()`.

Update `firstInRun`/`clearRun` (the prior plan's helpers) to use `perRun.get(runID)`/`perRun.clear(runID)` instead of the old map directly — e.g.:

```go
func (r *Recorder) firstInRun(runID string, family delegationFamily) bool {
	st := r.perRun.get(runID)
	r.perRun.mu.Lock()
	defer r.perRun.mu.Unlock()
	switch family {
	case familySubagent:
		if st.subagentSeen {
			return false
		}
		st.subagentSeen = true
	case familyTeam:
		if st.teamSeen {
			return false
		}
		st.teamSeen = true
	}
	return true
}
```

(Adjust exact lock placement so `runs.get`'s own internal lock and this method's use of the returned pointer don't double-lock or race — the simplest correct shape is for ALL mutation of a `*perRunState`'s fields to happen while holding `r.perRun.mu`, so restructure `get`/`clear` to return the state WITHOUT unlocking around field access, or have every state-mutating method take the lock itself around both the map lookup AND the field mutation as one critical section. Get this right and prove it with `-race` in Step 8 — this is the one place in this task worth extra care.)

- [ ] **Step 4: Add the `had_tool_call` attribute to `recordResult`**

```go
const attrHadToolCall = "had_tool_call"

func (r *Recorder) recordResult(ctx context.Context, res *session.ResultPayload, runID string) {
	st := r.perRun.clear(runID)
	hadToolCall := "false"
	if st.hadToolCall {
		hadToolCall = "true"
	}
	if res == nil {
		r.runsCompleted.Add(ctx, 1, metric.WithAttributes(
			attribute.String(attrStop, string(session.StopNone)),
			attribute.String(attrHadToolCall, hadToolCall)))
		return
	}
	r.runsCompleted.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrStop, string(res.Stop)),
		attribute.String(attrHadToolCall, hadToolCall)))
	u := res.Usage
	// ... existing tokens.Add(...) calls unchanged ...
}
```

Update `Emit`'s `case session.EvResult:` arm to pass `ev.RunID`: `r.recordResult(ctx, ev.Result, ev.RunID)`.

- [ ] **Step 5: Implement `ToolCallForRun` in `toolcall.go`**

```go
package productmetrics

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	attrCategory = "category"
	attrOutcome  = "outcome"
)

// mcpToolPrefix is the STRUCTURAL naming convention every client MCP tool is
// registered under (internal/adapter/mcp/tool.go: `"mcp__" + server + "__" +
// toolName`) — checking this prefix, rather than maintaining a built-in-tool
// allowlist, means (a) a new built-in tool is automatically and correctly
// categorized by its own real name with no allowlist to keep in sync, and
// (b) an MCP server/tool name can never leak, structurally, regardless of
// what any future MCP integration is named.
const mcpToolPrefix = "mcp__"

// toolCategory derives the bounded category attribute for a tool call: the
// tool's own name for a built-in (never sensitive — mecatl's own fixed
// catalog), or the single literal "mcp" for anything MCP-server-provided
// (never the specific server/tool name).
func toolCategory(name string) string {
	if strings.HasPrefix(name, mcpToolPrefix) {
		return "mcp"
	}
	return name
}

// ToolCall satisfies port.ToolCallRecorder for a caller that does not
// implement/use the richer port.RunAwareToolCallRecorder path — it records
// with no run correlation (runID ""), matching this package's pre-existing,
// always-counts behavior for the no-run-context case.
func (r *Recorder) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	r.ToolCallForRun("", id, call, result, queued, took)
}

// ToolCallForRun satisfies port.RunAwareToolCallRecorder. It records the
// bounded category/outcome attributes and tallies the per-run state Task 4's
// run_duration and this task's had_tool_call/tool_calls_per_run all read at
// EvResult time. It never reads call.Name/result.Content beyond the bounded
// category derivation above — no free text, no session id, no MCP
// server/tool name.
func (r *Recorder) ToolCallForRun(runID string, _ session.SessionID, call session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	ctx := context.Background()
	outcome := "success"
	if result.IsError {
		outcome = "error"
	}
	r.toolCalls.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrCategory, toolCategory(call.Name)),
		attribute.String(attrOutcome, outcome)))

	st := r.perRun.get(runID)
	r.perRun.mu.Lock()
	st.toolCallCount++
	if !result.IsError {
		st.hadToolCall = true
	}
	r.perRun.mu.Unlock()
}

// Compile-time interface checks.
var (
	_ port.ToolCallRecorder        = (*Recorder)(nil)
	_ port.RunAwareToolCallRecorder = (*Recorder)(nil)
)
```

(Remove the OLD `ToolCall` implementation this replaces — the prior plan's version that ignored every parameter and just bumped `r.toolCalls.Add(ctx, 1)` with no attributes.)

- [ ] **Step 6: Update `metrics.go`'s `tool_calls` instrument description**

The `mecatl.adoption.tool_calls`/`mecatl.product.tool_calls` counter's `metric.WithDescription(...)` string currently says "no tool identity attached" — update it:

```go
	if r.toolCalls, err = meter.Int64Counter("mecatl.product.tool_calls",
		metric.WithDescription("Total tool calls executed, by bounded category (a built-in tool's own name, or the single value \"mcp\" for any MCP-server tool) and outcome.")); err != nil {
```

- [ ] **Step 7: Extend `bounded_test.go`**

Add `attrCategory`, `attrOutcome`, `attrHadToolCall` to `allowedAttributeKeys`. Extend the existing `TestRecorderNeverAttachesUnboundedAttributesOrSensitiveContent` to ALSO drive `ToolCallForRun("run-x", ..., session.ToolCall{Name: "mcp__evilserver__leak_this_name"}, ...)` and assert `"evilserver"`/`"leak_this_name"` never appear as an attribute value anywhere in the collected output — this is the guard test's whole job, extend it rather than adding a separate one.

- [ ] **Step 8: Run tests with `-race` to verify correctness and no data races**

Run: `cd internal/adapter/productmetrics && go test ./... -race -v`
Expected: PASS, all tests including the new ones, `-race` clean (this task adds concurrent map + struct-field access — `-race` is not optional here).

- [ ] **Step 9: Commit**

```bash
git add internal/adapter/productmetrics/metrics.go internal/adapter/productmetrics/toolcall.go \
        internal/adapter/productmetrics/metrics_test.go internal/adapter/productmetrics/toolcall_test.go \
        internal/adapter/productmetrics/bounded_test.go
git commit -m "feat(productmetrics): had_tool_call, tool category/outcome via RunAwareToolCallRecorder"
```

---

### Task 4: `run_duration` histogram

**Files:**
- Modify: `internal/adapter/productmetrics/metrics.go`
- Test: `internal/adapter/productmetrics/metrics_test.go`

**Interfaces:**
- Consumes: `perRunState.startedAt` (Task 3).
- Produces: `mecatl.product.run_duration` histogram (seconds).

- [ ] **Step 1: Write the failing test**

```go
func TestRecorderRunDurationRecordedFromSessionInitToResult(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit, RunID: "run-1"})
	time.Sleep(5 * time.Millisecond)
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-1", Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	agg, ok := collect(t, reader)["mecatl.product.run_duration"]
	if !ok {
		t.Fatal("mecatl.product.run_duration missing")
	}
	hist, ok := agg.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("aggregation is %T, want Histogram[float64]", agg)
	}
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Count != 1 {
		t.Fatalf("expected exactly 1 recorded duration, got %+v", hist.DataPoints)
	}
	if hist.DataPoints[0].Sum <= 0 {
		t.Errorf("recorded duration sum = %v, want > 0", hist.DataPoints[0].Sum)
	}
}

func TestRecorderRunDurationNotRecordedWithoutMatchingSessionInit(t *testing.T) {
	// A run whose EvSessionInit this Recorder never observed (e.g. process
	// restarted mid-run — an edge case, not a common path) must not record a
	// bogus/negative duration.
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "orphan-run", Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	if agg, ok := collect(t, reader)["mecatl.product.run_duration"]; ok {
		if hist, ok := agg.(metricdata.Histogram[float64]); ok && len(hist.DataPoints) > 0 && hist.DataPoints[0].Count > 0 {
			t.Errorf("recorded a duration for a run with no observed EvSessionInit: %+v", hist.DataPoints)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderRunDuration -v`
Expected: FAIL — instrument doesn't exist yet.

- [ ] **Step 3: Add the instrument and start/stop bracketing**

In `NewRecorder`, add:

```go
	if r.runDuration, err = meter.Float64Histogram("mecatl.product.run_duration",
		metric.WithDescription("Wall-clock duration of a run, from session init to result, in seconds."),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("productmetrics: run_duration histogram: %w", err)
	}
```

Add `runDuration metric.Float64Histogram` to the `Recorder` struct.

In `Emit`'s `case session.EvSessionInit:` arm, stamp the start time:

```go
	case session.EvSessionInit:
		r.sessionsStarted.Add(ctx, 1)
		if ev.RunID != "" {
			st := r.perRun.get(ev.RunID)
			r.perRun.mu.Lock()
			st.startedAt = time.Now()
			r.perRun.mu.Unlock()
		}
```

In `recordResult` (or right after `st := r.perRun.clear(runID)` in `Emit`'s `EvResult` handling), record the duration only when `startedAt` was actually observed:

```go
	if !st.startedAt.IsZero() {
		r.runDuration.Record(ctx, time.Since(st.startedAt).Seconds())
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderRunDuration -v`
Expected: PASS

- [ ] **Step 5: Run the full package suite with `-race`**

Run: `cd internal/adapter/productmetrics && go test ./... -race -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/productmetrics/metrics.go internal/adapter/productmetrics/metrics_test.go
git commit -m "feat(productmetrics): run_duration histogram"
```

---

### Task 5: `tool_calls_per_run` histogram + `time_to_first_value`

**Files:**
- Modify: `internal/adapter/productmetrics/metrics.go` (the `tool_calls_per_run` histogram, recorded at `EvResult` from `perRunState.toolCallCount`)
- Create: `internal/adapter/productmetrics/firstvalue.go` (the one-time marker + histogram)
- Test: `internal/adapter/productmetrics/firstvalue_test.go`
- Modify: `internal/cliconfig/productmetrics.go` (thread the first-value check into `BuildProductMetrics`)

**Interfaces:**
- Produces: `mecatl.product.tool_calls_per_run` histogram; `mecatl.product.time_to_first_value` histogram (recorded at most once per install); `func LoadOrCreateFirstValueMarker(...) (alreadyRecorded bool, err error)` mirroring `installid.go`'s shape.

- [ ] **Step 1: `tool_calls_per_run` — write the failing test**

```go
func TestRecorderToolCallsPerRunRecordedAtResult(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-1", Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	agg := collect(t, reader)["mecatl.product.tool_calls_per_run"]
	hist, ok := agg.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("aggregation is %T, want Histogram[int64]", agg)
	}
	if len(hist.DataPoints) != 1 || hist.DataPoints[0].Sum != 2 {
		t.Fatalf("expected one data point summing to 2, got %+v", hist.DataPoints)
	}
}
```

- [ ] **Step 2: Run test to verify it fails, then implement**

Add to `NewRecorder`:

```go
	if r.toolCallsPerRun, err = meter.Int64Histogram("mecatl.product.tool_calls_per_run",
		metric.WithDescription("Total tool calls made within a single run.")); err != nil {
		return nil, fmt.Errorf("productmetrics: tool_calls_per_run histogram: %w", err)
	}
```

In `recordResult`, alongside the existing `st := r.perRun.clear(runID)`:

```go
	r.toolCallsPerRun.Record(ctx, st.toolCallCount)
```

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderToolCallsPerRun -v` — expect PASS after the change.

- [ ] **Step 3: `time_to_first_value` — write the failing test**

```go
package productmetrics

import (
	"errors"
	"os"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestLoadOrCreateFirstValueMarkerFirstTimeReportsNotYetRecorded(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/tester", nil },
	}
	written := map[string][]byte{}
	readFile := func(p string) ([]byte, error) {
		if d, ok := written[p]; ok {
			return d, nil
		}
		return nil, os.ErrNotExist
	}
	writeFile := func(p string, d []byte, _ os.FileMode) error { written[p] = d; return nil }
	mkdirAll := func(string, os.FileMode) error { return nil }

	already, err := LoadOrCreateFirstValueMarker(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("LoadOrCreateFirstValueMarker: %v", err)
	}
	if already {
		t.Error("already = true on first call, want false")
	}

	// Second call must report it as already recorded, and not error.
	already2, err := LoadOrCreateFirstValueMarker(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("second LoadOrCreateFirstValueMarker: %v", err)
	}
	if !already2 {
		t.Error("already = false on second call, want true")
	}
}

func TestLoadOrCreateFirstValueMarkerFailsClosedWithNoStateDir(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
	}
	if _, err := LoadOrCreateFirstValueMarker(env, nil, nil, nil); err == nil {
		t.Fatal("expected an error when no state dir can be resolved, got nil")
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestLoadOrCreateFirstValueMarker -v`
Expected: FAIL — `LoadOrCreateFirstValueMarker` undefined.

- [ ] **Step 5: Write `firstvalue.go`**

```go
package productmetrics

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// firstValueMarkerRelPath is the state-dir-relative path to a bare marker
// file recording whether this install's first meaningful-and-successful run
// has already been observed — so mecatl.product.time_to_first_value is
// recorded at most once per install, ever, mirroring installid.go's
// first-run marker pattern exactly.
const firstValueMarkerRelPath = "mecatl/first-value-recorded"

// LoadOrCreateFirstValueMarker reports whether this install's first-value
// moment was already recorded (already == true), creating the marker (and
// returning already == false) the first time it is called. Once created, it
// is never removed automatically — deleting it (like the install-id file)
// resets the install and lets time_to_first_value fire once more.
func LoadOrCreateFirstValueMarker(
	env xdgconfig.ResolveEnv,
	readFile func(string) ([]byte, error),
	writeFile func(string, []byte, os.FileMode) error,
	mkdirAll func(string, os.FileMode) error,
) (already bool, err error) {
	base := xdgconfig.UserStateDir(env)
	if base == "" {
		return false, fmt.Errorf("productmetrics: cannot resolve a state directory (no XDG_STATE_HOME and no home dir)")
	}
	path := filepath.Join(base, firstValueMarkerRelPath)

	if readFile != nil {
		if _, rerr := readFile(path); rerr == nil {
			return true, nil
		}
	}
	if mkdirAll != nil {
		if merr := mkdirAll(filepath.Dir(path), 0o700); merr != nil {
			return false, fmt.Errorf("productmetrics: create state dir: %w", merr)
		}
	}
	if writeFile != nil {
		if werr := writeFile(path, []byte("1"), 0o600); werr != nil {
			return false, fmt.Errorf("productmetrics: write first-value marker: %w", werr)
		}
	}
	return false, nil
}

// LoadOrCreateFirstValueMarkerDefault binds LoadOrCreateFirstValueMarker to
// the real process environment and filesystem.
func LoadOrCreateFirstValueMarkerDefault() (already bool, err error) {
	return LoadOrCreateFirstValueMarker(xdgconfig.OSEnv, os.ReadFile, os.WriteFile, os.MkdirAll)
}
```

- [ ] **Step 6: Add the `time_to_first_value` instrument and recording logic**

Add to `NewRecorder`:

```go
	if r.timeToFirstValue, err = meter.Float64Histogram("mecatl.product.time_to_first_value",
		metric.WithDescription("One-time-per-install duration from this install's first-seen moment to its first had_tool_call=true, stop=success run."),
		metric.WithUnit("s")); err != nil {
		return nil, fmt.Errorf("productmetrics: time_to_first_value histogram: %w", err)
	}
```

Add a field to `Recorder`: `firstSeenAt time.Time` (set once, at construction) and `firstValueRecorded *atomic.Bool` (or guard via the marker file check, done ONCE at `BuildProductMetrics` construction time rather than per-event — see Step 7, this is simpler than trying to gate it per-Emit-call).

Actually, the simplest correct design: do the "has this already been recorded" check ONCE, in `internal/cliconfig.BuildProductMetrics` (Step 7 below), NOT inside `Recorder` itself — pass a plain `bool` (`trackFirstValue`) into `NewRecorder`/`Config`, and have `Recorder.recordResult` check `res.Stop == session.StopEndTurn && st.hadToolCall && !r.firstValueAlreadyRecorded` before recording once and flipping an in-memory flag (`sync.Once` or a guarded bool) — the FILE write (marking it recorded forever) happens in the CALLER once, when `BuildProductMetrics` first observes `already == false` at startup... but that's wrong too, since the marker needs to be written the MOMENT the qualifying run actually happens, not at process startup (a process might never have a qualifying run). Correct shape: `Recorder` itself owns a `sync.Once`-guarded write-through: on the FIRST qualifying `EvResult`, it (a) computes and records the duration, (b) calls a caller-injected `markFirstValueRecorded func() error` closure (wrapping `os.WriteFile` at the real path) exactly once. Thread this closure into `NewRecorder` (or a new `NewRecorderWithFirstValue(mp, alreadyRecorded bool, markRecorded func())` variant) rather than `Config`, to keep `NewRecorder`'s existing signature stable for the (many) existing call sites/tests that don't care about this feature.

Given the added complexity, the pragmatic shape:

```go
// Recorder field additions:
	firstSeenAt         time.Time
	firstValueDone      bool // true if already recorded (this run OR a prior one)
	firstValueRecordFn  func() error // writes the local marker file; nil disables recording entirely
	firstValueMu        sync.Mutex
```

`NewRecorder`'s signature stays unchanged (existing callers/tests untouched); add a new setter-style method used only by `BuildProductMetrics`:

```go
// EnableFirstValueTracking arms mecatl.product.time_to_first_value tracking:
// firstSeenAt is this install's first-seen timestamp (from the SAME local
// install-id file's mtime, or "now" if unavailable — an approximation is
// fine, this metric's whole purpose is a coarse "how long did onboarding
// take" signal, not a billing-grade timer). alreadyRecorded, when true,
// permanently disables further recording for this Recorder's lifetime (this
// install already has its one sample). recordFn persists the marker so a
// LATER process invocation also stays disabled; it is called at most once.
func (r *Recorder) EnableFirstValueTracking(firstSeenAt time.Time, alreadyRecorded bool, recordFn func() error) {
	r.firstValueMu.Lock()
	defer r.firstValueMu.Unlock()
	r.firstSeenAt = firstSeenAt
	r.firstValueDone = alreadyRecorded
	r.firstValueRecordFn = recordFn
}
```

In `recordResult`, after the existing token/had_tool_call recording:

```go
	if res != nil && res.Stop == session.StopEndTurn && st.hadToolCall {
		r.firstValueMu.Lock()
		if !r.firstValueDone && !r.firstSeenAt.IsZero() {
			r.firstValueDone = true
			r.timeToFirstValue.Record(ctx, time.Since(r.firstSeenAt).Seconds())
			if r.firstValueRecordFn != nil {
				_ = r.firstValueRecordFn() // best-effort; a failed write just risks re-recording once on a later process, not a correctness bug
			}
		}
		r.firstValueMu.Unlock()
	}
```

- [ ] **Step 7: Wire this into `BuildProductMetrics`**

In `internal/cliconfig/productmetrics.go`, after constructing `recorder` and before returning:

```go
	firstValueAlready, fvErr := productmetrics.LoadOrCreateFirstValueMarkerDefault()
	// A failure here degrades to "track it anyway" (fvErr != nil implies
	// firstValueAlready's zero value false) rather than disabling the whole
	// pipeline — time_to_first_value is a nice-to-have signal, not
	// load-bearing enough to fail product metrics setup entirely over.
	firstSeenAt := time.Now()
	if info, statErr := os.Stat(installIDFilePath(...)); statErr == nil { // see note below
		firstSeenAt = info.ModTime()
	}
	recorder.EnableFirstValueTracking(firstSeenAt, fvErr == nil && firstValueAlready, func() error {
		_, _, err := productmetrics.LoadOrCreateFirstValueMarkerDefault()
		return err
	})
```

Note to implementer: `installIDFilePath(...)` is illustrative — `installid.go`'s `installIDRelPath` const plus `xdgconfig.UserStateDir` is how the REAL install-id file's path is computed; either export a small helper from `productmetrics` that returns this path (cleanest), or accept the simpler approximation of always using `time.Now()` as `firstSeenAt` when the install-id file's actual mtime isn't easily available at this call site — the metric's own doc comment already says an approximation is acceptable. Use your judgment on which is cleaner; either is acceptable, but document whichever you pick in the instrument's description string if it differs from what Step 6 already says.

- [ ] **Step 8: Run the full package suite**

Run: `cd internal/adapter/productmetrics && go test ./... -race -v` and `cd ../../cliconfig && go test ./... -race -v`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/adapter/productmetrics/metrics.go internal/adapter/productmetrics/firstvalue.go \
        internal/adapter/productmetrics/firstvalue_test.go internal/cliconfig/productmetrics.go
git commit -m "feat(productmetrics): tool_calls_per_run + time_to_first_value"
```

---

### Task 6: Update `DryRunRecorder` to match

**Files:**
- Modify: `internal/adapter/productmetrics/dryrun.go`
- Modify: `internal/adapter/productmetrics/dryrun_test.go`

**Interfaces:**
- Produces: `DryRunRecorder` now also implements `port.RunAwareToolCallRecorder`, and logs the same new bounded fields (`had_tool_call`, `category`, `outcome`) the real `Recorder` would have recorded — the dry-run's whole purpose is showing EXACTLY what the real pipeline would send, so it must track every new field the real one does.

- [ ] **Step 1: Write the failing test**

```go
func TestDryRunRecorderLogsCategoryOutcomeAndHadToolCall(t *testing.T) {
	diag := &capturingDiag{}
	r := NewDryRunRecorder(diag)

	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "mcp__someserver__sensitive_tool"}, session.ToolResult{IsError: true}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-1", Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	found := false
	for _, args := range diag.allArgs() { // see note: capturingDiag needs a small extension to expose recorded args, not just messages, for this assertion — extend it minimally
		for _, a := range args {
			if s, ok := a.(string); ok && (s == "someserver" || s == "sensitive_tool") {
				t.Fatalf("MCP server/tool name leaked in dry-run output: %q", s)
			}
		}
	}
	_ = found
}
```

Note to implementer: the existing `capturingDiag` fake (in `dryrun_test.go`) currently only captures `msg string`, not the `args ...any` — extend it minimally to also store `args` per call, since this test (and the privacy discipline this file exists to prove) needs to inspect them.

- [ ] **Step 2: Run test to verify it fails, then implement**

In `dryrun.go`, implement `ToolCallForRun` mirroring the real `Recorder`'s logic (category/outcome derivation), and update `ToolCall` to delegate to it with `runID=""`, same shape as Task 3's real `Recorder`:

```go
func (d *DryRunRecorder) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	d.ToolCallForRun("", id, call, result, queued, took)
}

func (d *DryRunRecorder) ToolCallForRun(runID string, _ session.SessionID, call session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	outcome := "success"
	if result.IsError {
		outcome = "error"
	}
	d.diag.Log(context.Background(), port.LevelInfo, "product metrics (dry-run): would record tool_calls+1",
		"category", toolCategory(call.Name), "outcome", outcome)
}

var _ port.RunAwareToolCallRecorder = (*DryRunRecorder)(nil)
```

Update `Emit`'s `EvResult` case to also log `had_tool_call` (reuse whatever per-run tracking is simplest for the dry-run path — a lighter-weight version than the real `Recorder`'s is fine here, e.g. its own small `runs *runs`-shaped field, or simply omitting exact had_tool_call tracking in dry-run and logging `"had_tool_call", "unknown (dry-run does not track per-run state)"` if that's meaningfully simpler — use your judgment, document whichever you choose).

- [ ] **Step 3: Run tests**

Run: `cd internal/adapter/productmetrics && go test ./... -race -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/adapter/productmetrics/dryrun.go internal/adapter/productmetrics/dryrun_test.go
git commit -m "feat(productmetrics): DryRunRecorder mirrors the new category/outcome/had_tool_call fields"
```

---

### Task 7: mecak8s install-id ConfigMap

**Files:**
- Create: `deploy/helm/mecak8s/templates/install-id-configmap.yaml`
- Modify: `deploy/helm/mecak8s/templates/deployment.yaml` (mount the id as an env var)
- Modify: `deploy/helm/mecak8s/chart_test.go` (or wherever this chart's existing tests live — extend, don't invent a new test file if one already renders/asserts this chart's templates)
- Modify: `cmd/mecak8s/observability.go` (read the env var instead of calling the local-file mechanism, when set)

**Interfaces:**
- Produces: a `ConfigMap` named e.g. `{{ include "mecak8s.fullname" . }}-install-id` holding one key (`installId`), generated once via the `lookup`-based idiom and reused across `helm upgrade`; an env var `MECATL_PRODUCT_METRICS_INSTALL_ID` sourced from it, mounted into the `mecak8s` container.

- [ ] **Step 1: Write the ConfigMap template**

```yaml
# deploy/helm/mecak8s/templates/install-id-configmap.yaml
#
# Generates ONE stable install-id for this Helm release, reused across every
# replica and every `helm upgrade` — unlike a per-pod local file (which mecak8s
# cannot use at all: it runs storage-free, no PVC, per ADR 0048, and every pod
# restart would otherwise mint a fresh, never-reused id — the worst-case
# cardinality pattern for the product-metrics pipeline this feeds). The
# `lookup` guard is the standard Helm idiom for "generate once, keep stable on
# upgrade": if a ConfigMap of this name already exists in this release's
# namespace, its EXISTING value is reused verbatim; only a genuinely first
# `helm install` (or a deliberately deleted ConfigMap) mints a new one.
{{- $existing := lookup "v1" "ConfigMap" .Release.Namespace (printf "%s-install-id" (include "mecak8s.fullname" .)) }}
{{- $installID := "" }}
{{- if $existing }}
{{- $installID = index $existing.data "installId" }}
{{- else }}
{{- $installID = uuidv4 }}
{{- end }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "mecak8s.fullname" . }}-install-id
  labels:
    {{- include "mecak8s.labels" . | nindent 4 }}
data:
  installId: {{ $installID | quote }}
```

(`mecak8s.fullname`/`mecak8s.labels` are illustrative — before writing this, check `deploy/helm/mecak8s/templates/_helpers.tpl` for the chart's REAL helper template names and use those, not invented ones.)

- [ ] **Step 2: Mount it as an env var in `deployment.yaml`**

In `deploy/helm/mecak8s/templates/deployment.yaml`, add to the container's `env:` list (find the existing `env:` block — confirmed at line 201 in the prior investigation):

```yaml
            - name: MECATL_PRODUCT_METRICS_INSTALL_ID
              valueFrom:
                configMapKeyRef:
                  name: {{ include "mecak8s.fullname" . }}-install-id
                  key: installId
```

- [ ] **Step 3: Read the env var in `cmd/mecak8s/observability.go`**

Before the existing call to `cliconfig.BuildProductMetrics(...)`, add:

```go
	// mecak8s cannot use the local-file install-id mechanism the other three
	// binaries share (storage-free, no PVC, per ADR 0048 — every pod restart
	// would mint a fresh id, the worst-case cardinality pattern). Instead, a
	// stable per-Helm-release id is provisioned via a ConfigMap (see
	// deploy/helm/mecak8s/templates/install-id-configmap.yaml) and threaded
	// through this env var. Empty (no chart-provisioned id, e.g. running the
	// binary directly outside the chart) falls back to whatever
	// BuildProductMetrics's own default local-file mechanism produces —
	// which will still work, just without the "one stable id per k8s
	// deployment" guarantee the chart provides.
	installIDOverride := os.Getenv("MECATL_PRODUCT_METRICS_INSTALL_ID")
```

Thread `installIDOverride` into `cliconfig.BuildProductMetrics`'s call — this requires a SMALL signature addition to `BuildProductMetrics` (an optional `installIDOverride string` parameter, empty meaning "use the default local-file mechanism"): when non-empty, `BuildProductMetrics` skips `LoadOrCreateInstallIDDefault()` entirely and uses the override value directly as `Config.InstallID`. Update the OTHER three binaries' call sites to pass `""` (unaffected, unchanged behavior).

- [ ] **Step 4: Update/extend the chart's existing test**

Find the existing Helm chart test (`deploy/helm/mecak8s/chart_test.go`, confirmed to exist by the earlier `grep -rln readOnlyRootFilesystem` search) and add a case asserting the new `ConfigMap` template renders with a valid UUID in `data.installId`, and that the `Deployment` template's env var correctly references it via `configMapKeyRef`.

- [ ] **Step 5: Run the chart tests and the `cmd/mecak8s`/`cliconfig` Go tests**

Run: `cd deploy/helm/mecak8s && go test ./... -v` (if this is how chart_test.go is invoked — check its actual invocation mechanism, e.g. it may use the `helm` binary via `os/exec` or a Go Helm-templating library; follow whatever the EXISTING tests in this file already do) and `cd /Users/reyniero/work/mecatl/.claude/worktrees/product-metrics-otel/cmd/mecak8s && go test ./... -race -v` and `cd ../../internal/cliconfig && go test ./... -race -v`.
Expected: PASS

- [ ] **Step 6: Run `task k8s:e2e` or equivalent if this repo has one (check Taskfile.yml for a k8s-specific e2e task) as an extra confidence check, given this touches the real Helm chart**

If such a task exists, run it; if it requires a live kind cluster and is out of scope for a quick local check, note that in your report and rely on the chart_test.go coverage instead.

- [ ] **Step 7: Commit**

```bash
git add deploy/helm/mecak8s/templates/install-id-configmap.yaml deploy/helm/mecak8s/templates/deployment.yaml \
        deploy/helm/mecak8s/chart_test.go cmd/mecak8s/observability.go internal/cliconfig/productmetrics.go
git commit -m "feat(mecak8s): provision a stable per-release install-id via a Helm ConfigMap"
```

---

### Task 8: ADR + user-docs + PR description updates, final verification

**Files:**
- Modify: `docs/adr/0319-product-metrics.md`
- Modify: `user-docs/building/what-you-get/observability.md`
- Modify: the open PR's description (via `gh pr edit`)

**Interfaces:** none — documentation only, plus final verification.

- [ ] **Step 1: Update the ADR's catalog table**

Add rows for `mecatl.product.run_duration`, `mecatl.product.tool_calls_per_run`, `mecatl.product.time_to_first_value`; update `runs_completed`'s row to show its new `had_tool_call` attribute; update `tool_calls`'s row to show its new `category`/`outcome` attributes (removing the "no tool/MCP-server name label at all" claim, replacing it with an accurate description of the bounded category scheme).

- [ ] **Step 2: Add a new ADR section documenting the reinstatement**

Add a section (e.g. "## Reinstating `mecatl.install.id`, and the mecak8s ConfigMap") recording: why it was reinstated (sized, accepted cost — cite the actual $/month figures from this conversation), the `RunAwareToolCallRecorder` engine addition and why it's additive/non-breaking, and the mecak8s-specific ConfigMap mechanism and why the local-file approach cannot work there (storage-free, ADR 0048, pod churn).

- [ ] **Step 3: Update `user-docs/building/what-you-get/observability.md`**

Update the "What's collected" paragraph to include the new fields (tool category/outcome, had_tool_call, run duration, tool-calls-per-run, time-to-first-value) and to accurately state that an anonymous per-install identifier is now collected (reversing the prior "no ... identifier" framing) — be precise and honest here, this is the user-facing disclosure text's source of truth.

- [ ] **Step 4: Update `internal/cliconfig/productmetrics.go`'s `ProductMetricsDisclosureNotice` string**

This is the actual STARTUP disclosure text users see — it must also honestly reflect that a per-install identifier is now collected. Update its wording accordingly.

- [ ] **Step 5: Run every gate**

```bash
task lint
task test
task build
task docs
task site:build
```
All must pass clean.

- [ ] **Step 6: Update the PR description**

Fetch the current PR body (`gh pr view <PR#> --json body -q .body`), update the "Metrics catalog" section to reflect the new/changed instruments, add a short "Reinstating install.id" note explaining the reversal and its rationale (cost sizing, mecak8s ConfigMap mechanism), and push via `gh pr edit`.

- [ ] **Step 7: Commit the doc changes**

```bash
git add docs/adr/0319-product-metrics.md user-docs/building/what-you-get/observability.md internal/cliconfig/productmetrics.go
git commit -m "docs: document had_tool_call, tool category/outcome, and the install.id reinstatement"
git push
```
