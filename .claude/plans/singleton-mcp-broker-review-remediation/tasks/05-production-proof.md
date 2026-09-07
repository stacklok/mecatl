---
id: 05-production-proof
title: Production-path authorization and deployment proofs
blocked_by: [04-deployment-release]
status: in-progress
attempt: 1
branch: "plan-singleton-mcp-broker-review-remediation/05-production-proof-attempt-1"
worktree: "/Users/jakub/devel/mecatl/.worktrees/distributed-broker-contract"
issue: ""
retries: 0
last_error: ""
accumulator: acc/singleton-mcp-broker-review-remediation
---

# Task brief

Replace shallow/name-only broker proofs with an offline production-equivalent vertical through
engine/session Stage 3, real callback handlers, authenticated remote transport, and embedded
ToolHive. Cover hostile callback state, restart boundaries, confidential-client custody, and
real build/render artifacts. Strengthen the original named tests rather than adding vacuous
sentinels.

## Acceptance criteria

- AC5.1: An offline production-equivalent engine/session run parks one protected ToolHive call, completes authorization through the mounted browser callback, and resumes that exact call once through authenticated remote broker transport.
  - verify: `TestSingletonBrokerRemediation_Scenario5_Stage3RemoteVertical`
- AC5.2: The real fixed ToolHive callback bundle and final callback accept success only through opaque high-entropy broker-created state bound to one enrollment and expiry, consume it once, and never trust browser-supplied session, owner, backend, route, or principal selectors. Missing, malformed, expired, duplicate, replayed, and cross-enrollment state gets the same generic public rejection before exchange or unrelated mutation, with no state, code, token, enrollment, or upstream identity in responses or diagnostics.
  - verify: `TestSingletonBrokerRemediation_Scenario5_CallbackCorrelationReplayAndNonDisclosure`
- AC5.3: The production-equivalent callback and refresh flow preserves ADR 0302 custody: the generated broker-client secret is high-entropy, process-memory-only, stored by ToolHive only as its required hash, used only as `client_secret_basic`, and absent from form bodies, protobuf/metadata, snapshots/events, callbacks, diagnostics, metrics, rendered configuration, and upstream MCP requests.
  - verify: `TestADR_0302_SingletonBrokerConfidentialClientCustody`
- AC5.4: Killing and replacing the broker during pre-prompt enrollment proves fresh-client recovery, while replacement during the parked continuation proves deterministic interruption with no redispatch.
  - verify: `TestSingletonBrokerRemediation_Scenario5_RestartBoundary`
- AC5.5: The required build gate creates an executable `mecabroker` binary and the deployment gate renders and validates complete production fixtures rather than asserting source substrings.
  - verify: `TestSingletonBrokerRemediation_Scenario5_ProductionArtifacts`
- AC5.6: The original production-broker scenario tests themselves drive the production factories and externally observable boundaries named by their ACs; replacing any factory with a hand-written attachment, callback, coordinator, or source-substring fixture makes the proof fail.
  - verify: `TestInvariant_singleton_broker_named_proofs_use_production_paths`
