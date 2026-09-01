import { Code, ConnectError } from "@connectrpc/connect";

// BEGIN MECATL_ERROR_CODES
/** Stable server error codes, kept in parity with the Go registry. @public */
export const MECATL_ERROR_CODES = [
  "activity_gap",
  "child_not_found",
  "cleanup_backend",
  "cleanup_plan_stale",
  "cleanup_unsupported",
  "client_mcp_unreachable",
  "client_mcp_unsupported",
  "conflict",
  "cursor_expired",
  "cursor_malformed",
  "draining",
  "dream_apply_failed",
  "dream_capacity",
  "dream_conflict",
  "dream_deadline",
  "dream_generate_failed",
  "dream_in_progress",
  "dream_not_found",
  "dream_request_failed",
  "dream_terminal_conflict",
  "dream_unavailable",
  "failed_precondition",
  "failed_step_retry_ineligible",
  "fire_now_overlap",
  "internal",
  "invalid_argument",
  "learning_unavailable",
  "management_unauthorized",
  "migration_backend",
  "migration_conflict",
  "migration_unsupported",
  "no_active_run",
  "no_event_log",
  "no_mcp_provider",
  "no_schedule_store",
  "not_awaiting_plan",
  "not_found",
  "proposal_conflict",
  "request_too_large",
  "resource_exhausted",
  "schedule_disabled",
  "schedule_exhausted",
  "schedule_not_found",
  "schedule_not_leader",
  "schedule_unsupported",
  "scheduler_not_running",
  "session_delete_unsupported",
  "session_leased_elsewhere",
  "session_metadata_cursor_restart",
  "session_metadata_paging_unsupported",
  "session_not_found",
  "stale_run_control",
  "storage_health_backend",
  "team_not_found",
  "team_not_running",
  "team_running",
  "teams_disabled",
  "too_many_session_engines",
  "too_many_teams",
  "unimplemented",
  "watch_lagging",
  "watch_unsupported",
] as const;
// END MECATL_ERROR_CODES

/** @public */
export type ServerErrorCode = (typeof MECATL_ERROR_CODES)[number] | "unknown";
/** @public */
export type SDKErrorCode =
  | "authentication"
  | "incompatible_server"
  | "invalid_state"
  | "protocol"
  | "transport"
  | "unsupported_feature";
/** @public */
export type MecatlErrorCode = ServerErrorCode | SDKErrorCode;
/** @public */
export type TransportKind = "grpc" | "http";

/** @public */
export interface MecatlErrorOptions {
  cause?: unknown;
  code: MecatlErrorCode;
  requestId?: string | undefined;
  status?: number | undefined;
  transport: TransportKind;
}

/** Base class for every error authored by the SDK. @public */
export class MecatlError extends Error {
  readonly code: MecatlErrorCode;
  readonly requestId: string | undefined;
  readonly status: number | undefined;
  readonly transport: TransportKind;

  constructor(message: string, options: MecatlErrorOptions) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.name = new.target.name;
    this.code = options.code;
    this.requestId = options.requestId;
    this.status = options.status;
    this.transport = options.transport;
  }

  toJSON(): Record<string, unknown> {
    return {
      name: this.name,
      message: this.message,
      code: this.code,
      transport: this.transport,
      ...(this.status === undefined ? {} : { status: this.status }),
      ...(this.requestId === undefined ? {} : { requestId: this.requestId }),
    };
  }
}

/** @public */
export class TransportError extends MecatlError {
  constructor(message: string, options: Omit<MecatlErrorOptions, "code">) {
    super(message, { ...options, code: "transport" });
  }
}

/** @public */
export class AuthenticationError extends MecatlError {
  constructor(message: string, options: Omit<MecatlErrorOptions, "code">) {
    super(message, { ...options, code: "authentication" });
  }
}

/** @public */
export class ProtocolError extends MecatlError {
  constructor(message: string, options: Omit<MecatlErrorOptions, "code">) {
    super(message, { ...options, code: "protocol" });
  }
}

/** @public */
export class UnsupportedFeatureError extends MecatlError {
  readonly feature: string;

  constructor(feature: string, options: Omit<MecatlErrorOptions, "code">) {
    super(`The server does not advertise the ${feature} feature`, {
      ...options,
      code: "unsupported_feature",
    });
    this.feature = feature;
  }
}

/** @public */
export class InvalidStateError extends MecatlError {
  constructor(message: string, options: Omit<MecatlErrorOptions, "code">) {
    super(message, { ...options, code: "invalid_state" });
  }
}

/** A local run is already active on this Session handle. @public */
export class SessionBusyError extends InvalidStateError {}

/** @public */
export class IncompatibleServerError extends MecatlError {
  constructor(message: string, options: Omit<MecatlErrorOptions, "code">) {
    super(message, { ...options, code: "incompatible_server" });
  }
}

/** @public */
export class ServerError extends MecatlError {
  declare readonly code: ServerErrorCode;

  // biome-ignore lint/complexity/noUselessConstructor: this narrows code to the server vocabulary.
  constructor(
    message: string,
    options: Omit<MecatlErrorOptions, "code"> & { code: ServerErrorCode },
  ) {
    super(message, options);
  }
}

export interface ProblemDetails {
  code?: unknown;
  detail?: unknown;
  error?: unknown;
  request_id?: unknown;
  status?: unknown;
  title?: unknown;
  type?: unknown;
}

