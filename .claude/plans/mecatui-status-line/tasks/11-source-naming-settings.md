---
id: 11-source-naming-settings
title: Rename source boundary and isolate client settings
blocked_by: [10-source-correction-batch]
status: done
branch: plan-mecatui-status-line/11-source-naming-settings
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Rename the public and concrete status boundary consistently to `Source`: source files/tests, constructors, UI dependency, references, ADR/acceptance/task docs, and tests. Remove the revive suppression. Move status settings parsing/source composition out of any keymap-named file into a deliberately named client-settings file, retaining only keymap concerns in keymap code. Preserve behavior; do not address defaults, input population, or three-tier schema in this task.

## Acceptance criteria

Static checks prove no `StatusLineGenerator`, `StatusGenerator`, generator-named source files, or status settings in keymap-named files remain.
