# ADR 0324 — Internal Shell compatibility diagnostic

- Status: Proposed
- Date: 2026-09-09
- Scope: parser-backed Shell portability diagnostic ownership and API boundary
- Supersedes: ADR 0317 decision paragraph on parser placement only

## Context

ADR 0317 introduced bounded compatibility feedback for commands issued through the
model-facing Shell tool. It placed the parser in `engine/governance`, but the diagnostic
does not decide permission, authorization, guardrail, trust, or sandbox policy. It advises
the model that a small set of Bash constructs will not be portable when the configured
executor is named `sh` or `dash`.

The diagnostic is shared by the loop-aware Shell tool and the synchronous reference Shell
tool. Exposing it from `governance` would add a public engine API solely to share an
implementation heuristic. External consumers should receive the behavior through the
shipped Shell tools, not depend on its parser details.

## Decision

Place the parser-backed diagnostic in `engine/internal/shellcompat`. It is shared only by
engine-internal Shell tool implementations. Its parser walk, result classification, and
concrete error type remain unexported outside that internal package.

Keep the behavior from ADR 0317 unchanged: inspect only the final post-PreToolUse command
for configured shell-path basenames `sh` and `dash`, reject only the selected non-portable
Bash constructs as a normal non-executing tool error, preserve original bytes for permitted
commands, and never use the result for authorization or sandboxing.

Do not add an exported governance or tool diagnostic helper and do not widen
`tool.CommandRunner` for profile discovery. Permit `mvdan.cc/sh/v3/syntax` only in the
internal helper's dependency allowance.

## Consequences

The Shell portability advice has an ownership boundary that matches its execution-helper
role and does not create a supported public parser API. The engine module retains the
parser dependency, but callers cannot couple to the selected AST heuristic.

The internal package is a deliberate shared implementation seam for the two Shell tool
bodies. Future shell tools inside the engine may reuse it; external consumers cannot. A
future need for externally callable compatibility classification requires a separate public
API decision.

## See also

- [ADR 0317 — Canonical Shell command tool](./0317-canonical-shell-command-tool.md)
- [Canonical Shell command-tool acceptance plan](../acceptance/canonical-shell-command-tool.md)
- [Command execution architecture](../architecture/ports.md)
- [ADR 0037 — Engine stability contract](./0037-engine-stability-contract.md)
