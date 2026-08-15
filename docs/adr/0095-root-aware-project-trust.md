# ADR 0095 — Root-aware project trust

- Status: Accepted
- Date: 2026-08-05
- Scope: operator-tier workspace trust, project steering ingestion, and read-only delegation shell admission
- Supersedes: [ADR 0092](./0092-no-project-trust-pin.md), [ADR 0094](./0094-opt-in-project-ingestion.md)
- Related: [ADR 0202](./0202-diagnostic-only-posture-reporting.md) (reporting surface only — it does NOT supersede this decision)

> **Revision history.** This rule was reached in iterations over a single day, none of
> which shipped in a release: ADR 0092 proposed a negative `--no-project-trust`
> suppressor; ADR 0094 replaced it with a two-axis positive-grant split
> (`ProjectIngestionGranted` + `SubagentShellGranted`); review and the live e2e found the
> two-axis shape both over-trusted headless auto (the worktree shell must not be
> posture-granted) and under-trusted interactive strict runs. This ADR is the single
> authoritative statement of the final rule — one root-aware `TrustProject` fold — and
> supersedes 0092 and 0094. ADR 0202 is an orthogonal reporting-surface decision layered
> on top.

## Context

Headless schedulers need `--posture auto` for unattended permission behavior, but must not automatically trust a freshly cloned repository. The original posture ladder raised `TrustProject` at `trusted`/`auto`/`yolo` for every deployment root, so headless auto admitted repository steering.

ADR 0094 corrected the ingestion default with a second `ProjectIngestionGranted` value and an intermediate `SubagentShellGranted` design. The issue-#40 security correction established that the read-only worktree shell cannot be posture-granted independently: creating or using the worktree invokes git against the repository's `.git`, so the operator must vouch for that repository. Once an untrusted headless repository intentionally gets neither steering nor the worktree shell, the second ingestion grant has no legitimate production state. It also created an ordering bug: `applyPosture` derived it before `resolveTrust`, so later declarative or remembered trust could set `TrustProject=true` while leaving project ingestion false.

## Decision

### One effective positive trust decision

`Config.TrustProject` is the ONE effective positive workspace-trust decision after composition folds all legitimate trust sources:

- explicit `--trust-project`;
- an operator-authored `trustedWorkspaces:` declaration;
- an undrifted remembered `trust.yaml` entry; or
- the interactive posture floor.

Every successful trust source intentionally admits BOTH the repository's project steering and the read-only subagent/member worktree shell. `ProjectIngestionGranted` and `SubagentShellGranted` do not exist.

### Root-aware posture ladder

`Config.Headless` is explicit deployment identity, not inferred from `Interactive`:

- mecated maps `--headless`;
- mecatequi and mecak8s default headless true;
- mecatui is false.

`applyPosture` remains root-aware:

- on an INTERACTIVE root, `trusted`/`auto`/`yolo` may raise `TrustProject`, preserving the existing developer experience;
- on a HEADLESS root, posture NEVER raises `TrustProject`;
- explicit, declarative, and remembered trust may trust either root.

This is not the original bug. The original defect was the unconditional posture raise. The root-aware ladder removes that grant on headless roots; no second synchronized boolean is needed.

### Named ingestion seam

`internal/app/project_ingestion.go` (`projectIngestionAdmitted`) remains the SINGLE semantic seam every project-tier ingestion consumer uses, but returns `cfg.TrustProject`. Keeping the named seam provides one audit point and a future split point without creating a presently meaningless capability.

The consumers include AGENTS.md/CLAUDE.md, project permission ALLOWs and model bindings, `.claude/rules`, agent definitions and project agent memory, skills, project soul, slash commands, and the git snapshot.

The read-only shell consumers (`buildSandboxedCommandRunner`, `subagentShellUntrustedReason`, `applyUntrustedMemberShellNote`, `buildWorktreeLister`, and `bashScopeMissReason`) read the same final `cfg.TrustProject` decision.

### Resulting behavior

