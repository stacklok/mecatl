# ADR 0222 — mecatui: ctrl+t routes by ask type; full-screen ask-args view

- Status: Accepted
- Date: 2026-08-14
- Scope: mecatui (`cmd/mecatui/ui`) — the permission modal's rendering of long tool args
- Supersedes: —
- Superseded by: —

## Context

The permission modal rendered a non-diff ask's args as one pretty-JSON blob
(`prettyJSON`), so a long Bash command painted a single unreadable line across
the terminal (issue #488) — the operator was asked to authorize a command they
could not read. The card had no wrap, no scroll, and no way to see the whole
payload; the only reveal affordance (`ctrl+t`, ExpandTools) existed for
Edit/Write diffs and tool-result bodies, not for the args of an ordinary ask.

Two neighboring surfaces already solved adjacent problems and set the pattern
the fix had to fit: the plan-approval gate fills the conversation region with a
dedicated scrollable viewport (`planVP`) instead of growing the centered card,
and the modal's diff path expands in place under `ctrl+t`. A third delegation
family was NOT the point to extract shared machinery (the modal's args region
is a plain string slice + offset, deliberately NOT a third `viewport.Model` —
both the render and the mouse hit-test rebuild the body per frame/call from the
same source, and a viewport's set-once content fights that).

## Decision

**`ctrl+t` routes by ask type** inside the permission modal (the phase stays
`phaseAwaitingApproval`; routing is Model state, not a new phase):

- A **non-diff, non-plan ask** opens the **full-screen ask-args view** — the
  args fill the conversation region in a scrollable viewport (`argsVP`) with
  the generic verdict buttons pinned to the bottom bar; `esc`/`ctrl+t` returns
  to the modal, and the verdict keys work from inside the view.
- An **Edit/Write ask** (the diff-capable set, the same switch as
  `renderToolDiff`) keeps the in-modal diff expand, unchanged byte-for-byte —
  a malformed-Edit-args ask still classifies diff-capable.
- A **plan ask** keeps the scrollable plan-review view, untouched.

The modal's own args region **wraps at the card content width** and caps (ten
rows at writing; the constant is `permissionModalArgsMaxLines`) plus a hint
line, scrolled by arrow/pgup/pgdn keys and by the mouse wheel (only while the
cursor is over the card — the wheel elsewhere keeps scrolling the conversation
behind the modal). The command content renders in the bright `askArgs` style
with a tool-coloured left accent bar so the thing being approved reads distinct
from the muted reason/hint metadata, and the card sizes to its content up to
132 columns (`permissionModalMaxWidth`) on a wide terminal. Only long-args asks
grow the modal; a short ask's card is otherwise unchanged.

The view renders **two tiers** (operator decision, fix round): the raw tier is
the **verbatim wire args string** (`sanitizeTerminal(ask.Args)` — control bytes
neutralized, the text otherwise untouched; NO prettyJSON, no re-indent), the
escape hatch that can never lie — "exactly what am I approving". The pretty
tier is the **readable decode**: a Bash `{"command": …}` decodes into the
command text (real newlines — deliberately NOT shell-aware reformatting; no
mvdan dependency) with a muted `timeout_ms: N` annotation when the envelope
carries one (so the pretty tier loses nothing the envelope carries), falling
back to pretty JSON for every other tool/shape. A bare `r` (the rebindable
`RawArgs` key, consulted ONLY inside the view) toggles the tiers, and the
toggle/hint render only when the tiers genuinely differ (any Bash ask, or a
non-Bash ask whose pretty tier re-indents the verbatim raw) — byte-identical
tiers (empty args, a non-JSON single-line passthrough) hide the hint. A
soft-wrapped logical line is marked with a warning-coloured `↩` at the END of
each continued row (both the full-screen view and the modal's mini-viewport —
the glyph is appended after wrapping into a reserved margin, so it never
overflows the budget, and the colour signals "not a natural break"). Future
domain-specific pretty-printers enrich ONLY the pretty tier; the verbatim raw
stays the constant floor.

`RawArgs` shares the default chord `r` with the overlay-internal `Refresh` in
disjoint surfaces; the keymap validator rejects only an explicit rebind that
re-overlaps the pair.

The hand-testing affordance for these surfaces is **env-var-only**: `MECATUI_DEBUG_ASK=1`
registers a `/debug-ask` built-in that injects a fake long-args ask through the
real reducer (cycling three canned payloads). It is deliberately never a flag,
so it stays out of `--help`. Because a debug ask opens from `phaseIdle`, the
modal's close path **resumes the phase the ask interrupted** (`askResumePhase`,
recorded by the one ask-opening reducer) instead of hardcoding `phaseRunning` —
a wire ask resumes to running; a debug ask resumes to idle, never a
spinner-running phase no run owns.

## Consequences

- Reading a long command before authorizing it is the default, not a toggle.
- The render and the click hit-test share ONE body builder
  (`permissionModalBodyParts` measures the buttons row at the structural point
  it writes them), so the added region/hint rows cannot desync the click
  geometry by construction.
- `RawArgs` adds a 26th rebindable action; the RawArgs/Refresh pair is the
  first validator rule that guards two defaults that legitimately collide.
- The mini-viewport is per-ask state (`askVPOffset`) reset on every ask
  transition; the full-screen view is per-ask too — a queued successor or a
  retract closes it rather than inheriting a stale view.
- Cost: a second full-screen ask surface whose trio (open/clear/fingerprint +
  layout + render) mirrors the plan-review trio one-for-one; the two are
  deliberately NOT merged (their headers, bars, and content pipelines differ).

## See also

- [docs/tui.md](../tui.md) — the keys table + the long-args paragraph.
- The plan-review pattern this mirrors: `cmd/mecatui/ui/approval_render.go`
  (`openPlanReviewView`).
