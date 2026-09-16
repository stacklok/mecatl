import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  InvalidStateError,
  ProtocolError,
  SessionBusyError,
  TransportError,
} from "../src/index.js";

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

describe("run choreography", () => {
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
