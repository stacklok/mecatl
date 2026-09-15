import { createRouterTransport } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";
import { credentialInterceptor } from "../src/credentials.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { AuthenticationError, createHttpTransport, createRawClient } from "../src/index.js";
import { fetchFor, type RecordedRequest, scriptedState } from "./scripted-state.js";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("transport credentials", () => {
  it("credential providers attach headers and never leak", async () => {
    const secret = "credential-that-must-stay-secret";
    const grpcHeaders: Headers[] = [];
    const grpcTransport = createRouterTransport(
      (router) => {
        router.service(HarnessService, {
          createSession: (_request, context) => {
            grpcHeaders.push(new Headers(context.requestHeader));
            return { sessionId: scriptedState.sessionId };
          },
          getCompatibilityInfo: (_request, context) => {
            grpcHeaders.push(new Headers(context.requestHeader));
            return { apiMajor: 1, capabilities: {}, features: ["server_info"] };
          },
        });
      },
      {
        transport: {
          interceptors: [
            credentialInterceptor({
              credentialProvider: async () => ({ authorization: `Bearer ${secret}` }),
              headers: { "x-static": "static" },
            }),
          ],
        },
      },
    );
    const grpc = createRawClient({ transport: grpcTransport });
    await grpc.unary(HarnessService.method.createSession, {});
    expect(grpcHeaders).toHaveLength(2);
    for (const headers of grpcHeaders) {
      expect(headers.get("authorization")).toBe(`Bearer ${secret}`);
      expect(headers.get("x-static")).toBe("static");
    }

    const requests: RecordedRequest[] = [];
    const http = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        credentialProvider: async () => ({ authorization: `Bearer ${secret}` }),
        credentials: "include",
        fetch: fetchFor(scriptedState, requests),
        headers: { "x-static": "static" },
      }),
    });
    await http.unary(HarnessService.method.createSession, {});
    expect(requests).toHaveLength(2);
    for (const request of requests) {
      expect(request.headers.get("authorization")).toBe(`Bearer ${secret}`);
      expect(request.headers.get("x-static")).toBe("static");
      expect(request.credentials).toBe("include");
    }

    const rejected = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        credentialProvider: async () => {
          throw new Error(secret);
        },
        fetch: fetchFor(),
      }),
    });
    const error = await rejected
      .unary(HarnessService.method.getCompatibilityInfo, {})
      .catch((cause) => cause);
    expect(error).toBeInstanceOf(AuthenticationError);
    expect(error.message).not.toContain(secret);
    expect(JSON.stringify(error)).not.toContain(secret);
  });

  it("pluggable fetch owns every HTTP request", async () => {
    const globalFetch = vi.fn(() => {
      throw new Error("global fetch must not run");
    });
    vi.stubGlobal("fetch", globalFetch);
    const requests: RecordedRequest[] = [];
    const client = createRawClient({
      transport: createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: fetchFor(scriptedState, requests),
      }),
    });

    await client.unary(HarnessService.method.createSession, {});
    expect(requests.map((request) => new URL(request.url).pathname)).toEqual([
      "/v1/compatibility",
      "/v1/sessions",
    ]);
    expect(globalFetch).not.toHaveBeenCalled();
  });
});
