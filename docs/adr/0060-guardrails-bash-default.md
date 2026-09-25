# ADR 0060 — Add Bash to the default guardrail rule set with a read-only pre-filter

- Status: Accepted
- Date: 2026-06-26
- Scope: the built-in default guardrail rule set — `internal/app/guardrails.go` (`defaultGuardrailSpecs`) — plus a new per-rule `SkipReadOnlyBash` pre-filter AND a Bash-specific inspection rubric (`modelhook.DefaultBashPrePrompt`) in the `internal/adapter/modelhook` adapter (`matcher.go`, `modelhook.go`). No engine/port/proto/wire change.
- Supersedes: the "Local tools (Read/Edit/Write/Bash/Grep/Glob) are deliberately NOT matched" clause of [ADR 0021](./0021-guardrails.md) ONLY — as it applies to `Bash`. 0021's threat model, modes, matcher, enforcement, recursion guard, fail-open/closed, and cost guards carry over unchanged, as does the exclusion of the other local tools.
- Superseded by: [ADR 0363](./0363-contextual-investigative-guardrails.md) for contextual coverage and fixed-rubric/additive-policy behavior

## Context

[ADR 0021](./0021-guardrails.md) shipped the LLM-backed guardrails adapter and, with [ADR 0053](./0053-guardrails-default-block.md), a default-on **block** rule set covering `WebSearch`, `WebFetch`, and `mcp__*`. The local tools — including `Bash` — were deliberately left unmatched, on the reasoning that the agent-facing shell is gated by the permission layer.

A real incident exposed the gap: an agent ran `gh pr merge <n> --squash` as a `Bash` call and merged its own PR unattended. The permission layer auto-approved it (under a permissive posture), and guardrails never saw it because the default set matched only `WebSearch`/`WebFetch`/`mcp__*`. The local shell is the single largest blast radius an agent has — it can push, merge, delete, exfiltrate — and a configured security control that ignores it is a hole.

The naive fix (match `Bash` like any other tool) would inspect *every* shell command with an LLM call: an `ls`, a `grep`, a `git status`. That is an unacceptable cost and latency tax for commands that cannot do harm. The vast majority of an agent's shell calls are read-only.

## Decision

**Add `Bash` to `defaultGuardrailSpecs` (pre, block) with a read-only pre-filter.** The new default Bash rule carries a per-rule `SkipReadOnlyBash` flag, honored by the modelhook adapter ONLY for `tool=="Bash"` on the `pre` (outbound) phase:

- Before assembling any prompt or calling the checker, the adapter extracts the command from the call args (`bashCmdFromArgs`: the `command` field, then `cmd`) and classifies it (`bashFullyReadOnly`).
- A command that is **confidently read-only** — `governance.ReadOnlyBash` for the whole line, OR every `governance.SplitCommands` segment is a provably-read-only `governance.SubstitutionReadOnly` — **skips the checker entirely**: zero LLM calls, zero latency, the zero (pass-through) outcome.
- Everything else — a mutating/outward command (`gh pr merge`, `git commit`, a push, `rm`, a redirection), an unknown verb, a substitution-as-verb, or an unparseable args object — falls through to the normal checker path and is inspected.

The classification reuses the engine/governance bash classifiers **verbatim** (the same ones the permission gate uses), so the skip decision matches the gate's fail-safe direction exactly: substitution/ambiguity is inspected, never skipped. The pre-filter is a strict cost optimisation — it can only ever *skip* (pass through), never block; the decision to block stays with the checker.

The **other local tools** (`Read`/`Edit`/`Write`/`Grep`/`Glob`) remain deliberately unmatched: `Read`/`Grep`/`Glob` are local reads with no outward reach, and `Edit`/`Write` are workspace mutations that git already covers as the rollback layer — none carry the outward/destructive blast radius that motivates inspecting the shell.

An explicit operator `rules:` list still replaces the defaults entirely (the default Bash rule, and its pre-filter, are then gone). An operator who authors an explicit `Bash` rule gets `SkipReadOnlyBash=false` — their rule inspects *every* command, which is the intended behaviour for a hand-authored Bash guardrail.

### Bash-specific inspection rubric

**The default Bash rule carries its own rubric (`modelhook.DefaultBashPrePrompt`), not the generic exfiltration rubric.** The built-in `defaultPrePrompt` is written for network/MCP boundaries — it flags "sensitive local data being transmitted off the machine" and ends with a blanket "if you are uncertain, judge unsafe." Applied to *local-shell* args, a weak checker reading that rubric false-positives on ordinary dev work: a real incident blocked a legitimate local write to a sibling git repo (`cat > .../sibling-repo/...`) as "writes outside the allowed workspace, potentially exfiltrating code." That is wrong — a local write is data **staying on the machine**, never exfiltration.

