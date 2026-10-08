import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
} from "@bufbuild/protobuf";
import type { ContextValues, StreamResponse, Transport, UnaryResponse } from "@connectrpc/connect";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  AuthenticationError,
  connect,
  createHttpTransport,
  type MecatlError,
  SESSION_ID_HEADER_NAME,
  ServerError,
  type Session,
  TransportError,
  WorkspaceEnrollmentStatus,
} from "../src/index.js";

const sessionId = "workspace-transport-session";
const workspaceTargetMethods = new Set([
  "ListSessionMcpConnectors",
  "ConnectWorkspaceServices",
  "RetryWorkspaceEnrollment",
  "CancelWorkspaceEnrollment",
]);

interface RecordedCall {
  readonly header: Headers;
  readonly input: unknown;
  readonly method: string;
  readonly signal: AbortSignal | undefined;
  readonly timeoutMs: number | undefined;
}

class RecordingTransport implements Transport {
  readonly calls: RecordedCall[] = [];
  readonly rejectedTargetCalls: string[] = [];
  rejectTargets = false;
  readonly #delegate: Transport;

  constructor(delegate?: Transport) {
    this.#delegate =
      delegate ??
      createRouterTransport((router) => {
        router.service(HarnessService, {
          cancelWorkspaceEnrollment: (request, context) => {
            context.responseHeader.set("x-response", "cancel-header");
            context.responseTrailer.set("x-response", "cancel-trailer");
            return {
              enrollmentId: request.enrollmentId,
              requiredServices: 2,
              status: "cancelled",
            };
          },
          connectWorkspaceServices: (_request, context) => {
            context.responseHeader.set("x-response", "connect-header");
            context.responseTrailer.set("x-response", "connect-trailer");
            return {
              enrollmentId: "enrollment-1",
              presentationUrl: "https://example.com/authorize",
              requiredServices: 2,
              status: "pending",
            };
          },
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: ["workspace_enrollment_test"],
          }),
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
          listSessionMcpConnectors: (_request, context) => {
            context.responseHeader.set("x-response", "list-header");
            context.responseTrailer.set("x-response", "list-trailer");
            return {
              availability: "available",
              connectors: [{ catalogueState: "discovered", name: "calendar", toolCount: 3 }],
              enrollmentState: "pending",
              totalConnectors: 1,
            };
          },
          retryWorkspaceEnrollment: (_request, context) => {
            context.responseHeader.set("x-response", "retry-header");
            context.responseTrailer.set("x-response", "retry-trailer");
            return {
              enrollmentId: "enrollment-2",
              presentationUrl: "http://127.0.0.1/authorize",
              requiredServices: 2,
              status: "pending",
            };
          },
        });
      });
  }

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: MessageInitShape<I>,
    contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    if (this.rejectTargets && workspaceTargetMethods.has(method.name)) {
      this.rejectedTargetCalls.push(method.name);
      throw new Error(`pre-aborted ${method.name} reached the transport`);
    }
    this.calls.push({ header: new Headers(header), input, method: method.name, signal, timeoutMs });
    return this.#delegate.unary(method, signal, timeoutMs, header, input, contextValues);
  }

  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: AsyncIterable<MessageInitShape<I>>,
    contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    return this.#delegate.stream(method, signal, timeoutMs, header, input, contextValues);
  }
}

async function spinUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error("condition was not reached");
}

async function drainEventLoop(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await Promise.resolve();
}

