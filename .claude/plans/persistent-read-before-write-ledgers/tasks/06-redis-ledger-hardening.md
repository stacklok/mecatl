---
id: 06-redis-ledger-hardening
title: Fail closed on corrupt Redis state and delete evidence atomically
blocked_by: [05-environment-ledger-separation]
status: done
branch: "plan-persistent-read-before-write-ledgers/06-redis-ledger-hardening"
worktree: ""
issue: "888"
retries: 0
last_error: ""
accumulator: acc/persistent-read-ledgers
---

# Repair-wave brief

Resolve the Redis panel blockers after the Environment/Workspace separation lands. Use the narrow FileVersion serialization API. Reject invalid zero versions on record. Decode stored records presence-aware so missing, null, wrongly typed, malformed, or unknown-format state returns `ok=false` with an error wrapping `tool.ErrLedgerUnavailable`; preserve valid empty opaque tokens. Add the read-ledger key to both canonical atomic Redis session-deletion scripts and prove normal deletion, conditional deletion, and session-ID reuse remove evidence. Retain explicit idempotent ledger-only cleanup if useful. Update shared conformance and focused offline miniredis tests. Do not edit living/acceptance/ADR docs.

## Repair acceptance

- Invalid FileVersion input never becomes durable evidence.
- Every malformed durable representation fails closed; valid empty tokens still round-trip.
- Store.Delete and DeleteSessionIfUnchanged remove the ledger atomically with other session sidecars.
- Reusing a deleted session ID begins with no evidence.
