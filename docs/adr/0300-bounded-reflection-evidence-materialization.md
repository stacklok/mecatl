# ADR 0300 — Bounded reflection evidence materialization

- Status: Proposed
- Date: 2026-09-04
- Scope: reflection input selection, evidence provenance, coordinator identity, and proposal verification
- Supersedes: [ADR 0109](./0109-staged-learning-proposals.md) only for its raw-size rejection, input-coordinate evidence handles, and proposal evidence identity decisions; legacy ADR-0109 records remain readable under their historical protocol
- Superseded by: none

## Context

ADR 0109 made reflection resource-safe by bounding input and rejecting oversized raw
trajectory/event material before projection, marshal, or queue allocation. That rule prevents
unbounded coordinator retention, but it also treats retained session size as evidence quality.
An otherwise ordinary completed session can exceed the default 256 KiB job limit because it
contains many tool results, binary media, provider reasoning, raw arguments, or old history
that the reflector must not receive anyway. Explicit `/reflect` then fails, and automatic
reflection silently loses an otherwise eligible current trajectory.

Projection after a raw-size gate cannot solve this: prohibited bytes still independently
disqualify the session. Constructing an unbounded `learning.Input` or full canonical projection
before the gate would merely move the memory and cancellation risk earlier. Automatic admission
must inspect the full eligible source and verified current span, but the provider needs only one
bounded selected view. Those are different operations and must not share an unbounded value.

The old input-local `EvidenceRef.Ordinal` and `m:n`/`e:n` handles also conflate the reflector's
compact selected view with durable original coordinates. A staged candidate needs a durable,
immutable description of the complete input that the reflector saw, not only its own citations.
Approval must verify that exact description without re-running relevance selection and silently
substituting a nearby unit.

The existing coordinator limits, automatic admission controls, provider/model routing,
proposal CAS lifecycle, evidence-preview safety, and promotion policy are sound. The defect is
the unit being bounded and identified: raw retained material or an ephemeral input position,
rather than a deterministic selected-evidence aggregate with durable provenance.

## Decision

### Admission before bounded selection

Define one storage-neutral, versioned materialization protocol in `engine/learning`, initially
`reflection-evidence/v1`. Both explicit `/reflect` and automatic completed-trajectory reflection
use it before queue or receipt allocation, singleflight insertion, budget reservation, provider
work, or proposal work. Raw retained trajectory/event size no longer independently
disqualifies a normally eligible session.

Automatic admission scans the full eligible source trajectory, its verified current span, and
eligible event stream incrementally. It may retain only bounded counters, coordinates, digests,
and ranking state; it MUST NOT construct or copy an unbounded `learning.Input` or complete
canonical projection. Existing threshold, sensitivity, stop, current-span, cooldown, and budget
rules still decide admission. Only after admission succeeds does materialization construct one
bounded selected `learning.Input`. Explicit reflection enters the same materializer directly.

The two invocation paths share only selected-evidence identity. Invocation mode, host signals,
automatic admission facts, and bounded existing-memory comparison context are not part of the
selected-evidence digest, singleflight key component, or provenance manifest. They remain
separate request/admission context and may differ without changing what selected source evidence
means.

### Atomic units, ranking, and bounds

The message selection unit is a connected tool-turn component: one assistant message together
with all tool calls on that message and every corresponding tool-result message. Connectivity is
transitive where the canonical conversation structure links those messages. A message outside a
tool component is a singleton unit; an eligible event is an event unit. Selection either includes
an entire unit or omits it, preserves original source order in the final input, and always passes
bidirectional tool pairing.

Selection priority is closed and deterministic, highest first:

1. the mandatory verified automatic current span and every connected paired-tool closure it
   reaches;
2. genuine user evidence carrying explicit remember or learn-procedure intent;
3. source context supporting repeated correction, failure recovery, or repeated stable tool
   sequence signals;
4. the most-recent eligible user/assistant context;
5. eligible event evidence.

Within a tier, original source order/coordinates are the stable tie-breaker. Recency determines
membership/priority of tier 4, then source coordinates break ties. After choosing whole units,
the materializer emits messages and events in source order and assigns selected-local handles.
Map iteration, goroutine order, slice capacity, and input arrival batching cannot affect output.

