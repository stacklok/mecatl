# ADR 0062 — Out-of-band approve-once for guardrail blocks

- Status: Accepted
- Date: 2026-06-29
- Scope: the guardrails recovery path — the engine seam (`governance.HookOutcome.AskApproval`, `session.PendingAsk.HookOriginated`, the new optional `port.HookApprovalLearner`, the `engine/agent` preHook→ask seam) and the composition/adapter wiring (`internal/adapter/modelhook` waiver + askable-block outcome, `internal/app` posture-coupling). It REMOVES the ADR-0061 prompt-directive machinery (`OverrideArmer`, the `StartRunContent` scan).
- Supersedes: [ADR 0061](./0061-guardrails-human-override.md)
- Superseded by: none

## Context

[ADR 0061](./0061-guardrails-human-override.md) gave the operator a one-shot recovery from a guardrail false-positive: a `/guardrail-allow` directive on the FIRST line of the genuine user prompt, scanned in `Service.StartRunContent`, armed a session-keyed `OverrideArmer` that the guardrails Runner consumed on the next matching block. The channel separation (arm only from the genuine principal prompt, never from tool content) was the load-bearing security property.

It worked but had real costs. The directive is a bespoke grammar the operator must learn and re-type AHEAD of the block — they must PREDICT which tool the agent will pick and pre-authorize it, then re-issue the whole prompt. It is a second, parallel approval channel that does NOT reuse the permission-ask machinery the harness already has (PauseForApproval → StateAwaiting → EvPermissionAsk → Approve), so every client (mecatui, ACP, a custom gRPC consumer) would need bespoke directive-aware UI to surface it well. And it is strictly one-shot: a recurring legitimate command (a `gh pr merge` the operator does ten times a session) costs ten directives.

The harness ALREADY has the exact primitive a guardrail block wants: an out-of-band approval that pauses the run, surfaces a modal to the human, and resolves with a verdict. A permission ask is precisely "the policy is unsure; ask the human." A guardrail false-positive is precisely "the checker is unsure; ask the human." Reusing one mechanism for both is the obvious shape.

## Decision

**Replace the prompt directive with an out-of-band APPROVE-ONCE flow that reuses the existing permission-ask machinery, plus a session-scoped waiver, plus posture-coupling.**

**1. Askable block (the engine seam).** `governance.HookOutcome` gains `AskApproval bool`, meaningful only on a PreToolUse outcome with `Block==true`: it REFINES a block into an askable block. `engine/agent`'s `preHook` returns a normalized `preHookResult`; when the outcome is `{Block, AskApproval}` and a human approver is attached (`Deps.Interactive`), the dispatcher surfaces it via `askHookApproval` — mint an askID, PauseForApproval → StateAwaiting → emit `EvPermissionAsk` → block on the verdict — exactly like a policy ask. Allow once / Allow & don't ask runs the call (directly, NOT re-running the hook); Deny refuses it. A HEADLESS engine (no human approver) IGNORES `AskApproval`: the block stands as a terminal block (byte-identical, fail-safe). The ask is sequenced one-at-a-time in dispatch (Phase 1 of the read-batch, never the parallel fan-out), so two asks never surface at once.

**2. HookOriginated resume marker.** `session.PendingAsk` gains `HookOriginated bool` (`json:"hook_originated,omitempty"`, SERIALIZED). The cross-process awaiting-resume path (`Engine.ResumeApproval` → `resolvePendingCall`) RE-RUNS `preHook` on an Allow — which for a hook ask would re-block / re-ask in a fresh process that has no in-memory waiver. The serialized marker is the only durable signal that the human already authorized THIS exact blocked call, so `resolvePendingCall` keys a "execute WITHOUT re-running preHook" branch on it. (Unlike the run-scoped `ConfiguredAsk`/`FlooredConfiguredAllow`, this MUST serialize — resume is a fresh process.)

