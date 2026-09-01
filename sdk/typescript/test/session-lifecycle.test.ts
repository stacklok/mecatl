import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";
import { connectTransport } from "../src/client.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, InvalidStateError, ServerError } from "../src/index.js";

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function notFound(): ConnectError {
  const error = new ConnectError("session missing", Code.NotFound);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([
      ...field(1, bytes("session_not_found")),
      ...field(2, bytes("mecatl.stacklok.com")),
    ]),
  });
  return error;
}

describe("session lifecycle", () => {
  it("create, get, fork, close, and delete map to their RPCs", async () => {
    const calls: string[] = [];
    const sessions = new Set<string>();
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        closeSession: (request) => {
          calls.push(`close:${request.sessionId}`);
          if (!sessions.has(request.sessionId)) throw notFound();
          return {};
        },
        createSession: () => {
          calls.push("create");
          sessions.add("created");
          return { sessionId: "created" };
        },
        deleteSession: (request) => {
          calls.push(`delete:${request.sessionId}`);
          if (!sessions.delete(request.sessionId)) throw notFound();
          return {};
        },
        forkSession: (request) => {
          calls.push(`fork:${request.sourceSessionId}:${request.title}`);
          sessions.add("forked");
          return { sessionId: "forked" };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => {
          calls.push(`get:${request.sessionId}`);
          if (!sessions.has(request.sessionId)) throw notFound();
          return { session: { sessionId: request.sessionId } };
        },
      });
    });
    const client = connect({ transport });

    const created = await client.sessions.create({ workspace: "/workspace" });
    expect(created.id).toBe("created");
    expect((await client.sessions.get(created.id)).id).toBe("created");

    const forked = await client.sessions.fork(created.id, { title: "peer" });
    expect(forked.id).toBe("forked");

    await created.close();
    expect((await client.sessions.get(created.id)).id).toBe("created");
    await created.delete();

    const missing = await client.sessions.get(created.id).catch((error) => error);
    expect(missing).toBeInstanceOf(ServerError);
    expect(missing.code).toBe("session_not_found");
    expect(calls).toEqual([
      "create",
      "get:created",
      "fork:created:peer",
      "close:created",
      "get:created",
      "delete:created",
      "get:created",
    ]);
    await client.close();
  });

  it("client disposal releases resources and fails fast after", async () => {
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
    const statuses: string[] = [];
    client.status.subscribe((status) => statuses.push(status));
    await client.sessions.create({ workspace: "/workspace" });

    await client[Symbol.asyncDispose]();
    await expect(client.sessions.create({ workspace: "/workspace" })).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    expect(() => client.status.subscribe(() => undefined)).toThrow(InvalidStateError);
    expect(compatibilityCalls).toBe(1);
    expect(statuses.at(-1)).toBe("online");

    const dispose = vi.fn(async () => undefined);
    const ownedTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "owned-session" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
      });
    }) as ReturnType<typeof createRouterTransport> & AsyncDisposable;
    ownedTransport[Symbol.asyncDispose] = dispose;
    const owned = connectTransport({
      owned: true,
      transport: ownedTransport,
      transportKind: "grpc",
      visibility: false,
    });
    await owned.sessions.create({ workspace: "/workspace" });
    await owned.close();
    expect(dispose).toHaveBeenCalledOnce();
  });
});
