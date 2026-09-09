# ADR 0317 — Product (adoption) metrics over OTLP

- Status: Accepted
- Date: 2026-09-09
- Scope: `internal/adapter/productmetrics` (new), `internal/cliconfig`, `internal/adapter/permconfig` (new `telemetry:` operator section), `cmd/mecated`, `cmd/mecatui`, `cmd/mecatequi`, `cmd/mecak8s`
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
- Its own install-identity file (`installid.go`).

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

### The shipped `mecatl.adoption.*` catalog

All instrument names are namespaced under `mecatl.adoption.*` — passes the
collector's `^mecatl\..*` filter, and is visually/query-wise distinct from
the operator-facing `mecatl.tool.*`/`mecatl.runs`/etc. family, so nobody
looking at either series family can mistake one for the other.

**Resource attributes** (set once per process via `productmetrics.Config`,
not per-metric labels): `service.name=mecatl`, `service.version`,
`mecatl.install.id` (a random v4 UUID), `mecatl.binary` (one of the closed
`Binary` set: `mecated`/`mecatui`/`mecatequi`/`mecak8s`).

**Heartbeat** (`metrics.go`/`heartbeat.go` — on start, then every ~24h for
long-running processes; single fire for `mecatequi`):

| Instrument | Kind | Attributes | Notes |
|---|---|---|---|
| `mecatl.adoption.heartbeat` | counter, +1 per fire | none | install/liveness signal |
| `mecatl.adoption.feature_enabled` | counter, +1 per enabled feature per heartbeat | `feature` | closed `Feature` set — see refinement below |
| `mecatl.adoption.provider_configured` | counter | `family` | closed `ProviderFamily`: `anthropic`/`openai`/`openrouter`/`other` — never a model id/alias |
| `mecatl.adoption.deployment_mode` | counter | `mode` | closed `DeploymentMode`: `interactive`/`headless`/`k8s` |

**Coarse usage** (`metrics.go`/`toolcall.go` — derived from the
`port.EventSink`/`port.ToolCallRecorder` tap, exported on the provider's
normal periodic-reader cadence since these are cumulative counters):

| Instrument | Kind | Attributes | Fires on |
|---|---|---|---|
| `mecatl.adoption.sessions_started` | counter | none | `EvSessionInit` |
| `mecatl.adoption.runs_completed` | counter | `stop` (reuses `session.StopReason`) | `EvResult` |
| `mecatl.adoption.tool_calls` | counter | none — **no tool/MCP-server name label at all** | every `ToolCallRecorder.ToolCall` |
| `mecatl.adoption.tokens` | counter | `kind` (`input`/`output`/`cache_read`/`cache_write`/`reasoning`) | `EvResult`'s `Usage` |
| `mecatl.adoption.subagent_used` | counter | none | `EvSubagentStart` |
| `mecatl.adoption.team_used` | counter | none | `EvTeamStart` |

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
`mecatl.adoption.subagent_used`/`team_used` fire once per run the first time
that delegation family is actually invoked — which is a truer adoption
signal than a static capability flag. This ADR records the *shipped* set;
readers should treat the design spec (`docs/superpowers/specs/2026-09-08-product-metrics-otel-design.md`)
as historical context, not the current catalog.

Nothing here is free text, a session/run/model identifier, a tool or MCP
server name, a file path, a prompt, or an output. Every label value is drawn
from a closed Go-level enum already defined in `internal/adapter/productmetrics/config.go`
or reused from `engine/session` (`StopReason`).

### Opt-out precedence and the operator-tier-only settings gate

Product metrics are **enabled by default** (opt-out). `internal/cliconfig.ResolveProductMetricsEnabled`
(`productmetrics_config.go`) folds four inputs, highest precedence first:

1. An explicit CLI flag: `--product-metrics=false` (all four binaries).
2. The `DO_NOT_TRACK` environment variable (any non-empty value) — the
   cross-ecosystem convention (consoledonottrack.com), so the one env var
   that already opts CI fleets and dev machines out of *other* tools'
   telemetry covers mecatl too, with no mecatl-specific variable to
   remember. (A dedicated `MECATL_PRODUCT_METRICS=0` was deliberately not
   added on top of it — one standard signal beats two overlapping ones.)
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
- A new small persisted file per install
  (`$XDG_STATE_HOME/mecatl/telemetry-id`, a bare random v4 UUID) — trivially
  reset by deleting it, and carrying no machine or user information.
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
