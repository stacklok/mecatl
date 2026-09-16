import type { Transport } from "@connectrpc/connect";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  NoRunsError,
  ServerError,
  type SessionActivity,
  UnsupportedFeatureError,
  type WatchEnvelope,
} from "../src/index.js";

const watchFeature = "watch_session_events";

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function codedError(code: string, status: Code): ConnectError {
  const error = new ConnectError(code, status);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([...field(1, bytes(code)), ...field(2, bytes("mecatl.stacklok.com"))]),
  });
  return error;
}

function event(runId: string, text: string, cursor: string, phase = "replay") {
  return {
    cursor,
    event: { runId, text, type: "message.delta" },
    phase,
  };
}

function terminal(runId: string, cursor: string, phase = "live") {
  return {
    cursor,
    event: {
      result: { stop: "end_turn", text: "done" },
      runId,
      type: "result",
    },
    phase,
  };
}

function boundary(cursor: string) {
  return { cursor, phase: "live" };
}

async function collect(activity: SessionActivity): Promise<WatchEnvelope[]> {
  const envelopes: WatchEnvelope[] = [];
  for await (const envelope of activity) envelopes.push(envelope);
  return envelopes;
}

async function sessionFor(
  transport: Transport,
  sessionId: string,
  transportKind: "grpc" | "http" = "grpc",
) {
  const client = connect({ transport, transportKind });
  const session = await client.sessions.get(sessionId);
  return { client, session };
}

describe("session attachment", () => {
  it("attach with no run id selects the newest run over one unfiltered watch", async () => {
    const filters: string[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents: async function* (request) {
          filters.push(request.runId);
          yield event("run-old", "old", "cursor-1");
          yield terminal("run-old", "cursor-2", "replay");
          yield event("run-new", "new", "cursor-3");
          yield boundary("cursor-3");
          yield terminal("run-new", "cursor-4");
        },
      });
    });
    const { client, session } = await sessionFor(transport, "session-newest");

    const attached = await session.attach();
    expect(attached.runId).toBe("run-new");
    expect(filters).toEqual([""]);
    const envelopes = await collect(attached);
    expect(
      envelopes.map((envelope) =>
        envelope.kind === "event"
          ? `${envelope.event.runId}:${envelope.event.text}`
          : envelope.kind,
      ),
    ).toEqual(["run-new:new", "boundary", "run-new:"]);
    expect(filters).toEqual([""]);
    await client.close();
  });

  it("live flips false when the terminal is observed", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents: async function* () {
          yield event("run-live", "working", "cursor-1");
          yield terminal("run-live", "cursor-2");
        },
      });
    });
    const { client, session } = await sessionFor(transport, "session-live");
    const attached = await session.attach("run-live");
    const iterator = attached[Symbol.asyncIterator]();

    expect(attached.live).toBe(true);
    await iterator.next();
    expect(attached.live).toBe(true);
    const result = await iterator.next();
    expect(result.value?.kind).toBe("event");
    expect(attached.live).toBe(false);
    expect((await iterator.next()).done).toBe(true);
    await client.close();
  });

  it("a readable session with no run-bearing events is NoRunsError", async () => {
    let enteredFollow = false;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents: async function* (request) {
          if (request.sessionId === "session-runless") {
            yield event("", "session metadata", "cursor-1");
          }
          yield boundary("cursor-1");
          enteredFollow = true;
          yield event("future-run", "must not follow", "cursor-2", "live");
        },
      });
    });
    const client = connect({ transport });

    for (const sessionId of ["session-empty", "session-runless"]) {
      const session = await client.sessions.get(sessionId);
      await expect(session.attach()).rejects.toBeInstanceOf(NoRunsError);
    }
    expect(enteredFollow).toBe(false);
    await client.close();
  });

  it("a running session with an empty log reports NoRunsError", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
        getSession: (request) => ({
          session: { sessionId: request.sessionId, state: "running" },
        }),
        watchSessionEvents: async function* () {
          yield boundary("");
        },
      });
    });
    const { client, session } = await sessionFor(transport, "session-running-empty");

    await expect(session.attach()).rejects.toBeInstanceOf(NoRunsError);
    await client.close();
  });

  it("an unknown or foreign session id is session_not_found, never NoRunsError", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents: async function* () {
          yield* [];
          throw codedError("session_not_found", Code.NotFound);
        },
      });
    });
    const { client, session } = await sessionFor(transport, "session-foreign");

    const rejection = await session.attach().catch((error) => error);
    expect(rejection).toBeInstanceOf(ServerError);
    expect(rejection).not.toBeInstanceOf(NoRunsError);
    expect(rejection).toMatchObject({ code: "session_not_found" });
    await client.close();
  });

  it("an explicit run id uses the server run filter", async () => {
    const filters: string[] = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents: async function* (request) {
          filters.push(request.runId);
          yield event(request.runId, "filtered", "cursor-1");
          yield terminal(request.runId, "cursor-2");
        },
      });
    });
    const { client, session } = await sessionFor(transport, "session-explicit");

    const attached = await session.attach("run-explicit");
    await collect(attached);
    expect(filters).toEqual(["run-explicit"]);
    await client.close();
  });

  it("the watch feature gate fires on both transports", async () => {
    for (const transportKind of ["grpc", "http"] as const) {
      let watches = 0;
      const transport = createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: ["server_info"],
          }),
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
          watchSessionEvents: async function* () {
            watches += 1;
            yield* [];
          },
        });
      });
      const { client, session } = await sessionFor(
        transport,
        `session-no-feature-${transportKind}`,
        transportKind,
      );

      for (const attach of [() => session.attach(), () => session.activity()]) {
        const rejection = await attach().catch((error) => error);
        expect(rejection).toBeInstanceOf(UnsupportedFeatureError);
        expect(rejection).toMatchObject({
          feature: watchFeature,
          transport: transportKind,
        });
      }
      expect(watches).toBe(0);
      await client.close();
    }

    for (const code of ["watch_unsupported", "no_event_log"]) {
      const transport = createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
          watchSessionEvents: async function* () {
            yield* [];
            throw codedError(code, Code.Unimplemented);
          },
        });
      });
      const { client, session } = await sessionFor(transport, `session-${code}`);
      await expect(session.attach()).rejects.toMatchObject({ code });
      await client.close();
    }
  });

  it("a delegation child id is refused and a sched-- id is not", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: [watchFeature] }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        watchSessionEvents: async function* (request) {
          if (/^(subagent-|parallel-|team-)/.test(request.sessionId)) {
            throw codedError("invalid_argument", Code.InvalidArgument);
          }
          yield event("run-scheduled", "scheduled", "cursor-1");
          yield boundary("cursor-1");
          yield terminal("run-scheduled", "cursor-2");
        },
      });
    });
    const client = connect({ transport });

    for (const prefix of ["subagent-", "parallel-", "team-"]) {
      const child = await client.sessions.get(`${prefix}child`);
      await expect(child.attach()).rejects.toMatchObject({ code: "invalid_argument" });
      const activity = await child.activity();
      await expect(collect(activity)).rejects.toMatchObject({ code: "invalid_argument" });
    }

    const scheduled = await client.sessions.get("sched--fire-1");
    const attached = await scheduled.attach();
    expect(attached.runId).toBe("run-scheduled");
    await expect(collect(attached)).resolves.toHaveLength(3);
    await client.close();
  });
});
