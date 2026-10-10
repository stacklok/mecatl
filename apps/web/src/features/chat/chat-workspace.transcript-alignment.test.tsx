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
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
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

describe("transcript selection: Add to chat", () => {
  it("adds the selection to the composer's draft after a blank line", async () => {
    const bff = new ActivityFixture();
    const activity = heldStream();
    bff.responses.set("", [activity.response]);
    await mountWorkspace(bff);
    await act(async () => {
      activity.send(runEvent("user_prompt", 1, "A question"));
      activity.send(runEvent("message.delta", 2, "Quotable answer"));
    });
    const composer = screen.getByRole("textbox", { name: "Message Mecatl" }) as HTMLTextAreaElement;
    fireEvent.change(composer, { target: { value: "About this:" } });
    const answer = await screen.findByText("Quotable answer");
    const range = document.createRange();
    range.selectNodeContents(answer.firstChild as Node);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);
    fireEvent.pointerUp(screen.getByRole("region", { name: "Conversation transcript" }));
    fireEvent.click(screen.getByRole("button", { name: "Add to chat" }));
    expect(composer.value).toBe("About this:\n\nQuotable answer");
    expect(document.activeElement).toBe(composer);
    expect(screen.queryByRole("button", { name: "Add to chat" })).toBeNull();
    expect(window.getSelection()?.toString()).toBe("");
  });
});
