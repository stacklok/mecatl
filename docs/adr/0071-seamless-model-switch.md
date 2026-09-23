# ADR 0071 — Seamless model switch: always keep the conversation

- Status: Accepted
- Date: 2026-07-23
- Scope: the `/models` picker switch UX (mecatui), the `CreateSession.source_session_id` carryover surface (server), and the new `session.StripProviderState` domain transform (engine/session)
- Supersedes: the "Mid-session model switch" + "Same-provider history carryover" decisions in [ADR 0016](./0016-multi-provider.md) (the confirm-overlay UX and the same-provider-only gate), and the "same-provider constraint" note for carryover in [ADR 0065](./0065-conversation-fork.md)
- Superseded by: [ADR 0343](./0343-session-configuration-generations.md) only for the replacement session's durable generation identity

## Context

The first cut of issue #20 (shipped as `f5a31fa4`) added model-switch carryover
behind a confirm overlay: picking a model offered `[enter]` restart-fresh,
`[c]` carry-over (same-provider only), `[s]` switch-next-time, `[esc]` undo.
Two forces made that the wrong shape:

1. **The fresh-vs-carry choice is redundant with `/clear`.** Dropping the
   conversation is already a first-class, explicit action (the `/clear` slash
   command). Offering a *second*, hidden fresh-start inside the model switcher
   duplicates it and makes the destructive path the default keystroke
   (`[enter]`). The overlay was a speed bump for a reversible action — a model
   switch can always be undone by switching again.
2. **The same-provider restriction was over-cautious.** v1 rejected
   cross-provider carryover outright, deferring it to a "v2" that would need a
   reasoning-blob strip. The fear (recorded in ADR 0016 and ADR 0065) was that
   replaying one provider's encrypted reasoning blob to another provider is
   "HTTP 400 at worst." That is true of a *verbatim* cross-provider replay —
   but it misses that a *stripped* history is safe: both the OpenAI and
   Anthropic adapters treat an **empty** replay blob as "no blob" and omit it
   on the wire (OpenAI `request.go` guards `if m.Reasoning != ""`,
   `if call.ItemID != ""`, `if m.ProviderPhase != ""`; Anthropic
   `unpackReasoning("")` returns nil). The provider-private state is exactly
   three fields — `Message.Reasoning`, `Message.ProviderPhase`, and
   `ToolCall.ItemID`; the conversation *content* (roles, text, tool-call
   IDs/Args, tool results, media parts) is provider-neutral and fully
   portable. The 400 ADR 0016 worried about is a *request-config* condition
   (e.g. Anthropic extended-thinking requiring thinking blocks on a
   tool-bearing turn), driven by the **new** model's per-request thinking
   config — which the adapter re-derives from the new model, not from the
   stripped history.

The user-facing principle that settled it: **losing your conversation is
always the surprising, destructive outcome** — nobody picks a new model
expecting their work to vanish. Keeping context should be the default for
*every* switch; dropping it belongs to exactly one verb (`/clear`).

## Decision

A model switch **always keeps the conversation**. Picking a model in the
`/models` picker switches immediately — no confirm overlay, no `[c]` key — and
the new session is seeded from the current one via the existing
`session.ForkSnapshot` + `SeedHistory` primitives (zero engine-loop change;
`CreateSessionRequest.source_session_id`, additive proto field 8).

- **Same provider** → the snapshot replays **verbatim** (`Reasoning` /
  `ProviderPhase` / `ToolCall.ItemID` intact, warm prompt-cache prefix).
- **Cross provider** → the snapshot is **stripped** to a provider-neutral copy
  by the new `session.StripProviderState` (`engine/session/conversation.go`),
  a pure, non-mutating transform clearing the three provider-private fields.
  The new session's thinking/reasoning config is derived from the new model,
  so a stripped history replays safely to any provider.

**Cross-provider onto OpenAI synthesizes stable ItemIDs.** The OpenAI
`store:false` replay de-duplicates `function_call` items by the provider `id`
(`fc_N`); an id-less carried item collides with the auto-assigned `fc_N` on
the *second* post-switch turn (Azure GPT-5.x "Duplicate item" 400). So when
the new provider is OpenAI, `validateCarryover` synthesizes deterministic,
non-`fc_`-prefixed `carryover_item_N` ids on the carried tool calls (unique
within the history, stable across re-creates). Anthropic ignores `ItemID`, so
non-OpenAI destinations stay bare-stripped. The synthesis lives in
**composition** (`internal/adapter/server/service.go`), not the domain —
`StripProviderState` stays a pure strip, provider-agnostic.

**The confirm overlay is removed.** `chooseModel` routes directly: a live
session switches via `restartOnModelWithCarryover`
(`CreateSessionWithCarryover`); no live session falls back to a plain create.
A one-shot status note names the switch ("switched to \<model\> — conversation
kept"; cross-provider adds "prior reasoning cache dropped" so the cold-cache /
dropped thinking continuity is not silent). `/clear` is untouched and is the
documented fresh-start.

The strip-vs-verbatim decision lives in composition (`validateCarryover`),
which is the only layer that knows both resolved provider ids. The domain owns
only the pure transform.

## Consequences

**Easier.** The common model switch (same-provider, e.g. GPT-5.x → 5.5) is
zero-friction — one keystroke, conversation continues. Cross-provider switches
(e.g. OpenAI → Anthropic mid-task because the model is struggling) keep the
thread instead of forcing a cold start. No second, hidden fresh-start to
stumble into; `/clear` is the single obvious "drop my context" verb. The
server-side `source_session_id` surface is unchanged in shape — only the
gate relaxed — so the wire is additive and backward-compatible.

**Harder / costs.** Cross-provider carryover is **lossy**: the carried history
keeps its text and tool records but drops the prior model's reasoning
continuity and its warm prompt-cache prefix (a cold first turn on the new
model). This is honest — surfaced in the status note — but it is a real cost
a same-provider switch does not pay. The OpenAI ItemID synthesis is verified
offline (ids present, stable, collision-free in prefix space) but **not
against a live Azure endpoint**; a live `task e2e` run would confirm the
provider's de-dup accepts the synthesized ids. Removing the overlay also
retires the "switch next time" pending-next affordance from the picker (the
`headerNextBadge` `next:` badge still serves the global-default `ctrl+g`
path); a switch is now always immediate, which is a behavior change for any
operator who used "switch next time" deliberately. ADR 0016's mid-session
section and ADR 0065's same-provider carryover note are superseded (their
bodies are frozen; the pointer above is the record).

## See also

- [ADR 0065 — Conversation fork](./0065-conversation-fork.md) — the
  `ForkSnapshot`/`SeedHistory` primitives and the fork same-provider
  constraint this relaxes for the *carryover* path (the `ForkSession` RPC
  itself remains same-provider/model-locked; that is a separate operation).
- [ADR 0016 — Multi-provider](./0016-multi-provider.md) — the superseded
  mid-session-switch overlay and same-provider carryover gate.
- [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) —
  the living "Model-switch context carryover" section.
- [`docs/tui.md`](../tui.md) — the `/models` picker section (seamless switch,
  `/clear` as fresh-start).
- [`docs/design/PRODUCTION-READINESS.md`](../design/PRODUCTION-READINESS.md) —
  the status tracker.
