// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { client } from "@mecatl-studio/contracts/client";
import { listSessionsQueryKey } from "@mecatl-studio/contracts/query";
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
import type { ApprovalRequest } from "./approval-panel";
import { applyRunDelivery, type ChatMessage, initialRunDeliveryState } from "./chat-state";
import { ChatTranscript } from "./chat-transcript";
import { ChatWorkspace } from "./chat-workspace";
import { EscapeHintContext } from "./escape-hint-context";
import { Message } from "./message";
import { PlanReviewCard } from "./plan-review-card";
import { SideThreadPanel } from "./side-thread-panel";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const recovery: AuthRecoveryContextValue = {
  banner: { authenticated: true, publicStatusFailed: false, sessionCheckFailed: false },
  loginUrl: "/api/v1/auth/login",
  phase: "ready",
  popupIssue: null,
  retrySession: () => {},
  startPopupLogin: () => {},
};

const editArgs = JSON.stringify({
  new_string: "after\nline two",
  old_string: "before\nline two",
  path: "notes.txt",
});

function ask(overrides: Partial<ApprovalRequest> = {}): ApprovalRequest {
  return {
    args: editArgs,
    askId: "ask-a",
    callId: "call-a",
    controlTarget: { askId: "ask-a", runId: "run-a", sessionId: "chat-a" },
    reason: "Needs file access",
    tool: "Edit",
    ...overrides,
  };
}

function event(
  kind: string,
  seq: string,
  runId: string,
  payload?: unknown,
  text = "",
): RunStreamEvent {
  return {
    event: { kind, payload, runId, seq, text, turn: 1, unknown: false },
    type: "run.event",
  } as RunStreamEvent;
}

function heldStream() {
  let writer!: ReadableStreamDefaultController<Uint8Array>;
  return {
    response: new Response(
      new ReadableStream<Uint8Array>({ start: (controller) => (writer = controller) }),
      { headers: { "Content-Type": "text/event-stream" } },
    ),
    send: (delivery: RunStreamEvent, cursor?: string) =>
      writer.enqueue(
        new TextEncoder().encode(
          `${cursor ? `id: ${cursor}\n` : ""}data: ${JSON.stringify(delivery)}\n\n`,
        ),
      ),
    close: () => writer.close(),
  };
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

class Fixture {
  readonly stream = heldStream();
  readonly activityResponses: Response[] = [];
  readonly activityRequests: Array<{ search: string; signal: AbortSignal }> = [];
  readonly requests: Array<{ body: unknown; method: string; pathname: string }> = [];
  readonly verdicts: Array<Promise<Response>> = [];
  runtimeFeatures: string[] = [];
  sessionState = "running";

  readonly fetch = async (request: Request): Promise<Response> => {
    const url = new URL(request.url);
    const pathname = decodeURIComponent(url.pathname);
    const body = request.method === "GET" ? undefined : await request.clone().text();
    this.requests.push({
      body: body ? JSON.parse(body) : undefined,
      method: request.method,
      pathname,
    });
    if (pathname === "/api/v1/runtime")
      return json({
        capabilities: { image: false, posture: "managed" },
        connection: "online",
        features: this.runtimeFeatures,
      });
    if (pathname === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: true });
    if (pathname === "/api/v1/sessions")
      return json({
        complete: true,
        items: ["chat-a", "thread-a"].map((id) => ({
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
          id,
          modelId: "test-model",
          state: this.sessionState,
          title: `Chat ${id}`,
          titleProvenance: "",
          titleRevision: "0",
          turns: 0,
          updatedAt: "2026-09-24T12:00:00.000Z",
        })),
      });
    if (pathname.endsWith("/transcript"))
      return json({ complete: true, messages: [], sessionId: pathname.split("/")[4] });
    if (pathname.endsWith("/activity")) {
      this.activityRequests.push({ search: url.search, signal: request.signal });
      return this.activityResponses.shift() ?? this.stream.response;
    }
    if (/^\/api\/v1\/sessions\/[^/]+$/u.test(pathname))
      return json({
        capabilities: { image: false, manualCompaction: false, modelSelection: false },
        id: pathname.split("/")[4],
        mode: "default",
        state: this.sessionState,
        usage: {
          cacheReadTokens: "0",
          cacheWriteTokens: "0",
          inputTokens: "0",
          outputTokens: "0",
          reasoningTokens: "0",
        },
      });
    if (pathname.includes("/permissions/")) {
      const verdict = this.verdicts.shift();
      if (!verdict) throw new Error(`Unexpected verdict: ${pathname}`);
      return verdict;
    }
    if (pathname.includes("/plan-asks/")) {
      const verdict = this.verdicts.shift();
      if (!verdict) throw new Error(`Unexpected plan verdict: ${pathname}`);
      return verdict;
    }
    throw new Error(`Unexpected request: ${request.method} ${pathname}`);
  };

  posts() {
    return this.requests.filter(
      (request) => request.method === "POST" && request.pathname.includes("/permissions/"),
    );
  }
}

