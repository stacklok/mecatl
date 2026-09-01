---
id: 01-handle-projection-presentation
title: Shared terminal-safe session handle projection and presentation protocol
blocked_by: []
status: in-progress
branch: ""
worktree: ""
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Replace the ordinary SHA-256 display digest with one client-owned fixed escaped-prefix handle utility. Make every ordinary mecatui presentation consumer use that projection without inventory access: active header, `/sessions` rows, debugger target chrome, terminal title, and the status-line input/templates. Rename the external status-line field from `Session.Digest` to `Session.Handle`, advance its protocol version to v2, and remove the digest alias. Preserve `/session` exact-ID copy behavior and all debugger evidence, scope, manifest, and cryptographic handles. Keep this proto-free and client-only; do not add server/proto alternate-ID behavior, collision expansion, or a second handle implementation.

Expected seam: the current header helper in `cmd/mecatui/client/client.go`, digest-based ordinary projections in `cmd/mecatui/ui/{view.go,sessions_surface.go,statusline_source.go,wintitle.go}`, and the status-line schema/templates in `cmd/mecatui/statusline/`. Extend or replace the existing digest-focused UI/status-line tests with the named scenario proofs, including terminal-safe valid UTF-8, control-bearing input, invalid UTF-8, empty IDs, collision stability, and inventory independence.

## Acceptance criteria

- AC1.1: A normal generated session ID has a handle containing its first twelve characters;
  header, `/sessions`, debugger target chrome, terminal title, and status input use that same
  literal rather than an ordinary display digest.
  - verify: `TestPredictableSessionHandles_Scenario1_SharedNormalHandle`
- AC1.2: Two IDs with the same fixed handle retain that same twelve-column projection; rendering
  neither expands either token nor loads the complete inventory, and pagination or ordering
  cannot change the displayed literal.
  - verify: `TestPredictableSessionHandles_Scenario1_FixedCollisionBehavior`
- AC1.3: An arbitrary non-empty valid-UTF-8 ID, including a long, shell-significant, or
  control-bearing legacy/custom value, produces the documented complete-atom, at-most-twelve
  ASCII-column literal. The literal contains only `[A-Za-z0-9._%-]`; empty IDs remain
  corrupt/invalid for actionable handling and receive no fabricated handle.
  - verify: `TestPredictableSessionHandles_Scenario1_EscapedUTF8AndControls`
- AC3.3: Header, `/sessions`, debugger-target presentation, terminal title, status input, and
  shipped StatusML templates use the same handle grammar. The cross-boundary table below proves
  its edge cases and that only ordinary presentation changes; `InspectSession` scope/history
  handles, evidence digests, and target+incarnation cryptographic handles remain unchanged.
  - verify: `TestPredictableSessionHandles_Scenario3_PresentationParityAndSafety`
