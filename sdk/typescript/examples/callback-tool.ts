import { spawn } from "@stacklok/mecatl-sdk/node";

await using client = await spawn({ args: ["--authority-evaluator", "noop"] });

client.tool(
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
console.log((await (await session.run("Look up issue 821")).result()).text);
