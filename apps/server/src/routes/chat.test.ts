// SPDX-License-Identifier: Apache-2.0

import {
  type CreateSessionRequest,
  imageAttachmentSchema,
  isPaddedBase64,
  type RunStreamEvent,
  type StartRunRequest,
} from "@mecatl-studio/contracts";
import { MecatlError } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import { createApp } from "../app";
import type { ActivityDelivery, ChatService } from "../mecatl/chat";
import { csrfHeaders } from "../testing/fakes";

let createdSessionRequest: CreateSessionRequest | undefined;
let startedRunRequest: StartRunRequest | undefined;

const chat = {
  async *activity(sessionId: string): AsyncIterable<ActivityDelivery> {
    yield { delivery: { runId: "run-attached", sessionId, type: "run.started" } };
  },
  async cancelRun() {
    return true;
  },
  async clearSession() {
    return { id: "session-clear" };
  },
  async compactSession() {
    return { compacted: true };
  },
  async createSession(request: CreateSessionRequest) {
    createdSessionRequest = request;
    return { id: "session-2" };
  },
  async deleteSession() {},
  async detail(sessionId: string) {
    return {
      capabilities: { image: true, manualCompaction: true, modelSelection: true },
      id: sessionId,
      mode: "plan" as const,
      model: {
        contextWindow: "200000",
        id: "test-model",
        providerId: "test",
        reasoningEffort: "high" as const,
      },
      state: "idle",
      usage: {
        cacheReadTokens: "4",
        cacheWriteTokens: "5",
        inputTokens: "100",
        outputTokens: "20",
        reasoningTokens: "6",
      },
    };
  },
  async forkSession() {
    return { id: "session-fork" };
  },
  async listSessions() {
    return {
      complete: true,
      items: [
        {
          capabilities: { delete: true, deleteReason: "", rename: true, renameReason: "" },
          createdAt: "2026-09-16T12:00:00.000Z",
          debugTargetSessionId: "",
          id: "session-1",
          modelId: "test-model",
          state: "idle",
          title: "First chat",
          titleProvenance: "operator",
          titleRevision: "9007199254740993",
          turns: 1,
          updatedAt: "2026-09-16T12:01:00.000Z",
        },
      ],
    };
  },
  async renameSession(_sessionId: string, title: string) {
    return { title, titleProvenance: "operator", titleRevision: "9007199254740994" };
  },
  async *retry(sessionId: string): AsyncIterable<RunStreamEvent> {
    yield { runId: "run-retry", sessionId, type: "run.started" };
  },
  async resolvePermission() {
    return true;
  },
  async steer() {
    return true;
  },
  async *run(sessionId: string, request: StartRunRequest): AsyncIterable<RunStreamEvent> {
    startedRunRequest = request;
    yield { runId: "run-1", sessionId, type: "run.started" };
    yield {
      event: {
        kind: "message.delta",
        runId: "run-1",
        seq: "1",
        text: "hello",
        turn: 1,
        unknown: false,
      },
      type: "run.event",
    };
  },
  async setMode(_sessionId: string, mode: "acceptEdits" | "default" | "plan") {
    return { mode };
  },
  async transcript(sessionId: string) {
    return { complete: true, messages: [], sessionId };
  },
} satisfies ChatService;

const app = createApp({ chat });

/** Sends the bootstrap's same-origin + double-submit CSRF pair on every mutation. */
const request = (input: string, init?: RequestInit) => {
  const method = init?.method?.toUpperCase() ?? "GET";
  const headers = new Headers(init?.headers);
  if (["POST", "PUT", "PATCH", "DELETE"].includes(method)) {
    for (const [name, value] of Object.entries(csrfHeaders())) headers.set(name, value);
  }
  return app.request(input, { ...init, headers });
};

