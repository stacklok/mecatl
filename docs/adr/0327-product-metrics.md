# ADR 0327 — Product (adoption) metrics over OTLP

- Status: Accepted
- Date: 2026-09-09
- Scope: `internal/adapter/productmetrics` (new), `internal/cliconfig`, `internal/adapter/permconfig` (new `telemetry:` operator section), `engine/port` (new `RunAwareToolCallRecorder`), `engine/agent` (dispatch wiring), `cmd/mecated`, `cmd/mecatui`, `cmd/mecatequi`, `cmd/mecak8s`, `deploy/helm/mecak8s`
- Supersedes: —
- Superseded by: —

## Context

mecatl had no visibility into community adoption: no install counts, no
feature-adoption signal, no aggregate usage depth. Stacklok's infra team
stood up a dedicated, internet-facing OTLP/HTTP metrics ingest at
`https://metrics.stacklok.com/v1/metrics` specifically for mecatl binaries
running on infrastructure Stacklok does not control (`stacklok/infra#5604`):
API-key-gated at the edge (`x-mecatl-metrics-key` header, stripped before the
collector) and server-side filtered to accept only metric names matching
`^mecatl\..*`.

This is a genuinely different concern from mecatl's existing operator-facing
observability (ADR 0018/0045/0098, and the three-channel taxonomy in
ADR 0020: `port.Diagnostics`, `port.ToolCallRecorder`, `port.EventSink`→
`internal/adapter/telemetry`). That pipeline exists so an *operator* can
observe *their own* deployment, pointed at *their own* collector. Product
metrics is Stacklok observing aggregate, anonymous community adoption across
every install — a different audience, a different destination, and a
different consent model. Reusing any part of the existing three-channel
pipeline as a transport for this data would be a category error: an
operator's own `--otlp-endpoint` must have zero effect on what does or
doesn't reach Stacklok, and enabling/disabling product metrics must have
zero effect on what the operator's own collector receives. The two cannot
be allowed to share a `MeterProvider`, a struct, or a destination, or that
guarantee becomes an implementation accident instead of a structural fact.

