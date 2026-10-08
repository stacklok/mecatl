import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { ScheduleService } from "../src/gen/mecatl/v1/schedule_pb.js";
import {
  AuthenticationError,
  CursorExpiredError,
  createHttpTransport,
  ProtocolError,
  ServerError,
  TransportError,
} from "../src/index.js";

const baseUrl = "http://mecatl.test";
const validWatchFrame = {
  cursor: "before-malformed",
  event: { seq: 1, text: "before", type: "message.delta" },
  phase: "replay",
};
const laterWatchFrame = {
  cursor: "after-malformed",
  event: { seq: 3, text: "must not be yielded", type: "message.delta" },
  phase: "live",
};

async function* one<T>(value: T): AsyncIterable<T> {
  yield value;
}

async function caught(operation: Promise<unknown>): Promise<unknown> {
  try {
    await operation;
  } catch (error) {
    return error;
  }
  throw new Error("expected operation to reject");
}

function data(value: unknown): string {
  return `data: ${JSON.stringify(value)}\n\n`;
}

function sse(body: string, requestId?: string): Response {
  return new Response(body, {
    headers: {
      "content-type": "text/event-stream",
      ...(requestId === undefined ? {} : { "x-request-id": requestId }),
    },
  });
}

async function unary(fetch: typeof globalThis.fetch): Promise<unknown> {
  return createHttpTransport({ baseUrl, fetch }).unary(
    ScheduleService.method.getSchedule,
    undefined,
    undefined,
    undefined,
    { name: "nightly" },
  );
}

async function watch(fetch: typeof globalThis.fetch) {
  return createHttpTransport({ baseUrl, fetch }).stream(
    HarnessService.method.watchSessionEvents,
    undefined,
    undefined,
    undefined,
    one({ sessionId: "session-1" }),
  );
}

function expectSafeProtocol(
  error: unknown,
  expected: {
    readonly canary: string;
    readonly message: string;
    readonly requestId?: string | undefined;
  },
): ProtocolError {
  expect(error).toBeInstanceOf(ProtocolError);
  const protocol = error as ProtocolError;
  expect(Object.hasOwn(protocol, "cause")).toBe(false);
  expect(protocol.cause).toBeUndefined();
  expect(protocol).toMatchObject({
    code: "protocol",
    message: expected.message,
    requestId: expected.requestId,
    status: 200,
    transport: "http",
  });
  expect(`${protocol.message} ${JSON.stringify(protocol.toJSON())}`).not.toContain(expected.canary);
  return protocol;
}

