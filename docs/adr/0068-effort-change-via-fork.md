# ADR 0068 — Change reasoning effort via conversation fork (keep the transcript)

- Status: Accepted
- Date: 2026-07-21
- Scope: `ForkSession` wire/service gains an optional `reasoning_effort` override; mecatui `/effort` switches effort by fork-resume instead of a transcript-wiping restart
- Supersedes: the confirm-overlay interim shipped under issue #246 (superseded in place — that path is removed, not layered)
- Superseded by: [ADR 0343](./0343-session-configuration-generations.md) only for the replacement session's durable generation identity

## Context

[ADR 0055](./0055-reasoning-effort.md) made reasoning effort a per-session server
setting applied at `CreateSession` via the per-session engine factory re-mint. The
only way to change it mid-conversation was to **restart the session**: the mecatui
`/effort` picker fired the same restart-now handoff as `/models`, which tears down
the session and **wipes the transcript**. That is the wrong shape for the actual
operation — an operator wants to *turn the reasoning dial up/down and keep going*,
not abandon the conversation.

A first PR (issue #246) "fixed" the destructive restart by adding a confirmation
overlay warning that the transcript resets. That was the wrong fix: it made the
data-loss *acknowledged*, not *removed*. The operator still lost the conversation to
change a knob.

[ADR 0065](./0065-conversation-fork.md) already shipped `ForkSession`: create a peer
session from a conversation-history snapshot, inheriting the source's mode,
workspace, limits, and provider/model/profile labels — same provider and model only.
That is exactly the primitive a non-destructive effort switch needs: the transcript
*is* the thing that must survive, and a fork carries it.

## Decision

**Change effort by forking the conversation onto a peer session at the new tier —
never by restarting.**

- `ForkSessionRequest` gains an optional `string reasoning_effort = 3` field, and
  `Service.ForkSession` gains a matching `effortOverride string` parameter. Empty
  (the default) inherits the source's effort verbatim; a non-empty value replaces
  **only** the effort label/engine. **Provider and model ALWAYS inherit** — the
  effort is the ONE permitted selector delta a fork may carry (a fork carries
  provider-private replay blobs, so cross-provider/model stays out of scope, per
  ADR 0065).
- A non-empty override makes the fork need a per-session engine
  (`sessionNeedsPerFactory` fires on the changed selector), which the existing
  rehydrate path builds — the new effort is applied by the same ADR 0055
  engine-factory re-mint, not a new code path.
- It is a **field on fork, not a new RPC.** The fork already mints a peer session,
  copies labels, and rehydrates; the effort is just one more label with an override
  hook. A dedicated `SetEffort` RPC would duplicate the fork's engine-rebind logic
  and still have to solve the "what happens to the transcript" question — which the
  fork answers for free.
- The wire surfaces (gRPC `ForkSession`, HTTP `POST /v1/sessions/{id}/fork`) accept
  the optional field; empty/absent preserves today's inherit behaviour byte-for-byte.
- **mecatui `/effort` applies directly on enter** via a fork-resume handoff
  (`switchEffort`): it ends any in-flight run, persists the pick, drives
  `phaseConnecting`, forks the current session at the new effort, closes the source
  (best-effort, swallowed), and refetches the fork's resolved model (the effort echo
  that drives the header suffix + the picker's ● cursor). The confirm overlay is
  **removed** — a non-destructive switch has nothing to warn about.
- The handoff deliberately does **NOT** `resetSession()` (the divergence from
  `restartOnModel`): the fork carries the conversation, so the client transcript
  (`m.conv`) survives. Usage/context-tokens are zeroed on the fork (a fresh
  aggregate), so the footer self-corrects from the new session's `SessionReadyMsg`
  and the next turn-end refetch.
- **Old-session close discipline:** the source is closed only **after** a successful
  fork, best-effort (orphaning is preferable to blocking the handoff). On a failed
  fork the source is **not** closed — it is still the user's live session — and the
  recoverable `restartFailedMsg` reducer leaves the app idle with enter-to-retry.

## Consequences

**Easier.** An operator can now turn the reasoning dial mid-conversation and keep
the transcript — the operation the feature always should have been. The fork is the
single mechanism (no parallel "effort restart" path), and ADR 0055's engine re-mint
applies the new tier unchanged. The mecatui picker drops a whole overlay state (no
confirm step), simplifying the UI.

**Harder / costs.** The forked session starts with **zeroed `Counters`/`Usage`** (a
fresh aggregate), so a session that had burned budget restarts its budget on the
fork — the accepted trade-off for a clean peer (same as any ADR 0065 fork). Two
sessions briefly coexist (source + fork) until the source is closed; a failed close
orphans the source (the operator can still see/close it via `ListSessions`). The
fork is same-provider/model only, so an effort switch can't also change model — that
stays a `/models` operation (deliberately separate). The mecatui transcript-preservation
is guarded by a dedicated test against re-introducing `resetSession`.

## See also

- [ADR 0065 — conversation fork](./0065-conversation-fork.md) — the primitive this builds on.
- [ADR 0055 — reasoning effort](./0055-reasoning-effort.md) — the per-session effort setting being switched.
- [ADR 0002 — documentation lifecycle](./0002-documentation-lifecycle.md) — the frozen-ADR convention.
- [Architecture — conversation fork](../architecture.md) — the living behaviour doc (mentions the effort override).
- [mecatui TUI](../tui.md) — the `/effort` fork-resume UX.
