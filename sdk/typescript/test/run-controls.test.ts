import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import { HarnessService, SteerOutcome } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type AttachedRun,
  type Client,
  connect,
  createHttpTransport,
  imagePart,
  type PermissionVerdict,
  PromptValidationError,
  ProtocolError,
  type RequestOptions,
  type Run,
  type RunControls,
  type RunSteerAcknowledgement,
  type RunSteerCancellationAcknowledgement,
  type RunSteerOptions,
  type Session,
  textPart,
  UnsupportedFeatureError,
} from "../src/index.js";

const sessionId = "session-controls";
const runId = "run-controls";
const feature = "prompt_free_controls";

interface RecordedHttpRequest {
  readonly body: Record<string, unknown>;
  readonly path: string;
}

interface HttpHarnessOptions {
  readonly capabilities?: { readonly audio?: boolean; readonly image?: boolean };
  readonly features?: readonly string[];
  readonly response?: (path: string, body: Record<string, unknown>) => unknown | Response;
}

function httpHarness(options: HttpHarnessOptions = {}): {
  readonly client: Client;
  readonly requests: RecordedHttpRequest[];
} {
  const requests: RecordedHttpRequest[] = [];
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const path = new URL(String(input)).pathname;
    const body =
      typeof init?.body === "string"
        ? (JSON.parse(init.body) as Record<string, unknown>)
        : ({} as Record<string, unknown>);
    requests.push({ body, path });
    if (path === "/v1/compatibility") {
      return Response.json({
        api_major: 1,
        capabilities: {},
        features: options.features ?? [feature, "watch_session_events"],
      });
    }
    if (path === `/v1/sessions/${sessionId}`) {
      return Response.json({
        session_capabilities: options.capabilities ?? { audio: true, image: true },
        session_id: sessionId,
        state: "idle",
      });
    }
    if (path === `/v1/sessions/${sessionId}/watch`) {
      return new Response('data: {"cursor":"live","phase":"live"}\n\n', {
        headers: { "content-type": "text/event-stream" },
      });
    }
    const supplied = options.response?.(path, body);
    return supplied instanceof Response ? supplied : Response.json(supplied ?? {});
  };
  return {
    client: connect({
      transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch }),
      transportKind: "http",
    }),
    requests,
  };
}

interface GrpcHarnessOptions {
  readonly cancel?: () => unknown;
  readonly cancelSteer?: () => unknown;
  readonly features?: readonly string[];
  readonly resolve?: () => unknown;
  readonly steer?: () => unknown;
}

function grpcHarness(options: GrpcHarnessOptions = {}): {
  readonly calls: string[];
  readonly client: Client;
} {
  const calls: string[] = [];
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      cancelRun: () => {
        calls.push("cancel");
        return (options.cancel?.() ?? { runId }) as never;
      },
      cancelRunSteer: () => {
        calls.push("cancelSteer");
        return (options.cancelSteer?.() ?? {
          messageId: "",
          outcome: SteerOutcome.NONE_PENDING,
          runId,
        }) as never;
      },
      getCompatibilityInfo: () => {
        calls.push("compatibility");
        return { apiMajor: 1, capabilities: {}, features: [...(options.features ?? [feature])] };
      },
      getSession: () => {
        calls.push("getSession");
        return {
          session: {
            sessionCapabilities: { audio: true, image: true },
            sessionId,
          },
        };
      },
      resolveRunAsk: () => {
        calls.push("resolveAsk");
        return (options.resolve?.() ?? { askId: "ask-1", runId }) as never;
      },
      steerRun: () => {
        calls.push("steer");
        return (options.steer?.() ?? {
          messageId: "",
          outcome: SteerOutcome.ACCEPTED,
          runId,
        }) as never;
      },
      watchSessionEvents: async function* () {
        calls.push("watch");
        yield { cursor: "live", phase: "live" };
      },
    });
  });
  return { calls, client: connect({ transport }) };
}

async function grpcSession(options: GrpcHarnessOptions = {}): Promise<{
  readonly calls: string[];
  readonly client: Client;
  readonly session: Session;
}> {
  const harness = grpcHarness(options);
  const session = await harness.client.sessions.get(sessionId);
  return { ...harness, session };
}

