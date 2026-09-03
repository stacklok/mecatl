---
id: 18-documentation-reconciliation
title: Operator contract, inventories, and generated documentation
blocked_by: [07-public-api-reconciliation, 10-awaiting-lease-loss-handoff, 11-close-session-semantics, 12-graceful-drain-ownership, 14-modeled-rehydrate-repair, 15-modeled-transport-bytes, 16-modeled-awaiting-takeover, 17-helm-affinity-neutrality]
status: in-progress
branch: ""
worktree: ".scratch/task-session-affinity-18"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Perform the final single-writer documentation reconciliation after behavior is assembled. Update living architecture, usage, implementation notes, cloud-native resource/rehydration inventories, and public user docs with the exact affinity, authority, lease-loss, close/drain, modeled-handoff, and infrastructure-rollout contract. Regenerate `llms.txt` only through `task docs` in this serial task.

**Likely scope:** `docs/architecture.md`, `docs/usage.md` and focused usage chapters, `docs/design/IMPLEMENTATION-NOTES.md`, `docs/adr/0027-cloud-native.md` inventory rows/audit if new state or lifetimes landed, `user-docs/building/deployment/mecak8s.md` and relevant client pages, plus generated `llms.txt`. ADR-0290 and frozen ADR-0216 are not edited; the acceptance plan status is orchestrator-owned.

**Invariants:** routing grants no authority; provider identity is authoritative run context; errors disclose neither compared value; missing headers remain compatible; local invalidation is not backend fencing; already-started calls may complete; modeled tests do not claim Gateway/EndpointSlice behavior. Document AC8.5 as a blocking external infrastructure prerequisite with authenticated admission, request/header bounds, and client/IP/principal rate-limit validation—not as a chart guarantee or an offline mecatl proof. Run `task docs`, `task site:build`, and the named documentation contract test; do not hand-edit generated output.

## Acceptance criteria

- AC8.3: architecture, usage, implementation notes, and public user docs describe the
  exact field behavior, missing-header compatibility, non-disclosing failures,
  authoritative provider context, lease-loss limits, close/drain/handoff sequence, and
  the fact that routing grants no authority; they distinguish modeled PR tests from
  infrastructure rollout verification.
  - verify: `TestADR_0290_DocumentationContract`

- AC8.4: Generated `llms.txt` contains the new ADR and acceptance-plan contract and is
  fresh after `task docs`.
  - verify: demonstration — `task docs` regenerates and checks the documentation corpus

- AC8.5: Before enabling affinity in the separate infrastructure rollout, its Gateway,
  mesh, or equivalent ingress policy is live-validated to apply authenticated admission,
  request and header-size bounds, and client/IP/principal rate limits before or
  independently of affinity routing. The validation demonstrates that legal,
  attacker-chosen session IDs cannot create an unbounded targeted-replica sink; this is
  an infrastructure prerequisite, not a mecak8s chart guarantee.
  - verify: live infrastructure acceptance — the separate infrastructure PR records the
    deployed policy and an authenticated, bounded load test covering client, IP, and
    principal limits before rollout/cutover
