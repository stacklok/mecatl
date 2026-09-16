# ADR 0288 — TypeScript SDK durable attachment: the watch envelope union, attach/activity semantics, and reconnect authority

- Status: Accepted
- Date: 2026-09-02
- Scope: `sdk/typescript/src/` — the `WatchSessionEvents` envelope union, `SessionActivity` / `AttachedRun`, `session.attach()` / `session.activity()`, cursor encoding and checkpointing, the reconnect authority, the attachment error vocabulary, and the connection-status monitor. Client-side only.
- Superseded by: [ADR 0346](./0346-run-id-addressed-prompt-free-controls.md) (Decision 6 only)

## Context

[Issue #821](https://github.com/stacklok/mecatl/issues/821)'s M2 is "durable
attachment and connection authority". Its first two work items — the cursor
storage seam and the watch transport — **already landed on `main`** under
[ADR 0250](./0250-durable-cursors-and-watch.md): `port.CursorEventLog` with
four backends and a shared conformance suite, the `WatchSessionEvents` gRPC
method, the `GET /v1/sessions/{id}/watch` SSE route, and the
`watch_session_events` feature identifier. M2's third item — "SDK
continuity" — is what remains, and it is entirely inside `sdk/typescript/`.

[ADR 0279](./0279-typescript-sdk-architecture.md) settled the SDK's
architecture (Connect-ES v2, protobuf-es, the injected-Transport seam, the
in-repo pnpm project) and M1 shipped `Client`/`Session`/`Run` on top of it.
0279 deliberately did **not** settle attachment: its Decision 5 says only
that "the milestone that lands each one owns its client-side lifecycle
contract (M2 attachment, M3 spawn/tools)". This ADR is that contract.

#821 states the *behaviour* — prefer the active run then the latest,
`NoRunsError`, replay-then-follow through terminal, filter log-only events by
default with an opt-in, advance the checkpoint when the consumer asks for the
next event, reconnect indefinitely but retry only classified-idempotent
reads. Reading the shipped server surface closely turns up **seven** things it
does not settle, each of which would otherwise be invented differently by two
different implementers.

**1. The envelope has three inhabited shapes and no discriminant.**
`WatchSessionEventsResponse` is `{event, cursor, phase}` where `event` is
absent on the replay→live marker *and* on every gap frame, and `phase` is a
deliberately **open string** ([ADR 0250](./0250-durable-cursors-and-watch.md)
Decision 5, mirroring the `AGENTS.md` discipline that event `type`/`stop` are
string passthroughs). A TypeScript consumer cannot `switch` on anything the
wire provides: `phase === "live"` covers both the boundary marker (no event)
and every subsequent real event (with one). The M1 event union solved the
same problem with `{ kind: "unknown", wireKind }`; the envelope needs the
analogous treatment, and the arms cannot be quietly changed after `v0.1.0`.

**2. There is no RPC that reports a session's active run.**
`attach(runId?)` must "prefer the active run and otherwise the latest". The
proto's `Session` carries `state` (`idle`/`running`/`awaiting`/…) but no
`run_id`, and there is no `ListRuns`. The only place run identity is
observable is `Event.run_id` on the durable log. Since this milestone may not
touch `contracts/proto/`, run selection has to be derived from the log —
which means deciding what it costs.

**3. A cursor is scoped to its `run_id` filter, and only the client can
enforce it.** Both the proto comment and
[`internal/adapter/server/watch.go`](../../internal/adapter/server/watch.go)
are emphatic: a filtered watch advances its position over records the filter
dropped, so handing that cursor back under a *different* filter resumes past
events the other filter would have delivered — "with no signal". The cursor
envelope in
[`engine/port/cursoreventlog.go`](../../engine/port/cursoreventlog.go) is
`{version, session, generation, position}`. **`run_id` is not in it.** The
server structurally cannot catch the mistake; `DecodeCursor` validates the
session and generation and returns a real, wrong position.

**4. The client's headline use case crosses a process boundary, and #821's
own wording hides it.** "Rejoin a running session across a reload" destroys
the JavaScript heap. #821 says the SDK "keeps an in-memory reconnect
checkpoint and exposes cursors for application-owned persistence" — which
means the exposed cursor must survive `JSON.stringify` into `localStorage`
and come back to a *different* `Client` instance. A cursor that is opaque by
virtue of a TypeScript brand satisfies every in-process test and silently
loses its brand on the round trip, defeating point 3 for exactly the case
point 3 exists for.

**5. The live-relay filter is not "the three log-only kinds", and the watch
route applies neither of the server's two filters.** `relayLiveEvent` in
[`internal/adapter/server/grpc.go`](../../internal/adapter/server/grpc.go)
skips `EvApproval` and `EvCompactionArchive` unconditionally but **relays** an
`EvUserPrompt` that is a scheduled-fire delivery note, detected by a
fenced-provenance prefix. Separately, `isPublicEvent` strips
`EvNetworkAttempt` and `EvRequestManifest` from every client surface —
including the `/events` replay route, whose comment says they "remain
debugger-only even on durable read-back". `watchEnvelopeFor` filters on
`run_id` and the gap and **nothing else**, so a watch client is the one
consumer that receives the debugger-only kinds. Any client-side filter list
written as prose will be wrong on day one and rot after that.

**6. There is no prompt-free control channel over gRPC.** Enumerating every
`rpc` in `HarnessService` yields no standalone `Approve`, `Cancel`, or
`Steer`: they exist only as `ConverseRequest` frames, and
[`internal/adapter/server/grpc.go`](../../internal/adapter/server/grpc.go)
enforces "converse: first frame must be a prompt or retry". An attachment
owns no run and therefore has no stream to put a frame on. HTTP is different
— `POST /v1/sessions/{id}/approve` and `/cancel` are prompt-free routes that
rehydrate an awaiting session. So the attached-control surface is
**structurally asymmetric between transports**, and pretending otherwise
would ship acceptance criteria that pass against a fake and fail on the wire.

**7. "Retry only classified idempotent reads" needs a classification on two
axes, not one.** #821 forbids retrying "mutations, prompts, approvals, or
owned runs" — a statement about *operations*. But "reconnect indefinitely"
also needs a statement about *failures*, and #821 makes none. `watchLog`
refuses eagerly for a session the caller may not read, a session that does
not exist, a delegation child session id, a missing cursor seam, and a
missing event log. An attachment that treats those as transport blips becomes
a hot loop against an answer that will not change.

The connection-status monitor is the milestone's second half. M1 shipped the
closed six-value vocabulary and a subscriber-gated heartbeat
([`sdk/typescript/src/client.ts`](../../sdk/typescript/src/client.ts)); M2
adds a *second* long-lived writer to it, which needs an arbitration rule M1
never had to have.

## Decision

**1. The envelope decodes to a four-arm discriminated union keyed on a
client-authored `kind`, with `phase` narrowed on the known arms.**

```ts
type WatchEnvelope =
  | { kind: "event";    phase: "replay" | "live"; cursor: SdkCursor; event: Event }
  | { kind: "boundary"; phase: "live";            cursor: SdkCursor }
  | { kind: "gap";      phase: "gap" }
  | { kind: "unknown";  phase: string;            cursor: SdkCursor; event?: Event };
```

`kind` is **derived, not transmitted** — the same shape M1 chose for events,
so a consumer narrows one way across both unions.

The arm names and the narrowing are load-bearing, in three specific ways:

- **`boundary`, not `phase`.** Every arm carries a phase; the arm that has
  nothing *but* a phase is specifically the replay→live marker. Naming it
  `phase` invites the next known phase-only frame to be folded into the same
  arm with a new string, which reintroduces exactly the silent-normal problem
  the union exists to prevent. `boundary` forces the next one to be a visible
  arm decision.
- **`phase` is narrowed on the known arms.** "Am I rendering transcript or
  live?" is the primary consumer question and `kind` does not answer it. By
  construction of the decoder the known arms *are* known phases, so typing
  them costs nothing now and is a breaking change later.
- **`wireKind` is deliberately absent.** M1's events need it because the wire
  field is `type` and the SDK field is `kind`; here `phase` already *is* the
  raw string and rides the `unknown` arm, so a `wireKind` alias would be two
  names for one value.

**The `unknown` arm is a tripwire, and that is why it carries an optional
`event`.** Because the envelope's shape is fully determined by
event-presence, a three-arm union with an open `phase` string would satisfy
"never coerced to `live`" just as well. The fourth arm earns its place for a
different reason: without it, a future delivery-significant phase — a
`truncated` sibling of `gap` — folds into `kind: "event"` and a consumer's
`switch` treats known-incomplete delivery as normal, which is the precise
failure [ADR 0250](./0250-durable-cursors-and-watch.md) Decision 5 warns
about. Since phase-knownness is orthogonal to event-presence, and "never
dropped" forbids discarding a payload, `event?: Event` follows necessarily.

**The `gap` arm carries no cursor.** The server's gap frame *does* carry one
(`watchEnvelopeFor` returns `rec.Cursor` with `WatchPhaseGap`), and handing
that to a consumer as a resumable SDK cursor would invite resuming past
known-missing records — while the attachment's own checkpoint refuses to
advance for exactly that reason, and `ErrWatchLagging` deliberately carries no
cursor on the same logic. Two halves of one API must not disagree. The raw
seam still surfaces the server's token, because the raw seam takes raw tokens
anyway (Decision 3).

**The boundary is announced once per ATTACHMENT, not once per watch.**
`pumpWatch` emits one `live` marker per watch, so an attachment that
reconnects N times crosses N boundaries. The raw watch surfaces every one; the
**ergonomic attachment yields only the first**. The marker's documented
purpose is to let a client "render the transcript and switch to a live view",
and the consumer's unit is the attachment — a consumer that flapped
transcript→live on every network blip would be reading the frame exactly as
specified and still be wrong. This must be decided here because both readings
satisfy the obvious acceptance criteria and nothing downstream could tell them
apart.

Events inside an envelope are decoded by M1's existing `decodeEvent` — no
second decoder, no second unknown-event convention, so the kind-parity gate
keeps covering the watch path. One honest caveat for the docs:
`RunEventRecorder` **coalesces** streaming text deltas before append, so a
replayed `message.delta` sequence is bounded chunks rather than the live
stream's granularity. Type-identical, granularity-different; harmless when
concatenating, visible to anyone counting deltas.

**2. `attach()` reads ONE unfiltered watch and filters run-side in the
client; a caller-supplied `runId` uses the server filter.**

Because runs are normally serial per session, the active run **is** the newest
run in the log, so #821's two limbs agree and `attach()` needs only "the
newest `run_id` in the log".

The mechanism matters for cost. An earlier draft opened a discovery watch,
learned the run, then opened a *second*, server-filtered watch — two full log
reads (the first cursor cannot seed the second; Decision 3 forbids it) and a
discovery watch that must be explicitly released or it doubles every
attachment's server-side cost. Instead: **`attach()` with no `runId` opens one
unfiltered watch and applies the run filter client-side**, sharing its code
path with `activity()`. `attach(runId)` uses the server's `run_id` filter,
because there the filter is known before the first byte and pushing it down is
free.

The active/latest distinction survives as an observable property rather than a
selection rule: `AttachedRun.live` is a **getter**, true while the run is in
flight and false once its terminal `result` has been observed — not a value
frozen at attach time, which would be indistinguishable in a point-in-time
test and materially different in use.

A log with no run-bearing event yields **`NoRunsError`** — read from the log,
not inferred from `Session.state`, so `attach()` needs no second round-trip
whose answer could disagree with the log it is about to read. That is distinct
from a session the caller may not read or that does not exist, which
`watchLog` refuses eagerly with `session_not_found` when `OwnershipEnforced`
is set.

**`NoRunsError` has a known false positive, and we ship it documented rather
than papered over.** `Service.StartRunContent` mints the run id and stamps it
on the aggregate *before* launching the engine goroutine — deliberately, so the
id is on the snapshot the moment the run can park awaiting an approval. Between
that stamp and the goroutine's first emitted event, the run genuinely exists
and the log genuinely holds nothing about it, so an `attach()` landing in that
window reads an empty replay and reports `NoRunsError` for a session that is
actively running.

Every fix is worse than the disclosure:

- *Wait for a first event.* A truly run-less session then hangs instead of
  answering, converting a wrong answer into no answer.
- *Consult `Session.state` and wait only when `running`.* This is the least-bad
  alternative and is genuinely tempting, but it reintroduces exactly the second
  round-trip this decision removed, and its answer can disagree with the log it
  is about to read — plus it needs an arbitrary wait bound that is a guess at
  engine startup latency.
- *Retry briefly, unconditionally.* Same arbitrary bound, paid by every
  legitimately empty session.

What makes this tolerable is that the caller who would care most is
**structurally immune**, though not by the route an earlier draft claimed. That
draft said a caller could mitigate by passing the id from `session.run()`; that
is not a mitigation, it is an impossibility — `session.run()` resolves only
after the first run-ID-bearing event arrives, and the relay appends every event
to the durable log *before* sending it. So by the time any caller holds a run
id from `run()`, the log already contains the record that ends the window. The
window cannot be observed by the caller who started the run; it is reachable
only by a third party attaching blind to a session someone else started
microseconds earlier, with no id in hand.

It closes properly with the deferred server-side active-run field, which is the
same dependency the replay scan wants. The two demand opposite caller responses — create a run, versus fix
your id or your credentials — so conflating them would teach applications to
swallow an authorization failure.

**The single-writer premise is a documented assumption, not a proof.**
Same-process, the authoritative gate is the run registry — `LookupRun(id)`
plus a non-terminal loaded state, which
[`internal/adapter/server/service.go`](../../internal/adapter/server/service.go)
refuses with "session already has an active run". It is *not* `runEntryMu`,
whose own comment says it "only serializes the RUN-ENTRY section, not a run's
full lifetime". Cross-process, the only gate is `port.SessionLease`, and per
[`AGENTS.md`](../../AGENTS.md) the default path is byte-identical without one:
"no flag → `buildSessionLease` returns nil → no acquire/renewer/release". So a
multi-replica `mecated` over a shared Redis log with no `--session-lease-*`
flag *can* interleave two runs on one session — in exactly the deployment
shape [ADR 0250](./0250-durable-cursors-and-watch.md) was written for. The
behaviour stays well-defined ("newest in the log" is always computable); what
we decline to claim is that it is necessarily the run the caller meant.
`mecak8s` wires a lease; a bare multi-replica `mecated` does not. Stating the
residual is the discipline
[ADR 0250](./0250-durable-cursors-and-watch.md) Decision 6 set one ADR earlier
for the gap guarantee, and the deferred `active_run_id` server field is the fix
for a *named* dependency rather than a nice-to-have.

**3. The SDK cursor is a versioned, serializable, opaque string branded with
its effective server filter.**

The wire form is `base64url({v: "sdkcur/1", token, filter, run})` — a versioned
envelope for the same reason
[`engine/port/cursoreventlog.go`](../../engine/port/cursoreventlog.go)'s
`cur/1` has one: a future encoding change must be distinguishable from
corruption. The three payload fields answer three different questions and an
earlier draft collapsed two of them:

- `token` — the server's opaque cursor. *Where* in the log.
- `filter` — the `run_id` the watch was **issued to the server** under (`""`
  when none was). What the position *means*, i.e. which records it advanced
  over.
- `run` — the run the attachment is **bound to**, which for an implicit
  `attach()` is chosen client-side and is therefore **not** the same as
  `filter`. *Which run* this cursor belongs to.

Keeping `run` separate is what makes the reload case work at all. An implicit
`attach()` opens an unfiltered watch (Decision 2) and filters run-side, so its
cursor carries `filter: ""` while being bound to run `R`. With only `filter`
stored, a reload had no good move: `attach(R, {from: cursor})` was refused for
scope mismatch, and `attach(undefined, {from: cursor})` re-derived "the newest
run in the log", which after a later run exists silently attaches to a
*different* run — or fails `NoRunsError`. Both outcomes break the headline
scenario for the commonest way to reach it. With `run` in the envelope, a
resume restores the binding rather than re-deriving it.

A **string**, not a branded type, because Context point 4's headline case is a
page reload: the cursor has to survive `JSON.stringify` into application
storage and come back to a different `Client`. A TypeScript brand would pass
every in-process test and evaporate on that round trip.

**The scope rule is delivered-set containment over BOTH fields.** The invariant
is not "which records did the position advance over" — that framing was wrong,
and it is what made an earlier draft unsafe. What matters is **which records the
cursor's own view actually delivered**, because a resume can only ever start
after the position: anything the earlier view advanced past *without yielding*
is lost to every later view.

That distinction bites precisely on the implicit `attach()` this decision
introduced. Its watch is unfiltered, so its position advances over every record
— including other runs' — while it yields only run `R`'s. Judged on position
basis alone its cursor looks maximally broad and safe to hand anywhere. Judged
on what it delivered, it is *narrow*: hand it to `activity()` and every other
run's records between the log's start and that position are silently skipped,
which is exactly the class of failure Context point 3 exists to prevent.

So a cursor may be handed only to a view whose delivered set is a **subset** of
its own, and `run` is the load-bearing field:

- `run: "R"` (either attach form) resumes **only** a view bound to run `R`.
  `activity()` and `attach(otherRun)` are refused with `CursorScopeError`.
- `run: ""` (from `activity()`) resumes anything — `activity()`, or `attach(R)`
  for any `R`, since an attachment yields a subset of what activity already
  delivered.
- `filter` is checked alongside it as the record of the position's server-side
  basis: a cursor issued under a server filter may never be widened past it.
  For same-run resumes this is automatically satisfied, which is why `run` does
  the real work — but keeping the check explicit means a future server-side
  scoping change cannot quietly invalidate stored cursors.

Both checks run **locally**, before any request.

This is the one guarantee the SDK adds beyond the server's, and it is
justified by the server structurally not being able to add it. We keep that
discipline: it is the *only* one. In particular the SDK does **not** re-brand
`session_id` — the server refuses a cross-session cursor loudly and typed
(`ErrCursorMalformed`), and the port doc-comment explains why that check lives
in the port rather than per backend.

**Opacity is a promise about authoring, not about serializability** — exactly
the distinction [ADR 0250](./0250-durable-cursors-and-watch.md) already drew
when it wrote that the encoding "is stateless and therefore inspectable by a
determined client".

**And the SDK does not claim to detect provenance**, because it structurally
cannot. The envelope is stateless and unsigned, so a fresh `Client` — which by
Context point 4 is the entire point — holds no record of what it issued and
cannot distinguish an issued cursor from a byte-identical hand-built one. An
earlier draft promised exactly that, which would have been the kind of
untestable claim this ADR refuses elsewhere. What the SDK guarantees is
**validation, not issuance**:

- a value that is not a well-formed `sdkcur/1` envelope — wrong version,
  undecodable, missing fields, or a raw server token lifted from the raw seam —
  is refused locally with `CursorMalformedError`;
- a well-formed envelope whose `filter` disagrees with the attachment's
  effective server filter is refused locally with `CursorScopeError`;
- a well-formed, correctly-filtered envelope is **accepted**, whoever built it,
  and its inner `token` then faces the server's own generation and session
  validation — which is where a forged or stale position was always going to be
  caught.

Signing would buy real provenance and is deliberately not done: it needs a
client-side secret the SDK has no way to hold, and the property it would protect
(a caller lying to itself about its own cursor) is not a threat model. The
public type still exposes no member a caller can meaningfully author, so the
ergonomic path never invites hand-building one.

**4. Filtering is a yield-time decision, consumption is a checkpoint-time one,
and the filter set is DERIVED from the server rather than written down.**

The checkpoint advances **when the consumer requests the next envelope**, not
when the SDK yields one — #821's "natural at-least-once", so a consumer that
dies after processing an envelope but before asking for another re-receives it.
It also advances over records the ergonomic filter dropped, because the watch
route is the read-back of the durable log and carries kinds the live relay
skips; not advancing would re-deliver a burst the consumer declined.

The **consume-but-do-not-yield** split has a second consumer the naive reading
misses: `approval` is filtered by default, but `permission.ask` and
`permission.retract` are not, and M1's run bookkeeping retires an ask when its
`approval` arrives. An attachment that only *yielded* on the filter would
replay asks with no verdicts and report long-resolved asks as pending. So the
attachment **consumes `approval` for internal bookkeeping even when it does not
yield it** — the same split, applied to a second piece of state.

The default filter set is **derived from the server and gated**, never a prose
list. It is the union of `isPublicEvent`'s exclusions (`network.attempt`,
`request.manifest` — debugger-only on every other client surface, and *not*
stripped on the watch route) and `relayLiveEvent`'s log-only skips
(`approval`, `compaction.archive`, `user_prompt`), pinned by a Go parity gate
that extends the mechanism M1's
`TestSDKTypescriptCore_Scenario6_LogOnlyKindsAudited` already uses — it parses
both `isPublicEvent`'s body and the skip condition out of Go source, so no new
machinery is required.

