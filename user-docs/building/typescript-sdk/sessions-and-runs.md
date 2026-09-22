---
title: Work with sessions and runs
description:
  Inspect and manage Mecatl sessions, consume runs, retry failed model steps,
  send controls, and attach media.
sidebar_position: 4
---

# Work with sessions and runs

A `Session` is a durable conversation handle. Each call to `session.run()`
creates one identified run and returns a single-consumption `Run` after the
server accepts it.

## Create or load a session

Create a session for a new conversation:

```ts
const session = await client.sessions.create({});
```

Load a known session with `client.sessions.get(sessionId)`. Use
`client.sessions.fork(sessionId)` when the application needs a new session with
the source conversation history.

`session.close()` releases runtime resources without removing stored state.
`session.delete()` permanently removes the session and its store-managed
sidecars.

## Inspect a session and its transcript

Use `snapshot()` when the application needs authoritative session state, such
as the current mode, model, title, limits, token usage, placement metadata, or
media capabilities:

```ts
const snapshot = await session.snapshot();

console.log(snapshot.state, snapshot.title?.value);
console.log(snapshot.resolvedModel?.providerId, snapshot.resolvedModel?.modelId);
```

Use `transcript()` for the ordered conversation currently visible to the
model:

```ts
const transcript = await session.transcript();

for (const message of transcript.messages) {
  console.log(message.role, message.text);
}
```

Snapshots and transcripts are detached, readonly SDK values. They do not
expose generated protobuf messages or provider-private replay fields. The
`complete` field on a transcript confirms that it came from one authoritative
session-store load. Activity replay remains a separate, non-authoritative
event view.

## Change a session

Rename an eligible session or change its permission mode:

```ts
import { SessionMode } from '@stacklok-oss/mecatl-sdk';

const renamed = await session.rename('Review authentication changes');
const planned = await session.setMode(SessionMode.Plan);
```

Both methods return the resulting snapshot. The SDK also refreshes the
session's media gates from that snapshot, which matters when a mode change
selects a different model.

Request one manual compaction pass with `session.compact()`. It returns `true`
when the server reduced the model-visible history and `false` when no reduction
was needed. The server decides whether a session is eligible for each mutation.

## Create a successor session

Use `clear()` to create an empty-history successor:

```ts
const cleared = await session.clear();
```

Use the sessions namespace to fork the current history:

```ts
const forked = await client.sessions.fork(session.id, {
  title: 'Try another approach',
  reasoningEffort: 'high',
});
```

Both calls return a new `Session`. They do not redirect, close, or otherwise
invalidate the source handle. Close or delete the source and successor
independently according to the application's retention policy.

`clear()` accepts an optional opaque `worktreeSelector`. `fork()` also accepts
`providerId`, `modelId`, `reasoningEffort`, `title`, and `worktreeSelector`.
The SDK passes worktree selectors to the server without interpreting them.

## Retry a failed model step

Call `retry()` after an eligible model failure:

```ts
const retry = await session.retry();
const result = await retry.result();
```

The server selects the failed step to retry. The client does not supply a run
ID or failed-step ID. `retry()` returns the same `Run` type as `run()`, with the
same event iteration, responders, controls, cancellation, terminal outcomes,
and single-consumption rule.

Domain options and request controls use separate arguments. For example, the
final argument to `run()` and `retry()` can carry a cancellation signal,
headers, or a deadline without mixing those controls into `RunOptions`:

```ts
const controller = new AbortController();
const run = await session.retry(
  { onPermissionAsk: () => 'deny' },
  { signal: controller.signal, timeoutMs: 30_000 },
);
```

The same request controls are available on session creation, loading, forking,
inspection, mutations, closing, and deletion. The SDK preserves caller headers
while adding its session-affinity hint when the session ID can be represented.

## Choose one run-consumption mode

Call `result()` when the application expects the run to complete with a terminal
result:

```ts
const run = await session.run('Summarize this repository');
const result = await run.result();

console.log(result.text, result.stopReason, result.usage);
```

Iterate the run when the application needs intermediate events:

```ts
const run = await session.run('Summarize this repository');

for await (const event of run) {
  if (event.kind === 'message.delta') process.stdout.write(event.text);
  if (event.kind === 'result' && event.payload !== undefined) {
    console.log('\nstop:', event.payload.stop);
  }
}
```

A run can be iterated, drained with `result()`, or drained with `outcome()`,
once. Calling more than one of these methods is an invalid local lifecycle
operation. Server-declared terminal outcomes such as cancellation, limits, or
budget exhaustion resolve as `RunResult` values. Transport and protocol
failures throw typed SDK errors.

## Handle a run parked for MCP authorization

Use `outcome()` when an MCP server can require external authorization. It
returns either the completed result or a detached authorization handoff:

This lifecycle applies to session-scoped ToolHive broker handoffs. Direct and
global MCP profiles use the host-local
[`mecated mcp` commands](/features/security-and-execution/mcp-oauth-and-credentials.md) instead.

