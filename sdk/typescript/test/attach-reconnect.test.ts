import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport, type Transport } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";

import { credentialInterceptor } from "../src/credentials.js";
import {
  ActivityGapError,
  CursorExpiredError,
  CursorMalformedError,
  IncompatibleServerError,
} from "../src/errors.js";
import {
  HarnessService,
  type WatchSessionEventsResponse,
  WatchSessionEventsResponseSchema,
} from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type AttachedRun,
  type Client,
  connect,
  type MecatlError,
  ServerError,
  TransportError,
  UnsupportedFeatureError,
} from "../src/index.js";
import { createAttachedRun } from "../src/watch.js";

const sessionId = "session-reconnect";
const runId = "run-reconnect";
const watchFeature = "watch_session_events";

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

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

function event(
  token: string,
  text: string,
  run = runId,
  phase = "live",
): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, {
    cursor: token,
    event: { runId: run, text, type: "message.delta" },
    phase,
  });
}

function boundary(token: string): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, { cursor: token, phase: "live" });
}

function result(token: string, run = runId, phase = "live"): WatchSessionEventsResponse {
  return create(WatchSessionEventsResponseSchema, {
    cursor: token,
    event: { result: { stop: "end_turn", text: "done" }, runId: run, type: "result" },
    phase,
  });
}

function watchTransport(
  watchSessionEvents: (request: {
    cursor: string;
    runId: string;
    sessionId: string;
  }) => AsyncIterable<WatchSessionEventsResponse>,
  compatibility: () => { apiMajor: number; features?: string[] } = () => ({
    apiMajor: 1,
    features: [watchFeature],
  }),
): Transport {
  return createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: compatibility,
      getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      watchSessionEvents,
    });
  });
}

async function sessionFor(transport: Transport) {
  const client = connect({ transport });
  const session = await client.sessions.get(sessionId);
  return { client, session };
}

function failedWatch(error: Error): AsyncIterable<WatchSessionEventsResponse> {
  return {
    [Symbol.asyncIterator]: () => ({
      next: async () => Promise.reject(error),
    }),
  };
}

function terminalError(code: string): MecatlError {
  switch (code) {
    case "activity_gap":
      return new ActivityGapError();
    case "cursor_expired":
      return new CursorExpiredError(code, { transport: "grpc" });
    case "cursor_malformed":
      return new CursorMalformedError(code, { transport: "grpc" });
    case "incompatible_server":
      return new IncompatibleServerError(code, { transport: "grpc" });
    default:
      return new ServerError(code, {
        code: code as
          | "invalid_argument"
          | "management_unauthorized"
          | "no_event_log"
          | "session_not_found"
          | "watch_unsupported",
        transport: "grpc",
      });
  }
}

async function spinUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    if (predicate()) return;
    await Promise.resolve();
  }
  throw new Error("condition did not become true");
}