One deliberate, documented divergence: the SDK filters **all** `user_prompt`,
where `relayLiveEvent` relays the scheduled-fire delivery note. Matching the
server exactly would mean re-implementing its fenced-provenance prefix sniff in
the client — a heuristic about untrusted-fence framing is the wrong thing for
an SDK to own a copy of. `{ includeLogOnly: true }` restores every filtered
kind, so nothing is unreachable; the divergence is one opt-in wide and is
recorded rather than discovered.

`attach()` and `activity()` accept `from: "start" | "now" | Cursor`. `"start"`
is the empty cursor; an explicit cursor resumes, subject to Decision 3.
**`"now"` is a client-side discard, not a wire capability** —
`WatchSessionEventsRequest` has only `session_id`, `cursor`, and `run_id`, so
there is no skip-replay option and the replay still crosses the network. It
therefore requires an explicit `runId` not because it *cannot* observe the run
(it sees the replay frames and drops them) but because we **decline to pay for
a scan whose results we are throwing away**. Both facts were stated backwards
in an earlier draft; the constraint stands, the justification is the honest
one.

**5. Reconnect is indefinite and bounded; the classification is three-way and
keyed on a closed set of codes.**

An attachment reconnects until its `AbortSignal` fires or it is disposed, with
bounded exponential backoff plus jitter, resuming from its own checkpoint under
its own filter.

