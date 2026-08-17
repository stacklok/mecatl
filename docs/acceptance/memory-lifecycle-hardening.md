# Memory lifecycle hardening — acceptance plan

**Phase:** post-merge hardening follow-up to the operator-profile memory lifecycle
**Status:** in-progress, 2026-08-14. Synthesized from a multi-agent panel review (Spec/Standards/Domain axes) of PR #515 after merge, plus a human-in-the-loop resolution pass on the judgement calls it raised.
**ADR:** [ADR-0226](../adr/0226-memory-lifecycle-hardening.md) — wire-boundary attribution trust and undo semantics.
**Accumulator branch:** `acc/memory-lifecycle-hardening` (off `main`).

The smallest set of work that closes the one live security gap the memory-lifecycle
feature ([ADR-0107](../adr/0107-operator-profile-memory-lifecycle.md)) shipped with,
fixes the one internally-inconsistent behavior (`UndoLatest`'s mismatched predicates),
and clears the correctness/hygiene items a panel review found — without re-opening
ADR 0107's own design decisions that this plan's human review confirmed are correct
as shipped.

The doc is organized scenario-first because acceptance is about what the running
harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0226](../adr/0226-memory-lifecycle-hardening.md) — the `modelAuthored`
  instruction-injection exemption is preserved for every in-process caller; only the
  gRPC driver-server boundary stops trusting wire-supplied attribution for it.
- [ADR-0226](../adr/0226-memory-lifecycle-hardening.md) — `Undo*`'s permission floor
  is left unchanged. `Remember*` is equally floor-Allow and provides an identical,
  ungated path back to a "forgotten" value, so gating `Undo` alone would not deliver
  the property it appears to; the real property (a tombstoned key needs the same
  approval to be written to again, regardless of which tool performs the write) is
  deferred as a separate design question, not solved by a one-line permission edit.
- [ADR-0226](../adr/0226-memory-lifecycle-hardening.md) — `RememberVersioned`'s
  empty-`expected` last-write-wins semantics is retained as intentional, distinct
  from [ADR-0208](../adr/0208-execution-environment.md)'s file-Workspace CAS
  discipline (different domain, different risk, deliberately not harmonized).

## In scope — 6 scenarios, in implementation order

Scenarios are listed in implementation order. Each is independently demoable; later
scenarios assume earlier ones but don't change their acceptance criteria.

### Scenario 1 — Wire-boundary attribution can no longer suppress the injection scan

