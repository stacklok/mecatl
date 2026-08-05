# ADR 0094 — Opt-in project ingestion: two-axis positive grants

- Status: Superseded
- Superseded by: [ADR 0095](./0095-root-aware-project-trust.md)
- Date: 2026-08-05
- Scope: operator-tier trust/policy — splitting project-tier ingestion and the subagent shell into two independent positive grants, with a fail-safe headless default
- Supersedes: [ADR 0092](./0092-no-project-trust-pin.md)

## Context

The posture ladder ([ADR 0022](./0022-allow-all-posture.md)) raises `TrustProject=true` at
`trusted`/`auto`/`yolo` tiers. Trust governs two things:

1. Whether the repo's **project-tier authority set** is ingested — AGENTS.md/CLAUDE.md,
   `.mecatl`/`.claude` rules, agent defs, skills, project soul, slash commands, project
   ALLOW rules, and the git snapshot.
2. Whether a **read-only subagent shell** exists — the gate lives in workspace trust
   ([ADR 0023](./0023-workspace-trust.md)), keyed on `TrustProject`.

A scheduler (mecatequi / mecak8s) running unattended on cloned repos needs `--posture auto`
for its approval semantics. But `auto` ingests the cloned repo's project-tier steering — and
in a dark factory, whoever can push to the cloned ref controls the agent's instructions.
Lowering `TrustProject=false` would kill the approvals AND the read-only subagent shell.

**ADR 0092** shipped a `--no-project-trust` suppressor: a negative boolean that kept
`TrustProject` true while suppressing ingestion. Consumer (titlani) feedback identified a
**fail-open critique**: the suppressor's zero-value (false = "not suppressing") preserves the
default behaviour — but the operator's real intent for a scheduler on untrusted repos is
"don't ingest," and forgetting the flag silently grants ingestion. The right shape is a
**fail-safe positive grant**: the operator opts *into* ingestion, not out of it. The
suppressor is removed.

## Decision

**Split the overloaded `TrustProject` bool into TWO independent positive grants:**

