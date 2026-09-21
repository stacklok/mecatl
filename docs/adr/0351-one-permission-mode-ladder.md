# ADR 0351 — One permission-mode ladder: operator ceiling, session selection

- Status: Proposed
- Date: 2026-09-21
- Scope: the operator/session permission vocabulary (`session.PermissionMode`, the proto
  `PermissionMode` enum, the `--posture`/`--yolo`/`--mode` flag surface across all four
  composition roots, and the operator-tier `posture:` YAML key), plus the two boot-time
  admission gates this collapse makes possible. NO change to `engine/governance` (the
  evaluator, deny-dominance, the configured-Ask floor, bash splitting, and the
  substitution classifiers are REUSED unchanged) and NO change to the guardrails
  checker itself.
- Supersedes: the "**No new `PermissionMode`**" clause of
  [ADR 0022](./0022-allow-all-posture.md) (its decision 1) ONLY. ADR 0022's decisions 2
  (subordinate to the governance invariants, no evaluator bypass), 3 (no hardcoded
  command circuit breaker), and 4 (loud, root/sandbox-gated opt-in) are NOT superseded
  and carry over verbatim as binding constraints on this design.
- Related: [ADR 0046](./0046-guardrails-slot-enable.md) (configure = enable, preserved),
  [ADR 0095](./0095-root-aware-project-trust.md) (one root-aware trust fold, preserved),
  [ADR 0021](./0021-guardrails.md) (the checker itself, untouched).

## Context

Three independent operator controls decide how much a session does without asking, and an
operator has to discover all three separately to get the intended behaviour:

- `--posture strict|trusted|auto|yolo` (composition, boot, operator-tier only) selects the
  rule/trust ladder. It is the [ADR 0022](./0022-allow-all-posture.md) ladder.
- `PermissionMode` (`default`/`plan`/`acceptEdits`) is per-session, client-settable, and on
  the wire (`CreateSessionRequest.mode`, `SetMode`, `ApprovePlanRequest.target_mode`).
- `guardrails.model` / `--guardrails-model` / the `guardrail` model slot
  ([ADR 0046](./0046-guardrails-slot-enable.md)) enables the content checker.

