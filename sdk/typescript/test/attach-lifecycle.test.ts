import { fromJson, type JsonValue } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";
import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import {
  HarnessService,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  createHttpTransport,
  InvalidStateError,
  type SessionActivity,
  type WatchEnvelope,
} from "../src/index.js";

const watchFeature = "watch_session_events";
const sessionId = "session-lifecycle";
const runId = "run-lifecycle";

type ScriptedFrame = Record<string, JsonValue>;

const lifecycleFrames: readonly ScriptedFrame[] = [
  {
    cursor: "cursor-1",
    event: { run_id: runId, seq: "1", text: "durable one", type: "message.delta" },
    phase: "replay",
  },
  {
    cursor: "cursor-2",
    event: { run_id: runId, seq: "2", text: "durable two", type: "message.delta" },
    phase: "replay",
  },
  { cursor: "cursor-2", phase: "live" },
  {
    cursor: "cursor-3",
    event: { run_id: runId, seq: "3", text: "appended live", type: "message.delta" },
    phase: "live",
  },
  {
    cursor: "cursor-4",
    event: {
      result: { stop: "end_turn", text: "done" },
      run_id: runId,
      seq: "4",
      text: "done",
      type: "result",
    },
    phase: "live",
  },
];

interface WatchRequest {
  cursor: string;
  runId: string;
}

interface ScriptedHarness {
  readonly requests: WatchRequest[];
  readonly served: ScriptedFrame[];
  readonly transport: Transport;
}

function staticFrames(frames: readonly ScriptedFrame[]): () => AsyncIterable<ScriptedFrame> {
  return async function* () {
    yield* frames;
  };
}

function scriptedHarness(
  kind: "grpc" | "http",
  source: () => AsyncIterable<ScriptedFrame> = staticFrames(lifecycleFrames),
): ScriptedHarness {
  const requests: WatchRequest[] = [];
  const served: ScriptedFrame[] = [];

  if (kind === "grpc") {
    return {
      requests,
      served,
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
          watchSessionEvents: async function* (request) {
            requests.push({ cursor: request.cursor, runId: request.runId });
            for await (const frame of source()) {
              served.push(frame);
              yield fromJson(WatchSessionEventsResponseSchema, frame);
            }
          },
        });
      }),
    };
  }

  const fetch: typeof globalThis.fetch = async (input) => {
    const url = new URL(String(input));
    if (url.pathname === "/v1/compatibility") {
      return Response.json({ api_major: 1, capabilities: {}, features: [watchFeature] });
    }
    if (url.pathname === `/v1/sessions/${sessionId}`) {
      return Response.json({ session_id: sessionId });
    }
    if (url.pathname === `/v1/sessions/${sessionId}/watch`) {
      requests.push({
        cursor: url.searchParams.get("cursor") ?? "",
        runId: url.searchParams.get("run_id") ?? "",
      });
      return sseStream(
        (async function* () {
          for await (const frame of source()) {
            served.push(frame);
            yield frame;
          }
        })(),
      );
    }
    return Response.json({}, { status: 404 });
  };

  return {
    requests,
    served,
    transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch }),
  };
}

function sseStream(frames: AsyncIterable<ScriptedFrame>): Response {
  const encoder = new TextEncoder();
  const iterator = frames[Symbol.asyncIterator]();
  return new Response(
    new ReadableStream({
      async cancel() {
        await iterator.return?.();
      },
      async pull(controller) {
        const next = await iterator.next();
        if (next.done) {
          controller.close();
          return;
        }
        controller.enqueue(encoder.encode(`data: ${JSON.stringify(next.value)}\n\n`));
      },
    }),
    { headers: { "content-type": "text/event-stream" } },
  );
}

