import { setTimeout as delay } from "node:timers/promises";

import { describe, expect, it } from "vitest";

import { errorFromProblem, type ProblemDetails } from "../src/errors.js";
import { connect as connectHttp, ServerError } from "../src/index.js";
import { connect as connectGrpc } from "../src/node.js";
import { fixture, withDaemon } from "./harness.js";

describe("offline control wire", () => {
  it("cancel and stale controls on the real wire", async () => {
    await withDaemon({ script: fixture("cancel.json") }, async ({ ready, workspace }) => {
      const client = connectGrpc({ baseUrl: `http://${ready.grpc_address}` });
      try {
        const session = await client.sessions.create({ workspace });
        const run = await session.run("cancel this delayed turn");
        await run.cancel();
        await expect(run.result()).resolves.toMatchObject({ stopReason: "cancelled" });
        await session.delete();
      } finally {
        await client.close();
      }
    });

    await withDaemon(
      { http: true, script: fixture("stale-control.json") },
      async ({ ready, workspace }) => {
        if (ready.http_address === undefined) throw new Error("mecated omitted HTTP readiness");
        const client = connectHttp({ baseUrl: `http://${ready.http_address}` });
        try {
          const oldSession = await client.sessions.create({ workspace });
          const nextSession = await client.sessions.get(oldSession.id);
          const oldRun = await oldSession.run("finish the old run");
          await delay(100);
          const nextRun = await nextSession.run("keep the next run active");

          const response = await fetch(
            `http://${ready.http_address}/v1/sessions/${encodeURIComponent(oldSession.id)}/cancel`,
            {
              body: JSON.stringify({ expected_run_id: oldRun.id }),
              headers: { "content-type": "application/json" },
              method: "POST",
            },
          );
          const stale = errorFromProblem(
            (await response.json()) as ProblemDetails,
            response.status,
            response.headers.get("x-request-id") ?? undefined,
          );
          expect(response.status).toBe(409);
          expect(stale).toBeInstanceOf(ServerError);
          expect(stale).toMatchObject({ code: "stale_run_control" });
          await expect(oldRun.result()).resolves.toMatchObject({
            stopReason: "end_turn",
            text: "old run finished",
          });

          await expect(nextRun.result()).resolves.toMatchObject({
            stopReason: "end_turn",
            text: "next run remained untouched",
          });
          await nextSession.delete();
        } finally {
          await client.close();
        }
      },
    );
  });
});