| deployment | posture | other trust source | project steering | read-only child shell |
|---|---|---|---|---|
| headless | auto/yolo | none | withheld | withheld |
| headless | any | explicit/declared/remembered | admitted | admitted |
| interactive | auto/yolo | none | admitted | admitted |
| interactive | strict | explicit/declared/remembered | admitted | admitted |
| interactive | strict | none | withheld | withheld |

The headless untrusted capability loss is deliberate: `--posture auto` still supplies unattended permission behavior, but gets neither repository steering nor a read-only worktree shell because the repository and its `.git` were not vouched. `--trust-project`, a declaration, or remembered trust opts into both.

## Git threat model and audit

The threat is not `.gitattributes` by itself. A filter command requires git configuration. Relevant attacker-controlled surfaces include repository-local `.git/config`, hooks and `core.hooksPath`, `core.fsmonitor`, external diff configuration, and tracked `.gitattributes` selecting an attacker-named `filter.<driver>.smudge` or `diff.<driver>.textconv`. `git worktree add` can execute a post-checkout hook or configured filter before the child runner exists.

The read-only workspace git paths were audited:

- `internal/app/build.go` (`buildSandboxedCommandRunner`) returns nil unless final `TrustProject` is true. The Subagent worktree forker is wired only when that runner exists.
- `internal/app/build.go` (`buildTeamWiring`) marks read-only isolation available only when the same trusted runner exists; otherwise no read-only worktree is created.
- `internal/app/build.go` (`buildWorktreeLister`) is absent unless `TrustProject` is true.
- `internal/app/build.go` (`gitSnapshot`) is gated through `projectIngestionAdmitted`, therefore final `TrustProject`.
- `internal/adapter/forker/forker.go` (`runGit`, `runGitCapture`, `gitRepoRoot`) uses `gitenv.Scrub(envscrub.Scrub(os.Environ()))`, disabling fixed-key hooks/pager/fsmonitor/external-diff vectors and removing harness secrets. The trust gate remains load-bearing because fixed-key hardening cannot neutralize arbitrary attacker-named filter/textconv drivers.
- Mutating members and Parallel branches use `forker.WithForceCopy`: fork creation is a pure filesystem copy with no git invocation. Their later git use over the copied repository is the accepted main-session-parity residual.

No secret-scrubbing or git-environment hardening is weakened.

## Rejected intermediate models

### `ProjectIngestionGranted`

Rejected as over-modeling and an order-risk. Once every valid trust source should admit both capabilities, there is no valid `TrustProject=true` / ingestion=false state. Deriving a second bool before `resolveTrust` caused declared and remembered trust to disagree with explicit trust.

### `SubagentShellGranted`

Rejected because posture alone cannot authorize git against an unvouched `.git`. It over-trusted headless auto repositories and under-trusted interactive strict runs where the operator explicitly trusted the repository.

### Negative `--no-project-trust`

Rejected by ADR 0094's fail-safe analysis. A forgotten negative flag silently admitted steering. The root-aware positive trust fold gives the desired safe default without a suppressor.

## Consequences

- Headless auto without a trust source gets neither project steering nor a read-only child shell.
- Explicit, declarative, remembered, and interactive-ladder trust are equivalent at consumers: all admit both.
- The resolved tier and the root-aware trust decision are observable through the `operator posture` startup diagnostic (`narratePosture`), which runs after `resolveTrust` and so cannot contradict the workspace-trust narration (see ADR 0202 for the reporting-surface decision).
- The change is composition-only; no trust type enters `engine/`.

## See also

- [ADR 0022 — Unattended / allow-all posture](./0022-allow-all-posture.md)
- [ADR 0023 — Workspace Trust](./0023-workspace-trust.md)
- [ADR 0202 — Diagnostic-only posture reporting](./0202-diagnostic-only-posture-reporting.md)
- `internal/app/posture.go` (`applyPosture`, `ResolveAuthoritativePosture`, `narratePosture`)
- `internal/app/project_ingestion.go` (`projectIngestionAdmitted`)
- `internal/app/trust.go` (`resolveTrust`)