function deferred(): { readonly promise: Promise<void>; resolve(): void } {
  let resolve: () => void = () => undefined;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

async function sessionFor(harness: ScriptedHarness, kind: "grpc" | "http" = "grpc") {
  const client = connect({ transport: harness.transport, transportKind: kind });
  const session = await client.sessions.get(sessionId);
  return { client, session };
}

async function collect(activity: SessionActivity): Promise<WatchEnvelope[]> {
  const envelopes: WatchEnvelope[] = [];
  for await (const envelope of activity) envelopes.push(envelope);
  return envelopes;
}

function signature(envelope: WatchEnvelope): string {
  if (envelope.kind !== "event") return `${envelope.phase}:${envelope.kind}`;
  return `${envelope.phase}:${envelope.event.kind}:${envelope.event.text}`;
}

describe("attachment lifecycle", () => {
  it("an attachment replays, crosses the live boundary, and follows to the terminal", async () => {
    const liveAppend = deferred();
    const terminalAppend = deferred();
    const harness = scriptedHarness("grpc", () =>
      (async function* () {
        yield* lifecycleFrames.slice(0, 3);
        await liveAppend.promise;
        yield lifecycleFrames[3] as ScriptedFrame;
        await terminalAppend.promise;
        yield lifecycleFrames[4] as ScriptedFrame;
      })(),
    );
    const { client, session } = await sessionFor(harness);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();

    expect(signature((await iterator.next()).value as WatchEnvelope)).toBe(
      "replay:message.delta:durable one",
    );
    expect(signature((await iterator.next()).value as WatchEnvelope)).toBe(
      "replay:message.delta:durable two",
    );
    expect(signature((await iterator.next()).value as WatchEnvelope)).toBe("live:boundary");

    let liveDelivered = false;
    const pendingLive = iterator.next().then((next) => {
      liveDelivered = true;
      return next;
    });
    await Promise.resolve();
    expect(liveDelivered).toBe(false);
    liveAppend.resolve();
    expect(signature((await pendingLive).value as WatchEnvelope)).toBe(
      "live:message.delta:appended live",
    );

    let terminalDelivered = false;
    const pendingTerminal = iterator.next().then((next) => {
      terminalDelivered = true;
      return next;
    });
    await Promise.resolve();
    expect(terminalDelivered).toBe(false);
    terminalAppend.resolve();
    expect(signature((await pendingTerminal).value as WatchEnvelope)).toBe("live:result:done");
    expect(attached.live).toBe(false);
    expect((await iterator.next()).done).toBe(true);
    await client.close();
  });

  it("an attachment on a finished run replays to its terminal and completes", async () => {
    let enteredFollow = false;
    const harness = scriptedHarness("grpc", () =>
      (async function* () {
        yield lifecycleFrames[0] as ScriptedFrame;
        yield { ...lifecycleFrames[4], phase: "replay" };
        enteredFollow = true;
        await new Promise<never>(() => undefined);
      })(),
    );
    const { client, session } = await sessionFor(harness);
    const attached = await session.attach(runId);

    await expect(collect(attached)).resolves.toHaveLength(2);
    expect(attached.live).toBe(false);
    expect(enteredFollow).toBe(false);
    await client.close();
  });

  it("from now discards the replay locally and requires a run id", async () => {
    const harness = scriptedHarness("grpc");
    const { client, session } = await sessionFor(harness);
    const attached = await session.attach(runId, { from: "now" });

    expect((await collect(attached)).map(signature)).toEqual([
      "live:boundary",
      "live:message.delta:appended live",
      "live:result:done",
    ]);
    expect(harness.served.map((frame) => frame.phase)).toEqual([
      "replay",
      "replay",
      "live",
      "live",
      "live",
    ]);
    expect(harness.requests).toEqual([{ cursor: "", runId }]);

    const rejection = await session.attach(undefined, { from: "now" }).catch((error) => error);
    expect(rejection).toBeInstanceOf(InvalidStateError);
    expect(rejection).toMatchObject({ code: "invalid_state", transport: "local" });
    expect(harness.requests).toHaveLength(1);
    await client.close();
  });

  it("the attachment lifecycle agrees across gRPC and HTTP", async () => {
    const deliveries: WatchEnvelope[][] = [];
    for (const kind of ["grpc", "http"] as const) {
      const harness = scriptedHarness(kind);
      const { client, session } = await sessionFor(harness, kind);
      deliveries.push(await collect(await session.attach(runId)));
      expect(harness.requests).toEqual([{ cursor: "", runId }]);
      await client.close();
    }

    expect(deliveries[0]).toEqual(deliveries[1]);
    expect(deliveries[0]?.map(signature)).toEqual([
      "replay:message.delta:durable one",
      "replay:message.delta:durable two",
      "live:boundary",
      "live:message.delta:appended live",
      "live:result:done",
    ]);
  });
});
