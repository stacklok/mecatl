# ADR 0317 — Canonical Shell command tool

- Status: Accepted
- Date: 2026-09-07
- Scope: command-tool identity, legacy Bash compatibility, portable-POSIX command feedback, and exported engine naming
- Supersedes: ADR 0201 decision D1 only (the command-tool identity)
- Superseded by: ADR 0324 decision paragraph on parser placement only

## Context

The command-execution tool is named `Bash`, but the default runner invokes `/bin/sh -c`
and composition permits another configured shell. The mismatch causes models and operators to
write Bash-specific commands against a tool that does not promise a Bash interpreter.

`Bash` is also more than a label: the command-specific permission evaluator, plan-mode gate,
learned rules, guardrails, hooks, background status tool, configuration, and persisted pending
calls carry that identifier. A second catalog tool would bypass the command-specific evaluator;
a superficial rename could silently weaken existing deny rules or strand an awaiting session.

The project has not made a public release. Retaining Bash as the canonical model and exported
engine vocabulary would make the correction more expensive later. At the same time, existing
operator configuration and stored session state must retain safe behavior across the rename.

## Decision

Make `Shell` the sole canonical model-facing command-tool name. Make `ShellStatus` the canonical
background-command status tool and `ShellSystemTemp` the synthetic system-temporary-scope
permission capability. Move all command-specific policy, guardrail, hook, and catalog behavior
with that canonical identity atomically.

Remove Bash-named exported engine symbols and expose canonical Shell-named replacements before
public release. This is a breaking engine API change and follows the engine compatibility process.
Keep `ServerCapabilities.bash` and Go `Capabilities.Bash` as compatibility-stable fields whose
documented meaning is shell-command availability; their wire/API rename is out of scope.

Accept `Bash` only in legacy operator configuration (including permission and guardrail matchers),
tool-list configuration, and the `--no-bash` flag. Normalize those legacy inputs before policy so
old deny rules stay effective. Do not normalize a new or restored agent `Bash` tool call: only
`Shell` is registered, so the legacy name is rejected as an unknown tool. An upgrade during a
human-in-the-loop approval of an existing Bash call may therefore finish with that ordinary
unknown-tool error; no migration is required for this one-time switch-over. New calls and emitted
current events use `Shell`; completed historical records retain their original spelling.
`--no-shell` is the documented flag and legacy `--no-bash` remains an undocumented equivalent.

Add `mvdan.cc/sh/v3` to the engine module for bounded portable-POSIX compatibility feedback on
final, post-PreToolUse model-facing commands only when the trusted configured shell path has
basename `sh` or `dash`; do not resolve symlinks. A configured `bash` path, or any other basename,
does not enable the diagnostic and permits Bash syntax. Keep the parser in the session-free
governance leaf as a private syntax-classification helper; amend its strict depguard allowlist and
architecture import rule for this exact dependency, while retaining the standalone-engine closure
proof. When enabled, parse in Bash mode and reject only `TestClause` (`[[ … ]]`), `ProcSubst`
(input/output process substitution), `ArrayExpr`, and dollar-prefixed `SglQuoted` (`$'…'`) AST
nodes without spawning a process. Do not diagnose `set -o pipefail`; parsed forms outside that
bounded set and parse failures pass the original command bytes unchanged to the configured shell.
Prove the behavior with offline fixtures that cover syntax-looking data in quotes, comments,
escapes, and heredocs. Preserve the existing raw-text command-policy helpers and their fuzzed
fail-safe semantics; parser output is never canonicalized back into a permission match. This parser
feedback is neither authorization nor a sandbox and does not apply to trusted hooks or internal
git operations.

## Consequences

Models and current operator documentation receive a precise Shell contract with a bounded
portable-POSIX diagnostic. Existing operator configuration retains its command-specific protections
while users migrate naturally to Shell spelling. A legacy call already awaiting human approval may
be rejected after an upgrade, an accepted one-time switch-over cost. The engine API intentionally
breaks before its public release, and the standalone engine gains a maintained BSD-3-Clause
dependency.

Implementation must enumerate every literal `Bash` behavior dependency and distinguish legacy
input normalization from historical display preservation. It must update engine API snapshots and
changelog, current docs and user docs, but must not rewrite frozen historical ADRs. The parser
must never be presented as a security control; permission, guardrails, trust, and environment
scrubbing remain independently required.

## See also

- [ADR 0201 — Background Bash commands](./0201-background-bash.md)
- [ADR 0036 — Engine module](./0036-engine-module.md)
- [ADR 0037 — Engine stability contract](./0037-engine-stability-contract.md)
- [ADR 0306 — Human-reviewed development contracts](./0306-human-reviewed-development-contracts.md)
- [Command execution architecture](../architecture/ports.md)
- [Canonical Shell command tool acceptance plan](../acceptance/canonical-shell-command-tool.md)
