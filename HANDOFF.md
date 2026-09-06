# HANDOFF — managed temporary command leases

## Current branch and PR

- Branch: `acc/managed-temporary-command-leases`
- Draft PR: https://github.com/stacklok/mecatl/pull/994
  - Title: `feat(temp): add managed temporary command leases`
  - Base: `main`
  - Current local/remote head before this handoff documentation update: `c062838f1`.
- The branch was rebased onto then-current upstream main and force-with-lease updated.
- All eight acceptance-plan tasks are marked done under
  `.claude/plans/managed-temporary-command-leases/tasks/`.
- The acceptance plan is `landed`: `docs/acceptance/managed-temporary-command-leases.md`.

## Implemented behavior

- Operator-global `temporary_storage` configuration with managed/system mode, TTL,
  sweep interval, regular sweep timeout, and shutdown timeout.
- `Bash.temp_scope`: managed default and separately permission-gated system escape.
- Per-command (`cmd-`) and background-job (`job-`) managed leases, process-group
  lifecycle handling, bounded reaper, and Build-owned maintenance worker.
- IDs use 96 bits encoded with unpadded URL-safe Base64 (16 characters).
- Managed workspace keys use a domain-separated SHA-256 96-bit prefix in the same
  encoding. The global workspace index was removed.
- `workspace.manifest` retains the key/version and canonical current workspace path
  for owner-only debugging; raw workspace identity is not persisted.
- Security repair wave covers scoped-runner fallback, pre-hook managed→system scope
  rewrite authorization, no implicit command timeout, lease replacement safety,
  process-start identity, lease-name validation, and test-home marker validation.

## Verification completed

- `task lint`
- `task test`
- `task docs`
- `task site:build`
- `task ac-trace-strict`
- `go run ./cmd/mecademo`

The only prior site-build warnings were missing `background-commands` anchors; the
heading was restored in `c062838f1` and the warning is gone.

## Known unrelated issue

- https://github.com/stacklok/mecatl/issues/1153 — `Converse` can return EOF rather
  than InvalidArgument for a second start frame. Reproduces with:

  ```sh
  cd internal/adapter/server
  go test -run '^TestADR_0294_ConverseRejectsSecondRetryButIgnoresUnsetFrames$' -cpu=1 -count=10
  ```

  It is an upstream `controlErr`/closed-event-channel select race and is not touched
  by this branch.

## Approved next change: macOS support

The operator explicitly requires managed temporary storage to support macOS in v1,
not Linux-only. ADR 0281 was partly updated in the working tree to say Linux + macOS;
those edits are not yet committed or pushed.

Implementation direction:

- Move common managed namespace, manifest, lease, and reaper code to a shared Unix
  implementation (`linux || darwin`) where the APIs/semantics match.
- Keep process-start identity platform-specific: Linux can retain `/proc/<pid>/stat`;
  Darwin needs a real Darwin process API equivalent. Do not fall back to PID-only.
- Keep Windows and other platforms on fail-loud `mode: managed` rejection with
  `mode: system` available.
- Add Darwin compilation and behavior tests. Preserve Linux security tests and all
  handle-rooted/no-link/owner-mode behavior.
- Update ADR 0281’s remaining Linux-only wording, the acceptance plan, architecture,
  `docs/usage.md`, and `user-docs/` together.

## Immediate restart checklist

1. Read `AGENTS.md`, this handoff, ADR 0281, and the landed acceptance plan.
2. Inspect and commit the pending ADR 0281 macOS wording before implementation.
3. Add a repair task under `.claude/plans/managed-temporary-command-leases/tasks/`
   for macOS support; retain the existing accumulator/PR rather than opening a second PR.
4. Implement shared Unix + Darwin process-identity support with offline tests.
5. Run `task lint && task test && task docs && task site:build && task ac-trace-strict`
   and `go run ./cmd/mecademo`.
6. Run panel review again because platform/lifecycle code changed, repair blockers,
   and push the updated draft PR. Do not merge the PR.

## Deliberate deferrals

- Workspace scratchpad lifecycle: ADR 0282.
- Managed delegation-fork lifecycle: ADR 0283.
- Sharing a parent managed temporary workspace for recursive mecatl invocations.
- Test-only eager cleanup semantics for direct `task test` / `t.TempDir` residue
  outside a managed parent command lease.
- Windows managed temporary storage.
