---
id: 01-double-escape
title: Physical non-remappable double-Escape draft clearing
blocked_by: []
status: done
attempt: 1
branch: plan-mecatui-double-escape/01-double-escape-attempt-1
worktree: .scratch/worker-mecatui-double-escape-01-double-escape-attempt-1
issue: "605"
retries: 0
last_error: ""
accumulator: acc/mecatui-double-escape
---

# Physical non-remappable double-Escape draft clearing

Implement the focused mecatui UI behavior from issue #605 on the accumulator. Keep the gesture physical and non-remappable: two distinct Escape presses clear a non-empty idle, focused draft through the existing `clearPrompt` path. Preserve repeat filtering, generation-tagged expiry, owner-first precedence, all draft-local cleanup, live help, documentation, and only the goldens that actually change.

## Superseded acceptance criteria and proof references

This first attempt is superseded by task 02 and the final acceptance plan at
`docs/acceptance/mecatui-double-escape.md`. Its obsolete `TestADR_0025` criteria
and proof references are intentionally not retained: they do not describe the
landed ADR 0302 contract.

## Implementation boundaries

- Preserve selection-first and owner-first input routing. Every owner consumes or disarms Escape before the idle gesture; an owner dismissal cannot leave an armed destructive second press.
- Define non-empty from all draft-local content: text, staged media, large-paste content, and pending media. Use only `clearPrompt` to perform cleanup.
- Do not add or expose a keymap/config/remapping surface, production clock, engine/API/proto dependency, or broad golden refresh.
- Update live help, `docs/tui.md`, and `user-docs/mecatui/keybindings.md` consistently; inspect golden changes to prove they are limited to live help.

## Required verification

- `task lint`
- `task test`
- `task docs`
- `task site:build`
- `task ac-trace`
- `task ac-trace-strict`
- `go run ./cmd/mecademo`
