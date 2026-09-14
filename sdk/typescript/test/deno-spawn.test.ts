import { afterEach, expect, test, vi } from "vitest";
import { spawn } from "../src/deno-spawn.js";
import { createNodeTransport } from "../src/node-transport.js";

vi.mock("../src/node-transport.js", () => ({ createNodeTransport: vi.fn() }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

function fixture(ready: unknown) {
  const directory = "/private/deno-test";
  let resolveExit!: (status: { code: number; signal: null; success: boolean }) => void;
  const status = new Promise<{ code: number; signal: null; success: boolean }>((resolve) => {
    resolveExit = resolve;
  });
  const close = vi.fn(() => resolveExit({ code: 0, signal: null, success: true }));
  const kill = vi.fn();
  const remove = vi.fn(async () => {});
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  vi.stubGlobal("Deno", {
    Command: class {
      spawn() {
        return {
          pid: 42,
          status,
          stdin: new WritableStream({ close }),
          stderr: new ReadableStream({ start: (controller) => controller.close() }),
          kill,
        };
      }
    },
    makeTempDir: async () => directory,
    readTextFile: async () => {
      if (ready === undefined) throw Object.assign(new Error("missing"), { name: "NotFound" });
      return JSON.stringify(ready);
    },
    remove,
  });
  return { close, directory, fetch, kill, remove };
}

const validReady = {
  schema: "mecated-ready/1",
  pid: 42,
  api_major: 1,
  features: [],
  transport: "tcp",
  grpc_address: "127.0.0.1:9000",
};

test.each([
  ["stale pid", { pid: 43 }, "does not match the spawned daemon"],
  ["invalid pid", { pid: 0 }, "valid daemon pid"],
  ["remote address", { grpc_address: "example.com:9000" }, "loopback gRPC address"],
  ["unbound address", { grpc_address: "127.0.0.1:0" }, "loopback gRPC address"],
  ["out-of-range port", { grpc_address: "127.0.0.1:65536" }, "loopback gRPC address"],
  ["non-canonical port", { grpc_address: "127.0.0.1:09000" }, "loopback gRPC address"],
  ["Unix transport", { transport: "unix" }, "TCP transport"],
  ["missing transport", { transport: undefined }, "TCP transport"],
  ["unsupported schema", { schema: "mecated-ready/99" }, "Unsupported ready-file schema"],
] as const)(
  "Deno spawn rejects %s before connecting and cleans up",
  async (_name, override, message) => {
    const checks = fixture({ ...validReady, ...override });
    await expect(spawn()).rejects.toMatchObject({
      code: "spawn_failed",
      message: expect.stringContaining(message),
    });
    expect(checks.fetch).not.toHaveBeenCalled();
    expect(createNodeTransport).not.toHaveBeenCalled();
    expect(checks.close).toHaveBeenCalledOnce();
    expect(checks.kill).not.toHaveBeenCalled();
    expect(checks.remove).toHaveBeenCalledExactlyOnceWith(checks.directory, { recursive: true });
  },
);

test("Deno readiness timeout closes stdin and removes the runtime directory", async () => {
  const checks = fixture(undefined);
  await expect(spawn({ readinessTimeoutMs: 30 })).rejects.toMatchObject({
    code: "readiness_timeout",
  });
  expect(checks.fetch).not.toHaveBeenCalled();
  expect(createNodeTransport).not.toHaveBeenCalled();
  expect(checks.close).toHaveBeenCalledOnce();
  expect(checks.kill).not.toHaveBeenCalled();
  expect(checks.remove).toHaveBeenCalledExactlyOnceWith(checks.directory, { recursive: true });
});