it("pre-aborted workspace enrollment requests never reach either transport", async () => {
  const methods: ReadonlyArray<{
    readonly invoke: (session: Session, signal: AbortSignal) => Promise<unknown>;
    readonly name: string;
  }> = [
    {
      invoke: (session, signal) => session.listMcpConnectors({ signal }),
      name: "listMcpConnectors",
    },
    {
      invoke: (session, signal) => session.connectWorkspaceServices({ signal }),
      name: "connectWorkspaceServices",
    },
    {
      invoke: (session, signal) => session.retryWorkspaceEnrollment("pending-1", { signal }),
      name: "retryWorkspaceEnrollment",
    },
    {
      invoke: (session, signal) => session.cancelWorkspaceEnrollment("pending-1", { signal }),
      name: "cancelWorkspaceEnrollment",
    },
  ];

  for (const transportKind of ["grpc", "http"] as const) {
    const delegate =
      transportKind === "grpc"
        ? undefined
        : createHttpTransport({
            baseUrl: "http://mecatl.test",
            fetch: async (input) => {
              const path = new URL(String(input)).pathname;
              if (path === "/v1/compatibility")
                return Response.json({
                  api_major: 1,
                  capabilities: {},
                  features: ["workspace_enrollment_test"],
                });
              if (path === `/v1/sessions/${sessionId}`)
                return Response.json({ session_id: sessionId });
              throw new Error(`unexpected pre-aborted HTTP request ${path}`);
            },
          });
    const transport = new RecordingTransport(delegate);
    const client = connect({ transport, transportKind });
    const session = await client.sessions.get(sessionId);
    transport.rejectTargets = true;

    for (const { invoke, name } of methods) {
      const controller = new AbortController();
      controller.abort(new Error(`pre-aborted ${name}`));
      await expect(invoke(session, controller.signal)).rejects.toMatchObject({
        transport: transportKind,
      });
    }
    await drainEventLoop();

    expect(transport.rejectedTargetCalls).toEqual([]);
    expect(transport.calls.filter(({ method }) => workspaceTargetMethods.has(method))).toEqual([]);
    await client.close();
  }
});

