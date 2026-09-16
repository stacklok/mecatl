import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type Client,
  connect,
  createHttpTransport,
  SESSION_ID_HEADER_NAME,
  ServerError,
  SessionMode,
} from "../src/index.js";

interface SeenCall {
  readonly affinity: string | null;
  readonly body?: unknown;
  readonly caller: string | null;
  readonly operation: string;
}

function terminal(runId: string) {
  return { event: { result: { stop: "end_turn", text: "done" }, runId, type: "result" } };
}

function sse(runId: string): Response {
  return new Response(
    `data: ${JSON.stringify({ result: { stop: "end_turn", text: "done" }, run_id: runId, type: "result" })}\n\n`,
    { headers: { "content-type": "text/event-stream" } },
  );
}

async function exercise(client: Client): Promise<void> {
  const requestOptions = { headers: { "x-caller": "kept" }, timeoutMs: 5_000 };
  const source = await client.sessions.create(
    {
      debugTargetSessionId: "session",
      mode: SessionMode.Default,
      modelId: "model",
      providerId: "provider",
    },
    requestOptions,
  );
  await client.sessions.get(source.id, requestOptions);
  await source.snapshot(requestOptions);
  await source.transcript(requestOptions);
  await source.rename("title", requestOptions);
  await source.setMode(SessionMode.Plan, requestOptions);
  await source.compact(requestOptions);
  await source.clear({ worktreeSelector: "clear-selector" }, requestOptions);
  await client.sessions.fork(
    source.id,
    {
      modelId: "fork-model",
      providerId: "fork-provider",
      reasoningEffort: "high",
      title: "fork title",
      worktreeSelector: "fork-selector",
    },
    requestOptions,
  );
  await (await source.run("prompt", {}, requestOptions)).result();
  await (await source.retry({}, requestOptions)).result();
  await source.close(requestOptions);
  await source.delete(requestOptions);
}

