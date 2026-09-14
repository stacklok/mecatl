import { createRouterTransport } from "@connectrpc/connect";
import { afterEach, expect, test, vi } from "vitest";
import { spawn } from "../src/deno-spawn.js";
import { type DiagnosticRecord, MecatlError } from "../src/errors.js";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { createNodeTransport } from "../src/node-transport.js";

vi.mock("../src/node-transport.js", () => ({ createNodeTransport: vi.fn() }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  vi.useRealTimers();
});

interface FixtureOptions {
  stubborn?: boolean;
  stderr?: readonly Uint8Array[];
}

interface CommandOptions {
  args: string[];
  env?: Record<string, string>;
  stderr: "piped";
  stdin: "piped";
  stdout: "null";
}

function fixture(ready: unknown, options: FixtureOptions = {}) {
  const directory = "/private/deno-test";
  const events: { kind: string; at: number }[] = [];
  const requests: { command: string; options: CommandOptions }[] = [];
  let resolveExit!: (status: { code: number; signal: string | null; success: boolean }) => void;
  const status = new Promise<{ code: number; signal: string | null; success: boolean }>(
    (resolve) => {
      resolveExit = resolve;
    },
  );
  let stderrController!: ReadableStreamDefaultController<Uint8Array>;
  const stderr = new ReadableStream<Uint8Array>({
    start(controller) {
      stderrController = controller;
      for (const chunk of options.stderr ?? []) controller.enqueue(chunk);
    },
  });
  const exit = (signal: string | null) => {
    events.push({ kind: "exit", at: Date.now() });
    resolveExit({ code: signal === null ? 0 : 137, signal, success: signal === null });
    stderrController.close();
  };
  const close = vi.fn(() => {
    events.push({ kind: "EOF", at: Date.now() });
    if (!options.stubborn) exit(null);
  });
  const stdin = new WritableStream<Uint8Array>({ close });
  const kill = vi.fn((signal: "SIGTERM" | "SIGKILL") => {
    events.push({ kind: signal, at: Date.now() });
    if (signal === "SIGKILL") exit(signal);
  });
  const remove = vi.fn(async () => {
    events.push({ kind: "remove", at: Date.now() });
  });
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  vi.mocked(createNodeTransport).mockReturnValue(
    createRouterTransport((router) => {
      router.service(HarnessService, { getCompatibilityInfo: () => ({ apiMajor: 1 }) });
    }),
  );
  vi.stubGlobal("Deno", {
    Command: class {
      constructor(command: string, commandOptions: CommandOptions) {
        requests.push({ command, options: commandOptions });
      }
      spawn() {
        return {
          pid: 42,
          status,
          stdin,
          stderr,
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
  return { close, directory, events, fetch, kill, remove, requests, stderr, stdin };
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

test("Deno spawn explicitly disables HTTP while launching gRPC without a shell", async () => {
  const checks = fixture(validReady);
  const client = await spawn({ args: ["--mock"], binaryPath: "/opt/mecated" });
  try {
    expect(checks.requests).toEqual([
      {
        command: "/opt/mecated",
        options: {
          args: [
            "serve",
            "--grpc-addr",
            "127.0.0.1:0",
            "--http-addr",
            "",
            "--ready-file",
            `${checks.directory}/ready.json`,
            "--lifetime-stdin",
            "--mock",
          ],
          stderr: "piped",
          stdin: "piped",
          stdout: "null",
        },
      },
    ]);
    expect(createNodeTransport).toHaveBeenCalledExactlyOnceWith({
      baseUrl: `http://${validReady.grpc_address}`,
    });
  } finally {
    await client.close();
  }
});

test.each(["close", "startup failure"])(
  "Deno %s gives a stubborn child bounded EOF, SIGTERM, and SIGKILL grace periods",
  async (mode) => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    const checks = fixture(
      mode === "close" ? validReady : { ...validReady, schema: "unsupported" },
      { stubborn: true },
    );
    let settled = false;
    const operation =
      mode === "close"
        ? (await spawn()).close()
        : spawn().then(
            () => {
              throw new Error("invalid readiness unexpectedly succeeded");
            },
            (error: unknown) => expect(error).toMatchObject({ code: "spawn_failed" }),
          );
    const completion = operation.then(() => {
      settled = true;
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(checks.events).toEqual([{ kind: "EOF", at: 0 }]);
    expect(settled).toBe(false);
    expect(checks.remove).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(2_999);
    expect(checks.kill).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(checks.events).toEqual([
      { kind: "EOF", at: 0 },
      { kind: "SIGTERM", at: 3_000 },
    ]);
    await vi.advanceTimersByTimeAsync(2_999);
    expect(settled).toBe(false);
    expect(checks.kill).toHaveBeenCalledExactlyOnceWith("SIGTERM");
    await vi.advanceTimersByTimeAsync(1);
    expect(settled).toBe(true);
    await completion;
    expect(checks.events).toEqual([
      { kind: "EOF", at: 0 },
      { kind: "SIGTERM", at: 3_000 },
      { kind: "SIGKILL", at: 6_000 },
      { kind: "exit", at: 6_000 },
      { kind: "remove", at: 6_000 },
    ]);
    expect(checks.close).toHaveBeenCalledOnce();
    expect(checks.stderr.locked).toBe(false);
    expect(checks.stdin.locked).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  },
);

async function spawnFailure(diagnostics: DiagnosticRecord[]) {
  try {
    await spawn({ diagnostics: (record) => diagnostics.push(record) });
  } catch (error) {
    expect(error).toBeInstanceOf(MecatlError);
    return error as MecatlError & { stderrTail?: string };
  }
  throw new Error("invalid readiness unexpectedly succeeded");
}

test("Deno startup preserves TextDecoder handling of BOM and malformed UTF-8 stderr", async () => {
  fixture(
    { ...validReady, schema: "unsupported" },
    { stderr: [new Uint8Array([0xef, 0xbb, 0xbf, 0x61, 0xff, 0x0d, 0x0a])] },
  );
  const diagnostics: DiagnosticRecord[] = [];
  const error = await spawnFailure(diagnostics);
  expect(error.stderrTail).toBe("a\uFFFD\r\n");
  expect(diagnostics[0]?.fields.stderrTail).toBe(error.stderrTail);
});

test("Deno startup reports only complete lines from the last 4 KiB of captured stderr", async () => {
  const diagnostics: DiagnosticRecord[] = [];
  const encoder = new TextEncoder();
  const checks = fixture(
    { ...validReady, schema: "unsupported" },
    {
      stderr: [
        encoder.encode("discarded-context\n"),
        encoder.encode(`partial-${"x".repeat(70_000)}`),
        encoder.encode("\ncomplete-line\nlast-line\n"),
      ],
    },
  );
  const error = await spawnFailure(diagnostics);
  expect(error.stderrTail).toBe("complete-line\nlast-line\n");
  expect(encoder.encode(error.stderrTail).length).toBeLessThanOrEqual(4 * 1024);
  expect(error.message).toBe(
    `Unsupported ready-file schema "unsupported"; expected mecated-ready/1\nstderr tail:\n${error.stderrTail}`,
  );
  expect(diagnostics).toEqual([
    {
      code: "spawn_failed",
      fields: { stderrTail: error.stderrTail },
      level: "error",
      message: error.message,
    },
  ]);
  expect(checks.stderr.locked).toBe(false);
});

test("Deno startup redacts whole credential-shaped stderr lines in errors and diagnostics", async () => {
  const secrets = [
    "assignment-secret",
    "sk-direct-secret",
    "ghp_directsecret",
    "xoxb-direct-secret",
    "eyJhbGciOiJIUzI1NiJ9.payload.signature",
  ];
  const diagnostics: DiagnosticRecord[] = [];
  const lines = [
    `export OPENAI_API_KEY =${secrets[0]}`,
    `prefix ${secrets[1]} suffix`,
    `prefix ${secrets[2]} suffix`,
    `prefix ${secrets[3]} suffix`,
    `prefix ${secrets[4]} suffix`,
    "safe startup context",
    "",
  ];
  fixture(
    { ...validReady, schema: "unsupported" },
    { stderr: [new TextEncoder().encode(lines.join("\r\n"))] },
  );
  const error = await spawnFailure(diagnostics);
  const safeTail = `${"[REDACTED]\r\n".repeat(secrets.length)}safe startup context\r\n`;
  expect(error.stderrTail).toBe(safeTail);
  expect(diagnostics[0]?.fields.stderrTail).toBe(safeTail);
  expect(diagnostics[0]?.message).toBe(error.message);
  for (const secret of secrets) {
    expect(`${error.message}\n${JSON.stringify(diagnostics)}`).not.toContain(secret);
  }
});
