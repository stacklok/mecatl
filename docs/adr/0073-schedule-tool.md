# ADR 0073 — Schedule tool: model-facing scheduling, on-by-default, drop the declarative/CLI surfaces

- Status: Accepted
- Date: 2026-07-24
- Scope: the scheduled-tasks feature's USER-facing surface (issue #189, follow-up) — a model-facing `Schedule` catalog tool (create/list/inspect/pause/resume/delete/fire) wired through the existing `Service` seam, the scheduler ON by default, and the removal of the operator-tier `schedules:` settings block + the `mecated schedules` CLI
- Supersedes: [ADR 0059](./0059-scheduled-tasks.md) — **decision #5's "no create API in Phase 1" operator-only posture and the Phase 2b declarative-settings/CLI surfaces only.** The durable registry, tick loop, leader-lease, claim-before-fire at-most-once, fresh-session-per-fire, and posture-pinning decisions stand.
- Superseded by: none

## Context

ADR 0059 shipped scheduling as an **operator/infra** feature: schedules were
curated by a deployment operator via the gRPC/REST API, the operator-tier
`settings.yaml` `schedules:` block, and the `mecated schedules` CLI; the tick
loop was OFF by default (`--scheduler`). The recurring user feedback is that
this shape is **not useful as a product**: the person who wants a scheduled task
is the human/agent **in the conversation** ("check CI every hour and ping me"),
not a deployment operator hand-editing a YAML file. An operator who wants a
nightly unattended job already has `cron` / `systemd timers` — the harness
duplicating that at the settings layer adds nothing. The forces at play:

1. **The in-chat affordance is the product.** The value of scheduling inside an
   agentic harness is that the MODEL can register a task mid-conversation (a
   reminder, a poll, a follow-up) and the human can steer it in the same chat.
   That requires a `Schedule` catalog tool, on by default, like `Skill`/`Remember`.

2. **The settings/CLI surfaces are redundant with the OS.** A declarative
   `schedules:` block and a `mecated schedules create` CLI answer "how does an
   operator register a recurring job" — which the OS already answers better.
   Keeping three authoring surfaces (settings, CLI, API) for a feature whose
   real surface is the chat is maintenance dead weight.

3. **On-by-default is the only useful default.** A scheduler that is off unless
   an operator finds a flag is a feature nobody discovers. But ON-by-default
   must not change the byte-identical no-scheduling path for a store that can't
   back it, and must not let the MODEL mint an unbounded autonomous workload.

4. **The model is an untrusted scheduler author.** A `Schedule` tool lets the
   model create recurring self-spawning work. The existing ADR 0059 guards
   (posture-pinned, read-leaning default, `MaxTurns`/`MaxFires`/`MinInterval`
   bounds, singleton overlap suppression) already bound a fire; the new risk is
   **create-time** (the model minting a tight-cadence or runaway schedule), so
   the cadence floor and the permission posture move to the tool gate.

The design resolves these into four decisions, recorded here as a frozen record;
current behaviour lives in `docs/architecture.md`.

## Decision

1. **A model-facing `Schedule` catalog tool is the single authoring surface.**
   The tool (engine-side, `engine/agent` like the Subagent/Team tools, or a
   catalog adapter registered in composition) exposes the verbs `create`, `list`,
   `inspect`, `pause`, `resume`, `delete`, `fire` over the existing
   `Service.CreateSchedule/ListSchedules/…` seam — the SAME validated
   create-seam (`validateScheduleSpec` + `applyScheduleDefaults`) the REST/gRPC
   handlers already call, never a second path. It is registered in the catalog
   for every session that has a backing `ScheduleStore`, exactly like the six
   memory tools are registered when a memory store is wired. The gRPC
   `ScheduleService` + REST `/v1/schedules` surface is RETAINED as the thin
   transport for the mecatui `/schedule` overlay and for out-of-band management;
   only the *declarative settings block* and the *CLI* are removed (decision 3).

2. **The scheduler is ON by default; the flag becomes a disable knob.**
   `SchedulerEnabled` defaults to true whenever the configured store exposes a
   `ScheduleStore` (jsonlstore `--store-dir`, redisstore `--redis-url`, or a
   driver store that implements it). The `--scheduler` bool is DELETED and
   replaced by `--no-scheduler` (mecak8s keeps its own equivalent), so the
   default path for a schedule-capable store ticks; a store with no
   `ScheduleStore` stays on the byte-identical no-scheduling path (the tool is
   absent, the capability bit false). `--scheduler` is NOT kept as a deprecated
   alias — it fails fast as an unknown flag (clean removal, no carried shim).
   The memstore default (mecademo, mecatequi, tests) is unchanged: no store →
   no scheduling. This flips ADR 0059's "wires a scheduler ONLY when an operator
   selects a backend by flag" to "wires it whenever the backend supports it,
   unless the operator opts out."

