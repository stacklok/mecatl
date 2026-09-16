---
title: Resume durable activity
description:
  Replay and follow Mecatl run or session activity from an application-owned
  cursor.
sidebar_position: 6
---

# Resume durable activity

Use `session.attach()` to replay and follow one run. Use `session.activity()`
for the ordered cross-run session timeline, including schedule activity. Both
views expose a serializable cursor for application-owned checkpoint storage.
Watching and controlling are independent. A stored session ID and run ID are
enough to create controls without opening a durable view.

## Follow a session timeline

Load the session, restore its previous cursor when present, and open the
activity stream:

```ts
import { connect } from '@stacklok-oss/mecatl-sdk/node';

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? 'http://127.0.0.1:8080',
});
const sessionId = process.env.MECATL_SESSION_ID;
if (sessionId === undefined) throw new Error('MECATL_SESSION_ID is required');

const session = await client.sessions.get(sessionId);
const previous = process.env.MECATL_CURSOR;
await using activity = await session.activity(
  previous === undefined ? { from: 'start' } : { from: previous }
);

for await (const envelope of activity) {
  if ('cursor' in envelope) {
    // Persist after the application's side effect.
    console.log('checkpoint', envelope.cursor);
  }
}
```

Replace the checkpoint log with application-owned persistence. Commit each side
effect before its cursor. Delivery of durably appended events is ordered and at
least once, so make the side effect idempotent. A storage failure can create the
gaps described under
[Handle terminal watch errors](#handle-terminal-watch-errors).

The SDK stores reconnect state only in memory. It does not write cursors to
browser storage or the filesystem.

## Attach to one run

Pass a run ID when the application already knows which run to follow:

```ts
const runId = process.env.MECATL_RUN_ID;
if (runId === undefined) throw new Error('MECATL_RUN_ID is required');

await using attached = await session.attach(runId, { from: 'start' });

for await (const envelope of attached) {
  if (envelope.kind === 'event') console.log(envelope.event);
}
```

Without a run ID, `attach()` scans the durable replay and selects the newest
run. Use an explicit run ID with `from: "now"` because a live-only view has no
replay from which to select a run.

An attached run ends after its terminal result. `attached.cancel()` cancels the
exact run through its established transport-specific path. Approval and steer
methods on a durable attachment return a typed unsupported-feature error and
direct the application to `session.controls(attached.runId)`.

## Control a stored run without watching

Persist the run ID alongside the activity cursor. A replacement process can
load a fresh session handle and address that exact run:

```ts
const session = await client.sessions.get(storedSessionId);
const controls = session.controls(storedRunId);

const acknowledgement = await controls.steer('Focus on the failing test', {
  messageId: crypto.randomUUID(),
});
console.log(acknowledgement.runId, acknowledgement.messageId);
```

Creating `controls` does not open, resume, or retain an attachment. Each method
makes one unary request, so watching the run does not affect control authority.
The server applies the operation only when `storedRunId` is still the exact
eligible run.

A transport failure, caller cancellation, or deadline can happen after the
server accepts a control but before the acknowledgement reaches the
application. Reconcile that ambiguous case from the authoritative session
snapshot and durable activity before retrying. Approval, steer, retraction, and
terminal events provide the durable observation path.

## Include log-only events

Durable views omit audit-oriented records such as user prompts and compaction
archives by default. Pass `includeLogOnly: true` to include them. The cursor
still advances over omitted records, preserving the source log's order.

## Handle terminal watch errors

- `CursorExpiredError` means the underlying log generation changed. Choose an
  explicit restart from the beginning or reload the session transcript.
- `ActivityGapError` means durable delivery contains a known gap. Retrying
  cannot recover the missing records.
- `CursorScopeError` means a cursor was reused with a wider run or event filter.
- `NoRunsError` means the readable session log contains no run-bearing event.

Transient classified read failures reconnect with bounded backoff. This includes
`watch_capacity`, which means the server's durable-follow admission limit is
full, and `watch_lagging`, which means the client did not consume the server's
bounded delivery buffer quickly enough. Both reconnect from the last processed
cursor with the same filter. The SDK does not retry prompts, approvals,
controls, mutations, or owned runs automatically.

## Next steps

- [Handle permissions and plans](./permissions-and-plans.md) to resolve ordinary
  asks on an exact run and keep plan resolution separate.
- [Session continuity](/features/session-continuity.md) for the server-side
  persistence model.

## Related information

- [TypeScript SDK core API](/reference/typescript-sdk-api/core.md) for cursor,
  envelope, and attachment types.
- [HTTP and SSE API reference](/reference/http-sse-api.md)
- [gRPC API reference](/reference/grpc-api.md)
