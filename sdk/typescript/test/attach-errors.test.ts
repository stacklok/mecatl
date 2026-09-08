import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import {
  HarnessService,
  type WatchSessionEventsResponse,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  ActivityGapError,
  CursorExpiredError,
  CursorMalformedError,
  connect,
  createHttpTransport,
  createRawClient,
  MecatlError,
} from "../src/index.js";
import { decodeWatchEnvelope } from "../src/watch.js";

const sessionId = "session-errors";
const runId = "run-errors";
const watchFeature = "watch_session_events";

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function errorInfo(reason: string, requestId: string): Uint8Array {
  const metadata = [...field(1, bytes("request_id")), ...field(2, bytes(requestId))];
  return new Uint8Array([
    ...field(1, bytes(reason)),
    ...field(2, bytes("mecatl.stacklok.com")),
    ...field(3, metadata),
  ]);
}

function statusError(
  code: "cursor_expired" | "cursor_malformed",
  status: Code,
  requestId: string,
): ConnectError {
  const error = new ConnectError(`${code} from gRPC`, status);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: errorInfo(code, requestId),
  });
  return error;
}

function routerWatch(
  watchSessionEvents: () => AsyncIterable<WatchSessionEventsResponse>,
): Transport {
  return createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => ({ apiMajor: 1, features: [watchFeature] }),
      getSession: () => ({ session: { sessionId } }),
      watchSessionEvents,
    });
  });
}

function failedWatch(error: Error): AsyncIterable<WatchSessionEventsResponse> {
  return {
    [Symbol.asyncIterator]: () => ({ next: async () => Promise.reject(error) }),
  };
}

async function* watchInput() {
  yield { cursor: "", runId, sessionId };
}

async function caught(operation: Promise<unknown>): Promise<MecatlError> {
  try {
    await operation;
  } catch (error) {
    if (error instanceof MecatlError) return error;
    throw error;
  }
  throw new Error("expected the operation to fail");
}

async function attachmentError(transport: Transport): Promise<MecatlError> {
  const client = connect({ transport });
  try {
    const session = await client.sessions.get(sessionId);
    const attached = await session.attach(runId);
    return await caught(attached[Symbol.asyncIterator]().next());
  } finally {
    await client.close();
  }
}

function httpFault(code: "cursor_expired" | "cursor_malformed", requestId: string): Transport {
  const fetch: typeof globalThis.fetch = async (input) => {
    const path = new URL(String(input)).pathname;
    if (path === "/v1/compatibility") {
      return Response.json({ api_major: 1, features: [watchFeature] });
    }
    if (path === `/v1/sessions/${sessionId}`) {
      return Response.json({ session_id: sessionId });
    }
    if (path === `/v1/sessions/${sessionId}/watch`) {
      return new Response(
        `event: error\ndata: ${JSON.stringify({ code, error: `${code} from SSE` })}\n\n`,
        {
          headers: { "content-type": "text/event-stream", "x-request-id": requestId },
        },
      );
    }
    return Response.json({}, { status: 404 });
  };
  return createHttpTransport({ baseUrl: "http://mecatl.test", fetch });
}

describe("attachment errors", () => {
  it("a gap becomes a local ActivityGapError and never advances the checkpoint", async () => {
    const transport = routerWatch(async function* () {
      yield create(WatchSessionEventsResponseSchema, {
        cursor: "before-gap",
        event: { runId, text: "durable", type: "message.delta" },
        phase: "replay",
      });
      yield create(WatchSessionEventsResponseSchema, {
        cursor: "after-gap",
        phase: "gap",
      });
      yield create(WatchSessionEventsResponseSchema, {
        cursor: "after-gap-event",
        event: { runId, text: "must not arrive", type: "message.delta" },
        phase: "live",
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();

    const first = await iterator.next();
    if (first.done || first.value.kind !== "event") throw new Error("expected the first event");
    const failure = await caught(iterator.next());

    expect(failure).toBeInstanceOf(ActivityGapError);
    expect(failure).toMatchObject({ code: "activity_gap", transport: "local" });
    expect(attached.cursor).toBe(first.value.cursor);
    await client.close();
  });

  it("an expired cursor ends the attachment instead of restarting it", async () => {
    let watchRequests = 0;
    const transport = routerWatch(() => {
      watchRequests += 1;
      return failedWatch(statusError("cursor_expired", Code.FailedPrecondition, "request-expired"));
    });

    const failure = await attachmentError(transport);

    expect(failure).toBeInstanceOf(CursorExpiredError);
    expect(failure).toMatchObject({ code: "cursor_expired", transport: "grpc" });
    expect(watchRequests).toBe(1);
  });

  it("a cursor fault maps identically from a gRPC status and an SSE error frame", async () => {
    const cases = [
      {
        code: "cursor_expired" as const,
        errorType: CursorExpiredError,
        requestId: "request-expired",
        status: Code.FailedPrecondition,
      },
      {
        code: "cursor_malformed" as const,
        errorType: CursorMalformedError,
        requestId: "request-malformed",
        status: Code.InvalidArgument,
      },
    ];

    for (const testCase of cases) {
      const grpc = await attachmentError(
        routerWatch(() =>
          failedWatch(statusError(testCase.code, testCase.status, testCase.requestId)),
        ),
      );
      const http = await attachmentError(httpFault(testCase.code, testCase.requestId));

      expect(grpc).toBeInstanceOf(testCase.errorType);
      expect(http).toBeInstanceOf(testCase.errorType);
      expect(grpc.constructor).toBe(http.constructor);
      expect(grpc).toMatchObject({
        code: testCase.code,
        requestId: testCase.requestId,
        status: testCase.status,
        transport: "grpc",
      });
      expect(http).toMatchObject({
        code: testCase.code,
        requestId: testCase.requestId,
        status: 200,
        transport: "http",
      });
    }
  });

  it("the raw watch surfaces the gap arm rather than throwing", async () => {
    const raw = createRawClient({
      transport: routerWatch(async function* () {
        yield create(WatchSessionEventsResponseSchema, {
          cursor: "raw-after-gap",
          phase: "gap",
        });
      }),
    });

    const next = await raw
      .stream(HarnessService.method.watchSessionEvents, watchInput())
      [Symbol.asyncIterator]()
      .next();
    if (next.done) throw new Error("expected a raw watch envelope");

    expect(decodeWatchEnvelope(next.value, "grpc")).toEqual({ kind: "gap", phase: "gap" });
  });
});
