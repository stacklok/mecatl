import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  JsonValue,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import { create, fromJson, toJson } from "@bufbuild/protobuf";
import type { ContextValues, StreamResponse, Transport, UnaryResponse } from "@connectrpc/connect";
import type { CredentialOptions } from "./credentials.js";
import { credentialHeaders } from "./credentials.js";
import type { ProblemDetails } from "./errors.js";
import {
  errorFromProblem,
  ProtocolError,
  TransportError,
  UnsupportedFeatureError,
} from "./errors.js";
import type { ConverseRequest } from "./gen/mecatl/v1/harness_pb.js";
import { registerRawJson, registerTransport } from "./raw.js";
import {
  type HTTPMethod,
  type HTTPOnlyControlName,
  resolveHTTPOnlyControl,
} from "./rpc-catalog.js";

/** @public */
export interface HttpTransportOptions extends CredentialOptions {
  baseUrl: string;
  /** Passed to every request made by this transport. */
  credentials?: RequestCredentials;
  /** When supplied, global fetch is never consulted. */
  fetch?: typeof globalThis.fetch;
}

type JsonRecord = Record<string, JsonValue>;
type Route = { body: boolean; method: HTTPMethod; path: string };
type SSEFrame = { data: string; event: string };

function record(value: JsonValue): JsonRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
  return value as JsonRecord;
}

function stringField(value: JsonRecord, name: string): string {
  const field = value[name];
  return typeof field === "string" ? field : "";
}

function permissionMode(value: JsonValue | undefined): string {
  switch (value) {
    case 2:
      return "plan";
    case 3:
      return "accept_edits";
    default:
      return "default";
  }
}

function approvalVerdict(value: JsonValue | undefined): string {
  switch (value) {
    case 2:
      return "allow_once";
    case 3:
      return "allow_always";
    default:
      return "deny";
  }
}

function contentKind(value: number): string {
  if (value === 1) return "image";
  if (value === 2) return "audio";
  return "";
}

function encodeInput<I extends DescMessage>(
  method: DescMethodUnary<I, DescMessage>,
  input: MessageInitShape<I>,
): JsonRecord;
function encodeInput<I extends DescMessage>(
  method: DescMethodStreaming<I, DescMessage>,
  input: MessageInitShape<I>,
): JsonRecord;
function encodeInput(
  method: DescMethodUnary | DescMethodStreaming,
  input: MessageInitShape<DescMessage>,
): JsonRecord {
  const message = create(method.input, input);
  const json = record(
    toJson(method.input, message, { enumAsInteger: true, useProtoFieldName: true }),
  );
  if (method.name === "CreateSession" || method.name === "SetMode") {
    json.mode = permissionMode(json.mode);
  }
  return json;
}

function unaryRoute(name: string, input: JsonRecord): Route | undefined {
  const sessionId = encodeURIComponent(stringField(input, "session_id"));
  const sourceSessionId = encodeURIComponent(stringField(input, "source_session_id"));
  switch (name) {
    case "GetCompatibilityInfo":
      return { body: false, method: "GET", path: "/v1/compatibility" };
    case "GetServerInfo": {
      const provider = stringField(input, "provider_id");
      return {
        body: false,
        method: "GET",
        path: provider === "" ? "/v1/info" : `/v1/info?provider_id=${encodeURIComponent(provider)}`,
      };
    }
    case "CreateSession":
      return { body: true, method: "POST", path: "/v1/sessions" };
    case "GetSession":
      return { body: false, method: "GET", path: `/v1/sessions/${sessionId}` };
    case "SetMode":
      return { body: true, method: "POST", path: `/v1/sessions/${sessionId}/mode` };
    case "CloseSession":
      return { body: false, method: "DELETE", path: `/v1/sessions/${sessionId}` };
    case "RenameSession":
      return { body: true, method: "POST", path: `/v1/sessions/${sessionId}/rename` };
    case "DeleteSession":
      return { body: false, method: "POST", path: `/v1/sessions/${sessionId}/delete` };
    case "CompactSession":
      return { body: false, method: "POST", path: `/v1/sessions/${sessionId}/compact` };
    case "ForkSession":
      return { body: true, method: "POST", path: `/v1/sessions/${sourceSessionId}/fork` };
    case "ListSessions":
      return { body: false, method: "GET", path: "/v1/sessions" };
    case "ListModels":
      return { body: false, method: "GET", path: "/v1/models" };
    default:
      return undefined;
  }
}

function requestBody(name: string, input: JsonRecord): JsonRecord {
  const body = { ...input };
  delete body.session_id;
  delete body.source_session_id;
  if (name === "SetMode") body.mode = permissionMode(input.mode);
  return body;
}

