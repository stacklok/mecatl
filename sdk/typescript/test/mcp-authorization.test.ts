import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
} from "@bufbuild/protobuf";
import { create } from "@bufbuild/protobuf";
import type { ContextValues, StreamResponse, Transport, UnaryResponse } from "@connectrpc/connect";
import { describe, expect, expectTypeOf, it } from "vitest";

import {
  connect,
  createHttpTransport,
  InvalidStateError,
  type McpAuthorization,
  type McpAuthorizationResult,
  ProtocolError,
  type RequestOptions,
  SESSION_ID_HEADER_NAME,
  ServerError,
} from "../src/index.js";

const sessionId = "session-authorization";
const authorizationId = "authorization-1";
const continuationRunId = "run-continuation";

function authorization(
  type: "authorization.required" | "authorization.resolved",
  status: string,
  options: {
    readonly authorizationId?: string;
    readonly callId?: string;
    readonly displayName?: string;
    readonly runId?: string;
  } = {},
) {
  return {
    authorization: {
      authorizationId: options.authorizationId ?? authorizationId,
      callId: options.callId ?? "call-1",
      displayName: options.displayName ?? "Example connector",
      status,
    },
    runId: options.runId ?? "",
    type,
  };
}

function message(runId = continuationRunId, text = "continuing") {
  return { runId, text, type: "message.delta" };
}

function terminal(runId = continuationRunId, text = "complete") {
  return {
    result: { stop: "end_turn", text, usage: { inputTokens: 4n, outputTokens: 2n } },
    runId,
    type: "result",
  };
}

type WireEvent =
  | ReturnType<typeof authorization>
  | ReturnType<typeof message>
  | ReturnType<typeof terminal>;

interface TransportCall {
  readonly headers: Headers;
  readonly input: unknown;
  readonly method: string;
  readonly signal: AbortSignal | undefined;
  readonly timeoutMs: number | undefined;
}

class AuthorizationTransport implements Transport {
  readonly calls: TransportCall[] = [];
  readonly controls: Array<{ readonly method: "cancel" | "recheck"; readonly request: unknown }> =
    [];
  readonly #streams: WireEvent[][];

  constructor(streams: WireEvent[][] = [], presentation = "https://identity.example/authorize") {
    this.#streams = streams.map((events) => [...events]);
    this.presentation = presentation;
  }

  readonly presentation: string;

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    headers: HeadersInit | undefined,
    input: MessageInitShape<I>,
    contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    this.calls.push({
      headers: new Headers(headers),
      input,
      method: method.name,
      signal,
      timeoutMs,
    });
    void contextValues;
    const responseHeader = new Headers();
    const responseTrailer = new Headers();
    let value: Record<string, unknown>;
    switch (method.name) {
      case "GetCompatibilityInfo":
        value = { apiMajor: 1, capabilities: {}, features: [] };
        break;
      case "GetSession":
        value = { session: { sessionId } };
        break;
      case "GetMcpAuthorizationPresentation":
        responseHeader.set("x-response", "presentation-header");
        responseTrailer.set("x-response", "presentation-trailer");
        value = { url: this.presentation };
        break;
      default:
        throw new Error(`Unexpected unary ${method.name}`);
    }
    return {
      header: responseHeader,
      message: create(method.output, value as MessageInitShape<O>),
      method,
      service: method.parent,
      stream: false,
      trailer: responseTrailer,
    };
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    headers: HeadersInit | undefined,
    input: AsyncIterable<MessageInitShape<I>>,
    contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    this.calls.push({
      headers: new Headers(headers),
      input,
      method: method.name,
      signal,
      timeoutMs,
    });
    void contextValues;
    const first = await input[Symbol.asyncIterator]().next();
    this.controls.push({
      method: method.name === "CancelMcpAuthorization" ? "cancel" : "recheck",
      request: first.value,
    });
    const events = this.#streams.shift() ?? [];
    const messages = (async function* () {
      for (const event of events) {
        yield create(method.output, { event } as unknown as MessageInitShape<O>);
      }
    })();
    return {
      header: new Headers(),
      message: messages,
      method,
      service: method.parent,
      stream: true,
      trailer: new Headers(),
    };
  }
}

