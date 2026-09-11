import { fileURLToPath } from "node:url";
import { spawn } from "@stacklok/mecatl-sdk/node";

const mockScript = fileURLToPath(new URL("./callback-tool-script.json", import.meta.url));
await using client = await spawn({
  args: ["--authority-evaluator", "noop", "--mock-script", mockScript],
});

const tool = client.tool(
  "lookup_issue",
  {
    additionalProperties: false,
    properties: { issue: { type: "number" } },
    required: ["issue"],
    type: "object",
  },
  ({ issue }) => `Issue ${String(issue)} is ready for review`,
  { readOnly: true },
);

const session = await client.sessions.create({});
const run = await session.run("Look up issue 821", {
  onPermissionAsk: (ask) => (ask.tool === tool.modelName ? "allow_once" : "deny"),
});

console.log((await run.result()).text);
