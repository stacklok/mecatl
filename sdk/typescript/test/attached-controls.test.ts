import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type AttachedRun,
  type Client,
  connect,
  createHttpTransport,
  ServerError,
  TransportError,
  UnsupportedFeatureError,
  type WatchEnvelope,
} from "../src/index.js";

const watchFeature = "watch_session_events";
const sessionId = "session-attached-controls";
const runId = "run-attached-controls";

interface RecordedRequest {
  readonly body: Record<string, unknown>;
  readonly method: string;
  readonly path: string;
}

interface HttpHarnessOptions {
  readonly cancel?: (body: Record<string, unknown>) => Promise<Response> | Response;
  readonly features?: readonly string[];
  readonly watch?: (url: URL) => Response;
}

function resultFrame(run: string, stop = "end_turn", phase = "live") {
  return {
    cursor: `cursor-${run}-${stop}`,
    event: {
      result: { stop, text: stop },
      run_id: run,
      text: stop,
      type: "result",
    },
    phase,
  };
}

function sseResponse(frames: readonly unknown[]): Response {
  return new Response(frames.map((frame) => `data: ${JSON.stringify(frame)}\n\n`).join(""), {
    headers: { "content-type": "text/event-stream" },
  });
}

function httpHarness(options: HttpHarnessOptions = {}): {
  readonly client: Client;
  readonly requests: RecordedRequest[];
} {
  const requests: RecordedRequest[] = [];
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? "GET";
    const body =
      typeof init?.body === "string"
        ? (JSON.parse(init.body) as Record<string, unknown>)
        : ({} as Record<string, unknown>);
    requests.push({ body, method, path: url.pathname });

    if (url.pathname === "/v1/compatibility") {
      return Response.json({
        api_major: 1,
        capabilities: {},
        features: options.features ?? [watchFeature],
      });
    }
    if (url.pathname === `/v1/sessions/${sessionId}` && method === "GET") {
      return Response.json({ session_id: sessionId });
    }
    if (url.pathname === `/v1/sessions/${sessionId}/cancel` && method === "POST") {
      return options.cancel?.(body) ?? new Response(null, { status: 204 });
    }
    if (url.pathname === `/v1/sessions/${sessionId}/watch` && method === "GET") {
      return (
        options.watch?.(url) ??
        sseResponse([
          { cursor: "cursor-boundary", phase: "live" },
          resultFrame(url.searchParams.get("run_id") ?? runId),
        ])
      );
    }
    return Response.json({ code: "not_found", detail: "not found" }, { status: 404 });
  };
  const transport = createHttpTransport({ baseUrl: "http://mecatl.test", fetch });
  return {
    client: connect({ transport, transportKind: "http" }),
    requests,
  };
}

async function attachHttp(options: HttpHarnessOptions = {}): Promise<{
  readonly attached: AttachedRun;
  readonly client: Client;
  readonly requests: RecordedRequest[];
}> {
  const harness = httpHarness(options);
  const session = await harness.client.sessions.get(sessionId);
  const attached = await session.attach(runId);
  return { attached, ...harness };
}

function grpcHarness(): {
  readonly client: Client;
  readonly converseCalls: () => number;
} {
  let calls = 0;
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      converse: async function* () {
        calls += 1;
        yield { event: { runId, text: "unexpected", type: "message.delta" } };
      },
      getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
      getSession: () => ({ session: { sessionId } }),
      watchSessionEvents: async function* () {
        yield { cursor: "cursor-boundary", phase: "live" };
      },
    });
  });
  return { client: connect({ transport }), converseCalls: () => calls };
}

async function attachGrpc(): Promise<{
  readonly attached: AttachedRun;
  readonly client: Client;
  readonly converseCalls: () => number;
}> {
  const harness = grpcHarness();
  const session = await harness.client.sessions.get(sessionId);
  const attached = await session.attach(runId);
  return { attached, ...harness };
}

async function collect(attached: AttachedRun): Promise<WatchEnvelope[]> {
  const envelopes: WatchEnvelope[] = [];
  for await (const envelope of attached) envelopes.push(envelope);
  return envelopes;
}

function deferred<T>(): { readonly promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined;
  const promise = new Promise<T>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

async function spinUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (predicate()) return;
    await Promise.resolve();
  }
  throw new Error("condition was not reached");
}

