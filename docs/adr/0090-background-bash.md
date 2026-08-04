# ADR 0090 — Background Bash commands

- Status: Accepted
- Date: 2026-08-04
- Scope: the `Bash` tool (`background: true` flag + the `BashStatus` companion), the child-run registry's non-delegation `bash-cmd` family, `tool.CommandStreamer`, the osfs command runner's process-group kill, and the composition catalog wiring.
- Supersedes: —

## Context

Issue #23 asked for background task execution. The delegation half — background
**subagents** — shipped under [ADR 0015](./0015-background-subagents.md): a detached
child loop, run-scoped, drained at run end, its result collected through
`SubagentStatus`. The **commands** half stayed open: the field's commonest background
task is not a child agent but a long-running shell command — a dev server, a watch
loop, a slow build — whose output the model wants to peek at while it keeps working.
Claude Code has this (`run_in_background` on the Bash tool + Ctrl+B); mecatl's `Bash`
blocked the turn for the command's full lifetime.

The design forces:

1. **The permission gate special-cases the literal name `"Bash"`.**
   `engine/governance/evaluator.go` (`resolveBash` / `planModeDecision` /
   `LearnableRule`) matches `tool == "Bash"` by literal string to run the
   substitution/newline-aware compound-command split and the plan-mode read-only
   classifier. A *new* tool name (`BackgroundBash`, `BashBackground`, …) would fall
   out of every one of those paths — a silent permission bypass on the most
   dangerous tool in the catalog. This is the headline constraint: it dictates the
   whole shape.
2. **The background half needs the run's child registry.** Detached lifetime,
   cancel-at-end, the job-count gate, and the completion notice all live on
   `engine/agent`'s `childRunRegistry` — an agent-package type the `fstools` adapter
   (where the foreground Bash body lives) cannot import without inverting the
   dependency arrow.
3. **A detached command needs its RECENT output, not its first bytes.** The
   foreground runner captures into head-capped buffers (first 25 KiB wins). A
   background job's useful signal is the *tail* (the server's latest log lines), so
   the capture seam itself had to change shape.
4. **A detached command makes cancel-soundness load-bearing.** A foreground command
   whose cancel orphans a grandchild is a wart; a background job whose run-end drain
   (or `BashStatus` cancel) kills only the shell and orphans its children is a leak
   the feature ships at scale — every backgrounded `make` leaves compiler children
   behind.

## Decision

**D1 — `background: true` is a flag on the existing `Bash` tool, never a new tool
name.** The permission evaluator special-cases the literal `"Bash"`; the background
variant therefore registers under exactly that name so the start of a background job
resolves through the identical deny/ask/allow fold, the compound-command split, the
plan-mode gate, and the guardrail modelhook `Bash` rules as a foreground call. The
generalized rule this records: **any new shell affordance must register under the
gated name or extend the gate** — a second name for a shell-executing tool is a
permission bypass by construction. The name's single authority moved to
`engine/tool/tool.go` (`BashToolName`) so every implementation (the fstools adapter's
AND the agent loop's) imports the one constant instead of re-spelling the literal.

**D2 — the tool lives in `engine/agent` over the `childCapableTool` seam, foreground
byte-identical to fstools.** `engine/agent/bashtool.go` (`BashTool` /
`NewBashTool`) re-implements the fstools Bash body's orchestration verbatim (same arg
validation, timeout ctx, `runner.Run`, combined-output shaping, 25 000-byte cap,
exit-code error) — `engine/agent` must not import the fstools adapter, so the two
halves are kept byte-identical by discipline — and adds the background half through
`ExecuteWithParent`, the same dispatcher seam `Subagent`/`SubagentStatus` use to
reach the parent run's `parentCaps`. A `background: true` call on the caps-less
plain-`Execute` path, or a runner without the streaming capability, gets an honest
model-addressable error — never a silent foreground fallback. The composition root
registers the agent `BashTool` everywhere Bash appears (main catalogs, the read-only
explorer, per-def scoped catalogs, team members), so a **child** (Subagent /
explorer / team member) can background a command against its OWN run's registry.

**D3 — real-workspace, no isolation; Ctrl+B deferred.** A background command runs in
the session's REAL workspace root (the main session's command runner, main-session
parity) with no worktree, no copy, no sandbox beyond what the runner already has: its
effects may interleave with the model's own file changes, and the tool description
says so plainly ("the tree is not stable while it runs; prefer the foreground for
anything whose result you need before acting"). Isolation was rejected: a background
dev server must serve the REAL tree (its whole point), and the permission ask at
start is the operator's gate. The interactive **foreground→background mid-flight
promotion (Ctrl+B)** is deferred: the blocking `CommandRunner.Run` seam gives a
running foreground call no handle to detach through, and adding one rewrites the hot
dispatch path for a client affordance — v2.

**D4 — the job rides the child-run registry as a NON-delegation family.** A
background job registers on the parent run's `childRunRegistry` under the new
`childFamilyBashCmd` (`"bash-cmd"`) with the id spelling `bashcmd-<callID>`
(`engine/agent/childregistry.go`, `BashCmdJobPrefix`). It is NOT a fourth delegation
family: no child session, no engine, no `subagent.*` events, no
`InspectSubagent`/`resume` affordances — the ChildActivity extraction trip-wire is
deliberately left unfired. It rides the registry only for what the registry already
is: the run-scoped cancel-at-end drain, the background gate, and the
notice/collect/wait machinery. The two status tools project the SHARED registry
DISJOINTLY — `SubagentStatus` filters bash-cmd entries out (a bash job is never
mislabeled "a subagent"), `BashStatus` filters the three delegation families out —
via one family-filtered walk (`statusSnapshotMatching` / `collectMatching` /
`liveBackgroundIDsMatching`), so neither tool can drift its view of an entry. The
`bashcmd-` prefix names NO session: the InspectSubagent prefix gate and the
child-session retention GC must never learn it.

**D5 — permissions identical to foreground Bash; no mid-run ask.** The start of a
background job is an ordinary main-run permission decision: policy Ask ⇒ the run
pauses on the existing ask path before anything detaches. Once started, the job runs
to completion, cancellation, or the run-end drain with NO further gating — exactly
like a foreground command, which is also gated once at start. `BashStatus` is a
read-only floor-scoped Allow alongside `SubagentStatus` (config-overridable); its
`cancel` verb only *signals* the job's context (the kill is the job drive's own ctx
reaction), so the tool stays read-only and asks nothing.

