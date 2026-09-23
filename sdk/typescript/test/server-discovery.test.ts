import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import {
  create,
  type DescMessage,
  type DescMethodStreaming,
  type DescMethodUnary,
  type MessageInitShape,
} from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  type ContextValues,
  createRouterTransport,
  type StreamResponse,
  type Transport,
  type UnaryResponse,
} from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import * as denoEntry from "../src/deno.js";
import { HarnessService, ListModelsRequestSchema } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  AuthenticationError,
  connect,
  createRawClient,
  IncompatibleServerError,
  ProtocolError,
  SESSION_ID_HEADER_NAME,
  type Server,
  type ServerCompatibility,
  ServerError,
  ServerFeature,
  type ServerInfo,
  type ServerInfoOptions,
  ServerPosture,
  SUPPORTED_API_MAJOR,
  TransportError,
  UnsupportedFeatureError,
} from "../src/index.js";
import * as nodeEntry from "../src/node.js";
import { readRawCompatibility } from "../src/raw.js";
import { isWellFormedUnicode, projectServerCompatibility } from "../src/server.js";

const packageRoot = fileURLToPath(new URL("../", import.meta.url));

const completeCapabilities = {
  agents: true,
  audio: true,
  shell: true,
  debugMcp: true,
  image: true,
  learnedSkills: true,
  learningProposals: true,
  manualCompaction: true,
  manualDream: {
    projectMemory: { decide: true, generate: true },
    userModel: { decide: false, generate: false, unavailableReason: "not_configured" },
  },
  mcp: true,
  mcpConnectorStatus: true,
  memory: true,
  modelSelection: true,
  posture: "future-posture",
  reflection: true,
  scheduling: true,
  sessionDebug: true,
  skills: true,
  slashCommands: true,
  soul: true,
  steer: true,
  storageCleanup: true,
  storageHealth: true,
  teams: true,
  userModel: true,
  workspaceEnrollment: true,
  worktrees: true,
};

const validCompatibility = {
  apiMajor: SUPPORTED_API_MAJOR,
  capabilities: completeCapabilities,
  deployment: "eu-west / blue",
  features: [ServerFeature.ServerInfo, "future_feature"],
};

type UnaryCall = {
  readonly headers: Headers;
  readonly input: unknown;
  readonly method: string;
  readonly signal: AbortSignal | undefined;
  readonly timeoutMs: number | undefined;
};

class Deferred<T> {
  readonly promise: Promise<T>;
  resolve!: (value: T) => void;
  reject!: (reason: unknown) => void;

  constructor() {
    this.promise = new Promise<T>((resolve, reject) => {
      this.resolve = resolve;
      this.reject = reject;
    });
  }
}

class DiscoveryTransport implements Transport {
  readonly calls: UnaryCall[] = [];
  readonly compatibility: Array<() => Promise<MessageInitShape<DescMessage>>> = [];
  info: MessageInitShape<DescMessage> = {
    buildId: "v1.2.3",
    llmProviderDisplayEndpoint: "https://api.example.com/v1",
    serverImplementation: "mecated",
  };

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: MessageInitShape<I>,
    _contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    this.calls.push({
      headers: new Headers(header),
      input,
      method: method.name,
      signal,
      timeoutMs,
    });
    let init: MessageInitShape<DescMessage> = {};
    if (method.name === "GetCompatibilityInfo") {
      init = await (this.compatibility.shift()?.() ?? Promise.resolve(validCompatibility));
    } else if (method.name === "GetServerInfo") {
      init = this.info;
    }
    return {
      header: new Headers({ "x-test": `${method.name}-header` }),
      message: create(method.output, init as MessageInitShape<O>),
      method,
      service: method.parent,
      stream: false,
      trailer: new Headers({ "x-test": `${method.name}-trailer` }),
    };
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    _method: DescMethodStreaming<I, O>,
    _signal: AbortSignal | undefined,
    _timeoutMs: number | undefined,
    _header: HeadersInit | undefined,
    _input: AsyncIterable<MessageInitShape<I>>,
    _contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    throw new Error("server-discovery fixtures are unary");
  }
}

class SynchronousFailureTransport extends DiscoveryTransport {
  compatibilityAttempts = 0;

  override unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: MessageInitShape<I>,
    contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    if (method.name === "GetCompatibilityInfo") {
      this.compatibilityAttempts += 1;
      if (this.compatibilityAttempts === 2) throw new Error("synchronous offline");
    }
    return super.unary(method, signal, timeoutMs, header, input, contextValues);
  }
}

function grpcClient(transport = new DiscoveryTransport()) {
  return { client: connect({ transport }), transport };
}

