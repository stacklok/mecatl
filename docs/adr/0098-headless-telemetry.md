# ADR 0098 — Telemetry for the headless binaries (mecatequi, mecak8s)

- Status: Accepted
- Date: 2026-08-05
- Scope: `cmd/mecatequi`, `cmd/mecak8s`, `internal/adapter/telemetry`, `internal/cliconfig`
- Supersedes: —
- Superseded by: —

## Context

mecated (the interactive daemon) ships the full observability pipeline:
`telemetry.Setup` builds an always-on Prometheus-exporter `MeterProvider` +
an opt-in OTLP trace exporter; `NewAdminMux` serves `/metrics` (and the
runtime-introspection surface) on a loopback admin listener; the role-scoped
`Metrics.WithRole` + `MetricsRoleScoper` closure tags every series with the
bounded issue-#47 closed `Role*` set. The two headless mains deliberately
opted OUT of this (`cmd/mecatequi/flags.go`, `cmd/mecak8s/flags.go` nil'd
`Config.Sink`/`ToolCallRecorder`/`MetricsRoleScoper`), so a headless
deployment had no per-run token/turn/tool metrics and no traces — a gap for
CI observability (mecatequi) and a k8s pod a scrape target could not reach
(mecak8s).

The fix is REUSE, not new telemetry. The adapter already builds the whole
pipeline via `telemetry.Setup`; the headless mains just did not wire it. The
constraint is that the no-telemetry posture must stay byte-identical (the
default), so the wiring is OPT-IN behind flags that default off.

The two binaries have different shapes:

- **mecatequi** is single-shot and short-lived (often sub-second). A Prometheus
  scrape does not fit a process that exits before a scrape interval. It needs a
  PUSH model — an OTLP METRICS push reader (a `PeriodicReader` over an
  `otlpmetricgrpc`/`otlpmetrichttp` exporter) alongside the existing OTLP
  trace push — and an explicit flush before `os.Exit`.
- **mecak8s** is a long-lived server. A Prometheus scrape fits, but `/metrics`
  must stay on a SEPARATE loopback listener (ADR 0018 decision 6: the admin mux
  output is secret-shaped) — mecak8s binds `0.0.0.0:8081` for its API today, so
  `/metrics` cannot ride that surface. OTLP metrics push is offered as an opt-in
  twin for non-scrape deployments.

## Decision

1. **Reuse `telemetry.Setup` + `NewMetrics` + `WithRole`/role-scoper.** Both
   headless mains wire the SAME observability path mecated wires inline, behind
   opt-in flags, via a shared helper (`internal/cliconfig.HeadlessTelemetry`,
   the Rule-of-Three extraction site for the three mains). The helper returns
   zero handles (byte-identical no-telemetry posture) when no endpoint is set.
   mecated's inline path is NOT refactored in this PR (a follow-up); the helper
   is the single source for the two NEW mains.

2. **mecatequi: OTLP push (metrics + traces) with flush-before-exit.** New
   flags `--otlp-endpoint`, `--otlp-protocol`, `--otlp-insecure`,
   `--otlp-metrics-endpoint`, `--otlp-metrics-protocol`, and
   `--otlp-shutdown-timeout` (default 5s). When any endpoint is set, the
   pipeline is built and `app.Config.Sink`/`ToolCallRecorder`/`MetricsRoleScoper`
   are populated. The flush (`Providers.Shutdown`) runs in a `defer` registered
   AFTER `defer built.Close()` (LIFO → flush first), bounded by
   `--otlp-shutdown-timeout` so a dead collector cannot hang CI. `os.Exit` then
   kills any lingering export goroutine.

3. **mecak8s: `/metrics` scrape listener + opt-in OTLP.** New flags
   `--metrics-addr` (loopback-only, fail-closed at parse time), plus the OTLP
   trace/metrics flags. When `--metrics-addr` is set, `serve` starts a third
   `http.Server` serving `telemetry.NewAdminMux` on a SEPARATE loopback
   listener; the listener joins the `errCh` set + the `boundedShutdown`
   sequence. SIGTERM flushes OTLP via the `defer` (LIFO before `built.Close()`).

4. **Extend `telemetry.Setup` with an optional OTLP metrics push reader.**
   `OTLPConfig` gains `MetricsEndpoint`/`MetricsProtocol`/`MetricsInsecure`/
   `MetricsHeaders`/`MetricsTimeout`/`MetricsPushInterval`. When
   `MetricsEndpoint != ""`, `newMeterProvider` attaches a `PeriodicReader` over
   an `otlpmetricgrpc`/`otlpmetrichttp` exporter alongside the always-on
   prometheus reader. `Providers.Shutdown` flushes + stops it. Existing
   scrape-only callers are byte-identical (no periodic reader when empty).

5. **Per-run label discipline inherits the issue-#47 closed `Role*` set.** No
   session id, model id, run id, or free text becomes a label — the headless
   mains use the SAME `attrRole`/`roleFamily` mapping mecated does
   (`metrics.go` + `internal/app/build.go`). The metric surface reuses the
   existing instruments (`mecatl.tokens`, `mecatl.runs`, `mecatl.tool.calls`/
   `duration`/`queue`, `mecatl.turns`/`turn.duration`/`ttft`/`inter_token`/
   `inter_token.max`, `mecatl.active_runs`, `mecatl.permission.asks`,
   `mecatl.cache_hit_ratio`). NONE are added.

6. **Cost metric deferred to #192.** A per-run `mecatl.cost` counter is NOT
   landed: there is no `Cost` field anywhere and `internal/adapter/providercatalog/catalog.go`
   explicitly does not parse cost. It is a follow-up tracked in #192.

## Consequences

- A new direct dependency: `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/
  otlpmetricgrpc` (and `otlpmetrichttp` promoted to direct), matching the
  existing `otlptrace*` family. `task tidy` updates `go.mod`.

- mecak8s gains one extra loopback listener when `--metrics-addr` is set. The
  admin mux output (pprof/expvar/metrics) stays loopback-only — a non-loopback
  bind is REJECTED at parse time (fail-closed), mirroring mecated's `--perf-mcp`
  loopback refusal.

- mecatequi gains a flush-before-exit seam: the telemetry `Shutdown` defer must
  run before `built.Close()` (LIFO ordering) and on EVERY exit path
  (setup-failure included). A bounded ctx prevents a dead collector from
  hanging the run.

- ADR-0027 List-1 (resource inventory) gains two rows: the mecatequi OTLP
  periodic-reader + flush-on-exit (owner: `realMain` defer; cleanup:
  `Providers.Shutdown`) and the mecak8s `/metrics` loopback listener (owner:
  `serve`; cleanup: `boundedShutdown`). No List-2 row — both are stateless
  across restart by design (a short-lived run has no scrape state; the
  prometheus reader is process-local).

- The Rule-of-Three helper lives in `internal/cliconfig` (NOT `internal/app`):
  composition stays telemetry-import-free, and the cmd mains already route flag
  wiring through this package.

## See also

- [ADR 0018 — Performance & observability](./0018-perf-observability.md) (the
  `/metrics` loopback decision 6 the metrics-addr gate enforces)
- [ADR 0027 — Cloud-native](./0027-cloud-native.md) (List-1 resource inventory)
- [ADR 0045 — Explicit-bucket latency histograms](./0045-explicit-bucket-latency-histograms.md)
- [ADR 0048 — mecak8s](./0048-mecak8s.md)
- [ADR 0028 — mecatequi](./0028-mecatequi.md)
- Issue #343 (telemetry for the headless binaries); issue #47 (role-scoped
  metrics); issue #192 (pricing/cost metric, deferred)