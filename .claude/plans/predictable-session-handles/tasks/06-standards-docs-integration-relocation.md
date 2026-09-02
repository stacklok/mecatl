---
id: 06-standards-docs-integration-relocation
title: ADR-0285 standards repair, documentation, and integration-test relocation
blocked_by: [05-resolver-exact-semantics-api-cleanup]
status: done
branch: "plan-predictable-session-handles/06-standards-docs-integration-relocation"
worktree: ".scratch/worktrees/issue-922-task06"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

After task 05, restore the mecatui package boundary and reconcile the public contract. Move the
real rendered-header-to-real `Client.CreateDebugSession` proof out of `cmd/mecatui/ui` into the
composition-level `cmd/mecatui` package. Drive the public UI update and `View` paths instead of
widening production APIs merely for test access; `cmd/mecatui/ui` production and tests must import
neither protobuf nor gRPC. Keep the resulting request-boundary proof for normal and escaped control-bearing valid-UTF-8
IDs.

Apply ADR-0285 identity consistently across plan, ADR index, task references, docs, and future test
names. Preserve ADR-0217's body and its decision-8-only supersession backlink. Update command help,
`docs/tui.md`, `docs/usage.md`, status-line reference, relevant architecture/implementation notes,
and existing `user-docs/` material for leading-hyphen encoding, exact-ID precedence, projected
ambiguity, and the embedded/connected one-`TARGET` grammar. Update landed session-continuity
AC4.2/AC4.3 verification names to
the current scenario tests, then remove stale `TestADR_0108_DisplayDigestIsNotAnID`, other
digest-named compatibility aliases, and pre-ADR-0285 session-handle test names. Preserve
`InspectSession` scope/history handles, evidence/manifest digests, and target+incarnation digests
unchanged. Regenerate docs as required and run the acceptance trace checks.

## Acceptance criteria

- AC2.5: The literal emitted by the real rendered normal-session header passes unchanged through
  the real `Client.CreateDebugSession` path and binds the resulting request to that header's exact
  target, including an escaped control-bearing ID. This proof lives in composition-level
  `cmd/mecatui`, drives public UI update/View paths, and keeps `cmd/mecatui/ui` tests proto/gRPC-free
  without widening production APIs for test access.
  - verify: `TestPredictableSessionHandles_Scenario2_RenderedHeaderCreatesBoundDebugger`
- AC3.2: `docs/tui.md`, `docs/usage.md`, the relevant `user-docs/` session/debug guides, and the
  status-line input reference use the handle term, explain leading-hyphen encoding and the fixed
  escaped-prefix projection, preserve `/session` exact-copy guidance, and document both embedded
  and connected one-`TARGET` examples. They document the `Session.Digest` → `Session.Handle` schema
  rename and protocol v2, with no digest alias.
  - verify: inspection — `task docs` and `task site:build` validate the reviewed documentation paths
- AC3.3: Header, `/sessions`, debugger-target presentation, terminal title, status input, and
  shipped StatusML templates use the same handle grammar. The cross-boundary table proves its edge
  cases and that only ordinary presentation changes; `InspectSession` scope/history handles,
  evidence digests, and target+incarnation cryptographic handles remain unchanged. The relocated
  transport-spanning proof is composition-level and introduces no new proto/gRPC dependency into
  `cmd/mecatui/ui`.
  - verify: `TestPredictableSessionHandles_Scenario3_PresentationParitySafetyAndLayering`
- AC3.4: The landed session-continuity plan's AC4.2 and AC4.3 point directly to current ADR-0285
  scenario tests. Stale `TestADR_0108_DisplayDigestIsNotAnID`, digest-named compatibility aliases,
  and pre-ADR-0285 session-handle test names are absent; ordinary presentation is never described
  as a debugger evidence digest.
  - verify: `TestADR_0285_OrdinaryHandleDoesNotAlterDebuggerEvidenceHandles`
