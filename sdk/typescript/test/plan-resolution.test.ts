import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService, PermissionMode } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  type Event,
  InvalidStateError,
  PlanContinuationStartError,
  ProtocolError,
} from "../src/index.js";

function event(runId: string, type = "message.delta", text = "chunk") {
  return { runId, text, type };
}

function terminal(runId: string, stop: string, text: string) {
  return {
    result: { stop, text, usage: { inputTokens: 3n, outputTokens: 2n } },
    runId,
    type: "result",
  };
}

async function collect(source: AsyncIterable<Event>): Promise<Event[]> {
  const events: Event[] = [];
  for await (const value of source) events.push(value);
  return events;
}

describe("streaming plan resolution", () => {
  it("resolvePlan streams the resumed run then its continuation", async () => {
    const targetModes: PermissionMode[] = [];
    let call = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        approvePlan: async function* (request) {
          targetModes.push(request.targetMode);
          call += 1;
          yield event(`resumed-${call}`, "message.delta", "plan accepted");
          yield terminal(`resumed-${call}`, "plan_approved", "approved");
          yield event(`continuation-${call}`, "message.delta", "executing");
          yield terminal(`continuation-${call}`, "end_turn", "implemented");
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.get("session-plan");

    const streamed = await collect(session.resolvePlan("accept_edits"));
    expect(streamed.map((value) => `${value.runId}:${value.kind}`)).toEqual([
      "resumed-1:message.delta",
      "resumed-1:result",
      "continuation-1:message.delta",
      "continuation-1:result",
    ]);

    const result = await session.resolvePlan().result();
    expect(result.resumed).toMatchObject({
      runId: "resumed-2",
      sessionId: "session-plan",
      stopReason: "plan_approved",
    });
    expect(result.continuation).toMatchObject({
      runId: "continuation-2",
      sessionId: "session-plan",
      stopReason: "end_turn",
      text: "implemented",
    });
    expect(result.resumed.runId).not.toBe(result.continuation?.runId);
    expect(targetModes).toEqual([PermissionMode.ACCEPT_EDITS, PermissionMode.DEFAULT]);
    await client.close();
  });

  it("a denied plan resolution has no continuation run", async () => {
    let stream = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        approvePlan: async function* () {
          stream += 1;
          if (stream === 1) {
            yield event("resumed-denied", "message.delta", "iterate");
            yield terminal("resumed-denied", "plan_iterate", "revise the plan");
            return;
          }
          if (stream === 2) {
            yield terminal("resumed-third", "plan_approved", "approved");
            yield terminal("continuation-third", "end_turn", "done");
            yield terminal("third-run", "end_turn", "impossible");
            return;
          }
          yield event("resumed-missing");
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.get("session-denied");

    const denied = await session.resolvePlan("iterate").result();
    expect(denied.resumed).toMatchObject({
      runId: "resumed-denied",
      stopReason: "plan_iterate",
    });
    expect(denied.continuation).toBeUndefined();

    await expect(session.resolvePlan().result()).rejects.toBeInstanceOf(ProtocolError);
    await expect(session.resolvePlan().result()).rejects.toThrow(
      "without the resumed run terminal",
    );
    await client.close();
  });

  it("PlanResolution has one consumption mode", async () => {
    let controls = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        approvePlan: async function* () {
          controls += 1;
          yield terminal("resumed-once", "plan_iterate", "iterate");
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.get("session-once");
    const resolution = session.resolvePlan("iterate");

    const result = resolution.result();
    expect(() => resolution[Symbol.asyncIterator]()).toThrow(InvalidStateError);
    await expect(resolution.result()).rejects.toBeInstanceOf(InvalidStateError);
    await expect(result).resolves.toMatchObject({
      resumed: { runId: "resumed-once", stopReason: "plan_iterate" },
    });
    expect(controls).toBe(1);
    await client.close();
  });

  it("continuation-start failure preserves the server error", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        approvePlan: async function* () {
          yield terminal("resumed-failure", "plan_approved", "approved");
          yield {
            result: {
              error: "continuation run failed to start: placement unavailable",
              stop: "error",
            },
            runId: "",
            type: "result",
          };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.get("session-failure");

    const failure = await session
      .resolvePlan()
      .result()
      .catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(PlanContinuationStartError);
    expect(failure).toMatchObject({
      code: "plan_continuation_start",
      message: "continuation run failed to start: placement unavailable",
      transport: "grpc",
    });
    expect(failure).not.toBeInstanceOf(ProtocolError);
    await client.close();
  });
});
