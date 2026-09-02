import { create, type JsonValue } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import type { Event } from "../src/events.js";
import {
  HarnessService,
  type WatchSessionEventsResponse,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { createHttpTransport, createRawClient, type WatchEnvelope } from "../src/index.js";
import { decodeWatchEnvelope } from "../src/watch.js";
import { sseResponse } from "./scripted-state.js";

const watchFeature = "watch_session_events";

async function* watchInput() {
  yield { cursor: "", runId: "run-watch", sessionId: "session-watch" };
}

async function collect(
  values: AsyncIterable<WatchSessionEventsResponse>,
  transport: "grpc" | "http",
): Promise<WatchEnvelope[]> {
  const envelopes: WatchEnvelope[] = [];
  for await (const value of values) envelopes.push(decodeWatchEnvelope(value, transport));
  return envelopes;
}

function grpcWatch(frames: WatchSessionEventsResponse[]) {
  return createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => ({ apiMajor: 1, features: [watchFeature] }),
      watchSessionEvents: async function* () {
        yield* frames;
      },
    });
  });
}

function httpWatch(values: unknown[], requests: string[] = []): typeof globalThis.fetch {
  return async (input) => {
    const url = String(input);
    const parsed = new URL(url);
    if (parsed.pathname === "/v1/compatibility") {
      return Response.json({ api_major: 1, features: [watchFeature] });
    }
    if (parsed.pathname === "/v1/sessions/session-watch/watch") {
      requests.push(url);
      return sseResponse(values);
    }
    return Response.json({}, { status: 404 });
  };
}

