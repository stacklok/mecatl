# ADR 0059 — MCP typed tool results

- Status: Accepted
- Date: 2026-06-30
- Scope: `engine/session`, `internal/adapter/mcp`, `internal/app`, `contracts/proto`
- Supersedes: none
- Superseded by: none

## Context

Issue #223. An MCP `CallToolResult` is a typed, audience-aware content array
(`content[]`: text, image, audio, `resource`/`EmbeddedResource`,
`resource_link`) plus an optional `structuredContent` JSON value (object, array,
or primitive); the tool
definition may carry an `outputSchema`. mecatl collapses all of that to a single
string and discards the rest.

The choke point is `flattenContent` (`internal/adapter/mcp/tool.go`)
(`flattenContent`): a `TextContent` is kept verbatim, but `ImageContent`/
`AudioContent` lose their `Data` (only a MIME-type bracket note survives),
`ResourceLink` keeps only the URI (losing `Name`/`Title`/`Description`/
`MIMEType`/`Size`/`Annotations` incl. `audience`), `EmbeddedResource` collapses
to the literal `[embedded resource]` (the inline body the server shipped is
dropped), and `res.StructuredContent` and `outputSchema` are never read at all.

The architectural root cause is that the domain value object
`session.ToolResult` (`engine/session/toolcall.go`) (`ToolResult`) is
`{Content string, IsError bool}` — string-only, with no structured-content
channel. The gRPC `ToolResult` proto mirrors that. By the time a result reaches
the loop, the relay, or any client, everything is already a flat string.

The round-trip is broken against our own server: `internal/adapter/mcpperf`
(`tools.go`) deliberately emits user-audience `ResourceLink` content
(`userResourceLink`) so a human-facing client can render a downloadable artifact
while the model-facing text omits the raw path — but when mecatl-the-client
consumes that same server, `flattenContent` strips the audience annotation and
hands the model a bare URI it cannot resolve.

## Decision

Carry the MCP typed content as **the domain's own neutral type** on
`session.ToolResult`, reconstructed by the existing event-sourced fold, with
audience/capability routing and all untrusted-server defenses in composition and
the adapter. Ten converged decisions:

1. **Typed content blocks are the domain's own neutral type, not MCP types.**
   Generalize the existing `session.Content` (`engine/session/content.go`)
   (`Content`) and add a `Parts []Content` field to `session.ToolResult`
   (`engine/session/toolcall.go`) (`ToolResult`). No `mcpsdk.*` imports in
   `engine/` — the layering rule (ADR 0036) holds. The MCP adapter maps its
   `mcpsdk.Content` union into `session.Content` at the adapter boundary; the
   domain never learns the MCP taxonomy.

2. **Typed content rides `EvToolResult.ToolResult`, not a relay sidecar.**
   The blocks live on the domain `ToolResult`, so `engine/adapter/eventsource`
   (`eventsource.go`) (`Fold`) reconstructs them with no sidecar and no
   re-coupling. The loop stays storage-agnostic: it only emits the event; the
   relay persists it as it does today.

3. **Audience/capability routing lives in COMPOSITION, not the MCP adapter.**
   `flattenContent` cannot know the provider's modalities — that is the SINGLE
   composition-computed intersection at `internal/app/capability.go`
   (`modelCapability`) over `Service.ProviderCapabilities()`. The MCP adapter
   produces a neutral structured payload plus a default model-facing string;
   composition decides per-result whether to upgrade typed blocks to
   `session.Content` parts for the request.

4. **`audience` is advisory display routing only — it NEVER suppresses
   model-facing content.** The MCP server is an untrusted supply-chain surface
   (CWE-345); trusting `audience:["user"]` to *suppress* the model copy inverts
   the trust model (a server hides an injection payload, or routes a secret
   into model context). A `["user"]` block may render an *additional*
   human-facing copy; the model copy is always present (TextContent parity).
   Untrusted-fencing/redaction runs regardless of audience.

5. **NEVER auto-dereference server-returned `resource_link` URIs (SSRF,
   CWE-918).** A server pointing at an internal/metadata host is the threat
   actor. v1 surfaces a `resource_link` as a string reference (URI + name +
   description + MIME). Only `https://` may ever be client-fetched, and only
   through `engine/session/content.go` (`ValidateMediaURL`) — absolute https,
   IP-deny, redirect re-validation, no cross-origin credential attachment.
   Non-`https` schemes stay server-readonly.

