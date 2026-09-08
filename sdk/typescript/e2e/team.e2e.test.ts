import { describe, expect, it } from "vitest";

import { ServerError } from "../src/index.js";
import { connect } from "../src/node.js";
import { withDaemon } from "./harness.js";

describe("offline direct team wire", () => {
  it("team member session ids remain child ids that attach refuses against the real daemon", async () => {
    await withDaemon({}, async ({ ready }) => {
      const client = connect({ baseUrl: `http://${ready.grpc_address}` });
      try {
        const session = await client.sessions.create({});
        const team = await client.teams.create({
          goal: "return one concise report",
          members: [
            {
              initialPrompt: "Finish without creating tasks.",
              lead: true,
              name: "lead",
            },
          ],
          sessionId: session.id,
        });
        const childId = team.initialMembers[0]?.sessionId;
        expect(childId).toMatch(/^team-.+-lead$/u);

        await expect(team.run().result()).resolves.toMatchObject({ stop: "end_turn" });
        if (childId === undefined) throw new Error("CreateTeam omitted the lead session id");
        const child = await client.sessions.get(childId);
        const refused = await child.attach().catch((error: unknown) => error);
        expect(refused).toBeInstanceOf(ServerError);
        expect(refused).toMatchObject({ code: "invalid_argument", transport: "grpc" });

        await team.cleanup();
        await session.delete();
      } finally {
        await client.close();
      }
    });
  });
});