describe("chat routes", () => {
  it("lists sessions through the product contract", async () => {
    const response = await request("/api/v1/sessions");

    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toMatchObject({
      complete: true,
      items: [{ id: "session-1", title: "First chat", titleRevision: "9007199254740993" }],
    });
  });

  it("returns the renamed title and its revision through the BFF route", async () => {
    const response = await request("/api/v1/sessions/session-1", {
      body: JSON.stringify({ title: "Renamed chat" }),
      headers: { "Content-Type": "application/json" },
      method: "PATCH",
    });
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({
      title: "Renamed chat",
      titleProvenance: "operator",
      titleRevision: "9007199254740994",
    });
  });

  it("creates sessions", async () => {
    const response = await request("/api/v1/sessions", {
      body: JSON.stringify({
        mode: "plan",
        model: { id: "claude-sonnet", providerId: "anthropic" },
        reasoningEffort: "high",
        toolAccess: "noFilesystem",
      }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });

    expect(response.status).toBe(201);
    await expect(response.json()).resolves.toEqual({ id: "session-2" });
    expect(createdSessionRequest).toEqual({
      mode: "plan",
      model: { id: "claude-sonnet", providerId: "anthropic" },
      reasoningEffort: "high",
      toolAccess: "noFilesystem",
    });
  });

  it("returns session detail and applies lifecycle controls", async () => {
    const detail = await request("/api/v1/sessions/session-1");
    const mode = await request("/api/v1/sessions/session-1/mode", {
      body: JSON.stringify({ mode: "acceptEdits" }),
      headers: { "Content-Type": "application/json" },
      method: "PUT",
    });
    const compaction = await request("/api/v1/sessions/session-1/compaction", {
      method: "POST",
    });
    const fork = await request("/api/v1/sessions/session-1/fork", {
      body: JSON.stringify({
        model: { id: "next-model", providerId: "test" },
        reasoningEffort: "medium",
      }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    const clear = await request("/api/v1/sessions/session-1/clear", { method: "POST" });

    expect(detail.status).toBe(200);
    await expect(detail.json()).resolves.toMatchObject({
      id: "session-1",
      mode: "plan",
      usage: { inputTokens: "100" },
    });
    await expect(mode.json()).resolves.toEqual({ mode: "acceptEdits" });
    await expect(compaction.json()).resolves.toEqual({ compacted: true });
    expect(fork.status).toBe(201);
    await expect(fork.json()).resolves.toEqual({ id: "session-fork" });
    expect(clear.status).toBe(201);
    await expect(clear.json()).resolves.toEqual({ id: "session-clear" });
  });

  it("streams durable session activity", async () => {
    const response = await request("/api/v1/sessions/session-1/activity");

    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("text/event-stream");
    expect(await response.text()).toContain('"runId":"run-attached"');
  });

  it("streams a durable retry through the product route", async () => {
    const response = await request("/api/v1/sessions/session-1/retry", { method: "POST" });

    expect(response.status).toBe(200);
    expect(await response.text()).toContain('"runId":"run-retry"');
  });

  it("streams normalized run events over SSE", async () => {
    const response = await request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({ prompt: "Hello" }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });

    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("text/event-stream");
    const body = await response.text();
    expect(body).toContain("event: run.started");
    expect(body).toContain('"kind":"message.delta"');
    expect(startedRunRequest).toEqual({ images: [], prompt: "Hello" });
  });

  it("starts an image-only run", async () => {
    const response = await request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({
        images: [{ data: "aGVsbG8=", mimeType: "image/png", name: "diagram.png" }],
        prompt: "",
      }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });

    expect(response.status).toBe(200);
    await response.text();
    expect(startedRunRequest).toEqual({
      images: [{ data: "aGVsbG8=", mimeType: "image/png", name: "diagram.png" }],
      prompt: "",
    });
  });

  it("rejects empty and malformed image prompts before starting a run", async () => {
    const empty = await request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({ images: [], prompt: "" }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    const malformed = await request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({
        images: [{ data: "not base64", mimeType: "text/plain", name: "bad.txt" }],
        prompt: "",
      }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });

    expect(empty.status).toBe(400);
    expect(malformed.status).toBe(400);
  });

  it("routes controls to the active run", async () => {
    const cancel = await request("/api/v1/sessions/session-1/runs/run-1/cancel", {
      method: "POST",
    });
    const permission = await request("/api/v1/sessions/session-1/runs/run-1/permissions/ask-1", {
      body: JSON.stringify({ verdict: "allow_once" }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });
    const steer = await request("/api/v1/sessions/session-1/runs/run-1/steer", {
      body: JSON.stringify({ text: "focus on the auth module instead" }),
      headers: { "Content-Type": "application/json" },
      method: "POST",
    });

    expect(cancel.status).toBe(204);
    expect(permission.status).toBe(204);
    expect(steer.status).toBe(204);
  });
  it("returns 409 stale_run_control when a control targets an ended run", async () => {
    const stale = createApp({
      chat: {
        ...chat,
        cancelRun: async () => false,
        resolvePermission: async () => false,
        steer: async () => false,
      },
    });
    const send = (path: string, body?: unknown) =>
      stale.request(path, {
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        headers: csrfHeaders("t", { "Content-Type": "application/json" }),
        method: "POST",
      });
    for (const response of await Promise.all([
      send("/api/v1/sessions/session-1/runs/run-old/cancel"),
      send("/api/v1/sessions/session-1/runs/run-old/steer", { text: "stop" }),
      send("/api/v1/sessions/session-1/runs/run-old/permissions/ask-1", { verdict: "deny" }),
    ])) {
      expect(response.status).toBe(409);
      expect(response.headers.get("content-type")).toContain("application/problem+json");
      await expect(response.json()).resolves.toMatchObject({ code: "stale_run_control" });
    }
  });

  it("chat routes require a session when interactive login is active and satisfy the CSRF gate", async () => {
    // Without the CSRF pair every mutation, the two POST streams included, is refused.
    for (const [path, init] of [
      ["/api/v1/sessions", { body: JSON.stringify({}), method: "POST" }],
      ["/api/v1/sessions/session-1", { body: JSON.stringify({ title: "x" }), method: "PATCH" }],
      ["/api/v1/sessions/session-1", { method: "DELETE" }],
      [
        "/api/v1/sessions/session-1/runs",
        { body: JSON.stringify({ prompt: "hi" }), method: "POST" },
      ],
      ["/api/v1/sessions/session-1/retry", { method: "POST" }],
      [
        "/api/v1/sessions/session-1/mode",
        { body: JSON.stringify({ mode: "plan" }), method: "PUT" },
      ],
    ] as const) {
      const response = await app.request(path, {
        ...init,
        headers: { "Content-Type": "application/json" },
      });
      expect(response.status).toBe(403);
      await expect(response.json()).resolves.toMatchObject({ code: "cross_site_request" });
    }

    // With interactive login active and no session, reads and mutations alike are 401.
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
        signInRequired: async () => true,
        startLogin: async () => "https://issuer.example.com/authorize",
      },
      chat,
    });
    const list = await gated.request("/api/v1/sessions");
    expect(list.status).toBe(401);
    await expect(list.json()).resolves.toMatchObject({ code: "unauthenticated" });
    const run = await gated.request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({ prompt: "hi" }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "POST",
    });
    expect(run.status).toBe(401);
  });

  it("ends a failed stream with a run.error frame and aborts the run when the client disconnects", async () => {
    let seenSignal: AbortSignal | undefined;
    const failing = createApp({
      chat: {
        ...chat,
        async *run(sessionId, _request, signal): AsyncIterable<RunStreamEvent> {
          seenSignal = signal;
          yield { runId: "run-9", sessionId, type: "run.started" };
          throw new MecatlError("upstream exploded", {
            code: "internal",
            status: 500,
            transport: "grpc",
          });
        },
      },
    });
    const response = await failing.request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({ prompt: "hi" }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "POST",
    });
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("text/event-stream");
    const body = await response.text();
    expect(body).toContain("event: run.started");
    expect(body).toContain("event: run.error");
    expect(body).toContain('"code":"internal"');
    expect(seenSignal).toBeDefined();

    // Disconnecting the browser aborts the SDK run through the forwarded signal.
    let runSignal: AbortSignal | undefined;
    let release: (() => void) | undefined;
    const hanging = createApp({
      chat: {
        ...chat,
        async *run(sessionId, _request, signal): AsyncIterable<RunStreamEvent> {
          runSignal = signal;
          yield { runId: "run-10", sessionId, type: "run.started" };
          await new Promise<void>((resolve) => {
            release = resolve;
            signal?.addEventListener("abort", () => resolve(), { once: true });
          });
        },
      },
    });
    const controller = new AbortController();
    const streaming = await hanging.request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({ prompt: "hi" }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "POST",
      signal: controller.signal,
    });
    const reader = streaming.body?.getReader();
    await reader?.read();
    expect(runSignal?.aborted).toBe(false);
    controller.abort();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(runSignal?.aborted).toBe(true);
    release?.();
    await reader?.cancel().catch(() => undefined);

    // A browser already gone before the route attached its listener aborts the
    // run too: an already-aborted signal never fires an "abort" event.
    let lateSignal: AbortSignal | undefined;
    const gone = createApp({
      chat: {
        ...chat,
        async *run(sessionId, _request, signal): AsyncIterable<RunStreamEvent> {
          lateSignal = signal;
          yield { runId: "run-11", sessionId, type: "run.started" };
        },
      },
    });
    const goneResponse = await gone.request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({ prompt: "hi" }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "POST",
      signal: AbortSignal.abort(),
    });
    await goneResponse.text().catch(() => undefined);
    expect(lateSignal?.aborted).toBe(true);
  });
  it("resumes activity from Last-Event-ID and stamps each frame with its cursor", async () => {
    const seen: Array<string | undefined> = [];
    const resuming = createApp({
      chat: {
        ...chat,
        async *activity(sessionId, request): AsyncIterable<ActivityDelivery> {
          seen.push(request.cursor);
          yield { delivery: { runId: "run-9", sessionId, type: "run.started" } };
          yield {
            cursor: "c7",
            delivery: {
              event: {
                kind: "message.delta",
                runId: "run-9",
                seq: "7",
                text: "hi",
                turn: 1,
                unknown: false,
              },
              type: "run.event",
            },
          };
        },
      },
    });

    const header = await resuming.request("/api/v1/sessions/session-1/activity", {
      headers: { "Last-Event-ID": "c6" },
    });
    expect(header.status).toBe(200);
    const body = await header.text();
    // The event frame carries its durable cursor as the SSE id; run.started does not.
    expect(body).toMatch(/event: run\.event\nid: c7\n|id: c7\n/u);
    expect(body).toContain("event: run.started");

    const query = await resuming.request("/api/v1/sessions/session-1/activity?resumeFrom=c5");
    expect(query.status).toBe(200);
    await query.text();

    const both = await resuming.request("/api/v1/sessions/session-1/activity?resumeFrom=c5", {
      headers: { "Last-Event-ID": "c6" },
    });
    await both.text();

    // Header, query, then the header winning over the query.
    expect(seen).toEqual(["c6", "c5", "c6"]);

    const fresh = await resuming.request("/api/v1/sessions/session-1/activity");
    await fresh.text();
    expect(seen.at(-1)).toBeUndefined();
  });

  it("rejects an unusable cursor and caps concurrent activity streams per session", async () => {
    const rejecting = createApp({
      chat: {
        ...chat,
        // The daemon's refusal surfaces at the first `next()`, exactly as the
        // real adapter's async generator does.
        activity(): AsyncIterable<ActivityDelivery> {
          return {
            [Symbol.asyncIterator]: () => ({
              next: () =>
                Promise.reject(
                  new MecatlError("cursor is scoped to another filter", {
                    code: "cursor_scope",
                    status: 400,
                    transport: "grpc",
                  }),
                ),
            }),
          };
        },
      },
    });
    const invalid = await rejecting.request("/api/v1/sessions/session-1/activity", {
      headers: { "Last-Event-ID": "not-a-cursor" },
    });
    expect(invalid.status).toBe(400);
    expect(invalid.headers.get("content-type")).toContain("application/problem+json");
    await expect(invalid.json()).resolves.toMatchObject({ code: "invalid_cursor" });

    // Two streams that never finish, against a cap of two.
    let release: (() => void) | undefined;
    const parked = new Promise<void>((resolve) => {
      release = resolve;
    });
    const capped = createApp({
      activity: { maxStreams: 2, replayMax: 10 },
      chat: {
        ...chat,
        async *activity(sessionId): AsyncIterable<ActivityDelivery> {
          yield { delivery: { runId: "run-1", sessionId, type: "run.started" } };
          await parked;
        },
      },
    });
    const first = await capped.request("/api/v1/sessions/session-1/activity");
    const second = await capped.request("/api/v1/sessions/session-1/activity");
    const firstRead = first.body?.getReader();
    const secondRead = second.body?.getReader();
    await firstRead?.read();
    await secondRead?.read();

    const third = await capped.request("/api/v1/sessions/session-1/activity");
    expect(third.status).toBe(429);
    expect(third.headers.get("retry-after")).toBe("5");
    await expect(third.json()).resolves.toMatchObject({ code: "too_many_streams" });

    // Another session has its own budget.
    const other = await capped.request("/api/v1/sessions/session-2/activity");
    expect(other.status).toBe(200);
    const otherRead = other.body?.getReader();
    await otherRead?.read();

    // Finishing a stream frees its slot.
    release?.();
    await firstRead?.cancel().catch(() => undefined);
    await new Promise((resolve) => setTimeout(resolve, 20));
    const afterRelease = await capped.request("/api/v1/sessions/session-1/activity");
    expect(afterRelease.status).toBe(200);
    await afterRelease.body?.cancel().catch(() => undefined);
    await secondRead?.cancel().catch(() => undefined);
    await otherRead?.cancel().catch(() => undefined);
  });

  it("attaches an idle session's activity stream at once and refuses SDK sentinel cursors", async () => {
    let attachedWith: string | undefined = "unset";
    let finish: (() => void) | undefined;
    const waiting = new Promise<void>((resolve) => {
      finish = resolve;
    });
    const idle = createApp({
      chat: {
        ...chat,
        async *activity(_sessionId, request): AsyncIterable<ActivityDelivery> {
          attachedWith = request.cursor;
          // The replay-to-live boundary of a session with no history, then a
          // wait for a live event that has not happened yet.
          yield {};
          await waiting;
        },
      },
    });
    // Headers are committed without waiting for a live event.
    const response = await idle.request("/api/v1/sessions/session-1/activity");
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("text/event-stream");
    expect(attachedWith).toBeUndefined();
    finish?.();
    // The marker itself is never written as a frame.
    await expect(response.text()).resolves.toBe("");

    for (const cursor of ["start", "now", "x".repeat(1_025)]) {
      const header = await idle.request("/api/v1/sessions/session-1/activity", {
        headers: { "Last-Event-ID": cursor },
      });
      expect(header.status).toBe(400);
      await expect(header.json()).resolves.toMatchObject({ code: "invalid_cursor" });
    }
    const query = await idle.request("/api/v1/sessions/session-1/activity?resumeFrom=now");
    expect(query.status).toBe(400);
  });

  it("validates multi-megabyte base64 attachments without exhausting the regex stack", () => {
    // 9 MiB of image data encodes to ~12.6 million characters.
    const large = Buffer.alloc(9 * 1024 * 1024, 0xab).toString("base64");
    expect(
      imageAttachmentSchema.safeParse({ data: large, mimeType: "image/png", name: "big.png" })
        .success,
    ).toBe(true);
    for (const valid of ["AAAA", "AAA=", "AA==", "+/9z"]) expect(isPaddedBase64(valid)).toBe(true);
    for (const invalid of ["AAA", "A===", "AA=A", "AA-_", "AAAA\n", "=AAA"]) {
      expect(isPaddedBase64(invalid)).toBe(false);
    }
  });

  it("refuses an oversized body with 413 before reading it", async () => {
    let started = false;
    const guarded = createApp({
      chat: {
        ...chat,
        async *run(sessionId): AsyncIterable<RunStreamEvent> {
          started = true;
          yield { runId: "run-12", sessionId, type: "run.started" };
        },
      },
    });
    const oversized = await guarded.request("/api/v1/sessions/session-1/runs", {
      body: JSON.stringify({ images: [], prompt: "x".repeat(33 * 1024 * 1024) }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "POST",
    });
    expect(oversized.status).toBe(413);
    await expect(oversized.json()).resolves.toMatchObject({ code: "request_too_large" });
    expect(started).toBe(false);

    // An ordinary route has the smaller bound.
    const rename = await guarded.request("/api/v1/sessions/session-1", {
      body: JSON.stringify({ title: "x".repeat(9 * 1024 * 1024) }),
      headers: csrfHeaders("t", { "Content-Type": "application/json" }),
      method: "PATCH",
    });
    expect(rename.status).toBe(413);
  });
});
