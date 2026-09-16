import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Client, ConnectionStatus } from "../src/client.js";
import {
  HarnessService,
  type WatchSessionEventsResponse,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import { connect } from "../src/index.js";

const HEARTBEAT_INTERVAL_MS = 30_000;
const WATCH_FEATURE = "watch_session_events";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function deferred(): { promise: Promise<void>; resolve(): void } {
  let resolve: () => void = () => undefined;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function boundary(cursor: string): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, { cursor, phase: "live" });
}

function message(cursor: string, runId: string): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, {
    cursor,
    event: { runId, text: "still watching", type: "message.delta" },
    phase: "live",
  });
}

function result(cursor: string, runId: string): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, {
    cursor,
    event: { result: { stop: "end_turn", text: "done" }, runId, type: "result" },
    phase: "live",
  });
}

async function sessionFor(
  watchSessionEvents: (request: {
    cursor: string;
    runId: string;
    sessionId: string;
  }) => AsyncIterable<WatchSessionEventsResponse>,
  compatibility: () => { apiMajor: number; features?: string[] } = () => ({
    apiMajor: 1,
    capabilities: {},
    features: [WATCH_FEATURE],
  }),
): Promise<{ client: Client; session: Awaited<ReturnType<Client["sessions"]["get"]>> }> {
  const client = connect({
    transport: createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: compatibility,
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents,
      });
    }),
  });
  return { client, session: await client.sessions.get("session-status") };
}

function observe(client: Client): { statuses: ConnectionStatus[]; unsubscribe(): void } {
  const statuses: ConnectionStatus[] = [];
  const unsubscribe = client.status.subscribe((status) => {
    expect(client.status.getSnapshot()).toBe(status);
    statuses.push(status);
  });
  return { statuses, unsubscribe };
}

async function waitForStatus(client: Client, target: ConnectionStatus): Promise<void> {
  if (client.status.getSnapshot() === target) return;
  await new Promise<void>((resolve) => {
    let unsubscribe: (() => void) | undefined;
    unsubscribe = client.status.subscribe((status) => {
      expect(client.status.getSnapshot()).toBe(status);
      if (status !== target) return;
      queueMicrotask(() => {
        unsubscribe?.();
        resolve();
      });
    });
  });
}

