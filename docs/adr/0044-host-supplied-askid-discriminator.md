# ADR 0044 — Host-supplied askID discriminator (cross-process-reconstructable askID)

- Status: Accepted
- Date: 2026-06-22
- Scope: the agent loop's permission-ask identifier (`agent.newAskID` + `agent.RunOptions`) — how the trailing component of an askID is derived, so a durable host can reconstruct an askID across processes for cross-process resume/correlation.

## Context

A permission ask is identified by an askID minted in `newAskID` (`engine/agent/dispatch.go`)
with the grammar:

```
<sessionID>:<n>:<callID>:r<runSerial>
```

The three leading components are reconstructable from persisted session state: the
session id, the per-session tool-call counter `n`, and the provider-assigned tool-call
id. The trailing component, `r<runSerial>`, is NOT: `runSerial` (`engine/agent/loop.go`)
is a process-global `atomic.Int64` that resets to zero on every process start. It exists
as a CWE-863 replay guard — two RUNS of the same session must mint disjoint askIDs, so a
stale/queued verdict for a retracted ask (cancel a parked ask, then `resume` the same
child id in the same parent run where `Counters` reset on Interrupt and the provider
re-mints the same call id) cannot resolve a re-minted ask of a different attempt.

Because the trailing component is process-local and non-deterministic, a host that
restarts (or a second process) cannot reproduce the askID for a session it loaded from
durable storage. The session lease (#60/#119) fixes WHERE a run is driven (routing); it
does not give the host a stable askID to CORRELATE a persisted pending ask with. Today
the downstream consumer works around this by synthesizing its OWN `resumeAskID` and parsing the askID
grammar to extract the call id (a downstream-consumer issue #552) — fragile coupling to an internal
format.

Separately, the durable approval-replay path (ADR 0027 Phase 3b) already STOPPED parsing
the askID grammar: it correlates an allow-always verdict back to its `ToolCall` via the
structured `session.ApprovalPayload.Call` field (`internal/app/approvalreplay.go`), NOT a
`callIDFromAskID` grammar parse (no such function exists in the code). The request half of
that correlation — the pending ask — had no equivalent structured field.

## Decision

Two additive, opt-in changes, both following the run-scoped `RunOptions`/`Deps` seam
discipline (a per-call knob lives on the `Run`, never mutating the shared engine — the
same pattern as `MaxRunTokensOverride`/`ExtraTools`, ADR 0077, and the driver-seam
opt-in convention of ADR 0005):

1. **`session.PendingAsk.Call` (`session.ToolCallID`)** — surface the gated tool-call id
   on the ask REQUEST half, the structured twin of `ApprovalPayload.Call` on the verdict
   half. It round-trips in the sessnap snapshot (it is correlation data, not run-scoped
   policy state like `ConfiguredAsk`/`FlooredConfiguredAllow`), so a host pauses on a
   `PendingAsk` and reads `Call` instead of parsing the askID grammar. It is an OPAQUE
   identifier already implicitly encoded inside the askID, so surfacing it opens no new
   leak surface. (#148)

2. **`agent.RunOptions.AskIDDiscriminator` (`string`)** — when non-empty and colon-free,
   it REPLACES the `r<runSerial>` trailing component of every askID minted this run, so
   the askID becomes `<sessionID>:<n>:<callID>:<discriminator>` — fully reconstructable
   across processes from persisted state. `newAskID` now takes the resolved discriminator
   STRING rather than the `int64` serial; `startRun` resolves it once per run (host value
   when valid, else `fmt.Sprintf("r%d", serial)`). A durable host (e.g. a downstream consumer) passes
   its own RunID. (#117)

## Constraints

The change must hold four invariants:

1. **Preserve the `<sessionID>:` prefix.** `cmd/mecatui`'s `isChildAsk` classifies a
   surfaced ask as main-agent vs subagent purely by the leading session-id prefix. The
   discriminator is a SUFFIX, so the consumed prefix contract is untouched.

2. **Preserve the CWE-863 replay guard.** The process-global serial guaranteed
   disjointness automatically. A host discriminator inherits the SAME guarantee only via
   a host CONTRACT: the value MUST be unique-per-run-ATTEMPT AND stable-per-attempt
   across processes. A stale verdict for a retracted ask must never resolve a re-minted
   ask of a different attempt.

3. **Colon-free grammar.** A colon would make the askID grammar ambiguous. A
   colon-containing discriminator is IGNORED and the run falls back to `r<serial>` with a
   WARN — NOT stripped (stripping could collapse two distinct host ids onto one askID and
   re-open the replay collision), and NOT a hard error (that would force a breaking
   error-path onto the run-entry signature and violate "no change when unset"). The
   fallback resolution lives in `startRun`, not `newAskID`.

4. **No behaviour change when unset.** `Run`/`RunContent` build a zero `RunOptions`, so
   the empty discriminator resolves to `r<serial>` and reproduces today's exact askID
   format byte-for-byte. mecatui, the tests, and in-memory hosts pass nothing and are
   unaffected.

## Consequences

- A durable host can reconstruct an askID across processes from persisted state alone,
  unblocking a downstream-consumer issue #581 — the downstream consumer passes its durable RunID as the discriminator
  and drops both its synthesized `resumeAskID` and the askID-grammar parse
  (a downstream-consumer issue #552).
- The CWE-863 guard is now SHARED responsibility when a discriminator is supplied: the
  engine no longer guarantees disjointness on its own — the host contract carries it. The
  doc-comments on `RunOptions.AskIDDiscriminator` and `newAskID` state the contract
  explicitly. The LOAD-BEARING obligation an adopting host (e.g. a downstream consumer for
  a downstream-consumer issue #581) must honour is that the discriminator is unique per run-ATTEMPT,
  NOT merely stable per session: a per-session-stable value would re-mint an identical
  askID after a cancel-then-resume on the same session, re-opening exactly the
  retract/re-mint replay collision the process-global serial closed. "Stable" in the
  contract means stable across processes for the SAME attempt only — a fresh attempt
  (including a resume after Interrupt) MUST carry a fresh discriminator. the downstream consumer's durable
  per-run RunID satisfies both halves.
- `newAskID`'s signature changed from `int64` to `string`; this is an internal function
  (no public API), but its callers and the per-RUN serial reader (`startRun`) move in
  lockstep.
- This corrects the stale `callIDFromAskID` claim that lingered in the docs: approval
  replay correlates via the structured `ApprovalPayload.Call`, not a grammar parse, and
  there is no `callIDFromAskID` function in the code.

### Enforcement tests

- `engine/agent/dispatch_internal_test.go`: `TestNewAskIDDiscriminatorReconstructable`
  (same inputs → identical askID; distinct discriminators → disjoint),
  `TestNewAskIDNoDiscriminatorMatchesSerial` (the `r<serial>` fallback reproduces the
  legacy format), `TestNewAskIDDiscriminatorPreservesPrefix` (the `<sessionID>:` prefix
  holds).
- `engine/agent/resume_approval_test.go`: `TestRunOptionsAskIDDiscriminatorReplacesSerial`
  (positive), `TestRunOptionsAskIDDiscriminatorColonFallsBack` (colon → `r<serial>`
  fallback, AND asserts the operator WARN fired),
  `TestAskIDDiscriminatorReconstructableAcrossRuns` (end-to-end: two independent runs over
  the same session id + discriminator mint a byte-identical emitted askID),
  `TestPendingAskCarriesGatedCallID` (#148 main path).
- `engine/agent/surfaced_ask_e2e_test.go`:
  `TestE2E_SurfacedChildAskCarriesChildGatedCallID` (#148 surfaced-child path).
- `engine/adapter/sessnap/sessnap_test.go`: `TestRoundTripAwaitingPreservesPendingAsk`
  (the `PendingAsk.Call` snapshot round-trip).

## See also

- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — the run-entry resume seams and
  the `ApprovalPayload.Call` verdict-half correlation this completes.
- [ADR 0077 — Direct-write writable Subagent](./0077-direct-write-subagent.md) — the
  run-scoped `RunOptions` opt-in seam pattern.
- [ADR 0005 — Driver seams](./0005-driver-seams.md) — the opt-in seam convention.
- The living grammar/correlation reference in [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md).
