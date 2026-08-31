## 16. Live e2e suite

A live, ginkgo-driven end-to-end suite lives under `e2e/` (build tag `e2e` —
`task build`/`task test`/`task lint` never compile it). It spawns
`bin/mecated` against **OpenRouter** and drives real model runs over the gRPC
`Converse` stream, so it costs (a little) real money and needs the network.
Full detail — scenarios, environment knobs, the permission posture, the
prompt-filter caveats — is in `e2e/README.md`.

Run it locally:

```sh
# Export the key yourself, in-shell, from wherever you keep it — the Taskfile
# and the suite never read a key file, and the key must never appear on a
# logged command line.
export OPENROUTER_API_KEY=...
task e2e
```

What the scenarios prove (event-stream / side-effect assertions, never model
prose): a provider smoke canary, global + workspace skill activation, parallel
`Subagent` fan-out, the `Parallel` construct, a 3-member team with recorded
findings, `/metrics` counters, user + project memory writes, and the soul
composition fact.

Artifacts: every run writes JSONL transcripts per scenario plus the captured
`mecated.log` under `.scratch/e2e-<timestamp>-<rand>/artifacts/` (the scratch
root is kept on failure — the transcripts are the deliverable of a failed
run). A failing spec attaches a self-diagnosing report (network vs model vs
harness classification, transcript path, usage); the suite ends with a
cumulative token/cost estimate.

Remote target (the future-cloud env contract): point `MECATL_E2E_TARGET` at an
existing `host:port` to skip the local spawn; `MECATL_E2E_WORKSPACE` (required)
is the absolute workspace root on the server host, `MECATL_E2E_METRICS_URL`
enables the metrics spec, `MECATL_E2E_AUTH_TOKEN` supplies a bearer token.
Remote runs write artifacts to `.scratch/e2e-artifacts/`.

In CI the suite runs as the **non-blocking**
[`e2e-live` workflow](../../.github/workflows/e2e-live.yml) (nightly + manual
dispatch + the `e2e-live` PR label).

---

See also: the [gRPC API](grpc-api.md) and the [HTTP/SSE API](http-sse-api.md)
the suite drives, or the [operator guide index](../usage.md).

