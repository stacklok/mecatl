---
id: 09-subagent-resume-owner-check
title: Enforce ownership on Subagent resume
blocked_by: [01-ownership-core]
status: done
branch: "plan-caller-separation/09-subagent-resume-owner-check"
worktree: ""
issue: "368"
retries: 0
last_error: ""
accumulator: acc/caller-separation
---

# Enforce ownership on Subagent resume

An adversarial pass over this plan found that `engine/agent/subagent.go`'s
`resolveResumeSession` (the load path for the Subagent tool's `resume: <agentId>`
argument) makes NO ownership check: it loads a persisted child session from
`t.store` (a `port.SessionStore`, shared across the whole engine, not
owner-partitioned) purely by ID, and never compares the loaded session's `Owner`
against the calling principal. AC3.3 ("A model given another caller's subagent or
team handle cannot inspect its transcript or act on it") was implemented at HALF
strength: `InspectSubagentTool`/`InspectMemberTool` correctly authorize inspection of
the SAME store by the SAME kind of id, but resume — the "act on it" half — does not.

A dedicated go-architect review (dispatched against this exact finding) confirmed the
impact is WORSE than a read leak and confirmed the fix. Follow it — this is not
exploratory, the design is settled:

**Why this is worse than InspectSubagent's read path** (state this in the commit
message, per the review):
- `InspectSubagent` returns a BOUNDED transcript (40 messages, 1000 runes each, 8000
  total) as one ToolResult. Resume replays the foreign owner's ENTIRE conversation
  into a child engine whose next turn is driven by the resuming caller's own
  model-authored prompt — the resuming caller can direct an interrogation of the
  foreign transcript with no clamp.
- Resume MUTATES the foreign owner's persisted record: the loaded session is saved
  back under the same id (`persistChild`), so the resuming caller's turns get appended
  to the FOREIGN owner's stored session. That owner's own later resume or Inspect then
  reads content authored by someone else.
- This happens even when the child drive itself fails: `subagent.go`'s resume-start
  persist re-saves the loaded snapshot BEFORE the child drives (issue #38) — placing
  the check inside `resolveActiveSession`, before that persist, is what keeps that
  write unreachable to a foreign caller.

## Design (implement this shape; adjust only if implementation surfaces a real
constraint the review missed)

1. **Reuse the existing helper verbatim — do not write a new one.**
   `engine/agent/teaminspect.go` already defines:
   ```go
   func callerOwnsTranscript(ctx context.Context, sess *session.Session) bool {
       caller := session.PrincipalFromContext(ctx)
       return caller == nil || (sess != nil && sess.Owner != nil && sess.Owner.SameIdentity(caller))
   }
   ```
   This is entirely domain-layer (`session.Principal`/`PrincipalFromContext`/
   `SameIdentity` all live in `engine/session`, already imported by `engine/agent`) —
   no new import, no layering change. The `caller == nil` arm is the ownerless-compat
   no-op (a non-OIDC deployment, or any context with no stamped principal) — do not
   gate this on a composition-layer flag; `engine/agent` cannot and must not import
   `internal/adapter/server`'s `OwnershipEnforced` concept.
2. **Use `session.PrincipalFromContext(ctx)`, NOT `parentCaps.owner`.** The review
   considered comparing against the parent aggregate's owner instead and rejected it:
   it would require threading `caps` into `resolveResumeSession` (signature churn with
   no payoff) for zero gain over the ctx-based check, which already matches every
   sibling (`InspectSubagentTool`/`InspectMemberTool`). Do NOT change
   `resolveResumeSession`'s parameters beyond what's needed for the check itself.
3. **Insert the check immediately after the `Load`, before the state `switch`,
   reusing the EXISTING not-found branch — do not add a second error message:**
   ```go
   loaded, err := t.store.Load(ctx, id)
   if err == nil && !callerOwnsTranscript(ctx, loaded) {
       err = port.ErrSessionNotFound
       loaded = nil
   }
   switch {
   case errors.Is(err, port.ErrSessionNotFound) || (err == nil && loaded == nil):
       // ... existing message, UNCHANGED
   ```
   A second, differently-worded message would itself be the distinguishing signal
   this plan's "a refusal is indistinguishable from absence" principle forbids —
   especially here, since the reader is a model that could be steered to probe for
   the difference. The existing message's guidance ("use the id exactly as shown on
   the 'agentId:' line of a previous Subagent result") is already the correct
   actionable text whether the id was wrong, expired, or foreign.
4. **Both existing call sites** (`subagent.go`, the foreground path inside
   `prepareChildSession` and the background path inside `startBackground`) already
   funnel through this one function — confirm both stay covered by this single fix
   (they should; the review found no call site that re-reads `loaded.Owner` for a
   second purpose afterward).
5. **Do not touch any other session-store-by-id read.** The review confirmed the
   other three non-test `engine/agent` call sites of `SessionStore.Load` are already
   correct or structurally not exposed to a foreign id: `teaminspect.go` (checked),
   `subagentinspect.go` (checked), `usermodelreview.go` (an internal id from the
   just-finished run, not model-supplied). `SubagentStatus`/`BashStatus` read a
   run-scoped in-memory registry, not the store, and are correctly scoped. Team
   members are never loaded by a caller-supplied id (`teamsupervisor.go` only
   `Save`s). Parallel branches have no resume path (`validateResume` rejects it).
