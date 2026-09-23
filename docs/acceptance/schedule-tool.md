# Schedule tool — acceptance plan

**Phase:** capability — in-chat scheduled tasks
**Status:** landed, 2026-07-24. Follow-up to issue #189: scheduling becomes a model-facing, on-by-default affordance instead of an operator-only infra feature.
**ADR:** [ADR-0073](../adr/0073-schedule-tool.md) — the model-facing `Schedule` tool, on-by-default scheduler, and the removal of the declarative settings block + `mecated schedules` CLI.
**Accumulator branch:** `acc/schedule-tool` (off `main`).

The smallest set of work that makes scheduled tasks usable from inside the
conversation: a `Schedule` catalog tool the model (and the human, via the agent)
calls to create/list/inspect/pause/resume/delete/fire a schedule, the scheduler
ticking by default on any schedule-capable store, and the redundant operator
authoring surfaces (the `settings.yaml` `schedules:` block, the `mecated
schedules` CLI) deleted.

The doc is organized scenario-first because acceptance is about what the
running harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0059](../adr/0059-scheduled-tasks.md) — the durable registry, tick loop,
  leader-lease, claim-before-fire at-most-once, fresh-session-per-fire, and
  posture-pinning are KEPT; only the "no in-band create" operator-only posture
  and the declarative/CLI surfaces change.
- [ADR-0070](../adr/0070-model-visible-affordance-gate.md) — the `Schedule`
  tool is a model-facing affordance, so it ships a prompt-layer instruction +
  a test proving the instruction lands in the built engine's system prompt.