1. **Ingestion axis** — `Config.ProjectIngestionGranted`. Admits the repo's project-tier
   steering. The single predicate `projectIngestionAdmitted(cfg) =
   cfg.TrustProject && cfg.ProjectIngestionGranted` (`internal/app/project_ingestion.go`)
   gates every project-tier ingestion site. `TrustProject` stays the workspace-trust fold
   (flag/declared/remembered). On HEADLESS roots the grant is **OPT-IN** (only an explicit
   `--trust-project`); on INTERACTIVE roots the posture ladder still grants it at auto/yolo
   (so a dev's own CLAUDE.md keeps working — no interactive regression).

2. **Subagent-shell axis** — `Config.SubagentShellGranted`. The read-only worktree shell,
   keyed off `posture >= auto` on BOTH roots (the tiers that already give the main agent an
   allow-all shell). Decoupled from ingestion: a scheduler gets the shell WITHOUT the
   steering. `trusted` does NOT grant the shell (it loosens nothing else).

3. **`Config.Headless`** — explicit deployment identity set by each cmd root (mecated
   `--headless`, mecatequi/mecak8s headless-default-true, mecatui false). `applyPosture`
   is root-aware on it. NOT inferred from the runtime-approver `Interactive` axis.

4. **mecatequi gains `--trust-project`** (default OFF) — the positive opt-in a scheduler
   passes for a repo it trusts. **`--no-project-trust` is REMOVED entirely.**

**`applyPosture` grant logic** (`internal/app/posture.go`): reads the PRE-raise
`cfg.TrustProject` (explicit `--trust-project` opts into ingestion on BOTH roots);
`if !cfg.Headless && posture>=auto` grants ingestion (interactive ladder); `if posture>=auto`
grants the shell (both roots). Each line only RAISES.

**The fail-safe default (the whole point):** `mecatequi --posture auto` over an untrusted
clone now yields allow-all approvals + a working child shell but NO repo steering (the
scheduler supplies its own `--instructions`). A forgotten flag degrades to a less-steered
agent, not a hijacked one — the asymmetry titlani asked for.

**Truth tables** (T=TrustProject, G=ingest granted, S=shell granted):

HEADLESS (G=T only; S=posture>=auto):
| posture | --trust-project | T | G | S |
|---|---|---|---|---|
| strict | no | F | F | F |
| strict | yes | T | T | F |
| trusted | no | F | F | F |
| trusted | yes | T | T | F |
| auto | no | F | F | **T** (fail-safe default) |
| auto | yes | T | T | T |
| yolo | no | F | F | T |
| yolo | yes | T | T | T |

INTERACTIVE (G=T OR posture>=auto; S=posture>=auto):
| posture | --trust-project | T | G | S |
|---|---|---|---|---|
| strict | no | F | F | F |
| strict | yes | T | T | F |
| trusted | no | F | F | F |
| trusted | yes | T | T | F |
| auto | no | F | **T** (dev default) | T |
| auto | yes | T | T | T |
| yolo | no | F | T | T |
| yolo | yes | T | T | T |

### Alternatives considered

**A. The negative suppressor (`--no-project-trust`, ADR 0092).** A single boolean that
suppresses ingestion when true. The zero-value (false) preserves the default behaviour, so
a forgotten flag silently grants ingestion — exactly the fail-open titlani identified.
Rejected because the positive-grant + two-axis shape is both safer (fail-safe default) and
cleaner long-term (the subagent shell is independently decoupled from ingestion, so a
scheduler that wants the shell but not the steering gets exactly that with no flag at all).

**B. Collapse the two axes onto `TrustProject`.** Rejected because lowering
`TrustProject=false` to suppress ingestion would also kill the read-only subagent shell —
the original coupling the scheduler needs to break.

## Consequences

- **A headless scheduler gets `--posture auto` with no project steering by default.**
  mecatequi / mecak8s invocations on cloned repos no longer need a suppressor flag to stay
  safe — the fail-safe default is no-ingestion. The operator adds `--trust-project` only
  when the repo is trusted and its steering is wanted.
- **The subagent shell is independently controlled.** A scheduler that wants the child
  shell but not the repo's steering gets exactly that: `--posture auto` (shell on, ingestion
  off). The two axes are observable in the `--print-posture` breakdown.
- **`--no-project-trust` is removed.** No binary accepts it. The `no-project-trust:` YAML
  key is likewise removed from the operator settings schema. The ADR 0092 suppressor is
  superseded.
- **No engine API change.** Composition-only (two new bools on `app.Config`, a new
  `Headless` field, and the two-axis `applyPosture` logic). `project_ingestion.go` is the
  single-source helper.
- **Code size.** The old suppressor file is replaced by
  `internal/app/project_ingestion.go`; `posture.go` gains the two-axis grant logic; flags
  are removed from mecated/mecak8s/mecatequi and added to mecatequi. No domain change.

## See also

- [ADR 0022 — Unattended / allow-all posture](./0022-allow-all-posture.md) — the posture ladder that sets `TrustProject=true` at `auto`/`yolo`.
- [ADR 0023 — Workspace Trust](./0023-workspace-trust.md) — what `TrustProject` gates (the project authority set + the subagent shell).
- [ADR 0092 — Project-trust suppression pin](./0092-no-project-trust-pin.md) — the superseded negative-suppressor design.
- [Issue #359](https://github.com/stacklok/mecatl/issues/359) — the feature request.
- `internal/app/project_ingestion.go` (`projectIngestionAdmitted`) — the single-source helper.
- `internal/app/posture.go` (`applyPosture`) — where the two-axis grants are derived.
- `docs/usage/workspace-trust.md` — the operator walkthrough of the opt-in ingestion design.