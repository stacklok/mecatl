---
id: 03-redis-ledger
title: Durable session-scoped Redis ledger proof
blocked_by: [01-core-ledger-and-file-tools]
status: done
branch: "plan-persistent-read-before-write-ledgers/03-redis-ledger"
worktree: ""
issue: "888"
retries: 0
last_error: ""
accumulator: acc/persistent-read-ledgers
---

# Task brief

Add the first non-memory read-ledger implementation under the existing root Redis adapter, reusing its shared client ownership, secure connection posture, and key-prefix conventions. Bind each handle to one session scope; use an injective physical identity and a versioned, validated stored representation. Prove reopen across independent handles, session isolation, missing versus timeout/unavailable/corrupt state, concurrent access, delete-and-ID-reuse safety, and adversarial separator handling through miniredis and the shared conformance suite. The ledger must expose no file-content operation and must not become a production default or add mecak8s configuration. Do not edit shared plan/docs/generated surfaces.

## Acceptance criteria

- AC2.1: A version recorded through one Redis ledger handle is returned after reopening another handle for the same session, including when the two handles represent separate process/replica instances.
  - verify: `TestPersistentReadLedgers_Scenario2_RedisReopen`
- AC2.2: Two sessions sharing one Redis server and one file-content backend retain independent path/version entries; neither session can use the other's prior read.
  - verify: `TestPersistentReadLedgers_Scenario2_RedisSessionIsolation`
- AC2.3: Redis ledger storage carries an injectively encoded session scope plus normalized path identity and opaque version data only; adversarial delimiters cannot make two `(session, path)` pairs address the same entry, and the ledger API has no file-content operation.
  - verify: `TestInvariant_persistent_read_ledger_storage_independence`
- AC2.4: Missing state returns “not recorded,” while timeout, unavailable transport, undecodable data, or corrupt state returns an error distinguishable from absence.
  - verify: `TestPersistentReadLedgers_Scenario2_RedisFailureClassification`
- AC2.5: The in-memory and Redis implementations pass one shared read-ledger conformance contract entirely offline.
  - verify: `TestReadLedgerConformance`
- AC2.6: Concurrent record and lookup operations on one session ledger are race-free, and independently opened Redis handles converge on complete opaque tokens rather than torn or partially decoded state.
  - verify: `TestPersistentReadLedgers_Scenario2_ConcurrentAccess`
- AC2.7: Removing a Redis ledger scope removes all evidence for that session; reopening or reusing the same session ID starts with no recorded version and cannot inherit a deleted session's authorization evidence.
  - verify: `TestPersistentReadLedgers_Scenario2_DeleteAndReuseStartsEmpty`
- AC2.8: Distinct session/path pairs containing separators or common prefix material remain distinct Redis addresses and cannot observe one another's evidence.
  - verify: `TestPersistentReadLedgers_Scenario2_InjectiveRedisIdentity`
