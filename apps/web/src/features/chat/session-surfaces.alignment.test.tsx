// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * The session surfaces the prototype adds on Studio's rules: the chat
 * options' direct entries to each dialog, the conversation's select and copy,
 * the list rows' menu, and the dialogs' extra facts.
 */

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
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
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
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

const usage = {
  cacheReadTokens: "11",
  cacheWriteTokens: "12",
  inputTokens: "13",
  outputTokens: "14",
  reasoningTokens: "15",
};

function row(id: string, inspectOnly = false): SessionSummaryResponse {
  return {
    capabilities: {
      copyId: true,
      copyIdReason: "",
      delete: !inspectOnly,
      deleteReason: inspectOnly ? "inspect_only_kind" : "",
      fork: !inspectOnly,
      forkReason: inspectOnly ? "inspect_only_kind" : "",
      inspect: true,
      inspectReason: "",
      publicChat: !inspectOnly,
      publicChatReason: inspectOnly ? "inspect_only_kind" : "",
      rename: !inspectOnly,
      renameReason: inspectOnly ? "inspect_only_kind" : "",
      viewTranscript: true,
      viewTranscriptReason: "",
    },
    createdAt: "2026-09-24T12:00:00.000Z",
    debugTargetSessionId: "",
    id,
    kind: inspectOnly ? "subagent" : "main",
    modelId: "model-a",
    state: "idle",
    title: inspectOnly ? "Recorded worker" : "Main chat",
    titleProvenance: "",
    titleRevision: "0",
    turns: 2,
    updatedAt: "2026-09-24T12:00:00.000Z",
  };
}

type RecordedRequest = { method: string; path: string; body?: unknown };

class Bff {
  rows: SessionSummaryResponse[] = [row("chat")];
  requests: RecordedRequest[] = [];
  capabilities = { soul: true, worktrees: true, sessionDebug: true };
  soul: Record<string, unknown> = {
    present: true,
    content: "Be brief and kind.",
    sizeBytes: "18",
    sha256: "abc123",
    provenance: "project",
    trusted: true,
    drifted: false,
  };
  worktrees: Record<string, unknown>[] = [
    {
      selector: "opaque-a",
      kind: "worktree",
      label: "Feature",
      branch: "feature/a",
      revision: "abcdef1234",
      bare: false,
    },
  ];
  clearStatus = 201;
  manualCompaction = false;
  transcriptMessages: Record<string, unknown>[] | undefined;
  worktreeCalls = 0;
  readonly fetch = async (request: Request) => {
    const path = decodeURIComponent(new URL(request.url).pathname);
    const text = request.method === "GET" ? "" : await request.clone().text();
    const body = text ? JSON.parse(text) : undefined;
    this.requests.push({ method: request.method, path, body });
    const json = (value: unknown, status = 200) =>
      Response.json(value, {
        status,
        headers: {
          "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
        },
      });
    if (path === "/api/v1/runtime")
      return json({ connection: "online", capabilities: this.capabilities });
    if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: false });
    if (path === "/api/v1/sessions" && request.method === "GET")
      return json({ complete: true, items: this.rows });
    if (path === "/api/v1/sessions" && request.method === "POST")
      return json({ id: "successor" }, 201);
    if (path === "/api/v1/soul") return json(this.soul);
    const worktrees = /^\/api\/v1\/sessions\/([^/]+)\/worktrees$/.exec(path);
    if (worktrees) {
      this.worktreeCalls++;
      return json({ items: this.worktrees });
    }
    const transcript = /^\/api\/v1\/sessions\/([^/]+)\/transcript$/.exec(path);
    if (transcript)
      return json({
        complete: transcript[1] !== "chat",
        sessionId: transcript[1],
        messages:
          transcript[1] === "successor"
            ? []
            : (this.transcriptMessages ?? [
                { role: "user", text: "Saved question", images: [], toolCalls: [] },
                { role: "assistant", text: "Saved answer", images: [], toolCalls: [] },
              ]),
      });
    if (/^\/api\/v1\/sessions\/[^/]+\/fork$/.test(path)) return json({ id: "successor" }, 201);
    if (/^\/api\/v1\/sessions\/[^/]+\/compaction$/.test(path)) return json({ compacted: true });
    const clear = /^\/api\/v1\/sessions\/([^/]+)\/clear$/.exec(path);
    if (clear)
      return json(
        this.clearStatus === 201
          ? { id: "successor" }
          : { code: "placement_selector_stale", detail: "Stale", status: this.clearStatus },
        this.clearStatus,
      );
    const detail = /^\/api\/v1\/sessions\/([^/]+)$/.exec(path);
    if (detail)
      return json({
        id: detail[1],
        kind: this.rows.find((item) => item.id === detail[1])?.kind ?? "main",
        state: "idle",
        mode: "default",
        model: {
          id: "model-a",
          providerId: "provider-a",
          reasoningEffort: "medium",
          contextWindow: "200000",
        },
        usage,
        capabilities: {
          image: false,
          manualCompaction: this.manualCompaction,
          modelSelection: false,
        },
        relationship: detail[1] === "debug-chat" ? { debugTargetSessionId: "chat" } : undefined,
        placement: {
          kind: "worktree",
          label: "Main checkout",
          branch: "main",
          revision: "0123456789",
        },
      });
    throw new Error(`Unexpected ${request.method} ${path}`);
  };
  calls(path: string, method = "GET") {
    return this.requests.filter((request) => request.path === path && request.method === method);
  }
}

