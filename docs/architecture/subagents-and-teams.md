# Subagents & teams

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the `Subagent` tool (child agent loops, per-call knobs: fork, read-write, background, resume, output_schema, model/agent overrides), the 4-step child permission-ask model, background subagents + `SubagentStatus`, per-child cancel, and agent teams (`Supervisor`/`TeamTool`) as a coordinating delegation family.

**Prerequisites:** [the agent loop](agent-loop.md) — the Subagent tool delegates to child loops built from the same loop.

**Follow-on:** [parallelism](parallelism.md) — the sibling delegation family (fork-join branches).

`SubagentTool` is a `tool.Tool` (catalog name `Subagent`) that delegates a focused,
self-contained task (multi-step investigation or build/test/git work) to a **child agent loop**. Its `Execute`:
1. **Workspace selection.** When a child forker is wired (`WithChildForker` — the
   composition root wires it **iff** the child catalog includes Shell) it forks the
   incoming `ws` into an **isolated git worktree** (the forker DEFAULT mode — shares
   the base repo's `.git`, so the child sees full history) and runs the child there;
   the worktree is torn down after the child drains. Without a forker the child has
   no Shell and runs against the **parent** `ws`, exactly as before. A fork **failure**
   on the wired path is a tool **error**, never a silent fallback to the shared `ws`
   (running the child's Shell in the shared base is the exact hazard isolation exists
   to prevent).
2. Builds a **fresh** child `session.New(...)` — own conversation, own (tighter)
   `Limits` (`defaultChildLimits`: 500 turns / 2000 tool calls / 5 failures, issue #50 —
   deliberately far below the main session's 2000/8000 default so a delegation fan-out
   stays bounded),
   scoped to the **run** workspace root (the worktree when forked, else the parent).
   **On `resume`** (a Subagent call carrying `resume: <agentId>`) it instead RELOADS the
   persisted child by that id and recovers its terminal state — `completed` → `Reopen()`,
   `cancelled` → `Interrupt()` (history-repair), `failed` → `Recover()` (history-repair;
   ADR 0200, issue #318 — matching the main session's `failed → Recover → idle` seam, since
   a long-running direct-write child accumulates applied mutations and discarding it costs
   more than a main session's transcript). Only a NON-terminal state — a snapshot still
   recorded `running` — is refused. It then
   re-homes the session onto the fresh fork (`session.Session.Rehome`) and prepends an honest
   resume note: the read-only **staleness** note (the conversation survives, the workspace
   does NOT) or, only when this call is `mode:"read-write"` AND the resumed snapshot's
   persisted workspace IS the real parent root, the **edits-survived** note. That second
   condition is deliberate: a previously read-only child's worktree was torn down, so
   resuming it `read-write` must not claim its edits are still in place. Resume runs on the
   **default explorer engine only** (rejected with `agent`/`model`); an in-flight guard
   rejects a concurrent run on the same id.
3. Runs the child via the injected
   `childEngine.Run(ctx, child, runWS, RunRequest{Text: prompt, ...})`. The
   request also carries every per-run override — notably the tighten-only
   `MaxRunTokensOverride` and the structured-output `ExtraTools` overlay — so
   retries and salvage drives copy the base request and replace only `Text`
   rather than dropping run-scoped controls.
4. **Drains the child's entire Event stream inside `Execute`**
   (`drainChildObserved` — the single redaction chokepoint all three delegation
   families share), relaying only the REDACTED, bounded-preview
   `subagent.start/tool/end` projection (ADR 0079; [the domain model](domain-model.md)) and **returning only the final
   summary string** as one `ToolResult` (gauntlet #7) — no child transcript
   ever enters the parent conversation.

**Per-call knobs (`subagentArgs`).** Beyond `prompt`/`description`/`agent`, a Subagent call may
supply: `max_turns`/`max_tool_calls`/`max_run_tokens` (TIGHTEN-ONLY caps — the model can
make its child stricter than the operator's bound, never looser; `max_run_tokens` is the
**preferred** cumulative input+output run-budget arg, `max_tokens` the **deprecated** alias for
the same budget — supplying both with different positive values is a model-visible error;
**default: inherited/unlimited**); `timeout_ms` (a
wall-clock deadline → a time-budget tool error); `model` (pin THIS child to a specific
provider model — minted via the composition-supplied `WithSubagentEngineFactory` closure
through the contamination-safe `newChildEngineForProvider` path, NEVER a clone-and-swap;
mutually exclusive with `agent`); and `output_schema` (a model-authored JSON schema —
the child is given a synthetic `SubmitResult` tool whose params ARE the schema, must
call it to deliver, and the submitted payload is validated by `session.ValidateJSON`
with a bounded correction-retry, NO `tool_choice` forcing). The Subagent RESULT is labelled
by terminal reason (success / `[subagent stopped: …]` note / structured-output
validation error / error) and carries an `agentId: <childID>` trailer on every terminal
(model-visible, mirroring the Team-id line) so the parent can discover the child id and
read its persisted transcript via the read-only `InspectSubagent` tool (the id is used
verbatim), or pass it as `resume` to CONTINUE that subagent with a follow-up prompt
(default engine only, fresh fork + a resume note — the read-only staleness note, or the
edits-survived note when the resumed child's OWN earlier run wrote to the real tree and
this call is `read-write` again). EVERY terminal is resumable, `failed`
included (ADR 0200): a failed child recovers through `session.Session.Recover`, and its
error result carries a store-gated resume hint so the model can discover the path — the
hint states what actually carries over (conversation yes, workspace no). A direct-write
child's failure/timeout instead carries ONE combined resume-or-discard decision that names
`mode:"read-write"` explicitly, because `writable` is taken from the CURRENT call only: a
bare `resume` returns the read-only explorer, which can neither see nor finish the partial
edits sitting in the operator's tree.
`fork: true` (issue #34) seeds the child from a DEEP COPY of the parent conversation
(via `session.ForkSnapshot` — trailing fork-call orphan stripped — and the idle-only
`session.Session.SeedHistory`) instead of an empty context, TRUST-NEUTRAL (carried
VERBATIM, no re-fence — the child inherits the parent's EXACT raw message posture, the
main loop records tool results unfenced anyway, and the read-only explorer sandbox adds
no new untrusted ingress; re-fencing would also bust the byte-stable prompt-cache
prefix the feature relies on) and SAME-PROVIDER only (mutually exclusive with
`model`/`agent`/`resume`; a forked child runs on the parent's engine).
`mode: "read-write"` (ADR 0077, superseding 0040's writable path; closed set
`{"","read-only","read-write"}`, default read-only) runs the child DIRECTLY against
the REAL parent workspace with Edit/Write — NO fork, NO copy, NO merge-back. Its
Edit/Write/Shell mutate the real tree IN PLACE, exactly as the main agent does, and
git is the rollback layer — the "delegate one task and land its edits" path
(default-wired, no flag; rejected with `background`, with explicit `agent`+`model` together
(v1 scope limit), and under the no-FS profile; `read-write`+`agent` alone runs the named
specialist WRITABLE when the deployment wires the writable-specialist factory). An unpinned,
same-provider named specialist is eligible for semantic routing: the routed engine preserves
its scoped catalog/prompt/skills, Edit/Write authority, MAIN runner, and per-definition limits.
Pinned definitions, `fork`, and `resume` bypass routing; inline MCP remains unsupported on
this per-call writable path, and an unavailable routed target falls back to the ordinary
writable specialist with truthful routing metadata (ADR 0242). The result text honestly notes the edits landed directly (review with `git
diff`/`git status`); a crashed/cancelled child can leave PARTIAL edits behind
(recoverable via git — the accepted direct-write trade-off). When
neither `agent` nor `model` pins one, a def-less child runs on the global
`--subagent-model` default (the analogue of `CLAUDE_CODE_SUBAGENT_MODEL`; a concrete
id or a `--model-alias` name, resolved same-provider; precedence `def.Model >
--subagent-model > parent model`, empty inheriting the parent's). None of
these widen `port.LLMRequest` — they are `subagentArgs`/`RunRequest`/factory concerns.

**Background, SubagentStatus & per-child cancel (`docs/adr/0015-background-subagents.md`).**
`background: true` DETACHES the child, RUN-scoped: the call returns an immediate
started-result (agentId trailer first) and a goroutine owns fork → drive → persist →
result-stash in the parent `Run`'s **child-run registry** (`childRunRegistry` — every
run registers ALL children of all three families under their child session ids). The
result body's sole channel is the read-only **`SubagentStatus`** tool (no args → this
run's roster; `agent_id` → state + the stored body, delivered exactly once; `wait_ms`
parks up to 120s) — a turn-boundary harness NOTICE (ids + stop labels only, nothing
child-authored) tells the model when a background child finishes, and ONE
background-pending nudge defers a would-be clean end so results aren't silently lost.
At run end live background children are cancelled, joined (bounded two-phase drain),
sealed (`safeEmit` makes post-seal child emits no-ops), and persisted — resumable
next run. The registry also powers **per-child cancel**: `Run.CancelChild(childID)`
(gRPC `ConverseRequest.cancel_child`, HTTP `POST /v1/sessions/{id}/cancel-child`,
mecatui's `x` key) cancels ONE subagent / parallel branch / team member without
touching the run, retracting any permission ask the child had parked
(`permission.retract`); the child persists and stays resumable. The headless
RunTeam path has its own member cancel: the `CancelTeammate(team_id, member)` unary
(HTTP `POST /v1/teams/{id}/members/cancel`, issue #29) reaches a running team's
member directly through `Supervisor.CancelMember` — no parent registry on that path.

Dedicated session debugging does not reuse `InspectSubagent`'s raw child-id input. The
`InspectSession related` view traverses validated Subagent, Parallel, Team, and scheduled
relationships from one authorized root only when both parent/origin ID and opaque persisted
incarnation match, and returns opaque incarnation-bound handles only for
retained descendants admitted by the deployment's ownership posture. When ownership is
enforced, equality is stable issuer+subject identity rather than display/grant metadata;
without enforcement, owner comparisons are omitted. `delegation` projects typed lifecycle and parent-result facts;
pruned children remain visible only as content-free tombstones, including across same-ID
recreation, and retained child
transcripts are read through revalidated scope handles. This keeps unrelated session IDs
unprobeable and makes retention gaps explicit ([ADR 0258](../adr/0258-cryptographic-session-incarnations.md)).

**Background Shell jobs ride the same registry as a NON-delegation family**
(`docs/adr/0201-background-bash.md`). A `background: true` call on the `Shell`
tool registers a `bash-cmd` entry (`bashcmd-<callID>` — a bare process, NO child
session/engine, no `subagent.*` events, no InspectSubagent/resume), returns the
job id immediately, and detaches the drive; the run-scoped cancel-at-end drain,
the turn-boundary notice, and the background-pending nudge all cover it (the
notice/nudge are family-aware: the subagent clause keeps its exact wording and a
"background command(s) …" clause naming `ShellStatus` is appended only when bash
jobs are among the finished/live). The registry is SHARED but the two status
tools project it DISJOINTLY: `SubagentStatus` filters bash-cmd entries out, the
read-only **`ShellStatus`** tool (registered iff Shell is, never in child
catalogs) serves ONLY bash-cmd jobs — roster (ids+state+stop only), per-job
command + retained 64 KiB output tail (live) or exactly-once collected result
(done), `wait_ms` park, `cancel` verb. A child (Subagent/explorer/team member)
gets the same background-capable `Shell` against its OWN run's registry, but
never `ShellStatus`. See [ports](ports.md) for the tool/streaming seam.

The child is a **read-only explorer with a shell** by default — capability flows down
from the parent (which has Shell); isolation, not catalog read-only-ness, is the
security boundary:
- The composition layer wires `childEngine` with **Read/Grep/Glob plus Shell**
  (`buildChildEngine` registers Shell via the **sandboxed** runner —
  `buildSandboxedCommandRunner`, the SAME hardening team members get, since the
  worktree shares the parent `.git`), **never `Subagent`/`Parallel`/`ToolSearch`** (no
  recursion / fan-out) and **never Edit/Write** (the read-only explorer inspects, it
  does not edit the project). A `mode:"read-write"` call instead runs the SEPARATE
  `writableChildEngine` (`buildWritableSubagentChildEngine`: the explorer surface +
  **Edit/Write**, over the REAL parent workspace + the MAIN session's command runner
  `buildCommandRunner` — main-session parity, NO fork — ADR 0077); it is NOT isolated
  (`isolated:false`, so the A2 isolation auto-approve does not apply to its Shell) and
  git is the rollback. A `read-write`+`agent` call routes through
  `agentWritableFactory` (`buildAgentWritableEngineFactory`) on the definition's resolved
  model, or—when the definition is unpinned and same-provider—through
  `agentWritableModelFactory` (the routed half of `buildAgentWritableEngineFactories`) on the semantic
  router's pick. Both rebuild the specialist with `allowMutating=true` over the MAIN runner,
  preserving its prompt/skills/catalog and per-def limits (ADR 0058/0239). A routed factory
  decline falls back to the ordinary writable specialist and reports the unavailable target
  rather than claiming the routed model ran. Per-def Subagent engines keep Shell via `scopedToolNamesMode`'s
  `allowShell` and share the one read-only `SubagentTool` forker. With no runner
  (`--no-shell`) the child is a Shell-less read-only explorer and no forker is wired — the
  original behaviour. The policy is **allow-all** so the child never prompts a human
  (`internal/app`: `buildSubagentTool` / `buildChildEngine` /
  `buildWritableSubagentChildEngine` / `buildAgentSubagentEngines`).
- `SubagentTool.ReadOnly()` stays **`true`**, letting the parent run read-only `Subagent`
  calls concurrently with other read-only tools. This is safe because a read-only
  child's (mutating-classified) Shell writes land in the **isolated worktree**, never the
  shared base the parent's other read-only calls race over; the only shared surface is
  the `.git` object DB/refs (git-locked; config-driven code-exec vectors neutralised via
  `gitenv`).
  A `mode:"read-write"` call WILL mutate the parent IN PLACE during its run (direct-write,
  ADR 0077), so it declares `MutatesParent(call)==true` and the dispatcher runs it
  **alone, mutate-serial** — never batched with a sibling read it could tear.
  `MutatesParent` is decoupled from any merger (there is none); the
  `parentMutatingCaller` seam and the `SerializingMerger` are reused only by Parallel's
  single-branch merge (ADR 0040).
- `WithMaxConcurrentChildren` (default 8; `WithMaxConcurrentSubagentShells` is a
  deprecated alias) sizes the **child concurrency gate**, acquired at the top of
  `run()` for ALL children (forking and forker-less) — Subagent is read-parallel, so
  the model can fan many out; each child consumes a session + an LLM slot (and, when
  forked, a worktree). Foreground acquisition blocks; a **background** child's
  acquisition is **fail-fast** (a full gate is a model-addressable error listing the
  live background ids — a background child holds its slot across turns, so blocking
  could deadlock the model against itself).
- A child's permission ask resolves through the **4-step model** (see CLAUDE.md's
  subagent-shell gotcha): read-only-substitution and isolation auto-approve resolve
  most asks in `governance`; what remains is **surfaced to the human** when the parent
  run is interactive (the child parks; `Run.Approve` routes the verdict by the
  child-namespaced askID) or **auto-denied with an accurate model-facing message**
  when headless — never a blanket deny.
- The child run is bounded by the parent `ctx`; `SubagentStop` fires
  best-effort (on a detached short-lived context if the parent is already
  cancelled). `NewSubagentTool` panics on a nil child Engine.

This mirrors the **team-member** worktree treatment (§ below): same `gitenv`
hardening, same untrusted-`.gitattributes` residual. The workspace-trust gate
**shipped** (issue #40), shared by both: an **untrusted** workspace nils the
sandboxed runner, so read-only subagents and team members get NO shell there —
an honest Spec note tells the model, and the gate is narrated once at build.
Mutating members / Parallel branches keep their hardened force-copy shells
(force-copy forking runs no git, so the fork-time checkout hazard the gate
closes cannot fire there).

A free-text Subagent result is never a silent "(subagent produced no summary)" on
an empty terminal (issue #48/#152): a bounded two-stage recovery runs first — one
wrap-up turn, then a fallback digest of the child's last non-empty assistant
text — before the placeholder is ever shown, and the eventual result still names
the stop reason honestly.

## Prerequisites

- [The agent loop the children run](agent-loop.md)

## Follow-on reading

- [Parallelism — fork-join delegation](parallelism.md)

## Related

- [Providers — per-subagent provider routing](providers.md)

---

[← Architecture guide](../architecture.md)
