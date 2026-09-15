import { afterEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "./errors";
import { resetHarnessClient } from "./sdk";
import {
  dataFrame,
  jsonResponse,
  problemResponse,
  sessionSnapshot,
  sseResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";
import {
  type WatchDelivery,
  WatchStreamError,
  watchSessionEvents,
} from "./watch";

/**
 * Pins the ADR-0250 watch client over the SDK's `session.activity()`:
 * envelope delivery with cursor tracking and the replay→live boundary, the
 * typed terminal faults (`activity_gap`, `cursor_expired`, and a pre-stream
 * refusal as HarnessApiError), and the cursor round-trip on resume.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const faultFrame = (code: string, error: string) =>
  `event: error\n${dataFrame({ code, error })}`;

describe("watchSessionEvents", () => {
  it("delivers replay frames, the live boundary, and tracks the cursor", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s-1")
        return jsonResponse(200, sessionSnapshot("s-1"));
      if (request.path.startsWith("/v1/sessions/s-1/watch"))
        return sseResponse([
          dataFrame({
            event: { type: "user_prompt", user_prompt: { text: "hello" } },
            cursor: "c-1",
            phase: "replay",
          }),
          dataFrame({
            event: { type: "message.delta", text: "hi", run_id: "run-1" },
            cursor: "c-2",
            phase: "replay",
          }),
          // The silent lifecycle kind still advances the cursor (null event).
          dataFrame({
            event: { type: "turn.start" },
            cursor: "c-3",
            phase: "replay",
          }),
          dataFrame({ cursor: "c-3", phase: "live" }),
        ]);
      return undefined;
    });
    const controller = new AbortController();
    const deliveries: WatchDelivery[] = [];
    await watchSessionEvents(
      "s-1",
      (delivery) => {
        deliveries.push(delivery);
        // The boundary is the natural detach point for this test.
        if (delivery.phase === "live" && !delivery.event) controller.abort();
      },
      { signal: controller.signal },
    );
    expect(deliveries.map(({ phase, event }) => ({ phase, event }))).toEqual([
      { phase: "replay", event: { type: "user_prompt", text: "hello" } },
      { phase: "replay", event: { type: "token", text: "hi", runId: "run-1" } },
      { phase: "replay", event: null },
      { phase: "live", event: null },
    ]);
    // Cursors are opaque SDK resume tokens: present, and stable across the
    // event-less frame that shares the server cursor with the boundary.
    expect(deliveries.every((d) => d.cursor.length > 0)).toBe(true);
    expect(deliveries[2].cursor).toBe(deliveries[3].cursor);
    expect(deliveries[0].cursor).not.toBe(deliveries[1].cursor);
    const watch = requests.find((r) =>
      r.path.startsWith("/v1/sessions/s-1/watch"),
    );
    expect(watch?.url).toBe("/api/mecatl/v1/sessions/s-1/watch");
  });

  it("resumes from a delivered cursor, handing the server its own token back", async () => {
    let watches = 0;
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s-2")
        return jsonResponse(200, sessionSnapshot("s-2"));
      if (request.path.startsWith("/v1/sessions/s-2/watch")) {
        watches += 1;
        return watches === 1
          ? sseResponse([
              dataFrame({
                event: { type: "message.delta", text: "a", run_id: "run-1" },
                cursor: "c-7",
                phase: "replay",
              }),
              dataFrame({ cursor: "c-7", phase: "live" }),
            ])
          : sseResponse([dataFrame({ cursor: "c-7", phase: "live" })]);
      }
      return undefined;
    });
    const first = new AbortController();
    let resumeCursor = "";
    await watchSessionEvents(
      "s-2",
      (delivery) => {
        resumeCursor = delivery.cursor;
        if (delivery.phase === "live") first.abort();
      },
      { signal: first.signal },
    );
    expect(resumeCursor).not.toBe("");
    const second = new AbortController();
    await watchSessionEvents("s-2", () => second.abort(), {
      cursor: resumeCursor,
      signal: second.signal,
    });
    const resumed = requests.filter((r) =>
      r.path.startsWith("/v1/sessions/s-2/watch"),
    )[1];
    expect(resumed.url).toContain("cursor=c-7");
  });

  it("throws a typed WatchStreamError on activity_gap — the transcript-refetch signal", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s-3")
        return jsonResponse(200, sessionSnapshot("s-3"));
      if (request.path.startsWith("/v1/sessions/s-3/watch"))
        return sseResponse([
          dataFrame({
            event: { type: "message.delta", text: "a", run_id: "run-1" },
            cursor: "c-1",
            phase: "replay",
          }),
          faultFrame("activity_gap", "append failed"),
        ]);
      return undefined;
    });
    const deliveries: WatchDelivery[] = [];
    await expect(
      watchSessionEvents("s-3", (delivery) => deliveries.push(delivery)),
    ).rejects.toMatchObject({ name: "WatchStreamError", code: "activity_gap" });
    expect(deliveries).toHaveLength(1);
  });

  it("throws a typed WatchStreamError on cursor_expired", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s-4")
        return jsonResponse(200, sessionSnapshot("s-4"));
      if (request.path.startsWith("/v1/sessions/s-4/watch"))
        return sseResponse([faultFrame("cursor_expired", "superseded")]);
      return undefined;
    });
    const fault = await watchSessionEvents("s-4", () => undefined).catch(
      (caught) => caught,
    );
    expect(fault).toBeInstanceOf(WatchStreamError);
    expect((fault as WatchStreamError).code).toBe("cursor_expired");
  });

  it("rejects a cursor this SDK never issued as cursor_malformed, before any request", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s-5")
        return jsonResponse(200, sessionSnapshot("s-5"));
      return undefined;
    });
    const fault = await watchSessionEvents("s-5", () => undefined, {
      cursor: "not-an-sdk-cursor!",
    }).catch((caught) => caught);
    expect(fault).toBeInstanceOf(WatchStreamError);
    expect((fault as WatchStreamError).code).toBe("cursor_malformed");
    expect(requests.some((r) => r.path.includes("/watch"))).toBe(false);
  });

  it("surfaces a pre-stream refusal as the typed HarnessApiError", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s-6")
        return jsonResponse(200, sessionSnapshot("s-6"));
      if (request.path.startsWith("/v1/sessions/s-6/watch"))
        return problemResponse(
          501,
          "no_event_log",
          "no durable event log configured",
        );
      return undefined;
    });
    const fault = await watchSessionEvents("s-6", () => undefined).catch(
      (caught) => caught,
    );
    expect(fault).toBeInstanceOf(HarnessApiError);
    expect((fault as HarnessApiError).code).toBe("no_event_log");
  });

  it("refuses to attach when the daemon lacks the watch feature", async () => {
    stubHarnessFetch(
      (request) =>
        request.path === "/v1/sessions/s-7"
          ? jsonResponse(200, sessionSnapshot("s-7"))
          : undefined,
      { features: ["http_steer"] },
    );
    const fault = await watchSessionEvents("s-7", () => undefined).catch(
      (caught) => caught,
    );
    expect(fault).toBeInstanceOf(HarnessApiError);
    expect((fault as HarnessApiError).code).toBe("unsupported_feature");
  });
});
