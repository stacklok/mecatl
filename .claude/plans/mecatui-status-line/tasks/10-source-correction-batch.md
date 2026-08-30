---
id: 10-source-correction-batch
title: Apply reviewed status-line source corrections
blocked_by: [04-template-overrides, 09-generator-foundation]
status: done
branch: plan-mecatui-status-line/10-source-correction-batch
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Apply every item in `.scratch/status-line-feedback.md` as one correction batch, plus the agreed three-tier template schema. This task supersedes the current partial source/template wiring.

- Keep the public boundary, concrete implementation, constructors, UI dependency, tests, documentation, and files consistently source-named as `Source` and `source.go`; remove the revive suppression.
- Deliberately split/rename the former keymap-named settings file so status settings source composition lives in a settings-owned file, with corresponding tests; do not leave status parsing in a keymap-named file.
- Remove `ui.Deps.StatusLine`, `Model.StatusLine`, `ui.StatusLine`, `StatusSurface`, and `activeStatusLine`. Source owns shipped default, variant, absent-surface, and last-good behavior. UI retains only latest `Result` plus renderer-owned chrome.
- Populate a complete raw `Input` from actual UI state: server transport/target, session/model/effort, usage/cache, context, workspace provenance, main agent activity/approval, leaf delegation states, terminal geometry, per-surface widths, and raw clock. The source updates clock autonomously. Commands will receive exactly this raw input later.
- Replace two-document `wide`/`compact` template config with independent `header` and `footer` full/compact/minimal variants. Source selects each surface independently. Strict config validation, defaults, tests, plan examples, and docs must match.
- Reject command configuration as unsupported until task 05 implements it; never silently use defaults.
- Preserve the source lifecycle architecture: Submit/Changed/Latest Result/Close, capacity-one wakeups, deep-copy snapshots, source-owned stale work, and one UI listener. Close must have a bounded, detached cleanup context; prepare render execution to accept a cancellation context so command work can be cancelled/joined in task 05.
- Keep StatusML HTTP(S) semantic links/no OSC behavior.

Update ADR 0247, the acceptance plan, task briefs, and user/operator docs as needed to reflect the shipped correction. Add focused regression tests for each corrected boundary. Run Taskfile lint/test/docs and site build where docs change. Do not push.
