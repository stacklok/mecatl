import { describe, expect, it } from "vitest";

import {
  InvalidStateError,
  SESSION_ID_HEADER_NAME,
  ServerError,
  UnsupportedFeatureError,
} from "../src/index.js";
import {
  ask,
  authorization,
  authorizationId,
  continuation,
  continuationRunId,
  flush,
  harness,
  httpAsk,
  httpContinuation,
  httpHarness,
  result,
  retract,
  sessionId,
} from "./mcp-authorization-control-fixture.js";

describe("MCP authorization continuation controls", () => {
  it("MCP authorization permission decisions and request options remain application owned", async () => {
    const automatic = await harness({
      streams: [{ events: continuation(ask("ask-auto"), result()) }],
    });
    const seen: string[] = [];
    const automaticController = new AbortController();
    const automaticHeaders: string[] = [];
    const automaticTrailers: string[] = [];
    const flow = automatic.session.mcpAuthorization(authorizationId).recheck(
      {
        onPermissionAsk: async (permission, signal) => {
          expect(signal.aborted).toBe(false);
          seen.push(permission.askId);
          return "allow_once" as const;
        },
        permissionRequestOptions: {
          headers: { "x-authority": "automatic" },
          onHeader: (headers) => automaticHeaders.push(headers.get("x-fixture-response") ?? ""),
          onTrailer: (headers) => automaticTrailers.push(headers.get("x-fixture-trailer") ?? ""),
          signal: automaticController.signal,
          timeoutMs: 91,
        },
      },
      { headers: { "x-authority": "stream" }, timeoutMs: 90 },
    );
    const automaticIterator = flow[Symbol.asyncIterator]();
    await automaticIterator.next();
    await automaticIterator.next();
    await automaticIterator.next();
    await flush();
    const automaticCall = automatic.transport.calls.find((call) => call.method === "ResolveRunAsk");
    expect(seen).toEqual(["ask-auto"]);
    expect(automaticCall).toMatchObject({
      input: { askId: "ask-auto", expectedRunId: continuationRunId, sessionId },
      timeoutMs: 91,
    });
    expect(automaticCall?.headers.get("x-authority")).toBe("automatic");
    expect(automaticHeaders).toEqual(["ResolveRunAsk"]);
    expect(automaticTrailers).toEqual(["ResolveRunAsk"]);
    automaticController.abort(new Error("caller finished"));
    expect(automaticCall?.signal?.aborted).toBe(true);

    const manual = await harness({
      streams: [
        {
          events: continuation(
            ask("ask-manual"),
            ask("ask-retracted"),
            retract("ask-retracted"),
            result(),
          ),
        },
      ],
    });
    const manualFlow = manual.session.mcpAuthorization(authorizationId).recheck();
    const iter = manualFlow[Symbol.asyncIterator]();
    await iter.next();
    await iter.next();
    await iter.next();
    await expect(
      manualFlow.resolveAsk("ask-manual", "deny", {
        headers: { "x-authority": "manual" },
        timeoutMs: 92,
      }),
    ).resolves.toBeUndefined();
    const manualCall = manual.transport.calls.find((call) => call.method === "ResolveRunAsk");
    expect(manualCall?.headers.get("x-authority")).toBe("manual");
    const resolvedCallCount = manual.transport.calls.filter(
      (call) => call.method === "ResolveRunAsk",
    ).length;
    await expect(manualFlow.resolveAsk("ask-manual", "allow_once")).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    expect(manual.transport.calls.filter((call) => call.method === "ResolveRunAsk")).toHaveLength(
      resolvedCallCount,
    );
    await iter.next();
    await iter.next();
    await expect(manualFlow.resolveAsk("ask-retracted", "deny")).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    await expect(manualFlow.resolveAsk("unknown", "deny")).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    await automatic.client.close();
    await manual.client.close();
  });

  it("MCP authorization retirement suppresses an admitted automatic verdict", async () => {
    let releaseDispatch: () => void = () => undefined;
    const dispatchGate = new Promise<void>((resolve) => {
      releaseDispatch = resolve;
    });
    let markSetup: () => void = () => undefined;
    const setup = new Promise<void>((resolve) => {
      markSetup = resolve;
    });
    let admittedSignal: AbortSignal | undefined;
    const instance = await harness({
      beforeUnaryDispatch: async (method, _input, signal) => {
        if (method !== "ResolveRunAsk") return;
        admittedSignal = signal;
        markSetup();
        await dispatchGate;
      },
      streams: [{ events: continuation(ask("ask-retired"), retract("ask-retired"), result()) }],
    });
    const flow = instance.session.mcpAuthorization(authorizationId).recheck({
      onPermissionAsk: () => "allow_once",
    });
    const iterator = flow[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();
    await iterator.next();
    await setup;
    expect(admittedSignal?.aborted).toBe(false);

    await iterator.next();
    expect(admittedSignal?.aborted).toBe(true);
    releaseDispatch();
    await flush();
    expect(instance.transport.calls.filter((call) => call.method === "ResolveRunAsk")).toHaveLength(
      0,
    );
    await expect(iterator.next()).resolves.toMatchObject({
      done: false,
      value: { kind: "result" },
    });
    await expect(iterator.next()).resolves.toEqual({ done: true, value: undefined });
    await instance.client.close();
  });

  it("MCP authorization continuation controls are exact run and feature gated", async () => {
    const active = await harness({ streams: [{ events: continuation(result()) }] });
    const flow = active.session.mcpAuthorization(authorizationId).recheck();
    await expect(flow.cancelContinuation()).rejects.toBeInstanceOf(InvalidStateError);
    const iterator = flow[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();
    await expect(
      flow.cancelContinuation({ headers: { "x-control": "cancel" } }),
    ).resolves.toBeUndefined();
    const cancel = active.transport.calls.find((call) => call.method === "CancelRun");
    expect(cancel?.input).toEqual({ expectedRunId: continuationRunId, sessionId });

    const unsupported = await harness({
      features: ["watch_session_events"],
      streams: [
        { events: [authorization("authorization.resolved", "granted")] },
        { events: continuation(result()) },
      ],
    });
    await expect(
      unsupported.session.mcpAuthorization(authorizationId).recheck().result(),
    ).resolves.toMatchObject({ outcome: "settled" });
    const unsupportedFlow = unsupported.session.mcpAuthorization(authorizationId).recheck();
    const unsupportedIterator = unsupportedFlow[Symbol.asyncIterator]();
    await unsupportedIterator.next();
    await unsupportedIterator.next();
    await expect(unsupportedFlow.cancelContinuation()).rejects.toBeInstanceOf(
      UnsupportedFeatureError,
    );
    expect(unsupported.transport.calls.some((call) => call.method === "CancelRun")).toBe(false);
    await active.client.close();
    await unsupported.client.close();
  });

  it("MCP authorization request options and unsupported plan asks stay separated", async () => {
    const serverFailure = new ServerError("control failed", {
      code: "not_found",
      transport: "grpc",
    });
    const instance = await harness({
      streams: [
        {
          events: continuation(
            ask("ask-manual"),
            ask("ask-plan", { tool: "PresentPlan" }),
            result(),
          ),
        },
      ],
      unary: (method) => (method === "ResolveRunAsk" ? serverFailure : undefined),
    });
    let responderCalls = 0;
    const flow = instance.session.mcpAuthorization(authorizationId).recheck(
      {
        onPermissionAsk: () => {
          responderCalls += 1;
          return undefined;
        },
        permissionRequestOptions: { headers: { "x-option": "auto" } },
      },
      { headers: { "x-option": "stream" }, timeoutMs: 101 },
    );
    const iterator = flow[Symbol.asyncIterator]();
    await iterator.next();
    await iterator.next();
    await iterator.next();
    await expect(
      flow.resolveAsk("ask-manual", "deny", { headers: { "x-option": "manual" }, timeoutMs: 102 }),
    ).rejects.toBeInstanceOf(ServerError);
    await iterator.next();
    await flush();
    expect(responderCalls).toBe(1);
    expect(
      instance.transport.calls
        .find((call) => call.method === "RecheckMcpAuthorization")
        ?.headers.get("x-option"),
    ).toBe("stream");
    const control = instance.transport.calls.find((call) => call.method === "ResolveRunAsk");
    expect(control?.headers.get("x-option")).toBe("manual");
    expect(control?.headers.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
    expect(control?.timeoutMs).toBe(102);
    await expect(flow.resolveAsk("ask-plan", "deny")).rejects.toBeInstanceOf(InvalidStateError);
    expect(instance.transport.calls.filter((call) => call.method === "ResolveRunAsk")).toHaveLength(
      1,
    );

    const http = await httpHarness(httpContinuation(httpAsk("ask-http")));
    const httpFlow = http.session
      .mcpAuthorization(authorizationId)
      .recheck(undefined, { headers: { "x-option": "http-stream" }, timeoutMs: 103 });
    const httpIterator = httpFlow[Symbol.asyncIterator]();
    await httpIterator.next();
    await httpIterator.next();
    await httpIterator.next();
    await httpFlow.resolveAsk("ask-http", "allow_always", {
      headers: { "x-option": "http-control" },
      timeoutMs: 104,
    });
    expect(
      http.requests.find((request) => request.path.endsWith("/recheck"))?.headers.get("x-option"),
    ).toBe("http-stream");
    expect(
      http.requests
        .find((request) => request.path.endsWith("/resolve-ask"))
        ?.headers.get("x-option"),
    ).toBe("http-control");
    expect(
      http.requests
        .find((request) => request.path.endsWith("/resolve-ask"))
        ?.headers.get(SESSION_ID_HEADER_NAME),
    ).toBe(sessionId);
    await http.client.close();

    const automaticFailure = await harness({
      streams: [{ events: continuation(ask("ask-failing")), hold: true }],
      unary: (method) => (method === "ResolveRunAsk" ? serverFailure : undefined),
    });
    const failingFlow = automaticFailure.session.mcpAuthorization(authorizationId).recheck({
      onPermissionAsk: () => "allow_once",
      permissionRequestOptions: { headers: { "x-option": "automatic-failure" } },
    });
    const failingIterator = failingFlow[Symbol.asyncIterator]();
    await failingIterator.next();
    await failingIterator.next();
    await failingIterator.next();
    await expect(failingIterator.next()).rejects.toBeInstanceOf(ServerError);
    expect(
      automaticFailure.transport.calls.filter((call) => call.method === "ResolveRunAsk"),
    ).toHaveLength(1);
    expect(
      automaticFailure.transport.calls
        .find((call) => call.method === "ResolveRunAsk")
        ?.headers.get("x-option"),
    ).toBe("automatic-failure");
    await automaticFailure.client.close();
    await instance.client.close();
  });
});
