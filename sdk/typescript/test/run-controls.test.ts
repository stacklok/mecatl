import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { HarnessService } from "../src/gen/mecatl/v1/harness_pb.js";
import {
  type Client,
  connect,
  createHttpTransport,
  imagePart,
  ServerError,
  UnsupportedFeatureError,
} from "../src/index.js";

const sessionId = "session-run-controls";
const runId = "run-run-controls";

interface RecordedRequest {
  readonly body: Record<string, unknown> | undefined;
  readonly method: string;
  readonly path: string;
}

interface HarnessOptions {
  readonly features?: readonly string[];
  readonly respond?: (
    path: string,
    body: Record<string, unknown> | undefined,
  ) => Response | undefined;
  readonly sessionCapabilities?: { image: boolean; audio: boolean };
}

function stale(): Response {
  return Response.json(
    {
      code: "stale_run_control",
      detail: "run is no longer current",
      status: 409,
      title: "Conflict",
      type: "https://mecatl.stacklok.com/problems/stale_run_control",
    },
    { headers: { "content-type": "application/problem+json" }, status: 409 },
  );
}

function httpHarness(options: HarnessOptions = {}): {
  readonly client: Client;
  readonly requests: RecordedRequest[];
} {
  const requests: RecordedRequest[] = [];
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? "GET";
    const body =
      typeof init?.body === "string"
        ? (JSON.parse(init.body) as Record<string, unknown>)
        : undefined;
    requests.push({ body, method, path: url.pathname });
    if (url.pathname === "/v1/compatibility") {
      return Response.json({
        api_major: 1,
        features: options.features ?? ["http_steer"],
      });
    }
    if (url.pathname === `/v1/sessions/${sessionId}` && method === "GET") {
      return Response.json({
        session_capabilities: options.sessionCapabilities ?? { audio: false, image: true },
        session_id: sessionId,
      });
    }
    const scripted = options.respond?.(url.pathname, body);
    if (scripted !== undefined) return scripted;
    if (url.pathname === `/v1/sessions/${sessionId}/approve`) {
      return new Response(null, { status: 204 });
    }
    if (url.pathname === `/v1/sessions/${sessionId}/cancel`) {
      return new Response(null, { status: 204 });
    }
    if (url.pathname === `/v1/sessions/${sessionId}/steer`) {
      return Response.json({ message_id: body?.message_id ?? "", outcome: "accepted" });
    }
    if (url.pathname === `/v1/sessions/${sessionId}/cancel-steer`) {
      return Response.json({ message_id: body?.message_id ?? "", outcome: "retracted" });
    }
    return Response.json({ code: "not_found", detail: "not found" }, { status: 404 });
  };
  const transport = createHttpTransport({ baseUrl: "http://mecatl.test", fetch });
  return { client: connect({ transport, transportKind: "http" }), requests };
}

function controlRequests(requests: RecordedRequest[]): RecordedRequest[] {
  return requests.filter(
    (request) =>
      request.path !== "/v1/compatibility" && request.path !== `/v1/sessions/${sessionId}`,
  );
}