it("workspace enrollment has equivalent grpc and http request semantics", async () => {
  const grpcTransport = new RecordingTransport();
  const grpc = connect({ transport: grpcTransport });
  const grpcSession = await grpc.sessions.get(sessionId);
  const grpcHeaders: string[] = [];
  const grpcTrailers: string[] = [];
  const grpcController = new AbortController();
  const grpcOptions = {
    headers: { "x-caller": "grpc-kept" },
    onHeader: (headers: Headers) => grpcHeaders.push(headers.get("x-response") ?? ""),
    onTrailer: (trailers: Headers) => grpcTrailers.push(trailers.get("x-response") ?? ""),
    signal: grpcController.signal,
    timeoutMs: 4_321,
  };
  const grpcResults = [
    await grpcSession.listMcpConnectors(grpcOptions),
    await grpcSession.connectWorkspaceServices(grpcOptions),
    await grpcSession.retryWorkspaceEnrollment("enrollment-1", grpcOptions),
    await grpcSession.cancelWorkspaceEnrollment("enrollment-2", grpcOptions),
  ];
  const grpcTargets = grpcTransport.calls.filter((call) =>
    [
      "ListSessionMcpConnectors",
      "ConnectWorkspaceServices",
      "RetryWorkspaceEnrollment",
      "CancelWorkspaceEnrollment",
    ].includes(call.method),
  );
  expect(grpcTargets.map(({ method }) => method)).toEqual([
    "ListSessionMcpConnectors",
    "ConnectWorkspaceServices",
    "RetryWorkspaceEnrollment",
    "CancelWorkspaceEnrollment",
  ]);
  expect(grpcTargets.map(({ input }) => input)).toEqual([
    expect.objectContaining({ sessionId }),
    expect.objectContaining({ sessionId }),
    expect.objectContaining({ enrollmentId: "enrollment-1", sessionId }),
    expect.objectContaining({ enrollmentId: "enrollment-2", sessionId }),
  ]);
  expect(grpcTargets.every(({ header }) => header.get("x-caller") === "grpc-kept")).toBe(true);
  expect(grpcTargets.every(({ header }) => header.get(SESSION_ID_HEADER_NAME) === sessionId)).toBe(
    true,
  );
  expect(grpcTargets.every(({ timeoutMs }) => timeoutMs === 4_321)).toBe(true);
  expect(grpcHeaders).toEqual(["list-header", "connect-header", "retry-header", "cancel-header"]);
  expect(grpcTrailers).toEqual([
    "list-trailer",
    "connect-trailer",
    "retry-trailer",
    "cancel-trailer",
  ]);

  const httpCalls: Array<{ headers: Headers; path: string; signal: AbortSignal | null }> = [];
  const httpHeaders: string[] = [];
  const httpTrailers: string[] = [];
  const httpController = new AbortController();
  const http = connect({
    baseUrl: "http://mecatl.test",
    fetch: async (input, init) => {
      const path = new URL(String(input)).pathname;
      httpCalls.push({
        headers: new Headers(init?.headers),
        path,
        signal: init?.signal ?? null,
      });
      const value = (() => {
        switch (path) {
          case "/v1/compatibility":
            return { api_major: 1, capabilities: {}, features: ["workspace_enrollment_test"] };
          case `/v1/sessions/${sessionId}`:
            return { session_id: sessionId };
          case `/v1/sessions/${sessionId}/mcp/connectors`:
            return {
              availability: "available",
              connectors: [{ catalogue_state: "discovered", name: "calendar", tool_count: 3 }],
              enrollment_state: "pending",
              total_connectors: 1,
            };
          case `/v1/sessions/${sessionId}/workspace-enrollment/connect`:
            return {
              enrollment_id: "enrollment-1",
              presentation_url: "https://example.com/authorize",
              required_services: 2,
              status: "pending",
            };
          case `/v1/sessions/${sessionId}/workspace-enrollment/enrollment-1/retry`:
            return {
              enrollment_id: "enrollment-2",
              presentation_url: "http://127.0.0.1/authorize",
              required_services: 2,
              status: "pending",
            };
          case `/v1/sessions/${sessionId}/workspace-enrollment/enrollment-2/cancel`:
            return {
              enrollment_id: "enrollment-2",
              required_services: 2,
              status: "cancelled",
            };
          default:
            throw new Error(`unexpected HTTP path ${path}`);
        }
      })();
      return Response.json(value, { headers: { "x-response": path } });
    },
  });
  const httpSession = await http.sessions.get(sessionId);
  const httpOptions = {
    headers: { "x-caller": "http-kept" },
    onHeader: (headers: Headers) => httpHeaders.push(headers.get("x-response") ?? ""),
    onTrailer: (trailers: Headers) => httpTrailers.push(trailers.get("x-response") ?? ""),
    signal: httpController.signal,
    timeoutMs: 4_321,
  };
  const httpResults = [
    await httpSession.listMcpConnectors(httpOptions),
    await httpSession.connectWorkspaceServices(httpOptions),
    await httpSession.retryWorkspaceEnrollment("enrollment-1", httpOptions),
    await httpSession.cancelWorkspaceEnrollment("enrollment-2", httpOptions),
  ];
  expect(httpResults).toEqual(grpcResults);
  const httpTargets = httpCalls.slice(2);
  expect(httpTargets.map(({ path }) => path)).toEqual([
    `/v1/sessions/${sessionId}/mcp/connectors`,
    `/v1/sessions/${sessionId}/workspace-enrollment/connect`,
    `/v1/sessions/${sessionId}/workspace-enrollment/enrollment-1/retry`,
    `/v1/sessions/${sessionId}/workspace-enrollment/enrollment-2/cancel`,
  ]);
  expect(httpTargets.every(({ headers }) => headers.get("x-caller") === "http-kept")).toBe(true);
  expect(
    httpTargets.every(({ headers }) => headers.get(SESSION_ID_HEADER_NAME) === sessionId),
  ).toBe(true);
  expect(httpTargets.every(({ signal }) => signal instanceof AbortSignal)).toBe(true);
  expect(httpHeaders).toEqual(httpTargets.map(({ path }) => path));
  expect(httpTrailers).toEqual(["", "", "", ""]);

  grpcController.abort();
  httpController.abort();
  expect(grpcTargets.every(({ signal }) => signal?.aborted === true)).toBe(true);
  expect(httpTargets.every(({ signal }) => signal?.aborted === true)).toBe(true);
  await Promise.all([grpc.close(), http.close()]);
});

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function statusError(code: string, status: Code, requestId: string): ConnectError {
  const metadata = [...field(1, bytes("request_id")), ...field(2, bytes(requestId))];
  const error = new ConnectError(`${code} from gRPC`, status);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([
      ...field(1, bytes(code)),
      ...field(2, bytes("mecatl.stacklok.com")),
      ...field(3, metadata),
    ]),
  });
  return error;
}

function waitForAbort(signal: AbortSignal): Promise<never> {
  return new Promise((_resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason);
      return;
    }
    signal.addEventListener("abort", () => reject(signal.reason), { once: true });
  });
}

