import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import { type SpawnOptions, spawnInternal } from "../src/spawn.js";

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

let testRoot: string;

beforeEach(async () => {
  await mkdir(scratchRoot, { recursive: true });
  testRoot = await mkdtemp(join(scratchRoot, "sdk-spawn-hosting-test-"));
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
    deployment: "test",
    ...overrides,
  };
}

function launchHarness(overrides: Record<string, unknown> = {}): {
  internal: {
    createTransport(): Transport;
    launcher(request: CapturedLaunch): {
      closeLifetime(): void;
      exit: Promise<{ code: number | null; signal: NodeJS.Signals | null }>;
      isRunning(): boolean;
      kill(signal: "SIGKILL" | "SIGTERM"): void;
    };
    scheduler: { sleep(): Promise<void> };
    tempDirectory: string;
  };
  processes: FakeProcessRecord[];
  requests: CapturedLaunch[];
} {
  const requests: CapturedLaunch[] = [];
  const processes: FakeProcessRecord[] = [];
  return {
    internal: {
      createTransport: routerTransport,
      launcher: (request) => {
        requests.push(request);
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
        const readyFile = argumentValue(request.args, "--ready-file");
        const socketPath = argumentValue(request.args, "--grpc-unix-socket");
        setImmediate(() => {
          void writeFile(readyFile, JSON.stringify(readyDocument(socketPath, overrides)));
        });
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
    processes,
    requests,
  };
}

async function spawnWithHarness(
  options: SpawnOptions = {},
  readyOverrides: Record<string, unknown> = {},
) {
  const harness = launchHarness(readyOverrides);
  const client = await spawnInternal(
    { binaryPath: process.execPath, ...options },
    harness.internal,
  );
  const request = harness.requests[0];
  if (request === undefined) throw new Error("missing launch request");
  return { client, harness, request };
}

async function waitForFileText(path: string, fragment: string): Promise<string> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    try {
      const text = await readFile(path, "utf8");
      if (text.includes(fragment)) return text;
    } catch {
      // The child has not published the marker yet.
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 10));
  }
  throw new Error(`timed out waiting for ${fragment} in ${path}`);
}

async function startLifetimeProbe() {
  const executable = join(testRoot, "fake-mecated.mjs");
  const marker = join(testRoot, "lifetime-marker.txt");
  await writeFile(
    executable,
    `#!${process.execPath}
import { appendFileSync, fstatSync, writeFileSync } from "node:fs";
import { Socket } from "node:net";

const value = (flag) => process.argv[process.argv.indexOf(flag) + 1];
const fd = Number(value("--lifetime-pipe-fd"));
const marker = process.env.MECATL_SDK_LIFETIME_MARKER;
const peer = new Socket({ fd, readable: true, writable: false });
let received = 0;
peer.on("data", (chunk) => { received += chunk.length; });
peer.on("end", () => {
  appendFileSync(marker, JSON.stringify({ event: "eof", received }) + "\\n");
  process.exit(received === 0 ? 0 : 71);
});
peer.resume();
process.on("SIGTERM", () => {
  appendFileSync(marker, JSON.stringify({ event: "sigterm" }) + "\\n");
});
appendFileSync(marker, JSON.stringify({
  event: "ready",
  fd,
  isSocket: fstatSync(fd).isSocket(),
  readable: peer.readable,
  writable: peer.writable,
  argv: process.argv.slice(2),
}) + "\\n");
writeFileSync(value("--ready-file"), JSON.stringify({
  schema: "mecated-ready/1",
  pid: process.pid,
  transport: "unix",
  grpc_address: value("--grpc-unix-socket"),
  socket_path: value("--grpc-unix-socket"),
  api_major: 1,
  features: ["mcp_servers_on_create"],
}));
setInterval(() => {}, 1_000);
`,
    { mode: 0o700 },
  );
  await chmod(executable, 0o700);
  const client = await spawnInternal(
    {
      binaryPath: executable,
      env: { MECATL_SDK_LIFETIME_MARKER: marker },
    },
    {
      createTransport: routerTransport,
      tempDirectory: testRoot,
    },
  );
  const initial = await waitForFileText(marker, '"event":"ready"');
  return { client, initial, marker };
}

