import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MECATL_ERROR_CODES, ServerError, UnsupportedFeatureError } from "../src/errors.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { connect } from "../src/node-client.js";
import { spawnInternal } from "../src/spawn.js";
import type { ToolSchema } from "../src/tool.js";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const scratchRoot = join(repositoryRoot, ".scratch");
const callbackFeature = "mcp_servers_on_create";
const objectSchema = { type: "object" } as const satisfies ToolSchema;

let testRoot: string;

beforeEach(async () => {
  await mkdir(scratchRoot, { recursive: true });
  testRoot = await mkdtemp(join(scratchRoot, "sdk-tool-refusal-test-"));
});

afterEach(async () => {
  await rm(testRoot, { force: true, recursive: true });
});

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)];
}

function field(number: number, value: number[]): number[] {
  return [(number << 3) | 2, value.length, ...value];
}

function serverError(reason: string, status: Code): ConnectError {
  const error = new ConnectError(`${reason} from gRPC`, status);
  (error.details as unknown[]).push({
    type: "google.rpc.ErrorInfo",
    value: new Uint8Array([...field(1, bytes(reason)), ...field(2, bytes("mecatl.stacklok.com"))]),
  });
  return error;
}

function argumentValue(args: readonly string[], flag: string): string {
  const index = args.indexOf(flag);
  const value = args[index + 1];
  if (index === -1 || value === undefined) throw new Error(`missing ${flag}`);
  return value;
}

function captureThrow(action: () => unknown): unknown {
  try {
    action();
  } catch (error) {
    return error;
  }
  throw new Error("expected action to throw");
}

interface SpawnHarnessOptions {
  createSessionError?: ConnectError;
  features?: readonly string[];
  http?: boolean;
}

async function spawnHarness(options: SpawnHarnessOptions = {}) {
  const features = [...(options.features ?? [callbackFeature])];
  let createAttempts = 0;
  const createMcpCounts: number[] = [];
  const toolHost = {
    abort: vi.fn(),
    mcpServer: vi.fn(() => ({
      headers: { Authorization: "Bearer test-capability" },
      type: "http" as const,
      url: "http://127.0.0.1:43123/mcp",
    })),
    start: vi.fn(),
    stop: vi.fn(async () => undefined),
  };
  const transport = createRouterTransport((router) => {
    router.service(HarnessService, {
      createSession: (request) => {
        createAttempts += 1;
        createMcpCounts.push(request.mcpServers.length);
        if (options.createSessionError !== undefined) throw options.createSessionError;
        return { sessionId: `session-${createAttempts}` };
      },
      getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features }),
      listMcpSources: () => ({ sources: [] }),
    });
  });
  let running = true;
  let resolveExit:
    | ((exit: { code: number | null; signal: NodeJS.Signals | null }) => void)
    | undefined;
  const exit = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>(
    (resolvePromise) => {
      resolveExit = resolvePromise;
    },
  );
  const client = await spawnInternal(
    { binaryPath: process.execPath, ...(options.http === true ? { http: true } : {}) },
    {
      client: { toolHost },
      createTransport: () => transport,
      launcher: (request) => {
        const socketPath = argumentValue(request.args, "--grpc-unix-socket");
        void writeFile(
          argumentValue(request.args, "--ready-file"),
          JSON.stringify({
            api_major: 1,
            features,
            pid: 4242,
            schema: "mecated-ready/1",
            socket_path: socketPath,
            transport: "unix",
          }),
        );
        return {
          closeLifetime: () => undefined,
          exit,
          isRunning: () => running,
          kill: (signal: "SIGKILL" | "SIGTERM") => {
            running = false;
            resolveExit?.({ code: null, signal });
          },
        };
      },
      scheduler: { sleep: () => new Promise((resolveSleep) => setImmediate(resolveSleep)) },
      tempDirectory: testRoot,
    },
  );
  return { client, createAttempts: () => createAttempts, createMcpCounts, toolHost };
}