3. **Delete the operator-tier `schedules:` settings block and the `mecated
   schedules` CLI.** The `schedules:` YAML subtree
   (`permconfig.Resolver.OperatorSchedules`, `foldOperatorSchedules`,
   `reconcileSchedules`) and the `schedules <verb>` subcommand group
   (the schedules subcommand group, since deleted from cmd/mecated) are removed outright — not deprecated, and
   with NO presence-marker / WARN shim carried for the removed key (a residual
   `schedules:` block in an old config is silently ignored by the lenient
   top-level decode, the same as any removed YAML key). The in-chat `Schedule`
   tool is the replacement for both; an operator with a genuine recurring-job
   need uses the OS scheduler. This removes the project-tier-ignored-with-WARN
   special case and the reconcile-on-startup machinery entirely.

4. **The `Schedule` tool is posture- and cadence-bounded, floor-scoped like the
   memory tools.** The tool is a `ScopeBuiltinDefault` Allow (pre-approved but
   config-overridable — the same floor as `Remember`/`Recall`), so it works
   out-of-the-box under the default `auto` posture AND under `strict`/`trusted`
   (a schedule CREATE does not itself mutate the workspace; the FIRE's posture
   is pinned at create-time by the existing `Mutating`/`Mode` invariant). The
   existing `SchedulerMinInterval` frequency floor is now CONSULTED at the
   tool's create verb (it was inert while there was no in-band create API): a
   cadence tighter than the operator's floor is rejected fail-closed. A
   non-mutating schedule still must run in plan mode; a mutating schedule still
   requires the explicit `mutating: true` opt-in. Plan-mode sessions can create
   (read-leaning) schedules; only a `mutating: true` create is gated by the
   session's posture (plan-mode hard-deny still vetoes the mutating create).

## Consequences

- **A schedule created in-chat is indistinguishable from an API-created one.**
  The tool rides the same create-seam, the same `sched--` fire family, the same
  GC retention, and the same `ScheduleService` read surface — so the mecatui
  `/schedule` overlay and `GetFire`/`ListFires` work on chat-created schedules
  with no special-casing.

- **Fire-time delivery is unchanged (pull-only).** ADR 0059 decision #8 stands:
  a fire mints a fresh `sched--` session and ends there; the result is pulled
  via `GetFire`/`ListFires` or the overlay. Routing a fire's result BACK into
  the originating conversation (the "ping me when X" loop) is a follow-up
  concern (a delivery channel), deliberately out of scope here.

- **The removal is breaking for the settings/CLI user, and CLEAN (no shims).**
  A deployment that declared schedules in `settings.yaml`, managed them via
  `mecated schedules`, or passed `--scheduler` loses those surfaces with no
  deprecated alias or WARN shim kept behind: the settings key is silently
  ignored by the lenient decode, and `--scheduler` fails fast as an unknown
  flag. This is an accepted, called-out breaking change: the in-chat tool + the
  retained REST/gRPC API + the OS scheduler cover the use cases, and the
  settings/CLI were the redundant path. The mecatui `/schedule` overlay
  (REST/gRPC consumer) is UNAFFECTED.

- **A model can now mint recurring work — bounded at three points.** Create-time
  (the cadence floor + posture pin), fire-time (the existing per-fire
  `MaxTurns`/`MaxToolCalls`/`MaxFires` bounds and singleton suppression), and
  the posture floor (the tool is overridable, never a privilege escalation). The
  guards that made an operator-created schedule safe apply verbatim to a
  model-created one.

- **Default-on is honest about capability.** `ServerCapabilities.Scheduling`
  already reports `scheduleStore() != nil`; with the scheduler on by default the
  bit and the tick loop agree (a store-backed server both advertises AND
  auto-fires). A store with no `ScheduleStore` still reports false and never
  ticks.

## See also

- [ADR 0059 — Scheduled tasks](./0059-scheduled-tasks.md) — the substrate this
  amends (registry, tick loop, leader-lease, at-most-once, fire model).
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — the ports + inventory
  discipline the scheduler rides.
- [ADR 0070 — Model-visible affordance gate](./0070-model-visible-affordance-gate.md) —
  the rule the `Schedule` tool satisfies (a model-facing affordance needs a
  prompt-layer instruction + a test proving it lands).
- [`docs/architecture.md`](../architecture.md) — the living reference.
