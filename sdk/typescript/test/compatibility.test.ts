import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";
import { credentialInterceptor } from "../src/credentials.js";
import { AuthenticationError, ProtocolError, TransportError } from "../src/errors.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  createHttpTransport,
  createRawClient,
  IncompatibleServerError,
  SESSION_ID_HEADER_NAME,
  withSessionAffinity,
} from "../src/index.js";
import { invalidateRawCompatibility } from "../src/raw.js";
import { fetchFor, type RecordedRequest, scriptedState } from "./scripted-state.js";

describe("compatibility floor", () => {
  it("TestTypeScriptRawHelperCompatibility", async () => {
    const sessionId = "Session-Affinity_opaque.01";
    const grpcHeaders: Headers[] = [];
    const grpcTransport = createRouterTransport(
      (router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: (_request, context) => {
            grpcHeaders.push(new Headers(context.requestHeader));
            return { apiMajor: 2, capabilities: {}, features: ["server_info"] };
          },
          getSession: (_request, context) => {
            grpcHeaders.push(new Headers(context.requestHeader));
            return {};
          },
          streamSessionEvents: async function* (_request, context) {
            grpcHeaders.push(new Headers(context.requestHeader));
            yield { type: "result" };
          },
        });
      },
      {
        transport: {
          interceptors: [
            credentialInterceptor({ headers: { authorization: "Bearer grpc-credential" } }),
          ],
        },
      },
    );
    const grpc = createRawClient({ transport: grpcTransport });
    const grpcOptions = withSessionAffinity(sessionId, { headers: { "x-caller": "grpc" } });
    await grpc.unary(HarnessService.method.getSession, { sessionId }, grpcOptions);
    async function* grpcRequests() {
      yield { sessionId };
    }
    for await (const _event of grpc.stream(
      HarnessService.method.streamSessionEvents,
      grpcRequests(),
      grpcOptions,
    )) {
      // Consume the fixture stream.
    }

    expect(SESSION_ID_HEADER_NAME).toBe("X-Mecatl-Session-ID");
    expect(grpcHeaders).toHaveLength(3);
    expect(grpcHeaders[0]?.has(SESSION_ID_HEADER_NAME)).toBe(false);
    expect(grpcHeaders[0]?.get("authorization")).toBe("Bearer grpc-credential");
    for (const headers of grpcHeaders.slice(1)) {
      expect(headers.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
      expect(headers.get("x-caller")).toBe("grpc");
      expect(headers.get("authorization")).toBe("Bearer grpc-credential");
    }

    const httpRequests: RecordedRequest[] = [];
    const http = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: fetchFor({ ...scriptedState, sessionId }, httpRequests),
        headers: { authorization: "Bearer http-credential" },
      }),
    });
    const httpOptions = withSessionAffinity(sessionId, { headers: { "x-caller": "http" } });
    await http.unary(HarnessService.method.getSession, { sessionId }, httpOptions);
    async function* httpRequestsForStream() {
      yield { sessionId };
    }
    for await (const _event of http.stream(
      HarnessService.method.streamSessionEvents,
      httpRequestsForStream(),
      httpOptions,
    )) {
      // Consume the fixture stream.
    }
    await http.unary(HarnessService.method.getSession, { sessionId });

    expect(httpRequests).toHaveLength(4);
    expect(httpRequests[0]?.headers.has(SESSION_ID_HEADER_NAME)).toBe(false);
    expect(httpRequests[0]?.headers.get("authorization")).toBe("Bearer http-credential");
    for (const request of httpRequests.slice(1, 3)) {
      expect(request.headers.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
      expect(request.headers.get("x-caller")).toBe("http");
      expect(request.headers.get("authorization")).toBe("Bearer http-credential");
    }
    expect(httpRequests[3]?.headers.has(SESSION_ID_HEADER_NAME)).toBe(false);

    for (const illegal of ["session\tid", "session-α", " session", "session ", "x".repeat(257)]) {
      const callerHeaders = {
        authorization: "Bearer kept",
        [SESSION_ID_HEADER_NAME]: "caller-value",
      };
      expect(() => withSessionAffinity(illegal, { headers: callerHeaders })).toThrow(
        /session affinity.*printable ASCII.*boundary spaces/i,
      );
      expect(callerHeaders[SESSION_ID_HEADER_NAME]).toBe("caller-value");
    }
  });

  it("missing or incompatible server info fails the floor", async () => {
    let missingCreated = 0;
    const missing = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => {
          missingCreated += 1;
          return { sessionId: "must-not-exist" };
        },
      });
    });

    let incompatibleCreated = 0;
    const incompatible = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: () => {
          incompatibleCreated += 1;
          return { sessionId: "must-not-exist" };
        },
        getCompatibilityInfo: () => ({ apiMajor: 3 }),
      });
    });

    await expect(
      createRawClient({ transport: missing }).unary(HarnessService.method.createSession, {}),
    ).rejects.toBeInstanceOf(IncompatibleServerError);
    await expect(
      createRawClient({ transport: incompatible }).unary(HarnessService.method.createSession, {}),
    ).rejects.toBeInstanceOf(IncompatibleServerError);
    expect({ incompatibleCreated, missingCreated }).toEqual({
      incompatibleCreated: 0,
      missingCreated: 0,
    });
  });

  it.each([
    { httpStatus: 404, httpCode: "unknown", problem: { status: 404 } },
    { httpStatus: 501, httpCode: "unimplemented", problem: { status: 501, code: "unimplemented" } },
  ])(
    "missing descriptors (HTTP $httpStatus) block unary and streaming RPCs",
    async ({ httpStatus, httpCode, problem }) => {
      let grpcRequested = 0;
      const grpc = createRawClient({
        transport: createRouterTransport((router) => {
          router.service(HarnessService, {
            getSession: () => {
              grpcRequested += 1;
              return {};
            },
            streamSessionEvents: async function* () {
              grpcRequested += 1;
              yield { type: "result" };
            },
          });
        }),
      });

      await expect(grpc.unary(HarnessService.method.getSession, {})).rejects.toMatchObject({
        cause: expect.objectContaining({
          cause: expect.any(ConnectError),
          code: "unknown",
          status: Code.Unimplemented,
          transport: "grpc",
        }),
        code: "incompatible_server",
        status: undefined,
        message: "server does not support compatibility checking; update server",
        transport: "grpc",
      });
      await expect(
        consume(grpc.stream(HarnessService.method.streamSessionEvents, emptyRequests())),
      ).rejects.toMatchObject({
        cause: expect.objectContaining({
          cause: expect.any(ConnectError),
          code: "unknown",
          status: Code.Unimplemented,
          transport: "grpc",
        }),
        code: "incompatible_server",
        status: undefined,
        message: "server does not support compatibility checking; update server",
        transport: "grpc",
      });
      expect(grpcRequested).toBe(0);

      const httpRequests: RecordedRequest[] = [];
      const http = createRawClient({
        transport: createHttpTransport({
          baseUrl: "http://mecatl.test",
          fetch: async (input, init) => {
            httpRequests.push({
              credentials: init?.credentials,
              headers: new Headers(init?.headers),
              method: init?.method ?? "GET",
              url: String(input),
            });
            return Response.json(problem, { status: httpStatus });
          },
        }),
      });
      await expect(http.unary(HarnessService.method.getSession, {})).rejects.toMatchObject({
        cause: expect.objectContaining({
          cause: problem,
          code: httpCode,
          status: httpStatus,
          transport: "http",
        }),
        code: "incompatible_server",
        status: undefined,
        message: "server does not support compatibility checking; update server",
        transport: "http",
      });
      await expect(
        consume(http.stream(HarnessService.method.streamSessionEvents, emptyRequests())),
      ).rejects.toMatchObject({
        cause: expect.objectContaining({
          cause: problem,
          code: httpCode,
          status: httpStatus,
          transport: "http",
        }),
        code: "incompatible_server",
        status: undefined,
        message: "server does not support compatibility checking; update server",
        transport: "http",
      });
      expect(httpRequests).toHaveLength(2);
      expect(
        httpRequests.every((request) => new URL(request.url).pathname === "/v1/compatibility"),
      ).toBe(true);
    },
  );

  it.each([
    {
      apiMajor: 1,
      message: "server API is older than this SDK (expected major 2, observed 1); update server",
    },
    {
      apiMajor: 3,
      message: "server API is newer than this SDK (expected major 2, observed 3); update SDK",
    },
    { apiMajor: 0, message: "server returned invalid compatibility information" },
    { apiMajor: -1, message: "server returned invalid compatibility information" },
  ])("blocks unary and streaming RPCs for API major $apiMajor", async ({ apiMajor, message }) => {
    let grpcRequested = 0;
    const grpc = createRawClient({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({ apiMajor }),
          getSession: () => {
            grpcRequested += 1;
            return {};
          },
          streamSessionEvents: async function* () {
            grpcRequested += 1;
            yield { type: "result" };
          },
        });
      }),
    });
    await expect(grpc.unary(HarnessService.method.getSession, {})).rejects.toMatchObject({
      code: "incompatible_server",
      message,
      transport: "grpc",
    });
    await expect(
      consume(grpc.stream(HarnessService.method.streamSessionEvents, emptyRequests())),
    ).rejects.toMatchObject({ code: "incompatible_server", message, transport: "grpc" });
    expect(grpcRequested).toBe(0);

    const httpRequests: RecordedRequest[] = [];
    const http = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: async (input, init) => {
          const url = String(input);
          httpRequests.push({
            credentials: init?.credentials,
            headers: new Headers(init?.headers),
            method: init?.method ?? "GET",
            url,
          });
          if (new URL(url).pathname === "/v1/compatibility") {
            return Response.json({
              api_major: apiMajor,
              capabilities: {},
              features: ["future_feature"],
            });
          }
          throw new Error("the requested RPC must not execute");
        },
      }),
    });
    await expect(http.unary(HarnessService.method.getSession, {})).rejects.toMatchObject({
      code: "incompatible_server",
      message,
      transport: "http",
    });
    await expect(
      consume(http.stream(HarnessService.method.streamSessionEvents, emptyRequests())),
    ).rejects.toMatchObject({ code: "incompatible_server", message, transport: "http" });
    expect(httpRequests).toHaveLength(2);
  });

  it.each([
    { body: '{"secret-canary":', kind: "JSON parse" },
    { body: '{"api_major":"secret-canary"}', kind: "protobuf schema" },
  ])(
    "rejects malformed HTTP descriptors ($kind) without disclosing decoder input",
    async ({ body }) => {
      const requests: RecordedRequest[] = [];
      const client = createRawClient({
        transport: createHttpTransport({
          baseUrl: "http://mecatl.test",
          fetch: async (input, init) => {
            requests.push({
              credentials: init?.credentials,
              headers: new Headers(init?.headers),
              method: init?.method ?? "GET",
              url: String(input),
            });
            return new Response(body, { headers: { "x-request-id": "probe-request" } });
          },
        }),
      });
      for (const operation of [
        () => client.unary(HarnessService.method.getSession, {}),
        () => consume(client.stream(HarnessService.method.streamSessionEvents, emptyRequests())),
      ]) {
        const error = await operation().catch((error: unknown) => error);
        expect(error).toBeInstanceOf(IncompatibleServerError);
        expect(error).toMatchObject({
          code: "incompatible_server",
          message: "server returned invalid compatibility information",
          transport: "http",
          cause: {
            code: "protocol",
            requestId: "probe-request",
            status: 200,
            transport: "http",
          },
        });
        expect(error).toHaveProperty("cause", expect.any(ProtocolError));
        expect((error as IncompatibleServerError).cause).not.toHaveProperty("cause");
        expect(JSON.stringify(error)).not.toContain("secret-canary");
        expect(String((error as IncompatibleServerError).cause)).not.toContain("secret-canary");
      }
      expect(requests).toHaveLength(2);
      expect(
        requests.every((request) => new URL(request.url).pathname === "/v1/compatibility"),
      ).toBe(true);
    },
  );

  describe.each(["grpc", "http"] as const)("%s probe failures", (transportKind) => {
    it.each(["authentication", "network"] as const)("retries after %s failure", async (failure) => {
      let probes = 0;
      let requested = 0;
      const grpcCause = new ConnectError(
        "probe failed",
        failure === "authentication" ? Code.Unauthenticated : Code.Unavailable,
      );
      const httpCause = failure === "authentication" ? { status: 401 } : new TypeError("offline");
      const successfulFetch = fetchFor();
      const client = createRawClient({
        transport:
          transportKind === "grpc"
            ? createRouterTransport((router) => {
                router.service(HarnessService, {
                  getCompatibilityInfo: () => {
                    probes += 1;
                    if (probes % 2 === 1) throw grpcCause;
                    return { apiMajor: 2 };
                  },
                  getSession: () => {
                    requested += 1;
                    return {};
                  },
                  streamSessionEvents: async function* () {
                    requested += 1;
                    yield { type: "result" };
                  },
                });
              })
            : createHttpTransport({
                baseUrl: "http://mecatl.test",
                fetch: async (input, init) => {
                  if (new URL(String(input)).pathname === "/v1/compatibility") {
                    probes += 1;
                    if (probes % 2 === 1) {
                      if (failure === "authentication")
                        return Response.json(httpCause, { status: 401 });
                      throw httpCause;
                    }
                  } else {
                    requested += 1;
                  }
                  return successfulFetch(input, init);
                },
              }),
      });
      for (const operation of [
        () =>
          client.unary(HarnessService.method.getSession, { sessionId: scriptedState.sessionId }),
        () => consume(client.stream(HarnessService.method.streamSessionEvents, sessionRequests())),
      ]) {
        // Explicitly clear the successful generation to exercise each operation's failure boundary.
        invalidateRawCompatibility(client);
        const requestedBefore = requested;
        const error = await operation().catch((error: unknown) => error);
        expect(error).toBeInstanceOf(
          failure === "authentication" ? AuthenticationError : TransportError,
        );
        expect(error).not.toBeInstanceOf(IncompatibleServerError);
        expect(error).toMatchObject({
          cause:
            transportKind === "grpc"
              ? expect.objectContaining({ code: grpcCause.code, rawMessage: "probe failed" })
              : httpCause,
          code: failure === "authentication" ? "authentication" : "transport",
          status:
            transportKind === "grpc"
              ? grpcCause.code
              : failure === "authentication"
                ? 401
                : undefined,
          transport: transportKind,
        });
        if (transportKind === "grpc") {
          expect(error).toHaveProperty("cause", expect.any(ConnectError));
        } else if (failure === "network") {
          expect((error as TransportError).cause).toBe(httpCause);
        }
        expect(requested).toBe(requestedBefore);
        await operation();
        expect(requested).toBe(requestedBefore + 1);
      }
      expect({ probes, requested }).toEqual({ probes: 4, requested: 2 });
    });
  });

  it.each(["network", "abort"] as const)(
    "retries HTTP body-read %s failures without reclassification",
    async (failure) => {
      const cause =
        failure === "network"
          ? new TypeError("connection lost")
          : new DOMException("aborted", "AbortError");
      let probes = 0;
      const requests: RecordedRequest[] = [];
      const successfulFetch = fetchFor(scriptedState, requests);
      const client = createRawClient({
        transport: createHttpTransport({
          baseUrl: "http://mecatl.test",
          fetch: async (input, init) => {
            if (new URL(String(input)).pathname === "/v1/compatibility") {
              probes += 1;
              if (probes % 2 === 1) {
                const response = Response.json({ api_major: 2 });
                vi.spyOn(response, "text").mockRejectedValue(cause);
                return response;
              }
            }
            return successfulFetch(input, init);
          },
        }),
      });
      for (const operation of [
        () =>
          client.unary(HarnessService.method.getSession, { sessionId: scriptedState.sessionId }),
        () => consume(client.stream(HarnessService.method.streamSessionEvents, sessionRequests())),
      ]) {
        invalidateRawCompatibility(client);
        const requestsBefore = requests.length;
        const error = await operation().catch((error: unknown) => error);
        expect(error).toBeInstanceOf(ProtocolError);
        expect(error).not.toBeInstanceOf(IncompatibleServerError);
        expect(error).toMatchObject({ code: "protocol", status: 200, transport: "http" });
        expect((error as ProtocolError).cause).toBe(cause);
        expect(requests).toHaveLength(requestsBefore);
        await operation();
        expect(requests).toHaveLength(requestsBefore + 2);
      }
      expect(probes).toBe(4);
    },
  );

  it("retries a failed compatibility generation", async () => {
    let probes = 0;
    let requested = 0;
    const client = createRawClient({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => {
            probes += 1;
            if (probes === 1) throw new ConnectError("temporarily unavailable", Code.Unavailable);
            return { apiMajor: 2, capabilities: {}, features: ["future_feature"] };
          },
          getSession: () => {
            requested += 1;
            return {};
          },
        });
      }),
    });

    await expect(client.unary(HarnessService.method.getSession, {})).rejects.toBeInstanceOf(
      TransportError,
    );
    await client.unary(HarnessService.method.getSession, {});
    expect({ probes, requested }).toEqual({ probes: 2, requested: 1 });
  });
});

async function consume(stream: AsyncIterable<unknown>): Promise<void> {
  for await (const _ of stream) {
    // Consume the fixture stream.
  }
}

async function* sessionRequests(): AsyncIterable<{ sessionId: string }> {
  yield { sessionId: scriptedState.sessionId };
}

async function* emptyRequests(): AsyncIterable<Record<string, never>> {
  yield {};
}
