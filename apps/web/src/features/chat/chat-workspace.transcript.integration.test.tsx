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

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

function frame(value: RunStreamEvent): Uint8Array {
  return new TextEncoder().encode(`data: ${JSON.stringify(value)}\n\n`);
}

function completedStream(events: RunStreamEvent[]): Response {
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const event of events) controller.enqueue(frame(event));
        controller.close();
      },
    }),
    { headers: { "Content-Type": "text/event-stream" } },
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
    response: new Response(body, { headers: { "Content-Type": "text/event-stream" } }),
    send: (value: RunStreamEvent) => writer.enqueue(frame(value)),
  };
}

function runEvent(kind: string, seq: number, text = "", payload?: unknown): RunStreamEvent {
  return {
    event: { kind, payload, runId: "run-a", seq: String(seq), text, turn: 1, unknown: false },
    type: "run.event",
  } as RunStreamEvent;
}

const session = {
  capabilities: {
    copyId: true,
    copyIdReason: "",
    delete: true,
    deleteReason: "",
    fork: true,
    forkReason: "",
    inspect: true,
    inspectReason: "",
    publicChat: true,
    publicChatReason: "",
    rename: true,
    renameReason: "",
    viewTranscript: true,
    viewTranscriptReason: "",
  },
  createdAt: "2026-09-24T12:00:00.000Z",
  debugTargetSessionId: "",
  id: "chat-a",
  kind: "main",
  modelId: "test-model",
  state: "running",
  title: "Chat chat-a",
  titleProvenance: "",
  titleRevision: "0",
  turns: 0,
  updatedAt: "2026-09-24T12:00:00.000Z",
};

class ActivityFixture {
  readonly requests: { method: string; pathname: string; search: string }[] = [];
  readonly responses = new Map<string, Response[]>();

  readonly fetch = async (request: Request): Promise<Response> => {
    const url = new URL(request.url);
    const pathname = decodeURIComponent(url.pathname);
    this.requests.push({ method: request.method, pathname, search: url.search });
    if (request.method === "GET" && pathname === "/api/v1/runtime") {
      return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
    }
    if (request.method === "GET" && pathname === "/api/v1/settings/runtime") {
      return json({ models: [], modelsSupported: true });
    }
    if (request.method === "GET" && pathname === "/api/v1/sessions") {
      return json({ complete: true, items: [session] });
    }
    if (request.method === "GET" && pathname === "/api/v1/sessions/chat-a/transcript") {
      return json({ complete: true, messages: [], sessionId: "chat-a" });
    }
    if (request.method === "GET" && pathname === "/api/v1/sessions/chat-a") {
      return json({
        capabilities: { image: false, manualCompaction: false, modelSelection: false },
        id: "chat-a",
        mode: "default",
        state: "running",
        usage: {
          cacheReadTokens: "0",
          cacheWriteTokens: "0",
          inputTokens: "0",
          outputTokens: "0",
          reasoningTokens: "0",
        },
      });
    }
    if (request.method === "GET" && pathname === "/api/v1/sessions/chat-a/activity") {
      const cursor = url.searchParams.get("resumeFrom") ?? "";
      const response = this.responses.get(cursor)?.shift();
      if (!response) throw new Error(`Unexpected activity cursor: ${cursor}`);
      return response;
    }
    throw new Error(`Unexpected BFF request: ${request.method} ${pathname}`);
  };
}

