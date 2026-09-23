# TypeScript SDK examples

These small programs compile against the built `@stacklok-oss/mecatl-sdk` package exports. They
are deliberately independent examples, not an application framework. The session lifecycle,
capability discovery, and MCP enrollment examples use the root HTTP entry point. They run on
Node.js 22+ and Bun. The MCP authorization example uses `./node` for gRPC. Other Node.js and Bun
examples that need gRPC or local spawn also use `./node`. Deno 2.9.3 through Deno 2.x uses
`./deno` for gRPC and `Deno.Command` local ownership, or `.` for remote HTTP/SSE.

| Example | Focus |
| --- | --- |
| [`quickstart.ts`](./quickstart.ts) | Run one prompt against a private offline daemon. |
| [`remote-connect.ts`](./remote-connect.ts) | Connect to an operator-owned daemon from Node or Bun. |
| [`deno-remote.ts`](./deno-remote.ts) | Connect to an operator-owned daemon from Deno over gRPC. |
| [`deno-local.ts`](./deno-local.ts) | Start and own a local daemon with `Deno.Command`. |
| [`local-spawn.ts`](./local-spawn.ts) | Spawn and dispose a private local daemon. |
| [`one-shot-query.ts`](./one-shot-query.ts) | Run one local prompt with `query()`. |
| [`callback-tool.ts`](./callback-tool.ts) | Register one local callback tool before session creation. |
| [`session-lifecycle.ts`](./session-lifecycle.ts) | Inspect and rename a session, then fork and clear into independent successors. |
| [`capability-discovery.ts`](./capability-discovery.ts) | Check deployment capabilities and safe server identity before creating a session. |
| [`mcp-workspace-enrollment.ts`](./mcp-workspace-enrollment.ts) | Inspect connectors, start enrollment, and observe it after an application-controlled authorization step. |
| [`browser-bff.ts`](./browser-bff.ts) | Show the browser side of the recommended same-origin BFF deployment. This is guidance, not shipped BFF server code. |
| [`permissions.ts`](./permissions.ts) | Resolve permission asks with a narrow callback. |
| [`mcp-authorization.ts`](./mcp-authorization.ts) | Present and explicitly recheck a parked MCP authorization, including chained authorization and bounded recovery. |
| [`run-events.ts`](./run-events.ts) | Consume one run as a typed event stream. |
| [`multimodal.ts`](./multimodal.ts) | Send text and a local image in one prompt. |
| [`durable-attachment.ts`](./durable-attachment.ts) | Resume durable cross-run activity from an application-owned cursor. |
| [`teams.ts`](./teams.ts) | Create, run, and explicitly clean up a direct team. |
| [`schedules.ts`](./schedules.ts) | Create a recurring schedule through the typed namespace. |
| [`plan-resolution.ts`](./plan-resolution.ts) | Resolve a parked plan across its resumed and continuation runs. |

Run the compiler gate from the repository root:

```sh
task sdk:examples:typecheck
task sdk:deno
task sdk:e2e
```

`task sdk:e2e` compiles and executes the four session lifecycle, capability discovery, MCP
enrollment, and MCP authorization workflow entry points against offline fixtures. It also runs
the rest of the SDK wire suite.

To run one workflow example against a daemon, build the SDK and compile the examples from
`sdk/typescript/`:

```sh
pnpm run build
pnpm exec tsc -p examples/tsconfig.json --noEmit false --outDir .api-extractor-temp/examples
MECATL_URL=http://127.0.0.1:8080 node .api-extractor-temp/examples/capability-discovery.js
```

Run `session-lifecycle.js` against an HTTP endpoint that allows session creation and mutation.
`mcp-workspace-enrollment.js` needs a deployment with connector inspection and workspace
enrollment enabled for the authenticated principal. Complete the printed authorization URL in
your application, then press Enter to make one explicit status observation. The
`mcp-authorization.js` example uses the Node gRPC entry point and needs an OAuth-protected MCP
broker that can park a run for authorization; set `MECATL_URL` to its gRPC address, complete the
printed URL, and press Enter to recheck. The SDK does not open either URL or choose when to
recheck.

The larger [`slack-bot/`](./slack-bot/) example is intentionally a separate pnpm project. Its
own `task slack-bot:typecheck` CI leg builds the SDK and type-checks that application against the
same package exports.