async function mount(fixture: Fixture, side = false) {
  setRequestRecoveryState({ identityEpoch: 0, phase: "ready", workspaceMounted: true });
  vi.stubGlobal("fetch", fixture.fetch);
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
    component: () =>
      side ? (
        <SideThreadPanel
          messageKey="message-a"
          onClose={() => {}}
          parentSessionId="chat-a"
          sessionId="thread-a"
        />
      ) : (
        <ChatWorkspace sessionId="chat-a" />
      ),
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
        <AuthRecoveryContext.Provider value={recovery}>
          <RouterProvider router={router} />
        </AuthRecoveryContext.Provider>
      </QueryClientProvider>,
    );
    await router.load();
  });
  await waitFor(() =>
    expect(fixture.requests.some((request) => request.pathname.endsWith("/activity"))).toBe(true),
  );
  return queryClient;
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ordinary approval interactions", () => {
  it("shows the active tool ask with inspectable arguments and diff", () => {
    const message: ChatMessage = {
      content: "",
      id: "assistant-a",
      role: "assistant",
      tools: [
        { args: editArgs, id: "call-a", name: "Edit", runId: "run-a" },
        { args: editArgs, id: "call-b", name: "Edit", runId: "run-b" },
      ],
    };
    const matched = ask();
    const unmatched = ask({
      askId: "ask-child",
      callId: "call-b",
      controlTarget: {
        askId: "ask-child",
        runId: "run-a",
        sessionId: "chat-a",
      },
    });
    const legacy = ask({
      askId: "ask-legacy",
      callId: undefined,
      controlTarget: {
        askId: "ask-legacy",
        runId: "run-a",
        sessionId: "chat-a",
      },
    });
    const child = ask({
      askId: "ask-child-only",
      callId: "nested-call",
      controlTarget: {
        askId: "ask-child-only",
        runId: "run-a",
        sessionId: "chat-a",
      },
    });
    render(
      <ChatTranscript
        approvals={[matched, unmatched, legacy, child]}
        messages={[message]}
        onRespondToApproval={() => {}}
        showToolCalls
      />,
    );
    const row = screen.getAllByText("Tool: Edit")[0]?.closest("li");
    const otherRunRow = screen.getAllByText("Tool: Edit")[1]?.closest("li");
    expect(row).not.toBeNull();
    expect(otherRunRow).not.toBeNull();
    expect(
      within(row as HTMLElement).getAllByRole("region", { name: "Permission required: Edit" }),
    ).toHaveLength(1);
    expect(
      within(otherRunRow as HTMLElement).queryByRole("region", {
        name: "Permission required: Edit",
      }),
    ).toBeNull();
    expect(screen.getAllByRole("region", { name: "Permission required: Edit" })).toHaveLength(4);
    const card = within(row as HTMLElement).getByRole("region", {
      name: "Permission required: Edit",
    });
    expect(within(card).getByText("Needs file access")).toBeTruthy();
    expect(within(card).getByTestId("edit-diff")).toBeTruthy();
    expect(within(card).getByText("before")).toBeTruthy();
    expect(within(card).getByText("after")).toBeTruthy();
    fireEvent.click(within(card).getByText("Raw arguments"));
    expect(within(card).getByText(editArgs)).toBeTruthy();
    cleanup();

    render(
      <Message
        agentAvatar=""
        agentName="Mecatl"
        approvals={[matched, unmatched]}
        message={message}
        onRespondToApproval={() => {}}
        showToolCalls
        streaming={false}
        threadDisabled
        userAvatar=""
        userName="You"
      />,
    );
    expect(screen.getAllByRole("region", { name: "Permission required: Edit" })).toHaveLength(2);
    expect(screen.getAllByText("Needs file access")).toHaveLength(2);
    cleanup();

    render(
      <ChatTranscript
        approvals={[ask({ args: "" })]}
        messages={[message]}
        onRespondToApproval={() => {}}
        showToolCalls
      />,
    );
    expect(screen.getByText("Arguments unavailable")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Deny" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect((screen.getByRole("button", { name: "Allow once" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    cleanup();

    render(
      <ChatTranscript
        approvals={[ask({ args: "{broken" })]}
        messages={[message]}
        onRespondToApproval={() => {}}
        showToolCalls
      />,
    );
    expect(screen.queryByTestId("edit-diff")).toBeNull();
    expect(screen.getByText("{broken")).toBeTruthy();
    cleanup();

    const longArgs = JSON.stringify({ path: "notes.txt", content: "line\n".repeat(20_000) });
    render(
      <ChatTranscript
        approvals={[ask({ args: longArgs, tool: "Write" })]}
        messages={[message]}
        onRespondToApproval={() => {}}
        showToolCalls
      />,
    );
    expect(screen.queryByTestId("write-block")).toBeNull();
    expect(screen.getByText(longArgs)).toBeTruthy();
    cleanup();

    render(
      <ChatTranscript
        approvals={[
          ask({ args: JSON.stringify({ path: "new.txt", content: "fresh" }), tool: "Write" }),
        ]}
        messages={[message]}
        onRespondToApproval={() => {}}
        showToolCalls
      />,
    );
    expect(screen.getByTestId("write-block")).toBeTruthy();
    expect(screen.getByText("fresh")).toBeTruthy();
    cleanup();

    render(
      <ChatTranscript
        approvals={[
          ask({
            args: JSON.stringify({ path: "many.txt", content: "line\n".repeat(60) }),
            tool: "Write",
          }),
        ]}
        messages={[message]}
        onRespondToApproval={() => {}}
        showToolCalls
      />,
    );
    expect(screen.getByTestId("write-block")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Show all/ })).toBeNull();
    expect(screen.getByText(/more diff lines/)).toBeTruthy();
    cleanup();

    const options = { newId: () => "assistant-a", now: 1, replay: true, sessionId: "chat-a" };
    const initial = initialRunDeliveryState("assistant-a", "");
    const asked = applyRunDelivery(
      initial,
      event("permission.ask", "1", "run-a", {
        askId: "ask-a",
        args: editArgs,
        callId: "call-a",
        tool: "Edit",
      }),
      options,
    );
    const retracted = applyRunDelivery(
      asked,
      event("permission.retract", "2", "run-a", { askId: "ask-a" }),
      options,
    );
    const repeated = applyRunDelivery(
      retracted,
      event("permission.ask", "3", "run-a", {
        askId: "ask-a",
        args: editArgs,
        callId: "call-a",
        tool: "Edit",
      }),
      options,
    );
    expect(repeated.approvals).toEqual([]);
    const approved = applyRunDelivery(
      asked,
      event("approval", "2", "run-a", { askId: "ask-a" }),
      options,
    );
    expect(
      applyRunDelivery(
        approved,
        event("permission.ask", "3", "run-a", {
          askId: "ask-a",
          args: editArgs,
          callId: "call-a",
          tool: "Edit",
        }),
        options,
      ).approvals,
    ).toEqual([]);
  });

  it("sends one POST when an ordinary verdict button is double-clicked", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";
      await act(async () => {
        fixture.stream.send({ runId: "run-a", sessionId, type: "run.started" });
      });
      expect(await screen.findByText("Esc to Stop")).toBeTruthy();
      await act(async () => {
        fixture.stream.send(
          event("permission.ask", "1", "run-a", {
            askId: "ask-a",
            args: editArgs,
            callId: "call-a",
            tool: "Edit",
          }),
        );
      });
      const card = await screen.findByRole("region", { name: "Permission required: Edit" });
      const button = within(card).getByRole("button", { name: "Allow once" });
      // Both clicks land before React re-renders the button as disabled.
      await act(async () => {
        button.click();
        button.click();
      });
      await waitFor(() => expect(fixture.posts()).toHaveLength(1));
      expect(fixture.posts()).toHaveLength(1);
      cleanup();
      fixture.stream.close();
    }
  });

  it("submits one ordinary verdict for the exact active run and ask", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      let answer!: (response: Response) => void;
      fixture.verdicts.push(
        new Promise<Response>((resolve) => {
          answer = resolve;
        }),
      );
      await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";
      await act(async () => {
        fixture.stream.send({ runId: "run-a", sessionId, type: "run.started" });
      });
      expect(await screen.findByText("Esc to Stop")).toBeTruthy();
      await act(async () => {
        fixture.stream.send(
          event("tool.call", "1", "run-a", { args: editArgs, id: "call-a", name: "Edit" }),
        );
        fixture.stream.send(
          event("permission.ask", "2", "run-a", {
            askId: "ask-a",
            args: editArgs,
            callId: "call-a",
            reason: "Needs file access",
            tool: "Edit",
          }),
        );
      });
      const card = await screen.findByRole("region", { name: "Permission required: Edit" });
      fireEvent.click(within(card).getByRole("button", { name: "Allow once" }));
      fireEvent.click(within(card).getByRole("button", { name: "Allow once" }));
      await waitFor(() => expect(fixture.posts()).toHaveLength(1));
      expect(fixture.posts()[0]).toEqual({
        body: { verdict: "allow_once" },
        method: "POST",
        pathname: `/api/v1/sessions/${sessionId}/runs/run-a/permissions/ask-a`,
      });
      expect(
        (within(card).getByRole("button", { name: "Deny" }) as HTMLButtonElement).disabled,
      ).toBe(true);
      await act(async () =>
        answer(
          new Response(JSON.stringify({ detail: "Ask already resolved" }), {
            headers: { "Content-Type": "application/problem+json" },
            status: 409,
          }),
        ),
      );
      expect(await screen.findByText(/outcome is uncertain/i)).toBeTruthy();
      fireEvent.click(within(card).getByRole("button", { name: "Deny" }));
      expect(fixture.posts()).toHaveLength(1);
      await act(async () =>
        fixture.stream.send(event("approval", "3", "run-a", { askId: "ask-a" })),
      );
      await waitFor(() =>
        expect(screen.queryByRole("region", { name: "Permission required: Edit" })).toBeNull(),
      );
      await act(async () =>
        fixture.stream.send(
          event("permission.ask", "3b", "run-a", {
            askId: "ask-a",
            args: editArgs,
            tool: "Edit",
          }),
        ),
      );
      expect(screen.queryByRole("region", { name: "Permission required: Edit" })).toBeNull();

      // A reused ask ID belongs to its own run. The old card cannot authorize it.
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      await act(async () => {
        fixture.stream.send(
          event("permission.ask", "4", "run-a", {
            askId: "ask-stale",
            args: editArgs,
            reason: "Old run",
            tool: "Edit",
          }),
        );
        fixture.stream.send({ runId: "run-b", sessionId, type: "run.started" });
        fixture.stream.send(
          event("permission.ask", "5", "run-b", {
            askId: "ask-a",
            args: editArgs,
            reason: "New run",
            tool: "Edit",
          }),
        );
      });
      const oldCard = screen.getByText("Old run").closest("section");
      const newCard = screen.getByText("New run").closest("section");
      expect(oldCard).not.toBeNull();
      expect(newCard).not.toBeNull();
      expect(
        (within(oldCard as HTMLElement).getByRole("button", { name: "Deny" }) as HTMLButtonElement)
          .disabled,
      ).toBe(true);
      fireEvent.click(within(oldCard as HTMLElement).getByRole("button", { name: "Deny" }));
      expect(fixture.posts()).toHaveLength(1);
      fireEvent.click(within(newCard as HTMLElement).getByRole("button", { name: "Deny" }));
      await waitFor(() => expect(fixture.posts()).toHaveLength(2));
      expect(fixture.posts()[1]).toEqual({
        body: { verdict: "deny" },
        method: "POST",
        pathname: `/api/v1/sessions/${sessionId}/runs/run-b/permissions/ask-a`,
      });
      await waitFor(() => expect(screen.queryByText("New run")).toBeNull());

      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      await act(async () => {
        fixture.stream.send({ runId: "run-c", sessionId, type: "run.started" });
        fixture.stream.send(
          event("permission.ask", "6", "run-c", {
            askId: "ask-c",
            args: editArgs,
            reason: "Always test",
            tool: "Edit",
          }),
        );
      });
      fireEvent.click(
        within(screen.getByText("Always test").closest("section") as HTMLElement).getByRole(
          "button",
          { name: "Always allow" },
        ),
      );
      await waitFor(() => expect(fixture.posts()).toHaveLength(3));
      expect(fixture.posts()[2]).toEqual({
        body: { verdict: "allow_always" },
        method: "POST",
        pathname: `/api/v1/sessions/${sessionId}/runs/run-c/permissions/ask-c`,
      });
      await waitFor(() => expect(screen.queryByText("Always test")).toBeNull());

      let loseAck!: (error: Error) => void;
      fixture.verdicts.push(
        new Promise<Response>((_resolve, reject) => {
          loseAck = reject;
        }),
      );
      await act(async () => {
        fixture.stream.send({ runId: "run-d", sessionId, type: "run.started" });
        fixture.stream.send(
          event("permission.ask", "7", "run-d", {
            askId: "ask-d",
            args: editArgs,
            reason: "Lost reply",
            tool: "Edit",
          }),
        );
      });
      const lostCard = screen.getByText("Lost reply").closest("section") as HTMLElement;
      fireEvent.click(within(lostCard).getByRole("button", { name: "Allow once" }));
      await waitFor(() => expect(fixture.posts()).toHaveLength(4));
      await act(async () => loseAck(new Error("connection lost")));
      expect(await within(lostCard).findByText(/outcome is uncertain/i)).toBeTruthy();
      fireEvent.click(within(lostCard).getByRole("button", { name: "Deny" }));
      expect(fixture.posts()).toHaveLength(4);

      if (side) {
        await act(async () => {
          fixture.stream.send({ runId: "run-e", sessionId, type: "run.started" });
          fixture.stream.send(
            event("permission.ask", "8", "run-e", {
              askId: "plan-e",
              args: JSON.stringify({ plan: "Keep scope" }),
              tool: "PresentPlan",
            }),
          );
        });
        expect(screen.getByRole("region", { name: "Plan review" })).toBeTruthy();
        expect(fixture.posts()).toHaveLength(4);
      }
      await act(async () => fixture.stream.close());
      expect(await screen.findByText(/outcome is uncertain/i)).toBeTruthy();
      cleanup();
    }
  }, 20_000);
});

