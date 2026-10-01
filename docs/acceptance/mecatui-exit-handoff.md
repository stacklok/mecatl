# Mecatui exit handoff — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — improves one client-owned exit presentation without changing session storage, run authority, or public service contracts.
**Decision record:** None — the existing session snapshot and resume flags supply the required data; this is a bounded terminal-client workflow decision.
**Phase:** session continuation
**Status:** landed in this implementation candidate, 2026-10-01. Authoritative on merge.
**Delivery:** Split. The additional CLI output and its compatibility with the existing machine-readable line warrant interface review before implementation.
**Expected tasks:** deferred to orchestration.
**Issue:** [stacklok/mecatl#1992](https://github.com/stacklok/mecatl/issues/1992).

After a normal embedded `mecatui` exit, the operator sees an exact command to resume the final active chat, a separately labelled `--resume-latest` alternative, and a short factual session summary. The existing JSON-quoted `final-session-id` stderr record remains intact for scripts. A connected client's human-readable handoff is optional; the required behavior is limited to embedded mode.

## Illustrative output

For a clean embedded exit with an available snapshot, aim for a compact block like this after the terminal is restored:

```text
mecatui: final-session-id="01JOPAQUESESSIONID"
Session: Fix the failing tests
Model calls: 12
Tokens (main): 29,713 input, 1,807 output, 6,432 cache read
Resume: mecatui --resume '01JOPAQUESESSIONID'
Or: mecatui --resume-latest (may select a different chat)
```

This is presentation direction, not a byte-for-byte golden. The interface contract and acceptance criteria govern the record, data sources, shell safety, and failure cases; a missing title or snapshot omits the corresponding summary lines. The embedded server's separate startup notice is not shown here.

## Human decisions

None — the operator selected embedded mode as the required path, made connected-mode presentation optional based on implementation complexity, and requested the exact resume command, `--resume-latest` guidance, title, model-call count, and token counts. This plan defines their sources and failure behavior.

## Interface contract

- **gRPC / protobuf:** None — the existing `GetSession` snapshot already carries title metadata, the number of model calls begun (`turns`), and `token_usage["main"].total`.
- **Exported Go APIs / interfaces:** The command-local `cmd/mecatui/client.SessionSnapshot` gains `Turns int32`, projected from `Session.turns` by the existing `GetSession` wrapper. Its `Title` remains the server's display-title projection (which may be derived from the first prompt), not necessarily the persisted title metadata. No engine API or other exported Go contract changes.
- **Tool schemas:** None — the exit handoff is terminal output, not a model-visible tool.
- **CLI / config:** No new flags or settings. On normal embedded exit, preserve the current JSON-quoted `mecatui: final-session-id=` stderr record byte-for-byte and print human-readable lines after it: `Session:` followed by a non-empty display title; `Model calls:` followed by the decimal snapshot count and `Tokens (main):` followed by decimal input and output counts when the snapshot is available; `Resume: mecatui --resume` followed by a POSIX-shell-quoted final ID when the ID is safe as a single physical terminal line; and `Or: mecatui --resume-latest (may select a different chat)` when an exact command is printed. Include nonzero cache-read and cache-write counts as separately labelled components, never as additional tokens summed with input or output. These are lifetime main-session counts, not the latest turn's context occupancy, auxiliary title-generation tokens, or a price. Connected mode retains its current machine-readable handoff as the required baseline; a human-readable connected command is optional only if it correctly includes `connect ADDRESS`, never prints credentials, and meets the same safety/verification criteria as embedded mode.
- **Events / persistence:** None — read the existing authoritative session snapshot without writing events or changing stored state.
- **Security / authority:** Read only the already-bound final session through the existing client and ownership-checked `GetSession` while the connection is live; never inspect another session, reveal auth flags, or interpolate a title or ID as terminal control sequences or executable shell syntax. If no single-line safe exact command can be represented, omit that command rather than fabricate one; retain the JSON-safe ID record.
- **Compatibility / migration:** The JSON handoff retains its exact prefix, quoting, timing after alternate-screen teardown, and one-line grammar in embedded and connected modes. Human lines are additive stderr output; consumers of the documented handoff must continue to be able to locate and decode the original record. No migration or change to `--resume` / `--resume-latest` selection semantics.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — Embedded exit offers an actionable continuation

The final bound session, not the startup session, determines the handoff. The snapshot carries canonical count and usage values ([session continuity UX](session-continuity-ux.md#scenario-7--exit-leaves-a-safe-continuation-handoff), [ADR 0307](../adr/0307-canonical-durable-token-accounting.md), [`GetSession` contract](../../contracts/proto/mecatl/v1/harness.proto)). The client obtains the summary before closing its connection and emits it only after terminal teardown and normal cleanup. The owning public guide for this workflow is [`mecatui` sessions](../../user-docs/mecatui/sessions.md).

**Acceptance:**
- AC1.1: After a clean embedded exit with a final active session, stderr contains exactly one unchanged, JSON-decodable `final-session-id` record; for a single-line safe ID it also contains a copyable `mecatui --resume` command for that exact final ID and a visibly qualified `--resume-latest` alternative. The record and human lines follow terminal teardown and cleanup; stdout is untouched.
  - verify: `TestMecatuiExitHandoff_Scenario1_EmbeddedResumeAfterTeardown`
- AC1.2: When a snapshot is available, the summary uses the server's display title (stored or server-derived), model-call count (not user-prompt count), and lifetime `main` input/output totals; optional nonzero cache components are separately labelled. An empty display title is omitted; `session_title` token usage is never included in main usage.
  - verify: `TestMecatuiExitHandoff_Scenario1_AuthoritativeSummary`
- AC1.3: For single-line UTF-8 IDs containing spaces, shell metacharacters, quotes, and backslashes, the printed command parses under POSIX `sh` into precisely the intended `mecatui`, `--resume`, and byte-exact final-ID arguments, with no extra commands. A control-character or multiline ID suppresses the human command without changing the JSON-safe, byte-exact machine ID; an untrusted title cannot add output lines or terminal effects.
  - verify: `TestMecatuiExitHandoff_Scenario1_SafePresentation`

### Scenario 2 — Exit failures preserve an honest handoff

Session metadata is advisory to the human summary, not a reason to lose the existing exit record. Keep the prior clean-exit and connect-restart boundaries ([ADR 0217](../adr/0217-session-discovery-continuation.md), [exit writer](../../cmd/mecatui/exit_handoff.go), [command lifecycle](../../cmd/mecatui/main.go), [session continuity UX](session-continuity-ux.md#scenario-7--exit-leaves-a-safe-continuation-handoff)).

**Acceptance:**
- AC2.1: The snapshot lookup starts after the program returns but before the client is closed, uses a deadline of at most one second, and cannot hold up cleanup beyond that deadline. A blocked, failed, or missing snapshot leaves the existing JSON ID line and safe exact embedded resume guidance intact but emits no stale title, calls, or token totals. All human-readable output follows alternate-screen restoration and normal cleanup.
  - verify: `TestMecatuiExitHandoff_Scenario2_SnapshotUnavailable`
- AC2.2: No-session, unsuccessful program run, interrupted/forced exit, and connect-restart paths emit no misleading success summary; connected mode retains the existing exact ID record on normal exit. If connected-mode human guidance is implemented, tests also prove that it names the original remote address, does not emit an embedded command, and exposes no auth material.
  - verify: `TestMecatuiExitHandoff_Scenario2_ExitMatrix`
- AC2.3: The owning user guide describes the exact and latest continuation choices and the meaning of summary counts only once the behavior ships.
  - verify: inspection — review `user-docs/mecatui/sessions.md`; `task docs` and `task site:build`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Connected-mode human-readable handoff | Optional in this implementation, otherwise follow-up | Not an acceptance gate; only include if the exact remote invocation can be reconstructed safely under the interface and Scenario 2 constraints. The existing JSON ID record remains required. |
| Hiding the embedded server's startup socket notice | Separate issue scope | This is emitted before the TUI starts, independently of the post-teardown handoff; changing diagnostics is not necessary for an actionable exit. |
| Background work control and usage across delegated or auxiliary sessions | Separate proposal | No new lifecycle control or accounting scope is inferred from the exit summary. |

## Definition of done

1. Focused offline process and client projection tests pass in addition to `task lint`, `task test:race`, `task docs`, and `task site:build` on the implementation candidate.
2. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The implementation PR links the approved plan commit and reports interface conformance; `/panel-review` reports no unwaived ship blockers.

## Deferred decisions and known risks

- Snapshot lookup adds work to shutdown. Bound it and fall back to the existing handoff if it fails. Do not derive model calls from TUI event replay or infer token totals from the context meter.