So Bash gets a rubric that flags only **concrete dangerous categories** — (1) data sent off the machine to a network destination (curl/wget/scp/nc upload, piping into a network request), especially secrets; (2) fetching and executing remote code (`curl … | sh`); (3) an irreversible action on a remote you may not control (force-push, push/merge to a remote, `gh pr merge`, publishing a release, deleting a remote branch/repo); (4) a destructive hard-to-reverse local operation (recursive tree deletion, overwriting a disk device, mass recursive chmod/chown); (5) a **local-persistence** write to a credential / SSH-key / shell-startup / scheduler (cron/systemd) / git-hook target in a way that grants later off-machine access or persistent code execution (e.g. appending to an authorized_keys file, a shell rc/profile, a crontab or systemd unit, or a repository's git-hooks directory) — and **explicitly declares ORDINARY local writes (source/config/build output/notes, including to sibling repos), builds, tests, local file moves/copies, and routine origin-remote git operations SAFE.** Category (5) and the local-write carve-out **coexist**: a normal source-file write to a sibling repo stays SAFE; only the named sensitive targets are UNSAFE — closing the gap where a local privilege-persistence write (which never leaves the machine, so categories 1-4 miss it) would otherwise be waved through. The blanket "if uncertain, judge unsafe" is replaced, for Bash specifically, with **"judge SAFE unless you can identify one of the specific dangerous actions above"** — a deliberate rubric-level posture choice: the named categories still catch the genuinely dangerous cases (incl. `gh pr merge`), and the one-shot `/guardrail-allow` human override (ADR 0061) covers any residual a checker still over-flags. Web/MCP rules keep `defaultPrePrompt` — they *are* network boundaries, where the exfiltration rubric is correct.

ADR 0363 retains per-rule `Prompt` only as additive operator task-risk context beneath the fixed harness safety, authority, provenance, evidence, and structured-output rubric. It no longer replaces the rubric, and the broader contextual default set now covers local action and inbound boundaries.

## Consequences

**Easier / better:**

- A configured guardrail now protects the local-shell blast radius out of the box — the `gh pr merge` class of incident is inspected (and, in block mode, vetoed).
- The veto is independent of the permission posture: a guardrail block survives `yolo` (an allow-all permission policy), because the hook veto and the permission auto-approve are separate layers.
- The cost is bounded: an LLM call fires only on a mutating/outward command, not on the `ls`/`grep`/`git status` traffic that dominates a shell session.
- The Bash-specific rubric makes the default usable: ordinary local writes (incl. to sibling repos), builds, tests, and origin-remote git stop being false-flagged as exfiltration, so the default block rule does not become noise an operator turns off.

**Costs:**

- A mutating/outward Bash command now incurs a checker LLM call when guardrails are configured. This is the intended trade-off — it is exactly the surface worth inspecting — and read-only traffic is free.
- The pre-filter is conservative by design: a borderline command (an unknown verb, a substitution it cannot prove read-only) is inspected rather than skipped, so a small number of harmless-but-unrecognised commands pay the checker cost. Failing toward inspection is the safe direction.
- The Bash rubric trades recall for precision: its "judge SAFE unless you can identify a specific dangerous action" posture is the opposite of `defaultPrePrompt`'s "if uncertain, judge unsafe." The five named categories cover the genuinely dangerous shell actions — off-machine upload, fetch-and-execute, irreversible remote actions, destructive local ops, AND local-persistence writes to sensitive targets (category 5, added so a local privilege-persistence write that never leaves the machine is not waved through by the local-write carve-out) — and the `/guardrail-allow` override (ADR 0061) recovers any residual block; but a *novel* dangerous shape outside the five categories could pass. This is the deliberate choice for a local-shell default where the false-positive rate of the strict rubric made it unusable.
- One more place that depends on the engine/governance bash classifiers' fail-safe contract. The adapter does NOT reimplement any splitting logic — it calls `ReadOnlyBash`/`SplitCommands`/`SubstitutionReadOnly` directly — so the contract has a single source of truth.

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature whose "local tools deliberately not matched" clause this supersedes (for `Bash`).
- [ADR 0053](./0053-guardrails-default-block.md) — flipped the default rule set to block; this ADR mirrors its supersede-don't-edit precedent against 0021.
- [ADR 0061](./0061-guardrails-human-override.md) — the one-shot `/guardrail-allow` human override that recovers a residual false-positive the Bash rubric's strict posture still leaves.
- [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — the living per-subsystem guardrails reference (the "Default-on with no rules" paragraph).
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
