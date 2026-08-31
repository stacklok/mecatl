---
id: 03-attempt-domain-values
title: Define content-free learning attempt values and provenance
blocked_by: [02-direct-draft-inactivity]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add closed `engine/learning` attempt identifiers, states, outcomes, safe failure codes, immutable admission provenance, source binding, claim generations, projections, bounds, and validation. Deterministic identity must partition by caller/session/durable RunID/canonical digest without retaining principal values or content. Add structural reflection guards over stored and projected shapes.

## Acceptance criteria

- AC2.2: The immutable, content-free provenance records admission class plus the exact current-prompt/RunID/canonical-digest binding. Workers never re-derive explicit authority from replayed user-role text; forged provenance fields and synthetic continuations fail closed.
  - verify: `TestADR_0254_AdmissionProvenanceBindsCurrentPromptAndRejectsForgery`
- AC2.4: Attempt projections and stored records contain only bounded safe metadata and closed failure codes; structural tests reject raw content, paths, principal values, credentials, tokens, headers, secret-shaped values, driver-error text, diagnostics, metrics, watch envelopes, and optional EventLog projections.
  - verify: `TestADR_0254_AttemptSurfacesContainNoContentOrSecrets`
