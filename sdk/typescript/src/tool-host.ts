import { createHash, randomBytes, timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

import type { DiagnosticsSink } from "./errors.js";
import type { AdvertisedTool, CallToolResult, ToolHostBinding, ToolJsonValue } from "./tool.js";

const LOOPBACK_ADDRESS = "127.0.0.1";
const MCP_PATH = "/mcp";
const BEARER_BYTES = 32;
const DIGEST_ALGORITHM = "sha256";
const DEFAULT_HANDLER_CONCURRENCY = 8;
const DEFAULT_QUEUE_DEPTH = 64;
const DEFAULT_BODY_BYTES = 1 << 20;
const DEFAULT_CALL_TIMEOUT_MS = 30_000;

// Mirrored deliberately from internal/adapter/toolkit.MaxOutputBytes. Scenario 9
// adds the Go-side source-parity guard for this client-side refusal boundary.
// BEGIN MECATL_TOOL_OUTPUT_CAP
export const TOOLKIT_MAX_OUTPUT_BYTES = 25_000;
// END MECATL_TOOL_OUTPUT_CAP

export const MCP_PROTOCOL_VERSIONS = [
  "2026-07-28",
  "2025-11-25",
  "2025-06-18",
  "2025-03-26",
  "2024-11-05",
] as const;

const LATEST_LEGACY_PROTOCOL_VERSION = "2025-11-25";

interface ToolHostRegistry {
  advertisedTools(): readonly AdvertisedTool[];
  concurrencyFor(name: string): number | undefined;
  invoke(name: string, arguments_: unknown, signal: AbortSignal): Promise<unknown>;
}

interface ToolHostScheduler {
  clear(handle: unknown): void;
  schedule(callback: () => void, delayMs: number): unknown;
}

interface ToolHostInternalOptions {
  bodyBytes?: number;
  callTimeoutMs?: number;
  concurrency?: number;
  queueDepth?: number;
  randomBytes?: (size: number) => Buffer;
  scheduler?: Partial<ToolHostScheduler>;
}

interface JsonRpcRequest {
  readonly id?: number | string | null;
  readonly jsonrpc: "2.0";
  readonly method: string;
  readonly params?: unknown;
}

interface Invocation {
  readonly arguments_: unknown;
  readonly controller: AbortController;
  done: boolean;
  readonly externalSignal: AbortSignal;
  readonly name: string;
  readonly onExternalAbort: () => void;
  resolve(result: CallToolResult): void;
  state: "queued" | "running";
  timer: unknown;
}

class BodyTooLarge extends Error {}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isJsonRpcRequest(value: unknown): value is JsonRpcRequest {
  return (
    isRecord(value) &&
    value.jsonrpc === "2.0" &&
    typeof value.method === "string" &&
    (value.id === undefined ||
      value.id === null ||
      typeof value.id === "string" ||
      typeof value.id === "number")
  );
}

function explicitCallToolResult(value: unknown): value is CallToolResult {
  return (
    isRecord(value) &&
    Array.isArray(value.content) &&
    value.content.every((block) => isRecord(block) && typeof block.type === "string")
  );
}

function errorResult(message: string): CallToolResult {
  return { content: [{ text: message, type: "text" }], isError: true };
}

function normalizeResult(value: unknown): CallToolResult {
  if (explicitCallToolResult(value)) return value;
  if (typeof value === "string") {
    return { content: [{ text: value, type: "text" }] };
  }
  const mirror = JSON.stringify(value);
  if (mirror === undefined) {
    throw new TypeError("callback tool returned a value that is not JSON serializable");
  }
  const structuredContent = JSON.parse(mirror) as ToolJsonValue;
  return {
    content: [{ text: mirror, type: "text" }],
    structuredContent,
  };
}

function serializedBytes(value: unknown): number {
  const serialized = JSON.stringify(value);
  if (serialized === undefined) {
    throw new TypeError("callback tool returned a value that is not JSON serializable");
  }
  return Buffer.byteLength(serialized);
}

function jsonResponse(response: ServerResponse, id: JsonRpcRequest["id"], result: unknown): void {
  response.statusCode = 200;
  response.setHeader("Content-Type", "application/json");
  response.end(JSON.stringify({ id: id ?? null, jsonrpc: "2.0", result }));
}

function jsonError(
  response: ServerResponse,
  id: JsonRpcRequest["id"],
  code: number,
  message: string,
): void {
  response.statusCode = 200;
  response.setHeader("Content-Type", "application/json");
  response.end(JSON.stringify({ error: { code, message }, id: id ?? null, jsonrpc: "2.0" }));
}

function refuseBeforeBody(
  request: IncomingMessage,
  response: ServerResponse,
  status: number,
): void {
  request.pause();
  response.statusCode = status;
  response.setHeader("Connection", "close");
  response.end("request refused", () => request.destroy());
}

async function readBoundedBody(request: IncomingMessage, limit: number): Promise<string> {
  const declared = Number(request.headers["content-length"]);
  if (Number.isFinite(declared) && declared > limit) throw new BodyTooLarge();

  return new Promise<string>((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    const cleanup = () => {
      request.off("aborted", onAborted);
      request.off("data", onData);
      request.off("end", onEnd);
      request.off("error", onError);
    };
    const onAborted = () => {
      cleanup();
      reject(new Error("request aborted"));
    };
    const onData = (chunk: Buffer) => {
      size += chunk.byteLength;
      if (size > limit) {
        cleanup();
        request.pause();
        reject(new BodyTooLarge());
        return;
      }
      chunks.push(Buffer.from(chunk));
    };
    const onEnd = () => {
      cleanup();
      resolve(Buffer.concat(chunks).toString("utf8"));
    };
    const onError = (error: Error) => {
      cleanup();
      reject(error);
    };
    request.on("aborted", onAborted);
    request.on("data", onData);
    request.on("end", onEnd);
    request.on("error", onError);
  });
}

function positiveInteger(value: number | undefined, fallback: number): number {
  return value !== undefined && Number.isSafeInteger(value) && value > 0 ? value : fallback;
}

export class LoopbackToolHost implements ToolHostBinding {
  readonly #activeByTool = new Map<string, number>();
  readonly #all = new Set<Invocation>();
  readonly #bodyBytes: number;
  readonly #callTimeoutMs: number;
  readonly #concurrency: number;
  readonly #diagnostics: DiagnosticsSink | undefined;
  readonly #expectedDigest: Buffer;
  readonly #queue: Invocation[] = [];
  readonly #queueDepth: number;
  readonly #randomBytes: (size: number) => Buffer;
  readonly #registry: () => ToolHostRegistry;
  readonly #scheduler: ToolHostScheduler;
  readonly #token: string;
  #accepting = true;
  #active = 0;
  #boundAuthority: string | undefined;
  #server: Server | undefined;
  #startPromise: Promise<void> | undefined;
  #stopPromise: Promise<void> | undefined;

  constructor(
    registry: () => ToolHostRegistry,
    diagnostics?: DiagnosticsSink,
    internal: ToolHostInternalOptions = {},
  ) {
    this.#registry = registry;
    this.#diagnostics = diagnostics;
    this.#bodyBytes = positiveInteger(internal.bodyBytes, DEFAULT_BODY_BYTES);
    this.#callTimeoutMs = positiveInteger(internal.callTimeoutMs, DEFAULT_CALL_TIMEOUT_MS);
    this.#concurrency = positiveInteger(internal.concurrency, DEFAULT_HANDLER_CONCURRENCY);
    this.#queueDepth = positiveInteger(internal.queueDepth, DEFAULT_QUEUE_DEPTH);
    this.#randomBytes = internal.randomBytes ?? randomBytes;
    this.#scheduler = {
      clear: internal.scheduler?.clear ?? ((handle) => clearTimeout(handle as NodeJS.Timeout)),
      schedule:
        internal.scheduler?.schedule ?? ((callback, delayMs) => setTimeout(callback, delayMs)),
    };
    this.#token = this.#randomBytes(BEARER_BYTES).toString("base64url");
    this.#expectedDigest = this.#digest(`Bearer ${this.#token}`);
  }

  abort(reason: unknown): void {
    this.#accepting = false;
    for (const invocation of [...this.#all]) {
      this.#finish(
        invocation,
        errorResult(`Callback tool ${JSON.stringify(invocation.name)} was cancelled`),
        reason,
      );
    }
  }

  mcpServer(): { headers: Record<string, string>; type: "http"; url: string } {
    if (this.#boundAuthority === undefined) {
      throw new Error("The callback tool host is not listening");
    }
    return {
      headers: { Authorization: `Bearer ${this.#token}` },
      type: "http",
      url: `http://${this.#boundAuthority}${MCP_PATH}`,
    };
  }

  start(): Promise<void> {
    this.#startPromise ??= this.#listen();
    return this.#startPromise;
  }

  stop(): Promise<void> {
    this.#stopPromise ??= this.#stop();
    return this.#stopPromise;
  }

  async #listen(): Promise<void> {
    if (!this.#accepting) throw new Error("The callback tool host is closed");
    const server = createServer((request, response) => {
      void this.#handle(request, response).catch(() => {
        if (!response.headersSent) response.statusCode = 500;
        if (!response.writableEnded) response.end("callback tool host failure");
      });
    });
    this.#server = server;
    await new Promise<void>((resolve, reject) => {
      const onError = (error: Error) => {
        server.off("listening", onListening);
        reject(error);
      };
      const onListening = () => {
        server.off("error", onError);
        resolve();
      };
      server.once("error", onError);
      server.once("listening", onListening);
      server.listen(0, LOOPBACK_ADDRESS);
    });
    const address = server.address() as AddressInfo | null;
    if (address === null || address.address !== LOOPBACK_ADDRESS) {
      await new Promise<void>((resolve) => server.close(() => resolve()));
      throw new Error("The callback tool host did not bind the required IPv4 loopback address");
    }
    this.#boundAuthority = `${LOOPBACK_ADDRESS}:${address.port}`;
  }

  async #stop(): Promise<void> {
    this.abort(new Error("The callback tool host is closing"));
    try {
      await this.#startPromise;
    } catch {
      // A failed listener has no live port to close.
    }
    const server = this.#server;
    this.#boundAuthority = undefined;
    if (server === undefined || !server.listening) return;
    const closed = new Promise<void>((resolve, reject) => {
      server.close((error) => (error === undefined ? resolve() : reject(error)));
    });
    server.closeAllConnections();
    await closed;
  }

  #digest(value: string): Buffer {
    return createHash(DIGEST_ALGORITHM).update(value, "utf8").digest();
  }

  #authenticated(request: IncomingMessage): boolean {
    const presented = Array.isArray(request.headers.authorization)
      ? request.headers.authorization.join(",")
      : (request.headers.authorization ?? "");
    return timingSafeEqual(this.#digest(presented), this.#expectedDigest);
  }

  async #handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    if (request.method !== "POST") {
      response.statusCode = 405;
      response.setHeader("Allow", "POST");
      response.end("method not allowed");
      return;
    }
    if (request.headers.origin !== undefined || request.headers.host !== this.#boundAuthority) {
      refuseBeforeBody(request, response, 403);
      return;
    }
    if (!this.#authenticated(request)) {
      refuseBeforeBody(request, response, 401);
      return;
    }
    if (request.url !== MCP_PATH) {
      refuseBeforeBody(request, response, 404);
      return;
    }
    if (!this.#accepting) {
      refuseBeforeBody(request, response, 503);
      return;
    }

    let body: string;
    try {
      body = await readBoundedBody(request, this.#bodyBytes);
    } catch (error) {
      if (error instanceof BodyTooLarge) {
        response.statusCode = 413;
        response.setHeader("Connection", "close");
        response.end("request body too large", () => request.destroy());
      }
      return;
    }

    let rpc: unknown;
    try {
      rpc = JSON.parse(body);
    } catch {
      jsonError(response, null, -32700, "Parse error");
      return;
    }
    if (!isJsonRpcRequest(rpc)) {
      jsonError(response, null, -32600, "Invalid Request");
      return;
    }

    switch (rpc.method) {
      case "server/discover":
        jsonError(response, rpc.id, -32601, "Method not found");
        return;
      case "initialize": {
        const requested =
          isRecord(rpc.params) && typeof rpc.params.protocolVersion === "string"
            ? rpc.params.protocolVersion
            : "";
        const protocolVersion =
          requested !== "2026-07-28" &&
          MCP_PROTOCOL_VERSIONS.includes(requested as (typeof MCP_PROTOCOL_VERSIONS)[number])
            ? requested
            : LATEST_LEGACY_PROTOCOL_VERSION;
        jsonResponse(response, rpc.id, {
          capabilities: { tools: {} },
          protocolVersion,
          serverInfo: { name: "@stacklok-oss/mecatl-sdk", version: "0.1.0" },
          supportedVersions: [...MCP_PROTOCOL_VERSIONS],
        });
        return;
      }
      case "notifications/initialized":
        response.statusCode = 202;
        response.end();
        return;
      case "tools/list":
        jsonResponse(response, rpc.id, { tools: this.#registry().advertisedTools() });
        return;
      case "tools/call": {
        if (!isRecord(rpc.params) || typeof rpc.params.name !== "string") {
          jsonError(response, rpc.id, -32602, "Invalid params");
          return;
        }
        const requestAbort = new AbortController();
        const abort = () => requestAbort.abort(new Error("MCP caller disconnected"));
        request.once("aborted", abort);
        response.once("close", () => {
          if (!response.writableEnded) abort();
        });
        const result = await this.#enqueue(
          rpc.params.name,
          rpc.params.arguments,
          requestAbort.signal,
        );
        request.off("aborted", abort);
        if (!response.destroyed) jsonResponse(response, rpc.id, result);
        return;
      }
      case "ping":
        jsonResponse(response, rpc.id, {});
        return;
      default:
        jsonError(response, rpc.id, -32601, "Method not found");
    }
  }

  #enqueue(
    name: string,
    arguments_: unknown,
    externalSignal: AbortSignal,
  ): Promise<CallToolResult> {
    if (!this.#accepting) return Promise.resolve(errorResult("The callback tool host is closed"));
    if (this.#queue.length >= this.#queueDepth) {
      return Promise.resolve(
        errorResult(
          `Callback tool ${JSON.stringify(name)} could not start because the ${this.#queueDepth}-call queue is full`,
        ),
      );
    }
    return new Promise<CallToolResult>((resolve) => {
      const controller = new AbortController();
      const invocation = {} as Invocation;
      const onExternalAbort = () => {
        this.#finish(
          invocation,
          errorResult(`Callback tool ${JSON.stringify(name)} was cancelled`),
          externalSignal.reason,
        );
      };
      Object.assign(invocation, {
        arguments_,
        controller,
        done: false,
        externalSignal,
        name,
        onExternalAbort,
        resolve,
        state: "queued" as const,
        timer: this.#scheduler.schedule(() => {
          this.#finish(
            invocation,
            errorResult(
              `Callback tool ${JSON.stringify(name)} exceeded its ${this.#callTimeoutMs}ms deadline`,
            ),
            new Error("callback tool deadline exceeded"),
          );
        }, this.#callTimeoutMs),
      });
      this.#all.add(invocation);
      externalSignal.addEventListener("abort", onExternalAbort, { once: true });
      if (externalSignal.aborted) {
        onExternalAbort();
        return;
      }
      this.#queue.push(invocation);
      this.#drain();
    });
  }

  #drain(): void {
    if (!this.#accepting) return;
    while (this.#active < this.#concurrency) {
      const index = this.#queue.findIndex((invocation) => {
        const configured = this.#registry().concurrencyFor(invocation.name);
        const limit = Math.min(this.#concurrency, configured ?? this.#concurrency);
        return (this.#activeByTool.get(invocation.name) ?? 0) < limit;
      });
      if (index < 0) return;
      const [invocation] = this.#queue.splice(index, 1);
      if (invocation === undefined || invocation.done) continue;
      invocation.state = "running";
      this.#active += 1;
      this.#activeByTool.set(invocation.name, (this.#activeByTool.get(invocation.name) ?? 0) + 1);
      void this.#run(invocation);
    }
  }

  async #run(invocation: Invocation): Promise<void> {
    try {
      const raw = await this.#registry().invoke(
        invocation.name,
        invocation.arguments_,
        invocation.controller.signal,
      );
      if (invocation.done) return;
      const result = normalizeResult(raw);
      if (serializedBytes(result) > TOOLKIT_MAX_OUTPUT_BYTES) {
        this.#finish(
          invocation,
          errorResult(
            `Callback tool ${JSON.stringify(invocation.name)} result exceeded the ${TOOLKIT_MAX_OUTPUT_BYTES}-byte output cap and was not returned`,
          ),
        );
        return;
      }
      this.#finish(invocation, result);
    } catch (cause) {
      if (invocation.done) return;
      const correlationId = this.#randomBytes(12).toString("hex");
      this.#emitDiagnostic({
        cause,
        code: "tool_handler_failed",
        fields: Object.freeze({ correlationId, tool: invocation.name }),
        level: "error",
        message: "A callback tool handler threw",
      });
      this.#finish(
        invocation,
        errorResult(
          `Callback tool ${JSON.stringify(invocation.name)} failed (correlation id: ${correlationId})`,
        ),
      );
    }
  }

  #finish(invocation: Invocation, result: CallToolResult, abortReason?: unknown): void {
    if (invocation.done) return;
    invocation.done = true;
    this.#all.delete(invocation);
    this.#scheduler.clear(invocation.timer);
    invocation.externalSignal.removeEventListener("abort", invocation.onExternalAbort);
    if (abortReason !== undefined && !invocation.controller.signal.aborted) {
      invocation.controller.abort(abortReason);
    }
    if (invocation.state === "queued") {
      const index = this.#queue.indexOf(invocation);
      if (index >= 0) this.#queue.splice(index, 1);
    } else {
      this.#active -= 1;
      const next = (this.#activeByTool.get(invocation.name) ?? 1) - 1;
      if (next === 0) this.#activeByTool.delete(invocation.name);
      else this.#activeByTool.set(invocation.name, next);
    }
    invocation.resolve(result);
    this.#drain();
  }

  #emitDiagnostic(record: Parameters<DiagnosticsSink>[0]): void {
    try {
      this.#diagnostics?.(Object.freeze(record));
    } catch {
      // Diagnostics observers never alter callback execution.
    }
  }
}
