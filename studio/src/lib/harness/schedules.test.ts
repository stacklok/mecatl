import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ScheduleCarriedSpec, ScheduleSpecDraft } from "@/lib/protocol";
import { HarnessApiError } from "./errors";
import {
  harnessScheduleAction,
  listScheduleFires,
  listScheduleRows,
  saveHarnessSchedule,
} from "./schedules";
import { resetHarnessClient } from "./sdk";

/**
 * Pins the wire the schedule module drives through the SDK: the routes, the
 * methods, and the spec body the SDK serialises (snake_case protojson — an
 * RFC 3339 one-shot, a "5s" fire timeout, integer enums) and the responses it
 * decodes (the daemon marshals with Go stdlib encoding/json, so Timestamps and
 * Durations arrive as `{seconds, nanos}` objects). The daemon is a stubbed
 * `fetch`; the compatibility probe the SDK issues first is answered as an
 * API-major-1 build.
 */

type Recorded = { method: string; path: string; body: unknown };

const recorded: Recorded[] = [];
let routes: Record<string, (recordedCall: Recorded) => Response>;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubFetch(): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(
        typeof input === "string" || input instanceof URL
          ? String(input)
          : input.url,
        "http://studio.test",
      );
      const method = (init?.method ?? "GET").toUpperCase();
      const body =
        typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      const call = { method, path: url.pathname, body };
      recorded.push(call);
      if (url.pathname === "/api/mecatl/v1/compatibility") {
        return json({ api_major: 1, features: [] });
      }
      const route = routes[`${method} ${url.pathname}`];
      if (route) return route(call);
      return json({ error: "no route", code: "not_found" }, 404);
    }),
  );
}

const cronDraft: ScheduleSpecDraft = {
  name: "nightly digest",
  prompt: "summarise the day",
  trigger: { kind: "cron", cron: "0 9 * * *", timezone: "Europe/London" },
  profile: "",
  mode: 2,
  mutating: false,
  maxFires: 30,
  limits: { maxTurns: 8, maxToolCalls: 40, maxConsecutiveFailures: 3 },
  oneShotRetry: false,
  oneShotMaxRetries: 0,
};

const carried: ScheduleCarriedSpec = {
  selectorProvider: "openrouter",
  selectorModel: "big-1",
  misfire: 2,
  singleton: true,
  carryContext: true,
  fireTimeoutSeconds: 5,
  parts: [],
};

/** Only the schedule calls — the SDK's own compatibility probe is not the subject. */
function scheduleCalls(): Recorded[] {
  return recorded.filter((call) => call.path.includes("/v1/schedules"));
}

