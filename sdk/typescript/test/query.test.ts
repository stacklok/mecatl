import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import { connectTransport } from "../src/client.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import type { Client, Session } from "../src/index.js";
import {
  type DiagnosticRecord,
  MECATL_EVENT_KINDS,
  MecatlError,
  UnsupportedFeatureError,
} from "../src/index.js";
import { queryInternal } from "../src/query.js";

function event(runId: string, type = "message.delta", text = "hello") {
  return { event: { runId, text, type } };
}

function ask(runId: string, askId = "ask-1") {
  return {
    event: {
      ask: { args: "{}", askId, reason: "test policy", tool: "Bash" },
      runId,
      type: "permission.ask",
    },
  };
}

function terminal(runId: string, stop = "end_turn") {
  return { event: { result: { stop, text: "done" }, runId, type: "result" } };
}

interface QueryHarnessOptions {
  ask?: boolean;
  diagnostics?: (record: DiagnosticRecord) => void;
  waitForCancel?: boolean;
}

function queryHarness(options: QueryHarnessOptions = {}) {
  const calls: string[] = [];
  const controls: Array<{ askId: string; case: string; verdict: number }> = [];
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      createSession: () => {
        calls.push("create");
        return { sessionId: "query-session" };
      },
      deleteSession: (request) => {
        calls.push(`delete:${request.sessionId}`);
        return {};
      },
      getCompatibilityInfo: () => ({ apiMajor: 1 }),
      getSession: (request) => ({ session: { sessionId: request.sessionId } }),
      converse: async function* (requests) {
        const input = requests[Symbol.asyncIterator]();
        const prompt = await input.next();
        calls.push(`prompt:${prompt.value?.kind.case ?? "missing"}`);
        const runId = "query-run";
        if (options.ask === true) {
          yield ask(runId);
          const response = await input.next();
          if (response.value?.kind.case === "resumeApproval") {
            controls.push({
              askId: response.value.kind.value.askId,
              case: response.value.kind.case,
              verdict: response.value.kind.value.verdict,
            });
          }
          yield event(runId, "message.delta", "continued after denial");
        } else {
          yield event(runId);
          if (options.waitForCancel === true) {
            const response = await input.next();
            calls.push(`control:${response.value?.kind.case ?? "missing"}`);
            yield terminal(runId, "cancelled");
            return;
          }
        }
        yield terminal(runId);
      },
    });
  }) as Transport & AsyncDisposable;
  transport[Symbol.asyncDispose] = async () => {
    calls.push("transport:close");
  };

  const client = connectTransport({
    ...(options.diagnostics === undefined ? {} : { diagnostics: options.diagnostics }),
    internal: {
      daemon: {
        exit: new Promise<never>(() => undefined),
        removeRuntime: async () => {
          calls.push("runtime:remove");
        },
        stop: async () => {
          calls.push("daemon:stop");
        },
      },
    },
    owned: true,
    transport,
    transportKind: "grpc",
    visibility: false,
  });
  const spawn = vi.fn(async () => client);
  return { calls, client, controls, spawn };
}

async function consume(query: AsyncIterable<{ kind: string }>): Promise<string[]> {
  const kinds: string[] = [];
  for await (const value of query) kinds.push(value.kind);
  return kinds;
}

function fakeClient(create: () => Promise<Session>, close: () => Promise<void>): Client {
  return {
    agents: undefined as never,
    close,
    commands: undefined as never,
    mcp: undefined as never,
    models: undefined as never,
    sessions: {
      create,
      fork: vi.fn(),
      get: vi.fn(),
    },
    status: {
      getSnapshot: () => "online",
      subscribe: () => () => undefined,
    },
    worktrees: undefined as never,
    [Symbol.asyncDispose]: close,
  };
}

