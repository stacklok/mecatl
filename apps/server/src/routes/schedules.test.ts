// SPDX-License-Identifier: Apache-2.0

import type { ScheduleResponse } from "@mecatl-studio/contracts";
import { beforeEach, describe, expect, it } from "vitest";
import { createApp } from "../app";
import { type ScheduleService, ScheduleTriggerImmutableError } from "../mecatl/schedules";
import { csrfHeaders } from "../testing/fakes";

const example: ScheduleResponse = {
  enabled: true,
  fireCount: 2,
  lastFireAt: "2026-09-15T09:00:00.000Z",
  lastFireSessionId: "session-1",
  maxFires: 0,
  mode: "plan",
  modelId: "",
  mutating: false,
  name: "daily-summary",
  nextFireAt: "2026-09-16T09:00:00.000Z",
  oneShotMaxRetries: 0,
  oneShotRetry: false,
  owner: "",
  profile: "all",
  prompt: "Summarize the day",
  providerId: "",
  status: "scheduled",
  trigger: { expression: "0 9 * * *", kind: "cron", timezone: "Europe/Rome" },
};

const calls: string[] = [];
const schedules: ScheduleService = {
  supported: true,
  async create() {
    calls.push("create");
    return example;
  },
  async delete() {
    calls.push("delete");
  },
  async fire() {
    calls.push("fire");
  },
  async list() {
    return { items: [example], reason: "", supported: true };
  },
  async listFires() {
    return { items: [] };
  },
  async pause() {
    calls.push("pause");
  },
  async resume() {
    calls.push("resume");
  },
  async update() {
    calls.push("update");
    return example;
  },
};

const app = createApp({ schedules });

const mutating = (body?: unknown, method = "POST") => ({
  ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  headers: csrfHeaders("t", { "Content-Type": "application/json" }),
  method,
});

