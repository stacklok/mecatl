// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { client } from "@mecatl-studio/contracts/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { ChatWorkspace } from "./chat-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

interface RecordedRequest {
  body: unknown;
  method: string;
  pathname: string;
  search: string;
  signal: AbortSignal;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
    status,
  });
}

function frame(value: RunStreamEvent): Uint8Array {
  return new TextEncoder().encode(`data: ${JSON.stringify(value)}\n\n`);
}

function heldStream() {
  let writer!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      writer = controller;
    },
  });
  return {
    close: () => writer.close(),
    response: new Response(body, { headers: { "Content-Type": "text/event-stream" } }),
    send: (value: RunStreamEvent) => writer.enqueue(frame(value)),
  };
}

function completedStream(...events: RunStreamEvent[]): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const event of events) controller.enqueue(frame(event));
      controller.close();
    },
  });
  return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
}

function runStarted(sessionId = "chat-a", runId = "run-a"): RunStreamEvent {
  return { runId, sessionId, type: "run.started" };
}

function runEvent(
  kind: string,
  seq: string,
  text = "",
  runId = "run-a",
  payload?: unknown,
): RunStreamEvent {
  return {
    event: { kind, payload, runId, seq, text, turn: 1, unknown: false },
    type: "run.event",
  } as RunStreamEvent;
}

function session(id: string, state = "idle") {
  return {
    capabilities: { delete: true, deleteReason: "", rename: true, renameReason: "" },
    createdAt: "2026-09-24T12:00:00.000Z",
    debugTargetSessionId: "",
    id,
    modelId: "test-model",
    state,
    title: `Chat ${id}`,
    titleProvenance: "",
    titleRevision: "0",
    turns: 0,
    updatedAt: "2026-09-24T12:00:00.000Z",
  };
}

class BffFixture {
  readonly requests: RecordedRequest[] = [];
  readonly rows = new Map<string, ReturnType<typeof session>>();
  readonly runResponses: Response[] = [];
  readonly activityResponses = new Map<string, Response[]>();
  retryResponse = completedStream(
    runStarted("chat-a", "run-retry"),
    runEvent("result", "9", "", "run-retry", { stop: "end_turn" }),
  );
  steerStatus = 204;

  constructor(...rows: ReturnType<typeof session>[]) {
    for (const row of rows) this.rows.set(row.id, row);
  }

  requestsFor(method: string, suffix: string): RecordedRequest[] {
    return this.requests.filter(
      (request) => request.method === method && request.pathname.endsWith(suffix),
    );
  }

  readonly fetch = async (request: Request): Promise<Response> => {
    const url = new URL(request.url);
    const pathname = decodeURIComponent(url.pathname);
    const body = request.method === "GET" ? undefined : await request.clone().text();
    this.requests.push({
      body: body ? JSON.parse(body) : undefined,
      method: request.method,
      pathname,
      search: url.search,
      signal: request.signal,
    });
    if (request.method === "GET" && pathname === "/api/v1/runtime") {
      return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
    }
    if (request.method === "GET" && pathname === "/api/v1/settings/runtime") {
      return json({ models: [], modelsSupported: true });
    }
    if (request.method === "GET" && pathname === "/api/v1/sessions") {
      return json({ complete: true, items: [...this.rows.values()] });
    }
    if (request.method === "POST" && pathname === "/api/v1/sessions") {
      this.rows.set("chat-a", session("chat-a"));
      return json({ id: "chat-a" }, 201);
    }
    const match = pathname.match(/^\/api\/v1\/sessions\/([^/]+)(?:\/(.*))?$/);
    if (!match) throw new Error(`Unexpected BFF request: ${request.method} ${pathname}`);
    const [, sessionId, rest] = match;
    if (!sessionId) throw new Error(`Missing session ID in ${pathname}`);
    if (request.method === "GET" && rest === "transcript") {
      return json({ complete: true, messages: [], sessionId });
    }
    if (request.method === "GET" && rest === "activity") {
      const cursor = url.searchParams.get("resumeFrom") ?? "";
      const response = this.activityResponses.get(cursor)?.shift();
      if (!response) throw new Error(`Unexpected activity cursor: ${cursor}`);
      return response;
    }
    if (request.method === "GET" && !rest) {
      return json({
        capabilities: { image: false, manualCompaction: false, modelSelection: false },
        id: sessionId,
        mode: "default",
        state: this.rows.get(sessionId)?.state ?? "idle",
        usage: {
          cacheReadTokens: "0",
          cacheWriteTokens: "0",
          inputTokens: "0",
          outputTokens: "0",
          reasoningTokens: "0",
        },
      });
    }
    if (request.method === "POST" && rest === "runs") {
      const response = this.runResponses.shift();
      if (!response) throw new Error("Unexpected run start");
      return response;
    }
    if (request.method === "POST" && rest === "retry") return this.retryResponse;
    if (request.method === "POST" && rest?.endsWith("/steer")) {
      return this.steerStatus === 409
        ? json({ code: "stale_run_control", status: 409, detail: "The run ended" }, 409)
        : new Response(null, { status: 204 });
    }
    if (request.method === "POST" && rest?.endsWith("/cancel")) {
      return new Response(null, { status: 204 });
    }
    throw new Error(`Unexpected BFF request: ${request.method} ${pathname}`);
  };
}

