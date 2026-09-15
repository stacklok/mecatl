import {
  create,
  type DescMessage,
  type DescMethodStreaming,
  type DescMethodUnary,
  fromJson,
  type JsonValue,
  type MessageInitShape,
  type MessageShape,
} from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  type ContextValues,
  type StreamResponse,
  type Transport,
  type UnaryResponse,
} from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import * as browserEntry from "../src/index.js";
import * as nodeEntry from "../src/node.js";
import type { RawClient } from "../src/raw.js";
import { RPC_CATALOG, type RPCCatalogEntry, resolveHTTPRoute } from "../src/rpc-catalog.js";

async function* singleValue<T>(value: T): AsyncIterable<T> {
  yield value;
}

async function drain<T>(values: AsyncIterable<T>): Promise<void> {
  for await (const _value of values) {
    // Reaching one decoded value is the assertion made by the caller.
  }
}

function codedError(code: string, status: Code): ConnectError {
  const bytes = (value: string): number[] => [...new TextEncoder().encode(value)];
  const field = (number: number, value: number[]): number[] => [
    (number << 3) | 2,
    value.length,
    ...value,
  ];
  const error = new ConnectError(code, status);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([...field(1, bytes(code)), ...field(2, bytes("mecatl.stacklok.com"))]),
  });
  return error;
}

type RecordedCall = {
  readonly headers: Headers;
  readonly method: string;
  readonly timeoutMs: number | undefined;
};

class RecordingTransport implements Transport {
  readonly calls: RecordedCall[] = [];

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    _signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    _input: MessageInitShape<I>,
    _contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    this.calls.push({ headers: new Headers(header), method: method.name, timeoutMs });
    const message = create(
      method.output,
      (method.name === "GetCompatibilityInfo"
        ? { apiMajor: 1, capabilities: {}, features: ["server_info"] }
        : {}) as MessageInitShape<O>,
    );
    return {
      header: new Headers({ "x-test-response": "header" }),
      message,
      method,
      service: method.parent,
      stream: false,
      trailer: new Headers({ "x-test-response": "trailer" }),
    };
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    _signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    _input: AsyncIterable<MessageInitShape<I>>,
    _contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    this.calls.push({ headers: new Headers(header), method: method.name, timeoutMs });
    const message = (async function* (): AsyncIterable<MessageShape<O>> {
      yield create(method.output);
    })();
    return {
      header: new Headers({ "x-test-response": "header" }),
      message,
      method,
      service: method.parent,
      stream: true,
      trailer: new Headers({ "x-test-response": "trailer" }),
    };
  }
}

function mappingSource(mapping: string): string {
  const separator = mapping.indexOf("=");
  return separator < 0 ? mapping : mapping.slice(separator + 1);
}

function setJsonPath(target: Record<string, JsonValue>, path: string, value: JsonValue): void {
  const segments = path.split(".");
  let current = target;
  for (const segment of segments.slice(0, -1)) {
    const child: Record<string, JsonValue> = {};
    current[segment] = child;
    current = child;
  }
  current[segments.at(-1) ?? ""] = value;
}

function requestFor(entry: RPCCatalogEntry): MessageShape<DescMessage> {
  const json: Record<string, JsonValue> = {};
  if (entry.http.kind === "http") {
    for (const mapping of [...entry.http.pathParameters, ...entry.http.queryParameters]) {
      const source = mappingSource(mapping);
      if (source.startsWith("@")) continue;
      const numeric = source === "limit" || source === "page_size";
      setJsonPath(json, source, numeric ? 1 : "value / with spaces");
    }
  }
  return fromJson(entry.grpc.descriptor.input, json, { ignoreUnknownFields: true });
}

async function invoke(raw: RawClient, entry: RPCCatalogEntry): Promise<void> {
  const request = requestFor(entry);
  const options = {
    headers: { "x-raw-option": "preserved" },
    timeoutMs: 4_000,
  };
  if (entry.shape === "unary") {
    await raw.unary(entry.grpc.descriptor as DescMethodUnary, request, options);
    return;
  }
  await drain(
    raw.stream(entry.grpc.descriptor as DescMethodStreaming, singleValue(request), options),
  );
}

