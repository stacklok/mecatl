import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { createServer, request as httpRequest } from "node:http";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { createRouterTransport } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { connectTransport, type SessionMcpServer } from "../src/client.js";
import type { DiagnosticRecord } from "../src/errors.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { spawnInternal } from "../src/spawn.js";
import {
  type CallToolResult,
  ToolRegistry,
  type ToolSchema,
  withToolRegistration,
} from "../src/tool.js";
import {
  LoopbackToolHost,
  MCP_PROTOCOL_VERSIONS,
  TOOLKIT_MAX_OUTPUT_BYTES,
} from "../src/tool-host.js";

const objectSchema = { additionalProperties: true, type: "object" } as const satisfies ToolSchema;
const scratchRoot = join(process.cwd(), ".scratch");

interface WireResponse {
  readonly body: string;
  readonly headers: NodeJS.Dict<string | string[]>;
  readonly status: number;
}

interface HostHarness {
  readonly diagnostics: DiagnosticRecord[];
  readonly host: LoopbackToolHost;
  readonly registry: ToolRegistry;
  readonly server: SessionMcpServer;
}

const cleanups = new Set<() => Promise<void>>();
let testRoot: string;

beforeEach(async () => {
  await mkdir(scratchRoot, { recursive: true });
  testRoot = await mkdtemp(join(scratchRoot, "sdk-tool-host-test-"));
});

afterEach(async () => {
  for (const cleanup of [...cleanups]) await cleanup();
  cleanups.clear();
  vi.restoreAllMocks();
  await rm(testRoot, { force: true, recursive: true });
});

async function hostHarness(
  internal: ConstructorParameters<typeof LoopbackToolHost>[2] = {},
): Promise<HostHarness> {
  const diagnostics: DiagnosticRecord[] = [];
  let registry: ToolRegistry | undefined;
  const host = new LoopbackToolHost(
    () => {
      if (registry === undefined) throw new Error("registry not installed");
      return registry;
    },
    (record) => diagnostics.push(record),
    internal,
  );
  registry = new ToolRegistry("sdk", host);
  await registry.start();
  const server = registry.mcpServer();
  cleanups.add(async () => {
    registry?.abort(new Error("test cleanup"));
    await registry?.stop();
  });
  return { diagnostics, host, registry, server };
}

function wire(
  url: string,
  options: {
    body?: string;
    headers?: Record<string, string>;
    method?: string;
  } = {},
): Promise<WireResponse> {
  return new Promise<WireResponse>((resolve, reject) => {
    const request = httpRequest(
      url,
      {
        headers: options.headers,
        method: options.method ?? "POST",
      },
      (response) => {
        const chunks: Buffer[] = [];
        response.on("data", (chunk: Buffer) => chunks.push(Buffer.from(chunk)));
        response.on("end", () => {
          resolve({
            body: Buffer.concat(chunks).toString("utf8"),
            headers: response.headers,
            status: response.statusCode ?? 0,
          });
        });
      },
    );
    request.once("error", reject);
    request.end(options.body);
  });
}

async function rpc(
  harness: HostHarness,
  method: string,
  params: unknown = {},
  id: number | string = 1,
): Promise<{ readonly response: WireResponse; readonly value: Record<string, unknown> }> {
  const response = await wire(harness.server.url ?? "", {
    body: JSON.stringify({ id, jsonrpc: "2.0", method, params }),
    headers: {
      ...(harness.server.headers ?? {}),
      "Content-Type": "application/json",
    },
  });
  return { response, value: JSON.parse(response.body) as Record<string, unknown> };
}

async function call(
  harness: HostHarness,
  name: string,
  arguments_: Record<string, unknown> = {},
): Promise<CallToolResult> {
  const response = await rpc(harness, "tools/call", { arguments: arguments_, name });
  return response.value.result as CallToolResult;
}

