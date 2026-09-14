# ADR 0335 — Idle-session MCP broker workspace refresh

- Status: Proposed
- Date: 2026-09-14
- Scope: broker workspace-enrollment timing, catalogue replacement, and same-session restart recovery in mecak8s
- Supersedes: ADR 0310's pre-prompt-only workspace-enrollment timing and its static-wrapper retention during an explicit workspace replacement; ADR 0322's pre-prompt-only broker setup extension and, for an explicit replacement only, its rule that completed broker publication survives a later engine-build or session-persistence failure
- Superseded by: None

## Context

`ConnectWorkspaceServices` currently accepts a broker workspace-enrollment only before the first prompt. The aggregate requires an idle session with an empty conversation, and completion atomically replaces its admitted tool names. That turns a transient enrollment failure or a mecak8s restart into a conversation-reset problem: an established session cannot explicitly restore its protected workspace tools.

The restriction is not required to preserve ToolHive's ownership of upstream OAuth. ToolHive must remain the sole owner of upstream presentation, callback state, PKCE/code exchange, grant/token custody, refresh, and backend-specific bearer injection. Mecatl owns the caller-authorized session boundary and admits only one verified, complete broker catalogue. ADR 0310's static declared tools and lazy per-tool authorization remain supported for initial session admission; during an explicit workspace replacement this decision withdraws those static wrappers with every other broker wrapper.

The bundled broker is presently process-local. A persisted external binding cannot match after a pod restart because its runtime prefix and generation are in-memory. Broker-mode mecak8s is already schema-constrained to one replica. This decision deliberately improves explicit same-session recovery in that topology; it does not create remote broker ownership, durable broker runtime state, or multi-replica routing.

## Decision

Make `ConnectWorkspaceServices` an explicit **idle-session refresh** control rather than a pre-prompt-only control.

1. An authenticated, matching owner may begin, observe, retry, or cancel one whole-bundle workspace refresh after any completed conversation turn. The control first uses the existing completed-to-idle reopen transition, preserving conversation history, and then admits refresh only from idle. It is rejected while an agent run, permission/authorization pause, or another workspace refresh is active. Retry holds the existing run-entry and lease serialization continuously across cancellation of the exact old operation and beginning its replacement, so a prompt cannot enter between them.
2. Refresh remains one opaque ToolHive operation over every configured protected connector. The root-internal broker attachment gains one narrow reset operation: it retires the completed catalogue generation and permits a new whole-bundle enrollment while preserving the logical ToolHive session. It does not use durable broker-session deletion or broad server session cleanup. Mecatl makes no grant-reuse guarantee; ToolHive may reuse a valid held grant only where its configured storage and lifecycle permit it, otherwise it may require consent. Mecatl neither selects connectors nor copies/interprets upstream grants.
3. Starting refresh is destructive. Under session and broker serialization, mecatl withdraws the broker-bearing engine and attachment, resets the broker's completed catalogue, and then begins replacement enrollment. Conversation, placement, and unrelated session state remain intact. Persisted capability names alone are inert metadata: executable broker authority requires their conjunction with the exact live binding and installed wrappers. Static declared protected-tool wrappers from ADR 0310 are withdrawn with discovered broker wrappers until replacement succeeds.
4. Completion requires authenticated discovery of the complete configured bundle. “Connected” is published only after replacement engine construction, broker attachment commit, aggregate authority/binding update, and snapshot persistence succeed. This is an ordered fail-closed publication protocol, not an atomic transaction across broker memory, engine registration, and `SessionStore`. For an explicit replacement, this supersedes ADR 0322's rule that a completed broker publication survives a later engine-build or persistence failure: compensation resets that replacement publication as well as removing its executable wrappers. A failure or restart at any boundary leaves no executable replacement or stale broker wrapper and permits a later explicit retry. Initial pre-prompt enrollment and read-only inventory retain ADR 0322's existing publication semantics. No partial/additive catalogue, candidate generation, or rollback generation is introduced.
5. After broker-process loss, an established session continues through ordinary run rehydration without broker tools. That path does not attach, rebind, discover, refresh credentials, or initiate browser consent; it treats persisted broker names as non-executable without a matching live binding and wrappers. Only an explicit `/tools-connect` establishes a fresh attachment and completes the whole-bundle refresh.
6. The existing workspace-enrollment pending record, exact opaque reference, external binding, authority fields, and snapshot paths are reused. No refresh-specific persisted state, status cache, background worker, connector selection, or model/history event is added.
7. The supported deployment topology remains one mecak8s replica for broker mode. No affinity, distributed runtime, or replica takeover mechanism is introduced by this change.
8. Tool definitions supplied to the model on a later request are the sole model-facing indication of a successful refresh. Mecatl adds no synthetic conversation message or system-prompt notification.