describe("session lifecycle transport parity", () => {
  it("both injected transports cover the lifecycle surface", async () => {
    const grpcSeen: SeenCall[] = [];
    const recordGrpc = (operation: string, context: { requestHeader: Headers }, body?: unknown) => {
      grpcSeen.push({
        affinity: context.requestHeader.get(SESSION_ID_HEADER_NAME),
        body,
        caller: context.requestHeader.get("x-caller"),
        operation,
      });
    };
    let grpcRun = 0;
    const grpcTransport = createRouterTransport((router) => {
      router.service(HarnessService, {
        clearSession: (request, context) => {
          recordGrpc("clear", context, request);
          return { sessionId: "cleared" };
        },
        closeSession: (request, context) => {
          recordGrpc("close", context, request);
          return {};
        },
        compactSession: (request, context) => {
          recordGrpc("compact", context, request);
          return { compacted: true };
        },
        createSession: (request, context) => {
          recordGrpc("create", context, request);
          return { sessionCapabilities: { image: true }, sessionId: "session" };
        },
        deleteSession: (request, context) => {
          recordGrpc("delete", context, request);
          return {};
        },
        forkSession: (request, context) => {
          recordGrpc("fork", context, request);
          return { sessionId: "forked" };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: ["server_info"] }),
        getSession: (request, context) => {
          recordGrpc("get", context, request);
          return {
            session: {
              mode: SessionMode.Default,
              sessionCapabilities: { image: true },
              sessionId: request.sessionId,
            },
          };
        },
        getSessionTranscript: (request, context) => {
          recordGrpc("transcript", context, request);
          return { complete: true, sessionId: request.sessionId };
        },
        renameSession: (request, context) => {
          recordGrpc("rename", context, request);
          return { session: { mode: SessionMode.Default, sessionId: request.sessionId } };
        },
        setMode: (request, context) => {
          recordGrpc("setMode", context, request);
          return { session: { mode: request.mode, sessionId: request.sessionId } };
        },
        converse: async function* (requests, context) {
          const start = await requests[Symbol.asyncIterator]().next();
          recordGrpc(start.value?.kind.case ?? "missing", context, start.value);
          grpcRun += 1;
          yield terminal(`grpc-${grpcRun}`);
        },
      });
    });
    const grpc = connect({ transport: grpcTransport, transportKind: "grpc" });
    await exercise(grpc);
    expect(grpcSeen.map((call) => call.operation)).toEqual([
      "create",
      "get",
      "get",
      "transcript",
      "rename",
      "setMode",
      "compact",
      "clear",
      "fork",
      "prompt",
      "retry",
      "close",
      "delete",
    ]);
    expect(grpcSeen.every((call) => call.caller === "kept")).toBe(true);
    expect(grpcSeen.every((call) => call.affinity === "session")).toBe(true);
    expect(grpcSeen.find((call) => call.operation === "clear")?.body).toMatchObject({
      worktreeSelector: "clear-selector",
    });
    expect(grpcSeen.find((call) => call.operation === "fork")?.body).toMatchObject({
      modelId: "fork-model",
      providerId: "fork-provider",
      reasoningEffort: "high",
      title: "fork title",
      worktreeSelector: "fork-selector",
    });
    await grpc.close();

    const httpSeen: SeenCall[] = [];
    let httpRun = 0;
    const httpTransport = createHttpTransport({
      baseUrl: "http://mecatl.test",
      fetch: async (input, init) => {
        const url = new URL(String(input));
        const path = url.pathname;
        const method = init?.method ?? "GET";
        if (path === "/v1/compatibility")
          return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
        const headers = new Headers(init?.headers);
        const body = init?.body === undefined ? undefined : JSON.parse(String(init.body));
        const operation =
          path === "/v1/sessions"
            ? "create"
            : path.endsWith("/transcript")
              ? "transcript"
              : path.endsWith("/rename")
                ? "rename"
                : path.endsWith("/mode")
                  ? "setMode"
                  : path.endsWith("/compact")
                    ? "compact"
                    : path.endsWith("/clear")
                      ? "clear"
                      : path.endsWith("/fork")
                        ? "fork"
                        : path.endsWith("/prompt")
                          ? "prompt"
                          : path.endsWith("/retry")
                            ? "retry"
                            : path.endsWith("/delete")
                              ? "delete"
                              : method === "DELETE"
                                ? "close"
                                : "get";
        httpSeen.push({
          affinity: headers.get(SESSION_ID_HEADER_NAME),
          body,
          caller: headers.get("x-caller"),
          operation,
        });
        if (operation === "create") {
          return Response.json(
            { session_capabilities: { image: true }, session_id: "session" },
            { status: 201 },
          );
        }
        if (operation === "get") {
          return Response.json({
            mode: "default",
            session_capabilities: { image: true },
            session_id: "session",
          });
        }
        if (operation === "transcript") {
          return Response.json({ complete: true, session_id: "session" });
        }
        if (operation === "rename") {
          return Response.json({ mode: "default", session_id: "session" });
        }
        if (operation === "setMode") {
          return Response.json({ mode: "plan", session_id: "session" });
        }
        if (operation === "compact") return Response.json({ compacted: true });
        if (operation === "clear") {
          return Response.json({ session_id: "cleared" }, { status: 201 });
        }
        if (operation === "fork") {
          return Response.json({ session_id: "forked" }, { status: 201 });
        }
        if (operation === "prompt" || operation === "retry") {
          httpRun += 1;
          return sse(`http-${httpRun}`);
        }
        return new Response(null, { status: 204 });
      },
    });
    const http = connect({ transport: httpTransport, transportKind: "http" });
    await exercise(http);
    expect(httpSeen.map((call) => call.operation)).toEqual([
      "create",
      "get",
      "get",
      "transcript",
      "rename",
      "setMode",
      "compact",
      "clear",
      "fork",
      "prompt",
      "retry",
      "close",
      "delete",
    ]);
    expect(httpSeen.every((call) => call.caller === "kept")).toBe(true);
    expect(httpSeen.every((call) => call.affinity === "session")).toBe(true);
    expect(httpSeen.find((call) => call.operation === "clear")?.body).toEqual({
      worktree_selector: "clear-selector",
    });
    expect(httpSeen.find((call) => call.operation === "fork")?.body).toEqual({
      model_id: "fork-model",
      provider_id: "fork-provider",
      reasoning_effort: "high",
      title: "fork title",
      worktree_selector: "fork-selector",
    });
    await http.close();
  });

  it("both injected transports normalize every unary lifecycle failure class", async () => {
    const classes = ["projection", "snapshot-mutation", "acknowledgement", "acquisition"] as const;
    for (const failureClass of classes) {
      const grpcTransport = createRouterTransport((router) => {
        router.service(HarnessService, {
          compactSession: () => {
            if (failureClass === "acknowledgement") {
              throw new ConnectError("rejected", Code.InvalidArgument);
            }
            return { compacted: true };
          },
          createSession: () => {
            if (failureClass === "acquisition") {
              throw new ConnectError("rejected", Code.InvalidArgument);
            }
            return { sessionId: "session" };
          },
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: ["server_info"],
          }),
          getSession: () => {
            if (failureClass === "projection") {
              throw new ConnectError("rejected", Code.InvalidArgument);
            }
            return { session: { mode: SessionMode.Default, sessionId: "session" } };
          },
          renameSession: () => {
            if (failureClass === "snapshot-mutation") {
              throw new ConnectError("rejected", Code.InvalidArgument);
            }
            return { session: { mode: SessionMode.Default, sessionId: "session" } };
          },
        });
      });
      const grpc = connect({ transport: grpcTransport, transportKind: "grpc" });
      const grpcFailure = async () => {
        if (failureClass === "acquisition") return grpc.sessions.create({});
        const session = await grpc.sessions.create({});
        if (failureClass === "projection") return session.snapshot();
        if (failureClass === "snapshot-mutation") return session.rename("title");
        return session.compact();
      };
      await expect(grpcFailure()).rejects.toBeInstanceOf(ServerError);
      await grpc.close();

      const problem = () =>
        Response.json(
          {
            code: "invalid_argument",
            detail: "rejected",
            status: 400,
            title: "Invalid argument",
          },
          { headers: { "content-type": "application/problem+json" }, status: 400 },
        );
      const httpTransport = createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: async (input) => {
          const path = new URL(String(input)).pathname;
          if (path === "/v1/compatibility")
            return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
          if (path === "/v1/sessions") {
            return failureClass === "acquisition"
              ? problem()
              : Response.json({ session_id: "session" });
          }
          if (path === "/v1/sessions/session") {
            return failureClass === "projection"
              ? problem()
              : Response.json({ mode: "default", session_id: "session" });
          }
          if (path.endsWith("/rename")) {
            return failureClass === "snapshot-mutation"
              ? problem()
              : Response.json({ mode: "default", session_id: "session" });
          }
          if (path.endsWith("/compact")) {
            return failureClass === "acknowledgement"
              ? problem()
              : Response.json({ compacted: true });
          }
          throw new Error(`unexpected path ${path}`);
        },
      });
      const http = connect({ transport: httpTransport, transportKind: "http" });
      const httpFailure = async () => {
        if (failureClass === "acquisition") return http.sessions.create({});
        const session = await http.sessions.create({});
        if (failureClass === "projection") return session.snapshot();
        if (failureClass === "snapshot-mutation") return session.rename("title");
        return session.compact();
      };
      await expect(httpFailure()).rejects.toBeInstanceOf(ServerError);
      await http.close();
    }
  });
});
