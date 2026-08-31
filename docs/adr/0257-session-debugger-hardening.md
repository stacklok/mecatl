# ADR 0257 — Session debugger incarnation and disclosure hardening

- Status: Accepted
- Date: 2026-08-31
- Scope: debugger target authorization, MCP approval, manifests, and durable lineage
- Supersedes: ADR 0256 where this decision is stricter
- Superseded by: —

## Context

ADR 0256 bound debugger evidence to a target ID and allowed positively annotated read-only selected MCP tools to follow the deployment policy. Review found four unsafe ambiguities: a deleted target ID could be reused, an outbound read can disclose target-derived data, request-content digests are offline oracles, and pruning tombstones retained full Principal PII. Local JSONL snapshot and lineage updates also have a crash boundary that Redis avoids with scripts.

## Decision

Persist a domain-separated target-incarnation fingerprint over the target ID, creation timestamp, and non-reversible owner scope on each debug session. It is internal snapshot/driver state and is never projected to the model or public harness API. Debugger construction, every evidence read, selected-MCP authorization, and restart rehydration always reload the target and require the expected fingerprint. When deployment ownership enforcement is enabled they additionally require the target and current principal to have the same stable `(issuer, subject)` owner identity as the binding; display name and grant metadata are not identity. When ownership enforcement is disabled those owner comparisons are deliberately omitted, consistently with the deployment posture. Deletion, replacement, enforced-ownership drift, or missing legacy binding is inaccessible.

Every selected direct MCP call asks for fresh interactive approval, regardless of its read-only annotation or posture. The base deployment policy is evaluated first: denies remain absolute and configured asks retain their provenance; the debugger adds only its lower authority floor. Headless calls deny. `allow_always` executes the current call but is not learned, so the next call asks again. `InspectSession` follows the same base-first rule, granting its debugger read floor only when the base policy does not tighten it. No local exact operator allowlist exists in this version.

Request manifests retain provider, model, reasoning effort, context window, message count/bytes, tool names, closed tool decisions, and prompt-component provenance/byte counts. They retain no prompt, message, or component content digest.

Durable lineage records are keyed by `(session ID, incarnation)`. Tombstones retain only a non-reversible owner-scope token and an incarnation token, never a Principal. A retained row is authorized only after loading and exactly revalidating its snapshot. Tombstones have no expiry API and survive restart and ID recreation indefinitely; recreating an ID adds a retained incarnation without replacing prior tombstones. Bounded queries return the current retained root before historical root tombstones, then direct records deterministically by ID, state, and incarnation. JSONL writes a tombstone before deleting the snapshot and reconciles missing/stale retained rows and interrupted tombstone deletion by incarnation. Redis keeps save/delete plus lineage updates atomic in its existing scripts.

A selected child scope returns only that child's descendants, never siblings. Any lineage truncation makes scan and retention completeness false. Restart requires exact equality between the persisted selected direct MCP tool set and the current set; additions, removals, and renames all fail closed.

## Consequences

Debug sessions created before the incarnation field cannot be rehydrated and must be recreated. Selected MCP reporting is deliberately approval-heavy, including outbound reads. Manifest evidence cannot confirm content equality, only request structure and size. Permanent tombstones consume bounded metadata until a future explicit retention decision supersedes this ADR.

## See also

- [ADR 0256](./0256-session-debugger-evidence-and-reporting.md)
- [Architecture overview](../architecture.md)
- [gRPC API](../usage/grpc-api.md)
