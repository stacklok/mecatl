# ADR 0226 — Memory lifecycle hardening: wire-boundary attribution trust and undo semantics

- Status: Accepted
- Date: 2026-08-14
- Scope: `engine/tool` (memory lifecycle validation), `internal/adapter/grpcdriver`
  (driver wire boundary), `internal/app` (permission floors, operator-profile
  child gating), `engine/adapter/memmemory` (undo semantics)
- Supersedes: none
- Superseded by: none

## Context

ADR 0107 shipped an optional, versioned memory-lifecycle capability
(`RememberVersioned`/`Inspect`/`ForgetVersioned`/`UndoLatest`) alongside the
existing `tool.MemoryStore`. A post-merge multi-axis review (spec, standards,
and a domain panel spanning security, Go architecture, and protocol design)
surfaced several gaps in how that capability enforces its own stated
invariants. This ADR resolves the ones that need an explicit, durable
decision rather than a pure bug fix.

**Attribution is wire-reachable and gates a security check.**
`tool.MemoryAttribution` (`Writer`/`Origin`/`Source`) is documented as
carrying no authority: "stores must never derive authorization from context
attribution." In practice, `ValidateMemoryContentWrite`'s `modelAuthored`
predicate uses `attribution.Writer == MemoryWriterModel` (among other
conditions) to decide whether the instruction-injection scan
(`DirectiveShapedUserMemory`) runs at all on a `user/`-scoped write. Because
`internal/adapter/grpcdriver/server.go`'s `withProtoAttribution` threads the
gRPC `MemoryStoreService` request's `Attribution` field verbatim into this
same context value, any caller of that driver RPC can set `Writer="user"`
(or omit the field) on an instruction-shaped `user/` value and the scan
silently does not run. The field's documented "no authority" contract and
its actual behavior have diverged. The scan runs from every RPC whose
request carries a `MemoryEntry` the stores will validate — `RememberEntry`,
`RememberVersioned`, **and `RememberIfCurrent`** (`ForgetVersioned` and
`UndoLatest` carry no new content to scan — a tombstone has no value, and an
undo restores an already-validated prior revision), so the bypass is real
but narrower than "every lifecycle RPC".

Enumerating that set correctly is the whole difficulty, and two of the three
members are easy to miss. The legacy `RememberEntry` gRPC handler never calls
`withProtoAttribution` at all, so it carries *zero* attribution and the scan
is silently skipped for every wire write with no forged `Writer` even
required — a hole already open on a handler that predates the lifecycle
capability. And `RememberIfCurrent`, the convergence write, does not look
like a lifecycle write from the handler list but validates through the same
`tool.ValidateMemoryEntryWrite` as its two siblings
(`engine/adapter/memmemory/memmemory.go`, `internal/adapter/memory/store.go`),
so it is scanned identically. A three-member set that is this easy to
under-count should not be maintained as three hand-written test cases, so
the rule is expressed as a TABLE over the write RPCs
(`TestADR_0226_ContentBearingWriteRPCsForceInjectionScan`): the membership
criterion is "the request message carries a `MemoryEntry`", and a fourth
such RPC added later fails the table rather than silently escaping the gate.

The `modelAuthored` gate is not incidental, though: it is a real, load-bearing
exemption. A human explicitly asking their assistant to remember something
instruction-shaped (an unusual but deliberate preference) is legitimate;
a model autonomously persisting instruction-shaped text — the shape produced
when automatic learning summarizes a poisoned tool result — is the actual
attack. `Writer`/`Origin` is self-reported everywhere it is set, in-process
and over the wire; the difference is that in-process it is stamped by trusted
composition code the operator's own deployment controls, while over the gRPC
driver RPC it arrives inside a request message.

That difference needs care, because this repo has already decided what a
driver *is*, and it is not "an untrusted peer."
[ADR 0005](0005-driver-seams.md) places a driver at the
operator-infrastructure tier — the same trust class as the on-disk store
directory — and observes that a compromised agent-source driver executes
arbitrary shell on the harness host via def hooks, resolving that case to
"treat it as harness-equivalent infrastructure" rather than defending against
it. [ADR 0212](0212-caller-ownership-enforcement.md) and
[ADR 0213](0213-driver-caller-ownership.md) go further: the raw driver
endpoint is deployment-internal, never tenant-reachable, and 0213's target
state authenticates the peer as a specific mTLS workload identity. So this
decision does NOT rest on "the peer is hostile" — under those ADRs it is not,
and if it were, memory-value scanning would not be the exposure that mattered.

