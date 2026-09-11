import type { Client } from "./client.js";
import { connectTransport, disposeTransport } from "./client.js";
import {
  type ClientDiagnosticsOptions,
  type DiagnosticFieldValue,
  type DiagnosticRecord,
  type DiagnosticsSink,
  MecatlError,
} from "./errors.js";
import { createHttpTransport } from "./http.js";
import { createRawClient } from "./raw.js";

const READY_SCHEMA = "mecated-ready/1";
const READY_FILE_NAME = "ready.json";
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
  "--lifetime-stdin",
] as const;

/** Options for starting one Deno-owned local daemon. @public */
export interface SpawnOptions extends ClientDiagnosticsOptions {
  /** Additional daemon arguments. SDK-owned listener and lifecycle flags cannot be replaced. */
  args?: readonly string[];
  /** Explicit mecated executable. Defaults to resolving `mecated` through PATH. */
  binaryPath?: string;
  /** Environment overrides inherited by the daemon. */
  env?: Readonly<Record<string, string>>;
  /** Deadline for publication of a complete supported ready document. */
  readinessTimeoutMs?: number;
  /** Parent directory for the private ready-file directory. Defaults to Deno's temporary directory. */
  tempDirectory?: string;
}

/** Non-secret facts published by a Deno-owned daemon. @public */
export interface DaemonInfo {
  /** The ready document's wire API major. */
  readonly apiMajor: number;
  /** Deployment-scoped feature identifiers reported by the daemon. */
  readonly features: readonly string[];
  /** Loopback HTTP/SSE address used by this client. */
  readonly httpAddress: string;
  /** The spawned daemon's process identifier. */
  readonly pid: number;
  /** Deno-spawned clients use the HTTP/SSE transport. */
  readonly transport: "http";
}

/** A Client that owns one Deno.Command-launched local daemon. @public */
export interface SpawnedClient extends Client {
  /** The ready document's non-secret daemon facts. */
  readonly daemon: DaemonInfo;
}

interface NativeCommandOptions {
  args: string[];
  env?: Record<string, string>;
  stderr: "piped";
  stdin: "piped";
  stdout: "null";
}

interface NativeCommandStatus {
  code: number;
  signal: string | null;
  success: boolean;
}

interface NativeChildProcess {
  readonly pid: number;
  readonly status: Promise<NativeCommandStatus>;
  readonly stderr: ReadableStream<Uint8Array>;
  readonly stdin: WritableStream<Uint8Array>;
  kill(signal: "SIGKILL" | "SIGTERM"): void;
}

interface NativeCommand {
  spawn(): NativeChildProcess;
}

interface NativeCommandConstructor {
  new (command: string, options: NativeCommandOptions): NativeCommand;
}

interface DenoRuntime {
  readonly Command: NativeCommandConstructor;
  makeTempDir(options: { dir?: string; prefix: string }): Promise<string>;
  readTextFile(path: string): Promise<string>;
  realPath(path: string): Promise<string>;
  remove(path: string, options: { recursive: true }): Promise<void>;
}

interface ReadyDocument {
  readonly apiMajor: number;
  readonly features: readonly string[];
  readonly httpAddress: string;
  readonly pid: number;
}

interface LaunchedProcess {
  readonly captureDone: Promise<void>;
  readonly exit: Promise<NativeCommandStatus>;
  readonly pid: number;
  readonly stderr: BoundedByteTail;
  closeLifetime(): Promise<void>;
  isRunning(): boolean;
  kill(signal: "SIGKILL" | "SIGTERM"): void;
}

type StartupError = MecatlError & {
  cleanupFailed?: true;
  exitCode?: number | null;
  signal?: string | null;
  stderrTail?: string;
};

class ChildExitedBeforeReady extends Error {
  constructor(readonly status: NativeCommandStatus) {
    super("mecated exited before readiness");
  }
}

class ReadyPollAborted extends Error {}