## Consequences

An established conversation can recover its protected tools without `/clear`, while an external authorization operation cannot race a tool dispatch or change the catalogue in the middle of a turn. On a restart, the session remains available for non-broker work but protected tools remain absent until an explicitly requested refresh succeeds.

The existing gRPC, HTTP, and mecatui workspace-enrollment controls retain their names and response shape. Their eligibility changes from fresh-empty session to owner-authorized stable idle session. `/tools-connect` and `/mcp` offer the same explicit whole-bundle action for fresh/not-started, established/connected, terminal-after-failure, and process-lost/unavailable inventory states. Running or awaiting sessions and busy/pending controls remain ineligible; a pending refresh exposes only observe/cancel. Inventory availability and persisted names neither prove connectivity nor suppress an explicit recovery action. The control's aggregate, presentation-safe response continues to exclude connector identifiers, OAuth state, endpoints, codes, and tokens.

Starting refresh intentionally withdraws currently active broker wrappers without tearing down the conversation, placement, or unrelated session state. While replacement is pending, prompts remain blocked under the one-control-at-a-time rule; the user may inspect/cancel the control. After a terminal failure, the session is available for non-broker work. Only successful complete discovery and the ordered publication sequence install a new broker catalogue. Rehydration after process loss follows the same non-broker state until an explicit refresh commits a fresh binding and catalogue.

## Rejected alternatives

- **Keep pre-prompt-only enrollment.** It requires a new conversation to recover from a routine broker restart and contradicts the intended ongoing-workspace experience.
- **Allow partial connector success or per-connector selection.** This would replace one exact admitted bundle with a mixed-generation authority model and widen the UI/control contract.
- **Refresh or reauthorize automatically on an ordinary prompt.** It could initiate external OAuth unexpectedly and hides a material availability/authorization transition.
- **Keep stale broker tools advertised after process loss.** Persisted names do not prove that an executable attachment, valid runtime state, or current schemas exist.
- **Introduce incumbent/candidate generations or a cross-resource transaction.** The process-local broker and current `SessionStore` cannot provide one atomic commit across runtime and persistence. An ordered fail-closed sequence gives the required safety without a second catalogue state machine or token-fenced storage redesign.
- **Delete and recreate the logical broker session for refresh.** Durable deletion has broader grant and replay consequences than catalogue replacement requires. A narrow completed-enrollment reset preserves ToolHive's custody boundary without promising grant reuse.
- **Add a model-visible refresh notice.** Current tool definitions are sufficient; a synthetic turn or system state adds prompt and persistence complexity without enforcement value.
- **Solve multi-replica broker routing here.** Durable/remote broker ownership is a separate architectural change.

## See also

- [ADR 0310 — Lazy ToolHive authorization for statically declared protected tools](0310-lazy-toolhive-static-tools.md)
- [ADR 0311 — ToolHive-owned multi-upstream MCP broker OAuth](0311-per-upstream-mcp-broker-oauth-grants.md)
- [ADR 0322 — Session-owned broker connector inspection](0322-broker-mcp-status.md)
- [Idle-session MCP broker workspace refresh acceptance plan](../acceptance/idle-session-broker-workspace-refresh.md)
- [Architecture guide](../architecture.md)
