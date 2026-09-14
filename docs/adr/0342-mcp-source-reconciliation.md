# ADR 0342 — Reconcile direct MCP through durable bindings and leased generations

- Status: Proposed
- Date: 2026-09-14
- Scope: direct/global MCP source discovery, durable authority identity, generation-owned catalogue publication, and root-session refresh
- Supersedes: ADR 0057 only for its deferred “no live catalog mutation” decision; its notification transport, dirty invalidation, bounded lazy-list, reconnect, and teardown decisions remain
- Superseded by: None

## Context

Mecatl has an ordered, source-neutral MCP discovery seam: configured/static servers precede ToolHive workloads, so configured names win collisions. It currently connects one manager and registers its tools into process-wide and per-session catalogues. Status can later re-probe sources, and MCP servers emit list-changed notifications, but existing catalogues cannot safely change. ADR 0057 deferred this because `tool.Catalog` is append-only.

Name-only durable authority is insufficient for live reconciliation. A same-named capability can move to another source or endpoint, change schema, or change read-only classification while retaining its catalogue name. Treating that as the same grant silently changes dispatch authority. Conversely, deleting only a process-local catalogue entry is not durable: restart can make the same name executable again from persisted name authority.

In-place manager/catalogue mutation also creates mixed-generation behavior and unsafe teardown. A model could receive one schema, permission-check another definition, dispatch through a replaced connection, and construct a child from a third view. Cached engines, direct RunTeam, referenced-agent MCP, and out-of-run resource/prompt calls outlive one lookup and therefore need ownership stronger than a run-local pointer.

Protected broker MCP is separate. ADR 0335 gives it `/tools-connect`, ToolHive-owned consent/grants, and session attachment state. Client-provided MCP is also session-local. Neither belongs in process-wide direct/global reconciliation.

## Decision

1. **Reconcile ordered source snapshots through one Build-owned component.** The source-neutral reconciler consumes complete immutable desired snapshots. Earlier sources win collisions, preserving configured/static-over-ToolHive precedence. ToolHive is the first dynamic source; future sources use the same seam. Configured settings, CLI inputs, and environment-referenced credentials are captured at process start and reused in every cycle. Project-tier MCP stays ignored; ordinary sessions never reload operator configuration.

2. **Bind durable authority to capability identity, not only name.** Add `session.DirectMCPBinding` records to `session.Authority`. Each record carries a capability name, an opaque `sha256:<64 lowercase hex>` digest, and revoked state. Composition computes the secret-free digest with domain-separated deterministic encoding over source identity, server identity, canonical endpoint/routing identity, the complete advertised tool contract/schema, and every dispatch-relevant annotation including read-only. It excludes headers, credentials, token values, and rotating OAuth material. Direct MCP synthetic per-server resource capabilities receive bindings under the same rule. Execution requires both the existing `CapabilitySet.Tools` name and an active digest equal to the selected generation's digest.

3. **Treat identity change as removal plus addition.** A same-name source, server, canonical endpoint/routing, schema/contract, or dispatch-annotation change revokes the old grant. At the next run entry, the Service compares durable bindings with the current generation, removes missing/mismatched names, retains old binding records as revoked/unavailable evidence, and durably confirms attenuation before model or tool work. Reappearance never automatically grants authority, including after restart. If storage exclusion or persistence confirmation fails, the run does not begin.

4. **Adopt legacy name-only authority once without widening.** An absent binding field denotes legacy authority; a present empty list denotes completed adoption with no direct grants. Before the first post-upgrade run, materialize bindings only for current same-name direct MCP capabilities already present in the legacy exact tool ceiling. Never add a name absent from that ceiling. Persist adoption before model work. Once the field is present, later reappearance cannot trigger adoption.

5. **Refresh only the direct/global subset.** `/mcp-refresh` is an owner-authorized, argument-free, idle ordinary-root operation. Let `B` be prior direct binding records, `C(B)` all their names, `T` all prior tool names, and `A` successfully active current-generation direct bindings. Refresh sets tool names to `stableUnique((T ∖ C(B)) ∪ C(A))`: all non-direct/core/latent-Team/profile/client/broker names survive, every prior identified direct name is removed, and only current active direct names are added. Current `A` records become active; prior records whose capability is absent from `A` remain revoked evidence. A same-name changed record is replaced by the new active record because this explicit operation is the regrant, preserving one record per capability. The bounded aggregate rejects overflow instead of silently dropping evidence. Refresh preserves conversation, placement, ownership, and every non-direct authority axis.