describe("side-thread plan review", () => {
  it("reattaches each chat surface to the observed execution run after plan approval", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      fixture.runtimeFeatures = ["exact_plan_ask_control"];
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      const follow = heldStream();
      const execution = heldStream();
      fixture.activityResponses.push(fixture.stream.response, follow.response, execution.response);
      const queryClient = await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";
      await act(async () => {
        fixture.stream.send({ runId: "run-plan", sessionId, type: "run.started" });
        fixture.stream.send(
          event("permission.ask", "1", "run-plan", {
            args: '{"plan":"Execute this"}',
            askId: "ask-plan",
            reason: "Review",
            tool: "PresentPlan",
          }),
        );
      });
      fireEvent.click(screen.getByRole("button", { name: "Approve & run" }));
      await waitFor(() =>
        expect(
          fixture.requests.filter((request) => request.pathname.endsWith("/activity")),
        ).toHaveLength(2),
      );
      await act(async () => {
        follow.send(event("result", "2", "run-plan", { stop: "plan_approved" }), "plan-terminal");
        follow.send({ runId: "run-execution", sessionId, type: "run.started" });
      });
      await waitFor(() =>
        expect(
          fixture.requests.filter((request) => request.pathname.endsWith("/activity")),
        ).toHaveLength(3),
      );
      expect(
        fixture.requests.filter((request) => request.pathname.endsWith("/activity"))[2]?.pathname,
      ).toBe(`/api/v1/sessions/${sessionId}/activity`);
      expect(fixture.activityRequests[2]?.search).toBe("?resumeFrom=plan-terminal");
      expect(fixture.activityRequests[0]?.signal.aborted).toBe(true);
      fixture.sessionState = "idle";
      await act(async () => {
        await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
      });
      expect(fixture.activityRequests[2]?.signal.aborted).toBe(false);
      await act(async () => {
        execution.send({ runId: "run-execution", sessionId, type: "run.started" });
        execution.send(event("message.delta", "1", "run-execution", undefined, "Execution output"));
        execution.send(
          event("permission.ask", "2", "run-execution", {
            askId: "ask-execution",
            args: editArgs,
            reason: "Execution permission",
            tool: "Edit",
          }),
        );
      });
      expect(await screen.findByText("Execution output")).toBeTruthy();
      const card = screen.getByText("Execution permission").closest("section") as HTMLElement;
      expect(within(card).getByRole("button", { name: "Allow once" })).toBeTruthy();
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      fireEvent.click(within(card).getByRole("button", { name: "Allow once" }));
      await waitFor(() =>
        expect(
          fixture.requests.some(
            (request) =>
              request.pathname ===
              `/api/v1/sessions/${sessionId}/runs/run-execution/permissions/ask-execution`,
          ),
        ).toBe(true),
      );
      cleanup();
    }
  });

  it("iterates the exact plan ask in its independent transcript", async () => {
    const fixture = new Fixture();
    fixture.runtimeFeatures = ["exact_plan_ask_control"];
    fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
    await mount(fixture, true);
    await act(async () => {
      fixture.stream.send({ runId: "run-plan", sessionId: "thread-a", type: "run.started" });
      fixture.stream.send(
        event("tool.call", "1", "run-plan", {
          args: '{"plan":"Side plan"}',
          id: "call-plan",
          name: "PresentPlan",
        }),
      );
      fixture.stream.send(
        event("permission.ask", "2", "run-plan", {
          args: '{"plan":"Side plan","note":"Check side thread"}',
          askId: "ask-plan",
          callId: "call-plan",
          reason: "Review",
          tool: "PresentPlan",
        }),
      );
    });
    const card = await screen.findByRole("region", { name: "Plan review" });
    expect(within(card).getByText("Side plan")).toBeTruthy();
    expect(within(card).getByText("Check side thread")).toBeTruthy();
    fireEvent.click(within(card).getByRole("button", { name: "Iterate" }));
    await waitFor(() =>
      expect(
        fixture.requests.filter((request) => request.pathname.includes("/plan-asks/")),
      ).toEqual([
        {
          body: { verdict: "iterate" },
          method: "POST",
          pathname: "/api/v1/sessions/thread-a/runs/run-plan/plan-asks/ask-plan",
        },
      ]),
    );
  });
});