describe("spawn hosting", () => {
  it("the default daemon is unix transport with no http address", async () => {
    const { client, request } = await spawnWithHarness();
    expect(argumentValue(request.args, "--http-addr")).toBe("");
    const ready = JSON.parse(
      await readFile(argumentValue(request.args, "--ready-file"), "utf8"),
    ) as Record<string, unknown>;
    expect(ready).not.toHaveProperty("http_address");
    expect(client.daemon).toMatchObject({
      transport: "unix",
      socketPath: expect.any(String),
    });
    await client.close();
  });

  it("tool capability is read from the ready-file features", async () => {
    const withFeature = await spawnWithHarness();
    expect(withFeature.client.daemon.features).toContain("mcp_servers_on_create");
    await withFeature.client.close();

    const withoutFeature = await spawnWithHarness({}, { features: [] });
    expect(argumentValue(withoutFeature.request.args, "--http-addr")).toBe("");
    expect(withoutFeature.client.daemon.features).not.toContain("mcp_servers_on_create");
    await withoutFeature.client.close();
  });

  it("enabling http costs the mcp_servers_on_create feature", async () => {
    const { client, request } = await spawnWithHarness(
      { http: true },
      { features: [], http_address: "127.0.0.1:43123" },
    );
    expect(argumentValue(request.args, "--http-addr")).toBe("127.0.0.1:0");
    const ready = JSON.parse(
      await readFile(argumentValue(request.args, "--ready-file"), "utf8"),
    ) as Record<string, unknown>;
    expect(ready.http_address).toBe("127.0.0.1:43123");
    expect(client.daemon.features).not.toContain("mcp_servers_on_create");
    await client.close();
  });

  it("the lifetime pipe is inherited at fd 3 and never written", async () => {
    const { client, initial, marker } = await startLifetimeProbe();
    try {
      const ready = JSON.parse(initial.trim().split("\n")[0] ?? "{}") as Record<string, unknown>;
      expect(ready).toMatchObject({
        event: "ready",
        fd: 3,
        isSocket: true,
        readable: true,
        writable: false,
      });
      expect(ready.argv).toEqual(expect.arrayContaining(["--lifetime-pipe-fd", "3"]));
    } finally {
      await client.close();
    }
    const terminal = await waitForFileText(marker, '"event":"eof"');
    expect(terminal).toContain('"received":0');
  });

  it("releasing the parent end delivers EOF to the child", async () => {
    const { client, marker } = await startLifetimeProbe();
    await client.close();
    const terminal = await waitForFileText(marker, '"event":"eof"');
    expect(terminal).toContain('{"event":"eof","received":0}');
  });

  it("opting out of the lifetime pipe still stops the daemon on close", async () => {
    const { client, harness, request } = await spawnWithHarness({ lifetimePipe: false });
    expect(request.args).not.toContain("--lifetime-pipe-fd");
    expect(request.stdio).toEqual(["ignore", "ignore", "pipe"]);
    await client.close();
    expect(harness.processes[0]?.killed).toEqual(["SIGTERM"]);
  });

  it("the exposed daemon facts are the non-secret allowlist", async () => {
    const secret = "credential-that-must-not-escape";
    const { client } = await spawnWithHarness(
      {},
      {
        auth_token: secret,
        deployment: secret,
        grpc_address: secret,
        headers: { Authorization: secret },
        http_address: "127.0.0.1:43123",
      },
    );
    expect(Object.keys(client.daemon).sort()).toEqual([
      "apiMajor",
      "features",
      "pid",
      "socketPath",
      "transport",
    ]);
    expect(JSON.stringify(client.daemon)).not.toContain(secret);
    expect(Object.isFrozen(client.daemon)).toBe(true);
    expect(Object.isFrozen(client.daemon.features)).toBe(true);
    await client.close();
  });

  it("environment is inherited and never enumerated outward", async () => {
    const secret = "parent-secret-value";
    const harness = launchHarness();
    const client = await spawnInternal(
      {
        binaryPath: process.execPath,
        env: { CALLER_ONLY: "caller", OVERRIDDEN: "caller" },
      },
      {
        ...harness.internal,
        env: { INHERITED: "parent", OVERRIDDEN: "parent", SECRET_TOKEN: secret },
      },
    );
    expect(harness.requests[0]?.env).toMatchObject({
      CALLER_ONLY: "caller",
      INHERITED: "parent",
      OVERRIDDEN: "caller",
      SECRET_TOKEN: secret,
    });
    expect(JSON.stringify(client.daemon)).not.toContain(secret);
    expect(JSON.stringify(client)).not.toContain(secret);
    await client.close();
  });
});