6. **Expose narrow aggregate and run-scoped engine APIs.** `Session.AttenuateDirectMCPAuthority([]DirectMCPBinding) error` performs non-widening run-entry attenuation and one-time legacy adoption for ordinary roots. `Session.ReplaceDirectMCPAuthority([]DirectMCPBinding) error` performs the explicit refresh algebra. Both are idle-only, bounded, clone inputs, preserve unrelated aggregate state, and reject pending permission/external-authorization/workspace-enrollment state. Delegated authority derivation copies only the digest-identical active parent bindings whose names survive the child's tightened tool ceiling; persisted child resume rejects binding drift and never treats a child as a legacy root. `agent.RunRequest.UnavailableTools []string` carries bounded unavailable names derived only from that session's revoked/mismatched bindings and shadows even a current same-name catalogue entry before schema projection, lookup, authority, permission, or dispatch. Such calls return the fixed permanent result `removed from current MCP configuration; do not retry unless the catalog changes`. There is no process-global tombstone cache and no synthetic model/history refresh message. These are intentional Added/minor engine API changes.

7. **Publish immutable generation-owned bundles with explicit leases.** A generation owns direct/global servers and manager view, tools, resource/prompt provider, source/status snapshot, binding identities, and generation-bound shared engine/factories. `assembleCatalog` remains the single complete registration path and receives one explicit generation contribution. Service atomically swaps the current shared-engine generation. Cached per-session engines are generation-tagged; publication marks them stale, and the next eligible boundary evicts/rebuilds them under existing run-entry/liveness guards. Stale idle eviction is bounded so unused sessions cannot retain a generation indefinitely; active engines close after their runs.

   Every generation-bound shared, per-session, debug, specialist, and team engine holds a lease for its useful lifetime. Direct-MCP debug sessions remain non-refreshable: their selected-name ceiling additionally requires digest-identical bindings, and removal or identity drift fails closed without admitting additions. Active root runs, direct RunTeam, delegation/reference-MCP consumers, and any operation not already covered by an engine lifetime hold the needed run/operation lease. Each out-of-run list/read/get resource or prompt operation takes one short-lived current-generation lease; a list followed by a later get is not snapshot-atomic, and a stale later identifier fails visibly. Prompt expansion inside a run uses that run's generation. No displaced generation closes until all engine, run, and operation leases drain.

8. **Publish a complete active set, not an all-or-nothing desired set.** A source consultation failure retains that source's last-known-good desired snapshot. A successful snapshot, including empty, is authoritative desired truth; removal withdraws immediately in new generations. Reuse unchanged healthy server connections/bindings. Connect additions independently. For a changed binding, withdraw the old identity and attempt the new one; failed connect/initialize/list/validation leaves it unavailable and never restores the superseded binding. Failure of one addition/replacement does not block unrelated valid changes. Partially built candidates close. The published immutable generation contains the complete successfully active set; observed-versus-active status remains stale/degraded where desired entries failed.

9. **Bound every trigger and retained structure.** Automatic ToolHive polling exists only when ToolHive discovery is enabled and is bounded and jittered. Tool/resource/prompt list-changed notifications and manual refresh enter the same reconciler. Exactly one cycle runs, at most one invalidation is queued/coalesced, and a global cooldown prevents sequential session refreshes from forcing unbounded reconnect work. Caller cancellation stops waiting but never cancels shared reconciliation; a refresh during cooldown adopts the current active generation. Hard finite source, server, tool, schema, and generation-retention constants fail stale with bounded diagnostics. Automatic work never launches browser/OAuth consent.

10. **Persist authority before use under existing exclusion.** Refresh and attenuation use existing owner authorization, `runEntryMu`, mutation lease, and `SessionMutationCapability`; a shared store without proven mutation exclusion fails closed. They mutate a detached authoritative load, not a live/cached session. A successful save is confirmed before widening or model work. After an ambiguous save, reload authoritative state: exact candidate means success, exact old state means failure, and mismatch/unknown fails closed. `/mcp-refresh` builds no per-session engine; the next run selects/rebuilds against the current generation. No widened live engine is published before durability is known.

