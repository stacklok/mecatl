# ADR 0045 — Explicit-bucket latency histograms (zero-config quantiles on /metrics)

- Status: Accepted
- Date: 2026-06-23
- Scope: `internal/adapter/telemetry` (the latency-instrument aggregation `LatencyViews`/`latencyBucketAggregation`), and by reflection the perf-MCP reducer (`internal/adapter/mcpperf`) that consumes the Prometheus exposition
- Supersedes: the histogram-aggregation decision in [ADR 0018](./0018-perf-observability.md) §5 decision 2 ONLY (the rest of ADR 0018 — the phased hybrid, the perf-MCP server, the loopback posture — is unchanged)

## Context

ADR 0018 §5 decision 2 chose **base-2 exponential (OTel) / native (Prometheus)
histograms** for the latency instruments (tool/turn duration, TTFT, inter-token
mean+max, tool queue), reasoning that exponential buckets give accurate tails
across a wide dynamic range and are fleet-aggregatable.

There is exactly ONE `MeterProvider` with ONE reader: the Prometheus exporter
(`internal/adapter/telemetry/otlp.go`, `newMeterProvider`). **OTLP metrics are NOT
wired** — the push path is only a documented seam (`Setup`'s comment: "adding an
otlpmetric reader here … is a one-line change"); no exponential-tail consumer
exists today.

The otel→Prometheus exporter renders an exponential histogram as a Prometheus
**native** histogram. In the classic **text** exposition (`curl :9099/metrics`,
the surface a developer actually reaches for locally, and what promtool reads)
a native histogram degrades to `sum`/`count` plus a lone `le="+Inf"` bucket —
the exponential buckets cannot be represented in classic exposition. So every
latency series showed only `le="+Inf"`: the mean was obtainable, but **p50/p90/p99
were not** (issue #158, found via live perf monitoring). The perf-MCP reducer's
`nativeLadder` path does reconstruct quantiles from the native fields, so the
**MCP** path worked — but the human/promtool **text** path did not, and that is
precisely the surface you want for local tail-latency analysis.

Options considered: (1) a dual aggregation — exponential for OTLP, explicit
buckets for the Prometheus reader; (2) Prometheus native histograms end-to-end
(needs native-histogram scrape config, not zero-config); (3) document the
limitation only (a band-aid). With OTLP metrics unwired there is no exponential
consumer to preserve, so a dual aggregation adds machinery for a benefit nobody
collects.

## Decision

Aggregate the latency instruments as **explicit-bucket histograms**
(`sdkmetric.AggregationExplicitBucketHistogram`) with a single shared boundary
ladder (seconds):

```
0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300
```

The `.001–.005` low end gives the millisecond-scale signals (inter-token gap,
tool queue) real resolution; the `30–300` high end covers minute-scale turns
(the ~41s main turns seen in live monitoring). Explicit-bucket histograms render
as classic `le=` buckets in the Prometheus text exposition, so a plain `curl` /
promtool — and the perf-MCP reducer's `classicLadder` path — yield p50/p90/p99
with **zero scrape config**. The ladder lives once in
`telemetry.latencyBucketBoundaries`, applied to every latency instrument via the
existing `LatencyViews()` (one view per instrument); adding a latency instrument
to `latencyInstruments` inherits the ladder with no per-call-site duplication.

This SUPERSEDES ADR 0018 §5 decision 2 (the exponential/native histogram choice).

## Consequences

- A developer gets usable p50/p90/p99 for every latency histogram off the local
  `/metrics` surface and promtool with no extra configuration; the perf-MCP
  `perf://metrics/summary` reports finite, non-degenerate quantiles instead of
  collapsing to the `+Inf` ceiling.
- We lose the exponential representation's tail precision and unbounded dynamic
  range — quantiles are now bounded by the fixed ladder (a value above 300s
  reports the 300s bound; a quantile resolves to a bucket UPPER bound, never an
  interpolated exact value, which the perf-MCP `QuantileBound.UpperBound` naming
  already states honestly). This cost is currently **unconsumed**: no OTLP metrics
  reader exists, so nothing was aggregating the exponential tail. If an OTLP push
  path is added later and fleet-grade exponential tails are wanted, a dual
  aggregation (exponential on the OTLP reader, explicit buckets on the Prometheus
  reader) becomes the natural follow-up — a new ADR at that point.
- Each latency series now emits ~18 `le=` bucket lines instead of one `+Inf`
  line, a small increase in scrape size — bounded and proportionate.

## See also

- [ADR 0018 — Performance observability](./0018-perf-observability.md) (the
  superseded histogram-aggregation decision is §5 decision 2; the phased-hybrid
  decision and the perf-MCP design are unaffected).
- [ADR 0019 — Long-term performance & resource regression tracking](./0019-perf-tracking.md).
- The living per-subsystem detail in [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
  (telemetry / perf observability), and the perf-MCP companion skill
  `.claude/skills/perf-mcp-interpretation/SKILL.md`.
- The documentation-lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
