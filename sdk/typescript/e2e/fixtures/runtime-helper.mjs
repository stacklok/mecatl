import { access } from "node:fs/promises";
import { dirname } from "node:path";

import { spawn } from "../../dist/node.js";

const [mode, binaryPath, script, workspace] = process.argv.slice(2);
if (
  mode === undefined ||
  binaryPath === undefined ||
  script === undefined ||
  workspace === undefined
) {
  throw new Error("usage: runtime-helper.mjs <hold|roundtrip> <mecated> <script> <workspace>");
}

const client = await spawn({
  args: [
    "--mock-script",
    script,
    "--workspace",
    workspace,
    "--no-soul",
    "--no-user-model",
    "--no-scheduler",
    "--flight-recorder=false",
    "--authority-evaluator",
    "noop",
  ],
  binaryPath,
});
const runtimeDirectory = dirname(client.daemon.socketPath);

if (mode === "hold") {
  process.stdout.write(`${JSON.stringify({ daemonPid: client.daemon.pid, runtimeDirectory })}\n`);
  setInterval(() => undefined, 60_000);
} else if (mode === "roundtrip") {
  let invoked = false;
  client.tool(
    "lookup",
    {
      additionalProperties: false,
      properties: { query: { type: "string" } },
      required: ["query"],
      type: "object",
    },
    ({ query }) => {
      invoked = true;
      return `bun lookup: ${query}`;
    },
    { readOnly: true },
  );
  const session = await client.sessions.create({});
  const events = [];
  const run = await session.run("exercise the callback", {
    onPermissionAsk: () => "allow_once",
  });
  for await (const event of run) events.push(event);
  const result = events.find(
    (event) => event.kind === "tool.result" && event.payload.callId === "lookup-1",
  );
  const terminal = events.findLast((event) => event.kind === "result");
  await client.close();
  let runtimeRemoved = false;
  try {
    await access(runtimeDirectory);
  } catch (error) {
    runtimeRemoved = error?.code === "ENOENT";
  }
  process.stdout.write(
    `${JSON.stringify({
      content: result?.kind === "tool.result" ? result.payload.content : undefined,
      daemonPid: client.daemon.pid,
      invoked,
      runtimeDirectory,
      runtimeRemoved,
      stop: terminal?.kind === "result" ? terminal.payload.stop : undefined,
    })}\n`,
  );
} else {
  await client.close();
  throw new Error(`unknown helper mode ${JSON.stringify(mode)}`);
}