- Fire-time result **delivery back into the originating chat** ("ping me when
  X") is OUT of scope (deferred — see Out of scope); the fire stays pull-only
  (`GetFire`/`ListFires`, the `/schedule` overlay), per ADR 0059 decision #8.

## In scope — 4 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable;
later scenarios assume earlier ones but don't change their acceptance criteria.
Within each scenario, ACs progress trivial happy path → richer happy path →
edges → cross-cutting.

### Scenario 1 — the `Schedule` catalog tool

The model calls a `Schedule` tool from the catalog to manage schedules in-chat.
The tool is a thin, validated adapter over the EXISTING `Service` schedule seam
(`CreateSchedule`/`GetSchedule`/`ListSchedules`/`UpdateSchedule`/`DeleteSchedule`/
`PauseSchedule`/`ResumeSchedule`/`FireNow`/`ListFires`) — the same create-seam
(`validateScheduleSpec` + `applyScheduleDefaults`) the REST/gRPC handlers call,
never a second path. Composition injects the seam into the tool exactly as it
injects the child-engine factory into the Subagent tool
([`AGENTS.md` — the layering rule](../../AGENTS.md): the loop is storage-agnostic;
no adapter/proto/server type crosses into `engine/agent`). The tool is registered
in the catalog for every session whose store backs a `ScheduleStore`, the same
conditional-registration shape as the six memory tools
([`internal/app/catalog.go`](../../internal/app/catalog.go)).

**Work:**
- engine app (`engine/agent`): the `Schedule` tool — verbs
  `create|list|inspect|pause|resume|delete|fire`, args → the injected schedule
  seam, results rendered as model-readable text (name, trigger, next fire,
  enabled, last-fire stop reason). Read-only verbs (`list`/`inspect`) report
  `ReadOnly()==true`; mutating verbs (`create`/`pause`/`resume`/`delete`/`fire`)
  report `ReadOnly()==false` so dispatch stays read-parallel/mutate-serial
  ([`AGENTS.md` — dispatch invariant](../../AGENTS.md)).
- ports (`engine/port`): a narrow consumer-local `ScheduleManager` interface
  (the consumer-defined-port idiom — the Subagent tool's injected
  `port.SessionStore` via `WithSubagentStore` is the precedent, NOT the memory
  tools, which are host adapters over a domain `engine/tool` store) that
  composition satisfies with the `Service` methods — the tool never imports
  `internal/adapter/server`.
- adapters / composition (`internal/app`): wire the `Service` seam into the
  tool and register it in the catalog when `scheduleStore() != nil`; add the
  `ScopeBuiltinDefault` floor Allow for the tool name so it is pre-approved but
  config-overridable (the memory-tool floor precedent). Thread
  `SchedulerMinInterval` into the create-seam (`validateScheduleSpec`, currently
  signature-absent) so the cadence floor is enforced at the SAME seam the tool
  and the REST handler both ride — the load-bearing edit for AC1.3.

**Acceptance:**
- AC1.1: A `Schedule` tool is present in the catalog of a session backed by a
  `ScheduleStore`, and absent (honest, not a stub) on a store with no
  `ScheduleStore` — `ServerCapabilities.Scheduling` and the tool registration
  agree.
  - verify: `TestScheduleTool_RegisteredOnlyWhenStoreBacked`
- AC1.2: `Schedule create` with a cron expression saves an enabled schedule whose
  `NextFireAt` is the cronparse-computed first fire, and rejects (fail-closed) an
  invalid cron, a non-plan non-mutating mode, or an empty workspace on a
  default-profile schedule — the SAME validation the REST create-seam enforces.
  - verify: `TestScheduleTool_CreateValidatesLikeRESTSeam`
- AC1.2b: `Schedule create` enforces the create-seam's Phase-2 field rules by
  name: `oneShotRetry: true` on a cron trigger is rejected fail-closed (a cron
  self-heals via misfire), and a one-shot with `oneShotRetry: true` and no
  `maxRetries` gets the create-seam default of 3 — so the tool cannot be wired
  to a subset of the seam.
  - verify: `TestScheduleTool_CreateEnforcesPhase2FieldRules`
- AC1.2c: `Schedule create` rejects an unknown/uncatalogued provider+model
  selector at create time (fail-closed, like an invalid cron) — a schedule fire
  must not silently target a provider the deployment never configured, surfacing
  hours later as a fire-time failure.
  - verify: `TestScheduleTool_CreateRejectsUnknownSelector`
- AC1.3: `Schedule create` rejects a cadence tighter than the configured
  `SchedulerMinInterval` frequency floor (the floor is now CONSULTED at the
  in-band create verb, no longer inert).
  - verify: `TestScheduleTool_CreateEnforcesMinInterval`
- AC1.4: The read-only verbs (`list`, `inspect`) report `ReadOnly()==true` and the
  mutating verbs (`create`, `pause`, `resume`, `delete`, `fire`) report
  `ReadOnly()==false`, so a mutating Schedule call never runs concurrently with a
  sibling read.
  - verify: `TestScheduleTool_ReadOnlyPartition`
- AC1.5: `Schedule fire <name>` returns the fire id + session id of the minted
  `sched--` session (the synchronous-to-terminal FireNow seam), and
  `Schedule list`/`inspect` surface the fire's terminal stop reason.
  - verify: `TestScheduleTool_FireAndInspectRoundTrip`
- AC1.5b: `Schedule fire <name>` on a schedule with a fire already in flight
  returns the singleton-overlap error (the same `ErrFireNowOverlap` the REST
  `FireNow` returns), not a second concurrent fire — the create-seam's
  default-true `Singleton` holds through the tool.
  - verify: `TestScheduleTool_FireOverlapRejected`
- AC1.6: A schedule created via the tool is visible, pausable, resumable, and
  deletable through the SAME store the REST/gRPC surface reads (one store, one
  truth — no tool-specific shadow state).
  - verify: `TestScheduleTool_SharesStoreWithRESTSurface`

---

### Scenario 2 — the scheduler is ON by default

`SchedulerEnabled` defaults to true whenever the configured store exposes a
`ScheduleStore`; the `--scheduler` opt-in flag is replaced by a `--no-scheduler`
disable knob (mecak8s keeps its own equivalent). A store with no `ScheduleStore`
(the in-memory default: mecademo, mecatequi, offline tests) stays on the
byte-identical no-scheduling path — the tool is absent, the capability bit is
false, no tick goroutine starts. This flips ADR 0059's "wires a scheduler ONLY
when an operator selects a backend by flag" ([ADR-0059](../adr/0059-scheduled-tasks.md),
[ADR-0073](../adr/0073-schedule-tool.md) decision 2).

