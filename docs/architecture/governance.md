# Governance: permissions, trust, hooks, and guardrails

> Part of the [Mecatl architecture guide](../architecture.md).

Governance decides whether a tool call runs, which project files the harness believes,
what a command sees in its environment, and how untrusted text reaches a model. The
policy lives in `engine/governance`, a standard-library-only, session-free domain leaf;
`internal/app` composes the rules, posture, and trust for a process. For details, see
[permissions](../../user-docs/building/what-you-get/permissions.md) and
[hooks](../../user-docs/building/what-you-get/hooks.md).

## Permission resolution

`governance.Evaluator` resolves each call to Allow, Ask, or Deny.
`engine/adapter/permpolicy` wraps it and adds per-session learned allows and the
file-based rules `internal/adapter/permconfig` resolves for the workspace.

- **Deny wins absolutely**, from any scope.
- **Ask beats Allow.** Within one effect, the highest scope wins
  (`Managed > CLI > LocalProject > SharedProject > User > BuiltinDefault`).
- **No matching rule means Ask**, never a silent allow.
- **One narrow loosening:** a higher-scope Allow can override an Ask only from the
  built-in floor (`ScopeBuiltinDefault`: reads allowed, mutations asked), never a
  *configured* Ask.
- **Plan mode runs first** and denies mutating tools and non-read-only Shell before any
  rule is consulted.
- **`Copy` and `Move` fold per operand**, so an allow on one path cannot authorize the other.

A configured Ask is someone's explicit request for a human checkpoint. If posture, trust,
or a learned "allow always" could cancel it, one switch would erase deliberate
checkpoints, including managed policy. So every loosening mechanism here only adds
Allow rules or relaxes the built-in floor. Learned allows are exact-match, user-scope
rules, and `LearnableRule` refuses compound or substituted Shell lines. The
[agent loop](agent-loop.md) owns the Ask pause and resume.

## Shell checks

One Shell line can hold many programs, so `engine/governance/shell.go` decomposes it:

1. **Split.** `SplitCommands` splits on `&&`, `||`, `;`, `|`, a bare `&`, and newlines,
   honoring quotes. The worst segment wins, so a deny on `rm` blocks
   `git status && rm -rf /`.
2. **Canonicalize.** `Canonicalize` strips a closed set of transparent wrappers
   (`timeout`, `time`, `nice`, `env`, `stdbuf`, `ionice`) and keeps re-entrant launchers
   (`sudo`, `docker exec`, `npx`), because those change what runs.
3. **Floor substitutions.** `$(...)`, backticks, `<(...)`, and `(`/`{` grouping can hide
   a command the splitter cannot see, so `HasSubstitutionOrGrouping` floors the segment
   at Ask. The floor lifts when `SubstitutionReadOnly` proves the inner and outer
   commands read-only, or when posture loosens it.

Plan mode uses the separate `ReadOnlyShell` classifier. Children add approval steps for
floored segments; see [subagents and teams](subagents-and-teams.md).

## Posture ladder

Posture (`internal/app/posture.go`) is the one operator knob for how much the harness
approves on its own. It composes rules and evaluator options; it never bypasses the
evaluator.

| Posture | Allow-all rule | Substitution floor loosened | Raises project trust |
| --- | --- | --- | --- |
| `strict` (default) | No | No | No |
| `trusted` | No | No | Unless headless |
| `auto` | Main and children | Main engine | Unless headless |
| `yolo` | Main and children | Main and children | Unless headless |

`trusted` changes no permission rule; its only effect is raising project trust. Headless
is a per-binary setting, not inferred from the environment: `mecatui` is always
interactive, `mecated` is interactive unless started with `--headless`, and `mecak8s` and
`mecatequi` default to headless.

The allow-all rule is one ordinary `ScopeCLI` Allow, pinned per audience, so it loosens
only the built-in floor: every Deny and configured Ask still applies at `yolo`. Only
`yolo` lets children auto-run substitutions, which turns off the child prompt-injection
defense. A project file's `posture:` is ignored with a warning, an unknown value fails
closed to `strict`, and `auto` or `yolo` are refused as root without a declared sandbox.

## Workspace trust

Trust decides whether a checkout's *project authority set* is admitted: its Allow rules,
soul, model bindings, and project-tier agents, commands, skills, rules, and instruction
files. It is a composition decision; `governance`, `session`, `prompt`, and `tool` know
nothing of it. `resolveTrust` (`internal/app/trust.go`) takes the first match of the
`--trust-project` flag, a declared `trustedWorkspaces:` entry, or a remembered registry
entry whose identity anchor still matches.

Trust is root-aware. Posture never raises it on a headless root, so a `mecak8s`
deployment, a `mecatequi` CI run, or `mecated --headless` never trusts a checkout from
posture alone, and a session-selected alternate root never inherits the launch root's
trust. Untrusted means "ask the human", not "do nothing": project Allow rules are dropped while
project Deny and Ask rules still apply, user-tier and explicit operator configuration
stay active, and read-only subagents lose their shell. Trust only grants; it never
overrides a Deny or a configured Ask.

