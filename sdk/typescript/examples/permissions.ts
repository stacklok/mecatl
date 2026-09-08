import { connect } from "@stacklok/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8081",
});
const session = await client.sessions.create({});
const run = await session.run("Inspect the repository without changing it", {
  onPermissionAsk: (ask) => (ask.tool === "Read" ? "allow_once" : "deny"),
});

console.log((await run.result()).text);
