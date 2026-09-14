import {
  type DiagnosticFieldValue,
  type DiagnosticRecord,
  type DiagnosticsSink,
  MecatlError,
} from "./errors.js";

const STDERR_CAPTURE_BYTES = 64 * 1024;
const STDERR_REPORT_BYTES = 4 * 1024;
const REDACTED_LINE = "[REDACTED]";

export const SDK_OWNED_FLAGS = [
  "--grpc-unix-socket",
  "--grpc-addr",
  "--http-addr",
  "--ready-file",
  "--lifetime-pipe-fd",
] as const;

export class BoundedByteTail {
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

export class ChildExitedBeforeReady extends Error {
  constructor(readonly status: { code: number | null; signal: string | null }) {
    super("mecated exited before readiness");
  }
}

export type StartupError = MecatlError & {
  cleanupFailed?: true;
  exitCode?: number | null;
  signal?: string | null;
  stderrTail?: string;
};

export function localError(
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

export function validateExtraArguments(
  args: readonly string[],
  ownedFlags: readonly string[],
): void {
  for (const argument of args) {
    // Go's flag package accepts both -flag and --flag, with or without =value.
    if (!argument.startsWith("-")) continue;
    const bare = argument.replace(/^--?/, "").split("=", 1)[0];
    const flag = ownedFlags.find((candidate) => candidate.slice(2) === bare);
    if (flag !== undefined) {
      throw localError("spawn_failed", `Extra argument ${flag} collides with an SDK-owned flag`);
    }
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

export function reportableStderr(bytes: Uint8Array, decode: (bytes: Uint8Array) => string): string {
  const start = Math.max(0, bytes.length - STDERR_REPORT_BYTES);
  let report = bytes.subarray(start);
  if (start > 0 && bytes[start - 1] !== 0x0a) {
    const firstNewline = report.indexOf(0x0a);
    report = firstNewline === -1 ? new Uint8Array() : report.subarray(firstNewline + 1);
  }
  // Runtime adapters retain their original decoder's BOM/invalid UTF-8 behavior.
  return redactStderr(decode(report));
}

export function createStartupError(reason: unknown, tail: string): StartupError {
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

export function emitStartupDiagnostic(
  sink: DiagnosticsSink | undefined,
  error: StartupError,
): void {
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