- **Resume** — a transport-shaped failure; `watch_lagging`; a **clean
  non-terminal EOF**; and an ordinary `authentication` failure.
  `watch_lagging` is explicitly resumable:
  [`internal/adapter/server/watch.go`](../../internal/adapter/server/watch.go)
  documents that its error carries no cursor precisely because the client's own
  last-received envelope is the correct resume point. The clean-EOF arm is not
  a defensive guess: `Service.closeWatches` ends every watch on shutdown and
  says so — "A shutdown cancel is a CLEAN end, not a gap: no append failed, so
  the stream simply ends and the client reconnects with its cursor". Over
  HTTP/SSE that arrives as a body that simply finishes, so an attachment that
  classified only *errors* would complete silently, mid-run, having observed no
  terminal event — the exact silence this milestone exists to abolish. The rule
  is therefore: a stream that ends without an error, and without the terminal
  this attachment is waiting for, is a **resume**, not a completion.

  "The terminal this attachment is waiting for" is the load-bearing clause,
  because the two attachment types have different ones. An `AttachedRun`
  completes on its run's `result`. **`SessionActivity` has no terminal at
  all** — it is a session timeline, so a `result` in it belongs to one run and
  says nothing about whether more runs will follow. An activity stream
  therefore reconnects on **every** clean EOF, including EOFs that arrive long
  after it has observed one or several `result` events. Collapsing the two
  would make `activity()` quietly stop at the first run's end, which is both
  wrong and invisible.
  `authentication` resumes because it is the one refusal a retry can actually
  clear: M1's credentials seam supports an async per-request provider, so the
  next attempt re-invokes it and may present a fresh token. The attachment
  reports `unauthorized` while it does (Decision 8), so an application holding a
  static header that will never change can see the state and abort — #821's
  "reconnect indefinitely until aborted" puts that call with the caller.