async function grpcSession(
  streams: WireEvent[][] = [],
  presentation?: string,
): Promise<{
  readonly authorization: McpAuthorization;
  readonly client: ReturnType<typeof connect>;
  readonly transport: AuthorizationTransport;
}> {
  const transport = new AuthorizationTransport(streams, presentation);
  const client = connect({ transport });
  const session = await client.sessions.get(sessionId);
  return { authorization: session.mcpAuthorization(authorizationId), client, transport };
}

function httpClient(
  presentation: string | Response,
  streams: readonly (readonly Record<string, unknown>[])[] = [],
): {
  readonly client: ReturnType<typeof connect>;
  readonly requests: Array<{ readonly body: BodyInit | null | undefined; readonly path: string }>;
} {
  const requests: Array<{ readonly body: BodyInit | null | undefined; readonly path: string }> = [];
  const remaining = streams.map((events) => [...events]);
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const path = new URL(String(input)).pathname;
    requests.push({ body: init?.body, path });
    if (path === "/v1/compatibility") {
      return Response.json({ api_major: 1, capabilities: {}, features: [] });
    }
    if (path === `/v1/sessions/${sessionId}`) {
      return Response.json({ session_id: sessionId, state: "idle" });
    }
    if (path.endsWith("/presentation")) {
      return presentation instanceof Response ? presentation : Response.json({ url: presentation });
    }
    const events = remaining.shift() ?? [];
    return new Response(events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(""), {
      headers: { "content-type": "text/event-stream" },
    });
  };
  return {
    client: connect({
      transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch }),
      transportKind: "http",
    }),
    requests,
  };
}

function httpAuthorization(
  type: "authorization.required" | "authorization.resolved",
  status: string,
  runId = "",
  id = authorizationId,
): Record<string, unknown> {
  return {
    authorization: {
      authorization_id: id,
      call_id: "call-1",
      display_name: "Example connector",
      status,
    },
    run_id: runId,
    type,
  };
}

