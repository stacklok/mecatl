import { connect } from "@stacklok-oss/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});
const sessionId = process.env.MECATL_SESSION_ID;
if (sessionId === undefined) throw new Error("MECATL_SESSION_ID is required");

const session = await client.sessions.get(sessionId);
const previous = process.env.MECATL_CURSOR;
await using activity = await session.activity(
  previous === undefined ? { from: "start" } : { from: previous },
);

for await (const envelope of activity) {
  if ("cursor" in envelope) {
    // Persist after the application's side effect. Resuming may redeliver this envelope.
    console.log("checkpoint", envelope.cursor);
  }
}