describe("attachment connection status", () => {
  it("a reconnecting attachment drives the connection status monitor", async () => {
    vi.useFakeTimers();
    const drop = deferred();
    let attempts = 0;
    const runId = "run-reconnect-status";
    const { client, session } = await sessionFor(async function* () {
      attempts += 1;
      yield boundary(`boundary-${attempts}`);
      if (attempts === 1) {
        await drop.promise;
        throw new ConnectError("watch dropped", Code.Unavailable);
      }
      yield result("result", runId);
    });
    const status = observe(client);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();
    await iterator.next();

    const resumed = iterator.next();
    drop.resolve();
    await waitForStatus(client, "reconnecting");
    expect(status.statuses.at(-1)).toBe("reconnecting");

    await vi.advanceTimersByTimeAsync(100);
    await waitForStatus(client, "online");
    expect((await resumed).value?.kind).toBe("event");
    expect(client.status.getSnapshot()).toBe(status.statuses.at(-1));

    await iterator.next();
    status.unsubscribe();
    await client.close();
  });

  it("reconnecting wins while any attachment is reconnecting", async () => {
    vi.useFakeTimers();
    const drop = deferred();
    const finishHealthy = deferred();
    const attempts = new Map<string, number>();
    const downRun = "run-down";
    const healthyRun = "run-healthy";
    const { client, session } = await sessionFor(async function* (request) {
      const attempt = (attempts.get(request.runId) ?? 0) + 1;
      attempts.set(request.runId, attempt);
      yield boundary(`${request.runId}-${attempt}`);
      if (request.runId === downRun && attempt === 1) {
        await drop.promise;
        throw new ConnectError("watch dropped", Code.Unavailable);
      }
      if (request.runId === healthyRun) await finishHealthy.promise;
      yield result(`${request.runId}-result`, request.runId);
    });
    const status = observe(client);
    const down = (await session.attach(downRun))[Symbol.asyncIterator]();
    const healthy = (await session.attach(healthyRun))[Symbol.asyncIterator]();
    await Promise.all([down.next(), healthy.next()]);

    const resumed = down.next();
    const healthyResult = healthy.next();
    drop.resolve();
    await waitForStatus(client, "reconnecting");
    const onlineEmissions = status.statuses.filter((value) => value === "online").length;

    await client.sessions.get("successful-unary");
    expect(client.status.getSnapshot()).toBe("reconnecting");
    expect(status.statuses.filter((value) => value === "online")).toHaveLength(onlineEmissions);

    await vi.advanceTimersByTimeAsync(100);
    await resumed;
    await waitForStatus(client, "online");

    finishHealthy.resolve();
    await healthyResult;
    await Promise.all([down.next(), healthy.next()]);
    status.unsubscribe();
    await client.close();
  });

  it("status resolves by fixed precedence rather than last writer", async () => {
    vi.useFakeTimers();
    let apiMajor = 1;
    const reconnectDrop = deferred();
    const authDrop = deferred();
    const laterReconnectDrop = deferred();
    const incompatibleDrop = deferred();
    const gates = new Map([
      ["run-reconnecting", reconnectDrop],
      ["run-auth", authDrop],
      ["run-later-reconnecting", laterReconnectDrop],
      ["run-incompatible", incompatibleDrop],
    ]);
    const { client, session } = await sessionFor(
      async function* (request) {
        yield boundary(`${request.runId}-boundary`);
        await gates.get(request.runId)?.promise;
        if (request.runId === "run-auth") {
          throw new ConnectError("expired credential", Code.Unauthenticated);
        }
        throw new ConnectError("watch dropped", Code.Unavailable);
      },
      () => ({ apiMajor, capabilities: {}, features: [WATCH_FEATURE] }),
    );
    const status = observe(client);

    const reconnecting = (await session.attach("run-reconnecting"))[Symbol.asyncIterator]();
    const unauthorized = (await session.attach("run-auth"))[Symbol.asyncIterator]();
    const laterReconnect = (await session.attach("run-later-reconnecting"))[Symbol.asyncIterator]();
    const incompatible = (await session.attach("run-incompatible"))[Symbol.asyncIterator]();
    await Promise.all([
      reconnecting.next(),
      unauthorized.next(),
      laterReconnect.next(),
      incompatible.next(),
    ]);
    const reconnectingNext = reconnecting.next().catch((error: unknown) => error);
    const unauthorizedNext = unauthorized.next().catch((error: unknown) => error);
    const laterReconnectNext = laterReconnect.next().catch((error: unknown) => error);
    const incompatibleNext = incompatible.next().catch((error: unknown) => error);

    reconnectDrop.resolve();
    await waitForStatus(client, "reconnecting");
    authDrop.resolve();
    await waitForStatus(client, "unauthorized");
    laterReconnectDrop.resolve();
    await Promise.resolve();
    expect(client.status.getSnapshot()).toBe("unauthorized");

    apiMajor = 2;
    incompatibleDrop.resolve();
    await vi.advanceTimersByTimeAsync(100);
    expect(await incompatibleNext).toMatchObject({ code: "incompatible_server" });
    expect(client.status.getSnapshot()).toBe("incompatible");

    await Promise.allSettled([reconnectingNext, unauthorizedNext, laterReconnectNext]);
    apiMajor = 1;
    await client.sessions.get("successful-unary");
    expect(client.status.getSnapshot()).toBe("online");

    status.unsubscribe();
    await client.close();
  });

  it("an attachment is not a heartbeat subscriber", async () => {
    vi.useFakeTimers();
    let compatibilityCalls = 0;
    let released = false;
    const { client, session } = await sessionFor(
      async function* () {
        try {
          yield boundary("open");
        } finally {
          released = true;
        }
      },
      () => {
        compatibilityCalls += 1;
        return { apiMajor: 1, capabilities: {}, features: [WATCH_FEATURE] };
      },
    );
    const iterator = (await session.attach("run-heartbeat"))[Symbol.asyncIterator]();
    await iterator.next();

    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS * 2);
    expect(compatibilityCalls).toBe(1);
    expect(released).toBe(false);

    const unsubscribe = client.status.subscribe(() => undefined);
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(compatibilityCalls).toBe(2);
    unsubscribe();
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS * 2);
    expect(compatibilityCalls).toBe(2);
    expect(released).toBe(false);

    await iterator.return?.();
    await client.close();
  });

  it("page visibility does not pause an attachment", async () => {
    vi.useFakeTimers();
    const visibility = new EventTarget() as EventTarget & {
      visibilityState: DocumentVisibilityState;
    };
    visibility.visibilityState = "visible";
    vi.stubGlobal("document", visibility);

    let compatibilityCalls = 0;
    let released = false;
    const deliver = deferred();
    const { client, session } = await sessionFor(
      async function* (request) {
        try {
          yield boundary("open");
          await deliver.promise;
          yield message("hidden-message", request.runId);
        } finally {
          released = true;
        }
      },
      () => {
        compatibilityCalls += 1;
        return { apiMajor: 1, capabilities: {}, features: [WATCH_FEATURE] };
      },
    );
    const unsubscribe = client.status.subscribe(() => undefined);
    const iterator = (await session.attach("run-visible"))[Symbol.asyncIterator]();
    await iterator.next();
    const hiddenDelivery = iterator.next();

    visibility.visibilityState = "hidden";
    visibility.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS * 2);
    expect(compatibilityCalls).toBe(1);
    expect(released).toBe(false);

    deliver.resolve();
    expect((await hiddenDelivery).value?.kind).toBe("event");
    expect(released).toBe(false);

    await iterator.return?.();
    unsubscribe();
    await client.close();
  });
});
