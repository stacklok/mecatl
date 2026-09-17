import { create } from "@bufbuild/protobuf";
import { durationFromMs, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import { PermissionMode } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  CreateScheduleRequestSchema,
  DeleteScheduleRequestSchema,
  GetScheduleRequestSchema,
} from "../src/gen/mecatl/v1/schedule_pb.js";
import { connect, getRawJson } from "../src/index.js";
import { cannedMockReply, collectRun, withDaemon } from "./harness.js";

describe("offline HTTP wire", () => {
  it("full run lifecycle over HTTP/SSE", async () => {
    await withDaemon({ http: true }, async ({ ready }) => {
      if (ready.http_address === undefined) throw new Error("mecated omitted HTTP readiness");
      const client = connect({ baseUrl: `http://${ready.http_address}` });
      try {
        const session = await client.sessions.create({});
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

  it("schedule timestamps and durations decode from the real daemon", async () => {
    await withDaemon({ durable: true, http: true }, async ({ ready }) => {
      if (ready.http_address === undefined) throw new Error("mecated omitted HTTP readiness");
      const client = connect({ baseUrl: `http://${ready.http_address}` });
      const oneShot = timestampFromDate(new Date(Date.now() + 3_600_123));
      const fireTimeout = durationFromMs(1_500);
      try {
        const created = await client.schedules.create(
          create(CreateScheduleRequestSchema, {
            spec: {
              fireTimeout,
              mode: PermissionMode.PLAN,
              name: "http-wkt-e2e",
              prompt: "verify HTTP schedule decoding",
              trigger: { oneShot },
            },
          }),
        );
        const response = await client.schedules.get(
          create(GetScheduleRequestSchema, { name: "http-wkt-e2e" }),
        );

        for (const result of [created, response]) {
          expect(result.schedule?.spec?.trigger?.oneShot).toEqual(oneShot);
          expect(result.schedule?.spec?.fireTimeout).toEqual(fireTimeout);
          expect(getRawJson(result)).toMatchObject({
            schedule: {
              spec: {
                fire_timeout: { nanos: 500_000_000, seconds: 1 },
                trigger: {
                  one_shot: { nanos: oneShot.nanos, seconds: Number(oneShot.seconds) },
                },
              },
            },
          });
        }
      } finally {
        await client.schedules.delete(
          create(DeleteScheduleRequestSchema, { name: "http-wkt-e2e" }),
        );
        await client.close();
      }
    });
  });
});