describe("attachment reconnect authority", () => {
  it("a dropped watch reconnects from its checkpoint under the same filter", async () => {
    const requests: Array<{ cursor: string; runId: string }> = [];
    const transport = watchTransport(async function* (request) {
      requests.push({ cursor: request.cursor, runId: request.runId });
      if (requests.length === 1) {
        yield boundary("token-0");
        yield event("token-1", "one");
        throw new ConnectError("dropped", Code.Unavailable);
      }
      yield boundary(request.cursor);
      yield event("token-2", "two");
      yield result("token-3");
    });
    const { client, session } = await sessionFor(transport);
    const attached = await session.attach(runId);
    const seen: string[] = [];

    for await (const envelope of attached) {
      if (envelope.kind === "event" && envelope.event.kind === "message.delta") {
        seen.push(envelope.event.text);
      }
    }

    expect(seen).toEqual(["one", "two"]);
    expect(requests).toEqual([
      { cursor: "", runId },
      { cursor: "token-1", runId },
    ]);
    await client.close();
  });

  it("a daemon-side gRPC cancel reconnects the watch", async () => {
    const requests: string[] = [];
    const transport = watchTransport(async function* (request) {
      requests.push(request.cursor);
      if (requests.length === 1) {
        yield boundary("cancel-0");
        yield event("cancel-1", "before restart");
        throw new ConnectError("daemon stopped", Code.Canceled);
      }
      yield boundary(request.cursor);
      yield event("cancel-2", "after restart");
      yield result("cancel-3");
    });
    const { client, session } = await sessionFor(transport);
    const seen: string[] = [];

    for await (const envelope of await session.attach(runId)) {
      if (envelope.kind === "event" && envelope.event.kind === "message.delta") {
        seen.push(envelope.event.text);
      }
    }

    expect(seen).toEqual(["before restart", "after restart"]);
    expect(requests).toEqual(["", "cancel-1"]);
    await client.close();
  });

  it("backoff grows monotonically to a cap and is jittered", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    const delays: number[] = [];
    let watches = 0;
    const clientAbort = new AbortController();
    const operations = {
      cancelRun: async () => undefined,
      clientSignal: clientAbort.signal,
      features: async () => new Set([watchFeature]),
      invalidateCompatibility: () => undefined,
      transportKind: "grpc" as const,
      watch: () => {
        watches += 1;
        if (watches > 9) throw terminalError("cursor_expired");
        return failedWatch(new TransportError("dropped", { transport: "grpc" }));
      },
    };
    const attached = await createAttachedRun(
      sessionId,
      runId,
      operations,
      {},
      {
        scheduler: {
          sleep: async (ms) => {
            delays.push(ms);
          },
        },
      },
    );

    await expect(attached[Symbol.asyncIterator]().next()).rejects.toMatchObject({
      code: "cursor_expired",
    });

    expect(delays).toHaveLength(9);
    expect(delays.every((delay, index) => index === 0 || delay >= (delays[index - 1] ?? 0))).toBe(
      true,
    );
    expect(delays.at(-2)).toBe(5_000);
    expect(delays.at(-1)).toBe(5_000);
    expect(delays[0]).toBe(75);
  });

  it("a lagging watch is resumable and reconnects rather than failing", async () => {
    const requests: string[] = [];
    const transport = watchTransport(async function* (request) {
      requests.push(request.cursor);
      if (requests.length === 1) {
        yield boundary("");
        yield event("lag-1", "before lag");
        throw codedError("watch_lagging", Code.ResourceExhausted);
      }
      yield boundary(request.cursor);
      yield event("lag-2", "after lag");
      yield result("lag-3");
    });
    const { client, session } = await sessionFor(transport);
    const texts: string[] = [];

    for await (const envelope of await session.attach(runId)) {
      if (envelope.kind === "event" && envelope.event.kind === "message.delta") {
        texts.push(envelope.event.text);
      }
    }

    expect(texts).toEqual(["before lag", "after lag"]);
    expect(requests).toEqual(["", "lag-1"]);
    await client.close();
  });

  it("mutations, prompts, approvals, and owned runs are never retried", async () => {
    let mutationAttempts = 0;
    const mutation = createRouterTransport((router) => {
      router.service(HarnessService, {
        deleteSession: () => {
          mutationAttempts += 1;
          throw new ConnectError("dropped mutation", Code.Unavailable);
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const mutationClient = await sessionFor(mutation);
    await expect(mutationClient.session.delete()).rejects.toBeInstanceOf(TransportError);
    expect(mutationAttempts).toBe(1);
    await mutationClient.client.close();

    let promptAttempts = 0;
    const prompt = createRouterTransport((router) => {
      router.service(HarnessService, {
        converse: async function* () {
          promptAttempts += 1;
          yield await Promise.reject(new ConnectError("dropped prompt", Code.Unavailable));
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const promptClient = await sessionFor(prompt);
    await expect(promptClient.session.run("hello")).rejects.toBeInstanceOf(TransportError);
    expect(promptAttempts).toBe(1);
    await promptClient.client.close();

    let approvalStreams = 0;
    const approval = createRouterTransport((router) => {
      router.service(HarnessService, {
        converse: async function* (requests) {
          approvalStreams += 1;
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield {
            event: {
              ask: { args: "{}", askId: "ask-1", reason: "test", tool: "Bash" },
              runId,
              type: "permission.ask",
            },
          };
          await input.next();
          throw new ConnectError("dropped approval", Code.Unavailable);
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const approvalClient = await sessionFor(approval);
    const approvalRun = await approvalClient.session.run("approve");
    await approvalRun.resolveAsk("ask-1", "allow_once");
    const approvalEvents = approvalRun[Symbol.asyncIterator]();
    await approvalEvents.next();
    await expect(approvalEvents.next()).rejects.toBeInstanceOf(TransportError);
    expect(approvalStreams).toBe(1);
    await approvalClient.client.close();

    let ownedRunStreams = 0;
    const owned = createRouterTransport((router) => {
      router.service(HarnessService, {
        converse: async function* () {
          ownedRunStreams += 1;
          yield { event: { runId, text: "started", type: "message.delta" } };
          throw new ConnectError("dropped owned run", Code.Unavailable);
        },
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      });
    });
    const ownedClient = await sessionFor(owned);
    const ownedRun = await ownedClient.session.run("run");
    const ownedEvents = ownedRun[Symbol.asyncIterator]();
    await ownedEvents.next();
    await expect(ownedEvents.next()).rejects.toBeInstanceOf(TransportError);
    expect(ownedRunStreams).toBe(1);
    await ownedClient.client.close();
  });

  it("a permanent precondition code ends the attachment rather than retrying", async () => {
    const codes = [
      "cursor_expired",
      "cursor_malformed",
      "activity_gap",
      "session_not_found",
      "invalid_argument",
      "management_unauthorized",
      "incompatible_server",
      "watch_unsupported",
      "no_event_log",
    ];

    for (const code of codes) {
      let watches = 0;
      const clientAbort = new AbortController();
      const operations = {
        cancelRun: async () => undefined,
        clientSignal: clientAbort.signal,
        features: async () => new Set([watchFeature]),
        invalidateCompatibility: () => undefined,
        transportKind: "grpc" as const,
        watch: () => {
          watches += 1;
          return failedWatch(terminalError(code));
        },
      };
      const attached = await createAttachedRun(
        sessionId,
        runId,
        operations,
        {},
        {
          scheduler: { sleep: async () => undefined },
        },
      );

      await expect(attached[Symbol.asyncIterator]().next()).rejects.toMatchObject({ code });
      expect(watches).toBe(1);
    }
  });

  it("a clean non-terminal stream end resumes rather than completing", async () => {
    const requests: string[] = [];
    const transport = watchTransport(async function* (request) {
      requests.push(request.cursor);
      if (requests.length === 1) {
        yield boundary("");
        yield event("clean-1", "before eof");
        return;
      }
      yield boundary(request.cursor);
      yield event("clean-2", "after eof");
      yield result("clean-3");
    });
    const { client, session } = await sessionFor(transport);
    const texts: string[] = [];

    for await (const envelope of await session.attach(runId)) {
      if (envelope.kind === "event" && envelope.event.kind === "message.delta") {
        texts.push(envelope.event.text);
      }
    }

    expect(texts).toEqual(["before eof", "after eof"]);
    expect(requests).toEqual(["", "clean-1"]);
    await client.close();
  });

  it("activity resumes after a clean EOF that follows a run result", async () => {
    const requests: string[] = [];
    const transport = watchTransport(async function* (request) {
      requests.push(request.cursor);
      if (requests.length === 1) {
        yield boundary("");
        yield result("activity-1", "run-one");
        return;
      }
      yield boundary(request.cursor);
      yield event("activity-2", "next run", "run-two");
    });
    const { client, session } = await sessionFor(transport);
    const activity = await session.activity();
    const iterator = activity[Symbol.asyncIterator]();

    expect((await iterator.next()).value).toMatchObject({ kind: "boundary" });
    expect((await iterator.next()).value).toMatchObject({ event: { kind: "result" } });
    expect((await iterator.next()).value).toMatchObject({ event: { text: "next run" } });
    expect(requests).toEqual(["", "activity-1"]);
    await iterator.return?.();
    await client.close();
  });

  it("authentication resumes with a refreshed credential while management_unauthorized terminates", async () => {
    let credential = 0;
    let watchAttempts = 0;
    const order: string[] = [];
    const authenticated = createRouterTransport(
      (router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: (_request, context) => {
            order.push(`compat:${context.requestHeader.get("authorization")}`);
            return { apiMajor: 1, features: [watchFeature] };
          },
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
          watchSessionEvents: async function* (_request, context) {
            watchAttempts += 1;
            order.push(`watch:${context.requestHeader.get("authorization")}`);
            if (watchAttempts === 1) {
              yield boundary("");
              yield event("auth-1", "before refresh");
              throw new ConnectError("expired", Code.Unauthenticated);
            }
            yield boundary("auth-1");
            yield result("auth-2");
          },
        });
      },
      {
        transport: {
          interceptors: [
            credentialInterceptor({
              credentialProvider: async () => ({ authorization: `Bearer token-${++credential}` }),
            }),
          ],
        },
      },
    );
    const authenticatedClient = await sessionFor(authenticated);
    for await (const _envelope of await authenticatedClient.session.attach(runId)) {
      // Drain through the refreshed reconnect.
    }
    const firstWatch = order.findIndex((entry) => entry.startsWith("watch:"));
    const secondWatch = order.findIndex(
      (entry, index) => index > firstWatch && entry.startsWith("watch:"),
    );
    expect(order.slice(firstWatch + 1, secondWatch)).toContainEqual(
      expect.stringMatching(/^compat:/u),
    );
    expect(order[firstWatch]).not.toBe(order[secondWatch]);
    expect(watchAttempts).toBe(2);
    await authenticatedClient.client.close();

    let deniedAttempts = 0;
    const denied = watchTransport(() => {
      deniedAttempts += 1;
      return failedWatch(codedError("management_unauthorized", Code.PermissionDenied));
    });
    const deniedClient = await sessionFor(denied);
    const deniedRun = await deniedClient.session.attach(runId);
    await expect(deniedRun[Symbol.asyncIterator]().next()).rejects.toMatchObject({
      code: "management_unauthorized",
    });
    expect(deniedAttempts).toBe(1);
    await deniedClient.client.close();
  });

  it("a reconnect after a gap invalidates the compatibility cache", async () => {
    let compatibilityCalls = 0;
    let watchAttempts = 0;
    const transport = watchTransport(
      async function* () {
        watchAttempts += 1;
        yield boundary("");
        yield event("cache-1", "before restart");
        throw new ConnectError("daemon stopped", Code.Unavailable);
      },
      () => {
        compatibilityCalls += 1;
        return {
          apiMajor: 1,
          features: compatibilityCalls === 1 ? [watchFeature] : [],
        };
      },
    );
    const { client, session } = await sessionFor(transport);
    const attached = await session.attach(runId);
    const iterator = attached[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();

    await expect(iterator.next()).rejects.toBeInstanceOf(UnsupportedFeatureError);
    expect(compatibilityCalls).toBe(2);
    expect(watchAttempts).toBe(1);
    await client.close();
  });

  it("a reconnect does not re-announce the replay-to-live boundary", async () => {
    let attempts = 0;
    const transport = watchTransport(async function* (request) {
      attempts += 1;
      yield boundary(request.cursor);
      if (attempts === 1) {
        yield event("boundary-1", "one");
        throw new ConnectError("dropped", Code.Unavailable);
      }
      yield event("boundary-2", "two");
      yield result("boundary-3");
    });
    const { client, session } = await sessionFor(transport);
    const kinds: string[] = [];

    for await (const envelope of await session.attach(runId)) kinds.push(envelope.kind);

    expect(kinds.filter((kind) => kind === "boundary")).toHaveLength(1);
    expect(kinds).toEqual(["boundary", "event", "event", "event"]);
    await client.close();
  });

  it("every release path stops the loop and clears the timer", async () => {
    vi.useFakeTimers();

    async function midBackoff(
      release: (
        client: Client,
        attached: AttachedRun,
        controller: AbortController,
      ) => Promise<void> | void,
    ): Promise<void> {
      let attempts = 0;
      let releases = 0;
      const transport = watchTransport(async function* () {
        attempts += 1;
        try {
          yield boundary("");
          yield event("release-1", "before release");
          throw new ConnectError("dropped", Code.Unavailable);
        } finally {
          releases += 1;
        }
      });
      const { client, session } = await sessionFor(transport);
      const controller = new AbortController();
      const attached = await session.attach(runId, { signal: controller.signal });
      const iterator = attached[Symbol.asyncIterator]();
      await iterator.next();
      await iterator.next();
      const pending = iterator.next();
      for (let attempt = 0; attempt < 20 && vi.getTimerCount() === 0; attempt += 1) {
        await vi.advanceTimersByTimeAsync(0);
      }
      expect(vi.getTimerCount()).toBeGreaterThan(0);

      await release(client, attached, controller);
      await pending;
      expect(vi.getTimerCount()).toBe(0);
      await vi.runAllTimersAsync();
      expect(attempts).toBe(1);
      expect(releases).toBe(1);
      await client.close();
    }

    await midBackoff((_client, _attached, controller) => controller.abort());
    await midBackoff(async (_client, attached) => attached[Symbol.asyncDispose]());
    await midBackoff(async (client) => client.close());

    let breakAttempts = 0;
    let breakReleases = 0;
    const breakClientAbort = new AbortController();
    const breakAttached = await createAttachedRun(sessionId, runId, {
      cancelRun: async () => undefined,
      clientSignal: breakClientAbort.signal,
      features: async () => new Set([watchFeature]),
      invalidateCompatibility: () => undefined,
      transportKind: "grpc",
      watch: (_session, _run, _cursor, signal) =>
        (async function* () {
          breakAttempts += 1;
          signal.addEventListener("abort", () => {
            breakReleases += 1;
          });
          yield boundary("");
          yield event("break-1", "unread");
        })(),
    });
    for await (const _envelope of breakAttached) break;
    await spinUntil(() => breakReleases === 1);
    expect(breakAttempts).toBe(1);
    expect(vi.getTimerCount()).toBe(0);
  });
});