- **Terminate** — a refusal the server has durably answered. The set is
  **closed and enumerated in one place**, keyed on the stable `code` string
  rather than a status or an error shape, for the same anti-rot reason the
  operation axis gets: `cursor_expired`, `cursor_malformed`, `activity_gap`,
  `session_not_found`, `invalid_argument`, `management_unauthorized`,
  `incompatible_server`, `watch_unsupported`, `no_event_log`.
  `watch_unsupported` and `no_event_log` are both registered `Unimplemented`, so
  a client that classified either as transient would dial forever.

  `management_unauthorized` sits here while `authentication` sits in the resume
  arm because they are different questions — which is why the server registers
  them as different codes. `management_unauthorized` is `PermissionDenied`/403,
  an authorization *decision* about who this caller is, which no token refresh
  changes: a 401 says "try again with better credentials", a 403 says "these are
  your credentials and the answer is no".

  `incompatible_server` terminates because a floor failure is a deployment fact
  rather than a transient one. But a reconnect can legitimately meet a
  **different build** — AC10.4 and AC10.5 restart the daemon under a live
  attachment — so the cached compatibility info is **invalidated before the
  first reconnect attempt**, the moment the attachment leaves `online`, and
  re-probed as part of that attempt. Invalidating on success instead would be
  too late by exactly one attempt: that attempt would already have been made on
  the strength of a floor learned from a process that may no longer exist, so a
  restarted-and-downgraded daemon would be dialled once by a client still
  believing it supports the watch, and only afterwards re-checked.
