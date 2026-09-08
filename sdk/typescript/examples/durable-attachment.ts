import { connect } from "@stacklok/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8081",
});
const session = await client.sessions.get(process.env.MECATL_SESSION_ID ?? "session-id");
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
