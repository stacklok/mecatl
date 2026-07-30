# Path-escape posture relax — acceptance plan

**Phase:** capability / FS-tool out-of-workspace access
**Status:** landed, 2026-07-30. Design discussion (osfs containment value) settled the posture table.
**Accumulator branch:** `acc/path-escape-posture` (off `main`).

The smallest set of work that turns the osfs out-of-root rejection from a silent
dead-end (`ErrPathEscape`, which today only forces the model into a Bash
workaround that loses the FS tools' invariants and audit shape) into a
**posture-appropriate decision** — allow at `yolo`/`auto`, ask at
`strict`/`trusted` — while keeping the untrusted-read-only-child population's
hard-deny boundary and the symlink-escape defense byte-for-byte intact.

The doc is organized scenario-first because acceptance is about what the running
harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [`AGENTS.md` — the osfs / `WithReadRoots` gotcha](../../AGENTS.md) — the containment
  is an `*os.Root` + canonicalize-then-reject invariant, not a permission lookup;
  the relax must consult policy *before* the tool body, never strip the vetting.
- [ADR-0047](../adr/0047-absolute-path-resolution.md) — the frozen decision that
  defines `resolveInRoot`, the canonicalize-then-reject contract, the read-ledger
  key normalization, and the Glob/Grep-no-behavioural-change rule this plan relaxes
  *the decision of* (never the vetting).
- The guardrail matcher keys on tool **name** only
  ([`internal/adapter/modelhook/matcher.go`](../../internal/adapter/modelhook/matcher.go)
  `ruleMatches`), never on call args — so "gate on the guardrails decision" cannot
  be a path-scoped guardrail rule. Path-escape gating is a **separate composition
  seam** that may optionally route through the checker, not a new rule syntax.
- Posture ladder (`strict < trusted < auto < yolo`) is the single trust axis —
  [`internal/app/posture.go`](../../internal/app/posture.go) `applyPosture`. The
  escape decision derives from it; no new trust surface is introduced.
- **Pseudo-filesystems are never relaxed.** `Read`/`Stat` of a path under `/proc`,
  `/sys`, or `/dev` stays a hard deny at every posture: an in-process FS read of
  `/proc/self/environ` returns the *server's* raw, unscrubbed environment, whereas
  the Bash parity channel (`cat /proc/self/environ`) reads the child shell's
  `envscrub.Scrub`-scrubbed env ([`AGENTS.md` — the env-scrub gotcha](../../AGENTS.md)).
  Relaxing pseudo-fs would open a NEW secret-exfiltration channel Bash does not
  provide, breaking the parity premise.

### The posture → escape-decision table

| Posture | Read escape | Write escape |
|---|---|---|
| `yolo` | allow | allow |
| `auto` | allow (guardrail-gated iff the escape knob is configured — **v2, deferred**) | ask (guardrail-gated iff configured — **v2, deferred**) |
| `strict` / `trusted` | **ask** | **ask** |
| plan mode | allow read | hard deny (plan mode wins first — unchanged) |
| pseudo-fs (`/proc`,`/sys`,`/dev`) | **hard deny, every posture** | **hard deny, every posture** |
| any child engine (Subagent / member / branch) | **hard deny, every posture** | **hard deny, every posture** |

`auto` without the escape guardrail knob configured defaults to **allow-read /
ask-write** (Bash parity for reads; never silent un-asked mutation below `yolo`).
The guardrail-gated clause is the v2-deferred route, not v1 behaviour.

> **The child row is about engine scope, not workspace trust.** There is no
> "untrusted child FS" concept: a child's osfs workspace is built by the SAME
> `newForkWorkspace` helper as every fork family and has the same containment as
> the main session's. The issue-#40 trust gate nils a read-only child's *Bash
> shell* on an untrusted repo — it never touches the FS tools. So the child
> boundary the relax must respect is simply that the relaxed construction options
> are wired into the MAIN session's workspace only, never into `newForkWorkspace`
> or any child engine — at every posture.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — the escape-classification seam (no behaviour change yet)

A new composition-layer classification answers "is this FS-tool call an
out-of-root escape?" as a pure function over the call name + args + the session
root, reusing osfs's canonicalization so it can never disagree with the tool
body about what is an escape. It is consumed by a wrapping permission policy but
changes no decision yet (the wrapper returns the inner decision verbatim in this
wave). This lands the shared, tested predicate both later waves build on, per
the layering rule that the escape *decision* is composition, not domain —
[`AGENTS.md` — the layering rule](../../AGENTS.md). It must reuse BOTH osfs
algorithms, not one: `resolveInRoot`'s symlink-aware canonicalization for the
in-root/escape decision AND `allowedReadRoot`'s lexical prefix-match for the
read-root third state — they differ on a symlinked absolute path whose target is
inside a read-root but whose lexical form is outside it, and a classifier that
only canonicalizes would drift from the tool body
([ADR-0047](../adr/0047-absolute-path-resolution.md)). It also classifies
pseudo-filesystem paths (`/proc`,`/sys`,`/dev`) as a distinct never-relaxed
category (see the scope cuts).

**Work:**
- engine domain (`tool`): no change — `FileSystem`/`Workspace` stay put (the
  `port↔tool` cycle gotcha).
- adapters (`internal/adapter/osfs`): expose a canonicalize-only helper
  (resolve-then-report, no `*os.Root` open) the composition classifier reuses so
  the escape definition is single-sourced with `resolveInRoot`
  ([`internal/adapter/osfs/osfs.go`](../../internal/adapter/osfs/osfs.go)).
- composition (`internal/app`): the `escapeClassifier` (call → in-root /
  read-root / escape, with the absolute-vs-relative and symlink-aware semantics
  of `resolveInRoot`).

**Acceptance:**
- AC1.1: a relative path and an absolute path that canonicalize inside the
  workspace root classify as in-root; a `".."` traversal and an absolute path
  outside classify as escape — matching `resolveInRoot`'s verdict on the same
  inputs.
  - verify: `TestPathEscapePosture_Scenario1_ClassifierMatchesResolveInRoot`
- AC1.2: an absolute path under a configured `WithReadRoots` read-only root
  classifies as read-root (readable, not writable), distinct from both in-root
  and escape — using `allowedReadRoot`'s lexical match so a symlinked absolute
  path classifies identically to the tool body.
  - verify: `TestPathEscapePosture_Scenario1_ReadRootClassification`
- AC1.3: a symlink inside the workspace whose target escapes classifies as
  escape (the canonicalize-then-reject path is exercised, not bypassed).
  - verify: `TestPathEscapePosture_Scenario1_SymlinkEscapeIsEscape`
- AC1.4: the classification is a pure function with no `*os.Root` open and no
  I/O beyond the canonicalization stat syscalls `resolveInRoot` already performs.
  - verify: inspection — the classifier shares `resolveInRoot`'s stat-based
    ancestor resolution and opens no root; confirmed by review.
- AC1.5: a path under `/proc`, `/sys`, or `/dev` classifies as the never-relaxed
  pseudo-fs category (distinct from a regular escape), at every posture.
  - verify: `TestPathEscapePosture_Scenario1_PseudoFsClassification`

---

### Scenario 2 — `yolo` and `auto` allow reads (and `auto` reads stay Bash-parity)

Under posture `yolo` and `auto`, an out-of-root absolute-path `Read`/`Stat`
succeeds through the FS tool instead of failing with `ErrPathEscape`. This is
the honesty fix: at those postures Bash already reads the same bytes
unconditionally (posture `auto`/`yolo` derives `AllowAllTools` —
[`internal/app/posture.go`](../../internal/app/posture.go) `applyPosture`), so
the FS read boundary was cosmetic and only pushed the model to a worse-audited
`cat`. The relax flows through the ordinary `authorize → preHook + execute`
tail so audit (`ToolCallRecorder`), `EvToolResult`, and any PostToolUse hook
fire identically to an in-root read — the same discipline as the
`askHookApproval` allow path, and the same "the loop emits, the ports persist"
separation [`architecture.md`](../architecture.md) describes for the dispatch
lifecycle. Plan mode still permits the read (reads are not mutations).

**Work:**
- composition (`internal/app`): the posture-derived escape policy consulted by a
  root-aware wrapping `port.PermissionPolicy` (a `permpolicy` sibling that closes
  over the session root + the classifier); at `auto`/`yolo` a read escape
  resolves Allow. The wrapper **delegates to the inner policy first** and only
  relaxes a non-deny — it never overrides an inner Deny (deny-dominance holds).
- adapters (`internal/adapter/osfs`): `Read`/`Stat` serve a canonicalized
  out-of-root absolute path when the workspace is constructed in a relaxed-read
  posture (an explicit construction option, NOT a default — the default stays
  deny). Serving opens a fresh `*os.Root` on the target's parent and serves the
  leaf through it — symlink vetting is unchanged, and a symlink *inside* the
  target dir that escapes further is refused by that root's containment (never a
  bare `os.Open`). Pseudo-fs paths are never served.

