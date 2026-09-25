// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type {
  RunStreamEvent,
  RuntimeResponse,
  RuntimeSettingsResponse,
  SessionDetailResponse,
  SessionTranscriptResponse,
} from "@mecatl-studio/contracts";
import { client } from "@mecatl-studio/contracts/client";
import { getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { setRequestRecoveryState } from "../../lib/api-client";
import { AuthRecoveryContext, type AuthRecoveryContextValue } from "../auth/auth-recovery-context";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { ChatWorkspace } from "./chat-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const readyRecovery: AuthRecoveryContextValue = {
  banner: { authenticated: true, publicStatusFailed: false, sessionCheckFailed: false },
  loginUrl: "/api/v1/auth/login",
  phase: "ready",
  popupIssue: null,
  retrySession: () => {},
  startPopupLogin: () => {},
};

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

function runtimeResponse(connection: RuntimeResponse["connection"] = "online"): Response {
  return json({ capabilities: { image: false, posture: "managed" }, connection });
}

function detailResponse(
  sessionId: string,
  state = "idle",
  overrides: Partial<SessionDetailResponse> = {},
): Response {
  return json({
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: sessionId,
    mode: "default",
    state,
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
    ...overrides,
  });
}

function heldResponse() {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((done) => {
    resolve = done;
  });
  return { promise, resolve };
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

const inventoryModels = [
  {
    contextLimit: "128000",
    displayName: "Current model",
    id: "current",
    image: false,
    providerId: "provider",
    reasoning: true,
  },
  {
    contextLimit: "128000",
    displayName: "Allowed model",
    id: "allowed",
    image: true,
    providerId: "provider",
    reasoning: true,
  },
  {
    contextLimit: "128000",
    displayName: "Hidden model",
    id: "hidden",
    image: false,
    providerId: "provider",
    reasoning: true,
  },
] satisfies RuntimeSettingsResponse["models"];

const currentModel = {
  contextWindow: "128000",
  id: "current",
  providerId: "provider",
  reasoningEffort: "medium",
} satisfies NonNullable<SessionDetailResponse["model"]>;

function serveModelInventory(bff: BffFixture) {
  bff.nextReplies.set("/api/v1/settings/runtime", [
    Promise.resolve(json({ models: inventoryModels, modelsSupported: true })),
  ]);
}

function hideModel() {
  window.localStorage.setItem(
    "studio.chat.models.disabled",
    JSON.stringify([JSON.stringify(["provider", "hidden"])]),
  );
}

class BffFixture {
  readonly requests: RecordedRequest[] = [];
  readonly rows = new Map<string, ReturnType<typeof session>>();
  readonly transcripts = new Map<string, SessionTranscriptResponse>();
  readonly nextReplies = new Map<string, Promise<Response>[]>();
  readonly runResponses: Response[] = [];
  readonly activityResponses = new Map<string, Response[]>();
  runtimeConnection: RuntimeResponse["connection"] = "online";
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

  requestsAt(method: string, pathname: string): RecordedRequest[] {
    return this.requests.filter(
      (request) => request.method === method && request.pathname === pathname,
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
    const queuedReply = this.nextReplies.get(pathname)?.shift();
    if (queuedReply) return queuedReply;
    if (request.method === "GET" && pathname === "/api/v1/runtime") {
      return runtimeResponse(this.runtimeConnection);
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
      return json(this.transcripts.get(sessionId) ?? { complete: true, messages: [], sessionId });
    }
    if (request.method === "GET" && rest === "activity") {
      const cursor = url.searchParams.get("resumeFrom") ?? "";
      const response = this.activityResponses.get(cursor)?.shift();
      if (!response) throw new Error(`Unexpected activity cursor: ${cursor}`);
      return response;
    }
    if (request.method === "GET" && !rest) {
      return detailResponse(sessionId, this.rows.get(sessionId)?.state);
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
  setRequestRecoveryState({ identityEpoch: 0, phase: "ready", workspaceMounted: true });
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
  const away = createRoute({
    component: () => <p>Outside chat</p>,
    getParentRoute: () => root,
    path: "/away",
  });
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: [`/workspace/chat${sessionId ? `?sessionId=${sessionId}` : ""}`],
    }),
    routeTree: root.addChildren([chat, away]),
  });
  await act(async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <AuthRecoveryContext.Provider value={readyRecovery}>
          <RouterProvider router={router} />
        </AuthRecoveryContext.Provider>
      </QueryClientProvider>,
    );
    await router.load();
  });
  await screen.findByRole("textbox", { name: "Message Mecatl" });
  return { queryClient, router };
}