Settings and state are split: Mecatl only reads the human-authored `settings.yaml`, and
writes the separate `trust.yaml` only after the `mecatui` first-encounter prompt
(`mecated` never writes or prompts). Entries are keyed by real path and hash the project
soul and project-tier agent, command, and skill files, but not `settings.yaml`:
re-prompting on every permission edit would train operators to approve blindly. Drift,
or a missing or corrupt file, fails safe to untrusted.

## Command-runner environment

Every command runner starts from the process environment minus secrets
(`internal/adapter/envscrub`): all `MECATL_*` names, the credentials the harness reads,
and secret-shaped names such as `*_API_KEY`, `*_TOKEN`, and `AWS_*`. It is a denylist so
the toolchain environment survives. An operator can restore named CLI credentials with the user-global
`command_runner.environment.inherit` list; a project-tier block is ignored. Grants name
variables, never values. They reach the main Shell runner (including local
alternate-placement roots) and direct-write subagents, which share the parent's runner.
Hardened children (read-only and isolated subagents, team members, parallel branches),
internal Git, and microVM manager operations always get the full scrub. Reserved names
stay scrubbed even when listed: `MECATL_*`, provider and web-search keys, names operator
credential references use, and each configured MCP server's `MCP_<SERVER>_TOKEN`.

The scrub is name-based hygiene, not an OS sandbox. A granted credential is ambient
authority for every allowed main-shell command, and nothing here protects same-user files
or processes. Stronger isolation needs a separate OS identity, a container, or a VM; see
[microVM environments](microvm-environments.md).

## Untrusted content and child isolation

When a prompt mixes harness instructions with peer, tool, or model-authored text, that
text goes inside the canonical `engine/governance/fence.go` fence, byte-identical across
every prompt builder (`WriteUntrustedBlock`, `FenceUntrusted`). `NeutraliseFraming` removes forged fences and section headers
so a body cannot break out, and `NeutraliseDelegationResult` is the narrower form for
child text folded into a parent. Child isolation complements the fence: a subagent's
conversation and intermediate events never enter the parent's context; only its
neutralised result returns.

## Lifecycle hooks

Hooks are operator-deployed, so the loop trusts them more than the model
(`engine/governance/hookevent.go`).

| Phase | Can block | Can rewrite |
| --- | --- | --- |
| `SessionStart`, `UserPromptSubmit` | Yes; a hook error also blocks | The prompt (`UserPromptSubmit`) |
| `PreToolUse` | Yes, or refine the block into an approval ask | The call's arguments |
| `PostToolUse` | Annotates only | The result |
| `Stop`, `SubagentStop`, `TeammateIdle` | No | No |
| `TaskCreated`, `TaskCompleted` | Only on an explicit block | No |

The run-level gates fail safe: a block or hook error ends the run before any model call,
and `Stop` still fires, even after cancellation. `engine/agent/dispatch.go` orders a tool
call as permission, `PreToolUse`, re-authorization of a rewritten call, action review,
execution, `PostToolUse`, then inbound review. A denied call never reaches the hook.
Without a human approver, a `PreToolUse` approval ask becomes a block.

`PostToolUse` cannot undo execution, so a block there only annotates. To enforce a check
on incoming results, a hook rewrites the effective result. `session.RepairToolResult`
then repairs its text to valid UTF-8 at one choke point, so the recorded result, the
streamed event, and the model's history carry the same bytes. Binary payloads and
secret-shaped values stay byte-exact; a malformed mutation is ignored with a notice.

## Model-backed guardrails

Guardrails send configured `(phase, tool)` matches to a checker model
(`engine/agent/actionreview.go`, `inboundreview.go`; composed in `internal/app/guardrails.go`):

- **Action review** judges the exact effective call after hooks and re-authorization.
- **Inbound review** judges a produced result before it is recorded, streamed, or seen
  by the working model.
- **Permission review** can authorize one floored child Shell substitution.

The fixed rubric (`internal/app/contextual_reviewer.go`) needs affirmative evidence of
an attempt to cross or redirect authority; imperatives, admitted project instructions,
and quoted attacks are not findings by themselves. The reviewer runs file-less with no
ordinary tools: it can only read bounded review-local evidence, as data, and submit an
assessment. It fires no hooks and cannot trigger another review.

Rules are `block` or `advisory`, and the checker never rewrites anything. An enforcing
action finding asks the human to run once, approve an exact repeatable action when every
dependency is version-bound, or cancel. An enforcing inbound finding holds the result
privately until the human releases it, without rerunning the tool. Headless, the action
is denied and the result withheld. If the checker errors or times out, `onCheckerDown`
applies: fail closed by default, or warn, with per-rule overrides. Guardrails are
operator-tier only; a project cannot add, weaken, or disable them.

Checker model usage is charged to the reviewed session, including a worker session,
under the `guardrail` usage kind. Usage that arrives after the owning run has ended
is dropped, not recorded.

## Related

- [The agent loop](agent-loop.md)
- [Subagents and teams](subagents-and-teams.md)
- [Deployment and hardening](deployment-and-hardening.md)
- [Permissions and posture](../../user-docs/features/permissions-and-posture.md)
- [Hooks](../../user-docs/building/what-you-get/hooks.md)
