# ADR 0092 — Project-trust suppression pin

- Status: Superseded
- Superseded by: [ADR 0095](./0095-root-aware-project-trust.md) (via the intermediate ADR 0094)
- Date: 2026-08-04
- Scope: operator-tier trust/policy — suppressing project-tier steering ingestion while preserving the posture ladder's approvals and the child-shell gate

## Context

The posture ladder ([ADR 0022](./0022-allow-all-posture.md)) raises `TrustProject=true` at
`trusted`/`auto`/`yolo` tiers. Trust governs two things:

1. Whether the repo's **project-tier authority set** is ingested — AGENTS.md/CLAUDE.md,
   `.mecatl`/`.claude` rules, agent defs, skills, project soul, slash commands, project
   ALLOW rules, and the git snapshot.
2. Whether a **read-only subagent shell** exists — the gate lives in workspace trust
   ([ADR 0023](./0023-workspace-trust.md)), keyed on `TrustProject`.

A scheduler (mecatequi / mecak8s) running unattended needs `--posture auto` for its
approval semantics. But `auto` ingests the cloned repo's project-tier steering — and in a
dark factory, whoever can push to the cloned ref controls the agent's instructions. Lowering
`TrustProject=false` would kill the approvals AND the read-only subagent shell. The operator
needs a pin that decouples the two: keep auto's approvals, drop the repo's steering.

## Decision

Add a new **operator-tier** knob: `--no-project-trust` (CLI flag, default OFF) +
`no-project-trust:` in `settings.yaml` (operator-tier ONLY — a project file's key is IGNORED
with a WARN). It backs `Config.NoProjectIngest` and a single-source helper:

```go
// internal/app/no_project_trust.go
func ingestProjectTier(cfg Config) bool {
    return cfg.TrustProject && !cfg.NoProjectIngest
}
```

AGENTS.md/CLAUDE.md — the highest-value injection point — IS gated by the pin. The scheduler
supplies its own `--instructions`; the repo's unchecked prompt content must not reach the
model.

Design constraints:

- **Separate `NoProjectIngest` from `TrustProject`.** Collapsing the pin onto
  `TrustProject=false` would also kill the read-only subagent shell at
  `build.go:4497` — a scheduler running on third-party repos wants to keep explorer
  subagents with real shells.
- **The pin WINS over posture.** `applyPosture` never sets `NoProjectIngest` (it only
  derives `AllowAllTools`, the substitution loosening, and the `TrustProject` floor). So
  `auto`/`yolo` can't re-raise ingestion.
- **Operator-tier only.** CLI out-ranks the operator YAML. The `NoProjectTrustFlagSet`
  bool tracks the CLI source, separate from the YAML value — so an explicit CLI `false`
  wins, mirroring the `NoSoulFlag`/`NoUserModelFlag` precedent.
- **No engine API change.** Composition-only (`grep -rn "NoProjectIngest\|ingestProjectTier\|NoProjectTrust" engine/` is empty).
- **AGENTS.md gating is explicit.** `ingestProjectTier` gates the AGENTS.md/CLAUDE.md
  discovery AND every other project-tier source in one call site — the single helper is
  the single source of truth; a second gate point would drift.

The pin is a negative boolean by intent: `--no-project-trust` describes what it *removes*
(project-injected steering), and its zero-value (false = not suppressing) keeps the default
behaviour unchanged — the operator opts out of project ingestion, not into it.

## Consequences

- **A scheduler gets `--posture auto` with no project steering.** mecatequi / mecak8s
  invocations on cloned repos add `--no-project-trust` and keep auto-approval, child-shell,
  and child-injection defence.
- **AGENTS.md is the primary surface gated.** It is the repo's only unfenced, unrestricted
  injection into the system prompt — every other project surface is already trust-gated or
  fenced by mechanism. The pin gating it closes the last direct-instruction channel a dark
  push can exploit.
- **Bypass is CLARITY, not subtlety.** An operator who sets `--posture auto
  --no-project-trust` reads what they are doing on the command line; if they later add
  `--trust-project`, the `resolvePosture` MAX-fold yields `trusted` (which sets
  `TrustProject=true`), and `ingestProjectTier` returns `true ∧ !NoProjectIngest` — the
  explicit flag still wins. The resolution is unambiguous.
- **No new trust knob.** Trust stays `TrustProject`. The pin is a separate dimension
  (suppress-ingestion), not a third trust state. The workspace-trust ADR's phases,
  registry, and drift anchor are untouched.
- **Code size.** One file (`internal/app/no_project_trust.go`), one boolean on
  `app.Config`, one flag per binary, one gate at the ingestion sites. No domain change.

## See also

- [ADR 0022 — Unattended / allow-all posture](./0022-allow-all-posture.md) — the posture ladder that sets `TrustProject=true` at `auto`/`yolo`.
- [ADR 0023 — Workspace Trust](./0023-workspace-trust.md) — what `TrustProject` gates (the project authority set + the subagent shell).
- [Issue #359](https://github.com/stacklok/mecatl/issues/359) — the feature request.
- `internal/app/no_project_trust.go` (`ingestProjectTier`) — the single-source helper.
- `internal/app/posture.go` (`applyPosture`) — where `TrustProject` is derived from posture.
- `docs/usage/workspace-trust.md` — the operator walkthrough of `--no-project-trust`.
