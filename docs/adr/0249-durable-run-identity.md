# ADR 0249 — Durable run identity: a host-minted `run_id`

- Status: Proposed
- Date: 2026-08-28
- Scope: run identity minting, persistence, event stamping, and the
  `expected_run_id` guard on approve/cancel/steer.

## Context

[Issue #821](https://github.com/stacklok/mecatl/issues/821)'s SDK contract needs a stable
handle for one run: `await session.run(prompt)` resolves "only after server acceptance
and the first run-ID-bearing event", `session.attach(runId?)` follows exactly one run,
and stale controls must "fail without touching a newer run".

mecatl has no such identifier. A run today is identified only by *being* the live run of
a session — `Service.LookupRun(id)` finds the in-flight one, and `session.Event.Seq` is
monotonic **within a run** but restarts every run, so it cannot distinguish two runs of
the same session. This is fine for a client holding one bidi stream, which is what
`mecatui` does. It fails the moment a second client attaches, or the same client
reconnects: "approve ask X" and "cancel" are addressed to *the session*, so a control
that was in flight while a run ended lands on whatever run started next. That is a
correctness bug today, not merely an SDK ergonomics gap.

The obvious shape — mint the id inside `agent.Engine` when a run starts — cannot satisfy
the requirement that resuming an awaiting approval "remains the same run". The engine has
no way to know that a previous process already minted an id for this attempt; that fact
lives in persisted session state, which the engine is deliberately storage-agnostic
about.

There is, however, an existing seam built for precisely this value.
[`agent.RunRequest.AskIDDiscriminator`](../../engine/agent/loop.go) ([ADR 0044](./0044-host-supplied-askid-discriminator.md))
carries a host-supplied component of every askID minted during a run, and its contract
is already exactly what a run id must satisfy:

> the host MUST supply a value that is (a) UNIQUE per run-ATTEMPT and (b) STABLE across
> processes for the SAME attempt … (c) colon-free … **A durable host (e.g. a downstream
> consumer) passes its own RunID.**

Nothing in `internal/` or `cmd/` supplies it today. It is an unused socket, specified in
terms of a run id that does not yet exist.

The *stamping* half looked at first like `session.Event.Actor`
([ADR 0204](./0204-caller-identity-threading.md)), documented as "LOG-ONLY and
DERIVE-AT-APPEND: every emit site — the loop included — leaves it nil … and the server
relay's single `appendEvent` chokepoint stamps it". Following that shape was the initial
plan, and it does not work here: `Actor` is log-only, so persistence has the single
chokepoint it needs, whereas a run id must also reach the client wire, which never passes
through `appendEvent`. Decision 4 below records where that leads and why.

## Decision

**1. The run id is minted by the host (`Service`), not the engine, from
`crypto/rand`.** It is opaque and colon-free — the askID grammar is
`<sessionID>:<n>:<callID>:<discriminator>`, so colons are structurally forbidden and a
colon-bearing value is already specified to be ignored with a WARN.

**Minting is centralised and cryptographic because the run id becomes the askID
discriminator, and that discriminator is a security control.**
[ADR 0044](./0044-host-supplied-askid-discriminator.md) requires it to be unique per
run-attempt to preserve the CWE-863 replay guard: if two attempts can share one, a stale
verdict from attempt A can resolve an ask in attempt B. Today's process-global `r<serial>`
counter makes that impossible by construction, and an id that is merely validated as
colon-free would trade that for caller-trust — a downstream host embedding
`engine/agent`, or mecatl's own `Service` across a restart or a second replica, would only
have to reuse one value to reopen the hole.

Two shapes were considered. The first was to split the concerns: keep the host value as a
client-facing label for `attach()`/`expected_run_id` filtering, which only needs to be
distinguishable, and derive the discriminator by appending a per-session monotonic counter
minted by the `Session` aggregate. The second — the one taken — is to mint the whole id
from 128 bits of `crypto/rand` at **one** call site
(`runid.go` in the server adapter). It is the stronger of the two: a monotonic counter
has to survive restarts and replicas to stay monotonic, so it moves the burden onto a
persistence/CAS scheme that can itself be got wrong, whereas 128 random bits collide with
negligible probability regardless of how the process is restarted or how many replicas
mint concurrently. A colliding id therefore is not a security bypass but an arithmetic
impossibility, and the label and the discriminator can stay the same value.

The public `RunRequest.RunID` field remains settable by an embedder, which is what ADR
0044 intends; that residual is why the UTF-8 mapper repair extends to `RunId`.

**2. It reaches the engine as a new `RunRequest.RunID` field**, and the engine derives
the ask discriminator from it when `AskIDDiscriminator` is empty. The host therefore sets
**one** field, and [ADR 0044](./0044-host-supplied-askid-discriminator.md)'s "a durable
host (e.g. a downstream consumer) passes its own RunID" is satisfied literally rather
than by coincidence. `AskIDDiscriminator` is retained for compatibility and gains no
second writer.

**3. It is persisted on the session snapshot** as `sessnap.Snapshot.RunID`
(`omitempty`, additive, no format-tag bump — the `Profile`/`ProviderID`/`Usage`
precedent). This is what makes awaiting-resume *the same run*: the resume path **reuses
the persisted id rather than minting a new one**, which is how ADR 0044's "stable across
processes for the same attempt" obligation is discharged mechanically instead of assumed.

**4. The LOOP stamps `session.Event.RunID`, at `Run.emit`/`emitOrAbort`.**
`eventsource.Fold` ignores it, as it does `Actor`.

This is the one place this ADR departs from the `Actor` precedent that otherwise shapes
it, and the departure is deliberate on two independent grounds.

*Mechanically, the relay chokepoint does not exist.* `Actor` can be stamped inside
`appendEvent` because it is **log-only** — persistence genuinely has a single chokepoint.
`RunID` must also reach the **client wire** (the SDK resolves a started run on the first
run-ID-bearing event, and the watch layer filters envelopes by run), and the wire path
never passes through `appendEvent`. `Service.relayEvent` is not a substitute: it takes
`session.Event` **by value**, so it cannot mutate the copy its caller hands to `toProto`,
and five further `recorder.Observe` sites bypass it entirely. Stamping "at the relay"
means stamping at roughly nine sites by hand — the per-surface drift class
[`AGENTS.md`](../../AGENTS.md) records as having already fired three times.

*Semantically, the exclusion argument does not transfer.* `Actor` stays out of the loop
because the loop "knows nothing about principals" — a real domain boundary. A run id is
not external metadata: the run is the **loop's own concept**, and the loop already labels
every event it emits with run-scoped identity (`ev.Seq = r.seq.Add(1)`, at exactly the
two sites in question). `RunID` belongs on the next line. The loop learns nothing about
storage, transport, or callers; it learns the name of the thing it already is.

A host that supplies no `RunID` emits events with an empty one, byte-identical to the
behaviour before this ADR.

**5. Each run-entry seam mints or reuses exactly one id:**

| Seam | Action |
| --- | --- |
| `Service.StartRunContent` → `engine.Run` (covers `StartRun`, `RetryFailedRun`, scheduler fires, steer-promotion) | mint |
| `Service.resumeFromAwaiting` → `engine.ResumeApproval` | reuse persisted |
| `Service.ApprovePlan` | reuse for the resumed run; mint for the continuation run |

The plan case is why #821 has `session.resolvePlan()` return a `PlanResolution` rather
than a `Run`: it may stream a resumed run followed by a new continuation run with a
different id.

**6. An empty `RunID` is a meaning, not a bug: "session-scoped, not run-scoped".** It is
enforced by a **closed enumerated set** of run-less event types, with a test — the
`harnessTokenFields` idiom, where adding an entry is a deliberate, visible act. Today the
set is exactly the three `schedule.*` lifecycle events, which
[`internal/adapter/server/schedule.go`](../../internal/adapter/server/schedule.go) appends
from the scheduler in composition, outside any loop. Any other event type reaching the
append chokepoint with an empty `RunID` fails CI.

The consequence falls out cleanly onto the API #821 already specifies: `attach()` filters
to events matching one run id and therefore never contains run-less events — correct,
because an attachment "replays that run then follows through its terminal result" and a
schedule-fired notice has no terminal result. `activity()` returns everything. Run-less
events are precisely *why* `activity()` must exist as a separate operation rather than
sugar over `attach()`.

**7. `expected_run_id` is an optional field on approve/cancel/steer controls.** When
supplied and mismatched, the control fails without touching the newer run. The SDK always
supplies it. It stays optional on the wire so existing clients — `mecatui` — are
unaffected.

## Consequences

**Easier.** Stale controls become impossible to land on the wrong run, closing a real
current bug. Multi-client and reconnecting-client scenarios become expressible. ADR
0044's askID reconstruction becomes reachable in production instead of theoretical.

**Harder — the honest costs.**

- **Every run-entry seam is now load-bearing for identity.** Adding a fourth seam that
  drives the engine without minting or reusing an id produces events the watch layer
  cannot attribute. The closed-set test catches the *event-type* case, not a new seam
  that reuses an existing type.
- **The empty-`RunID` carve-out is a permanent two-tier model.** Consumers must
  understand that `attach()` and `activity()` return different things for the same
  session, and that a session whose only log content is `schedule.*` events yields
  `NoRunsError` from `attach()` while `activity()` succeeds. That asymmetry is deliberate
  and will look like an inconsistency to anyone who has not read this ADR.
- **Engine public API grows.** `session.Event.RunID`, `agent.RunRequest.RunID`, an
  aggregate accessor and mutator, and `sessnap.Snapshot.RunID`. All **Added = minor**
  under [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md), requiring
  `task api:update` and a changelog entry.
- **The loop now carries a run identifier it did not previously have.** That is a real
  widening of what `engine/agent` knows, and it is the cost of decision 4. The bound we
  accept is narrow: the loop may STAMP the id and DERIVE the ask discriminator from it,
  and nothing else. It must never branch on it, log it, send it to a provider, or use it
  to reach storage — the moment it does, the storage-agnostic posture is gone and this
  decision was the door.
- **Two derived values now share one source.** `Event.RunID` and the ask discriminator
  both come from `RunRequest.RunID`. If a future requirement needs an ask discriminator
  that is *not* the run id, `AskIDDiscriminator` is still there to set explicitly — the
  derivation only applies when it is empty — so the coupling is a default, not a
  constraint.
- **A legacy snapshot has no `RunID`.** It restores empty; the next run stamps one. There
  is no migration sweep, matching the `Profile`/`ProviderID` precedent.

## See also

- [Issue #821](https://github.com/stacklok/mecatl/issues/821); [`docs/acceptance/sdk-server-enablers.md`](../acceptance/sdk-server-enablers.md).
- [ADR 0044](./0044-host-supplied-askid-discriminator.md) — the host-supplied askID
  discriminator whose seam this fills.
- [ADR 0204](./0204-caller-identity-threading.md) — `Event.Actor`, the derive-at-append
  stamping precedent.
- [ADR 0038](./0038-event-sourced-rehydration.md) — the fold that ignores `RunID`.
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md) and [ADR 0250](./0250-durable-cursors-and-watch.md)
  — the sibling decisions in this stack.
- [`AGENTS.md`](../../AGENTS.md) — the run-entry seam inventory and the storage-agnostic
  loop invariant.