- **Never retried at all** — every non-watch operation. **The watch read is the
  only thing the SDK ever retries automatically**, decided where the operation
  is *named* rather than inferred from an error, because an allowlist a future
  RPC can join by resembling one is what rots.

Backoff needs an **internal** scheduler seam — `{ delayFor(attempt), sleep(ms,
signal) }` threaded through an unexported options bag — because jitter is
`Math.random`, which fake timers do not control, and "the timer is cleared" is
otherwise unobservable. It stays out of the public surface: the bounds are not
caller configuration, because no caller has asked for them.

**Detach never cancels.** Aborting the signal, `Symbol.asyncDispose`, and
`break`ing out of `for await` all release the watch and its reconnect loop and
nothing else; the run continues. `break` matters most — it is the commonest
release path and the one whose leak outlives every request. An attachment is
**single-consumption** like M1's `Run`, because two concurrent consumers over
one checkpoint would each silently see a subset.

Closing the owning `Client` — including mid-backoff — stops the loop, clears
the pending timer, and issues no further request. M1 shipped the sibling
contract for its own resources; M2 adds the first client-owned object with a
*timer*, and a leaked `setTimeout` keeps a Node event loop alive.

**6. `cancel()` is the only attached control in M2: HTTP-only, with a typed
gRPC refusal. `approve()`, `resolveAsk()`, and `steer()` are deferred.**

