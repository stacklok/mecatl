# ADR 0308 — Asynchronous session-title generation and durable token usage

- Status: Proposed
- Date: 2026-08-30
- Scope: session-title lifecycle and provenance, mecatui title UX, title-specific server operation, composition model-slot selection, and durable token accounting
- Supersedes: none
- Superseded by: none

## Context

A session title currently comes directly from the first genuine user prompt, while an operator can later rename an idle main session through the sessions inventory. The raw prompt is often too long or insufficiently specific, and the rename interaction is hard to discover during a chat.

A title generator needs a model call after a normal exchange without delaying or altering the main agent run. The frontend must not receive provider credentials or choose billable models. The server already owns session authorization, persistence, provider construction, and composition-owned `models.slots` resolution. Several existing non-run model calls either discard their usage or fold it into main session usage, whose token budget applies to normal agent runs. The system has no durable, operation-attributed account for that spend.

## Decision

Add a mecatui-only `/title` command. `/title <text>` optimistically updates the active session display and sends the existing `RenameSession` gRPC operation to the server; the server response remains authoritative and a rejected request restores/refetches the authoritative title. `/title` with no text reports the current title and usage. An explicit rename records operator provenance and permanently prevents automatic title generation for that session.

Automatic title generation is a server-owned session feature, not a client-requested RPC. At creation, composition determines whether the explicit `models.slots.title` binding resolves for the session's fixed provider. The aggregate persists `TitleGeneration=pending` only in that case; otherwise it persists `disabled`. This is durable session intent and lifecycle state, not persisted model configuration: every attempt still checks current composition policy. The loop records only the first three genuine, non-empty principal text prompts as bounded title-source prompts at the original prompt-recording seam; it never reconstructs them from generic user-role history, which can include harness continuations and be compacted.

On a successful exchange that adds an eligible source prompt, the Service submits the session to a bounded server-owned title-generation coordinator. The coordinator is owned by the Build/Service lifecycle, has bounded admission and workers, deduplicates by session and durable attempt ID, is stopped/cancelled/joined on shutdown, and is entered in ADR-0027's resource/restart inventory. A title job claims one eligible attempt under the session-management lock and lease, snapshots source prompts, and releases exclusion before the provider call. A concurrent completion/job makes no second call. After the provider returns, the job reacquires exclusion, reloads, revalidates eligibility and ownership, then atomically stores the terminal attempt record and (only when eligible) the generated title.

The Service consumes a host-private `SessionTitleGenerator` seam. At each invocation composition resolves the persisted session's provider/model binding and passes the generator one already-selected `port.LLMProvider`, resolved title model, and bounded title-source prompts. The generator does not receive a provider registry, provider ID, aliases, slots, credentials, a session store, or mutation authority, and therefore cannot route the call elsewhere. The core `LLMProvider` is provider-scoped; its model is supplied in the provider-neutral `LLMRequest`, and no existing first-class callable provider/model/effort binding exists. Reasoning effort remains a provider-construction concern; its title-call policy is deferred. The standard implementation makes one tool-less bounded direct `LLMProvider.Stream` call through the existing configured provider adapter, following the fenced input, strict output, timeout, and usage capture discipline of `agent.EvidenceReflector`, but does not run the normal `agent.Engine` loop.

The generator produces one strict result: `title` with a valid title, or `defer`. On `defer`, the next successful exchange that adds an eligible candidate may submit another job until three genuine non-empty text prompts have been considered. A valid generated title becomes durable model provenance and ends the lifecycle. A known timeout or provider failure may consume the one durable retry allowance; malformed output, an absent/unresolvable title slot, cancellation, a crash-interrupted claimed attempt, or exhaustion after prompt three leaves the deterministic first-prompt title in place and ends automatic attempts. A crash-interrupted attempt records an unknown/interrupted terminal outcome rather than reissuing a possibly billable call. Server-side conditional mutation prevents a late completion from overwriting an operator title, a generated title, or a deleted/ineligible session.

`models.slots.title` is an opt-in slot with no default-tier fallback: it is recognized by slot validation but intentionally excluded from `slotDefaultTier`; an absent binding disables title generation rather than spending on the session/default/cheap model. As with other slots, configuration resolves aliases in composition and remains subject to the existing operator/project binding and allowlist policy.

Every persisted title or title-generation state change emits a new `session.title` event after save, carrying the authoritative title, provenance, title-generation state, and bounded latest attempt—never prompt text or provider error text. The Service appends it to the durable EventLog when configured and publishes it to the existing gRPC `StreamSessionLive` subscribers. Mecatui and other clients subscribe for active/open sessions, apply the event immediately, and re-fetch `GetSession` on stream reconnect or session reopen. Live subscribers are intentionally best-effort (a full queue can drop an event), so snapshot re-fetch remains the authoritative reconciliation path; this ADR does not claim exactly-once push delivery. Per-run HTTP SSE has no out-of-band title live push: HTTP clients discover title changes from the authoritative snapshot and durable event stream.

The engine owns only durable session truth: `SessionTitle` is the canonical generated-title provenance and lifecycle projection, guarded aggregate title mutation, bounded title-source/lifecycle state, and the `session.title` event value. Title lifecycle attempts retain only identity, outcome, and time. The legacy bare title/provenance fields and `Session.Usage` snapshot projection are dual-written deprecated compatibility fields. The canonical `token_usage` ledger maps a usage kind to a total plus opaque server-produced provider/model attribution entries; every total is the sum of its model entries. Legacy records without exact attribution use `unknown`, never a current-model guess. Title usage records the composition-selected title provider/model under `session_title`; it does not add usage to the main bucket, `MaxRunTokens` budget, or normal `EvResult.Usage`. Authoritative session metadata passed to event-sourced reconstruction carries title lifecycle and ledger state as it already carries title/provenance. Other model callers are deliberately not migrated in this ADR.

## Consequences

Operators get an easy immediate title override and, when explicitly configured, concise model-generated labels without title generation delaying or changing the chat. Credentials, ownership, provider selection, and billable-model controls remain server/composition concerns. The asynchronous completion is race-safe and restart-safe because eligibility and attempts persist with the session.

The feature creates optional extra model spend, a bounded asynchronous coordinator, snapshot/event/API changes, configuration, and client state. The coordinator adds shutdown and restart responsibilities; its gRPC live notification is best-effort, so clients must reconcile with the stored snapshot after reconnect, while HTTP clients use snapshots and the durable event stream. Canonical token usage records tokens but does not claim a currency estimate: pricing, currencies, and historical rate changes need a separate policy. Future model-backed capabilities add their own public methods and server-owned coordinators only after defining their authorization, bounded input projection, slot, output validation, lifecycle, accounting, and notification behavior.

## See also

- [ADR 0030 — Layered model-selection heuristics](./0030-model-selection-heuristics.md)
- [ADR 0016 — Multi-provider composition](./0016-multi-provider.md)
- [ADR 0020 — Diagnostics](./0020-diagnostics.md)
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md)
- [Session title generation and token usage acceptance plan](../acceptance/session-title-generation.md) — scenario proofs, including coordinator shutdown and accounting
- [Issue #621](https://github.com/stacklok/mecatl/issues/621)