A caller of the `MemoryStoreService` gRPC driver RPC
(`internal/adapter/grpcdriver/server.go`) can today set `Attribution.Writer="user"`
(or omit it) on an instruction-shaped `user/`-scoped value and have
`engine/tool.ValidateMemoryContentWrite`'s `modelAuthored` gate silently skip
`DirectiveShapedUserMemory`'s scan, because `withProtoAttribution` threads the wire
message verbatim into the same context value every in-process caller trusts. The scan
runs from every RPC whose request carries a `MemoryEntry`: `RememberVersioned`,
`RememberIfCurrent`, and `RememberEntry` — `ForgetVersioned` writes a
tombstone with no new content, and `UndoLatest` restores an already-validated prior
revision, so neither has a scan to bypass. Two members of that set are easy to
under-count: the legacy `RememberEntry` gRPC handler never calls
`withProtoAttribution` at all, so it carries zero attribution today and the scan is
*already* silently skipped for every wire `RememberEntry` write — no forged `Writer`
even required; and `RememberIfCurrent` reads as a convergence CAS rather than a
lifecycle write, but validates through the same `tool.ValidateMemoryEntryWrite` as
its two siblings, so it is a scan site too. Per
[ADR-0226](../adr/0226-memory-lifecycle-hardening.md#decision), the fix covers all
three handlers, driven as a table over the write RPCs so a fourth cannot silently
escape the gate: every write arriving over any of them is classified as `modelAuthored`
for scanning purposes regardless of its claimed (or absent) `Attribution`, while every
in-process call site — including the legitimate human-preference carve-out
[ADR-0226](../adr/0226-memory-lifecycle-hardening.md#context) documents — is
unchanged, and the classification override must not alter what is persisted as the
revision's own `Writer`/`Origin` provenance.

**Work:**
- engine domain (`engine/tool`): no change to `ValidateMemoryContentWrite`'s
  in-process behavior.
- adapters (`internal/adapter/grpcdriver`): the `RememberVersioned`, `RememberEntry`,
  and `RememberIfCurrent` server handlers force the scan's `modelAuthored`
  classification to true for every wire write, via a classification-only input
  distinct from the `MemoryAttribution` value persisted onto the resulting revision.
  The forcing helper delegates to `withProtoAttribution` so persisted provenance
  (including `MemorySource.ProposalID`) cannot drift from it.
  `ForgetVersioned`/`UndoLatest` are unchanged (no scan to bypass).

**Acceptance:**
- AC1.1: A `RememberVersioned` call over the `MemoryStoreService` gRPC RPC with
  `Attribution.Writer` unset or set to `"user"` on an instruction-shaped
  `user/`-scoped value is rejected with `ErrInstructionMemory`, identically to a
  model-authored in-process call.
  - verify: `TestADR_0226_WireAttributionCannotBypassInjectionScan`
- AC1.2: A legacy `RememberEntry` call over the same gRPC RPC on an instruction-shaped
  `user/`-scoped value is likewise rejected, closing the pre-existing zero-attribution
  gap on that handler.
  - verify: `TestADR_0226_LegacyRememberEntryWireCannotBypassInjectionScan`
- AC1.2b: EVERY `MemoryStoreService` RPC whose request carries a `MemoryEntry` —
  `RememberEntry`, `RememberVersioned`, and `RememberIfCurrent` — rejects an
  instruction-shaped `user/` value under both an absent and a `"user"`-claiming
  `Attribution`, asserted as one table so a fourth write RPC cannot skip the gate.
  - verify: `TestADR_0226_ContentBearingWriteRPCsForceInjectionScan`
- AC1.3: An in-process call with genuine `MemoryAttribution.Writer == MemoryWriterUser`
  on an instruction-shaped `user/` value is still accepted — the legitimate
  human-preference exemption is unchanged.
  - verify: `TestADR_0226_InProcessAttributionExemptionPreserved`
- AC1.4: A `RememberVersioned`/`RememberEntry` call over the gRPC RPC with a genuine,
  non-instruction-shaped value persists the revision's `Writer`/`Origin` exactly as
  the request's `Attribution` reported — the scan-classification override does not
  corrupt persisted provenance for an ordinary write.
  - verify: `TestADR_0226_WireProvenancePreservedUnderScanOverride`
- AC1.5: No code path other than the scan-classification input reads
  `MemoryAttribution.Writer`/`Origin` for a security or authorization decision.
  - verify: inspection — a repo-wide grep for comparisons against `.Writer`/`.Origin`
    confirms the single classification site; not independently unit-testable as one
    assertion.

---

### Scenario 2 — `UndoLatest`'s contract is explicit and its predicates match

[ADR-0226](../adr/0226-memory-lifecycle-hardening.md#decision) states the intended
undo contract for the first time: no redo, and repeated undo on a key walks strictly
backward through revisions never yet undone. `engine/adapter/memmemory/memmemory.go`'s
`UndoLatest` currently applies that filter to target *selection* but not to the
*restore source* (`target-1`), so a sequence exists where it could restore an
already-undone value; separately, `r.undone` is never pruned when `retainLatest`
truncates old revisions, so a hot key accumulates an unbounded map of dead version
keys ([`AGENTS.md`'s resource-inventory discipline](../../AGENTS.md) — any resource
outliving a call needs a bounded lifetime).

**Work:**
- engine domain (`engine/tool`): `MemoryLifecycleStore`'s doc comment states the undo
  contract explicitly.
- adapters (`engine/adapter/memmemory`, `internal/adapter/memory`): `UndoLatest`'s
  restore-source read uses the identical origin/undone filter as target selection;
  `r.undone` entries for revisions dropped by `retainLatest` are pruned in the same
  operation.

**Acceptance:**
- AC2.1: `MemoryLifecycleStore`'s doc comment states the no-redo, strictly-backward
  undo contract.
  - verify: inspection — doc-comment review, documentation-only
- AC2.2: `UndoLatest`'s restore-source uses the same origin/undone filter as its
  target-selection scan, so a Remember→Undo→Undo→Remember→Undo sequence never
  restores a value that was itself already undone.
  - verify: `TestADR_0226_UndoNeverRestoresAlreadyUndoneRevision`
- AC2.3: Repeated `Undo` calls on the same key, with no intervening `Remember`, walk
  strictly backward and terminate in a "no mutation remains to undo" error rather
  than looping or restoring a stale value.
  - verify: `TestADR_0226_RepeatedUndoWalksStrictlyBackward`
- AC2.4: `r.undone` entries for a key are pruned in lockstep with `retainLatest`'s
  truncation, so the map does not grow unboundedly across the store's lifetime.
  - verify: `TestADR_0226_UndoneMapPrunedWithRetainLatest`

---

### Scenario 3 — History truncation is visible, not just eventually fatal

The reference store's 64-revision-per-key cap
(`engine/adapter/memmemory/memmemory.go`'s `retainLatest`) and the local file store's
equivalent `HistoryTruncated` flag are both internal-only today; neither
`tool.MemoryRecord` nor the wire `MemoryRecord` proto message
(`contracts/proto/mecatl/driver/v1/memory_store.proto`) exposes truncation, so a
caller of `Inspect` learns about it only when a later `UndoLatest` fails. Per
[ADR-0226](../adr/0226-memory-lifecycle-hardening.md#decision), both surfaces gain an
explicit truncation signal.

**Work:**
- engine domain (`engine/tool`): `MemoryRecord` gains a `Truncated bool` field.
- adapters (`engine/adapter/memmemory`, `internal/adapter/memory`,
  `internal/adapter/grpcdriver`): both stores populate `Truncated`; the gRPC client
  and server thread a mirrored `history_truncated` proto field.
- ports/contracts (`contracts/proto/mecatl/driver/v1/memory_store.proto`): additive
  `history_truncated` field on `MemoryRecord`.
- adapters (`engine/adapter/memorytools`): `Inspect*`'s tool-result rendering
  surfaces an explicit note when `Truncated` is true.

**Acceptance:**
- AC3.1: `tool.MemoryRecord.Truncated` is `true` whenever the backing store's history
  for that key has been truncated by its retention cap, for both the in-memory
  reference store and the local file store.
  - verify: `TestADR_0226_MemoryRecordExposesTruncation`
- AC3.2: The wire `MemoryRecord.history_truncated` field round-trips from the local
  store's truncation signal through the gRPC server to the client's
  `tool.MemoryRecord.Truncated`.
  - verify: `TestADR_0226_WireHistoryTruncatedRoundTrips`
- AC3.3: `Inspect*`'s model-facing tool result includes an explicit truncation note
  when `Truncated` is true.
  - verify: `TestADR_0226_InspectToolSurfacesTruncationNote`

---

### Scenario 4 — Operator-profile access is an allow-list, not a deny-list

`internal/app/build.go`'s `childOperatorProfileSource` excludes a fixed set of
internal-purpose roles (guardrail-checker, ask-reviewer, model-router,
usermodel-review, judge variants) and defaults to *allow* for anything else — the
opposite polarity from `roleFamily`'s deliberate fail-safe-to-`"child"` bucket a few
lines above in the same file. Per
[ADR-0226](../adr/0226-memory-lifecycle-hardening.md#decision), this inverts: only
recognized first-class role shapes receive the profile. The allow-list must cover
every role shape a first-class engine actually uses today, including the per-call
model-override roles (`"task:model="+model` for Subagent, `"parallel:model="+model`
for Parallel) — an earlier draft of this scenario recognized only the bare `"task"`
and `"parallel"` roles and would have silently stripped the profile from a routed
Parallel branch, a real code path (`internal/app/build.go:5533`), contradicting the
"behavior-preserving for every role recognized today" claim this decision depends on.

**Work:**
- composition (`internal/app`): `childOperatorProfileSource` returns the profile
  source only for the main engine (empty role), Subagent children (`"task"` or a
  `"task:"` prefix, covering the per-call model-override role `"task:model="+model`),
  team members (a `"member:"` prefix), and Parallel branches (`"parallel"` or a
  `"parallel:"` prefix, covering the per-branch model-override role
  `"parallel:model="+model`, `internal/app/build.go:5533` — a real, already-shipped
  role an earlier draft of this plan would have silently regressed); every other role
  returns `nil`.

**Acceptance:**
- AC4.1: The main engine, a Subagent child (both the plain `"task"` role and the
  `"task:model="` override role), a team member, and a Parallel branch (both the
  plain `"parallel"` role and the `"parallel:model="` override role) each receive a
  non-nil `OperatorProfileSource`.
  - verify: `TestADR_0226_OperatorProfileAllowListIncludesFirstClassRoles`
- AC4.2: `guardrail-checker`, `ask-reviewer`, `model-router`, `usermodel-review`,
  `parallel-judge` (and every other judge variant), and a synthetic role invented by
  the test that matches nothing in the allow-list all receive a `nil`
  `OperatorProfileSource`.
  - verify: `TestADR_0226_OperatorProfileAllowListDefaultExcludes` (supersedes
    `TestInternalPurposeChildRolesExcludeOperatorProfile`'s deny-list assertion)

---

### Scenario 5 — Driver wire hygiene: uniform error mapping, bounded `Inspect`

`internal/adapter/grpcdriver/memorystore.go` maps a driver's `UNIMPLEMENTED` response
to a clean error in `ForgetVersioned`/`UndoLatest` but not in
`RememberVersioned`/`Inspect` — and since `versionConflict` calls `Inspect`
internally, a driver reporting `lifecycle=true` while omitting `InspectMemory` turns
an ordinary version conflict into a raw `Unimplemented` RPC error. Separately,
`InspectMemoryRequest` has no cap on returned history size (a unary RPC against a
driver with unbounded history can exceed the default gRPC recv limit), and the
version-token fields lack the `buf.validate` size bound the sibling `entry` field
carries. Per [ADR-0107](../adr/0107-operator-profile-memory-lifecycle.md)'s decision
that "an old `UNIMPLEMENTED` capability response is base-only, never a partial
lifecycle plan," the four lifecycle client methods must degrade identically — a
driver's incomplete lifecycle support should never look like a different failure
mode depending on which method happened to hit it first.

**Work:**
- adapters (`internal/adapter/grpcdriver`): `RememberVersioned` and `Inspect` map
  `UNIMPLEMENTED` the same way `ForgetVersioned`/`UndoLatest` already do.
- ports/contracts (`contracts/proto/mecatl/driver/v1/memory_store.proto`): additive
  optional `max_revisions` field on `InspectMemoryRequest`; `buf.validate` max-size
  annotations on `expected_version` and `version`.
- adapters (`internal/adapter/memory`, `internal/adapter/grpcdriver`): the local
  reference driver honors `max_revisions` by returning at most that many most-recent
  revisions.

**Acceptance:**
- AC5.1: All four `grpcdriver.MemoryLifecycleStore` client methods
  (`RememberVersioned`, `Inspect`, `ForgetVersioned`, `UndoLatest`) map a driver's
  `UNIMPLEMENTED` response identically.
  - verify: `TestADR_0226_UnimplementedMappedUniformlyAcrossLifecycleMethods`
- AC5.2: A version conflict against a driver missing `InspectMemory` surfaces a clean
  `ErrMemoryLifecycleUnsupported`-shaped error, not a raw `Unimplemented` status.
  - verify: `TestADR_0226_VersionConflictHandlesMissingInspect`
- AC5.3: `InspectMemoryRequest.max_revisions`, when set, bounds the local reference
  driver's returned revision count to the most recent N.
  - verify: `TestADR_0226_InspectHonorsMaxRevisions`
- AC5.4: `expected_version` and `version` carry the same `buf.validate` size-bound
  discipline as `entry`.
  - verify: inspection — proto/buf-lint review

---

### Scenario 6 — Tooling and API hygiene sweep

Four independent, low-risk cleanups a panel review found: the six new lifecycle
tools are absent from the caller-owned-tool audit list; `forgetTool`'s description
doesn't disclose that Forget is reversible; `engine/tool/memorylifecycle.go` exposes
five overlapping validation entry points where two would do; and three `sort.Slice`
call sites could be the Go 1.26 stdlib idiom.

**Work:**
- adapters (`internal/adapter/server/classification.go`): add the six lifecycle tool
  names to `modelToolAccessTable`/`ModelToolBoundaries`.
- adapters (`engine/adapter/memorytools`): `forgetTool.Spec()`'s description states
  that Forget is a reversible tombstone, readable via `Inspect*`.
- engine domain (`engine/tool`): collapse `ValidateMemoryWrite`, `ValidateMemoryEntry`,
  and `ValidateMemoryContent` (the zero-attribution shims) into their
  attribution-aware siblings (`ValidateMemoryEntryWrite`, `ValidateMemoryContentWrite`),
  updating every call site.
- adapters (`engine/adapter/memmemory`, `internal/adapter/memory`): `sort.Slice` →
  `slices.SortFunc` + `cmp.Compare` at the three identified call sites.

**Acceptance:**
- AC6.1: `ModelToolBoundaries`/`modelToolAccessTable` include `ForgetMemory`,
  `ForgetUserMemory`, `InspectMemory`, `InspectUserMemory`, `UndoMemory`, and
  `UndoUserMemory`.
  - verify: `TestADR_0226_LifecycleToolsClassified`
- AC6.2: `forgetTool.Spec().Description` states plainly that the operation is
  reversible and the underlying value remains readable via Inspect.
  - verify: `TestADR_0226_ForgetToolDisclosesReversibility`
- AC6.3: `engine/tool`'s public validation surface is exactly
  `ValidateMemoryEntryWrite` and `ValidateMemoryContentWrite`; the three shims are
  removed, `engine/api/tool.txt` reflects the removal, and `engine/CHANGELOG.md`
  records it as Removed/breaking per `engine/COMPATIBILITY.md`.
  - verify: demonstration — `task api:check` passes against the regenerated
    `engine/api/tool.txt`, and the `engine/CHANGELOG.md` entry is present
- AC6.4: `memmemory.go` and `store.go`'s three `sort.Slice` call sites are
  `slices.SortFunc`/`cmp.Compare`, with unchanged ordering behavior.
  - verify: existing `List`/`Search` ordering tests in `engine/adapter/memmemory` and
    `internal/adapter/memory`, unmodified, continue to pass

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Gating writes to a tombstoned key behind the same approval `Forget` required (the property the Undo/Forget asymmetry gestures at) | A future design pass, scoped on its own | [ADR-0226](../adr/0226-memory-lifecycle-hardening.md#context) |
| Harmonizing `RememberVersioned`'s empty-`expected` semantics with ADR 0208's file-Workspace CAS discipline | Not planned — treated as a permanent, intentional divergence | [ADR-0226](../adr/0226-memory-lifecycle-hardening.md#context) |
| General `buf.validate` size-bound hardening across every driver RPC (not just the memory-lifecycle ones touched here) | A separate proto-hardening pass | [ADR-0226](../adr/0226-memory-lifecycle-hardening.md#consequences) |
| A reflective/catalog-walking completeness test for `ModelToolBoundaries` (so a *future* caller-owned tool can't silently go unclassified the way this one did) | Deferred; this plan only pins today's six names | none — noted as a known risk below |
| Server-enforced `max_revisions` against a driver that ignores the cap | Deferred; this plan hardens the reference driver only | [ADR-0226](../adr/0226-memory-lifecycle-hardening.md#consequences) |

## Sequencing recommendation

Scenario 1 (the live security gap) should land first and can ship alone if needed.
Scenarios 2–3 (undo correctness + truncation visibility) touch the same store
implementations and are natural to land together. Scenario 4 (operator-profile
allow-list) is fully independent of 1–3 and 5–6. Scenarios 5–6 are independent,
low-risk cleanups that can land in any order, including in parallel with
everything else.

Every scenario here is confined to the memory subsystem and its driver boundary.
That is a deliberate scope line: consolidating the two injection phrase lists was
considered and cut, because `engine/tool.DirectiveShapedUserMemory` is also the
operator-profile RENDER filter, so touching it reaches outside this subsystem and
can silently drop stored facts from the prompt. See *Deferred decisions* below and
[ADR-0226](../adr/0226-memory-lifecycle-hardening.md#consequences).

## Named tests landing in this plan

- `TestADR_0226_WireAttributionCannotBypassInjectionScan`
- `TestADR_0226_LegacyRememberEntryWireCannotBypassInjectionScan`
- `TestADR_0226_ContentBearingWriteRPCsForceInjectionScan`
- `TestADR_0226_InProcessAttributionExemptionPreserved`
- `TestADR_0226_WireProvenancePreservedUnderScanOverride`
- `TestADR_0226_UndoNeverRestoresAlreadyUndoneRevision`
- `TestADR_0226_RepeatedUndoWalksStrictlyBackward`
- `TestADR_0226_UndoneMapPrunedWithRetainLatest`
- `TestADR_0226_MemoryRecordExposesTruncation`
- `TestADR_0226_WireHistoryTruncatedRoundTrips`
- `TestADR_0226_InspectToolSurfacesTruncationNote`
- `TestADR_0226_OperatorProfileAllowListIncludesFirstClassRoles`
- `TestADR_0226_OperatorProfileAllowListDefaultExcludes`
- `TestADR_0226_UnimplementedMappedUniformlyAcrossLifecycleMethods`
- `TestADR_0226_VersionConflictHandlesMissingInspect`
- `TestADR_0226_InspectHonorsMaxRevisions`
- `TestADR_0226_LifecycleToolsClassified`
- `TestADR_0226_ForgetToolDisclosesReversibility`

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — `llms.txt` regenerated and the matlatl strict link gate green.
3. `task generate` — proto changes (Scenarios 3, 6) regenerated via buf, committed.
4. `task api:check` passes (or `task api:update` was run and the
   `engine/CHANGELOG.md` note is present) — Scenario 6's validation-surface collapse
   and Scenario 3's `MemoryRecord.Truncated` field both touch the engine's exported
   surface.
5. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is
   `landed`).
6. The named tests above are green and grep-locatable by their identifiers.
7. `go run ./cmd/mecademo` still prints a full offline session.
8. `docs/adr/0226-memory-lifecycle-hardening.md` is unedited from this plan's landing
   (ADRs are frozen once Accepted).

## Deferred decisions and known risks

- **The three memory→prompt render sinks disagree about the directive-shape filter.**
  `engine/prompt/operatorprofile.go`'s `operatorProfileEntryAllowed` applies both
  `SecretShapedMemoryValue` and `DirectiveShapedUserMemory`; `engine/prompt/memoryindex.go`
  and `engine/prompt/usermodel.go` render `key + description` behind the secret check
  ONLY. So instruction-shaped text in a *description* reaches the tier-0 index and the
  user-model block even though the operator profile would drop it. This is pre-existing
  and out of scope here — but it is the reason Scenario 1's write-time gate is
  load-bearing rather than merely redundant with the render layer, and it should be
  resolved deliberately (align the sinks, or record why they differ) rather than left
  as an accident. Related to, but separate from, the phrase-list item below.
- **Consolidating the two instruction-injection phrase lists is deliberately NOT in
  this plan.** `engine/tool.DirectiveShapedUserMemory` and
  `engine/adapter/skillfs.ScanForInjection` do maintain overlapping deny-lists for the
  same attack shape and have drifted, so merging them looks like free hygiene. It is
  not. The lists are not equivalent in either direction, so there is no neutral
  "merge" — whichever becomes canonical changes the other's consumers. And
  `DirectiveShapedUserMemory` has a second consumer with the opposite cost profile:
  `engine/prompt/operatorprofile.go`'s `operatorProfileEntryAllowed` applies it at
  RENDER time, unconditionally and with no attribution gate, so widening it does not
  merely reject new writes — it drops already-stored, previously-rendered operator
  facts from the prompt every turn, with no error, no diagnostic, and no entry in the
  profile's own `omitted` count (disallowed entries leave `active` before `omitted` is
  computed, so the block can render empty). A write-time rejection is loud and
  recoverable; a render-time drop is silent and permanent. Any future convergence must
  be evaluated against BOTH consumers and must converge on the line-anchored
  semantics, not away from them. See
  [ADR-0226](../adr/0226-memory-lifecycle-hardening.md#consequences).
- **Forcing the scan at the wire boundary is a real, permanent capability loss for a
  legitimate trusted-remote-driver deployment, not merely "extra scan work."**
  `MemoryStoreService` is a documented driver-protocol surface a separate trusted
  process can implement; a real human writing a genuinely instruction-shaped
  preference through such a remote client will now be unconditionally rejected, with
  no override, because the classification can no longer distinguish that peer from an
  untrustworthy one. This is accepted as a deliberate fail-safe over an unverifiable
  boundary, not treated as free.
- **Tombstone-write re-approval.** The Forget/Undo asymmetry's real fix — requiring
  the same approval to write to a tombstoned key as was required to delete it,
  regardless of which tool performs the write — is out of scope and unresolved. It
  is a materially larger design than this plan and needs its own scoping pass.
- **`ModelToolBoundaries` can still silently drift.** Scenario 6 pins today's six
  tool names but does not add a structural completeness check (e.g., walking the
  real tool catalog and asserting every caller-owned tool is classified). A future
  caller-owned tool can reintroduce the same gap this plan closes for today's six.
- **`max_revisions` is driver-cooperative only.** A non-compliant or hostile remote
  driver can still return unbounded history; this plan hardens the reference local
  driver and the request contract, not enforcement against an arbitrary driver
  implementation.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is
satisfied.