11. **Add status/control metadata without breaking inventory.** Preserve `McpServerInfo.url` and existing per-source pre-shadow rows. Add separate observed/active generation and stale/degraded/reconciling fields, plus matching gRPC, HTTP, and mecatui refresh controls. New status, diagnostics, errors, binding records, and model results expose no new raw configuration, header, token, or credential material; canary tests enforce that boundary.

## Consequences

A durable grant now identifies the direct capability contract it authorized. Endpoint, schema, or read-only changes require explicit owner refresh even when names remain stable. Automatic attenuation and one-time legacy adoption add a persistence operation to run entry; unavailable or ambiguous storage can prevent a run, which is the deliberate fail-closed cost of avoiding authority resurrection.

Generation consistency extends across engine lifetimes, cached variants, direct RunTeam/delegation/reference MCP, and out-of-run resource/prompt operations. Old connections remain alive until every lease drains, while bounded stale-idle eviction prevents unused cached sessions from retaining them forever. Complete active-set publication allows unrelated healthy changes through but makes observed, desired, and active truth distinct and requires clear stale/degraded status.

The reconciler may reuse unchanged healthy connections, but changed identities are withdrawn before replacement succeeds. This favors authority correctness over availability for that capability. Source consultation failure is different: its LKG remains desired because no newer source truth was obtained. Successful empty is newer truth and withdraws the source.

`Authority` snapshot JSON gains an additive binding list. Missing is the one-time legacy state; present empty is meaningful and must not collapse back to missing. The maximum of 512 durable binding records, 256-byte capability framing, and fixed digest format are public validation contracts. Other source/server/tool/schema/generation/cooldown/eviction bounds remain tested internal constants.

The process gains long-lived generation bundles, leases, source LKG/status caches, stale-engine eviction, and trigger/cooldown state. Implementation must inventory every such resource in ADR 0027 List 1 and every restart-relevant state decision in List 2. There is no process-global tombstone cache; unavailable-call evidence is session-durable.

Configured settings/CLI/environment inputs remain restart-only. Live operator settings reload is deferred to a future narrow admin operation and may not become ordinary session behavior. Broker/protected and client-provided MCP remain separate authority/lifecycle domains.

## Rejected alternatives

- **Authorize by name only.** Same-name endpoint/schema/read-only changes silently alter the granted capability and can resurrect after restart.
- **Use a generation ID as durable authority.** Process generations are runtime ownership, not stable capability identity; a secret-free contract digest survives restart without persisting endpoints.
- **Keep a process-global removed-name tombstone cache.** It is lost on restart, detached from session authority, and grows another long-lived cache. Durable revoked bindings already provide exact evidence.
- **Mutate shared catalogues/managers in place or pin only active runs.** Cached engines, teams, referenced agents, and out-of-run resource/prompt operations can outlive the pin and observe closed or mixed managers.
- **Publish desired state all-or-nothing.** One failed addition would block unrelated removals and healthy changes. Publishing the successfully active subset is safer and more available while status remains honest.
- **Keep a superseded binding when replacement fails.** That executes an identity the successful source snapshot explicitly replaced.
- **Treat source consultation failure as empty.** A transient runtime failure would masquerade as authoritative removal; LKG is retained until a successful source snapshot says otherwise.
- **Automatically add every new binding to old sessions.** Dynamic discovery would silently widen durable exact authority.
- **Mutate a live session/engine before save or accept ambiguous save as success.** Failure can leave runtime authority wider than durable truth. Detached mutation plus reload confirmation avoids that split.
- **Reload operator settings during `/mcp-refresh`.** It gives an ordinary session an operator-config ingestion capability and obscures the restart boundary.
- **Fold broker or client MCP into global generations.** Their authorization, attachment, and teardown contracts are session-local and materially different.
- **Make list-then-get snapshot-atomic.** That requires public generation handles and retained cross-call leases; individual call consistency with visible stale identifiers is sufficient.

## See also

- [MCP source reconciliation acceptance plan](../acceptance/mcp-source-reconciliation.md)
- [ADR 0057 — MCP client server notifications](./0057-mcp-server-notifications.md)
- [ADR 0335 — Idle-session MCP broker workspace refresh](./0335-idle-session-broker-workspace-refresh.md)
- [ADR 0027 — Cloud-native arc and resource inventory](./0027-cloud-native.md)
- [Extensibility architecture](../architecture/extensibility.md)