describe("session.controls (HTTP)", () => {
  it("resolves an ask with the strict approve body", async () => {
    const { client, requests } = httpHarness();
    const session = await client.sessions.get(sessionId);
    await session.controls(runId).resolveAsk("ask-1", "allow_always");
    expect(controlRequests(requests)).toEqual([
      {
        body: {
          allow: true,
          ask_id: "ask-1",
          expected_run_id: runId,
          verdict: "allow_always",
        },
        method: "POST",
        path: `/v1/sessions/${sessionId}/approve`,
      },
    ]);
    await client.close();
  });

  it("cancels the named run only", async () => {
    const { client, requests } = httpHarness();
    const session = await client.sessions.get(sessionId);
    await session.controls(runId).cancel();
    expect(controlRequests(requests)).toEqual([
      {
        body: { expected_run_id: runId },
        method: "POST",
        path: `/v1/sessions/${sessionId}/cancel`,
      },
    ]);
    await client.close();
  });

  it("steers with text, media parts, and the caller's message id, returning the outcome", async () => {
    const { client, requests } = httpHarness();
    const session = await client.sessions.get(sessionId);
    const ack = await session
      .controls(runId)
      .steer(
        [
          { kind: "text", text: "focus" },
          imagePart({ bytes: new Uint8Array([1, 2]), mimeType: "image/png" }),
        ],
        {
          messageId: "m-7",
        },
      );
    expect(ack).toEqual({ messageId: "m-7", outcome: "accepted", promoted: false, runId: "" });
    const [steer] = controlRequests(requests);
    expect(steer?.path).toBe(`/v1/sessions/${sessionId}/steer`);
    expect(steer?.body).toMatchObject({
      expected_run_id: runId,
      message_id: "m-7",
      text: "focus",
    });
    expect(steer?.body?.parts).toEqual([{ data: "AQI=", kind: "image", mime_type: "image/png" }]);
    await client.close();
  });

  it("reports too_late verbatim so the caller keeps the text", async () => {
    const { client } = httpHarness({
      respond: (path) =>
        path === `/v1/sessions/${sessionId}/steer`
          ? Response.json({ outcome: "too_late", promoted: true, run_id: "run-next" })
          : undefined,
    });
    const session = await client.sessions.get(sessionId);
    await expect(session.controls(runId).steer("late")).resolves.toEqual({
      messageId: "",
      outcome: "too_late",
      promoted: true,
      runId: "run-next",
    });
    await client.close();
  });

  it("retracts the pending steer bundle", async () => {
    const { client, requests } = httpHarness();
    const session = await client.sessions.get(sessionId);
    await expect(session.controls(runId).cancelSteer({ messageId: "m-9" })).resolves.toEqual({
      messageId: "m-9",
      outcome: "retracted",
    });
    expect(controlRequests(requests)).toEqual([
      {
        body: { expected_run_id: runId, message_id: "m-9" },
        method: "POST",
        path: `/v1/sessions/${sessionId}/cancel-steer`,
      },
    ]);
    await client.close();
  });

  it("surfaces a stale control as the server's typed error", async () => {
    const { client } = httpHarness({
      respond: (path) => (path === `/v1/sessions/${sessionId}/approve` ? stale() : undefined),
    });
    const session = await client.sessions.get(sessionId);
    const failure = session.controls(runId).resolveAsk("ask-1", "deny");
    await expect(failure).rejects.toBeInstanceOf(ServerError);
    await expect(failure).rejects.toMatchObject({ code: "stale_run_control", status: 409 });
    await client.close();
  });

  it("gates steer and cancel-steer on the http_steer feature", async () => {
    const { client } = httpHarness({ features: [] });
    const session = await client.sessions.get(sessionId);
    const controls = session.controls(runId);
    await expect(controls.steer("x")).rejects.toMatchObject({
      code: "unsupported_feature",
      feature: "http_steer",
    });
    await expect(controls.cancelSteer()).rejects.toMatchObject({
      code: "unsupported_feature",
      feature: "http_steer",
    });
    await client.close();
  });

  it("refuses an empty run id", async () => {
    const { client } = httpHarness();
    const session = await client.sessions.get(sessionId);
    expect(() => session.controls("")).toThrow(/run id/);
    await client.close();
  });
});

describe("session.controls (gRPC)", () => {
  it("is a typed unsupported-feature error on a protobuf transport", async () => {
    const transport = createRouterTransport((router) => {
      router.service(HarnessService, {
        getCompatibilityInfo: () => ({ apiMajor: 1, features: [] }),
        getSession: () => ({ session: { sessionId } }),
      });
    });
    const client = connect({ transport });
    const session = await client.sessions.get(sessionId);
    const failure = session.controls(runId).cancel();
    await expect(failure).rejects.toBeInstanceOf(UnsupportedFeatureError);
    await expect(failure).rejects.toMatchObject({
      feature: "prompt_free_controls",
      transport: "grpc",
    });
    await client.close();
  });
});
