import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { EventSchema, HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  InvalidStateError,
  ProtocolError,
  RunAuthorizationRequiredError,
  SessionBusyError,
  TransportError,
} from "../src/index.js";
import { RunImpl } from "../src/run.js";

function deferred<T = void>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((settle) => {
    resolve = settle;
  });
  return { promise, resolve };
}

function event(runId: string, type: string, text = "") {
  return { event: { runId, text, type } };
}

function terminal(runId: string, stop = "end_turn", text = "done") {
  return {
    event: {
      result: {
        stop,
        text,
        usage: { inputTokens: 7n, outputTokens: 3n },
      },
      runId,
      type: "result",
    },
  };
}

function authorizationRequired(
  runId: string,
  authorizationId = "authorization-1",
  callId = "call-1",
) {
  return {
    event: {
      authorization: {
        authorizationId,
        callId,
        displayName: "Example MCP",
        status: "pending",
      },
      runId,
      type: "authorization.required",
    },
  };
}

describe("run choreography", () => {
  it("Run outcome discriminates completion from authorization parking", async () => {
    let sequence = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-outcome" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          sequence += 1;
          const runId = `run-${sequence}`;
          if (sequence === 1) {
            yield event(runId, "message.delta", "hello");
            yield terminal(runId);
            return;
          }
          yield authorizationRequired(runId);
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    const completed = await (await session.run("complete")).outcome();
    expect(completed).toMatchObject({
      outcome: "completed",
      result: { runId: "run-1", sessionId: "session-outcome", text: "done" },
    });

    const parked = await (await session.run("authorize")).outcome();
    expect(parked).toMatchObject({
      authorization: {
        kind: "authorization.required",
        payload: {
          authorizationId: "authorization-1",
          callId: "call-1",
          status: "pending",
        },
      },
      outcome: "authorization_required",
      runId: "run-2",
      sessionId: "session-outcome",
    });
    await client.close();
  });

  it("authorization parked Run iteration and result use normal handoff semantics", async () => {
    let sequence = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-handoff" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          sequence += 1;
          yield authorizationRequired(
            `run-${sequence}`,
            `authorization-${sequence}`,
            `call-${sequence}`,
          );
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    const iterated = await session.run("iterate");
    const events = [];
    for await (const value of iterated) events.push(value);
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({
      kind: "authorization.required",
      payload: { authorizationId: "authorization-1", callId: "call-1", status: "pending" },
    });

    const drained = await session.run("result");
    let failure: unknown;
    try {
      await drained.result();
    } catch (error) {
      failure = error;
    }
    expect(failure).toBeInstanceOf(RunAuthorizationRequiredError);
    expect(failure).not.toBeInstanceOf(ProtocolError);
    expect((failure as RunAuthorizationRequiredError).outcome).toMatchObject({
      authorization: {
        payload: { authorizationId: "authorization-2", callId: "call-2", status: "pending" },
      },
      outcome: "authorization_required",
      runId: "run-2",
      sessionId: "session-handoff",
    });
    await client.close();
  });

  it("authorization parked Run releases SDK ownership without cancelling authorization", async () => {
    const requestEnds: Array<Promise<IteratorResult<unknown>>> = [];
    const responseClosed = new Set<string>();
    let sequence = 0;
    const never = new Promise<void>(() => undefined);
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-release" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          const first = await input.next();
          sequence += 1;
          const runId = `run-${sequence}`;
          requestEnds.push(input.next());
          try {
            yield authorizationRequired(runId, `authorization-${sequence}`, `call-${sequence}`);
            if (first.value?.kind.case === "prompt" && first.value.kind.value.text === "return") {
              await never;
            }
          } finally {
            responseClosed.add(runId);
          }
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    const iteratedToEof = await session.run("eof");
    for await (const _event of iteratedToEof) {
      // Drain through authorization EOF.
    }
    expect(responseClosed.has("run-1")).toBe(true);

    await (await session.run("outcome")).outcome();
    expect(responseClosed.has("run-2")).toBe(true);

    await expect((await session.run("result")).result()).rejects.toBeInstanceOf(
      RunAuthorizationRequiredError,
    );
    expect(responseClosed.has("run-3")).toBe(true);

    const returned = await session.run("return");
    const iterator = returned[Symbol.asyncIterator]();
    await expect(iterator.next()).resolves.toMatchObject({
      done: false,
      value: { kind: "authorization.required" },
    });
    await expect(iterator.return?.()).resolves.toMatchObject({ done: true });

    const authorization = session.mcpAuthorization("authorization-4");
    expect(authorization).toMatchObject({
      authorizationId: "authorization-4",
      sessionId: "session-release",
    });
    expect(sequence).toBe(4);

    const afterPark = await session.run("after park");
    await afterPark.outcome();
    expect(responseClosed.has("run-5")).toBe(true);

    const requestResults = await Promise.all(requestEnds);
    expect(requestResults).toHaveLength(5);
    expect(requestResults.every((result) => result.done === true)).toBe(true);
    await client.close();

    let responseIteratorReturns = 0;
    const direct = new RunImpl(
      "session-direct",
      "run-direct",
      create(EventSchema, authorizationRequired("run-direct").event),
      {
        next: async () => ({ done: true, value: undefined }),
        return: async () => {
          responseIteratorReturns += 1;
          return { done: true, value: undefined };
        },
      },
      {
        assertOpen: () => undefined,
        send: () => undefined,
        transportKind: "grpc",
      },
    );
    const directIterator = direct[Symbol.asyncIterator]();
    await directIterator.next();
    await directIterator.return?.();
    expect(responseIteratorReturns).toBe(1);
  });

  it("Run outcome preserves completed and malformed stream behavior", async () => {
    let sequence = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-contract" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          sequence += 1;
          const runId = `run-${sequence}`;
          if (sequence === 1) {
            yield terminal(runId, "end_turn", "still completed");
            return;
          }
          if (sequence === 2) {
            yield event(runId, "message.delta", "truncated");
            return;
          }
          if (sequence === 3) {
            yield {
              event: {
                authorization: { authorizationId: "", callId: "", status: "granted" },
                runId,
                type: "authorization.required",
              },
            };
            return;
          }
          yield terminal(runId);
          if (sequence === 4) yield terminal(runId, "error", "duplicate");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    const resultRun = await session.run("result");
    await expect(resultRun.result()).resolves.toMatchObject({
      runId: "run-1",
      stopReason: "end_turn",
      text: "still completed",
    });
    await expect(resultRun.outcome()).rejects.toBeInstanceOf(InvalidStateError);
    expect(() => resultRun[Symbol.asyncIterator]()).toThrow(InvalidStateError);

    const truncated = await session.run("truncated");
    await expect(truncated.outcome()).rejects.toBeInstanceOf(ProtocolError);
    await expect(truncated.result()).rejects.toBeInstanceOf(InvalidStateError);

    await expect(session.run("malformed authorization")).rejects.toBeInstanceOf(ProtocolError);

    const duplicate = await session.run("duplicate terminal");
    await expect(duplicate.outcome()).rejects.toBeInstanceOf(ProtocolError);

    const outcomeRun = await session.run("outcome claim");
    await expect(outcomeRun.outcome()).resolves.toMatchObject({ outcome: "completed" });
    await expect(outcomeRun.result()).rejects.toBeInstanceOf(InvalidStateError);
    expect(() => outcomeRun[Symbol.asyncIterator]()).toThrow(InvalidStateError);
    await client.close();
  });

  it("run resolves on acceptance with the first run ID", async () => {
    const accepted = deferred();
    const releaseEvent = deferred();
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-1" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests) {
          const first = await requests[Symbol.asyncIterator]().next();
          expect(first.value?.kind.case).toBe("prompt");
          accepted.resolve();
          await releaseEvent.promise;
          yield event("run-1", "message.delta", "hello");
          yield terminal("run-1");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    let resolved = false;
    const pending = session.run("hello").then((run) => {
      resolved = true;
      return run;
    });

    await accepted.promise;
    await Promise.resolve();
    expect(resolved).toBe(false);
    releaseEvent.resolve();

    const run = await pending;
    expect(run.id).toBe("run-1");
    expect((await run.result()).runId).toBe("run-1");
    await client.close();
  });

  it("a run has exactly one consumption mode", async () => {
    let sequence = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-1" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          sequence += 1;
          const runId = `run-${sequence}`;
          yield event(runId, "message.delta", "chunk");
          yield terminal(runId);
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    const iterated = await session.run("iterate");
    const kinds: string[] = [];
    for await (const value of iterated) kinds.push(value.kind);
    expect(kinds).toEqual(["message.delta", "result"]);
    await expect(iterated.result()).rejects.toBeInstanceOf(InvalidStateError);
    expect(() => iterated[Symbol.asyncIterator]()).toThrow(InvalidStateError);

    const drained = await session.run("result");
    await expect(drained.result()).resolves.toMatchObject({ stopReason: "end_turn" });
    await expect(drained.result()).rejects.toBeInstanceOf(InvalidStateError);
    expect(() => drained[Symbol.asyncIterator]()).toThrow(InvalidStateError);
    await client.close();
  });

  it("server terminals resolve typed, transport failures throw", async () => {
    const stops = ["error", "cancelled", "max_turns", "max_tool_calls", "budget"];
    let sequence = 0;
    const terminalTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-terminals" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          const stop = stops[sequence] ?? "end_turn";
          sequence += 1;
          yield event(`run-${sequence}`, "message.delta", "partial");
          yield terminal(`run-${sequence}`, stop, `${stop} text`);
        },
      });
    });
    const client = connect({ transport: terminalTransport });
    const session = await client.sessions.create({});
    for (const stop of stops) {
      const outcome = await (await session.run(stop)).result();
      expect(outcome).toMatchObject({
        content: `${stop} text`,
        runId: `run-${sequence}`,
        sessionId: "session-terminals",
        stopReason: stop,
        text: `${stop} text`,
      });
      expect(outcome.usage).toMatchObject({ inputTokens: 7n, outputTokens: 3n });
      expect(outcome.rawEvent.kind).toBe("result");
    }
    await client.close();

    const failureTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-failure" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          yield event("run-failure", "message.delta");
          throw new ConnectError("connection lost", Code.Unavailable);
        },
      });
    });
    const failureClient = connect({ transport: failureTransport });
    const failureSession = await failureClient.sessions.create({});
    await expect((await failureSession.run("fail")).result()).rejects.toBeInstanceOf(
      TransportError,
    );
    await failureClient.close();

    const protocolTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-protocol" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* () {
          yield event("run-protocol", "message.delta");
        },
      });
    });
    const protocolClient = connect({ transport: protocolTransport });
    const protocolSession = await protocolClient.sessions.create({});
    await expect((await protocolSession.run("fail")).result()).rejects.toBeInstanceOf(
      ProtocolError,
    );
    await protocolClient.close();
  });

  it("a second local run is SessionBusyError", async () => {
    const finish = deferred();
    let prompts = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-1" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests) {
          await requests[Symbol.asyncIterator]().next();
          prompts += 1;
          yield event("run-busy", "message.delta");
          await finish.promise;
          yield terminal("run-busy");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("first");

    await expect(session.run("must not be sent")).rejects.toBeInstanceOf(SessionBusyError);
    expect(prompts).toBe(1);

    finish.resolve();
    await run.result();
    expect(prompts).toBe(1);
    await client.close();
  });

  it("cancel is a typed outcome", async () => {
    let expectedRunId = "";
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session-1" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield event("run-cancel", "message.delta");
          const control = await input.next();
          if (control.value?.kind.case === "cancel") {
            expectedRunId = control.value.kind.value.expectedRunId;
          }
          yield terminal("run-cancel", "cancelled", "partial answer");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const run = await session.run("start");
    await run.cancel();

    await expect(run.result()).resolves.toMatchObject({
      stopReason: "cancelled",
      text: "partial answer",
    });
    expect(expectedRunId).toBe("run-cancel");
    await client.close();
  });
});
