import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  rename,
  rm,
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
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

interface FakeProcessRecord {
  closedLifetime: number;
  killed: ("SIGKILL" | "SIGTERM")[];
}

interface LaunchHarness {
  dials: string[];
  launched: Promise<CapturedLaunch>;
  processes: FakeProcessRecord[];
  requests: CapturedLaunch[];
  transport: Transport;
}

let testRoot: string;

beforeEach(async () => {
  await mkdir(scratchRoot, { recursive: true });
  testRoot = await mkdtemp(join(scratchRoot, "sdk-spawn-test-"));
});

afterEach(async () => {
  await rm(testRoot, { force: true, recursive: true });
});

function argumentValue(args: readonly string[], flag: string): string {
  const index = args.indexOf(flag);
  const value = args[index + 1];
  if (index === -1 || value === undefined) throw new Error(`missing ${flag}`);
  return value;
}

function routerTransport(): Transport {
  return createRouterTransport((router) => {
    router.service(HarnessService, {
      getCompatibilityInfo: () => ({ apiMajor: 1 }),
    });
  });
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

function launchHarness(options: { autoReady?: boolean; document?: object } = {}): LaunchHarness & {
  internal: {
    createTransport(socketPath: string): Transport;
    launcher(request: CapturedLaunch): {
      closeLifetime(): void;
      exit: Promise<{ code: number | null; signal: NodeJS.Signals | null }>;
      isRunning(): boolean;
      kill(signal: "SIGKILL" | "SIGTERM"): void;
    };
    scheduler: { sleep(): Promise<void> };
    tempDirectory: string;
  };
} {
  const requests: CapturedLaunch[] = [];
  const dials: string[] = [];
  const processes: FakeProcessRecord[] = [];
  const transport = routerTransport();
  let resolveLaunch: ((request: CapturedLaunch) => void) | undefined;
  const launched = new Promise<CapturedLaunch>((resolvePromise) => {
    resolveLaunch = resolvePromise;
  });
  return {
    dials,
    internal: {
      createTransport: (socketPath) => {
        dials.push(socketPath);
        return transport;
      },
      launcher: (request) => {
        requests.push(request);
        resolveLaunch?.(request);
        let running = true;
        let resolveExit:
          | ((exit: { code: number | null; signal: NodeJS.Signals | null }) => void)
          | undefined;
        const exit = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>(
          (resolvePromise) => {
            resolveExit = resolvePromise;
          },
        );
        const record: FakeProcessRecord = { closedLifetime: 0, killed: [] };
        processes.push(record);
        if (options.autoReady !== false) {
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
          closeLifetime: () => {
            record.closedLifetime += 1;
          },
          exit,
          isRunning: () => running,
          kill: (signal) => {
            record.killed.push(signal);
            running = false;
            resolveExit?.({ code: null, signal });
          },
        };
      },
      scheduler: { sleep: () => new Promise((resolveSleep) => setImmediate(resolveSleep)) },
      tempDirectory: testRoot,
    },
    launched,
    processes,
    requests,
    transport,
  };
}

async function waitForLaunch(harness: LaunchHarness): Promise<CapturedLaunch> {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      harness.launched,
      new Promise<never>((_resolve, reject) => {
        timeout = setTimeout(() => reject(new Error("launcher was not called")), 5_000);
      }),
    ]);
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }
}