class BoundedByteTail {
  #bytes = new Uint8Array();

  append(chunk: Uint8Array): void {
    const suffix = chunk.subarray(Math.max(0, chunk.length - STDERR_CAPTURE_BYTES));
    const retained = Math.min(this.#bytes.length, STDERR_CAPTURE_BYTES - suffix.length);
    const joined = new Uint8Array(retained + suffix.length);
    joined.set(this.#bytes.subarray(this.#bytes.length - retained));
    joined.set(suffix, retained);
    this.#bytes = joined;
  }

  value(): Uint8Array {
    return this.#bytes;
  }
}

function denoRuntime(): DenoRuntime {
  const runtime = (globalThis as typeof globalThis & { Deno?: DenoRuntime }).Deno;
  if (runtime === undefined || typeof runtime.Command !== "function") {
    throw localError("spawn_failed", "The Deno runtime and Deno.Command are required");
  }
  return runtime;
}

function localError(
  code: "readiness_timeout" | "spawn_failed",
  message: string,
  cause?: unknown,
): MecatlError {
  return new MecatlError(message, {
    ...(cause === undefined ? {} : { cause }),
    code,
    transport: "local",
  });
}

function errorNamed(error: unknown, name: string): boolean {
  return error instanceof Error && error.name === name;
}

function pollAborted(signal: AbortSignal | undefined): boolean {
  return signal?.aborted === true;
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (pollAborted(signal)) {
      reject(new ReadyPollAborted());
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", abort);
      resolve();
    }, ms);
    const abort = () => {
      clearTimeout(timer);
      reject(new ReadyPollAborted());
    };
    signal?.addEventListener("abort", abort, { once: true });
  });
}

function rejectsOwnedFlag(argument: string): string | undefined {
  if (!argument.startsWith("-")) return undefined;
  const bare = argument.replace(/^--?/, "").split("=", 1)[0];
  return SDK_OWNED_FLAGS.find((flag) => flag.slice(2) === bare);
}

function validateExtraArguments(args: readonly string[]): void {
  for (const argument of args) {
    const flag = rejectsOwnedFlag(argument);
    if (flag !== undefined) {
      throw localError("spawn_failed", `Extra argument ${flag} collides with an SDK-owned flag`);
    }
  }
}

async function readReadyDocument(
  runtime: DenoRuntime,
  path: string,
): Promise<ReadyDocument | undefined> {
  let source: string;
  try {
    source = await runtime.readTextFile(path);
  } catch (error) {
    if (errorNamed(error, "NotFound")) return undefined;
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
  if (
    typeof document.http_address !== "string" ||
    !/^127\.0\.0\.1:[1-9][0-9]*$/.test(document.http_address)
  ) {
    throw localError(
      "spawn_failed",
      "The ready document does not contain the SDK-owned loopback HTTP address",
    );
  }
  return {
    apiMajor: Number(document.api_major),
    features: [...document.features],
    httpAddress: document.http_address,
    pid: Number(document.pid),
  };
}

async function waitForReady(
  runtime: DenoRuntime,
  path: string,
  timeoutMs: number,
  signal: AbortSignal,
): Promise<ReadyDocument> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (pollAborted(signal)) throw new ReadyPollAborted();
    const document = await readReadyDocument(runtime, path);
    if (document !== undefined) return document;
    await sleep(READY_POLL_INTERVAL_MS, signal);
  }
  throw localError(
    "readiness_timeout",
    `mecated did not publish a supported ready file within ${timeoutMs}ms`,
  );
}

async function captureStderr(
  stream: ReadableStream<Uint8Array>,
  tail: BoundedByteTail,
): Promise<void> {
  const reader = stream.getReader();
  try {
    for (;;) {
      const result = await reader.read();
      if (result.done) return;
      tail.append(result.value);
    }
  } finally {
    reader.releaseLock();
  }
}

