# ADR 0012 — Conversation compaction

- Status: Accepted
- Date: 2026
- Scope: `engine/agent` (compaction trigger, HeuristicCompactor, CascadeCompactor), `engine/session` (ReplaceHistory, ValidateToolPairing), `internal/app` (buildCompactor wiring).
- Superseded by: [ADR 0043](./0043-ephemeral-turn0-instruction-fragments.md) (turn-0 fragment persistence → ephemeral; the compaction pin's injected-fragment skip is now defense-in-depth, and the "once-per-session injection" related-fix note below is replaced by per-run ephemeral prepend).

## Context

A coding agent's conversation grows without bound: every tool call appends large, low-signal results. A naive sliding window fails because load-bearing facts (the goal, the plan, decisions from 40 turns ago) are scattered across the whole history, not concentrated in the recent tail. A naive summarise-old-messages approach loses tool IDs, file paths, and error context. The context window must be reclaimed before the provider returns an overflow error, but the compaction must preserve what the agent actually needs to continue.

## Decision

Implement compaction as a turn-boundary check at 80% of the context window, applying a four-tier cascade cheapest-first: snip settled turns, strip large tool bodies, collapse file-read bodies to re-fetch pointers, then (with an LLM injected) produce a structured summary. Both compactors pin the first user message (the goal), back-snap the verbatim tail to the most-recent user instructions (not a role-blind last-N), and include a touched-file-paths synthesis so the agent can re-read on demand. Compaction is best-effort and never fatal: any failure keeps the original history and emits a WARN. A non-destructive EvCompactionArchive event carries the pre-compaction conversation to the durable event log so pre-compaction turns are recoverable.

## Consequences

The agent survives long coding sessions without losing its goal or the most-recent user instruction. Middle instructions (between the goal and the recent tail) are best-effort: summarised verbatim by tier 4 when an LLM is wired, dropped otherwise. The unpaired-history invariant is enforced by three independent layers (forward-snap, ValidateToolPairing, ReplaceHistory). The archive event grows the event log super-linearly across many compactions; the deferred delta-archive optimization is recorded in docs/adr/0027-cloud-native.md. Current behaviour: docs/architecture.md. Shipped/deferred state: docs/design/PRODUCTION-READINESS.md.

---

This is the deep reference for how mecatl compresses a
conversation that has grown past the context window. The terse per-subsystem status
detail lives in [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) ("Compaction never emits
unpaired history"); the cloud-native non-destructive archive is owned by
`docs/adr/0027-cloud-native.md` (Phase 3b). This doc is the rationale and the full
mechanics; those two stay the status/inventory channels and cross-link back here.

Code lives in three files: the trigger in `engine/agent/loop.go` (`maybeCompact`),
the default single-summary compactor in `engine/agent/compaction.go`
(`HeuristicCompactor`), and the tiered compactor in `engine/agent/cascade.go`
(`CascadeCompactor`). The aggregate-level guards are in `engine/session/session.go`
(`ReplaceHistory`) and `engine/session/conversation.go` (`ValidateToolPairing`).

## 1. Why compaction exists

A model's context window is finite. A coding agent's conversation is not: every
turn appends an assistant message, every tool call appends a result, and tool
results — file reads, grep dumps, test output — are large and low-signal per token.
Left alone the replayed history eventually exceeds the window and the provider
returns an overflow error mid-task, bricking the run. Compaction reclaims room by
replacing the history with a shorter, semantically-equivalent one before that
happens.

The naive fix is a **sliding window**: keep the last N turns verbatim plus the
system prompt, drop the rest. It is cheap and needs no model call, and it is exactly
wrong for a coding agent. A sliding window works for chat-style sessions where state is mostly recent, but fails for coding
agents that need to remember the test failure from 30 turns ago. The load-bearing facts of a long coding
session — the goal, the plan, the decision made 40 turns ago, the file paths touched
— are scattered across the whole history, not concentrated in the recent tail.
Dropping the middle by position throws them away. The other classic failure is
**compaction without signal preservation** — a naive `summarize-old-messages` that
loses tool IDs, file paths, and error context. mecatl's compaction is designed against both.

### The trigger

Compaction is a turn-boundary check, run by `engine/agent/loop.go` (`maybeCompact`)
once per turn from the loop's drive path. It fires when the estimated history token
count crosses a threshold:

```
threshold = ContextWindow() × CompactionRatio
```

`Deps.ContextWindow` is a `func() int` resolver for the model's window, read live at
each `maybeCompact` (a nil closure or a `<=0` return **disables** compaction entirely —
`maybeCompact` returns early). Resolve-at-use means a post-construction live-catalog
swap self-corrects the (never-rebuilt) shared engine on the next turn. `Deps.CompactionRatio` defaults to
`defaultCompactionRatio` = **0.8** (`engine/agent/loop.go`), matching the prior-art
guidance to trigger at 70–80% of the window rather than waiting for the wall, leaving
headroom for the summarisation call itself. The size estimate comes from `Deps.TokenCounter.CountMessages`
(the `engine/agent/tokencount.go` (`TokenCounter`) seam; production wires a tiktoken
counter, the offline default is `engine/agent/tokencount.go` (`HeuristicTokenCounter`)).

The model's window is resolved once in composition (`internal/app/build.go`) from the
live / catalogued / 128k fallback. This is the SAME resolution for BOTH the default
model (`baseEngineDeps`, via `reg.meta.contextWindowFor`) and a per-session selector —
the default model is **not** pinned to the 128k floor (issue #63); a catalogued
1M-context default (e.g. `gpt-5.5`) gets its real window, flooring to 128k only when the
model is genuinely unknown. The operator can **override** it with the
`--context-window-override` flag (`app.Config.ContextWindowOverride`): a positive value
replaces the resolved window, so a small value forces compaction at a tiny, cheap
threshold (0.8 × the override). `0` (the default) is **disabled** and leaves the
resolution byte-identical. The override is used by the live e2e (below) and to
stress-test compaction, and doubles as a workaround for a model that under-reports its
window or sits behind a proxy that does. See `docs/usage.md` for the flag.

When it fires, `maybeCompact` calls the injected `Compactor`, validates the result,
swaps the conversation via `engine/session/session.go` (`ReplaceHistory`), and emits
two events (§7). On any failure it keeps the original history and continues — a
degraded but never-fatal mode (§6).

## 2. The Compactor seam

Compaction is a port-shaped seam, not a hardcoded algorithm. The interface is
`engine/agent/compaction.go` (`Compactor`):

```go
Compact(ctx, conv *session.Conversation) (compacted []session.Message, summary string, err error)
```

It returns the replacement message slice, a human-readable summary of what was
dropped (surfaced on the compaction event), and an error only on genuine failure.
Two implementations satisfy it:

- **`engine/agent/compaction.go` (`HeuristicCompactor`)** — the default,
  network-free, single-summary compactor. It performs **no LLM call**, so it is
  fully deterministic and offline-testable. It keeps the system prompt and the goal,
  synthesises one summary message listing every touched file path, truncates large
  tool bodies, and preserves a back-snapped recent tail.

- **`engine/agent/cascade.go` (`CascadeCompactor`)** — the tiered, cheapest-first
  compactor. It applies up to four stages in order, stopping as soon as the history
  fits the budget. Tiers 1–3 are deterministic and offline; tier 4 is the LLM
  summary and only runs when an `engine/port` (`LLMProvider`) is injected.

### How composition wires the production compactor

`internal/app/build.go` (`buildCompactor`) selects from `cfg.Compaction`. The default
(`"heuristic"`/empty) returns the bare `HeuristicCompactor{}`. `"cascade"` returns a
`CascadeCompactor` wired with the session's token counter, the live `LLMProvider`,
the session model, and a `BudgetTokens` target of
`defaultContextWindowTokens × defaultCompactionTargetRatio` — `defaultCompactionTargetRatio`
= **0.6** (`internal/app/build.go`), deliberately **below** the 0.8 trigger ratio so a
compaction pass reclaims enough room not to re-trigger on the next turn (hysteresis).

Because composition injects a real `LLMProvider`, the production cascade's **tier 4
is live** — the "no LLM → deterministic offline" property is a test/seam property,
not the production posture. `buildCompactor` runs per session AND per child engine,
so a Subagent / team member inherits the same strategy.

### The "no LLM → deterministic offline" property

Both compactors degrade to a pure, network-free result with no LLM:

- `HeuristicCompactor` never calls a model at all.
- `CascadeCompactor` with a `nil` `LLM` **stops at tier 3** and never makes a model
  call (`engine/agent/cascade.go` (`Compact`)).

This is what makes compaction testable offline (gauntlet #12-lite) and is why the
engine's offline default in `engine/agent/loop.go` (`NewEngine`) is the heuristic
one — the engine predates composition and must work standalone.

## 3. The four-tier cascade

`CascadeCompactor` applies four tiers, cheapest first, stopping the moment the
history fits the budget. Each tier operates on the **middle** segment only — the
messages strictly between the preserved head and the preserved tail (see §4 for the
partition).

| Tier | Name | Cost | What it does |
|------|------|------|--------------|
| 1 | **snip** | free | Drop the oldest low-value turns: keep the more-recent half of the middle, drop the older half (settled work the agent already acted on). `engine/agent/cascade.go` (`snip`). |
| 2 | **strip** | free | Truncate large tool-result bodies in the middle to `cascadeStripToolBodyChars` (200) with an elision marker — the lightest-touch "tool-result clearing". `engine/agent/cascade.go` (`truncateToolBody`). |
| 3 | **collapse** | free | Replace large file-read bodies (over `cascadeMaxCollapseChars`, 1024) with a `<<tool_result_collapsed id=… size=…B; re-run the tool to retrieve it>>` pointer, dropping the body entirely (the model can re-read on demand). `engine/agent/cascade.go` (`collapseToolBody`). |
| 4 | **summarize** | high | If still over budget AND an `LLMProvider` is injected, ask the model for a compact structured summary of the oldest segment and replace it. `engine/agent/cascade.go` (`summarize`). |

This is the four-tier shape mecatl adopted:
snip → strip-tool-noise → collapse-reads-to-pointers → LLM-summary, cheapest first.

### Stop-when-it-fits

After each tier, `engine/agent/cascade.go` (`fits`) re-measures the assembled
candidate history against `BudgetTokens` and returns through `engine/agent/cascade.go`
(`finish`) the moment it fits. The budget mechanics have one deliberate quirk: a
**zero** `BudgetTokens` means "no target" — `fits` reports `false` until the last
deterministic tier, so every deterministic tier runs **once** (the offline default,
`snip → strip → collapse`) and the cascade always terminates without an LLM. With a
positive budget (the production posture), the cascade stops at the first tier that
brings the history under it, so a session that only slightly overflows pays only for
`snip`.

Tier 4 is reached only when an `LLM` is injected AND tiers 1–3 left the history over
budget (`engine/agent/cascade.go` (`Compact`)). It replaces the whole (already
stripped/collapsed) middle with a single summary user message.

### The consts and their override knobs

| Const (`engine/agent/cascade.go`) | Value | Field override |
|-----------------------------------|-------|----------------|
| `cascadeKeepLastTurns` | 6 | `KeepLastTurns` |
| `cascadeStripToolBodyChars` | 200 | `StripToolBodyChars` |
| `cascadeMaxCollapseChars` | 1024 | `MaxCollapseChars` |
| `defaultSummaryMaxTokens` | 1024 | `SummaryMaxTokens` |

`BudgetTokens` and the `Counter`/`LLM`/`Model` fields have no const — they are
composition-supplied. The heuristic compactor's knobs are `maxToolBodyChars` (400,
override `MaxToolBodyChars`) and `keepLastTurns` (6, override `KeepLastTurns`) in
`engine/agent/compaction.go`.

## 4. The preserve/drop contract

This is the heart of the design: what survives a compaction pass and why. Both
compactors share the same contract, partitioning the history into a preserved
**head**, a compactible **middle**, and a preserved **tail**.

### What survives (verbatim)

- **The system prompt(s).** Leading `RoleSystem` messages are copied through
  verbatim (`engine/agent/compaction.go` (`Compact`); `engine/agent/cascade.go`
  (`preservedHead`)).

- **The first GENUINE user message — the goal — pinned.** `engine/agent/compaction.go`
  (`firstUser`) preserves it verbatim, and the cascade includes it in
  `preservedHead`. The pin anchors on the first **genuine** user instruction,
  skipping the harness-injected turn-0 context fragments (project instructions /
  soul / memory index / user model) that the `InstructionAssembler` chain records as
  `RoleUser` messages BEFORE the user's real prompt — mirroring the
  `isSynthesisedSummary` skip for compaction summaries. `engine/agent/compaction.go`
  (`isGenuineUserTurn`) is the shared predicate (composing `engine/prompt/turn0.go`
  (`IsInjectedTurn0Fragment`) — the assemblers' own headers are its source of truth —
  with `isSynthesisedSummary`); `firstUser`, `userSnapFloor`, and `engine/agent/cascade.go`
  (`preservedHead`) all use it, so the three pin/floor sites stay in lockstep. Without
  this skip the pin anchored on the first `RoleUser` message — which, with a
  soul/memory/user-model deployment, is an injected fragment, not the goal — so the
  genuine first instruction fell into the summarised middle and was dropped. The
  count of leading injected fragments is config-variable (0–4+), so a positional
  "first N" cannot work; the anchor must be content-identified. This is the
  deliberate divergence from harnesses that summarize away the first user message and
  expect durable rules to live in a standing-instructions file. mecatl pins it instead.
  Cline and Roo Code similarly pin the original task.

  > **Once-per-session injection (related fix).** The turn-0 context fragments are
  > injected ONCE per session lifetime, gated in `engine/agent/loop.go` (`recordPrompt`)
  > on `engine/agent/compaction.go` (`hasGenuineUserTurn`) — "no genuine user turn
  > recorded yet" — rather than on `Counters.Turns == 0`. `Reopen`/`Interrupt`/`Recover`
  > zero `Counters`, so a turn-counter gate re-fired the injection on every resumed run
  > and re-appended soul/AGENTS.md/memory/user-model into the persisted history (prompt
  > bloat that also recreated the pin ambiguity). The genuine-user-turn gate fires the
  > injection exactly once at the true session start and skips it on every resume.

- **The MOST-RECENT user instructions — the user-turn-boundary back-snap.** The
  verbatim tail is not a blind last-N slice. `engine/agent/compaction.go`
  (`snapCutToRecentUserTurn`) walks the cut **backward** so the tail begins at a
  recent user turn, pulling the most-recent user instruction(s) into the verbatim
  window. It is bounded three ways:
  - `recentUserTurnsKept` (3): snap to at most the third-most-recent user turn.
  - `maxUserSnapLookback` (`keepLastTurns × 4` = 24): a hard cap on how far back to
    reach, so a single ancient lone user turn can't drag the whole conversation into
    the tail and defeat compaction. When the bound is hit the most-recent 1–2 user
    turns still survive; older ones stay summarisable.
  - `engine/agent/compaction.go` (`userSnapFloor`) (one past the first-user index):
    the back-snap never reaches into the head, so the first-user pin and the tail
    stay disjoint and the goal is never double-emitted.
  External harnesses commonly keep recent user turns verbatim rather than
  summarising them. `engine/agent/compaction.go`
  (`isRecentUserTurn`) (an alias of `isGenuineUserTurn`) ensures the back-snap anchors
  only on **genuine** user turns, skipping BOTH the harness-injected turn-0 context
  fragments (via `engine/prompt/turn0.go` (`IsInjectedTurn0Fragment`)) AND the
  synthesised summary messages — the paths-summary AND the tier-4 LLM summary — via
  `engine/agent/compaction.go` (`isSynthesisedSummary`) (see §6, the re-compaction
  footgun).

  > **Live guard.** `e2e/compaction_test.go` drives a **real model across a real
  > compaction** (spawning its own mecated with `--context-window-override` to force a
  > tiny window) and asserts a distinctive task issued *after* the first-user pin still
  > gets executed once compaction has replaced the head — the highest-fidelity guard for
  > this back-snap (the role-blind-tail bug that dropped the task lived here). It is
  > anti-vacuity gated: the spec fails unless an `EvCompaction` actually fired, and
  > fired *before* the task-execution turn. See `e2e/README.md`.

- **Touched file paths — a synthesised summary.** `engine/agent/compaction.go`
  (`touchedPaths`) scans every tool call's args for a `path` / `file_path` field
  (`engine/agent/compaction.go` (`extractPath`)) across the FULL history, and
  `engine/agent/compaction.go` (`buildSummary`) renders them into one synthesised
  `RoleUser` summary message. The cascade computes `touchedPaths` from the full
  history **first**, so a path survives even if every turn that touched it is later
  dropped. This lets the model re-load files on demand after compaction.

- **The verbatim recent tail.** Everything from the (back-snapped, boundary-snapped)
  cut to the end is preserved verbatim — the recent working set the model is mid-task
  on. The heuristic compactor still truncates oversized tool bodies in the tail
  (`engine/agent/compaction.go` (`truncateToolBody`)) so an old file dump doesn't
  dominate the kept window.

### What's dropped or best-effort

- **Middle user instructions — best-effort, and we are honest about it.** A user
  instruction that is neither the first nor recent enough to be back-snapped falls
  into the middle. When tier 4 runs it is summarised (the structured summariser is
  instructed to preserve every dropped user directive verbatim, §5). When tier 4 does
  NOT run — the heuristic compactor, or the cascade stopping at tier 3 — it is
  **dropped**. `buildSummary`'s wording is deliberately honest: "The original goal
  and the most-recent user instructions are preserved verbatim ... earlier or
  superseded context ... were summarised or dropped." Not every user message survives;
  the design guarantees the first and the recent, and makes the middle best-effort.

- **File bodies, verbose tool output, old stack traces.** Truncated (tier 2),
  collapsed to pointers (tier 3), or summarised (tier 4). This is the
  "drop file contents, verbose grep output, old stack traces" half of the corpus
  contract — the agent can re-read on demand.

The tier ordering reflects that tool outputs are the largest and least signal-dense
part of the history: snip drops
settled turns, then strip/collapse target tool bodies, and only tier 4 — the last
resort — touches the rest.

## 5. The tier-4 structured summariser

When tier 4 runs, `engine/agent/cascade.go` (`summarize`) asks the LLM to compress
the middle into a **section-locked** structured block. The template lives in
`engine/agent/cascade.go` (`summarizerSystemPrompt`) and mandates exactly these
sections, in order:

```
## Goal
## User instructions and intent
## Current plan
## Completed work
## Key decisions
## Relevant files and symbols
## Tool results worth remembering
## Open questions and known errors
## Next steps
```

The fixed shape (the opencode / Claude-Code compaction template) makes the summary
easy for the next turn to recover from. Three properties make it safe and complete:

- **"This is your only memory" framing.** The Rules line states the summary is the
  agent's ONLY memory of the dropped turns, so every user directive MUST be preserved
  verbatim. The `## User instructions and intent` section requires enumerating every
  user directive in order — the original request, modifications, the current ask —
  quoting each and noting where intent CHANGED (Claude Code's "All user messages" +
  "changing intent", Cline's "Task Evolution"). This **complements** the back-snap:
  the back-snap keeps the RECENT user turns out of the summary entirely; the
  summariser captures the OLDER directives that the back-snap couldn't reach.

- **DATA-not-instructions safety framing.** The prompt opens by declaring the
  conversation is DATA to be summarised, not instructions to follow — a compaction
  summary is untrusted context, never elevated instructions. Compaction summaries are
  a real attack surface: payloads can be crafted to survive a compaction pass and
  persist. The framing is the defence; the summary itself is re-injected
  as a plain `RoleUser` message and never trusted as instruction.

- **Text-only.** `engine/agent/cascade.go` (`deMediaMessages`) substitutes a compact
  text placeholder (`[image: image/png, 24KB]`) for every media part before the call,
  so image/audio bytes are NEVER shipped to the summary model (`engine/agent/cascade.go`
  (`mediaPlaceholder`)).

**Fail-open structure / fail-safe empty.** Output structure is NOT validated: a model
that misses a section still produces a usable summary, so the cascade accepts it
as-is. But an EMPTY/whitespace-only output IS an error — `summarize` returns it and
`Compact` **aborts to the original history** rather than replacing real turns with a
blank message. Any LLM call error, stream error, or empty result lands the same way
(§6).

The soft size budget (`SummaryMaxTokens`, default 1024) is expressed in the
**prompt** — `engine/agent/cascade.go` (`summarizerRequestTemplate`) — never as a
`port.LLMRequest` field. The request stays provider-neutral; the budget rides as
prompt text only, and the model may overshoot (fail-open).

## 6. Safety invariants

These have tests that fail if regressed (see [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md),
"Compaction never emits unpaired history").

### Never emit unpaired history

A history whose tail STARTS on a `RoleTool` result whose matching assistant tool call
was dropped is an **orphan**: it draws a provider HTTP 400 on replay and drives the
run to `failed`. The defence is layered:

- **`engine/agent/compaction.go` (`snapCutToTurnBoundary`) stays LAST.** Both
  compactors apply the three cut adjustments in order: count-cut → back-snap (toward
  the head) → forward-snap past leading tool results. The forward snap is applied
  **last** so the orphan guarantee always holds regardless of what the back-snap did.
  Tier 1's `snip` snaps its own drop boundary the same way (`engine/agent/cascade.go`
  (`snip`)).

- **`engine/session/conversation.go` (`ValidateToolPairing`) self-check.** Both
  compactors validate their assembled output before returning. The heuristic does it
  inline in `Compact`; the cascade funnels EVERY successful return through
  `engine/agent/cascade.go` (`finish`), which validates once. On failure the compactor
  returns `engine/agent/compaction.go` (`ErrCompactionWouldOrphan`) **alongside the
  ORIGINAL messages**, so a caller that ignores the sentinel still gets a safe slice.

- **Abort-to-original at the loop and aggregate level.** `engine/agent/loop.go`
  (`maybeCompact`) treats `ErrCompactionWouldOrphan` like any other compaction
  failure — WARN and continue uncompacted, reusing the existing compaction-fail WARN
  (no new diagnostics line, preserving the "loop emits exactly three lines"
  invariant). It also re-validates with `ValidateToolPairing` BEFORE `ReplaceHistory`
  as a last line of defence, and `engine/session/session.go` (`ReplaceHistory`)
  itself rejects an unpaired slice (the aggregate guard). Three independent layers,
  all degrade-and-continue.

### Compaction is best-effort, never fatal

Every failure path in `maybeCompact` — Compact error, orphan sentinel, validation
failure, or `ReplaceHistory` rejection — keeps the existing history and continues the
run with a single WARN on the operator channel. A run that keeps growing
uncompacted is diagnosable but never aborted by compaction.

### Byte-stable prompt prefix

The preserved head (system + goal) is carried **verbatim** — never re-fenced or
rewritten — so the prompt-cache prefix the LLM adapters rely on stays byte-stable
across a compaction. The tier-4 summary is appended as new text after the stable
prefix; it does not perturb the cached head.

### No `port.LLMRequest` widening

Compaction is a loop/composition concern, not a provider field. The trigger ratio,
the budget, and the summary token target are all `Deps`/`Config` knobs or
prompt-expressed text — never new `port.LLMRequest` fields. The tier-4 call goes out
as an ordinary provider-neutral request (`engine/agent/cascade.go` (`summarize`)
sends `System`/`Messages`/`Model` only).

### The re-compaction footgun

BOTH synthesised summaries are a `RoleUser` message (so the model reads them as
context): the heuristic / cascade **paths-summary** (prefixed `compactionSummaryMarker`
= `[conversation compacted]`, emitted by `engine/agent/compaction.go` (`buildSummary`))
and the cascade **tier-4 LLM summary** (prefixed `tier4SummaryMarker` =
`[earlier turns summarised]`, emitted by `engine/agent/cascade.go` (`summarize`)). If
the back-snap treated either as a real user turn, a SECOND compaction would anchor the
verbatim tail on the prior summary and snap the entire post-summary history into the
tail — defeating re-compaction. `engine/agent/compaction.go` (`isSynthesisedSummary`)
recognises **both** markers, and `engine/agent/compaction.go` (`isRecentUserTurn`)
skips any message it flags, so the back-snap only ever anchors on a genuine user
instruction. The tier-4 marker is therefore load-bearing beyond display — keep the
`summarize` prefix byte-for-byte in sync with the const. Mutation-guarded by
`TestCompactorReCompactionDoesNotAnchorOnPriorSummary` (heuristic, cascade, and
cascade-tier-4 variants) and `TestIsSynthesisedSummary`.

## 7. The non-destructive archive

On a successful compaction `maybeCompact` emits **two** events:

1. `EvCompaction` (`engine/session/event.go`) — the human-readable notice, carrying
   the per-tier summary string. A transient status line in mecatui.

2. `EvCompactionArchive` (`engine/session/event.go`) — the durable, non-destructive
   archive, carrying `engine/session/event.go` (`CompactionArchivePayload`) with the
   FULL pre-compaction conversation in `Replaced`. The loop captures the original
   `Messages` slice BEFORE `ReplaceHistory` mutates it (messages are immutable
   per-element, so the slice ref is safe to hold across the replace) and emits the
   archive only AFTER a successful replace — the degrade branches emit nothing.

The loop ONLY emits these; it never persists. The relay
(`internal/adapter/server/grpc.go` / `internal/adapter/server/http.go`) appends
`EvCompactionArchive` to `engine/port/eventlog.go` (`EventLog`), so a later consumer
can replay the log and recover the pre-compaction turns the session snapshot no
longer holds. Like `EvApproval` it is log-only — skipped on the client wire. The
full mechanics, the no-leak contract (the archive is the parent's OWN conversation,
gauntlet #7), and the deliberate log-growth cost are owned by
`docs/adr/0027-cloud-native.md` (Phase 3b) — see there, not duplicated here.

## 8. Prior art and what mecatl adopted

The compaction design is a synthesis of patterns from the harnesses studied in the
research corpus. The axis that matters most for a coding agent is **goal/task
survival** — does the original task and the changing user intent survive a
compaction pass.

| Harness | Goal/task survival approach |
|---------|-----------------------------|
| Claude Code | Four-tier cheapest-first cascade; LLM auto-compact summarises older history. The first user message is **summarized away** — durable rules belong in CLAUDE.md, which is re-read from disk after compaction. |
| Codex | Keeps the recent **user turns** verbatim rather than summarising them, so the live instruction survives. |
| aider | Repo-map + recent edits; relies on explicit context management more than autonomous summarisation. |
| opencode | Persisted sessions + reserved-output/safety-buffer compaction math; structured summary template. |
| Cline | Pins the original task; "Task Evolution" tracking of changing intent. |
| Roo-Code | Pins the original task (Cline lineage). |
| gemini-cli | Keeps recent user turns verbatim; "this summary is your only memory" framing. |
| goose | Summarisation-based context management over a long-running session. |

**What mecatl adopted, and why:**

- **First-user pin** (Cline / Roo-Code) — the goal is load-bearing for the whole
  session and is the one message most likely to be pushed out by a position-based
  cut. Pin it rather than trust a summary to retain it.
- **User-turn-boundary tail back-snap** (Codex / gemini-cli) — the most-recent user
  instruction is the live task; a role-blind last-N tail loses it during heavy tool
  use (§9). Snap the verbatim window back to recent user turns.
- **Structured summariser with a changing-intent section** (Claude Code "All user
  messages" + Cline "Task Evolution") — the older directives the back-snap can't
  reach are captured verbatim in `## User instructions and intent`, with explicit
  notes where intent changed.
- **Cheapest-first four-tier cascade** (Claude Code) — don't pay for an LLM call when
  dropping settled turns or truncating tool noise gets the history under budget.

## 9. The failure mode this design fixes

The concrete bug: **"I don't have the original task / goal in the compacted context"**
— the model, post-compaction, asks the user to re-send the task it was already given.

Root cause: a **role-blind count-tail**. During heavy tool use, the last N messages
by COUNT are almost all assistant/tool messages — the user's actual instruction sits
further back. A blind "keep the last `keepLastTurns` messages" cut drops the user's
task into the summarised/dropped head, and a heuristic compactor with no tier-4
summary loses it outright.

The fix is the user-turn-boundary back-snap (§4): `snapCutToRecentUserTurn` moves the
cut backward until the verbatim tail begins at a recent user turn, so the most-recent
user instruction(s) stay verbatim instead of being summarised away. The bounds
(`recentUserTurnsKept`, `maxUserSnapLookback`, `userSnapFloor`) keep the back-snap
from dragging the whole conversation into the tail.

The honest weaker guarantee: this fixes the **first** and the **most-recent** user
instructions absolutely. A **middle** instruction — superseded directives between the
goal and the recent tail — is best-effort: summarised verbatim by tier 4 when it
runs, dropped otherwise (§4, §10). The structured summariser's changing-intent
section narrows that gap when an LLM is wired, but the deterministic-only path makes
no promise about the middle, and `buildSummary` says so.

## 10. What's NOT guaranteed / future work

- **Middle-instruction survival is best-effort.** With the heuristic compactor (or
  the cascade stopping before tier 4), a superseded middle user instruction is
  dropped, not summarised. The guarantee is first + recent; the middle depends on
  tier 4. A future improvement could surface middle user directives into the
  synthesised summary even on the deterministic path.

- **Bound tuning is heuristic.** `recentUserTurnsKept` (3), `maxUserSnapLookback`
  (24), `keepLastTurns` (6), and the tier byte budgets are reasoned defaults, not
  empirically tuned per model/window. They are all override knobs; a deployment with
  a very large or very small window may want to retune.

- **The deterministic-tier-only path produces no real summary.** Without an LLM the
  cascade collapses/strips but never produces the structured `## Goal / ## Next steps`
  block — it relies entirely on the first-user pin, the back-snapped tail, and the
  touched-paths list. That is the deliberate offline/testable posture, but it is a
  weaker memory than the live tier-4 summary.

- **The archive is the full pre-compaction history, not a delta.** Across many
  compactions this re-logs the retained tail and grows the event log super-linearly.
  The deferred optimization (a delta archive) is owned by
  `docs/adr/0027-cloud-native.md` (Phase 3b), where the cost is recorded as an accepted,
  reasoned decision.


---

*Part of the [design docs](../design/README.md). Related: [Genuine tiered memory (closing the tier-0 gap)](0009-tiered-memory.md), [Cloud-native arc: disposable process, externalized state, durable record](0027-cloud-native.md).*