async function mount(bff: Bff, id = "chat") {
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
  const route = createRoute({
    component: () => <ChatWorkspace sessionId={route.useSearch().sessionId} />,
    getParentRoute: () => root,
    path: "/workspace/chat",
    validateSearch: (search: Record<string, unknown>): { sessionId?: string } => ({
      sessionId: typeof search.sessionId === "string" ? search.sessionId : undefined,
    }),
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [`/workspace/chat?sessionId=${id}`] }),
    routeTree: root.addChildren([route]),
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
  return { queryClient, router };
}

/** The chat's ··· menu, reaching an item whether it sits at the top level or in the Copy submenu. */
async function chooseChatOption(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(await screen.findByRole("button", { name: "Chat options" }));
  let item = screen.queryByRole("menuitem", { name });
  if (!item) {
    await user.click(screen.getByRole("menuitem", { name: "Copy" }));
    item = await screen.findByRole("menuitem", { name });
  }
  // Keyboard selection works for a top-level item and a submenu item alike.
  item.focus();
  await user.keyboard("{Enter}");
}

async function openDetails(user: ReturnType<typeof userEvent.setup>) {
  await chooseChatOption(user, "Inspect session");
  return await screen.findByRole("dialog", { name: "Session details" });
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("chat options entries", () => {
  it("opens the saved transcript, the soul, and the worktree picker directly", async () => {
    const bff = new Bff();
    await mount(bff);
    const user = userEvent.setup();
    await chooseChatOption(user, "View transcript");
    const transcript = await screen.findByRole("dialog", { name: "Saved transcript" });
    expect(await within(transcript).findByText("Saved answer")).toBeTruthy();
    expect(within(transcript).getByText(/Main chat · chat/)).toBeTruthy();
    fireEvent.click(within(transcript).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    await chooseChatOption(user, "Soul");
    const soul = await screen.findByRole("dialog", { name: "Soul inspection" });
    expect(await within(soul).findByText("Be brief and kind.")).toBeTruthy();
    expect(within(soul).getByText("Project soul")).toBeTruthy();
    expect(within(soul).getByText("trusted")).toBeTruthy();
    expect(within(soul).getByText("sha256 abc123")).toBeTruthy();
    fireEvent.click(within(soul).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    await chooseChatOption(user, "Switch worktree…");
    const picker = await screen.findByRole("dialog", { name: "Choose successor worktree" });
    expect(await within(picker).findByRole("radio", { name: /Feature/ })).toBeTruthy();
    expect(within(picker).getByText("abcdef1")).toBeTruthy();
  });

  it("hides the entries the connection or the chat does not offer", async () => {
    const bff = new Bff();
    bff.capabilities = { soul: false, worktrees: false, sessionDebug: false };
    bff.rows = [
      {
        ...row("chat"),
        capabilities: { ...row("chat").capabilities, viewTranscript: false },
      },
    ];
    await mount(bff);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Chat options" }));
    for (const name of ["View transcript", "Soul", "Switch worktree…", "Debug with AI"]) {
      expect(screen.queryByRole("menuitem", { name })).toBeNull();
    }
    expect(screen.getByRole("menuitem", { name: "Inspect session" })).toBeTruthy();
  });

  it("selects only the conversation, and copies it with speaker headings", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    await mount(new Bff());
    await screen.findByText("Saved answer");
    await chooseChatOption(user, "Select conversation");
    const selected = window.getSelection()?.toString() ?? "";
    expect(selected).toContain("Saved question");
    expect(selected).toContain("Saved answer");
    expect(selected).not.toContain("Chat History");
    await chooseChatOption(user, "Copy conversation");
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        "**You**\n\nSaved question\n\n**Mecatl**\n\nSaved answer",
      ),
    );
  });

  it("compacts from the chat options when the daemon allows it", async () => {
    const bff = new Bff();
    bff.manualCompaction = true;
    await mount(bff);
    await screen.findByText("Saved answer");
    await chooseChatOption(userEvent.setup(), "Compact conversation");
    await waitFor(() =>
      expect(bff.calls("/api/v1/sessions/chat/compaction", "POST")).toHaveLength(1),
    );
    expect(await screen.findByText("Conversation context was compacted.")).toBeTruthy();
  });
});

