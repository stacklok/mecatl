// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the session surfaces' data, actions, and rules before their UI moves
 * to the prototype's dialogs and menus: each inspection view's data, the
 * worktree rejection and relist rule, the inspect-only classification, the
 * debug consent, and the copy actions. These assert outcomes (requests,
 * clipboard writes, visible facts), not layout, so they hold across the
 * restyle unchanged.
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
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { setRequestRecoveryState } from "../../lib/api-client";
import { AuthRecoveryContext, type AuthRecoveryContextValue } from "../auth/auth-recovery-context";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { ChatWorkspace } from "./chat-workspace";
import { isInspectOnlySession, isProvenChatSession } from "./session-inspection";

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
  worktreeCalls = 0;
  readonly fetch = async (request: Request) => {
    const path = decodeURIComponent(new URL(request.url).pathname);
    const body = request.method === "GET" ? undefined : await request.clone().json();
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
            : [
                { role: "user", text: "Saved question", images: [], toolCalls: [] },
                { role: "assistant", text: "Saved answer", images: [], toolCalls: [] },
              ],
      });
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
        capabilities: { image: false, manualCompaction: false, modelSelection: false },
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

function viewButton(dialog: HTMLElement, name: string) {
  return within(dialog).getByRole("button", { name });
}

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("inspect-only classification", () => {
  const base = row("x");
  const withCaps = (caps: Partial<SessionSummaryResponse["capabilities"]>, extra = {}) => ({
    ...base,
    ...extra,
    capabilities: { ...base.capabilities, ...caps },
  });

  it("is inspect-only only for a refused inspect_only_kind row with no debug binding", () => {
    expect(
      isInspectOnlySession(withCaps({ publicChat: false, publicChatReason: "inspect_only_kind" })),
    ).toBe(true);
    expect(
      isInspectOnlySession(withCaps({ publicChat: true, publicChatReason: "inspect_only_kind" })),
    ).toBe(false);
    expect(
      isInspectOnlySession(withCaps({ publicChat: false, publicChatReason: "awaiting_approval" })),
    ).toBe(false);
    expect(isInspectOnlySession(withCaps({ publicChat: false, publicChatReason: "" }))).toBe(false);
    expect(
      isInspectOnlySession(
        withCaps(
          { publicChat: false, publicChatReason: "inspect_only_kind" },
          { debugTargetSessionId: "t" },
        ),
      ),
    ).toBe(false);
  });

  it("proves a chat by public chat, a debug binding, or a temporarily blocked main row", () => {
    expect(isProvenChatSession(withCaps({ publicChat: true }))).toBe(true);
    expect(
      isProvenChatSession(
        withCaps(
          { publicChat: false, publicChatReason: "inspect_only_kind" },
          { debugTargetSessionId: "t" },
        ),
      ),
    ).toBe(true);
    for (const reason of ["awaiting_approval", "active_elsewhere"]) {
      expect(isProvenChatSession(withCaps({ publicChat: false, publicChatReason: reason }))).toBe(
        true,
      );
      expect(
        isProvenChatSession(
          withCaps({ publicChat: false, publicChatReason: reason }, { kind: "subagent" }),
        ),
      ).toBe(false);
    }
    expect(
      isProvenChatSession(withCaps({ publicChat: false, publicChatReason: "inspect_only_kind" })),
    ).toBe(false);
    expect(isProvenChatSession(withCaps({ publicChat: false, publicChatReason: "" }))).toBe(false);
  });
});

describe("session details pins", () => {
  it("shows the session's recorded facts and copies its ID", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const success = vi.spyOn(toast, "success").mockReturnValue(0);
    await mount(new Bff());
    const details = await openDetails(user);
    for (const value of [
      "chat",
      "main",
      "idle",
      "model-a",
      "Main checkout",
      "main",
      "0123456789",
      "11",
      "12",
      "13",
      "14",
      "15",
    ]) {
      expect(within(details).getAllByText(value).length).toBeGreaterThan(0);
    }
    fireEvent.click(within(details).getByRole("button", { name: "Copy session ID" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("chat"));
    expect(success).toHaveBeenCalledWith("Session ID copied");
  });

  it("withholds the ID and its copy when the daemon denies copying", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const bff = new Bff();
    bff.rows = [
      {
        ...row("chat"),
        capabilities: { ...row("chat").capabilities, copyId: false, copyIdReason: "id_hidden" },
      },
    ];
    await mount(bff);
    const details = await openDetails(user);
    await within(details).findAllByText("model-a");
    expect(within(details).queryByText("chat")).toBeNull();
    expect(within(details).getByText(/id_hidden/)).toBeTruthy();
    const copy = within(details).queryByRole("button", { name: "Copy session ID" });
    if (copy) {
      expect(copy).toHaveProperty("disabled", true);
      fireEvent.click(copy);
    }
    expect(writeText).not.toHaveBeenCalled();
  });
});

