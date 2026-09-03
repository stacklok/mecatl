import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { credentialInterceptor } from "../src/credentials.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  createHttpTransport,
  createRawClient,
  IncompatibleServerError,
  SESSION_ID_HEADER_NAME,
  withSessionAffinity,
} from "../src/index.js";
import { fetchFor, type RecordedRequest, scriptedState } from "./scripted-state.js";

describe("compatibility floor", () => {
  it("TestADR_0290_TypeScriptRawHelperCompatibility", async () => {
    const sessionId = "Session-Affinity_opaque.01";
    const grpcHeaders: Headers[] = [];
    const grpcTransport = createRouterTransport(
      (router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: (_request, context) => {
            grpcHeaders.push(new Headers(context.requestHeader));
            return { apiMajor: 1 };
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
    for (const request of httpRequests.slice(1, 3)) {
      expect(request.headers.get(SESSION_ID_HEADER_NAME)).toBe(sessionId);
      expect(request.headers.get("x-caller")).toBe("http");
      expect(request.headers.get("authorization")).toBe("Bearer http-credential");
    }
    expect(httpRequests[3]?.headers.has(SESSION_ID_HEADER_NAME)).toBe(false);

    for (const illegal of ["session\tid", "session-α", " session", "session "]) {
      const options = withSessionAffinity(illegal, { headers: { authorization: "Bearer kept" } });
      const headers = new Headers(options.headers);
      expect(headers.has(SESSION_ID_HEADER_NAME)).toBe(false);
      expect(headers.get("authorization")).toBe("Bearer kept");
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
        getCompatibilityInfo: () => ({ apiMajor: 2 }),
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
});
