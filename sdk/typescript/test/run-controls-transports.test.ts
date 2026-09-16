import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
} from "@bufbuild/protobuf";
import type { ContextValues, StreamResponse, Transport, UnaryResponse } from "@connectrpc/connect";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { ApprovalVerdict, HarnessService, SteerOutcome } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  InvalidStateError,
  imagePart,
  PromptValidationError,
  ProtocolError,
  SESSION_ID_HEADER_NAME,
  ServerError,
} from "../src/index.js";

const feature = "prompt_free_controls";
const sessionId = "session-transport-controls";
const runId = "run-transport-controls";

class RecordingTransport implements Transport {
  readonly calls: Array<{
    readonly header: Headers;
    readonly input: unknown;
    readonly method: string;
    readonly signal: AbortSignal | undefined;
    readonly timeoutMs: number | undefined;
  }> = [];
  readonly #delegate: Transport;

  constructor() {
    this.#delegate = createRouterTransport((router) => {
      router.service(HarnessService, {
        cancelRun: (_request, context) => {
          context.responseHeader.set("x-response", "cancel-header");
          context.responseTrailer.set("x-response", "cancel-trailer");
          return { runId };
        },
        cancelRunSteer: () => ({
          messageId: "message-1",
          outcome: SteerOutcome.RETRACTED,
          runId,
        }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [feature] }),
        getSession: () => ({
          session: { sessionCapabilities: { audio: true, image: true }, sessionId },
        }),
        resolveRunAsk: (request) => ({ askId: request.askId, runId }),
        steerRun: (request) => ({
          messageId: request.messageId,
          outcome: SteerOutcome.ACCEPTED,
          runId,
        }),
      });
    });
  }

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: MessageInitShape<I>,
    contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    this.calls.push({ header: new Headers(header), input, method: method.name, signal, timeoutMs });
    return this.#delegate.unary(method, signal, timeoutMs, header, input, contextValues);
  }

  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: AsyncIterable<MessageInitShape<I>>,
    contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    return this.#delegate.stream(method, signal, timeoutMs, header, input, contextValues);
  }
}

