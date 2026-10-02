# ADR 0343 - Group explicit configuration replacements as session generations

- Status: Proposed
- Date: 2026-09-15
- Scope: main-session replacement intent, two-stage durable predecessor activation, bounded session inventory, and compatibility discovery
- Supersedes: ADR 0065 only where carryover lineage was out of scope, ADR 0068 and ADR 0071 only where replacement sessions lacked durable predecessor identity, and ADR 0217 Decision 1 only where model and effort replacements were lineage-free peers
- Superseded by: None

## Context

Changing a model or reasoning effort creates a new main session because provider and model
configuration stays fixed for each physical session. The replacement copies the conversation,
so the operator experiences one continuing chat. Session inventory lists both physical sessions
as unrelated chats.

Configuration overrides do not prove replacement intent. An operator can make an ordinary,
independent conversation fork while also selecting another model, provider, effort, or worktree.
Conversely, a replacement can resolve to the same configuration as its source. The request must
state the distinction.

The existing `SessionRelationship` describes trusted producer relationships for scheduled,
delegated, and debug sessions. Main sessions deliberately carry no such relationship. Durable
session incarnations already distinguish an exact session lifetime from later reuse of the same
session ID.

Grouping must happen before inventory counting and pagination. Client-side deduplication would
produce short pages, incorrect totals, and unstable continuation. Indexed stores cannot load or
scan every snapshot on each page, and opaque remote drivers need an explicit compatibility mode.
The design must preserve every non-main inventory row and fail visibly under corrupt graph state.

A target snapshot can become authoritative before a failed `Create` or `Save` response reaches
the server. Conversely, persisting a replacement edge before server-side registration, broker,
reattachment, or equivalent readiness succeeds would hide a still-usable source in favor of an
unready target. Persistence response success is therefore neither a sufficient readiness marker
nor an unambiguous account of storage state.

## Decision

Treat a history-carrying fork as a replacement only when `ForkSessionRequest.replaces_source` is
true. Mecatui sets this field for model and reasoning-effort switches. Ordinary forks omit it or
send false and remain independent, including forks with provider, model, effort, title, or
worktree overrides. Clear and worktree-only successors also remain independent. The resolved
configuration never classifies replacement intent.

A replacement has one optional exact predecessor reference containing the source session ID and
incarnation. The reference is exact lifetime metadata, not a credential or capability. It has no
durable root, generation number, readiness field, or `SessionGeneration` value. A session without
a valid predecessor is independent for inventory purposes. Legacy, partial, and structurally
malformed predecessor metadata becomes no edge without preventing transcript load.

Use the predecessor edge itself as the one-time activation marker:

1. Create and persist the target with no predecessor. Source and target are both visible and
   independently addressable.
2. Complete required server-side target registration, broker, reattachment, and equivalent
   readiness work. Preparation that already occurs before first persistence may remain there,
   but the contract does not rely on that ordering.
3. Attach the source's exact ID and incarnation once, then persist the target snapshot and the
   store's generation projection atomically. Only this persistence activates replacement
   visibility.

`Session.SetPredecessor(source)` performs that one-time activation transition whenever the target
has no predecessor, including after initial persistence. It accepts only valid main-session
aggregates with the same owner, derives the reference from `source.ID` and
`source.Incarnation()`, and never accepts caller-supplied reference fields. It rejects replacement
or removal after an edge is attached; it does not require a pristine or never-saved target.
`RestorePredecessor` accepts raw persisted metadata only at the trusted snapshot/storage
rehydration boundary. A crash or failure before activation leaves both physical rows visible. A
failure after proven activation, including response delivery, client handoff, or source closure,
leaves the server-ready target as the head; the operation does not roll the edge back.

