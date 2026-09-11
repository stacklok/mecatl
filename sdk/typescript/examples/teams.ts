import { connect } from "@stacklok-oss/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});
const session = await client.sessions.create({});
const team = await client.teams.create({
  goal: "Review the change and return one report",
  members: [
    { agentType: "explorer", lead: true, name: "lead" },
    { agentType: "reviewer", name: "reviewer" },
  ],
  sessionId: session.id,
});

try {
  console.log(await team.run().result());
} finally {
  await team.cleanup();
}