6. **Fix the classification guard entry** (`internal/adapter/server/classification.go`):
   - `ModelToolBoundaries` is MISSING `"Subagent"` entirely — add it.
   - `modelToolAccessTable`'s existing `"InspectSubagent"` row has a WRONG rationale
     ("resumes a persisted child session; teaminspect/subagentinspect authorize via
     sess.Owner.SameIdentity(caller) before returning its transcript") — the verb
     "resumes" belongs to `Subagent`, not `InspectSubagent` (which only reads). Fix
     the wording to "reads" and add a real `"Subagent"` row, e.g. `{KindCallerOwned,
     "a resume: <agentId> load authorizes the persisted child via
     sess.Owner.SameIdentity(caller) before recovering or re-persisting it"}`.
7. **Do NOT** build a static callgraph verifier proving the tool's internal logic
   matches its classification claim, and do NOT add a second classification table —
   the review explicitly scoped both out, same as task 08's sibling finding. A short
   doc-comment note that the guard proves presence/review, not enforcement, is
   welcome but optional.
8. **File two SEPARATE follow-up notes (do not fold into this task's fix):**
   - A ctx-vs-session-owner divergence on the scheduler fire path: the fire runs
     under the system principal on ctx (`syscaller.Context`) while the fire session's
     owner is the schedule's real owner (`server.WithOwner(fireSessionOwner(...))`,
     `internal/adapter/scheduler_fire.go`) — under a ctx-based check, a scheduled run
     resuming ITS OWN schedule's child would be refused as absent (fail-closed
     degradation, not a new hole — `InspectSubagent`/`InspectMember` already behave
     this way today). The clean fix is one line at the composition seam
     (`ctx = session.WithPrincipal(ctx, sess.Owner)`, mirroring what
     `usermodelreview.go` already does) that would repair all four tools at once —
     note it in the plan doc's "Deferred decisions" section, do not fix it here.
   - `inheritOwner`'s doc-comment (`engine/agent`) already ASSERTS the invariant this
     fix establishes and swallows `ErrOwnerAlreadySet` on that basis — after this fix
     the assertion becomes true; add a one-line comment note there, no code change.

## Acceptance criteria

AC3.3 already covers this ("act on it through an agent-facing tool") — this task
extends its `verify:` list, it does not add a new AC:

- AC3.3 (extended): A model given another caller's subagent handle cannot resume it
  through the Subagent tool's `resume: <agentId>` argument, in both the foreground and
  `background:true` paths; the refusal is absence-shaped (identical wording to an
  unknown id) and the foreign owner's persisted session is byte-identical afterward
  (same message count, same `Owner`, same state) — proving no mutation occurred, not
  merely that an error was returned. A same-owner resume succeeds and replays the
  owner's own history.
  - verify: `TestCallerSeparation_Scenario3_SubagentResumeIsOwnerChecked`

**Test placement and shape** (per the review — reuse existing harnesses, do not build
new ones): put this in `engine/agent/caller_identity_owner_test.go` (internal
`package agent`, already has `ownerAlice`/`ownerBob`, `childEngineForOwnerTest`,
`drainRunEvents`, and the pattern of driving a real run under
`session.WithPrincipal(ctx, other)`). `engine/agent/subagent_resume_test.go`'s
`resumeArgs`/`extractAgentID` and `subagentinspect_test.go`'s `runOneSubagent`/
`childEngineWith`/`catalogWith` are also directly reusable.

Table-driven over foreground and `background:true` (both call sites), covering:
1. Seed a store with a session owned by Alice, containing a distinctive marker
   (e.g. a prompt/message containing `"ALICE SECRET"`), state `StateCompleted`.
   Record its snapshot (message count, Owner, State) before the test act.
2. Bob's parent session resumes it → ToolResult is an error, wording matches the
   existing "no subagent found for resume id" message, contains NEITHER the marker
   NOR any hint the id exists under another owner.
3. Re-load the session from the store: assert it is BYTE-IDENTICAL to the recorded
   snapshot (same message count, `Owner` still Alice, still `StateCompleted`) — this
   is the assertion that catches the resume-start persist; a check placed too late
   would pass step 2 and fail here.
4. Positive control: Alice's own parent resumes her own child successfully, and the
   replayed history contains the marker — otherwise the test would pass vacuously
   against a hard-coded deny.
5. Ownerless control: no principal on ctx (or a nil-owner session) — resume still
   works, preserving the pre-existing compatibility path.

## Verification gate

- `task lint && task test` — full suite. Confirm `engine/` module tests specifically
  (`cd engine && go test ./agent/...`), since this fix lives in `engine/agent`, a
  package covered by the engine-standalone `GOWORK=off` hygiene proof.
- Re-run the full existing Scenario 1-6 + this plan's `TestInvariant_owned_access_is_classified`
  suite to confirm nothing regressed.
- This does NOT touch any exported `engine/` API surface listed in `engine/api/*.txt`
  (confirm — `callerOwnsTranscript` is unexported, `resolveResumeSession` is
  unexported) so `task api:update` should not be needed; double-check before
  assuming so.
