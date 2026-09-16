import {
  create,
  type DescMessage,
  type DescMethodStreaming,
  type DescMethodUnary,
  type MessageInitShape,
  type MessageShape,
} from "@bufbuild/protobuf";
import type { ContextValues, StreamResponse, Transport, UnaryResponse } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { ScheduleService } from "../src/gen/mecatl/v1/schedule_pb.js";
import { createRawClient } from "../src/raw.js";
import { HTTP_ONLY_CONTROLS, RPC_CATALOG, resolveHTTPOnlyControl } from "../src/rpc-catalog.js";

class CatalogTransport implements Transport {
  readonly calls: string[] = [];

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    _signal: AbortSignal | undefined,
    _timeoutMs: number | undefined,
    _header: HeadersInit | undefined,
    _input: MessageInitShape<I>,
    _contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    this.calls.push(method.name);
    const message = create(
      method.output,
      (method.name === "GetCompatibilityInfo"
        ? { apiMajor: 1, capabilities: {}, features: ["server_info"] }
        : {}) as MessageInitShape<O>,
    );
    return {
      header: new Headers(),
      message,
      method,
      service: method.parent,
      stream: false,
      trailer: new Headers(),
    };
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    _signal: AbortSignal | undefined,
    _timeoutMs: number | undefined,
    _header: HeadersInit | undefined,
    _input: AsyncIterable<MessageInitShape<I>>,
    _contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    this.calls.push(method.name);
    const message = (async function* (): AsyncIterable<MessageShape<O>> {
      yield create(method.output);
    })();
    return {
      header: new Headers(),
      message,
      method,
      service: method.parent,
      stream: true,
      trailer: new Headers(),
    };
  }
}

async function* singleValue<T>(value: T): AsyncIterable<T> {
  yield value;
}

describe("RPC transport catalog", () => {
  it("all 84 raw RPCs are callable through the public raw seam", async () => {
    const transport = new CatalogTransport();
    const raw = createRawClient({ transport });

    for (const entry of Object.values(RPC_CATALOG)) {
      const input = create(entry.grpc.descriptor.input);
      if (entry.shape === "unary") {
        await raw.unary(entry.grpc.descriptor as DescMethodUnary, input);
        continue;
      }
      for await (const _message of raw.stream(
        entry.grpc.descriptor as DescMethodStreaming,
        singleValue(input),
      )) {
        // One yielded message proves this descriptor crossed the public raw seam.
      }
    }

    expect(transport.calls).toHaveLength(84);
    expect(transport.calls.sort()).toEqual(
      Object.values(RPC_CATALOG)
        .map(({ method }) => method)
        .sort(),
    );
  });

  it("HTTP-only controls do not satisfy RPC coverage", () => {
    const descriptorKeys = [
      ...Object.values(HarnessService.method).map((method) => `HarnessService.${method.name}`),
      ...Object.values(ScheduleService.method).map((method) => `ScheduleService.${method.name}`),
    ];
    expect(descriptorKeys).toHaveLength(84);
    expect(Object.keys(RPC_CATALOG).sort()).toEqual(descriptorKeys.sort());

    const omitted = "HarnessService.GetSession";
    const candidateCoverage = new Set(Object.keys(RPC_CATALOG).filter((key) => key !== omitted));
    for (const controlName of Object.keys(HTTP_ONLY_CONTROLS)) {
      candidateCoverage.add(controlName);
    }

    expect(descriptorKeys.filter((key) => !candidateCoverage.has(key))).toEqual([omitted]);
    expect(Object.values(HTTP_ONLY_CONTROLS).every((entry) => entry.kind === "http")).toBe(true);
    expect(resolveHTTPOnlyControl("cancel", { session_id: "a session" })).toMatchObject({
      method: "POST",
      path: "/v1/sessions/a%20session/cancel",
      requestBody: "optional-json",
      response: "json",
    });
    expect(() => resolveHTTPOnlyControl("cancel", {})).toThrowError(
      "Missing HTTP control path field session_id",
    );
  });
});