describe("spawn", () => {
  it("an explicit binaryPath wins over MECATED_BIN and PATH", async () => {
    const harness = launchHarness();
    const accessed: string[] = [];
    const env = new Proxy<NodeJS.ProcessEnv>(
      {},
      {
        get: (_target, property) => {
          accessed.push(String(property));
          throw new Error(`unexpected environment lookup: ${String(property)}`);
        },
      },
    );

    const client = await spawnInternal(
      { binaryPath: process.execPath },
      { ...harness.internal, env },
    );
    expect(harness.requests[0]?.executable).toBe(process.execPath);
    expect(accessed).toEqual([]);
    await client.close();
  });

  it("resolution falls back MECATED_BIN then PATH without invoking a shell", async () => {
    const configured = launchHarness();
    const configuredClient = await spawnInternal(
      {},
      {
        ...configured.internal,
        env: { MECATED_BIN: process.execPath, PATH: "/must/not/be/read" },
      },
    );
    expect(configured.requests[0]).toMatchObject({ executable: process.execPath, shell: false });
    await configuredClient.close();

    const metacharDirectory = join(testRoot, "path;$(not-a-shell)");
    await mkdir(metacharDirectory);
    const pathBinary = join(metacharDirectory, "mecated");
    await writeFile(pathBinary, "not executed by the launcher seam");
    await chmod(pathBinary, 0o700);
    const pathHarness = launchHarness();
    const pathClient = await spawnInternal(
      {},
      { ...pathHarness.internal, env: { PATH: metacharDirectory } },
    );
    expect(pathHarness.requests[0]).toMatchObject({ executable: pathBinary, shell: false });
    await pathClient.close();
  });

  it("an unresolvable binary fails typed before any process is created", async () => {
    const missingHarness = launchHarness();
    await expect(
      spawnInternal({ binaryPath: join(testRoot, "missing") }, missingHarness.internal),
    ).rejects.toMatchObject({
      code: "spawn_failed",
      message: expect.stringContaining("binaryPath"),
    });

    const directoryHarness = launchHarness();
    await expect(
      spawnInternal({}, { ...directoryHarness.internal, env: { MECATED_BIN: testRoot } }),
    ).rejects.toMatchObject({
      code: "spawn_failed",
      message: expect.stringContaining("MECATED_BIN"),
    });

    const nonExecutable = join(testRoot, "path", "mecated");
    await mkdir(dirname(nonExecutable));
    await writeFile(nonExecutable, "not executable");
    await chmod(nonExecutable, 0o600);
    const pathHarness = launchHarness();
    await expect(
      spawnInternal({}, { ...pathHarness.internal, env: { PATH: dirname(nonExecutable) } }),
    ).rejects.toMatchObject({ code: "spawn_failed", message: expect.stringContaining("PATH") });

    expect([
      ...missingHarness.requests,
      ...directoryHarness.requests,
      ...pathHarness.requests,
    ]).toHaveLength(0);
  });

  it("the default argv is the tool-capable UDS topology", async () => {
    const harness = launchHarness();
    const client = await spawnInternal({ binaryPath: process.execPath }, harness.internal);
    const args = harness.requests[0]?.args ?? [];
    expect(args.slice(0, 9)).toEqual([
      "serve",
      "--grpc-unix-socket",
      expect.any(String),
      "--http-addr",
      "",
      "--ready-file",
      expect.any(String),
      "--lifetime-pipe-fd",
      "3",
    ]);
    const socketPath = argumentValue(args, "--grpc-unix-socket");
    const readyFile = argumentValue(args, "--ready-file");
    expect(dirname(socketPath)).toBe(dirname(readyFile));
    await client.close();
  });

  it("an extra argument colliding with an SDK-owned flag is refused", async () => {
    const harness = launchHarness();
    await expect(
      spawnInternal(
        { args: ["--http-addr=127.0.0.1:0"], binaryPath: process.execPath },
        harness.internal,
      ),
    ).rejects.toMatchObject({
      code: "spawn_failed",
      message: expect.stringContaining("--http-addr"),
    });
    expect(harness.requests).toHaveLength(0);
  });

  it("spawn resolves on the ready document and dials its socket path", async () => {
    const harness = launchHarness({ autoReady: false });
    const spawning = spawnInternal({ binaryPath: process.execPath }, harness.internal);
    const request = await waitForLaunch(harness);
    const readyFile = argumentValue(request.args, "--ready-file");
    await writeFile(readyFile, "{");
    await new Promise((resolveWait) => setImmediate(resolveWait));
    expect(harness.dials).toEqual([]);

    const publishedSocket = join(testRoot, "published-by-daemon.sock");
    await writeFile(readyFile, JSON.stringify(readyDocument(publishedSocket)));
    const client = await spawning;
    expect(harness.dials).toEqual([publishedSocket]);
    await client.close();
  });

  it("an unknown ready-file schema fails rather than degrading", async () => {
    const harness = launchHarness({
      document: { schema: "mecated-ready/2", socket_path: join(testRoot, "future.sock") },
    });
    await expect(
      spawnInternal({ binaryPath: process.execPath }, harness.internal),
    ).rejects.toMatchObject({
      code: "spawn_failed",
      message: expect.stringContaining("mecated-ready/2"),
    });
    expect(harness.dials).toEqual([]);
    expect(harness.processes[0]?.killed).toEqual(["SIGTERM"]);
  });

  it("the runtime directory is private and removed on disposal", async () => {
    const harness = launchHarness();
    const client = await spawnInternal({ binaryPath: process.execPath }, harness.internal);
    const request = harness.requests[0];
    if (request === undefined) throw new Error("missing launch request");
    const runtimeDirectory = dirname(argumentValue(request.args, "--ready-file"));
    expect((await stat(runtimeDirectory)).mode & 0o777).toBe(0o700);
    expect(runtimeDirectory).not.toBe(join(testRoot, "mecatl-sdk"));

    const original = `${runtimeDirectory}-original`;
    const protectedDirectory = join(testRoot, "protected");
    await mkdir(protectedDirectory);
    await writeFile(join(protectedDirectory, "sentinel"), "keep");
    await rename(runtimeDirectory, original);
    await symlink(protectedDirectory, runtimeDirectory);
    await client.close();

    await expect(stat(runtimeDirectory)).rejects.toMatchObject({ code: "ENOENT" });
    expect(await readFile(join(protectedDirectory, "sentinel"), "utf8")).toBe("keep");
    expect(harness.processes[0]?.closedLifetime).toBe(1);
    await rm(original, { force: true, recursive: true });
  });

  it("an over-long socket path is bounded before launch", async () => {
    const longBase = join(testRoot, "x".repeat(120));
    await mkdir(longBase);
    const prefixes: string[] = [];
    const harness = launchHarness();
    const client = await spawnInternal(
      { binaryPath: process.execPath },
      {
        ...harness.internal,
        fileSystem: {
          mkdtemp: async (prefix) => {
            prefixes.push(prefix);
            return mkdtemp(prefixes.length === 1 ? prefix : join(scratchRoot, "mecatl-sdk-short-"));
          },
        },
        tempDirectory: longBase,
      },
    );
    expect(prefixes).toHaveLength(2);
    expect(prefixes[1]).toBe("/tmp/mecatl-sdk-");
    const socketPath = argumentValue(harness.requests[0]?.args ?? [], "--grpc-unix-socket");
    expect(Buffer.byteLength(socketPath)).toBeLessThan(104);
    await client.close();
  });

  it("win32 is a typed unsupported platform before any work", async () => {
    let work = 0;
    await expect(
      spawnInternal(
        {},
        {
          fileSystem: {
            mkdtemp: async () => {
              work += 1;
              return testRoot;
            },
          },
          launcher: () => {
            work += 1;
            throw new Error("launcher should not run");
          },
          platform: "win32",
        },
      ),
    ).rejects.toMatchObject({ code: "unsupported_platform", transport: "local" });
    expect(work).toBe(0);
  });

  it("the sdk-owned argv never loosens the daemon posture", async () => {
    const harness = launchHarness();
    const client = await spawnInternal({ binaryPath: process.execPath }, harness.internal);
    const args = harness.requests[0]?.args ?? [];
    expect(args).not.toContain("--posture");
    expect(args).not.toContain("--yolo");
    expect(args).not.toContain("--trust-project");
    await client.close();
  });
});
