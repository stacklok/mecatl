---
title: Handle permissions and plans
description:
  Resolve Mecatl permission asks and continue approved plans from TypeScript.
sidebar_position: 5
---

# Handle permissions and plans

Pass narrow responder functions to a run when application code can decide
permission or plan-approval asks. The raw ask remains in the event stream so a
UI or audit consumer can still observe it.

## Resolve ordinary permission asks

`onPermissionAsk` receives the ask and an `AbortSignal`. Return a verdict only
for decisions the application is authorized to make:

```ts
import { connect } from '@stacklok-oss/mecatl-sdk/node';

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? 'http://127.0.0.1:8080',
});
const session = await client.sessions.create({});
const run = await session.run('Inspect the repository without changing it', {
  onPermissionAsk: (ask) => (ask.tool === 'Read' ? 'allow_once' : 'deny'),
});

console.log((await run.result()).text);
```

The available verdicts are `allow_once`, `allow_always`, and `deny`. Returning
`undefined` leaves the ask unresolved for another application path. Server-side
deny rules remain authoritative.

When a UI makes the decision after receiving a `permission.ask` event, call
`run.resolveAsk(askId, verdict)`. The first accepted verdict wins. A duplicate
or late response throws `PermissionAskAlreadyResolvedError`.

When the application retained only the session, run, and ask IDs, load a fresh
session handle and use prompt-free run controls:

```ts
const session = await client.sessions.get(storedSessionId);
await session.controls(storedRunId).resolveAsk(
  storedAskId,
  'allow_once',
  { timeoutMs: 10_000 }
);
```

This path resolves an ordinary root or surfaced-child permission ask on that
exact run. It can resume an ordinary ask from a persisted awaiting run after a
daemon restart. An unknown or already resolved ask returns
`ask_not_pending`. A plan-originated ask returns `plan_resolution_required` and
remains pending for the plan workflow.

The acknowledgement is one unary response. A transport failure, caller
cancellation, or deadline after dispatch can reject the promise after the
server accepts the verdict. Reconcile that ambiguous case from the session's
durable activity before retrying.

## Continue after MCP authorization

Create a session-bound authorization handle from the handoff returned by
`Run.outcome()`. The handle stores correlation only and performs no request or
state check during construction:

Use this workflow only for `authorization.required` handoffs from
session-scoped ToolHive broker tools. It does not configure direct or global
MCP profiles or manage their credentials. For those host-local profiles, use
[`mecated mcp add` or `mecated mcp login`](/features/mcp-oauth-and-credentials.md).

```ts
const outcome = await run.outcome();
if (outcome.outcome !== 'authorization_required') {
  console.log(outcome.result.text);
} else {
  const authorization = session.mcpAuthorization(
    outcome.authorization.payload.authorizationId
  );
  const url = await authorization.presentation({ timeoutMs: 10_000 });
  renderAuthorizationLink(url);
}
```

`presentation()` returns the server's current absolute HTTP(S) URL. Treat it as
live sensitive data. Your application chooses how to display it and whether to
open a browser. The SDK does not open, copy, cache, render, or persist the URL.
The server also keeps presentation URLs out of stored events and snapshots.

After the person completes the external flow, create one explicit recheck:

```ts
const flow = authorization.recheck(
  {
    onPermissionAsk: (ask) =>
      ask.tool === 'Read' ? 'allow_once' : 'deny',
    permissionRequestOptions: { timeoutMs: 10_000 },
  },
  { signal: recheckSignal, timeoutMs: 30_000 }
);
const result = await flow.result();
```

`recheck()` and `cancel()` each return a new lazy,
single-consumption `McpAuthorizationFlow`. The operation starts when the first
iterator `next()` or `result()` consumes the flow, not when your application
creates the flow or requests its iterator. Request headers, callbacks, signals,
and deadlines apply only to that flow, and the SDK adds exact session affinity.

The result discriminant defines the next application action:

|`outcome`|Meaning|
|-|-|
|`pending`|The same authorization remains pending. Its status is `pending`.|
|`settled`|The authorization ended without a continuation. Its status is `granted`, `denied`, `cancelled`, `expired`, `interrupted`, `failed`, or `closed`.|
|`completed`|A continuation finished with one ordinary `RunResult`.|
|`authorization_required`|The continuation parked on a different authorization. Create a new handle from `nextAuthorization` and present its live URL.|