async function mountWorkspace(bff: ActivityFixture) {
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
    component: () => <ChatWorkspace sessionId="chat-a" />,
    getParentRoute: () => root,
    path: "/workspace/chat",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/chat"] }),
    routeTree: root.addChildren([chat]),
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
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("mounted transcript streaming", () => {
  it("keeps the chosen canvas panel mounted through a live delivery", async () => {
    const bff = new ActivityFixture();
    const activity = heldStream();
    bff.responses.set("", [activity.response]);
    await mountWorkspace(bff);
    fireEvent.click(screen.getByRole("button", { name: "Open local canvas" }));
    const panel = screen.getByRole("complementary", { name: "Local canvas" });
    const body = screen.getByTestId("side-panel-body");
    fireEvent.change(screen.getByRole("textbox", { name: "Local canvas" }), {
      target: { value: "# Saved note" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    const edit = screen.getByRole("button", { name: "Edit" });
    edit.focus();
    body.scrollTop = 64;
    fireEvent.keyDown(screen.getByRole("button", { name: "Resize panel" }), {
      key: "ArrowLeft",
    });
    const width = panel.style.getPropertyValue("--content-panel-width");

    await act(async () => {
      activity.send(runEvent("user_prompt", 1, "A new live turn"));
      activity.send(runEvent("message.delta", 2, "Live answer"));
    });
    expect(await screen.findByText("Live answer")).toBeTruthy();
    expect(screen.getByRole("complementary", { name: "Local canvas" })).toBe(panel);
    expect(screen.getByTestId("side-panel-body")).toBe(body);
    expect(screen.getByText("Saved note")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit" })).toBe(edit);
    expect(document.activeElement).toBe(edit);
    expect(body.scrollTop).toBe(64);
    expect(panel.style.getPropertyValue("--content-panel-width")).toBe(width);
  });

  it("keeps a minimap selection above the bottom during live deltas", async () => {
    const bff = new ActivityFixture();
    const activity = heldStream();
    bff.responses.set("", [activity.response]);
    await mountWorkspace(bff);
    await act(async () => {
      activity.send(runEvent("user_prompt", 1, "A question"));
      activity.send(runEvent("message.delta", 2, "First answer"));
    });
    const scroll = screen.getByRole("region", {
      name: "Conversation transcript",
    }) as HTMLDivElement;
    Object.defineProperties(scroll, {
      clientHeight: { configurable: true, value: 400 },
      scrollHeight: { configurable: true, value: 2000 },
      scrollTop: { configurable: true, value: 900, writable: true },
    });
    scroll.getBoundingClientRect = () => ({ top: 100 }) as DOMRect;
    Object.defineProperty(scroll, "scrollTo", {
      configurable: true,
      value: vi.fn((options: ScrollToOptions) => {
        scroll.scrollTop = Number(options.top);
      }),
    });
    const first = screen.getByText("A question").closest("article");
    if (!first) throw new Error("Live prompt row missing");
    first.getBoundingClientRect = () => ({ top: -750 }) as DOMRect;
    fireEvent.click(screen.getByRole("button", { name: /^Jump to message 1:/u }));
    expect(scroll.scrollTop).toBe(50);
    expect(screen.getByRole("button", { name: "Scroll to latest message" })).toBeTruthy();
    await act(async () => activity.send(runEvent("message.delta", 3, " continues")));
    expect(await screen.findByText("First answer continues")).toBeTruthy();
    expect(scroll.scrollTop).toBe(50);
  });

  it("keeps a scrolled reader in place and resumes following after the jump action", async () => {
    const bff = new ActivityFixture();
    const activity = heldStream();
    bff.responses.set("", [activity.response]);
    await mountWorkspace(bff);
    await act(async () => {
      activity.send(runEvent("user_prompt", 1, "A question"));
      activity.send(runEvent("message.delta", 2, "First answer"));
    });
    const answer = await screen.findByText("First answer");
    const scroll = answer.closest("section.overflow-y-auto") as HTMLElement | null;
    expect(scroll).not.toBeNull();
    if (!scroll) throw new Error("Transcript scroll container is missing");
    let scrollHeight = 2000;
    Object.defineProperties(scroll, {
      clientHeight: { configurable: true, value: 400 },
      scrollHeight: { configurable: true, get: () => scrollHeight },
      scrollTop: { configurable: true, value: 900, writable: true },
    });
    const scrollTo = vi.fn((options: ScrollToOptions) => {
      scroll.scrollTop = Number(options.top);
    });
    Object.defineProperty(scroll, "scrollTo", { configurable: true, value: scrollTo });
    fireEvent.scroll(scroll);
    expect(screen.getByRole("button", { name: "Scroll to latest message" })).toBeTruthy();

    await act(async () => activity.send(runEvent("message.delta", 3, " continues")));
    expect(await screen.findByText("First answer continues")).toBeTruthy();
    expect(scroll.scrollTop).toBe(900);

    fireEvent.click(screen.getByRole("button", { name: "Scroll to latest message" }));
    expect(scrollTo).toHaveBeenCalledWith({ behavior: "smooth", top: 2000 });
    expect(screen.queryByRole("button", { name: "Scroll to latest message" })).toBeNull();
    scrollHeight = 2400;
    await act(async () => activity.send(runEvent("message.delta", 4, " again")));
    expect(await screen.findByText("First answer continues again")).toBeTruthy();
    await waitFor(() => expect(scroll.scrollTop).toBe(2400));
  });

  it("replays more than 2,000 durable events over two bounded cursors, then catches live output", async () => {
    const bff = new ActivityFixture();
    const live = heldStream();
    const firstDeltas = Array.from({ length: 1000 }, (_, index) =>
      runEvent("message.delta", index + 2, "x"),
    );
    const secondDeltas = Array.from({ length: 1001 }, (_, index) =>
      runEvent("message.delta", index + 1002, "x"),
    );
    bff.responses.set("", [
      completedStream([
        runEvent("user_prompt", 1, "Long history"),
        ...firstDeltas,
        { cursor: "after-1001", reason: "bound", type: "run.truncated" },
      ]),
    ]);
    bff.responses.set("after-1001", [
      completedStream([
        runEvent("message.delta", 1001, "x"),
        ...secondDeltas,
        { cursor: "after-2002", reason: "bound", type: "run.truncated" },
      ]),
    ]);
    bff.responses.set("after-2002", [live.response]);
    await mountWorkspace(bff);
    await waitFor(
      () =>
        expect(
          bff.requests
            .filter((request) => request.pathname.endsWith("/activity"))
            .map((request) => request.search),
        ).toEqual(["", "?resumeFrom=after-1001", "?resumeFrom=after-2002"]),
      { timeout: 15000 },
    );
    const historicalAnswer = "x".repeat(2001);
    expect(await screen.findByText(historicalAnswer, {}, { timeout: 15000 })).toBeTruthy();
    expect(screen.getAllByText("Long history")).toHaveLength(1);

    await act(async () => {
      live.send(runEvent("message.delta", 2002, "x"));
      live.send(runEvent("message.delta", 2003, "live"));
    });
    expect(await screen.findByText(`${historicalAnswer}live`)).toBeTruthy();
    expect(screen.getAllByText("Long history")).toHaveLength(1);
  }, 30000);
});