function compatibilityCalls(transport: DiscoveryTransport): UnaryCall[] {
  return transport.calls.filter(({ method }) => method === "GetCompatibilityInfo");
}

function protocolWithoutRejectedValue(error: unknown, rejected: string): void {
  expect(error).toBeInstanceOf(ProtocolError);
  const protocol = error as ProtocolError;
  expect(protocol.message).not.toContain(rejected);
  expect(protocol.cause).toBeUndefined();
  expect(JSON.stringify(protocol.toJSON())).not.toContain(rejected);
}

function reportBlock(report: string, declaration: string): string[] {
  const body = new RegExp(`export ${declaration} \\{([\\s\\S]*?)\\n\\}`, "u").exec(report)?.[1];
  expect(body, declaration).toBeDefined();
  return (body ?? "")
    .trim()
    .split("\n")
    .map((line) => line.trim());
}

describe("SDK server discovery", () => {
  it("compatibility returns a detached typed pre-session projection", async () => {
    const { client, transport } = grpcClient();
    const abort = new AbortController();
    const result = await client.server.compatibility({
      headers: { [SESSION_ID_HEADER_NAME]: "must-be-stripped", "x-caller": "kept" },
      signal: abort.signal,
      timeoutMs: 4_321,
    });

    expect(result).toEqual({
      apiMajor: SUPPORTED_API_MAJOR,
      capabilities: completeCapabilities,
      deployment: "eu-west / blue",
      features: new Set([ServerFeature.ServerInfo, "future_feature"]),
    });
    const call = compatibilityCalls(transport).at(-1);
    expect(call?.headers.get("x-caller")).toBe("kept");
    expect(call?.headers.has(SESSION_ID_HEADER_NAME)).toBe(false);
    expect(call?.timeoutMs).toBe(4_321);
    expect(call?.signal).toBeDefined();
    abort.abort();
    expect(call?.signal?.aborted).toBe(true);
    expect(transport.calls.map(({ method }) => method)).toEqual([
      "GetCompatibilityInfo",
      "GetCompatibilityInfo",
    ]);

    (result.features as Set<string>).delete(ServerFeature.ServerInfo);
    await expect(client.server.info()).resolves.toMatchObject({ buildId: "v1.2.3" });
  });

  it("compatibility detaches nested capabilities from the shared cache", async () => {
    const raw = createRawClient({ transport: new DiscoveryTransport() });
    const first = projectServerCompatibility((await readRawCompatibility(raw)).message, "grpc");
    const mutable = first.capabilities as {
      agents: boolean;
      manualDream?: { projectMemory?: { decide: boolean } };
    };
    mutable.agents = false;
    if (mutable.manualDream?.projectMemory !== undefined) {
      mutable.manualDream.projectMemory.decide = false;
    }

    const second = projectServerCompatibility((await readRawCompatibility(raw)).message, "grpc");
    expect(second.capabilities.agents).toBe(true);
    expect(second.capabilities.manualDream?.projectMemory?.decide).toBe(true);
  });

  it("compatibility preserves unknown feature identifiers and known constant values", async () => {
    const { client } = grpcClient();
    const result = await client.server.compatibility();

    expect(result.capabilities).toEqual(completeCapabilities);
    expect([...result.features]).toEqual([ServerFeature.ServerInfo, "future_feature"]);
    expect(result.capabilities.posture).toBe("future-posture");
    expect(ServerFeature).toEqual({
      HttpSteer: "http_steer",
      McpServersOnCreate: "mcp_servers_on_create",
      PromptFreeControls: "prompt_free_controls",
      ServerInfo: "server_info",
      SessionActivityInventory: "session_activity_inventory",
      WatchSessionEvents: "watch_session_events",
    });
    expect(ServerPosture).toEqual({
      Strict: "strict",
      Trusted: "trusted",
      Auto: "auto",
      Yolo: "yolo",
    });

    const empty = new DiscoveryTransport();
    empty.compatibility.push(
      async () => validCompatibility,
      async () => ({ ...validCompatibility, deployment: "" }),
    );
    await expect(connect({ transport: empty }).server.compatibility()).resolves.not.toHaveProperty(
      "deployment",
    );
  });

  it("compatibility rejects malformed capability feature and deployment projections", async () => {
    const invalid: Array<{ rejected?: string; response: Record<string, unknown> }> = [
      { response: { ...validCompatibility, capabilities: undefined } },
      { response: { ...validCompatibility, features: [] } },
      { rejected: "", response: { ...validCompatibility, features: [""] } },
      {
        rejected: "secret-feature-47",
        response: { ...validCompatibility, features: ["secret-feature-47", "secret-feature-47"] },
      },
      { rejected: "\ud800", response: { ...validCompatibility, deployment: "\ud800" } },
      {
        rejected: "a".repeat(129),
        response: { ...validCompatibility, deployment: "a".repeat(129) },
      },
      { rejected: "é".repeat(65), response: { ...validCompatibility, deployment: "é".repeat(65) } },
      {
        rejected: "prod\u00a0blue",
        response: { ...validCompatibility, deployment: "prod\u00a0blue" },
      },
      { rejected: " \t", response: { ...validCompatibility, deployment: " \t" } },
      {
        rejected: "bad\u0000value",
        response: { ...validCompatibility, deployment: "bad\u0000value" },
      },
      { rejected: "bad\nvalue", response: { ...validCompatibility, deployment: "bad\nvalue" } },
    ];

    for (const { rejected, response } of invalid) {
      const transport = new DiscoveryTransport();
      transport.compatibility.push(
        async () => validCompatibility,
        async () => response,
      );
      const error = await connect({ transport })
        .server.compatibility()
        .catch((cause) => cause);
      expect(error).toBeInstanceOf(ProtocolError);
      expect((error as ProtocolError).cause).toBeUndefined();
      if (rejected !== undefined && rejected !== "") {
        protocolWithoutRejectedValue(error, rejected);
      }
    }

    const boundary = new DiscoveryTransport();
    boundary.compatibility.push(
      async () => validCompatibility,
      async () => ({ ...validCompatibility, deployment: "a".repeat(128) }),
    );
    await expect(connect({ transport: boundary }).server.compatibility()).resolves.toMatchObject({
      deployment: "a".repeat(128),
    });
  });

  it("explicit compatibility calls start fresh generations and ordinary calls share the newest", async () => {
    const transport = new DiscoveryTransport();
    const first = new Deferred<Record<string, unknown>>();
    const second = new Deferred<Record<string, unknown>>();
    transport.compatibility.push(
      async () => validCompatibility,
      () => first.promise,
      () => second.promise,
    );
    const client = connect({ transport });

    const firstRefresh = client.server.compatibility();
    const secondRefresh = client.server.compatibility();
    const ordinary = client.models.list(create(ListModelsRequestSchema));
    expect(compatibilityCalls(transport)).toHaveLength(3);

    second.resolve({ ...validCompatibility, deployment: "newest" });
    await expect(secondRefresh).resolves.toMatchObject({ deployment: "newest" });
    await expect(ordinary).resolves.toBeDefined();
    first.resolve({ ...validCompatibility, deployment: "oldest" });
    await expect(firstRefresh).resolves.toMatchObject({ deployment: "oldest" });
    await client.models.list(create(ListModelsRequestSchema));
    expect(compatibilityCalls(transport)).toHaveLength(3);
  });

  it("stale compatibility completions cannot overwrite or clear a newer generation", async () => {
    const transport = new DiscoveryTransport();
    const stale = new Deferred<Record<string, unknown>>();
    const current = new Deferred<Record<string, unknown>>();
    transport.compatibility.push(
      async () => validCompatibility,
      () => stale.promise,
      () => current.promise,
    );
    const client = connect({ transport });

    const staleRefresh = client.server.compatibility();
    const currentRefresh = client.server.compatibility();
    current.resolve({ ...validCompatibility, deployment: "current" });
    await currentRefresh;
    stale.resolve({ ...validCompatibility, deployment: "stale", features: ["old_feature"] });
    await staleRefresh;
    await expect(client.server.info()).resolves.toMatchObject({ serverImplementation: "mecated" });
    expect(compatibilityCalls(transport)).toHaveLength(3);

    const failures = new DiscoveryTransport();
    const oldFailure = new Deferred<Record<string, unknown>>();
    const newSuccess = new Deferred<Record<string, unknown>>();
    failures.compatibility.push(
      async () => validCompatibility,
      () => oldFailure.promise,
      () => newSuccess.promise,
    );
    const failureClient = connect({ transport: failures });
    const failedRefresh = failureClient.server.compatibility();
    const successfulRefresh = failureClient.server.compatibility();
    newSuccess.resolve(validCompatibility);
    await successfulRefresh;
    oldFailure.reject(new ConnectError("stale unavailable", Code.Unavailable));
    await expect(failedRefresh).rejects.toBeInstanceOf(TransportError);
    await failureClient.models.list(create(ListModelsRequestSchema));
    expect(compatibilityCalls(failures)).toHaveLength(3);

    const retryableFailures = [
      {
        expected: TransportError,
        failure: async () => Promise.reject(new ConnectError("unavailable", Code.Unavailable)),
      },
      {
        expected: IncompatibleServerError,
        failure: async () => ({ ...validCompatibility, apiMajor: 2 }),
      },
      {
        expected: IncompatibleServerError,
        failure: async () => Promise.reject(new ConnectError("missing", Code.Unimplemented)),
      },
      {
        expected: ProtocolError,
        failure: async () => ({ ...validCompatibility, features: [] }),
      },
      {
        expected: AuthenticationError,
        failure: async () =>
          Promise.reject(new ConnectError("unauthenticated", Code.Unauthenticated)),
      },
      {
        expected: ServerError,
        failure: async () => Promise.reject(new ConnectError("server failed", Code.Internal)),
      },
    ] as const;
    for (const { expected, failure } of retryableFailures) {
      const currentFailure = new DiscoveryTransport();
      currentFailure.compatibility.push(
        async () => validCompatibility,
        failure,
        async () => validCompatibility,
      );
      const retryClient = connect({ transport: currentFailure });
      await expect(retryClient.server.compatibility()).rejects.toBeInstanceOf(expected);
      await expect(retryClient.models.list(create(ListModelsRequestSchema))).resolves.toBeDefined();
      expect(compatibilityCalls(currentFailure)).toHaveLength(3);
    }

    const staleMalformedTransport = new DiscoveryTransport();
    const staleMalformed = new Deferred<Record<string, unknown>>();
    const newerPending = new Deferred<Record<string, unknown>>();
    staleMalformedTransport.compatibility.push(
      async () => validCompatibility,
      () => staleMalformed.promise,
      () => newerPending.promise,
    );
    const staleMalformedClient = connect({ transport: staleMalformedTransport });
    const staleMalformedRefresh = staleMalformedClient.server.compatibility();
    const ordinaryOnStaleGeneration = staleMalformedClient.models.list(
      create(ListModelsRequestSchema),
    );
    const newerRefresh = staleMalformedClient.server.compatibility();
    const ordinaryOnNewerGeneration = staleMalformedClient.models.list(
      create(ListModelsRequestSchema),
    );
    staleMalformed.resolve({ ...validCompatibility, features: [] });
    await expect(staleMalformedRefresh).rejects.toBeInstanceOf(ProtocolError);
    await expect(ordinaryOnStaleGeneration).resolves.toBeDefined();
    expect(compatibilityCalls(staleMalformedTransport)).toHaveLength(3);
    expect(
      staleMalformedTransport.calls.filter(({ method }) => method === "ListModels"),
    ).toHaveLength(1);
    newerPending.resolve(validCompatibility);
    await expect(newerRefresh).resolves.toBeDefined();
    await expect(ordinaryOnNewerGeneration).resolves.toBeDefined();
    await expect(staleMalformedClient.server.info()).resolves.toBeDefined();
    expect(compatibilityCalls(staleMalformedTransport)).toHaveLength(3);
  });

  it("ordinary calls remain attached to a newer pending compatibility generation", async () => {
    const successTransport = new DiscoveryTransport();
    const staleSuccess = new Deferred<Record<string, unknown>>();
    const pendingSuccess = new Deferred<Record<string, unknown>>();
    successTransport.compatibility.push(
      async () => validCompatibility,
      () => staleSuccess.promise,
      () => pendingSuccess.promise,
    );
    const successClient = connect({ transport: successTransport });
    const staleSuccessRefresh = successClient.server.compatibility();
    const pendingSuccessRefresh = successClient.server.compatibility();
    staleSuccess.resolve({ ...validCompatibility, deployment: "stale" });
    await staleSuccessRefresh;
    let ordinaryResolved = false;
    const ordinaryAfterSuccess = successClient.models
      .list(create(ListModelsRequestSchema))
      .then((value) => {
        ordinaryResolved = true;
        return value;
      });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(ordinaryResolved).toBe(false);
    expect(successTransport.calls.some(({ method }) => method === "ListModels")).toBe(false);
    expect(compatibilityCalls(successTransport)).toHaveLength(3);
    pendingSuccess.resolve({ ...validCompatibility, deployment: "current" });
    await pendingSuccessRefresh;
    await ordinaryAfterSuccess;

    const failureTransport = new DiscoveryTransport();
    const staleFailure = new Deferred<Record<string, unknown>>();
    const pendingAfterFailure = new Deferred<Record<string, unknown>>();
    failureTransport.compatibility.push(
      async () => validCompatibility,
      () => staleFailure.promise,
      () => pendingAfterFailure.promise,
    );
    const failureClient = connect({ transport: failureTransport });
    const staleFailureRefresh = failureClient.server.compatibility();
    const pendingFailureRefresh = failureClient.server.compatibility();
    staleFailure.reject(new ConnectError("stale unavailable", Code.Unavailable));
    await expect(staleFailureRefresh).rejects.toBeInstanceOf(TransportError);
    let ordinaryAfterFailureResolved = false;
    const ordinaryAfterFailure = failureClient.models
      .list(create(ListModelsRequestSchema))
      .then((value) => {
        ordinaryAfterFailureResolved = true;
        return value;
      });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(ordinaryAfterFailureResolved).toBe(false);
    expect(failureTransport.calls.some(({ method }) => method === "ListModels")).toBe(false);
    expect(compatibilityCalls(failureTransport)).toHaveLength(3);
    pendingAfterFailure.resolve(validCompatibility);
    await pendingFailureRefresh;
    await ordinaryAfterFailure;
  });

  it("compatibility floor and transport failures stay typed and retryable", async () => {
    const malformedOptionalProjection = {
      ...validCompatibility,
      capabilities: undefined,
      deployment: "private\nlabel",
      features: [ServerFeature.ServerInfo, ServerFeature.ServerInfo],
    };
    const rawTransport = new DiscoveryTransport();
    rawTransport.compatibility.push(async () => malformedOptionalProjection);
    const raw = createRawClient({ transport: rawTransport });
    await expect(raw.features()).resolves.toEqual(new Set([ServerFeature.ServerInfo]));
    await expect(
      raw.unary(HarnessService.method.listModels, create(ListModelsRequestSchema)),
    ).resolves.toBeDefined();
    expect(compatibilityCalls(rawTransport)).toHaveLength(1);

    const ordinaryTransport = new DiscoveryTransport();
    const ordinaryCompatibility = new Deferred<Record<string, unknown>>();
    ordinaryTransport.compatibility.push(() => ordinaryCompatibility.promise);
    const ordinaryClient = connect({ transport: ordinaryTransport });
    const ordinary = ordinaryClient.models.list(create(ListModelsRequestSchema));
    ordinaryCompatibility.resolve(malformedOptionalProjection);
    await expect(ordinary).resolves.toBeDefined();
    expect(compatibilityCalls(ordinaryTransport)).toHaveLength(1);
    await expect(ordinaryClient.server.info()).rejects.toBeInstanceOf(ProtocolError);
    expect(ordinaryTransport.calls.some(({ method }) => method === "GetServerInfo")).toBe(false);
    await expect(
      ordinaryClient.models.list(create(ListModelsRequestSchema)),
    ).resolves.toBeDefined();
    expect(compatibilityCalls(ordinaryTransport)).toHaveLength(2);

    const missing = createRouterTransport((router) => router.service(HarnessService, {}));
    await expect(connect({ transport: missing }).server.compatibility()).rejects.toBeInstanceOf(
      IncompatibleServerError,
    );

    const mismatch = new DiscoveryTransport();
    mismatch.compatibility.push(
      async () => validCompatibility,
      async () => ({ ...validCompatibility, apiMajor: 2 }),
    );
    await expect(connect({ transport: mismatch }).server.compatibility()).rejects.toBeInstanceOf(
      IncompatibleServerError,
    );

    const malformed = new DiscoveryTransport();
    malformed.compatibility.push(
      async () => validCompatibility,
      async () => ({ ...validCompatibility, features: [] }),
    );
    await expect(connect({ transport: malformed }).server.compatibility()).rejects.toBeInstanceOf(
      ProtocolError,
    );

    const unauthenticated = new DiscoveryTransport();
    unauthenticated.compatibility.push(
      async () => validCompatibility,
      async () => {
        throw new ConnectError("unauthenticated", Code.Unauthenticated);
      },
    );
    await expect(
      connect({ transport: unauthenticated }).server.compatibility(),
    ).rejects.toBeInstanceOf(AuthenticationError);

    const httpMissing = connect({
      baseUrl: "http://mecatl.test",
      fetch: async () => Response.json({}, { status: 404 }),
    });
    await expect(httpMissing.server.compatibility()).rejects.toBeInstanceOf(
      IncompatibleServerError,
    );

    const httpOffline = connect({
      baseUrl: "http://mecatl.test",
      fetch: async () => Promise.reject(new Error("offline")),
    });
    await expect(httpOffline.server.compatibility()).rejects.toBeInstanceOf(TransportError);

    let httpRetryAttempts = 0;
    const httpRetry = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input) => {
        const url = new URL(String(input));
        if (url.pathname !== "/v1/compatibility") return Response.json({});
        httpRetryAttempts += 1;
        return httpRetryAttempts <= 2
          ? Response.json({}, { status: 404 })
          : Response.json({
              api_major: 1,
              capabilities: completeCapabilities,
              features: [ServerFeature.ServerInfo],
            });
      },
    });
    await expect(httpRetry.server.compatibility()).rejects.toBeInstanceOf(IncompatibleServerError);
    await expect(httpRetry.models.list(create(ListModelsRequestSchema))).resolves.toBeDefined();
    expect(httpRetryAttempts).toBe(3);

    const synchronousFailure = new SynchronousFailureTransport();
    const synchronousClient = connect({ transport: synchronousFailure });
    await expect(synchronousClient.server.compatibility()).rejects.toBeInstanceOf(TransportError);
    await expect(
      synchronousClient.models.list(create(ListModelsRequestSchema)),
    ).resolves.toBeDefined();
    expect(synchronousFailure.compatibilityAttempts).toBe(3);
  });

  it("server info requires advertised support and sends only the explicit provider id", async () => {
    const { client, transport } = grpcClient();
    await client.server.info();
    await client.server.info({ providerId: "" });
    await client.server.info({ providerId: "provider/one" });
    await client.server.info();

    expect(compatibilityCalls(transport)).toHaveLength(1);
    const inputs = transport.calls
      .filter(({ method }) => method === "GetServerInfo")
      .map(({ input }) => input);
    expect(inputs).toEqual([{}, { providerId: "" }, { providerId: "provider/one" }, {}]);

    const unsupported = new DiscoveryTransport();
    unsupported.compatibility.push(async () => ({ ...validCompatibility, features: ["other"] }));
    const unsupportedClient = connect({ transport: unsupported });
    await expect(unsupportedClient.server.info()).rejects.toBeInstanceOf(UnsupportedFeatureError);
    await expect(unsupportedClient.server.info()).rejects.toMatchObject({
      feature: ServerFeature.ServerInfo,
    });
    expect(unsupported.calls.some(({ method }) => method === "GetServerInfo")).toBe(false);
  });

  it("server info validates and detaches safe identity projections", async () => {
    const transport = new DiscoveryTransport();
    transport.info = {
      buildId: "  v1.2.3-4-gabc  ",
      llmProviderDisplayEndpoint: "",
      serverImplementation: "  mecatl-test  ",
    };
    const client = connect({ transport });
    const controller = new AbortController();
    const responseHeaders: Array<string | null> = [];
    const responseTrailers: Array<string | null> = [];
    const info = await client.server.info(
      { providerId: "provider" },
      {
        headers: { authorization: "Bearer info", "x-caller": "kept" },
        onHeader: (headers) => responseHeaders.push(headers.get("x-test")),
        onTrailer: (trailers) => responseTrailers.push(trailers.get("x-test")),
        signal: controller.signal,
        timeoutMs: 1_234,
      },
    );
    expect(info).toEqual({ buildId: "v1.2.3-4-gabc", serverImplementation: "mecatl-test" });
    const call = transport.calls.find(({ method }) => method === "GetServerInfo");
    expect(call?.headers.get("x-caller")).toBe("kept");
    expect(call?.headers.get("authorization")).toBe("Bearer info");
    expect(call?.timeoutMs).toBe(1_234);
    expect(responseHeaders).toEqual(["GetServerInfo-header"]);
    expect(responseTrailers).toEqual(["GetServerInfo-trailer"]);
    controller.abort();
    expect(call?.signal?.aborted).toBe(true);

    transport.info = { buildId: "next", serverImplementation: "" };
    await expect(client.server.info()).resolves.toEqual({
      buildId: "next",
      serverImplementation: "unknown",
    });
    transport.info = { buildId: "next", serverImplementation: `a${"b".repeat(63)}` };
    await expect(client.server.info()).resolves.toMatchObject({
      serverImplementation: `a${"b".repeat(63)}`,
    });
    expect(info).toEqual({ buildId: "v1.2.3-4-gabc", serverImplementation: "mecatl-test" });
  });

  it("server info rejects unsafe identity data without echoing it", async () => {
    const diagnostics: unknown[] = [];
    const invalid = [
      { field: "buildId", value: "   " },
      { field: "serverImplementation", value: "Secret_Implementation" },
      { field: "serverImplementation", value: `a${"b".repeat(64)}` },
      { field: "llmProviderDisplayEndpoint", value: "HTTPS://api.example.com/v1" },
      { field: "llmProviderDisplayEndpoint", value: "https://user:secret@example.com/v1" },
      { field: "llmProviderDisplayEndpoint", value: "https://api.example.com/v1?secret=yes" },
      { field: "llmProviderDisplayEndpoint", value: "https://api.example.com/v1#secret" },
      { field: "llmProviderDisplayEndpoint", value: "https://api.example.com/a/../secret" },
      { field: "llmProviderDisplayEndpoint", value: "https://api.example.com/%" },
      { field: "llmProviderDisplayEndpoint", value: "https://api.example.com/%2" },
      { field: "llmProviderDisplayEndpoint", value: "https://api.example.com/%GG" },
      { field: "llmProviderDisplayEndpoint", value: "https://exa%6dple.com/v1" },
      { field: "llmProviderDisplayEndpoint", value: "foo://evil\u202e.com/v1" },
      { field: "llmProviderDisplayEndpoint", value: "https://api.example.com/bad\u0000path" },
      {
        field: "llmProviderDisplayEndpoint",
        value: `https://a/${"x".repeat(2_039)}`,
      },
      { field: "llmProviderDisplayEndpoint", value: "https://bad\\host.example/v1" },
      { field: "llmProviderDisplayEndpoint", value: "\ud800" },
    ] as const;

    for (const { field, value } of invalid) {
      const transport = new DiscoveryTransport();
      transport.info = {
        buildId: "valid-build",
        llmProviderDisplayEndpoint: "https://api.example.com/v1",
        serverImplementation: "mecated",
        [field]: value,
      };
      const client = nodeEntry.connect({
        diagnostics: (record) => diagnostics.push(record),
        transport,
      });
      const error = await client.server.info().catch((cause) => cause);
      protocolWithoutRejectedValue(error, value);
    }
    expect(diagnostics).toEqual([]);

    const accepted = new DiscoveryTransport();
    accepted.info = {
      buildId: "build",
      llmProviderDisplayEndpoint: `https://a/${"x".repeat(2_038)}`,
      serverImplementation: "mecated",
    };
    await expect(connect({ transport: accepted }).server.info()).resolves.toMatchObject({
      llmProviderDisplayEndpoint: `https://a/${"x".repeat(2_038)}`,
    });

    const escaped = new DiscoveryTransport();
    escaped.info = {
      buildId: "build",
      llmProviderDisplayEndpoint: "https://API.example.com/a%2Fb",
      serverImplementation: "mecated",
    };
    await expect(connect({ transport: escaped }).server.info()).resolves.toMatchObject({
      llmProviderDisplayEndpoint: "https://API.example.com/a%2Fb",
    });
  });

  it("server discovery has equivalent grpc and http behavior", async () => {
    const grpc = grpcClient().client;
    const requests: Array<{
      headers: Headers;
      signal: AbortSignal | null | undefined;
      url: string;
    }> = [];
    const http = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input, init) => {
        requests.push({
          headers: new Headers(init?.headers),
          signal: init?.signal,
          url: String(input),
        });
        const url = new URL(String(input));
        if (url.pathname === "/v1/compatibility") {
          return Response.json({
            api_major: 1,
            capabilities: completeCapabilities,
            deployment: "eu-west / blue",
            features: ["server_info", "future_feature"],
          });
        }
        return Response.json({
          build_id: "v1.2.3",
          llm_provider_display_endpoint: "https://api.example.com/v1",
          server_implementation: "mecated",
        });
      },
    });

    const controller = new AbortController();
    const options = {
      headers: { "x-caller": "kept" },
      signal: controller.signal,
      timeoutMs: 4_000,
    };
    const [grpcCompatibility, httpCompatibility] = await Promise.all([
      grpc.server.compatibility(options),
      http.server.compatibility(options),
    ]);
    expect(httpCompatibility).toEqual(grpcCompatibility);
    const [grpcInfo, httpInfo] = await Promise.all([
      grpc.server.info({ providerId: "provider/one" }, options),
      http.server.info({ providerId: "provider/one" }, options),
    ]);
    expect(httpInfo).toEqual(grpcInfo);
    expect(requests.slice(1).every(({ headers }) => headers.get("x-caller") === "kept")).toBe(true);
    expect(requests.slice(1).every(({ signal }) => signal !== undefined)).toBe(true);
    expect(requests.at(-1)?.url).toBe("http://mecatl.test/v1/info?provider_id=provider%2Fone");
    controller.abort();
    expect(requests.slice(1).every(({ signal }) => signal?.aborted === true)).toBe(true);

    const grpcMalformed = new DiscoveryTransport();
    grpcMalformed.compatibility.push(
      async () => ({ ...validCompatibility, features: [] }),
      async () => ({ ...validCompatibility, features: [] }),
    );
    await expect(
      connect({ transport: grpcMalformed }).server.compatibility(),
    ).rejects.toBeInstanceOf(ProtocolError);

    let httpCompatibilityCalls = 0;
    const httpMalformed = connect({
      baseUrl: "http://mecatl.test",
      fetch: async () => {
        httpCompatibilityCalls += 1;
        return Response.json({
          api_major: 1,
          capabilities: completeCapabilities,
          features: httpCompatibilityCalls === 1 ? [ServerFeature.ServerInfo] : [],
        });
      },
    });
    await expect(httpMalformed.server.compatibility()).rejects.toBeInstanceOf(ProtocolError);

    let unsupportedInfoCalls = 0;
    const httpUnsupported = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input) => {
        const url = new URL(String(input));
        if (url.pathname === "/v1/compatibility") {
          return Response.json({
            api_major: 1,
            capabilities: completeCapabilities,
            features: ["future_feature"],
          });
        }
        unsupportedInfoCalls += 1;
        return Response.json({ build_id: "build", server_implementation: "mecated" });
      },
    });
    await expect(httpUnsupported.server.info()).rejects.toBeInstanceOf(UnsupportedFeatureError);
    expect(unsupportedInfoCalls).toBe(0);

    const httpMalformedInfo = connect({
      baseUrl: "http://mecatl.test",
      fetch: async (input) => {
        const url = new URL(String(input));
        return url.pathname === "/v1/compatibility"
          ? Response.json({
              api_major: 1,
              capabilities: completeCapabilities,
              features: [ServerFeature.ServerInfo],
            })
          : Response.json({ build_id: " ", server_implementation: "mecated" });
      },
    });
    await expect(httpMalformedInfo.server.info()).rejects.toBeInstanceOf(ProtocolError);
  });

  it("server discovery exports and api reports stay complete", () => {
    const typeProof: [Server, ServerCompatibility, ServerInfo, ServerInfoOptions] | undefined =
      undefined;
    expect(typeProof).toBeUndefined();
    for (const entry of [nodeEntry, denoEntry]) {
      expect(entry.ServerFeature).toBe(ServerFeature);
      expect(entry.ServerPosture).toBe(ServerPosture);
    }

    for (const reportName of [
      "mecatl-sdk.api.md",
      "mecatl-sdk-node.api.md",
      "mecatl-sdk-deno.api.md",
    ]) {
      const report = readFileSync(`${packageRoot}/etc/${reportName}`, "utf8");
      for (const name of ["Server", "ServerCompatibility", "ServerInfo", "ServerInfoOptions"]) {
        expect(report).toContain(`export interface ${name}`);
      }
      expect(report).toContain("readonly server: Server;");
      expect(reportBlock(report, "interface Server")).toEqual([
        "compatibility(options?: RequestOptions): Promise<ServerCompatibility>;",
        "info(options?: ServerInfoOptions, requestOptions?: RequestOptions): Promise<ServerInfo>;",
      ]);
      expect(reportBlock(report, "interface ServerCompatibility")).toEqual([
        "readonly apiMajor: typeof SUPPORTED_API_MAJOR;",
        "readonly capabilities: ServerCapabilities;",
        "readonly deployment?: string;",
        "readonly features: ReadonlySet<string>;",
      ]);
      expect(reportBlock(report, "interface ServerInfo")).toEqual([
        "readonly buildId: string;",
        "readonly llmProviderDisplayEndpoint?: string;",
        "readonly serverImplementation: string;",
      ]);
      expect(reportBlock(report, "interface ServerInfoOptions")).toEqual([
        "readonly providerId?: string;",
      ]);
      expect(reportBlock(report, "const ServerFeature:")).toEqual([
        'readonly HttpSteer: "http_steer";',
        'readonly McpServersOnCreate: "mcp_servers_on_create";',
        'readonly PromptFreeControls: "prompt_free_controls";',
        'readonly ServerInfo: "server_info";',
        'readonly SessionActivityInventory: "session_activity_inventory";',
        'readonly WatchSessionEvents: "watch_session_events";',
      ]);
      expect(report).toContain(
        "export type ServerFeature = (typeof ServerFeature)[keyof typeof ServerFeature];",
      );
      expect(reportBlock(report, "const ServerPosture:")).toEqual([
        'readonly Strict: "strict";',
        'readonly Trusted: "trusted";',
        'readonly Auto: "auto";',
        'readonly Yolo: "yolo";',
      ]);
      expect(report).toContain(
        "export type ServerPosture = (typeof ServerPosture)[keyof typeof ServerPosture];",
      );
      expect(report).not.toMatch(/GetCompatibilityInfoResponse|GetServerInfoResponse/u);
    }
  });

  it("raw compatibility floor does not depend on the server namespace projection", () => {
    const rawSource = readFileSync(`${packageRoot}/src/raw.ts`, "utf8");
    expect(rawSource).not.toMatch(/from "\.\/server\.js"/u);
    expect(rawSource).toContain("export const SUPPORTED_API_MAJOR = 1;");
  });

  it("well-formed Unicode rejects a trailing unpaired high surrogate", () => {
    expect(isWellFormedUnicode("valid \ud83d\ude3a text")).toBe(true);
    expect(isWellFormedUnicode("trailing\ud800")).toBe(false);
  });
});
