# ADR 0350 — Durable workspace-enrollment broker authority provenance

- Status: Proposed
- Date: 2026-09-21
- Scope: session authority replacement, persistence, collision handling, and enrollment failure presentation for ToolHive workspace enrollment
- Supersedes: ADR 0335 Decision 6 only
- Superseded by: None

## Context

Workspace enrollment installs an authenticated, session-bound broker catalogue alongside the ordinary composed catalog. The existing completion path replaces the aggregate's entire durable tool capability set with the enrolled catalogue's names. That removes unrelated core, delegation, memory, skill, global-MCP, client-MCP, MCP-resource, and latent member capabilities from the session even when they remain available in the rebuilt runtime.

A replacement cannot instead intersect authority with final catalog registration names. The durable capability set contains opaque MCP-resource capabilities and latent member capabilities that are intentionally not catalog registrations. It also cannot infer the prior broker bundle from the process-local attachment after restart: snapshots contain an undifferentiated authority set, while the broker binding carries no completed-catalogue membership.

MCP remote tools conventionally use `mcp__<server>__<tool>` names, making a broker/non-broker collision unusual. It is nevertheless possible when two sources expose the same server and remote-tool names, and the catalog's exact registration key selects the executable implementation. Keeping one source's authority after silently switching that key to another source would make a durable authorization refer to a different implementation.

## Decision

1. The session aggregate persists one private, bounded, exact set of registration keys dynamically installed by the most recent successful workspace enrollment, together with an explicit presence bit. Presence with an empty set is a valid completed enrollment; absence means an unrepaired legacy snapshot and is not equivalent to an empty set. The ledger is distinct from the general derived capability set and carries no endpoints, connector metadata, OAuth state, credentials, or tool schemas. New sessions create a present empty ledger. Snapshot restore, clone, and validation preserve that distinction and reject malformed or oversized present values.
2. A successful enrollment uses the registration outcomes from the one successfully composed catalog. It does not re-read broker tools or call `Tool.Spec()` while calculating authority. It atomically replaces the prior dynamic-broker key set and computes the durable capability set as: all prior capabilities except the prior dynamic-broker keys, plus the newly registered dynamic-broker keys. All non-broker capabilities therefore survive unchanged, including opaque and latent capabilities; deliberately attenuated capabilities remain absent.
3. The private broker-key set and its presence bit are snapshotted, restored, and copied by ordinary session successors. Engine-build, aggregate-completion, attachment-commit, and persistence failures leave the prior authority and broker-key ledger unchanged. Resume and fork retain the successfully committed values.
4. Before publication, enrollment rejects a conflict between any key in the incoming broker bundle **or the prior recorded broker bundle** and a registration key owned by the current non-broker composition. It publishes no partial protected catalogue or candidate wrapper and changes neither candidate authority nor candidate provenance. It does persist the ordinary terminal cleanup that clears the pending enrollment; on a destructive refresh, the already-withdrawn broker runtime remains unavailable while the prior durable authority and ledger stay unchanged. The presentation-safe terminal failure identifies the conflicting registration key but never exposes OAuth URLs, endpoints, callback state, credentials, tokens, or raw broker configuration. A retry after configuration correction starts a new whole-bundle enrollment.
5. Existing snapshots without this private provenance are not repaired or inferred. Their owner cannot begin workspace enrollment: the server rejects the control before destructive reset, discovery, browser consent, attachment mutation, or runtime mutation, directs recovery to a fresh session, and leaves the legacy session subject to its persisted authority. A fresh session receives the new behavior.
6. This supersedes only ADR 0335's “no refresh-specific persisted state” clause. Its whole-bundle, explicit-control, ToolHive-custody, ordered publication, and no-partial-catalogue decisions remain unchanged.

## Consequences

New enrollment and refresh operations no longer clobber unrelated session authority, while broker refresh removes stale known broker keys exactly. The presence-bearing persistence field is a narrow provenance ledger, not a general catalog-origin model. It adds snapshot and successor coverage but no RPC, configuration, command, event, or model-facing surface.

A rare conflicting deployment fails visibly rather than selecting a precedence rule or silently omitting one protected tool. The owning user sees a terminal setup failure in `/mcp` and may retry `/tools-connect` after the operator removes or renames the conflicting MCP configuration. Pending prompts are not resumed because enrollment did not succeed.

Old affected snapshots remain narrowed by design and cannot begin workspace enrollment. This avoids an unsafe compatibility heuristic that could widen intentional attenuation, at the cost of requiring a fresh session for corrected authority semantics.

## Rejected alternatives

- **Replace authority with the final catalog names.** This deletes valid opaque MCP-resource and latent member capabilities and widens deliberately attenuated sessions.
- **Union the old authority with the enrolled catalogue.** This retains stale broker keys and can authorize a later unrelated registration with a stale broker capability.
- **Persist origin metadata for every catalog entry.** The durable need is limited to the dynamically installed broker bundle; broad catalog provenance adds an unnecessary engine-wide concept.
- **Omit only conflicting broker tools.** Workspace enrollment is a complete authenticated bundle; partial success would make an apparently successful connection incomplete and violate the no-partial-catalogue contract.
- **Choose broker or non-broker precedence on collisions.** Exact names are durable capabilities. Redirecting one name to a different implementation is an authority change, so collision fails closed.
- **Repair legacy snapshots heuristically.** Their current tool set cannot distinguish accidental loss from deliberate attenuation.

## See also

- [ADR 0234 — Derived delegation authority behind an evaluator port](./0234-authority-evaluator-port.md)
- [ADR 0335 — Idle-session MCP broker workspace refresh](./0335-idle-session-broker-workspace-refresh.md)
- [Workspace enrollment authority acceptance plan](../acceptance/workspace-enrollment-authority.md)