Iteration yields the same decoded events in wire order. Choose iteration or
`result()` once for each flow. An unknown status, mismatched authorization ID,
changed original call or continuation run ID, or malformed terminal sequence
throws `ProtocolError`. The server enforces session ownership through the session
affinity on the request.

The application owns permission policy. `onPermissionAsk` receives only an
ordinary permission ask observed on the continuation. Its verdict uses
`permissionRequestOptions`, while manual `resolveAsk()` uses only the options
passed to that method. Both controls address the exact observed continuation
run. A plan-originated ask remains in the event stream and requires the
separate plan workflow or explicit continuation cancellation. Servers need the
`prompt_free_controls` feature for permission replies and
`cancelContinuation()`; status-only flows do not require that feature.

### Bound polling and recovery

Choose the recheck cadence and its stopping condition in your application.
The SDK performs no polling, mutation retry, transparent reconnect, durable
watch, browser action, or authorization-state persistence. Cancelling a request
releases the SDK's stream and controls, but the server remains authoritative
for any transition committed before cancellation reached it.

A response lost before your application observes the status or
`continuationRunId` can be unrecoverable through this lifecycle. A later
`recheck()` is a new one-shot mutation that succeeds only while the same
authorization remains pending. If the earlier control cleared that pending
state, the server returns its not-found error instead of replaying the result.
Bound any deliberate retry and let that refusal surface.

After observing `continuationRunId`, you can use `session.attach(runId)` or
`session.activity()` where the deployment retains the needed activity. The
lifecycle does not search that activity or guarantee retention. An exact-run
attachment ends after it replays a pending `authorization.required` event with
the attached run ID and non-empty authorization and call IDs. It yields and
checkpoints that event, then sets `live` to `false`. A session-wide activity
stream remains open.

Disconnect effects depend on the continuation phase and transport. gRPC
detaches and drains ordinary continuation work, but it cancels a continuation
stranded on an ordinary permission ask. HTTP requests cancellation for a
still-active continuation and drains it. After a continuation commits a later
`authorization.required` park, either transport preserves that new pending
authorization. Terminal races remain server-authoritative.

For a complete package-export-only workflow, see
[`mcp-authorization.ts`](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/examples/mcp-authorization.ts).

## Resolve a plan during a live run

Plan approval is separate from ordinary permission approval. Pass
`onPlanApproval` when the run can call `PresentPlan`:

```ts
import { PermissionMode } from '@stacklok-oss/mecatl-sdk/gen';

const planSession = await client.sessions.create({ mode: PermissionMode.PLAN });
const run = await planSession.run('Plan and implement the requested change', {
  onPlanApproval: (_ask, signal) => (signal.aborted ? undefined : 'approve'),
});

console.log((await run.result()).stopReason);
```

The responder returns `approve`, `accept_edits`, `iterate`, or `undefined`.
`query()` requires `onPlanApproval` before it creates resources when the new
session uses plan mode. Set `session.mode` to `PermissionMode.PLAN` in the query
options.

`session.controls(runId).resolveAsk()` does not approve or deny plans. Use the
live plan responder above or `session.resolvePlan()` for a parked plan.

## Continue a parked plan

Use `session.resolvePlan()` when a plan is durably parked and no local `Run`
handle remains:

```ts
const resolution = session.resolvePlan('approve');
const { continuation, resumed } = await resolution.result();

console.log('resumed', resumed.runId);
if (continuation !== undefined) console.log('continuation', continuation.runId);
```

The resumed plan run keeps its original run ID. An approved plan starts a
separate continuation run with a new ID. `continuation` is absent when the
resumed run does not end with `plan_approved`.

Like `Run`, a `PlanResolution` has one consumption mode. Iterate its merged
events or call `result()`, once. Use `session.activity()` when another observer
needs the durable timeline across both run IDs.

## Next steps

- [Work with sessions and runs](./sessions-and-runs.md) to consume completed or
  authorization-parked runs.
- [Resume durable activity](./durable-activity.md) to observe approved plans
  across both runs.
- [Permissions and posture](/features/permissions-and-posture.md) for the
  server-side permission model.

## Related information

- [TypeScript SDK core API](/reference/typescript-sdk-api/core.md) for
  responder, error, and plan-resolution types.
- [Permissions and posture](/features/permissions-and-posture.md)
- [Start and resume sessions](/features/start-and-resume-sessions.md)
