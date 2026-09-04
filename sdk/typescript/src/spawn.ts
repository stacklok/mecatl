import { spawn as spawnChild } from "node:child_process";
import { constants } from "node:fs";
import { access, chmod, lstat, mkdtemp, readFile, rm, stat, unlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join, resolve } from "node:path";

import type { Transport } from "@connectrpc/connect";

import { connectTransport, disposeTransport } from "./client.js";
import {
  type ClientDiagnosticsOptions,
  type DiagnosticFieldValue,
  type DiagnosticRecord,
  type DiagnosticsSink,
  InvalidStateError,
  MecatlError,
} from "./errors.js";
import { createNodeTransport } from "./node-transport.js";
import { createRawClient } from "./raw.js";
import {
  DEFAULT_TOOL_SERVER_NAME,
  type NodeClient,
  type ToolHostBinding,
  ToolRegistry,
  withToolRegistration,
} from "./tool.js";

const READY_SCHEMA = "mecated-ready/1";
const READY_FILE_NAME = "ready.json";
const SOCKET_FILE_NAME = "mecated.sock";
const DARWIN_SUN_PATH_BYTES = 104;
const DEFAULT_READINESS_TIMEOUT_MS = 30_000;
const READY_POLL_INTERVAL_MS = 20;
const STOP_GRACE_MS = 3_000;
const STOP_KILL_WAIT_MS = 1_000;
const HTTP_LOOPBACK_ADDRESS = "127.0.0.1:0";
const STDERR_CAPTURE_BYTES = 64 * 1024;
const STDERR_REPORT_BYTES = 4 * 1024;
const REDACTED_LINE = "[REDACTED]";

const SDK_OWNED_FLAGS = [
  "--grpc-unix-socket",
  "--grpc-addr",
  "--http-addr",
  "--ready-file",
  "--lifetime-pipe-fd",
] as const;

/** Options for starting one SDK-owned local daemon. @public */
export interface SpawnOptions extends ClientDiagnosticsOptions {
  /** Additional daemon arguments. SDK-owned listener and lifecycle flags cannot be replaced. */
  args?: readonly string[];
  /** Explicit mecated executable. Resolution otherwise uses MECATED_BIN, then PATH. */
  binaryPath?: string;
  /** Environment overrides merged over the parent process environment. */
  env?: Readonly<NodeJS.ProcessEnv>;
  /** Also expose the daemon's HTTP/SSE listener on an ephemeral loopback port. */
  http?: boolean;
  /** Disable the inherited parent-liveness descriptor. Enabled by default. */
  lifetimePipe?: boolean;
  /** Deadline for publication of a complete supported ready document. */
  readinessTimeoutMs?: number;
  /** Stable MCP namespace for callback tools. Defaults to `sdk`. */
  toolServerName?: string;
}

/** Non-secret facts published by an SDK-owned daemon. @public */
export interface DaemonInfo {
  /** The ready document's wire API major. */
  readonly apiMajor: number;
  /** Deployment-scoped feature identifiers reported by the daemon. */
  readonly features: readonly string[];
  /** The spawned daemon's process identifier. */
  readonly pid: number;
  /** The private Unix-domain gRPC socket path. */
  readonly socketPath: string;
  /** Spawned clients always use the Unix-domain gRPC transport. */
  readonly transport: "unix";
}

/** A Client that owns one locally spawned daemon. @public */
export interface SpawnedClient extends NodeClient {
  /** The ready document's non-secret daemon facts. */
  readonly daemon: DaemonInfo;
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
  stdio: readonly ["ignore", "ignore", "pipe"] | readonly ["ignore", "ignore", "pipe", "pipe"];
}

interface LaunchedProcess {
  closeLifetime(): void;
  readonly exit: Promise<ProcessExit>;
  isRunning(): boolean;
  kill(signal: "SIGKILL" | "SIGTERM"): void;
  stderrTail?(): Uint8Array;
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

interface SpawnToolHostLifecycle {
  abort(reason: unknown): void;
  mcpServer?(): import("./client.js").SessionMcpServer;
  start(): Promise<void> | void;
  stop(): Promise<void>;
}

interface SpawnInternalOptions {
  client?: {
    onTeardownStep?: (step: string) => void;
    toolHost?: SpawnToolHostLifecycle;
  };
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
  api_major: number;
  features: string[];
  http_address?: string;
  pid: number;
  socket_path: string;
  transport: "unix";
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

class BoundedByteTail {
  #bytes = Buffer.alloc(0);