describe("harness schedules over the SDK", () => {
  beforeEach(() => {
    recorded.length = 0;
    routes = {};
    stubFetch();
  });

  afterEach(async () => {
    await resetHarnessClient();
    vi.unstubAllGlobals();
  });

  it("create POSTs the spec body to /v1/schedules as snake_case protojson", async () => {
    routes["POST /api/mecatl/v1/schedules"] = () => json({});

    await saveHarnessSchedule(cronDraft, { update: false });

    const [call] = scheduleCalls();
    expect(call.method).toBe("POST");
    expect(call.path).toBe("/api/mecatl/v1/schedules");
    expect(call.body).toEqual({
      name: "nightly digest",
      prompt: "summarise the day",
      trigger: { cron: "0 9 * * *" },
      timezone: "Europe/London",
      max_fires: 30,
      mode: 2,
      limits: { max_turns: 8, max_tool_calls: 40, max_consecutive_failures: 3 },
    });
    // The body is the bare spec, not a {spec: …} envelope.
    expect(call.body).not.toHaveProperty("spec");
  });

  it("encodes a one-shot as RFC 3339 and a carried fire timeout as a Duration string", async () => {
    routes["POST /api/mecatl/v1/schedules"] = () => json({});

    await saveHarnessSchedule(
      {
        ...cronDraft,
        name: "once",
        trigger: { kind: "one-shot", at: Date.parse("2026-08-20T09:00:00Z") },
        oneShotRetry: true,
        oneShotMaxRetries: 3,
      },
      { update: false, carried },
    );

    const [call] = scheduleCalls();
    expect(call.body).toMatchObject({
      name: "once",
      trigger: { one_shot: "2026-08-20T09:00:00Z" },
      one_shot_retry: true,
      one_shot_max_retries: 3,
      fire_timeout: "5s",
      selector: { provider_id: "openrouter", model_id: "big-1" },
      misfire: 2,
      singleton: true,
      carry_context: true,
    });
    // Cron-only fields stay off a one-shot; server-owned fields never ride.
    expect(call.body).not.toHaveProperty("max_fires");
    expect(call.body).not.toHaveProperty("timezone");
    expect(call.body).not.toHaveProperty("owner");
    expect(call.body).not.toHaveProperty("created_at");
  });

  it("update PUTs the spec to /v1/schedules/{name}, carrying the stored fields", async () => {
    routes["PUT /api/mecatl/v1/schedules/nightly%20digest"] = () => json({});

    await saveHarnessSchedule(cronDraft, { update: true, carried });

    const [call] = scheduleCalls();
    expect(call.method).toBe("PUT");
    expect(call.path).toBe("/api/mecatl/v1/schedules/nightly%20digest");
    expect(call.body).toMatchObject({
      name: "nightly digest",
      fire_timeout: "5s",
      singleton: true,
      misfire: 2,
    });
  });

  it("fire, pause, resume and delete hit their routes", async () => {
    routes["POST /api/mecatl/v1/schedules/job%2F1/fire"] = () =>
      json({ fire_id: "f1", session_id: "s1" });
    routes["POST /api/mecatl/v1/schedules/job%2F1/pause"] = () => json({});
    routes["POST /api/mecatl/v1/schedules/job%2F1/resume"] = () => json({});
    routes["DELETE /api/mecatl/v1/schedules/job%2F1"] = () => json({});

    await harnessScheduleAction("job/1", "fire");
    await harnessScheduleAction("job/1", "pause");
    await harnessScheduleAction("job/1", "resume");
    await harnessScheduleAction("job/1", "delete");

    expect(
      scheduleCalls().map((call) => `${call.method} ${call.path}`),
    ).toEqual([
      "POST /api/mecatl/v1/schedules/job%2F1/fire",
      "POST /api/mecatl/v1/schedules/job%2F1/pause",
      "POST /api/mecatl/v1/schedules/job%2F1/resume",
      "DELETE /api/mecatl/v1/schedules/job%2F1",
    ]);
    for (const call of scheduleCalls()) expect(call.body).toBeUndefined();
  });

  it("list decodes the registry into rows", async () => {
    routes["GET /api/mecatl/v1/schedules"] = () =>
      json({
        schedules: [
          {
            spec: {
              name: "nightly",
              prompt: "p",
              trigger: { cron: "0 9 * * *" },
              mode: 2,
              fire_timeout: { seconds: 900 },
              selector: { provider_id: "openrouter", model_id: "big-1" },
            },
            state: {
              enabled: true,
              fire_count: 2,
              next_fire_at: { seconds: 1755680400 },
              last_fire_session_id: "pending",
            },
          },
        ],
      });

    const rows = await listScheduleRows();

    expect(scheduleCalls()).toEqual([
      { method: "GET", path: "/api/mecatl/v1/schedules", body: undefined },
    ]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      name: "nightly",
      cron: "0 9 * * *",
      mode: 2,
      enabled: true,
      fireCount: 2,
      fireStage: "claimed",
      lastFireSessionId: "",
      nextFireAt: 1755680400000,
    });
    expect(rows[0].carried).toMatchObject({
      selectorProvider: "openrouter",
      selectorModel: "big-1",
      fireTimeoutSeconds: 900,
    });
  });

  it("surfaces a 501 on list as a HarnessApiError with that status", async () => {
    routes["GET /api/mecatl/v1/schedules"] = () =>
      json({ error: "scheduler not wired", code: "unsupported_feature" }, 501);

    const failure = await listScheduleRows().catch((error) => error);

    expect(failure).toBeInstanceOf(HarnessApiError);
    expect((failure as HarnessApiError).status).toBe(501);
  });

  it("listFires reads /v1/schedules/{name}/fires and orders newest first", async () => {
    routes["GET /api/mecatl/v1/schedules/nightly/fires"] = () =>
      json({
        fires: [
          {
            id: "f-old",
            schedule_name: "nightly",
            session_id: "s-old",
            fired_at: { seconds: 1755594000 },
            stop: "end_turn",
          },
          {
            id: "f-new",
            schedule_name: "nightly",
            session_id: "s-new",
            fired_at: { seconds: 1755680400 },
            started_at: { seconds: 1755680401, nanos: 500000000 },
          },
        ],
      });

    const fires = await listScheduleFires("nightly");

    expect(scheduleCalls().map((call) => call.path)).toEqual([
      "/api/mecatl/v1/schedules/nightly/fires",
    ]);
    expect(fires.map((fire) => fire.id)).toEqual(["f-new", "f-old"]);
    expect(fires[0].inFlight).toBe(true);
    expect(fires[0].startedAt).toBe(1755680401500);
    expect(fires[1].inFlight).toBe(false);
  });
});
