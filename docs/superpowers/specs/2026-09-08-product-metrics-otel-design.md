# Product (adoption) metrics over OTLP — design

- Status: Draft
- Date: 2026-09-08
- Scope: new `internal/adapter/productmetrics`, `internal/cliconfig`, `internal/adapter/permconfig` (new `telemetry:` operator section), `cmd/mecated`, `cmd/mecatui`, `cmd/mecatequi`, `cmd/mecak8s`

## Context

mecatl has no visibility into community adoption today: no install counts, no
feature-adoption signal, no aggregate usage depth. Stacklok's infra team has
stood up a dedicated, internet-facing OTLP/HTTP metrics ingest at
`https://metrics.stacklok.com/v1/metrics` specifically for mecatl binaries
running on infrastructure Stacklok does not control (`stacklok/infra#5604`):
API-key-gated at the edge (`x-mecatl-metrics-key` header, stripped before the
collector), and server-side filtered to accept only metric names matching
`^mecatl\..*`.

This is a **new, separate concern** from mecatl's existing operator-facing
observability. `internal/adapter/telemetry` already ships a full OTel pipeline
(ADR 0018/0045/0098): a `MeterProvider` with an always-on Prometheus reader and
an optional OTLP push reader an *operator* points at *their own* collector, plus
an OTLP trace exporter. That pipeline exists so an operator can observe their
own deployment. It must never become the transport for community-adoption data
— an operator's own `--otlp-endpoint` configuration must have zero effect on
what does or doesn't reach Stacklok, and enabling product metrics must have
zero effect on what an operator's own collector receives.

`toolhive-core` (already an mecatl dependency, `v0.0.43`) ships
`telemetry/providers`: a small, already-reviewed OTel SDK-wiring layer
(`providers.NewCompositeProvider`) that builds a `metric.MeterProvider` from an
options struct (endpoint, headers, service name/version, custom resource
attributes) without ever installing it as the process-global provider. This is
the natural building block for the new pipeline's OTLP/HTTP exporter — it
already speaks the exact shape the new collector expects (custom headers,
OTLP/HTTP, a resource with service name/version), so mecatl does not need to
hand-roll a third OTLP wiring implementation next to the two it already has
(`internal/adapter/telemetry/otlp.go`'s own inline construction, and the
toolhive-core one).

## Decision

### 1. A fully independent adapter, `internal/adapter/productmetrics`

Zero import relationship with `internal/adapter/telemetry`. It owns:

