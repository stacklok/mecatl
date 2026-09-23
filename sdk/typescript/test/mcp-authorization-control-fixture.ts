import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
} from "@bufbuild/protobuf";
import { create } from "@bufbuild/protobuf";
import type { ContextValues, StreamResponse, Transport, UnaryResponse } from "@connectrpc/connect";

import { type Client, connect, createHttpTransport, ServerError } from "../src/index.js";

export const sessionId = "session-authorization-controls";
export const authorizationId = "authorization-controls";
export const continuationRunId = "run-authorization-controls";

export interface RecordedCall {
  readonly headers: Headers;
  readonly input: unknown;
  readonly method: string;
  readonly signal: AbortSignal | undefined;
  readonly timeoutMs: number | undefined;
}

export type WireEvent = Record<string, unknown>;

export interface StreamPlan {
  readonly events?: readonly WireEvent[];
  readonly error?: unknown;
  readonly hold?: boolean;
}

export interface HarnessOptions {
  readonly beforeUnaryDispatch?: (
    method: string,
    input: Record<string, unknown>,
    signal: AbortSignal | undefined,
  ) => void | Promise<void>;
  readonly features?: readonly string[];
  readonly streams?: readonly StreamPlan[];
  readonly watchStreams?: readonly StreamPlan[];
  readonly unary?: (
    method: string,
    input: Record<string, unknown>,
    signal: AbortSignal | undefined,
  ) => unknown | Promise<unknown>;
}

export class LifecycleTransport implements Transport {
  readonly calls: RecordedCall[] = [];
  activeStreams = 0;
  closedStreams = 0;
  maxActiveStreams = 0;
  readonly #features: readonly string[];
  readonly #beforeUnaryDispatch: HarnessOptions["beforeUnaryDispatch"];
  readonly #streams: StreamPlan[];
  readonly #unary: HarnessOptions["unary"];
  readonly #watchStreams: StreamPlan[];