  append(chunk: Uint8Array): void {
    const suffix = Buffer.from(chunk).subarray(-STDERR_CAPTURE_BYTES);
    const joined = Buffer.concat([this.#bytes, suffix]);
    this.#bytes = joined.subarray(-STDERR_CAPTURE_BYTES);
  }

  value(): Uint8Array {
    return this.#bytes;
  }
}

class ChildExitedBeforeReady extends Error {
  constructor(readonly status: ProcessExit) {
    super("mecated exited before readiness");
  }
}

type StartupError = MecatlError & {
  cleanupFailed?: true;
  exitCode?: number | null;
  signal?: NodeJS.Signals | null;
  stderrTail?: string;
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
    stdio: [...request.stdio],
  });
  const stderr = new BoundedByteTail();
  child.stderr?.on("data", (chunk: Buffer) => stderr.append(chunk));

  const exit = new Promise<ProcessExit>((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code, signal) => resolve({ code, signal }));
  });
  const lifetime = child.stdio[3];

  return {
    closeLifetime: () => lifetime?.destroy(),
    exit,
    isRunning: () => child.exitCode === null && child.signalCode === null,
    kill: (signal) => {
      child.kill(signal);
    },
    stderrTail: () => stderr.value(),
  };
}

function reportableStderr(bytes: Uint8Array): string {
  const captured = Buffer.from(bytes);
  const start = Math.max(0, captured.length - STDERR_REPORT_BYTES);
  let report = captured.subarray(start);
  if (start > 0 && captured[start - 1] !== 0x0a) {
    const firstNewline = report.indexOf(0x0a);
    report = firstNewline === -1 ? Buffer.alloc(0) : report.subarray(firstNewline + 1);
  }
  return redactStderr(report.toString("utf8"));
}

function secretShapedLine(line: string): boolean {
  const value = line.endsWith("\r") ? line.slice(0, -1) : line;
  return (
    /^[\t ]*(?:export[\t ]+)?[A-Za-z_][A-Za-z0-9_]*[\t ]*=.*$/.test(value) ||
    /(?:^|[^A-Za-z0-9])(?:sk-|ghp_|xox[abps]-|eyJ)[A-Za-z0-9._-]*/.test(value)
  );
}

function redactStderr(stderr: string): string {
  return stderr
    .split("\n")
    .map((line) => {
      if (!secretShapedLine(line)) return line;
      return line.endsWith("\r") ? `${REDACTED_LINE}\r` : REDACTED_LINE;
    })
    .join("\n");
}

function startupError(reason: unknown, process: LaunchedProcess | undefined): StartupError {
  const exited = reason instanceof ChildExitedBeforeReady ? reason.status : undefined;
  const code = reason instanceof MecatlError ? reason.code : "spawn_failed";
  const base =
    reason instanceof ChildExitedBeforeReady
      ? localError(
          "spawn_failed",
          `mecated exited before readiness (code=${String(exited?.code)}, signal=${String(exited?.signal)})`,
        )
      : reason instanceof MecatlError && (code === "spawn_failed" || code === "readiness_timeout")
        ? reason
        : localError("spawn_failed", "mecated failed during startup", reason);
  const tail = reportableStderr(process?.stderrTail?.() ?? new Uint8Array());
  const error = (
    tail === ""
      ? base
      : new MecatlError(`${base.message}\nstderr tail:\n${tail}`, {
          cause: base,
          code: base.code,
          transport: "local",
        })
  ) as StartupError;
  if (exited !== undefined) {
    error.exitCode = exited.code;
    error.signal = exited.signal;
  }
  if (tail !== "") error.stderrTail = tail;
  return error;
}