**Acceptance:**
- AC2.1: at posture `yolo`, `Read` of an out-of-root absolute path returns the
  file's contents (no `ErrPathEscape`).
  - verify: `TestPathEscapePosture_Scenario2_YoloReadEscapeAllowed`
- AC2.2: at posture `auto` with no escape guardrail knob, `Read` of an
  out-of-root absolute path succeeds (Bash parity).
  - verify: `TestPathEscapePosture_Scenario2_AutoReadEscapeAllowed`
- AC2.3: an allowed read escape records the call verbatim in the
  `ToolCallRecorder` and emits `EvToolResult`, exactly as an in-root read.
  - verify: `TestPathEscapePosture_Scenario2_ReadEscapeAuditParity`
- AC2.4: at posture `strict`, a `Read` escape does NOT silently succeed in this
  wave (the ask lands in Scenario 4) — behaviour is unchanged from today.
  - verify: `TestPathEscapePosture_Scenario2_StrictReadUnchanged`
- AC2.5: at `yolo`, `Read /proc/self/environ` does NOT return the raw server
  environment (pseudo-fs hard-deny) — no `*_API_KEY`/`*_TOKEN`/`*_SECRET`
  substring reaches the result.
  - verify: `TestPathEscapePosture_Scenario2_ProcEnvironNotExposed`
