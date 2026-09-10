# ADR 0322 — Session-owned broker connector inspection

- Status: Accepted
- Date: 2026-09-09
- Scope: broker connector inventory in mecak8s and mecatui
- Supersedes: None
- Superseded by: None

## Context

The existing `/mcp` panel depends on a global MCP provider that supplies sources,
resources and prompts. Broker-only deployments intentionally omit that provider:
protected tools and authorization belong to individual sessions. Enabling the old
capability for broker mode would promise unsupported resource/prompt operations.
Reusing its sessionless source DTO would also expose upstream URLs through the wrong
ownership boundary.

Configured connectors, catalogue admission, enrollment, and live health are different
facts. Static protected tools can be visible before discovery under
[ADR 0310](0310-lazy-toolhive-static-tools.md). Successful enrollment freezes a catalogue;
it does not prove current reachability or credential validity. Observing enrollment
through the existing control helper can itself trigger discovery, so that helper is
not a read-only status API.

## Decision

Adopt a separate owner-authorized `ListSessionMcpConnectors` API and independent
`mcp_connector_status` capability, rendered through the existing `/mcp` command.
Keep the old `mcp` capability and all direct provider operations unchanged.
Require `OwnershipEnforced=true` and a verified principal for the new capability
and API; require a nonnil matching persisted owner for session disclosure. Transport
authentication alone is insufficient: the existing ownership helper deliberately
permits access when ownership enforcement is disabled. Keep that compatibility
behavior unchanged for other operations. Extend the descriptor-based session-affinity
matrix and HTTP session-route tests for the new operation.

These are accepted disclosure prerequisites. Existing transport authentication applies; no separate listener toggle exists.

Expose bounded connector display names, per-connector catalogue states and counts,
and aggregate enrollment state. Inspect existing process-local broker state under
its exact persisted binding, without attaching, rebinding, enrolling, discovering,
refreshing tokens, or persisting anything. Lost state is explicitly unavailable;
persisted tool names are not evidence of live enrollment or connector provenance.
No upstream health claim, probe, credential-state projection, or terminal-attempt
history belongs in this first version.

Define catalogue/count/completion as broker-local publication, not installed engine
state or durable session authority. Broker `completedEnrollment` is published before
server engine rebuild and aggregate Save. Either later step may fail without erasing
broker publication; the inventory reports that publication and never repairs the
session or implies those steps succeeded. Similarly, expired/terminal broker state
can coexist with a persisted pending enrollment that still gates prompts. Inspection
must not settle that gate. The panel explicitly says session installation, persistence
and prompt readiness are not verified. Offline tests inject post-discovery build/Save
failures and expired/terminal attempts with still-pending aggregates.

This ADR records the approved implementation boundary. The exact fields, status
vocabulary, disclosure policy and bounds are in the
[acceptance plan](../acceptance/broker-mcp-status.md); broker and direct MCP are
mutually exclusive supported compositions, so the broker panel never exposes
resources, prompts, or groups.

## Consequences

Broker-only Kubernetes users can inspect their connector catalogue without changing
deployment modes. Existing clients retain truthful resource/prompt capabilities.
The new operation requires end-to-end ownership, affinity, listener gating and
redaction tests, plus a pure broker snapshot interface separate from enrollment
controls. The UI must explain that no active enrollment is not proof that lazy
OAuth is unavailable, and that catalogue completion is not a health check.

Process-local inspection does not improve broker restart recovery or replica routing.
A restarted pod cannot claim a session is connected from persisted tool names, and
an established session must not receive an unconditional pre-prompt reconnect hint.
Count-only rows intentionally omit detailed tools, auth configuration and error text.

## See also

- [Broker MCP status acceptance plan](../acceptance/broker-mcp-status.md)
- [Architecture](../architecture.md)
- [Human-reviewed development contracts](0306-human-reviewed-development-contracts.md)
- [ToolHive-owned broker OAuth](0311-per-upstream-mcp-broker-oauth-grants.md)
