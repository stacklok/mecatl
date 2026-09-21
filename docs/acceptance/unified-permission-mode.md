# Unified permission mode — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural - it supersedes the "no new PermissionMode" clause of ADR 0022, changes the security boundary between operator-owned and session-owned authority, adds public proto enum values and an engine exported API, and reshapes the operator flag surface of all four composition roots.
**Decision record:** [ADR 0351](../adr/0351-one-permission-mode-ladder.md)
**Phase:** operator surface consolidation
**Status:** draft, 2026-09-21. Provenance: the [PR #1455 review comment](https://github.com/stacklok/mecatl/pull/1455#discussion_r4062802901) reporting that the two-knob posture plus guardrails setup is undiscoverable, that `--posture yolo` looks like the obvious pick while demoting the checker to advisory, and that `--posture auto` can run fully allow-all with nothing supervising.
**Delivery:** Split. Seven scenarios spanning the engine domain, the proto contract, composition, four command roots, the TUI, and user documentation; the security boundary between operator ceiling and session selection needs human contract review before implementation.
**Expected tasks:** 8
**Issue:** none yet. The review comment above is the provenance and the plan PR references it as ordinary text.
**Plan PR:** added when opened
**Approved baseline:** absent until approved

Collapse the operator posture ladder and the per-session permission mode into ONE ordered
vocabulary, `plan < default < accept-edits < trusted < auto < yolo`, bound at two scopes: an
operator ceiling set at boot and a session tier clamped to it. Add two boot-time admission
gates so a deployment whose ceiling promises supervision or project trust that is not actually
configured refuses to start with an error naming the fix, instead of starting silently
under-configured.

Discoverability and the vocabulary collapse are INDEPENDENT, and this plan keeps them separable. Scenario 7 closes the originating complaint through the help overlay and header, and it does so identically whichever way the open vocabulary decision goes. Scenario 4 and Scenario 5, the two admission gates, are likewise vocabulary-independent. A reviewer who wants the reported problem fixed without the vocabulary change can take scenarios 4, 5 and 7 and decline the rest; the decision should be judged on its own merits, not on a discoverability claim it does not carry.

The smallest demonstrable behavior: `mecated serve --permission-mode auto` with no guardrails
checker refuses to start and names all three fixes; with a checker it starts, sessions default
to `auto`, and a client asking for `yolo` is refused while a client asking for `plan` succeeds.

## Human decisions

- [x] How one setting reconciles operator-owned posture with session-owned permission mode - Decision: one vocabulary at two scopes. The operator sets a ceiling at boot; a session selects any tier at or below it; an above-ceiling request is refused loudly, never silently clamped. This preserves the ADR 0022 invariant that a session cannot enlarge its own blast radius.
- [x] Whether project trust joins the ladder, and where - Decision: yes, as the rung directly above `accept-edits`. Trust was already monotone up the old posture ladder. The accepted cost is the lost 2x2 corner "project ingestion admitted while mutations still prompt", which is today's `--posture trusted`; the deprecating alias warns for one release.
- [x] Whether plan mode joins the same ladder - Decision: yes, as the bottom rung. It is strictly the least permissive tier on the same "how much runs without asking" axis, so a separate toggle would reintroduce the two-vocabulary problem this plan exists to remove.
- [x] Which tiers require a configured guardrails checker - Decision: `auto` and `yolo`, gated on the ceiling at boot. Those tiers waive the built-in mutate-ask floor, which makes the checker the only remaining content inspection. `plan`, `default`, `accept-edits`, and `trusted` keep a human in the loop and require nothing.
- [x] What a headless root does when the ceiling names a trust-implying tier - Decision: refuse to start for `trusted` exactly, after `resolveTrust`, naming `--trust-project`, `trustedWorkspaces:`, and the lower tier. WARN but start for `auto` and `yolo`, because headless-auto-without-trust is the production state ADR 0095 was written to protect and refusing there would undo it and break the shipped `mecak8s` default.
- [x] Which mechanism carries the allow-all grant - Decision: none needed; this was a factual error, not a choice. `acceptEditsRules` is a per-call `extra` keyed on the session mode inside `permpolicy.Policy.Evaluate`, while `yoloAllowAllRule` is a static rule baked in at CONSTRUCTION through `mainRules`/`childRules` and keyed on `cfg.AllowAllTools`. They do not share a channel, and the main policy is a process singleton: `mainRules` has one construction site, `buildEngine` runs once, and `sessionEngineFactory` takes that policy as a parameter rather than rebuilding it. The session tier therefore drives exactly what it drives today, the plan hard-deny and the accept-edits rules; allow-all is derived from the ceiling at construction, and the kill-switch test stays green untouched.
- [ ] Given the correction above, `trusted`, `auto`, and `yolo` have NO per-session behaviour at all, because `permpolicy.Evaluate` distinguishes only `ModePlan`, `ModeAccept`, and everything else. Do they belong in the `session.PermissionMode` domain type and on the wire? (a) NO, and `--permission-mode` becomes one CLI, docs and TUI vocabulary that DISPATCHES on the token: at or below `accept-edits` it sets the session's default mode, at or above `trusted` it sets the posture tier. This drops the proto change, the domain widening, the SDK bridge work and most of scenario 8, keeps the domain free of an operator concern, and still delivers the discoverability fix the originating review asked for; the cost is that a client can never select those tiers per session, which costs nothing today because they would do nothing. (b) YES, as ceiling-only tokens that parse but are illegal at every session ingress, which keeps one enum at the price of three values that are rejected everywhere a session could name them. Note that (a) yields the agent-def unrepresentability bound in AC8.3 for free, but ONLY if the composition-bearing tiers live in `internal/app` as `app.Posture` values and are NOT added to `engine/session` at all. If `ModeTrusted`, `ModeAuto`, and `ModeYolo` exist on the domain type, `resolvePermissionMode` returns that type and can reach them, so the property is lost even though the wire never carries them. Under (a) the interface contract's proposed `engine/session` additions are therefore withdrawn entirely, `app.Posture` already has the right iota ordering, and `--permission-mode` is a composition-layer token. Both specialist reviews independently reached (a); the security review's own triage puts it at one High, two Mediums and three Lows against three Highs and six Mediums for the ceiling-plus-enum shape, and supports it on security grounds alone. (c) YES, and build per-session policy construction so the tiers become genuinely per-session: the largest change, and it contradicts ADR 0022 decision 1, which this plan does not supersede.
- [x] Whether the guardrails gate exempts a ceiling that came from a flag default rather than an explicit operator choice - Decision: no exemption. The gate keys on the effective ceiling regardless of provenance, and the shipped allow-all defaults are updated in this change to declare their checker choice explicitly (`mecak8s`, `.github/actions/mecatequi/action.yml`, and `.github/workflows/mecatequi.yml`, which run at `posture: auto` with an empty `guardrails-model` today). A provenance exemption would preserve exactly the silently-unsupervised deployment this plan exists to abolish, and it would abolish it only for operators who already thought about it. Making the shipped configuration say `--guardrails=off` out loud is the honest form.
- [x] How the existing surface is retired - Decision: deprecating aliases for one release. `--posture` and `--yolo` keep working with a WARN naming the replacement; proto values 1 to 3 are unchanged and new values are appended; removal is a later Cleanup. `--trust-project` is NOT deprecated because it stays a first-class trust source and is the fix the headless gate points at.

## Interface contract

- **gRPC / protobuf:** `PermissionMode` gains `PERMISSION_MODE_TRUSTED = 4`, `PERMISSION_MODE_AUTO = 5`, `PERMISSION_MODE_YOLO = 6`. Values 0 to 3 are unchanged and unrenumbered. No message, field number, or rpc is removed or renamed. `CreateSessionRequest.mode`, `SetModeRequest.mode`, `Session.mode`, and `ApprovePlanRequest.target_mode` accept the new values; an above-ceiling value returns `INVALID_ARGUMENT` naming the ceiling. Purely additive, so existing clients and the TypeScript SDK stay wire-compatible.
- **Exported Go APIs / interfaces:** `engine/session` gains `ModeTrusted PermissionMode = "trusted"`, `ModeAuto PermissionMode = "auto"`, `ModeYolo PermissionMode = "yolo"`, plus `PermissionMode.Rank() int` and `ParsePermissionMode(string) (PermissionMode, bool)` so the ordering and token grammar have exactly one definition. Existing values `ModeDefault`, `ModePlan`, and `ModeAccept` keep their current strings, `"acceptEdits"` included. `internal/app` gains `Config.PermissionModeCeiling session.PermissionMode` and `Config.PermissionModeFlagSet bool`, and populates the EXISTING `server.Config.DefaultMode` with the derived initial tier so no creation site hardcodes `ModeDefault`; `app.Posture`, `app.ParsePosture`, `app.ResolveAliasPosture`, `app.IsKnownPostureToken`, and `app.PostureRefusalReason` are retained as deprecated alias shims delegating to the ladder. All additions are Added under `engine/COMPATIBILITY.md`, so `task api:update` and an `engine/CHANGELOG.md` minor entry are required.
- **Client surfaces:** ACP `sessionModeState` (`internal/adapter/acp/agent.go`) advertises every tier at or below the ceiling instead of the three hardcoded entries, and `modeFromACP` accepts the three new tokens; its `CreateACPSession` call takes the derived initial tier rather than `session.ModeDefault`. The TypeScript SDK `SessionMode` const gains `Trusted: 4`, `Auto: 5`, `Yolo: 6`, regenerating all three api-extractor reports under `sdk/typescript/etc/`; existing members keep their numbers, so the change is additive. The SDK's HTTP transport carries two hand-written CLOSED enum bridges in `sdk/typescript/src/http.ts`, `permissionMode` (number to text, unknown coerced to `"default"`) and `permissionModeTextToNumber` (text to number, unknown coerced to `0`/UNSPECIFIED); both gain the three new tiers. Without that, an HTTP-transport client at an `auto` ceiling sends `"default"` and reads every session back as UNSPECIFIED while it runs fully allow-all. Additive enum numbering is wire-compatible on the Connect/gRPC path only; the HTTP path is compatible only because these two bridges are extended.
- **Tool schemas:** None - no model-facing tool gains, loses, or changes an input or output field. The ladder is an operator and client surface; the model continues to see permission outcomes only as allow, ask, or deny results.
- **CLI / config:** New `--permission-mode` on `mecated`, `mecatui`, `mecak8s`, and `mecatequi`, taking `plan`, `default`, `accept-edits`, `trusted`, `auto`, or `yolo`; it sets the ceiling. Default ceiling is `accept-edits` on every root except `mecak8s`, whose flag default becomes `auto` to reproduce its current `--posture auto` default. New operator-tier `permissionMode:` key in `settings.yaml`, parsed strictly, project-tier occurrences ignored with a WARN, exactly mirroring `posture:`. `Config.PermissionModeFlagSet` exists ONLY so an explicit flag outranks the YAML value, mirroring `Config.PostureFlagSet`; it is deliberately NOT an input to initial-tier derivation, because `mecak8s` reaches its `auto` ceiling through a flag DEFAULT with the bit false (pinned by `cmd/mecak8s/main_test.go`), so any provenance-based rule would silently give the shipped k8s root a different initial tier than its ceiling. Each root declares its own initial tier into the existing `server.Config.DefaultMode` instead. `--posture` and `--yolo` become deprecated aliases that still resolve, max-tier, with a WARN naming `--permission-mode`. The operator-tier `posture:` key behaves the same way. The mecatui `--mode` flag keeps its meaning as the initial session tier and gains the three new tokens. `--trust-project`, `--guardrails-model`, `--guardrails`, and `--model-slot guardrail=` are unchanged. The shipped allow-all defaults are updated in the same change to pass the kill-switch explicitly, since they run at `posture: auto` with no checker today: the `mecak8s` flag default, `.github/actions/mecatequi/action.yml` (`posture` default `auto`, `guardrails-model` empty), and `.github/workflows/mecatequi.yml`. Two new boot refusals exit non-zero before serving: ceiling at or above `auto` with no checker and no `--guardrails=off`, and ceiling exactly `trusted` on a headless root with no trust source.
- **Events / persistence:** None - `sessnap.Snapshot` stores `session.PermissionMode` as its existing opaque string, and the three current values keep their exact spellings, so old snapshots load unchanged and new ones carry the new tokens without a schema change or migration. No `session.Event` payload gains or loses a field; the ceiling is a composition fact reported by the existing build-once startup diagnostic, not a new event. A persisted schedule keeps storing its mode as today; `schedule_manager.go` defaults an unset one to the derived initial tier instead of `ModeDefault`.
- **Security / authority:** `session.ParsePermissionMode` is a TOKEN GRAMMAR ONLY and is documented as such on the symbol; it is never an authorization decision, and no caller may treat a successful parse as permission to use the tier. The clamp lives at ONE chokepoint, `Service.clampMode`, which every Service entry point calls, so the HTTP, gRPC, and ACP surfaces inherit it rather than each re-implementing it. The governing rule for ingested content is that an agent definition may ATTENUATE the caller's tier and may never raise it, enforced by two independent bounds. Bound one makes it UNREPRESENTABLE rather than validated: the ingestion path gets its own closed type, `agents.DefPermissionMode`, holding only the attenuating values, so a composition-bearing tier cannot be expressed in that position at all and a later refactor that simplifies a validation check cannot quietly reopen it. Bound two is a `min` against the caller in `resolveDefMode(diag, def, caller)`, so even a permitted token cannot raise. This holds at every trust level: project trust admits a repository's steering content, not authority over the ladder that governs it. The principled line is that every other trust-gated authority grant in this system is lattice-bounded, since a project ALLOW is tool-and-pattern scoped, stays deny-dominated, and cannot suppress a configured Ask, whereas a tier name is blanket and carries no scope at all; a mode is not a rule. Persisted mode ingress (snapshot restore, fork/clear successors, and schedule fires) clamps DOWN to the current ceiling with a WARN rather than refusing, because a stored session cannot retry with a different value and refusing would brick it; live client ingress still refuses, because a client can. Delegation children never exceed their parent's effective tier. The ceiling is operator-tier only and is the sole authority boundary: a session may select at or below it and is refused above it, so ADR 0022's "a bypass the session can reach is a bypass prompt-injection can reach" holds in the new vocabulary. No new code path returns a permission decision ahead of `governance.Evaluate`; deny-dominance, the configured-Ask floor, and the `ScopeManaged` floor are unchanged at every tier including `yolo`. Build-time knobs derive from the ceiling, never from a live session tier. The root and no-sandbox refusal for allow-all tiers is preserved and now keys on the ceiling. Project trust remains the single ADR 0095 root-aware fold with its four sources; naming a trust-implying tier on a headless root is refused rather than silently withheld.
- **Compatibility / migration:** Minor and additive on the wire and in the engine API; deprecating on the CLI for one release. Every current default is reproduced exactly: unconfigured interactive `mecated` gets ceiling `accept-edits` and initial tier `default` so shift+tab still cycles three tiers; unconfigured headless `mecated` gets the same; `mecak8s` gets ceiling and initial `auto`; `mecatui --mode plan` still starts in plan and can still cycle out. The one deliberate behavior change is `trusted`, which now also auto-accepts edits; its alias emits a WARN saying so. Flag removal is deferred to a separately classified Cleanup.

## In scope - 8 scenarios, in implementation order

### Scenario 1 - one ordered ladder is the only permission vocabulary

The ladder is the union of today's `session.PermissionMode` values and the
[ADR 0022](../adr/0022-allow-all-posture.md) posture tiers on one monotone axis. Ordering is a
contract exactly as the existing posture iota ordering is, and the token grammar gets one
definition so the command layer never reimplements it, mirroring the existing
`ParsePosture` discipline described in [ADR 0351](../adr/0351-one-permission-mode-ladder.md).

**Acceptance:**
- AC1.1: `session.PermissionMode` exposes `plan`, `default`, `acceptEdits`, `trusted`, `auto`, and `yolo`, and `Rank()` orders them strictly ascending in that sequence.
  - verify: `TestADR_0351_LadderOrderingIsContract`
- AC1.2: `ParsePermissionMode` accepts each canonical token plus the persisted `acceptEdits` spelling and the CLI `accept-edits` spelling, and reports not-ok for an unknown or empty token without panicking.
  - verify: `TestADR_0351_ParsePermissionModeGrammar`
- AC1.3: The three pre-existing values keep their exact current strings, so a snapshot written before this change loads with an unchanged mode.
  - verify: `TestADR_0351_ExistingModeStringsUnchanged`

### Scenario 2 - the operator ceiling clamps session selection and refuses above it

The ceiling is the operator-owned half of the two-scope split. This scenario pins the boundary
that [ADR 0022](../adr/0022-allow-all-posture.md) established and
[ADR 0351](../adr/0351-one-permission-mode-ladder.md) restates: a session selects within the
operator's blast radius and can never enlarge it.

**Acceptance:**
- AC2.1: `CreateSession` with a mode at or below the ceiling succeeds and the returned `Session.mode` is the requested tier.
  - verify: `TestADR_0351_CreateSessionAtOrBelowCeiling`
- AC2.2: `CreateSession`, `SetMode`, and `ApprovePlan` with a mode above the ceiling each return `INVALID_ARGUMENT` naming the requested tier and the ceiling, and leave the session's mode unchanged.
  - verify: `TestADR_0351_AboveCeilingIsRefusedNotClamped`
- AC2.3: The initial session tier is each root's DECLARED default, never inferred from whether the ceiling flag was explicitly passed: `mecated` and `mecatui` declare `default`, `mecak8s` and `mecatequi` declare their ceiling. A declared initial tier above the ceiling is a startup error, not a clamp.
  - verify: `TestADR_0351_InitialTierIsRootDeclared`
- AC2.4: The operator-tier `permissionMode:` key is honored from the user-global and CLI tiers, an explicit `--permission-mode` outranks it, and a project-tier occurrence is ignored with a WARN.
  - verify: `TestADR_0351_PermissionModeIsOperatorTierOnly`
- AC2.5: Every session-creation site takes the derived initial tier from `server.Config.DefaultMode` rather than a hardcoded `session.ModeDefault`, covering the gRPC unspecified-mode default, both HTTP defaults, `schedule_manager` fires, the ACP `CreateACPSession` call, and the `mecatequi` one-shot run.
  - verify: `TestADR_0351_EveryCreationSiteDerivesInitialTier`

### Scenario 3 - build-time knobs follow the ceiling and no tier bypasses the evaluator

This is the clause that keeps [ADR 0022](../adr/0022-allow-all-posture.md) decision 2 intact
after superseding its decision 1. The rejected `ModeYolo` spike short-circuited
`permpolicy.Evaluate` before the governance fold; the invariant recorded in
[AGENTS.md](../../AGENTS.md) is that a deny in any scope is absolute.

**Acceptance:**
- AC3.1: At every tier including `yolo`, a `ScopeManaged` deny and a deliberately configured Ask still win, and the decision is produced by `governance.Evaluate` rather than an earlier return.
  - verify: `TestInvariant_DenyDominanceHoldsAtEveryTier`
- AC3.2: Project ingestion, the main substitution loosening, and the child substitution loosening are derived from the ceiling and are identical to the values today's posture ladder derives for the equivalent tier.
  - verify: `TestADR_0351_CeilingDerivesBuildTimeKnobs`
- AC3.3: `SetMode` to a lower tier changes the per-call fold only; it does not retract project ingestion or re-tighten the substitution floor, and the user documentation states this.
  - verify: `TestADR_0351_SetModeDoesNotAlterBuildTimeKnobs`
- AC3.4: The root and no-sandbox refusal for allow-all tiers fires on a ceiling of `auto` or `yolo` and never on `plan`, `default`, `accept-edits`, or `trusted`.
  - verify: `TestADR_0351_PrivilegedRefusalKeysOnCeiling`
- AC3.5: The allow-all grant stays an evaluator-CONSTRUCTION input derived from the ceiling, not a per-call extra keyed on the session tier, so it still binds main and child rule sets and the existing kill-switch passes unmodified.
  - verify: `TestAllowAllToolsBindsMainAndChildren`
- AC3.7: A session tier at or above `trusted` produces a permission decision identical to the tier below it for the same call, which is the honest statement of the corrected wiring and the evidence behind the open domain-enum decision.
  - verify: `TestADR_0351_UpperTiersHaveNoPerSessionEffect`
- AC3.6: A `mecatequi` one-shot and a `mecak8s` session at an `auto` ceiling are allow-all end to end, proving the derived initial tier reaches the fold and the unattended path did not regress.
  - verify: `TestADR_0351_UnattendedRootsRemainAllowAll`

### Scenario 4 - an allow-all ceiling without a checker refuses to start

The precise gap the review comment reported: `--posture auto` leaves nothing supervising the
session. Configure-equals-enable from
[ADR 0046](../adr/0046-guardrails-slot-enable.md) is preserved; this adds a tier-conditional
requirement on top of it.

**Acceptance:**
- AC4.1: A ceiling of `auto` or `yolo` with no checker configured by either enable path and without the kill-switch makes `Build` fail, and the process exits non-zero before serving.
  - verify: `TestADR_0351_AllowAllCeilingRequiresChecker`
- AC4.2: The refusal message names the requested tier and all three fixes: `--guardrails-model`, the `guardrail` model slot, and `--guardrails=off`.
  - verify: `TestADR_0351_CheckerRefusalNamesEveryFix`
- AC4.3: The same ceiling starts normally when a checker is configured by the flag, by the `guardrail` slot, or by the `cheap`-tier fall-through, and starts with the existing guardrails-off posture line when `--guardrails=off` is passed.
  - verify: `TestADR_0351_AllowAllCeilingStartsWithChecker`
- AC4.4: A ceiling of `plan`, `default`, `accept-edits`, or `trusted` never requires a checker.
  - verify: `TestADR_0351_LowerCeilingsRequireNoChecker`
- AC4.5: The gate keys on the effective ceiling with no exemption for a defaulted flag, so a root whose flag default is `auto` refuses to start unless its shipped configuration supplies a checker or the kill-switch.
  - verify: `TestADR_0351_CheckerGateHasNoProvenanceExemption`
- AC4.6: Every shipped allow-all default boots after this change because it declares its checker choice explicitly: the `mecak8s` flag default, `.github/actions/mecatequi/action.yml`, and `.github/workflows/mecatequi.yml`.
  - verify: `TestADR_0351_ShippedAllowAllDefaultsDeclareCheckerChoice`

### Scenario 5 - a headless root refuses a ceiling of trusted it cannot honor

[ADR 0095](../adr/0095-root-aware-project-trust.md) keeps a headless root from gaining trust
from the tier. With trust on the ladder, naming `trusted` on such a root asks for an increment
that is withheld, so it is refused rather than silently ignored. `auto` and `yolo` keep ADR
0095's intended headless state.

**Acceptance:**
- AC5.1: Ceiling `trusted` on a headless root with no trust source makes `Build` fail, naming `--trust-project`, `trustedWorkspaces:`, and the `accept-edits` alternative.
  - verify: `TestADR_0351_HeadlessTrustedCeilingRefused`
- AC5.2: The same ceiling starts when any of the four ADR 0095 trust sources is present, because the gate runs after `resolveTrust`.
  - verify: `TestADR_0351_HeadlessTrustedAcceptsEveryTrustSource`
- AC5.3: Ceiling `auto` or `yolo` on a headless root with no trust source still starts, still admits no project ingestion, and emits a WARN naming the withheld trust.
  - verify: `TestADR_0351_HeadlessAllowAllStartsWithoutTrust`
- AC5.4: Ceiling `trusted` on an interactive root grants project trust with no refusal and no WARN, matching today's posture floor.
  - verify: `TestADR_0351_InteractiveTrustedGrantsTrust`

### Scenario 6 - the old surface still works, deprecated, and every default is reproduced

Compatibility is the condition for shipping this at all: `mecak8s` defaults to `--posture auto`
today and the TypeScript SDK pins the proto enum. The alias fold reuses the one max-tier grammar
described in [ADR 0351](../adr/0351-one-permission-mode-ladder.md).

**Acceptance:**
- AC6.1: `--posture` and `--yolo` still resolve to the equivalent ceiling, max-tier with each other and with `--permission-mode`, each emitting exactly one deprecation WARN naming `--permission-mode`.
  - verify: `TestADR_0351_DeprecatedAliasesStillResolve`
- AC6.2: `--posture trusted` emits an additional WARN stating that the tier now also auto-accepts edits.
  - verify: `TestADR_0351_TrustedAliasWarnsAboutEditWidening`
- AC6.3: Unconfigured interactive `mecated`, unconfigured headless `mecated`, `mecak8s` at its flag default, and `mecatui --mode plan` each produce the same ceiling, initial tier, and reachable tier set as today. The one deliberate exception is that the allow-all shipped defaults now also carry an explicit checker choice per AC4.6; their resolved tier behaviour is unchanged.
  - verify: `TestADR_0351_DefaultsReproduceCurrentBehaviour`
- AC6.4: Proto values 0 to 3 are unchanged and unrenumbered, the new values are appended, and `task api:check` passes after `task api:update` records the engine additions as minor.
  - verify: inspection - the proto field numbers and the `engine/api/*.txt` diff are reviewed directly, since no runtime test can prove an enum was not renumbered.
- AC6.5: Every tier round-trips through the SDK HTTP transport bridges without coercion: `permissionMode` renders each tier's text and `permissionModeTextToNumber` returns each tier's number, so no tier decodes as UNSPECIFIED or silently downgrades to `default`.
  - verify: `TestADR_0351_SDKHttpTransportRoundTripsEveryTier`

### Scenario 7 - the tier is discoverable where operators actually look

The originating complaint was discoverability, and it is INDEPENDENT of the vocabulary
question: it is unaffected by whichever shape the open decision picks, and an earlier revision
of this plan got it wrong by bundling them. That revision asserted the shift+tab cycle would
offer every tier at or below the ceiling. Since the default ceiling is `accept-edits`, an
unconfigured deployment would cycle `plan`, `default`, `accept-edits`, exactly what it cycles
today, and an operator would see `auto` only if they had already configured it, meaning they
already knew it existed. The fix fired only for operators who did not need it. There is also
no mode picker to grey entries out in: `ModeSwitch` is a blind keybinding cycle, and the only
mode surfaces are the cycle, the header, and the help overlay. This scenario therefore targets
the two surfaces that can show an operator a value they did NOT set. See
[ADR 0351](../adr/0351-one-permission-mode-ladder.md).

**Acceptance:**
- AC7.1: The shift+tab cycle is UNCHANGED and this plan makes no discoverability claim for it, because a cycle can only offer what the operator already configured.
  - verify: `TestADR_0351_ModeSwitchCycleUnchanged`
- AC7.2: The help overlay lists the whole vocabulary with its scope split, naming which tokens apply to this session and which to the whole process, and giving the exact invocation for the process-scoped ones.
  - verify: `TestADR_0351_HelpOverlayShowsVocabularyAndScopeSplit`
- AC7.3: The header shows the active session mode, and additionally shows the process tier whenever that tier is above the session's, so a session running under an allow-all process does not present as merely `default`.
  - verify: `TestADR_0351_HeaderShowsProcessTierWhenAboveSession`
- AC7.4: The build-once startup diagnostic reports the resolved tier, the initial session mode, the derived knobs, and the guardrails checker state on one correlated set of fields.
  - verify: `TestADR_0351_StartupDiagnosticReportsTierAndChecker`
- AC7.5: The ACP `sessionModeState` picker advertises exactly the session-selectable modes and rejects anything outside them.
  - verify: `TestADR_0351_ACPModePickerAdvertisesSessionSelectableOnly`
- AC7.6: `user-docs/features/permissions-and-posture.md` documents one vocabulary, the scope split, both refusals, and the runtime de-escalation caveat, and `task docs` passes.
  - verify: inspection - prose completeness is a human review judgment under the AGENTS.md rule against pinning documentation prose in tests; `task docs` mechanically proves links and generated reference freshness.

### Scenario 8 - every mode ingress clamps at one chokepoint, and ingested content cannot name authority

Widening the grammar widens every parser that reads it. A mode enters the system at ten
independent points, one of which, agent-definition frontmatter, is ingested repository content.
`resolvePermissionMode` is safe today only because it is an explicit allowlist, and
`session.Session.SetMode` validates the state transition but never the value, so the domain is
not a backstop. This scenario makes the clamp structural rather than per-handler, in the spirit
of the whole-graph guards [AGENTS.md](../../AGENTS.md) already requires elsewhere. See
[ADR 0351](../adr/0351-one-permission-mode-ladder.md).

**Acceptance:**
- AC8.1: Every Service entry point that accepts a mode routes through one `Service.clampMode`, so the gRPC, HTTP, and ACP surfaces inherit the same refusal without re-implementing it.
  - verify: `TestADR_0351_EveryServiceEntryClampsThroughOneChokepoint`
- AC8.2: A structural guard enumerates every mode-bearing ingress and fails when a new one appears that does not reach the chokepoint, so a future surface cannot quietly skip it.
  - verify: `TestADR_0351_ModeIngressStructuralGuard`
- AC8.3: A composition-bearing tier is unrepresentable in agent-definition frontmatter: the ingestion path parses into a closed `agents.DefPermissionMode` holding only attenuating values, so `trusted`, `auto`, and `yolo` warn and fall back at every trust level and every def tier, including an explicitly trusted project.
  - verify: `TestADR_0351_AgentDefCannotNameCompositionBearingTier`
- AC8.7: An agent definition may only attenuate: `resolveDefMode` returns the lower of the def's value and the caller's tier, so a def naming a tier above its caller resolves to the caller's.
  - verify: `TestADR_0351_AgentDefAttenuatesNeverRaises`
- AC8.4: `session.ParsePermissionMode` is documented as a token grammar that confers no authority, and the agent-def path does not call it.
  - verify: inspection - the guarantee is that one call site deliberately does NOT share a parser, which is proved by reading the code rather than by exercising it.
- AC8.5: A persisted mode above the current ceiling, arriving by snapshot restore, a fork or clear successor, or a schedule fire, clamps down to the ceiling with a WARN and the session still loads.
  - verify: `TestADR_0351_PersistedAboveCeilingClampsDownNotRefuses`
- AC8.6: A delegation child never runs at a tier above its parent session's effective tier.
  - verify: `TestADR_0351_ChildNeverExceedsParentTier`

## Out of scope

|Item|Defer-to|Decision|
|-|-|-|
|Removing `--posture`, `--yolo`, and the `posture:` key|A later release|Classified separately as Cleanup once the operator has chosen the breaking-state treatment, per the development process|
|Splitting the ceiling from the initial session tier into two operator flags|Later, if asked|One flag with a derived initial tier reproduces every current default; a second knob would reintroduce the two-setting problem this plan removes|
|Rebuilding a per-session engine so `SetMode` can change build-time knobs|Later, if asked|Build-time knobs follow the ceiling, which is byte-identical to today; a live rebuild is a much larger change with no reported demand|
|A managed-scope ceiling that clamps the operator's own ceiling|Later|The `resolvePosture` ceiling parameter already anticipates it; enforcement stays out of this plan, as ADR 0022 and the existing `postureNoCeiling` sentinel record|
|Restoring the lost 2x2 corner, project trust with mutate-ask|Not planned|Accepted cost of one vocabulary, recorded as a human decision and warned by the alias for one release|
|Changing the guardrails checker, its rules, modes, or failure handling|Not planned|ADR 0021 and ADR 0046 are untouched; this plan only gates on whether a checker is configured|

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The implementation PR links the Plan / Interface PR and approved commit and reports
   interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.
6. `task api:update` has been run and `engine/CHANGELOG.md` records the additions as minor
   under `engine/COMPATIBILITY.md`.

## Deferred decisions and known risks

- The alias fold and the ceiling clamp must share one grammar definition, or the security-relevant
  max-tier resolution can drift between the command layer and composition. The existing
  `ResolveAliasPosture` comment records this as the reason that helper exists; the implementation
  keeps one definition.
- The SDK HTTP transport's two enum bridges are CLOSED maps with silent-coercion fallbacks, so any future tier added without touching them decodes as UNSPECIFIED rather than failing loudly. This plan extends them and pins every tier with AC6.5, but does not change the fallback to a loud failure; that is a separate decision about SDK error semantics and is not taken here.
- `mecatui` is both operator and client in one process, so it carries both bindings: the ceiling
  and the `--mode` initial tier. Keeping both named in the one vocabulary is what preserves
  `--mode plan` behavior; collapsing them would pin such a session in plan.