describe("shortcut hints", () => {
  it("renders truthful shortcut hints and restores focus", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";
      await act(async () => {
        fixture.stream.send({ runId: "run-a", sessionId, type: "run.started" });
        fixture.stream.send(
          event("permission.ask", "1", "run-a", {
            askId: "ask-a",
            args: editArgs,
            reason: "Needs file access",
            tool: "Edit",
          }),
        );
      });
      const card = await screen.findByRole("region", { name: "Permission required: Edit" });
      expect(within(card).getByText("Esc to Deny")).toBeTruthy();
      expect(card.querySelector('[data-diff="added"]')?.textContent).toContain("Added line");
      expect(card.querySelector('[data-diff="removed"]')?.textContent).toContain("Removed line");
      expect(screen.queryByText("Esc to Stop")).toBeNull();
      const deny = within(card).getByRole("button", { name: "Deny" });
      deny.focus();
      expect(document.activeElement).toBe(deny);
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      await userEvent.setup().keyboard(side ? " " : "{Enter}");
      await waitFor(() => expect(fixture.posts()).toHaveLength(1));
      expect(fixture.posts()[0]?.body).toEqual({ verdict: "deny" });
      expect(fixture.requests.filter((request) => request.pathname.endsWith("/runs"))).toHaveLength(
        0,
      );
      cleanup();
    }

    const planAsk = ask({ args: '{"plan":"Review first"}', tool: "PresentPlan" });
    const rendered = render(
      <EscapeHintContext.Provider value={planAsk}>
        <PlanReviewCard approval={planAsk} disabled={false} onRespond={() => {}} />
      </EscapeHintContext.Provider>,
    );
    expect(screen.getByText("Esc to Iterate")).toBeTruthy();
    rendered.rerender(
      <EscapeHintContext.Provider value={planAsk}>
        <PlanReviewCard approval={planAsk} disabled onRespond={() => {}} />
      </EscapeHintContext.Provider>,
    );
    expect(screen.queryByText("Esc to Iterate")).toBeNull();
    cleanup();

    const fixture = new Fixture();
    fixture.sessionState = "idle";
    await mount(fixture);
    const canvas = screen.getByRole("button", { name: "Open local canvas" });
    canvas.focus();
    fireEvent.click(canvas);
    expect(await screen.findByText("Esc to Close")).toBeTruthy();
    const close = screen
      .getAllByRole("button", { name: "Close panel" })
      .at(-1) as HTMLButtonElement;
    close.focus();
    fireEvent.keyDown(close, { key: "Escape" });
    fireEvent.keyUp(close, { key: "Escape" });
    await waitFor(() => expect(screen.queryByText("Esc to Close")).toBeNull());
    expect(document.activeElement).toBe(canvas);
    fireEvent.click(canvas);
    const closeAgain = screen
      .getAllByRole("button", { name: "Close panel" })
      .at(-1) as HTMLButtonElement;
    closeAgain.focus();
    fireEvent.click(closeAgain);
    expect(document.activeElement).toBe(canvas);
    const composer = screen.getByRole("textbox", { name: "Message Mecatl" });
    await userEvent.setup().type(composer, "unsent draft");
    expect(await screen.findByText("Esc twice to clear draft")).toBeTruthy();
  });
});

