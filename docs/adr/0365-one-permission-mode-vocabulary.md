# ADR 0365 — One named permission-mode vocabulary, and two admission gates

- Status: Proposed
- Date: 2026-09-22
- Scope: the operator flag surface across all four composition roots (`--posture`, `--yolo`,
  mecatui `--mode`, and the operator-tier `posture:` key), two new boot-time admission gates,
  and the default for the headless child-ask reviewer. NO change to `engine/governance`, to
  `engine/session.PermissionMode`, to any gRPC message, field, enum value, or rpc, to the
  TypeScript SDK's behaviour, or to the guardrails checker itself. The one wire-visible
  correction is that an unspecified `CreateSession` mode honours the server's default mode.
- Amends: [ADR 0022](./0022-allow-all-posture.md) by renaming its operator surface. Its four
  decisions are NOT superseded and carry over verbatim; in particular there is still no new
  `PermissionMode`, and allow-all remains a server-wide operator posture set at process start.
- Related: [ADR 0046](./0046-guardrails-slot-enable.md) (configure = enable, preserved),
  [ADR 0095](./0095-root-aware-project-trust.md) (one root-aware trust fold, preserved),
  [ADR 0021](./0021-guardrails.md) (the checker), [ADR 0062](./0062-guardrails-approve-once.md)
  (the gate-free tier's advisory demotion, preserved).

## Context

Getting mecatl to behave autonomously requires combining two independently-documented knobs,
`--posture` for the rule layer and a guardrails checker model in the user-global settings file.
A review of the contextual-guardrails plan reported the consequence: an operator goes looking
for an autonomous mode, the TUI offers only the session permission modes, `posture` is not
discoverable until `--help`, and the combination that delivers the intended behaviour is the
one an operator is least likely to stumble into. `--posture yolo` reads as the obvious pick for
"just run it" while silently demoting the checker to advisory, and `--posture auto` starts a
fully allow-all deployment with nothing supervising tool content when no checker is configured.

Investigating the same complaint from the subagent side found a second, sharper instance of
it. Under `auto` a subagent keeps the command-substitution guard that the main agent loses
(`mainEvaluatorOptions` passes `WithLooseSubstitution` whenever allow-all is on;
`childEvaluatorOptions` only does so at the gate-free tier). So a subagent shell command
containing `$(...)`, backticks, or a subshell that the classifier cannot prove read-only is
held for approval, and headless with no ask-reviewer configured it is denied outright, while
the main agent's identical command runs. The documented way out is `--subagent-ask-reviewer`,
which is itself undiscoverable; the way an operator will actually find is the gate-free tier,
which silently turns their checker into a log line. The friction and the trap compose.

## Decision

**One named operator vocabulary over the mechanisms that already exist, plus two boot-time
admission gates and an adjudicating default for headless subagent asks.**

### 1. The vocabulary is an enumeration, not a ladder

An earlier draft of this decision modelled the tokens as one ordered ladder
(`plan < default < accept-edits < trusted < auto < yolo`). That was wrong: permission
relaxation and project trust are independent axes, and any total order over them collapses the
2x2 and makes a real combination unexpressible. Specifically, `--posture trusted --mode
accept-edits` (honour the project's rules AND auto-accept edits, prompt for everything else)
has no place on such a ladder.

So `--permission-mode` takes a NAMED token per supported combination, and each token maps to an
exact pair of values in the two mechanisms that already exist:

|Token|`cfg.Posture`|`server.Config.DefaultMode`|
|-|-|-|
|`plan`|strict|plan|
|`default`|strict|default|
|`accept-edits`|strict|accept-edits|
|`trusted`|trusted|default|
|`trusted-accept-edits`|trusted|accept-edits|
|`auto`|auto|default|
|`yolo`|yolo|default|

The table is the contract. It is a lookup, not an ordering, and adding a combination later is a
row rather than a re-ranking. Redundant products are deliberately absent: allow-all already
covers Edit and Write, so an accept-edits variant of `auto` or `yolo` would name a distinction
the evaluator does not make.

Because `trusted` maps to `default`, it keeps its CURRENT meaning exactly and no existing
deployment changes behaviour. The combination the earlier ladder would have destroyed is
`trusted-accept-edits`, which is now nameable for the first time as a single token.

### 2. Both targets are used for their designed purpose

`server.Config.DefaultMode` is documented as "applied when a `CreateSession` request leaves
mode unspecified", which is exactly what the session half of a token sets. `cfg.Posture` set
from a flag is what `--posture` does today. Nothing is repurposed and no interface is bent to a
new use; the flag is a parse plus a table lookup writing two existing fields. One wiring gap
is closed to make that documented purpose true on every transport: the gRPC `CreateSession`
handler collapsed an unspecified mode to `default` before the service could apply
`DefaultMode`, so it now passes an unspecified mode through, as the HTTP path already did.
With no permission mode configured the default is `default`, so no existing client changes.

The scope split is REAL and must stay visible rather than being smoothed over: the posture half
is process-wide and fixed at boot, the mode half is only a default that a session may later
change. `--help`, the generated configuration reference, the help overlay, and the startup
diagnostic all state which half a token sets. They also state that cycling the session mode
never changes the posture half, and whose restart a posture change needs: the operator's own
mecatui relaunch with the embedded server, or the server operator's `mecated` restart under
`mecatui connect`.

### 3. Nothing becomes session-selectable

`session.PermissionMode` keeps its three values and its three strings. No proto enum value, no
engine API symbol, no SDK bridge, and no snapshot migration. A session can still select only
`plan`, `default`, or `acceptEdits`, exactly as today, so no new value becomes reachable from a
client, from a persisted schedule, from a snapshot, or from agent-definition frontmatter.

This is deliberate and it is what keeps ADR 0022 decision 1 intact rather than superseded: the
composition-bearing tiers remain a server-wide operator posture, unreachable from a session and
therefore unreachable from prompt injection. It also preserves, for free, the property that a
repository's agent definitions cannot name a composition-bearing tier, because the type they
parse into has no such value. A rung belongs on the session side only if its increment has a
channel read after the session exists; project ingestion, allow-all, and the substitution
loosenings have none.

### 4. An allow-all token without a checker refuses to start

A token whose posture half is `auto` or `yolo` waives the built-in mutate-ask floor, which
makes the checker the only remaining inspection of tool content. Build REFUSES to start when
no checker is configured by either [ADR 0046](./0046-guardrails-slot-enable.md) enable path and
the kill-switch was not passed, naming every fix.

At the gate-free tier the mandated checker is still demoted to advisory by
`demoteForPosture`, which costs three properties, not one: the pre-tool veto, the ADR 0062
approve-once human ask, and fail-closed on checker failure, so a checker outage there is
indistinguishable from a clean result. The requirement is kept anyway, for operator-visible
findings, and BOTH the refusal message and the startup line state plainly that the checker is
observability-only at that tier. The startup line reports the checker as exactly one of
`enforcing`, `advisory`, or `disabled`, so an advisory checker is never read as protection.
Removing the demotion was considered and rejected as a
separate architectural question: it would supersede ADR 0062 sub-decision B and make the
gate-free tier no longer gate-free.

The gate keys on the resolved token with no exemption for a flag default, so the shipped
allow-all defaults must declare their checker choice explicitly. This change updates them.

### 5. A headless root refuses a token it cannot honour

Under [ADR 0095](./0095-root-aware-project-trust.md) a headless root never gains project trust
from posture. A token whose defining increment is trust (`trusted`, `trusted-accept-edits`)
therefore cannot be honoured there, and Build refuses it after `resolveTrust` so any of the
four legitimate trust sources satisfies the gate. `auto` and `yolo` are NOT refused: trust is
not their defining increment, and headless-allow-all-without-trust is the exact production
state ADR 0095 was written to protect. They WARN instead, naming the withheld trust.

### 6. Headless subagent asks are adjudicated, not denied, by default

An allow-all headless deployment with no `--subagent-ask-reviewer` denies every subagent
substitution the classifier cannot prove read-only. That friction is what pushes an operator
toward the gate-free tier and its silent checker demotion. So the reviewer becomes DEFAULT-ON
for headless allow-all tokens, resolving its model through the existing `ask-reviewer` slot
with the parent-model fallback already implemented; where no model resolves, behaviour is
unchanged and a WARN names the fix.

This is an explicit authority decision, not a convenience: it grants an autonomous approval
capability by default, and it spends tokens per adjudication. It is therefore opt-OUT by an
explicit flag value (`--subagent-ask-reviewer off`, mirroring `--guardrails off`), and
confined to headless allow-all tokens where the alternative is a silent denial. The startup
line says in plain words that a model may approve headless subagent permission requests and
that each adjudication spends tokens, and narrates the reviewer separately from the guardrails
checker, since the two serve different purposes.

## Consequences

Operators learn one vocabulary and read one `--help` entry, and the token names make the
supported combinations explicit instead of requiring the operator to discover that two flags
compose. Two previously-silent under-configurations now refuse to start with an error naming
the fix, and the subagent asymmetry is both stated and adjudicated rather than silently denied.

The cost is a wider token list than a ladder would need, and a flag whose two halves have
different scopes, which the surfaces must keep stating rather than hiding. Nothing is lost:
every combination expressible today remains expressible, and `trusted-accept-edits` is newly
nameable.

The outcome is launch-time configuration and discovery, not runtime selection of autonomy.
Only the three session values stay interactive in the TUI; the four carrying a posture half
need a restart of the process hosting the server, and no surface may present them as
interactive modes.

`engine/governance`, `engine/session`, the gRPC contract, and the SDK are untouched, so the
ADR 0022 invariants that the rejected `ModeYolo` spike broke are not even in scope to regress.

Current behaviour lives in `docs/architecture.md` and
`user-docs/features/permissions-and-posture.md`; shipped state is in the production-readiness
tracker.