describe("attached controls", () => {
  it("attached cancel goes through the async out-of-band control seam", async () => {
    const { attached, client, requests } = await attachHttp();

    await attached.cancel();

    expect(requests.filter((request) => request.path.endsWith("/cancel"))).toEqual([
      {
        body: { expected_run_id: runId },
        method: "POST",
        path: `/v1/sessions/${sessionId}/cancel`,
      },
    ]);
    expect(requests.some((request) => request.path.endsWith("/prompt"))).toBe(false);
    await attached.close();
    await client.close();
  });

  it("cancel resolves from the response and rejects on transport failure", async () => {
    const response = deferred<Response>();
    const successful = await attachHttp({
      cancel: () => response.promise,
      watch: () =>
        sseResponse([
          { cursor: "cursor-boundary", phase: "live" },
          resultFrame(runId, "cancelled"),
        ]),
    });
    let settled = false;
    const pending = successful.attached.cancel().then(() => {
      settled = true;
    });
    await Promise.resolve();
    expect(settled).toBe(false);

    response.resolve(new Response(null, { status: 204 }));
    await pending;
    expect(settled).toBe(true);
    const envelopes = await collect(successful.attached);
    expect(envelopes.at(-1)).toMatchObject({
      event: { kind: "result", payload: { stop: "cancelled" } },
    });
    await successful.client.close();

    const failed = await attachHttp({
      cancel: () => Promise.reject(new TypeError("connection reset")),
    });
    await expect(failed.attached.cancel()).rejects.toBeInstanceOf(TransportError);
    await failed.attached.close();
    await failed.client.close();
  });

  it("a stale attached control fails typed and leaves a newer run untouched", async () => {
    const newerRun = "run-newer";
    let currentRun = newerRun;
    const harness = httpHarness({
      cancel: (body) => {
        if (body.expected_run_id !== currentRun) {
          return Response.json(
            {
              code: "stale_run_control",
              detail: "run is no longer current",
              status: 409,
            },
            { status: 409 },
          );
        }
        currentRun = "";
        return new Response(null, { status: 204 });
      },
      watch: (url) =>
        sseResponse([
          {
            cursor: "cursor-newer-message",
            event: {
              run_id: url.searchParams.get("run_id") ?? "",
              text: "newer run still active",
              type: "message.delta",
            },
            phase: "replay",
          },
          resultFrame(url.searchParams.get("run_id") ?? "", "end_turn", "replay"),
        ]),
    });
    const session = await harness.client.sessions.get(sessionId);
    const stale = await session.attach(runId);

    const error = await stale.cancel().catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(ServerError);
    expect(error).toMatchObject({ code: "stale_run_control", transport: "http" });
    expect(currentRun).toBe(newerRun);

    const newer = await session.attach(newerRun);
    const envelopes = await collect(newer);
    expect(envelopes).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          event: expect.objectContaining({ text: "newer run still active" }),
        }),
      ]),
    );
    expect(currentRun).toBe(newerRun);
    await stale.close();
    await harness.client.close();
  });

  it("attached cancel over gRPC is a typed unsupported-feature error", async () => {
    const { attached, client, converseCalls } = await attachGrpc();

    const error = await attached.cancel().catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(UnsupportedFeatureError);
    expect(error).toMatchObject({
      feature: "prompt_free_controls",
      transport: "grpc",
    });
    expect(converseCalls()).toBe(0);
    await attached.close();
    await client.close();
  });

  it("attached approval keeps the local compatibility deferral on both transports", async () => {
    const grpc = await attachGrpc();
    for (const control of [
      () => grpc.attached.approve("ask-1", true),
      () => grpc.attached.resolveAsk("ask-1", "allow_once"),
    ]) {
      await expect(control()).rejects.toMatchObject({
        code: "unsupported_feature",
        feature: "attached_run_controls",
        transport: "grpc",
      });
    }
    expect(grpc.converseCalls()).toBe(0);
    await grpc.attached.close();
    await grpc.client.close();

    const http = await attachHttp();
    for (const control of [
      () => http.attached.approve("ask-1", true),
      () => http.attached.resolveAsk("ask-1", "allow_once"),
    ]) {
      await expect(control()).rejects.toMatchObject({
        code: "unsupported_feature",
        feature: "attached_run_controls",
        transport: "http",
      });
    }
    expect(http.requests.some((request) => request.path.endsWith("/approve"))).toBe(false);
    await http.attached.close();
    await http.client.close();
  });

  it("attached steer keeps the local compatibility deferral and never promotes", async () => {
    const grpc = await attachGrpc();
    await expect(grpc.attached.steer("new direction")).rejects.toMatchObject({
      code: "unsupported_feature",
      feature: "attached_run_controls",
      transport: "grpc",
    });
    expect(grpc.converseCalls()).toBe(0);
    await grpc.attached.close();
    await grpc.client.close();

    const http = await attachHttp({ features: [watchFeature, "http_steer"] });
    await expect(http.attached.steer("new direction")).rejects.toMatchObject({
      code: "unsupported_feature",
      feature: "attached_run_controls",
      transport: "http",
    });
    expect(
      http.requests.some(
        (request) => request.path.endsWith("/steer") || request.path.endsWith("/prompt"),
      ),
    ).toBe(false);
    await http.attached.close();
    await http.client.close();
  });

  it("every detach path leaves the run running", async () => {
    for (const path of ["abort", "dispose", "break"] as const) {
      let cancelRequests = 0;
      let terminal = false;
      let watchReleases = 0;
      const controller = new AbortController();
      const harness = httpHarness({
        cancel: () => {
          cancelRequests += 1;
          return new Response(null, { status: 204 });
        },
        watch: () => {
          if (terminal) return sseResponse([resultFrame(runId, "end_turn", "replay")]);
          const encoder = new TextEncoder();
          return new Response(
            new ReadableStream({
              cancel() {
                watchReleases += 1;
              },
              start(stream) {
                stream.enqueue(
                  encoder.encode(`data: ${JSON.stringify({ cursor: "live", phase: "live" })}\n\n`),
                );
              },
            }),
            { headers: { "content-type": "text/event-stream" } },
          );
        },
      });
      const session = await harness.client.sessions.get(sessionId);
      const attached = await session.attach(runId, { signal: controller.signal });

      if (path === "break") {
        for await (const _envelope of attached) break;
      } else {
        const iterator = attached[Symbol.asyncIterator]();
        await iterator.next();
        if (path === "abort") {
          controller.abort();
          expect((await iterator.next()).done).toBe(true);
        } else {
          await attached[Symbol.asyncDispose]();
        }
      }

      await spinUntil(() => watchReleases === 1);
      expect(cancelRequests).toBe(0);
      terminal = true;
      const fresh = await session.attach(runId);
      const replay = await collect(fresh);
      expect(replay.at(-1)).toMatchObject({ event: { kind: "result" } });
      expect(fresh.live).toBe(false);
      expect(cancelRequests).toBe(0);
      await harness.client.close();
    }
  });
});
