import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, ServerError } from "../src/index.js";

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function staleControl(): ConnectError {
  const error = new ConnectError("run is no longer current", Code.Aborted);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([
      ...field(1, bytes("stale_run_control")),
      ...field(2, bytes("mecatl.stacklok.com")),
    ]),
  });
  return error;
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

function event(runId: string, text: string) {
  return { event: { runId, text, type: "message.delta" } };
}

function terminal(runId: string) {
  return { event: { result: { stop: "end_turn", text: "done" }, runId, type: "result" } };
}

describe("run controls", () => {
  it("ergonomic controls always carry expected_run_id and stale controls fail typed", async () => {
    const controls: Array<{ expectedRunId: string; kind: string }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-controls" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield {
            event: {
              ask: { askId: "ask-1", tool: "Shell" },
              runId: "run-controls",
              type: "permission.ask",
            },
          };
          for (let index = 0; index < 3; index += 1) {
            const next = await input.next();
            const kind = next.value?.kind;
            if (kind?.case === "resumeApproval") {
              controls.push({ expectedRunId: kind.value.expectedRunId, kind: "approve" });
            } else if (kind?.case === "steer") {
              controls.push({ expectedRunId: kind.value.expectedRunId, kind: "steer" });
            } else if (kind?.case === "cancel") {
              controls.push({ expectedRunId: kind.value.expectedRunId, kind: "cancel" });
            }
          }
          yield terminal("run-controls");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start");
    await run.resolveAsk("ask-1", "allow_once");
    await run.steer("continue differently");
    await run.cancel();
    await run.result();

    expect(controls).toEqual([
      { expectedRunId: "run-controls", kind: "approve" },
      { expectedRunId: "run-controls", kind: "steer" },
      { expectedRunId: "run-controls", kind: "cancel" },
    ]);
    await client.close();

    const finishNewer = deferred();
    let runNumber = 0;
    let newerEvents = 0;
    const raceTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-race" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: () => ({ session: { sessionId: "session-race" } }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          runNumber += 1;
          if (runNumber === 1) {
            yield event("run-old", "old started");
            const stale = await input.next();
            expect(stale.value?.kind.case).toBe("steer");
            throw staleControl();
          }
          newerEvents += 1;
          yield event("run-new", "new started");
          await finishNewer.promise;
          newerEvents += 1;
          yield event("run-new", "new still running");
          yield terminal("run-new");
        },
      });
    });
    const raceClient = connect({ transport: raceTransport });
    const oldSession = await raceClient.sessions.create({});
    const newerSession = await raceClient.sessions.get("session-race");
    const oldRun = await oldSession.run("old");
    const newerRun = await newerSession.run("new");

    await oldRun.steer("too late");
    const stale = await oldRun.result().catch((error) => error);
    expect(stale).toBeInstanceOf(ServerError);
    expect(stale).toMatchObject({ code: "stale_run_control" });
    expect(newerEvents).toBe(1);

    finishNewer.resolve();
    await expect(newerRun.result()).resolves.toMatchObject({ runId: "run-new" });
    expect(newerEvents).toBe(2);
    await raceClient.close();
  });
});
