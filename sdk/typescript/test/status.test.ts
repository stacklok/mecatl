import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Client, ConnectionStatus } from "../src/client.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, TransportError } from "../src/index.js";
import { connect as connectNode } from "../src/node.js";

const HEARTBEAT_INTERVAL_MS = 30_000;

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function statusPromise(client: Client, target: ConnectionStatus): Promise<void> {
  return new Promise((resolve) => {
    client.status.subscribe((status) => {
      expect(client.status.getSnapshot()).toBe(status);
      if (status === target) resolve();
    });
  });
}

describe("connection status", () => {
  it("status walks the closed vocabulary", async () => {
    let finishFloor: (() => void) | undefined;
    const floor = new Promise<void>((resolve) => {
      finishFloor = resolve;
    });
    let dropRequests = false;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => {
          if (dropRequests) throw new ConnectError("dropped", Code.Unavailable);
          return { sessionId: "session" };
        },
        getCompatibilityInfo: async () => {
          await floor;
          return { apiMajor: 1 };
        },
      });
    });
    const client = connect({ transport });
    const statuses: ConnectionStatus[] = [];
    let online: (() => void) | undefined;
    const reachedOnline = new Promise<void>((resolve) => {
      online = resolve;
    });
    client.status.subscribe((status) => {
      statuses.push(status);
      expect(client.status.getSnapshot()).toBe(status);
      if (status === "online") online?.();
    });
    expect(statuses).toEqual(["connecting"]);
    finishFloor?.();
    await reachedOnline;

    dropRequests = true;
    await expect(client.sessions.create({})).rejects.toBeInstanceOf(TransportError);
    expect(statuses).toEqual(["connecting", "online", "reconnecting", "offline"]);

    const unauthorized = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => {
            throw new ConnectError("no credentials", Code.Unauthenticated);
          },
        });
      }),
    });
    await statusPromise(unauthorized, "unauthorized");

    const incompatible = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({ apiMajor: 2 }),
        });
      }),
    });
    await statusPromise(incompatible, "incompatible");

    expect(
      new Set<ConnectionStatus>([
        ...statuses,
        unauthorized.status.getSnapshot(),
        incompatible.status.getSnapshot(),
      ]),
    ).toEqual(
      new Set<ConnectionStatus>([
        "connecting",
        "online",
        "reconnecting",
        "offline",
        "unauthorized",
        "incompatible",
      ]),
    );
    await Promise.all([client.close(), unauthorized.close(), incompatible.close()]);
  });

  it("heartbeat is subscriber-gated", async () => {
    vi.useFakeTimers();
    let compatibilityCalls = 0;
    let dropHeartbeat = false;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session" }),
        getCompatibilityInfo: () => {
          compatibilityCalls += 1;
          if (dropHeartbeat) throw new ConnectError("dropped", Code.Unavailable);
          return { apiMajor: 1 };
        },
      });
    });
    const client = connect({ transport });
    await client.sessions.create({});
    expect(compatibilityCalls).toBe(1);

    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS * 2);
    expect(compatibilityCalls).toBe(1);

    const first = client.status.subscribe(() => undefined);
    const second = client.status.subscribe(() => undefined);
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(compatibilityCalls).toBe(2);

    first();
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(compatibilityCalls).toBe(3);

    second();
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS * 2);
    expect(compatibilityCalls).toBe(3);

    const statuses: ConnectionStatus[] = [];
    const stop = client.status.subscribe((status) => statuses.push(status));
    dropHeartbeat = true;
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(statuses.slice(-2)).toEqual(["reconnecting", "offline"]);

    dropHeartbeat = false;
    await client.sessions.create({});
    expect(client.status.getSnapshot()).toBe("online");
    stop();
    await client.close();
  });

  it("hidden pages pause the heartbeat", async () => {
    vi.useFakeTimers();
    const visibility = new EventTarget() as EventTarget & {
      visibilityState: DocumentVisibilityState;
    };
    visibility.visibilityState = "visible";
    vi.stubGlobal("document", visibility);

    let compatibilityCalls = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session" }),
        getCompatibilityInfo: () => {
          compatibilityCalls += 1;
          return { apiMajor: 1 };
        },
      });
    });
    const client = connect({ transport });
    await client.sessions.create({});
    const unsubscribe = client.status.subscribe(() => undefined);

    visibility.visibilityState = "hidden";
    visibility.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS * 2);
    expect(compatibilityCalls).toBe(1);

    visibility.visibilityState = "visible";
    visibility.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(compatibilityCalls).toBe(2);

    unsubscribe();
    await client.close();

    visibility.visibilityState = "hidden";
    let nodeCompatibilityCalls = 0;
    const node = connectNode({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          createSession: () => ({ sessionId: "node-session" }),
          getCompatibilityInfo: () => {
            nodeCompatibilityCalls += 1;
            return { apiMajor: 1 };
          },
        });
      }),
    });
    await node.sessions.create({});
    const unsubscribeNode = node.status.subscribe(() => undefined);
    await vi.advanceTimersByTimeAsync(HEARTBEAT_INTERVAL_MS);
    expect(nodeCompatibilityCalls).toBe(2);
    unsubscribeNode();
    await node.close();
  });
});