async function waitFor(predicate: () => boolean, timeoutMs = 2_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("condition did not become true");
    await new Promise<void>((resolve) => setTimeout(resolve, 2));
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("loopback callback tool host", () => {
  it("the host serves the five methods over json and 405s the stream", async () => {
    const harness = await hostHarness();
    harness.registry.register("echo", objectSchema, ({ value }) => String(value));

    const discover = await rpc(harness, "server/discover");
    expect(discover.response.status).toBe(200);
    expect(discover.response.headers["content-type"]).toContain("application/json");
    expect(discover.value).toMatchObject({ error: { code: -32601 }, jsonrpc: "2.0" });

    const initialize = await rpc(harness, "initialize", {
      protocolVersion: "2025-11-25",
    });
    expect(initialize.value).toMatchObject({
      result: {
        capabilities: { tools: {} },
        protocolVersion: "2025-11-25",
      },
    });

    const initialized = await wire(harness.server.url ?? "", {
      body: JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }),
      headers: { ...(harness.server.headers ?? {}), "Content-Type": "application/json" },
    });
    expect(initialized).toMatchObject({ body: "", status: 202 });

    expect((await rpc(harness, "tools/list")).value).toMatchObject({
      result: { tools: [{ name: "echo" }] },
    });
    expect(await call(harness, "echo", { value: "hello" })).toEqual({
      content: [{ text: "hello", type: "text" }],
    });
    expect((await rpc(harness, "ping")).value).toMatchObject({ result: {} });

    for (const method of ["GET", "DELETE"]) {
      const response = await wire(harness.server.url ?? "", { method });
      expect(response.status, method).toBe(405);
      expect(response.headers.allow, method).toBe("POST");
    }
  });

  it("the host is loopback-only independent of the bearer", async () => {
    const harness = await hostHarness();
    const url = new URL(harness.server.url ?? "");

    expect(url.hostname).toBe("127.0.0.1");
    expect(url.port).toMatch(/^\d+$/);
    expect(url.port).not.toBe("0");
    expect(harness.server.url).not.toMatch(/localhost|0\.0\.0\.0|\[::\]/);
  });

  it("a bad bearer is refused by a constant-time comparison", async () => {
    const harness = await hostHarness();
    const expected = harness.server.headers?.Authorization ?? "";
    const attempts = [undefined, "Bearer wrong", expected.slice(0, -1)];
    const responses = await Promise.all(
      attempts.map((authorization) =>
        wire(harness.server.url ?? "", {
          body: JSON.stringify({ id: 1, jsonrpc: "2.0", method: "ping" }),
          headers: authorization === undefined ? {} : { Authorization: authorization },
        }),
      ),
    );

    expect(responses.map(({ body, status }) => ({ body, status }))).toEqual([
      { body: "request refused", status: 401 },
      { body: "request refused", status: 401 },
      { body: "request refused", status: 401 },
    ]);
    const source = await readFile(
      fileURLToPath(new URL("../src/tool-host.ts", import.meta.url)),
      "utf8",
    );
    expect(source).toContain("timingSafeEqual(this.#digest(presented), this.#expectedDigest)");
    expect(source).toContain('createHash(DIGEST_ALGORITHM).update(value, "utf8").digest()');
  });

  it("the bearer is per-client and never surfaces outward", async () => {
    const first = await hostHarness();
    const second = await hostHarness();
    const firstBearer = first.server.headers?.Authorization ?? "";
    const secondBearer = second.server.headers?.Authorization ?? "";

    expect(firstBearer).toMatch(/^Bearer [A-Za-z0-9_-]{43}$/);
    expect(secondBearer).toMatch(/^Bearer [A-Za-z0-9_-]{43}$/);
    expect(firstBearer).not.toBe(secondBearer);
    expect(JSON.stringify(first.host)).not.toContain(firstBearer);
    expect(JSON.stringify(first.diagnostics)).not.toContain(firstBearer);
    expect(() => new LoopbackToolHost(() => first.registry).mcpServer()).toThrowError(
      expect.not.objectContaining({ message: expect.stringContaining(firstBearer) }),
    );
  });

  it("concurrency is bounded at eight and excess is queued", async () => {
    const harness = await hostHarness();
    const releases: Array<() => void> = [];
    let active = 0;
    let maximum = 0;
    let started = 0;
    harness.registry.register("slow", objectSchema, async () => {
      active += 1;
      started += 1;
      maximum = Math.max(maximum, active);
      await new Promise<void>((resolve) => releases.push(resolve));
      active -= 1;
      return "done";
    });

    const calls = Array.from({ length: 9 }, () => call(harness, "slow"));
    await waitFor(() => started === 8);
    expect(maximum).toBe(8);
    expect(started).toBe(8);
    releases.shift()?.();
    await waitFor(() => started === 9);
    for (const release of releases.splice(0)) release();

    await expect(Promise.all(calls)).resolves.toHaveLength(9);
    expect(maximum).toBe(8);
  });

  it("a per-tool limit tightens but never raises the client bound", async () => {
    const harness = await hostHarness();
    const limitedReleases: Array<() => void> = [];
    let limitedActive = 0;
    let limitedMaximum = 0;
    harness.registry.register(
      "limited",
      objectSchema,
      async () => {
        limitedActive += 1;
        limitedMaximum = Math.max(limitedMaximum, limitedActive);
        await new Promise<void>((resolve) => limitedReleases.push(resolve));
        limitedActive -= 1;
        return "done";
      },
      { concurrency: 2 },
    );
    const limited = Array.from({ length: 3 }, () => call(harness, "limited"));
    await waitFor(() => limitedReleases.length === 2);
    expect(limitedMaximum).toBe(2);
    limitedReleases.shift()?.();
    await waitFor(() => limitedReleases.length === 2);
    for (const release of limitedReleases.splice(0)) release();
    await Promise.all(limited);

    const wideReleases: Array<() => void> = [];
    let wideActive = 0;
    let wideMaximum = 0;
    harness.registry.register(
      "wide",
      objectSchema,
      async () => {
        wideActive += 1;
        wideMaximum = Math.max(wideMaximum, wideActive);
        await new Promise<void>((resolve) => wideReleases.push(resolve));
        wideActive -= 1;
        return "done";
      },
      { concurrency: 99 },
    );
    const wide = Array.from({ length: 9 }, () => call(harness, "wide"));
    await waitFor(() => wideReleases.length === 8);
    expect(wideMaximum).toBe(8);
    wideReleases.shift()?.();
    await waitFor(() => wideReleases.length === 8);
    for (const release of wideReleases.splice(0)) release();
    await Promise.all(wide);
    expect(wideMaximum).toBe(8);
  });

  it("handlers receive an abort signal that cancellation fires", async () => {
    const harness = await hostHarness({ concurrency: 1 });
    const received = deferred<AbortSignal>();
    harness.registry.register("blocked", objectSchema, (_arguments, { signal }) => {
      received.resolve(signal);
      return new Promise<never>(() => undefined);
    });
    harness.registry.register("next", objectSchema, () => "next");

    const controller = new AbortController();
    const first = fetch(harness.server.url ?? "", {
      body: JSON.stringify({
        id: 1,
        jsonrpc: "2.0",
        method: "tools/call",
        params: { arguments: {}, name: "blocked" },
      }),
      headers: { ...(harness.server.headers ?? {}), "Content-Type": "application/json" },
      method: "POST",
      signal: controller.signal,
    }).catch((error: unknown) => error);
    const signal = await received.promise;
    controller.abort();
    await waitFor(() => signal.aborted);

    await expect(call(harness, "next")).resolves.toEqual({
      content: [{ text: "next", type: "text" }],
    });
    await first;
  });

  it("results normalise three ways and an explicit result passes through", async () => {
    const harness = await hostHarness();
    const explicit: CallToolResult = {
      content: [{ text: "explicit", type: "text" }],
      isError: false,
      structuredContent: { source: "caller" },
    };
    harness.registry.register("text", objectSchema, () => "plain");
    harness.registry.register("json", objectSchema, () => ({ answer: 42 }));
    harness.registry.register("explicit", objectSchema, () => explicit);

    expect(await call(harness, "text")).toEqual({
      content: [{ text: "plain", type: "text" }],
    });
    expect(await call(harness, "json")).toEqual({
      content: [{ text: '{"answer":42}', type: "text" }],
      structuredContent: { answer: 42 },
    });
    expect(await call(harness, "explicit")).toEqual(explicit);
  });

  it("a thrown exception is generic to the model and full to diagnostics", async () => {
    const harness = await hostHarness();
    const cause = new Error("database password=do-not-show", {
      cause: new Error("private root cause"),
    });
    harness.registry.register("explode", objectSchema, () => {
      throw cause;
    });

    const result = await call(harness, "explode");
    const modelText = String(result.content[0]?.text);
    expect(result.isError).toBe(true);
    expect(modelText).toMatch(/correlation id: [a-f0-9]{24}/);
    expect(modelText).not.toContain(cause.message);
    expect(modelText).not.toContain("private root cause");
    expect(modelText).not.toContain(cause.stack ?? "Error");

    expect(harness.diagnostics).toHaveLength(1);
    expect(harness.diagnostics[0]?.cause).toBe(cause);
    expect(harness.diagnostics[0]).toMatchObject({
      code: "tool_handler_failed",
      fields: {
        correlationId: modelText.match(/correlation id: ([a-f0-9]{24})/)?.[1],
        tool: "explode",
      },
      level: "error",
    });
  });

  it("an intentional error result is delivered verbatim", async () => {
    const harness = await hostHarness();
    const intentional: CallToolResult = {
      content: [{ text: "Retry with a smaller date range", type: "text" }],
      isError: true,
    };
    harness.registry.register("intentional", objectSchema, () => intentional);

    expect(await call(harness, "intentional")).toEqual(intentional);
    expect(harness.diagnostics).toEqual([]);
  });

  it("closing the client stops the listener and drains the queue", async () => {
    const queuedAccepted = deferred<void>();
    let admitted = 0;
    const harness = await hostHarness({
      concurrency: 1,
      scheduler: {
        schedule: (callback, delayMs) => {
          admitted += 1;
          if (admitted === 2) queuedAccepted.resolve();
          return setTimeout(callback, delayMs);
        },
      },
    });
    let queuedStarted = false;
    const inFlight = deferred<AbortSignal>();
    harness.registry.register("blocked", objectSchema, (_arguments, { signal }) => {
      inFlight.resolve(signal);
      return new Promise<never>(() => undefined);
    });
    harness.registry.register("queued", objectSchema, () => {
      queuedStarted = true;
      return "unexpected";
    });
    const first = call(harness, "blocked").catch((error: unknown) => error);
    const signal = await inFlight.promise;
    const second = call(harness, "queued").catch((error: unknown) => error);
    await queuedAccepted.promise;
    const port = Number(new URL(harness.server.url ?? "").port);

    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1 }),
      });
    });
    const client = withToolRegistration(
      connectTransport({
        internal: { toolHost: harness.registry },
        owned: false,
        transport,
        transportKind: "grpc",
        visibility: false,
      }),
      harness.registry,
    );
    await client.close();
    await Promise.allSettled([first, second]);

    expect(signal.aborted).toBe(true);
    expect(queuedStarted).toBe(false);
    const replacement = createServer();
    await new Promise<void>((resolve, reject) => {
      replacement.once("error", reject);
      replacement.listen(port, "127.0.0.1", resolve);
    });
    await new Promise<void>((resolve) => replacement.close(() => resolve()));
  });

  it("the bearer never reaches argv the child environment or disk", async () => {
    let launch:
      | {
          readonly args: readonly string[];
          readonly env: NodeJS.ProcessEnv;
        }
      | undefined;
    let running = true;
    const exited = deferred<{ code: number | null; signal: NodeJS.Signals | null }>();
    const created: Array<{ mcpServers?: SessionMcpServer[] }> = [];
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        createSession: (input) => {
          created.push(input);
          return { sessionId: "hosted-session" };
        },
        getCompatibilityInfo: () => ({ apiMajor: 1, features: ["mcp_servers_on_create"] }),
        listMcpSources: () => ({ sources: [] }),
      });
    });
    const client = await spawnInternal(
      { binaryPath: process.execPath },
      {
        createTransport: () => transport,
        launcher: (input) => {
          launch = input;
          const readyIndex = input.args.indexOf("--ready-file");
          const socketIndex = input.args.indexOf("--grpc-unix-socket");
          const readyFile = input.args[readyIndex + 1] ?? "";
          const socketPath = input.args[socketIndex + 1] ?? "";
          void writeFile(
            readyFile,
            JSON.stringify({
              api_major: 1,
              features: ["mcp_servers_on_create"],
              pid: 999,
              schema: "mecated-ready/1",
              socket_path: socketPath,
              transport: "unix",
            }),
          );
          return {
            closeLifetime: () => undefined,
            exit: exited.promise,
            isRunning: () => running,
            kill: (signal: "SIGKILL" | "SIGTERM") => {
              running = false;
              exited.resolve({ code: null, signal });
            },
          };
        },
        tempDirectory: testRoot,
      },
    );
    client.tool("safe", objectSchema, () => "safe");
    await client.sessions.create({});

    const bearer = created[0]?.mcpServers?.[0]?.headers?.Authorization ?? "";
    expect(bearer).toMatch(/^Bearer /);
    expect(JSON.stringify(launch?.args)).not.toContain(bearer);
    expect(JSON.stringify(launch?.env)).not.toContain(bearer);
    expect(JSON.stringify(client.daemon)).not.toContain(bearer);
    const readyFile = launch?.args[(launch?.args.indexOf("--ready-file") ?? -2) + 1] ?? "";
    const runtimeFiles = await readdir(join(readyFile, ".."));
    for (const file of runtimeFiles) {
      const contents = await readFile(join(readyFile, "..", file), "utf8").catch(() => "");
      expect(contents, file).not.toContain(bearer);
    }
    await client.close();
  });

  it("the host emits no cors headers and refuses foreign origin or host", async () => {
    const harness = await hostHarness();
    for (const method of ["GET", "DELETE", "OPTIONS", "PUT"]) {
      const response = await wire(harness.server.url ?? "", { method });
      expect(response.status, method).toBe(405);
      expect(response.headers.allow, method).toBe("POST");
      expect(Object.keys(response.headers).some((name) => name.startsWith("access-control"))).toBe(
        false,
      );
    }
    const requestBody = JSON.stringify({ id: 1, jsonrpc: "2.0", method: "ping" });
    const foreignOrigin = await wire(harness.server.url ?? "", {
      body: requestBody,
      headers: { ...(harness.server.headers ?? {}), Origin: "https://attacker.example" },
    });
    const foreignHost = await wire(harness.server.url ?? "", {
      body: requestBody,
      headers: { ...(harness.server.headers ?? {}), Host: "attacker.example" },
    });
    expect(foreignOrigin.status).toBe(403);
    expect(foreignHost.status).toBe(403);
    expect(foreignOrigin.headers).not.toHaveProperty("access-control-allow-origin");
    expect(foreignHost.headers).not.toHaveProperty("access-control-allow-origin");
  });

  it("the host bounds bodies queue depth and call deadlines", async () => {
    const unauthenticated = await hostHarness();
    const refusedBeforeEnd = new Promise<number>((resolve, reject) => {
      const request = httpRequest(
        unauthenticated.server.url ?? "",
        { headers: { "Content-Length": "1000" }, method: "POST" },
        (response) => {
          resolve(response.statusCode ?? 0);
          response.resume();
        },
      );
      request.once("error", (error) => {
        if ((error as NodeJS.ErrnoException).code !== "ECONNRESET") reject(error);
      });
      request.write("{");
    });
    await expect(refusedBeforeEnd).resolves.toBe(401);

    const bodyBound = await hostHarness({ bodyBytes: 64 });
    const tooLarge = await wire(bodyBound.server.url ?? "", {
      body: "x".repeat(65),
      headers: bodyBound.server.headers ?? {},
    });
    expect(tooLarge.status).toBe(413);

    const queueBound = await hostHarness({ concurrency: 1, queueDepth: 1 });
    const firstStarted = deferred<void>();
    queueBound.registry.register("blocked", objectSchema, () => {
      firstStarted.resolve();
      return new Promise<never>(() => undefined);
    });
    queueBound.registry.register("queued", objectSchema, () => "queued");
    const first = call(queueBound, "blocked").catch((error: unknown) => error);
    await firstStarted.promise;
    const queued = call(queueBound, "queued").catch((error: unknown) => error);
    const overflow = await call(queueBound, "queued");
    expect(overflow).toMatchObject({
      content: [{ text: expect.stringContaining("1-call queue is full") }],
      isError: true,
    });

    const deadlineBound = await hostHarness({ callTimeoutMs: 30 });
    let deadlineSignal: AbortSignal | undefined;
    deadlineBound.registry.register("deadline", objectSchema, (_arguments, { signal }) => {
      deadlineSignal = signal;
      return new Promise<never>(() => undefined);
    });
    const deadline = await call(deadlineBound, "deadline");
    expect(deadline).toMatchObject({
      content: [{ text: expect.stringContaining("30ms deadline") }],
      isError: true,
    });
    expect(deadlineSignal?.aborted).toBe(true);
    queueBound.registry.abort(new Error("release queue test"));
    await Promise.allSettled([first, queued]);
  });

  it("the initialize result declares a negotiable protocol version set", async () => {
    const harness = await hostHarness();
    const supported = await rpc(harness, "initialize", { protocolVersion: "2025-06-18" });
    expect(supported.value.result).toEqual({
      capabilities: { tools: {} },
      protocolVersion: "2025-06-18",
      serverInfo: { name: "@stacklok/mecatl-sdk", version: "0.0.1" },
      supportedVersions: MCP_PROTOCOL_VERSIONS,
    });
    expect(MCP_PROTOCOL_VERSIONS).toContain(
      (supported.value.result as { protocolVersion: string }).protocolVersion,
    );

    const fallback = await rpc(harness, "initialize", { protocolVersion: "2099-01-01" }, 2);
    expect(fallback.value).toMatchObject({ result: { protocolVersion: "2025-11-25" } });
  });

  it("an over-cap handler result is refused client-side rather than mangled", async () => {
    const harness = await hostHarness();
    harness.registry.register("oversized", objectSchema, () =>
      "x".repeat(TOOLKIT_MAX_OUTPUT_BYTES),
    );

    const result = await call(harness, "oversized");
    expect(result).toMatchObject({
      content: [
        {
          text: expect.stringContaining(
            `Callback tool "oversized" result exceeded the ${TOOLKIT_MAX_OUTPUT_BYTES}-byte output cap`,
          ),
          type: "text",
        },
      ],
      isError: true,
    });
    expect(String(result.content[0]?.text)).not.toContain("x".repeat(1_000));
  });
});
