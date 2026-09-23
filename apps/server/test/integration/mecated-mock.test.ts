// SPDX-License-Identifier: Apache-2.0

/**
 * End-to-end over the REAL SDK: the BFF boots in mock mode, the SDK spawns
 * `mecated --mock`, and every product surface is exercised through the Hono
 * app exactly as the browser would. No fakes below the BFF.
 *
 * The daemon binary comes from STUDIO_MECATED_BIN, defaulting to the
 * repository's bin/mecated (`task build`). A direct run skips, loudly, when it
 * is absent; `task studio:test:integration` sets STUDIO_REQUIRE_MECATED=1, so
 * there — and in CI — a missing binary fails instead of passing vacuously.
 *
 * The spawned daemon inherits this process's environment, so the suite points
 * every XDG directory and HOME at a test-owned directory (no operator
 * settings, MCP servers, or state leak in) and sets DO_NOT_TRACK.
 */
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createApp } from "../../src/app.js";
import { type Bootstrapped, bootstrap } from "../../src/bootstrap.js";
import { silentLogger } from "../../src/log.js";
import { createMecatlChatService } from "../../src/mecatl/chat.js";

const binary =
  process.env.STUDIO_MECATED_BIN ?? resolve(import.meta.dirname, "../../../../bin/mecated");
const available = existsSync(binary);
if (!available) {
  if (process.env.STUDIO_REQUIRE_MECATED === "1") {
    throw new Error(`integration suite requires a mecated binary at ${binary}`);
  }
  console.warn(`integration suite skipped: no mecated binary at ${binary} (run \`task build\`)`);
}

const isolatedVariables = [
  "HOME",
  "XDG_CACHE_HOME",
  "XDG_CONFIG_HOME",
  "XDG_DATA_HOME",
  "XDG_STATE_HOME",
  "DO_NOT_TRACK",
] as const;

const csrf = {
  Cookie: "studio_csrf=t",
  "Sec-Fetch-Site": "same-origin",
  "X-Studio-CSRF": "t",
  "Content-Type": "application/json",
};