describe("watch envelope union", () => {
  it("event, boundary, and gap frames narrow by kind with a narrowed phase", () => {
    const event = decodeWatchEnvelope(
      create(WatchSessionEventsResponseSchema, {
        cursor: "cursor-replay",
        event: { runId: "run-watch", text: "replayed", type: "message.delta" },
        phase: "replay",
      }),
      "grpc",
    );
    if (event.kind !== "event") throw new Error("expected event envelope");
    expectTypeOf(event.phase).toEqualTypeOf<"live" | "replay">();
    expectTypeOf(event.event).toEqualTypeOf<Event>();
    expect(event).toMatchObject({ cursor: "cursor-replay", phase: "replay" });

    const boundary = decodeWatchEnvelope(
      create(WatchSessionEventsResponseSchema, {
        cursor: "cursor-boundary",
        phase: "live",
      }),
      "grpc",
    );
    if (boundary.kind !== "boundary") throw new Error("expected boundary envelope");
    expectTypeOf(boundary.phase).toEqualTypeOf<"live">();
    expect(boundary.cursor).toBe("cursor-boundary");

    const gap = decodeWatchEnvelope(
      create(WatchSessionEventsResponseSchema, { cursor: "wire-gap-cursor", phase: "gap" }),
      "grpc",
    );
    if (gap.kind !== "gap") throw new Error("expected gap envelope");
    expectTypeOf(gap.phase).toEqualTypeOf<"gap">();
    expect(gap).toEqual({ kind: "gap", phase: "gap" });
  });

  it("an unknown phase is preserved, never coerced and never thrown", () => {
    const decoded = [
      decodeWatchEnvelope(
        create(WatchSessionEventsResponseSchema, {
          cursor: "cursor-future",
          event: { runId: "run-watch", text: "future", type: "message.delta" },
          phase: "truncated",
        }),
        "grpc",
      ),
      decodeWatchEnvelope(
        create(WatchSessionEventsResponseSchema, {
          cursor: "cursor-live",
          event: { runId: "run-watch", text: "continues", type: "message.delta" },
          phase: "live",
        }),
        "grpc",
      ),
    ];

    const unknown = decoded[0];
    if (unknown?.kind !== "unknown") throw new Error("expected unknown envelope");
    expect(unknown).toMatchObject({
      cursor: "cursor-future",
      event: { kind: "message.delta", text: "future" },
      phase: "truncated",
    });
    expect(unknown.phase).not.toBe("live");
    expect(decoded[1]?.kind).toBe("event");
  });

  it("envelope events reuse the M1 event union and its unknown-kind convention", () => {
    const known = decodeWatchEnvelope(
      create(WatchSessionEventsResponseSchema, {
        cursor: "cursor-known",
        event: { runId: "run-watch", toolCall: { id: "call-1", name: "Read" }, type: "tool.call" },
        phase: "replay",
      }),
      "grpc",
    );
    if (known.kind !== "event" || known.event.kind !== "tool.call") {
      throw new Error("expected a known tool.call event");
    }
    expect(known.event.payload.name).toBe("Read");

    const rawHttpEvent = {
      future_payload: { answer: 42 },
      run_id: "run-watch",
      text: "future http event",
      type: "future.watch",
    };
    const rawEnvelope = {
      cursor: "cursor-unknown",
      event: rawHttpEvent,
      phase: "live",
    };
    const http = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: httpWatch([rawEnvelope]),
      }),
    });

    return collect(
      http.stream(HarnessService.method.watchSessionEvents, watchInput()),
      "http",
    ).then((envelopes) => {
      const decoded = envelopes[0];
      if (
        decoded?.kind !== "event" ||
        decoded.event.kind !== "unknown" ||
        decoded.event.transport !== "http"
      ) {
        throw new Error("expected an unknown HTTP event");
      }
      expectTypeOf(decoded.event.rawData).toEqualTypeOf<JsonValue>();
      expect(decoded.event).toMatchObject({
        rawData: rawHttpEvent,
        wireKind: "future.watch",
      });
    });
  });

  it("the same frame sequence decodes identically over gRPC and HTTP", async () => {
    const rawFrames = [
      {
        cursor: "cursor-1",
        event: { run_id: "run-watch", seq: "1", text: "replayed", type: "message.delta" },
        phase: "replay",
      },
      { cursor: "cursor-1", phase: "live" },
      {
        cursor: "cursor-2",
        event: { run_id: "run-watch", seq: "2", text: "live", type: "message.delta" },
        phase: "live",
      },
      { cursor: "cursor-gap", phase: "gap" },
    ];
    const grpcFrames = rawFrames.map((frame) =>
      create(WatchSessionEventsResponseSchema, {
        cursor: frame.cursor,
        ...(frame.event === undefined
          ? {}
          : {
              event: {
                runId: frame.event.run_id,
                seq: BigInt(frame.event.seq),
                text: frame.event.text,
                type: frame.event.type,
              },
            }),
        phase: frame.phase,
      }),
    );
    const requests: string[] = [];
    const grpc = createRawClient({ transport: grpcWatch(grpcFrames) });
    const http = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: httpWatch(rawFrames, requests),
      }),
    });

    const [grpcFeatures, httpFeatures, grpcEnvelopes, httpEnvelopes] = await Promise.all([
      grpc.features(),
      http.features(),
      collect(grpc.stream(HarnessService.method.watchSessionEvents, watchInput()), "grpc"),
      collect(http.stream(HarnessService.method.watchSessionEvents, watchInput()), "http"),
    ]);
    expect([...grpcFeatures]).toEqual([...httpFeatures]);
    expect(grpcEnvelopes).toEqual(httpEnvelopes);
    expect(requests).toHaveLength(1);
    expect(new URL(requests[0] ?? "").searchParams.get("run_id")).toBe("run-watch");

    const errorFetch: typeof globalThis.fetch = async (input) => {
      if (new URL(String(input)).pathname === "/v1/compatibility") {
        return Response.json({ api_major: 1, features: [watchFeature] });
      }
      return new Response(
        'event: error\ndata: {"code":"cursor_expired","detail":"expired","status":412}\n\n',
        { headers: { "content-type": "text/event-stream" } },
      );
    };
    const errored = createRawClient({
      transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch: errorFetch }),
    });
    await expect(
      collect(errored.stream(HarnessService.method.watchSessionEvents, watchInput()), "http"),
    ).rejects.toMatchObject({ code: "cursor_expired", transport: "http" });
  });

  it("the gap arm exposes no resumable cursor", () => {
    const raw = create(WatchSessionEventsResponseSchema, {
      cursor: "server-position-after-gap",
      phase: "gap",
    });
    expect(raw.cursor).toBe("server-position-after-gap");

    const decoded = decodeWatchEnvelope(raw, "grpc");
    if (decoded.kind !== "gap") throw new Error("expected gap envelope");
    type GapHasCursor =
      Extract<WatchEnvelope, { kind: "gap" }> extends {
        cursor: unknown;
      }
        ? true
        : false;
    expectTypeOf<GapHasCursor>().toEqualTypeOf<false>();
    expect("cursor" in decoded).toBe(false);
  });
});
