# path-escape-posture — Wave 2 index

Continuation of docs/acceptance/path-escape-posture.md (Wave 1 merged as PR #322,
squash `a7498ec6`). Wave 2 = Scenario 4 (the `strict`/`trusted` ASK path) + the
guardrail-routing decision + the Wave-1 panel-review follow-ups + the docs
deliverables. When Wave 2 lands, the plan flips `in-progress` → `landed` and
`task ac-trace-strict` gates all 26 ACs.

Wave-1 code is on `main` (base for this branch): `internal/app/escapepolicy.go`
(the posture fold — rule 6 currently leaves strict/trusted escapes to the inner
decision), `internal/app/escapeclassifier.go`, osfs `WithRelaxedReads`/
`WithRelaxedWrites`, and the child-never-relax guards.

## Tasks

- [01-strict-trusted-escape-ask](tasks/01-strict-trusted-escape-ask.md) —
  Scenario 4: strict/trusted resolve an escape to Ask (read + write), plan-mode
  precedence, headless no-hang. Satisfies AC4.1–AC4.4.
- [02-guardrail-routing-decision](tasks/02-guardrail-routing-decision.md) —
  resolve the deferred guardrail-routed escape checking (path-aware checker
  route). An ADR + the chosen mechanism.
- [03-panel-followups](tasks/03-panel-followups.md) — the Wave-1 panel
  follow-ups: Edit-ledger fingerprint pseudo-fs asymmetry,
  vetRelaxedParent→Canonicalize, the escapePolicy classifier-cache resource
  decision (CLOUD-NATIVE List 1).
- [04-docs-deliverables](tasks/04-docs-deliverables.md) — the two deferred docs:
  IMPLEMENTATION-NOTES "Path-escape posture" section + a user-docs note.

## Dependency graph

```
01-strict-trusted-escape-ask   (independent — Scenario 4 ACs)
02-guardrail-routing-decision  (independent — an ADR + mechanism)
03-panel-followups             (independent — Wave-1 cleanups)
04-docs-deliverables           (blocked by 01: documents the finished behaviour)
```

Wave 1: [01, 02, 03] parallel. Wave 2: [04] after 01.
