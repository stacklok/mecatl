# Work classification

Load this reference before `/to-acceptance-plan` classifies or drafts work.

1. Classify by decision and blast radius, never diff size:
   - **Spike:** evidence gathering with explicit questions and stop conditions; it is not shipped as-is.
   - **Routine:** an established or mechanical, reversible transformation with no new durable decision or durable-contract change.
   - **Cleanup:** removal of obsolete aliases, shims, duplicate representations, or legacy readers in favor of established canonical behavior after the operator has chosen the breaking-state treatment, with no new product behavior or design decision.
   - **Bounded:** substantive work needing an acceptance contract, but no durable architecture decision.
   - **Architectural:** a durable public/API compatibility, persistence/data-ownership, security/trust, deployment/operator, module/system-boundary, or cross-subsystem-invariant decision.
2. Route Spike, Routine, and Cleanup outside acceptance planning. Route Bounded and Architectural through this skill. Routine remains Routine regardless of locality or file count. Cleanup remains direct when interface removals or a large diff only execute the approved compatibility deletion. Record its scope, current contract, and approved breaking-state treatment in the implementation PR; no separate formulaic waiver is required. New product behavior, a new design choice, or unresolved data destruction is not Cleanup.
3. Create a new or superseding ADR only for the genuinely durable decision in Architectural work. Keep Bounded rationale in the issue, PR, or acceptance plan; current behavior in living docs; repeatable procedure in skills; temporary execution state in `.scratch/`.
4. If evidence does not support the lower class, escalate or stop for human-authorized Spike work; never silently downgrade. A worker that finds unrecorded material judgment is contract drift, not authority to decide.
5. Choose Split or Combined only after classification: delivery is orthogonal to work class. Split is default. Combined is only the compact one-task, one-scenario exception whose required interface categories are genuinely absent and whose rationale says why separate plan review adds no value.
6. Spike requires an explicit human request or authorization and must be reclassified before shipping; it is never merged to `main` as-is. A named explicit human waiver skips only plan/interface ceremony, never repository rules, applicable verification, or human merge authority. Do not infer either carve-out.
7. For Split, the merged Plan / Interface PR is the approval event, proved by git ancestry; no separate status-line edit is required. Combined still requires human merge of its sole PR.

After classifying Bounded or Architectural work, use the bundled [acceptance-plan template](ACCEPTANCE-PLAN-TEMPLATE.md).