function launch(
  runtime: DenoRuntime,
  executable: string,
  args: readonly string[],
  env: Readonly<Record<string, string>> | undefined,
): LaunchedProcess {
  const child = new runtime.Command(executable, {
    args: [...args],
    ...(env === undefined ? {} : { env: { ...env } }),
    stderr: "piped",
    stdin: "piped",
    stdout: "null",
  }).spawn();
  let exited = false;
  const exit = child.status.then((status) => {
    exited = true;
    return status;
  });
  const stderr = new BoundedByteTail();
  const lifetime = child.stdin.getWriter();
  let closeLifetimePromise: Promise<void> | undefined;
  return {
    captureDone: captureStderr(child.stderr, stderr),
    closeLifetime: () => {
      closeLifetimePromise ??= lifetime.close().finally(() => lifetime.releaseLock());
      return closeLifetimePromise;
    },
    exit,
    isRunning: () => !exited,
    kill: (signal) => child.kill(signal),
    pid: child.pid,
    stderr,
  };
}

async function settlesWithin(exit: Promise<NativeCommandStatus>, ms: number): Promise<boolean> {
  const controller = new AbortController();
  try {
    return await Promise.race([
      exit.then(() => true),
      sleep(ms, controller.signal).then(
        () => false,
        () => false,
      ),
    ]);
  } finally {
    controller.abort();
  }
}

async function stopProcess(process: LaunchedProcess): Promise<void> {
  const failures: unknown[] = [];
  try {
    await process.closeLifetime();
  } catch (error) {
    if (process.isRunning()) failures.push(error);
  }
  let stopped = await settlesWithin(process.exit, STOP_GRACE_MS);
  if (!stopped && process.isRunning()) {
    try {
      process.kill("SIGTERM");
    } catch (error) {
      if (!errorNamed(error, "NotFound") && process.isRunning()) failures.push(error);
    }
    stopped = await settlesWithin(process.exit, STOP_GRACE_MS);
  }
  if (!stopped && process.isRunning()) {
    try {
      process.kill("SIGKILL");
    } catch (error) {
      if (!errorNamed(error, "NotFound") && process.isRunning()) failures.push(error);
    }
    stopped = await settlesWithin(process.exit, STOP_KILL_WAIT_MS);
  }
  if (!stopped) failures.push(new Error("mecated did not exit after SIGKILL"));
  try {
    await process.captureDone;
  } catch (error) {
    failures.push(error);
  }
  if (failures.length > 0) throw new AggregateError(failures, "daemon shutdown failed");
}

