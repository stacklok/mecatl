import { describe, expect, it } from "vitest";

import { connect } from "../src/index.js";
import { cannedMockReply, collectRun, withDaemon } from "./harness.js";

describe("offline HTTP wire", () => {
  it("full run lifecycle over HTTP/SSE", async () => {
    await withDaemon({ http: true }, async ({ ready, workspace }) => {
      if (ready.http_address === undefined) throw new Error("mecated omitted HTTP readiness");
      const client = connect({ baseUrl: `http://${ready.http_address}` });
      try {
        const session = await client.sessions.create({ workspace });
        expect(client.status.getSnapshot()).toBe("online");
        const { events, terminal } = await collectRun(await session.run("hello over HTTP/SSE"));
        expect(events.find((event) => event.kind === "message.delta")).toMatchObject({
          kind: "message.delta",
          text: cannedMockReply,
        });
        expect(terminal.payload).toMatchObject({ stop: "end_turn", text: cannedMockReply });
        await session.delete();
      } finally {
        await client.close();
      }
    });
  });
});