function normalizeSession(value: JsonValue): JsonValue {
  const session = record(value);
  return { ...session, mode: permissionModeTextToNumber(session.mode) };
}

function permissionModeTextToNumber(value: JsonValue | undefined): number {
  switch (value) {
    case "default":
      return 1;
    case "plan":
      return 2;
    case "accept_edits":
    case "acceptedits":
      return 3;
    default:
      return 0;
  }
}

function normalizeUnaryResponse(name: string, raw: JsonValue): JsonValue {
  switch (name) {
    case "GetSession":
    case "SetMode":
    case "RenameSession":
      return { session: normalizeSession(raw) };
    default:
      return raw;
  }
}

function timeoutSignal(
  signal: AbortSignal | undefined,
  timeoutMs: number | undefined,
): AbortSignal | undefined {
  if (timeoutMs === undefined) return signal;
  const timeout = AbortSignal.timeout(timeoutMs);
  return signal === undefined ? timeout : AbortSignal.any([signal, timeout]);
}

function sessionControlRoute(name: HTTPOnlyControlName, sessionId: string): Route {
  const control = resolveHTTPOnlyControl(name, { session_id: sessionId });
  return {
    body: control.requestBody === "json",
    method: control.method,
    path: control.path,
  };
}

class HttpTransport implements Transport {
  readonly #baseUrl: string;
  readonly #credentials: RequestCredentials | undefined;
  readonly #credentialOptions: CredentialOptions;
  readonly #features = new Set<string>();
  readonly #fetch: typeof globalThis.fetch;

  constructor(options: HttpTransportOptions) {
    this.#baseUrl = options.baseUrl.replace(/\/$/, "");
    this.#credentials = options.credentials;
    this.#credentialOptions = {
      ...(options.headers === undefined ? {} : { headers: options.headers }),
      ...(options.credentialProvider === undefined
        ? {}
        : { credentialProvider: options.credentialProvider }),
    };
    this.#fetch = options.fetch ?? globalThis.fetch;
    if (this.#fetch === undefined) throw new TypeError("A fetch implementation is required");
  }

