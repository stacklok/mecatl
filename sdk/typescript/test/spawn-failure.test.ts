import { chmod, lstat, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import type { DiagnosticRecord, MecatlError } from "../src/index.js";
import { spawnInternal } from "../src/spawn.js";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
const scratchRoot = join(repositoryRoot, ".scratch");

interface CapturedLaunch {
  args: readonly string[];
  cwd: string;
  env: NodeJS.ProcessEnv;
  executable: string;
  shell: false;
  stdio: readonly ["ignore", "ignore", "pipe"] | readonly ["ignore", "ignore", "pipe", "pipe"];
}

interface ProcessExit {
  code: number | null;
  signal: NodeJS.Signals | null;
}

interface FailureError extends MecatlError {
  exitCode?: number | null;
  signal?: NodeJS.Signals | null;
  stderrTail?: string;
}

interface HarnessOptions {
  document?: object | false;
  exitBeforeReady?: ProcessExit;
  ignoreSigterm?: boolean;
  stderr?: string;
  transport?: Transport;
}

interface FailureHarness {
  events: string[];
  internal: {
    clock: { now(): number };
    createTransport(): Transport;
    fileSystem: {
      rm(path: string, options: { force: true; recursive: true }): Promise<void>;
    };
    launcher(request: CapturedLaunch): {
      closeLifetime(): void;
      exit: Promise<ProcessExit>;
      isRunning(): boolean;
      kill(signal: "SIGKILL" | "SIGTERM"): void;
      stderrTail(): Uint8Array;
    };
    scheduler: { sleep(ms: number): Promise<void> };
    tempDirectory: string;
  };
  isRunning(): boolean;
  killed: ("SIGKILL" | "SIGTERM")[];
  requests: CapturedLaunch[];
}

let testRoot: string;

beforeEach(async () => {
  await mkdir(scratchRoot, { recursive: true });
  testRoot = await mkdtemp(join(scratchRoot, "sdk-spawn-failure-test-"));
});

afterEach(async () => {
  vi.restoreAllMocks();
  await rm(testRoot, { force: true, recursive: true });
});

function argumentValue(args: readonly string[], flag: string): string {
  const index = args.indexOf(flag);
  const value = args[index + 1];
  if (index === -1 || value === undefined) throw new Error(`missing ${flag}`);
  return value;
}

function readyDocument(socketPath: string, overrides: Record<string, unknown> = {}): object {
  return {
    schema: "mecated-ready/1",
    pid: 4242,
    transport: "unix",
    grpc_address: socketPath,
    socket_path: socketPath,
    api_major: 1,
    features: ["mcp_servers_on_create"],
    ...overrides,
  };
}

function routerTransport(fails = false): Transport {
  return createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => {
        if (fails) throw new Error("first dial refused");
        return { apiMajor: 1 };
      },
    });
  });
}

function launchHarness(options: HarnessOptions = {}): FailureHarness {
  const requests: CapturedLaunch[] = [];
  const killed: ("SIGKILL" | "SIGTERM")[] = [];
  const events: string[] = [];
  let now = 0;
  let running = false;

  return {
    events,
    internal: {
      clock: { now: () => now },
      createTransport: () => options.transport ?? routerTransport(),
      fileSystem: {
        rm: async (path, rmOptions) => {
          events.push("rm");
          await rm(path, rmOptions);
        },
      },
      launcher: (request) => {
        requests.push(request);
        running = true;
        let resolveExit: ((status: ProcessExit) => void) | undefined;
        const exit = new Promise<ProcessExit>((resolvePromise) => {
          resolveExit = resolvePromise;
        });
        const finish = (status: ProcessExit) => {
          if (!running) return;
          running = false;
          resolveExit?.(status);
        };
        if (options.exitBeforeReady !== undefined) {
          setImmediate(() => finish(options.exitBeforeReady ?? { code: 1, signal: null }));
        } else if (options.document !== false) {
          const readyFile = argumentValue(request.args, "--ready-file");
          const socketPath = argumentValue(request.args, "--grpc-unix-socket");
          setImmediate(() => {
            void writeFile(
              readyFile,
              JSON.stringify(options.document ?? readyDocument(socketPath)),
            );
          });
        }
        return {
          closeLifetime: () => events.push("close-lifetime"),
          exit,
          isRunning: () => running,
          kill: (signal) => {
            killed.push(signal);
            events.push(signal);
            if (signal === "SIGTERM" && options.ignoreSigterm === true) return;
            finish({ code: null, signal });
          },
          stderrTail: () => Buffer.from(options.stderr ?? ""),
        };
      },
      scheduler: {
        sleep: async (ms) => {
          now += ms;
          if (ms >= 3_000 && options.ignoreSigterm !== true) {
            await new Promise<void>(() => undefined);
          }
          await new Promise<void>((resolveWait) => setImmediate(resolveWait));
        },
      },
      tempDirectory: testRoot,
    },
    isRunning: () => running,
    killed,
    requests,
  };
}

