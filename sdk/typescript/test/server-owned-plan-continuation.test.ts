import { create, fromJson } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import { decodeEvent } from "../src/events.js";
import {
  EventSchema,
  HarnessService,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  createHttpTransport,
  createRawClient,
  type EventOf,
  InvalidStateError,
  type PlanContinuationFailureEventPayload,
  ProtocolError,
  type RunOptions,
  ServerFeature,
  SessionBusyError,
  UnsupportedFeatureError,
} from "../src/index.js";
import { decodeWatchEnvelope } from "../src/watch.js";
import { sseResponse } from "./scripted-state.js";

const sessionId = "session-owned-plan";

function terminal(runId: string) {
  return { event: { result: { stop: "end_turn", text: "done" }, runId, type: "result" } };
}

function grpcSession(features: string[]) {
  const prompts: boolean[] = [];
  let sequence = 0;
  const client = connect({
    transport: createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features }),
        getSession: () => ({ session: { sessionId } }),
        converse: async function* (requests) {
          const first = await requests[Symbol.asyncIterator]().next();
          if (first.value?.kind.case === "prompt") {
            prompts.push(first.value.kind.value.serverOwnedPlanContinuation);
          }
          sequence += 1;
          yield terminal(`run-${sequence}`);
        },
      });
    }),
  });
  return { client, prompts };
}

describe("server-owned plan continuation run admission", () => {
  it("refuses an unadvertised opt-in before starting any stream", async () => {
    const { client, prompts } = grpcSession([ServerFeature.ServerInfo]);
    const session = await client.sessions.get(sessionId);

    await expect(
      session.run("review", { serverOwnedPlanContinuation: true }),
    ).rejects.toBeInstanceOf(UnsupportedFeatureError);
    expect(prompts).toEqual([]);
    await (await session.run("ordinary")).result();
    expect(prompts).toEqual([false]);
    await client.close();
  });

  it("refuses a client-owned plan responder and rejects retry opt-in before a stream", async () => {
    const { client, prompts } = grpcSession([ServerFeature.ExactPlanAskControl]);
    const session = await client.sessions.get(sessionId);

    await expect(
      session.run("review", {
        onPlanApproval: () => "approve",
        serverOwnedPlanContinuation: true,
      }),
    ).rejects.toBeInstanceOf(InvalidStateError);
    await expect(session.retry({ serverOwnedPlanContinuation: true })).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    expect(prompts).toEqual([]);
    await client.close();
  });

  it("directs an opted-in plan ask to the exact control", async () => {
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: [ServerFeature.ExactPlanAskControl],
          }),
          getSession: () => ({ session: { sessionId } }),
          converse: async function* (requests) {
            await requests[Symbol.asyncIterator]().next();
            yield {
              event: {
                ask: { askId: "plan-ask", tool: "PresentPlan" },
                runId: "run-plan",
                type: "permission.ask",
              },
            };
            yield terminal("run-plan");
          },
        });
      }),
    });
    const session = await client.sessions.get(sessionId);
    const run = await session.run("review", { serverOwnedPlanContinuation: true });

    await expect(run.resolveAsk("plan-ask", "deny")).rejects.toMatchObject({
      message: expect.stringContaining("resolvePlanAsk"),
    });
    await run.result();
    await client.close();
  });

  it("reserves the session during asynchronous feature preflight", async () => {
    const { client, prompts } = grpcSession([ServerFeature.ExactPlanAskControl]);
    const session = await client.sessions.get(sessionId);

    const first = session.run("first", { serverOwnedPlanContinuation: true });
    const second = session.run("second", { serverOwnedPlanContinuation: true });
    await expect(second).rejects.toBeInstanceOf(SessionBusyError);
    await (await first).result();
    expect(prompts).toEqual([true]);
    await client.close();
  });

  it("projects the gRPC flag and leaves default and false runs client-owned", async () => {
    expectTypeOf<RunOptions["serverOwnedPlanContinuation"]>().toEqualTypeOf<boolean | undefined>();
    const { client, prompts } = grpcSession([ServerFeature.ExactPlanAskControl]);
    const session = await client.sessions.get(sessionId);

    await (await session.run("owned", { serverOwnedPlanContinuation: true })).result();
    await (await session.run("default")).result();
    await (await session.run("false", { serverOwnedPlanContinuation: false })).result();
    expect(prompts).toEqual([true, false, false]);
    await client.close();
  });

  it("projects only a true opt-in to the HTTP prompt body", async () => {
    const bodies: unknown[] = [];
    const client = connect({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: async (input, init) => {
          const path = new URL(String(input)).pathname;
          if (path === "/v1/compatibility") {
            return Response.json({
              api_major: 1,
              capabilities: {},
              features: [ServerFeature.ExactPlanAskControl],
            });
          }
          if (path === `/v1/sessions/${sessionId}`) {
            return Response.json({ session_id: sessionId, state: "idle" });
          }
          if (path === `/v1/sessions/${sessionId}/prompt`) {
            bodies.push(JSON.parse(String(init?.body)));
            return sseResponse([
              {
                result: { stop: "end_turn", text: "done" },
                run_id: `run-${bodies.length}`,
                type: "result",
              },
            ]);
          }
          return Response.json({}, { status: 404 });
        },
      }),
      transportKind: "http",
    });
    const session = await client.sessions.get(sessionId);

    await (await session.run("owned", { serverOwnedPlanContinuation: true })).result();
    await (await session.run("default")).result();
    await (await session.run("false", { serverOwnedPlanContinuation: false })).result();
    expect(bodies).toEqual([
      { parts: [], server_owned_plan_continuation: true, text: "owned" },
      { parts: [], text: "default" },
      { parts: [], text: "false" },
    ]);
    await client.close();
  });
});