- AC2.6: a symlink inside an allowed out-of-root target dir whose target escapes
  further is refused by the serving `*os.Root` (containment survives the relax).
  - verify: `TestPathEscapePosture_Scenario2_NestedSymlinkEscapeRejected`
- AC2.7: a session that ran relaxed and is restarted rehydrates the SAME relaxed
  workspace (via `Service.rehydrateSession`), so a resumed out-of-root `Read`
  still succeeds rather than dead-ending on `ErrPathEscape`.
  - verify: `TestPathEscapePosture_Scenario2_RestartRehydratesRelaxedWorkspace`

---

### Scenario 3 — `yolo` allows writes; `auto` asks on writes

Under `yolo`, an out-of-root `Write`/`Edit` succeeds (the operator has accepted
full shell risk). Under `auto` (no guardrail knob), a write escape resolves
**Ask** — never a silent un-asked mutation below `yolo`. Writes route through
the mutate-serial path unchanged (the read-parallel / mutate-serial invariant
holds — [`AGENTS.md` — dispatch invariants](../../AGENTS.md)). Edit's three
invariants (read-before-edit, exact match, uniqueness) apply to an out-of-root
target identically, with the read-ledger keyed on the canonicalized path so an
out-of-root read followed by an out-of-root edit matches.

**Work:**
- composition (`internal/app`): write escapes resolve Allow at `yolo`, Ask at
  `auto`/`strict`/`trusted` via the wrapping policy. The wrapper delegates to the
  inner policy first and never overrides an inner Deny — a configured Deny (or a
  configured Ask) on the tool still wins over an escape Allow, preserving the
  deny-dominant fold ([`AGENTS.md` — permission-fold invariants](../../AGENTS.md)).
- adapters (`internal/adapter/osfs`): `Write` (and the Edit mutation path) serve
  a canonicalized out-of-root absolute path under the relaxed-write construction
  option, **through a fresh `*os.Root` on the target's parent** (never a direct
  `os.WriteFile`) so the symlink-escape containment
  ([ADR-0047](../adr/0047-absolute-path-resolution.md)) survives; the read-ledger
  (`RecordRead`/`WasReadUnchanged`) keys out-of-root paths by their canonical
  absolute form without regressing in-root cross-form matching.