it("workspace enrollment cancellation and failures stay typed", async () => {
  for (const transportKind of ["grpc", "http"] as const) {
    let targets = 0;
    const transport =
      transportKind === "grpc"
        ? createRouterTransport((router) => {
            router.service(HarnessService, {
              connectWorkspaceServices: (_request, context) => {
                targets += 1;
                return waitForAbort(context.signal);
              },
              getCompatibilityInfo: () => ({
                apiMajor: 1,
                capabilities: {},
                features: ["workspace_enrollment_test"],
              }),
              getSession: (request) => ({ session: { sessionId: request.sessionId } }),
            });
          })
        : createHttpTransport({
            baseUrl: "http://mecatl.test",
            fetch: async (input, init) => {
              const path = new URL(String(input)).pathname;
              if (path === "/v1/compatibility")
                return Response.json({
                  api_major: 1,
                  capabilities: {},
                  features: ["workspace_enrollment_test"],
                });
              if (path === `/v1/sessions/${sessionId}`)
                return Response.json({ session_id: sessionId });
              targets += 1;
              if (!(init?.signal instanceof AbortSignal)) throw new Error("missing signal");
              return waitForAbort(init.signal);
            },
          });
    const client = connect({ transport, transportKind });
    const session = await client.sessions.get(sessionId);

    const preAborted = new AbortController();
    preAborted.abort(new Error("pre-aborted"));
    await expect(
      session.connectWorkspaceServices({ signal: preAborted.signal }),
    ).rejects.toBeDefined();
    expect(targets).toBe(0);

    const midFlight = new AbortController();
    const aborted = session.connectWorkspaceServices({ signal: midFlight.signal });
    await spinUntil(() => targets === 1);
    midFlight.abort(new Error("mid-flight"));
    await expect(aborted).rejects.toMatchObject({ transport: transportKind });

    const deadline = session.connectWorkspaceServices({ timeoutMs: 1 });
    await expect(deadline).rejects.toMatchObject({ transport: transportKind });
    expect(targets).toBe(2);

    const closed = session.connectWorkspaceServices();
    await spinUntil(() => targets === 3);
    await client.close();
    await expect(closed).rejects.toMatchObject({ transport: transportKind });
    expect(targets).toBe(3);
  }

  for (const [code, status, expected] of [
    ["unauthenticated", Code.Unauthenticated, AuthenticationError],
    ["session_not_found", Code.NotFound, ServerError],
    ["mcp_connector_unavailable", Code.Unavailable, ServerError],
    ["failed_precondition", Code.FailedPrecondition, ServerError],
    ["internal", Code.Internal, ServerError],
  ] as const) {
    const requestId = `grpc-${code}`;
    const calls: string[] = [];
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          cancelWorkspaceEnrollment: () => {
            calls.push("cancel");
            return {};
          },
          connectWorkspaceServices: () => {
            calls.push(`connect:${code}`);
            throw statusError(code, status, requestId);
          },
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: ["workspace_enrollment_test"],
          }),
          getSession: (request) => ({ session: { sessionId: request.sessionId } }),
          listSessionMcpConnectors: () => {
            calls.push("inventory");
            return {};
          },
          retryWorkspaceEnrollment: () => {
            calls.push("retry");
            return {};
          },
        });
      }),
    });
    const session = await client.sessions.get(sessionId);
    let error: MecatlError | undefined;
    try {
      await session.connectWorkspaceServices();
    } catch (cause) {
      error = cause as MecatlError;
    }
    expect(error).toBeInstanceOf(expected);
    expect(error).toMatchObject({ requestId, transport: "grpc" });
    if (error === undefined) throw new Error("expected workspace enrollment error");
    if (expected === ServerError) expect(error.code).toBe(code);
    await drainEventLoop();
    expect(calls).toEqual([`connect:${code}`]);
    await client.close();
  }

  for (const [code, status, expected] of [
    ["unauthenticated", 401, AuthenticationError],
    ["session_not_found", 404, ServerError],
    ["mcp_connector_unavailable", 503, ServerError],
    ["failed_precondition", 412, ServerError],
    ["internal", 500, ServerError],
  ] as const) {
    const requestId = `http-${code}`;
    const calls: string[] = [];
    const client = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input) => {
        const path = new URL(String(input)).pathname;
        if (path === "/v1/compatibility")
          return Response.json({
            api_major: 1,
            capabilities: {},
            features: ["workspace_enrollment_test"],
          });
        if (path === `/v1/sessions/${sessionId}`) return Response.json({ session_id: sessionId });
        calls.push(path);
        return Response.json(
          { code, detail: "rejected", request_id: requestId },
          { headers: { "content-type": "application/problem+json" }, status },
        );
      },
    });
    const session = await client.sessions.get(sessionId);
    let error: MecatlError | undefined;
    try {
      await session.connectWorkspaceServices();
    } catch (cause) {
      error = cause as MecatlError;
    }
    expect(error).toBeInstanceOf(expected);
    expect(error).toMatchObject({ requestId, transport: "http" });
    if (error === undefined) throw new Error("expected workspace enrollment error");
    if (expected === ServerError) expect(error.code).toBe(code);
    await drainEventLoop();
    expect(calls).toEqual([`/v1/sessions/${sessionId}/workspace-enrollment/connect`]);
    await client.close();
  }

  const offlineCalls: string[] = [];
  const offline = connect({
    baseUrl: "http://mecatl.test",
    fetch: async (input) => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/compatibility")
        return Response.json({
          api_major: 1,
          capabilities: {},
          features: ["workspace_enrollment_test"],
        });
      if (path === `/v1/sessions/${sessionId}`) return Response.json({ session_id: sessionId });
      offlineCalls.push(path);
      throw new Error("offline");
    },
  });
  const offlineSession = await offline.sessions.get(sessionId);
  await expect(offlineSession.connectWorkspaceServices()).rejects.toBeInstanceOf(TransportError);
  await drainEventLoop();
  expect(offlineCalls).toEqual([`/v1/sessions/${sessionId}/workspace-enrollment/connect`]);
  await offline.close();
});

