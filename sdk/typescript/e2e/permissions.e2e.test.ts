import { describe, expect, it } from "vitest";

import { connect } from "../src/node.js";
import { collectRun, fixture, withDaemon } from "./harness.js";

describe("offline permission wire", () => {
  it("asks resolve through the responder end to end", async () => {
    await withDaemon({ script: fixture("permissions.json") }, async ({ ready, workspace }) => {
      const client = connect({ baseUrl: `http://${ready.grpc_address}` });
      try {
        const approvedSession = await client.sessions.create({ workspace });
        const approved = await collectRun(
          await approvedSession.run("approve the scripted write", {
            onPermissionAsk: () => "allow_once",
          }),
        );
        expect(approved.events.some((event) => event.kind === "permission.ask")).toBe(true);
        expect(
          approved.events.some(
            (event) => event.kind === "tool.result" && event.payload.isError === false,
          ),
        ).toBe(true);
        expect(approved.terminal.payload).toMatchObject({ stop: "end_turn" });
        await approvedSession.delete();

        const deniedSession = await client.sessions.create({ workspace });
        const denied = await collectRun(
          await deniedSession.run("deny the scripted write", {
            onPermissionAsk: () => "deny",
          }),
        );
        expect(denied.events.some((event) => event.kind === "permission.ask")).toBe(true);
        expect(
          denied.events.some(
            (event) => event.kind === "tool.result" && event.payload.isError === true,
          ),
        ).toBe(true);
        expect(
          denied.events.some(
            (event) =>
              event.kind === "message.delta" && event.text.includes("continued after denial"),
          ),
        ).toBe(true);
        expect(denied.terminal.payload).toMatchObject({ stop: "end_turn" });
        await deniedSession.delete();
      } finally {
        await client.close();
      }
    });
  });
});
