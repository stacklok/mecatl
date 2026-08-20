# Review 2 — post-panel-repair regression assessment

**Reviewed:** `acc/authority-evaluator-port` @ `789da531`, after the panel review and its repair wave (tasks 12–15).
**Date:** 2026-08-19.
**Verdict:** **do not merge.** One ship-blocking regression introduced by the repair wave, plus one panel blocker still open.

## Blocker 1 — the ownerless guard breaks every unauthenticated session

`c9921542` (task 13) made a bound session fail closed when it has no owner:

```go
if sess.Owner == nil || sess.Owner.Issuer == "" || sess.Owner.Subject == "" {
    return session.NewToolError(call.ID, ...("owner identity is unavailable")), true
}
```

The guard is unconditional for bound sessions. But caller identity is **optional**
(ADR 0204), and `app.Build` mints a root authority for every session
unconditionally. So an ordinary session created without OIDC is bound *and*
ownerless — and every tool call is denied.

`go test ./internal/app/ -count=1` fails **9 tests**, at least 5 directly on this
message:

```
TestCreateSessionNoFSProfileNoWorkspace   Remember denied: owner identity is unavailable
TestNoFSSessionSurvivesRestartE2E         post-restart Remember denied: owner identity is unavailable
TestSelectorSessionSurvivesRestartE2E     post-restart Remember denied: owner identity is unavailable
TestSubAgentPinsAnthropic                 Subagent denied: owner identity is unavailable
TestApproveAfterRestartE2E
TestGuardrailApproveOnceE2EInteractiveAllow
TestGuardrailApproveOnceE2EYoloAdvisory
TestOpenAICodexCompositionScenario
TestPhase3ReconstructFromStoreAndLog
```

Note `TestCreateSessionNoFSProfileNoWorkspace` is **not** a restart test — a
freshly created session already fails. Restart makes it worse because the owner
is not restored either.

**Attribution is confirmed, not inferred.** The same tests pass at `269aea35`
(the pre-repair commit, run in a throwaway worktree) and fail at `789da531`.

Why no existing test caught it: the vertical slice calls `session.WithPrincipal`,
so it always has an owner, and `mecademo` does not use `app.Build`, so it never
binds authority at all. The one test that pins the new behaviour
(`TestADR_0233_AuthorityEvaluator_OwnerlessBoundSessionFailsClosed`) asserts the
denial is correct — it encodes the regression as intended behaviour.

**The underlying question the repair skipped:** should an unauthenticated
deployment mint bound authority at all? Either mint unbound when there is no
caller identity, or make the evaluator's identity requirement conditional. The
panel brief said "fail closed when evaluator identity *requires* one" — the
implementation dropped the conditional.

## Blocker 2 — task 15 is still open

`15-authority-resource-identity` is `pending`. Cedar file-resource descriptors
use lexical `filepath.Clean`, so a symlink under an allowed path evades a policy
denying a target subtree. This is a live security hole, not a cleanup.

## Finding — the presence repair re-introduced a documented failure mode

`13f26bca` (task 14) made `authoritySpecs` return `nil` when a bound session has
no evaluator, yielding a tool-less request. That is verbatim what the plan's
documentation-work item 2 warned against:

> on an evaluator error `authoritySpecs` returned nil, yielding a tool-less
> request, an empty turn, and a no-progress nudge. That is an argument for the
> filter failing loudly, not for deleting it.

The fail-closed error at `execute` — the actual fix — is correct and tested. The
tool-less request is redundant on top of it and is the known-bad pattern. Only
reachable for directly-constructed engines (tests, external engine-module
consumers), since composition maps an empty mode to `local`.

## Test quality — measured by mutation

Eight mutations applied to the security-critical paths, each reverted after one
test run. A surviving mutation would mean no test detects the property's removal.

| mutation | result |
|---|---|
| `Narrow` unions instead of intersecting | caught (3 tests) |
| `Narrow` takes depth from left only | caught (3 tests) |
| `Narrow` ORs `FileSystem` | caught (1 test) |
| `Narrow` ORs `DirectWrite` | caught (3 tests) |
| dispatch ignores the evaluator's denial | caught (2 tests) |
| nil evaluator bypasses (re-introduce panel blocker 14) | caught (1 test) |
| ownerless guard removed | caught |
| `Descend` does not consume a hop | caught |

**8 of 8 caught.** The algebra and the dispatch guards have real teeth.

What mutation testing cannot score is missing code. The gaps:

1. **No fuzzing of `authorityTarget`.** It parses attacker-influenced JSON
   (`{server, tool}`) to decide *what gets authorized*. A wrong target there is a
   bypass. The existing fuzz target covers `Narrow`'s monotonicity only.
2. **No cross-process authority test.** Vertical-slice steps 8–9 (restart,
   resume under a narrowed parent) are still unimplemented — and restart is
   exactly where blocker 1 manifests.
3. **No test for bound + ownerless via the real composition path**, only via a
   hand-built session.
4. **No resource-only MCP server test** (review 1, finding 1).

## Gate status @ `789da531`

| gate | result |
|---|---|
| `task lint` | pass |
| engine module tests | pass (40 packages) |
| `task test:engine-standalone` | pass — Cedar contained |
| `task api:check` | pass |
| `internal/adapter/server` | pass |
| **`internal/app`** | **FAIL — 9 tests** |
| `mecademo` | pass (but does not use `app.Build`, so it proves nothing here) |
| `task tidy` / `task docs` / `task vuln` / `ac-trace-strict` | not run |

Also outstanding: `cedar-go` is `// indirect` in the root `go.mod` despite a
direct import in `internal/adapter/cedarauthority` — `task tidy` was not run.

## Recommended order

1. Fix blocker 1 — decide whether unauthenticated deployments mint bound
   authority, then make the guard match that decision.
2. Finish task 15.
3. Revert the `authoritySpecs` nil return; keep the `execute` fail-closed.
4. Add the fuzz target for `authorityTarget`.
5. Implement vertical-slice steps 8–9.
6. `task tidy`, then re-run the full gate set.
