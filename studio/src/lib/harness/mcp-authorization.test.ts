import { afterEach, describe, expect, it, vi } from "vitest";
import type { StreamEvent } from "@/features/agent/types";
import {
  CONTROL_FIRST_EVENT_TIMEOUT_MS,
  cancelMcpAuthorization,
  fetchMcpAuthorizationUrl,
  recheckMcpAuthorization,
} from "./mcp-authorization";
import { resetHarnessClient } from "./sdk";
import {
  dataFrame,
  jsonResponse,
  problemResponse,
  sessionSnapshot,
  sseResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";
import { streamHarnessPrompt } from "./sessions";

/**
 * Pins the wire contract of the mid-run MCP browser-authorization phase as
 * the SDK spells it: the presentation GET, the BODYLESS recheck/cancel POSTs
 * (the daemon rejects any body byte), the relayed continuation, and the
 * prompt stream that legitimately ENDS on `authorization.required`.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const base = "/v1/sessions/s1/mcp-authorizations/auth-1";
const snapshotFor = (sessionId: string) =>
  jsonResponse(200, sessionSnapshot(sessionId));

const authorization = (
  status: string,
  extra: Record<string, unknown> = {},
) => ({
  authorization_id: "auth-1",
  call_id: "call-1",
  display_name: "GitHub MCP",
  status,
  ...extra,
});

describe("fetchMcpAuthorizationUrl", () => {
  it("GETs the presentation control and returns the live URL", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === `${base}/presentation`)
        return jsonResponse(200, { url: "https://idp.example/authorize?s=1" });
      return undefined;
    });
    await expect(fetchMcpAuthorizationUrl("s1", "auth-1")).resolves.toBe(
      "https://idp.example/authorize?s=1",
    );
    const presentation = requests.find((r) => r.path.endsWith("/presentation"));
    expect(presentation?.method).toBe("GET");
    expect(presentation?.body).toBeUndefined();
  });

  it("surfaces the daemon's 404 reason verbatim on the typed error", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === `${base}/presentation`)
        return problemResponse(
          404,
          "not_found",
          "the pending MCP authorization has expired",
        );
      return undefined;
    });
    await expect(
      fetchMcpAuthorizationUrl("s1", "auth-1"),
    ).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 404,
      message: "the pending MCP authorization has expired",
    });
  });
});

describe("recheckMcpAuthorization", () => {
  it("POSTs the recheck route with NO body and relays the status plus the continuation run", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === `${base}/recheck`)
        return sseResponse([
          dataFrame({
            type: "authorization.resolved",
            run_id: "run-2",
            authorization: authorization("granted"),
          }),
          dataFrame({
            type: "message.delta",
            run_id: "run-2",
            text: "resumed",
          }),
          dataFrame({
            type: "result",
            run_id: "run-2",
            result: { stop: "end_turn", text: "resumed" },
          }),
        ]);
      return undefined;
    });
    const events: StreamEvent[] = [];
    const outcome = await recheckMcpAuthorization("s1", "auth-1", (event) =>
      events.push(event),
    );
    const recheck = requests.find((r) => r.path === `${base}/recheck`);
    expect(recheck?.method).toBe("POST");
    // The daemon reads ONE byte and 400s on any body; the request carries none.
    expect(recheck?.init?.body).toBeUndefined();
    expect(new Headers(recheck?.init?.headers).get("content-type")).toBeNull();
    expect(outcome).toEqual({ status: "granted", sawResult: true });
    expect(events).toEqual([
      {
        type: "authorization_resolved",
        authorizationId: "auth-1",
        displayName: "GitHub MCP",
        status: "granted",
        runId: "run-2",
      },
      { type: "token", text: "resumed", runId: "run-2" },
      {
        type: "run_result",
        stop: "end_turn",
        text: "resumed",
        errorText: "",
        permanent: false,
        runId: "run-2",
      },
    ]);
  });

  it("reports a still-pending sign-in from the lone status frame, without a truncation error", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === `${base}/recheck`)
        return sseResponse([
          dataFrame({
            type: "authorization.required",
            authorization: authorization("pending", {
              expires_at: "2027-01-01T00:00:00Z",
            }),
          }),
        ]);
      return undefined;
    });
    const events: StreamEvent[] = [];
    await expect(
      recheckMcpAuthorization("s1", "auth-1", (event) => events.push(event)),
    ).resolves.toEqual({ status: "pending", sawResult: false });
    expect(events).toEqual([
      {
        type: "authorization",
        authorizationId: "auth-1",
        callId: "call-1",
        displayName: "GitHub MCP",
        status: "pending",
        expiresAt: Date.parse("2027-01-01T00:00:00Z"),
      },
    ]);
  });

  it("surfaces a gone authorization as the daemon's typed 404", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === `${base}/recheck`)
        return problemResponse(
          404,
          "not_found",
          "the MCP authorization is no longer pending",
        );
      return undefined;
    });
    await expect(
      recheckMcpAuthorization("s1", "auth-1", () => undefined),
    ).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 404,
      message: "the MCP authorization is no longer pending",
    });
  });
});

describe("cancelMcpAuthorization", () => {
  it("POSTs the cancel route bodyless and reports the terminal status", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === `${base}/cancel`)
        return sseResponse([
          dataFrame({
            type: "authorization.resolved",
            authorization: authorization("cancelled"),
          }),
        ]);
      return undefined;
    });
    await expect(
      cancelMcpAuthorization("s1", "auth-1", () => undefined),
    ).resolves.toEqual({ status: "cancelled", sawResult: false });
    const cancel = requests.find((r) => r.path === `${base}/cancel`);
    expect(cancel?.method).toBe("POST");
    expect(cancel?.init?.body).toBeUndefined();
  });
});

describe("streamHarnessPrompt parked on an authorization", () => {
  it("ends normally when the stream closes on a pending authorization.required — the run is parked, not truncated", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/prompt")
        return sseResponse([
          dataFrame({ type: "turn.start", run_id: "run-1" }),
          dataFrame({
            type: "message.delta",
            run_id: "run-1",
            text: "Signing in… ",
          }),
          dataFrame({
            type: "authorization.required",
            run_id: "run-1",
            authorization: authorization("pending", {
              expires_at: "2027-01-01T00:10:00Z",
            }),
          }),
        ]);
      return undefined;
    });
    const events: StreamEvent[] = [];
    await expect(
      streamHarnessPrompt("s1", "use github", [], (event) =>
        events.push(event),
      ),
    ).resolves.toBeUndefined();
    expect(events).toEqual([
      { type: "token", text: "Signing in… ", runId: "run-1" },
      {
        type: "authorization",
        authorizationId: "auth-1",
        callId: "call-1",
        displayName: "GitHub MCP",
        status: "pending",
        expiresAt: Date.parse("2027-01-01T00:10:00Z"),
        runId: "run-1",
      },
    ]);
  });

  it("still fails loudly when the authorization was resolved on-stream and the result never came", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/prompt")
        return sseResponse([
          dataFrame({
            type: "authorization.required",
            run_id: "run-1",
            authorization: authorization("pending"),
          }),
          dataFrame({
            type: "authorization.resolved",
            run_id: "run-1",
            authorization: authorization("granted"),
          }),
        ]);
      return undefined;
    });
    await expect(
      streamHarnessPrompt("s1", "use github", [], () => undefined),
    ).rejects.toThrow("closed before Mecatl returned a final result");
  });
});

describe("control first-event bound", () => {
  it("fails a recheck whose stream produces no first event within 10 s, so the 3 s polling can recover instead of wedging", async () => {
    vi.useFakeTimers();
    try {
      // The daemon answers a recheck synchronously; a transport that drops
      // the stream (a stalled port-forward) never delivers a frame at all.
      stubHarnessFetch((request) => {
        if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
        if (request.path === `${base}/recheck`)
          // Like a real fetch, the hung request still honours its abort —
          // that is how the guard tears the SDK stream down afterwards.
          return new Promise<Response>((_, reject) => {
            request.init?.signal?.addEventListener(
              "abort",
              () =>
                reject(
                  new DOMException("The operation was aborted.", "AbortError"),
                ),
              { once: true },
            );
          });
        return undefined;
      });
      const started = Date.now();
      let settledAt: number | null = null;
      const outcome = recheckMcpAuthorization("s1", "auth-1", () => undefined)
        .then(() => "resolved")
        .catch((error: Error) => error.message)
        .finally(() => {
          settledAt = Date.now();
        });
      // The client probe, the session GET and the stream open each take a
      // macrotask hop before the first-event timer is even armed, so the
      // clock is stepped until the control settles (bounded well under the
      // 120 s idle bound, which must NOT be what fires).
      for (let step = 0; step < 30 && settledAt === null; step++) {
        await vi.advanceTimersByTimeAsync(1_000);
      }
      await expect(outcome).resolves.toBe(
        "Mecatl did not answer the authorization check within 10 seconds.",
      );
      expect(settledAt).not.toBeNull();
      const elapsed = (settledAt ?? started) - started;
      expect(elapsed).toBeGreaterThanOrEqual(CONTROL_FIRST_EVENT_TIMEOUT_MS);
      expect(elapsed).toBeLessThan(30_000);
    } finally {
      vi.useRealTimers();
    }
  });
});
