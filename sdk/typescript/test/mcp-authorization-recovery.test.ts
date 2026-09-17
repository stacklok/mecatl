import { describe, expect, it } from "vitest";

import { InvalidStateError, ProtocolError, ServerError } from "../src/index.js";
import {
  ask,
  authorization,
  authorizationId,
  continuation,
  continuationRunId,
  harness,
  notFound,
  result,
  sessionId,
} from "./mcp-authorization-control-fixture.js";

describe("MCP authorization recovery boundaries", () => {
  it("concurrent MCP authorization flows cannot cross consume or correlate", async () => {
    const instance = await harness({
      streams: [
        { events: continuation(ask("shared"), result()) },
        {
          events: [
            authorization("authorization.resolved", "granted", { authorizationId: "other" }),
          ],
        },
      ],
    });
    const first = instance.session.mcpAuthorization(authorizationId).recheck();
    const second = instance.session.mcpAuthorization(authorizationId).recheck();
    const firstIterator = first[Symbol.asyncIterator]();
    const secondIterator = second[Symbol.asyncIterator]();
    await firstIterator.next();
    await firstIterator.next();
    await firstIterator.next();
    await expect(secondIterator.next()).rejects.toBeInstanceOf(ProtocolError);
    await expect(second.resolveAsk("shared", "deny")).rejects.toBeInstanceOf(InvalidStateError);
    await expect(first.resolveAsk("shared", "deny")).resolves.toBeUndefined();
    expect(instance.transport.calls.find((call) => call.method === "ResolveRunAsk")?.input).toEqual(
      {
        askId: "shared",
        expectedRunId: continuationRunId,
        sessionId,
        verdict: 1,
      },
    );
    await instance.client.close();
  });

  it("MCP authorization flow cancellation releases only SDK owned resources", async () => {
    let markControlStarted: () => void = () => undefined;
    const controlStarted = new Promise<void>((resolve) => {
      markControlStarted = resolve;
    });
    let markControlAborted: () => void = () => undefined;
    const controlAborted = new Promise<void>((resolve) => {
      markControlAborted = resolve;
    });
    const returned = await harness({
      streams: [{ events: continuation(ask("ask-auto")), hold: true }],
      unary: (method, _input, signal) => {
        if (method !== "ResolveRunAsk") return undefined;
        markControlStarted();
        return new Promise<never>((_resolve, reject) => {
          const abort = () => {
            markControlAborted();
            reject(signal?.reason ?? new Error("automatic control aborted"));
          };
          if (signal?.aborted === true) abort();
          else signal?.addEventListener("abort", abort, { once: true });
        });
      },
    });
    const permissionController = new AbortController();
    const streamController = new AbortController();
    const returnedFlow = returned.session.mcpAuthorization(authorizationId).recheck(
      {
        onPermissionAsk: () => "allow_once",
        permissionRequestOptions: {
          headers: { "x-lifetime": "automatic-control" },
          signal: permissionController.signal,
          timeoutMs: 501,
        },
      },
      {
        headers: { "x-lifetime": "flow-stream" },
        signal: streamController.signal,
        timeoutMs: 500,
      },
    );
    const iterator = returnedFlow[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();
    await iterator.next();
    await controlStarted;
    const streamCall = returned.transport.calls.find(
      (call) => call.method === "RecheckMcpAuthorization",
    );
    const controlCall = returned.transport.calls.find((call) => call.method === "ResolveRunAsk");
    expect(streamCall?.headers.get("x-lifetime")).toBe("flow-stream");
    expect(streamCall?.timeoutMs).toBe(500);
    expect(controlCall?.headers.get("x-lifetime")).toBe("automatic-control");
    expect(controlCall?.timeoutMs).toBe(501);
    expect(controlCall?.signal?.aborted).toBe(false);
    expect(controlCall?.signal).not.toBe(streamCall?.signal);

    const returnedResult = iterator.return?.();
    const controlAbortedWithFlow = controlCall?.signal?.aborted === true;
    const permissionCallerStayedLive = !permissionController.signal.aborted;
    if (!controlAbortedWithFlow) permissionController.abort(new Error("test cleanup"));
    await controlAborted;
    await returnedResult;
    expect(controlAbortedWithFlow).toBe(true);
    expect(permissionCallerStayedLive).toBe(true);
    expect(streamController.signal.aborted).toBe(false);
    expect(returned.transport.activeStreams).toBe(0);
    expect(returned.transport.closedStreams).toBe(1);
    expect(
      returned.transport.calls.filter((call) => call.method === "RecheckMcpAuthorization"),
    ).toHaveLength(1);
    expect(returned.transport.calls.some((call) => call.method === "CancelRun")).toBe(false);

    const lost = await harness({ streams: [{ error: new Error("wire lost") }] });
    await expect(
      lost.session.mcpAuthorization(authorizationId).recheck().result(),
    ).rejects.toMatchObject({ code: "transport" });
    expect(
      lost.transport.calls.filter((call) => call.method === "RecheckMcpAuthorization"),
    ).toHaveLength(1);

    const closed = await harness({ streams: [{ events: continuation(), hold: true }] });
    const closeResult = closed.session.mcpAuthorization(authorizationId).recheck().result();
    await Promise.resolve();
    await closed.client.close();
    await expect(closeResult).rejects.toBeDefined();
    expect(closed.transport.activeStreams).toBe(0);
    await returned.client.close();
    await lost.client.close();
  });

  it("MCP authorization recovery never overpromises replay", async () => {
    let rechecks = 0;
    const instance = await harness({
      streams: [
        {
          events: [authorization("authorization.resolved", "granted")],
          error: new Error("response lost"),
        },
        { error: notFound() },
      ],
    });
    await expect(
      instance.session.mcpAuthorization(authorizationId).recheck().result(),
    ).rejects.toMatchObject({ code: "transport" });
    const failure = await instance.session
      .mcpAuthorization(authorizationId)
      .recheck()
      .result()
      .catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(ServerError);
    expect(failure).toMatchObject({ code: "not_found" });
    rechecks = instance.transport.calls.filter(
      (call) => call.method === "RecheckMcpAuthorization",
    ).length;
    expect(rechecks).toBe(2);
    await instance.client.close();
  });

  it("MCP authorization exposes correlation without automatic durable recovery", async () => {
    const instance = await harness({ streams: [{ events: continuation(), hold: true }] });
    const flow = instance.session.mcpAuthorization(authorizationId).recheck();
    const iterator = flow[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();
    expect(flow.continuationRunId).toBe(continuationRunId);
    expect(instance.transport.calls.some((call) => call.method === "WatchSessionEvents")).toBe(
      false,
    );
    const attached = await instance.session.attach(flow.continuationRunId);
    expect(attached.runId).toBe(continuationRunId);
    await attached.close();
    expect(instance.transport.calls.some((call) => call.method === "WatchSessionEvents")).toBe(
      false,
    );
    await iterator.return?.();
    await instance.client.close();
  });
});