function emitStartupDiagnostic(sink: DiagnosticsSink | undefined, error: StartupError): void {
  if (sink === undefined) return;
  const fields: Record<string, DiagnosticFieldValue> = {};
  if (error.cleanupFailed === true) fields.cleanupFailed = true;
  if (error.exitCode !== undefined) fields.exitCode = error.exitCode;
  if (error.signal !== undefined) fields.signal = error.signal;
  if (error.stderrTail !== undefined) fields.stderrTail = error.stderrTail;
  const record: DiagnosticRecord = Object.freeze({
    code: error.code,
    fields: Object.freeze(fields),
    level: "error",
    message: error.message,
  });
  try {
    sink(record);
  } catch {
    // Diagnostics observers never replace the startup failure they are observing.
  }
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
  if (document.transport !== "unix") {
    throw localError("spawn_failed", "The ready document does not describe a Unix transport");
  }
  if (!Number.isSafeInteger(document.pid) || Number(document.pid) <= 0) {
    throw localError("spawn_failed", "The ready document does not contain a valid daemon pid");
  }
  if (!Number.isSafeInteger(document.api_major) || Number(document.api_major) <= 0) {
    throw localError("spawn_failed", "The ready document does not contain a valid API major");
  }
  if (
    !Array.isArray(document.features) ||
    !document.features.every((feature) => typeof feature === "string")
  ) {
    throw localError("spawn_failed", "The ready document does not contain a valid feature list");
  }
  if (document.http_address !== undefined && typeof document.http_address !== "string") {
    throw localError("spawn_failed", "The ready document contains an invalid HTTP address");
  }
  return {
    schema: READY_SCHEMA,
    api_major: Number(document.api_major),
    features: [...document.features],
    ...(document.http_address === undefined ? {} : { http_address: document.http_address }),
    pid: Number(document.pid),
    socket_path: socketPath,
    transport: "unix",
  };
}

function withDaemonInfo(client: NodeClient, ready: ReadyDocument): SpawnedClient {
  const daemon: DaemonInfo = Object.freeze({
    apiMajor: ready.api_major,
    features: Object.freeze([...ready.features]),
    pid: ready.pid,
    socketPath: ready.socket_path,
    transport: ready.transport,
  });
  Object.defineProperty(client, "daemon", {
    configurable: false,
    enumerable: true,
    get: () => daemon,
  });
  return client as SpawnedClient;
}

function toolHostBinding(host: SpawnToolHostLifecycle | undefined): ToolHostBinding {
  return {
    abort: (reason) => host?.abort(reason),
    mcpServer: () => {
      const server = host?.mcpServer?.();
      if (server === undefined) {
        throw new InvalidStateError(
          "The callback tool host has not published its loopback address",
          {
            transport: "local",
          },
        );
      }
      return server;
    },
    start: () => host?.start(),
    stop: async () => {
      await host?.stop();
    },
  };
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
  const failures: unknown[] = [];
  let observedExit = false;
  const exited = process.exit.then(
    () => {
      observedExit = true;
      return true;
    },
    () => {
      observedExit = true;
      return true;
    },
  );

  const closeLifetime = () => {
    try {
      process.closeLifetime();
    } catch (error) {
      failures.push(error);
    }
  };

  if (!process.isRunning()) {
    closeLifetime();
    if (failures.length > 0) throw new AggregateError(failures, "daemon shutdown failed");
    return;
  }

  try {
    process.kill("SIGTERM");
  } catch (error) {
    failures.push(error);
  }
  closeLifetime();

  let stopped = observedExit;
  if (!stopped) {
    try {
      stopped = await Promise.race([exited, scheduler.sleep(STOP_GRACE_MS).then(() => false)]);
    } catch (error) {
      failures.push(error);
    }
  }
  if (!stopped && !observedExit && process.isRunning()) {
    try {
      process.kill("SIGKILL");
    } catch (error) {
      failures.push(error);
    }
  }
  if (!stopped) {
    try {
      stopped = await Promise.race([exited, scheduler.sleep(STOP_KILL_WAIT_MS).then(() => false)]);
    } catch (error) {
      failures.push(error);
    }
    if (!stopped) {
      failures.push(new Error("mecated did not exit after SIGKILL"));
    }
  }
  if (failures.length > 0) throw new AggregateError(failures, "daemon shutdown failed");
}

