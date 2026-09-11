import { connect } from "@stacklok-oss/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});
const session = await client.sessions.create({});
const run = await session.run("Summarize this repository");

for await (const event of run) {
  if (event.kind === "message.delta") process.stdout.write(event.text);
  if (event.kind === "result" && event.payload !== undefined) {
    console.log("\nstop:", event.payload.stop);
  }
}