Public transports accept only `source_session_id` plus replacement intent; no caller supplies a
predecessor incarnation. Both gRPC and HTTP converge on the existing `ForkSessionSuccessor`
service path. That path owner-authorizes the source, reloads and reauthorizes it under the source
lock, and gives the loaded aggregate to `SetPredecessor`; the target inherits that source's owner.
Inventory then filters ownership before evaluating edges. Knowledge of another session's ID and
incarnation therefore grants no access and cannot make a cross-owner edge hide either owner's row.
When ownership enforcement is disabled, this feature preserves the existing single-tenant
compatibility posture rather than claiming independent authentication.

Treat every `Create` or `Save` error as potentially occurring after the snapshot became
authoritative. After an ambiguous initial persistence error, probe the exact target ID and
incarnation and continue only if the authoritative row has that identity and no predecessor.
After an ambiguous activation error, probe the same target and treat activation as committed only
if the row has the expected exact predecessor. A missing, colliding, or mismatched row fails
closed. If storage is unreadable, stop the attempt without another mutation; once storage is
readable, its authoritative predecessor state determines whether both rows remain visible or the
server-ready target is the head. The same attempt must not blindly mint another target. This
resolves one exact attempt only; cross-request retry idempotency remains outside this decision.

Generation-aware inventory filters ownership first and groups only rows with valid main-session
metadata. It passes every other owner-visible row through unchanged and marks it unreplaced. This
includes scheduled, delegated, debug, unknown legacy, malformed-kind, and future unrecognized
rows.

For valid main sessions, a retained row is replaced only when another retained, owner-visible,
valid main session names its exact ID and incarnation as predecessor through an activated edge.
Malformed, dangling, incarnation-mismatched, cross-owner, and cross-kind edges hide no row.
Sibling successors are valid: they hide their shared predecessor and remain multiple visible
heads. If otherwise valid edges form a cycle, inventory reveals every row in the affected
undirected connected component, including attached tails and branches. An inability to derive or
corruption in a component likewise leaves every affected row unreplaced. Unrelated components
still derive heads normally.

Physical deletion never cascades or mutates another snapshot. Deleting a sole head can reveal its
predecessor. Deleting an intermediate row breaks adjacent retained edges as applicable; for
`A -> B -> C`, deleting B leaves A and C visible because C's edge is dangling.

A generation-capable store maintains a private exact-predecessor and reverse-edge projection plus
derived visibility. `Save`, `Delete`, and generation-relevant metadata mutations update snapshot
metadata, forward and reverse edges, derived `is_replaced` membership, ordering and ownership
indexes, and the existing pager mutation generation atomically, or fail visible. Edge addition or
removal recomputes the affected undirected component sufficiently to detect cycles. This is a
behavioral contract, not a prescribed backend data structure.

Generation-aware `Page` reads the already-derived bounded projection; it does not scan or load
every snapshot per page. Scan/reference adapters may derive the projection from their bounded
metadata index, never transcripts. Remote drivers own the same boundedness and atomic/fail-visible
guarantee when they advertise generation support. Ownership filtering and derived visibility
precede total count, ordering, cursor construction, and page formation.

Every `Save`, `Delete`, or metadata mutation capable of changing membership, ordering, ownership,
edge validity, component validity, or `is_replaced` advances the pager's existing mutation
generation in the same atomic update. A cursor from the previous generation returns
`ErrSessionMetadataCursorRestart`. Cursor scope also includes the selected `include_replaced`
view, and changing that view requires restart.

Generation-aware inventory returns derived heads plus every pass-through row by default. It
returns every retained physical row when public `include_replaced` is true and marks only hidden
valid main rows with `is_replaced`. The public request remains an ordinary bool: absent and false
both mean head-only on a capable server. Thus an old public client against a new capable server
intentionally receives head-only inventory. That additive behavioral change is the purpose of the
feature; compatibility does not promise byte-identical list results.