async function mountWorkspace(bff: BffFixture, sessionId?: string) {
  vi.stubGlobal("fetch", bff.fetch);
  client.setConfig({ baseUrl: "http://studio.test" });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  const root = createRootRoute({
    component: () => (
      <ShortcutProvider>
        <Outlet />
      </ShortcutProvider>
    ),
  });
  const chat = createRoute({
    component: () => <ChatWorkspace sessionId={chat.useSearch().sessionId} />,
    getParentRoute: () => root,
    path: "/workspace/chat",
    validateSearch: (search: Record<string, unknown>): { sessionId?: string } => ({
      sessionId: typeof search.sessionId === "string" ? search.sessionId : undefined,
    }),
  });
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: [`/workspace/chat${sessionId ? `?sessionId=${sessionId}` : ""}`],
    }),
    routeTree: root.addChildren([chat]),
  });
  await act(async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    await router.load();
  });
  await screen.findByRole("textbox", { name: "Message Mecatl" });
  return { queryClient, router };
}

function typePrompt(value: string) {
  fireEvent.change(screen.getByRole("textbox", { name: "Message Mecatl" }), { target: { value } });
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.unstubAllGlobals();
});

describe("mounted chat workspace BFF boundary", () => {
  it("creates one session and drains one queued prompt only after its run settles", async () => {
    const bff = new BffFixture();
    const first = heldStream();
    bff.runResponses.push(
      first.response,
      completedStream(
        runStarted("chat-a", "run-b"),
        runEvent("result", "4", "", "run-b", { stop: "end_turn" }),
      ),
    );
    const { router } = await mountWorkspace(bff);
    typePrompt("first prompt");
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(bff.requestsFor("POST", "/runs")).toHaveLength(1));
    await act(async () => first.send(runStarted()));
    await waitFor(() => expect(router.state.location.search.sessionId).toBe("chat-a"));

    typePrompt("queued prompt");
    fireEvent.click(screen.getByRole("button", { name: "Queue message" }));
    await waitFor(() =>
      expect(window.localStorage.getItem("studio.chat.queue.chat-a")).toContain("queued prompt"),
    );
    expect(bff.requestsFor("POST", "/runs")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Edit queued message 1" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Edit queued message 1" }), {
      target: { value: "edited prompt" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save queued message 1" }));
    typePrompt("discard this");
    fireEvent.click(screen.getByRole("button", { name: "Queue message" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete queued message 2" }));
    expect(
      JSON.parse(window.localStorage.getItem("studio.chat.queue.chat-a") ?? "[]"),
    ).toMatchObject([{ text: "edited prompt" }]);

    await act(async () => {
      first.send(runEvent("result", "2", "", "run-a", { stop: "end_turn" }));
      first.close();
    });
    await waitFor(() => expect(bff.requestsFor("POST", "/runs")).toHaveLength(2));
    expect(bff.requestsFor("POST", "/api/v1/sessions")).toHaveLength(1);
    expect(bff.requestsFor("POST", "/runs").map((request) => request.body)).toEqual([
      { images: [], prompt: "first prompt" },
      { images: [], prompt: "edited prompt" },
    ]);
    expect(
      bff
        .requestsFor("POST", "/runs")
        .every((request) => request.pathname === "/api/v1/sessions/chat-a/runs"),
    ).toBe(true);
  });

  it("steers and cancels only the reattached run, then queues a stale steer once", async () => {
    window.localStorage.setItem("studio.chat.composer.enterToSend", "steer");
    const bff = new BffFixture(session("chat-a", "running"));
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    await mountWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => activity.send(runStarted()));
    await screen.findByRole("button", { name: "Stop" });

    typePrompt("steer successfully");
    fireEvent.click(screen.getByRole("button", { name: "Steer message" }));
    await waitFor(() => expect(bff.requestsFor("POST", "/steer")).toHaveLength(1));
    await waitFor(() =>
      expect(
        (screen.getByRole("textbox", { name: "Message Mecatl" }) as HTMLTextAreaElement).value,
      ).toBe(""),
    );
    expect(window.localStorage.getItem("studio.chat.queue.chat-a")).toBeNull();

    bff.steerStatus = 409;
    typePrompt("change direction");
    fireEvent.click(screen.getByRole("button", { name: "Steer message" }));
    await waitFor(() => expect(bff.requestsFor("POST", "/steer")).toHaveLength(2));
    await waitFor(() =>
      expect(window.localStorage.getItem("studio.chat.queue.chat-a")).toContain("change direction"),
    );
    expect(
      JSON.parse(window.localStorage.getItem("studio.chat.queue.chat-a") ?? "[]"),
    ).toHaveLength(1);
    expect(bff.requestsFor("POST", "/steer")[1]).toMatchObject({
      body: { text: "change direction" },
      pathname: "/api/v1/sessions/chat-a/runs/run-a/steer",
    });
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(bff.requestsFor("POST", "/cancel")).toHaveLength(1));
    expect(bff.requestsFor("POST", "/cancel")[0]?.pathname).toBe(
      "/api/v1/sessions/chat-a/runs/run-a/cancel",
    );
  });

  it("retries a saved failed turn without starting a second run or resending its prompt", async () => {
    const bff = new BffFixture(session("chat-a"));
    window.sessionStorage.setItem(
      "studio.chat.failedRun.chat-a",
      JSON.stringify({ message: "provider failed", permanent: false, prompt: "original prompt" }),
    );
    await mountWorkspace(bff, "chat-a");
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    await waitFor(() => expect(bff.requestsFor("POST", "/retry")).toHaveLength(1));
    expect(bff.requestsFor("POST", "/retry")[0]?.pathname).toBe("/api/v1/sessions/chat-a/retry");
    expect(bff.requestsFor("POST", "/runs")).toHaveLength(0);
    expect(bff.requestsFor("POST", "/api/v1/sessions")).toHaveLength(0);
  });

  it("reattaches an authorizing run and aborts only its browser stream on view switch", async () => {
    const bff = new BffFixture(session("chat-a", "authorizing"), session("chat-b"));
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    window.localStorage.setItem(
      "studio.chat.queue.chat-a",
      JSON.stringify([{ createdAt: 1, id: "queued-1", text: "keep for chat-a" }]),
    );
    const { router } = await mountWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    const streamRequest = bff.requestsFor("GET", "/activity")[0];
    expect(streamRequest?.pathname).toBe("/api/v1/sessions/chat-a/activity");

    await act(async () => {
      await router.navigate({ search: { sessionId: "chat-b" }, to: "/workspace/chat" });
    });
    await waitFor(() => expect(streamRequest?.signal.aborted).toBe(true));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(bff.requestsFor("POST", "/cancel")).toHaveLength(0);
    expect(bff.requestsFor("POST", "/runs")).toHaveLength(0);
    expect(window.localStorage.getItem("studio.chat.queue.chat-a")).toContain("keep for chat-a");
  });

  it("reattaches across two bounded cursors without duplicating replayed turns", async () => {
    const bff = new BffFixture(session("chat-a", "running"));
    const continuation = heldStream();
    bff.activityResponses.set("", [
      completedStream(
        runEvent("user_prompt", "1", "first prompt"),
        runEvent("message.delta", "2", "first answer"),
        { cursor: "cursor-2", reason: "bound", type: "run.truncated" },
      ),
    ]);
    bff.activityResponses.set("cursor-2", [
      completedStream(
        runEvent("message.delta", "2", "first answer"),
        runEvent("result", "3", "", "run-a", { stop: "end_turn" }),
        runEvent("user_prompt", "4", "second prompt", "run-b"),
        { cursor: "cursor-4", reason: "bound", type: "run.truncated" },
      ),
    ]);
    bff.activityResponses.set("cursor-4", [continuation.response]);
    await mountWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(3));
    expect(bff.requestsFor("GET", "/activity").map((request) => request.search)).toEqual([
      "",
      "?resumeFrom=cursor-2",
      "?resumeFrom=cursor-4",
    ]);
    await act(async () =>
      continuation.send(runEvent("user_prompt", "4", "second prompt", "run-b")),
    );
    await act(async () =>
      continuation.send(runEvent("message.delta", "5", "second answer", "run-b")),
    );
    await waitFor(() => expect(screen.getAllByText("second answer")).toHaveLength(1));
    expect(screen.getByText(/replaying older activity first/i)).toBeTruthy();
    expect(screen.getAllByText("first prompt")).toHaveLength(1);
    expect(screen.getAllByText("first answer")).toHaveLength(1);
    expect(screen.getAllByText("second prompt")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(bff.requestsFor("POST", "/cancel")).toHaveLength(1));
    expect(bff.requestsFor("POST", "/cancel")[0]?.pathname).toBe(
      "/api/v1/sessions/chat-a/runs/run-b/cancel",
    );

    bff.rows.set("chat-a", session("chat-a"));
    await act(async () => {
      continuation.send(runEvent("result", "6", "", "run-b", { stop: "end_turn" }));
      continuation.close();
    });
    await waitFor(() => expect(screen.queryByText(/replaying older activity first/i)).toBeNull());
    expect(bff.requestsFor("GET", "/transcript").length).toBeGreaterThanOrEqual(2);
  });

  it("parks a queued prompt and reports missing history after a durable gap", async () => {
    const bff = new BffFixture(session("chat-a", "running"));
    bff.activityResponses.set("", [
      completedStream(runEvent("user_prompt", "1", "earlier prompt"), {
        cursor: "cursor-1",
        reason: "gap",
        type: "run.truncated",
      }),
    ]);
    window.localStorage.setItem(
      "studio.chat.queue.chat-a",
      JSON.stringify([{ createdAt: 1, id: "queued-1", text: "parked prompt" }]),
    );
    await mountWorkspace(bff, "chat-a");
    expect(await screen.findByText(/some activity is missing from the live log/i)).toBeTruthy();
    expect(window.localStorage.getItem("studio.chat.queue.chat-a")).toContain("parked prompt");
    expect(bff.requestsFor("GET", "/activity")).toHaveLength(1);
    expect(bff.requestsFor("POST", "/runs")).toHaveLength(0);
    expect(screen.queryByText("Completed")).toBeNull();
  });

  it("leaves an abruptly closed follow uncertain and does not drain its queue", async () => {
    const bff = new BffFixture(session("chat-a", "running"));
    bff.activityResponses.set("", [
      completedStream(
        runEvent("user_prompt", "1", "earlier prompt"),
        runEvent("message.delta", "2", "partial answer"),
      ),
    ]);
    window.localStorage.setItem(
      "studio.chat.queue.chat-a",
      JSON.stringify([{ createdAt: 1, id: "queued-1", text: "parked prompt" }]),
    );
    await mountWorkspace(bff, "chat-a");
    await screen.findByText(/unknown outcome — reconnect to check this run/i);
    expect(window.localStorage.getItem("studio.chat.queue.chat-a")).toContain("parked prompt");
    expect(bff.requestsFor("POST", "/runs")).toHaveLength(0);
    expect(screen.queryByText("Completed")).toBeNull();
  });
});