describe("transcript, soul, and worktree view pins", () => {
  it("replays the saved transcript in order and marks an incomplete one", async () => {
    await mount(new Bff());
    const user = userEvent.setup();
    const details = await openDetails(user);
    fireEvent.click(viewButton(details, "View transcript"));
    const transcript = await screen.findByRole("dialog", { name: "Saved transcript" });
    const question = await within(transcript).findByText("Saved question");
    const answer = within(transcript).getByText("Saved answer");
    expect(
      question.compareDocumentPosition(answer) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(within(transcript).getAllByText(/user/i).length).toBeGreaterThan(0);
    expect(within(transcript).getAllByText(/assistant/i).length).toBeGreaterThan(0);
    expect(within(transcript).getByText(/incomplete/i)).toBeTruthy();
  });

  it("shows the connection's soul with its provenance, and an absent soul plainly", async () => {
    const bff = new Bff();
    await mount(bff);
    const user = userEvent.setup();
    const details = await openDetails(user);
    fireEvent.click(viewButton(details, "Inspect soul"));
    const soul = await screen.findByRole("dialog", { name: "Soul inspection" });
    expect(await within(soul).findByText("Be brief and kind.")).toBeTruthy();
    expect(within(soul).getAllByText(/project/i).length).toBeGreaterThan(0);
    expect(bff.calls("/api/v1/soul")).toHaveLength(1);

    cleanup();
    const absent = new Bff();
    absent.soul = { ...absent.soul, present: false, content: "" };
    await mount(absent);
    const again = await openDetails(user);
    fireEvent.click(viewButton(again, "Inspect soul"));
    const empty = await screen.findByRole("dialog", { name: "Soul inspection" });
    expect(await within(empty).findByText(/no soul/i)).toBeTruthy();
  });

  it("lists worktrees by label, branch, and revision, and says when none are eligible", async () => {
    const bff = new Bff();
    await mount(bff);
    const user = userEvent.setup();
    const details = await openDetails(user);
    fireEvent.click(viewButton(details, "Choose worktree"));
    const picker = await screen.findByRole("dialog", { name: "Choose successor worktree" });
    const radio = await within(picker).findByRole("radio", { name: /Feature/ });
    expect(radio.closest("label")?.textContent).toContain("feature/a");
    expect(radio.closest("label")?.textContent).toContain("abcdef1");
    expect(
      within(picker).getByRole("button", { name: "Fork in selected worktree" }),
    ).toHaveProperty("disabled", true);
    expect(
      within(picker).getByRole("button", { name: "Clear in selected worktree" }),
    ).toHaveProperty("disabled", true);

    cleanup();
    const none = new Bff();
    none.worktrees = [];
    await mount(none);
    const again = await openDetails(user);
    fireEvent.click(viewButton(again, "Choose worktree"));
    const empty = await screen.findByRole("dialog", { name: "Choose successor worktree" });
    expect(await within(empty).findByText(/no .*eligible/i)).toBeTruthy();
  });

  it("relists after a selector rejection of a clear, and clears the choice", async () => {
    const bff = new Bff();
    bff.clearStatus = 409;
    await mount(bff);
    const user = userEvent.setup();
    const details = await openDetails(user);
    fireEvent.click(viewButton(details, "Choose worktree"));
    const picker = await screen.findByRole("dialog", { name: "Choose successor worktree" });
    fireEvent.click(await within(picker).findByRole("radio", { name: /Feature/ }));
    fireEvent.click(within(picker).getByRole("button", { name: "Clear in selected worktree" }));
    await waitFor(() => expect(bff.calls("/api/v1/sessions/chat/clear", "POST")).toHaveLength(1));
    expect(bff.calls("/api/v1/sessions/chat/clear", "POST")[0]?.body).toEqual({
      worktreeSelector: "opaque-a",
    });
    await waitFor(() => expect(bff.worktreeCalls).toBe(2));
    expect(within(picker).getByRole("alert").textContent).toMatch(/selection is stale/i);
    expect(
      within(picker)
        .getByRole("radio", { name: /Feature/ })
        .getAttribute("aria-checked"),
    ).toBe("false");
  });
});

describe("debug consent pins", () => {
  it("creates nothing when the consent is cancelled", async () => {
    const bff = new Bff();
    await mount(bff);
    const user = userEvent.setup();
    await chooseChatOption(user, "Debug with AI");
    const consent = await screen.findByRole("alertdialog", { name: /Debug with AI/ });
    expect(within(consent).getByText(/stored transcript and event evidence/)).toBeTruthy();
    fireEvent.click(within(consent).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(bff.calls("/api/v1/sessions", "POST")).toHaveLength(0);
  });

  it("binds the debug chat to the open chat once consent is given", async () => {
    const bff = new Bff();
    await mount(bff);
    const user = userEvent.setup();
    await chooseChatOption(user, "Debug with AI");
    const consent = await screen.findByRole("alertdialog", { name: /Debug with AI/ });
    fireEvent.click(within(consent).getByRole("button", { name: "Send evidence & debug" }));
    await waitFor(() => expect(bff.calls("/api/v1/sessions", "POST")).toHaveLength(1));
    expect(bff.calls("/api/v1/sessions", "POST")[0]?.body).toMatchObject({
      debugTargetSessionId: "chat",
    });
  });
});

describe("copy action pins", () => {
  it("copies the open chat's ID from the chat options", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    const success = vi.spyOn(toast, "success").mockReturnValue(0);
    await mount(new Bff());
    await screen.findByText("Saved answer");
    await chooseChatOption(user, "Copy session ID");
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("chat"));
    expect(success).toHaveBeenCalledWith("Session ID copied");
  });
});