describe("HTTP malformed-success decoding", () => {
  it("malformed successful HTTP and SSE payloads omit decoder causes", async () => {
    const unaryJsonCanary = "UNARY_JSON_PRESENTATION_URL_CANARY";
    const unaryJsonError = await caught(
      unary(
        async () =>
          new Response(`not-json-${unaryJsonCanary}`, {
            headers: { "x-request-id": "unary-json-request" },
          }),
      ),
    );
    expectSafeProtocol(unaryJsonError, {
      canary: unaryJsonCanary,
      message: "The mecatl server returned invalid JSON",
      requestId: "unary-json-request",
    });

    const unaryWktCanary = "UNARY_WKT_PRESENTATION_URL_CANARY";
    const unaryWktError = await caught(
      unary(async () =>
        Response.json({
          schedule: { spec: { created_at: { seconds: unaryWktCanary } } },
        }),
      ),
    );
    expectSafeProtocol(unaryWktError, {
      canary: unaryWktCanary,
      message: "The mecatl server returned an invalid response",
    });

    const unaryProtoCanary = "UNARY_PROTO_PRESENTATION_URL_CANARY";
    const unaryProtoError = await caught(
      unary(async () =>
        Response.json({
          schedule: { spec: { max_fires: unaryProtoCanary } },
        }),
      ),
    );
    expectSafeProtocol(unaryProtoError, {
      canary: unaryProtoCanary,
      message: "The mecatl server returned an invalid response",
    });

    const sseJsonCanary = "SSE_JSON_PRESENTATION_URL_CANARY";
    const invalidJsonStream = await watch(async () =>
      sse(
        `${data(validWatchFrame)}data: {"canary":"${sseJsonCanary}"\n\n${data(laterWatchFrame)}`,
        "sse-json-request",
      ),
    );
    const invalidJsonIterator = invalidJsonStream.message[Symbol.asyncIterator]();
    await expect(invalidJsonIterator.next()).resolves.toMatchObject({
      done: false,
      value: { cursor: "before-malformed" },
    });
    expectSafeProtocol(await caught(invalidJsonIterator.next()), {
      canary: sseJsonCanary,
      message: "The mecatl SSE stream contained invalid JSON",
      requestId: "sse-json-request",
    });
    await expect(invalidJsonIterator.next()).resolves.toEqual({ done: true, value: undefined });

    const sseWktCanary = "SSE_WKT_PRESENTATION_URL_CANARY";
    const invalidWktEvent = {
      cursor: "malformed",
      event: {
        authorization: {
          authorization_id: "authorization-1",
          expires_at: { seconds: sseWktCanary },
          status: "pending",
        },
        run_id: "run-1",
        type: "authorization.required",
      },
      phase: "live",
    };
    const invalidWktStream = await watch(async () =>
      sse(`${data(validWatchFrame)}${data(invalidWktEvent)}${data(laterWatchFrame)}`),
    );
    const invalidWktIterator = invalidWktStream.message[Symbol.asyncIterator]();
    await expect(invalidWktIterator.next()).resolves.toMatchObject({
      done: false,
      value: { cursor: "before-malformed" },
    });
    expectSafeProtocol(await caught(invalidWktIterator.next()), {
      canary: sseWktCanary,
      message: "The mecatl SSE stream contained an invalid event",
    });
    await expect(invalidWktIterator.next()).resolves.toEqual({ done: true, value: undefined });

    const sseProtoCanary = "SSE_PROTO_PRESENTATION_URL_CANARY";
    const invalidProtoEvent = {
      cursor: "malformed",
      event: { seq: sseProtoCanary, text: "invalid seq", type: "message.delta" },
      phase: "live",
    };
    const invalidProtoStream = await watch(async () =>
      sse(`${data(validWatchFrame)}${data(invalidProtoEvent)}${data(laterWatchFrame)}`),
    );
    const invalidProtoIterator = invalidProtoStream.message[Symbol.asyncIterator]();
    await expect(invalidProtoIterator.next()).resolves.toMatchObject({
      done: false,
      value: { cursor: "before-malformed" },
    });
    expectSafeProtocol(await caught(invalidProtoIterator.next()), {
      canary: sseProtoCanary,
      message: "The mecatl SSE stream contained an invalid event",
    });
    await expect(invalidProtoIterator.next()).resolves.toEqual({ done: true, value: undefined });
  });

  it("server and authentication errors retain their types and causes", async () => {
    const problem = {
      code: "session_not_found",
      detail: "session missing",
      status: 404,
      type: "https://mecatl.stacklok.com/problems/session_not_found",
    };
    const serverError = await caught(
      unary(async () =>
        Response.json(problem, {
          headers: { "content-type": "application/problem+json", "x-request-id": "server-404" },
          status: 404,
        }),
      ),
    );
    expect(serverError).toBeInstanceOf(ServerError);
    expect(serverError).toMatchObject({
      cause: problem,
      code: "session_not_found",
      message: "session missing",
      requestId: "server-404",
      status: 404,
      transport: "http",
    });

    const eventProblem = {
      code: "cursor_expired",
      detail: "cursor expired",
      status: 412,
      type: "https://mecatl.stacklok.com/problems/cursor_expired",
    };
    const errorStream = await watch(async () =>
      sse(`event: error\n${data(eventProblem)}`, "sse-error-request"),
    );
    const eventError = await caught(errorStream.message[Symbol.asyncIterator]().next());
    expect(eventError).toBeInstanceOf(CursorExpiredError);
    expect(eventError).toMatchObject({
      cause: eventProblem,
      code: "cursor_expired",
      message: "cursor expired",
      requestId: "sse-error-request",
      status: 200,
      transport: "http",
    });

    const credentialCause = new Error("credential source unavailable");
    const credentialError = await caught(
      createHttpTransport({
        baseUrl,
        credentialProvider: async () => {
          throw credentialCause;
        },
        fetch: async () => {
          throw new Error("fetch must not run");
        },
      }).unary(ScheduleService.method.getSchedule, undefined, undefined, undefined, {
        name: "nightly",
      }),
    );
    expect(credentialError).toBeInstanceOf(AuthenticationError);
    expect(credentialError).toMatchObject({
      cause: credentialCause,
      code: "authentication",
      message: "The credential provider failed",
      transport: "http",
    });

    const authenticationProblem = { detail: "upstream detail is not the SDK message" };
    const authenticationError = await caught(
      unary(async () =>
        Response.json(authenticationProblem, {
          headers: { "x-request-id": "authentication-401" },
          status: 401,
        }),
      ),
    );
    expect(authenticationError).toBeInstanceOf(AuthenticationError);
    expect(authenticationError).toMatchObject({
      cause: authenticationProblem,
      code: "authentication",
      message: "Authentication failed",
      requestId: "authentication-401",
      status: 401,
      transport: "http",
    });
  });

  it("HTTP transport body-read and cancellation failures retain their causes", async () => {
    const networkCause = new TypeError("connection reset");
    const networkError = await caught(
      unary(async () => {
        throw networkCause;
      }),
    );
    expect(networkError).toBeInstanceOf(TransportError);
    expect(networkError).toMatchObject({ cause: networkCause, transport: "http" });

    const bodyReadCause = new Error("response body stream failed");
    const bodyReadError = await caught(
      unary(
        async () =>
          new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                controller.error(bodyReadCause);
              },
            }),
          ),
      ),
    );
    expect(bodyReadError).toBeInstanceOf(ProtocolError);
    expect(bodyReadError).toMatchObject({
      cause: bodyReadCause,
      message: "The mecatl server returned invalid JSON",
      status: 200,
      transport: "http",
    });

    const abortCause = new Error("caller cancelled");
    const controller = new AbortController();
    controller.abort(abortCause);
    const abortError = await caught(
      createHttpTransport({
        baseUrl,
        fetch: async (_input, init) => {
          if (init?.signal?.aborted === true) throw init.signal.reason;
          throw new Error("expected an aborted signal");
        },
      }).unary(ScheduleService.method.getSchedule, controller.signal, undefined, undefined, {
        name: "nightly",
      }),
    );
    expect(abortError).toBeInstanceOf(TransportError);
    expect(abortError).toMatchObject({ cause: abortCause, transport: "http" });

    const readerCause = new Error("SSE reader failed");
    const encoder = new TextEncoder();
    let reads = 0;
    const readerFailureStream = await watch(
      async () =>
        new Response(
          new ReadableStream<Uint8Array>({
            pull(streamController) {
              if (reads === 0) {
                reads += 1;
                streamController.enqueue(encoder.encode(data(validWatchFrame)));
                return;
              }
              streamController.error(readerCause);
            },
          }),
          { headers: { "content-type": "text/event-stream" } },
        ),
    );
    const readerFailureIterator = readerFailureStream.message[Symbol.asyncIterator]();
    await expect(readerFailureIterator.next()).resolves.toMatchObject({
      done: false,
      value: { cursor: "before-malformed" },
    });
    await expect(readerFailureIterator.next()).rejects.toBe(readerCause);
  });
});
