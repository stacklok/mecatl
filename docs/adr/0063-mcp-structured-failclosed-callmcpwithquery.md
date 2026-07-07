# ADR 0063 — MCP structured results: fail-closed + CallMcpWithQuery

- Status: Accepted
- Date: 2026-07-01
- Scope: `internal/adapter/mcp`, `internal/app`
- Supersedes: none
- Superseded by: none

## Context

ADR 0059 carried MCP typed content as the domain's own neutral type and applied the
shared size bound (`toolkit.MaxOutputBytes`, issue #178) to MCP results — including
the new structured fields — *before* the typed-block widening. For an **unstructured
text** result that bound is honest: a truncated string with a marker is still a string
the model can read. For a **structured (JSON)** result it is not. Truncating a JSON
blob mid-token leaves the model with a partial, unparseable fragment it cannot reason
over — the size bound fires, but the output is useless, and the model has no signal
that narrowing would help.

The choke point is `internal/adapter/toolkit/toolkit.go` (`Truncate`), reached from
`remoteTool.Execute` in `internal/adapter/mcp/tool.go`. A structured MCP result that
exceeds `MaxOutputBytes` is today truncated into an unparseable tail.

Other harnesses solve the "big JSON result" problem by **persisting the full result to
a scratch file and running `jq(1)` over it**. mecatl cannot take that path: it runs
**cloud-native**. `mecak8s` (ADR 0048) is storage-free — no PVC, no local disk — and
the durable event log already carries an implicit size ceiling that a disk-spilled
result would still have to cross on the way back into context. A solution that depends
on a local filesystem is not portable across the FS and no-FS/cloud-native profiles
(ADR 0048, issue #55), and `toolkit.Truncate` must keep running first so the durable
log's ceiling is never punched through.

## Decision

Two tiers, both **environment-agnostic** (no local-disk dependency anywhere):

1. **Fail-closed on a structured result that exceeds the output cap.**
   `remoteTool.Execute` (`internal/adapter/mcp/tool.go`) returns an **actionable tool
   error** instead of a truncated JSON blob when a structured result is over
   `MaxOutputBytes`. The error names the two escape hatches — narrow/paginate the
   remote call using the remote tool's own filter/pagination parameters, or run the
   call through `CallMcpWithQuery` with a jq filter — so the model has a recovery path,
   not a dead end. A result is "structured" if **any** of three signals fire (OR'd):
   the remote tool advertised an `outputSchema`; the result carried
   `StructuredContent`; or a content block is JSON by MIME (`application/json`,
   `text/json`, any `+json`) or by text-parse (trimmed text starts with `{`/`[` and
   `json.Unmarshal`s). The text-parse probe only runs when the result is already
   over-cap, so the cost is paid only when needed. **Unstructured text still truncates
   with a marker** — the existing honest behaviour for non-JSON output is unchanged.
   Error results (`res.IsError`) are deliberately NOT fail-closed: an error payload
   stays string-only and truncates as today, so the model still reads the error text
   and self-corrects.

2. **`CallMcpWithQuery` meta-tool** (`internal/adapter/mcp/callmcpwithquery.go`):
   calls a remote MCP tool and filters its JSON result through a **jq expression in
   memory (no disk)** before the result enters context, so a large JSON response does
   not have to be narrowed or truncated into an unparseable blob. It is read-only
   (calls a remote tool that mutates nothing on this process, filters in memory), so
   it slots into read-parallel dispatch and survives the plan-mode catalog filter, and
   it works in **both** the FS and no-FS/cloud-native profiles. jq is provided by a
   sandboxed wrapper around `github.com/itchyny/gojq` (pure Go, MIT) in
   `internal/adapter/mcp/jq/jq.go`: the query is built via `gojq.Parse` and run with
   `gojq.RunWithContext` **without** `WithModuleLoader`/`WithInputIter`/
   `WithEnvironLoader`, so it cannot read files, stdin, or the environment; a context
   deadline bounds pathological compute (`while(1; .+1)`), defaulting to 5s when the
   caller's ctx carries no deadline; input ≤ 20 MiB (`MaxInputBytes`), output ≤ ~100
   KiB (`MaxOutputBytes`) so a too-broad filter does not simply move the context-budget
   problem from input to output. The JSON input fed to jq is chosen by **precedence**:
   `StructuredContent` (the typed view) first, then the first
   JSON-parseable `Text` content block, else a **loud error** (a non-JSON result is not
   silently filtered). A remote tool-level error (`IsError`) is surfaced verbatim
   (truncated) **pre-filter** — the model asked to filter a failed call, so it is told
   the call failed.

The two tiers compose: the fail-closed error points the model at `CallMcpWithQuery`,
and `CallMcpWithQuery` returns the narrowed subset within the same output cap.

## Consequences

- The model **never receives truncated JSON**. It gets either an actionable error
  naming the two escape hatches, or a filtered subset small enough to be useful. The
  size bound still runs first on the filtered output (`toolkit.Truncate`), so the
  durable-log ceiling and memory-DoS bound (issue #178) hold.
- `Provider.CallTool` + `CallResult` (`internal/adapter/mcp/calltool.go`) widen the
  MCP adapter so `CallMcpWithQuery` can fetch the **untruncated** raw result (a jq
  filter needs the full JSON to narrow). This is an **internal adapter** widening —
  no `engine/` API, no `port.LLMRequest` field, no proto change. `CallResult` carries
  no `mcpsdk` types, so `internal/app` can consume it without the SDK dependency.
- `gojq` is a **new root-module dependency** (pure Go, MIT). The engine module's tiny
  dep closure (ADR 0036) is untouched — `internal/adapter/mcp/jq` is host-repo only.
- `CallMcpWithQuery` is registered in `internal/app/catalog.go` (`mountGlobalMCP`) in
  **both** profiles (no disk), gated on the manager exposing ≥1 tool (meaningless
  otherwise); it is a floor-`Allow` (`ScopeBuiltinDefault`, config-overridable to
  ask/deny) in `internal/app/build.go`, same posture as `WebSearch`/`FetchMcpResource`
  — an outbound read; and it joins the guardrail **default block set** (pre+post,
  mirroring `mcp__*`) in `internal/app/guardrails.go`, since its outbound args (exfil
  into the remote call body) and inbound results (injection in the filtered response)
  are the same class as a direct `mcp__*` call.
- **Cloud-native portable.** No disk anywhere — the fail-closed error is a string, the
  jq filter runs in memory, and both paths work in the no-FS profile (issue #55) and on
  the storage-free `mecak8s` agent (ADR 0048).
- **`structuredContent` may be any valid JSON value** (object, array, or primitive),
  not just an object — the MCP spec's "JSON object" is a SHOULD. The optional
  `outputSchema` validation (`internal/adapter/mcp/tool.go` (`validateStructuredContent`))
  suppresses a top-level type mismatch (e.g. an array against an object-only schema);
  field-level violations inside a matching shape still surface as a warning.
- **Cost honestly:** a `CallMcpWithQuery` call fetches the full remote result before
  filtering, so it does not save bandwidth/latency on the remote hop — it saves the
  *context budget*. The model should prefer narrowing the remote call with its own
  pagination/filter parameters when possible; `CallMcpWithQuery` is the escape hatch
  when the remote tool offers no such parameters.

## Rejected alternatives

- **Persist the over-cap result to a scratch file + run `jq(1)` over it.** Not
  cloud-native-portable: `mecak8s` is storage-free (ADR 0048), and the no-FS profile
  (issue #55) has no filesystem to spill to. Also reintroduces a `jq(1)` process spawn
  (the no-spawn portability rule).
- **Hand-rolled JSON-path selector** instead of real jq. Not real jq — no `select`,
  `map`, `|`, or recursion; a second, weaker query language the model has to learn.
- **Shelling out to `jq(1)`.** Violates the no-spawn portability rule and adds a
  system dependency; gojq is pure Go and sandboxable.
- **A general `QueryJson` tool** over any JSON source. Needs a disk-backed source (a
  file path), redundant with `CallMcpWithQuery` for the MCP case, and widens the
  attack surface (arbitrary file read) for no gain.

## See also

- [ADR 0059](./0059-mcp-typed-tool-results.md) — MCP typed tool results; this is the
  over-cap follow-on for the structured case.
- [ADR 0048](./0048-mecak8s.md) — the storage-free, cloud-native posture that rules
  out disk-spill.
- [ADR 0027](./0027-cloud-native.md) — the cloud-native arc; no new outlives-a-call
  resource is introduced here (jq runs per-call, no goroutine/cache/LRU).
