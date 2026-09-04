---
id: 04-failure-and-adversarial-proofs
title: Fail-closed taxonomy, secret boundaries, and outage proofs
blocked_by: [03-output-profile]
status: in-progress
branch: ""
worktree: ""
issue: "372"
retries: 0
last_error: ""
accumulator: acc/acting-as-user-exchange
---

# Task brief

Complete the I3-C failure taxonomy, leakage defenses, and adversarial integration proof over the real `internal/actingaccess` entrypoint. Exercise every permanent/retryable category, secret sinks, stale facts, no-fallback behavior, mutation-resistant conjunction, repeated exchange expiry, and concurrent local-only outage isolation. Do not add a persistent replay ledger/cache, an engine integration, or ToolHive/B1/I3-S code; preserve disabled behavior.

## Acceptance criteria

- AC5.1: Invalid subject, owner mismatch, invalid actor, presenter denial, actor authority failure, target/scope denial, consent denial, unsupported profile, temporary unavailability, and invalid output have closed failure kinds with correct retryability.
  - verify: `TestActingAccess_Scenario5_FailureTaxonomy`
- AC5.2: Canary values placed in each secret wrapper are absent from every real I3-C error, diagnostic, decision trace, event, snapshot, tool result, JSON/text formatter, and persistence-capable value on success and every failure path; they appear only at the narrow mechanism call. The wrappers expose no `String`, `GoString`, marshal method, or exported raw-value accessor.
  - verify: `TestInvariant_acting_access_secret_sink_inventory`
- AC5.3: A denied or unavailable exchange never selects service, ownerless, ambient, broader actor, or provider credential authority. Retired/unknown subject, actor, or output verification keys and expired consent/association/policy facts fail closed after their bounded freshness window.
  - verify: `TestADR_0253_NoFallbackAuthorityAndStaleFacts`
- AC6.1: Mutating/removing each production conjunction predicate makes a black-box entrypoint test with independent decision-source spies fail; the test never derives its decision from fixture labels or a trace helper.
  - verify: `TestInvariant_acting_access_predicate_mutation_resistance`
- AC6.2: The output-token mechanism is not called for any pre-issuance refusal; the independent verifier receives compact bytes and public test material rather than fake structs or generic claims.
  - verify: `TestActingAccess_Scenario6_RefusalPrecedesIssuance`
- AC6.3: I3-C contains no persistent output cache; repeated exchange cannot mint an output beyond the original minimum verified validity, and unavailable external exchange affects only that external request.
  - verify: `TestActingAccess_Scenario6_NoPersistentCacheAndReplayBound`
- AC6.4: Through real I3-C composition, one blocked/unavailable external exchange and one concurrent local-only work item prove isolation: local work completes within a bounded deadline with zero subject-verifier/mechanism calls, then the external request returns only `unavailable` with no token or fallback identity.
  - verify: `TestActingAccess_Scenario6_LocalOnlyOutageIsolation`