const knownServerCodes = new Set<string>(MECATL_ERROR_CODES);

function serverCode(value: unknown): ServerErrorCode {
  return typeof value === "string" && knownServerCodes.has(value)
    ? (value as ServerErrorCode)
    : "unknown";
}

export function errorFromProblem(
  problem: ProblemDetails,
  status: number,
  requestId: string | undefined,
): MecatlError {
  const code = serverCode(problem.code);
  const safeRequestId = typeof problem.request_id === "string" ? problem.request_id : requestId;
  const message =
    typeof problem.detail === "string"
      ? problem.detail
      : typeof problem.title === "string"
        ? problem.title
        : typeof problem.error === "string"
          ? problem.error
          : `mecatl request failed with HTTP status ${status}`;
  if (status === 401) {
    return new AuthenticationError("Authentication failed", {
      cause: problem,
      requestId: safeRequestId,
      status,
      transport: "http",
    });
  }
  return new ServerError(message, {
    cause: problem,
    code,
    requestId: safeRequestId,
    status,
    transport: "http",
  });
}

type ErrorInfo = { domain: string; metadata: Record<string, string>; reason: string };

function readVarint(bytes: Uint8Array, start: number): [number, number] | undefined {
  let value = 0;
  let shift = 0;
  for (let offset = start; offset < bytes.length && shift < 35; offset += 1, shift += 7) {
    const byte = bytes[offset];
    if (byte === undefined) return undefined;
    value |= (byte & 0x7f) << shift;
    if ((byte & 0x80) === 0) return [value, offset + 1];
  }
  return undefined;
}

function decodeFields(bytes: Uint8Array): Map<number, Uint8Array[]> {
  const fields = new Map<number, Uint8Array[]>();
  let offset = 0;
  while (offset < bytes.length) {
    const tag = readVarint(bytes, offset);
    if (tag === undefined) break;
    offset = tag[1];
    const field = tag[0] >>> 3;
    const wire = tag[0] & 7;
    if (wire !== 2) break;
    const length = readVarint(bytes, offset);
    if (length === undefined) break;
    offset = length[1];
    const end = offset + length[0];
    if (end > bytes.length) break;
    const values = fields.get(field) ?? [];
    values.push(bytes.subarray(offset, end));
    fields.set(field, values);
    offset = end;
  }
  return fields;
}

function decodeErrorInfo(bytes: Uint8Array): ErrorInfo | undefined {
  const fields = decodeFields(bytes);
  const decoder = new TextDecoder();
  const reasonBytes = fields.get(1)?.[0];
  const domainBytes = fields.get(2)?.[0];
  if (reasonBytes === undefined || domainBytes === undefined) return undefined;
  const metadata: Record<string, string> = {};
  for (const entryBytes of fields.get(3) ?? []) {
    const entry = decodeFields(entryBytes);
    const key = entry.get(1)?.[0];
    const value = entry.get(2)?.[0];
    if (key !== undefined && value !== undefined)
      metadata[decoder.decode(key)] = decoder.decode(value);
  }
  return { domain: decoder.decode(domainBytes), metadata, reason: decoder.decode(reasonBytes) };
}

function errorInfo(error: ConnectError): ErrorInfo | undefined {
  for (const detail of error.details as unknown[]) {
    if (typeof detail !== "object" || detail === null) continue;
    const record = detail as Record<string, unknown>;
    if (record.type === "google.rpc.ErrorInfo" && record.value instanceof Uint8Array) {
      const decoded = decodeErrorInfo(record.value);
      if (decoded !== undefined) return decoded;
    }
    const descriptor = record.desc;
    const value = record.value;
    if (
      typeof descriptor === "object" &&
      descriptor !== null &&
      (descriptor as Record<string, unknown>).typeName === "google.rpc.ErrorInfo" &&
      typeof value === "object" &&
      value !== null
    ) {
      const info = value as Record<string, unknown>;
      return {
        domain: typeof info.domain === "string" ? info.domain : "",
        metadata:
          typeof info.metadata === "object" && info.metadata !== null
            ? (info.metadata as Record<string, string>)
            : {},
        reason: typeof info.reason === "string" ? info.reason : "",
      };
    }
  }
  return undefined;
}

export function normalizeError(reason: unknown, transport: TransportKind): MecatlError {
  if (reason instanceof MecatlError) return reason;
  if (!(reason instanceof ConnectError)) {
    return new TransportError("The request could not reach the mecatl server", {
      cause: reason,
      transport,
    });
  }
  const info = errorInfo(reason);
  const requestId = info?.metadata.request_id ?? reason.metadata.get("x-request-id") ?? undefined;
  if (reason.code === Code.Unauthenticated) {
    return new AuthenticationError("Authentication failed", {
      cause: reason,
      requestId,
      status: reason.code,
      transport,
    });
  }
  if (info?.domain === "mecatl.stacklok.com") {
    return new ServerError(reason.rawMessage, {
      cause: reason,
      code: serverCode(info.reason),
      requestId,
      status: reason.code,
      transport,
    });
  }
  if (reason.code === Code.Unavailable || reason.code === Code.Unknown) {
    return new TransportError("The request could not reach the mecatl server", {
      cause: reason,
      requestId,
      status: reason.code,
      transport,
    });
  }
  return new ServerError(reason.rawMessage, {
    cause: reason,
    code: "unknown",
    requestId,
    status: reason.code,
    transport,
  });
}