All existing message, event, evidence, encoded job/request byte, token, and provider limits apply
to the selected view. Existing canonical per-field projection limits still apply, but the
materializer never introduces a second arbitrary text truncation to make a unit fit. A component
that remains individually oversized after canonical safe-field projection is omitted whole.
If the mandatory current-span closure cannot fit, automatic reflection skips; explicit reflection
returns successful abstention with reason `mandatory_span_exceeds_bounds` when such a mandatory
span applies, otherwise `no_eligible_evidence`. No partial closure is legal.

### Safe-field projection

Projection classifies fields before copying or accounting selected content:

- retain bounded, UTF-8-repaired, canonical user/assistant text; tool call ID and name; tool
  result error bit and bounded safe textual content/parts; public textual media metadata already
  admitted by the canonical projection; and eligible content-free event type/sequence/turn/stop
  metadata;
- omit provider reasoning and provider item IDs, binary media/data and media payloads, actor
  identity, permission arguments, raw tool arguments, credential/secret-shaped values, URLs or
  metadata rejected by the existing secret/directive classifiers, and all Subagent/Parallel/Team
  delegation payloads or child-authored previews;
- represent omitted media/secret/prohibited fields only through an existing fixed canonical
  marker when the current projection already defines one; otherwise omit the field. Never copy,
  hash into a diagnostic label, or invent a content-derived reason from prohibited bytes;
- normalize retained hostile/control text with the existing canonical projection and untrusted
  framing before digesting or sending it. Existing projection truncation is part of the protocol;
  post-projection component truncation is forbidden.

An empty unit after this projection is ineligible. A source consisting only of omitted material
therefore abstains/skips before coordination work.

### Manifest, coordinates, handles, and identity

Every successfully materialized aggregate has an immutable exported
`MaterializationManifest` containing:

- protocol version;
- source identity boundary `{domain: "mecatl/reflection-evidence/source/v1", session_id}`,
  binding the manifest to that exact caller-owned session namespace rather than owner, proposal
  partition, invocation mode, or display metadata;
- the complete source-ordered selection manifest: one entry for every selected message and
  event, with a distinct durable original message coordinate or original event sequence,
  locator kind, canonical projected-evidence digest, and complete tool-call/component binding;
- the canonical selected-evidence SHA-256 digest over a domain-separated encoding of protocol,
  identity boundary, ordered manifest entries, and canonical selected evidence.

The manifest describes the whole aggregate and is persisted once with every staged proposal.
Candidate `EvidenceRef`s are citations into manifest entries; they are never a substitute for or
partial copy of the manifest. A v1 citation carries the aggregate digest plus selected manifest
entry index and must exactly match that entry's locator, original coordinate/event sequence,
digest, and tool binding. Model-facing handles are compact selected-local `m:0..n` and
`e:0..n`, resolved to those manifest indexes only against the current selected input. Durable
references preserve their separate original coordinates/event sequences and digest; removing an
earlier source unit cannot renumber durable identity.

Do not reinterpret legacy `EvidenceRef.Ordinal`. ADR-0109 records and APIs retain their historical
input-local meaning under `reflection-evidence/legacy-v0`. A persisted pre-version record decodes as
`legacy-v0` by this single schema-compatibility rule; new records MUST write an explicit version.
New v1 records use the manifest and distinct original-coordinate fields. All subsequent readers
dispatch on the resolved protocol; they do not infer coordinate meaning from the presence of new
fields or rewrite old ordinals.

Selected-evidence identity is exactly `(protocol version, identity-boundary domain/value,
canonical selected-evidence digest)`. It drives singleflight and contributes to deterministic
proposal IDs. Principal/project partition and canonical candidate remain additional proposal-ID
inputs, but host signals, invocation mode, and existing-memory comparison context do not enter
selected-evidence identity. One selected input still causes at most one provider reflection call;
there is no chunking, multi-pass extraction, map/reduce, or candidate merging.

### Re-materialization and proposal surfaces

Proposal detail and approval each load the immutable aggregate manifest and re-materialize that
exact ordered selection once. They do not re-run ranking. The operation owner-authorizes and
re-reads every original coordinate/event sequence, re-applies the versioned canonical projection,
reconstructs connected tool closure, validates every entry digest and binding, and validates the
aggregate digest and identity boundary. Candidate citations are then resolved into those verified
manifest entries. An unavailable, compacted, deleted, reordered, or changed source, unsupported
protocol, identity mismatch, tool-binding mismatch, or digest mismatch fails precondition and
never promotes; no nearby evidence is substituted.