async function mountConnectedWorkspace(bff: BffFixture, sessionId: string) {
  const mounted = await mountWorkspace(bff, sessionId);
  await screen.findByRole("heading", { name: `Chat ${sessionId}` });
  await waitFor(() =>
    expect(mounted.queryClient.getQueryData(getRuntimeOptions().queryKey)).toMatchObject({
      connection: "online",
    }),
  );
  await waitFor(() =>
    expect(bff.requestsAt("GET", `/api/v1/sessions/${sessionId}/transcript`)).toHaveLength(1),
  );
  return mounted;
}

function startClock() {
  vi.useFakeTimers({
    toFake: ["Date", "setTimeout", "clearTimeout", "setInterval", "clearInterval"],
  });
  vi.setSystemTime(new Date("2026-09-24T12:00:00.000Z"));
}

function startPollingClock() {
  // Keep Testing Library's waitFor timer real while mounting the workspace.
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  vi.setSystemTime(new Date("2026-09-24T12:00:00.000Z"));
}

async function advanceClock(milliseconds: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(milliseconds);
  });
}

function controlVisibility() {
  let state: DocumentVisibilityState = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => state);
  return async (next: DocumentVisibilityState) => {
    state = next;
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
  };
}

function typePrompt(value: string) {
  fireEvent.change(screen.getByRole("textbox", { name: "Message Mecatl" }), { target: { value } });
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("mounted chat workspace BFF boundary", () => {
  it("creates a draft with its selected controls and excludes a disabled model", async () => {
    const user = userEvent.setup();
    const bff = new BffFixture();
    hideModel();
    serveModelInventory(bff);
    bff.runResponses.push(
      completedStream(runStarted(), runEvent("result", "1", "", "run-a", { stop: "end_turn" })),
    );
    await mountWorkspace(bff);

    await user.click(screen.getByRole("button", { name: "Chat options" }));
    const options = screen.getByRole("dialog", { name: "Chat options" });
    expect(within(options).queryByRole("option", { name: "Hidden model" })).toBeNull();
    await user.selectOptions(within(options).getByLabelText("Model"), '["provider","allowed"]');
    await user.selectOptions(within(options).getByLabelText("Effort"), "high");
    await user.selectOptions(within(options).getByLabelText("Mode"), "plan");
    await user.selectOptions(within(options).getByLabelText("Tools"), "noFilesystem");
    await user.click(within(options).getByRole("button", { name: "Close chat options" }));

    typePrompt("Create with these options");
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(bff.requestsAt("POST", "/api/v1/sessions")).toHaveLength(1));
    expect(bff.requestsAt("POST", "/api/v1/sessions")[0]?.body).toEqual({
      mode: "plan",
      model: { id: "allowed", providerId: "provider" },
      reasoningEffort: "high",
      toolAccess: "noFilesystem",
    });
  });

  it("changes an existing mode in place but forks to change its model", async () => {
    const user = userEvent.setup();
    const bff = new BffFixture(session("chat-a"));
    hideModel();
    serveModelInventory(bff);
    const capabilities = { image: false, manualCompaction: false, modelSelection: true };
    bff.nextReplies.set("/api/v1/sessions/chat-a", [
      Promise.resolve(detailResponse("chat-a", "idle", { capabilities, model: currentModel })),
    ]);
    const { router } = await mountConnectedWorkspace(bff, "chat-a");
    const controls = screen.getByRole("region", { name: "Chat configuration and usage" });

    bff.nextReplies.set("/api/v1/sessions/chat-a/mode", [Promise.resolve(json({ mode: "plan" }))]);
    bff.nextReplies.set("/api/v1/sessions/chat-a", [
      Promise.resolve(
        detailResponse("chat-a", "idle", { capabilities, mode: "plan", model: currentModel }),
      ),
    ]);
    await user.click(within(controls).getByRole("button", { name: "Mode Manual" }));
    await user.click(screen.getByRole("menuitem", { name: /^Plan/ }));
    await waitFor(() =>
      expect(bff.requestsAt("PUT", "/api/v1/sessions/chat-a/mode")).toHaveLength(1),
    );
    expect(bff.requestsAt("PUT", "/api/v1/sessions/chat-a/mode")[0]?.body).toEqual({
      mode: "plan",
    });
    expect(await within(controls).findByRole("button", { name: "Mode Plan" })).toBeTruthy();

    bff.nextReplies.set("/api/v1/sessions/chat-a/fork", [
      Promise.resolve(json({ id: "chat-b" }, 201)),
    ]);
    await user.click(within(controls).getByRole("button", { name: /Model Current model/ }));
    const modelMenu = screen.getByRole("menuitem", { name: /^Model Current model/ });
    await user.hover(modelMenu);
    const allowed = await screen.findByRole("menuitem", { name: "Allowed model" });
    expect(screen.queryByRole("menuitem", { name: "Hidden model" })).toBeNull();
    act(() => allowed.focus());
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(bff.requestsAt("POST", "/api/v1/sessions/chat-a/fork")).toHaveLength(1),
    );
    expect(bff.requestsAt("POST", "/api/v1/sessions/chat-a/fork")[0]?.body).toEqual({
      model: { id: "allowed", providerId: "provider" },
      reasoningEffort: "medium",
    });
    expect(
      bff.requests
        .filter(
          (request) =>
            request.method !== "GET" && request.pathname.startsWith("/api/v1/sessions/chat-a/"),
        )
        .map(({ method, pathname }) => ({ method, pathname })),
    ).toEqual([
      { method: "PUT", pathname: "/api/v1/sessions/chat-a/mode" },
      { method: "POST", pathname: "/api/v1/sessions/chat-a/fork" },
    ]);
    expect(bff.requestsAt("POST", "/api/v1/sessions")).toHaveLength(0);
    await waitFor(() => expect(router.state.location.search.sessionId).toBe("chat-b"));
    await waitFor(() =>
      expect(bff.requestsAt("GET", "/api/v1/sessions/chat-b").length).toBeGreaterThan(0),
    );
  });

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

  it("adopts a late title and one recorded delivery on the next visible 20-second poll", async () => {
    const bff = new BffFixture(session("chat-a"));
    startPollingClock();
    await mountConnectedWorkspace(bff, "chat-a");
    expect(vi.getTimerCount()).toBe(1);
    const later = {
      ...session("chat-a"),
      title: "Generated title",
      titleProvenance: "generated",
      titleRevision: "1",
      updatedAt: "2026-09-24T12:00:15.000Z",
    };
    bff.rows.set("chat-a", later);
    bff.transcripts.set("chat-a", {
      complete: true,
      messages: [
        {
          delivery: { fireId: "fire-1", kind: "completed", scheduleName: "Daily report" },
          images: [],
          role: "user",
          text: "Recorded output",
          toolCalls: [],
        },
      ],
      sessionId: "chat-a",
    });

    await advanceClock(19_999);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(1);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(1);
    expect(screen.getByRole("heading", { name: "Chat chat-a" })).toBeTruthy();
    expect(document.querySelectorAll("[data-delivery-note]")).toHaveLength(0);

    await advanceClock(1);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(2);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2);
    expect(await screen.findByRole("heading", { name: "Generated title" })).toBeTruthy();
    expect(screen.getAllByText("Generated title")).toHaveLength(2);
    expect(await screen.findByText("Recorded output")).toBeTruthy();
    expect(document.querySelectorAll("[data-delivery-note]")).toHaveLength(1);

    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(3);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2);
    expect(document.querySelectorAll("[data-delivery-note]")).toHaveLength(1);
  });

  it("shows the recorded reply and another client's turns in an idle open chat", async () => {
    const user = userEvent.setup();
    const bff = new BffFixture(session("chat-a"));
    startPollingClock();
    await mountConnectedWorkspace(bff, "chat-a");
    bff.rows.set("chat-a", { ...session("chat-a"), updatedAt: "2026-09-24T12:00:20.000Z" });
    bff.transcripts.set("chat-a", {
      complete: true,
      messages: [
        {
          delivery: { fireId: "fire-1", kind: "started", scheduleName: "Daily report" },
          images: [],
          role: "user",
          text: "Scheduled prompt",
          toolCalls: [],
        },
        {
          images: [],
          role: "assistant",
          text: "Scheduled answer",
          toolCalls: [{ args: "{}", id: "read-1", name: "Read" }],
        },
        {
          images: [],
          role: "tool",
          text: "",
          toolCalls: [],
          toolResult: { callId: "read-1", content: "File contents", isError: false },
        },
        { images: [], role: "user", text: "Question from elsewhere", toolCalls: [] },
        { images: [], role: "assistant", text: "Answer from elsewhere", toolCalls: [] },
      ],
      sessionId: "chat-a",
    });

    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2);
    expect(await screen.findByText("Scheduled prompt")).toBeTruthy();
    expect(screen.getByText("Scheduled answer")).toBeTruthy();
    expect(screen.getByText("Question from elsewhere")).toBeTruthy();
    expect(screen.getByText("Answer from elsewhere")).toBeTruthy();
    expect(document.querySelectorAll("[data-delivery-note]")).toHaveLength(1);
    expect(screen.getAllByText("Scheduled answer")).toHaveLength(1);
    await user.click(screen.getAllByRole("button", { name: "Chat options" })[0] as HTMLElement);
    await user.click(screen.getByRole("menuitem", { name: "Show Tools" }));
    expect(screen.getByText("Tool: Read")).toBeTruthy();
    expect(screen.getByText("File contents")).toBeTruthy();
  });

  it.each(["unchanged", "missing", "incomplete"])(
    "checks a %s inventory's transcript at the 60-second safety poll",
    async (inventory) => {
      const bff = new BffFixture(session("chat-a"));
      startPollingClock();
      await mountConnectedWorkspace(bff, "chat-a");
      await advanceClock(40_000);
      expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(1);
      if (inventory !== "unchanged") {
        bff.nextReplies.set("/api/v1/sessions", [
          Promise.resolve(
            json({
              complete: inventory !== "incomplete",
              items: inventory === "missing" ? [] : [session("chat-a")],
            }),
          ),
        ]);
      }
      bff.transcripts.set("chat-a", {
        complete: true,
        messages: [
          {
            delivery: { fireId: "short-fire", kind: "completed", scheduleName: "Daily report" },
            images: [],
            role: "user",
            text: "Recorded output",
            toolCalls: [],
          },
          { images: [], role: "assistant", text: "Short fire reply", toolCalls: [] },
        ],
        sessionId: "chat-a",
      });

      await advanceClock(20_000);
      expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2);
      expect(await screen.findByText("Recorded output")).toBeTruthy();
      expect(screen.getByText("Short fire reply")).toBeTruthy();
      expect(document.querySelectorAll("[data-delivery-note]")).toHaveLength(1);
      await advanceClock(20_000);
      expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2);
    },
  );

  it("does not overlap inventory or transcript poll requests", async () => {
    const bff = new BffFixture(session("chat-a"));
    startPollingClock();
    await mountConnectedWorkspace(bff, "chat-a");
    const pendingInventory = heldResponse();
    bff.nextReplies.set("/api/v1/sessions", [pendingInventory.promise]);
    await advanceClock(40_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(2);
    await act(async () =>
      pendingInventory.resolve(json({ complete: true, items: [session("chat-a")] })),
    );

    const pendingTranscript = heldResponse();
    bff.nextReplies.set("/api/v1/sessions/chat-a/transcript", [pendingTranscript.promise]);
    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2);
    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2);
    await act(async () =>
      pendingTranscript.resolve(json({ complete: true, messages: [], sessionId: "chat-a" })),
    );
  });

  it("keeps rendered message anchors unique when a saved turn arrives before old history", async () => {
    const bff = new BffFixture(session("chat-a"));
    bff.transcripts.set("chat-a", {
      complete: true,
      messages: [{ images: [], role: "assistant", text: "Earlier answer", toolCalls: [] }],
      sessionId: "chat-a",
    });
    startPollingClock();
    await mountConnectedWorkspace(bff, "chat-a");
    expect(await screen.findByText("Earlier answer")).toBeTruthy();
    const oldAnchor = screen.getByText("Earlier answer").closest("article")?.id;
    expect(oldAnchor).toBe("chat-message-transcript-0");

    bff.rows.set("chat-a", { ...session("chat-a"), updatedAt: "2026-09-24T12:00:20.000Z" });
    bff.transcripts.set("chat-a", {
      complete: true,
      messages: [
        { images: [], role: "user", text: "Inserted prompt", toolCalls: [] },
        { images: [], role: "assistant", text: "Earlier answer", toolCalls: [] },
      ],
      sessionId: "chat-a",
    });
    await advanceClock(20_000);
    expect(await screen.findByText("Inserted prompt")).toBeTruthy();
    expect(screen.getByText("Earlier answer").closest("article")?.id).toBe(oldAnchor);
    const anchors = [...document.querySelectorAll("article[id^='chat-message-']")].map(
      (row) => row.id,
    );
    expect(anchors).toHaveLength(2);
    expect(new Set(anchors).size).toBe(anchors.length);
  });

  it("stops inventory polling while hidden", async () => {
    const bff = new BffFixture(session("chat-a"));
    startPollingClock();
    await mountConnectedWorkspace(bff, "chat-a");
    expect(vi.getTimerCount()).toBe(1);
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    expect(vi.getTimerCount()).toBe(0);
    await advanceClock(40_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(1);
  });

  it("stops inventory polling after leaving chat", async () => {
    const bff = new BffFixture(session("chat-a"));
    startPollingClock();
    const { router } = await mountConnectedWorkspace(bff, "chat-a");
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => {
      router.history.push("/away");
      await router.load();
    });
    expect(screen.getByText("Outside chat")).toBeTruthy();
    expect(vi.getTimerCount()).toBe(0);
    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(1);
  });

  it("stops inventory polling when the runtime goes offline", async () => {
    const bff = new BffFixture(session("chat-a"));
    startPollingClock();
    await mountConnectedWorkspace(bff, "chat-a");
    expect(vi.getTimerCount()).toBe(1);
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    bff.runtimeConnection = "offline";
    await advanceClock(20_000);
    await setVisibility("visible");
    expect(bff.requestsAt("GET", "/api/v1/runtime")).toHaveLength(2);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(2);
    expect(await screen.findByText("Mecatl is offline.")).toBeTruthy();
    expect(vi.getTimerCount()).toBe(0);
    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(2);
  });

  it("waits for runtime, inventory, and detail before showing one eight-second return notice", async () => {
    const bff = new BffFixture(session("chat-a"));
    await mountConnectedWorkspace(bff, "chat-a");
    startClock();
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    await advanceClock(20_000);
    const runtime = heldResponse();
    const inventory = heldResponse();
    const detail = heldResponse();
    bff.nextReplies.set("/api/v1/runtime", [runtime.promise]);
    bff.nextReplies.set("/api/v1/sessions", [inventory.promise]);
    bff.nextReplies.set("/api/v1/sessions/chat-a", [detail.promise]);

    await setVisibility("visible");
    expect(bff.requestsAt("GET", "/api/v1/runtime")).toHaveLength(2);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(2);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a")).toHaveLength(2);
    expect(screen.queryByText("This chat is idle.")).toBeNull();

    await act(async () => runtime.resolve(runtimeResponse()));
    expect(screen.queryByText("This chat is idle.")).toBeNull();
    await act(async () =>
      inventory.resolve(json({ complete: true, items: [...bff.rows.values()] })),
    );
    expect(screen.queryByText("This chat is idle.")).toBeNull();
    await act(async () => detail.resolve(detailResponse("chat-a")));
    expect(screen.getByText("This chat is idle.")).toBeTruthy();

    await advanceClock(7_999);
    expect(screen.getByText("This chat is idle.")).toBeTruthy();
    await advanceClock(1);
    expect(screen.queryByText("This chat is idle.")).toBeNull();
    await setVisibility("visible");
    expect(screen.queryByText("This chat is idle.")).toBeNull();
  });

  it("suppresses a return notice after a failed inventory refresh", async () => {
    const bff = new BffFixture(session("chat-a"));
    await mountConnectedWorkspace(bff, "chat-a");
    startClock();
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    await advanceClock(20_000);
    const inventory = heldResponse();
    bff.nextReplies.set("/api/v1/sessions", [inventory.promise]);

    await setVisibility("visible");
    expect(bff.requestsAt("GET", "/api/v1/runtime")).toHaveLength(2);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(2);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a")).toHaveLength(2);
    await act(async () => inventory.resolve(json({ detail: "unavailable", status: 503 }, 503)));
    expect(screen.queryByText("This chat is idle.")).toBeNull();
    expect(screen.queryByText("Mecatl is offline.")).toBeNull();
    await setVisibility("visible");
    expect(screen.queryByText("This chat is idle.")).toBeNull();
  });

  it("suppresses a stale return when visibility flips again before refresh completes", async () => {
    const bff = new BffFixture(session("chat-a"));
    await mountConnectedWorkspace(bff, "chat-a");
    startClock();
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    await advanceClock(20_000);
    const runtime = heldResponse();
    const inventory = heldResponse();
    const detail = heldResponse();
    bff.nextReplies.set("/api/v1/runtime", [runtime.promise]);
    bff.nextReplies.set("/api/v1/sessions", [inventory.promise]);
    bff.nextReplies.set("/api/v1/sessions/chat-a", [detail.promise]);

    await setVisibility("visible");
    await setVisibility("hidden");
    await act(async () => {
      runtime.resolve(runtimeResponse());
      inventory.resolve(json({ complete: true, items: [...bff.rows.values()] }));
      detail.resolve(detailResponse("chat-a"));
    });
    expect(screen.queryByText("This chat is idle.")).toBeNull();
    await advanceClock(1_000);
    await setVisibility("visible");
    expect(screen.queryByText("This chat is idle.")).toBeNull();
  });
});
