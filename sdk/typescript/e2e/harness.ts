import { type ChildProcess, spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

import type { Event, EventOf, Run } from "../src/index.js";

export const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");
export const cannedMockReply =
  "Mock provider: no real model is configured. Set OPENAI_API_KEY for live use.";

export interface ReadyDocument {
  api_major: number;
  grpc_address: string;
  http_address?: string;
  schema: string;
  socket_path?: string;
  transport: "tcp" | "unix";
}

export interface Daemon {
  ready: ReadyDocument;
  runtimeDirectory: string;
  storeDirectory: string;
  workspace: string;
  restart(options?: DaemonRestartOptions): Promise<void>;
}

export interface DaemonOptions {
  durable?: boolean;
  http?: boolean;
  script?: string;
  uds?: boolean;
}

export interface DaemonRestartOptions {
  script?: string;
}

interface RunningDaemon {
  child: ChildProcess;
  ready: ReadyDocument;
}

export function fixture(name: string): string {
  return join(repositoryRoot, "sdk", "typescript", "e2e", "fixtures", name);
}

export async function withDaemon<T>(
  options: DaemonOptions,
  run: (daemon: Daemon) => Promise<T>,
): Promise<T> {
  const scratchDirectory = join(repositoryRoot, ".scratch");
  await mkdir(scratchDirectory, { recursive: true });
  const runtimeDirectory = await mkdtemp(join(scratchDirectory, "sdk-e2e-"));
  const readyFile = join(runtimeDirectory, "ready.json");
  const storeDirectory = join(runtimeDirectory, "store");
  const workspace = join(runtimeDirectory, "workspace");
  await Promise.all([mkdir(storeDirectory), mkdir(workspace)]);
  let effectiveOptions = { ...options };
  try {
    let running = await startDaemon(
      effectiveOptions,
      readyFile,
      storeDirectory,
      workspace,
      undefined,
    );
    const daemon: Daemon = {
      ready: running.ready,
      restart: async (restartOptions = {}) => {
        const previousReady = running.ready;
        await stop(running.child);
        await rm(readyFile, { force: true });
        if (previousReady.socket_path !== undefined) {
          await rm(previousReady.socket_path, { force: true });
        }
        if (effectiveOptions.durable === true) {
          // The local JSONL store auto-wires a session lease. A killed daemon cannot
          // write its release tombstone, so wait past the deliberately short e2e TTL
          // before the replacement process tries to adopt the same session.
          await delay(400);
        }
        effectiveOptions = { ...effectiveOptions, ...restartOptions };
        running = await startDaemon(
          effectiveOptions,
          readyFile,
          storeDirectory,
          workspace,
          previousReady,
        );
        daemon.ready = running.ready;
      },
      runtimeDirectory,
      storeDirectory,
      workspace,
    };

    try {
      return await run(daemon);
    } finally {
      await stop(running.child);
    }
  } finally {
    await rm(runtimeDirectory, { force: true, recursive: true });
  }
}

async function startDaemon(
  options: DaemonOptions,
  readyFile: string,
  storeDirectory: string,
  workspace: string,
  previousReady: ReadyDocument | undefined,
): Promise<RunningDaemon> {
  const args = [
    "serve",
    "--mock",
    "--workspace",
    workspace,
    "--ready-file",
    readyFile,
    "--metrics-addr",
    "",
    "--no-soul",
    "--no-user-model",
    "--no-scheduler",
    "--flight-recorder=false",
  ];
  if (options.durable === true) {
    args.push("--store-dir", storeDirectory, "--session-lease-ttl", "300ms");
  }
  if (options.uds === true) {
    args.push(
      "--grpc-unix-socket",
      previousReady?.socket_path ?? join(dirname(readyFile), "mecated.sock"),
    );
    args.push("--http-addr", "");
  } else {
    args.push("--grpc-addr", previousReady?.grpc_address ?? "127.0.0.1:0");
    args.push(
      "--http-addr",
      options.http === true ? (previousReady?.http_address ?? "127.0.0.1:0") : "",
    );
  }
  if (options.script !== undefined) args.push("--mock-script", options.script);

  const environment = { ...process.env };
  delete environment.ANTHROPIC_API_KEY;
  delete environment.OPENAI_API_KEY;
  delete environment.OPENROUTER_API_KEY;
  const child = spawn(join(repositoryRoot, "bin", "mecated"), args, {
    cwd: repositoryRoot,
    env: environment,
    stdio: ["ignore", "ignore", "pipe"],
  });
  let stderr = "";
  child.stderr?.setEncoding("utf8");
  child.stderr?.on("data", (chunk: string) => {
    stderr += chunk;
  });

  try {
    const ready = await waitForReady(child, readyFile, () => stderr);
    assertLoopbackOnly(ready);
    return { child, ready };
  } catch (error) {
    await stop(child);
    throw error;
  }
}

export async function collectRun(run: Run): Promise<{
  events: Event[];
  terminal: EventOf<"result">;
}> {
  const events: Event[] = [];
  let terminal: EventOf<"result"> | undefined;
  for await (const event of run) {
    events.push(event);
    if (event.kind === "result") terminal = event;
  }
  if (terminal === undefined) throw new Error(`run ${run.id} ended without a result event`);
  return { events, terminal };
}

async function waitForReady(
  child: ChildProcess,
  path: string,
  stderr: () => string,
): Promise<ReadyDocument> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`mecated exited before readiness (${child.exitCode}):\n${stderr()}`);
    }
    try {
      const ready = JSON.parse(await readFile(path, "utf8")) as ReadyDocument;
      if (ready.schema === "mecated-ready/1") return ready;
    } catch {
      // The ready file is atomically published; absence means keep polling.
    }
    await delay(20);
  }
  throw new Error(`timed out waiting for mecated readiness:\n${stderr()}`);
}

function assertLoopbackOnly(ready: ReadyDocument): void {
  if (ready.transport === "tcp" && !ready.grpc_address.startsWith("127.0.0.1:")) {
    throw new Error(`non-loopback gRPC listener: ${ready.grpc_address}`);
  }
  if (ready.http_address !== undefined && !ready.http_address.startsWith("127.0.0.1:")) {
    throw new Error(`non-loopback HTTP listener: ${ready.http_address}`);
  }
}

async function stop(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null) return;
  const exited = new Promise<void>((resolveExit) => child.once("exit", () => resolveExit()));
  child.kill("SIGTERM");
  if ((await Promise.race([exited.then(() => true), delay(3_000).then(() => false)])) === false) {
    child.kill("SIGKILL");
    await exited;
  }
}