Proposal list remains metadata-only and does not materialize evidence. Proposal detail preserves
the existing evidence-preview contract: any preview remains at most the existing 1024-byte,
UTF-8-safe, canonical redacted/digest-verified projection and never becomes a raw transcript or a
manifest dump. The field is not repurposed. A later removal requires a separate additive
deprecation; this ADR does not remove it.

### Closed outcomes and error mapping

Materialization returns a closed disposition: `selected`, `abstained`, or `skipped`. Its closed,
content-free reason vocabulary is `selected`, `no_eligible_evidence`,
`mandatory_span_exceeds_bounds`, `cancelled`, and `closed`. Automatic no-evidence/bounds results
project as `skipped`; explicit no-evidence/bounds results project as successful `abstained`.
Clients receive stable harness-authored text mapped from those values, never arbitrary source,
provider, repository, or `error.Error()` text as a materialization reason.

Cancellation remains the existing cancellation classification, and `closed` remains the existing
coordinator/service unavailable-or-closed classification rather than a successful abstention.
Source mismatch is failed precondition. Provider, persistence, validation, queue, timeout, and
other infrastructure faults retain their existing non-Internal typed mappings; they are not
collapsed into materialization reasons or generic Internal errors. All outward strings remain
bounded and UTF-8 repaired.

### Lifecycle and surrounding controls

Materialization is Build-owned pre-admission work. A single Build lifecycle gate/context plus
bounded active-operation accounting covers synchronous caller-run scans; there is no goroutine per
materialization job and no materialization queue. Each scan checks both caller cancellation and
Build closure while streaming source records. `Built.Close` first closes this gate, rejects new
scans, cancels in-progress scans, and joins their active accounting before continuing the existing
coordinator close sequence. A cancelled/closed pre-admission operation leaves no queue,
singleflight, receipt, reservation, provider call, proposal repository open/stage, or promotion
work. Automatic work may detach from the originating run only after successful bounded
materialization and coordinator admission, as before.

Coordinator global/per-principal count limits, selected-job and aggregate queued-byte limits,
fair FIFO scheduling, bounded receipts, timeouts, cancellation, and worker joining remain
enforced. Automatic cooldown, completed cache, process/principal budgets, reservation accounting,
and review/auto promotion controls remain enforced. Explicit reflection still reloads the
completed session's persisted provider/model, works when automatic mode is off, and retains
ownership, staging, CAS, verification, promotion, and undo controls.

Change exported engine learning evidence/provenance APIs as needed to make the manifest and exact
verification contract host-implementable. Update `engine/api/*.txt` with `task api:update` and
classify the API change in `engine/CHANGELOG.md` under the compatibility rules.

## Consequences

- Large ordinary sessions can be admitted by streaming full-source signal evaluation, then
  reflected from bounded selected evidence without retaining a full `learning.Input`.
- The aggregate manifest, not a candidate's citation list, is the durable proof of exactly what
  the reflector saw.
- Selected-local model handles stay compact while durable original coordinates remain stable;
  legacy ordinals are not silently reinterpreted.
- Whole connected tool-turn components preserve pairing. Some useful oversized components are
  omitted, and an unfit mandatory span yields a closed no-work outcome rather than truncation.
- Detail and approval each perform one exact manifest-driven re-materialization; list remains
  metadata-only and previews retain their existing redacted bound.
- Build close now owns and joins pre-admission scans without introducing per-job goroutines or
  restart-worthy materialization state.
- There is still one bounded view and one provider pass. Broader synthesis requires another ADR.
- Exported `engine/learning` API changes require API snapshot and changelog updates.

## See also

- [Scalable reflection evidence acceptance plan](../acceptance/scalable-reflection-evidence.md)
- [ADR 0109 — Evidence-backed reflection and durable staged learning](./0109-staged-learning-proposals.md)
- [ADR 0114 — Configurable learning trigger policy](./0114-configurable-learning-trigger-policy.md)
- [ADR 0027 — Cloud-native stateless execution](./0027-cloud-native.md)
- [Architecture: Evidence-backed reflection](../architecture.md#evidence-backed-reflection)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