describe("schedule routes", () => {
  beforeEach(() => calls.splice(0));

  it("lists normalized schedules", async () => {
    const response = await app.request("/api/v1/schedules");
    expect(response.status).toBe(200);
    expect(await response.json()).toMatchObject({ items: [{ name: "daily-summary" }] });
  });

  it("routes lifecycle actions", async () => {
    for (const action of ["fire", "pause", "resume"] as const) {
      const response = await app.request(
        "/api/v1/schedules/daily-summary/actions",
        mutating({ action }),
      );
      expect(response.status).toBe(204);
    }
    const removed = await app.request(
      "/api/v1/schedules/daily-summary",
      mutating(undefined, "DELETE"),
    );
    expect(removed.status).toBe(204);
    expect(calls).toEqual(["fire", "pause", "resume", "delete"]);
  });

  it("answers 409 schedule_trigger_immutable when an update changes the trigger", async () => {
    const refusing = createApp({
      schedules: {
        ...schedules,
        async update(name) {
          calls.push("update");
          throw new ScheduleTriggerImmutableError(name);
        },
      },
    });
    const { name: _name, ...body } = example;
    const response = await refusing.request(
      "/api/v1/schedules/daily-summary",
      mutating({ ...body, trigger: { at: "2027-01-15T08:00:00.000Z", kind: "once" } }, "PUT"),
    );
    expect(response.status).toBe(409);
    expect(response.headers.get("Content-Type")).toContain("application/problem+json");
    expect(await response.json()).toMatchObject({
      code: "schedule_trigger_immutable",
      detail: expect.stringContaining("create a new schedule"),
      status: 409,
    });
  });

  it("still applies an update that keeps the trigger", async () => {
    const { name: _name, ...body } = example;
    const response = await app.request(
      "/api/v1/schedules/daily-summary",
      mutating({ ...body, prompt: "Summarize the week" }, "PUT"),
    );
    expect(response.status).toBe(200);
    expect(calls).toEqual(["update"]);
  });

  it("refuses an unknown cron time zone with a 400 problem before reaching Mecatl", async () => {
    const { name: _name, ...body } = example;
    const trigger = { expression: "0 9 * * *", kind: "cron", timezone: "Mars/Olympus" };
    for (const [path, init] of [
      ["/api/v1/schedules", mutating({ ...body, name: "x", trigger })],
      ["/api/v1/schedules/daily-summary", mutating({ ...body, trigger }, "PUT")],
    ] as const) {
      const response = await app.request(path, init);
      expect(response.status).toBe(400);
      expect(await response.json()).toMatchObject({
        code: "invalid_schedule",
        detail: expect.stringContaining("Mars/Olympus"),
      });
    }
    expect(calls).toEqual([]);
  });

  it("accepts an empty time zone and a one-shot instant with a numeric offset", async () => {
    const { name: _name, ...body } = example;
    for (const trigger of [
      { expression: "0 9 * * *", kind: "cron", timezone: "" },
      { at: "2027-01-15T10:00:00+02:00", kind: "once" },
    ]) {
      const response = await app.request(
        "/api/v1/schedules",
        mutating({ ...body, name: "x", trigger }),
      );
      expect(response.status, JSON.stringify(trigger)).toBe(201);
    }
    expect(calls).toEqual(["create", "create"]);
  });

  it("capability-disables unsupported mutations", async () => {
    const unsupported = createApp({ schedules: { ...schedules, supported: false } });
    const response = await unsupported.request(
      "/api/v1/schedules/daily-summary",
      mutating(undefined, "DELETE"),
    );
    expect(response.status).toBe(501);
    expect(await response.json()).toMatchObject({ code: "schedule_unsupported" });

    const list = await unsupported.request("/api/v1/schedules");
    expect(list.status).toBe(200);
    for (const [path, init] of [
      ["/api/v1/schedules", mutating({ ...example, name: "x" })],
      ["/api/v1/schedules/daily-summary", mutating(example, "PUT")],
      ["/api/v1/schedules/daily-summary/actions", mutating({ action: "fire" })],
      ["/api/v1/schedules/daily-summary/fires", undefined],
    ] as const) {
      const blocked = await unsupported.request(path, init);
      expect(blocked.status).toBe(501);
    }
  });

  it("answers 503 runtime_unavailable on every schedules route without a runtime", async () => {
    const detached = createApp();
    for (const [path, init] of [
      ["/api/v1/schedules", undefined],
      ["/api/v1/schedules", mutating({ ...example, name: "x" })],
      ["/api/v1/schedules/daily-summary", mutating(example, "PUT")],
      ["/api/v1/schedules/daily-summary/actions", mutating({ action: "pause" })],
      ["/api/v1/schedules/daily-summary", mutating(undefined, "DELETE")],
      ["/api/v1/schedules/daily-summary/fires", undefined],
    ] as const) {
      const response = await detached.request(path, init);
      expect(response.status).toBe(503);
      await expect(response.json()).resolves.toMatchObject({ code: "runtime_unavailable" });
    }
  });

  it("schedule mutations require the CSRF pair and a session when interactive login is active", async () => {
    for (const [path, init] of [
      ["/api/v1/schedules", { body: "{}", method: "POST" }],
      ["/api/v1/schedules/daily-summary", { body: "{}", method: "PUT" }],
      ["/api/v1/schedules/daily-summary/actions", { body: "{}", method: "POST" }],
      ["/api/v1/schedules/daily-summary", { method: "DELETE" }],
    ] as const) {
      const response = await app.request(path, {
        ...init,
        headers: { "Content-Type": "application/json" },
      });
      expect(response.status).toBe(403);
      await expect(response.json()).resolves.toMatchObject({ code: "cross_site_request" });
    }
    const gated = createApp({
      authentication: {
        clear: () => undefined,
        completeLogin: async () => {
          throw new Error("unused");
        },
        credential: async () => ({ status: "anonymous" }),
        logout: async () => undefined,
        noteLoginComplete: () => undefined,
        noteLoginFailure: () => undefined,
        save: async () => undefined,
        startLogin: async () => "https://issuer.example.com/authorize",
      },
      schedules,
    });
    const list = await gated.request("/api/v1/schedules");
    expect(list.status).toBe(401);
    const fire = await gated.request(
      "/api/v1/schedules/daily-summary/actions",
      mutating({ action: "fire" }),
    );
    expect(fire.status).toBe(401);
    expect(calls).toEqual([]);
  });
});
