import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { connect, SESSION_ID_HEADER_NAME } from "../src/index.js";

const sessionId = "session-affinity";

function terminal(runId: string) {
  return { event: { result: { stop: "end_turn", text: "done" }, runId, type: "result" } };
}

describe("high-level session affinity", () => {
  it("TestSessionAffinityAndHandoff_Scenario4_TypeScriptHighLevelPropagation", async () => {
    const seen: Array<{ affinity: string | null; operation: string }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        closeSession: (_request, context) => {
          seen.push({
            affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
            operation: "close",
          });
          return {};
        },
        createSession: (request, context) => {
          const derived = request.sourceSessionId || request.debugTargetSessionId;
          if (derived !== "") {
            seen.push({
              affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
              operation: "create-derived",
            });
          }
          return { sessionId };
        },
        deleteSession: (_request, context) => {
          seen.push({
            affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
            operation: "delete",
          });
          return {};
        },
        forkSession: (_request, context) => {
          seen.push({
            affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
            operation: "fork",
          });
          return { sessionId: "forked-session" };
        },
        getCompatibilityInfo: (_request, context) => {
          expect(context.requestHeader.has(SESSION_ID_HEADER_NAME)).toBe(false);
          return { apiMajor: 1 };
        },
        getSession: (request, context) => {
          seen.push({
            affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
            operation: "get",
          });
          return { session: { sessionId: request.sessionId } };
        },
        converse: async function* (requests, context) {
          seen.push({
            affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
            operation: "prompt",
          });
          const input = requests[Symbol.asyncIterator]();
          await input.next();
          yield { event: { runId: "different-run-id", text: "started", type: "message.delta" } };
          for (const operation of ["approve", "steer", "cancel"]) {
            await input.next();
            seen.push({
              affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
              operation,
            });
          }
          yield terminal("different-run-id");
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.create({});
    await client.sessions.create({ sourceSessionId: sessionId });
    await client.sessions.create({ debugTargetSessionId: sessionId });

    await client.sessions.get(session.id);
    await client.sessions.fork(session.id);
    const run = await session.run("hello");
    await run.approve("ask", true);
    await run.steer("continue");
    await run.cancel();
    await run.result();
    await session.close();
    await session.delete();

    expect(seen.map(({ operation }) => operation)).toEqual([
      "create-derived",
      "create-derived",
      "get",
      "fork",
      "prompt",
      "approve",
      "steer",
      "cancel",
      "close",
      "delete",
    ]);
    expect(seen.every(({ affinity }) => affinity === sessionId)).toBe(true);
    expect(seen.every(({ affinity }) => affinity !== run.id)).toBe(true);
    await client.close();

    const httpSeen: Array<{ affinity: string | null; operation: string }> = [];
    let promptController: ReadableStreamDefaultController<Uint8Array> | undefined;
    const encoder = new TextEncoder();
    const http = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input, init) => {
        const path = new URL(String(input)).pathname;
        const method = init?.method ?? "GET";
        const headers = new Headers(init?.headers);
        const operation =
          path === "/v1/compatibility"
            ? "compatibility"
            : path === "/v1/sessions"
              ? "create"
              : path.endsWith("/fork")
                ? "fork"
                : path.endsWith("/prompt")
                  ? "prompt"
                  : path.endsWith("/approve")
                    ? "approve"
                    : path.endsWith("/steer")
                      ? "steer"
                      : path.endsWith("/cancel")
                        ? "cancel"
                        : path.endsWith("/delete")
                          ? "delete"
                          : method === "DELETE"
                            ? "close"
                            : "get";
        if (operation !== "compatibility" && operation !== "create") {
          httpSeen.push({ affinity: headers.get(SESSION_ID_HEADER_NAME), operation });
        }
        if (operation === "compatibility") {
          return Response.json({ api_major: 1, features: ["http_steer"] });
        }
        if (operation === "create") {
          const body = JSON.parse(await new Response(init?.body).text()) as {
            debug_target_session_id?: string;
            source_session_id?: string;
          };
          if (body.source_session_id !== undefined || body.debug_target_session_id !== undefined) {
            httpSeen.push({
              affinity: headers.get(SESSION_ID_HEADER_NAME),
              operation: "create-derived",
            });
          }
          return Response.json({ session_id: sessionId }, { status: 201 });
        }
        if (operation === "get") {
          return Response.json({ session_id: sessionId, state: "idle" });
        }
        if (operation === "fork") {
          return Response.json({ session_id: "forked-session" }, { status: 201 });
        }
        if (operation === "prompt") {
          return new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                promptController = controller;
                controller.enqueue(
                  encoder.encode(
                    `data: ${JSON.stringify({ run_id: "different-http-run-id", text: "started", type: "message.delta" })}\n\n`,
                  ),
                );
              },
            }),
            { headers: { "content-type": "text/event-stream" } },
          );
        }
        if (operation === "cancel") {
          promptController?.enqueue(
            encoder.encode(
              `data: ${JSON.stringify({ result: { stop: "cancelled", text: "done" }, run_id: "different-http-run-id", type: "result" })}\n\n`,
            ),
          );
          promptController?.close();
        }
        return new Response(null, { status: 204 });
      },
    });
    const httpSession = await http.sessions.create({});
    await http.sessions.create({ sourceSessionId: sessionId });

    await http.sessions.get(httpSession.id);
    await http.sessions.fork(httpSession.id);
    const httpRun = await httpSession.run("hello");
    await httpRun.approve("ask", true);
    await httpRun.steer("continue");
    await httpRun.cancel();
    await httpRun.result();
    await httpSession.close();
    await httpSession.delete();

    expect(httpSeen.map(({ operation }) => operation)).toEqual([
      "create-derived",
      "get",
      "fork",
      "prompt",
      "approve",
      "steer",
      "cancel",
      "close",
      "delete",
    ]);
    expect(httpSeen.every(({ affinity }) => affinity === sessionId)).toBe(true);
    expect(httpSeen.every(({ affinity }) => affinity !== httpRun.id)).toBe(true);
    await http.close();
  });
});
