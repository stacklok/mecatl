# Measuring performance

This page maps performance questions to the measurement technique that answers
them, and to what Mecatl actually runs for each. For catching regressions between
commits, see [performance regression tracking](perf-tracking.md); for operator
flags and endpoints, see
[observability and resilience](../user-docs/building/what-you-get/observability.md).

## Pick the clock first

A streaming agent spends most of its wall time blocked on the model and on tool
I/O. A CPU profile of a slow session usually looks idle and explains nothing about
latency. Decide whether the question is about CPU time, wall-clock time, or time
spent blocked before choosing a tool.

Turn latency is several numbers, not one. Mecatl's latency histograms record time
to first token, inter-token gaps, total turn duration, tool duration, and
tool queue time separately. Queue time matters because mutating tools run one at a
time: a slow edit delays every mutation queued behind it. Measuring from execution
start would hide that wait (coordinated omission), so the tool queue histogram
measures from enqueue to execution start.

## Question to technique

| Question | Technique | What Mecatl uses |
| --- | --- | --- |
| Did a commit add allocations, tokens, or cache misses? | Offline benchmarks with a stored baseline | `task bench`, `task perf:scenarios`, and the CI gate in [perf tracking](perf-tracking.md) |
| Is turn or tool latency trending worse? | Aggregated histograms | OpenTelemetry metrics exported as Prometheus `/metrics`, with an explicit bucket ladder so quantiles work without scrape config |
| Is the process leaking goroutines or growing its heap? | `runtime/metrics` counters | The OpenTelemetry runtime collector on `/metrics`, plus an opt-in goroutine-count warning |
| Where is CPU or allocation going right now? | Sampling profiles | `net/http/pprof` on the loopback admin listener; mutex and block profiles need their opt-in flags |
| What led up to one slow turn? | Execution trace | A flight recorder, on by default, keeps a bounded recent window; a snapshot is read on demand |
| Is memory growing outside the Go heap? | Process RSS | Resident set size read from `/proc` on Linux, alongside the heap figures |
| Does a test leave goroutines running? | Leak check at test exit | `goleak.VerifyTestMain` in the packages that start per-run goroutines |
| Can an agent diagnose a live process? | Reduced numeric summaries | The opt-in perf MCP server |

Only process RSS sees memory the Go runtime does not manage, such as cgo or WASM
allocations. Heap profiles and `runtime/metrics` report such a leak as nothing, and
`GOMEMLIMIT` cannot bound it.

## Numbers for agents, pictures for humans

An agent can reason over numbers it can parse, not over a flame graph or a trace
timeline. `runtime/metrics`, the Prometheus exposition, and `expvar` at
`/debug/vars` are numeric already. CPU profiles and execution traces are binary
blobs that become useful only after a parse step ranks them.

The perf MCP server (`internal/adapter/mcpperf`) is built on that split. Its tools
and resources return reduced numeric summaries: histogram quantile bounds, a ranked
top-N of functions from a parsed profile, the runtime snapshot, and the slow-turn
list. Raw profiles and flight-recorder traces are offered only as links to the
admin endpoints, so a multi-megabyte blob never enters model context. The
[perf-mcp-interpretation skill](../.claude/skills/perf-mcp-interpretation/SKILL.md)
covers reading its output.

## Where the live surface lives

`telemetry.NewAdminMux` in `internal/adapter/telemetry` builds one admin surface:
`/metrics`, `/debug/pprof/`, `/debug/vars`, and, when a flight recorder is armed,
`/debug/flightrecorder`. `mecated` and the server that `mecatui` embeds both use it,
so the two expose the same endpoints; `mecak8s` serves it without the flight
recorder. The perf MCP server mounts at `/mcp` on the same listener.

That surface is unauthenticated and must stay on loopback (or, for `mecatui`, a
private UNIX socket). Profiles, traces, and goroutine dumps can contain prompt text,
file paths, and stack data. The pprof handlers are registered on this mux
explicitly rather than through the package's import side effect, which would put
them on `http.DefaultServeMux`. The runtime snapshot reads `runtime/metrics`
rather than `runtime.ReadMemStats`, which stops the world, and the slow-turn ring
buffer stores only scalar timings, never prompt or tool content.

## What Mecatl doesn't use

There is no continuous profiling backend, no eBPF tooling, and no `runtime/trace`
task or region annotations in the loop; the flight recorder captures the default
execution trace. Wall-clock benchmark numbers are recorded as advisory trends, not
gated, because shared CI runners are too noisy for them.

## Related

- [Performance regression tracking](perf-tracking.md)
- [Observability](architecture/observability.md)
- [Agent loop](architecture/agent-loop.md)
- [Observability and resilience](../user-docs/building/what-you-get/observability.md)
