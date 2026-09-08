---
id: 02-fork-ledger-isolation
title: Fresh child-session ledgers for every fork
blocked_by: [01-core-ledger-and-file-tools]
status: done
branch: "plan-persistent-read-before-write-ledgers/02-fork-ledger-isolation"
worktree: ""
issue: "888"
retries: 2
last_error: ""
accumulator: acc/persistent-read-ledgers
---

# Task brief

Pin and enforce ledger isolation across `EnvironmentForker` and every child/fork construction path. A child must receive a fresh child-session ledger and must never inherit the parent session's selected durable ledger, while its Workspace and bound runner remain in the same child namespace. Keep fork/merge, shell, and parent/child filesystem-content semantics otherwise unchanged. Do not edit shared plan/docs/generated surfaces.

## Acceptance criteria

- AC1.6: A forked child receives a fresh child-session ledger and never inherits or writes the parent session's ledger, including when the parent selected durable storage.
  - verify: `TestPersistentReadLedgers_Scenario1_ForkLedgerIsolation`