  async #request(
    route: Route,
    input: JsonRecord,
    signal: AbortSignal | undefined,
    requestHeaders?: HeadersInit,
  ): Promise<Response> {
    const headers = await credentialHeaders(this.#credentialOptions, "http", requestHeaders);
    if (route.body) headers.set("content-type", "application/json");
    const init: RequestInit = {
      headers,
      method: route.method,
      ...(route.body ? { body: JSON.stringify(input) } : {}),
      ...(this.#credentials === undefined ? {} : { credentials: this.#credentials }),
      ...(signal === undefined ? {} : { signal }),
    };
    try {
      return await this.#fetch(`${this.#baseUrl}${route.path}`, init);
    } catch (cause) {
      throw new TransportError("The HTTP request could not reach the mecatl server", {
        cause,
        transport: "http",
      });
    }
  }

  async #problem(response: Response): Promise<never> {
    let problem: ProblemDetails = {};
    try {
      problem = (await response.json()) as ProblemDetails;
    } catch {
      // The status remains useful even if a proxy replaced the problem body.
    }
    throw errorFromProblem(
      problem,
      response.status,
      response.headers.get("x-request-id") ?? undefined,
    );
  }

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: MessageInitShape<I>,
    _contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    const jsonInput = encodeInput(method, input);
    const route = unaryRoute(method.name, jsonInput);
    if (route === undefined) {
      throw new UnsupportedFeatureError(`http_${method.name}`, { transport: "http" });
    }
    const response = await this.#request(
      route,
      requestBody(method.name, jsonInput),
      timeoutSignal(signal, timeoutMs),
      header,
    );
    if (!response.ok) await this.#problem(response);
    let raw: JsonValue = {};
    if (response.status !== 204) {
      try {
        raw = (await response.json()) as JsonValue;
      } catch (cause) {
        throw new ProtocolError("The mecatl server returned invalid JSON", {
          cause,
          status: response.status,
          transport: "http",
        });
      }
    }
    const normalized = normalizeUnaryResponse(method.name, raw);
    let message: MessageShape<O>;
    try {
      message = fromJson(method.output, normalized, { ignoreUnknownFields: true });
    } catch (cause) {
      throw new ProtocolError("The mecatl server returned an invalid response", {
        cause,
        status: response.status,
        transport: "http",
      });
    }
    registerRawJson(message, raw);
    if (method.name === "GetCompatibilityInfo") {
      const features = record(normalized).features;
      if (Array.isArray(features)) {
        for (const feature of features) {
          if (typeof feature === "string") this.#features.add(feature);
        }
      }
    }
    return {
      header: response.headers,
      message,
      method,
      service: method.parent,
      stream: false,
      trailer: new Headers(),
    };
  }

  async #control(
    sessionId: string,
    frame: ConverseRequest,
    signal: AbortSignal | undefined,
    requestHeaders?: HeadersInit,
  ): Promise<void> {
    const kind = frame.kind;
    let route: Route;
    let body: JsonRecord;
    switch (kind.case) {
      case "resumeApproval":
        route = sessionControlRoute("approve", sessionId);
        body = {
          allow: kind.value.allow,
          ask_id: kind.value.askId,
          expected_run_id: kind.value.expectedRunId,
          verdict: approvalVerdict(kind.value.verdict),
        };
        break;
      case "cancel":
        await this.cancelRun(sessionId, kind.value.expectedRunId, signal, requestHeaders);
        return;
      case "cancelChild":
        route = sessionControlRoute("cancelChild", sessionId);
        body = { child_id: kind.value.childId };
        break;
      case "steer":
        if (!this.#features.has("http_steer")) {
          throw new UnsupportedFeatureError("http_steer", { transport: "http" });
        }
        route = sessionControlRoute("steer", sessionId);
        body = {
          expected_run_id: kind.value.expectedRunId,
          message_id: kind.value.messageId,
          parts: kind.value.parts.map((part) => ({
            ...(part.data.length === 0 ? {} : { data: bytesToBase64(part.data) }),
            kind: contentKind(part.kind),
            mime_type: part.mimeType,
            ...(part.url === "" ? {} : { url: part.url }),
          })) as unknown as JsonValue,
          text: kind.value.text,
        };
        break;
      case "steerCancel":
        if (!this.#features.has("http_steer")) {
          throw new UnsupportedFeatureError("http_steer", { transport: "http" });
        }
        route = sessionControlRoute("steerCancel", sessionId);
        body = {
          expected_run_id: kind.value.expectedRunId,
          message_id: kind.value.messageId,
        };
        break;
      default:
        throw new ProtocolError("Only control frames may follow the first Converse frame", {
          transport: "http",
        });
    }
    const response = await this.#request(route, body, signal, requestHeaders);
    if (!response.ok) await this.#problem(response);
  }

  async cancelRun(
    sessionId: string,
    runId: string,
    signal: AbortSignal | undefined,
    requestHeaders?: HeadersInit,
  ): Promise<void> {
    const response = await this.#request(
      sessionControlRoute("cancel", sessionId),
      { expected_run_id: runId },
      signal,
      requestHeaders,
    );
    if (!response.ok) await this.#problem(response);
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: AsyncIterable<MessageInitShape<I>>,
    _contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    const iterator = input[Symbol.asyncIterator]();
    const first = await iterator.next();
    const firstInput = first.done ? create(method.input) : create(method.input, first.value);
    const jsonInput = encodeInput(method, firstInput);
    const effectiveSignal = timeoutSignal(signal, timeoutMs);
    let route: Route;
    let body: JsonRecord = {};
    let wrapEvent = false;
    let startControls: (() => Promise<never>) | undefined;
    let controlFailure: Promise<never> = new Promise(() => undefined);

    if (method.name === "StreamSessionEvents") {
      route = {
        body: false,
        method: "GET",
        path: `/v1/sessions/${encodeURIComponent(stringField(jsonInput, "session_id"))}/events`,
      };
    } else if (method.name === "WatchSessionEvents") {
      const query = new URLSearchParams();
      const cursor = stringField(jsonInput, "cursor");
      const runId = stringField(jsonInput, "run_id");
      if (cursor !== "") query.set("cursor", cursor);
      if (runId !== "") query.set("run_id", runId);
      const suffix = query.toString();
      route = {
        body: false,
        method: "GET",
        path: `/v1/sessions/${encodeURIComponent(stringField(jsonInput, "session_id"))}/watch${suffix === "" ? "" : `?${suffix}`}`,
      };
    } else if (method.name === "Converse") {
      const frame = firstInput as unknown as ConverseRequest;
      const start = frame.kind;
      if (start.case !== "prompt" && start.case !== "retry") {
        throw new ProtocolError("The first Converse frame must be prompt or retry", {
          transport: "http",
        });
      }
      const sessionId = start.value.sessionId;
      wrapEvent = true;
      if (start.case === "prompt") {
        route = sessionControlRoute("prompt", sessionId);
        body = {
          parts: start.value.parts.map((part) => ({
            data: part.data.length === 0 ? undefined : bytesToBase64(part.data),
            kind: contentKind(part.kind),
            mime_type: part.mimeType,
            url: part.url,
          })) as unknown as JsonValue,
          text: start.value.text,
        };
      } else {
        route = sessionControlRoute("retry", sessionId);
      }
      startControls = async () => {
        for (;;) {
          const next = await iterator.next();
          if (next.done) return await new Promise<never>(() => undefined);
          await this.#control(
            sessionId,
            create(method.input, next.value) as unknown as ConverseRequest,
            effectiveSignal,
            header,
          );
        }
      };
    } else {
      throw new UnsupportedFeatureError(`http_${method.name}`, { transport: "http" });
    }

    const response = await this.#request(route, body, effectiveSignal, header);
    if (!response.ok) await this.#problem(response);
    const contentType = response.headers.get("content-type") ?? "";
    if (!contentType.toLowerCase().startsWith("text/event-stream")) {
      throw new ProtocolError("The mecatl streaming response is not an SSE stream", {
        status: response.status,
        transport: "http",
      });
    }
    if (startControls !== undefined) controlFailure = startControls();
    const frames = parseSSE(response, effectiveSignal);
    const output = method.output;
    const messages = (async function* (): AsyncIterable<MessageShape<O>> {
      const frameIterator = frames[Symbol.asyncIterator]();
      for (;;) {
        const next = await Promise.race([frameIterator.next(), controlFailure]);
        if (next.done) break;
        let raw: JsonValue;
        try {
          raw = JSON.parse(next.value.data) as JsonValue;
        } catch (cause) {
          throw new ProtocolError("The mecatl SSE stream contained invalid JSON", {
            cause,
            status: response.status,
            transport: "http",
          });
        }
        if (next.value.event === "error") {
          throw errorFromProblem(
            raw as ProblemDetails,
            response.status,
            response.headers.get("x-request-id") ?? undefined,
          );
        }
        const normalized = wrapEvent ? { event: raw } : raw;
        let message: MessageShape<O>;
        try {
          message = fromJson(output, normalized, { ignoreUnknownFields: true });
        } catch (cause) {
          throw new ProtocolError("The mecatl SSE stream contained an invalid event", {
            cause,
            status: response.status,
            transport: "http",
          });
        }
        registerRawJson(message, raw);
        if (wrapEvent) {
          const event = (message as { readonly event?: object | undefined }).event;
          if (event !== undefined) registerRawJson(event, raw);
        } else if (method.name === "WatchSessionEvents") {
          const event = (message as { readonly event?: object | undefined }).event;
          const rawEvent = record(raw).event;
          if (event !== undefined && rawEvent !== undefined) registerRawJson(event, rawEvent);
        }
        yield message;
      }
    })();
    return {
      header: response.headers,
      message: messages,
      method,
      service: method.parent,
      stream: true,
      trailer: new Headers(),
    };
  }
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