It rests on the OTHER rule ADR 0005 states, which is orthogonal to how
trusted the peer is: **"a driver is never trusted to sanitize."** Every driver
client in this tree already re-validates, re-caps, and re-normalizes what
comes back, and stamps origin labels harness-side because the wire's claimed
origin is driver-side observability only. `Attribution`'s scan-gating role is
the one place that rule was not being applied — a claimed `Writer` was
allowed to switch a harness-side validation OFF. Forcing the classification
restores the existing discipline to a field that had escaped it; it is not a
new claim that the driver is an adversary.

**Forget and Undo have mismatched approval floors, but fixing Undo's floor
alone does not close the gap it looks like it closes.** `ForgetVersioned` is
a floor-Ask (an operator must approve a deletion); `UndoLatest` is a
floor-Allow. A model can call `Undo*` immediately after an operator approves
a `Forget*`, silently restoring what was just deleted, with no further ask.
However, `RememberVersioned`/`RememberUser` are *also* floor-Allow and always
have been — a model that wants a forgotten fact back does not need `Undo` at
all; it can simply `Remember` the same value again, with or without any
change to Undo's floor. The real property the asymmetry gestures at — "once
forgotten, a value needs the same approval to come back, however it comes
back" — would require gating any write to a key whose current state is a
tombstone, not adjusting `Undo`'s floor in isolation. That is a materially
larger change to the write path than this pass scopes, and is called out
below as a separate, deferred question rather than answered by a one-line
permission-table edit that would not actually deliver the property it
implies.

**`UndoLatest`'s own semantics are internally inconsistent.** The
backward-scan that picks an undo *target* explicitly skips revisions whose
`Origin` is `MemoryOriginUndo` or that are already marked undone
(`r.undone`), but the value it restores *from* (`target-1`) is read without
the same filter. The interface has never stated the intended contract for
repeated undo, so there is no rule to check the code against.

**Silent history truncation is invisible to every caller.** The reference
store's `retainLatest` caps a key's revision history at 64 entries; the local
file store tracks an equivalent `HistoryTruncated` flag internally. Neither
Go's `tool.MemoryRecord` nor the wire `MemoryRecord` proto message exposes
this, so a caller of `Inspect` cannot distinguish complete history from a
truncated tail — it only surfaces as a confusing `UndoLatest` failure once
truncation has already made an undo impossible.

**`childOperatorProfileSource`'s role gate fails open.** The switch that
withholds the operator profile from internal-purpose engines
(guardrail-checker, ask-reviewer, model-router, usermodel-review, judge
variants) is a deny-list: any role string it doesn't recognize falls through
to the `default:` branch and receives the profile. A future internal-purpose
engine role leaks operator facts into a one-shot internal prompt until
someone remembers to add it to this list.

**`RememberVersioned`'s empty-`expected` semantics were considered against
ADR 0208's file-Workspace precedent** (`docs/adr/0208-execution-environment.md`,
which removed the analogous "no unconditional Write" sentinel from
`tool.Workspace`). On review, memory-fact writes and file-content writes are
judged to carry different risk: `RememberVersioned` with an empty `expected`
intentionally mirrors legacy `RememberEntry`'s last-write-wins behavior so
that ordinary explicit and automatic-learning writes stay low-friction and
don't require every caller to track a version token. This ADR keeps that
behavior and treats the two domains as distinct rather than harmonizing them.

## Decision

**The in-process `modelAuthored` exemption is preserved unchanged; the gRPC
driver-server boundary stops trusting wire-supplied attribution for the
*scan-gating decision only*, on all three handlers where the scan can
actually fire (`RememberVersioned`, `RememberIfCurrent`, and the legacy
`RememberEntry`).** Every in-process call site keeps today's behavior
exactly — composition code stamping genuine `Writer`/`Origin` still lets a
real human-authored, instruction-shaped preference through unscanned. But
those three wire handlers force the scan's `modelAuthored` classification to
true regardless of what `Attribution` the request claims, so a remote peer
can only ever receive *more* scrutiny than an in-process write, never less.
`ForgetVersioned`/`UndoLatest` are unaffected because there is no scan for
them to bypass.