// Pinned before the prototype's approval UI landed (#2207): the verdict order and
// tab order, the composer never answering an ask, and each verdict-ledger phase
// as the card shows it, on both chat surfaces.
describe("approval keyboard and verdict phases", () => {
  const verdictNames = ["Allow once", "Always allow", "Deny"] as const;

  function verdictButtons(card: HTMLElement): HTMLButtonElement[] {
    return verdictNames.map(
      (name) => within(card).getByRole("button", { name }) as HTMLButtonElement,
    );
  }

  function tabStops(card: HTMLElement): HTMLElement[] {
    return [
      ...card.querySelectorAll<HTMLElement>(
        "a[href], button, input, select, summary, textarea, [tabindex]",
      ),
    ].filter(
      (element) =>
        !(element as HTMLButtonElement).disabled && element.getAttribute("tabindex") !== "-1",
    );
  }

  async function startAsk(fixture: Fixture, sessionId: string, runId: string, reason: string) {
    await act(async () => {
      fixture.stream.send({ runId, sessionId, type: "run.started" });
      fixture.stream.send(
        event("permission.ask", "1", runId, {
          askId: `ask-${runId}`,
          args: editArgs,
          reason,
          tool: "Edit",
        }),
      );
    });
    return (await screen.findByText(reason)).closest("section") as HTMLElement;
  }

  it("keeps the verdict buttons as the card's last tab stops, in verdict order", () => {
    render(
      <ChatTranscript
        approvals={[ask()]}
        messages={[]}
        onRespondToApproval={() => {}}
        showToolCalls
      />,
    );
    const card = screen.getByRole("region", { name: "Permission required: Edit" });
    expect(tabStops(card).slice(-3)).toEqual(verdictButtons(card));
  });

  it("never answers an ask from keys typed in the composer", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const card = await startAsk(fixture, "chat-a", "run-a", "Typing test");
    const composer = screen.getByRole("textbox", { name: "Message Mecatl" });
    await userEvent.setup().type(composer, "yawnd YAWND{ArrowLeft}{ArrowRight}");
    expect((composer as HTMLTextAreaElement).value).toBe("yawnd YAWND");
    expect(fixture.posts()).toHaveLength(0);
    expect(verdictButtons(card).map((button) => button.disabled)).toEqual([false, false, false]);
  });

  it("shows each verdict phase on the card and ignores keys once the ask blocks", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";

      // idle: every verdict is offered, and Escape denies.
      const card = await startAsk(fixture, sessionId, "run-a", "Phase test");
      expect(verdictButtons(card).map((button) => button.disabled)).toEqual([false, false, false]);
      expect(within(card).getByText("Esc to Deny")).toBeTruthy();

      // inFlight: one POST, every verdict blocked, no Escape hint, keys ignored.
      let answer!: (response: Response) => void;
      fixture.verdicts.push(new Promise<Response>((resolve) => (answer = resolve)));
      fireEvent.click(within(card).getByRole("button", { name: "Allow once" }));
      await waitFor(() => expect(fixture.posts()).toHaveLength(1));
      expect(verdictButtons(card).map((button) => button.disabled)).toEqual([true, true, true]);
      expect(within(card).queryByText("Esc to Deny")).toBeNull();
      for (const key of ["y", "a", "w", "n", "d", "Escape"]) {
        fireEvent.keyDown(card, { key });
        fireEvent.keyUp(card, { key });
      }
      expect(fixture.posts()).toHaveLength(1);

      // acknowledged: the card leaves and the verdict is reported.
      await act(async () => answer(new Response(null, { status: 204 })));
      await waitFor(() => expect(screen.queryByText("Phase test")).toBeNull());
      expect(await screen.findByText("Permission allow once recorded.")).toBeTruthy();

      // uncertain: the card stays, says so, and nothing can retry it.
      let lose!: (error: Error) => void;
      fixture.verdicts.push(new Promise<Response>((_resolve, reject) => (lose = reject)));
      const lostCard = await startAsk(fixture, sessionId, "run-b", "Uncertain test");
      fireEvent.click(within(lostCard).getByRole("button", { name: "Deny" }));
      await waitFor(() => expect(fixture.posts()).toHaveLength(2));
      await act(async () => lose(new Error("connection lost")));
      expect(await within(lostCard).findByText(/outcome is uncertain/i)).toBeTruthy();
      expect(verdictButtons(lostCard).map((button) => button.disabled)).toEqual([true, true, true]);
      expect(within(lostCard).queryByText("Esc to Deny")).toBeNull();
      for (const key of ["y", "a", "w", "n", "d", "Escape"]) {
        fireEvent.keyDown(lostCard, { key });
        fireEvent.keyUp(lostCard, { key });
        fireEvent.keyDown(document.body, { key });
        fireEvent.keyUp(document.body, { key });
      }
      expect(fixture.posts()).toHaveLength(2);
      cleanup();
    }
  });

  it("shows each plan verdict phase on the plan card", async () => {
    const unavailable = new Fixture();
    await mount(unavailable);
    await act(async () => {
      unavailable.stream.send({ runId: "run-plan", sessionId: "chat-a", type: "run.started" });
      unavailable.stream.send(
        event("permission.ask", "1", "run-plan", {
          args: '{"plan":"Ship it"}',
          askId: "ask-plan",
          reason: "Review",
          tool: "PresentPlan",
        }),
      );
    });
    const blocked = await screen.findByRole("region", { name: "Plan review" });
    expect(
      within(blocked).getByText("Exact plan review is unavailable on this Mecatl server."),
    ).toBeTruthy();
    expect(within(blocked).queryByRole("button", { name: "Approve & run" })).toBeNull();
    expect(within(blocked).queryByText("Esc to Iterate")).toBeNull();
    cleanup();

    const fixture = new Fixture();
    fixture.runtimeFeatures = ["exact_plan_ask_control"];
    await mount(fixture);
    let lose!: (error: Error) => void;
    fixture.verdicts.push(new Promise<Response>((_resolve, reject) => (lose = reject)));
    await act(async () => {
      fixture.stream.send({ runId: "run-plan", sessionId: "chat-a", type: "run.started" });
      fixture.stream.send(
        event("permission.ask", "1", "run-plan", {
          args: '{"plan":"Ship it"}',
          askId: "ask-plan",
          reason: "Review",
          tool: "PresentPlan",
        }),
      );
    });
    const card = await screen.findByRole("region", { name: "Plan review" });
    const planButtons = () =>
      ["Approve & run", "Auto-accept edits", "Iterate"].map(
        (name) => (within(card).getByRole("button", { name }) as HTMLButtonElement).disabled,
      );
    expect(planButtons()).toEqual([false, false, false]);
    expect(within(card).getByText("Esc to Iterate")).toBeTruthy();
    fireEvent.click(within(card).getByRole("button", { name: "Approve & run" }));
    await waitFor(() =>
      expect(fixture.requests.filter((request) => request.method === "POST")).toHaveLength(1),
    );
    expect(planButtons()).toEqual([true, true, true]);
    expect(within(card).queryByText("Esc to Iterate")).toBeNull();
    await act(async () => lose(new Error("connection lost")));
    expect(await within(card).findByText(/outcome is uncertain/i)).toBeTruthy();
    expect(planButtons()).toEqual([true, true, true]);
    fireEvent.keyDown(card, { key: "Escape" });
    fireEvent.keyUp(card, { key: "Escape" });
    expect(fixture.requests.filter((request) => request.method === "POST")).toHaveLength(1);
  });
});