  constructor(options: HarnessOptions = {}) {
    this.#beforeUnaryDispatch = options.beforeUnaryDispatch;
    this.#features = options.features ?? ["prompt_free_controls", "watch_session_events"];
    this.#streams = [...(options.streams ?? [])];
    this.#unary = options.unary;
    this.#watchStreams = [...(options.watchStreams ?? [])];
  }

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    headers: HeadersInit | undefined,
    input: MessageInitShape<I>,
    contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    void contextValues;
    await this.#beforeUnaryDispatch?.(method.name, input as Record<string, unknown>, signal);
    if (signal?.aborted === true)
      throw signal.reason ?? new Error("request aborted before dispatch");
    this.calls.push({
      headers: new Headers(headers),
      input,
      method: method.name,
      signal,
      timeoutMs,
    });
    const supplied = await this.#unary?.(method.name, input as Record<string, unknown>, signal);
    if (supplied instanceof Error) throw supplied;
    let value: unknown = supplied;
    if (value === undefined) {
      switch (method.name) {
        case "GetCompatibilityInfo":
          value = { apiMajor: 1, capabilities: {}, features: [...this.#features] };
          break;
        case "GetSession":
          value = {
            session: { sessionId: String((input as { sessionId?: string }).sessionId ?? "") },
          };
          break;
        case "ResolveRunAsk":
          value = {
            askId: String((input as { askId?: string }).askId ?? ""),
            runId: String((input as { expectedRunId?: string }).expectedRunId ?? ""),
          };
          break;
        case "CancelRun":
          value = { runId: String((input as { expectedRunId?: string }).expectedRunId ?? "") };
          break;
        default:
          throw new Error(`Unexpected unary ${method.name}`);
      }
    }
    return {
      header: new Headers({ "x-fixture-response": method.name }),
      message: create(method.output, value as MessageInitShape<O>),
      method,
      service: method.parent,
      stream: false,
      trailer: new Headers({ "x-fixture-trailer": method.name }),
    };
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    headers: HeadersInit | undefined,
    input: AsyncIterable<MessageInitShape<I>>,
    contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    void contextValues;
    const first = await input[Symbol.asyncIterator]().next();
    this.calls.push({
      headers: new Headers(headers),
      input: first.value,
      method: method.name,
      signal,
      timeoutMs,
    });
    const watch = method.name === "WatchSessionEvents";
    const plan = (watch ? this.#watchStreams : this.#streams).shift() ?? { events: [] };
    const owner = this;
    const messages = (async function* () {
      owner.activeStreams += 1;
      owner.maxActiveStreams = Math.max(owner.maxActiveStreams, owner.activeStreams);
      try {
        for (const event of plan.events ?? []) {
          yield create(
            method.output,
            (watch ? event : { event }) as unknown as MessageInitShape<O>,
          );
        }
        if (plan.error !== undefined) throw plan.error;
        if (plan.hold === true) {
          await new Promise<never>((_resolve, reject) => {
            const abort = () => reject(signal?.reason ?? new Error("aborted"));
            if (signal?.aborted === true) abort();
            else signal?.addEventListener("abort", abort, { once: true });
          });
        }
      } finally {
        owner.activeStreams -= 1;
        owner.closedStreams += 1;
      }
    })();
    return {
      header: new Headers({ "x-fixture-response": method.name }),
      message: messages,
      method,
      service: method.parent,
      stream: true,
      trailer: new Headers({ "x-fixture-trailer": method.name }),
    };
  }
}

export async function harness(options: HarnessOptions = {}): Promise<{
  readonly client: Client;
  readonly session: Awaited<ReturnType<Client["sessions"]["get"]>>;
  readonly transport: LifecycleTransport;
}> {
  const transport = new LifecycleTransport(options);
  const client = connect({ transport });
  const session = await client.sessions.get(sessionId);
  return { client, session, transport };
}

export interface HttpRequest {
  readonly body: BodyInit | null | undefined;
  readonly headers: Headers;
  readonly path: string;
  readonly signal: AbortSignal | null | undefined;
}

export async function httpHarness(events: readonly Record<string, unknown>[]): Promise<{
  readonly client: Client;
  readonly requests: HttpRequest[];
  readonly session: Awaited<ReturnType<Client["sessions"]["get"]>>;
}> {
  const requests: HttpRequest[] = [];
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const path = new URL(String(input)).pathname;
    requests.push({
      body: init?.body,
      headers: new Headers(init?.headers),
      path,
      signal: init?.signal,
    });
    if (path === "/v1/compatibility") {
      return Response.json({
        api_major: 1,
        capabilities: {},
        features: ["prompt_free_controls", "watch_session_events"],
      });
    }
    if (path === `/v1/sessions/${sessionId}`) {
      return Response.json({ session_id: sessionId, state: "idle" });
    }
    if (path.endsWith("/recheck")) {
      return new Response(events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(""), {
        headers: { "content-type": "text/event-stream" },
      });
    }
    if (path.endsWith("/resolve-ask")) {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      return Response.json({ ask_id: body.ask_id, run_id: body.expected_run_id });
    }
    if (path.endsWith("/cancel")) {
      const body = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
      return Response.json({ run_id: body.expected_run_id });
    }
    throw new Error(`Unexpected HTTP request ${path}`);
  };
  const client = connect({
    transport: createHttpTransport({ baseUrl: "http://mecatl.test", fetch }),
    transportKind: "http",
  });
  const session = await client.sessions.get(sessionId);
  return { client, requests, session };
}

export function httpContinuation(...events: Record<string, unknown>[]): Record<string, unknown>[] {
  const authorization = {
    authorization_id: authorizationId,
    call_id: "authorization-call",
    display_name: "Example MCP",
    status: "granted",
  };
  return [
    { authorization, run_id: "", type: "authorization.resolved" },
    { authorization, run_id: continuationRunId, type: "authorization.resolved" },
    ...events,
  ];
}

export function httpAsk(askId: string): Record<string, unknown> {
  return {
    ask: { args: "{}", ask_id: askId, reason: "test", tool: "Shell" },
    run_id: continuationRunId,
    type: "permission.ask",
  };
}

export function authorization(
  type: "authorization.required" | "authorization.resolved",
  status: string,
  options: {
    readonly authorizationId?: string;
    readonly callId?: string;
    readonly runId?: string;
  } = {},
): WireEvent {
  return {
    authorization: {
      authorizationId: options.authorizationId ?? authorizationId,
      callId: options.callId ?? "authorization-call",
      displayName: "Example MCP",
      status,
    },
    runId: options.runId ?? "",
    type,
  };
}

export function ask(
  askId: string,
  options: { readonly runId?: string; readonly tool?: string } = {},
): WireEvent {
  return {
    ask: { args: "{}", askId, reason: "test", tool: options.tool ?? "Shell" },
    runId: options.runId ?? continuationRunId,
    type: "permission.ask",
  };
}

export function retract(askId: string, runId = continuationRunId): WireEvent {
  return {
    ask: { args: "{}", askId, reason: "test", tool: "Shell" },
    runId,
    type: "permission.retract",
  };
}

export function result(runId = continuationRunId): WireEvent {
  return {
    result: { stop: "end_turn", text: "done", usage: { inputTokens: 1n, outputTokens: 1n } },
    runId,
    type: "result",
  };
}

export function continuation(...events: WireEvent[]): WireEvent[] {
  return [
    authorization("authorization.resolved", "granted"),
    authorization("authorization.resolved", "granted", { runId: continuationRunId }),
    ...events,
  ];
}

export function notFound(message = "authorization no longer pending"): ServerError {
  return new ServerError(message, { code: "not_found", transport: "grpc" });
}

export async function flush(): Promise<void> {
  for (let index = 0; index < 20; index += 1) await Promise.resolve();
}