describe("known plan continuation failures", () => {
  it("decodes the safe payload as one typed event on both transports", () => {
    const grpc = create(EventSchema, {
      planContinuationFailure: { planRunId: "plan-run", askId: "plan-ask" },
      type: "plan.continuation_failed",
    });
    const http = fromJson(EventSchema, {
      plan_continuation_failure: { plan_run_id: "plan-run", ask_id: "plan-ask" },
      type: "plan.continuation_failed",
    });
    for (const [wire, transport] of [
      [grpc, "grpc"],
      [http, "http"],
    ] as const) {
      const event = decodeEvent(wire, transport);
      expect(event.kind).toBe("plan.continuation_failed");
      if (event.kind !== "plan.continuation_failed") throw new Error("failure kind was not typed");
      expectTypeOf(event).toEqualTypeOf<EventOf<"plan.continuation_failed">>();
      expectTypeOf(event.payload).toEqualTypeOf<PlanContinuationFailureEventPayload>();
      expect(event.runId).toBe("");
      expect(event.payload).toEqual({ planRunId: "plan-run", askId: "plan-ask" });
    }
    expect(() =>
      decodeEvent(create(EventSchema, { type: "plan.continuation_failed" }), "grpc"),
    ).toThrow(ProtocolError);
  });

  it("projects a session-scoped failure through gRPC and HTTP activity streams", async () => {
    const grpcFrame = create(WatchSessionEventsResponseSchema, {
      cursor: "cursor-failure",
      event: {
        planContinuationFailure: { planRunId: "plan-run", askId: "plan-ask" },
        type: "plan.continuation_failed",
      },
      phase: "replay",
    });
    const grpc = createRawClient({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: [ServerFeature.WatchSessionEvents],
          }),
          watchSessionEvents: async function* () {
            yield grpcFrame;
          },
        });
      }),
    });
    const http = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: async (input) => {
          const path = new URL(String(input)).pathname;
          if (path === "/v1/compatibility") {
            return Response.json({
              api_major: 1,
              capabilities: {},
              features: [ServerFeature.WatchSessionEvents],
            });
          }
          if (path === `/v1/sessions/${sessionId}/watch`) {
            return sseResponse([
              {
                cursor: "cursor-failure",
                event: {
                  plan_continuation_failure: { plan_run_id: "plan-run", ask_id: "plan-ask" },
                  type: "plan.continuation_failed",
                },
                phase: "replay",
              },
            ]);
          }
          return Response.json({}, { status: 404 });
        },
      }),
    });
    const collect = async (raw: typeof grpc, transport: "grpc" | "http") => {
      const events = [];
      const input = (async function* () {
        yield { cursor: "", runId: "", sessionId };
      })();
      for await (const frame of raw.stream(HarnessService.method.watchSessionEvents, input)) {
        events.push(decodeWatchEnvelope(frame, transport));
      }
      return events;
    };
    const [grpcEvents, httpEvents] = await Promise.all([
      collect(grpc, "grpc"),
      collect(http, "http"),
    ]);
    expect(grpcEvents).toEqual(httpEvents);
    expect(grpcEvents).toMatchObject([
      {
        event: {
          kind: "plan.continuation_failed",
          payload: { planRunId: "plan-run", askId: "plan-ask" },
          runId: "",
        },
      },
    ]);
  });
});
