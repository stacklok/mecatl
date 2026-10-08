import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  connect,
  createHttpTransport,
  InvalidStateError,
  SESSION_ID_HEADER_NAME,
  SessionBusyError,
} from "../src/index.js";

function event(runId: string, type: string, text = "") {
  return { event: { runId, text, type } };
}

function terminal(runId: string, stop = "end_turn") {
  return {
    event: { result: { stop, text: `${stop} result` }, runId, type: "result" },
  };
}

function sse(value: unknown): Uint8Array {
  return new TextEncoder().encode(`data: ${JSON.stringify(value)}\n\n`);
}

describe("session retry", () => {
  it("retry returns the ordinary run lifecycle", async () => {
    let starts = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests) {
          const input = requests[Symbol.asyncIterator]();
          const start = await input.next();
          expect(start.value?.kind).toEqual({
            case: "retry",
            value: expect.objectContaining({ sessionId: "session" }),
          });
          expect(Object.keys(start.value?.kind.value ?? {})).toEqual(["$typeName", "sessionId"]);
          starts += 1;
          yield event(`retry-${starts}`, "message.delta", "retried");
          yield terminal(`retry-${starts}`);
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});

    const iterated = await session.retry();
    expect(iterated.id).toBe("retry-1");
    const kinds: string[] = [];
    for await (const value of iterated) kinds.push(value.kind);
    expect(kinds).toEqual(["message.delta", "result"]);
    await expect(iterated.result()).rejects.toBeInstanceOf(InvalidStateError);

    const drained = await session.retry();
    await expect(drained.result()).resolves.toMatchObject({
      runId: "retry-2",
      sessionId: "session",
      stopReason: "end_turn",
    });
    await client.close();
  });

  it("run and retry preserve responders request controls cancellation and terminals", async () => {
    const permission = vi.fn(() => "allow_once" as const);
    const plan = vi.fn(() => "approve" as const);
    const starts: string[] = [];
    let sequence = 0;
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => ({ sessionId: "session" }),
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        converse: async function* (requests, context) {
          expect(context.requestHeader.get("x-caller")).toBe("kept");
          expect(context.requestHeader.get(SESSION_ID_HEADER_NAME)).toBe("session");
          const input = requests[Symbol.asyncIterator]();
          const start = await input.next();
          starts.push(start.value?.kind.case ?? "missing");
          sequence += 1;
          const runId = `run-${sequence}`;
          if (sequence <= 2) {
            yield {
              event: {
                ask: {
                  args: "{}",
                  askId: `ask-${sequence}`,
                  tool: sequence === 1 ? "PresentPlan" : "Read",
                },
                runId,
                type: "permission.ask",
              },
            };
            const control = await input.next();
            expect(control.value?.kind.case).toBe("resumeApproval");
            yield terminal(runId);
            return;
          }
          yield event(runId, "message.delta");
          const control = await input.next();
          expect(control.value?.kind.case).toBe("cancel");
          yield terminal(runId, "cancelled");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    const controller = new AbortController();
    const requestOptions = {
      headers: { "x-caller": "kept" },
      signal: controller.signal,
      timeoutMs: 5_000,
    };
    const responders = { onPermissionAsk: permission, onPlanApproval: plan };

    await expect(
      (await session.run("prompt", responders, requestOptions)).result(),
    ).resolves.toMatchObject({ stopReason: "end_turn" });
    await expect((await session.retry(responders, requestOptions)).result()).resolves.toMatchObject(
      {
        stopReason: "end_turn",
      },
    );
    expect(plan).toHaveBeenCalledOnce();
    expect(permission).toHaveBeenCalledOnce();

    const active = await session.retry({}, requestOptions);
    await expect(session.run("busy")).rejects.toBeInstanceOf(SessionBusyError);
    await active.cancel();
    await expect(active.result()).resolves.toMatchObject({ stopReason: "cancelled" });
    expect(starts).toEqual(["prompt", "retry", "retry"]);
    await client.close();

    let retryController: ReadableStreamDefaultController<Uint8Array> | undefined;
    const httpCalls: string[] = [];
    const httpTransport = createHttpTransport({
      baseUrl: "http://mecatl.test",
      fetch: async (input, init) => {
        const path = new URL(String(input)).pathname;
        const headers = new Headers(init?.headers);
        if (path === "/v1/compatibility")
          return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
        if (path === "/v1/sessions") return Response.json({ session_id: "session" });
        expect(headers.get("x-caller")).toBe("kept");
        expect(headers.get(SESSION_ID_HEADER_NAME)).toBe("session");
        httpCalls.push(path);
        if (path.endsWith("/retry")) {
          return new Response(
            new ReadableStream<Uint8Array>({
              start(streamController) {
                retryController = streamController;
                streamController.enqueue(
                  sse({ run_id: "http-retry", text: "started", type: "message.delta" }),
                );
              },
            }),
            { headers: { "content-type": "text/event-stream" } },
          );
        }
        if (path.endsWith("/cancel")) {
          retryController?.enqueue(
            sse({
              result: { stop: "cancelled", text: "cancelled result" },
              run_id: "http-retry",
              type: "result",
            }),
          );
          retryController?.close();
          return new Response(null, { status: 204 });
        }
        throw new Error(`unexpected path ${path}`);
      },
    });
    const http = connect({ transport: httpTransport, transportKind: "http" });
    const httpSession = await http.sessions.create({});
    const httpRetry = await httpSession.retry({}, requestOptions);
    await httpRetry.cancel();
    await expect(httpRetry.result()).resolves.toMatchObject({ stopReason: "cancelled" });
    expect(httpCalls).toEqual([
      "/v1/sessions/session/retry",
      "/v1/sessions/session/controls/cancel",
    ]);
    await http.close();
  });
});
