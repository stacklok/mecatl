import { copyFile, mkdir, mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { createConnection } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

import { expect, it } from "vitest";

import { type MecatlError, query, spawn } from "../src/node.js";
import { fixture, repositoryRoot } from "./harness.js";
import {
  binaryPath,
  childExit,
  spawnProductFixture,
  startRuntimeHelper,
} from "./product-harness.js";

const DEFAULT_GRPC_PORT = 8080;
const DEFAULT_HTTP_PORT = 8081;

it("the spawned daemon opens no tcp port", async () => {
  const instance = await spawnProductFixture("query-retention.json");
  try {
    const ready = JSON.parse(
      await readFile(join(dirname(instance.client.daemon.socketPath), "ready.json"), "utf8"),
    ) as Record<string, unknown>;
    expect(instance.client.daemon.transport).toBe("unix");
    expect(ready).toMatchObject({
      schema: "mecated-ready/1",
      socket_path: instance.client.daemon.socketPath,
      transport: "unix",
    });
    expect(ready).not.toHaveProperty("http_address");
    await expectConnectionRefused(DEFAULT_GRPC_PORT);
    await expectConnectionRefused(DEFAULT_HTTP_PORT);
  } finally {
    await instance.close();
  }
});

it("close stops the daemon and removes its runtime directory", async () => {
  const instance = await spawnProductFixture("query-retention.json");
  const pid = instance.client.daemon.pid;
  const runtimeDirectory = dirname(instance.client.daemon.socketPath);
  expect(processAlive(pid)).toBe(true);

  await instance.client.close();

  expect(processAlive(pid)).toBe(false);
  await expect(readFile(join(runtimeDirectory, "ready.json"), "utf8")).rejects.toMatchObject({
    code: "ENOENT",
  });
  await instance.close();
});

it("a killed parent stops the daemon through the lifetime pipe", async () => {
  for (const runtime of ["node", "bun"] as const) {
    const helper = await startRuntimeHelper(runtime, "hold");
    try {
      expect(processAlive(helper.record.daemonPid), `${runtime} daemon did not start`).toBe(true);
      helper.child.kill("SIGKILL");
      await childExit(helper.child);
      await waitForProcessExit(helper.record.daemonPid);
      expect(processAlive(helper.record.daemonPid), `${runtime} daemon survived parent death`).toBe(
        false,
      );
    } finally {
      await helper.cleanup();
      if (processAlive(helper.record.daemonPid)) {
        process.kill(helper.record.daemonPid, "SIGTERM");
        await waitForProcessExit(helper.record.daemonPid).catch(() => undefined);
      }
      await rm(helper.record.runtimeDirectory, { force: true, recursive: true });
    }
  }
});

it("retainSession survives on the wire and the default does not", async () => {
  const instance = await spawnProductFixture("query-retention.json");
  try {
    const retained = await query("retain this session", {
      client: instance.client,
      retainSession: true,
    });
    for await (const _event of retained) {
      // Drain through query-owned cleanup.
    }
    await expect(instance.client.sessions.get(retained.sessionId)).resolves.toMatchObject({
      id: retained.sessionId,
    });

    const transient = await query("delete this session", { client: instance.client });
    for await (const _event of transient) {
      // Drain through query-owned cleanup.
    }
    await expect(instance.client.sessions.get(transient.sessionId)).rejects.toMatchObject({
      code: "session_not_found",
    });
  } finally {
    await instance.close();
  }
});

it("a real startup failure is typed with a redacted tail and no orphan", async () => {
  await expect(
    spawn({ binaryPath: join(repositoryRoot, ".scratch", "missing-mecated") }),
  ).rejects.toMatchObject({ code: "spawn_failed", transport: "local" });

  const scratch = join(repositoryRoot, ".scratch");
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "sdk-startup-e2e-"));
  const secret = "sk-e2e-startup-secret";
  const invalidScript = join(root, `OPENAI_API_KEY=${secret}.json`);
  await copyFile(fixture("startup-invalid.json"), invalidScript);
  const before = await sdkRuntimeDirectories();
  try {
    let failure: (MecatlError & { exitCode?: number | null; stderrTail?: string }) | undefined;
    try {
      await spawn({
        args: ["--mock-script", invalidScript, "--workspace", root],
        binaryPath: binaryPath(),
      });
    } catch (error) {
      failure = error as MecatlError & { exitCode?: number | null; stderrTail?: string };
    }

    expect(failure).toMatchObject({ code: "spawn_failed", transport: "local" });
    expect(typeof failure?.exitCode).toBe("number");
    expect(failure?.stderrTail).toContain("[REDACTED]");
    expect(failure?.message).not.toContain(secret);
    expect(await sdkRuntimeDirectories()).toEqual(before);
  } finally {
    await rm(root, { force: true, recursive: true });
  }
});

function processAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ESRCH") return false;
    throw error;
  }
}

async function waitForProcessExit(pid: number): Promise<void> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (!processAlive(pid)) return;
    await delay(20);
  }
  throw new Error(`process ${pid} remained alive after its parent exited`);
}

async function expectConnectionRefused(port: number): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const socket = createConnection({ host: "127.0.0.1", port });
    const timeout = setTimeout(() => {
      socket.destroy();
      reject(new Error(`TCP connect to 127.0.0.1:${port} did not settle`));
    }, 1_000);
    socket.once("connect", () => {
      clearTimeout(timeout);
      socket.destroy();
      reject(new Error(`TCP connect to 127.0.0.1:${port} unexpectedly succeeded`));
    });
    socket.once("error", (error: NodeJS.ErrnoException) => {
      clearTimeout(timeout);
      socket.destroy();
      if (error.code === "ECONNREFUSED") resolve();
      else reject(error);
    });
  });
}

async function sdkRuntimeDirectories(): Promise<string[]> {
  const entries = await readdir(tmpdir(), { withFileTypes: true });
  return entries
    .filter((entry) => entry.isDirectory() && entry.name.startsWith("mecatl-sdk-"))
    .map((entry) => entry.name)
    .sort();
}