describe("chat list row menu", () => {
  it("opens another chat's saved transcript without leaving the open chat", async () => {
    const bff = new Bff();
    bff.rows = [row("chat"), { ...row("other"), title: "Other chat" }];
    const mounted = await mount(bff);
    const user = userEvent.setup();
    await screen.findByText("Saved answer");
    await user.click(
      screen.getByRole("button", { name: "Options for chat: Other chat", hidden: true }),
    );
    const item = await screen.findByRole("menuitem", { name: "View transcript" });
    item.focus();
    await user.keyboard("{Enter}");
    const transcript = await screen.findByRole("dialog", { name: "Saved transcript" });
    expect(within(transcript).getByText(/Other chat · other/)).toBeTruthy();
    expect(await within(transcript).findByText("Saved answer")).toBeTruthy();
    expect(bff.calls("/api/v1/sessions/other/transcript")).toHaveLength(1);
    expect(mounted.router.state.location.search).toMatchObject({ sessionId: "chat" });
    fireEvent.click(within(transcript).getByRole("button", { name: "Details" }));
    const details = await screen.findByRole("dialog", { name: "Session details" });
    expect(within(details).queryByRole("button", { name: "Debug with AI" })).toBeNull();
    expect(within(details).getByText("Open this chat to debug it with AI.")).toBeTruthy();
  });

  it("forks another chat with its own model and opens the fork", async () => {
    const bff = new Bff();
    bff.rows = [row("chat"), { ...row("other"), title: "Other chat" }, row("successor")];
    const mounted = await mount(bff);
    const user = userEvent.setup();
    await screen.findByText("Saved answer");
    await user.click(
      screen.getByRole("button", { name: "Options for chat: Other chat", hidden: true }),
    );
    const item = await screen.findByRole("menuitem", { name: "Fork chat" });
    item.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(bff.calls("/api/v1/sessions/other/fork", "POST")).toHaveLength(1));
    expect(bff.calls("/api/v1/sessions/other/fork", "POST")[0]?.body).toEqual({
      model: { id: "model-a", providerId: "provider-a" },
      reasoningEffort: "medium",
    });
    await waitFor(() =>
      expect(mounted.router.state.location.search).toMatchObject({ sessionId: "successor" }),
    );
  });

  it("copies a row's ID, and a debug chat's target ID", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const bff = new Bff();
    bff.rows = [
      row("chat"),
      { ...row("debug-chat"), debugTargetSessionId: "chat", kind: "debug", title: "Diagnose" },
    ];
    await mount(bff);
    await screen.findByText("Saved answer");
    const pick = async (name: string) => {
      await user.click(
        screen.getByRole("button", { name: "Options for chat: Diagnose", hidden: true }),
      );
      const item = await screen.findByRole("menuitem", { name });
      item.focus();
      await user.keyboard("{Enter}");
    };
    await pick("Copy session ID");
    await waitFor(() => expect(writeText).toHaveBeenLastCalledWith("debug-chat"));
    await pick("Copy debug target ID");
    await waitFor(() => expect(writeText).toHaveBeenLastCalledWith("chat"));
    await user.click(
      screen.getByRole("button", { name: "Options for chat: Diagnose", hidden: true }),
    );
    expect(screen.queryByRole("menuitem", { name: "Fork chat" })).toBeNull();
  });
});