`AttachedRun` exposes `cancel()`, carrying `expected_run_id` for the run it
attached to — the [ADR 0249](./0249-durable-run-identity.md) stale-control
contract, identical to M1's owned-`Run` path. Over HTTP it rides the
prompt-free `POST /v1/sessions/{id}/cancel` route, a `204` ack with no response
body. **Over gRPC it raises a typed unsupported-feature error naming the
missing channel**, because Context point 6 establishes there is none: controls
are `Converse` frames and `Converse`'s first frame must be a prompt.

That gRPC refusal is the exact mirror of how M1 handles `http_steer` — a
capability gap surfaced as a typed error rather than a silent degrade. Adding a
prompt-free control RPC is the design that wants to exist, but it is a proto
plus production-server change this milestone is scoped out of.

**Attached `steer` is unsupported on BOTH transports in M2** and says so: gRPC
has no channel, and HTTP's steer route waits on
[#873](https://github.com/stacklok/mecatl/issues/873)
([ADR 0252](./0252-http-steer-endpoint.md)). Since a promoted steer would mint a
run id the attachment's filter can never match — turning a refusal into
silence — the strict, never-promoting reading M1 pinned for owned runs is the
only acceptable one here too.

**Why approval is deferred rather than shipped over HTTP alongside `cancel`:**
the approve route's response body cannot be safely handled by any client-side
strategy.

`POST .../approve` has two paths. On the **same-process**
path (`run == nil`) it is also a 204: a live run resolves the ask over its own
channel and the existing stream delivers the effects. On the **rehydrate**
path — the process that parked the ask died and the loop re-entered here, i.e.
exactly the restart case — the resumed run has no stream to ride, so the
handler relays its events as SSE on the approve response.

That body is a trap from both ends. `relayRunSSE` spawns a goroutine on the
request context — "If the client disconnects, cancel the run" — so closing it
early cancels the run the caller just approved. Leaving it unread stalls the
relay. And draining it to EOF is **unbounded**: the resumed run can reach
another permission ask and park indefinitely, so a drain that survives
`Client.close()` (as it must, since aborting it is the cancel) holds a socket
open and keeps a Node event loop alive for as long as that run is parked. An
earlier draft of this ADR claimed the drain was "bounded by the run's own
completion"; a run awaiting approval has no such bound.

The server already contains the right shape and simply does not expose it: when
the `ResponseWriter` is not a `Flusher`, the same handler acks 204 and drains
the run itself on a cancel-detached context, recording every event to the
durable log. That branch is selected by a server-side type assertion no client
can influence. **The fix is to make it selectable** — an ack-only approve — and
that is a production-server change this milestone is scoped out of. Shipping an `approve()` whose only implementation strategy is a documented
resource leak would be worse than not shipping it, so `approve()` and
`resolveAsk()` raise a typed unsupported-feature error on both transports —
each naming its **own** dependency, because they are two independent server
changes: `approve_ack_only` over HTTP, `prompt_free_controls` over gRPC. One
identifier could not describe both, and naming them separately tells an operator
which change they are waiting on.

Unlike M1's `http_steer`, these identifiers do **not** latently unblock
anything. `http_steer` gates an implemented route, so the feature appearing is
enough. Here the methods are typed `Promise<never>` with no code path behind
them, and the gRPC half additionally needs a new proto descriptor and a
regenerated client — so each side requires an SDK release once its server half
lands. We chose that over shipping a speculative, server-untestable HTTP
implementation behind a dark gate: dead code written against a route that does
not exist yet is how a gate ships broken and nobody notices until the day it
opens.

Cancellation is unaffected and ships, so an observer can still stop a run it is
watching. The cost is real and is #821's, not ours to wave away: "an attached
run has explicit approve/steer/cancel methods" is one third fulfilled in M2.

**Attached `cancel()` needs a new seam; M1's does not fit.** `RunOperations` is
`{ transportKind, send(frame): void }` — a *synchronous* push of a
`ConverseRequest` frame onto a stream the owned `Run` already holds. An
attachment has no such stream, and `cancel()` must **await** its outcome: the
`204` that means accepted, or the typed stale-control failure that means the
run it named is gone. A `void` send can express neither.

M2 therefore adds a small out-of-band control seam alongside it —
`{ transportKind, cancelRun(sessionId, runId): Promise<void> }` — implemented
over HTTP by the prompt-free `/cancel` route and by a typed unsupported error
over gRPC. What stays **shared** with M1 is the thing worth sharing: the
[ADR 0249](./0249-durable-run-identity.md) `expected_run_id` stale-control
contract and its error mapping are expressed once and consumed by both seams.
What is deliberately not shared is the delivery mechanism, because "put a frame
on my stream" and "make a request about someone else's run" are different
operations that only look alike. Its *verdict* mapping is not in play: the only
control M2 ships is `cancel()`, which carries no verdict, and the verdict
vocabulary comes back into scope with the deferred approval methods. What is
*not* shared is an interface across `Run` and `AttachedRun`: `cancel()` means
"abort and end my stream" on one and "abort the run, keep watching" on the
other, so a common `RunControls` would be a Liskov violation dressed as
de-duplication.

**7. The attachment error vocabulary maps from the existing registry; no second
taxonomy.**

| Error | From | Code |
|---|---|---|
| `CursorExpiredError` | server | `cursor_expired` |
| `CursorMalformedError` | server, or a structurally invalid `sdkcur/1` value | `cursor_malformed` |
| `ActivityGapError` | server, **or** the `gap` envelope arm | `activity_gap` |
| `NoRunsError` | local — `attach()` found no run | `no_runs` (new `SDKErrorCode`) |
| `CursorScopeError` | local — Decision 3 brand check | `cursor_scope` (new `SDKErrorCode`) |

The two local errors carry no *registry* code because no server failure
produced them — but M1's `MecatlError` **requires** a `code`, and its local half
is the closed, API-Extractor-reported `SDKErrorCode` union. They therefore add
exactly two members rather than reusing `invalid_state`, which would make "you
attached to a session with no runs" indistinguishable from "you consumed this
run twice" and defeat the point of a typed hierarchy. Two new members of a
published union is an additive minor change; picking it here rather than per
worker is Decision 1's reasoning applied again.

`ActivityGapError` is reachable from both sides, so it is pinned on both: the
same `activity_gap` code either way, with `transport: "local"` when it was
raised from a `gap` arm rather than a server failure. The code names the
condition; the transport names who observed it. A missing
`watch_session_events` feature is the **existing** `UnsupportedFeatureError`
([ADR 0248](./0248-sdk-compatibility-and-error-contract.md) owns feature gating
and M1 already implements it for `http_steer`).

The gate needs a **features accessor at the `RawClient`/`Client` seam**. M1's
only feature set is private to the HTTP transport, which is correct for
`http_steer` — genuinely transport-specific — but copying that placement here
would gate the watch on HTTP and leave gRPC ungated, which is the opposite of
the intent.

**8. The status monitor gains attachment as an input, with an any-not-last
arbitration rule.**

The six values stay closed (`connecting`, `online`, `reconnecting`, `offline`,
`unauthorized`, `incompatible`) — M1's contract, unchanged. What M2 adds is
that a long-lived attachment's transport outcomes drive them.

**Arbitration is a fixed precedence over all inputs, not last-writer-wins.**
The states are ranked, highest first:

1. `incompatible` — a floor failure; terminal and a deployment fact.
2. `unauthorized` — any attachment or request is being refused for credentials.
3. `reconnecting` — any attachment is between attempts.
4. `connecting` — the client has not yet completed its first exchange.
5. `offline` — no attachment is reconnecting and the last outcome failed.
6. `online` — none of the above.

The ranking resolves the two rules that would otherwise collide: an attachment
retrying an `authentication` failure is *both* reconnecting and unauthorized,
and it reports `unauthorized`, because the credential is the actionable fact
and "reconnecting" would hide it behind a state that looks self-healing. A
reconnect loop consequently never publishes `offline` — `offline` sits below
`reconnecting`, so it can only be reached once nothing is retrying. M2 adds a second long-lived writer to a single client-level value,
and M1's error mapping moves `TransportError` → `reconnecting` → `offline`
immediately. Under last-writer-wins a healthy attachment's next frame would
report `online` while another is still down, and a genuinely reconnecting
attachment would report `offline` — which makes the whole scenario vacuous. So:
the client reports `reconnecting` while **any** attachment is reconnecting
*unless a higher-ranked state applies*, and `online` only when none is.
`unauthorized` and `incompatible` outrank it and stay until the condition
clears, because they name a deployment fact rather than one stream's luck —
which is what the ranking above encodes, and why "any reconnecting attachment
wins" is a rule about ties within one rank, never a rule that beats them.

An attachment is **not** a status subscriber — it must not keep a heartbeat
alive, because a long attachment is exactly where the heartbeat is redundant:
the stream itself is the liveness signal.

**Browser page-visibility pause is M4.** Pausing a *watch* on a hidden page is a
different and worse question than pausing a heartbeat — a hidden tab that stops
reading is precisely the slow consumer `watch_lagging` terminates — so M2 states
plainly that it does **not** pause attachments on visibility change, and defers
the decision to M4's real-browser matrix where it can be measured rather than
guessed.

**9. The two attachment types, stated.**

Left unstated, these would be invented twice, which is the risk Decision 1
exists to close.

```ts
interface SessionActivity extends AsyncIterable<WatchEnvelope>, AsyncDisposable {
  readonly cursor: SdkCursor;          // the current checkpoint, serializable
  close(): Promise<void>;              // detach; never cancels
}

interface AttachedRun extends SessionActivity {
  readonly runId: string;
  readonly live: boolean;                 // getter; false once the terminal is observed
  cancel(): Promise<void>;                // awaits the 204, or rejects typed-stale.
                                          // HTTP only; gRPC: typed unsupported
  approve(askId: string, allow: boolean): Promise<never>;        // deferred in M2
  resolveAsk(askId: string, v: PermissionVerdict): Promise<never>; // deferred in M2
  steer(text: string): Promise<never>;                            // deferred in M2
}
```

`session.activity()` returns `SessionActivity`; `session.attach(runId?)`
returns `AttachedRun`. `AttachedRun` extends the read surface rather than
duplicating it, and the naming aligns on the server's word for the durable
stream ("activity") instead of introducing a third vocabulary.

## Consequences

**Easier.**

- A reconnecting tab rejoins a run with no lost events and no transcript
  re-download — the capability [ADR 0250](./0250-durable-cursors-and-watch.md)
  built the server half for.
- One narrowing idiom across both unions: `kind` on events, `kind` on
  envelopes.
- The cross-filter cursor mistake becomes a local exception where it is made.
- `activity()` gives a cross-run session timeline no existing endpoint offered.
- `attach()` with no `runId` is **one** watch and one scan, sharing its code
  path with `activity()`.

**Harder — the honest costs.**

- **An `AttachedRun` does not survive a daemon restart, and cannot.** Its
  filter names one `run_id`; a run interrupted by a process death does not
  continue, and whatever runs next is a *new* run with a *new* id that the
  filter excludes by construction. So a restart under an `AttachedRun` yields a
  correctly-resumed cursor delivering nothing further — right, and useless. The
  two things that *do* cross a restart are `activity()`, whose unfiltered
  stream picks up the new run, and the **awaiting-approval resume**, where
  `Service.resumeFromAwaiting` reuses the persisted id
  ([ADR 0249](./0249-durable-run-identity.md)) so the same filter keeps
  matching. Those are the two shapes the e2e proves; a general
  "`AttachedRun` survives a restart" claim would be false.
- **`attach()` without a `runId` still scans the replay phase.** On a
  long-lived session that is the whole log crossing the network. The only
  mitigation is passing a known `runId`; `from: "now"` does **not** help
  (Decision 4), and the proper fix is a server-side active-run field this
  milestone is scoped out of.
- **The attached-control surface is one method wide, and that is visible.**
  `cancel()` ships (HTTP only); `approve()`, `resolveAsk()`, and `steer()` all
  raise typed unsupported errors. A consumer that wants attach-then-approve —
  the natural reading of #821 — cannot have it in M2 on either transport, and
  this is the first thing a user will file. Two distinct server changes unblock
  it: an ack-only approve response (which the handler already implements for
  non-`Flusher` writers) and a prompt-free control RPC for gRPC. We chose honest
  typed errors over shipping a method whose only implementation strategy is a
  resource leak.
- **The SDK brands cursors, so SDK cursors and raw server cursors are not
  interchangeable.** An application holding a raw token from the raw seam
  cannot hand it to `attach()`. Documented; the raw seam keeps taking raw
  tokens.
- **At-least-once means duplicates are a documented feature.** Any consumer
  with side effects needs its own idempotency, and the docs must say so rather
  than implying exactly-once.
- **We inherit ADR 0250's weaker-than-asked gap guarantee and must publish
  it.** A total backend outage plus process loss leaves an **undetectable**
  gap. [ADR 0250](./0250-durable-cursors-and-watch.md) Decision 6 chose to
  state that rather than ship an absolute Redis being down would falsify; the
  SDK's documentation repeats it rather than rounding it up.
- **One deliberate filter divergence from the live wire** (`user_prompt`
  delivery notes, Decision 4). Gated, opt-in-reachable, and recorded — but a
  divergence.
- **Redis follow capacity is not ours and is not solved.** N attachments are N
  blocked `XREAD`s competing with the write path
  ([#876](https://github.com/stacklok/mecatl/issues/876)). The SDK makes many
  attachments easy to open. We add no client-side admission limit — guessing
  one would be policy at the wrong layer — and instead name #876 as the
  dependency.
- **A new public surface pinned before `v0.1.0`.** `SessionActivity`,
  `AttachedRun`, the envelope union, the cursor string, and five error types
  enter the API Extractor report for `.`; changing `kind`'s arms after
  publication is breaking.
- **Two long-lived client-side objects** — the reconnect loop and the
  checkpoint. They live in the *client* process, so per
  [ADR 0279](./0279-typescript-sdk-architecture.md) Decision 5 they take no
  [ADR 0027](./0027-cloud-native.md) inventory row; that decision is now
  load-bearing rather than incidental, and each one's disposal is part of
  `Client.close()`'s contract.
- **Unknown-phase tolerance is only provable against a fake**, since the server
  emits exactly three phases. A forward-compatibility property has no other
  proof before the future exists.

## See also

- [Issue #821](https://github.com/stacklok/mecatl/issues/821) — the settled
  design contract, "Attachment and reconnection" and "Capability, status, auth,
  and errors"; `docs/acceptance/sdk-typescript-attach.md` — the acceptance plan
  this ADR anchors.
- [ADR 0250](./0250-durable-cursors-and-watch.md) — the server half: the cursor
  port, the watch transport, the phase vocabulary, the honestly bounded gap
  guarantee, and the cursor-opacity promise this decision inherits.
- [ADR 0249](./0249-durable-run-identity.md) — the `run_id` the watch filters
  on, the `expected_run_id` an attached control carries, and the
  `attach()`/`activity()` asymmetry its Consequences already predicted would
  "look like an inconsistency to anyone who has not read this ADR".
- [ADR 0248](./0248-sdk-compatibility-and-error-contract.md) — the feature
  vocabulary the watch gate reads and the error registry these errors map from.
- [ADR 0279](./0279-typescript-sdk-architecture.md) — the SDK architecture this
  extends; its Decision 5 deferred this contract to M2.
- [ADR 0252](./0252-http-steer-endpoint.md) —
  [#873](https://github.com/stacklok/mecatl/issues/873), the HTTP steer route
  attached `steer` waits on.
- `docs/acceptance/sdk-typescript-core.md` — M1, whose event union, error
  hierarchy, control path, and status monitor this reuses rather than
  duplicates.
