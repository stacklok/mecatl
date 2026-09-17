import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type Client,
  connect,
  createHttpTransport,
  type Event,
  InvalidStateError,
  ServerError,
} from "../src/index.js";

const sessionId = "session-authz";
const authorizationId = "authz-7";

interface RecordedRequest {
  readonly body: Record<string, unknown> | undefined;
  readonly method: string;
  readonly path: string;
  readonly rawBody: BodyInit | null | undefined;
}

function sse(frames: readonly unknown[]): Response {
  return new Response(frames.map((frame) => `data: ${JSON.stringify(frame)}\n\n`).join(""), {
    headers: { "content-type": "text/event-stream" },
  });
}

function httpHarness(
  options: {
    readonly features?: readonly string[];
    readonly respond?: (path: string, method: string) => Response | undefined;
  } = {},
): { readonly client: Client; readonly requests: RecordedRequest[] } {
  const requests: RecordedRequest[] = [];
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? "GET";
    const body =
      typeof init?.body === "string"
        ? (JSON.parse(init.body) as Record<string, unknown>)
        : undefined;
    requests.push({ body, method, path: url.pathname, rawBody: init?.body });
    if (url.pathname === "/v1/compatibility") {
      return Response.json({ api_major: 1, features: options.features ?? ["http_steer"] });
    }
    if (url.pathname === `/v1/sessions/${sessionId}` && method === "GET") {
      return Response.json({ session_id: sessionId });
    }
    const scripted = options.respond?.(url.pathname, method);
    if (scripted !== undefined) return scripted;
    return Response.json({ code: "not_found", detail: "not found" }, { status: 404 });
  };
  const transport = createHttpTransport({ baseUrl: "http://mecatl.test", fetch });
  return { client: connect({ transport, transportKind: "http" }), requests };
}

async function collect(source: AsyncIterable<Event>): Promise<Event[]> {
  const events: Event[] = [];
  for await (const value of source) events.push(value);
  return events;
}