describe("dialog facts", () => {
  it("lists a turn's tool calls in the saved transcript, and says when nothing replays", async () => {
    const bff = new Bff();
    bff.transcriptMessages = [
      { role: "user", text: "Look it up", images: [], toolCalls: [] },
      {
        role: "assistant",
        text: "Found it",
        images: [],
        toolCalls: [
          { args: "{}", id: "c1", name: "Read" },
          { args: "{}", id: "c2", name: "Grep" },
        ],
      },
    ];
    await mount(bff);
    const user = userEvent.setup();
    await chooseChatOption(user, "View transcript");
    const transcript = await screen.findByRole("dialog", { name: "Saved transcript" });
    expect(await within(transcript).findByText("2 tool calls")).toBeTruthy();
    expect(within(transcript).getByText("activity")).toBeTruthy();

    cleanup();
    const empty = new Bff();
    empty.transcriptMessages = [];
    await mount(empty);
    await chooseChatOption(user, "View transcript");
    const none = await screen.findByRole("dialog", { name: "Saved transcript" });
    expect(await within(none).findByText(/holds no replayable conversation/)).toBeTruthy();
  });

  it("names a debug chat's target with its own copy", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const bff = new Bff();
    bff.rows = [
      row("chat"),
      {
        ...row("debug-chat"),
        capabilities: { ...row("chat").capabilities, publicChat: false, publicChatReason: "" },
        debugTargetSessionId: "chat",
        kind: "debug",
        title: "Diagnose",
      },
    ];
    await mount(bff, "debug-chat");
    const details = await openDetails(user);
    expect(await within(details).findByText("Debug target")).toBeTruthy();
    expect(within(details).getByText("Debug session")).toBeTruthy();
    fireEvent.click(within(details).getByRole("button", { name: "Copy Debug target ID" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("chat"));
  });

  it("marks an untrusted, drifted soul", async () => {
    const bff = new Bff();
    bff.soul = { ...bff.soul, drifted: true, provenance: "user", trusted: false };
    await mount(bff);
    await chooseChatOption(userEvent.setup(), "Soul");
    const soul = await screen.findByRole("dialog", { name: "Soul inspection" });
    expect(await within(soul).findByText("User soul")).toBeTruthy();
    expect(within(soul).getByText("untrusted")).toBeTruthy();
    expect(within(soul).getByText("drifted")).toBeTruthy();
    expect(within(soul).getByText("18 bytes")).toBeTruthy();
  });
});