```ts
const run = await session.run('Use the configured MCP server');
const outcome = await run.outcome();

if (outcome.outcome === 'completed') {
  console.log(outcome.result.text);
} else {
  const authorization = session.mcpAuthorization(
    outcome.authorization.payload.authorizationId
  );
  console.log(await authorization.presentation());
}
```

An authorization park is a normal run outcome. Event iteration yields the
final `authorization.required` event and then ends. The completed-only
`result()` method throws `RunAuthorizationRequiredError`; its `outcome`
property carries the same handoff. All three paths release the SDK's live run
ownership without cancelling or resolving the pending authorization.

The handoff contains the exact session, run, call, and authorization
correlation. It does not contain the presentation URL or transfer lifecycle
authority to the SDK. Continue the workflow as described in
[Handle permissions and plans](./permissions-and-plans.md#continue-after-mcp-authorization).

## Send controls to a live run

The owned `Run` handle keeps controls on its Converse stream and binds them to
its exact run ID:

- `run.cancel()` requests cancellation. Continue consuming the run to receive
  its terminal result.
- `run.steer(text)` sends a strict mid-run instruction. A late steer is refused
  instead of becoming a new run.
- `run.resolveAsk(askId, verdict)` resolves a pending permission ask with
  `allow_once`, `allow_always`, or `deny`.

Use automatic responders for ordinary application approval flows. See
[Handle permissions and plans](./permissions-and-plans.md).

When the application retained a run ID but no longer owns its stream, create a
prompt-free control resource from any fresh handle for the same session:

```ts
const session = await client.sessions.get(storedSessionId);
const controls = session.controls(storedRunId);

const acknowledgement = await controls.steer(
  'Include the integration test result',
  { messageId: crypto.randomUUID() },
  { timeoutMs: 10_000 }
);

console.log(acknowledgement.outcome, acknowledgement.messageId);
```

`session.controls(runId)` is synchronous and does not probe the server, open a
watch, or register a live run. Each operation first checks that the server
advertises `prompt_free_controls`, then makes one unary request:

- `resolveAsk(askId, verdict, requestOptions?)` resolves an ordinary root or
  surfaced-child permission ask.
- `cancel(requestOptions?)` requests cancellation of the exact live run.
- `steer(prompt, options?, requestOptions?)` sends text or structured image and
  audio input.
- `cancelSteer(options?, requestOptions?)` retracts the pending steer bundle.

All four methods accept the same request headers, cancellation signal, response
callbacks, and deadline controls as other SDK unary calls. The SDK performs no
automatic retry or fallback. A server without `prompt_free_controls` raises a
typed `UnsupportedFeatureError` before the SDK sends a control RPC.

Every operation is strict about `runId`. A run that ended, is cancelling, or
was replaced returns the server's typed `stale_run_control` error. A delayed
steer cannot become a new run. Plan asks remain under the separate
`session.resolvePlan()` workflow.

Structured steer prompts use the same `PromptInput` and media validation as a
new run. Text fragments join with one newline, while image and audio parts keep
their order relative to other media. `messageId` is an optional application
correlation of up to 64 Unicode code points. The acknowledgement echoes the
exact run and message IDs. A steer reports `accepted` when it creates a pending
bundle or `appended` when it joins an existing bundle. Retraction reports
`retracted` or `none_pending`.

Treat a lost unary acknowledgement as ambiguous. The server can accept a
control before the connection, caller cancellation, or deadline prevents the
response from reaching the application. Reconcile the operation from the
authoritative session snapshot and durable activity before deciding whether to
retry it. See [Resume durable activity](./durable-activity.md).

## Send image or audio input

Structured prompts combine text with image or audio parts. Node.js and Bun can
read a local path through the `/node` entry point:

```ts
import { imagePartFromPath, textPart } from '@stacklok-oss/mecatl-sdk/node';

const image = await imagePartFromPath(
  new URL('./diagram.png', import.meta.url),
  'image/png'
);
const run = await session.run([textPart('Explain this diagram'), image]);

console.log((await run.result()).text);
```

Browser applications use `imagePartFromBlob()` or `audioPartFromBlob()`. Both
entry points also accept an absolute HTTPS media URL. The SDK validates the
source, MIME type, size, and advertised session capability before sending the
prompt; the server remains authoritative.

## Next steps

- [Handle permissions and plans](./permissions-and-plans.md) to automate narrow
  approval decisions.
- [Resume durable activity](./durable-activity.md) to observe runs after a
  disconnect or application restart.

## Related information

- [TypeScript SDK core API](/reference/typescript-sdk-api/core.md) for the full
  `Session`, `Run`, event, and media surfaces.
- [Start and resume sessions](/features/sessions/start-and-resume-sessions.md)
- [Multimodal input](/features/sessions/multimodal-input.md)
- [Agent loop](/features/sessions/agent-loop.md)
