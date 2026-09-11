# TypeScript SDK examples

These small programs compile against the built `@stacklok/mecatl-sdk` package exports. They
are deliberately independent examples, not an application framework. Node.js 22+ and Bun use
the same ESM `./node` entry point.

| Example | Focus |
| --- | --- |
| [`quickstart.ts`](./quickstart.ts) | Run one prompt against a private offline daemon. |
| [`remote-connect.ts`](./remote-connect.ts) | Connect to an operator-owned daemon from Node or Bun. |
| [`local-spawn.ts`](./local-spawn.ts) | Spawn and dispose a private local daemon. |
| [`one-shot-query.ts`](./one-shot-query.ts) | Run one local prompt with `query()`. |
| [`callback-tool.ts`](./callback-tool.ts) | Register one local callback tool before session creation. |
| [`browser-bff.ts`](./browser-bff.ts) | Show the browser side of the recommended same-origin BFF deployment. This is guidance, not shipped BFF server code. |
| [`permissions.ts`](./permissions.ts) | Resolve permission asks with a narrow callback. |
| [`run-events.ts`](./run-events.ts) | Consume one run as a typed event stream. |
| [`multimodal.ts`](./multimodal.ts) | Send text and a local image in one prompt. |
| [`durable-attachment.ts`](./durable-attachment.ts) | Resume durable cross-run activity from an application-owned cursor. |
| [`teams.ts`](./teams.ts) | Create, run, and explicitly clean up a direct team. |
| [`schedules.ts`](./schedules.ts) | Create a recurring schedule through the typed namespace. |
| [`plan-resolution.ts`](./plan-resolution.ts) | Resolve a parked plan across its resumed and continuation runs. |

Run the compiler gate from the repository root:

```sh
task sdk:examples:typecheck
```

The larger [`slack-bot/`](./slack-bot/) example is intentionally a separate pnpm project. Its
own `task slack-bot:typecheck` CI leg builds the SDK and type-checks that application against the
same package exports.