async function removeRuntime(runtime: DenoRuntime, directory: string): Promise<void> {
  try {
    await runtime.remove(directory, { recursive: true });
  } catch (error) {
    if (!errorNamed(error, "NotFound")) throw error;
  }
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

function reportableStderr(bytes: Uint8Array): string {
  const start = Math.max(0, bytes.length - STDERR_REPORT_BYTES);
  let report = bytes.subarray(start);
  if (start > 0 && bytes[start - 1] !== 0x0a) {
    const firstNewline = report.indexOf(0x0a);
    report = firstNewline === -1 ? new Uint8Array() : report.subarray(firstNewline + 1);
  }
  return redactStderr(new TextDecoder().decode(report));
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
  const tail = reportableStderr(process?.stderr.value() ?? new Uint8Array());
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

function withDaemonInfo(client: Client, ready: ReadyDocument): SpawnedClient {
  const daemon: DaemonInfo = Object.freeze({
    apiMajor: ready.apiMajor,
    features: Object.freeze([...ready.features]),
    httpAddress: ready.httpAddress,
    pid: ready.pid,
    transport: "http",
  });
  Object.defineProperty(client, "daemon", {
    configurable: false,
    enumerable: true,
    get: () => daemon,
  });
  return client as SpawnedClient;
}

/**
 * Starts one local `mecated` daemon with `Deno.Command` and waits for its ready-file barrier.
 *
 * @param options - Executable, daemon arguments, environment, readiness, and diagnostics options.
 * @returns A client that owns the daemon process and its private runtime directory.
 * @public
 */
export async function spawn(options: SpawnOptions = {}): Promise<SpawnedClient> {
  let runtime: DenoRuntime;
  try {
    runtime = denoRuntime();
    validateExtraArguments(options.args ?? []);
  } catch (reason) {
    const error = startupError(reason, undefined);
    emitStartupDiagnostic(options.diagnostics, error);
    throw error;
  }

  const timeoutMs = options.readinessTimeoutMs ?? DEFAULT_READINESS_TIMEOUT_MS;
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    const error = startupError(
      localError("spawn_failed", "readinessTimeoutMs must be a positive finite number"),
      undefined,
    );
    emitStartupDiagnostic(options.diagnostics, error);
    throw error;
  }

  let directory: string | undefined;
  let process: LaunchedProcess | undefined;
  let transport: ReturnType<typeof createHttpTransport> | undefined;
  const readyPoll = new AbortController();
  let stopPromise: Promise<void> | undefined;
  let removePromise: Promise<void> | undefined;
  const stopDaemon = () => {
    stopPromise ??= process === undefined ? Promise.resolve() : stopProcess(process);
    return stopPromise;
  };
  const removeRuntimeDirectory = () => {
    removePromise ??=
      directory === undefined ? Promise.resolve() : removeRuntime(runtime, directory);
    return removePromise;
  };

  try {
    const tempDirectory =
      options.tempDirectory === undefined
        ? undefined
        : await runtime.realPath(options.tempDirectory);
    directory = await runtime.makeTempDir({
      ...(tempDirectory === undefined ? {} : { dir: tempDirectory }),
      prefix: "mecatl-sdk-deno-",
    });
    const readyFile = `${directory}/${READY_FILE_NAME}`;
    process = launch(
      runtime,
      options.binaryPath ?? "mecated",
      [
        "serve",
        "--grpc-addr",
        HTTP_LOOPBACK_ADDRESS,
        "--http-addr",
        HTTP_LOOPBACK_ADDRESS,
        "--ready-file",
        readyFile,
        "--lifetime-stdin",
        ...(options.args ?? []),
      ],
      options.env,
    );
    let ready: ReadyDocument;
    try {
      ready = await Promise.race([
        waitForReady(runtime, readyFile, timeoutMs, readyPoll.signal),
        process.exit.then((status) => {
          throw new ChildExitedBeforeReady(status);
        }),
      ]);
    } finally {
      readyPoll.abort();
    }
    if (ready.pid !== process.pid) {
      throw localError("spawn_failed", "The ready document pid does not match the spawned daemon");
    }
    transport = createHttpTransport({ baseUrl: `http://${ready.httpAddress}` });
    await createRawClient({ transport, transportKind: "http" }).features({ timeoutMs });
    return withDaemonInfo(
      connectTransport({
        ...(options.diagnostics === undefined ? {} : { diagnostics: options.diagnostics }),
        internal: {
          daemon: {
            exit: process.exit,
            removeRuntime: removeRuntimeDirectory,
            stop: stopDaemon,
          },
        },
        owned: true,
        transport,
        transportKind: "http",
        visibility: false,
      }),
      ready,
    );
  } catch (reason) {
    readyPoll.abort();
    let cleanupFailed = false;
    if (transport !== undefined) {
      try {
        await disposeTransport(transport);
      } catch {
        cleanupFailed = true;
      }
    }
    try {
      await stopDaemon();
    } catch {
      cleanupFailed = true;
    }
    try {
      await removeRuntimeDirectory();
    } catch {
      cleanupFailed = true;
    }
    const error = startupError(reason, process);
    if (cleanupFailed) error.cleanupFailed = true;
    emitStartupDiagnostic(options.diagnostics, error);
    throw error;
  }
}
