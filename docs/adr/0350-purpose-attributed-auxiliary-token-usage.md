# ADR 0350 — Purpose-attributed auxiliary token usage

- Status: Proposed
- Date: 2026-09-22
- Scope: canonical durable accounting of session-associated auxiliary LLM calls
- Supersedes: none
- Superseded by: none

## Context

[ADR 0307](./0307-canonical-durable-token-accounting.md) makes `token_usage` the canonical durable ledger and deliberately limits its initial closed taxonomy to `main` and `session_title`. The title generator proves that a server-owned auxiliary model call can be attributed to its actual provider/model without changing the normal run budget or result usage.

Several other session-associated auxiliary calls still lose provider-reported `ChunkUsage`, or fold it into `main`. They include compaction summaries, evidence reflection, semantic model routing, the headless child-ask reviewer, guardrail checks, and Parallel winner judging. Folding those calls into `main` misstates both their purpose and, when a slot selected another model, their model attribution. Discarding them leaves the canonical ledger incomplete.

Dream and memory-consolidation planning can consume model tokens but has no source chat session. It cannot honestly be placed in a session ledger; [issue #1791](https://github.com/stacklok/mecatl/issues/1791) tracks its separate accounting boundary. This decision records tokens, not monetary pricing, rate cards, currency conversion, or billing reconciliation.

## Decision

1. Extend the closed `session.UsageKind` taxonomy with these session-associated auxiliary purposes:

   | Usage kind | Model-slot relationship | Owner |
   | --- | --- | --- |
   | `compaction` | `models.slots.compaction` when configured | the session being compacted |
   | `reflection` | `models.slots.reflection` when configured | the source session of the admitted or explicit reflection |
   | `router` | `models.slots.router` when configured | the session whose delegation is classified |
   | `ask_reviewer` | `models.slots.ask-reviewer` when configured | the parent session of the child ask |
   | `guardrail` | `models.slots.guardrail` when configured | the session whose tool hook is checked |
   | `parallel_judge` | no dedicated slot; uses its actual inherited/resolved model | the session that invoked Parallel |

   Existing `main` and `session_title` kinds remain unchanged. Usage kinds describe why a call happened, while slots describe one possible composition-time model-selection mechanism; the names align where doing so is honest but are not mechanically coupled.

2. At the physical-stream boundary, aggregate every `ChunkUsage` emission exactly once for
   the provider attempt that produced it; a retry contributes the reported usage of each distinct
   attempted stream, while a forwarding wrapper must not duplicate a usage value already passed
   upstream. Record that aggregate for each attempted, session-associated auxiliary logical call in
   its purpose bucket under the actual server-selected provider/model. Usage received before a
   terminal provider error or cancellation remains recorded. Accounting is best effort; a crash or
   failure between provider execution and durable session persistence is not retried merely to
   reconstruct billing.

3. Auxiliary kinds remain separate from `main`. They never change `Session.Usage`, the legacy
   snapshot `usage` mirror, normal `EvResult.Usage`, team token budgets, or conversation history.
   The `router` kind is the narrow spend-safety exception: its independent accumulated usage also
   contributes to the invoking session's internal `MaxRunTokens` calculation, without rolling into
   `main` or any client-visible run usage. All other auxiliary kinds are budget-neutral. Existing
   session and summary projections expose the canonical ledger without a new client event or a new
   usage rollup.

4. Server/composition code owns the provider/model attribution and session association. Every
   auxiliary call hands its immutable purpose, provider ID, model ID, owner session ID, and
   aggregate usage to the owner-side recorder; a custom/non-LLM helper with no reported usage adds
   nothing. Helpers receive neither client-selected billing controls nor a registry/credential-routing
   capability. They do not persist prompts, model output, request identifiers, provider error text,
   or credentials as accounting data. If a call has no valid source session, it is documented outside
   this ledger instead of receiving fabricated `unknown` session attribution; `unknown` remains only
   the established model-attribution migration value.

5. The ledger evolves forward only. Existing snapshots retain their established
   `main`/`session_title` and legacy-`unknown` restoration semantics. No historical backfill and no
   new per-attempt usage ledger are introduced. An older writer may discard unknown auxiliary kinds
   after reading and saving a new snapshot; mixed-version writer preservation is intentionally not a
   deployment guarantee. The engine API change is additive: it exposes the new closed `UsageKind`
   constants and updates its compatibility contract/changelog.

## Consequences

The durable ledger becomes an accurate, provider/model-granular account of all currently reachable session-associated auxiliary model work. Operators can later present a purpose breakdown without redefining normal run accounting. The router's existing spend safety remains a narrow internal budget calculation over its separate bucket; internal safety and housekeeping calls otherwise do not charge the chat budget.

The implementation must thread usage out of direct provider calls and ephemeral utility engines without widening `port.LLMRequest` or changing normal child-run accounting. Normal main, Subagent, Parallel-branch, and Team runs continue to account as `main` in their own sessions. Model selection behavior, including the absence of a Parallel-judge slot, remains unchanged.

Non-session dream and consolidation planning stays deliberately outside `Session.tokenUsage` pending #1791. Monetary cost remains a separate design requiring a rate source and pricing semantics.

## See also

- [ADR 0307 — Canonical durable token accounting and run-scoped budgets](./0307-canonical-durable-token-accounting.md)
- [ADR 0308 — Asynchronous session-title generation and durable token usage](./0308-session-title-generation-and-auxiliary-usage.md)
- [ADR 0030 — Layered model-selection heuristics](./0030-model-selection-heuristics.md)
- [Issue #1216](https://github.com/stacklok/mecatl/issues/1216)
- [Issue #1791](https://github.com/stacklok/mecatl/issues/1791)
- [Auxiliary token usage acceptance plan](../acceptance/auxiliary-token-usage.md)
