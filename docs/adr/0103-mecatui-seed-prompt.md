# ADR 0103 — mecatui seed prompt (`-p`/`--prompt`, `--prompt-file`)

- Status: Accepted
- Date: 2026-08-12
- Scope: `cmd/mecatui` (`config.go` flag registration + parse-time file read, `helpmeta.go` applicability, `main.go` `ui.Deps.InitialPrompt`), `cmd/mecatui/ui` (`model.go` `Deps.InitialPrompt`/`Model.pendingInitialPrompt`, `update.go` `applySessionReady`), `internal/cliconfig` (`prompt.go` `JoinPromptBody`, shared with `cmd/mecatequi`)
- Supersedes: none
- Superseded by: none

## Context

`mecatui` had no way to launch into a task. The operator started the TUI, waited for the
session to bind, then typed the prompt. For a repo-scoped chore ("summarise the failing
tests here") that is three steps where one would do, and it makes `mecatui` unusable from a
shell alias, a git alias, or an editor keybinding that already knows the task.

`claude -p` is the obvious reference UX, and it is a **one-shot**: print the answer, exit.
That shape does not fit `mecatui`. `mecatui` is an interactive alt-screen client whose whole
value is the conversation after the first turn — the approval modal, `/models`, the
transcript. A print-and-exit mode would need a second render path (no alt screen, no
overlays, no approval UI, structured stdout), which is a different program. `mecatequi`
already **is** that program: it has `--prompt`/`--prompt-file`, runs headless, and emits a
patch plus a JSON summary (ADR 0028). Duplicating its job inside the TUI would give two
answers to "how do I run one prompt non-interactively".

So the question was not "should mecatui get `-p`" but "what does `-p` mean in a client whose
point is that it stays open". The remaining forces:

- The submit path is not trivial. `submitPrompt` owns the bare-slash-command intercept,
  large-paste placeholder expansion, `@`-mention media/text expansion, the per-prompt media
  caps, and three loud-reject early returns. A seed that reimplemented any of that would
  drift from typed input.
- A session can bind more than once per process. `/models` restarts onto a new session, and
  a `connect`-time create whose saved model the server rejects binds via a zero-selection
  retry (the connect-fallback arm, issue #41). "On session ready" is therefore not "once".
- `mecatequi` already had a `joinPromptBody` for the literal-plus-file join, and it had no
  test.

## Decision

**The seed is a first turn, not a mode.** `-p`/`--prompt` and `--prompt-file` supply text
that is auto-submitted as the first turn once the session is ready; the TUI then stays
interactive for follow-ups. Print-and-exit is out of scope for `mecatui` — that need is
served by `mecatequi`, and the flag help, `docs/tui.md`, and `user-docs/` all say so
explicitly rather than leaving the operator to infer it from `claude -p`.

**The seed rides the identical typed-prompt path.** `applySessionReady` — the single seam
both bind arms funnel through — sets the textarea value and calls `submitPrompt`. No
parallel submit path exists, so the intercept, expansions, caps, and loud-rejects apply to a
seed exactly as to typed input. Two consequences are accepted deliberately rather than
special-cased:

- A `/`-prefixed seed (`-p /clear`) is intercepted by the built-in slash handler and never
  reaches the model. Special-casing it would mean the seed no longer takes the typed path,
  which is the property the whole design rests on.
- A `--prompt-file` body carries full typed-prompt authority, including `@path` expansion.
  This is the operator's own authority; see Consequences for the one case where that
  assumption weakens.

**The seed fires exactly once, enforced structurally.** `Model.pendingInitialPrompt` is
seeded from `Deps.InitialPrompt` at construction and consumed at one site, which clears the
field *before* calling `submitPrompt`. Both re-bind paths — a `/models` restart and the
connect-fallback rebind — re-enter `applySessionReady` with the field already empty. (`/clear`
is not one of them: it resets the conversation on the *same* session via `resetSession` and
never reaches this seam.) `ui.New` is called once per process, so there is no path that
re-seeds it. A whitespace-only seed is a no-op via a
`TrimSpace` gate, so `-p ""` and `-p "  "` behave alike.

**`--prompt-file` is read at flag-parse time and fails fast.** An unreadable path is a
startup error naming the path, not a surprise at first bind. Its body joins *after* the
`--prompt` literal, separated by a blank line, so a short directive can front a longer
brief.

**The join is shared, not mirrored.** `cliconfig.JoinPromptBody` is the single
implementation for both prompt-bearing mains — `mecatequi`'s one-shot (which then layers its
trusted-instructions / untrusted-fence wrapping on top) and `mecatui`'s seed. The two mains
must agree byte-for-byte on the join; `internal/cliconfig` exists for exactly this class
("the small slices of CLI/composition wiring that the four command mains would otherwise
copy-paste — extracted here so they cannot drift apart"), and the one table test now covers
both mains where previously `mecatequi`'s copy was untested.

**`-p` is a short-flag alias for `--prompt`.** It is `mecatui`'s first short flag. The
CLI-grammar rule in ADR 0089 ("one canonical spelling per action, no aliases/shims") governs
*command and transport* spellings — the deleted `local` subcommand, the deleted `--server`
flag, the deleted loopback probe — not flag short forms; `--inline` has been an alias for
`--no-alt-screen` since before this change. `-p` earns the exception because it is the flag
an operator types most often and interactively, and because the reference UX it is adapted
from is spelled `-p`.

Both flags are shared **session** flags, applicable in the bare (embedded) mode and under
`mecatui connect`. `mecated` is unchanged: it is a daemon, and prompts reach it over the
wire.

## Consequences

Easier: launching `mecatui` from an alias, an editor binding, or a script that already knows
the task. Because the seed is a first turn rather than a mode, everything downstream — the
approval modal, queueing, `/models`, the transcript — works on it with no new code and no
new render path.

Harder / accepted costs:

- **`applySessionReady` now does more than bind.** It may open a stream and start a run. Its
  one wrapping caller (the connect-fallback arm) keeps mutating the returned model after the
  call, so on a seeded fallback the arm's loud rejected-model warning overwrites the run
  status. Nothing reads the fields whose clearing now happens after the run opens, so this
  is cosmetic today — but the function's contract is wider than its name, and a future edit
  to either side needs to know about the other.
- **`-p` and `--prompt` share one destination**, so passing both silently keeps the last.
  Consistent with the existing `--inline`/`--no-alt-screen` pair, but that pair is boolean
  and this one loses information.
- **`parseTransportFlags` now performs I/O.** Parsing a valid `--prompt-file` requires the
  file to exist, so a future filesystem-free flag-validation path would need the read moved
  to a sibling validate step. `--help` short-circuits before the read, so help is unaffected.
- **The file read is unbounded** (`os.ReadFile`, no size cap), matching `mecatequi`. The
  blast radius is the operator's own token spend on their own local binary.
- **A `--prompt-file` whose content the operator did not author** — a checked-in task file, a
  shared runbook, a CI invocation — inherits full typed-prompt authority, including `@path`
  expansion that inlines workspace files. This is the same authority typing carries, in a
  client where the operator watches the first turn render, so no control was added; it is
  recorded here because it is the one case where "the flag value is principal-supplied"
  weakens.

## See also

- [ADR 0028](./0028-mecatequi.md) — the headless one-shot this deliberately does *not*
  duplicate.
- [ADR 0089](./0089-cli-clean-break-grammar.md) — the CLI grammar the `-p` short form is
  reconciled against.
- [`docs/tui.md`](../tui.md) — the operator-facing flag reference and the "Seeding an initial
  prompt" section.
- [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) — the living
  per-subsystem mechanics.
