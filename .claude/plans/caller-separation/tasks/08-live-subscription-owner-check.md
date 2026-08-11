---
id: 08-live-subscription-owner-check
title: Enforce ownership on the live event subscription
blocked_by: [01-ownership-core]
status: done
branch: "plan-caller-separation/08-live-subscription-owner-check"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Enforce ownership on the live event subscription

An adversarial pass over this plan (live-cluster probing + independent codebase
mapping) found that `Service.Subscribe` (`internal/adapter/server/service.go`) makes
NO ownership decision at all: it takes no `context.Context`, so it cannot consult the
caller's principal, and registers a live event channel keyed purely on the raw
wire-supplied session ID. Its only production caller is the gRPC `StreamSessionLive`
handler (`internal/adapter/server/grpc.go`) — an UNTRUSTED wire boundary, not the
trusted in-process embed its stale doc comment claims (mecatui dials it like any
remote client over a real gRPC socket; confirmed by a go-architect review — there is
no second, trusted caller to protect). Any OIDC-authenticated caller who knows another
caller's session ID can open this stream and receive that session's live tool calls,
prompts, and results in real time, indefinitely.

A dedicated go-architect review (dispatched against this exact finding) confirmed the
fix. Follow it — this is not exploratory, the design is settled:

## Design (implement this shape; adjust only if implementation surfaces a real
constraint the review missed)

1. **Change `Service.Subscribe`'s signature to take a `context.Context` and return an
   `error`, mirroring `StreamSessionEvents` byte-for-byte:**
   ```go
   func (s *Service) Subscribe(ctx context.Context, id session.SessionID) (<-chan session.Event, func(), error) {
       if s.cfg.OwnershipEnforced {
           if _, err := s.GetSession(ctx, id); err != nil {
               return nil, nil, err
           }
       }
       // ... existing body unchanged ...
       return ch, unsub, nil
   }
   ```
   Gate on `OwnershipEnforced` (not an unconditional `GetSession` call) — `GetSession`
   requires a persisted `Store.Load` to succeed, and unconditionally requiring that
   would change behavior on the ownerless-compatibility path for zero security
   benefit (no verifier means no caller to separate).
2. **Update the sole production call site** (`grpc.go`'s `StreamSessionLive`):
   ```go
   ch, unsub, err := h.svc.Subscribe(stream.Context(), id)
   if err != nil {
       return toStatus(err)
   }
   defer unsub()
   ```
   `toStatus` already maps `ErrNotFound` → `codes.NotFound` — reuse it exactly as
   `StreamSessionEvents`' handler does. Do NOT add a `PermissionDenied` arm: `GetSession`
   already returns the same `ErrNotFound` for both absence and owner mismatch (the
   plan's deny-as-absence contract), and a distinguishing error would make this RPC an
   existence oracle for other callers' session IDs. Keep the existing `InvalidArgument`
   check for an empty `session_id` unchanged (a different failure class — malformed
   request, not a resource decision).
3. **Update the three direct test call sites** in `internal/adapter/server/subscription_test.go`
   (mechanical: add `context.Background()` (or a principal-bearing context where the
   test needs one) and handle the new `error` return).
4. **Delete the stale doc comment** on `Subscribe` claiming an "in-process embedded
   server path (Wave 2, ADR 0075 decision #5)" caller and a "wire-transport analogue …
   is task 08" future — that caller never existed in this codebase, and it's the
   narrative that let this method's classification lie unnoticed. Replace it with an
   accurate description: the sole entry point is the gRPC wire handler, and it now
   authorizes via `GetSession` before registering a subscriber.
5. **Reclassify `Subscribe` in the guard** (`internal/adapter/server/classification.go`):
   change its table row from `{KindDerived, "live event fan-out keyed on an id the
   in-process embed caller (mecatui) already obtained from an authorized session
   open"}` (false rationale) to `{KindCallerOwned, "authorizes via GetSession before
   registering a live subscriber (issue #368)"}`.
6. **Strengthen the guard itself** (small, mechanical, worth doing in this same task
   per the review): a `KindCallerOwned` classification entry whose method has no
   `context.Context` first parameter is a contradiction — it cannot possibly consult a
   caller's principal. Add a structural check in `classifyNames`/`validate` that fails
   the guard when a `KindCallerOwned` entry's underlying method lacks a leading
   `context.Context` parameter (the type information is already available via
   reflection where `exportedMethodNames` builds the table). This does not verify a
   ctx-taking method actually USES the ctx — that's out of scope, a human-reviewed
   `derived`/rationale entry is still where judgment lives — but it makes the specific
   pre-fix state of `Subscribe` (claimed caller-owned, no ctx at all) unrepresentable
   going forward.
7. **Do NOT** attempt a static callgraph/AST proof that a method's body actually
   enforces its classification's claim, and do NOT add a second classification table
   for `*HarnessServer`/HTTP handlers in this task — the review flagged both as real
   gaps in the guard's reach but recommends leaving them to a deliberate human decision
   later, not folding them into this fix.

## Acceptance criteria

- AC3.6: Bob cannot open Alice's live event subscription (the gRPC `StreamSessionLive`
  feed); the refusal is absence-shaped and registers no subscriber, so no event
  Alice's run produces is ever fanned to him. Alice can open her own.
  - verify: `TestCallerSeparation_Scenario3_LiveSubscriptionIsOwnerChecked`

Write this test in TWO layers, per the review:
1. **Service level** (the boundary that now owns the decision), in
   `internal/adapter/server/caller_separation_live_test.go`, reusing its existing
   `callerSeparationLiveService(t)` harness (`OwnershipEnforced: true`, real Alice/Bob
   `session.Principal`s already wired): `svc.Subscribe(bobCtx, aliceSess.ID)` →
   `errors.Is(err, server.ErrNotFound)`; `svc.Subscribe(aliceCtx, aliceSess.ID)` →
   succeeds. Then assert the BEHAVIORAL half, which is what actually pins the leak
   rather than just the error value: after Bob's refused subscribe,
   `svc.PublishSessionEvent(aliceSess.ID, someEvent)` and assert Bob received nothing
   on any channel he might have obtained — i.e. confirm the denial registered NO
   subscriber, not merely that it returned an error alongside a live channel.
2. **Wire level**, in `internal/adapter/server/live_subscription_test.go`, over its
   existing bufconn harness (`dialLiveSubscriptionGRPC`,
   `probeLiveSubscription`/`waitForLiveEvent`/`liveEventTypes` helpers already exist).
   The existing `newLiveSubscriptionService` builds with `OwnershipEnforced` unset —
   add a variant (or a parameter) that sets it, plus a `grpc.StreamInterceptor` test
   helper wrapping the server stream so `stream.Context()` carries a principal (the
   production `principalStream` in `authn.go` is unexported; a small test-local
   equivalent is fine). Assert Bob's `stream.Recv()` returns `codes.NotFound` while
   Alice's receives the probe event.

The three existing `TestFireDelivery_Scenario6_*` tests in `live_subscription_test.go`
build their `Service` with `OwnershipEnforced` unset, so the new gate is inert for
them — confirm they stay green unchanged (only their `Subscribe` call sites need the
mechanical signature update if they call it directly; check).