describe("run controls", () => {
  it("session controls exposes the run controls resource without attaching", async () => {
    const { calls, client, session } = await grpcSession();
    const before = [...calls];

    const controls = session.controls(runId);

    expect(controls).toMatchObject({ runId, sessionId });
    expect(calls).toEqual(before);
    expect(calls).not.toContain("watch");
    expectTypeOf(controls).toEqualTypeOf<RunControls>();
    await client.close();
  });

  it("run controls signatures reuse approved inputs and options", () => {
    expectTypeOf<RunSteerOptions>().toEqualTypeOf<{ messageId?: string }>();
    expectTypeOf<RunControls["resolveAsk"]>().toEqualTypeOf<
      (askId: string, verdict: PermissionVerdict, requestOptions?: RequestOptions) => Promise<void>
    >();
    expectTypeOf<RunControls["cancel"]>().toEqualTypeOf<
      (requestOptions?: RequestOptions) => Promise<void>
    >();
    expectTypeOf<RunControls["steer"]>().toEqualTypeOf<
      (
        prompt: Parameters<Session["run"]>[0],
        options?: RunSteerOptions,
        requestOptions?: RequestOptions,
      ) => Promise<RunSteerAcknowledgement>
    >();
    expectTypeOf<RunControls["cancelSteer"]>().toEqualTypeOf<
      (
        options?: RunSteerOptions,
        requestOptions?: RequestOptions,
      ) => Promise<RunSteerCancellationAcknowledgement>
    >();
  });

  it("legacy run and attached controls keep their compatibility contract", async () => {
    expectTypeOf<Run["approve"]>().toEqualTypeOf<
      (askId: string, allow: boolean) => Promise<void>
    >();
    expectTypeOf<Run["resolveAsk"]>().toEqualTypeOf<
      (askId: string, verdict: PermissionVerdict) => Promise<void>
    >();
    expectTypeOf<AttachedRun["approve"]>().toEqualTypeOf<
      (askId: string, allow: boolean) => Promise<never>
    >();
    expectTypeOf<AttachedRun["steer"]>().toEqualTypeOf<(text: string) => Promise<never>>();

    const harness = grpcHarness({ features: [feature, "watch_session_events"] });
    const session = await harness.client.sessions.get(sessionId);
    const attached = await session.attach(runId);
    for (const operation of [
      () => attached.approve("ask-1", true),
      () => attached.resolveAsk("ask-1", "allow_once"),
      () => attached.steer("turn left"),
    ]) {
      const error = await operation().catch((cause: unknown) => cause);
      expect(error).toBeInstanceOf(UnsupportedFeatureError);
      expect(error).toMatchObject({ feature: "attached_run_controls" });
      expect((error as Error).message).toContain("session.controls(attached.runId)");
    }
    expect(harness.calls).not.toContain("resolveAsk");
    expect(harness.calls).not.toContain("steer");
    await attached.close();
    await harness.client.close();
  });

  it("cancel validates exact run acknowledgement before resolving", async () => {
    const valid = await grpcSession();
    await expect(valid.session.controls(runId).cancel()).resolves.toBeUndefined();
    expect(valid.calls.filter((call) => call === "cancel")).toHaveLength(1);
    await valid.client.close();

    for (const response of [{}, { runId: "newer-run" }, { runId: 7 }]) {
      const malformed = await grpcSession({ cancel: () => response });
      await expect(malformed.session.controls(runId).cancel()).rejects.toBeInstanceOf(
        ProtocolError,
      );
      await malformed.client.close();
    }
  });

  it("resolve and cancel reject malformed acknowledgements", async () => {
    const cases: ReadonlyArray<{
      readonly operation: "cancel" | "resolve";
      readonly response: unknown;
    }> = [
      { operation: "resolve", response: { ask_id: "ask-1" } },
      { operation: "resolve", response: { ask_id: "wrong", run_id: runId } },
      { operation: "resolve", response: { ask_id: "ask-1", run_id: 1 } },
      { operation: "cancel", response: {} },
      { operation: "cancel", response: { run_id: "wrong" } },
      { operation: "cancel", response: { run_id: false } },
    ];
    for (const testCase of cases) {
      const harness = httpHarness({ response: () => testCase.response });
      const session = await harness.client.sessions.get(sessionId);
      const controls = session.controls(runId);
      const call =
        testCase.operation === "resolve"
          ? controls.resolveAsk("ask-1", "allow_once")
          : controls.cancel();
      await expect(call).rejects.toBeInstanceOf(ProtocolError);
      await harness.client.close();
    }

    const compatible = httpHarness({
      response: (path) =>
        path.endsWith("resolve-ask")
          ? { ask_id: "ask-1", future: true, run_id: runId }
          : { future: true, run_id: runId },
    });
    const controls = (await compatible.client.sessions.get(sessionId)).controls(runId);
    await expect(controls.resolveAsk("ask-1", "allow_once")).resolves.toBeUndefined();
    await expect(controls.cancel()).resolves.toBeUndefined();
    await compatible.client.close();
  });

  it("steer preserves flattened text and ordered media across transports", async () => {
    const harness = httpHarness({
      response: (_path, body) => ({
        message_id: body.message_id ?? "",
        outcome: "accepted",
        run_id: runId,
      }),
    });
    const controls = (await harness.client.sessions.get(sessionId)).controls(runId);
    const prompt = [
      textPart("first"),
      imagePart({ bytes: new Uint8Array([1, 2]), mimeType: "image/png" }),
      textPart("second"),
      { kind: "audio" as const, mimeType: "audio/wav", url: "https://example.test/a.wav" },
    ];
    await controls.steer(prompt, { messageId: "message-1" });
    expect(harness.requests.find((request) => request.path.endsWith("/controls/steer"))).toEqual({
      body: {
        expected_run_id: runId,
        message_id: "message-1",
        parts: [
          { data: "AQI=", kind: "image", mime_type: "image/png" },
          { kind: "audio", mime_type: "audio/wav", url: "https://example.test/a.wav" },
        ],
        text: "first\nsecond",
      },
      path: `/v1/sessions/${sessionId}/controls/steer`,
    });
    await harness.client.close();

    const empty = await grpcSession();
    const before = [...empty.calls];
    await expect(empty.session.controls(runId).steer("")).rejects.toBeInstanceOf(
      PromptValidationError,
    );
    expect(empty.calls).toEqual(before);
    await empty.client.close();
  });

  it("steer preserves optional message correlation and narrows outcomes", async () => {
    for (const [outcome, expected] of [
      [SteerOutcome.ACCEPTED, "accepted"],
      [SteerOutcome.APPENDED, "appended"],
    ] as const) {
      const harness = await grpcSession({
        steer: () => ({ messageId: "message-1", outcome, runId }),
      });
      await expect(
        harness.session.controls(runId).steer("turn left", { messageId: "message-1" }),
      ).resolves.toEqual({ messageId: "message-1", outcome: expected, runId });
      await harness.client.close();
    }

    const omitted = httpHarness({
      response: (_path, body) => ({
        message_id: body.message_id ?? "",
        outcome: "appended",
        run_id: runId,
      }),
    });
    const result = await (await omitted.client.sessions.get(sessionId))
      .controls(runId)
      .steer("turn right");
    expect(result).toEqual({ messageId: "", outcome: "appended", runId });
    expect(
      omitted.requests.find((request) => request.path.endsWith("/controls/steer"))?.body,
    ).toMatchObject({ message_id: "" });
    await omitted.client.close();
  });

  it("cancelSteer returns narrowed correlated acknowledgements", async () => {
    for (const [outcome, expected] of [
      [SteerOutcome.RETRACTED, "retracted"],
      [SteerOutcome.NONE_PENDING, "none_pending"],
    ] as const) {
      const harness = await grpcSession({
        cancelSteer: () => ({ messageId: "message-1", outcome, runId }),
      });
      await expect(
        harness.session.controls(runId).cancelSteer({ messageId: "message-1" }),
      ).resolves.toEqual({ messageId: "message-1", outcome: expected, runId });
      await harness.client.close();
    }
  });

  it("steer controls reject malformed acknowledgements", async () => {
    const cases: ReadonlyArray<{
      readonly operation: "cancel-steer" | "steer";
      readonly response: unknown;
    }> = [
      { operation: "steer", response: { message_id: "", outcome: "accepted" } },
      { operation: "steer", response: { message_id: "", outcome: "retracted", run_id: runId } },
      {
        operation: "steer",
        response: { message_id: "changed", outcome: "accepted", run_id: runId },
      },
      { operation: "steer", response: { message_id: "", outcome: 1, run_id: runId } },
      {
        operation: "steer",
        response: { message_id: "", outcome: "accepted", promoted: true, run_id: runId },
      },
      {
        operation: "cancel-steer",
        response: { message_id: "", outcome: "accepted", run_id: runId },
      },
      { operation: "cancel-steer", response: { message_id: "", outcome: "none_pending" } },
      { operation: "cancel-steer", response: { outcome: "none_pending", run_id: runId } },
      { operation: "cancel-steer", response: { message_id: "", outcome: "future", run_id: runId } },
    ];
    for (const testCase of cases) {
      const harness = httpHarness({ response: () => testCase.response });
      const controls = (await harness.client.sessions.get(sessionId)).controls(runId);
      const call = testCase.operation === "steer" ? controls.steer("turn") : controls.cancelSteer();
      await expect(call).rejects.toBeInstanceOf(ProtocolError);
      await harness.client.close();
    }

    const malformedJson = httpHarness({
      response: () =>
        new Response("{", { headers: { "content-type": "application/json" }, status: 200 }),
    });
    const controls = (await malformedJson.client.sessions.get(sessionId)).controls(runId);
    await expect(controls.steer("turn")).rejects.toBeInstanceOf(ProtocolError);
    await malformedJson.client.close();

    const native = await grpcSession({
      steer: () => ({ messageId: "", outcome: 99, runId }),
    });
    await expect(native.session.controls(runId).steer("turn")).rejects.toBeInstanceOf(
      ProtocolError,
    );
    await native.client.close();
  });
});
