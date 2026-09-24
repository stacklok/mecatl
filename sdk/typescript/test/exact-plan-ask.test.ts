import { create } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import { decodeEvent } from "../src/events.js";
import { EventSchema, HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  createHttpTransport,
  type PlanApprovalVerdict,
  ProtocolError,
  type RequestOptions,
  type RunControls,
  ServerFeature,
  UnsupportedFeatureError,
} from "../src/index.js";

const sessionId = "session-plan-control";
const runId = "run-plan-control";
const askId = "ask-plan-control";

describe("exact plan ask SDK control", () => {
  it("exposes the approved public signature and maps each plan verdict over gRPC", async () => {
    expectTypeOf<RunControls["resolvePlanAsk"]>().toEqualTypeOf<
      (
        askId: string,
        verdict: PlanApprovalVerdict,
        requestOptions?: RequestOptions,
      ) => Promise<void>
    >();
    const requests: Array<{ askId: string; expectedRunId: string; verdict: number }> = [];
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: [ServerFeature.ExactPlanAskControl],
          }),
          getSession: () => ({ session: { sessionId } }),
          resolvePlanAsk: (request) => {
            requests.push(request);
            return { runId: request.expectedRunId, askId: request.askId };
          },
        });
      }),
    });
    const controls = (await client.sessions.get(sessionId)).controls(runId);
    await controls.resolvePlanAsk(askId, "approve");
    await controls.resolvePlanAsk(askId, "accept_edits");
    await controls.resolvePlanAsk(askId, "iterate");
    expect(requests).toMatchObject([
      { askId, expectedRunId: runId, verdict: 2 },
      { askId, expectedRunId: runId, verdict: 3 },
      { askId, expectedRunId: runId, verdict: 1 },
    ]);
    await client.close();
  });

  it("uses the exact HTTP route and snake-case body, checking both echoes", async () => {
    const requests: Array<{ path: string; body: unknown }> = [];
    let response: unknown = { run_id: runId, ask_id: askId };
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
          requests.push({ path, body: JSON.parse(String(init?.body)) });
          return Response.json(response);
        },
      }),
      transportKind: "http",
    });
    const controls = (await client.sessions.get(sessionId)).controls(runId);
    await controls.resolvePlanAsk(askId, "approve");
    expect(requests).toEqual([
      {
        path: `/v1/sessions/${sessionId}/controls/resolve-plan-ask`,
        body: { expected_run_id: runId, ask_id: askId, verdict: "allow_once" },
      },
    ]);
    response = { run_id: "newer-run", ask_id: askId };
    await expect(controls.resolvePlanAsk(askId, "iterate")).rejects.toBeInstanceOf(ProtocolError);
    expect(requests).toHaveLength(2);
    response = { run_id: runId, ask_id: "newer-ask" };
    await expect(controls.resolvePlanAsk(askId, "iterate")).rejects.toBeInstanceOf(ProtocolError);
    expect(requests).toHaveLength(3);
    await client.close();
  });

  it("rejects each malformed gRPC echo without retrying", async () => {
    let response: unknown = { runId: "newer-run", askId };
    let requests = 0;
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: [ServerFeature.ExactPlanAskControl],
          }),
          getSession: () => ({ session: { sessionId } }),
          resolvePlanAsk: () => {
            requests += 1;
            return response as never;
          },
        });
      }),
    });
    const controls = (await client.sessions.get(sessionId)).controls(runId);
    await expect(controls.resolvePlanAsk(askId, "approve")).rejects.toBeInstanceOf(ProtocolError);
    expect(requests).toBe(1);
    response = { runId, askId: "newer-ask" };
    await expect(controls.resolvePlanAsk(askId, "approve")).rejects.toBeInstanceOf(ProtocolError);
    expect(requests).toBe(2);
    await client.close();
  });

  it("refuses unsupported servers before sending a verdict", async () => {
    let submitted = false;
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: ["other_feature"],
          }),
          getSession: () => ({ session: { sessionId } }),
          resolvePlanAsk: () => {
            submitted = true;
            return { runId, askId };
          },
        });
      }),
    });
    const controls = (await client.sessions.get(sessionId)).controls(runId);
    await expect(controls.resolvePlanAsk(askId, "approve")).rejects.toBeInstanceOf(
      UnsupportedFeatureError,
    );
    expect(submitted).toBe(false);
    await client.close();
  });
});

describe("permission ask call correlation", () => {
  it("projects nonempty call IDs and omits absent or empty older-server values", () => {
    const event = (callId?: string) =>
      create(EventSchema, {
        type: "permission.ask",
        runId,
        ask: {
          askId,
          tool: "Read",
          args: "{}",
          reason: "review",
          ...(callId === undefined ? {} : { callId }),
        },
      });
    const withCall = decodeEvent(event("call-1"), "grpc");
    const empty = decodeEvent(event(""), "grpc");
    const absent = decodeEvent(event(), "grpc");
    if (
      withCall.kind !== "permission.ask" ||
      empty.kind !== "permission.ask" ||
      absent.kind !== "permission.ask"
    ) {
      throw new Error("expected permission.ask events");
    }
    expect(withCall.payload.callId).toBe("call-1");
    expect(empty.payload).not.toHaveProperty("callId");
    expect(absent.payload).not.toHaveProperty("callId");
  });
});
