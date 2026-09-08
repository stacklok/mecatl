import { connect } from "@stacklok/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8081",
});
const session = await client.sessions.get(process.env.MECATL_SESSION_ID ?? "session-id");
const { continuation, resumed } = await session.resolvePlan("approve").result();

console.log("resumed", resumed.runId);
if (continuation !== undefined) console.log("continuation", continuation.runId);
