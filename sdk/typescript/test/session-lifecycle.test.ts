import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";
import { connectTransport } from "../src/client.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  InvalidStateError,
  imagePart,
  ProtocolError,
  SESSION_ID_HEADER_NAME,
  ServerError,
  SessionMode,
  TransportError,
} from "../src/index.js";

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
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: (request) => {
          calls.push(`get:${request.sessionId}`);
          if (!sessions.has(request.sessionId)) throw notFound();
          return { session: { sessionId: request.sessionId } };
        },
      });
    });
    const client = connect({ transport });

    const created = await client.sessions.create({});
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
      "get:forked",
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
          return { apiMajor: 1, capabilities: {}, features: ["server_info"] };
        },
      });
    });
    const client = connect({ transport });
    const statuses: string[] = [];
    client.status.subscribe((status) => statuses.push(status));
    await client.sessions.create({});

    await client[Symbol.asyncDispose]();
    await expect(client.sessions.create({})).rejects.toBeInstanceOf(InvalidStateError);
    expect(() => client.status.subscribe(() => undefined)).toThrow(InvalidStateError);
    expect(compatibilityCalls).toBe(1);
    expect(statuses.at(-1)).toBe("online");

    const dispose = vi.fn(async () => undefined);
    const ownedTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "owned-session" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
      });
    }) as ReturnType<typeof createRouterTransport> & AsyncDisposable;
    ownedTransport[Symbol.asyncDispose] = dispose;
    const owned = connectTransport({
      owned: true,
      transport: ownedTransport,
      transportKind: "grpc",
      visibility: false,
    });
    await owned.sessions.create({});
    await owned.close();
    expect(dispose).toHaveBeenCalledOnce();
  });

  it("rename setMode and compact map lifecycle results", async () => {
    const calls: string[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        compactSession: (request) => {
          calls.push(`compact:${request.sessionId}`);
          return { compacted: true };
        },
        createSession: () => ({
          sessionCapabilities: { audio: false, image: false },
          sessionId: "session",
        }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        renameSession: (request) => {
          calls.push(`rename:${request.sessionId}:${request.title}`);
          return {
            session: {
              mode: SessionMode.Default,
              sessionCapabilities: { audio: false, image: false },
              sessionId: request.sessionId,
              titleMetadata: { provenance: "operator", title: request.title },
            },
          };
        },
        setMode: (request) => {
          calls.push(`mode:${request.sessionId}:${request.mode}`);
          return {
            session: {
              mode: request.mode,
              resolvedModel: {
                contextWindow: 128_000n,
                modelId: "plan-model",
                providerId: "provider",
              },
              sessionCapabilities: { audio: false, image: true },
              sessionId: request.sessionId,
            },
          };
        },
        converse: async function* (requests) {
          const first = await requests[Symbol.asyncIterator]().next();
          expect(first.value?.kind.case).toBe("prompt");
          if (first.value?.kind.case === "prompt")
            expect(first.value.kind.value.parts).toHaveLength(1);
          yield {
            event: { result: { stop: "end_turn", text: "done" }, runId: "run", type: "result" },
          };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    await expect(session.rename("A title")).resolves.toMatchObject({
      sessionId: "session",
      title: { provenance: "operator", value: "A title" },
    });
    await expect(session.setMode(SessionMode.Plan)).resolves.toMatchObject({
      mode: SessionMode.Plan,
      resolvedModel: { modelId: "plan-model" },
      sessionCapabilities: { audio: false, image: true },
    });
    await expect(session.compact()).resolves.toBe(true);
    await expect(
      (
        await session.run([
          imagePart({ mimeType: "image/png", url: "https://example.com/image.png" }),
        ])
      ).result(),
    ).resolves.toMatchObject({ stopReason: "end_turn" });
    expect(calls).toEqual(["rename:session:A title", "mode:session:2", "compact:session"]);
    await client.close();
  });

  it("unary lifecycle controls preserve request options affinity and typed errors", async () => {
    const seen: string[] = [];
    let compactFails = true;
    let createdId = "session";
    const observe = (operation: string, context: { requestHeader: Headers }) => {
      seen.push(
        `${operation}:${context.requestHeader.get("x-caller")}:${context.requestHeader.get(SESSION_ID_HEADER_NAME)}`,
      );
    };
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        closeSession: (_request, context) => {
          observe("close", context);
          return {};
        },
        compactSession: (_request, context) => {
          observe("compact", context);
          if (compactFails) throw new ConnectError("offline", Code.Unavailable);
          return { compacted: false };
        },
        createSession: () => ({ sessionId: createdId }),
        deleteSession: (_request, context) => {
          observe("delete", context);
          return {};
        },
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: (request, context) => {
          observe("snapshot", context);
          return { session: { mode: SessionMode.Default, sessionId: request.sessionId } };
        },
        getSessionTranscript: (request, context) => {
          observe("transcript", context);
          return { complete: true, sessionId: request.sessionId };
        },
        renameSession: (request, context) => {
          observe("rename", context);
          return { session: { mode: SessionMode.Default, sessionId: request.sessionId } };
        },
        setMode: (request, context) => {
          observe("setMode", context);
          return { session: { mode: request.mode, sessionId: request.sessionId } };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const requestOptions = {
      headers: { [SESSION_ID_HEADER_NAME]: "stale", "x-caller": "kept" },
      timeoutMs: 5_000,
    };

    await session.snapshot(requestOptions);
    await session.transcript(requestOptions);
    await session.rename("title", requestOptions);
    await session.setMode(SessionMode.Plan, requestOptions);
    await expect(session.compact(requestOptions)).rejects.toBeInstanceOf(TransportError);
    await session.close(requestOptions);
    await session.delete(requestOptions);
    expect(seen).toEqual([
      "snapshot:kept:session",
      "transcript:kept:session",
      "rename:kept:session",
      "setMode:kept:session",
      "compact:kept:session",
      "close:kept:session",
      "delete:kept:session",
    ]);

    createdId = "session-α";
    compactFails = false;
    const external = await client.sessions.create({});
    await external.snapshot(requestOptions);
    await external.transcript(requestOptions);
    await external.rename("title", requestOptions);
    await external.setMode(SessionMode.Plan, requestOptions);
    await external.compact(requestOptions);
    await external.close(requestOptions);
    await external.delete(requestOptions);
    expect(seen.slice(-7)).toEqual([
      "snapshot:kept:null",
      "transcript:kept:null",
      "rename:kept:null",
      "setMode:kept:null",
      "compact:kept:null",
      "close:kept:null",
      "delete:kept:null",
    ]);
    await client.close();
  });

  it("clear and fork return successors without invalidating the source", async () => {
    const closed: string[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        clearSession: () => ({ sessionId: "cleared" }),
        closeSession: (request, context) => {
          closed.push(`${request.sessionId}:${context.requestHeader.get(SESSION_ID_HEADER_NAME)}`);
          return {};
        },
        createSession: () => ({ sessionId: "source" }),
        forkSession: () => ({ sessionId: "forked" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: (request) => ({
          session: { mode: SessionMode.Default, sessionId: request.sessionId },
        }),
      });
    });
    const client = connect({ transport });
    const source = await client.sessions.create({});
    const cleared = await source.clear();
    const forked = await client.sessions.fork(source.id);

    expect(cleared.id).toBe("cleared");
    expect(forked.id).toBe("forked");
    expect("fork" in source).toBe(false);
    await expect(source.snapshot()).resolves.toMatchObject({ sessionId: "source" });
    await source.close();
    await cleared.close();
    await forked.close();
    expect(closed).toEqual(["source:source", "cleared:cleared", "forked:forked"]);
    await client.close();
  });

  it("fork and clear map exactly their approved domain options", async () => {
    const captured: unknown[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        clearSession: (request) => {
          captured.push(request);
          return { sessionId: "cleared" };
        },
        createSession: () => ({ sessionId: "source" }),
        forkSession: (request) => {
          captured.push(request);
          return { sessionId: "forked" };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const client = connect({ transport });
    const source = await client.sessions.create({});
    const selector = "opaque:α/../worktree";
    await source.clear({ worktreeSelector: selector });
    await client.sessions.fork(source.id, {
      modelId: "model",
      providerId: "provider",
      reasoningEffort: "max",
      title: "fork title",
      worktreeSelector: selector,
    });

    expect(captured[0]).toMatchObject({
      sourceSessionId: "source",
      worktreeSelector: selector,
    });
    expect(captured[1]).toMatchObject({
      modelId: "model",
      providerId: "provider",
      reasoningEffort: "max",
      sourceSessionId: "source",
      title: "fork title",
      worktreeSelector: selector,
    });
    await client.close();
  });

  it("create get and fork preserve request options and validate returned ids", async () => {
    const seen: string[] = [];
    let createId = "created";
    let getId: string | undefined;
    let forkId = "forked";
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: (_request, context) => {
          seen.push(`create:${context.requestHeader.get("x-caller")}`);
          return { sessionCapabilities: { audio: false, image: true }, sessionId: createId };
        },
        forkSession: (_request, context) => {
          seen.push(`fork:${context.requestHeader.get("x-caller")}`);
          return { sessionId: forkId };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: (request, context) => {
          seen.push(`get:${context.requestHeader.get("x-caller")}`);
          return {
            session: {
              mode: SessionMode.Default,
              sessionCapabilities: { audio: false, image: true },
              sessionId: getId ?? request.sessionId,
            },
          };
        },
      });
    });
    const client = connect({ transport });
    const options = { headers: { "x-caller": "kept" } };
    await expect(client.sessions.create({}, options)).resolves.toMatchObject({ id: "created" });
    await expect(client.sessions.get("created", options)).resolves.toMatchObject({ id: "created" });
    await expect(client.sessions.fork("created", {}, options)).resolves.toMatchObject({
      id: "forked",
    });
    expect(seen).toEqual(["create:kept", "get:kept", "fork:kept", "get:kept"]);

    createId = "";
    await expect(client.sessions.create({}, options)).rejects.toBeInstanceOf(ProtocolError);
    getId = "other";
    await expect(client.sessions.get("created", options)).rejects.toBeInstanceOf(ProtocolError);
    forkId = "";
    await expect(client.sessions.fork("created", {}, options)).rejects.toBeInstanceOf(
      ProtocolError,
    );
    await client.close();
  });
});
