---
title: Register callback tools
description:
  Expose local Node.js or Bun handlers as tools on an SDK-owned Mecatl daemon.
sidebar_position: 7
---

# Register callback tools

A client returned by `spawn()` can expose Node.js or Bun functions as MCP tools.
Register the complete callback-tool set before the client creates its first
session.

## Register a tool

This local example uses a scripted offline provider so it produces the same tool
call without model-provider credentials. Create `callback-tool-script.json`:

```json title="callback-tool-script.json"
{
  "turns": [
    {
      "tool_calls": [
        {
          "id": "lookup-1",
          "name": "mcp__sdk__lookup_issue",
          "args": { "issue": 821 }
        }
      ]
    },
    { "text": "The callback tool returned the issue status." }
  ]
}
```

Start a private daemon, register the tool, and allow only its permission ask:

```ts
import { fileURLToPath } from 'node:url';
import { spawn } from '@stacklok-oss/mecatl-sdk/node';

const mockScript = fileURLToPath(
  new URL('./callback-tool-script.json', import.meta.url)
);
await using client = await spawn({
  args: ['--authority-evaluator', 'noop', '--mock-script', mockScript],
});

const tool = client.tool(
  'lookup_issue',
  {
    additionalProperties: false,
    properties: { issue: { type: 'number' } },
    required: ['issue'],
    type: 'object',
  },
  ({ issue }) => `Issue ${String(issue)} is ready for review`,
  { readOnly: true }
);

const session = await client.sessions.create({});
const run = await session.run('Look up issue 821', {
  onPermissionAsk: (ask) =>
    ask.tool === tool.modelName ? 'allow_once' : 'deny',
});

console.log((await run.result()).text);
```

Callback tools currently require the `noop` authority evaluator because their
per-session names are absent from the daemon's root capability set. Both the
default local evaluator and the Cedar evaluator deny them. Because `noop`
disables authority evaluation, use callback tools only in a controlled local
environment until this limitation is removed.

The schema is JSON Schema 2020-12. Invalid schemas, duplicate names, reserved
namespaces, and invalid options fail locally with `ToolRegistrationError`.

## Set the dispatch behavior

Callback tools are mutating by default. Set `readOnly: true` only when the
handler has no side effects. Mecatl uses that declaration when deciding whether
tool calls can execute concurrently; the SDK cannot verify it.

Use `concurrency` to tighten the per-tool execution limit. The client also
bounds total handler concurrency and queueing.

## Return results safely

A handler can return a string, a JSON value, or an explicit `CallToolResult`.
Thrown values become generic model-visible failures with a correlation ID. The
original failure is sent only to the client's `diagnostics` callback.

Return an explicit result with `isError: true` when the model should receive an
intentional domain error and be able to respond to it.

Each handler receives an `AbortSignal`. Stop application work when it is
aborted. Client disposal aborts running handlers, drops queued calls, and closes
the local MCP listener.

## Understand the deployment boundary

Callback tools require a private SDK-owned daemon that advertises
`mcp_servers_on_create`. A client created with remote `connect()` refuses
registration with `UnsupportedFeatureError`.

The SDK hosts one streaming-HTTP MCP endpoint on `127.0.0.1` and protects it
with a random bearer capability passed to the daemon over its private Unix
socket. The registered tool set becomes immutable after the first successful
session creation.

## Next steps

- [Work with sessions and runs](./sessions-and-runs.md) to consume callback-tool
  events and terminal results.
- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
  to configure server-side tool decisions.

## Related information

- [TypeScript SDK Node.js and Bun API](/reference/typescript-sdk-api/node.md)
  for callback schemas, handlers, results, and error types.
- [MCP client](/features/security-and-execution/mcp-client.md)
- [Tool catalog extension point](/building/extension-points/tool-catalog.md)