describe("query one-shot lifecycle", () => {
  it("query spawns runs and yields the ordinary event union", async () => {
    const harness = queryHarness();
    const query = await queryInternal("hello", {}, { spawn: harness.spawn });

    const kinds = await consume(query);

    expect(harness.spawn).toHaveBeenCalledOnce();
    expect(harness.calls).toContain("create");
    expect(harness.calls).toContain("prompt:prompt");
    expect(kinds).toEqual(["message.delta", "result"]);
    expect(kinds.every((kind) => MECATL_EVENT_KINDS.includes(kind as never))).toBe(true);
  });

  it("query deletes its transient session and stops its daemon", async () => {
    const harness = queryHarness();
    const query = await queryInternal("hello", {}, { spawn: harness.spawn });

    await consume(query);

    expect(harness.calls).toEqual([
      "create",
      "prompt:prompt",
      "delete:query-session",
      "transport:close",
      "daemon:stop",
      "runtime:remove",
    ]);
  });

  it("retainSession keeps the session for the daemon's lifetime and reports its id", async () => {
    const harness = queryHarness();
    const query = await queryInternal("hello", {
      client: harness.client,
      retainSession: true,
    });

    expect(query.sessionId).toBe("query-session");
    await consume(query);

    expect(harness.calls).not.toContain("delete:query-session");
    await expect(harness.client.sessions.get(query.sessionId)).resolves.toMatchObject({
      id: "query-session",
    });
    expect(harness.calls).not.toContain("daemon:stop");
    await harness.client.close();
  });

  it("query closes only the resources it created", async () => {
    const harness = queryHarness();
    const query = await queryInternal("hello", { client: harness.client });

    await consume(query);

    expect(harness.calls).toContain("delete:query-session");
    expect(harness.calls).not.toContain("transport:close");
    expect(harness.calls).not.toContain("daemon:stop");
    await harness.client.close();
  });

  it("an ask without a responder is denied and the run continues", async () => {
    const harness = queryHarness({ ask: true });
    const query = await queryInternal("hello", {}, { spawn: harness.spawn });

    const kinds = await consume(query);

    expect(harness.controls).toEqual([{ askId: "ask-1", case: "resumeApproval", verdict: 1 }]);
    expect(kinds).toEqual(["permission.ask", "message.delta", "result"]);
  });

  it("the responder-less denial emits one diagnostic and no event", async () => {
    const diagnostics: DiagnosticRecord[] = [];
    const harness = queryHarness({ ask: true, diagnostics: (record) => diagnostics.push(record) });
    const query = await queryInternal("hello", {}, { spawn: harness.spawn });

    const kinds = await consume(query);

    expect(diagnostics).toEqual([
      {
        code: "query_permission_ask_denied",
        fields: { askId: "ask-1", tool: "Bash" },
        level: "info",
        message: "query() denied permission ask ask-1 for Bash because no responder was provided",
      },
    ]);
    expect(kinds).toEqual(["permission.ask", "message.delta", "result"]);
  });

  it("plan mode is refused before spawning", async () => {
    const spawn = vi.fn();
    const failure = queryInternal("plan it", { session: { mode: 2 } }, { spawn });

    await expect(failure).rejects.toMatchObject({
      code: "unsupported_feature",
      feature: "session.resolvePlan()",
    });
    await expect(failure).rejects.toBeInstanceOf(UnsupportedFeatureError);
    expect(spawn).not.toHaveBeenCalled();
  });

  it("an aborted or abandoned query still cleans up", async () => {
    const abandoned = queryHarness({ waitForCancel: true });
    const abandonedQuery = await queryInternal("hello", {}, { spawn: abandoned.spawn });
    for await (const _event of abandonedQuery) break;

    expect(abandoned.calls).toContain("control:cancel");
    expect(abandoned.calls).toContain("delete:query-session");
    expect(abandoned.calls).toContain("daemon:stop");

    const controller = new AbortController();
    const aborted = queryHarness({ waitForCancel: true });
    const abortedQuery = await queryInternal(
      "hello",
      { signal: controller.signal },
      { spawn: aborted.spawn },
    );
    controller.abort();
    await abortedQuery[Symbol.asyncIterator]().next();

    expect(aborted.calls).toContain("control:cancel");
    expect(aborted.calls).toContain("delete:query-session");
    expect(aborted.calls).toContain("daemon:stop");
  });

  it("a mid-setup failure unwinds what query already created", async () => {
    const spawnFailure = new MecatlError("spawn failed", {
      code: "spawn_failed",
      transport: "local",
    });
    await expect(
      queryInternal("hello", {}, { spawn: vi.fn(async () => Promise.reject(spawnFailure)) }),
    ).rejects.toBe(spawnFailure);

    const createFailure = new MecatlError("create failed", {
      code: "internal",
      transport: "grpc",
    });
    const closeAfterCreate = vi.fn(async () => undefined);
    const createClient = fakeClient(
      vi.fn(async () => Promise.reject(createFailure)),
      closeAfterCreate,
    );
    await expect(
      queryInternal("hello", {}, { spawn: vi.fn(async () => createClient) }),
    ).rejects.toBe(createFailure);
    expect(closeAfterCreate).toHaveBeenCalledOnce();

    const runFailure = new MecatlError("run failed", {
      code: "internal",
      transport: "grpc",
    });
    const deleteAfterRun = vi.fn(async () => undefined);
    const runSession = {
      delete: deleteAfterRun,
      id: "mid-setup-session",
      run: vi.fn(async () => Promise.reject(runFailure)),
    } as unknown as Session;
    const closeAfterRun = vi.fn(async () => undefined);
    const runClient = fakeClient(async () => runSession, closeAfterRun);
    await expect(queryInternal("hello", {}, { spawn: vi.fn(async () => runClient) })).rejects.toBe(
      runFailure,
    );
    expect(deleteAfterRun).toHaveBeenCalledOnce();
    expect(closeAfterRun).toHaveBeenCalledOnce();
  });

  it("the responder-less denial is query policy and never the run's", async () => {
    const controls: Array<{ askId: string; verdict: number }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "manual-session" }),
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield ask("manual-run", "manual-ask");
          const response = await input.next();
          if (response.value?.kind.case === "resumeApproval") {
            controls.push({
              askId: response.value.kind.value.askId,
              verdict: response.value.kind.value.verdict,
            });
          }
          yield terminal("manual-run");
        },
      });
    });
    const client = connectTransport({
      owned: false,
      transport,
      transportKind: "grpc",
      visibility: false,
    });
    const session = await client.sessions.create({});
    const run = await session.run("hello");
    const iterator = run[Symbol.asyncIterator]();

    await expect(iterator.next()).resolves.toMatchObject({
      value: { kind: "permission.ask", payload: { askId: "manual-ask" } },
    });
    expect(controls).toEqual([]);
    await run.resolveAsk("manual-ask", "deny");
    await iterator.next();

    expect(controls).toEqual([{ askId: "manual-ask", verdict: 1 }]);
    await client.close();
  });
});