describe("session.mcpAuthorization (HTTP)", () => {
  it("reads the live presentation URL", async () => {
    const { client, requests } = httpHarness({
      respond: (path) =>
        path === `/v1/sessions/${sessionId}/mcp-authorizations/${authorizationId}/presentation`
          ? Response.json({ url: "https://idp.example/authorize?state=abc" })
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    await expect(session.mcpAuthorization(authorizationId).presentation()).resolves.toEqual({
      url: "https://idp.example/authorize?state=abc",
    });
    expect(requests.at(-1)).toMatchObject({
      method: "GET",
      path: `/v1/sessions/${sessionId}/mcp-authorizations/${authorizationId}/presentation`,
    });
    await client.close();
  });

  it("recheck streams the daemon's bare events and reports the continuation terminal", async () => {
    const { client, requests } = httpHarness({
      respond: (path, method) =>
        method === "POST" &&
        path === `/v1/sessions/${sessionId}/mcp-authorizations/${authorizationId}/recheck`
          ? sse([
              {
                type: "authorization.resolved",
                authorization: { authorization_id: authorizationId, status: "authorized" },
              },
              { run_id: "run-2", type: "message.delta", text: "resumed" },
              { run_id: "run-2", type: "result", result: { stop: "end_turn", text: "done" } },
            ])
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    const kinds = (await collect(session.mcpAuthorization(authorizationId).recheck())).map(
      (event) => event.kind,
    );
    expect(kinds).toEqual(["authorization.resolved", "message.delta", "result"]);
    const outcome = await session.mcpAuthorization(authorizationId).recheck().result();
    expect(outcome.result).toMatchObject({ runId: "run-2", stopReason: "end_turn", text: "done" });
    const recheck = requests.find((request) => request.path.endsWith("/recheck"));
    // The daemon rejects any body on the authorization controls (HTTP 400).
    expect(recheck?.body).toBeUndefined();
    expect(recheck?.rawBody).toBeUndefined();
    await client.close();
  });

  it("cancel streams the cancellation outcome", async () => {
    const { client, requests } = httpHarness({
      respond: (path, method) =>
        method === "POST" &&
        path === `/v1/sessions/${sessionId}/mcp-authorizations/${authorizationId}/cancel`
          ? sse([
              {
                type: "authorization.resolved",
                authorization: { authorization_id: authorizationId, status: "cancelled" },
              },
            ])
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    const outcome = await session.mcpAuthorization(authorizationId).cancel().result();
    expect(outcome.result).toBeUndefined();
    const cancel = requests.find(
      (request) => request.path.endsWith("/cancel") && request.method === "POST",
    );
    expect(cancel).toBeDefined();
    expect(cancel?.rawBody).toBeUndefined();
    await client.close();
  });

  it("surfaces a stale authorization as the server's typed error", async () => {
    const { client } = httpHarness({
      respond: (path) =>
        path.endsWith("/presentation")
          ? Response.json(
              {
                code: "failed_precondition",
                detail: "authorization is no longer pending",
                status: 412,
              },
              { headers: { "content-type": "application/problem+json" }, status: 412 },
            )
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    await expect(session.mcpAuthorization(authorizationId).presentation()).rejects.toBeInstanceOf(
      ServerError,
    );
    await client.close();
  });

  it("refuses an empty authorization id", async () => {
    const { client } = httpHarness();
    const session = await client.sessions.get(sessionId);
    expect(() => session.mcpAuthorization("")).toThrow(InvalidStateError);
    await client.close();
  });
});

describe("session.mcpAuthorization (gRPC)", () => {
  it("streams the envelope's events over a protobuf transport", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, features: [] }),
        getSession: () => ({ session: { sessionId } }),
        recheckMcpAuthorization: async function* (requests) {
          for await (const request of requests) {
            expect(request.authorizationId).toBe(authorizationId);
          }
          yield { event: { runId: "run-9", text: "resumed", type: "message.delta" } };
          yield {
            event: { result: { stop: "end_turn", text: "ok" }, runId: "run-9", type: "result" },
          };
        },
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);
    const outcome = await session.mcpAuthorization(authorizationId).recheck().result();
    expect(outcome.result).toMatchObject({ runId: "run-9", stopReason: "end_turn" });
    await client.close();
  });
});

describe("session.mcpConnectors and workspaceEnrollment (HTTP)", () => {
  it("projects the connector catalogue", async () => {
    const { client } = httpHarness({
      respond: (path) =>
        path === `/v1/sessions/${sessionId}/mcp/connectors`
          ? Response.json({
              availability: "available",
              enrollment_state: "enrolled",
              connectors: [{ name: "github", catalogue_state: "ready", tool_count: 12 }],
              total_connectors: 1,
              truncated: false,
            })
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    await expect(session.mcpConnectors()).resolves.toEqual({
      availability: "available",
      connectors: [{ catalogueState: "ready", name: "github", toolCount: 12 }],
      enrollmentState: "enrolled",
      totalConnectors: 1,
      truncated: false,
    });
    await client.close();
  });

  it("connect, retry, and cancel hit their routes and project the enrollment", async () => {
    const enrollment = {
      enrollment_id: "enr-1",
      status: "pending",
      required_services: 2,
      presentation_url: "https://gateway.example/enroll/enr-1",
    };
    const { client, requests } = httpHarness({
      respond: (path, method) =>
        method === "POST" && path.startsWith(`/v1/sessions/${sessionId}/workspace-enrollment/`)
          ? Response.json(enrollment)
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    const expected = {
      enrollmentId: "enr-1",
      presentationUrl: "https://gateway.example/enroll/enr-1",
      requiredServices: 2,
      status: "pending",
    };
    await expect(session.workspaceEnrollment.connect()).resolves.toEqual(expected);
    await expect(session.workspaceEnrollment.retry("enr-1")).resolves.toEqual(expected);
    await expect(session.workspaceEnrollment.cancel("enr-1")).resolves.toEqual(expected);
    expect(
      requests.filter((request) => request.method === "POST").map((request) => request.path),
    ).toEqual([
      `/v1/sessions/${sessionId}/workspace-enrollment/connect`,
      `/v1/sessions/${sessionId}/workspace-enrollment/enr-1/retry`,
      `/v1/sessions/${sessionId}/workspace-enrollment/enr-1/cancel`,
    ]);
    await expect(session.workspaceEnrollment.retry("")).rejects.toBeInstanceOf(InvalidStateError);
    await client.close();
  });
});

describe("session.cancelChild (HTTP)", () => {
  it("posts the child id to cancel-child", async () => {
    const { client, requests } = httpHarness({
      respond: (path, method) =>
        method === "POST" && path === `/v1/sessions/${sessionId}/cancel-child`
          ? new Response(null, { status: 204 })
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    await session.cancelChild("subagent-abc");
    expect(requests.at(-1)).toMatchObject({
      body: { child_id: "subagent-abc" },
      method: "POST",
      path: `/v1/sessions/${sessionId}/cancel-child`,
    });
    await expect(session.cancelChild("")).rejects.toBeInstanceOf(InvalidStateError);
    await client.close();
  });
});
