import { connect } from "@stacklok/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});
const sessionId = process.env.MECATL_SESSION_ID;
if (sessionId === undefined) throw new Error("MECATL_SESSION_ID is required");

const session = await client.sessions.get(sessionId);
const { continuation, resumed } = await session.resolvePlan("approve").result();

console.log("resumed", resumed.runId);
if (continuation !== undefined) console.log("continuation", continuation.runId);