- Its own `metric.MeterProvider`, built via `toolhive-core/telemetry/providers`
  with a **hardcoded** endpoint (`https://metrics.stacklok.com/v1/metrics`) and
  a **hardcoded** header key baked into the binary at build time (matching the
  infra PR's `x-mecatl-metrics-key` contract) — neither is operator-configurable.
  There is exactly one place this data can go.
- Its own small `Recorder` type implementing `port.EventSink` +
  `port.ToolCallRecorder` (the same two seams `internal/adapter/telemetry`
  taps), but extracting *only* the bounded counts in the catalog below — the
  type has no field or parameter through which a tool name, session id, model
  id, or free text could ever flow.
- Its own heartbeat ticker (fires once at start, then every ~24h for
  long-running processes; a single fire + flush-before-exit for the short-lived
  `mecatequi`, mirroring the existing OTLP-push-with-flush precedent in ADR
  0098).
- Its own install-identity file.

Composition combines the two independent sinks with a trivial fan-out helper
in `internal/cliconfig` (the existing Rule-of-Three home for cross-binary
telemetry wiring, per ADR 0098) — `internal/app` stays import-free of
`productmetrics`, exactly as it is of `telemetry` today. Each `cmd/*/main.go`
builds its existing operator telemetry pipeline unchanged, and — only when
product metrics are enabled — separately constructs a `productmetrics.Recorder`
and tees it in alongside.

This means: an operator who disables their own OTLP export still has product
metrics flow (if enabled) to Stacklok, and an operator who fully disables
product metrics has zero effect on their own OTLP/Prometheus pipeline. The two
literally cannot leak into each other because they share no struct, provider,
registry, or destination — only the same two read-only observation points in
the engine (`port.EventSink`, `port.ToolCallRecorder`), which every consumer
of those ports already receives independently per composition's existing
fan-out discipline.

### 2. Metric catalog

All instrument names are namespaced under `mecatl.adoption.*` — passes the
collector's `^mecatl\..*` filter, and is visually/query-wise distinct from the
operator-facing `mecatl.tool.*`/`mecatl.runs`/etc. family, so nobody looking at
either series family can mistake one for the other.

**Resource attributes** (set once per process, not per-metric labels):
- `service.name` = `mecatl`, `service.version`
- `os.type`, `host.arch` (standard OTel semconv, generic platform facts)
- `mecatl.install.id` — a random v4 UUID (see §4)
- `mecatl.binary` — one of `mecated`/`mecatui`/`mecatequi`/`mecak8s` (closed set)

**Heartbeat** (on start, then every ~24h for long-running processes; single
fire for `mecatequi`):
- `mecatl.adoption.heartbeat` (counter, +1 per fire) — the install/liveness signal.
- `mecatl.adoption.feature_enabled{feature=...}` (counter, +1 per enabled
  feature per heartbeat) — `feature` is one of a closed set: `memory`,
  `learning`, `guardrails`, `mcp`, `teams`, `subagents`, `scheduling`.
- `mecatl.adoption.provider_configured{family=...}` (counter) — `family` is one
  of `anthropic`/`openai`/`openrouter`/`other` (never a model id/alias).
- `mecatl.adoption.deployment_mode{mode=...}` (counter) — `mode` is one of
  `interactive`/`headless`/`k8s`.

**Coarse usage** (derived from the event/tool-call tap, exported on the
provider's normal periodic-reader cadence — no manual batching needed since
these are cumulative counters):
- `mecatl.adoption.sessions_started` (counter) — on `EvSessionInit`.
- `mecatl.adoption.runs_completed{stop=...}` (counter) — on `EvResult`; `stop`
  reuses the existing bounded `session.StopReason` enum.
- `mecatl.adoption.tool_calls` (counter, **no tool/MCP-server name label at
  all**) — on every `ToolCallRecorder.ToolCall`.
- `mecatl.adoption.tokens{kind=...}` (counter) — `kind` reuses the existing
  bounded token-kind enum (`input`/`output`/`cache_read`/`cache_write`/
  `reasoning`).
- `mecatl.adoption.subagent_used` / `mecatl.adoption.team_used` (counter,
  bumped at most once per run when that delegation family appears at all — no
  def/member/model name ever surfaces).

Nothing here is free text, a session/run/model identifier, a tool or MCP
server name, a file path, a prompt, or an output. Every label value is drawn
from a closed enum that already exists internally or is defined fresh in this
package as a small closed set.

### 3. Config & opt-out

**Enabled by default** (opt-out), but the toggle is **operator-tier only** —
same trust boundary as `guardrails:`/`openrouter:` (AGENTS.md's existing
operator-tier-only precedent): a project-tier `.mecatl/settings.yaml` can
neither enable nor disable it for a user; a project silently overriding a
user's own telemetry choice in either direction would itself be a trust
violation, so it is parsed with the same WARN-and-ignore discipline as the
other operator-only subtrees.

`~/.config/mecatl/settings.yaml`:
```yaml
telemetry:
  productMetrics:
    enabled: true   # default; set false to opt out
```

Additional disable signals, in precedence order (highest first):
1. CLI flag `--product-metrics=false` (or `--no-product-metrics`) on all four binaries.
2. `DO_NOT_TRACK` environment variable (any non-empty value) — the
   cross-ecosystem convention (consoledonottrack.com), so a single env var
   already used to opt CI fleets and dev machines out of *other* tools' telemetry
   also covers mecatl, with no mecatl-specific config needed.
3. `telemetry.productMetrics.enabled: false` in the operator settings file.
4. Default: enabled.

A dedicated `MECATL_PRODUCT_METRICS=0` env var is deliberately **not** added on
top of `DO_NOT_TRACK` — one standard signal is preferable to two overlapping
ones with subtly different names.

**First-run disclosure.** The first time a binary is about to actually send
product metrics in a given run (i.e., telemetry is enabled and this is the
first invocation since the install-id file didn't yet exist), it prints one
non-blocking line to stderr: what is collected (link to the user-docs page),
and the exact flag/setting/env var to disable it. This is not a prompt — it
never blocks — but it is a hard requirement for an opt-out default to be
defensible to the community; the same disclosure text is what the mecatui
zero-state and mecated startup banner both use.

**Dry-run / audit flag.** `--product-metrics-dry-run` prints every metric this
process would have sent to stderr instead of exporting it, so a skeptical
operator can verify the "no PII" claim directly rather than trusting the docs.

### 4. Install identity

A random v4 UUID, generated on first use and persisted at
`$XDG_CONFIG_HOME/mecatl/telemetry-id` (or the platform equivalent via the
existing `internal/adapter/xdgconfig` helper). It contains no machine or user
information and is trivially reset by deleting the file (equivalent, from
Stacklok's side, to seeing what looks like a new install). It exists purely so
"ten heartbeats from one install" isn't miscounted as "ten installs" — no
other purpose.

### 5. Privacy safeguards

- A reflect-based guard test (mirroring the existing `attrRole`/`attrStop`
  bounded-label discipline, and the `engine/port/diagnostics_imports_test.go`
  import-tripwire pattern) asserts the `Recorder`'s entire public API accepts
  no bare `string`/free-text parameter — only bounded enum types (Go types
  with a small closed value set) and counts. This makes "no PII can flow
  through this type" a property a future PR's CI run checks, not just a
  code-review norm.
- Exporter failures are silent to the app (lazy-dial exporter matching the
  existing OTLP exporter pattern; a dead `metrics.stacklok.com` never blocks
  or slows down a session). `Shutdown` is bounded (~3s) so a hung network path
  can never delay process exit.
- No content, error message text, file path, or identifier of any kind is
  ever an attribute value — every label is a value from a fixed Go-level enum
  reviewed in this document.

### 6. Lifecycle across binaries

All four binaries (`mecated`, `mecatui`, `mecatequi`, `mecak8s`) wire this
identically through the shared `internal/cliconfig` helper (extending the
existing `HeadlessTelemetry`-style Rule-of-Three home). `mecatequi` (short-lived,
often sub-second) fires one heartbeat and flushes before exit, exactly as its
existing OTLP metrics push already does (ADR 0098). The other three run the
24h ticker for the process lifetime and flush on graceful shutdown.

### 7. Documentation

- A new ADR (next available number) records this decision, the exact catalog,
  and the "operator-tier only, separate pipeline, no PII" invariants — the
  same discipline as ADR 0098/0020, and the natural home for the "why opt-out
  is acceptable here" rationale (disclosure + easy universal disable + a
  published, reviewable catalog).
- A short `user-docs/` page explains what's collected, links the catalog in
  this doc, and gives the exact disable instructions (flag, env var, setting).

## Testing

- Unit tests for the `Recorder`'s bounded-enum bumps (one per metric, table-
  driven against the catalog above).
- The reflect-based "no free-text parameter" guard test (privacy invariant,
  §5).
- A conformance-style test that composing the operator sink + product-metrics
  sink and disabling one leaves the other's counters unaffected (isolation
  invariant).
- `internal/cliconfig` wiring test verifying the opt-out precedence order
  (flag > `DO_NOT_TRACK` > settings.yaml > default-enabled).
- Offline only — no live network call to `metrics.stacklok.com` in tests; the
  OTLP exporter construction is exercised against a local httptest server, the
  same pattern `internal/app/openrouter_route_e2e_test.go` already uses for a
  comparable "real adapter against an httptest stand-in" check.

## Out of scope (deferred)

- A per-run cost/dollar metric (mirrors the existing deferred #192 cost-metric
  gap in the operator pipeline — no `Cost` field exists anywhere yet).
- Any richer per-feature usage counters beyond the catalog above (e.g. per-MCP
  transport type, per-model-family latency) — start narrow, revisit only if a
  concrete adoption question the catalog can't answer comes up.
- A first-run interactive consent prompt (explicitly declined in favor of
  opt-out + disclosure, per the design decision above).
