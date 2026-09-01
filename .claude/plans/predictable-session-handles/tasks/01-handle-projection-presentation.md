---
id: 01-handle-projection-presentation
title: Shared terminal-safe session handle projection and presentation protocol
blocked_by: []
status: done
branch: "plan-predictable-session-handles/01-handle-projection-presentation"
worktree: ".scratch/worktrees/issue-922-task01"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Replace the ordinary SHA-256 display digest with one client-owned fixed escaped-prefix handle utility. Make every ordinary mecatui presentation consumer use that projection without inventory access: active header, `/sessions` rows, debugger target chrome, terminal title, and the status-line input/templates. Rename the external status-line field from `Session.Digest` to `Session.Handle`, advance its protocol version to v2, and remove the digest alias. Preserve `/session` exact-ID copy behavior and all debugger evidence, scope, manifest, and cryptographic handles. Keep this proto-free and client-only; do not add server/proto alternate-ID behavior, collision expansion, or a second handle implementation.

Expected seam: the current header helper in `cmd/mecatui/client/client.go`, digest-based ordinary projections in `cmd/mecatui/ui/{view.go,sessions_surface.go,statusline_source.go,wintitle.go}`, and the status-line schema/templates in `cmd/mecatui/statusline/`. Extend or replace the existing digest-focused UI/status-line tests with the named scenario proofs, including terminal-safe valid UTF-8, control-bearing input, invalid UTF-8, empty IDs, collision stability, and inventory independence.

## Acceptance ownership

Historical task completed before panel review. Its former AC1.1–AC1.3 and AC3.3 text is
superseded by the revised plan; task 05 owns revised AC1.1–AC1.6 where implementation repair is
required, and task 06 owns revised AC3.3. This done task owns no current numbered AC.