describe("prototype approval UI on the workspace ledger", () => {
  async function startAsk(
    fixture: Fixture,
    sessionId: string,
    runId: string,
    reason: string,
    args = editArgs,
    tool = "Edit",
  ) {
    await act(async () => {
      fixture.stream.send({ runId, sessionId, type: "run.started" });
      fixture.stream.send(
        event("permission.ask", "1", runId, { askId: `ask-${runId}`, args, reason, tool }),
      );
    });
    return (await screen.findByText(reason)).closest("section") as HTMLElement;
  }

  it("answers the active ask from the keyboard once, through the exact verdict route", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";
      (document.activeElement as HTMLElement | null)?.blur();
      const card = await startAsk(fixture, sessionId, "run-k", "Keyboard ask");
      // The ask never takes focus; the reader puts it in the card.
      expect(document.activeElement).toBe(document.body);
      within(card).getByRole("button", { name: "Raw arguments" }).focus();
      let answer!: (response: Response) => void;
      fixture.verdicts.push(new Promise<Response>((resolve) => (answer = resolve)));
      await userEvent.setup().keyboard("y");
      await waitFor(() => expect(fixture.posts()).toHaveLength(1));
      await userEvent.setup().keyboard("ynw");
      expect(fixture.posts()).toEqual([
        {
          body: { verdict: "allow_once" },
          method: "POST",
          pathname: `/api/v1/sessions/${sessionId}/runs/run-k/permissions/ask-run-k`,
        },
      ]);
      await act(async () => answer(new Response(null, { status: 204 })));
      await waitFor(() => expect(screen.queryByText("Keyboard ask")).toBeNull());
      cleanup();
    }
  });

  it("opens an ask in the detail panel and answers it there through the same ledger", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const longWrite = JSON.stringify({ content: "line\n".repeat(60), path: "many.txt" });
    const card = await startAsk(fixture, "chat-a", "run-d", "Detail ask", longWrite, "Write");
    expect(within(card).queryByRole("button", { name: /Show all/ })).toBeNull();
    fireEvent.click(within(card).getByRole("button", { name: "Open in detail panel" }));
    const panel = await screen.findByRole("complementary", { name: "Write" });
    expect(within(panel).getByText("Detail ask")).toBeTruthy();
    expect(within(panel).getByRole("button", { name: /Show all 61 lines/ })).toBeTruthy();
    expect(within(panel).queryByText("Esc to Close")).toBeNull();

    let lose!: (error: Error) => void;
    fixture.verdicts.push(new Promise<Response>((_resolve, reject) => (lose = reject)));
    fireEvent.click(within(panel).getByRole("button", { name: "Always allow" }));
    await waitFor(() => expect(fixture.posts()).toHaveLength(1));
    expect(fixture.posts()[0]).toEqual({
      body: { verdict: "allow_always" },
      method: "POST",
      pathname: "/api/v1/sessions/chat-a/runs/run-d/permissions/ask-run-d",
    });
    // The card and the panel read one ledger: both block while it is in flight.
    for (const surface of [card, panel]) {
      expect(
        ["Allow once", "Always allow", "Deny"].map(
          (name) => (within(surface).getByRole("button", { name }) as HTMLButtonElement).disabled,
        ),
      ).toEqual([true, true, true]);
    }
    await act(async () => lose(new Error("connection lost")));
    expect(await within(panel).findByText(/outcome is uncertain/i)).toBeTruthy();
    expect(within(card).getByText(/outcome is uncertain/i)).toBeTruthy();
    fireEvent.click(within(panel).getByRole("button", { name: "Deny" }));
    fireEvent.keyDown(panel, { key: "n" });
    expect(fixture.posts()).toHaveLength(1);

    // A stream event settles the ask; its panel closes with its card.
    await act(async () =>
      fixture.stream.send(event("approval", "2", "run-d", { askId: "ask-run-d" })),
    );
    await waitFor(() => expect(screen.queryByText("Detail ask")).toBeNull());
    expect(screen.queryByRole("complementary", { name: "Write" })).toBeNull();
  });

  it("closes the detail panel once the verdict is acknowledged", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const card = await startAsk(fixture, "chat-a", "run-e", "Acknowledged ask");
    fireEvent.click(within(card).getByRole("button", { name: "Open in detail panel" }));
    const panel = await screen.findByRole("complementary", { name: "Edit" });
    fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
    fireEvent.click(within(panel).getByRole("button", { name: "Deny" }));
    await waitFor(() => expect(screen.queryByRole("complementary", { name: "Edit" })).toBeNull());
    expect(screen.queryByText("Acknowledged ask")).toBeNull();
    expect(fixture.posts()).toEqual([
      {
        body: { verdict: "deny" },
        method: "POST",
        pathname: "/api/v1/sessions/chat-a/runs/run-e/permissions/ask-run-e",
      },
    ]);
  });

  it("moves focus to the composer when the detail panel closes after a verdict", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const card = await startAsk(fixture, "chat-a", "run-f", "Focus ask");
    // A real click focuses the opener, which leaves with its card once the ask settles.
    const opener = within(card).getByRole("button", { name: "Open in detail panel" });
    opener.focus();
    fireEvent.click(opener);
    const panel = await screen.findByRole("complementary", { name: "Edit" });
    const deny = within(panel).getByRole("button", { name: "Deny" });
    deny.focus();
    fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
    fireEvent.click(deny);
    await waitFor(() => expect(screen.queryByRole("complementary", { name: "Edit" })).toBeNull());
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Message Mecatl" })),
    );
  });

  it("moves focus to the composer when the stream retracts the ask under the panel", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const card = await startAsk(fixture, "chat-a", "run-g", "Retracted ask");
    // A real click focuses the opener, which leaves with its card once the ask settles.
    const opener = within(card).getByRole("button", { name: "Open in detail panel" });
    opener.focus();
    fireEvent.click(opener);
    const panel = await screen.findByRole("complementary", { name: "Edit" });
    within(panel).getByRole("button", { name: "Deny" }).focus();
    await act(async () =>
      fixture.stream.send(event("approval", "2", "run-g", { askId: "ask-run-g" })),
    );
    await waitFor(() => expect(screen.queryByRole("complementary", { name: "Edit" })).toBeNull());
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Message Mecatl" })),
    );
  });

  it("leaves focus alone when the reader already moved it before the panel closed", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const card = await startAsk(fixture, "chat-a", "run-h", "Elsewhere ask");
    // A real click focuses the opener, which leaves with its card once the ask settles.
    const opener = within(card).getByRole("button", { name: "Open in detail panel" });
    opener.focus();
    fireEvent.click(opener);
    const panel = await screen.findByRole("complementary", { name: "Edit" });
    const elsewhere = screen.getByRole("button", { name: "Chat options" });
    elsewhere.focus();
    await act(async () =>
      fixture.stream.send(event("approval", "2", "run-h", { askId: "ask-run-h" })),
    );
    await waitFor(() => expect(panel.isConnected).toBe(false));
    expect(document.activeElement).toBe(elsewhere);
  });

  it("offers the detail panel only where the surface has a panel slot", async () => {
    const fixture = new Fixture();
    await mount(fixture, true);
    const card = await startAsk(fixture, "thread-a", "run-s", "Thread ask");
    expect(within(card).queryByRole("button", { name: "Open in detail panel" })).toBeNull();
  });
});