**Work:**
- composition (`internal/app` / `cmd/mecated` / `cmd/mecak8s`): default
  `SchedulerEnabled` to true on a schedule-capable store; replace the bool
  opt-in flag with the disable knob; keep the no-store path byte-identical.
- the `--schedule-fire-retention` 7d default (already keyed on "scheduler on")
  now applies on the default path too.

**Acceptance:**
- AC2.1: With a durable (`--store-dir`) store and no flag, the scheduler ticks
  and a due schedule fires WITHOUT `--scheduler` being passed.
  - verify: `TestScheduleTool_SchedulerOnByDefault`
- AC2.1b: With a durable store and no retention flag, a `sched--` fire session
  older than 7d is swept by the `ScheduleFireRetention` GC pass (the 7d default
  now activates without `--scheduler`); an explicit `--schedule-fire-retention=0`
  disables it.
  - verify: `TestScheduleTool_FireRetentionDefaultActiveOnDefaultPath`
- AC2.2: `--no-scheduler` restores the pre-change behaviour (no tick loop, no
  auto-fire) on a schedule-capable store; the create/list/fire API still works
  (manual management is independent of the tick loop).
  - verify: `TestScheduleTool_NoSchedulerDisablesTickOnly`
- AC2.5: The old `--scheduler` opt-in flag is DELETED outright (no deprecated
  alias, no no-op): `mecated --scheduler` fails fast with the standard
  unknown-flag startup error. The migration note lives in the changelog/docs,
  not in kept code.
  - verify: `TestScheduleTool_SchedulerFlagRemoved`
- AC2.3: With the in-memory store (no `ScheduleStore`), startup neither ticks
  nor fails, `ServerCapabilities.Scheduling` is false, and the `Schedule` tool
  is absent — the byte-identical default.
  - verify: `TestScheduleTool_InMemoryStoreByteIdentical`
- AC2.4: mecatui's embedded server advertises `Scheduling` and auto-fires on its
  default per-workspace store (no flag), so the `/schedule` overlay and in-chat
  fires work out of the box.
  - verify: `TestScheduleTool_TuiEmbeddedSchedulerOn`

---

### Scenario 3 — delete the declarative settings block + the CLI

The operator-tier `schedules:` YAML subtree and the `mecated schedules <verb>`
subcommand group are removed outright (not deprecated). The in-chat `Schedule`
tool replaces both; the gRPC `ScheduleService` + REST `/v1/schedules` surface
(the mecatui `/schedule` overlay's transport, and out-of-band management) is
RETAINED ([ADR-0073](../adr/0073-schedule-tool.md) decision 3). The removal
deletes the project-tier-ignored-with-WARN special case, the startup reconcile
machinery, and their tests.

**Work:**
- adapters (`internal/adapter/permconfig`): delete the `schedules:` schema
  (`SchedulesSection`, `Resolver.OperatorSchedules`) and its strict-parse tests
  outright — no presence marker, no WARN machinery. A residual `schedules:` key
  in an old config is silently ignored by the lenient top-level decode, the
  same as any other removed YAML key.
- composition (`internal/app`): delete `foldOperatorSchedules` +
  `reconcileSchedules` (`internal/app/schedules.go`) and the
  `Config.DeclaredSchedules` fold; startup no longer reconciles declared
  schedules.
- cmd (`cmd/mecated`): delete `schedules_cmd.go` (+ tests) and the
  `os.Args[1]=="schedules"` dispatch in `main.go`.
- docs: drop the `schedules:` block from `user-docs/reference/configuration.md`,
  the declarative + CLI sections from `user-docs/`, and update
  `docs/architecture.md`'s scheduled-tasks section.

**Acceptance:**
- AC3.1: No `schedules:` key is honoured from any settings tier. The schema is
  deleted outright; a residual `schedules:` block in an old config file is
  silently ignored by the lenient top-level decode (no hard failure, no WARN
  machinery carried for a removed feature).
  - verify: `TestScheduleTool_SettingsSchedulesBlockRemoved`
- AC3.2: `mecated schedules <verb>` is gone; a bare `mecated schedules` falls
  through to normal startup (or an unknown-subcommand error), never to the
  deleted HTTP client.
  - verify: `TestScheduleTool_SchedulesCLIRemoved`
- AC3.3: The gRPC `ScheduleService` and REST `/v1/schedules` routes still serve
  create/list/get/update/delete/pause/resume/fire/fires (the overlay + out-of-band
  surface is unaffected by the settings/CLI removal).
  - verify: `TestScheduleTool_WireSurvivesSettingsCLIRemoval`

---

### Scenario 4 — model-visible affordance + permission posture

The `Schedule` tool is a model-facing affordance, so per
[ADR-0070](../adr/0070-model-visible-affordance-gate.md) it ships a prompt-layer
instruction (a posture note naming the tool + its exact expected use) and a test
proving the instruction lands in the BUILT engine's system prompt via the real
factory path. The tool is floor-scoped (`ScopeBuiltinDefault` Allow), so it is
pre-approved but config-overridable under EVERY posture tier, and a
`mutating: true` create is still gated by the session's posture (plan-mode
hard-deny vetoes the mutating create)
([`AGENTS.md` — permission resolution](../../AGENTS.md),
[ADR-0073](../adr/0073-schedule-tool.md) decision 4).

