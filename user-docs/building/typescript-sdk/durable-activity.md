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
exact run. Approval and steer controls on a durable attachment report a typed
unsupported-feature error; use a live `Run` handle for those controls.

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
mutations, or owned runs automatically.

## Next steps

- [Handle permissions and plans](./permissions-and-plans.md) for the live-run
  controls that durable attachments do not expose.
- [Session continuity](/features/session-continuity.md) for the server-side
  persistence model.

## Related information

- [TypeScript SDK core API](/reference/typescript-sdk-api/core.md) for cursor,
  envelope, and attachment types.
- [HTTP and SSE API reference](/reference/http-sse-api.md)
- [gRPC API reference](/reference/grpc-api.md)
