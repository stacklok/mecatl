// @vitest-environment happy-dom
// SPDX-License-Identifier: Apache-2.0

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
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setRequestRecoveryState } from "../../lib/api-client";
import { AuthRecoveryContext, type AuthRecoveryContextValue } from "../auth/auth-recovery-context";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { ChatWorkspace } from "./chat-workspace";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const recovery: AuthRecoveryContextValue = {
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

function event(kind: string, seq: string, payload: unknown): RunStreamEvent {
  return {
    event: { kind, payload, runId: "run-a", seq, text: "", turn: 1, unknown: false },
    type: "run.event",
  };
}

function heldStream() {
  let writer!: ReadableStreamDefaultController<Uint8Array>;
  const response = new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        writer = controller;
      },
    }),
    { headers: { "Content-Type": "text/event-stream" } },
  );
  return {
    close: () => writer.close(),
    response,
    send: (value: RunStreamEvent) => writer.enqueue(frame(value)),
  };
}

function completedStream(...deliveries: RunStreamEvent[]): Response {
  return new Response(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const delivery of deliveries) controller.enqueue(frame(delivery));
        controller.close();
      },
    }),
    { headers: { "Content-Type": "text/event-stream" } },
  );
}

let container: HTMLDivElement | undefined;
let root: Root | undefined;
let queryClient: QueryClient | undefined;

async function mountWorkspace(
  fetcher: (request: Request) => Promise<Response>,
  component: () => React.ReactNode,
) {
  setRequestRecoveryState({ identityEpoch: 0, phase: "ready", workspaceMounted: true });
  vi.stubGlobal("fetch", fetcher);
  client.setConfig({ baseUrl: "http://studio.test" });
  const mountedQueryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  queryClient = mountedQueryClient;
  const route = createRootRoute({
    component: () => (
      <ShortcutProvider>
        <Outlet />
      </ShortcutProvider>
    ),
  });
  const chat = createRoute({
    component,
    getParentRoute: () => route,
    path: "/workspace/chat",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/chat"] }),
    routeTree: route.addChildren([chat]),
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(
      <QueryClientProvider client={mountedQueryClient}>
        <AuthRecoveryContext.Provider value={recovery}>
          <RouterProvider router={router} />
        </AuthRecoveryContext.Provider>
      </QueryClientProvider>,
    );
    await router.load();
  });
}

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  container?.remove();
  root = undefined;
  container = undefined;
  queryClient?.clear();
  queryClient = undefined;
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.unstubAllGlobals();
});

