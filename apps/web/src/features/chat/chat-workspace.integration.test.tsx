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
import { getRuntimeOptions, listSessionsQueryKey } from "@mecatl-studio/contracts/query";
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

function frame(value: RunStreamEvent, cursor?: string): Uint8Array {
  return new TextEncoder().encode(
    `${cursor ? `id: ${cursor}\n` : ""}data: ${JSON.stringify(value)}\n\n`,
  );
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
    send: (value: RunStreamEvent, cursor?: string) => writer.enqueue(frame(value, cursor)),
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

async function mountWorkspace(bff: BffFixture, sessionId?: string, expectSeed = false) {
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
  if (expectSeed) {
    await screen.findByRole("dialog", { name: "Send this prompt?" });
  } else {
    await screen.findByRole("textbox", { name: "Message Mecatl" });
  }
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
  window.history.replaceState(window.history.state, "", "/");
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("mounted chat workspace BFF boundary", () => {
  it("does not treat a broken continuation as the earlier authorization park", async () => {
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => {
      activity.send(runStarted());
      activity.send(
        runEvent("authorization.required", "1", "", "run-a", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
      );
      activity.send(
        runEvent("authorization.resolved", "2", "", "", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "granted",
        }),
      );
      activity.send(runStarted("chat-a", "run-next"));
      activity.close();
    });
    await waitFor(() => expect(screen.getByText(/Unknown outcome/)).toBeTruthy());
    expect(screen.queryByText("Waiting for authorization")).toBeNull();
  });

  it("keeps an observed authorization on its call through review and continuation", async () => {
    window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    const recheck = heldStream();
    const recheckPath = "/api/v1/sessions/chat-a/authorizations/auth-1/recheck";
    bff.nextReplies.set(recheckPath, [Promise.resolve(recheck.response)]);
    bff.nextReplies.set("/api/v1/sessions/chat-a/runs/run-next/permissions/ask-next", [
      Promise.resolve(new Response(null, { status: 204 })),
    ]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => {
      activity.send(runStarted());
      activity.send(
        runEvent("tool.call", "1", "", "run-a", {
          args: "{}",
          id: "call-1",
          name: "Calendar",
        }),
      );
      activity.send(
        runEvent("authorization.required", "2", "", "run-a", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
        "before-control",
      );
      activity.close();
    });
    await waitFor(() => expect(screen.getAllByText("Waiting for authorization")).toHaveLength(1));
    expect(screen.queryByText(/Unknown outcome/)).toBeNull();
    const tool = (await screen.findByText("Tool: Calendar")).closest("li");
    if (!tool) throw new Error("The tool call row is missing");
    fireEvent.click(within(tool).getByRole("button", { name: "Review authorization" }));
    const review = screen.getByRole("complementary", { name: "Authorization review" });
    expect(
      within(review).getByRole("link", { name: "Open authorization" }).getAttribute("href"),
    ).toBe("/api/v1/sessions/chat-a/authorizations/auth-1/presentation");
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    fireEvent.click(within(review).getByRole("button", { name: "Cancel authorization" }));
    await waitFor(() => expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1));
    expect(
      bff.requestsAt("POST", "/api/v1/sessions/chat-a/authorizations/auth-1/cancel"),
    ).toHaveLength(0);

    await act(async () => {
      recheck.send(
        runEvent("authorization.resolved", "3", "", "", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "granted",
        }),
      );
      recheck.send(runStarted("chat-a", "run-next"));
      recheck.send(
        runEvent("permission.ask", "4", "", "run-next", {
          askId: "ask-next",
          args: "{}",
          reason: "Proceed",
          tool: "Read",
        }),
      );
    });
    await waitFor(() => expect(within(review).getByText("Access granted")).toBeTruthy());
    expect(screen.getByText("Waiting for approval")).toBeTruthy();
    expect(within(review).queryByRole("link", { name: "Open authorization" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Deny" }));
    await waitFor(() =>
      expect(
        bff.requestsAt("POST", "/api/v1/sessions/chat-a/runs/run-next/permissions/ask-next"),
      ).toHaveLength(1),
    );
    await act(async () => {
      recheck.send(runEvent("approval", "5", "", "run-next", { askId: "ask-next" }));
      recheck.send(runEvent("result", "6", "", "run-next", { stop: "end_turn" }));
      recheck.close();
    });
    fireEvent.click(within(review).getByRole("button", { name: "Close preview" }));
    expect(
      bff.requestsAt("POST", "/api/v1/sessions/chat-a/authorizations/auth-1/cancel"),
    ).toHaveLength(0);
  });

  it("cancels a pending authorization from the mounted review without sending a permission verdict", async () => {
    window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const activity = heldStream();
    const cancel = heldStream();
    const cancelPath = "/api/v1/sessions/chat-a/authorizations/auth-1/cancel";
    bff.activityResponses.set("", [activity.response]);
    bff.nextReplies.set(cancelPath, [Promise.resolve(cancel.response)]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => {
      activity.send(runStarted());
      activity.send(
        runEvent("tool.call", "1", "", "run-a", { args: "{}", id: "call-1", name: "Calendar" }),
      );
      activity.send(
        runEvent("authorization.required", "2", "", "run-a", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
        "before-control",
      );
      activity.close();
    });
    const row = (await screen.findByText("Tool: Calendar")).closest("li");
    if (!row) throw new Error("Tool row missing");
    fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
    const review = screen.getByRole("complementary", { name: "Authorization review" });
    fireEvent.click(within(review).getByRole("button", { name: "Cancel authorization" }));
    await waitFor(() => expect(bff.requestsAt("POST", cancelPath)).toHaveLength(1));
    expect(bff.requestsAt("POST", cancelPath)[0]?.body).toBeUndefined();
    await act(async () => {
      cancel.send(
        runEvent("authorization.resolved", "3", "", "run-a", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "cancelled",
        }),
      );
      cancel.send(runEvent("result", "4", "", "run-a", { stop: "cancelled" }));
      cancel.close();
    });
    await waitFor(() => expect(within(review).getByText("Authorization cancelled")).toBeTruthy());
    expect(
      bff.requests.filter(
        (request) => request.method === "POST" && request.pathname.includes("/permissions/"),
      ),
    ).toHaveLength(0);
    expect(bff.requestsAt("POST", cancelPath)).toHaveLength(1);
  });

  it.each(["run.error", "no status", "status then run.error and result"])(
    "keeps authorization controls uncertain after %s",
    async (outcome) => {
      window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
      const bff = new BffFixture(session("chat-a", "authorizing"));
      const activity = heldStream();
      bff.activityResponses.set("", [activity.response]);
      const control = heldStream();
      const recheckPath = "/api/v1/sessions/chat-a/authorizations/auth-1/recheck";
      const cancelPath = "/api/v1/sessions/chat-a/authorizations/auth-1/cancel";
      bff.nextReplies.set(recheckPath, [Promise.resolve(control.response)]);
      await mountConnectedWorkspace(bff, "chat-a");
      await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
      await act(async () => {
        activity.send(runStarted());
        activity.send(
          runEvent("tool.call", "1", "", "run-a", { args: "{}", id: "call-1", name: "Calendar" }),
        );
        activity.send(
          runEvent("authorization.required", "2", "", "run-a", {
            authorizationId: "auth-1",
            callId: "call-1",
            displayName: "Calendar connector",
            status: "pending",
          }),
          "before-control",
        );
        activity.close();
      });
      const row = (await screen.findByText("Tool: Calendar")).closest("li");
      if (!row) throw new Error("Tool row missing");
      fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
      const review = screen.getByRole("complementary", { name: "Authorization review" });
      fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
      await waitFor(() => expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1));
      await act(async () => {
        if (outcome === "status then run.error and result") {
          control.send(
            runEvent("authorization.required", "3", "", "run-a", {
              authorizationId: "auth-1",
              callId: "call-1",
              displayName: "Calendar connector",
              status: "pending",
            }),
          );
        }
        if (outcome.includes("run.error"))
          control.send({ type: "run.error", message: "stream failed" } as RunStreamEvent);
        if (outcome === "status then run.error and result")
          control.send(runEvent("result", "4", "", "run-a", { stop: "end_turn" }));
        control.close();
      });
      await waitFor(() => expect(within(review).getByText(/outcome.*uncertain/i)).toBeTruthy());
      fireEvent.click(within(review).getByRole("button", { name: "Cancel authorization" }));
      fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
      expect(bff.requestsAt("POST", cancelPath)).toHaveLength(0);
      expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1);
      fireEvent.click(within(review).getByRole("button", { name: "Close preview" }));
      fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
      const reopened = screen.getByRole("complementary", { name: "Authorization review" });
      expect(
        within(reopened).getByRole("button", { name: "Recheck" }).hasAttribute("disabled"),
      ).toBe(true);
      fireEvent.click(within(reopened).getByRole("button", { name: "Cancel authorization" }));
      expect(bff.requestsAt("POST", cancelPath)).toHaveLength(0);
    },
  );

  it("keeps authorization controls uncertain when the control status belongs to another call", async () => {
    window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    const control = heldStream();
    const recheckPath = "/api/v1/sessions/chat-a/authorizations/auth-1/recheck";
    const cancelPath = "/api/v1/sessions/chat-a/authorizations/auth-1/cancel";
    bff.nextReplies.set(recheckPath, [Promise.resolve(control.response)]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => {
      activity.send(runStarted());
      activity.send(
        runEvent("tool.call", "1", "", "run-a", { args: "{}", id: "call-1", name: "Calendar" }),
      );
      activity.send(
        runEvent("authorization.required", "2", "", "run-a", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
        "before-control",
      );
      activity.close();
    });
    const row = (await screen.findByText("Tool: Calendar")).closest("li");
    if (!row) throw new Error("Tool row missing");
    fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
    const review = screen.getByRole("complementary", { name: "Authorization review" });
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1));

    await act(async () => {
      control.send(
        runEvent("authorization.resolved", "3", "", "", {
          authorizationId: "auth-1",
          callId: "another-call",
          displayName: "Other connector",
          status: "granted",
        }),
      );
      control.close();
    });
    await waitFor(() => expect(within(review).getByText(/outcome.*uncertain/i)).toBeTruthy());
    expect(within(review).getByText("Pending authorization")).toBeTruthy();
    expect(within(review).getByRole("button", { name: "Recheck" }).hasAttribute("disabled")).toBe(
      true,
    );
    expect(
      within(review).getByRole("button", { name: "Cancel authorization" }).hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    fireEvent.click(within(review).getByRole("button", { name: "Cancel authorization" }));
    expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1);
    expect(bff.requestsAt("POST", cancelPath)).toHaveLength(0);
  });

  it("reconciles an uncertain pending handoff only after newer durable activity", async () => {
    window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const firstActivity = heldStream();
    const refreshedActivity = heldStream();
    bff.activityResponses.set("", [firstActivity.response]);
    bff.activityResponses.set("cursor-auth-1", [refreshedActivity.response]);
    const failedControl = heldStream();
    const nextControl = heldStream();
    const recheckPath = "/api/v1/sessions/chat-a/authorizations/auth-1/recheck";
    bff.nextReplies.set(recheckPath, [
      Promise.resolve(failedControl.response),
      Promise.resolve(nextControl.response),
    ]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    const required = runEvent("authorization.required", "2", "", "run-a", {
      authorizationId: "auth-1",
      callId: "call-1",
      displayName: "Calendar connector",
      status: "pending",
    });
    await act(async () => {
      firstActivity.send(runStarted());
      firstActivity.send(
        runEvent("tool.call", "1", "", "run-a", {
          args: "{}",
          id: "call-1",
          name: "Calendar",
        }),
      );
      firstActivity.send(required, "cursor-auth-1");
      firstActivity.close();
    });
    const row = (await screen.findByText("Tool: Calendar")).closest("li");
    if (!row) throw new Error("Tool row missing");
    fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
    const review = screen.getByRole("complementary", { name: "Authorization review" });
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1));
    await act(async () => {
      failedControl.send({ type: "run.error", message: "response lost" } as RunStreamEvent);
      failedControl.close();
    });
    await waitFor(() => expect(within(review).getByRole("alert")).toBeTruthy());
    fireEvent.click(within(review).getByRole("button", { name: "Refresh activity" }));
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(2));
    expect(bff.requestsFor("GET", "/activity")[1]?.search).toBe("?resumeFrom=cursor-auth-1");
    expect(screen.getByText("Tool: Calendar")).toBeTruthy();

    expect(within(review).getByRole("button", { name: "Recheck" }).hasAttribute("disabled")).toBe(
      true,
    );
    expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1);

    await act(async () => {
      refreshedActivity.send(
        runEvent("authorization.resolved", "0", "", "", {
          authorizationId: "auth-1",
          callId: "another-call",
          displayName: "Other connector",
          status: "granted",
        }),
      );
    });
    expect(within(review).getByRole("button", { name: "Recheck" }).hasAttribute("disabled")).toBe(
      true,
    );

    await act(async () => {
      refreshedActivity.send(
        runEvent("authorization.required", "0", "", "", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
      );
    });
    await waitFor(() =>
      expect(within(review).getByRole("button", { name: "Recheck" }).hasAttribute("disabled")).toBe(
        false,
      ),
    );
    expect(within(review).queryByRole("alert")).toBeNull();
    expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1);
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(bff.requestsAt("POST", recheckPath)).toHaveLength(2));
    await act(async () => {
      nextControl.send(
        runEvent("authorization.required", "0", "", "", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
      );
      nextControl.close();
    });
  });

  it("refreshes an uncertain cancelled handoff even after the session becomes idle", async () => {
    window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const firstActivity = heldStream();
    const refreshedActivity = heldStream();
    bff.activityResponses.set("", [firstActivity.response]);
    bff.activityResponses.set("before-cancel", [refreshedActivity.response]);
    const cancel = heldStream();
    const cancelPath = "/api/v1/sessions/chat-a/authorizations/auth-1/cancel";
    bff.nextReplies.set(cancelPath, [Promise.resolve(cancel.response)]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => {
      firstActivity.send(runStarted());
      firstActivity.send(
        runEvent("tool.call", "1", "", "run-a", { args: "{}", id: "call-1", name: "Calendar" }),
      );
      firstActivity.send(
        runEvent("authorization.required", "2", "", "run-a", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
        "before-cancel",
      );
      firstActivity.close();
    });
    const row = (await screen.findByText("Tool: Calendar")).closest("li");
    if (!row) throw new Error("Tool row missing");
    fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
    const review = screen.getByRole("complementary", { name: "Authorization review" });
    fireEvent.click(within(review).getByRole("button", { name: "Cancel authorization" }));
    await waitFor(() => expect(bff.requestsAt("POST", cancelPath)).toHaveLength(1));
    bff.rows.set("chat-a", session("chat-a", "idle"));
    await act(async () => {
      cancel.send({ type: "run.error", message: "response lost" } as RunStreamEvent);
      cancel.close();
    });
    await waitFor(() => expect(within(review).getByRole("alert")).toBeTruthy());
    fireEvent.click(within(review).getByRole("button", { name: "Refresh activity" }));
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(2));
    expect(bff.requestsFor("GET", "/activity")[1]?.search).toBe("?resumeFrom=before-cancel");
    await act(async () => {
      refreshedActivity.send(
        runEvent("authorization.resolved", "0", "", "", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "cancelled",
        }),
      );
      refreshedActivity.close();
    });
    await waitFor(() => expect(within(review).getByText("Authorization cancelled")).toBeTruthy());
    expect(within(review).queryByRole("alert")).toBeNull();
    expect(bff.requestsAt("POST", cancelPath)).toHaveLength(1);
  });

  it("checkpoints a cursorless handoff before control and keeps failed checkpoints actionable", async () => {
    window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const firstActivity = heldStream();
    const missingCheckpoint = heldStream();
    const checkpoint = heldStream();
    const refreshedActivity = heldStream();
    bff.activityResponses.set("", [
      firstActivity.response,
      missingCheckpoint.response,
      checkpoint.response,
    ]);
    bff.activityResponses.set("before-recheck", [refreshedActivity.response]);
    const control = heldStream();
    const recheckPath = "/api/v1/sessions/chat-a/authorizations/auth-1/recheck";
    bff.nextReplies.set(recheckPath, [Promise.resolve(control.response)]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    const required = runEvent("authorization.required", "2", "", "run-a", {
      authorizationId: "auth-1",
      callId: "call-1",
      displayName: "Calendar connector",
      status: "pending",
    });
    await act(async () => {
      firstActivity.send(runStarted());
      firstActivity.send(
        runEvent("tool.call", "1", "", "run-a", { args: "{}", id: "call-1", name: "Calendar" }),
      );
      firstActivity.send(required);
      firstActivity.close();
    });
    const row = (await screen.findByText("Tool: Calendar")).closest("li");
    if (!row) throw new Error("Tool row missing");
    fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
    const review = screen.getByRole("complementary", { name: "Authorization review" });
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(2));
    expect(bff.requestsAt("POST", recheckPath)).toHaveLength(0);
    await act(async () => {
      missingCheckpoint.send(required);
      missingCheckpoint.close();
    });
    await waitFor(() =>
      expect(within(review).getByRole("button", { name: "Recheck" }).hasAttribute("disabled")).toBe(
        false,
      ),
    );
    expect(bff.requestsAt("POST", recheckPath)).toHaveLength(0);
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(3));
    await act(async () => {
      checkpoint.send(
        runEvent("authorization.required", "2", "", "run-a", {
          authorizationId: "auth-1",
          callId: "another-call",
          displayName: "Other connector",
          status: "pending",
        }),
        "other-handoff",
      );
    });
    expect(bff.requestsAt("POST", recheckPath)).toHaveLength(0);
    await act(async () => {
      checkpoint.send(required, "before-recheck");
      checkpoint.close();
    });
    await waitFor(() => expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1));
    await act(async () => {
      control.send({ type: "run.error", message: "response lost" } as RunStreamEvent);
      control.close();
    });
    await waitFor(() => expect(within(review).getByRole("alert")).toBeTruthy());
    fireEvent.click(within(review).getByRole("button", { name: "Refresh activity" }));
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(4));
    expect(bff.requestsFor("GET", "/activity")[3]?.search).toBe("?resumeFrom=before-recheck");
    await act(async () => {
      refreshedActivity.send(
        runEvent("authorization.required", "0", "", "", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
      );
    });
    await waitFor(() =>
      expect(within(review).getByRole("button", { name: "Recheck" }).hasAttribute("disabled")).toBe(
        false,
      ),
    );
    expect(within(review).queryByRole("alert")).toBeNull();
  });

  it("does not duplicate continuation rows when refresh replays a failed control stream", async () => {
    window.localStorage.setItem("studio.profile.show-tool-calls", "visible");
    const bff = new BffFixture(session("chat-a", "authorizing"));
    const firstActivity = heldStream();
    const refreshedActivity = heldStream();
    bff.activityResponses.set("", [firstActivity.response]);
    bff.activityResponses.set("before-control", [refreshedActivity.response]);
    const control = heldStream();
    const recheckPath = "/api/v1/sessions/chat-a/authorizations/auth-1/recheck";
    bff.nextReplies.set(recheckPath, [Promise.resolve(control.response)]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => {
      firstActivity.send(runStarted());
      firstActivity.send(
        runEvent("tool.call", "1", "", "run-a", { args: "{}", id: "call-1", name: "Calendar" }),
      );
      firstActivity.send(
        runEvent("authorization.required", "2", "", "run-a", {
          authorizationId: "auth-1",
          callId: "call-1",
          displayName: "Calendar connector",
          status: "pending",
        }),
        "before-control",
      );
      firstActivity.close();
    });
    const row = (await screen.findByText("Tool: Calendar")).closest("li");
    if (!row) throw new Error("Tool row missing");
    fireEvent.click(within(row).getByRole("button", { name: "Review authorization" }));
    const review = screen.getByRole("complementary", { name: "Authorization review" });
    fireEvent.click(within(review).getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(bff.requestsAt("POST", recheckPath)).toHaveLength(1));
    const pending = runEvent("authorization.required", "0", "", "", {
      authorizationId: "auth-1",
      callId: "call-1",
      displayName: "Calendar connector",
      status: "pending",
    });
    const firstDelta = runEvent("message.delta", "2", "First", "run-b");
    const tool = runEvent("tool.call", "3", "", "run-b", {
      args: "{}",
      id: "call-2",
      name: "Read",
    });
    await act(async () => {
      control.send(pending);
      control.send(runStarted("chat-a", "run-b"));
      control.send(firstDelta);
      control.send(tool);
      control.send({ type: "run.error", message: "response lost" } as RunStreamEvent);
      control.close();
    });
    await waitFor(() => expect(within(review).getByRole("alert")).toBeTruthy());
    expect(screen.getAllByText("First")).toHaveLength(1);
    fireEvent.click(within(review).getByRole("button", { name: "Refresh activity" }));
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(2));
    await act(async () => {
      refreshedActivity.send(pending);
      refreshedActivity.send(runStarted("chat-a", "run-b"));
      refreshedActivity.send(firstDelta);
      refreshedActivity.send(tool);
      refreshedActivity.send(runEvent("message.delta", "4", " answer", "run-b"));
    });
    await waitFor(() => expect(screen.getAllByText("First answer")).toHaveLength(1));
    expect(screen.queryByText("First")).toBeNull();
    expect(screen.getAllByText("Tool: Read")).toHaveLength(1);
    expect(screen.getAllByText("Tool: Calendar")).toHaveLength(1);
  });

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
    const user = userEvent.setup();
    const bff = new BffFixture(session("chat-a", "running"));
    bff.transcripts.set("chat-a", {
      complete: true,
      messages: [
        { images: [], role: "user", text: "first prompt", toolCalls: [] },
        {
          images: [],
          role: "assistant",
          text: "first answer",
          toolCalls: [{ args: "{}", id: "read-1", name: "Read" }],
        },
        {
          images: [],
          role: "tool",
          text: "",
          toolCalls: [],
          toolResult: { callId: "read-1", content: "File contents", isError: false },
        },
        { images: [], role: "user", text: "second prompt", toolCalls: [] },
        { images: [], role: "assistant", text: "second answer", toolCalls: [] },
      ],
      sessionId: "chat-a",
    });
    const continuation = heldStream();
    bff.activityResponses.set("", [
      completedStream(
        runEvent("user_prompt", "1", "first prompt"),
        runEvent("message.delta", "2", "first answer"),
        runEvent("tool.call", "3", "", "run-a", { args: "{}", id: "read-1", name: "Read" }),
        runEvent("tool.result", "4", "", "run-a", {
          callId: "read-1",
          content: "File contents",
          isError: false,
        }),
        { cursor: "cursor-4", reason: "bound", type: "run.truncated" },
      ),
    ]);
    bff.activityResponses.set("cursor-4", [
      completedStream(
        runEvent("tool.result", "4", "", "run-a", {
          callId: "read-1",
          content: "File contents",
          isError: false,
        }),
        runEvent("result", "5", "", "run-a", { stop: "end_turn" }),
        runEvent("user_prompt", "6", "second prompt", "run-b"),
        { cursor: "cursor-6", reason: "bound", type: "run.truncated" },
      ),
    ]);
    bff.activityResponses.set("cursor-6", [continuation.response]);
    await mountWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(3));
    expect(bff.requestsFor("GET", "/activity").map((request) => request.search)).toEqual([
      "",
      "?resumeFrom=cursor-4",
      "?resumeFrom=cursor-6",
    ]);
    await act(async () =>
      continuation.send(runEvent("user_prompt", "6", "second prompt", "run-b")),
    );
    await act(async () =>
      continuation.send(runEvent("message.delta", "7", "second answer", "run-b")),
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
      continuation.send(runEvent("result", "8", "", "run-b", { stop: "end_turn" }));
      continuation.close();
    });
    await waitFor(() => expect(screen.queryByText(/replaying older activity first/i)).toBeNull());
    expect(bff.requestsFor("GET", "/transcript").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("first prompt")).toHaveLength(1);
    expect(screen.getAllByText("first answer")).toHaveLength(1);
    expect(screen.getAllByText("second prompt")).toHaveLength(1);
    expect(screen.getAllByText("second answer")).toHaveLength(1);
    await user.click(screen.getAllByRole("button", { name: "Chat options" })[0] as HTMLElement);
    await user.click(screen.getByRole("menuitem", { name: "Show Tools" }));
    expect(screen.getAllByText("Tool: Read")).toHaveLength(1);
    expect(screen.getAllByText("File contents")).toHaveLength(1);
  });

  it("keeps another client's image before an uploaded image-only prompt after refresh", async () => {
    const user = userEvent.setup();
    const bff = new BffFixture(session("chat-a"));
    bff.nextReplies.set("/api/v1/sessions/chat-a", [
      Promise.resolve(
        detailResponse("chat-a", "idle", {
          capabilities: { image: true, manualCompaction: false, modelSelection: false },
        }),
      ),
    ]);
    const run = heldStream();
    bff.runResponses.push(run.response);
    startPollingClock();
    await mountConnectedWorkspace(bff, "chat-a");
    await user.upload(
      await screen.findByLabelText("Choose images to attach"),
      new File(["image data"], "picture.png", { type: "image/png" }),
    );
    await waitFor(() => expect(screen.getByText("picture.png")).toBeTruthy());
    expect(
      (screen.getByRole("button", { name: "Send message" }) as HTMLButtonElement).disabled,
    ).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    expect(screen.getAllByRole("button", { name: "Preview picture.png" })).toHaveLength(2);

    bff.transcripts.set("chat-a", {
      complete: true,
      messages: [
        {
          images: [
            {
              data: "aW1hZ2UgZnJvbSBhbm90aGVyIGNsaWVudA==",
              mimeType: "image/png",
              name: "Image 1",
            },
          ],
          role: "user",
          text: "",
          toolCalls: [],
        },
        {
          images: [{ data: "aW1hZ2UgZGF0YQ==", mimeType: "image/png", name: "Image 1" }],
          role: "user",
          text: "",
          toolCalls: [],
        },
        { images: [], role: "assistant", text: "A picture", toolCalls: [] },
      ],
      sessionId: "chat-a",
    });
    await act(async () => {
      run.send(runStarted());
      run.send(runEvent("message.delta", "1", "A picture"));
      run.send(runEvent("result", "2", "", "run-a", { stop: "end_turn" }));
      run.close();
    });
    await waitFor(() =>
      expect(bff.requestsAt("POST", "/api/v1/sessions/chat-a/runs")).toHaveLength(1),
    );
    expect(bff.requestsAt("POST", "/api/v1/sessions/chat-a/runs")[0]?.body).toEqual({
      images: [{ data: "aW1hZ2UgZGF0YQ==", mimeType: "image/png", name: "picture.png" }],
      prompt: "",
    });
    await waitFor(() =>
      expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(2),
    );
    await waitFor(() =>
      expect(screen.getAllByRole("article", { name: "You message" })).toHaveLength(2),
    );
    expect(screen.getAllByText("A picture")).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Preview picture.png" })).toHaveLength(1);
    const userRows = screen.getAllByRole("article", { name: "You message" });
    expect(userRows).toHaveLength(2);
    expect(
      within(userRows[0] as HTMLElement).getByRole("button", { name: "Preview Image 1" }),
    ).toBeTruthy();
    expect(
      within(userRows[1] as HTMLElement).getByRole("button", { name: "Preview picture.png" }),
    ).toBeTruthy();

    bff.rows.set("chat-a", { ...session("chat-a"), updatedAt: "2026-09-24T12:00:20.000Z" });
    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions/chat-a/transcript")).toHaveLength(3);
    expect(screen.getAllByText("A picture")).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Preview picture.png" })).toHaveLength(1);
    const refreshedRows = screen.getAllByRole("article", { name: "You message" });
    expect(refreshedRows).toHaveLength(2);
    expect(
      within(refreshedRows[0] as HTMLElement).getByRole("button", { name: "Preview Image 1" }),
    ).toBeTruthy();
    expect(
      within(refreshedRows[1] as HTMLElement).getByRole("button", { name: "Preview picture.png" }),
    ).toBeTruthy();
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

  it.each(["offline", "busy"] as const)(
    "keeps an arrival seed inert in a mounted %s workspace",
    async (availability) => {
      const bff = new BffFixture(session("chat-a", availability === "busy" ? "running" : "idle"));
      const activity = heldStream();
      if (availability === "busy") bff.activityResponses.set("", [activity.response]);
      if (availability === "offline") bff.runtimeConnection = "offline";
      window.history.replaceState(
        window.history.state,
        "",
        "/workspace/chat?sessionId=chat-a&prompt=Review%20the%20change&send=1",
      );
      const mounted = await mountWorkspace(bff, "chat-a", true);
      const confirmation = await screen.findByRole("dialog", { name: "Send this prompt?" });
      await waitFor(() =>
        expect(mounted.queryClient.getQueryData(getRuntimeOptions().queryKey)).toMatchObject({
          connection: availability === "offline" ? "offline" : "online",
        }),
      );
      if (availability === "busy") {
        await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
        await act(async () => activity.send(runStarted()));
      }

      expect(within(confirmation).getByText("Chat chat-a")).toBeTruthy();
      expect(within(confirmation).getByText("Manual")).toBeTruthy();
      expect(within(confirmation).getByText("Review the change")).toBeTruthy();
      const send = within(confirmation).getByRole("button", { name: "Send prompt" });
      expect(send.hasAttribute("disabled")).toBe(true);
      fireEvent.click(send);
      expect(bff.requestsFor("POST", "/runs")).toHaveLength(0);
      expect(bff.requestsAt("POST", "/api/v1/sessions")).toHaveLength(0);
      expect(window.location.search).toBe("?sessionId=chat-a");
      fireEvent.click(within(confirmation).getByRole("button", { name: "Edit prompt" }));
      expect(
        (screen.getByRole("textbox", { name: "Message Mecatl" }) as HTMLTextAreaElement).value,
      ).toBe("Review the change");
      if (availability === "busy") {
        expect(screen.getByRole("button", { name: "Stop" })).toBeTruthy();
      }
    },
  );

  it("uses fresh inventory to replace a legacy title with revision zero", async () => {
    const bff = new BffFixture(session("chat-a"));
    window.localStorage.setItem(
      "studio.chat.folders",
      JSON.stringify({
        assignments: { "chat-a": "project-folder" },
        folders: [{ id: "project-folder", name: "Project" }],
      }),
    );
    startPollingClock();
    const mounted = await mountConnectedWorkspace(bff, "chat-a");
    expect(screen.getByRole("heading", { name: "Chat chat-a" })).toBeTruthy();
    expect(screen.getAllByText("Project").length).toBeGreaterThan(0);
    bff.rows.set("chat-a", { ...session("chat-a"), title: "Renamed legacy title" });

    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions")).toHaveLength(2);
    expect(mounted.queryClient.getQueryData(listSessionsQueryKey())).toMatchObject({
      items: [{ title: "Renamed legacy title" }],
    });
    expect(await screen.findByRole("heading", { name: "Renamed legacy title" })).toBeTruthy();
    expect(screen.getAllByText("Renamed legacy title")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Renamed legacy title idle" })).toBeTruthy();
  });

  it("keeps a rename response and newer title event over stale inventory in the header and folder", async () => {
    const user = userEvent.setup();
    const original = {
      ...session("chat-a", "running"),
      title: "Original title",
      titleProvenance: "generated",
      titleRevision: "3",
    };
    const bff = new BffFixture(original);
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    window.localStorage.setItem(
      "studio.chat.folders",
      JSON.stringify({
        assignments: { "chat-a": "project-folder" },
        folders: [{ id: "project-folder", name: "Project" }],
      }),
    );
    startPollingClock();
    await mountWorkspace(bff, "chat-a");
    await screen.findByRole("heading", { name: "Original title" });
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));

    bff.nextReplies.set("/api/v1/sessions/chat-a", [
      Promise.resolve(
        json({ title: "Operator title", titleProvenance: "operator", titleRevision: "5" }),
      ),
    ]);
    await user.click(screen.getAllByRole("button", { name: "Chat options" })[0] as HTMLElement);
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    const renameDialog = screen.getByRole("dialog", { name: "Rename chat" });
    await user.clear(within(renameDialog).getByRole("textbox", { name: "Chat name" }));
    await user.type(
      within(renameDialog).getByRole("textbox", { name: "Chat name" }),
      "Operator title",
    );
    await user.click(within(renameDialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(bff.requestsAt("PATCH", "/api/v1/sessions/chat-a")).toHaveLength(1));
    expect(await screen.findByRole("heading", { name: "Operator title" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Operator title running" })).toBeTruthy();

    await act(async () =>
      activity.send(
        runEvent("session.title", "2", "", "run-a", {
          provenance: "generated",
          revision: "4",
          title: "Late old generation",
        }),
      ),
    );
    expect(screen.getByRole("heading", { name: "Operator title" })).toBeTruthy();
    await act(async () =>
      activity.send(
        runEvent("session.title", "3", "", "run-a", {
          provenance: "generated",
          revision: "6",
          title: "Newer event title",
        }),
      ),
    );
    expect(await screen.findByRole("heading", { name: "Newer event title" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Newer event title running" })).toBeTruthy();

    await advanceClock(20_000);
    expect(bff.requestsAt("GET", "/api/v1/sessions").length).toBeGreaterThanOrEqual(3);
    expect(screen.getByRole("heading", { name: "Newer event title" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Newer event title running" })).toBeTruthy();
    expect(screen.queryByText("Late old generation")).toBeNull();
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

  it("reports a result delivered to this tab while it was hidden", async () => {
    const bff = new BffFixture(session("chat-a", "running"));
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => activity.send(runStarted()));
    startClock();
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    await advanceClock(20_000);
    bff.rows.set("chat-a", session("chat-a"));
    await act(async () =>
      activity.send(runEvent("result", "2", "", "run-a", { stop: "end_turn" })),
    );
    await setVisibility("visible");
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByText("The run finished while you were away.")).toBeTruthy();
  });

  it("does not attribute an approval received after return to the hidden interval", async () => {
    const bff = new BffFixture(session("chat-a", "awaiting"));
    const activity = heldStream();
    bff.activityResponses.set("", [activity.response]);
    await mountConnectedWorkspace(bff, "chat-a");
    await waitFor(() => expect(bff.requestsFor("GET", "/activity")).toHaveLength(1));
    await act(async () => activity.send(runStarted()));
    startClock();
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    await advanceClock(20_000);
    bff.rows.set("chat-a", session("chat-a"));
    const runtime = heldResponse();
    const inventory = heldResponse();
    const detail = heldResponse();
    bff.nextReplies.set("/api/v1/runtime", [runtime.promise]);
    bff.nextReplies.set("/api/v1/sessions", [inventory.promise]);
    bff.nextReplies.set("/api/v1/sessions/chat-a", [detail.promise]);

    await setVisibility("visible");
    await act(async () =>
      activity.send(runEvent("approval", "2", "", "run-a", { askId: "ask-1" })),
    );
    await act(async () => {
      runtime.resolve(runtimeResponse());
      inventory.resolve(json({ complete: true, items: [...bff.rows.values()] }));
      detail.resolve(detailResponse("chat-a"));
    });
    expect(screen.getByText("This chat is idle.")).toBeTruthy();
    expect(screen.queryByText("An approval was resolved while you were away.")).toBeNull();
  });

  it("measures hidden time at return before a slow refresh", async () => {
    const bff = new BffFixture(session("chat-a"));
    await mountConnectedWorkspace(bff, "chat-a");
    startClock();
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    await advanceClock(19_999);
    const runtime = heldResponse();
    const inventory = heldResponse();
    const detail = heldResponse();
    bff.nextReplies.set("/api/v1/runtime", [runtime.promise]);
    bff.nextReplies.set("/api/v1/sessions", [inventory.promise]);
    bff.nextReplies.set("/api/v1/sessions/chat-a", [detail.promise]);

    await setVisibility("visible");
    await advanceClock(1);
    await act(async () => {
      runtime.resolve(runtimeResponse());
      inventory.resolve(json({ complete: true, items: [...bff.rows.values()] }));
      detail.resolve(detailResponse("chat-a"));
    });
    expect(screen.queryByText("This chat is idle.")).toBeNull();
  });

  it("shows a verified offline notice when chat refreshes fail", async () => {
    const bff = new BffFixture(session("chat-a"));
    await mountConnectedWorkspace(bff, "chat-a");
    startClock();
    const setVisibility = controlVisibility();
    await setVisibility("hidden");
    await advanceClock(20_000);
    bff.runtimeConnection = "offline";
    bff.nextReplies.set("/api/v1/sessions", [Promise.resolve(json({ status: 503 }, 503))]);
    bff.nextReplies.set("/api/v1/sessions/chat-a", [Promise.resolve(json({ status: 503 }, 503))]);

    await setVisibility("visible");
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByText("Mecatl is offline.")).toBeTruthy();
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