The membership rule is "the request carries a `MemoryEntry`", and
`withForcedMemoryWriteAttribution` is the single attribution entry point for
that set. It DELEGATES to `withProtoAttribution` rather than rebuilding the
attribution field by field. That is deliberate: the forced variant differs
from the plain one in exactly one bit, so any hand-copied version of the
other fields is a silent divergence waiting to happen — dropping
`MemorySource.ProposalID`, say, would break the audit link
`engine/adapter/memorypromotion` uses to match a promoted proposal to its
resulting revision, while a provenance test that checked only
`Writer`/`Origin` kept passing. Delegation makes a future `MemorySource`
field arrive in both paths for free.

This is implemented as a distinct classification input, not by overwriting
the request's `MemoryAttribution` in place: the value persisted onto the
resulting revision's `Writer`/`Origin` fields must still reflect what the
caller actually reported, for provenance and display. Conflating "what do we
scan as" with "what do we record as the writer" would corrupt the durable
provenance of every legitimate wire write in order to fix a scan that only
needed a classification override. `MemoryAttribution` remains exactly as
documented — informational provenance, never itself a security decision —
for every caller that can be trusted to report it honestly, and is simply
not consulted for the scan decision at the one boundary that cannot be.

**`Undo*`'s floor is left unchanged (floor-Allow), because raising it does
not deliver the property it appears to.** As established in Context, the
Forget/Undo asymmetry is real but not closeable by adjusting Undo alone:
`Remember` already provides an equivalent, ungated path back to any
"forgotten" value. Gating `Undo` specifically would add friction to the
common, benign case (an operator or model correcting an ordinary mistake)
while leaving the actual restoration path (`Remember`) exactly as open as
before. The property worth having — a tombstoned key requires the same
approval to be written to again, regardless of which tool performs the
write — is deferred as a separate design question (see *Deferred decisions*
in the accompanying acceptance plan); it was not answered here because
answering it correctly means gating writes generally on a key's tombstone
state, not editing one permission-table entry.

**`UndoLatest`'s contract is: no redo, and repeated undo walks strictly
backward through revisions never yet undone.** The restore-source is
filtered by the same predicate as the target-selection scan (excluding
`MemoryOriginUndo` revisions and any revision already recorded in
`r.undone`), and this rule is stated in `MemoryLifecycleStore`'s doc comment,
not left implicit.

**`MemoryRecord` gains a `Truncated` field** (and the wire `MemoryRecord`
message gains a mirrored `history_truncated` field), populated from each
store's existing internal truncation signal. A caller of `Inspect` can now
tell complete history from a truncated tail without waiting for `UndoLatest`
to fail.

**`childOperatorProfileSource` inverts to an allow-list.** Only recognized
first-class role shapes receive the operator profile: the main engine
(empty role), Subagent children (`"task"` or a `"task:"` prefix, covering
the per-call model-override role `"task:model="+model`), team members
(a `"member:"` prefix), and Parallel branches (`"parallel"` or a
`"parallel:"` prefix, covering the per-branch model-override role
`"parallel:model="+model` — a real, already-shipped role this ADR's first
draft missed and would have silently regressed). `"parallel-judge"` and
every other judge variant is excluded, since neither `"parallel-judge"` nor
any judge role matches the `"parallel:"` prefix (a hyphen, not a colon) or
the bare `"parallel"` string. Every other role — recognized-internal or not
yet invented — is excluded by default. A new internal-purpose engine role
now leaks the profile only if someone positively adds it to the allow-list,
not if someone forgets to add it to a deny-list.

## Consequences

Closing the attribution bypass restores the "never trusted to sanitize" rule
to the one field that had escaped it, while leaving every legitimate
in-process human-preference write exactly as unrestricted as before. It is
one defense among several, not a sole channel: an instruction-shaped `user/`
value that reached storage anyway is still dropped at RENDER by
`engine/prompt/operatorprofile.go`'s `operatorProfileEntryAllowed`, which
applies the same predicate unconditionally. The write-time gate is not
redundant with it, though — `engine/prompt/memoryindex.go` and
`engine/prompt/usermodel.go` render `key + description` behind a
secret-shape check ONLY, with no directive-shape filter, so for those two
sinks the write boundary is the gate that fires. That the three render sinks
disagree about this predicate is a real inconsistency, out of scope here and
recorded in the acceptance plan's deferred list.

