import { spawn as spawnChild } from "node:child_process";
import { constants } from "node:fs";
import { access, chmod, lstat, mkdtemp, readFile, rm, stat, unlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join, resolve } from "node:path";

import type { Transport } from "@connectrpc/connect";

import { type Client, connectTransport } from "./client.js";
import { MecatlError } from "./errors.js";
import { createNodeTransport } from "./node-transport.js";

const READY_SCHEMA = "mecated-ready/1";
const READY_FILE_NAME = "ready.json";
const SOCKET_FILE_NAME = "mecated.sock";
const DARWIN_SUN_PATH_BYTES = 104;
const DEFAULT_READINESS_TIMEOUT_MS = 30_000;
const READY_POLL_INTERVAL_MS = 20;
const STOP_GRACE_MS = 3_000;

const SDK_OWNED_FLAGS = [
  "--grpc-unix-socket",
  "--grpc-addr",
  "--http-addr",
  "--ready-file",
  "--lifetime-pipe-fd",
] as const;

/** Options for starting one SDK-owned local daemon. @public */
export interface SpawnOptions {
  /** Additional daemon arguments. SDK-owned listener and lifecycle flags cannot be replaced. */
  args?: readonly string[];
  /** Explicit mecated executable. Resolution otherwise uses MECATED_BIN, then PATH. */
  binaryPath?: string;
  /** Deadline for publication of a complete supported ready document. */
  readinessTimeoutMs?: number;
}

interface ProcessExit {
  code: number | null;
  signal: NodeJS.Signals | null;
}

interface LaunchRequest {
  args: readonly string[];
  cwd: string;
  env: NodeJS.ProcessEnv;
  executable: string;
  shell: false;
}

interface LaunchedProcess {
  closeLifetime(): void;
  readonly exit: Promise<ProcessExit>;
  isRunning(): boolean;
  kill(signal: "SIGKILL" | "SIGTERM"): void;
}

interface SpawnFileSystem {
  access(path: string, mode: number): Promise<void>;
  chmod(path: string, mode: number): Promise<void>;
  lstat(path: string): Promise<{ isSymbolicLink(): boolean }>;
  mkdtemp(prefix: string): Promise<string>;
  readFile(path: string, encoding: "utf8"): Promise<string>;
  rm(path: string, options: { force: true; recursive: true }): Promise<void>;
  stat(path: string): Promise<{ isFile(): boolean }>;
  unlink(path: string): Promise<void>;
}

interface SpawnScheduler {
  sleep(ms: number): Promise<void>;
}

interface SpawnClock {
  now(): number;
}

interface SpawnInternalOptions {
  clock?: Partial<SpawnClock>;
  createTransport?: (socketPath: string) => Transport;
  cwd?: string;
  env?: NodeJS.ProcessEnv;
  fileSystem?: Partial<SpawnFileSystem>;
  launcher?: (request: LaunchRequest) => LaunchedProcess;
  platform?: NodeJS.Platform;
  scheduler?: Partial<SpawnScheduler>;
  tempDirectory?: string;
}

interface ReadyDocument {
  schema: typeof READY_SCHEMA;
  socket_path: string;
}

interface RuntimePaths {
  directory: string;
  readyFile: string;
  socketPath: string;
}

const defaultFileSystem: SpawnFileSystem = {
  access,
  chmod,
  lstat,
  mkdtemp,
  readFile,
  rm,
  stat,
  unlink,
};

function localError(
  code: "readiness_timeout" | "spawn_failed" | "unsupported_platform",
  message: string,
  cause?: unknown,
): MecatlError {
  return new MecatlError(message, {
    ...(cause === undefined ? {} : { cause }),
    code,
    transport: "local",
  });
}

function errnoCode(error: unknown): string | undefined {
  return typeof error === "object" && error !== null && "code" in error
    ? String(error.code)
    : undefined;
}

function realLauncher(request: LaunchRequest): LaunchedProcess {
  const child = spawnChild(request.executable, [...request.args], {
    cwd: request.cwd,
    env: request.env,
    shell: request.shell,
    stdio: ["ignore", "ignore", "pipe", "pipe"],
  });
  child.stderr?.resume();

  const exit = new Promise<ProcessExit>((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) => resolve({ code, signal }));
  });
  const lifetime = child.stdio[3];

  return {
    closeLifetime: () => lifetime?.destroy(),
    exit,
    isRunning: () => child.exitCode === null && child.signalCode === null,
    kill: (signal) => {
      child.kill(signal);
    },
  };
}

function defaultSleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function rejectsOwnedFlag(argument: string): string | undefined {
  return SDK_OWNED_FLAGS.find((flag) => argument === flag || argument.startsWith(`${flag}=`));
}

function validateExtraArguments(args: readonly string[]): void {
  for (const argument of args) {
    const flag = rejectsOwnedFlag(argument);
    if (flag !== undefined) {
      throw localError("spawn_failed", `Extra argument ${flag} collides with an SDK-owned flag`);
    }
  }
}

async function isExecutableFile(path: string, fileSystem: SpawnFileSystem): Promise<boolean> {
  try {
    const info = await fileSystem.stat(path);
    if (!info.isFile()) return false;
    await fileSystem.access(path, constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

async function requireExecutable(
  candidate: string,
  source: "binaryPath" | "MECATED_BIN",
  fileSystem: SpawnFileSystem,
): Promise<string> {
  const absolute = resolve(candidate);
  if (await isExecutableFile(absolute, fileSystem)) return absolute;
  throw localError(
    "spawn_failed",
    `The ${source} candidate is not an existing executable file: ${candidate}`,
  );
}

async function resolveBinary(
  options: SpawnOptions,
  env: NodeJS.ProcessEnv,
  fileSystem: SpawnFileSystem,
): Promise<string> {
  if (options.binaryPath !== undefined) {
    return requireExecutable(options.binaryPath, "binaryPath", fileSystem);
  }

  const configured = env.MECATED_BIN;
  if (configured !== undefined) {
    return requireExecutable(configured, "MECATED_BIN", fileSystem);
  }

  const pathValue = env.PATH;
  if (pathValue !== undefined) {
    for (const entry of pathValue.split(delimiter)) {
      const candidate = resolve(join(entry === "" ? "." : entry, "mecated"));
      if (await isExecutableFile(candidate, fileSystem)) return candidate;
    }
  }
  throw localError("spawn_failed", "PATH does not contain an executable mecated file");
}

async function removeRuntimeDirectory(path: string, fileSystem: SpawnFileSystem): Promise<void> {
  try {
    const info = await fileSystem.lstat(path);
    if (info.isSymbolicLink()) {
      await fileSystem.unlink(path);
      return;
    }
    await fileSystem.rm(path, { force: true, recursive: true });
  } catch (error) {
    if (errnoCode(error) !== "ENOENT") throw error;
  }
}

async function createRuntimeAt(base: string, fileSystem: SpawnFileSystem): Promise<RuntimePaths> {
  const directory = await fileSystem.mkdtemp(join(base, "mecatl-sdk-"));
  try {
    await fileSystem.chmod(directory, 0o700);
  } catch (error) {
    await removeRuntimeDirectory(directory, fileSystem);
    throw error;
  }
  return {
    directory,
    readyFile: join(directory, READY_FILE_NAME),
    socketPath: join(directory, SOCKET_FILE_NAME),
  };
}

function socketPathFits(path: string): boolean {
  return Buffer.byteLength(path) < DARWIN_SUN_PATH_BYTES;
}

async function createRuntime(
  requestedBase: string,
  fileSystem: SpawnFileSystem,
): Promise<RuntimePaths> {
  const primary = await createRuntimeAt(requestedBase, fileSystem);
  if (socketPathFits(primary.socketPath)) return primary;
  await removeRuntimeDirectory(primary.directory, fileSystem);

  const fallback = await createRuntimeAt("/tmp", fileSystem);
  if (socketPathFits(fallback.socketPath)) return fallback;
  await removeRuntimeDirectory(fallback.directory, fileSystem);
  throw localError(
    "spawn_failed",
    `The Unix socket path reaches the ${DARWIN_SUN_PATH_BYTES}-byte Darwin sun_path bound`,
  );
}

async function readReadyDocument(
  path: string,
  fileSystem: SpawnFileSystem,
): Promise<ReadyDocument | undefined> {
  let source: string;
  try {
    source = await fileSystem.readFile(path, "utf8");
  } catch (error) {
    if (errnoCode(error) === "ENOENT") return undefined;
    throw localError("spawn_failed", `The ready file could not be read: ${path}`, error);
  }

  let value: unknown;
  try {
    value = JSON.parse(source) as unknown;
  } catch {
    return undefined;
  }
  const document =
    typeof value === "object" && value !== null ? (value as Record<string, unknown>) : undefined;
  if (document?.schema !== READY_SCHEMA) {
    throw localError(
      "spawn_failed",
      `Unsupported ready-file schema ${JSON.stringify(document?.schema)}; expected ${READY_SCHEMA}`,
    );
  }
  const socketPath = document.socket_path;
  if (typeof socketPath !== "string" || socketPath === "") {
    throw localError("spawn_failed", "The ready document does not contain a Unix socket path");
  }
  return { schema: READY_SCHEMA, socket_path: socketPath };
}

async function waitForReady(
  path: string,
  timeoutMs: number,
  fileSystem: SpawnFileSystem,
  clock: SpawnClock,
  scheduler: SpawnScheduler,
): Promise<ReadyDocument> {
  const deadline = clock.now() + timeoutMs;
  while (clock.now() < deadline) {
    const document = await readReadyDocument(path, fileSystem);
    if (document !== undefined) return document;
    await scheduler.sleep(READY_POLL_INTERVAL_MS);
  }
  throw localError(
    "readiness_timeout",
    `mecated did not publish a supported ready file within ${timeoutMs}ms`,
  );
}

async function stopProcess(process: LaunchedProcess, scheduler: SpawnScheduler): Promise<void> {
  process.closeLifetime();
  if (!process.isRunning()) return;
  process.kill("SIGTERM");
  const exited = process.exit.then(
    () => true,
    () => true,
  );
  if ((await Promise.race([exited, scheduler.sleep(STOP_GRACE_MS).then(() => false)])) === false) {
    process.kill("SIGKILL");
    await exited;
  }
}

/** Internal construction seam used by the unit suite; not exported from the package entry point. */
export async function spawnInternal(
  options: SpawnOptions = {},
  internal: SpawnInternalOptions = {},
): Promise<Client> {
  const platform = internal.platform ?? process.platform;
  if (platform === "win32") {
    throw localError(
      "unsupported_platform",
      "Local mecated spawning is unsupported on win32; connect to a remote daemon instead",
    );
  }

  const args = options.args ?? [];
  validateExtraArguments(args);
  const fileSystem: SpawnFileSystem = { ...defaultFileSystem, ...internal.fileSystem };
  const env = internal.env ?? process.env;
  const executable = await resolveBinary(options, env, fileSystem);
  const timeoutMs = options.readinessTimeoutMs ?? DEFAULT_READINESS_TIMEOUT_MS;
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    throw localError("spawn_failed", "readinessTimeoutMs must be a positive finite number");
  }

  let runtime: RuntimePaths;
  try {
    runtime = await createRuntime(internal.tempDirectory ?? tmpdir(), fileSystem);
  } catch (error) {
    if (error instanceof MecatlError) throw error;
    throw localError("spawn_failed", "The private runtime directory could not be created", error);
  }

  const scheduler: SpawnScheduler = {
    sleep: internal.scheduler?.sleep ?? defaultSleep,
  };
  const clock: SpawnClock = {
    now: internal.clock?.now ?? Date.now,
  };
  const launcher = internal.launcher ?? realLauncher;
  let child: LaunchedProcess | undefined;
  let cleaned = false;
  const cleanup = async () => {
    if (cleaned) return;
    cleaned = true;
    try {
      if (child !== undefined) await stopProcess(child, scheduler);
    } finally {
      await removeRuntimeDirectory(runtime.directory, fileSystem);
    }
  };

  try {
    const daemonArgs = [
      "serve",
      "--grpc-unix-socket",
      runtime.socketPath,
      "--http-addr",
      "",
      "--ready-file",
      runtime.readyFile,
      "--lifetime-pipe-fd",
      "3",
      ...args,
    ];
    child = launcher({
      args: daemonArgs,
      cwd: internal.cwd ?? process.cwd(),
      env,
      executable,
      shell: false,
    });

    const ready = await Promise.race([
      waitForReady(runtime.readyFile, timeoutMs, fileSystem, clock, scheduler),
      child.exit.then(
        ({ code, signal }) => {
          throw localError(
            "spawn_failed",
            `mecated exited before readiness (code=${String(code)}, signal=${String(signal)})`,
          );
        },
        (error: unknown) => {
          throw localError("spawn_failed", "mecated could not be launched", error);
        },
      ),
    ]);
    const transport = (
      internal.createTransport ?? ((socketPath) => createNodeTransport({ socketPath }))
    )(ready.socket_path);
    return connectTransport({
      afterClose: cleanup,
      owned: true,
      transport,
      transportKind: "grpc",
      visibility: false,
    });
  } catch (error) {
    await cleanup();
    if (error instanceof MecatlError) throw error;
    throw localError("spawn_failed", "mecated failed before readiness", error);
  }
}

/** Starts one local mecated daemon and resolves after its ready-file barrier. @public */
export function spawn(options: SpawnOptions = {}): Promise<Client> {
  return spawnInternal(options);
}
