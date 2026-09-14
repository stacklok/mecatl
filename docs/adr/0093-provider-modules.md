# ADR 0093 — Ship the real LLM provider adapters as opt-in Go modules under provider/

- Status: Accepted
- Date: 2026-08-04
- Scope: the LLM provider wire-format adapters (anthropic, openai, openaichat, ssefilter), the monorepo module layout, and their release/tag grammar.
- Supersedes: —
- Superseded by: [ADR 0330](./0330-isolated-redis-follow-capacity.md) (Go compatibility floor only)

## Context

The engine module ([ADR 0036](./0036-engine-module.md)) is a deliberately tiny,
importable core — its standalone dependency closure is just `doublestar` +
`robfig/cron` + `go.yaml.in/yaml/v3` + `x/sync` (+ test-only `goleak`). That
closure is a hard product property: an external consumer that `go get`s
`github.com/stacklok/mecatl/engine/agent` inherits only those deps, not mecatl's
heavy root require cone.

But the real LLM provider adapters — the OpenAI Responses adapter, the OpenAI
Chat Completions adapter, the native Anthropic Messages adapter, and the shared
SSE keepalive filter — lived in `internal/adapter/`, where only the root module
could reach them. A consumer embedding the engine (issue #362) who wanted a
working provider had to **re-derive the wire-format logic** (SSE→Chunk
translation, request/response shaping, the keepalive filter, reasoning replay)
by hand. That cost is silent and per-consumer: the bug-for-bug subtleties the
adapters encode (stateless full-replay, the SSE keepalive frame-drop, the
reasoning `Include` dance) are exactly what gets lost in a re-implementation.

The obvious "just put them in the engine module" answer is rejected by ADR
0036's tiny-closure invariant: the adapters pull the heavy provider SDKs
(`anthropic-sdk-go`, `openai-go/v3`), and adding those to the engine's require
would poison every engine consumer's dependency graph with SDKs they may never
use. A flat `mecatl/utils`-style kitchen-sink package has the same problem (one
package, all SDKs, no selectivity) and no versioned identity of its own.

## Decision

Ship the real provider adapters as a **`provider/` subtree of sibling Go
submodules**, each opt-in by import:

```
provider/
  anthropic/   module github.com/stacklok/mecatl/provider/anthropic
  openai/      module github.com/stacklok/mecatl/provider/openai
  openaichat/  module github.com/stacklok/mecatl/provider/openaichat
  ssefilter/   module github.com/stacklok/mecatl/provider/ssefilter
```

Each is its own Go module, `git mv`'d from `internal/adapter/<name>`, with its
own `go.mod`: `go 1.26`, `require github.com/stacklok/mecatl/engine` (+ the
shared `provider/ssefilter` for the two openai-go adapters) + its SDK. The
dependency is **opt-in by import**: a consumer `go get`s exactly the provider(s)
it wants and pulls only that provider's SDK.

- **`replace` directives live in `go.work` (dev) and the root `go.mod` (the
  monorepo consumer), NEVER in a published provider `go.mod`** — a committed
  `replace` breaks downstream `go get`.
- **A provider `go.mod` requires ONLY the engine module (+ `provider/ssefilter`
  for openai/openaichat) + its SDK — never the root module.** The engine is
  published, so it is required at a real version; `provider/ssefilter` is
  required at the placeholder and resolved by `go.work use`/`replace` in dev.
- **Tag grammar: `provider/<name>/vX.Y.Z`** (Go's convention for a module in a
  nested subdirectory, mirroring `engine/v*`). The `v*` release trigger in
  `release.yml` has no leading path segment, so it does NOT fire on these tags —
  no container image is built for a provider release.
- The root module still consumes all four (registry, model lister, demo, tests)
  via `require` + `replace => ./provider/<name>` in the root `go.mod` and
  `use ./provider/*` in `go.work`, exactly the engine pattern.
- **`llmresilience` stays root-side.** It is a harness decorator (retry/breaker
  + stream-idle watchdog), not a provider — it only couples to `openai-go` for
  `*oai.Error` typing with a `StatusCode()` fallback. `providercatalog`,
  `openrouter`, and `openaicompat` also stay root-side (stdlib-only leaves, not
  LLM wire adapters).
- Each provider shares the root `.golangci.yml` depguard allowlist
  (`$gostd` + `engine/{port,prompt,session,tool}` + its SDK + `provider/ssefilter`;
  deny `os` + the root module), so the layering rule travels with the modules.

## Consequences

- **Easier:** a consumer embedding the engine imports a working provider with one
  `go get`, inheriting the adapters' encoded wire-format correctness instead of
  re-deriving it. Each provider's SDK stays out of every non-consumer's graph.
- **Registration machinery per module:** go.work `use` entries, root `go.mod`
  require+replace, Taskfile build/test/lint/tidy/vuln/cover steps, CI jobs +
  cache keys, dependabot gomod entries, depguard rules, and the tag grammar all
  had to be taught about the four modules once. Landing it on all four at once
  amortizes that boilerplate; anthropic alone would have landed the same
  machinery and left the rest as immediate follow-ups touching the same files.
- **Doc-citation churn:** `docs/lint` (`CheckCitations`) gates on dead
  `internal/adapter/...` citations, so the frozen ADRs and living docs that cited
  the moved files were repointed to `provider/<name>/...` (path updated, symbol
  citations kept — the sanctioned anti-drift edit).
- **The standalone proof runs offline; warming it needs read-only git auth.** The
  `GOWORK=off` standalone gate (`test:provider-standalone` + the
  `provider-standalone` CI job) covers `ssefilter` + `anthropic`. Because mecatl
  is a **private/INTERNAL repo**, a module that `require`s another mecatl module
  can only be fetched from the origin WITH auth — `proxy.golang.org` cannot serve
  a private module. So the CI job WARMS the module cache with the workflow's
  read-only `GITHUB_TOKEN` (an `insteadOf` rewrite, scoped to the warm step and
  dropped immediately after), then runs the gate itself fully **offline**
  (`GOPROXY=off`): the proof that the closure is self-contained never touches the
  network, and no credential is present on the proof step. `anthropic` qualifies
  (its `require engine v0.8.0` is a published tag, fetchable with auth);
  `ssefilter` is self-contained. `openai`/`openaichat` are excluded only because
  they `require provider/ssefilter v0.0.0`, which no tag exists to resolve until
  the first `provider/ssefilter/vX.Y.Z` is cut — a one-time bootstrap gap closed
  by cutting the tag. Once mecatl is open-sourced the warm needs no token (the
  proxy serves the modules) and the gate can cover all four. Until then
  `openai`/`openaichat` build + test through the workspace (`GOWORK=on`).

## See also

- [ADR 0036](./0036-engine-module.md) — the engine module boundary and the
  tiny-closure invariant the providers must not poison.
- [ADR 0037](./0037-engine-stability-contract.md) — the engine API-compat gate;
  providers pin a published engine version, so an engine breaking change
  surfaces at provider-release time.
- `docs/architecture.md` + `docs/architecture/providers.md` — the living
  provider reference (adapter paths now under `provider/`).
- Issue #362.
