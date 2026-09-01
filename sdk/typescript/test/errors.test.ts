import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { normalizeError } from "../src/errors.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { createHttpTransport, createRawClient, ServerError } from "../src/index.js";
import { fetchFor } from "./scripted-state.js";

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function errorInfo(reason: string, requestId: string): Uint8Array {
  const metadata = [...field(1, bytes("request_id")), ...field(2, bytes(requestId))];
  return new Uint8Array([
    ...field(1, bytes(reason)),
    ...field(2, bytes("mecatl.stacklok.com")),
    ...field(3, metadata),
  ]);
}

describe("typed error normalization", () => {
  it("problem+json and gRPC status details normalize to one typed error", async () => {
    const requestId = "request-42";
    const httpFetch: typeof globalThis.fetch = async (input, init) => {
      if (new URL(String(input)).pathname === "/v1/compatibility") return fetchFor()(input, init);
      return Response.json(
        {
          code: "session_not_found",
          detail: "session missing",
          request_id: requestId,
          status: 404,
          title: "Session not found",
          type: "https://mecatl.stacklok.com/problems/session_not_found",
        },
        { headers: { "content-type": "application/problem+json" }, status: 404 },
      );
    };
    const http = createRawClient({
      transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch: httpFetch }),
    });
    const httpError = await http
      .unary(HarnessService.method.getSession, { sessionId: "missing" })
      .catch((error) => error);

    const connectError = new ConnectError("session missing", Code.NotFound);
    (connectError.details as unknown[]).push({
      type: "google.rpc.ErrorInfo",
      value: errorInfo("session_not_found", requestId),
    });
    const grpcError = normalizeError(connectError, "grpc");

    expect(httpError).toBeInstanceOf(ServerError);
    expect(grpcError).toBeInstanceOf(ServerError);
    expect({
      code: httpError.code,
      requestId: httpError.requestId,
      transport: httpError.transport,
    }).toEqual({
      code: grpcError.code,
      requestId: grpcError.requestId,
      transport: "http",
    });
    expect(grpcError.cause).toBe(connectError);
    expect(httpError.cause).toBeDefined();
  });
});
