# Debugger scan continuation - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — extend the existing target-bound debugger with the existing cursor port; preserve storage ownership, event formats, and authorization decisions.
**Decision record:** None — this proposal composes the established cursor and debugger-handle contracts without adding a backend capability, durable key, retained registry, or new authority source.
**Phase:** Stored-session diagnosis
**Status:** proposed, 2026-09-25. Delivery scope accepted by the operator; awaiting the Split Plan / Interface approval checkpoint.
**Delivery:** Split. The operator explicitly authorized a stacked implementation PR before plan merge on 2026-09-25; retain separate plan/interface and implementation reviews and human merge authority.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1914](https://github.com/stacklok/mecatl/issues/1914).
**Plan PR:** Not opened; local drafting only.
**Approved baseline:** Not approved.

A debugger inspecting a long retained log can continue beyond the first 10,000
records while retaining the 10,000-record projection budget. It can page rows
within a fixed scan window separately from advancing to the next window, and
distinguish a scan limit from a read failure or unknown retention.

The implementation baseline inspected for this draft is
`4c28406fdd15a68fa089625b8ad9daaffa900db2`. Its `activity` view already supports
offsets beyond 10,000 and is unchanged. The affected paths are
`status.lifetime_event_log`, `performance`, `network`, `delegation`, `manifest`,
and history catalog discovery. This document specifies proposed behavior.
The operator authorized implementation stacked on this plan's exact commit;
that exception does not claim the plan has merged or the feature has shipped.

## Human decisions

- [x] Confirm the issue's delivery scope. The operator accepted the research-backed recommendation with "OK, let's do that and kick off the fix via agents". — Decision: deliver resumable retained-event evidence and archive access, preserve existing initial-prefix and short-log replay capabilities, use explicitly window-local aggregates and non-additive observed run counts, and leave a new durable cumulative projection or arbitrary-length conversation-replay service outside this issue.

## Design basis

Primary guidance checked on 2026-09-25:

| Source | Applied guidance | Limit of the analogy |
|---|---|---|
| [Google AIP-158: Pagination](https://google.aip.dev/158) | Use a single opaque continuation token, bound page size, preserve query arguments, authorize every request, and distinguish a continuation from EOF even when a page has no matching rows. | The tool retains its existing `limit` input and JSON output rather than copying Google's protobuf conventions. AIP-158 permits an extra empty terminal page; it does not require avoiding lookahead. |
| [Elasticsearch pagination](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/paginate-search-results) | Resume from a stable position rather than deep offsets; preserve ordering and reject or stabilize changed projection inputs. | Mecatl reuses append-order, generation-bound cursors and validates a fixed interval. It does not introduce Elasticsearch or a server-retained point-in-time resource. |
| [Azure Event Sourcing pattern](https://learn.microsoft.com/en-us/azure/architecture/patterns/event-sourcing) | Full reconstruction requires the preceding ordered history or a suitable snapshot plus its suffix. Query projections need explicit coverage and duplicate-processing discipline. | Mecatl's observational EventLog is not an authoritative complete event-sourced system of record. A materialized projection/checkpoint is additional architecture, not a prerequisite for pagination. |

Use one model-visible cursor and keep row-versus-window continuation internal.
Preserve the existing one-record EOF probe at the 10,000-event boundary; this
avoids a replay regression without counting the probe as projected evidence.
Keep legacy initial evidence with an explicit unsupported-continuation result.
Window-local counters are the scoped design recommendation, not a universal
standard for lifetime queries. No claim of exact cumulative run counts or
unbounded historical reconstruction is added by this change.

## Interface contract

The contract below records the accepted scoped design. Under the operator's
explicit stacked-delivery exception, implementation starts from the recorded
plan commit before plan merge. Any substantive contract drift still stops work
for human authorization.

- **gRPC / protobuf:** None — `InspectSession` remains an ordinary debugger tool call/result. Public session replay and watch transports are unchanged; debugger-only network and manifest content stays off those transports.
- **Exported Go APIs / interfaces:** Add `port.ErrCursorUnsupported` as an `errors.New("port: event-log cursors are not supported by this backend")` sentinel for the existing unsupported-capability outcome. Keep the internal driver's `grpcdriver.ErrDriverCursorUnsupported` name as an alias to that sentinel so existing `errors.Is` callers retain identity. The debugger matches the port error without importing another host adapter. This is an additive engine API change requiring the API snapshot and classified changelog entry; `CursorEventLog`, `EventLog`, and `sessiondebug.NewBound` signatures remain unchanged.
- **Tool schemas:** Add one optional string input `cursor` to `InspectSession`, opaque and at most 16 KiB encoded, accepted only for the affected event paths listed above. A supplied cursor requires `offset` to be zero or omitted and is incompatible with `history_handle`. Add one optional `next_cursor` output at the result root. Its internal claim selects the next row page or event window; the model never chooses that phase. Other view names and existing inputs remain available. `limit` retains its existing ceilings; performance gains row paging up to its existing 50-turn ceiling, and history catalog pages use the existing `maxHistoryRows` value of 20. Add the coverage metadata and history selection specified below.
- **CLI / config:** None — retain the 10,000-record projection budget, existing row ceilings, and 64 KiB fenced response bound. Permit one non-projected EOF probe and one reread of an already-covered endpoint for publication validation; account for these separately from coverage. No scan-depth flag, retention change, or backend-selection change.
- **Events / persistence:** None — no event changes, cursor registry, stored aggregate, durable key, or data migration. New opaque handles appear only in ordinary debugger tool responses/transcripts, which already persist tool evidence. Cursor gap records are read through the existing port and never converted into session events.
- **Security / authority:** Reuse the incarnation-derived authenticated, encrypted handle pattern in `views.go`, with a separate key domain and token prefix for debugger continuation. Bind the token to root fingerprint, owner scope, selected scope fingerprint, view, token purpose, and exact backend cursor(s). Root/descendant authorization is checked before storage access and again before returning evidence. A token is a position selector, never independent authority. No raw backend cursor, scope ID, generation, key material, or gap reason is projected to the model.
- **Compatibility / migration:** Existing calls without cursors still start at the beginning. Existing nonzero output offsets remain supported only for that initial window, with no implicit advance to a later raw segment. Existing snapshot, transcript, related, and activity offsets are unchanged. All history-source handles become versioned encrypted selectors; old unversioned handles for snapshot, archive, and replay sources return the existing refresh/stale error rather than rebinding. Availability of partial legacy-backend evidence is preserved under the proposed decision above. The unsupported sentinel follows the additive engine compatibility policy; no storage migration is required.

### Event windows and row pages

For affected views, add `event_window` to the result; for status, place it inside
`lifetime_event_log`. Its fields are:

| Field | Type | Meaning |
|---|---|---|
| `id` | opaque string | Stable identity for this root/scope/view and exact scanned interval; row pages repeat it. Not a backend cursor. Omitted when no cursor-backed window exists. |
| `continuation_supported` | boolean | The configured log supports cursor reads, including remote capability negotiation; no inference from session-store type alone. |
| `record_limit` | integer | Always 10000. |
| `records_scanned` | integer | Processed event and gap records in this window; endpoint revalidation is not new coverage. |
| `scanned_events` | integer | Processed event records only. |
| `gap_records` | integer or null | Gap records within a cursor-backed window, without raw reason; null when the legacy read cannot observe gaps. |
| `scan_complete` | boolean | The original window scan established its observed tail without cancellation or read failure, including an EOF probe when the budget was full. It does not assert future quiescence or complete retention. |
| `retention_complete` | boolean | False; the current port does not certify complete lifetime retention. |
| `stop_reason` | string | One of `end_of_log`, `scan_limit`, `read_error`, `not_configured`. |
| `has_more_records` | boolean | A successful probe observed another record after this window. It is scan metadata, not a second continuation input. |

`row_page` is present on performance, network, delegation, manifest, and history
catalog results. It contains `limit`, `returned`, `projection_complete`, and
`has_more_rows`. The single result-root `next_cursor` resumes remaining rows in
the same window first. Once those rows are exhausted it advances to the next
raw window if `has_more_records` is true. It is absent only when both forms of
continuation are exhausted, or the documented error/capability path prevents
continuation. An empty matching page still carries `next_cursor` if later raw
records exist. No second row or event cursor is exposed.

An initial or event-window continuation uses
`ReadOptions{Limit:10001, Follow:false}`: process at most 10,000 records and use
at most one additional record solely to establish continuation. The probe is
not counted or projected. Resume after the last processed record so the probe
becomes the next window's first record. Exactly 10,000 records followed by EOF
remain a complete initial window and eligible for full retained replay. This
preserves the existing `readEvents` boundary behavior. Gaps consume the record
budget, and actual yielded-read counts distinguish projection, probe, and
endpoint validation work.

A row-continuation call rereads only its recorded interval and keeps the same
interval ID, counters, and authenticated scan-time termination metadata, even
if new events have since been appended. It must encounter the exact sealed end
cursor within the bound; early EOF or a different end is stale. The token binds
both endpoint positions and the original EOF/scan-limit disposition. The row
ordinal refers to projected rows, including multi-row delegation events.

For delegation, row tokens also bind a digest of the projection basis: the
lineage graph and the snapshot facts used by `parentResults` and parent-call
matching. First-window history catalog row tokens bind their snapshot-derived
source metadata. Recompute and compare that digest before and after projection;
ordinary compaction, result updates, or child retention changes that alter the
basis invalidate the page with the stale-continuation error. Do not silently
renumber rows when authorization or enrichment inputs change. Event-window
cursors advance raw positions independently; their next call uses current
per-call lineage and snapshot evidence.

After projection, verify the last processed record still belongs to the same
log generation by a one-record endpoint reread from its immediately preceding
cursor, checking exact endpoint equality. This is at most one extra validation
read of an already-covered record, not an enlarged scan window. The preceding
and endpoint positions are authenticated in tokens. For an empty continuation,
validate the input cursor's predecessor/endpoint instead. An initially empty
scan creates no token because it has no generation-bound position. This is a
publication checkpoint, not a guarantee against mutation after that check.
Require an uncancelled context and final target/scope authorization as well.

Every successful page with remaining output advances to a later row ordinal.
An individually unrepresentable row produces an explicit omission entry with
`index` and closed `reason:"response_bound"`, marks projection incomplete, and
advances. Ordinary page-size truncation does not mark an individual row omitted.
Reserve space for continuation/omission metadata inside the 64 KiB bound.

For cursor-capable paths, existing response fields follow these exact rules:

- `offset` is the current window's output ordinal, whether selected by an initial
  offset or a row cursor. `limit` is the applied row ceiling.
- `next_offset` is emitted only on calls without `cursor`, when more rows exist
  within that first window. Network no longer gates it on scan EOF. Cursor-input
  calls omit it and use the result-root `next_cursor` exclusively.
- Where present, `truncated` is true if scanning is unfinished, output rows
  remain, or a row/field is omitted by projection limits.
- Activity and performance keep `complete:false`. Network `complete` is true
  only when the window reached EOF, no output rows remain, no invalid attempts
  or gap records were omitted, and projection is complete. It describes this
  window's remaining page, never earlier unrequested windows.
- Existing `scan_complete` and `scanned_events` mirror their window fields.
  Existing `projection_complete` mirrors `row_page.projection_complete` for
  affected catalog/row views. A full row page alone is not an omitted row.
- Existing top-level lineage-based `retention_complete` on delegation is
  unchanged; the nested event-window retention flag remains independently false.
- Successful scan exhaustion uses `stop_reason:"scan_limit"`, not an `error`.
  Existing lineage errors remain distinct; storage failure follows the failure
  rules below. Snapshot-only response fields keep their existing semantics.

The model-facing instructions say: pass the result-root `next_cursor` as
`cursor` on the next call, retaining the same view and scope handle. Only `limit`
may change; the server chooses row-versus-window continuation internally. Stop
when no continuation is returned; an empty row page alone is not EOF. Never
fabricate or decode tokens. Both the tool specification and the real debug
factory's system prompt teach this workflow. All required continuation values
are returned in the model-visible tool result.

### Coverage and aggregates

Add `aggregate_scope:"event_window"` to lifetime and performance results and
`counts_scope:"event_window"` to network results. Existing scalar counters and
performance totals describe only the selected window, not earlier windows.
Replaying a row page or retrying the same window repeats those values; callers
must not add them twice. Empty windows have zero counters.

Usage, turn-end duration, turn counts, retry counts, tool-call/result/failure
counts, stop counts, and validated network-attempt counts can be added once per
non-overlapping window. Their coverage remains retained-event evidence, not
complete target state. Lifetime `runs` also gains
`runs_scope:"distinct_observed_within_window"` and `runs_additive:false`.
Run IDs are not assumed contiguous: out-of-band events can interleave, and one
run can span windows. No sum of window-local run counts is advertised as an
exact lifetime count. Snapshot latest-run counters and cumulative usage remain
separate and unchanged.

Existing `complete` and `authoritative` fields retain their view-specific
meaning. A network gap prevents a claim of complete evidence even when the
scan reaches EOF. `scan_complete` describes available-record exhaustion, while
`row_page.has_more_rows` describes remaining output and `retention_complete`
describes the absence of a lifetime-retention guarantee. Existing top-level
`scanned_events` and `scan_complete`, where present, mirror the window values.

### History sources

History discovery pages contain current snapshot, self-contained compaction
archives, and the retained-replay source availability. The snapshot and replay
availability entries appear only in the first window; later windows catalog
archives from their own interval. Row cursors page these catalog entries.

An archive history handle identifies its exact event position using an encrypted
claim containing the preceding backend cursor and expected archive-event cursor.
Selecting it performs a bounded point read and validates both position and event
kind before invoking the existing transcript projection. It does not rescan
from the beginning, and remains subject to current root/scope authorization.
Transcript `offset`/`limit` within a selected archive remain unchanged.

`retained_event_history` is selectable only after an initial window reaches EOF
without gaps and `eventsource.Fold` succeeds. Its encrypted handle binds that
complete interval and original scan disposition; selection rereads that exact
interval and revalidates its endpoint before folding. Appends do not silently
expand the selected source. Snapshot handles select the current authorized
snapshot directly, without event replay.

When the EOF probe finds records beyond the initial window, include an
unselectable replay source entry with `available:false`, no history handle, and
`reason:"full_replay_window_limit"`. Exactly 10,000 events followed by EOF still
qualify when gap-free and foldable. Gaps use `reason:"event_gap"`; a failed fold
uses `reason:"replay_incomplete"`. Successfully discovered archives remain
available when the scan hits its bound. A suffix never becomes a complete
reconstructed conversation.

### Failures, capabilities, and token lifetime

Malformed, oversized, tampered, wrong-purpose, wrong-view, wrong-scope, or stale
tokens fail without echoing token contents, raw backend errors, or identifiers.
The tool error is `continuation is invalid or stale; restart this view without
cursors`. Storage read failures expose `event log read failed`, no new cursors,
and no partial event rows or aggregates; snapshot-only status/history evidence
can remain available with `stop_reason:"read_error"`. An iterator ending after
context cancellation is a failed scan even if it yields no error. Check context
state before endpoint validation and before publication; do not mint tokens or
return partial event-derived evidence on cancellation.

For a configured plain `EventLog`, initial requests retain the existing bounded
projection and report `continuation_supported:false`. They offer no new cursor
and state `continuation_error:"configured event log does not support resumable
reads"`. The same rule applies when a gRPC driver explicitly returns
`port.ErrCursorUnsupported` (including the driver's retained alias); a Go type
assertion alone does not establish remote support. Initial-call fallback is
allowed only for absent capability or that explicit unsupported outcome, never
arbitrary I/O errors.
A continuation request against that backend fails explicitly. An absent log
reports `not_configured`. No cursor request silently falls back to a fresh
prefix read. Legacy gaps remain unknowable: report `gap_records:null`, not zero,
and keep retention incomplete. New initial-window metadata on a legacy backend
describes its old scan, while existing legacy paging/replay behavior is retained.
Legacy history selectors bind their source ordinal and bounded-prefix digest
instead of inventing a backend position; selection replays the same bounded
legacy prefix and rejects changed source identity. A successful legacy Fold is
retained-event reconstruction with unknown gap coverage, never certified full
history. The cursor-backed gap-free eligibility rule does not remove that
existing explicitly incomplete legacy source.

Enforce the 16 KiB encoded limit before decoding and on emitted continuation and
history handles. Token creation failure or an oversized backend position
returns `continuation could not be represented within the response bound`, with
no partial event evidence. Use nonce generation safe for distinct full claims;
never derive an AES-GCM nonce solely from a shared interval ID.

Tokens contain only bounded position, scan-disposition, projection-basis digest,
and binding metadata, not conversation bodies, accumulated messages, run-ID
sets, or aggregate state. They survive debug
engine reconstruction with the same target/scope incarnations and owner binding.
Deletion, target replacement, changed authorized lineage, or invalid backend log
generation invalidates them. The existing stateless design owns no resource that
outlives the call beyond already-persisted tool output; no new ADR 0027 inventory
entry is needed. A design requiring retained state must stop and reclassify.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — Continue a bounded event scan and page every projected row

Use the existing [cursor port](../../engine/port/cursoreventlog.go),
[network scan bound](../adr/0255-sanitized-network-attempt-evidence.md), and
[debugger projection](../../internal/adapter/sessiondebug/sessiondebug.go)
boundaries. Output pagination must not hide the next raw window.

**Acceptance:**
- AC1.1: Empty, exactly-10,000, and 10,001-record logs distinguish EOF from scan exhaustion using at most one non-projected probe. Exactly 10,000 gap-free events remain eligible for complete replay. A relevant event after 10,000 irrelevant events is reachable without skipping the probe or recounting records.
  - verify: `TestDebuggerScanContinuation_Scenario1_RecordBoundaries`
- AC1.2: Performance, network, delegation, manifest, and history catalog row pages remain traversable when the window itself is scan-incomplete; multi-row events, changed page limits, and response-byte omissions never cause a non-advancing cursor.
  - verify: `TestDebuggerScanContinuation_Scenario1_RowPages`
- AC1.3: Initial output offsets apply only to the initial window. Snapshot/transcript/related offsets and activity pagination beyond 10,000 remain unchanged; invalid cursor/offset/history-handle combinations fail explicitly.
  - verify: `TestDebuggerScanContinuation_Scenario1_Compatibility`

### Scenario 2 — Report the coverage of counters and incomplete evidence

[Status and lifetime evidence](../../internal/adapter/sessiondebug/views.go)
and [ADR 0254](../adr/0254-session-debugger-admin-transport.md) distinguish stored
state from bounded event-derived claims.

**Acceptance:**
- AC2.1: Each window reports only its own additive counters. Combining distinct adjacent windows matches the equivalent complete retained-event reduction; repeating row pages does not present new aggregate coverage.
  - verify: `TestDebuggerScanContinuation_Scenario2_WindowAggregates`
- AC2.2: A modern run and a legacy run crossing a window boundary, including interleaved repeated run IDs, never produce an advertised additive lifetime run count. Snapshot counters remain unchanged.
  - verify: `TestDebuggerScanContinuation_Scenario2_RunScope`
- AC2.3: Gap records consume the record budget and advance continuation, expose no raw reason, and prevent a completeness claim. EOF, output pagination, scan exhaustion, unknown retention, and real read failure remain distinguishable.
  - verify: `TestDebuggerScanContinuation_Scenario2_Completeness`

### Scenario 3 — Discover later archives without inventing full replay

[History selection](../../internal/adapter/sessiondebug/views.go) and
[ADR 0256](../adr/0256-session-debugger-evidence-and-reporting.md) require
separate sources for the snapshot, archives, and reconstruction.

**Acceptance:**
- AC3.1: Archives before and after the first window remain discoverable and selectable with model-visible history handles. A later archive read uses its exact retained position, not a prefix scan.
  - verify: `TestDebuggerScanContinuation_Scenario3_ArchiveSelection`
- AC3.2: Scan exhaustion preserves discovered archives and reports `full_replay_window_limit` for full reconstruction only when additional records exist. A gap-free complete log, including exactly 10,000 events followed by EOF, still reconstructs; gap, malformed replay, and read failure have their distinct specified outcomes.
  - verify: `TestDebuggerScanContinuation_Scenario3_ReplayCoverage`

### Scenario 4 — Revalidate continuation and backend capabilities

The [existing authenticated scope handles](../../internal/adapter/sessiondebug/views.go),
[cryptographic incarnations](../adr/0258-cryptographic-session-incarnations.md),
and [durable cursor contract](../adr/0250-durable-cursors-and-watch.md) remain the
authority and storage boundaries.

**Acceptance:**
- AC4.1: Tampering, oversized input, cross-view/purpose/scope use, owner changes under enforcement, lineage changes, deletion/recreation, and log generation replacement reject tokens without leaking contents or returning partial event evidence. An unchanged authorized incarnation can resume after engine reconstruction.
  - verify: `TestDebuggerScanContinuation_Scenario4_TokenBinding`
- AC4.2: Concurrent appends cannot expand an existing row-page interval or change its sealed EOF/scan-limit disposition. Snapshot/lineage changes that alter projection rows invalidate row continuation rather than skip or duplicate surviving rows. Reset during scan/replay or before the publication check, and a silently ending cancelled iterator, return no partial event evidence or new cursor. Endpoint validation rereads at most one covered record.
  - verify: `TestDebuggerScanContinuation_Scenario4_ConcurrentReads`
- AC4.3: A plain EventLog and a remote driver explicitly lacking cursor RPCs preserve explicitly limited initial evidence and refuse continuation; arbitrary errors never trigger fallback. Cursor-enabled memstore, JSONL, Redis, and gRPC-driver paths use the existing capability. The unsupported sentinel preserves existing `errors.Is` checks through the driver alias, with no new durable state, format, or cursor-port method.
  - verify: `TestDebuggerScanContinuation_Scenario4_BackendCapability`; inspection — API snapshot and changelog classify the additive sentinel; existing cursor conformance and storage contracts remain unchanged.

### Scenario 5 — The debugger model can follow the continuation workflow

[AGENTS.md](../../AGENTS.md) requires model-visible instructions through the real
factory. The [session diagnosis guide](../../user-docs/mecatui/sessions.md) owns
the operator task; the [observability topic](../architecture/observability.md)
owns the short contributor explanation and links to the guide.

**Acceptance:**
- AC5.1: Through the real debug-session factory and offline model loop, a model obtains the single `next_cursor` solely from tool results, passes it as `cursor`, and reaches evidence after the first window without choosing a row-versus-window phase. Its system prompt and tool schema describe continuation, empty-page behavior, and window-local counts.
  - verify: `TestDebuggerScanContinuation_Scenario5_ModelWorkflow`
- AC5.2: An uncooperative offline model returning malformed or wrong-scope continuation cannot access another session, mutate the target, or gain filesystem tools; a subsequent valid call can recover using the documented workflow.
  - verify: `TestDebuggerScanContinuation_Scenario5_AdversarialModel`
- AC5.3: The existing diagnosis guide describes view-specific limits, unavailable full replay, legacy backend limitations, and coverage without suggesting that false retention completeness proves deletion.
  - verify: inspection — verify guide claims against the accepted interface and implemented tests; `task docs` and `task site:build` pass.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Arbitrarily long full conversation reconstruction | Separate replay/checkpoint proposal if approved | No unbounded carried conversation or suffix-as-full-history claim. |
| Exact cumulative distinct run counts across windows | Separate aggregate contract if required | Window-local run counts are explicitly non-additive; no contiguity assumption. |
| Raising/removing scan limits or configuring retention | No change in this issue | Preserve resource limits and operator retention policy. |
| New activity scan cap or live following in InspectSession | No change in this issue | Preserve current activity offsets; the existing watch API owns following. |
| Public network/manifest replay, new backend APIs or durable token registry | Separate Architectural proposal | Preserve existing transport, storage, and authority decisions. |

## Definition of done

1. Human decisions are resolved and the plan passes its checker and advisory
   review. Record the exact parent plan commit and the operator's stacked-delivery
   authorization; both PRs remain subject to human review and merge.
2. Focused offline tests use explicit test-owned stores, directories, and
   `UserModelDir` at real composition boundaries. Test invalidation and ordering
   use deterministic seams rather than sleeps or ambient operator state.
3. The implementation updates the existing diagnosis guide and only the owning
   observability paragraph/link; no separate feature-status page is added.
4. The integrated candidate passes `task test`, `task lint`, `task test:race`,
   `task docs`, `task site:build`, and the offline demo. Run `task api:update`
   and include the additive sentinel's API snapshot and `engine/CHANGELOG.md`
   entry; `task api:check` passes. `task ac-trace-strict` resolves the proofs
   when the plan becomes landed.
5. Independent panel review covers specification, standards, test adequacy,
   security, and operator/developer ergonomics. The implementation PR names the
   approved plan commit and any explicitly authorized contract amendment.

## Deferred decisions and known risks

The accepted delivery scope excludes exact cross-window run totals and full
long-log replay. The cited guidance supports continuation and honest coverage
but does not turn pagination into either capability. A later requirement for
those capabilities needs its own contract rather than a silent scope expansion.

A record bound is not a byte-allocation bound. Existing adapters can materialize
records before yielding, and one compaction archive can itself be large. This
plan preserves existing projection-size protections and does not claim to bound
all backend allocations. Repeated row pages cost one bounded interval replay;
there is no cache with a new lifecycle or invalidation policy.
