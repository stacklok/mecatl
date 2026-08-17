---
id: 06-api-and-tool-hygiene
title: Finish lifecycle tool classification and public API hygiene
blocked_by: [02-undo-semantics, 03-truncation-visibility, 04-profile-allowlist, 05-driver-wire-hygiene]
status: done
branch: "plan-memory-lifecycle-hardening/06-api-and-tool-hygiene"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/memory-lifecycle-hardening
---

# Task brief

Add all six lifecycle tools to the caller-owned-tool classification audit; describe Forget as a reversible tombstone visible through Inspect; remove the three zero-attribution validation shims in favor of the attribution-aware APIs; update all callers, engine API baseline, and breaking-change changelog note; and replace the three identified `sort.Slice` calls with Go 1.26 `slices.SortFunc`/`cmp.Compare` while retaining ordering behavior.

## Acceptance criteria

- AC7.1: `ModelToolBoundaries`/`modelToolAccessTable` include `ForgetMemory`, `ForgetUserMemory`, `InspectMemory`, `InspectUserMemory`, `UndoMemory`, and `UndoUserMemory`.
  - verify: `TestADR_0226_LifecycleToolsClassified`
- AC7.2: `forgetTool.Spec().Description` states plainly that the operation is reversible and the underlying value remains readable via Inspect.
  - verify: `TestADR_0226_ForgetToolDisclosesReversibility`
- AC7.3: `engine/tool`'s public validation surface is exactly `ValidateMemoryEntryWrite` and `ValidateMemoryContentWrite`; the three shims are removed, `engine/api/tool.txt` reflects the removal, and `engine/CHANGELOG.md` records it as Removed/breaking per `engine/COMPATIBILITY.md`.
  - verify: demonstration — `task api:check` passes against the regenerated `engine/api/tool.txt`, and the `engine/CHANGELOG.md` entry is present
- AC7.4: `memmemory.go` and `store.go`'s three `sort.Slice` call sites are `slices.SortFunc`/`cmp.Compare`, with unchanged ordering behavior.
  - verify: existing `List`/`Search` ordering tests in `engine/adapter/memmemory` and `internal/adapter/memory`, unmodified, continue to pass
