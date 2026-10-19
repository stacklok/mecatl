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
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { setRequestRecoveryState } from "../../lib/api-client";
import { AuthRecoveryContext } from "../auth/auth-recovery-context";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { ChatWorkspace } from "./chat-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

function heldActivity() {
  let writer!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      writer = controller;
    },
  });
  return {
    close: () => writer.close(),
    response: new Response(body, { headers: { "Content-Type": "text/event-stream" } }),
    send: (delivery: RunStreamEvent) =>
      writer.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(delivery)}\n\n`)),
  };
}

function runEvent(kind: string, seq: string, payload: unknown, text = ""): RunStreamEvent {
  return {
    event: { kind, payload, runId: "run-a", seq, text, turn: 1, unknown: false },
    type: "run.event",
  };
}

async function mountChat(activity: ReturnType<typeof heldActivity>) {
  const requests: Array<{ method: string; pathname: string }> = [];
  setRequestRecoveryState({ identityEpoch: 0, phase: "ready", workspaceMounted: true });
  vi.stubGlobal("fetch", async (request: Request) => {
    const { pathname } = new URL(request.url);
    requests.push({ method: request.method, pathname });
    if (request.method === "GET" && pathname === "/api/v1/runtime")
      return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
    if (request.method === "GET" && pathname === "/api/v1/settings/runtime")
      return json({ models: [], modelsSupported: true });
    if (request.method === "GET" && pathname === "/api/v1/sessions")
      return json({
        complete: true,
        items: [
          {
            capabilities: {
              delete: true,
              deleteReason: "",
              publicChat: true,
              publicChatReason: "",
              rename: true,
              renameReason: "",
            },
            createdAt: "2026-09-24T12:00:00.000Z",
            debugTargetSessionId: "",
            id: "chat-a",
            modelId: "test-model",
            state: "running",
            title: "Chat chat-a",
            titleProvenance: "",
            titleRevision: "0",
            turns: 0,
            updatedAt: "2026-09-24T12:00:00.000Z",
          },
        ],
      });
    if (request.method === "GET" && pathname === "/api/v1/sessions/chat-a")
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
    if (request.method === "GET" && pathname === "/api/v1/sessions/chat-a/transcript")
      return json({ complete: true, messages: [], sessionId: "chat-a" });
    if (request.method === "GET" && pathname === "/api/v1/sessions/chat-a/activity")
      return activity.response;
    if (request.method === "POST" && pathname === "/api/v1/sessions/chat-a/runs/run-a/steer")
      return new Response(null, { status: 204 });
    throw new Error(`Unexpected BFF request: ${request.method} ${pathname}`);
  });
  client.setConfig({ baseUrl: "http://studio.test" });
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
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  await act(async () => {
    render(
      <QueryClientProvider client={queryClient}>
        <AuthRecoveryContext.Provider
          value={{
            banner: { authenticated: true, publicStatusFailed: false, sessionCheckFailed: false },
            loginUrl: "/api/v1/auth/login",
            phase: "ready",
            popupIssue: null,
            retrySession: () => {},
            startPopupLogin: () => {},
          }}
        >
          <RouterProvider router={router} />
        </AuthRecoveryContext.Provider>
      </QueryClientProvider>,
    );
    await router.load();
  });
  await screen.findByRole("textbox", { name: "Message Mecatl" });
  await waitFor(() =>
    expect(requests.some((request) => request.pathname.endsWith("/activity"))).toBe(true),
  );
  return requests;
}

function storedValues(storage: Storage): string {
  return Array.from({ length: storage.length }, (_, index) => {
    const key = storage.key(index);
    return key ? `${key}:${storage.getItem(key)}` : "";
  }).join("\n");
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("shows only evidence-backed steer metadata in developer view", async () => {
  window.localStorage.setItem("studio.chat.composer.enterToSend", "steer");
  const activity = heldActivity();
  const log = vi.spyOn(console, "log");
  const info = vi.spyOn(console, "info");
  const warn = vi.spyOn(console, "warn");
  const error = vi.spyOn(console, "error");
  const requests = await mountChat(activity);
  const user = userEvent.setup();
  await act(async () =>
    activity.send({ runId: "run-a", sessionId: "chat-a", type: "run.started" }),
  );
  await user.click(screen.getAllByRole("button", { name: "Chat options" })[0] as HTMLElement);
  await user.click(screen.getByRole("menuitem", { name: "Show developer steer trace" }));
  const trace = screen.getByRole("region", { name: "Developer steer trace" });
  expect(within(trace).getByText(/outcome unknown/i)).toBeTruthy();

  fireEvent.change(screen.getByRole("textbox", { name: "Message Mecatl" }), {
    target: { value: "request-only-secret" },
  });
  await user.click(screen.getByRole("button", { name: "Steer message" }));
  await waitFor(() =>
    expect(requests.some((request) => request.pathname.endsWith("/steer"))).toBe(true),
  );
  expect(trace.textContent).toMatch(/outcome unknown/i);
  expect(trace.textContent).not.toMatch(/accepted|committed|received/i);
  await act(async () => activity.send(runEvent("tool.call", "1", { messageId: "false-steer" })));
  expect(trace.textContent).not.toContain("false-steer");
  expect(trace.textContent).toMatch(/outcome unknown/i);
  await act(async () =>
    activity.send({
      event: {
        kind: "steer",
        payload: { messageId: "unrecognized-steer" },
        runId: "run-a",
        seq: "2",
        text: "",
        turn: 1,
        unknown: true,
      },
      type: "run.event",
    }),
  );
  expect(trace.textContent).not.toContain("unrecognized-steer");

  const forbidden = [
    "request-only-secret",
    "event-text-secret",
    "payload-text-secret",
    "part-secret",
    "args-secret",
    "https://leak.test/steer",
    "header-secret",
    "credential-secret",
  ];
  await act(async () =>
    activity.send(
      runEvent(
        "steer.outcome",
        "3",
        {
          args: "args-secret",
          headers: { Authorization: "Bearer header-secret" },
          messageId: "steer-1",
          outcome: 1,
          parts: [{ data: "part-secret", url: "https://leak.test/steer" }],
          promoted: false,
          secret: "credential-secret",
          text: "payload-text-secret",
        },
        "event-text-secret",
      ),
    ),
  );
  await waitFor(() => expect(trace.textContent).toMatch(/steer-1/));
  expect(trace.textContent).toContain("Run run-a");
  expect(trace.textContent).toContain("Message steer-1");
  expect(trace.textContent).toContain("Event steer.outcome");
  expect(trace.textContent).toContain("Outcome accepted");
  expect(trace.textContent).toContain("Promoted no");

  await act(async () =>
    activity.send(
      runEvent(
        "steer",
        "4",
        {
          messageId: "steer-1",
          parts: [{ data: "part-secret", url: "https://leak.test/steer" }],
          text: "payload-text-secret",
        },
        "event-text-secret",
      ),
    ),
  );
  await waitFor(() => expect(within(trace).getAllByText(/steer-1/)).toHaveLength(2));
  expect(trace.textContent).toContain("Event steer");
  expect(trace.textContent).toMatch(/outcome unknown/i);
  expect(trace.textContent).not.toContain("request-only-secret");
  for (const value of forbidden) {
    expect(trace.textContent).not.toContain(value);
    expect(document.body.textContent).not.toContain(value);
    expect(storedValues(window.localStorage)).not.toContain(value);
    expect(storedValues(window.sessionStorage)).not.toContain(value);
    expect(
      JSON.stringify([log.mock.calls, info.mock.calls, warn.mock.calls, error.mock.calls]),
    ).not.toContain(value);
  }
  await act(async () => activity.close());
}, 20_000);
