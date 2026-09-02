import { describe, expect, it } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { createHttpTransport, createRawClient, getRawJson } from "../src/index.js";
import { fetchFor, routerFor, scriptedState } from "./scripted-state.js";

describe("raw transport parity", () => {
  it("raw operations agree across gRPC and HTTP", async () => {
    const grpc = createRawClient({ transport: routerFor() });
    const http = createRawClient({
      transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch: fetchFor() }),
    });

    const [grpcCompatibility, httpCompatibility] = await Promise.all([
      grpc.unary(HarnessService.method.getCompatibilityInfo, {}),
      http.unary(HarnessService.method.getCompatibilityInfo, {}),
    ]);
    expect(httpCompatibility.features).toEqual(grpcCompatibility.features);

    const [grpcCreated, httpCreated] = await Promise.all([
      grpc.unary(HarnessService.method.createSession, {}),
      http.unary(HarnessService.method.createSession, {}),
    ]);
    expect(httpCreated.sessionId).toBe(grpcCreated.sessionId);

    const [grpcSession, httpSession] = await Promise.all([
      grpc.unary(HarnessService.method.getSession, { sessionId: grpcCreated.sessionId }),
      http.unary(HarnessService.method.getSession, { sessionId: httpCreated.sessionId }),
    ]);
    expect(grpcSession.session?.placement).toMatchObject(scriptedState.placement);
    expect({
      mode: httpSession.session?.mode,
      placement: httpSession.session?.placement,
      sessionId: httpSession.session?.sessionId,
      state: httpSession.session?.state,
    }).toEqual({
      mode: grpcSession.session?.mode,
      placement: grpcSession.session?.placement,
      sessionId: grpcSession.session?.sessionId,
      state: grpcSession.session?.state,
    });
  });

  it("unknown fields survive the raw seam", async () => {
    const grpc = createRawClient({ transport: routerFor() });
    const http = createRawClient({
      transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch: fetchFor() }),
    });
    const [grpcInfo, httpInfo] = await Promise.all([
      grpc.unary(HarnessService.method.getCompatibilityInfo, {}),
      http.unary(HarnessService.method.getCompatibilityInfo, {}),
    ]);

    expect(grpcInfo.features).toContain("future_transport");
    expect(httpInfo.features).toContain("future_transport");
    expect(getRawJson(httpInfo)).toMatchObject({ future_field: "kept-verbatim" });
  });
});