// #2251 review: letter keys answer only the card or panel that holds focus,
// a new ask never takes focus, and Escape in a detail panel never denies a
// different ask.
describe("approval focus scope", () => {
  async function sendAsks(
    fixture: Fixture,
    runId: string,
    asks: Array<{ askId: string; reason: string }>,
    sessionId = "chat-a",
  ) {
    await act(async () => {
      fixture.stream.send({ runId, sessionId, type: "run.started" });
      for (const [index, { askId, reason }] of asks.entries()) {
        fixture.stream.send(
          event("permission.ask", String(index + 1), runId, {
            args: editArgs,
            askId,
            reason,
            tool: "Edit",
          }),
        );
      }
    });
    const cards = [];
    for (const { reason } of asks)
      cards.push((await screen.findByText(reason)).closest("section") as HTMLElement);
    return cards;
  }

  const pair = [
    { askId: "ask-1", reason: "First ask" },
    { askId: "ask-2", reason: "Second ask" },
  ];

  it("never answers the active ask from another card or from the page body", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const [first, second] = (await sendAsks(fixture, "run-a", pair)) as [HTMLElement, HTMLElement];
    const user = userEvent.setup();
    within(second).getByRole("button", { name: "Raw arguments" }).focus();
    await user.keyboard("yawnd");
    within(second).getByRole("button", { name: "Deny" }).focus();
    await user.keyboard("yw");
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);
    await user.keyboard("Why does it need this?");
    expect(fixture.posts()).toEqual([]);
    for (const card of [first, second])
      expect(
        ["Allow once", "Always allow", "Deny"].map(
          (name) => (within(card).getByRole("button", { name }) as HTMLButtonElement).disabled,
        ),
      ).toEqual([false, false, false]);
  });

  it("never takes focus for a new ask, so a blind W or Enter grants nothing", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    (document.activeElement as HTMLElement | null)?.blur();
    const user = userEvent.setup();
    const [card] = (await sendAsks(fixture, "run-w", [
      { askId: "ask-w", reason: "Blind ask" },
    ])) as [HTMLElement];
    expect(within(card).getByText("Esc to Deny")).toBeTruthy();
    await user.keyboard("W");
    await user.keyboard("{Enter}y");
    expect(fixture.posts()).toEqual([]);
    expect(document.activeElement).toBe(document.body);
    // Once the reader focuses the card, the keys answer it through the exact route.
    within(card).getByRole("button", { name: "Raw arguments" }).focus();
    fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
    await user.keyboard("n");
    await waitFor(() => expect(fixture.posts()).toHaveLength(1));
    expect(fixture.posts()[0]).toEqual({
      body: { verdict: "deny" },
      method: "POST",
      pathname: "/api/v1/sessions/chat-a/runs/run-w/permissions/ask-w",
    });
  });

  it("leaves focus alone for the next ask after a keyboard verdict, so stray keys answer nothing", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    (document.activeElement as HTMLElement | null)?.blur();
    const user = userEvent.setup();
    const [first] = (await sendAsks(fixture, "run-k", [
      { askId: "ask-k", reason: "Keyboard ask" },
    ])) as [HTMLElement];
    within(first).getByRole("button", { name: "Raw arguments" }).focus();
    fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
    await user.keyboard("y");
    await waitFor(() => expect(screen.queryByText("Keyboard ask")).toBeNull());

    const [next] = (await sendAsks(fixture, "run-m", [{ askId: "ask-m", reason: "Next ask" }])) as [
      HTMLElement,
    ];
    expect(next.contains(document.activeElement)).toBe(false);
    await user.keyboard("{Enter}wyn");
    expect(fixture.posts()).toEqual([
      {
        body: { verdict: "allow_once" },
        method: "POST",
        pathname: "/api/v1/sessions/chat-a/runs/run-k/permissions/ask-k",
      },
    ]);
    expect(
      ["Allow once", "Always allow", "Deny"].map(
        (name) => (within(next).getByRole("button", { name }) as HTMLButtonElement).disabled,
      ),
    ).toEqual([false, false, false]);
  });

  it("answers the detail panel's ask once from focus anywhere in its frame", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const [card] = (await sendAsks(fixture, "run-a", [pair[0] as (typeof pair)[number]])) as [
      HTMLElement,
    ];
    fireEvent.click(within(card).getByRole("button", { name: "Open in detail panel" }));
    const panel = await screen.findByRole("complementary", { name: "Edit" });
    // The frame's close button takes focus when the panel opens.
    expect(document.activeElement).toBe(within(panel).getByRole("button", { name: "Close panel" }));
    let answer!: (response: Response) => void;
    fixture.verdicts.push(new Promise<Response>((resolve) => (answer = resolve)));
    await userEvent.setup().keyboard("w");
    await waitFor(() => expect(fixture.posts()).toHaveLength(1));
    expect(fixture.posts()[0]).toEqual({
      body: { verdict: "allow_always" },
      method: "POST",
      pathname: "/api/v1/sessions/chat-a/runs/run-a/permissions/ask-1",
    });
    await act(async () => answer(new Response(null, { status: 204 })));
    await waitFor(() => expect(screen.queryByRole("complementary", { name: "Edit" })).toBeNull());
    expect(fixture.posts()).toHaveLength(1);
  });

  it("closes a detail panel on Escape instead of denying a different ask", async () => {
    const fixture = new Fixture();
    await mount(fixture);
    const [first, second] = (await sendAsks(fixture, "run-a", pair)) as [HTMLElement, HTMLElement];
    const user = userEvent.setup();

    // The panel shows the second ask; the active ask is the first.
    fireEvent.click(within(second).getByRole("button", { name: "Open in detail panel" }));
    let panel = await screen.findByRole("complementary", { name: "Edit" });
    expect(within(panel).getByText("Second ask")).toBeTruthy();
    expect(panel.contains(document.activeElement)).toBe(true);
    await user.keyboard("{Escape}");
    await waitFor(() => {
      expect(fixture.posts()).toEqual([]);
      expect(screen.queryByRole("complementary", { name: "Edit" })).toBeNull();
    });
    expect(screen.getByText("First ask")).toBeTruthy();
    expect(screen.getByText("Second ask")).toBeTruthy();
    // Its hints say so: Escape closes this panel, and no key answers its ask.
    fireEvent.click(within(second).getByRole("button", { name: "Open in detail panel" }));
    panel = await screen.findByRole("complementary", { name: "Edit" });
    expect(within(panel).getByText("Esc to Close")).toBeTruthy();
    // Under the verdicts, not in the header, where it would squeeze the tool name.
    expect(panel.querySelector("header")?.textContent).not.toContain("Esc to Close");
    expect(within(panel).queryByText("Esc to Deny")).toBeNull();
    expect(panel.querySelector("kbd")).toBeNull();
    fireEvent.click(within(panel).getByRole("button", { name: "Close panel" }));

    // A panel showing the active ask still denies it, as its card does.
    fireEvent.click(within(first).getByRole("button", { name: "Open in detail panel" }));
    panel = await screen.findByRole("complementary", { name: "Edit" });
    expect(within(panel).getByText("Esc to Deny")).toBeTruthy();
    expect(within(panel).queryByText("Esc to Close")).toBeNull();
    fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(fixture.posts()).toHaveLength(1));
    expect(fixture.posts()[0]).toEqual({
      body: { verdict: "deny" },
      method: "POST",
      pathname: "/api/v1/sessions/chat-a/runs/run-a/permissions/ask-1",
    });
  });

  it("denies the active ask on Escape from its own card or from outside every card", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";
      const [first, second] = (await sendAsks(fixture, "run-a", pair, sessionId)) as [
        HTMLElement,
        HTMLElement,
      ];
      const user = userEvent.setup();
      const deny = (askId: string) => ({
        body: { verdict: "deny" },
        method: "POST",
        pathname: `/api/v1/sessions/${sessionId}/runs/run-a/permissions/${askId}`,
      });

      // From inside the active ask's own card: a held key denies nothing, a press denies it.
      expect(within(first).getByText("Esc to Deny")).toBeTruthy();
      const denyButton = within(first).getByRole("button", { name: "Deny" });
      denyButton.focus();
      fireEvent.keyDown(denyButton, { key: "Escape", repeat: true });
      fireEvent.keyUp(denyButton, { key: "Escape" });
      expect(fixture.posts()).toEqual([]);
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByText("First ask")).toBeNull());
      expect(fixture.posts()).toEqual([deny("ask-1")]);

      // From outside every card, now that the second ask is the active one: the
      // workspace's composer, or a thread control (the thread's composer is
      // disabled while its run waits).
      expect(within(second).getByText("Esc to Deny")).toBeTruthy();
      screen
        .getByRole(side ? "button" : "textbox", {
          name: side ? "Thread options" : "Message Mecatl",
        })
        .focus();
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByText("Second ask")).toBeNull());
      expect(fixture.posts()).toEqual([deny("ask-1"), deny("ask-2")]);
      cleanup();
    }
  });

  it("answers no ask on Escape from another ask's card", async () => {
    for (const side of [false, true]) {
      const fixture = new Fixture();
      await mount(fixture, side);
      const sessionId = side ? "thread-a" : "chat-a";
      const [first, second] = (await sendAsks(fixture, "run-a", pair, sessionId)) as [
        HTMLElement,
        HTMLElement,
      ];
      const user = userEvent.setup();
      // The first ask is the active one; focus sits in the second ask's card.
      expect(within(first).getByText("Esc to Deny")).toBeTruthy();
      expect(within(second).queryByText("Esc to Deny")).toBeNull();
      for (const name of ["Raw arguments", "Allow once", "Deny"]) {
        within(second).getByRole("button", { name }).focus();
        await user.keyboard("{Escape}");
      }
      // No verdict for either ask, and no stop: nothing is posted at all.
      expect(fixture.requests.filter((request) => request.method === "POST")).toEqual([]);
      for (const card of [first, second])
        expect(
          ["Allow once", "Always allow", "Deny"].map(
            (name) => (within(card).getByRole("button", { name }) as HTMLButtonElement).disabled,
          ),
        ).toEqual([false, false, false]);

      // Escape from the active ask's own card still denies it, and only it.
      within(first).getByRole("button", { name: "Raw arguments" }).focus();
      fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
      await user.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByText("First ask")).toBeNull());
      expect(fixture.posts()).toEqual([
        {
          body: { verdict: "deny" },
          method: "POST",
          pathname: `/api/v1/sessions/${sessionId}/runs/run-a/permissions/ask-1`,
        },
      ]);
      expect(screen.getByText("Second ask")).toBeTruthy();
      cleanup();
    }
  });

  it("answers no ask on Escape from a plan card that isn't the active ask's", async () => {
    const fixture = new Fixture();
    fixture.runtimeFeatures = ["exact_plan_ask_control"];
    await mount(fixture);
    await act(async () => {
      fixture.stream.send({ runId: "run-a", sessionId: "chat-a", type: "run.started" });
      fixture.stream.send(
        event("permission.ask", "1", "run-a", {
          args: editArgs,
          askId: "ask-1",
          reason: "First ask",
          tool: "Edit",
        }),
      );
      fixture.stream.send(
        event("permission.ask", "2", "run-a", {
          args: '{"plan":"Ship it"}',
          askId: "ask-plan",
          reason: "Plan ask",
          tool: "PresentPlan",
        }),
      );
    });
    const first = (await screen.findByText("First ask")).closest("section") as HTMLElement;
    const plan = await screen.findByRole("region", { name: "Plan review" });
    const user = userEvent.setup();
    expect(within(first).getByText("Esc to Deny")).toBeTruthy();
    expect(within(plan).queryByText("Esc to Iterate")).toBeNull();
    for (const name of ["Raw arguments", "Iterate"]) {
      within(plan).getByRole("button", { name }).focus();
      await user.keyboard("{Escape}");
    }
    expect(fixture.requests.filter((request) => request.method === "POST")).toEqual([]);

    // From the active ask's own card, Escape denies that ask, never the plan.
    within(first).getByRole("button", { name: "Raw arguments" }).focus();
    fixture.verdicts.push(Promise.resolve(new Response(null, { status: 204 })));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByText("First ask")).toBeNull());
    expect(fixture.requests.filter((request) => request.method === "POST")).toEqual([
      {
        body: { verdict: "deny" },
        method: "POST",
        pathname: "/api/v1/sessions/chat-a/runs/run-a/permissions/ask-1",
      },
    ]);
    // The plan is now the active ask, and its card says Escape iterates it.
    expect(within(plan).getByText("Esc to Iterate")).toBeTruthy();
  });
});
