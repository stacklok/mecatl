/**
 * Test support for the SDK-backed harness modules: a `globalThis.fetch` stub
 * that answers the SDK's compatibility probe and routes every other daemon
 * request to a per-test handler, recording what crossed the wire.
 *
 * The SDK sends through the ambient `fetch` (sdk.ts resolves it per call), so
 * `vi.stubGlobal("fetch", …)` reaches an already-created client; the client
 * caches the compatibility document, so tests reset it in `afterEach`.
 */

import { vi } from "vitest";

export interface RecordedRequest {
  /** The request URL as the SDK spelled it (`/api/mecatl/v1/...`). */
  url: string;
  /** The path after the proxy prefix, e.g. `/v1/sessions/s1/steer`. */
  path: string;
  method: string;
  /** The JSON-decoded body, undefined for a bodyless request. */
  body: unknown;
  init: RequestInit | undefined;
}

/**
 * Answers one recorded request: a `Response` is sent as-is, a plain value is
 * sent as a 200 JSON body, and `undefined` becomes a 404 problem so a test
 * that forgets a route fails loudly rather than hanging.
 */
export type StubHandler = (request: RecordedRequest) => unknown;

/**
 * The compatibility document the stub serves for the SDK's probe. `features`
 * defaults to everything Studio's session modules gate on; any other key
 * (e.g. `capabilities`) is merged into the document verbatim.
 */
export interface StubOptions {
  features?: string[];
  [key: string]: unknown;
}

export interface FetchStub {
  /** Every non-probe request, in order. */
  requests: RecordedRequest[];
  /** Alias of `requests`. */
  calls: RecordedRequest[];
  /** The most recent non-probe request (throws when none was made). */
  last(): RecordedRequest;
}

const DEFAULT_FEATURES = ["http_steer", "server_info", "watch_session_events"];

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers:
      status >= 400
        ? { "Content-Type": "application/problem+json" }
        : { "Content-Type": "application/json" },
  });
}

/** A daemon RFC 9457 refusal with the stable machine code. */
export function problemResponse(
  status: number,
  code: string,
  error: string,
): Response {
  return jsonResponse(status, { code, error, status });
}

/** One `text/event-stream` response; each frame is written verbatim then
 *  terminated by the blank line. */
export function sseResponse(frames: string[]): Response {
  return new Response(frames.map((frame) => `${frame}\n\n`).join(""), {
    status: 200,
    headers: { "Content-Type": "text/event-stream" },
  });
}

/** A `data:` frame carrying one JSON payload. */
export const dataFrame = (payload: unknown) =>
  `data: ${JSON.stringify(payload)}`;

/** A minimal session snapshot as `GET /v1/sessions/{id}` answers it. */
export function sessionSnapshot(
  sessionId: string,
  extra: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    session_id: sessionId,
    state: "idle",
    mode: "default",
    session_capabilities: { image: true, audio: false },
    ...extra,
  };
}

/**
 * Installs the fetch stub. Requests the handler leaves unanswered get a 404
 * problem, so a test that forgets a route fails loudly rather than hanging.
 */
export function stubHarnessFetch(
  handler: StubHandler,
  options?: StubOptions,
): FetchStub {
  const requests: RecordedRequest[] = [];
  const { features = DEFAULT_FEATURES, ...compatibility } = options ?? {};
  vi.stubGlobal(
    "fetch",
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const path = url.replace(/^\/api\/mecatl/, "");
      if (path === "/v1/compatibility") {
        // The SDK's server namespace requires a capabilities object on the
        // document (an older daemon that omits it is refused), so the stub
        // always serves one; a test may override or extend it.
        return jsonResponse(200, {
          api_major: 1,
          capabilities: {},
          features,
          ...compatibility,
        });
      }
      const request: RecordedRequest = {
        url,
        path,
        method: init?.method ?? "GET",
        body:
          typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
        init,
      };
      requests.push(request);
      const answer = await handler(request);
      if (answer instanceof Response) return answer;
      if (answer === undefined) {
        return problemResponse(
          404,
          "not_found",
          `unstubbed route ${request.path}`,
        );
      }
      return jsonResponse(200, answer);
    },
  );
  return {
    requests,
    calls: requests,
    last() {
      const call = requests.at(-1);
      if (!call) throw new Error("no request was made");
      return call;
    },
  };
}