async function failure(promise: Promise<unknown>): Promise<FailureError> {
  try {
    await promise;
  } catch (error) {
    return error as FailureError;
  }
  throw new Error("expected spawn to fail");
}

async function expectMissing(path: string): Promise<void> {
  await expect(lstat(path)).rejects.toMatchObject({ code: "ENOENT" });
}

describe("spawn startup failure", () => {
  it("a child exiting before readiness is spawn_failed with its exit status", async () => {
    const harness = launchHarness({ exitBeforeReady: { code: 23, signal: null } });
    const error = await failure(spawnInternal({ binaryPath: process.execPath }, harness.internal));

    expect(error).toMatchObject({ code: "spawn_failed", exitCode: 23, signal: null });
    expect(error.message).toContain("code=23, signal=null");
    expect(harness.killed).toEqual([]);
  });

  it("a readiness timeout is typed and leaves no orphan child", async () => {
    const harness = launchHarness({ document: false });
    const error = await failure(
      spawnInternal({ binaryPath: process.execPath, readinessTimeoutMs: 1 }, harness.internal),
    );

    expect(error.code).toBe("readiness_timeout");
    expect(harness.killed).toEqual(["SIGTERM"]);
    expect(harness.isRunning()).toBe(false);
  });

  it("the stderr tail is bounded from the end of the stream", async () => {
    const executable = join(testRoot, "stderr-failure.mjs");
    await writeFile(
      executable,
      `#!${process.execPath}\nimport { writeSync } from "node:fs";\nwriteSync(2, "partial-" + "x".repeat(70_000) + "\\ncomplete-line\\nlast-line\\n");\nprocess.exit(19);\n`,
    );
    await chmod(executable, 0o700);
    const error = await failure(
      spawnInternal(
        { binaryPath: executable },
        { createTransport: () => routerTransport(), tempDirectory: testRoot },
      ),
    );

    expect(error.stderrTail).toBe("complete-line\nlast-line\n");
    expect(Buffer.byteLength(error.stderrTail ?? "")).toBeLessThanOrEqual(4 * 1024);
    expect(error.message).not.toContain("partial-");
  });

  it("a secret-shaped stderr line is redacted wholesale", async () => {
    const secrets = [
      "assignment-secret",
      "sk-direct-secret",
      "ghp_directsecret",
      "xoxb-direct-secret",
      "eyJhbGciOiJIUzI1NiJ9.payload.signature",
    ];
    const stderr = `${"truncated".repeat(600)}\n${[
      `OPENAI_API_KEY =${secrets[0]}`,
      secrets[1],
      secrets[2],
      secrets[3],
      secrets[4],
      "safe startup context",
      "",
    ].join("\n")}`;
    const diagnostics: DiagnosticRecord[] = [];
    const harness = launchHarness({ exitBeforeReady: { code: 2, signal: null }, stderr });
    const error = await failure(
      spawnInternal(
        { binaryPath: process.execPath, diagnostics: (record) => diagnostics.push(record) },
        harness.internal,
      ),
    );

    expect(error.stderrTail).toBe(
      `${Array.from({ length: secrets.length }, () => "[REDACTED]").join("\n")}\nsafe startup context\n`,
    );
    const outward = `${error.message}\n${JSON.stringify(diagnostics)}`;
    for (const secret of secrets) expect(outward).not.toContain(secret);
  });

  it("a failed spawn leaves no runtime directory or socket", async () => {
    const unknownSchema = launchHarness({
      document: { schema: "mecated-ready/2", socket_path: join(testRoot, "future.sock") },
    });
    await failure(spawnInternal({ binaryPath: process.execPath }, unknownSchema.internal));
    const unknownRequest = unknownSchema.requests[0];
    if (unknownRequest === undefined) throw new Error("missing launch request");
    const unknownSocket = argumentValue(unknownRequest.args, "--grpc-unix-socket");
    await expectMissing(dirname(unknownSocket));
    await expectMissing(unknownSocket);

    const failedDial = launchHarness({ transport: routerTransport(true) });
    await failure(spawnInternal({ binaryPath: process.execPath }, failedDial.internal));
    const dialRequest = failedDial.requests[0];
    if (dialRequest === undefined) throw new Error("missing launch request");
    const dialSocket = argumentValue(dialRequest.args, "--grpc-unix-socket");
    await expectMissing(dirname(dialSocket));
    await expectMissing(dialSocket);
  });

  it("a failed spawn never requests a tcp listener", async () => {
    const published = readyDocument(join(testRoot, "published.sock"));
    const harness = launchHarness({ document: published, transport: routerTransport(true) });
    await failure(spawnInternal({ binaryPath: process.execPath }, harness.internal));
    const request = harness.requests[0];
    if (request === undefined) throw new Error("missing launch request");

    expect(request.args).not.toContain("--grpc-addr");
    expect(argumentValue(request.args, "--http-addr")).toBe("");
    expect(request.args).toContain("--grpc-unix-socket");
    expect(published).toMatchObject({ transport: "unix" });
    expect(published).not.toHaveProperty("http_address");
  });

  it("no sink means nothing is written to console", async () => {
    const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
    const error = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const info = vi.spyOn(console, "info").mockImplementation(() => undefined);
    const log = vi.spyOn(console, "log").mockImplementation(() => undefined);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const harness = launchHarness({ exitBeforeReady: { code: 1, signal: null } });

    await failure(spawnInternal({ binaryPath: process.execPath }, harness.internal));

    for (const spy of [debug, error, info, log, warn]) expect(spy).not.toHaveBeenCalled();
  });

  it("the diagnostics sink receives one structured startup record", async () => {
    const diagnostics: DiagnosticRecord[] = [];
    const harness = launchHarness({
      exitBeforeReady: { code: 17, signal: null },
      stderr: "safe failure detail\n",
    });
    await failure(
      spawnInternal(
        { binaryPath: process.execPath, diagnostics: (record) => diagnostics.push(record) },
        harness.internal,
      ),
    );

    expect(diagnostics).toEqual([
      {
        code: "spawn_failed",
        fields: {
          exitCode: 17,
          signal: null,
          stderrTail: "safe failure detail\n",
        },
        level: "error",
        message: expect.stringContaining("mecated exited before readiness"),
      },
    ]);
    expect(diagnostics[0]).not.toHaveProperty("kind");
  });

  it("a post-launch failure stops the child before removing its directory", async () => {
    const harness = launchHarness({
      document: { schema: "mecated-ready/2" },
      ignoreSigterm: true,
    });
    await failure(spawnInternal({ binaryPath: process.execPath }, harness.internal));

    expect(harness.killed).toEqual(["SIGTERM", "SIGKILL"]);
    const term = harness.events.indexOf("SIGTERM");
    const kill = harness.events.indexOf("SIGKILL");
    const remove = harness.events.lastIndexOf("rm");
    expect(term).toBeGreaterThanOrEqual(0);
    expect(kill).toBeGreaterThan(term);
    expect(remove).toBeGreaterThan(kill);
    expect(harness.isRunning()).toBe(false);
  });
});
