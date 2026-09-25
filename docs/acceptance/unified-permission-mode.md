# Unified permission mode — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - it changes the operator/deployment surface of all four composition roots, adds two boot refusals that stop shipped defaults from starting, and flips the headless child-ask reviewer to default-on, which grants an autonomous approval capability by default.
**Decision record:** [ADR 0365](../adr/0365-one-permission-mode-vocabulary.md)
**Phase:** operator surface consolidation
**Status:** proposed, 2026-09-22. Provenance: the [PR #1455 review comment](https://github.com/stacklok/mecatl/pull/1455#discussion_r4062802901) reporting that the two-knob posture plus guardrails setup is undiscoverable and that the gate-free tier silently demotes the checker, plus an investigation of the same complaint from the subagent side recorded in ADR 0365.
**Delivery:** Split. Six scenarios spanning composition, four command roots, the TUI, and user documentation, including two refusals that stop currently-valid deployments from booting and a default that grants autonomous approval; the human contract review is the point of the exercise.
**Expected tasks:** 6
**Issue:** none yet. The review comment above is the provenance and the plan PR references it as ordinary text.
**Plan PR:** https://github.com/stacklok/mecatl/pull/1730
**Approved baseline:** absent until approved

Give operators one named vocabulary for the permission behaviour they want, over the two
mechanisms that already carry it, and refuse to start a deployment that promises supervision or
project trust it does not actually have. Each token maps to an exact pair of existing values,
so nothing becomes session-selectable, no wire or domain type changes, and every combination
expressible today stays expressible.

The delivered outcome is narrower than the originating request, and every surface must say so.
This plan makes autonomous startup understandable: one launch-time vocabulary, refusals for
under-configured deployments, and a header and help overlay that show what is running. It does
NOT make autonomy selectable from inside the TUI. Only the three session values stay
interactive; the four values carrying a posture half are launch-time settings, and nothing in
this plan presents all seven as interactive modes.

The smallest demonstrable behavior: `mecated serve --permission-mode auto` with no guardrails
checker refuses to start and names every fix; with one it starts, reports both halves of the
token on one startup line, and adjudicates headless subagent substitution asks instead of
denying them.

## The vocabulary at a glance

What each value does. Deny rules and any deliberately configured ask still win at every value,
including `yolo`.

|Value|Project instructions and rules|Edits|Shell and other mutations|Command substitution in subagents|
|-|-|-|-|-|
|`plan`|not loaded|denied|denied|n/a, read-only|
|`default`|not loaded|asks|asks|asks|
|`accept-edits`|not loaded|auto-accepted|asks|asks|
|`trusted`|loaded|asks|asks|asks|
|`trusted-accept-edits`|loaded|auto-accepted|asks|asks|
|`auto`|loaded|allowed|allowed|adjudicated by reviewer|
|`yolo`|loaded|allowed|allowed|allowed|

What each value sets, and the lifetime of each half. The posture half is fixed when the
process starts; the session half is only a starting point a session may later change.

|Value|`cfg.Posture` (process-wide)|`server.Config.DefaultMode` (session start)|
|-|-|-|
|`plan`|strict|plan|
|`default`|strict|default|
|`accept-edits`|strict|accept-edits|
|`trusted`|trusted|default|
|`trusted-accept-edits`|trusted|accept-edits|
|`auto`|auto|default|
|`yolo`|yolo|default|

What is reachable at runtime. Only the three session values are cyclable, exactly as today; the
four carrying a posture half need a restart. This is a known gap against the originating
requirement and is recorded as a resolved decision below rather than left implicit.

|Value|Cyclable in the TUI|Needs a restart|
|-|-|-|
|`plan`|yes|no|
|`default`|yes|no|
|`accept-edits`|yes|no|
|`trusted`|no|yes|
|`trusted-accept-edits`|no|yes|
|`auto`|no|yes|
|`yolo`|no|yes|

"Needs a restart" means a restart of the process that hosts the server, which is not always the
operator's own process. With the embedded server, the operator relaunches mecatui with the new
value. Under `mecatui connect`, relaunching mecatui changes nothing: the server operator changes
`mecated`'s configuration and restarts it. Surfaces must name the right one of the two.

Cycling the session mode never changes the posture half. An operator who starts with `auto` and
cycles to `default`, or through `plan` and back, is still under allow-all, and the header keeps
saying so at every step.

Defaults: `default` on every root except `mecak8s`, which keeps `auto` to reproduce its current
behaviour. The same values are accepted by the operator-tier `permissionMode:` key.

Two values can refuse to start. `auto` and `yolo` refuse when no guardrails checker is
configured and the kill-switch was not passed. `trusted` and `trusted-accept-edits` refuse on a
headless root with no trust source, while `auto` and `yolo` warn and start there.

Deliberately absent: an accept-edits variant of `auto` or `yolo`, because allow-all already
permits edits and the token would name a distinction the evaluator does not make.

## Human decisions

- [x] Whether one setting can replace posture and permission mode without bending either mechanism - Decision: yes, and it was verified rather than assumed. `server.Config.DefaultMode` is documented as applied when a CreateSession request leaves mode unspecified, which is exactly the session half; `cfg.Posture` set from a flag is what `--posture` already does. The flag is a parse plus a table lookup writing two existing fields, so no interface is used for a purpose it was not designed for.
- [x] Whether the vocabulary is an ordered ladder or a named enumeration - Decision: a named enumeration. An ordered ladder was drafted first and is wrong: permission relaxation and project trust are independent axes, and any total order collapses them and makes `--posture trusted --mode accept-edits` unexpressible. Named tokens per supported combination lose nothing, keep `trusted` at its current meaning so no deployment changes behaviour, and make `trusted-accept-edits` nameable for the first time.
- [x] Whether the composition-bearing tiers become session-selectable - Decision: no. They have no channel read after a session exists, so selecting one per session could not mean anything without building per-session policy construction, which is out of scope. Keeping them operator-only preserves ADR 0022 decision 1 intact rather than superseding it, and means no proto value, engine API symbol, SDK bridge, or snapshot migration, and no new value reachable from a client, a schedule, a snapshot, or agent-definition frontmatter.
- [x] Which tokens require a configured guardrails checker - Decision: those whose posture half is `auto` or `yolo`, gated on the resolved token with no exemption for a flag default. The shipped allow-all defaults therefore declare their checker choice explicitly in this same change.
- [x] What the checker requirement means at the gate-free tier, where a configured checker is demoted to advisory - Decision: still require it, and state plainly in both the refusal and the startup line that it is observability-only there. Verified in code that the demotion costs the pre-tool veto, the approve-once human ask, and fail-closed on checker failure, so a checker outage there is indistinguishable from a clean result. Removing the demotion was rejected as a separate architectural question. Because requiring a checker while removing its veto risks false reassurance, the startup line reports the checker as exactly one of three named states, `enforcing`, `advisory`, or `disabled`, so an advisory checker at the gate-free tier can never be read as protection. In mecatui, whose startup diagnostics go to a log file, the help overlay carries the same fact statically for `yolo`; it needs no wire state because the checker refusal guarantees a checker is either configured or explicitly disabled there.
- [x] What a headless root does when a token names project trust it cannot be granted - Decision: refuse after the trust fold, so any legitimate trust source satisfies it, for the two trust-naming tokens only. `auto` and `yolo` warn and start, because headless allow-all without trust is the production state ADR 0095 exists to protect.
- [x] Whether the headless subagent ask reviewer becomes default-on - Decision: yes, for headless allow-all tokens, resolving through the existing ask-reviewer slot with its parent-model fallback. This is an authority decision, not a convenience: it grants an autonomous approval capability by default and spends tokens per adjudication, so it is opt-out by `--subagent-ask-reviewer off`, mirroring the existing `--guardrails off` kill-switch, and the startup line says in plain words that a model may approve headless subagent permission requests, that each adjudication spends tokens, and how to opt out. The reviewer and the guardrails checker serve different purposes, so they are narrated as two separate items under their own names and neither is described in the other's terms. It is confined to the case where the alternative is a silent denial, which is the friction that pushes operators toward the gate-free tier and its silent checker demotion.
- [x] Whether all seven tokens are selectable from the TUI at runtime, which the originating requirement asked for - Decision: no, and the gap is accepted knowingly rather than deferred silently. Only the three session tokens stay cyclable, exactly as today; the four carrying a posture half need a restart. The reason is structural: project ingestion happens once before any session exists, and allow-all and the substitution loosenings are compiled into a permission evaluator built once per process and shared, so a runtime control for those four would be wired to nothing. Closing it properly needs per-session policy construction and per-session ingestion, which is larger than the whole of this plan and belongs in its own. Restarting the embedded server on change was rejected: it works only when mecatui hosts its own server, not when connected to a remote one, and it presents a process restart as a mode toggle. The mitigation is honesty, not a workaround: the help overlay carries all seven with the exact invocation, and the header shows the active posture, so the operator can always see what they are running under and what to type to change it. That guidance names whose restart it is: with the embedded server the operator relaunches mecatui, while under `mecatui connect` the server operator must change `mecated`'s configuration and restart it, so the overlay never offers a local invocation as if it could change a remote server. The delivered outcome is described accordingly everywhere: better discovery and launch-time configuration, not runtime selection of autonomy.
- [x] How the existing surface is retired - Decision: `--permission-mode` replaces `--posture`, `--yolo`, and the mecatui `--mode`, which keep working as deprecated aliases for one release with a WARN naming the replacement. The operator-tier `posture:` key behaves the same way. `--trust-project` is NOT deprecated: it stays a first-class trust source and is what the headless refusal points at.

## Interface contract

- **gRPC / protobuf:** No message, field, enum value, or rpc changes - nothing becomes session-selectable, so `PermissionMode` keeps its four enum values and their numbers. A client, a persisted schedule, a snapshot, and a successor session all continue to carry only the three existing values. One documented semantic correction: a `CreateSession` request that leaves `mode` UNSPECIFIED now starts in the server's configured default mode (the session half of the token) instead of always `default`. `server.Config.DefaultMode` was already documented that way and the HTTP create path already behaved that way, but the gRPC handler collapsed UNSPECIFIED to `default` first, so the session half would never have reached a gRPC client. The field comment on `CreateSessionRequest.mode` changes accordingly; the generated Go, TypeScript, and schema-reference comments follow. `SetMode` is unchanged: UNSPECIFIED there still means `default`. With no permission mode configured the default is `default`, so existing clients see no change.
- **Exported Go APIs / interfaces:** No exported `engine/` surface changes. `engine/session.PermissionMode` keeps `ModeDefault`, `ModePlan`, and `ModeAccept` with their exact current strings, so `task api:check` stays green with no `task api:update` and no `engine/CHANGELOG.md` entry. In `internal/app`, unexported additions only: a token type with its parse and its lookup table onto the existing `Posture` and `session.PermissionMode` pair.
- **Tool schemas:** None - no model-facing tool gains, loses, or changes a field. The vocabulary is an operator surface; the model continues to see only allow, ask, or deny outcomes.
- **CLI / config:** New `--permission-mode` on `mecated`, `mecatui`, `mecak8s`, and `mecatequi`, taking exactly `plan`, `default`, `accept-edits`, `trusted`, `trusted-accept-edits`, `auto`, or `yolo`. Each maps to an exact pair per the ADR table. Default is `default` on every root except `mecak8s`, whose default becomes `auto` to reproduce its current `--posture auto` default. New operator-tier `permissionMode:` key parsed strictly, project-tier occurrences ignored with a WARN, mirroring `posture:`. `--posture`, `--yolo`, and mecatui `--mode` become deprecated aliases that still resolve with a WARN; `--trust-project`, `--guardrails-model`, `--guardrails`, and `--model-slot` are unchanged. `--subagent-ask-reviewer` gains a default for headless allow-all tokens and the explicit opt-out value `off`, mirroring `--guardrails off`. Two boot refusals exit non-zero before serving: an allow-all token with no checker and no kill-switch, and a trust-naming token on a headless root with no trust source. The shipped allow-all defaults declare their checker choice: the `deploy/helm/mecak8s` chart passes `--permission-mode=auto` and gains a `guardrails.model` value that renders `--guardrails-model` with that model when set and an explicit `--guardrails=off` when empty; `.github/actions/mecatequi/action.yml` gains a `permission-mode` input (empty means `auto`, so existing `posture:` callers keep working; setting both fails the step) with `posture` kept as a deprecated alias, and passes `--guardrails off` when `guardrails-model` is empty; `.github/workflows/mecatequi.yml` switches to `permission-mode: "auto"`. The `mecak8s` binary itself keeps its `auto` default, so a bare `mecak8s` run with no checker decision now refuses to start through the ordinary checker refusal. Under `mecatui connect`, a token whose posture half is not strict is a startup error, since that posture belongs to the remote server; `plan`, `default`, and `accept-edits` still set the mode of the sessions that client creates. With no mode flag, mecatui requests an unspecified mode so the server's default applies.
- **Events / persistence:** None - no persisted field changes. `sessnap.Snapshot` continues to store one of the three existing mode strings, schedules continue to persist one of those three, and old snapshots load unchanged. The resolved token is a composition fact reported by the existing build-once startup diagnostic, not a new event or a stored value.
- **Security / authority:** The composition-bearing tiers stay operator-only and unreachable from any session, preserving ADR 0022 decision 1 rather than superseding it, so no new value is reachable from a client or from prompt injection. Agent-definition frontmatter keeps its existing closed allowlist and gains the property for free, since the tiers never enter the type it parses into. The headless subagent reviewer becoming default-on is the one authority expansion in this change: it grants an autonomous approval capability by default, bounded to headless allow-all deployments, opt-out by flag, stated at startup, and subject to the existing per-run denial breaker. Deny-dominance, the configured-Ask floor, and the root-and-no-sandbox refusal are unchanged.
- **Compatibility / migration:** No wire, domain, persistence, or SDK compatibility surface is touched, so there is nothing to version. Every current default is reproduced and every combination expressible today stays expressible; `trusted` keeps its exact current meaning, so unlike the rejected ladder there is no behaviour change for existing `--posture trusted` deployments. The deliberate exceptions are the two new refusals, which stop a currently-valid but under-configured deployment from booting, and the reviewer default. Old flags work as deprecated aliases for one release; their removal is a later Cleanup, classified separately.

## In scope - 6 scenarios, in implementation order

### Scenario 1 - one named vocabulary over the mechanisms that already exist

Each token maps to an exact pair of existing values, so the flag is a parse plus a lookup and
neither mechanism is bent to a new purpose. See
[ADR 0365](../adr/0365-one-permission-mode-vocabulary.md).

**Acceptance:**
- AC1.1: Each of the seven tokens resolves to exactly the posture and default-session-mode pair the ADR table names, and an unknown token is a startup error naming the valid set.
  - verify: `TestADR_0365_TokenTableResolvesExactPairs`
- AC1.2: `trusted` resolves to the trusted posture with the default session mode, so a deployment using it today sees no behaviour change.
  - verify: `TestADR_0365_TrustedKeepsCurrentMeaning`
- AC1.3: `trusted-accept-edits` resolves to the trusted posture with the accept-edits session mode, the combination that was previously only reachable by passing two flags.
  - verify: `TestADR_0365_TrustedAcceptEditsIsNameable`
- AC1.4: The flag writes only the two existing fields and introduces no third source of permission state.
  - verify: inspection - the guarantee is the absence of a new mechanism, which is proved by reading the resolution path rather than by exercising it.

### Scenario 2 - an allow-all token without a checker refuses to start

The gap the originating review reported: allow-all with nothing supervising tool content. It
builds on the configure-equals-enable model of
[ADR 0046](../adr/0046-guardrails-slot-enable.md) by adding a token-conditional requirement.

**Acceptance:**
- AC2.1: A token whose posture half is `auto` or `yolo`, with no checker configured by either enable path and no kill-switch, makes `Build` fail and the process exit non-zero before serving.
  - verify: `TestADR_0365_AllowAllTokenRequiresChecker`
- AC2.2: The refusal names the token and all three fixes: the checker flag, the `guardrail` model slot, and the kill-switch.
  - verify: `TestADR_0365_CheckerRefusalNamesEveryFix`
- AC2.3: The gate keys on the resolved token with no exemption for a flag default, and every shipped allow-all default boots because it declares its checker choice explicitly.
  - verify: `TestADR_0365_ShippedAllowAllDefaultsDeclareCheckerChoice`
- AC2.4: At the gate-free token both the refusal and the startup line state that a configured checker is observability-only there, naming the veto, the human ask, and fail-closed as the properties it does not have.
  - verify: `TestADR_0365_GateFreeTokenStatesCheckerIsAdvisoryOnly`
- AC2.5: A token whose posture half is `strict` or `trusted` never requires a checker.
  - verify: `TestADR_0365_NonAllowAllTokensRequireNoChecker`
- AC2.6: The startup line reports the checker as exactly one of `enforcing`, `advisory`, or `disabled`, so a configured checker at the gate-free tier reads `advisory` and never as protection.
  - verify: `TestADR_0365_StartupLineNamesCheckerState`

### Scenario 3 - a headless root refuses a token it cannot honour

[ADR 0095](../adr/0095-root-aware-project-trust.md) keeps a headless root from gaining project
trust from posture, so a token whose defining increment is trust cannot be honoured there.

**Acceptance:**
- AC3.1: `trusted` or `trusted-accept-edits` on a headless root with no trust source makes `Build` fail, naming the trust flag, the declarative setting, and a non-trust token.
  - verify: `TestADR_0365_HeadlessTrustTokenRefused`
- AC3.2: The same tokens start when any legitimate trust source is present, because the gate runs after the trust fold.
  - verify: `TestADR_0365_HeadlessTrustTokenAcceptsEveryTrustSource`
- AC3.3: `auto` and `yolo` on a headless root with no trust source still start, still admit no project ingestion, and WARN naming the withheld trust.
  - verify: `TestADR_0365_HeadlessAllowAllStartsWithoutTrust`
- AC3.4: A trust-naming token on an interactive root grants project trust with no refusal and no WARN.
  - verify: `TestADR_0365_InteractiveTrustTokenGrantsTrust`

### Scenario 4 - the subagent restriction is visible, and adjudicated rather than denied

Under allow-all a subagent keeps the command-substitution guard the main agent loses, so a
substitution the classifier cannot prove read-only is denied headless while the main agent's
identical command runs. That friction is what pushes an operator toward the gate-free tier and
its silent checker demotion, as recorded in
[ADR 0365](../adr/0365-one-permission-mode-vocabulary.md) and the
[AGENTS.md](../../AGENTS.md) four-step child-ask model.

**Acceptance:**
- AC4.1: An allow-all token narrates the subagent asymmetry at startup, stating that subagents keep the substitution guard and naming the reviewer as the supported way to adjudicate rather than deny.
  - verify: `TestADR_0365_AllowAllNarratesSubagentAsymmetry`
- AC4.2: A headless allow-all deployment with no explicit reviewer value engages the reviewer by default, resolving its model through the existing slot with the parent-model fallback.
  - verify: `TestADR_0365_HeadlessAllowAllDefaultsReviewerOn`
- AC4.3: `--subagent-ask-reviewer off` restores today's behaviour. When the default is in effect, the startup line says in plain words that a model may approve headless subagent permission requests, that each adjudication spends tokens, and names `--subagent-ask-reviewer off` as the opt-out.
  - verify: `TestADR_0365_ReviewerDefaultIsOptOutAndNarrated`
- AC4.4: Where no reviewer model resolves, behaviour is byte-identical to today and a WARN names the fix, so the default can never silently fail open into something other than the existing denial.
  - verify: `TestADR_0365_ReviewerDefaultDegradesToTodayBehaviour`
- AC4.5: The reviewer default changes no non-headless and no non-allow-all deployment.
  - verify: `TestADR_0365_ReviewerDefaultIsConfinedToHeadlessAllowAll`
- AC4.6: The startup line narrates the subagent reviewer and the guardrails checker as two separate items under their own names, and neither item describes itself in the other's terms.
  - verify: `TestADR_0365_ReviewerAndCheckerNarratedSeparately`

### Scenario 5 - the vocabulary is discoverable where operators look

The originating complaint was discoverability, and it is independent of the vocabulary change:
a keybinding cycle can only offer what the operator already configured, and there is no picker
to grey entries out in. This targets the two surfaces that can show an operator a value they
did NOT set. See [ADR 0365](../adr/0365-one-permission-mode-vocabulary.md).

**Acceptance:**
- AC5.1: The session mode cycle is unchanged, and this plan makes no discoverability claim for it.
  - verify: `TestADR_0365_ModeSwitchCycleUnchanged`
- AC5.2: The help overlay lists every token, states which half of each is process-wide and which is a session default, gives the exact invocation, and states that cycling the session mode never changes the posture half.
  - verify: `TestADR_0365_HelpOverlayShowsVocabularyAndScopeSplit`
- AC5.3: The header shows the active session mode, and additionally shows the process-wide posture whenever it is above strict, so an allow-all session does not present as merely default. The posture already reaches every client through the existing `Capabilities.posture` field, so this holds under `mecatui connect` with no wire change; today's badge covers only `auto` and `yolo` and gains `trusted`.
  - verify: `TestADR_0365_HeaderShowsPostureWhenAboveStrict`
- AC5.4: Starting under `auto` and cycling the session mode through `plan` and back to `default`, the header shows the `auto` posture at every step, so returning to `default` never reads as turning autonomy off.
  - verify: `TestADR_0365_HeaderKeepsPostureAcrossModeCycle`
- AC5.5: The help overlay's restart guidance names whose restart it is: with the embedded server it gives the mecatui relaunch invocation, and under `mecatui connect` it states that the server operator must change `mecated`'s configuration and restart it, offering no local invocation.
  - verify: `TestADR_0365_HelpOverlayNamesWhoseRestart`
- AC5.6: One build-once startup line reports the resolved token, both halves it set, the checker state, and the reviewer state, each as a separately scannable item.
  - verify: `TestADR_0365_StartupLineReportsTokenBothHalvesCheckerAndReviewer`
- AC5.7: `user-docs/features/permissions-and-posture.md` documents the token table, the scope split, whose restart a posture change needs, both refusals, the three checker states, the reviewer default and its opt-out, and the subagent asymmetry. It describes the outcome as launch-time configuration and discovery, not runtime selection of autonomy from the TUI, and `task docs` passes.
  - verify: inspection - prose completeness is a human review judgment under the AGENTS.md rule against pinning documentation prose in tests; `task docs` mechanically proves links and generated reference freshness.

### Scenario 6 - the old surface still works, deprecated, and every default is reproduced

Compatibility is the condition for shipping: `mecak8s` defaults to `--posture auto` today and
`mecatui --mode plan` is a documented invocation. See
[ADR 0022](../adr/0022-allow-all-posture.md) for the surface being renamed.

**Acceptance:**
- AC6.1: `--posture`, `--yolo`, and mecatui `--mode` still resolve to the equivalent token, each emitting exactly one deprecation WARN naming `--permission-mode`.
  - verify: `TestADR_0365_DeprecatedAliasesStillResolve`
- AC6.2: An alias combination with no single-token equivalent still resolves to the pair it produces today, so deprecation never removes an expressible configuration before the aliases are removed.
  - verify: `TestADR_0365_AliasCombinationsStillResolve`
- AC6.3: Unconfigured interactive `mecated`, unconfigured headless `mecated`, `mecak8s` at its default, and `mecatui --mode plan` each produce the same posture and session mode as today. `mecak8s` at its default is an allow-all posture, so it boots only with a declared checker choice, which its Helm chart supplies per AC2.3.
  - verify: `TestADR_0365_DefaultsReproduceCurrentBehaviour`
- AC6.4: The operator-tier `permissionMode:` key is honoured from the user-global and CLI tiers, an explicit flag outranks it, and a project-tier occurrence is ignored with a WARN.
  - verify: `TestADR_0365_PermissionModeIsOperatorTierOnly`
- AC6.5: No proto, engine API, or persisted surface changed: `task api:check` passes with no `task api:update`, and a snapshot written before the change loads with an unchanged mode.
  - verify: `TestInvariant_NoWireOrDomainSurfaceChanged`

## Out of scope

|Item|Defer-to|Decision|
|-|-|-|
|Removing `--posture`, `--yolo`, `--mode`, and the `posture:` key|A later release|Classified separately as Cleanup once the operator has chosen the breaking-state treatment|
|Making the composition-bearing tiers session-selectable|Later, if asked|They have no channel read after a session exists, so it needs per-session policy construction; keeping them operator-only also preserves ADR 0022 decision 1|
|Runtime TUI selection of the four tokens carrying a posture half|Its own plan|Needs per-session policy construction and per-session ingestion; larger than this whole plan. An explicit non-goal, recorded in Human decisions because it is a known gap against the originating requirement, so no surface may imply it works|
|Removing the gate-free tier's advisory demotion of the checker|Later, if asked|Would supersede ADR 0062 sub-decision B and make the gate-free tier no longer gate-free; a separate architectural question|
|Changing the guardrails checker, its rules, modes, or failure handling|Not planned|ADR 0021 and ADR 0046 are untouched; this plan only gates on whether a checker is configured|
|Adding token rows for redundant products such as an accept-edits variant of `auto`|Not planned|Allow-all already covers Edit and Write, so the token would name a distinction the evaluator does not make|

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The implementation PR links the Plan / Interface PR and approved commit and reports
   interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The flag's two halves have different scopes, process-wide and session-default. That is real
  and is surfaced rather than hidden, but every surface must keep stating it or operators will
  reasonably assume a token is entirely per-session. The sharpest form is cycling back to
  `default` under an allow-all posture, which is why the header's posture display is pinned
  across a mode cycle rather than only at startup.
- The reviewer default grants an autonomous approval capability without the operator asking for
  it. It is bounded, opt-out, and narrated, and the alternative it replaces is a silent denial,
  but it is the one authority expansion here and deserves specific attention at contract review.
- The token list grows as combinations are added. That is the accepted cost of an enumeration
  over an ordering, and adding a row is cheaper than re-ranking a ladder, but the list should
  stay confined to combinations the evaluator actually distinguishes.
