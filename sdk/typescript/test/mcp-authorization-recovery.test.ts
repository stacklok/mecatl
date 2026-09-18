import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";
import { InvalidStateError, ProtocolError, ServerError, TransportError } from "../src/index.js";
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
  afterEach(() => {
    vi.useRealTimers();
  });

  it("concurrent MCP authorization flows cannot cross consume or correlate", async () => {
    const peerAuthorizationId = "authorization-peer";
    const peerCallId = "authorization-call-peer";
    const peerRunId = "run-authorization-peer";
    const instance = await harness({
      streams: [
        {
          events: continuation(
            ask("shared"),
            authorization("authorization.required", "pending", {
              authorizationId: "authorization-chained",
              callId: "authorization-call-chained",
              runId: continuationRunId,
            }),
          ),
          hold: true,
        },
        {
          events: [
            authorization("authorization.resolved", "granted", {
              authorizationId: peerAuthorizationId,
              callId: peerCallId,
            }),
            authorization("authorization.resolved", "granted", {
              authorizationId: peerAuthorizationId,
              callId: peerCallId,
              runId: peerRunId,
            }),
            ask("shared", { runId: peerRunId }),
          ],
          hold: true,
        },
        {
          events: [
            authorization("authorization.resolved", "granted", { authorizationId: "other" }),
          ],
        },
      ],
    });
    const first = instance.session.mcpAuthorization(authorizationId).recheck();
    const second = instance.session.mcpAuthorization(peerAuthorizationId).recheck();
    const firstIterator = first[Symbol.asyncIterator]();
    const secondIterator = second[Symbol.asyncIterator]();

    const [firstStatus, secondStatus] = await Promise.all([
      firstIterator.next(),
      secondIterator.next(),
    ]);
    expect(firstStatus).toMatchObject({
      done: false,
      value: { payload: { authorizationId }, runId: "" },
    });
    expect(secondStatus).toMatchObject({
      done: false,
      value: { payload: { authorizationId: peerAuthorizationId }, runId: "" },
    });
    expect(instance.transport.maxActiveStreams).toBe(2);

    await firstIterator.next();
    await firstIterator.next();
    await secondIterator.next();
    await secondIterator.next();
    await expect(first.resolveAsk("shared", "deny")).resolves.toBeUndefined();
    await expect(second.resolveAsk("shared", "allow_once")).resolves.toBeUndefined();
    await expect(firstIterator.next()).resolves.toMatchObject({
      done: false,
      value: {
        payload: {
          authorizationId: "authorization-chained",
          callId: "authorization-call-chained",
        },
        runId: continuationRunId,
      },
    });
    expect(
      instance.transport.calls
        .filter((call) => call.method === "RecheckMcpAuthorization")
        .map((call) => call.input),
    ).toEqual([
      { authorizationId, sessionId },
      { authorizationId: peerAuthorizationId, sessionId },
    ]);
    expect(
      instance.transport.calls
        .filter((call) => call.method === "ResolveRunAsk")
        .map((call) => call.input),
    ).toEqual([
      {
        askId: "shared",
        expectedRunId: continuationRunId,
        sessionId,
        verdict: 1,
      },
      {
        askId: "shared",
        expectedRunId: peerRunId,
        sessionId,
        verdict: 2,
      },
    ]);
    await firstIterator.return?.();
    await secondIterator.return?.();

    const mismatched = instance.session.mcpAuthorization(authorizationId).recheck();
    await expect(mismatched.result()).rejects.toBeInstanceOf(ProtocolError);
    await instance.client.close();
  });

  it("MCP authorization flow cancellation releases only SDK owned resources", async () => {
    vi.useFakeTimers();

    async function pausedFlowLifetime(
      trigger: (context: {
        readonly controller: AbortController;
        readonly iterator: AsyncIterator<unknown>;
      }) => Promise<void>,
    ) {
      let releaseLateVerdict: (verdict: "deny") => void = () => undefined;
      const lateVerdict = new Promise<"deny">((resolve) => {
        releaseLateVerdict = resolve;
      });
      let lateResponderSignal: AbortSignal | undefined;
      let markControlStarted: () => void = () => undefined;
      const controlStarted = new Promise<void>((resolve) => {
        markControlStarted = resolve;
      });
      let markControlAborted: () => void = () => undefined;
      const controlAborted = new Promise<void>((resolve) => {
        markControlAborted = resolve;
      });
      const instance = await harness({
        streams: [{ events: continuation(ask("ask-late"), ask("ask-auto")), hold: true }],
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
      const flow = instance.session.mcpAuthorization(authorizationId).recheck(
        {
          onPermissionAsk: (permission, signal) => {
            if (permission.askId === "ask-late") {
              lateResponderSignal = signal;
              return lateVerdict;
            }
            return "allow_once";
          },
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
      const iterator = flow[Symbol.asyncIterator]();
      await iterator.next();
      await iterator.next();
      await iterator.next();
      await iterator.next();
      await controlStarted;
      const streamCall = instance.transport.calls.find(
        (call) => call.method === "RecheckMcpAuthorization",
      );
      const controlCall = instance.transport.calls.find((call) => call.method === "ResolveRunAsk");
      expect(streamCall?.headers.get("x-lifetime")).toBe("flow-stream");
      expect(streamCall?.timeoutMs).toBe(500);
      expect(controlCall?.headers.get("x-lifetime")).toBe("automatic-control");
      expect(controlCall?.timeoutMs).toBe(501);
      expect(controlCall?.signal?.aborted).toBe(false);
      expect(lateResponderSignal?.aborted).toBe(false);
      expect(controlCall?.signal).not.toBe(streamCall?.signal);

      await trigger({ controller: streamController, iterator });
      const controlAbortedWithFlow = controlCall?.signal?.aborted === true;
      const responderAbortedWithFlow = lateResponderSignal?.aborted === true;
      const permissionCallerStayedLive = !permissionController.signal.aborted;
      if (!controlAbortedWithFlow) permissionController.abort(new Error("test cleanup"));
      if (!responderAbortedWithFlow) await iterator.return?.();
      await controlAborted;
      releaseLateVerdict("deny");
      await Promise.resolve();
      await Promise.resolve();

      expect(controlAbortedWithFlow).toBe(true);
      expect(responderAbortedWithFlow).toBe(true);
      expect(permissionCallerStayedLive).toBe(true);
      expect(instance.transport.activeStreams).toBe(0);
      expect(instance.transport.closedStreams).toBe(1);
      expect(
        instance.transport.calls.filter((call) => call.method === "ResolveRunAsk"),
      ).toHaveLength(1);
      expect(
        instance.transport.calls.filter((call) => call.method === "RecheckMcpAuthorization"),
      ).toHaveLength(1);
      expect(instance.transport.calls.some((call) => call.method === "CancelRun")).toBe(false);
      await instance.client.close();
    }

    await pausedFlowLifetime(async ({ controller, iterator }) => {
      const reason = new Error("caller cancelled while consumption was paused");
      controller.abort(reason);
      await Promise.resolve();
      const failure = await iterator.next().catch((error: unknown) => error);
      expect(failure).toBeInstanceOf(TransportError);
      expect(failure).toMatchObject({
        cause: reason,
        code: "transport",
        transport: "grpc",
      });
      await expect(iterator.next()).resolves.toEqual({ done: true, value: undefined });
      expect(vi.getTimerCount()).toBe(0);
    });
    await pausedFlowLifetime(async ({ controller, iterator }) => {
      await vi.advanceTimersByTimeAsync(500);
      expect(controller.signal.aborted).toBe(false);
      const failure = await iterator.next().catch((error: unknown) => error);
      expect(failure).toBeInstanceOf(ServerError);
      expect(failure).toMatchObject({
        code: "unknown",
        status: Code.DeadlineExceeded,
        transport: "grpc",
      });
      await expect(iterator.next()).resolves.toEqual({ done: true, value: undefined });
      expect(vi.getTimerCount()).toBe(0);
    });
    await pausedFlowLifetime(async ({ controller, iterator }) => {
      const pendingRead = iterator.next();
      await Promise.resolve();
      const returned = iterator.return?.();
      await expect(returned).resolves.toEqual({ done: true, value: undefined });
      await expect(pendingRead).resolves.toEqual({ done: true, value: undefined });
      await expect(iterator.return?.()).resolves.toEqual({ done: true, value: undefined });
      await expect(iterator.next()).resolves.toEqual({ done: true, value: undefined });
      expect(controller.signal.aborted).toBe(false);
      expect(vi.getTimerCount()).toBe(0);
    });

    const supplied = new TransportError("caller-owned cancellation", { transport: "local" });
    await pausedFlowLifetime(async ({ controller, iterator }) => {
      controller.abort(supplied);
      await expect(iterator.next()).rejects.toBe(supplied);
      await expect(iterator.next()).resolves.toEqual({ done: true, value: undefined });
    });

    const lost = await harness({ streams: [{ error: new Error("wire lost") }] });
    await expect(
      lost.session.mcpAuthorization(authorizationId).recheck().result(),
    ).rejects.toMatchObject({ code: "transport" });
    expect(
      lost.transport.calls.filter((call) => call.method === "RecheckMcpAuthorization"),
    ).toHaveLength(1);

    const closed = await harness({ streams: [{ events: continuation(), hold: true }] });
    const closedFlow = closed.session.mcpAuthorization(authorizationId).recheck();
    const closeResult = closedFlow.result();
    await Promise.resolve();
    await closed.client.close();
    await expect(closeResult).rejects.toBeInstanceOf(InvalidStateError);
    const closedCalls = closed.transport.calls.length;
    await expect(closedFlow.cancelContinuation()).rejects.toBeInstanceOf(InvalidStateError);
    await expect(closedFlow.resolveAsk("unknown", "deny")).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    expect(closed.transport.calls).toHaveLength(closedCalls);
    expect(closed.transport.activeStreams).toBe(0);
    expect(closed.transport.closedStreams).toBe(1);

    const completed = await harness({ streams: [{ events: continuation(result()) }] });
    const completedFlow = completed.session.mcpAuthorization(authorizationId).recheck();
    await expect(completedFlow.result()).resolves.toMatchObject({
      continuationRunId,
      outcome: "completed",
    });
    const completedCalls = completed.transport.calls.length;
    await expect(completedFlow.cancelContinuation()).rejects.toBeInstanceOf(InvalidStateError);
    await expect(completedFlow.resolveAsk("unknown", "deny")).rejects.toBeInstanceOf(
      InvalidStateError,
    );
    expect(completed.transport.calls).toHaveLength(completedCalls);
    expect(completed.transport.closedStreams).toBe(1);

    const statusOnly = await harness({
      streams: [{ events: [authorization("authorization.required", "pending")] }],
    });
    const statusOnlyFlow = statusOnly.session.mcpAuthorization(authorizationId).recheck();
    const statusOnlyIterator = statusOnlyFlow[Symbol.asyncIterator]();
    await expect(statusOnlyIterator.next()).resolves.toMatchObject({
      done: false,
      value: { kind: "authorization.required" },
    });
    await expect(statusOnlyIterator.next()).resolves.toEqual({ done: true, value: undefined });
    await expect(statusOnlyIterator.next()).resolves.toEqual({ done: true, value: undefined });
    await expect(statusOnlyIterator.return?.()).resolves.toEqual({
      done: true,
      value: undefined,
    });

    let releaseManual: () => void = () => undefined;
    let manualStarted: () => void = () => undefined;
    const manualStart = new Promise<void>((resolve) => {
      manualStarted = resolve;
    });
    const manualResponse = new Promise<void>((resolve) => {
      releaseManual = resolve;
    });
    let admittedSignal: AbortSignal | undefined;
    const admitted = await harness({
      streams: [{ events: continuation(ask("manual")), hold: true }],
      unary: async (method, _input, signal) => {
        if (method !== "ResolveRunAsk") return undefined;
        admittedSignal = signal;
        manualStarted();
        await manualResponse;
        return { askId: "manual", runId: continuationRunId };
      },
    });
    const admittedFlow = admitted.session.mcpAuthorization(authorizationId).recheck();
    const admittedIterator = admittedFlow[Symbol.asyncIterator]();
    await admittedIterator.next();
    await admittedIterator.next();
    await admittedIterator.next();
    const manualControl = admittedFlow.resolveAsk("manual", "deny");
    await manualStart;
    await admittedIterator.return?.();
    expect(admittedSignal?.aborted).toBe(false);
    releaseManual();
    await expect(manualControl).resolves.toBeUndefined();
    expect(admitted.transport.closedStreams).toBe(1);
    await admitted.client.close();
    await statusOnly.client.close();
    await completed.client.close();
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

  it("disconnect attach replays chained authorization park as terminal", async () => {
    const nextAuthorizationId = "authorization-after-disconnect";
    const instance = await harness({
      streams: [
        {
          error: new ConnectError("authorization response disconnected", Code.Unavailable),
          events: [
            authorization("authorization.resolved", "granted"),
            authorization("authorization.resolved", "granted", {
              runId: continuationRunId,
            }),
          ],
        },
      ],
      watchStreams: [
        { events: [{ cursor: "activity-boundary", phase: "live" }], hold: true },
        {
          events: [
            {
              cursor: "wrong-run",
              event: authorization("authorization.required", "pending", {
                authorizationId: "wrong-run-authorization",
                callId: "wrong-run-call",
                runId: "wrong-run",
              }),
              phase: "replay",
            },
            {
              cursor: "wrong-status",
              event: authorization("authorization.required", "granted", {
                authorizationId: nextAuthorizationId,
                callId: "next-call",
                runId: continuationRunId,
              }),
              phase: "replay",
            },
            {
              cursor: "empty-authorization",
              event: authorization("authorization.required", "pending", {
                authorizationId: "",
                callId: "next-call",
                runId: continuationRunId,
              }),
              phase: "replay",
            },
            {
              cursor: "empty-call",
              event: authorization("authorization.required", "pending", {
                authorizationId: nextAuthorizationId,
                callId: "",
                runId: continuationRunId,
              }),
              phase: "replay",
            },
            {
              cursor: "valid-park",
              event: authorization("authorization.required", "pending", {
                authorizationId: nextAuthorizationId,
                callId: "next-call",
                runId: continuationRunId,
              }),
              phase: "replay",
            },
          ],
          hold: true,
        },
      ],
    });
    const flow = instance.session.mcpAuthorization(authorizationId).recheck();
    const flowIterator = flow[Symbol.asyncIterator]();
    await flowIterator.next();
    await flowIterator.next();
    await expect(flowIterator.next()).rejects.toBeInstanceOf(TransportError);
    expect(flow.continuationRunId).toBe(continuationRunId);

    const activity = await instance.session.activity();
    const activityIterator = activity[Symbol.asyncIterator]();
    await expect(activityIterator.next()).resolves.toMatchObject({
      done: false,
      value: { kind: "boundary" },
    });

    const attached = await instance.session.attach(flow.continuationRunId);
    const iterator = attached[Symbol.asyncIterator]();
    await expect(iterator.next()).resolves.toMatchObject({
      done: false,
      value: { event: { payload: { status: "granted" } } },
    });
    expect(attached.live).toBe(true);
    await expect(iterator.next()).resolves.toMatchObject({
      done: false,
      value: { event: { payload: { authorizationId: "", status: "pending" } } },
    });
    expect(attached.live).toBe(true);
    await expect(iterator.next()).resolves.toMatchObject({
      done: false,
      value: { event: { payload: { callId: "", status: "pending" } } },
    });
    expect(attached.live).toBe(true);
    const terminalPark = await iterator.next();
    expect(terminalPark).toMatchObject({
      done: false,
      value: {
        event: {
          payload: {
            authorizationId: nextAuthorizationId,
            callId: "next-call",
            status: "pending",
          },
        },
      },
    });
    if (terminalPark.done || terminalPark.value.kind !== "event") {
      throw new Error("expected the replayed authorization park");
    }
    expect(attached.live).toBe(false);
    await expect(iterator.next()).resolves.toEqual({ done: true, value: undefined });
    expect(attached.cursor).toBe(terminalPark.value.cursor);
    expect(instance.transport.activeStreams).toBe(1);

    await activity.close();
    expect(instance.transport.activeStreams).toBe(0);
    await instance.client.close();
  });
});