6. **Size bound FIRST (issue #178).** `internal/adapter/toolkit` (`toolkit.go`)
   (`MaxOutputBytes`) / (`Truncate`) is applied to MCP results — including the
   new structured fields — with the truncation marker preserved. The bound
   lands before the typed-block widening so the durable log's implicit size
   ceiling (`.events.jsonl` / Redis EventLog) is not punched through, and
   memory-DoS across replicas stays bounded.

7. **Do NOT validate `StructuredContent` with `session.ValidateJSON`.**
   `engine/session/jsonschema.go` (`ValidateJSON`) is a deliberate fail-open
   *subset* validator for model-authored structured output (Subagent
   `SubmitResult`); MCP `outputSchema` is arbitrary server-provided full JSON
   Schema and `ValidateJSON` would silently under-enforce, creating a second,
   weaker path. For optional defense-in-depth client validation use
   `github.com/google/jsonschema-go` (direct dep, MIT) — never hand-roll a
   third validator.

8. **Reuse the existing validating constructors — no second validation path.**
   `engine/session/content.go` (`NewContent`) and (`ValidateMediaParts`) are
   the single choke point the ACP adapter already uses for prompt media. The
   MCP adapter routes image/audio/resource blocks through those same
   constructors; the per-type switch differs, the per-part validation does not.

9. **`NewToolResult`/`NewToolError` stay 2-arg; the new path is additive.**
   ~200 call sites across `engine/agent`, `engine/adapter/*`, `internal/adapter/*`,
   and external consumers depend on the 2-arg constructors. Widening them is
   breaking (Changed); an additive `Parts` field with a builder for the new
   path is Added/minor. `Content` is the legacy model-facing string; `Parts`
   is preferred when non-empty, and the struct documents the precedence.
   api-compat gate classifies this Added (minor).

10. **This ADR is the contract.** Landing it triggers `task api:update` (the
    additive `ToolResult.Parts` surface), `task generate` (proto), `task docs`
    (llms.txt + the strict link gate), and a CLOUD-NATIVE.md re-audit (see
    [ADR 0027](./0027-cloud-native.md) "Re-audit: MCP typed tool results
    (#223)").

## Consequences

- **Additive domain/proto fields.** `session.ToolResult.Parts` and the mirrored
  proto `ToolResult.blocks` are additive; legacy sessions round-trip with a
  zero-value `Parts` (string-only), so `engine/adapter/sessnap` and
  `engine/adapter/eventsource` (`Fold`) load old snapshots/events unchanged.
  The migration is noted in `engine/CHANGELOG.md`.
- **The mcpperf round-trip is fixed.** A user-audience `ResourceLink` from
  `internal/adapter/mcpperf` (`tools.go`) reaches the client as a typed block
  with its metadata intact, and the model still gets the TextContent parity
  copy — the server's spec-correct pattern no longer collides with the
  client's flattener.
- **The MCP adapter grows a mapping layer** in place of `flattenContent`: a
  per-type switch over `mcpsdk.Content` producing `session.Content` parts (for
  image/audio/resource) and string references (for `resource_link`), plus a
  default model-facing string. The per-part validation is shared (decision 8);
  the size bound is shared (decision 6).
- **`FetchMcpResource` (Phase 2) introduces an `http.Client`** for the
  model-facing affordance to fetch an `https://` `resource_link`. It MUST be
  per-call (bounded, no cross-origin creds) or, if cached/reused, inventoried
  in ADR 0027 List 1 — it is the one outlives-a-call resource this feature can
  introduce, and only if Phase 2 caches it.
- **Cost honestly:** the loop, the relay, and the TUI tool card all gain a
  typed branch they did not have. The `audience` axis is a mecatl-specific
  extension beyond the spec's model-facing contract, surfaced to clients as
  advisory display routing (never a suppression control). The defense-in-depth
  `outputSchema` SHOULD-validate is optional; the spec puts validation
  server-side, and the SDK's `CallTool` does not validate client-side.

## Deferred (out of scope v1)

- Resource `subscribe` / `notifications/resources/updated` — deferred to ADR
  0057's Phase 2; captured content is point-in-time.
- Resource templates.
- `priority` / `lastModified` consumption beyond carry-through (stored on the
  block, not acted on).
- **The backward-compat TextContent mirror for `structuredContent` is REQUIRED
  (spec SHOULD):** when `structuredContent` is present, a parallel TextContent
  block carrying the same JSON is emitted so older clients/models see it.
- **`outputSchema` client SHOULD-validate is optional defense-in-depth** (see
  decision 7); conformance is the SHOULD half only, not assumed.

## See also

- [ADR 0027](./0027-cloud-native.md) — the re-audit stub for this change.
- [ADR 0036](./0036-engine-module.md) / [ADR 0037](./0037-engine-stability-contract.md)
  — the engine module + API-compat gate the additive `Parts` field rides.
- [ADR 0038](./0038-event-sourced-rehydration.md) — `Fold` reconstructs typed
  blocks from the durable log.
- [ADR 0057](./0057-mcp-server-notifications.md) — resource `updated` is its
  Phase 2.
- [ADR 0001](./0001-acp-adapter.md) — the `NewContent`/`ValidateMediaParts`
  choke point this reuses.