**D6 — `tool.CommandStreamer` + a bounded 64 KiB tail ring.**
`engine/tool/tool.go` (`CommandStreamer`) is an OPTIONAL `CommandRunner` capability
(`RunStreaming(ctx, command, workdir, out io.Writer) (exitCode int, err error)`):
same shell/workdir/timeout/cancel rules as `Run`, but stdout+stderr stream
INTERLEAVED into a caller-owned sink the runner never caps. Discovered by type
assertion; a runner that lacks it declines and the background call fails soft (D2's
honest error). Each job streams into `engine/agent/tailbuffer.go` (`tailBuffer`), a
mutex-guarded sliding-window ring retaining the LAST 64 KiB (8 jobs ≈ 512 KiB worst
case, bounded); the retained tail backs both the live-job peek and the stored
terminal result, with a truncation marker when bytes were dropped. The osfs runner
implements the interface by sharing ONE private spawn/wait tail between `Run` and
`RunStreaming` so the two cannot drift.

**D7 — run-scoped with cancel-at-end, reusing ADR 0015's D8 rationale verbatim.** A
background job's lifetime is the RUN's: its ctx derives from the run's, and
`drainChildren` at the top of both terminate paths cancels and joins whatever is
still live (the same bounded two-phase drain, now covering the bash-cmd family).
Session-scoped detach is deferred for exactly the reasons ADR 0015 D8 records —
delivery has nowhere to go with no live run, and there is no durable outbox — plus
one of its own: a detached OS process has no re-attach story across a restart at
all. Run-scope still delivers the value: the model regains the turn immediately and
overlaps its own work with the command.

**D8 — no wire change in v1.** A background job emits NO events: the started-result,
the registry (notice / `BashStatus` / collect), and the stored result are the only
channels. The turn-boundary completion notice and the background-pending nudge are
FAMILY-AWARE — the delegation clause keeps its byte-exact historical wording (a
stable test key) and a "background command(s) finished / still running" clause naming
`BashStatus` is APPENDED only when bash jobs are among the finished/live, so a
subagent-only run renders byte-identically to before. `bashcmd.*` wire events and a
TUI fleet-pane lane are deferred to v2 (a client that wants the state polls nothing —
the model drives `BashStatus`).

**D9 — the `procgroup` extraction is a mandatory pre-fix, not a refactor.** Before
this feature the osfs runner killed only the direct child on ctx cancel; a
backgrounded grandchild (`sleep 30 &`, `make`'s compiler children) was orphaned and
held the pipes. `internal/adapter/procgroup` (extracted from `hookexec`, which
already had the code) puts the child in its own process group and kills the WHOLE
group on cancel (POSIX; no-op elsewhere). The osfs `CommandRunner` now configures it
unconditionally — fixing grandchild orphans for FOREGROUND Bash too — because D4/D7
make cancel the primary lifecycle of a detached job: a run-end drain or a
`BashStatus` cancel that leaves grandchildren running is a process leak the feature
would ship at scale.

## Consequences

- **Easier:** long-running commands stop blocking turns; the model polls/peeks/cancels
  through one read-only tool; the registry, drain, notice, and nudge machinery is
  reused with zero new lifecycle code paths; foreground Bash gains the
  process-group-kill fix; children get the same background capability against their
  own runs.
- **Harder / costs:** the Bash tool body now exists twice (fstools + `engine/agent`),
  kept byte-identical by discipline and tests — a fix to one must land in both.
  `BashStatus` is a new always-registered tool (wherever Bash is) adding catalog
  surface. A background job's real-workspace effects are honestly unstable — the
  model is told, but interleaving bugs are now possible in a way a blocking command
  never allowed. Max 8 live jobs per run (fail-fast, mirroring the Subagent gate).
- **Deferred (named v2):** foreground→background mid-flight promotion (Ctrl+B — D3);
  session-scoped detach (D7, the #28 sibling); `bashcmd.*` wire events + TUI fleet
  pane (D8); output paging beyond the retained tail.

## See also

- [ADR 0015 — Background subagents](./0015-background-subagents.md) — the registry,
  drain/seal, notice/nudge, and status-tool machinery this feature rides (D4/D7/D8).
- [ADR 0060 — Bash in the default guardrail rule set](./0060-guardrails-bash-default.md)
  — the guardrail rules a background start inherits unchanged (D5).
- Living behaviour: `docs/architecture/ports.md` (the command-execution seam),
  `docs/architecture/subagents-and-teams.md` (the registry families);
  status in `docs/design/PRODUCTION-READINESS.md`.