async function spawnAttempt(
  options: SpawnOptions = {},
  internal: SpawnInternalOptions = {},
): Promise<SpawnedClient> {
  const platform = internal.platform ?? process.platform;
  if (platform === "win32") {
    throw localError(
      "unsupported_platform",
      "Local mecated spawning is unsupported on win32; connect to a remote daemon instead",
    );
  }

  const toolRegistry = new ToolRegistry(
    options.toolServerName ?? DEFAULT_TOOL_SERVER_NAME,
    toolHostBinding(internal.client?.toolHost),
  );

  const args = options.args ?? [];
  validateExtraArguments(args);
  const fileSystem: SpawnFileSystem = { ...defaultFileSystem, ...internal.fileSystem };
  const env = { ...(internal.env ?? process.env), ...options.env };
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
  let transport: Transport | undefined;
  let stopPromise: Promise<void> | undefined;
  let removePromise: Promise<void> | undefined;
  const stopDaemon = () => {
    stopPromise ??= child === undefined ? Promise.resolve() : stopProcess(child, scheduler);
    return stopPromise;
  };
  const removeRuntime = () => {
    removePromise ??= removeRuntimeDirectory(runtime.directory, fileSystem);
    return removePromise;
  };
  const cleanup = async () => {
    try {
      await stopDaemon();
    } finally {
      await removeRuntime();
    }
  };

  try {
    const lifetimePipe = options.lifetimePipe ?? true;
    const daemonArgs = [
      "serve",
      "--grpc-unix-socket",
      runtime.socketPath,
      "--http-addr",
      options.http === true ? HTTP_LOOPBACK_ADDRESS : "",
      "--ready-file",
      runtime.readyFile,
      ...(lifetimePipe ? ["--lifetime-pipe-fd", "3"] : []),
      ...args,
    ];
    child = launcher({
      args: daemonArgs,
      cwd: internal.cwd ?? process.cwd(),
      env,
      executable,
      shell: false,
      stdio: lifetimePipe ? ["ignore", "ignore", "pipe", "pipe"] : ["ignore", "ignore", "pipe"],
    });

    const ready = await Promise.race([
      waitForReady(runtime.readyFile, timeoutMs, fileSystem, clock, scheduler),
      child.exit.then(
        (status) => {
          throw new ChildExitedBeforeReady(status);
        },
        (error: unknown) => {
          throw localError("spawn_failed", "mecated could not be launched", error);
        },
      ),
    ]);
    transport = (internal.createTransport ?? ((socketPath) => createNodeTransport({ socketPath })))(
      ready.socket_path,
    );
    await createRawClient({ transport, transportKind: "grpc" }).features({ timeoutMs });
    return withDaemonInfo(
      withToolRegistration(
        connectTransport({
          ...(options.diagnostics === undefined ? {} : { diagnostics: options.diagnostics }),
          internal: {
            daemon: {
              exit: child.exit,
              removeRuntime,
              stop: stopDaemon,
            },
            ...(internal.client?.onTeardownStep === undefined
              ? {}
              : { onTeardownStep: internal.client.onTeardownStep }),
            toolHost: toolRegistry,
          },
          owned: true,
          transport,
          transportKind: "grpc",
          visibility: false,
        }),
        toolRegistry,
      ),
      ready,
    );
  } catch (error) {
    const failure = startupError(error, child);
    let cleanupFailed = false;
    if (transport !== undefined) {
      try {
        await disposeTransport(transport);
      } catch {
        cleanupFailed = true;
        // The child and its credential-bearing socket still have to be removed.
      }
    }
    try {
      await cleanup();
    } catch {
      cleanupFailed = true;
    }
    if (cleanupFailed) failure.cleanupFailed = true;
    throw failure;
  }
}

/** Internal construction seam used by the unit suite; not exported from the package entry point. */
export async function spawnInternal(
  options: SpawnOptions = {},
  internal: SpawnInternalOptions = {},
): Promise<SpawnedClient> {
  try {
    return await spawnAttempt(options, internal);
  } catch (reason) {
    const error =
      reason instanceof MecatlError ? (reason as StartupError) : startupError(reason, undefined);
    emitStartupDiagnostic(options.diagnostics, error);
    throw error;
  }
}

/** Starts one local mecated daemon and resolves after its ready-file barrier. @public */
export function spawn(options: SpawnOptions = {}): Promise<SpawnedClient> {
  return spawnInternal(options);
}