`toolhive-core` (already an mecatl dependency) ships `telemetry/providers`: a
small, already-reviewed OTel SDK-wiring layer (`providers.NewCompositeProvider`)
that builds a `metric.MeterProvider` from an options struct (endpoint,
headers, service name/version, custom resource attributes) without ever
installing it as the process-global provider — the natural building block
for this pipeline's OTLP/HTTP exporter, so mecatl does not hand-roll a third
OTLP wiring implementation next to the two it already has
(`internal/adapter/telemetry/otlp.go`'s inline construction, and this one).

## Decision

### A fully independent adapter: `internal/adapter/productmetrics`

Zero import relationship with `internal/adapter/telemetry`. It owns:

- Its own `metric.MeterProvider` (`Provider`, wrapping
  `toolhive-core/telemetry/providers.CompositeProvider`), built against a
  **hardcoded** endpoint (`https://metrics.stacklok.com/v1/metrics`) and a
  **hardcoded** header key baked into the binary at build time via
  `-X …productmetrics.bakedKey=…` (`Taskfile.yml`'s `BUILD_LDFLAGS`) — neither
  is operator-configurable, and there is exactly one place this data can go.
  An empty baked key (every local/dev/CI-test build) makes `NewProvider`
  refuse to construct at all: a non-release build can never accidentally
  phone home.
- Its own `Recorder`, implementing `port.EventSink` (`Emit`) and
  `port.ToolCallRecorder` (`ToolCall`) — the same two seams
  `internal/adapter/telemetry` taps, but a type whose public API cannot
  accept a bare free-text `string` anywhere (see "Privacy guard" below).
- Its own heartbeat ticker (`RunHeartbeat`): fires once immediately, then
  every `DefaultHeartbeatInterval` (24h) for long-running processes
  (`mecated`, `mecatui`, `mecak8s`); a single fire with no ticker
  (`interval<=0`) plus flush-before-exit for the short-lived `mecatequi`,
  mirroring the OTLP-push-with-flush precedent ADR 0098 already established
  for that binary's shape.
- Its own local install-identity file (`installid.go`) — a first-run marker
  whose UUID value is now also attached as the `mecatl.install.id` resource
  attribute (see "A per-install identifier IS attached" below).

Composition combines the two independent sinks with a trivial fan-out helper
in `internal/cliconfig` (`BuildProductMetrics`, `TeeToolCallRecorder`) — the
existing Rule-of-Three home for cross-binary telemetry wiring per ADR 0098.
`internal/app` stays import-free of `productmetrics`, exactly as it is of
`telemetry` today. Each `cmd/*/main.go` builds its existing operator
telemetry pipeline unchanged, and — only when product metrics are enabled —
separately constructs a `productmetrics.Recorder` (or, under
`--product-metrics-dry-run`, a `DryRunRecorder`) and tees it in alongside via
`TeeToolCallRecorder` / `internal/adapter/telemetry.NewSink`'s existing
`EventSink` fan-out.

An operator who disables their own OTLP export still has product metrics
flow (if enabled) to Stacklok; an operator who fully disables product
metrics has zero effect on their own OTLP/Prometheus pipeline. The two
cannot leak into each other because they share no struct, provider,
registry, or destination — only the same two read-only observation points
every consumer of those ports already receives independently, per
composition's existing fan-out discipline.

### The shipped `mecatl.product.*` catalog

All instrument names are namespaced under `mecatl.product.*` — passes the
collector's `^mecatl\..*` filter, and is visually/query-wise distinct from
the operator-facing `mecatl.tool.*`/`mecatl.runs`/etc. family, so nobody
looking at either series family can mistake one for the other.

**Resource attributes** (set once per process via `productmetrics.Config`,
not per-metric labels): `service.name=mecatl`, `service.version`,
`mecatl.install.id` (a per-install random UUID — see below),
`mecatl.binary` (one of the closed `Binary` set:
`mecated`/`mecatui`/`mecatequi`/`mecak8s`).

**A per-install identifier IS attached, after being sized and accepted.**
This pipeline's destination is a Prometheus-remote-write backend
(`stacklok/infra#5604`), where every resource attribute becomes a permanent
label on *every* instrument's time series. A random per-install UUID was
initially left out over exactly this concern — it multiplies active series
by (installs × instrument count), an unbounded-cardinality cost that grows
with adoption. It was reinstated after sizing that cost against the actual
backend (Amazon Managed Service for Prometheus, per `stacklok/infra#5604`):
roughly $1,930/month at 100,000 installs under a worst-case assumption (every
instrument in this catalog, 24/7 uptime), and roughly $650/month under a
more realistic assumption (an interactive CLI tool, ~8h/day active use) —
both well within what the adoption signal an exact per-install breakdown
enables (activation rate, time-to-first-value, weekly retention) is worth to
the product team, and a defensible ceiling rather than a runaway.
`installid.go` still persists a local UUID; its value is now threaded into
`Config.InstallID` and attached as the `mecatl.install.id` resource
attribute. `mecak8s` — storage-free, no PVC (ADR 0048) — cannot use that
same local-file mechanism (every pod restart would mint a fresh, never-reused
id, the worst-case cardinality pattern this pipeline could hit); its Helm
chart instead provisions one stable id per release via a Kubernetes
ConfigMap (see "mecak8s's install-id: a Helm ConfigMap, not a local file"
below).

**Heartbeat** (`metrics.go`/`heartbeat.go` — on start, then every ~24h for
long-running processes; single fire for `mecatequi`):

| Instrument | Kind | Attributes | Notes |
|---|---|---|---|
| `mecatl.product.heartbeat` | counter, +1 per fire | none | install/liveness signal |
| `mecatl.product.feature_enabled` | counter, +1 per enabled feature per heartbeat | `feature` | closed `Feature` set — see refinement below |
| `mecatl.product.provider_configured` | counter | `family` | closed `ProviderFamily`: `anthropic`/`openai`/`openrouter`/`other` — never a model id/alias |
| `mecatl.product.deployment_mode` | counter | `mode` | closed `DeploymentMode`: `interactive`/`headless`/`k8s` |

**Coarse usage** (`metrics.go`/`toolcall.go` — derived from the
`port.EventSink`/`port.ToolCallRecorder` tap, exported on the provider's
normal periodic-reader cadence since these are cumulative counters):

| Instrument | Kind | Attributes | Fires on |
|---|---|---|---|
| `mecatl.product.sessions_started` | counter | none | `EvSessionInit` |
| `mecatl.product.runs_completed` | counter | `stop` (reuses `session.StopReason`), `had_tool_call` (`true`/`false`: at least one SUCCESSFUL tool call in the run — an errored-only run is `false`) | `EvResult` |
| `mecatl.product.tool_calls` | counter | `category` (a built-in tool's own name from a closed allowlist, `mcp` for anything MCP-server-provided via the structural `mcp__` name prefix, or `other` for anything unrecognized — **never a raw MCP server/tool name**), `outcome` (`success`/`error`) | every `ToolCallRecorder.ToolCall`/`RunAwareToolCallRecorder.ToolCallForRun` |
| `mecatl.product.tokens` | counter | `kind` (`input`/`output`/`cache_read`/`cache_write`/`reasoning`) | `EvResult`'s `Usage` |
| `mecatl.product.subagent_used` | counter | none | `EvSubagentStart` |
| `mecatl.product.team_used` | counter | none | `EvTeamStart` |
| `mecatl.product.run_duration` | histogram (seconds) | none | `EvResult`, when this process observed the run's `EvSessionInit` |
| `mecatl.product.tool_calls_per_run` | histogram (count) | none | every `EvResult` (a tool-less run contributes an honest 0) |
| `mecatl.product.time_to_first_value` | histogram (seconds) | none | at most once per install, ever, on the first run with `had_tool_call=true` and `stop=StopEndTurn` |


**Deliberate scope refinement from the original design spec.** The spec's
illustrative heartbeat catalog listed `teams`/`subagents`/`learning` as
feature-enabled flags alongside `memory`/`guardrails`/`mcp`/`scheduling`.
During implementation those three were dropped from the heartbeat: there is
no reliable per-CLI-flag boolean signal for "is Team/Subagent/learning
enabled" the way there is for a settings toggle like memory or guardrails —
those features are always *available*, not gated by a single config
boolean, so a `feature_enabled{feature="teams"}` counter would either always
fire (uninformative) or require inventing a proxy signal (dishonest). The
shipped `productmetrics.Feature` closed set is exactly four values:
`memory`, `guardrails`, `mcp`, `scheduling` (`config.go`). Team/Subagent
adoption is instead captured honestly via the coarse-usage event tap —
`mecatl.product.subagent_used`/`team_used` fire once per run the first time
that delegation family is actually invoked — which is a truer adoption
signal than a static capability flag. This ADR records the *shipped* set;
readers should treat the design spec (`docs/superpowers/specs/2026-09-08-product-metrics-otel-design.md`)
as historical context, not the current catalog.

Nothing here is free text, a session/run/model identifier, an MCP server/tool
name, a file path, a prompt, or an output. `category` is the one attribute
that emits a name verbatim — but only a built-in tool's own name, drawn from
a maintained closed allowlist in `toolcall.go` (`builtinToolCategories`), with
`"mcp"`/`"other"` as safe catch-alls; every other label value is drawn from a
closed Go-level enum defined in `internal/adapter/productmetrics/config.go`
or reused from `engine/session` (`StopReason`).

### Opt-out precedence and the operator-tier-only settings gate

Product metrics are **enabled by default** (opt-out). `internal/cliconfig.ResolveProductMetricsEnabled`
(`productmetrics_config.go`) folds four inputs, highest precedence first:

1. An explicit CLI flag: `--product-metrics=false` (all four binaries).
2. The `DO_NOT_TRACK` environment variable set to a truthy value (`""`/`"0"`/
   `"false"`, case-insensitive, are NOT an opt-out) — the cross-ecosystem
   convention (donottrack.sh), so the one env var that already opts CI
   fleets and dev machines out of *other* tools' telemetry covers mecatl
   too, with no mecatl-specific variable to remember. (A dedicated
   `MECATL_PRODUCT_METRICS=0` was deliberately not added on top of it — one
   standard signal beats two overlapping ones.)
3. `telemetry.productMetrics.enabled: false` in the **operator-tier**
   settings file (`~/.config/mecatl/settings.yaml` + CLI-loaded equivalents)
   — `permconfig.Resolver.OperatorProductMetricsEnabled()`.
4. Default: enabled.

The settings toggle is **operator-tier only**, the same trust boundary as
`guardrails:`/`openrouter:` (AGENTS.md's existing operator-tier-only
precedent). A project-tier `.mecatl/settings.yaml` can neither enable nor
disable it for a user — parsed with the same WARN-and-ignore discipline as
the other operator-only subtrees (`internal/adapter/permconfig/resolve.go`'s
`captureTelemetry` + the project-tier ignore WARN). This direction matters
specifically because it is the *reverse* of most tighten-only project gates:
a project silently overriding a user's own telemetry opt-out (in *either*
direction — forcing it on, or forcing it off to hide activity from an
otherwise-informed operator) would itself be a trust violation, so the
whole subtree is simply inadmissible from a project file.

### Privacy guard test discipline

A reflect-based guard test (mirroring the existing `attrRole`/`attrStop`
bounded-label discipline elsewhere in the codebase, and the
`engine/port/diagnostics_imports_test.go` import-tripwire pattern) asserts
that `Recorder`'s entire public API accepts no bare free-text `string`
parameter — only bounded enum types (Go string-alias types with a small
closed value set) and counts/durations. This makes "no PII can flow through
this type" a property CI checks on every future change to the package, not
just a code-review norm that erodes over time. `DryRunRecorder` mirrors
`Recorder`'s restraint field-for-field (it logs the exact same bounded set
`Recorder` would have recorded, via `port.Diagnostics`, never more).

Additional structural safeguards: exporter failures are silent to the app
(lazy-dial exporter, matching the existing OTLP exporter pattern — a dead
`metrics.stacklok.com` never blocks or slows a session); `Shutdown` is
bounded so a hung network path can never delay process exit; the
`MeterProvider` is never installed as the process-global provider (mirrors
`internal/adapter/telemetry`'s own discipline), so it structurally cannot
collide with, or be mistaken for, an operator's own OTel setup.

### The dry-run audit mode

`--product-metrics-dry-run` (all four binaries) builds a
`productmetrics.DryRunRecorder` in place of the real OTLP `Recorder`
(`BuildProductMetrics` in `internal/cliconfig/productmetrics.go`): no
install-id read/write, no real provider construction, no real heartbeat
ticker — it fires exactly one representative `Heartbeat` in its own
goroutine (a dry run only needs to demonstrate one sample, not simulate the
full ~24h cadence) and logs every would-be observation through the injected
`port.Diagnostics` instead of exporting it. It logs precisely the same
bounded fields the real `Recorder` would have read (event type, stop
reason, token counts by kind, feature/provider/mode enum values) — never
more. This exists so a skeptical operator can verify the "no PII, here is
exactly what leaves this process" claim by running the binary once, rather
than trusting this document.

### Why opt-out is defensible here

Opt-out telemetry is usually a trust liability. Four things together make it
defensible in this specific case, and all four are load-bearing — remove any
one and the balance shifts back toward requiring opt-in:

1. **A visible, non-blocking first-run disclosure.** The first time a
   binary is about to actually send product metrics (telemetry enabled, and
   this install's telemetry-id file did not yet exist), it prints
   `cliconfig.ProductMetricsDisclosureNotice` once to stderr — what is
   collected, that it is on by default, and the exact three ways to turn it
   off. It never blocks. An opt-out default with no visible disclosure is
   the pattern that burns community trust; this is the whole of that
   disclosure, and it is not optional or hidden in a man page.
2. **`DO_NOT_TRACK` support**, so the opt-out is not mecatl-specific
   knowledge — anyone who already opts every other tool in their environment
   out of telemetry gets mecatl covered for free, with zero new config to
   learn.
3. **A reviewable, tested, closed catalog.** Every metric this pipeline can
   ever emit is enumerated in this document and enforced by the privacy
   guard test above — an operator (or a contributor reading this ADR) does
   not have to trust a claim; they can read the closed `Feature`/
   `ProviderFamily`/`DeploymentMode`/token-kind enums and the guard test that
   pins them, and know that is the entire surface, mechanically, not just as
   of today.
4. **The dry-run self-verification path.** `--product-metrics-dry-run` lets
   anyone confirm the catalog claim empirically on their own machine before
   trusting `--product-metrics` for real, rather than trusting either the
   docs or the code review that produced them.

### Correlating a tool call to its run: `port.RunAwareToolCallRecorder`

`had_tool_call`, `tool_calls_per_run`, and `time_to_first_value` all need to
know which *run* a given tool call belongs to. The existing
`port.ToolCallRecorder.ToolCall(id session.SessionID, ...)` only carries a
`SessionID` — a session can span many sequential runs over its lifetime, so a
`SessionID` alone cannot answer "did this run make a tool call". The engine's
other event-sourced callback, `port.EventSink.Emit(ctx, session.Event)`, has
the opposite problem: `session.Event.RunID` is present, but `Emit` never sees
a tool call at all.

The fix is a new, standalone, OPTIONAL port interface —
`port.RunAwareToolCallRecorder` — added to `engine/port/log.go`, mirroring the
existing `HookApprovalLearner` precedent (`engine/port/hookrunner.go`) exactly:

```go
type RunAwareToolCallRecorder interface {
	ToolCallForRun(runID string, id session.SessionID, call session.ToolCall,
		result session.ToolResult, queued, took time.Duration)
}
```

`engine/agent/dispatch.go`'s one `ToolCallRecorder` call site type-asserts for
this richer interface and prefers it (passing the enclosing `Run`'s own
`RunID()`, already in scope — no new parameter threading needed anywhere in
the call chain) when a recorder implements it, falling back to the plain
`ToolCall` otherwise. This is purely additive: no existing `ToolCallRecorder`
implementer (the operator telemetry pipeline, `jsonlstore`, `redisstore`) is
affected, and `engine/CHANGELOG.md` records it as `Added` (minor) per
`engine/COMPATIBILITY.md`.

`internal/adapter/productmetrics.Recorder` implements
`RunAwareToolCallRecorder` (`toolcall.go`): `ToolCallForRun` records the
bounded `category`/`outcome` attributes on `tool_calls` and tallies a
per-run state (`hadToolCall`, `toolCallCount`, `startedAt`) keyed by `RunID`
in a mutex-guarded `perRunTracker`, cleared at the run's `EvResult`. The
category derivation is a closed-set projection, `toolCategory`
(`toolcall.go`): the `mcp__` structural name prefix (`internal/adapter/mcp`'s
`"mcp__" + server + "__" + toolName` construction) buckets every MCP-server
tool under the single literal `"mcp"` with no allowlist needed; every other
name is checked against a maintained `builtinToolCategories` allowlist
(mecatl's own fixed tool catalog), falling back to the literal `"other"` for
anything unrecognized. A hand-maintained allowlist for non-MCP tools was not
the original design (a pure structural-prefix rule was) — it became necessary
because this package's existing privacy-guard test (`bounded_test.go`)
correctly rejects any tool name reaching an attribute value verbatim, and a
prefix-only rule would let an agent-def name, a learned-skill name, or a
future extension-seam name (all potentially operator- or model-derived free
text) leak straight onto an exported counter. The allowlist trades "zero
maintenance for new built-ins" for "structurally impossible to leak" — a new
built-in tool shows up as `"other"` until a line is added, which is visible
and harmless, never a leak.

### mecak8s's install-id: a Helm ConfigMap, not a local file

`mecak8s` runs storage-free with no PVC (ADR 0048) — the local-file mechanism
`installid.go` uses for the other three binaries would mint a fresh,
never-reused install id on every pod restart, the worst-case cardinality
pattern this pipeline could hit (every replica of every deployment counted as
a distinct, ever-churning "install"). Its Helm chart
(`deploy/helm/mecak8s/templates/install-id-configmap.yaml`) instead
provisions ONE stable id per release into a `<release>-mecak8s-install-id`
ConfigMap, using Helm's standard `lookup`+`uuidv4` "generate once, keep stable
on upgrade" idiom: a `lookup` against the release namespace for an existing
ConfigMap of that name reuses its `installId` value verbatim on every `helm
upgrade` (and safely re-mints a fresh id if the lookup finds no usable value —
a nil `.data` map or a missing key degrade to "mint a new one", never an
error or an empty string); only a genuinely first `helm install` mints a new
id. `lookup` runs with the Helm client's own credentials at render time, not
the pod's ServiceAccount at runtime, so no RBAC grant was needed. The id is
threaded into the container via a `MECATL_PRODUCT_METRICS_INSTALL_ID`
environment variable (`configMapKeyRef`), which `cmd/mecak8s/observability.go`
reads and passes to `internal/cliconfig.BuildProductMetrics`'s new
`installIDOverride` parameter — non-empty skips `LoadOrCreateInstallIDDefault`
entirely and never reports `FirstRun` (the chart, not the process, owns the
id's lifecycle). The other three binaries pass `""` and keep the local-file
behavior unchanged. Deleting the ConfigMap resets the id, the same as
deleting the local file does for the other three binaries.

## Consequences

- A new direct dependency surface: `github.com/stacklok/toolhive-core/telemetry/providers`
  (already present at `v0.0.43`) is now also used by `internal/adapter/productmetrics`,
  in addition to `internal/adapter/telemetry`'s own inline OTLP construction —
  two independent OTLP wiring call sites in the same binary, by design, never
  merged.
- A new operator-tier-only YAML subtree (`telemetry.productMetrics.enabled`)
  and its own strict-decode schema (`permconfig.TelemetrySection`/
  `ProductMetricsSection`) — a project-tier `telemetry:` block is parsed and
  then discarded with a WARN, never silently accepted.
- Four new CLI flags across four binaries (`--product-metrics`,
  `--product-metrics-dry-run`), all defaulting to the byte-identical
  disabled-pipeline posture when a non-release build carries no baked ingest
  key (`bakedKey == ""` refuses `NewProvider` outright) — a local `go build`/
  `go test`/CI build can never phone home regardless of flag state.
- Two small persisted files per install, both under `$XDG_STATE_HOME/mecatl/`:
  `telemetry-id` (a bare random v4 UUID, now attached as the
  `mecatl.install.id` resource attribute — see the reinstatement rationale
  above) and `first-value-recorded` (a bare marker recording whether
  `time_to_first_value` has already been sampled). Both are trivially reset
  by deleting them, carry no machine or user information, and (on
  `mecak8s`) are replaced entirely by the Helm-provisioned ConfigMap — see
  "mecak8s's install-id" above.
- ADR-0027 List-1 (resource inventory) is NOT extended: the heartbeat
  ticker's lifetime matches the process (owned by the caller's
  `heartbeatCtx`, cancelled on shutdown alongside the rest of composition's
  shutdown sequence) and the `Provider`/`Recorder` pair holds no cross-run
  state a restart would need to rehydrate — this pipeline is stateless
  across restart by design, the same reasoning ADR 0098 used for its own
  periodic-reader rows.
- Going forward, any NEW metric added to this package must (a) be added to
  the catalog table above, (b) use only an existing or newly-defined closed
  enum type (never a bare `string`), and (c) pass the privacy guard test
  unchanged — the test is the enforcement mechanism, this ADR is the record.
- A per-run cost/dollar metric and any richer per-feature usage counters
  (per-MCP-transport type, per-model-family latency) remain explicitly out
  of scope, mirroring the deferred #192 cost-metric gap in the operator
  pipeline — start narrow, revisit only if a concrete adoption question the
  current catalog cannot answer comes up.

## See also

- [ADR 0098 — Telemetry for the headless binaries](./0098-headless-telemetry.md)
  — the operator-facing OTLP/Prometheus pipeline and the
  `internal/cliconfig` Rule-of-Three home this ADR's composition helper
  (`BuildProductMetrics`) reuses structurally, but shares NO runtime state
  with. Prior art deliberately not reused: this is a new, separate channel,
  not a fourth use of the existing telemetry pipeline.
- [ADR 0020 — Diagnostics, audit, and the global-slog ban](./0020-diagnostics.md)
  — the three-channel observability taxonomy (`port.Diagnostics`,
  `port.ToolCallRecorder`, `port.EventSink`→`internal/adapter/telemetry`)
  this decision deliberately does NOT add a fourth row to; product metrics
  taps the same two ports (`EventSink`, `ToolCallRecorder`) every existing
  consumer already receives independently, but through its own entirely
  separate adapter, `MeterProvider`, and destination.
- [ADR 0018 — Performance & observability](./0018-perf-observability.md)
- `user-docs/building/what-you-get/observability.md` — the "Product /
  adoption metrics (opt-out)" section added alongside the existing
  operator-facing channels.
- `docs/superpowers/specs/2026-09-08-product-metrics-otel-design.md` — the
  original design spec this ADR records the shipped outcome of (see the
  scope-refinement note above for the one deliberate deviation).
- `stacklok/infra#5604` — the dedicated public OTLP ingest this pipeline
  reports to.