Reviewing the contextual-guardrails plan surfaced the failure mode
([PR #1455 review](https://github.com/stacklok/mecatl/pull/1455#discussion_r4062802901)):
an operator looking for an autonomous mode finds only `default` and `accept-edits` in the
TUI, never finds `posture` until someone points at `--help`, and the combination that
actually delivers the intended behaviour is the one they are least likely to stumble into.
Worse, `--posture yolo` looks like the obvious pick for "just run it" while silently
demoting the guardrails checker to advisory, and `--posture auto` starts a fully
allow-all deployment with nothing supervising tool content when no checker is configured.

Two properties make the naive collapse dangerous, and ADR 0022 already paid for learning one
of them:

1. **The two controls have different owners.** Posture is set at process start by whoever
   owns the blast radius. `PermissionMode` is selected by the client, which in an agentic
   harness means it is reachable by prompt injection. ADR 0022 rejected a `ModeYolo` spike
   precisely because it short-circuited `permpolicy.Evaluate` before the governance fold and
   therefore defeated a `ScopeManaged` deny: "a bypass the session can reach is a bypass
   prompt-injection can reach."
2. **Some tier effects are build-time, not per-call.** The allow-all rule and the
   accept-edits rules are per-call `extra` rules the evaluator already folds. Project
   ingestion ([ADR 0095](./0095-root-aware-project-trust.md)) and the main/child
   substitution loosening are composition-time: they shape the prompt, the catalog, and the
   evaluator's construction options. They cannot be flipped per call on a live session.

## Decision

**One ordered ladder is the single permission vocabulary. It binds twice, at two scopes: an
operator-set ceiling at boot, and a session-selected tier clamped to that ceiling. Two new
boot-time gates refuse to start a deployment whose ceiling promises supervision or trust that
is not actually configured.**

### 1. The ladder

```text
plan  <  default  <  accept-edits  <  trusted  <  auto  <  yolo
```

One monotone axis: how much runs without asking. Each rung is the rung below it plus one
increment.

|Tier|Increment over the tier below|
|-|-|
|`plan`|Read-only toolset; mutations hard-denied; `PresentPlan` approval gate|
|`default`|The built-in floor: read allows, mutate asks|
|`accept-edits`|`Edit` and `Write` auto-allowed against the built-in ask floor|
|`trusted`|Project ingestion admitted (the [ADR 0095](./0095-root-aware-project-trust.md) trust fold)|
|`auto`|Allow-all rule (main and children); main substitution floor loosened|
|`yolo`|Child substitution floor loosened (child prompt-injection defence off)|

`trusted` joins the ladder rather than staying a separate axis. Trust was already monotone up
the old posture ladder (`auto` and `yolo` both raise the trust floor on interactive roots), so
the ordering is not new. What is new is the collapse of the 2x2: the combination "project
ingestion admitted while every mutation still prompts", which today's `--posture trusted`
expresses, is no longer reachable. That is the accepted cost of a single vocabulary, and the
deprecating alias warns about it (see 6).

`--trust-project` is NOT folded in. It remains a first-class trust SOURCE, one of the four
[ADR 0095](./0095-root-aware-project-trust.md) sources, and it is the documented fix the
headless gate points at. Only `--posture` and `--yolo` become aliases.

### 2. Two scopes, one vocabulary

- **Ceiling.** `--permission-mode` at every composition root, plus the operator-tier
  `permissionMode:` YAML key. Operator-tier only, exactly as `posture:` is today: a project
  file's key is ignored with a WARN. Default ceiling is `accept-edits`, which is precisely
  today's reachable set for an unconfigured deployment.
- **Session tier.** `CreateSessionRequest.mode`, `SetMode`, `ApprovePlanRequest.target_mode`,
  the mecatui `--mode` start flag, and shift+tab. Clamped to the ceiling.

A session tier ABOVE the ceiling is REFUSED loudly (`InvalidArgument`), never silently
clamped and never granted. This is the ADR 0022 invariant restated in the new vocabulary: the
session selects within the blast radius the operator set, and cannot enlarge it.

The initial session tier is DECLARED per root, never inferred from whether the ceiling flag was
explicitly passed: `mecated` and `mecatui` declare `default`, leaving the client headroom to
cycle; `mecak8s` and `mecatequi` declare their ceiling, because an unattended root has no
client to select a tier. Provenance is not usable as the test: `mecak8s` reaches its `auto`
ceiling through a flag DEFAULT whose `postureFlagSet` bit is false and pinned false by
`cmd/mecak8s/main_test.go`, so an "explicitly set" rule would silently hand the shipped k8s
root an initial tier below its own ceiling. This reproduces every current default exactly
(see 6).

### 3. Build-time knobs derive from the CEILING, never from the live session tier

Project ingestion, the main and child substitution loosening, the guardrails wiring, and the
root/no-sandbox refusal are composition facts derived from the ceiling, exactly as
`applyPosture` derives them from the posture tier today. The per-session tier drives the
per-call fold that `permpolicy.Policy.Evaluate` already performs: the plan-mode hard-deny, the
accept-edits rules, and the allow-all rule contributed as `extra` rules.

The allow-all grant is the ONE OPEN QUESTION in this decision, and it is deliberately left
open rather than settled here. An earlier draft asserted it simply moves out of `applyPosture`
into the per-call fold. That is wrong as written: `childRules` injects the grant at build time
from `cfg.AllowAllTools`, all three child constructors build at `session.ModeDefault`, and
`internal/app/allowall_test.go` pins the grant reaching BOTH the main and child rule sets as an
explicit kill-switch, so a naive move silently drops allow-all for every subagent, parallel
branch, and team member.

The fork is real and it decides how much of this ADR is justified. If the grant stays
build-time from the ceiling, nothing regresses, but `trusted` and `yolo` have no effect at
`permpolicy.Evaluate`, the only place the domain reads the value, so three of six tiers carry
no domain meaning and holding them in a domain type is hard to defend. If it moves per-call
and children inherit their parent's effective tier, the tiers become meaningful but child
semantics change and allow-all becomes session-selectable, which is the shape ADR 0022
decision 1 refused, now as a rule rather than a bypass. The acceptance plan carries the
candidate resolutions and their costs as its one unresolved human decision; whichever is
chosen, the binding constraint is that the grant must keep reaching child engines and must
stay a rule inside the governance fold.

### 4. The guardrails admission gate

A ceiling of `auto` or `yolo` waives the built-in mutate-ask floor, which makes the checker the
only thing left inspecting tool content. Build REFUSES to start when the ceiling is at or above
`auto`, no checker is configured by either [ADR 0046](./0046-guardrails-slot-enable.md) enable
path, and the kill-switch was not passed. The error names every fix:

```text
error: permission mode "auto" removes the approval prompt, but no guardrails checker is
  configured, so nothing would inspect tool content.
  Fix one of:
    --guardrails-model MODEL          enable the checker on a model
    --model-slot guardrail=MODEL      enable the checker on a slot
    --guardrails=off                  proceed deliberately with no checker
```

Configure = enable is PRESERVED: configuring a checker still enables it, and this gate adds
only a tier-conditional REQUIREMENT on top. `--guardrails=off` stays the kill-switch and is the
deliberate opt-out for an unsupervised allow-all deployment.

The gate keys on the EFFECTIVE ceiling, with no exemption for a ceiling that arrived from a
flag default rather than an operator typing it. An exemption would leave exactly the
silently-unsupervised deployment this ADR exists to abolish, and would abolish it only for
operators who had already thought about it. The consequence is that the shipped allow-all
defaults must declare their choice out loud, and this change updates them: the `mecak8s` flag
default, `.github/actions/mecatequi/action.yml` (which defaults `posture: auto` with an empty
`guardrails-model`), and `.github/workflows/mecatequi.yml`. A deployment that wants to run
unsupervised still can; it just has to say so where the next reader can see it.

### 5. The headless trust gate

Under [ADR 0095](./0095-root-aware-project-trust.md) a headless root never gains project trust
from the tier. With trust on the ladder, `--permission-mode trusted` on a headless root would
name a tier whose defining and only increment is withheld. Build REFUSES it, after
`resolveTrust`, so any of the four legitimate trust sources satisfies the gate:

```text
error: permission mode "trusted" adds project trust and nothing else, but this is a headless
  root, which never gains project trust from the tier.
  Fix one of:
    --trust-project                   vouch for this repository and its .git
    trustedWorkspaces:                declare it in the operator settings file
    --permission-mode accept-edits    proceed without project trust
```

The gate fires for `trusted` ONLY. At `auto` and `yolo` trust is not the defining increment,
and headless-auto-without-trust is the exact production state ADR 0095 was written to protect
("headless schedulers need `--posture auto` for unattended permission behavior, but must not
automatically trust a freshly cloned repository"). Refusing there would undo ADR 0095 and break
the shipped `mecak8s` default. Those tiers instead emit a WARN naming the withheld trust, which
is the legibility fix the review asked for.

### 6. One clamp chokepoint, and ingested content cannot name authority

Widening the grammar widens every parser that reads it, and a mode enters the system at ten
independent points: the gRPC create/setmode/approveplan mapper, two HTTP decoders, the ACP
`session/set_mode` bridge, agent-definition frontmatter, persisted schedule specs, snapshot
restore, fork and clear successors, delegation children, and the SDK and TUI clients. Only the
first three were in the original statement of the clamp.

The domain is not a backstop. `session.Session.SetMode` validates the state transition and
assigns the value unchecked. So the clamp is specified as a property of ONE chokepoint,
`Service.clampMode`, called by every Service entry point, which is why HTTP and ACP inherit it
instead of each re-deriving it, with a structural guard that fails when a new ingress appears
that does not reach it.

`session.ParsePermissionMode` is a TOKEN GRAMMAR and nothing more. A successful parse is never
permission to use the tier. The one call site that deliberately does NOT share it is
agent-definition frontmatter: `resolvePermissionMode` keeps its explicit allowlist of
`default`, `plan`, and `acceptEdits`, warning and falling back otherwise, so a
composition-bearing tier is structurally unnameable from repository content at ANY trust level.
This preserves today's behaviour exactly rather than adding a restriction, and it is deliberate
that project trust does not unlock it: trust admits a repository's instructions, not its
authority over the ladder that governs them. The "one shared grammar" discipline this ADR takes
from `ParsePosture` applies to operator and client surfaces; applied naively to the agent-def
allowlist it would convert a safe closed set into the full widened grammar, which is exactly
the class of change this section exists to prevent.

Persisted ingress is asymmetric with live ingress, deliberately. A live client request above the
ceiling is REFUSED, because the client can retry with a valid value. A persisted mode above the
ceiling, arriving from snapshot restore, a successor, or a schedule fire, CLAMPS DOWN with a
WARN, because a stored session cannot retry and refusing would brick it. Both fail safe; only
one can fail loud.

### 7. Compatibility

Purely additive on the wire, deprecating on the CLI, for one release.

- `PERMISSION_MODE_TRUSTED = 4`, `PERMISSION_MODE_AUTO = 5`, `PERMISSION_MODE_YOLO = 6` are
  ADDED. Values 1 to 3 are unchanged and unrenumbered, so existing gRPC/Connect clients stay
  wire-compatible by protobuf's own unknown-enum tolerance. That tolerance does NOT extend to
  the TypeScript SDK's HTTP transport, which bridges the enum through two hand-written CLOSED
  maps in `sdk/typescript/src/http.ts` that coerce an unrecognised value to `"default"`
  outbound and to `0`/UNSPECIFIED inbound. Left alone they would report a fully allow-all
  session as having no mode at all, so this change extends both. Additive numbering is a
  necessary condition for compatibility here, not a sufficient one.
- `session.PermissionMode` gains `ModeTrusted`, `ModeAuto`, `ModeYolo`. The three existing
  string values, including the persisted `"acceptEdits"`, are unchanged, so no snapshot
  migration is required. Added = minor under `engine/COMPATIBILITY.md`.
- `--posture` and `--yolo` keep working as aliases onto the ladder and log a deprecation WARN
  naming the replacement. `--posture trusted` additionally warns that the tier now also
  auto-accepts edits. Removal is a later Cleanup, classified separately.
- `--trust-project`, `--guardrails-model`, `--guardrails`, and the mecatui `--mode` start flag
  are NOT deprecated. `--mode` is the initial-tier binding; it simply gains the new tokens.

Every current default is reproduced: an unconfigured interactive `mecated` gets ceiling
`accept-edits` and initial `default`, so shift+tab still cycles plan/default/accept-edits; an
unconfigured headless `mecated` gets the same ceiling and the same `default` initial tier;
`mecak8s`, whose flag defaults to `auto`, gets ceiling and initial `auto`.

## Consequences

Operators learn one vocabulary and read one `--help` entry. The TUI cycles the same tokens the
server flag takes, so the autonomous tier is discoverable where the review said operators
actually look. Two deployments that were silently under-configured now refuse to start with an
error naming the fix, which converts the review's "hard to discover" into "impossible to miss".

The cost is the lost 2x2 corner: `trusted` no longer means "project rules honoured, mutations
still prompt". Deployments relying on that see a behaviour change, warned by the alias for one
release. Trust also becomes reachable by naming a tier, which makes the headless gate load-bearing
rather than cosmetic.

`engine/governance` is untouched, every tier still resolves through `governance.Evaluate`, and
the ADR 0022 invariants that the rejected `ModeYolo` spike broke remain machine-checked.

Current behaviour lives in `docs/architecture.md` and
`user-docs/features/permissions-and-posture.md`; shipped state is in the production-readiness
tracker.
