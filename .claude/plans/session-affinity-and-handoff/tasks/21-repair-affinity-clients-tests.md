---
id: 21-repair-affinity-clients-tests
title: Repair transport contract, client compatibility, Helm regression, and test oracles
blocked_by: [02-grpc-affinity-validation, 03-http-affinity-validation, 04-mecatui-affinity, 05-typescript-raw-affinity, 06-typescript-high-level-affinity, 17-helm-affinity-neutrality]
status: done
branch: "plan-session-affinity-and-handoff/21-repair-affinity-clients-tests"
worktree: ".scratch/task-session-affinity-21"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Repair brief

Resolve the cross-confirmed affinity/client/Helm/test findings while preserving missing-header compatibility and no protobuf changes.

- Define the official cross-transport affinity value set as exact non-empty printable ASCII supported by gRPC metadata and browser Headers; external IDs outside it remain usable but omit affinity. Reconcile ADR/plan/docs/vectors and retain ADR 0216's broader outbound-provider compatibility where required by released provider modules.
- Validate derived `CreateSession` affinity against exactly one authoritative `source_session_id` or `debug_target_session_id`; reject ambiguous dual references. Propagate from mecatui and TypeScript high-level creation.
- Preserve mecatui's existing `Converser` extension interface; add an optional/additive session-bound capability path. Explicit session-bound operations must return a useful error for illegal affinity values instead of silently opening unbound calls.
- Restore the unrelated Kubernetes pod scheduling `affinity` value/schema/template and its public documentation. Only Gateway/session-affinity surfaces remain forbidden.
- Strengthen gRPC and HTTP route matrices with headerless baseline, exact legal acceptance, mismatch/duplicate rejection for every classified route; exercise real Converse controls and second-session rejection.
- Add actual retry-wrapper/provider attempt coverage, make concurrency barriers bounded/cancellable, and replace brittle Go source-substring mirrors with meaningful behavioral/API-report/ac-trace proof.
- Keep Gateway/EndpointSlice live behavior in infra; do not add chart-owned gateway resources.

Protects AC1.1–AC1.5, AC2.1–AC2.5, AC3.1–AC3.4, AC4.1–AC4.5, and AC8.1–AC8.5. Run SDK gates, race tests, API checks, lint/test/docs/site/ac-trace. Commit locally; no push.