- composition: the escape Ask surfaces through the ordinary `surfaceAsk` spine
  (mint askID → PauseForApproval → EvPermissionAsk), reusing the existing
  ask mechanics — no new ask channel.

**Acceptance:**
- AC3.1: at `yolo`, `Write` to an out-of-root absolute path creates/replaces the
  file.
  - verify: `TestPathEscapePosture_Scenario3_YoloWriteEscapeAllowed`
- AC3.2: at `auto` (no guardrail knob), a `Write` escape surfaces an
  `EvPermissionAsk` and executes only on an allow verdict.
  - verify: `TestPathEscapePosture_Scenario3_AutoWriteEscapeAsks`
- AC3.3: an out-of-root `Edit` enforces read-before-edit-and-unchanged: editing
  a path not first read (or changed since) is rejected, keyed on the canonical
  path (a path with `..` components normalizing to the same canonical form
  matches).
  - verify: `TestPathEscapePosture_Scenario3_EditLedgerOutOfRoot`
- AC3.4: two concurrent write escapes never run in parallel (mutate-serial
  preserved).
  - verify: `TestPathEscapePosture_Scenario3_WriteEscapeMutateSerial`
- AC3.5: an out-of-root `Write`/`Edit` flows through an `*os.Root` on the
  target's parent (not a direct `os` call): a symlinked parent component that
  escapes is refused, exactly as an in-root write.
  - verify: `TestPathEscapePosture_Scenario3_WriteEscapeServedThroughOsRoot`
- AC3.6: a configured Deny on the tool still wins over a posture-relaxed escape
  Allow (deny-dominance); a configured Ask is never suppressed by the relax.
  - verify: `TestPathEscapePosture_Scenario3_ConfiguredDenyWinsOverEscapeAllow`

---

### Scenario 4 — `strict` / `trusted` ask instead of deny

At `strict` and `trusted`, an out-of-root read or write resolves **Ask** rather
than today's hard `ErrPathEscape`. This is the real UX win: the operator already
gets an ask in these modes today, but it arrives as an opaque Bash `cat /path`
after the model's FS attempt dead-ends; moving the ask onto the FS tool makes it
legible and keeps the FS tools' invariants in play. Plan mode still hard-denies
a write escape first (plan-mode deny precedes any rule —
[`engine/adapter/permpolicy/permpolicy.go`](../../engine/adapter/permpolicy/permpolicy.go)
`Evaluate`, the plan-mode gate [`architecture.md`](../architecture.md) documents
for the permission lifecycle), and a read escape in plan mode follows the read
row (allow). The Ask is deny-safe headless: a non-interactive engine auto-denies
with the accurate message, per the existing headless-ask discipline
([`engine/agent/dispatch.go`](../../engine/agent/dispatch.go) `surfaceAsk`).

**Work:**
- composition (`internal/app`): the wrapping policy resolves an escape to Ask at
  `strict`/`trusted` for both read and write; the escape reason names the path
  and that it lies outside the workspace. The wrapper delegates to the inner
  policy FIRST (so the plan-mode hard-deny inside `EvaluateWith` runs before any
  escape decision) and never converts an inner Deny into an escape Ask/Allow.
- no engine change — the Ask rides the existing `authorize`/`surfaceAsk` spine.

**Acceptance:**
- AC4.1: at `strict`, a `Read` escape surfaces an `EvPermissionAsk`; on allow
  the read executes.
  - verify: `TestPathEscapePosture_Scenario4_StrictReadEscapeAsks`
- AC4.2: at `trusted`, a `Write` escape surfaces an `EvPermissionAsk`; on deny a
  deny result is recorded and nothing is written.
  - verify: `TestPathEscapePosture_Scenario4_TrustedWriteEscapeAsks`
- AC4.3: a headless main-engine escape Ask at `strict`/`trusted` does NOT block
  forever: with no approver wired the run surfaces an actionable stop (the
  existing headless main-ask behaviour — the await ends on cancellation), never
  a silent hang and never a fabricated "denied by user".
  - verify: `TestPathEscapePosture_Scenario4_HeadlessEscapeAskDoesNotHang`