The cost is not merely "extra scan work," and should not be described that
way: a genuinely legitimate remote deployment — `MemoryStoreService` is a
documented driver-protocol surface a separate trusted process can implement
— that wants to write a real, human-authored, instruction-shaped preference
*over the wire* loses that capability permanently, with no override, because
the forced classification cannot tell that peer from any other. This is an
accepted, deliberate fail-safe, not a free hardening with no behavioral cost.
Its present-day reach is nil and should be stated as such: no in-tree code
registers `MemoryStoreServiceServer` in production, and the server wrappers
live in `internal/` because the bufconn conformance fixtures need them
([ADR 0005](0005-driver-seams.md)'s deferred server-wrapper promotion). The
cost is therefore currently hypothetical — it is priced here because this
code is the reference a real driver host would be built from, not because a
deployment is paying it today. Leaving `Undo*`'s floor unchanged means the Forget/Undo
asymmetry remains visible in a permission-table read of the two tools, with
this ADR as the recorded reason it was not "fixed" superficially. The
`Truncated`/`history_truncated` fields are additive on both the Go struct
and the wire message and do not change existing serialization for a caller
that ignores the new field. Inverting `childOperatorProfileSource`'s
polarity is behavior-preserving for every role recognized today, including
the per-call model-override roles for both Subagent and Parallel, and only
changes behavior for a role nobody has written yet.

`RememberVersioned`'s last-write-wins-on-empty-`expected` behavior is
explicitly retained and documented as intentional, not deferred or
reconsidered — a caller that wants compare-and-swap semantics must supply a
non-empty `expected` version, exactly as before.

`InspectMemoryRequest` gains an optional `max_revisions` cap and the
version-token fields (`expected_version`, `version`) gain `buf.validate`
size bounds on every request that carries one, `RememberIfCurrentRequest`
included. Both are additive, non-breaking wire changes; a driver that does
not honor `max_revisions` continues to behave as it does today (unbounded),
so this is a hardening default for compliant drivers, not an enforcement
mechanism this codebase can impose on a third-party driver implementation.
The `buf.validate` annotations are DOCUMENTATION plus future enforcement:
`internal/adapter/server/doc.go` records that the protovalidate runtime is
not wired, so nothing in-tree rejects an oversized token today. When the
server honors `max_revisions` it also sets `history_truncated`, because
capping the response is truncation from the caller's point of view and
reporting `false` there would recreate the exact "is this all of it?"
ambiguity the field was added to remove.

**Detector convergence is explicitly OUT OF SCOPE, and this decision records
why so a later pass does not treat it as free cleanup.**
`engine/tool.DirectiveShapedUserMemory` and
`engine/adapter/skillfs.ScanForInjection` do maintain overlapping deny-lists
for the same attack shape, and they have drifted; folding one into the other
looks like obvious hygiene. It is not, for two reasons.

First, the lists are not equivalent in either direction, so "merge them" has
no neutral meaning: whichever becomes canonical, the other's consumers change
behaviour. Converging on unanchored matching, the tempting choice because it
is the broader-looking list, also interacts badly with
`CanonicalMemoryText`, which DELETES control runes rather than normalising
them to `\n` — `ignore previous\rinstructions` canonicalises to
`ignore previousinstructions` and matches nothing.

Second, and decisively, `DirectiveShapedUserMemory` has a second consumer
with a very different cost profile. `engine/prompt/operatorprofile.go`
(`operatorProfileEntryAllowed`) applies it at RENDER time, unconditionally
and with no attribution gate, dropping a matching entry SILENTLY on every
turn. The write-time gate rejects loudly and recoverably; the render-time
filter removes already-stored, previously-rendered operator facts from the
prompt with no error, no diagnostic, and no entry in the profile's own
`omitted` count (disallowed entries leave `active` before `omitted` is
computed, so the block can render as the empty string). Widening the shared
predicate is therefore not a tightening — it is silent data loss from the
model's view of the operator.

Any future convergence must be evaluated against BOTH consumers and must
converge on the line-anchored semantics, not away from them. Tracked as
separate work.

## See also

- [ADR 0107 — Live operator profiles and reversible memory lifecycle](0107-operator-profile-memory-lifecycle.md)
- [ADR 0208 — Execution environments](0208-execution-environment.md) (the file-Workspace version-sentinel precedent this ADR distinguishes itself from)
- [Memory architecture](../architecture/memory.md)
- [docs/acceptance/memory-lifecycle-hardening.md](../acceptance/memory-lifecycle-hardening.md) — the verification contract for this decision