**Work:**
- composition (`internal/app`): a `schedulePostureNote` + `applySchedulePosture`
  (the `applyNoFSPosture`/`applyPlanModePosture` idiom) that appends the
  Schedule-tool instruction to the main engine's Role suffix.
- engine app / composition: the floor Allow + the mutating-create posture gate.

**Acceptance:**
- AC4.1: The built engine's system prompt (`req.System.StablePrefix`) contains
  the Schedule-tool instruction via the REAL factory path, so deleting the
  wiring fails CI.
  - verify: `TestScheduleTool_EngineSystemPromptContainsScheduleContract`
- AC4.2: The `Schedule` tool resolves as a `ScopeBuiltinDefault` Allow (no ask)
  under the default `auto` posture AND under `strict`/`trusted`, and remains
  config-overridable (an operator deny binds it).
  - verify: `TestScheduleTool_FloorScopedAllowAllTiers`
- AC4.3: A `mutating: true` create in a plan-mode session is denied (the
  plan-mode hard-deny on mutations); a read-leaning (`mutating: false`) create
  is allowed in plan mode.
  - verify: `TestScheduleTool_MutatingCreateGatedByPlanMode`
- AC4.4: `TestScheduleTool_Scenario4_FullInChatFlow` — an offline engine drives
  the model to create a schedule, list it, and fire it, and the fire mints a
  `sched--` session that runs to a terminal stop.
  - verify: `TestScheduleTool_Scenario4_FullInChatFlow`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Fire-result delivery BACK into the originating chat ("ping me when X") | follow-up | [ADR-0059](../adr/0059-scheduled-tasks.md) decision #8 (pull-only v1) |
| `mecak8s schedules` CLI (never existed — no surface to remove) | n/a | parity with the existing policy |
| In-overlay schedule Create form + NL→cron in mecatui | later phase | `user-docs/` scheduled-tasks note |
| Per-schedule reasoning-effort override | later phase | [ADR-0055](../adr/0055-reasoning-effort.md) / ADR 0059 (port omits effort) |
| `singleton: false` overlap opt-out (real bool) | engine-port/proto change | [ADR-0059](../adr/0059-scheduled-tasks.md) / config-reference note |

## Cross-cutting deliverables