it("workspace enrollment lost acknowledgements do not trigger follow-up work", async () => {
  const calls: string[] = [];
  let recovered = false;
  const client = connect({
    transport: createRouterTransport((router) => {
      router.service(HarnessService, {
        cancelWorkspaceEnrollment: (request) => {
          calls.push(`cancel:${request.enrollmentId}`);
          throw new ConnectError("cancel acknowledgement lost", Code.Unavailable);
        },
        connectWorkspaceServices: () => {
          calls.push("connect:pending-1");
          throw new ConnectError("connect acknowledgement lost", Code.Unavailable);
        },
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: {},
          features: ["workspace_enrollment_test"],
        }),
        getSession: (request) => ({ session: { sessionId: request.sessionId } }),
        listSessionMcpConnectors: () => {
          calls.push("inventory");
          return {};
        },
        retryWorkspaceEnrollment: (request) => {
          calls.push(`retry:${request.enrollmentId}`);
          if (!recovered) {
            recovered = true;
            return {
              enrollmentId: "pending-2",
              presentationUrl: "https://example.com/replacement",
              requiredServices: 2,
              status: "pending",
            };
          }
          throw new ConnectError("retry acknowledgement lost", Code.Unavailable);
        },
      });
    }),
  });
  const session = await client.sessions.get(sessionId);

  await expect(session.connectWorkspaceServices()).rejects.toBeInstanceOf(TransportError);
  await drainEventLoop();
  expect(calls).toEqual(["connect:pending-1"]);

  await expect(session.retryWorkspaceEnrollment("pending-1")).resolves.toEqual({
    enrollmentId: "pending-2",
    presentationUrl: "https://example.com/replacement",
    requiredServices: 2,
    status: WorkspaceEnrollmentStatus.Pending,
  });
  await drainEventLoop();
  expect(calls).toEqual(["connect:pending-1", "retry:pending-1"]);

  await expect(session.retryWorkspaceEnrollment("pending-2")).rejects.toBeInstanceOf(
    TransportError,
  );
  await expect(session.cancelWorkspaceEnrollment("pending-2")).rejects.toBeInstanceOf(
    TransportError,
  );
  await drainEventLoop();
  expect(calls).toEqual([
    "connect:pending-1",
    "retry:pending-1",
    "retry:pending-2",
    "cancel:pending-2",
  ]);
  await client.close();
});