describe("run control transports", () => {
  it("run controls preserve request options and gate every operation", async () => {
    const transport = new RecordingTransport();
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);
    const controls = session.controls(runId);
    const controller = new AbortController();
    const headers: string[] = [];
    const trailers: string[] = [];
    const options = {
      headers: { "x-caller": "kept" },
      onHeader: (value: Headers) => headers.push(value.get("x-response") ?? ""),
      onTrailer: (value: Headers) => trailers.push(value.get("x-response") ?? ""),
      signal: controller.signal,
      timeoutMs: 4_321,
    };

    await controls.resolveAsk("ask-1", "allow_always", options);
    await controls.cancel(options);
    await controls.steer("turn", { messageId: "message-1" }, options);
    await controls.cancelSteer({ messageId: "message-1" }, options);

    const methods = transport.calls.filter((call) =>
      ["ResolveRunAsk", "CancelRun", "SteerRun", "CancelRunSteer"].includes(call.method),
    );
    expect(methods.map((call) => call.method)).toEqual([
      "ResolveRunAsk",
      "CancelRun",
      "SteerRun",
      "CancelRunSteer",
    ]);
    expect(methods.every((call) => call.header.get("x-caller") === "kept")).toBe(true);
    expect(methods.every((call) => call.header.get(SESSION_ID_HEADER_NAME) === sessionId)).toBe(
      true,
    );
    expect(methods.every((call) => call.timeoutMs === 4_321)).toBe(true);
    expect(methods.every((call) => call.signal !== controller.signal)).toBe(true);
    expect(methods.map((call) => call.input)).toEqual([
      {
        askId: "ask-1",
        expectedRunId: runId,
        sessionId,
        verdict: ApprovalVerdict.ALLOW_ALWAYS,
      },
      { expectedRunId: runId, sessionId },
      expect.objectContaining({
        expectedRunId: runId,
        messageId: "message-1",
        sessionId,
      }),
      { expectedRunId: runId, messageId: "message-1", sessionId },
    ]);
    expect(headers).toContain("cancel-header");
    expect(trailers).toContain("cancel-trailer");
    await client.close();

    const httpCalls: Array<{ headers: Headers; path: string; signal: AbortSignal | null }> = [];
    const httpHeaders: string[] = [];
    const httpTrailers: string[] = [];
    const httpClient = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input, init) => {
        const path = new URL(String(input)).pathname;
        if (path === "/v1/compatibility") {
          return Response.json({ api_major: 1, capabilities: {}, features: [feature] });
        }
        if (path === `/v1/sessions/${sessionId}`) {
          return Response.json({ session_id: sessionId });
        }
        httpCalls.push({
          headers: new Headers(init?.headers),
          path,
          signal: init?.signal ?? null,
        });
        return Response.json({ run_id: runId }, { headers: { "x-response": "http-header" } });
      },
    });
    const httpController = new AbortController();
    await (await httpClient.sessions.get(sessionId)).controls(runId).cancel({
      headers: { "x-caller": "http-kept" },
      onHeader: (value) => httpHeaders.push(value.get("x-response") ?? ""),
      onTrailer: (value) => httpTrailers.push(value.get("x-response") ?? ""),
      signal: httpController.signal,
      timeoutMs: 4_321,
    });
    expect(httpCalls).toHaveLength(1);
    expect(httpCalls[0]).toMatchObject({
      path: `/v1/sessions/${sessionId}/controls/cancel`,
    });
    expect(httpCalls[0]?.headers.get("x-caller")).toBe("http-kept");
    expect(httpCalls[0]?.headers.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
    expect(httpCalls[0]?.signal).toBeInstanceOf(AbortSignal);
    expect(httpHeaders).toEqual(["http-header"]);
    expect(httpTrailers).toEqual([""]);
    await httpClient.close();

    for (const invoke of [
      (missing: ReturnType<typeof connect>) =>
        missing.sessions.get(sessionId).then((s) => s.controls(runId).resolveAsk("ask", "deny")),
      (missing: ReturnType<typeof connect>) =>
        missing.sessions.get(sessionId).then((s) => s.controls(runId).cancel()),
      (missing: ReturnType<typeof connect>) =>
        missing.sessions.get(sessionId).then((s) => s.controls(runId).steer("turn")),
      (missing: ReturnType<typeof connect>) =>
        missing.sessions.get(sessionId).then((s) => s.controls(runId).cancelSteer()),
    ]) {
      let controlCalls = 0;
      const missingFeature = connect({
        transport: createRouterTransport((router) => {
          router.service(HarnessService, {
            cancelRun: () => {
              controlCalls += 1;
              return { runId };
            },
            cancelRunSteer: () => {
              controlCalls += 1;
              return { messageId: "", outcome: SteerOutcome.NONE_PENDING, runId };
            },
            getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [] }),
            getSession: () => ({ session: { sessionId } }),
            resolveRunAsk: () => {
              controlCalls += 1;
              return { askId: "ask", runId };
            },
            steerRun: () => {
              controlCalls += 1;
              return { messageId: "", outcome: SteerOutcome.ACCEPTED, runId };
            },
          });
        }),
      });
      await expect(invoke(missingFeature)).rejects.toMatchObject({
        feature,
        transport: "grpc",
      });
      expect(controlCalls).toBe(0);
      await missingFeature.close();
    }
  });

  it("run controls cover feature options errors and malformed matrices", async () => {
    const noHandlers = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [feature] }),
          getSession: () => ({ session: { sessionId } }),
        });
      }),
    });
    const unsupported = (await noHandlers.sessions.get(sessionId)).controls(runId);
    for (const call of [
      unsupported.resolveAsk("ask", "deny"),
      unsupported.cancel(),
      unsupported.steer("turn"),
      unsupported.cancelSteer(),
    ]) {
      await expect(call).rejects.toBeInstanceOf(ServerError);
    }
    await noHandlers.close();

    let controlCalls = 0;
    const mediaClient = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [feature] }),
          getSession: () => ({
            session: { sessionCapabilities: { audio: false, image: false }, sessionId },
          }),
          steerRun: () => {
            controlCalls += 1;
            return { messageId: "", outcome: SteerOutcome.ACCEPTED, runId };
          },
        });
      }),
    });
    const mediaControls = (await mediaClient.sessions.get(sessionId)).controls(runId);
    await expect(
      mediaControls.steer([imagePart({ bytes: new Uint8Array([1]), mimeType: "image/png" })]),
    ).rejects.toBeInstanceOf(PromptValidationError);
    expect(controlCalls).toBe(0);
    await mediaClient.close();

    const closedTransport = new RecordingTransport();
    const closedClient = connect({ transport: closedTransport });
    const closedControls = (await closedClient.sessions.get(sessionId)).controls(runId);
    await closedClient.close();
    await expect(closedControls.cancel()).rejects.toBeInstanceOf(InvalidStateError);
    expect(closedTransport.calls.filter((call) => call.method === "CancelRun")).toHaveLength(0);

    const cancelledTransport = new RecordingTransport();
    const cancelledClient = connect({ transport: cancelledTransport });
    const cancelledControls = (await cancelledClient.sessions.get(sessionId)).controls(runId);
    const abort = new AbortController();
    abort.abort();
    await expect(cancelledControls.cancel({ signal: abort.signal })).rejects.toBeDefined();

    const malformed = [
      {},
      { messageId: "", outcome: SteerOutcome.UNSPECIFIED, runId },
      { messageId: "", outcome: SteerOutcome.TOO_LATE, runId },
      { messageId: "wrong", outcome: SteerOutcome.ACCEPTED, runId },
      { messageId: "", outcome: SteerOutcome.ACCEPTED, runId: "wrong" },
    ];
    for (const response of malformed) {
      const client = connect({
        transport: createRouterTransport((router) => {
          router.service(HarnessService, {
            getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [feature] }),
            getSession: () => ({ session: { sessionId } }),
            steerRun: () => response as never,
          });
        }),
      });
      const controls = (await client.sessions.get(sessionId)).controls(runId);
      await expect(controls.steer("turn")).rejects.toBeInstanceOf(ProtocolError);
      await client.close();
    }
  });
});
