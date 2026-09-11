import { PermissionMode } from "@stacklok/mecatl-sdk/gen";
import { connect } from "@stacklok/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});
const session = await client.sessions.create({});
const run = await session.run("Inspect the repository without changing it", {
  onPermissionAsk: (ask) => (ask.tool === "Read" ? "allow_once" : "deny"),
});

console.log((await run.result()).text);

const planSession = await client.sessions.create({ mode: PermissionMode.PLAN });
const planRun = await planSession.run("Plan and implement the requested change", {
  onPlanApproval: (_ask, signal) => (signal.aborted ? undefined : "approve"),
});

console.log((await planRun.result()).stopReason);