- `docs/adr/0073-schedule-tool.md` (this plan's ADR — added alongside).
- `docs/architecture.md` scheduled-tasks section: rewrite for the model-facing
  tool, on-by-default scheduler, and the removed settings/CLI surfaces.
- `user-docs/`: replace the declarative-settings + `mecated schedules` CLI
  sections with the in-chat `Schedule` tool + on-by-default behaviour.
- `user-docs/reference/configuration.md`: remove the `schedules` subtree.
- `engine/api/*.txt` + `engine/CHANGELOG.md` if the tool/registration changes
  the engine's exported surface (`task api:update`).
- configuration-reference regeneration + the matlatl strict link gate (`task docs`).

## Sequencing recommendation

Scenario 1 (the tool) is the keystone and lands first — Scenarios 2–4 assume a
working in-band create verb. Scenario 3 (the removal) can land in parallel with
Scenario 2 (default-on) but should land AFTER Scenario 1 so the in-chat
replacement exists before the operator surfaces disappear. Scenario 4 (posture
+ prompt affordance) lands last, hardening the tool.

## Named tests landing in this plan

- `TestScheduleTool_RegisteredOnlyWhenStoreBacked`
- `TestScheduleTool_CreateValidatesLikeRESTSeam`
- `TestScheduleTool_CreateEnforcesPhase2FieldRules`
- `TestScheduleTool_CreateRejectsUnknownSelector`
- `TestScheduleTool_CreateEnforcesMinInterval`
- `TestScheduleTool_ReadOnlyPartition`
- `TestScheduleTool_FireAndInspectRoundTrip`
- `TestScheduleTool_FireOverlapRejected`
- `TestScheduleTool_SharesStoreWithRESTSurface`
- `TestScheduleTool_SchedulerOnByDefault`
- `TestScheduleTool_FireRetentionDefaultActiveOnDefaultPath`
- `TestScheduleTool_NoSchedulerDisablesTickOnly`
- `TestScheduleTool_SchedulerFlagRemoved`
- `TestScheduleTool_InMemoryStoreByteIdentical`
- `TestScheduleTool_TuiEmbeddedSchedulerOn`
- `TestScheduleTool_SettingsSchedulesBlockRemoved`
- `TestScheduleTool_SchedulesCLIRemoved`
- `TestScheduleTool_WireSurvivesSettingsCLIRemoval`
- `TestScheduleTool_EngineSystemPromptContainsScheduleContract`
- `TestScheduleTool_FloorScopedAllowAllTiers`
- `TestScheduleTool_MutatingCreateGatedByPlanMode`
- `TestScheduleTool_Scenario4_FullInChatFlow`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — configuration reference regenerated and the matlatl strict link gate green.
3. `task api:check` passes (or `task api:update` was run and the
   `engine/CHANGELOG.md` note is present) — the `Schedule` tool touches the
   engine's exported surface.
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`).
5. The named tests (`TestScheduleTool_*`) are green and grep-locatable by their
   identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session (in-memory store
   → no scheduling, byte-identical).
7. An offline engine drives an in-chat create → list → fire round trip and the
   fire mints a terminal `sched--` session (AC4.4).

## Deferred decisions and known risks

- **Fire-result delivery channel.** Routing a fire's result back into the
  originating conversation is the real "ping me when X" loop and is deliberately
  deferred — it needs a session-reentry/delivery design, not a schedule change.
- **Model-minted recurring work.** Bounded at create (cadence floor + posture
  pin + selector validation), fire (per-fire limits + singleton), and the
  overridable floor Allow — but a hostile prompt could still mint many
  read-leaning schedules; a per-session/per-principal schedule-count cap is a
  possible follow-up if abuse emerges.
- **`mode: acceptEdits` on an unattended fire (accepted trade-off).** A model
  running under `default` mode (writes ask a human) can mint a
  `mutating: true` + `mode: acceptEdits` schedule whose fires auto-approve every
  write with no human present — a temporal oversight downgrade, not a capability
  escalation (the model could already write directly). Accepted: `acceptEdits`
  is a legitimate operator posture, and an operator who wants to forbid it
  denies the `Schedule` tool name (the floor Allow is config-overridable) or
  denies `acceptEdits` schedules via the permission config. Not blocked at the
  create-seam; re-evaluate if it proves surprising in practice.
- **Breaking change.** Removing the `schedules:` settings block, the CLI, and
  the `--scheduler` flag is called out in ADR 0073; a deployment relying on
  them must move to the REST/gRPC API or the OS scheduler. The removals are
  CLEAN (no deprecated aliases, no WARN shims): a residual `schedules:` key is
  silently ignored by the lenient decode, and `--scheduler` fails fast as an
  unknown flag. The 7d `--schedule-fire-retention` default now activates on the
  default path (AC2.1b) — an operator who wants fire sessions kept forever sets
  `--schedule-fire-retention=0` explicitly.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this
plan is satisfied.
