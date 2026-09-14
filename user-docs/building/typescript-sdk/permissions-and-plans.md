---
title: Handle permissions and plans
description: Resolve Mecatl permission asks and continue approved plans from TypeScript.
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

- [Resume durable activity](./durable-activity.md) to observe approved plans
  across both runs.
- [Permissions and posture](/features/permissions-and-posture.md) for the
  server-side permission model.

## Related information

- [TypeScript SDK core API](/reference/typescript-sdk-api/core.md) for responder,
  error, and plan-resolution types.
- [Permissions and guardrails for builders](/building/what-you-get/permissions.md)
- [Start and resume sessions](/features/start-and-resume-sessions.md)