describe("complete HTTP route transport", () => {
  it("path query body and response codecs preserve the reviewed HTTP wire shapes", () => {
    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.ReadMcpResource"].grpc.descriptor, {
        server: "primary / tools",
        uri: "file://notes/a b?rev=1",
      }),
    ).toMatchObject({
      body: undefined,
      method: "GET",
      path: "/v1/mcp/resources/read?server=primary+%2F+tools&uri=file%3A%2F%2Fnotes%2Fa+b%3Frev%3D1",
    });

    const schedule = {
      cron: "0 9 * * *",
      name: "daily / review",
    };
    expect(
      resolveHTTPRoute(RPC_CATALOG["ScheduleService.UpdateSchedule"].grpc.descriptor, {
        spec: schedule,
      }),
    ).toMatchObject({
      body: schedule,
      method: "PUT",
      path: "/v1/schedules/daily%20%2F%20review",
    });
    expect(
      resolveHTTPRoute(RPC_CATALOG["ScheduleService.GetFire"].grpc.descriptor, {
        fire_id: "fire / one",
      }),
    ).toMatchObject({
      body: undefined,
      method: "GET",
      path: "/v1/schedules/unused/fires/fire%20%2F%20one",
    });

    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.ClearSession"].grpc.descriptor, {
        source_session_id: "session/one",
      }),
    ).toMatchObject({
      body: undefined,
      path: "/v1/sessions/session%2Fone/clear",
    });
    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.ClearSession"].grpc.descriptor, {
        source_session_id: "session/one",
        worktree_selector: "branch:next",
      }),
    ).toMatchObject({ body: { worktree_selector: "branch:next" } });

    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.WatchSessionEvents"].grpc.descriptor, {
        cursor: "1 / 2",
        run_id: "run/one",
        session_id: "session/one",
      }),
    ).toMatchObject({
      body: undefined,
      path: "/v1/sessions/session%2Fone/watch?cursor=1+%2F+2&run_id=run%2Fone",
    });
    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.DiffLearnedSkillVersions"].grpc.descriptor, {
        from_version: "v1",
        id: "skill/one",
        to_version: "v2",
      }),
    ).toMatchObject({
      path: "/v1/skills/learned/skill%2Fone/diff?from=v1&to=v2",
    });

    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.ResumeSessionMigration"].grpc.descriptor, {
        job_id: "job/one",
      }),
    ).toMatchObject({
      body: undefined,
      path: "/v1/storage/migrations/job%2Fone/resume",
    });
    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.ApplySessionMigration"].grpc.descriptor, {
        batch_size: 25,
        plan_id: "plan-one",
      }),
    ).toMatchObject({ body: { batch_size: 25, plan_id: "plan-one" } });

    expect(
      resolveHTTPRoute(RPC_CATALOG["HarnessService.RunTeam"].grpc.descriptor, {
        team_id: "team/one",
      }),
    ).toMatchObject({
      body: undefined,
      path: "/v1/teams/team%2Fone/run",
    });
    expect(RPC_CATALOG["HarnessService.GetSession"].http).toMatchObject({
      response: "json",
      responseField: "session",
    });
    expect(RPC_CATALOG["HarnessService.ApprovePlan"].http).toMatchObject({
      requestBody: "optional-json",
      response: "sse",
    });
  });

  it("server streams decode without buffering on the HTTP transport", async () => {
    const methods = [
      RPC_CATALOG["HarnessService.RunTeam"],
      RPC_CATALOG["HarnessService.ApprovePlan"],
      RPC_CATALOG["HarnessService.StreamSessionEvents"],
      RPC_CATALOG["HarnessService.WatchSessionEvents"],
    ] as const;

    for (const entry of methods) {
      let streamRequested = false;
      const fetch: typeof globalThis.fetch = async (input) => {
        const path = new URL(String(input)).pathname;
        if (path === "/v1/compatibility")
          return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
        streamRequested = true;
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(new TextEncoder().encode("data: {}\n\n"));
            // Deliberately stay open. A whole-body decoder waits forever here.
          },
        });
        return new Response(body, {
          headers: { "content-type": "text/event-stream" },
          status: 200,
        });
      };
      const raw = browserEntry.createRawClient({
        transport: browserEntry.createHttpTransport({ baseUrl: "http://mecatl.test", fetch }),
      });
      const abort = new AbortController();
      const stream = raw.stream(
        entry.grpc.descriptor as DescMethodStreaming,
        singleValue(requestFor(entry)),
        { signal: abort.signal },
      );
      const iterator = stream[Symbol.asyncIterator]();
      const first = await Promise.race([
        iterator.next(),
        new Promise<never>((_, reject) =>
          setTimeout(() => reject(new Error(`${entry.method} waited for response EOF`)), 1_000),
        ),
      ]);
      expect(streamRequested).toBe(true);
      expect(first.done).toBe(false);
      abort.abort();
      await iterator.return?.();
    }
  });

  it("raw operations reach every classified RPC on both transports", async () => {
    const sseRoutePatterns = Object.values(RPC_CATALOG)
      .filter((entry) => entry.http.kind === "http" && entry.http.response === "sse")
      .map(
        (entry) =>
          new RegExp(
            `^${(entry.http as { pathTemplate: string }).pathTemplate.replace(/\{[^}]+\}/g, "[^/]+")}$`,
          ),
      );
    const grpcTransport = new RecordingTransport();
    const grpc = browserEntry.createRawClient({ transport: grpcTransport });
    const httpRequests: Array<{ headers: Headers; method: string; path: string; signal: boolean }> =
      [];
    const fetch: typeof globalThis.fetch = async (input, init) => {
      const url = new URL(String(input));
      httpRequests.push({
        headers: new Headers(init?.headers),
        method: init?.method ?? "GET",
        path: `${url.pathname}${url.search}`,
        signal: init?.signal !== undefined && init.signal !== null,
      });
      if (url.pathname === "/v1/compatibility")
        return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
      if (sseRoutePatterns.some((pattern) => pattern.test(url.pathname))) {
        return new Response("data: {}\n\n", {
          headers: { "content-type": "text/event-stream" },
          status: 200,
        });
      }
      return Response.json({});
    };
    const http = nodeEntry.createRawClient({
      transport: nodeEntry.createHttpTransport({ baseUrl: "http://mecatl.test", fetch }),
    });

    const rows = Object.values(RPC_CATALOG);
    for (const entry of rows) await invoke(grpc, entry);
    for (const entry of rows) {
      if (entry.http.kind === "http") await invoke(http, entry);
    }

    expect(grpcTransport.calls.map(({ method }) => method).sort()).toEqual(
      rows.map(({ method }) => method).sort(),
    );
    expect(
      grpcTransport.calls.every(({ headers }) => headers.get("x-raw-option") === "preserved"),
    ).toBe(true);
    expect(grpcTransport.calls.every(({ timeoutMs }) => timeoutMs === 4_000)).toBe(true);
    expect(
      httpRequests
        .filter(({ path }) => path !== "/v1/compatibility")
        .every(({ headers, signal }) => headers.get("x-raw-option") === "preserved" && signal),
    ).toBe(true);

    const failingGrpcTransport = new RecordingTransport();
    const originalUnary = failingGrpcTransport.unary.bind(failingGrpcTransport);
    failingGrpcTransport.unary = async (...args) => {
      if (args[0].name === "GetCompatibilityInfo") return originalUnary(...args);
      throw codedError("management_unauthorized", Code.PermissionDenied);
    };
    const failingGrpc = browserEntry.createRawClient({ transport: failingGrpcTransport });
    await expect(
      failingGrpc.unary(RPC_CATALOG["HarnessService.GetServerInfo"].grpc.descriptor, {}),
    ).rejects.toMatchObject({ code: "management_unauthorized", transport: "grpc" });

    let compatible = false;
    const failingHTTP = nodeEntry.createRawClient({
      transport: nodeEntry.createHttpTransport({
        baseUrl: "http://mecatl.test",
        fetch: async () => {
          if (!compatible) {
            compatible = true;
            return Response.json({ api_major: 1, capabilities: {}, features: ["server_info"] });
          }
          return Response.json(
            { code: "management_unauthorized", detail: "denied" },
            { status: 403 },
          );
        },
      }),
    });
    await expect(
      failingHTTP.unary(RPC_CATALOG["HarnessService.GetServerInfo"].grpc.descriptor, {}),
    ).rejects.toMatchObject({ code: "management_unauthorized", transport: "http" });
  });
});