describe("mounted delegated activity", () => {
  it("replays settled delegation without replacing the saved transcript or showing run controls", async () => {
    const requests: string[] = [];
    const fetcher = async (request: Request): Promise<Response> => {
      const path = new URL(request.url).pathname;
      requests.push(path);
      if (path === "/api/v1/runtime") {
        return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
      }
      if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
      if (path === "/api/v1/sessions") {
        // The inventory proves this direct link is an editable main chat.
        return json({
          complete: true,
          items: [
            {
              capabilities: { publicChat: true, publicChatReason: "" },
              createdAt: "2026-09-24T12:00:00Z",
              debugTargetSessionId: "",
              id: "session-a",
              kind: "main",
              modelId: "test",
              state: "idle",
              title: "Session A",
              titleProvenance: "",
              titleRevision: "0",
              turns: 1,
              updatedAt: "2026-09-24T12:00:00Z",
            },
          ],
        });
      }
      if (path === "/api/v1/sessions/session-a") {
        return json({
          capabilities: { image: false, manualCompaction: false, modelSelection: false },
          id: "session-a",
          mode: "default",
          state: "idle",
          usage: {
            cacheReadTokens: "0",
            cacheWriteTokens: "0",
            inputTokens: "0",
            outputTokens: "0",
            reasoningTokens: "0",
          },
        });
      }
      if (path === "/api/v1/sessions/session-a/transcript") {
        return json({
          complete: true,
          messages: [
            { images: [], role: "user", text: "Keep my saved question", toolCalls: [] },
            {
              images: [],
              role: "assistant",
              text: "Keep my saved answer",
              toolCalls: [
                { args: "{}", id: "call-a", name: "Subagent" },
                { args: "{}", id: "call-p", name: "Parallel" },
                { args: "{}", id: "call-t", name: "Team" },
              ],
            },
          ],
          sessionId: "session-a",
        });
      }
      if (path === "/api/v1/sessions/session-a/activity") {
        return completedStream(
          { runId: "run-a", sessionId: "session-a", type: "run.started" },
          event("subagent.start", "1", {
            childId: "child-a",
            goal: "Inspect",
            parentCallId: "call-a",
          }),
          event("parallel.start", "2", {
            branchCount: 1,
            join: "first",
            parentCallId: "call-p",
          }),
          event("team.start", "3", {
            parentCallId: "call-t",
            roster: [{ lead: true, name: "lead", role: "Reviewer" }],
            teamId: "team-a",
          }),
          event("subagent.tool", "4", {
            childId: "child-a",
            parentCallId: "call-a",
            toolCount: 1,
            toolName: "Read",
          }),
          event("parallel.branch", "5", {
            branchIndex: 0,
            branchLabel: "branch-a",
            kind: "branch_start",
            parentCallId: "call-p",
          }),
          event("parallel.branch", "6", {
            branchIndex: 0,
            kind: "branch_end",
            parentCallId: "call-p",
            stop: "end_turn",
          }),
          event("team.tasks", "7", {
            parentCallId: "call-t",
            tasks: [
              { assignee: "lead", deps: [], description: "Review", id: "task-a", state: "done" },
            ],
            teamId: "team-a",
          }),
          event("subagent.end", "8", {
            childId: "child-a",
            parentCallId: "call-a",
            stop: "error",
          }),
          event("parallel.end", "9", {
            branchCount: 1,
            join: "first",
            parentCallId: "call-p",
            winner: 0,
          }),
          event("team.end", "10", {
            dispositions: [{ errorRounds: 0, name: "lead", reason: 0, stopped: false }],
            findings: [{ body: "Found evidence", member: "lead" }],
            parentCallId: "call-t",
            tasks: [
              { assignee: "lead", deps: [], description: "Review", id: "task-a", state: "done" },
            ],
            teamId: "team-a",
          }),
          event("result", "11", { stop: "end_turn" }),
        );
      }
      if (path === "/api/v1/sessions/session-a/runs" && request.method === "POST") {
        return completedStream(
          { runId: "run-b", sessionId: "session-a", type: "run.started" },
          {
            event: {
              kind: "result",
              payload: { stop: "end_turn" },
              runId: "run-b",
              seq: "1",
              text: "",
              turn: 1,
              unknown: false,
            },
            type: "run.event",
          },
        );
      }
      throw new Error(`Unexpected request ${path}`);
    };

    await mountWorkspace(fetcher, () => <ChatWorkspace sessionId="session-a" />);
    await act(async () =>
      vi.waitFor(() => expect(requests).toContain("/api/v1/sessions/session-a/activity")),
    );
    await act(async () => {});
    expect(container?.textContent).toContain("Subagent child-a");
    expect(container?.textContent).toContain("Parallel group");
    expect(container?.textContent).toContain("Team team-a");
    expect(container?.textContent).toContain("Keep my saved question");
    expect(container?.textContent).toContain("Keep my saved answer");
    expect(container?.textContent).toContain("Winner: Branch 1");
    expect(container?.textContent).toContain("Failed");
    expect(container?.textContent).not.toContain("Reconnected to run");
    expect(container?.textContent).not.toContain("Mecatl is working");
    expect(
      [...(container?.querySelectorAll("header button") ?? [])].some(
        (button) => button.textContent?.trim() === "Stop",
      ),
    ).toBe(false);
    expect(container?.querySelector('aside[aria-label="Session activity"]')).toBeNull();
    await act(async () =>
      container
        ?.querySelector<HTMLButtonElement>('button[aria-label="Open session activity"]')
        ?.click(),
    );
    await act(async () =>
      container
        ?.querySelector<HTMLButtonElement>('button[role="tab"][aria-label^="Teams"]')
        ?.click(),
    );
    expect(container?.textContent).not.toContain("Activity history incomplete");
    await act(async () =>
      [
        ...(container?.querySelectorAll<HTMLButtonElement>(
          'aside[aria-label="Session activity"] button',
        ) ?? []),
      ]
        .find((button) => button.textContent?.includes("Team team-a"))
        ?.click(),
    );
    expect(container?.textContent).toContain("Found evidence");
    await act(async () =>
      container
        ?.querySelector<HTMLButtonElement>(
          'aside[aria-label="Session activity"] button[aria-label="Close panel"]',
        )
        ?.click(),
    );
    const transcriptRequests = requests.filter(
      (path) => path === "/api/v1/sessions/session-a/transcript",
    ).length;
    const textarea = container?.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="Message Mecatl"]',
    );
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(textarea, "Another task");
      textarea?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () =>
      container?.querySelector<HTMLButtonElement>('button[aria-label="Send message"]')?.click(),
    );
    await act(async () =>
      vi.waitFor(() =>
        expect(
          requests.filter((path) => path === "/api/v1/sessions/session-a/transcript").length,
        ).toBeGreaterThan(transcriptRequests),
      ),
    );
    await act(async () =>
      container
        ?.querySelector<HTMLButtonElement>('button[aria-label="Open session activity"]')
        ?.click(),
    );
    expect(container?.textContent).not.toContain("Activity history incomplete");
  });

  it("aborts settled replay when switching sessions and rejects the old activity", async () => {
    const oldStream = heldStream();
    let oldSignal: AbortSignal | undefined;
    const fetcher = async (request: Request): Promise<Response> => {
      const path = new URL(request.url).pathname;
      if (path === "/api/v1/runtime") {
        return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
      }
      if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
      if (path === "/api/v1/sessions") {
        return json({
          complete: true,
          items: ["session-a", "session-b"].map((id) => ({
            capabilities: {
              delete: true,
              deleteReason: "",
              publicChat: true,
              publicChatReason: "",
              rename: true,
              renameReason: "",
            },
            createdAt: "2026-09-24T12:00:00Z",
            debugTargetSessionId: "",
            kind: "main",
            id,
            modelId: "test",
            state: "idle",
            title: id,
            titleProvenance: "",
            titleRevision: "0",
            turns: 1,
            updatedAt: "2026-09-24T12:00:00Z",
          })),
        });
      }
      if (path.endsWith("/transcript")) {
        return json({ complete: true, messages: [], sessionId: path.split("/")[4] });
      }
      if (path === "/api/v1/sessions/session-a" || path === "/api/v1/sessions/session-b") {
        return json({
          capabilities: { image: false, manualCompaction: false, modelSelection: false },
          id: path.endsWith("session-a") ? "session-a" : "session-b",
          mode: "default",
          state: "idle",
          usage: {
            cacheReadTokens: "0",
            cacheWriteTokens: "0",
            inputTokens: "0",
            outputTokens: "0",
            reasoningTokens: "0",
          },
        });
      }
      if (path === "/api/v1/sessions/session-a/activity") {
        oldSignal = request.signal;
        return oldStream.response;
      }
      if (path === "/api/v1/sessions/session-b/activity") {
        return completedStream(
          { runId: "run-b", sessionId: "session-b", type: "run.started" },
          {
            event: {
              kind: "subagent.start",
              payload: { childId: "new-child", parentCallId: "call-b" },
              runId: "run-b",
              seq: "1",
              text: "",
              turn: 1,
              unknown: false,
            },
            type: "run.event",
          },
        );
      }
      throw new Error(`Unexpected request ${path}`);
    };
    function Switchable() {
      const [sessionId, setSessionId] = useState("session-a");
      return (
        <>
          <button onClick={() => setSessionId("session-b")} type="button">
            Switch to B
          </button>
          <ChatWorkspace sessionId={sessionId} />
        </>
      );
    }

    await mountWorkspace(fetcher, Switchable);
    await act(async () => vi.waitFor(() => expect(oldSignal).toBeDefined()));
    await act(async () => {
      oldStream.send({ runId: "run-a", sessionId: "session-a", type: "run.started" });
      oldStream.send(
        event("subagent.start", "1", { parentCallId: "call-a", childId: "old-child" }),
      );
    });
    expect(container?.textContent).toContain("Subagent old-child");
    await act(async () =>
      [...(container?.querySelectorAll("button") ?? [])]
        .find((button) => button.textContent === "Switch to B")
        ?.click(),
    );
    expect(oldSignal?.aborted).toBe(true);
    await act(async () =>
      vi.waitFor(() => expect(container?.textContent).toContain("Subagent new-child")),
    );
    expect(container?.textContent).toContain("Outcome unknown");
    expect(container?.textContent).not.toContain("Subagent old-child");
    try {
      await act(async () =>
        oldStream.send(
          event("subagent.end", "2", {
            parentCallId: "call-a",
            childId: "old-child",
            stop: "end_turn",
          }),
        ),
      );
    } catch {
      // The aborted SSE reader may already have closed the test stream.
    }
    expect(container?.textContent).not.toContain("Subagent old-child");
  });

  it("marks an unfinished replayed child unknown when a new run starts in the same session", async () => {
    const oldStream = heldStream();
    const newRun = heldStream();
    let oldSignal: AbortSignal | undefined;
    let newRunRequests = 0;
    const fetcher = async (request: Request): Promise<Response> => {
      const path = new URL(request.url).pathname;
      if (path === "/api/v1/runtime") {
        return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
      }
      if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
      if (path === "/api/v1/sessions") {
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
              createdAt: "2026-09-24T12:00:00Z",
              debugTargetSessionId: "",
              kind: "main",
              id: "session-a",
              modelId: "test",
              state: "idle",
              title: "Session A",
              titleProvenance: "",
              titleRevision: "0",
              turns: 1,
              updatedAt: "2026-09-24T12:00:00Z",
            },
          ],
        });
      }
      if (path === "/api/v1/sessions/session-a") {
        return json({
          capabilities: { image: false, manualCompaction: false, modelSelection: false },
          id: "session-a",
          mode: "default",
          state: "idle",
          usage: {
            cacheReadTokens: "0",
            cacheWriteTokens: "0",
            inputTokens: "0",
            outputTokens: "0",
            reasoningTokens: "0",
          },
        });
      }
      if (path === "/api/v1/sessions/session-a/transcript") {
        return json({ complete: true, messages: [], sessionId: "session-a" });
      }
      if (path === "/api/v1/sessions/session-a/activity") {
        oldSignal = request.signal;
        return oldStream.response;
      }
      if (path === "/api/v1/sessions/session-a/runs" && request.method === "POST") {
        newRunRequests += 1;
        return newRun.response;
      }
      throw new Error(`Unexpected request ${request.method} ${path}`);
    };

    await mountWorkspace(fetcher, () => <ChatWorkspace sessionId="session-a" />);
    await act(async () => vi.waitFor(() => expect(oldSignal).toBeDefined()));
    await act(async () => {
      oldStream.send({ runId: "run-a", sessionId: "session-a", type: "run.started" });
      oldStream.send(
        event("subagent.start", "1", { parentCallId: "call-a", childId: "old-child" }),
      );
    });
    expect(container?.textContent).toContain("Subagent old-child");
    expect(container?.textContent).toContain("Running");

    const textarea = container?.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="Message Mecatl"]',
    );
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(textarea, "Another task");
      textarea?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const send = container?.querySelector<HTMLButtonElement>('button[aria-label="Send message"]');
    expect(send?.disabled).toBe(false);
    await act(async () => send?.click());
    await act(async () => vi.waitFor(() => expect(newRunRequests).toBe(1)));
    expect(oldSignal?.aborted).toBe(true);
    expect(container?.textContent).toContain("Subagent old-child");
    expect(container?.textContent).toContain("Outcome unknown");
    await act(async () =>
      [...(container?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
        .find((button) => button.textContent?.includes("Subagent old-child"))
        ?.click(),
    );
    expect(container?.textContent).toContain("Activity history incomplete");

    await act(async () => {
      newRun.send({ runId: "run-b", sessionId: "session-a", type: "run.started" });
      newRun.send({
        event: {
          kind: "result",
          payload: { stop: "end_turn" },
          runId: "run-b",
          seq: "1",
          text: "",
          turn: 1,
          unknown: false,
        },
        type: "run.event",
      });
      newRun.close();
    });
  });

  it("discloses unread settled history when a new run starts after an observed child finished", async () => {
    const oldStream = heldStream();
    const newRun = heldStream();
    let oldSignal: AbortSignal | undefined;
    let activityRequests = 0;
    let newRunRequests = 0;
    const fetcher = async (request: Request): Promise<Response> => {
      const path = new URL(request.url).pathname;
      if (path === "/api/v1/runtime") {
        return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
      }
      if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
      if (path === "/api/v1/sessions") {
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
              createdAt: "2026-09-24T12:00:00Z",
              debugTargetSessionId: "",
              kind: "main",
              id: "session-a",
              modelId: "test",
              state: "idle",
              title: "Session A",
              titleProvenance: "",
              titleRevision: "0",
              turns: 1,
              updatedAt: "2026-09-24T12:00:00Z",
            },
          ],
        });
      }
      if (path === "/api/v1/sessions/session-a") {
        return json({
          capabilities: { image: false, manualCompaction: false, modelSelection: false },
          id: "session-a",
          mode: "default",
          state: "idle",
          usage: {
            cacheReadTokens: "0",
            cacheWriteTokens: "0",
            inputTokens: "0",
            outputTokens: "0",
            reasoningTokens: "0",
          },
        });
      }
      if (path === "/api/v1/sessions/session-a/transcript") {
        return json({ complete: true, messages: [], sessionId: "session-a" });
      }
      if (path === "/api/v1/sessions/session-a/activity") {
        activityRequests += 1;
        oldSignal = request.signal;
        return oldStream.response;
      }
      if (path === "/api/v1/sessions/session-a/runs" && request.method === "POST") {
        newRunRequests += 1;
        return newRun.response;
      }
      throw new Error(`Unexpected request ${request.method} ${path}`);
    };

    await mountWorkspace(fetcher, () => <ChatWorkspace sessionId="session-a" />);
    await act(async () => vi.waitFor(() => expect(oldSignal).toBeDefined()));
    await act(async () => {
      oldStream.send({ runId: "run-a", sessionId: "session-a", type: "run.started" });
      oldStream.send(
        event("subagent.start", "1", { parentCallId: "call-a", childId: "old-child" }),
      );
      oldStream.send(
        event("subagent.end", "2", {
          parentCallId: "call-a",
          childId: "old-child",
          stop: "end_turn",
        }),
      );
      oldStream.send(event("result", "3", { stop: "end_turn" }));
    });
    const oldCard = [...(container?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
      (button) => button.textContent?.includes("Subagent old-child"),
    );
    expect(oldCard?.textContent).toContain("Finished");

    const textarea = container?.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="Message Mecatl"]',
    );
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(textarea, "Another task");
      textarea?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const send = container?.querySelector<HTMLButtonElement>('button[aria-label="Send message"]');
    expect(send?.disabled).toBe(false);
    await act(async () => send?.click());
    await act(async () => vi.waitFor(() => expect(newRunRequests).toBe(1)));
    expect(oldSignal?.aborted).toBe(true);

    await act(async () => {
      newRun.send({ runId: "run-b", sessionId: "session-a", type: "run.started" });
      newRun.send({
        event: {
          kind: "result",
          payload: { stop: "end_turn" },
          runId: "run-b",
          seq: "1",
          text: "",
          turn: 1,
          unknown: false,
        },
        type: "run.event",
      });
      newRun.close();
    });
    await act(async () =>
      vi.waitFor(() => expect(container?.textContent).not.toContain("Mecatl is working")),
    );
    const completedCard = [
      ...(container?.querySelectorAll<HTMLButtonElement>("button") ?? []),
    ].find((button) => button.textContent?.includes("Subagent old-child"));
    expect(completedCard?.textContent).toContain("Finished");
    expect(container?.textContent).not.toContain("Subagent unseen-child");
    await act(async () => completedCard?.click());
    expect(container?.textContent).toContain("Activity history incomplete");
    expect(container?.textContent).not.toContain("Subagent unseen-child");
    expect(activityRequests).toBe(1);
  });

  it("retains an interrupted settled card when the same session becomes active elsewhere", async () => {
    const oldStream = heldStream();
    const followStream = heldStream();
    let sessionState = "idle";
    let activityRequests = 0;
    let oldSignal: AbortSignal | undefined;
    const fetcher = async (request: Request): Promise<Response> => {
      const path = new URL(request.url).pathname;
      if (path === "/api/v1/runtime") {
        return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
      }
      if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
      if (path === "/api/v1/sessions") {
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
              createdAt: "2026-09-24T12:00:00Z",
              debugTargetSessionId: "",
              kind: "main",
              id: "session-a",
              modelId: "test",
              state: sessionState,
              title: "Session A",
              titleProvenance: "",
              titleRevision: "0",
              turns: 1,
              updatedAt: "2026-09-24T12:00:00Z",
            },
          ],
        });
      }
      if (path === "/api/v1/sessions/session-a") {
        return json({
          capabilities: { image: false, manualCompaction: false, modelSelection: false },
          id: "session-a",
          mode: "default",
          state: sessionState,
          usage: {
            cacheReadTokens: "0",
            cacheWriteTokens: "0",
            inputTokens: "0",
            outputTokens: "0",
            reasoningTokens: "0",
          },
        });
      }
      if (path === "/api/v1/sessions/session-a/transcript") {
        return json({ complete: true, messages: [], sessionId: "session-a" });
      }
      if (path === "/api/v1/sessions/session-a/activity") {
        activityRequests += 1;
        if (activityRequests === 1) {
          oldSignal = request.signal;
          return oldStream.response;
        }
        return followStream.response;
      }
      throw new Error(`Unexpected request ${path}`);
    };

    await mountWorkspace(fetcher, () => <ChatWorkspace sessionId="session-a" />);
    await act(async () => vi.waitFor(() => expect(oldSignal).toBeDefined()));
    await act(async () => {
      oldStream.send({ runId: "run-a", sessionId: "session-a", type: "run.started" });
      oldStream.send(
        event("subagent.start", "1", { parentCallId: "call-a", childId: "old-child" }),
      );
    });
    expect(container?.textContent).toContain("Subagent old-child");
    expect(container?.textContent).toContain("Running");

    sessionState = "running";
    await act(async () => {
      await queryClient?.invalidateQueries();
    });
    await act(async () => vi.waitFor(() => expect(activityRequests).toBe(2)));
    expect(oldSignal?.aborted).toBe(true);
    expect(container?.textContent).toContain("Subagent old-child");
    expect(container?.textContent).toContain("Outcome unknown");
    await act(async () =>
      [...(container?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
        .find((button) => button.textContent?.includes("Subagent old-child"))
        ?.click(),
    );
    expect(container?.textContent).toContain("Activity history incomplete");
  });

  for (const closeBeforeIdle of [false, true]) {
    it(`replays the terminal child event after an attached stream ${closeBeforeIdle ? "ends without a result" : "is interrupted by idle status"}`, async () => {
      const followStream = heldStream();
      let sessionState = "running";
      let activityRequests = 0;
      let followSignal: AbortSignal | undefined;
      const fetcher = async (request: Request): Promise<Response> => {
        const path = new URL(request.url).pathname;
        if (path === "/api/v1/runtime") {
          return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
        }
        if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
        if (path === "/api/v1/sessions") {
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
                createdAt: "2026-09-24T12:00:00Z",
                debugTargetSessionId: "",
                kind: "main",
                id: "session-a",
                modelId: "test",
                state: sessionState,
                title: "Session A",
                titleProvenance: "",
                titleRevision: "0",
                turns: 1,
                updatedAt: "2026-09-24T12:00:00Z",
              },
            ],
          });
        }
        if (path === "/api/v1/sessions/session-a") {
          return json({
            capabilities: { image: false, manualCompaction: false, modelSelection: false },
            id: "session-a",
            mode: "default",
            state: sessionState,
            usage: {
              cacheReadTokens: "0",
              cacheWriteTokens: "0",
              inputTokens: "0",
              outputTokens: "0",
              reasoningTokens: "0",
            },
          });
        }
        if (path === "/api/v1/sessions/session-a/transcript") {
          return json({ complete: true, messages: [], sessionId: "session-a" });
        }
        if (path === "/api/v1/sessions/session-a/activity") {
          activityRequests += 1;
          if (activityRequests === 1) {
            followSignal = request.signal;
            return followStream.response;
          }
          return completedStream(
            { runId: "run-a", sessionId: "session-a", type: "run.started" },
            event("subagent.start", "1", {
              childId: "child-a",
              parentCallId: "call-a",
            }),
            event("subagent.end", "2", {
              childId: "child-a",
              parentCallId: "call-a",
              stop: "end_turn",
            }),
            event("result", "3", { stop: "end_turn" }),
          );
        }
        throw new Error(`Unexpected request ${path}`);
      };

      await mountWorkspace(fetcher, () => <ChatWorkspace sessionId="session-a" />);
      await act(async () => vi.waitFor(() => expect(followSignal).toBeDefined()));
      await act(async () => {
        followStream.send({ runId: "run-a", sessionId: "session-a", type: "run.started" });
        followStream.send(
          event("subagent.start", "1", { childId: "child-a", parentCallId: "call-a" }),
        );
      });
      expect(container?.textContent).toContain("Subagent child-a");
      expect(container?.textContent).toContain("Running");

      if (closeBeforeIdle) {
        await act(async () => followStream.close());
        await act(async () =>
          vi.waitFor(() => expect(container?.textContent).toContain("Outcome unknown")),
        );
      }
      sessionState = "idle";
      await act(async () => {
        await queryClient?.invalidateQueries();
      });
      if (!closeBeforeIdle) {
        await act(async () => vi.waitFor(() => expect(followSignal?.aborted).toBe(true)));
      }
      await act(async () =>
        vi.waitFor(() => expect(container?.textContent).toContain("Session Aidle")),
      );
      await act(async () => vi.waitFor(() => expect(activityRequests).toBe(2)));
      const card = [...(container?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
        (button) => button.textContent?.includes("Subagent child-a"),
      );
      expect(card?.textContent).toContain("Finished");
      expect(card?.textContent).not.toContain("Outcome unknown");
    });
  }

  it("keeps inline activity through a transcript refresh and opens only on request", async () => {
    const stream = heldStream();
    const requests: string[] = [];
    let sessionState = "running";
    let savedMessages: unknown[] = [];
    const fetcher = async (request: Request) => {
      const path = new URL(request.url).pathname;
      requests.push(path);
      if (path === "/api/v1/runtime") {
        return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
      }
      if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
      if (path === "/api/v1/sessions") {
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
              createdAt: "2026-09-24T12:00:00Z",
              debugTargetSessionId: "",
              kind: "main",
              id: "session-a",
              modelId: "test",
              state: sessionState,
              title: "Session A",
              titleProvenance: "",
              titleRevision: "0",
              turns: 0,
              updatedAt: "2026-09-24T12:00:00Z",
            },
          ],
        });
      }
      if (path === "/api/v1/sessions/session-a") {
        return json({
          capabilities: { image: false, manualCompaction: false, modelSelection: false },
          id: "session-a",
          mode: "default",
          state: sessionState,
          usage: {
            cacheReadTokens: "0",
            cacheWriteTokens: "0",
            inputTokens: "0",
            outputTokens: "0",
            reasoningTokens: "0",
          },
        });
      }
      if (path === "/api/v1/sessions/session-a/transcript") {
        return json({ complete: true, messages: savedMessages, sessionId: "session-a" });
      }
      if (path === "/api/v1/sessions/session-a/activity") return stream.response;
      throw new Error(`Unexpected request ${path}`);
    };
    await mountWorkspace(fetcher, () => <ChatWorkspace sessionId="session-a" />);
    await act(async () =>
      vi.waitFor(() => expect(requests).toContain("/api/v1/sessions/session-a/activity")),
    );

    await act(async () => {
      stream.send({ runId: "run-a", sessionId: "session-a", type: "run.started" });
      stream.send(event("user_prompt", "1", { text: "Delegate" }));
      stream.send(event("tool.call", "2", { id: "call-a", name: "Subagent", args: "{}" }));
      stream.send(
        event("subagent.start", "3", {
          parentCallId: "call-a",
          childId: "child-a",
          goal: "Inspect",
        }),
      );
      stream.send(
        event("subagent.start", "4", {
          parentCallId: "call-missing",
          childId: "child-b",
          goal: "Review",
        }),
      );
    });
    const card = [...(container?.querySelectorAll("button") ?? [])].find((button) =>
      button.textContent?.includes("Subagent child-a"),
    ) as HTMLButtonElement | undefined;
    expect(card).toBeDefined();
    expect(container?.querySelector('aside[aria-label="Session activity"]')).toBeNull();
    await act(async () => card?.click());
    expect(container?.querySelector('aside[aria-label="Session activity"]')).not.toBeNull();
    expect(document.activeElement?.textContent).toBe("Subagent child-a");

    // The normal transcript may refresh while the stream is still active.
    await act(async () => {
      stream.send(
        event("subagent.tool", "5", {
          parentCallId: "call-a",
          childId: "child-a",
          toolCount: 1,
          toolName: "Read",
        }),
      );
    });
    expect(container?.textContent).toContain("Tools: 1");
    expect(document.activeElement?.textContent).toBe("Subagent child-a");
    await act(async () =>
      document.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" })),
    );
    expect(container?.querySelector('aside[aria-label="Session activity"]')).toBeNull();
    expect(document.activeElement).toBe(card);

    sessionState = "idle";
    savedMessages = [
      { images: [], role: "user", text: "Delegate", toolCalls: [] },
      {
        images: [],
        role: "assistant",
        text: "Delegating",
        toolCalls: [{ args: "{}", id: "call-a", name: "Subagent" }],
      },
    ];
    await act(async () => {
      stream.send(
        event("subagent.end", "6", {
          parentCallId: "call-a",
          childId: "child-a",
          stop: "end_turn",
        }),
      );
      stream.send(event("result", "7", { stop: "end_turn" }));
      stream.close();
    });
    await act(async () =>
      vi.waitFor(() =>
        expect(requests.filter((path) => path.endsWith("/transcript")).length).toBeGreaterThan(1),
      ),
    );
    const inline = [...(container?.querySelectorAll("fieldset") ?? [])]
      .map((row) => row.textContent)
      .join(" ");
    expect(inline).toContain("Subagent child-a");
    expect(inline).toContain("Subagent child-b");
    const activityControl = container?.querySelector(
      'button[aria-label="Open session activity"]',
    ) as HTMLButtonElement;
    activityControl.focus();
    await act(async () => activityControl.click());
    expect(
      container
        ?.querySelector('aside[aria-label="Session activity"]')
        ?.contains(document.activeElement),
    ).toBe(true);
    expect(container?.textContent).toContain("Activity history incomplete");
    expect(container?.textContent).toContain("Outcome unknown");
    await act(async () =>
      (
        container?.querySelector(
          'aside[aria-label="Session activity"] button[aria-label="Close panel"]',
        ) as HTMLButtonElement
      )?.click(),
    );
    expect(document.activeElement).toBe(activityControl);
    await act(async () =>
      vi.waitFor(() => expect(container?.textContent).toContain("Session Aidle")),
    );
    await act(async () => {});
    expect(requests.filter((path) => path === "/api/v1/sessions/session-a/activity")).toHaveLength(
      1,
    );
  });

  it("clears the former session fleet and aborts its stream on a session switch", async () => {
    const oldStream = heldStream();
    let oldSignal: AbortSignal | undefined;
    const fetcher = async (request: Request): Promise<Response> => {
      const path = new URL(request.url).pathname;
      if (path === "/api/v1/runtime") {
        return json({ capabilities: { image: false, posture: "managed" }, connection: "online" });
      }
      if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
      if (path === "/api/v1/sessions") {
        return json({
          complete: true,
          items: ["session-a", "session-b"].map((id) => ({
            capabilities: {
              delete: true,
              deleteReason: "",
              publicChat: true,
              publicChatReason: "",
              rename: true,
              renameReason: "",
            },
            createdAt: "2026-09-24T12:00:00Z",
            debugTargetSessionId: "",
            kind: "main",
            id,
            modelId: "test",
            state: id === "session-a" ? "running" : "idle",
            title: id,
            titleProvenance: "",
            titleRevision: "0",
            turns: 0,
            updatedAt: "2026-09-24T12:00:00Z",
          })),
        });
      }
      if (path.endsWith("/transcript")) {
        return json({
          complete: true,
          messages: [],
          sessionId: path.includes("session-a") ? "session-a" : "session-b",
        });
      }
      if (path === "/api/v1/sessions/session-a" || path === "/api/v1/sessions/session-b") {
        return json({
          capabilities: { image: false, manualCompaction: false, modelSelection: false },
          id: path.endsWith("session-a") ? "session-a" : "session-b",
          mode: "default",
          state: path.endsWith("session-a") ? "running" : "idle",
          usage: {
            cacheReadTokens: "0",
            cacheWriteTokens: "0",
            inputTokens: "0",
            outputTokens: "0",
            reasoningTokens: "0",
          },
        });
      }
      if (path === "/api/v1/sessions/session-a/activity") {
        oldSignal = request.signal;
        return oldStream.response;
      }
      throw new Error(`Unexpected request ${path}`);
    };
    function Switchable() {
      const [sessionId, setSessionId] = useState("session-a");
      return (
        <>
          <button onClick={() => setSessionId("session-b")} type="button">
            Switch to B
          </button>
          <ChatWorkspace sessionId={sessionId} />
        </>
      );
    }
    await mountWorkspace(fetcher, Switchable);
    await act(async () => vi.waitFor(() => expect(oldSignal).toBeDefined()));
    await act(async () => {
      oldStream.send({ runId: "run-a", sessionId: "session-a", type: "run.started" });
      oldStream.send(
        event("subagent.start", "1", { parentCallId: "call-a", childId: "old-child" }),
      );
    });
    expect(container?.textContent).toContain("Subagent old-child");
    const switchButton = [...(container?.querySelectorAll("button") ?? [])].find(
      (button) => button.textContent === "Switch to B",
    );
    expect(switchButton).toBeDefined();
    await act(async () => switchButton?.click());
    expect(oldSignal?.aborted).toBe(true);
    expect(container?.textContent).not.toContain("Subagent old-child");
    try {
      await act(async () =>
        oldStream.send(
          event("subagent.end", "2", {
            parentCallId: "call-a",
            childId: "old-child",
            stop: "end_turn",
          }),
        ),
      );
    } catch {
      // The aborted SSE reader may already have closed the test stream.
    }
    expect(container?.textContent).not.toContain("Subagent old-child");
  });

  for (const sessionState of ["running", "idle"] as const) {
    for (const terminal of ["gap", "budget"] as const) {
      it(`keeps a ${sessionState} card across bound reattach and reports unknown outcome after ${terminal}`, async () => {
        const cursors: string[] = [];
        const fetcher = async (request: Request): Promise<Response> => {
          const url = new URL(request.url);
          const path = url.pathname;
          if (path === "/api/v1/runtime") {
            return json({
              capabilities: { image: false, posture: "managed" },
              connection: "online",
            });
          }
          if (path === "/api/v1/settings/runtime")
            return json({ models: [], modelsSupported: true });
          if (path === "/api/v1/sessions") {
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
                  createdAt: "2026-09-24T12:00:00Z",
                  debugTargetSessionId: "",
                  kind: "main",
                  id: "session-a",
                  modelId: "test",
                  state: sessionState,
                  title: "Session A",
                  titleProvenance: "",
                  titleRevision: "0",
                  turns: 0,
                  updatedAt: "2026-09-24T12:00:00Z",
                },
              ],
            });
          }
          if (path === "/api/v1/sessions/session-a/transcript") {
            return json({ complete: true, messages: [], sessionId: "session-a" });
          }
          if (path === "/api/v1/sessions/session-a") {
            return json({
              capabilities: { image: false, manualCompaction: false, modelSelection: false },
              id: "session-a",
              mode: "default",
              state: sessionState,
              usage: {
                cacheReadTokens: "0",
                cacheWriteTokens: "0",
                inputTokens: "0",
                outputTokens: "0",
                reasoningTokens: "0",
              },
            });
          }
          if (path === "/api/v1/sessions/session-a/activity") {
            const cursor = url.searchParams.get("resumeFrom") ?? "";
            cursors.push(cursor);
            if (!cursor) {
              return completedStream(
                { runId: "run-a", sessionId: "session-a", type: "run.started" },
                event("subagent.start", "1", { parentCallId: "call-a", childId: "child-a" }),
                { cursor: "cursor-1", reason: "bound", type: "run.truncated" },
              );
            }
            if (terminal === "gap") {
              return completedStream(
                event("subagent.tool", "2", {
                  parentCallId: "call-a",
                  childId: "child-a",
                  toolCount: 1,
                }),
                { cursor: "", reason: "gap", type: "run.truncated" },
              );
            }
            return completedStream({
              cursor: `cursor-${cursors.length}`,
              reason: "bound",
              type: "run.truncated",
            });
          }
          throw new Error(`Unexpected request ${path}`);
        };
        await mountWorkspace(fetcher, () => <ChatWorkspace sessionId="session-a" />);
        await act(async () =>
          vi.waitFor(() => expect(cursors.length).toBe(terminal === "gap" ? 2 : 51), {
            timeout: 5000,
          }),
        );
        expect(cursors[1]).toBe("cursor-1");
        expect(container?.querySelector('aside[aria-label="Session activity"]')).toBeNull();
        expect(container?.textContent).toContain("Subagent child-a");
        const activityControl = container?.querySelector<HTMLButtonElement>(
          'button[aria-label="Open session activity"]',
        );
        expect(activityControl).toBeDefined();
        await act(async () => activityControl?.click());
        expect(container?.textContent).toContain("Activity history incomplete");
        expect(container?.textContent).toContain("Outcome unknown");
      });
    }
  }
});