async function* parseSSE(
  response: Response,
  signal: AbortSignal | undefined,
): AsyncIterable<SSEFrame> {
  if (response.body === null) {
    throw new ProtocolError("The mecatl SSE response has no body", {
      status: response.status,
      transport: "http",
    });
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let event = "message";
  let data: string[] = [];
  const abort = () => {
    void reader.cancel(signal?.reason).catch(() => undefined);
  };
  signal?.addEventListener("abort", abort, { once: true });
  try {
    for (;;) {
      const result = await reader.read();
      buffer += decoder.decode(result.value, { stream: !result.done });
      let newline = buffer.indexOf("\n");
      while (newline >= 0) {
        let line = buffer.slice(0, newline);
        buffer = buffer.slice(newline + 1);
        if (line.endsWith("\r")) line = line.slice(0, -1);
        if (line === "") {
          if (data.length > 0) yield { data: data.join("\n"), event };
          data = [];
          event = "message";
        } else if (!line.startsWith(":")) {
          const separator = line.indexOf(":");
          const field = separator < 0 ? line : line.slice(0, separator);
          let value = separator < 0 ? "" : line.slice(separator + 1);
          if (value.startsWith(" ")) value = value.slice(1);
          if (field === "data") data.push(value);
          if (field === "event") event = value;
        }
        newline = buffer.indexOf("\n");
      }
      if (result.done) break;
    }
    if (buffer !== "") data.push(buffer.startsWith("data: ") ? buffer.slice(6) : buffer);
    if (data.length > 0) yield { data: data.join("\n"), event };
  } finally {
    signal?.removeEventListener("abort", abort);
    reader.releaseLock();
  }
}

/** Creates the browser-safe mecated HTTP/JSON/SSE implementation of Connect Transport. @public */
export function createHttpTransport(options: HttpTransportOptions): Transport {
  const transport = new HttpTransport(options);
  return registerTransport(transport, "http", {
    cancelRun: (sessionId, runId, signal) => transport.cancelRun(sessionId, runId, signal),
  });
}
