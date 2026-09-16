import { connect, type SdkCursor } from "@stacklok-oss/mecatl-sdk/node";

type DurableState = {
  cursor?: SdkCursor;
  runId?: string;
};

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});
const sessionId = process.env.MECATL_SESSION_ID;
if (sessionId === undefined) throw new Error("MECATL_SESSION_ID is required");

// Load this value from application-owned durable storage in production.
const durableState = JSON.parse(process.env.MECATL_DURABLE_STATE ?? "{}") as DurableState;

// A replacement process can load a fresh Session and control the exact stored run
// without opening or owning its activity stream.
const session = await client.sessions.get(sessionId);
const instruction = process.env.MECATL_STEER;
if (durableState.runId !== undefined && instruction !== undefined) {
  const acknowledgement = await session.controls(durableState.runId).steer(instruction, {
    messageId: crypto.randomUUID(),
  });
  console.log("steer acknowledgement", acknowledgement);
}

await using activity = await session.activity(
  durableState.cursor === undefined ? { from: "start" } : { from: durableState.cursor },
);

for await (const envelope of activity) {
  if (envelope.kind === "event" && envelope.event.runId !== "") {
    durableState.runId = envelope.event.runId;
  }
  if ("cursor" in envelope) {
    durableState.cursor = envelope.cursor;
    // Persist after the application's side effect. Resuming may redeliver this envelope.
    console.log("persist", JSON.stringify(durableState));
  }
}