describe.skipIf(!available)("Studio BFF against a spawned mecated --mock", () => {
  let booted: Bootstrapped;
  const request = (path: string, init?: RequestInit) => booted.app.request(path, init);
  const json = async (path: string, init?: RequestInit) => {
    const response = await request(path, init);
    return { body: (await response.json()) as Record<string, unknown>, status: response.status };
  };

  let home = "";
  const saved = new Map<string, string | undefined>();

  beforeAll(async () => {
    home = mkdtempSync(join(tmpdir(), "studio-integration-"));
    for (const variable of isolatedVariables) saved.set(variable, process.env[variable]);
    process.env.HOME = home;
    process.env.XDG_CACHE_HOME = join(home, "cache");
    process.env.XDG_CONFIG_HOME = join(home, "config");
    process.env.XDG_DATA_HOME = join(home, "data");
    process.env.XDG_STATE_HOME = join(home, "state");
    process.env.DO_NOT_TRACK = "1";
    booted = await bootstrap({
      environment: { MECATED_BIN: binary, MECATL_DEV_MOCK: "1", STUDIO_LOG_LEVEL: "error" },
      logger: silentLogger,
    });
    await booted.runtime.ready();
  });

  afterAll(async () => {
    await booted?.runtime.close();
    for (const [variable, value] of saved) {
      if (value === undefined) delete process.env[variable];
      else process.env[variable] = value;
    }
    if (home !== "") rmSync(home, { force: true, recursive: true });
  });

  it("boots the BFF against a spawned mecated mock and reports the negotiated runtime", async () => {
    expect((await request("/api/health")).status).toBe(200);
    const runtime = await json("/api/v1/runtime");
    expect(runtime.status).toBe(200);
    expect(runtime.body).toMatchObject({
      apiMajor: 1,
      connection: "online",
      mock: true,
      source: "local",
    });
    expect(Object.keys(runtime.body.capabilities as object).length).toBeGreaterThan(10);
    await expect(json("/api/v1/auth/session")).resolves.toMatchObject({
      body: { mode: "none", status: "disabled" },
    });
    expect(booted.runtime.authMode).toBe("none");
  });

  it("creates a session, streams a run over SSE, and reads the transcript back", async () => {
    const created = await json("/api/v1/sessions", {
      body: JSON.stringify({ mode: "default", reasoningEffort: "default", toolAccess: "all" }),
      headers: csrf,
      method: "POST",
    });
    expect(created.status).toBe(201);
    const id = created.body.id as string;
    expect(id).toMatch(/^[0-9a-f]{16,}$/);

    const listed = await json("/api/v1/sessions");
    expect((listed.body.items as Array<{ id: string }>).some((item) => item.id === id)).toBe(true);

    const run = await request(`/api/v1/sessions/${id}/runs`, {
      body: JSON.stringify({ images: [], prompt: "Say hello in one word." }),
      headers: csrf,
      method: "POST",
    });
    expect(run.status).toBe(200);
    expect(run.headers.get("content-type")).toContain("text/event-stream");
    const frames = await run.text();
    const types = [...frames.matchAll(/^event: (.+)$/gmu)].map((m) => m[1]);
    expect(types[0]).toBe("run.started");
    expect(types).toContain("run.event");
    expect(types).not.toContain("run.error");
    const kinds = new Set([...frames.matchAll(/"kind":"([a-z._]+)"/gu)].map((m) => m[1]));
    for (const kind of ["session.init", "turn.start", "message.delta", "turn.end", "result"]) {
      expect(kinds, `missing ${kind}`).toContain(kind);
    }
    expect(frames).not.toContain('"unknown":true');

    const transcript = await json(`/api/v1/sessions/${id}/transcript`);
    expect(transcript.status).toBe(200);
    const roles = (transcript.body.messages as Array<{ role: string }>).map((m) => m.role);
    expect(roles).toEqual(["user", "assistant"]);

    const detail = await json(`/api/v1/sessions/${id}`);
    expect(detail.body).toMatchObject({ id, mode: "default", state: "completed" });

    const renamed = await json(`/api/v1/sessions/${id}`, {
      body: JSON.stringify({ title: "Integration chat" }),
      headers: csrf,
      method: "PATCH",
    });
    expect(renamed).toEqual({ body: { title: "Integration chat" }, status: 200 });

    const removed = await request(`/api/v1/sessions/${id}`, { headers: csrf, method: "DELETE" });
    expect(removed.status).toBe(204);
  });

  it("answers 409 stale_run_control for a control on an unknown run", async () => {
    const created = await json("/api/v1/sessions", {
      body: JSON.stringify({ mode: "default", reasoningEffort: "default", toolAccess: "all" }),
      headers: csrf,
      method: "POST",
    });
    const id = created.body.id as string;
    const stale = await json(`/api/v1/sessions/${id}/runs/run-does-not-exist/cancel`, {
      headers: csrf,
      method: "POST",
    });
    expect(stale).toMatchObject({ body: { code: "stale_run_control" }, status: 409 });
    const steer = await json(`/api/v1/sessions/${id}/runs/run-does-not-exist/steer`, {
      body: JSON.stringify({ text: "stop" }),
      headers: csrf,
      method: "POST",
    });
    expect(steer.status).toBe(409);
  });

  it("reports scheduling unsupported honestly when the daemon has it off", async () => {
    const runtime = await json("/api/v1/runtime");
    const scheduling = (runtime.body.capabilities as { scheduling: boolean }).scheduling;
    const list = await json("/api/v1/schedules");
    expect(list.status).toBe(200);
    expect(list.body.supported).toBe(scheduling);
    if (!scheduling) {
      expect(list.body).toMatchObject({ items: [], supported: false });
      expect(list.body.reason).not.toBe("");
      const create = await json("/api/v1/schedules", {
        body: JSON.stringify({
          maxFires: 0,
          mode: "plan",
          mutating: false,
          name: "integration-daily",
          oneShotMaxRetries: 0,
          oneShotRetry: false,
          profile: "all",
          prompt: "Summarize the day",
          trigger: { expression: "0 9 * * *", kind: "cron", timezone: "UTC" },
        }),
        headers: csrf,
        method: "POST",
      });
      expect(create).toMatchObject({ body: { code: "schedule_unsupported" }, status: 501 });
    }
  });

  it("lists knowledge inventories and generates a memory consolidation plan", async () => {
    for (const path of [
      "/api/v1/skills",
      "/api/v1/learned-skills",
      "/api/v1/learned-skills/changes",
      "/api/v1/learning-proposals?status=staged",
      "/api/v1/user-memory",
    ]) {
      const response = await json(path);
      expect(response.status, path).toBe(200);
      expect(Array.isArray(response.body.items), path).toBe(true);
    }
    const missing = await json("/api/v1/user-memory/does-not-exist");
    expect(missing).toMatchObject({ body: { code: "not_found" }, status: 404 });

    const runtime = await json("/api/v1/runtime");
    const dream = (
      runtime.body.capabilities as { manualDream?: { userModel?: { generate: boolean } } }
    ).manualDream?.userModel;
    const plan = await json("/api/v1/user-memory/consolidation/plans", {
      headers: csrf,
      method: "POST",
    });
    if (dream?.generate) {
      expect(plan.status).toBe(201);
      expect(plan.body).toMatchObject({ target: "user_model" });
      expect(typeof plan.body.id).toBe("string");
    } else {
      expect(plan).toMatchObject({
        body: { code: "memory_consolidation_unsupported" },
        status: 501,
      });
    }
  });

  it("serves the runtime settings allowlist and the storage health", async () => {
    const settings = await json("/api/v1/settings/runtime");
    expect(settings.status).toBe(200);
    expect(Object.keys(settings.body).sort()).toEqual([
      "buildId",
      "management",
      "models",
      "modelsReason",
      "modelsSupported",
      "providerEndpoint",
      "providers",
      "serverImplementation",
    ]);
    expect(settings.body.management).toMatchObject({
      providerConfiguration: false,
      routingConfiguration: false,
    });
    const health = await json("/api/v1/storage/health");
    expect(health.status).toBe(200);
    expect(typeof health.body.supported).toBe("boolean");
    expect(typeof health.body.sessionCount).toBe("string");
  });

  it("refuses cross-site mutations and serves no CORS headers on the real app", async () => {
    const blocked = await json("/api/v1/sessions", {
      body: "{}",
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    expect(blocked).toMatchObject({ body: { code: "cross_site_request" }, status: 403 });
    const response = await request("/api/v1/runtime", {
      headers: { Origin: "https://evil.example" },
    });
    expect(response.headers.get("access-control-allow-origin")).toBeNull();
    expect(response.headers.get("content-security-policy")).toContain("default-src 'self'");
  });
  it("bounds a durable replay and resumes from its cursor without duplication", async () => {
    // A real session with real durable history, replayed through a BFF whose
    // bound is deliberately tiny. Everything below the route is real: the SDK,
    // the spawned daemon, and its event log.
    const created = await json("/api/v1/sessions", {
      body: JSON.stringify({ mode: "default", reasoningEffort: "default", toolAccess: "all" }),
      headers: csrf,
      method: "POST",
    });
    const id = created.body.id as string;
    const run = await request(`/api/v1/sessions/${id}/runs`, {
      body: JSON.stringify({ images: [], prompt: "Say hello in one word." }),
      headers: csrf,
      method: "POST",
    });
    await run.text();

    const bounded = createApp({
      activity: { maxStreams: 4, replayMax: 2 },
      chat: createMecatlChatService(booted.runtime.client),
      runtime: booted.runtime,
    });
    const frames = (body: string) =>
      body
        .split("\n\n")
        .filter((block) => block.includes("event:"))
        .map((block) => ({
          id: /^id: (.*)$/mu.exec(block)?.[1],
          type: /^event: (.*)$/mu.exec(block)?.[1],
          data: JSON.parse(/^data: (.*)$/mu.exec(block)?.[1] ?? "{}") as Record<string, unknown>,
        }));

    const first = await bounded.request(`/api/v1/sessions/${id}/activity`);
    expect(first.status).toBe(200);
    const firstFrames = frames(await first.text());

    // Exactly two durable events, then the truncation frame naming the bound.
    const firstEvents = firstFrames.filter((frame) => frame.type === "run.event");
    expect(firstEvents).toHaveLength(2);
    const truncated = firstFrames.at(-1);
    expect(truncated?.type).toBe("run.truncated");
    expect(truncated?.data).toMatchObject({ reason: "bound" });
    const resumeFrom = truncated?.data.cursor as string;
    expect(resumeFrom).toBeTruthy();
    // Every event frame carries its durable cursor as the SSE id, and the
    // truncation frame points at the last one delivered.
    const firstCursors = firstEvents.map((frame) => frame.id);
    expect(firstCursors.every((cursor) => typeof cursor === "string" && cursor.length > 0)).toBe(
      true,
    );
    expect(firstCursors.at(-1)).toBe(resumeFrom);

    const resumed = await bounded.request(`/api/v1/sessions/${id}/activity`, {
      headers: { "Last-Event-ID": resumeFrom },
    });
    expect(resumed.status).toBe(200);
    const resumedFrames = frames(await resumed.text());
    const resumedCursors = resumedFrames
      .filter((frame) => frame.type === "run.event")
      .map((frame) => frame.id);

    // Exact resume: the first frame is the next durable event, and nothing
    // already delivered comes back.
    expect(resumedFrames[0]?.type).toBe("run.event");
    expect(resumedCursors.length).toBeGreaterThan(0);
    for (const cursor of resumedCursors) expect(firstCursors).not.toContain(cursor);

    // Walking the cursors to the end reaches the run's terminal result without
    // ever replaying the whole history in one request.
    let cursor = resumeFrom;
    const walked = [...firstCursors];
    const kinds = firstEvents.map((frame) => (frame.data.event as { kind: string }).kind);
    let reachedEnd = false;
    for (let hop = 0; hop < 20; hop += 1) {
      const next = await bounded.request(`/api/v1/sessions/${id}/activity`, {
        headers: { "Last-Event-ID": cursor },
      });
      const hopFrames = frames(await next.text());
      const events = hopFrames.filter((frame) => frame.type === "run.event");
      for (const frame of events) {
        expect(walked).not.toContain(frame.id);
        walked.push(frame.id);
        kinds.push((frame.data.event as { kind: string }).kind);
      }
      expect(events.length).toBeLessThanOrEqual(2);
      const end = hopFrames.at(-1);
      if (end?.type !== "run.truncated") {
        reachedEnd = true;
        break;
      }
      cursor = end.data.cursor as string;
      expect(cursor).toBeTruthy();
    }
    // The walk ended because the stream closed at the run's end, not because
    // the hop budget ran out, and the run's terminal result was among the
    // events delivered.
    expect(reachedEnd).toBe(true);
    expect(kinds).toContain("result");
    expect(walked.length).toBeGreaterThan(2);
    expect(new Set(walked).size).toBe(walked.length);

    // An unusable cursor is refused rather than silently replaying everything.
    const bad = await bounded.request(`/api/v1/sessions/${id}/activity`, {
      headers: { "Last-Event-ID": "not-a-real-cursor" },
    });
    expect(bad.status).toBe(400);
    await expect(bad.json()).resolves.toMatchObject({ code: "invalid_cursor" });
  });
});