- AC4.4: in plan mode a write escape is hard-denied before any escape Ask is
  surfaced — the wrapper consults the inner policy first, so the plan-mode deny
  inside `EvaluateWith` precedes the escape decision (plan-mode precedence and
  deny-dominance both hold).
  - verify: `TestPathEscapePosture_Scenario4_PlanModeWriteEscapeDenied`

---

### Scenario 5 — child engines never relax + Glob/Grep confinement hold at every posture

The relax is **scoped to the main session's workspace construction only**. A
child engine (Subagent / team member / Parallel branch) builds its FS workspace
through the SAME shared `newForkWorkspace` helper as every fork family
([`internal/app/build.go`](../../internal/app/build.go) `newForkWorkspace`), and
that helper must never receive the relaxed options — otherwise a read-only
explorer child (which has Read/Grep/Glob even where its Bash shell is gated)
would silently gain the main session's escape reach. This is a scope boundary,
not a trust boundary: it holds at every posture, for trusted and untrusted
workspaces alike. Glob/Grep stay workspace-confined in **every** posture
(patterns are not paths; there is no parity argument for enumerating outside the
root — [ADR-0047](../adr/0047-absolute-path-resolution.md) point 5).

**Work:**
- composition (`internal/app`): the relaxed escape options are only ever
  constructed for the main session's workspace; `newForkWorkspace` and every
  child engine derivation never receives them, independent of posture.
- no Glob/Grep change — an AC pins they stay confined.

**Acceptance:**
- AC5.1: a read-only child engine's `Read` of an out-of-root absolute path is
  denied, even when the main session runs relaxed at `auto`/`yolo` — the relax
  does not propagate to the child.
  - verify: `TestPathEscapePosture_Scenario5_ChildReadEscapeDenied`
- AC5.2: `Glob` and `Grep` never serve out-of-root matches at any posture,
  including `yolo`.
  - verify: `TestPathEscapePosture_Scenario5_GlobGrepConfined`
- AC5.3: the relaxed escape options are absent from every child engine
  derivation — proven structurally: no call site of the relaxed construction
  option exists under `newForkWorkspace` / `buildSubagentTool` /
  `buildTeamWiring` / `buildMemberEngine` / the parallel child engine builder.
  - verify: `TestPathEscapePosture_Scenario5_ChildEnginesNeverRelaxed`
- AC5.4: the zero-value workspace construction (no relaxed option) denies an
  out-of-root absolute read at every posture — the default stays deny.
  - verify: `TestPathEscapePosture_DefaultConstructionDeniesEscape`

---

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Guardrail-routed escape checking (the `auto`-gated path through the modelhook checker) | ~~v2~~ **resolved** | [ADR-0080](../adr/0080-guardrail-routed-escape-checking.md): a composition-level pre-check in the escape policy, auto-only, `guardrails.escape` knob — pinned by `TestPathEscapePosture_GuardrailRoutedEscape` |
| A managed-scope posture ceiling for escapes | future | [`internal/app/posture.go`](../../internal/app/posture.go) `postureNoCeiling` precedent |
| Path-scoped permission **rules** (operator YAML matching on paths) | future | rules stay tool-name-scoped this plan |
| "Allow always" persistence on an escape Ask | v2 | v1 asks are allow-once only (no learned out-of-root rule) |

## Cross-cutting deliverables

- A construction option on `osfs.NewFileSystem` / the workspace factory for the
  relaxed read/write posture (default off), threaded from composition only.
- The `escapeClassifier` + root-aware wrapping `port.PermissionPolicy` in
  composition, consulted in `Evaluate` before the tool body.
- `docs/design/IMPLEMENTATION-NOTES.md` — a "Path-escape posture" section.
- `user-docs/` — a short note that FS tools can reach outside the workspace at
  `auto`/`yolo` (with the operator consent model), linking to `docs/usage/`.

## Sequencing recommendation

