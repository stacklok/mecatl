import type { Transport } from "@connectrpc/connect";
import { createRouterTransport } from "@connectrpc/connect";

import { HarnessService, PermissionMode } from "../src/gen/mecatl/v1/harness_pb.js";

export interface ScriptedState {
  features: string[];
  futureField?: string;
  placement: {
    branch: string;
    kind: string;
    label: string;
    revision: string;
  };
  sessionId: string;
}

export interface RecordedRequest {
  credentials: RequestCredentials | undefined;
  headers: Headers;
  method: string;
  url: string;
}

export const scriptedState: ScriptedState = {
  features: ["server_info", "future_transport"],
  futureField: "kept-verbatim",
  placement: {
    branch: "main",
    kind: "local",
    label: "default",
    revision: "abc123",
  },
  sessionId: "session-scripted",
};

export function routerFor(
  state: ScriptedState = scriptedState,
  transportOptions?: Parameters<typeof createRouterTransport>[1],
): Transport {
  return createRouterTransport((router) => {
    router.service(HarnessService, {
      createSession: () => ({ placement: state.placement, sessionId: state.sessionId }),
      getCompatibilityInfo: () => ({ apiMajor: 1, capabilities: {}, features: state.features }),
      getSession: () => ({
        session: {
          mode: PermissionMode.DEFAULT,
          placement: state.placement,
          sessionId: state.sessionId,
          state: "idle",
        },
      }),
      streamSessionEvents: async function* () {
        yield { seq: 1n, text: "offline", type: "message.delta" };
        yield { seq: 2n, type: "result" };
      },
      converse: async function* (requests) {
        for await (const request of requests) {
          yield {
            event: {
              seq: 1n,
              text: request.kind.case ?? "unknown",
              type: "message.delta",
            },
          };
        }
      },
    });
  }, transportOptions);
}

export function fetchFor(
  state: ScriptedState = scriptedState,
  requests: RecordedRequest[] = [],
): typeof globalThis.fetch {
  return async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    requests.push({
      credentials: init?.credentials,
      headers: new Headers(init?.headers),
      method,
      url,
    });
    const path = new URL(url).pathname;
    if (path === "/v1/compatibility") {
      return Response.json({
        api_major: 1,
        capabilities: {},
        features: state.features,
        future_field: state.futureField,
      });
    }
    if (path === "/v1/sessions" && method === "POST") {
      return Response.json(
        { placement: state.placement, session_id: state.sessionId },
        { status: 201 },
      );
    }
    if (path === `/v1/sessions/${state.sessionId}` && method === "GET") {
      return Response.json({
        mode: "default",
        placement: state.placement,
        session_id: state.sessionId,
        state: "idle",
      });
    }
    if (path === `/v1/sessions/${state.sessionId}/events`) {
      return sseResponse([
        { seq: 1, text: "offline", type: "message.delta" },
        { seq: 2, type: "result" },
      ]);
    }
    if (path === `/v1/sessions/${state.sessionId}/prompt`) {
      return sseResponse([{ seq: 1, text: "prompt", type: "message.delta" }]);
    }
    return Response.json(
      {
        code: "not_found",
        detail: `No scripted route for ${method} ${path}`,
        status: 404,
        title: "Not found",
        type: "https://mecatl.stacklok.com/problems/not_found",
      },
      { headers: { "content-type": "application/problem+json" }, status: 404 },
    );
  };
}

export function sseResponse(values: unknown[]): Response {
  return new Response(values.map((value) => `data: ${JSON.stringify(value)}\n\n`).join(""), {
    headers: { "content-type": "text/event-stream" },
  });
}