**3. Session waiver — "Allow & don't ask again".** A new OPTIONAL `port.HookApprovalLearner` interface (`LearnHookApproval(ctx, governance.HookEvent)`) is type-asserted on `Deps.Hooks` and called ONLY on a `VerdictAllowAlways` verdict for a hook-originated ask. No method is added to `HookRunner` (that would be a breaking API change). The engine stays generic — it hands the neutral `HookEvent` (SessionID + Tool + Input) to the consumer, which decides the waiver scope. The guardrails adapter implements the learner by arming a session-keyed `WaiverHolder` (tool-exact + Bash-command-substring, the matching shape inherited from the removed `OverrideScope`, now justified by a real human verdict). A matching waiver short-circuits a later Pre block WITHOUT surfacing AND without an LLM call. Session-keying gives child isolation for free. The waiver is in-memory only — NOT persisted across a process restart (the SAFE direction, the same posture as the permstore-rehydrate wart).

**4. Posture-coupling (sub-decision B).** Under posture **yolo ONLY** (the truly-off, gate-free tier that maps to Claude Code's bypassPermissions) every guardrail rule is DEMOTED to advisory (observe-only: log + EvHook, never block, never ask). `strict`/`trusted`/**auto** keep ENFORCING — under `auto` the interactive approve-once ask IS the auto-mode behaviour (the checker blocks, an interactive human allows once, exactly like Claude Code's auto mode), so demoting `auto` would remove that very behaviour. The interactive approve-once path is gated on `Deps.Interactive`, NOT on posture. This is composition-only (`internal/app`'s `demoteForPosture`, the single posture→mode coupling point); posture is operator-tier and guardrails are operator-tier, so there is no project-tier downgrade.

**5. Remove the directive.** The ADR-0061 `OverrideArmer`, `OverrideScope`, `ParseOverrideDirective`, `LooksLikeOverrideDirective`, the model-visible `overrideHint`, and the `StartRunContent` genuine-prompt scan/strip/near-miss are DELETED. `text` flows straight through. The block message carries NO directive grammar.

## Consequences

**Easier / better:**

- One approval mechanism for both policy asks and guardrail blocks. Every client that already renders an `EvPermissionAsk` modal (mecatui, ACP, custom gRPC) gets the guardrail approve-once for free — Allow once / Allow & don't ask / Deny, the same buttons.
- The human acts AT the block, in real time, on the exact tool call — no predicting the tool, no re-typing a directive, no re-issuing the prompt.
- "Allow & don't ask again" makes a recurring legitimate command a one-time decision instead of one directive per occurrence.
- The security posture is preserved and arguably tightened: a waiver arms ONLY from a genuine human verdict routed through the engine's verdict site — there is no prompt-channel scan at all, so a tool result / fetched page / MCP response carrying directive-shaped text can never arm anything.
- Headless stays fail-safe (terminal block) and the cross-process resume is correct (the HookOriginated marker prevents a re-ask).

**Costs:**

- A session waiver is one more piece of in-memory state outliving a call (the `WaiverHolder` map). It is composition/adapter-owned (the loop never imports it), small, and NOT persisted — an un-restarted waiver is lost on restart (the safe direction; a stale waiver never silently survives).
- The engine API grows by two struct fields and one optional interface (all additive/minor; see `engine/CHANGELOG.md`). The new surfaces are generic — they carry no guardrail vocabulary — so the engine stays domain-clean, but they are now part of the stability contract.
- The HookOriginated marker is the only serialized, load-bearing-for-correctness new field: without it a resumed guardrail ask re-asks. It is covered by a snapshot round-trip test.

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature this is the recovery path for.
- [ADR 0061](./0061-guardrails-human-override.md) — the superseded prompt-directive override.
- [ADR 0060](./0060-guardrails-bash-default.md) — the default Bash rule (the most common block an operator will approve-once).
- [ADR 0027](./0027-cloud-native.md) — the awaiting-resume seam (Phase 2) the HookOriginated marker rides.
- `docs/usage/guardrails.md` — the operator-facing approve-once / waiver / posture behaviour.
- [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — the approve-once mechanics (the preHook→ask seam, headless degrade, resume skip, waiver, posture-coupling).
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