Scenario 1 (the classifier) must land first — both allow and ask waves reuse it,
and it carries no behaviour change so it is safe to merge alone. Scenarios 2–3
(the allow waves, `auto`/`yolo`) are the honesty fix and land next; Scenario 4
(the ask waves, `strict`/`trusted`) is the larger permission-fold work and lands
after. Scenario 5 is cross-cutting and its tests guard every wave. This maps to
the two waves the design called v1 (Scenarios 1–3, 5) and v2 (Scenario 4 +
guardrail routing), which may be driven as separate `/plan-orchestrate` runs.

## Named tests landing in this plan

`TestPathEscapePosture_Scenario1_ClassifierMatchesResolveInRoot`,
`TestPathEscapePosture_Scenario1_ReadRootClassification`,
`TestPathEscapePosture_Scenario1_SymlinkEscapeIsEscape`,
`TestPathEscapePosture_Scenario1_PseudoFsClassification`,
`TestPathEscapePosture_Scenario2_YoloReadEscapeAllowed`,
`TestPathEscapePosture_Scenario2_AutoReadEscapeAllowed`,
`TestPathEscapePosture_Scenario2_ReadEscapeAuditParity`,
`TestPathEscapePosture_Scenario2_StrictReadUnchanged`,
`TestPathEscapePosture_Scenario2_ProcEnvironNotExposed`,
`TestPathEscapePosture_Scenario2_NestedSymlinkEscapeRejected`,
`TestPathEscapePosture_Scenario2_RestartRehydratesRelaxedWorkspace`,
`TestPathEscapePosture_Scenario3_YoloWriteEscapeAllowed`,
`TestPathEscapePosture_Scenario3_AutoWriteEscapeAsks`,
`TestPathEscapePosture_Scenario3_EditLedgerOutOfRoot`,
`TestPathEscapePosture_Scenario3_WriteEscapeMutateSerial`,
`TestPathEscapePosture_Scenario3_WriteEscapeServedThroughOsRoot`,
`TestPathEscapePosture_Scenario3_ConfiguredDenyWinsOverEscapeAllow`,
`TestPathEscapePosture_Scenario4_StrictReadEscapeAsks`,
`TestPathEscapePosture_Scenario4_TrustedWriteEscapeAsks`,
`TestPathEscapePosture_Scenario4_HeadlessEscapeAskDoesNotHang`,
`TestPathEscapePosture_Scenario4_PlanModeWriteEscapeDenied`,
`TestPathEscapePosture_Scenario5_ChildReadEscapeDenied`,
`TestPathEscapePosture_Scenario5_GlobGrepConfined`,
`TestPathEscapePosture_Scenario5_ChildEnginesNeverRelaxed`,
`TestPathEscapePosture_DefaultConstructionDeniesEscape`,
`TestPathEscapePosture_GuardrailRoutedEscape` (wave 2 — ADR-0080),
`TestPathEscapePosture_EditLedgerPseudoFSGuarded` (wave 2 — AC-W2-F1),
`TestPathEscapePosture_VetRelaxedParentSharesCanonicalize` (wave 2 — AC-W2-F2).

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — `llms.txt` regenerated and the matlatl strict link gate green.
3. `task api:check` passes (or `task api:update` + `engine/CHANGELOG.md` note)
   if the engine's exported surface changed.
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`).
5. The named tests above are green and grep-locatable.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. Plan-specific: no FS-tool path is ever served without canonicalization — the
   relax changes the *decision*, never the vetting (pinned by Scenarios 1 and 5);
   and every out-of-root serve flows through an `*os.Root` (containment survives),
   never a direct `os` call (pinned by AC2.6 / AC3.5).

## Deferred decisions and known risks

- **Guardrail-routed escape checking.** ~~The `auto`-gated route through the
  modelhook checker needs a path-aware checker *route* (the matcher is
  name-only); v2 decides whether that is a new checker input field or a
  composition-level pre-check that calls `RunGuardrailCheck` directly.~~
  **Resolved by [ADR-0080](../adr/0080-guardrail-routed-escape-checking.md):**
  a composition-level pre-check inside the escape policy (option b), auto-only,
  configured by the operator-tier `guardrails.escape` knob, fail-closed to the
  write-escape Ask on a checker error. Pinned by
  `TestPathEscapePosture_GuardrailRoutedEscape`.
- **Escape Ask rule persistence.** An "allow always" on an escape Ask is v2;
  v1 asks are allow-once only (no learned out-of-root rule).
- **Edit-ledger key for out-of-root paths** must be the canonical absolute form,
  not root-relative (out-of-root paths have no root-relative form); pinned by
  AC3.3.
- **Pseudo-fs surface is broader than `/proc/self/environ`.** v1 denies
  `/proc`,`/sys`,`/dev` outright (AC1.5/AC2.5); whether a narrower allow (e.g.
  read-only `/proc/<pid>/cmdline`) is ever useful is a v2 question, default no.
- **Headless main-engine escape Ask UX.** v1 inherits the existing headless
  main-ask behaviour (await ends on cancellation — AC4.3); a dedicated
  escape-specific headless auto-deny message would be a behaviour change beyond
  "reuse the existing ask spine" and is out of v1.

### Wave-1 panel-review follow-ups (deferred to Wave 2, none a gate)

**Resolved in Wave 2 (task 03-panel-followups):**

- AC-W2-F1: an Edit-ledger read of a pseudo-fs path cannot bypass the
  pseudo-fs guard — the asymmetry is CLOSED: `escapeWorkspace` overrides
  `RecordRead`/`WasReadUnchanged` with the same `refusePseudoFS` guard
  Read/Stat/Write consult (fail-safe no-op / never-read), so the inner osfs
  `fingerprint` read is no longer the one tool-body read site the wrapper
  forgot.
  - verify: `TestPathEscapePosture_EditLedgerPseudoFSGuarded`
- AC-W2-F2: `vetRelaxedParent` delegates to `osfs.Canonicalize`
  (canonicalize-then-compare against the verbatim cleaned prefix) — no third
  hand-rolled ancestor walk; the existing osfs containment tests stay green.
  - verify: `TestPathEscapePosture_VetRelaxedParentSharesCanonicalize`
- AC-W2-F3: the `escapePolicy` classifier cache (`escapePolicy.clfs`) is
  documented as a bounded-cardinality resource — cardinality is bounded by
  construction by the distinct session workspace roots the process classifies
  (live-session-cap order, values are fd-free canonicalized strings; an LRU
  was rejected: eviction only drops a rebuildable derivation).
  - verify: inspection — [ADR-0027](../adr/0027-cloud-native.md) List 1 row 36
    names it.

The original follow-up notes (kept for the record, all three resolved above):

- **Edit-ledger `fingerprint` pseudo-fs asymmetry (Medium, defense-in-depth).**
  `osfs.Workspace.fingerprint` reads via the inner `w.fs.Read`, bypassing
  `escapeWorkspace.refusePseudoFS`. Not live today (the escape policy hard-denies
  pseudo-fs for Edit before the tool body runs), but a latent asymmetry: if v2
  ever narrows the pseudo-fs allow, the Edit ledger would read unguarded. The fix
  is not a clean one-liner (`fingerprint` is an inner-osfs method, the wrapper is
  composition); decide the routing in v2 alongside the pseudo-fs-narrowing call.
- **`vetRelaxedParent` → delegate to `osfs.Canonicalize` (Low).** Within osfs the
  ancestor-walk is now expressed three times (`resolveInRoot`, `Canonicalize`,
  `vetRelaxedParent`) with three return contracts. `vetRelaxedParent` should
  canonicalize-then-compare via the already-extracted `Canonicalize` rather than
  re-walk, so a future semantic change can't drift the containment check.
- **`escapePolicy` per-root classifier cache (Medium, resource).** `p.clfs` is an
  unbounded map keyed by session root + a mutex on the hot permission path. If
  the distinct-root cardinality is genuinely bounded by the deployment, document
  it in `docs/adr/0027-cloud-native.md` List 1; otherwise move the classifier onto
  the session-scoped `escapeWorkspace` (which already builds one) so the policy
  holds no per-root state. Wave 2 decides; not a Wave-1 gate.
- **Cross-cutting docs deliverables not yet done.** The plan names a
  `docs/design/IMPLEMENTATION-NOTES.md` "Path-escape posture" section and a
  `user-docs/` note; Wave 1 shipped code + the acceptance doc only. Land both
  with Wave 2.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan
is satisfied.
