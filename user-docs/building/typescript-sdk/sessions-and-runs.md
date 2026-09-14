---
title: Work with sessions and runs
description:
  Create Mecatl sessions, consume run events or results, send controls, and
  attach media.
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

## Choose one run-consumption mode

Call `result()` when the application needs only the terminal outcome:

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

A run can be iterated or drained with `result()`, once. Calling both is an
invalid local lifecycle operation. Server-declared terminal outcomes such as
cancellation, limits, or budget exhaustion resolve as `RunResult` values.
Transport and protocol failures throw typed SDK errors.

## Send controls to a live run

The `Run` handle binds controls to its exact run ID:

- `run.cancel()` requests cancellation. Continue consuming the run to receive
  its terminal result.
- `run.steer(text)` sends a strict mid-run instruction. A late steer is refused
  instead of becoming a new run.
- `run.resolveAsk(askId, verdict)` resolves a pending permission ask with
  `allow_once`, `allow_always`, or `deny`.

Use automatic responders for ordinary application approval flows. See
[Handle permissions and plans](./permissions-and-plans.md).

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
- [Start and resume sessions](/features/start-and-resume-sessions.md)
- [Multimodal input](/features/multimodal-input.md)
- [Agent loop](/building/what-you-get/agent-loop.md)
