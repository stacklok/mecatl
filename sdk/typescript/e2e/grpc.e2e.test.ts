import { describe, expect, it } from "vitest";

import { connect } from "../src/node.js";
import { cannedMockReply, collectRun, withDaemon } from "./harness.js";

describe("offline gRPC wire", () => {
  it("full run lifecycle over gRPC", async () => {
    for (const transport of ["tcp", "uds"] as const) {
      await withDaemon({ uds: transport === "uds" }, async ({ ready }) => {
        if (transport === "uds" && ready.socket_path === undefined) {
          throw new Error("mecated UDS readiness omitted socket_path");
        }
        const client =
          transport === "uds"
            ? connect({ socketPath: ready.socket_path as string })
            : connect({ baseUrl: `http://${ready.grpc_address}` });
        try {
          const session = await client.sessions.create({});
          expect(client.status.getSnapshot()).toBe("online");
          const { events, terminal } = await collectRun(
            await session.run(`hello over ${transport}`),
          );
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
    }
  });
});
