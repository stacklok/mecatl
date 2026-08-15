# ADR 0202 — Diagnostic-only posture reporting

- Status: Accepted
- Date: 2026-08-05
- Scope: how operators observe the resolved operator posture tier and the root-aware workspace-trust decision
- Related: [ADR 0095](./0095-root-aware-project-trust.md) (the trust decision this ADR only *reports*; this ADR is orthogonal and does NOT supersede 0095)

## Context

ADR 0095 converged the workspace-trust decision on ONE root-aware `Config.TrustProject`
fold and made the read-only subagent/member shell gate read the same decision. It also
recorded that `mecated --print-posture` reports the root-aware, fully folded trust
decision. The reporting surface was implemented as a second, parallel composition
projection (`internal/app/posture.go` `PostureProjection` / `ResolvePostureProjection`)
that re-ran `foldOperatorPosture` → `resolvePosture` → `applyPosture` → `resolveTrust`
outside `Build`, and a `cmd/mecated` flag that printed a hand-rolled `renderPostureReport`
and exited.

That second projection drifted from `Build` by construction: it duplicated the ordered
trust fold in a non-`Build` path, so a future change to the real fold could diverge from
the print path silently. The flag also introduced a print-and-exit CLI surface distinct
from the structured `operator posture` startup diagnostic `Build` already emits (via
`narratePosture`, after `resolveTrust`) — the diagnostic that carries the SAME final
root-aware `trust_project`/`project_ingestion` fields with the SAME provenance as the
running server.

The user considered `--print-posture` a hack: a parallel observation surface that
duplicated composition and could lie about the live server.

## Decision

There is no special print-and-exit CLI for posture. The authoritative observation/debug
surface is the structured `operator posture` startup diagnostic `app.Build` emits
(`narratePosture`), which runs once after `resolveTrust` and carries the resolved tier
plus the fully folded, root-aware `trust_project`/`project_ingestion` decision. No new
log level, debug flag, endpoint, or replacement CLI is introduced.

Concretely:

- `cmd/mecated` removes `config.printPosture`, the `--print-posture` registration and
  help metadata, the print-and-exit branch, and `renderPostureReport`.
- `cmd/mecated`'s `applyPostureCLI` keeps ONLY the authoritative tier resolution, the
  unknown-token warning, the root/no-sandbox fast refusal, and the tier WARN/INFO lines;
  it resolves the tier via `app.ResolveAuthoritativePosture`.
- `internal/app` removes `PostureProjection` and `ResolvePostureProjection` (no
  production caller remains). `ResolveAuthoritativePosture` stays for mecatui (embedded
  server posture) and the mecated fast-path. `narratePosture` stays the single
  authoritative structured startup diagnostic after `resolveTrust`; its level is
  unchanged and no debug logging controls are added.

The root-aware trust design from ADR 0095 is carried forward unchanged. Only the
reporting decision changes: one structured diagnostic, no duplicate projection, no
print-and-exit flag.

## Consequences

- The resolved posture tier and the final root-aware `trust_project`/`project_ingestion`
  fields are observable only through the `operator posture` startup diagnostic, which
  has the SAME provenance as the running server (it is emitted inside `Build` after the
  real trust fold). It cannot drift from the live decision the way a parallel projection
  can.
- `TestBuildOperatorYAMLPostureSeam` already pins the diagnostic's root-aware
  `trust_project`/`project_ingestion` fields across headless-withheld and
  interactive-granted cells, so the removed `--print-posture` surface leaves no coverage
  gap.
- Operators confirming what a flag/env/settings combination resolves to read the
  startup diagnostic (e.g. `mecated serve` logs it once before serving). There is no
  standalone "dry-run" print; the server is the source of truth.
- A future reporting need is a new ADR, not a re-add of the flag.

## See also

- [ADR 0022 — Allow-all posture](./0022-allow-all-posture.md)
- [ADR 0023 — Workspace Trust](./0023-workspace-trust.md)
- [ADR 0095 — Root-aware project trust](./0095-root-aware-project-trust.md) (the trust decision this ADR reports; not superseded)
- `internal/app/posture.go` (`ResolveAuthoritativePosture`, `narratePosture`)
- `internal/app/build.go` (`narratePosture` call site, after `resolveTrust`)
