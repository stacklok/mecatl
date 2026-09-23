# ADR 0256 — Target-bound related evidence and approval-gated reporting

- Status: Accepted
- Date: 2026-08-31
- Scope: stored-session debugger lineage, request evidence, and selected reporting tools
- Supersedes: —
- Superseded by: —

## Context

ADRs 0254 and 0255 established a separate target-bound debugger and sanitized network-attempt evidence. A useful post-incident workflow also needs to explain delegated and scheduled work, recover pre-compaction context, describe the exact provider-neutral request shape, and optionally turn a diagnosis into an external report. Raw session IDs, inferred lineage, prompt copies in manifests, or a posture-level MCP allow would either cross the target's authority boundary or let analysis silently become mutation.

Durable stores may retain a child snapshot, retain only its content-free pruning tombstone, or have incomplete event retention. Debug sessions must remain restartable without introducing a handle registry or loading a target through a run-entry path. Reporting servers may expose both read and mutating tools, and their metadata is not uniformly trustworthy: only an explicit positive read-only annotation can justify bypassing approval.

## Decision

Expose related evidence through the target-bound `InspectSession` tool. Durable stores provide a bounded content-free lineage reader over validated session kind and relationship metadata. The debugger supplements, but never overrides, that index with typed Subagent, Parallel, Team, and schedule events. It returns deterministic opaque scope handles only for currently retained, same-owner descendants. Every scoped read rescans and revalidates the root, relationship, ownership, and retention state. Raw session IDs are not accepted as scope selectors. Pruned, inaccessible, absent, not-retained, and never-produced statuses are reported only when evidence supports them; completeness and retention limits remain explicit.

Add three debugger projections. `delegation` reports typed lifecycle, task, finding, disposition, stop, and parent-result facts without parsing prose or claiming causality. `history` catalogs the current snapshot, retained compaction archives, and retained event reconstruction behind separate target-bound history handles. `manifest` projects log-only request manifests emitted immediately before each provider call: prompt components, final tool names and observed projection decisions, message counts, and domain-separated digests, but no message or prompt bodies, tool schemas, arguments, URLs, headers, credentials, or provider-private content. Status distinguishes latest-run counters and cumulative snapshot usage from bounded lifetime EventLog aggregates.

A debug session may select configured server-global MCP servers by explicit name at creation. It borrows only their direct tools over the existing streaming-HTTP manager, persists the selected names and exact initial tool-name ceiling, and reconnects to neither inline nor client-supplied servers. Unknown, disconnected, empty, duplicate, or changed selections fail closed on creation or restart. Resource and query meta-tools are excluded.

Preserve the deployment permission policy, then apply a mandatory debugger reporting boundary. Deny remains absolute. Explicitly read-only selected tools retain their evaluated decision. Every selected tool without a positive read-only annotation is mutating and requires a fresh interactive human approval even when posture, configuration, or a prior verdict would otherwise allow it. Headless mutation is denied. `Allow always` executes only the current call and is not learned. The model may draft a report during diagnosis, but it may invoke a mutating reporting tool only after a later genuine current operator request explicitly asks to publish or send it.

## Consequences

A restarted debugger can inspect retained descendants, tombstones, delegation, archived history, request structure, lifetime counters, and sanitized network evidence without mutating or leasing the target. Handles reveal neither unrelated IDs nor durable bearer authority, but they are intentionally invalidated when current authorization or lineage no longer holds. Event retention, scan bounds, absent successful-request timing, and projections without raw audit records limit what can be concluded.

Selected reporting provides a practical GitHub-like issue journey while keeping analysis and publication separate. It adds an approval on every remote mutation and deliberately makes broad yolo/configured allows insufficient. Deployments must keep selected streaming-HTTP MCP servers configured across restart; tool-set drift fails rehydration rather than silently widening or narrowing authority.

## See also

- [ADR 0254](./0254-session-debugger-admin-transport.md)
- [ADR 0255](./0255-sanitized-network-attempt-evidence.md)
- [Architecture overview](../architecture.md)
- [Subagents and teams](../architecture/subagents-and-teams.md)
- [Observability](../architecture/observability.md)
- [Usage guide](../usage.md)
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)