The existing `SessionMetadataPager` remains unchanged. A new optional
`SessionGenerationMetadataPager` returns the existing `SessionDiscoveryMeta` projection plus
`IsReplaced`; it exposes no self reference, predecessor, root, or graph. A server advertises the
public `session_generation_inventory` feature only when its active store supports this optional
pager. A remote store depends on the driver's separate `generation_inventory` capability.

Driver Save supplies self incarnation and optional predecessor ID/incarnation. Driver paging adds
presence-aware `optional bool include_replaced`: absent means legacy physical inventory, present
false means head-only, and present true means all physical rows. A new server always sends the
field explicitly when invoking a generation-capable driver. This makes all pairings coherent:

- old server -> new driver omits the field and receives legacy physical inventory;
- new server -> old or non-capable driver uses legacy paging, exposes physical inventory, and
  omits the public generation feature;
- new server -> new capable driver sends false for heads or true for all physical rows;
- new client -> old server has new request fields ignored and receives physical inventory;
- old public client -> new capable server receives the intended head-only default and ignores
  `is_replaced`; its omitted `replaces_source` also keeps its own forks independent.

Deployments with only the legacy pager return physical inventory for either public request value.
Deployments with neither pager retain the existing unsupported error. Older binaries can discard
predecessor metadata when rewriting a snapshot; a newer binary then exposes that row
independently.

Generation grouping creates no shared chat state. Each physical session retains its own
lifecycle, title, token usage, event log, permissions, placement, exact-ID access, rename, and
deletion.

## Consequences

Default session inventory matches the operator's continuing-chat model only after the initiating
path explicitly declares replacement intent and the target is server-ready. Independent forks do
not group merely because they carry configuration overrides.

Predecessor-only metadata avoids durable root identity and root-consistency failure modes. The
same edge doubles as activation, so no readiness field or mutable head record is needed. The
cost is two durable writes for replacement creation and a visible predecessor-free target when
preparation fails.

Ambiguous storage responses require exact-ID/incarnation probes. Matching predecessor state lets
the same attempt continue safely; missing or mismatched state fails closed. This prevents blind
duplicate creation within an attempt but does not provide an idempotency protocol across retries.

The storage layer gains an optional paging capability, private forward/reverse-edge metadata, and
derived visibility. Writes may pay recomputation cost for an affected connected component so page
reads stay bounded. Cycles, corruption, and derivation failures expose rows rather than hiding
them. The public and engine page results remain bounded and do not expose the graph.

All inventory-affecting writes invalidate outstanding cursors through the existing pager mutation
generation. Clients may restart more often, but they never continue across changed membership,
ordering, ownership, edge validity, or visibility.

Mixed-version wire compatibility is additive but list results are deliberately not byte-identical
for old public clients against a new capable server: omission means the new head-only default.
Presence awareness exists only on the driver projection, where omission must preserve an old
server's expectation of physical inventory.

Concurrent activated replacements and retries after an exact attempt is abandoned can produce
more than one visible head. Preventing siblings would require conditional head mutation and a
cross-request idempotency protocol, which are outside this decision.

A failure after proven activation can leave a prepared target that the initiating client did not
receive. Inventory and exact-ID access make it recoverable; automatic handoff recovery remains
separate work.

Mecatui receives head-only inventory automatically from a capable server. It does not add a
historical-generations view in this increment. Administrative clients can request all physical
rows through gRPC or HTTP.

## See also

- [Acceptance plan](../acceptance/session-configuration-generations.md)
- [ADR 0065](./0065-conversation-fork.md) - independent conversation forks
- [ADR 0068](./0068-effort-change-via-fork.md) - reasoning-effort replacement mechanics
- [ADR 0071](./0071-seamless-model-switch.md) - model-switch carryover
- [ADR 0217](./0217-session-discovery-continuation.md) - session taxonomy and inventory
- [ADR 0291](./0291-server-owned-session-placement.md) - current ForkSession and successor surface
- [ADR 0258](./0258-cryptographic-session-incarnations.md) - exact session lifetimes
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md) - feature negotiation