describe("callback tool refusals", () => {
  it("a connected client refuses tool with unsupported_feature", async () => {
    const client = connect({
      transport: createRouterTransport((router) => {
        router.service(HarnessService, {
          getCompatibilityInfo: () => ({
            apiMajor: 1,
            capabilities: {},
            features: [callbackFeature],
          }),
        });
      }),
    });
    try {
      const error = captureThrow(() => client.tool("lookup", objectSchema, () => "found"));

      expect(error).toBeInstanceOf(UnsupportedFeatureError);
      expect(error).toMatchObject({ code: "unsupported_feature", transport: "local" });
    } finally {
      await client.close();
    }
  });

  it("the refusal is local and issues no rpc", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({
          apiMajor: 1,
          capabilities: {},
          features: [callbackFeature],
        }),
      });
    });
    const unary = vi.spyOn(transport, "unary");
    const stream = vi.spyOn(transport, "stream");
    const client = connect({
      transport,
    });
    try {
      await vi.waitFor(() => expect(unary).toHaveBeenCalled());
      unary.mockClear();
      stream.mockClear();

      expect(() => client.tool("lookup", objectSchema, () => "found")).toThrowError(
        expect.objectContaining({ code: "unsupported_feature", transport: "local" }),
      );
      expect(unary).not.toHaveBeenCalled();
      expect(stream).not.toHaveBeenCalled();
    } finally {
      await client.close();
    }
  });

  it("an http-enabled spawned client names the missing feature", async () => {
    const harness = await spawnHarness({ features: ["server_info"], http: true });
    try {
      const error = captureThrow(() => harness.client.tool("lookup", objectSchema, () => "found"));

      expect(error).toBeInstanceOf(UnsupportedFeatureError);
      expect(error).toMatchObject({
        code: "unsupported_feature",
        feature: callbackFeature,
        transport: "local",
      });
      expect((error as Error).message).toContain(callbackFeature);
      expect(harness.toolHost.start).not.toHaveBeenCalled();
    } finally {
      await harness.client.close();
    }
  });

  it("a server refusal surfaces client_mcp_unsupported unchanged", async () => {
    const harness = await spawnHarness({
      createSessionError: serverError("client_mcp_unsupported", Code.Unimplemented),
    });
    try {
      harness.client.tool("lookup", objectSchema, () => "found");

      const error = await harness.client.sessions.create({}).catch((reason: unknown) => reason);

      expect(error).toBeInstanceOf(ServerError);
      expect(error).toMatchObject({ code: "client_mcp_unsupported", transport: "grpc" });
      expect(harness.createAttempts()).toBe(1);
      expect(harness.createMcpCounts).toEqual([1]);
    } finally {
      await harness.client.close();
    }
  });

  it("an unreachable host surfaces client_mcp_unreachable", async () => {
    const harness = await spawnHarness({
      createSessionError: serverError("client_mcp_unreachable", Code.Unavailable),
    });
    try {
      harness.client.tool("lookup", objectSchema, () => "found");

      const error = await harness.client.sessions.create({}).catch((reason: unknown) => reason);

      expect(error).toBeInstanceOf(ServerError);
      expect(error).toMatchObject({ code: "client_mcp_unreachable", transport: "grpc" });
      expect(harness.createAttempts()).toBe(1);
      expect(harness.createMcpCounts).toEqual([1]);
      expect(() =>
        harness.client.tool("after-refusal", objectSchema, () => "still open"),
      ).not.toThrow();
    } finally {
      await harness.client.close();
    }
  });

  it("exactly four local codes are added and none shadows a server code", async () => {
    const source = await readFile(
      fileURLToPath(new URL("../src/errors.ts", import.meta.url)),
      "utf8",
    );
    const codes = [...source.matchAll(/^\s*\| "([a-z0-9_]+)" \/\/ M3_LOCAL_ERROR_CODE$/gm)]
      .map((match) => match[1] ?? "")
      .sort();

    expect(codes).toEqual(
      ["spawn_failed", "readiness_timeout", "unsupported_platform", "tool_registration"].sort(),
    );
    const serverCodes = new Set<string>(MECATL_ERROR_CODES);
    expect(codes.filter((code) => serverCodes.has(code))).toEqual([]);
  });
});