describe("MCP authorization lifecycle", () => {
  it("MCP authorization handle binds exact correlation without I/O", async () => {
    const transport = new AuthorizationTransport();
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);
    const before = transport.calls.length;

    const handle = session.mcpAuthorization(authorizationId);

    expect(handle).toMatchObject({ authorizationId, sessionId });
    expect(transport.calls).toHaveLength(before);
    expectTypeOf(handle).toEqualTypeOf<McpAuthorization>();
    await client.close();
  });

  it("MCP authorization presentation is live validated and application owned", async () => {
    const { authorization: handle, client, transport } = await grpcSession();
    const controller = new AbortController();
    const responseHeaders: string[] = [];
    const responseTrailers: string[] = [];
    const requestOptions: RequestOptions = {
      headers: { "x-caller": "kept" },
      onHeader: (headers) => responseHeaders.push(headers.get("x-response") ?? ""),
      onTrailer: (headers) => responseTrailers.push(headers.get("x-response") ?? ""),
      signal: controller.signal,
      timeoutMs: 4_321,
    };

    await expect(handle.presentation(requestOptions)).resolves.toBe(
      "https://identity.example/authorize",
    );
    const call = transport.calls.find(
      (candidate) => candidate.method === "GetMcpAuthorizationPresentation",
    );
    expect(call).toMatchObject({
      input: { authorizationId, sessionId },
      signal: controller.signal,
      timeoutMs: 4_321,
    });
    expect(call?.headers.get("x-caller")).toBe("kept");
    expect(call?.headers.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
    expect(responseHeaders).toEqual(["presentation-header"]);
    expect(responseTrailers).toEqual(["presentation-trailer"]);
    expect(
      transport.calls.filter((candidate) => candidate.method === "GetMcpAuthorizationPresentation"),
    ).toHaveLength(1);
    await client.close();
  });

  it("MCP authorization presentation preserves protocol and server failures", async () => {
    for (const value of ["", "/authorize", "file:///secret", "not a URL"]) {
      const harness = await grpcSession([], value);
      await expect(harness.authorization.presentation()).rejects.toBeInstanceOf(ProtocolError);
      await harness.client.close();
    }

    const failure = httpClient(
      Response.json({ code: "not_found", detail: "authorization unavailable" }, { status: 404 }),
    );
    const session = await failure.client.sessions.get(sessionId);
    const error = await session
      .mcpAuthorization(authorizationId)
      .presentation()
      .catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(ServerError);
    expect(error).toMatchObject({ code: "not_found", transport: "http" });
    await failure.client.close();
  });

  it("MCP authorization operations start only on first consumption", async () => {
    const {
      authorization: handle,
      client,
      transport,
    } = await grpcSession([
      [authorization("authorization.required", "pending")],
      [authorization("authorization.resolved", "cancelled")],
    ]);
    const before = transport.calls.length;
    const recheck = handle.recheck();
    const cancel = handle.cancel();

    expect(recheck).toMatchObject({ authorizationId, operation: "recheck", sessionId });
    expect(cancel).toMatchObject({ authorizationId, operation: "cancel", sessionId });
    expect(recheck).not.toBe(cancel);
    const iterator = recheck[Symbol.asyncIterator]();
    expect(transport.calls).toHaveLength(before);

    await expect(iterator.next()).resolves.toMatchObject({
      done: false,
      value: { kind: "authorization.required" },
    });
    expect(
      transport.calls.filter((call) => call.method === "RecheckMcpAuthorization"),
    ).toHaveLength(1);
    expect(transport.calls.some((call) => call.method === "CancelMcpAuthorization")).toBe(false);
    await iterator.return?.();
    await client.close();
  });

  it("MCP authorization flow start preserves request timing and exact control", async () => {
    const {
      authorization: handle,
      client,
      transport,
    } = await grpcSession([
      [authorization("authorization.required", "pending")],
      [authorization("authorization.resolved", "cancelled")],
    ]);
    const controller = new AbortController();
    const options: RequestOptions = {
      headers: { "x-caller": "kept" },
      signal: controller.signal,
      timeoutMs: 4_567,
    };
    await expect(handle.recheck(undefined, options).result()).resolves.toMatchObject({
      outcome: "pending",
      status: "pending",
    });
    const call = transport.calls.find(
      (candidate) => candidate.method === "RecheckMcpAuthorization",
    );
    expect(call).toMatchObject({ signal: expect.any(AbortSignal), timeoutMs: 4_567 });
    expect(call?.headers.get("x-caller")).toBe("kept");
    expect(call?.headers.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
    expect(transport.controls).toEqual([
      { method: "recheck", request: { authorizationId, sessionId } },
    ]);

    const aborted = new AbortController();
    aborted.abort(new Error("caller stopped"));
    const before = transport.calls.length;
    await expect(
      handle.cancel(undefined, { signal: aborted.signal }).result(),
    ).rejects.toMatchObject({
      code: "transport",
    });
    expect(transport.calls).toHaveLength(before);

    const http = httpClient("https://identity.example/authorize", [
      [httpAuthorization("authorization.required", "pending")],
    ]);
    const httpSession = await http.client.sessions.get(sessionId);
    await expect(
      httpSession.mcpAuthorization(authorizationId).recheck().result(),
    ).resolves.toMatchObject({ outcome: "pending" });
    expect(http.requests.find((request) => request.path.endsWith("/recheck"))).toEqual({
      body: undefined,
      path: `/v1/sessions/${sessionId}/mcp-authorizations/${authorizationId}/recheck`,
    });
    await http.client.close();
    await client.close();
  });

  it("MCP authorization flow validates the authoritative control result", async () => {
    const malformed: WireEvent[][] = [
      [message("", "unknown event")],
      [authorization("authorization.required", "pending", { runId: "unexpected-run" })],
      [authorization("authorization.required", "pending", { authorizationId: "other" })],
      [authorization("authorization.required", "pending", { callId: "" })],
      [authorization("authorization.required", "future")],
      [authorization("authorization.resolved", "pending")],
      [authorization("authorization.required", "granted")],
    ];
    const { authorization: handle, client } = await grpcSession(malformed);
    for (const _events of malformed) {
      await expect(handle.recheck().result()).rejects.toBeInstanceOf(ProtocolError);
    }
    await client.close();
  });

  it("MCP authorization status-only results are discriminated values", async () => {
    const statuses = [
      "pending",
      "granted",
      "denied",
      "cancelled",
      "expired",
      "interrupted",
      "failed",
      "closed",
    ] as const;
    const streams = statuses.map((status) => [
      authorization(
        status === "pending" ? "authorization.required" : "authorization.resolved",
        status,
      ),
    ]);
    const { authorization: handle, client } = await grpcSession(streams);
    const results: McpAuthorizationResult[] = [];
    for (const _status of statuses) results.push(await handle.recheck().result());

    expect(results.map(({ outcome, status }) => `${outcome}:${status}`)).toEqual([
      "pending:pending",
      "settled:granted",
      "settled:denied",
      "settled:cancelled",
      "settled:expired",
      "settled:interrupted",
      "settled:failed",
      "settled:closed",
    ]);
    expectTypeOf<McpAuthorizationResult>().toMatchTypeOf<
      | { readonly outcome: "pending"; readonly status: "pending" }
      | {
          readonly outcome: "settled" | "completed" | "authorization_required";
          readonly status: Exclude<(typeof statuses)[number], "pending">;
        }
    >();
    await client.close();
  });

  it("MCP authorization flow is single consumption", async () => {
    const {
      authorization: handle,
      client,
      transport,
    } = await grpcSession([
      [authorization("authorization.required", "pending")],
      [authorization("authorization.required", "pending")],
    ]);
    const iterated = handle.recheck();
    const iterator = iterated[Symbol.asyncIterator]();
    expect(() => iterated[Symbol.asyncIterator]()).toThrow(InvalidStateError);
    await expect(iterated.result()).rejects.toBeInstanceOf(InvalidStateError);
    await iterator.return?.();

    const drained = handle.recheck();
    await expect(drained.result()).resolves.toMatchObject({ outcome: "pending" });
    await expect(drained.result()).rejects.toBeInstanceOf(InvalidStateError);
    expect(() => drained[Symbol.asyncIterator]()).toThrow(InvalidStateError);
    expect(
      transport.calls.filter((call) => call.method === "RecheckMcpAuthorization"),
    ).toHaveLength(1);
    await client.close();
  });

  it("MCP authorization continuation validates run and repeated resolution grammar", async () => {
    const original = authorization("authorization.resolved", "granted");
    const repeated = authorization("authorization.resolved", "granted", {
      runId: continuationRunId,
    });
    const cases: WireEvent[][] = [
      [original, repeated, message("changed-run"), terminal("changed-run")],
      [original, message(), terminal()],
      [original, repeated, repeated, terminal()],
      [original, repeated, terminal(), terminal()],
      [original, repeated, terminal(), message()],
      [
        original,
        repeated,
        authorization("authorization.required", "pending", {
          runId: continuationRunId,
        }),
      ],
    ];
    const { authorization: handle, client } = await grpcSession(cases);
    for (const _events of cases) {
      await expect(handle.recheck().result()).rejects.toBeInstanceOf(ProtocolError);
    }
    await client.close();
  });

  it("MCP authorization continuation returns one ordinary completed result", async () => {
    const sequence = [
      authorization("authorization.resolved", "granted"),
      authorization("authorization.resolved", "granted", { runId: continuationRunId }),
      message(),
      terminal(),
    ];
    const { authorization: handle, client } = await grpcSession([sequence, sequence]);

    await expect(handle.recheck().result()).resolves.toMatchObject({
      authorization: { kind: "authorization.resolved" },
      continuation: {
        content: "complete",
        runId: continuationRunId,
        sessionId,
        stopReason: "end_turn",
      },
      continuationRunId,
      outcome: "completed",
      status: "granted",
    });

    const iterated = handle.recheck();
    const events = [];
    for await (const event of iterated) events.push(event);
    expect(events.map((event) => `${event.runId}:${event.kind}`)).toEqual([
      ":authorization.resolved",
      `${continuationRunId}:authorization.resolved`,
      `${continuationRunId}:message.delta`,
      `${continuationRunId}:result`,
    ]);
    expect(iterated.continuationRunId).toBe(continuationRunId);
    await client.close();
  });

  it("MCP authorization continuation hands off a chained authorization", async () => {
    const nextAuthorizationId = "authorization-2";
    const sequence = [
      authorization("authorization.resolved", "granted"),
      authorization("authorization.resolved", "granted", { runId: continuationRunId }),
      message(),
      authorization("authorization.required", "pending", {
        authorizationId: nextAuthorizationId,
        callId: "call-2",
        runId: continuationRunId,
      }),
    ];
    const { authorization: handle, client } = await grpcSession([sequence]);

    await expect(handle.recheck().result()).resolves.toMatchObject({
      authorization: { payload: { authorizationId }, kind: "authorization.resolved" },
      continuationRunId,
      nextAuthorization: {
        payload: { authorizationId: nextAuthorizationId, callId: "call-2", status: "pending" },
        kind: "authorization.required",
        runId: continuationRunId,
      },
      outcome: "authorization_required",
      status: "granted",
    });
    await client.close();
  });
});
