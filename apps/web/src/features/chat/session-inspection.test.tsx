// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { client } from "@mecatl-studio/contracts/client";
import { getSessionTranscriptOptions } from "@mecatl-studio/contracts/query";
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
  cacheReadTokens: "1",
  cacheWriteTokens: "2",
  inputTokens: "3",
  outputTokens: "4",
  reasoningTokens: "5",
};

function row(id: string, inspectOnly = true): SessionSummaryResponse {
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
    kind: inspectOnly ? "child" : "main",
    modelId: "model-a",
    state: "completed",
    title: inspectOnly ? "Child inspection" : "Main chat",
    titleProvenance: "",
    titleRevision: "0",
    turns: 2,
    updatedAt: "2026-09-24T12:00:00.000Z",
  };
}

type RecordedRequest = { method: string; path: string; body?: unknown };

class Bff {
  rows: SessionSummaryResponse[] = [row("child")];
  requests: RecordedRequest[] = [];
  capabilities = { soul: true, worktrees: true, sessionDebug: true };
  connection: "online" | "offline" = "online";
  transcriptStatus = 200;
  forkStatus = 201;
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
      return json({ connection: this.connection, capabilities: this.capabilities });
    if (path === "/api/v1/settings/runtime") return json({ models: [], modelsSupported: false });
    if (path === "/api/v1/sessions" && request.method === "GET")
      return json({ complete: true, items: this.rows });
    if (path === "/api/v1/sessions" && request.method === "POST")
      return json({ id: "successor" }, 201);
    if (path === "/api/v1/soul")
      return json({
        present: true,
        content: "Soul text",
        sizeBytes: "9",
        sha256: "hash",
        provenance: "user",
        trusted: true,
        drifted: false,
      });
    if (path === "/api/v1/sessions/child/worktrees") {
      this.worktreeCalls++;
      return json({
        items: [
          {
            selector: "opaque-selector",
            kind: "worktree",
            label: "Feature",
            branch: "feature",
            revision: "abc",
            bare: false,
          },
        ],
      });
    }
    if (path === "/api/v1/sessions/child/transcript")
      return json(
        {
          complete: false,
          sessionId: "child",
          messages: [
            { role: "user", text: "First saved row", images: [], toolCalls: [] },
            { role: "assistant", text: "Second saved row", images: [], toolCalls: [] },
          ],
        },
        this.transcriptStatus,
      );
    if (path === "/api/v1/sessions/child")
      return json({
        id: "child",
        kind: this.rows.find((item) => item.id === "child")?.kind ?? "child",
        state: this.rows.find((item) => item.id === "child")?.state ?? "completed",
        mode: "default",
        model: {
          id: "model-a",
          providerId: "provider",
          reasoningEffort: "medium",
          contextWindow: "1000",
        },
        usage,
        capabilities: { image: false, manualCompaction: false, modelSelection: false },
        placement: { kind: "worktree", label: "Feature", branch: "feature", revision: "abc" },
      });
    if (path === "/api/v1/sessions/child/fork")
      return json(
        this.forkStatus === 409
          ? { code: "stale_worktree_selector", detail: "Selection is stale", status: 409 }
          : { id: "successor" },
        this.forkStatus,
      );
    if (path === "/api/v1/sessions/child/clear") return json({ id: "successor" }, 201);
    if (path === "/api/v1/sessions/successor")
      return json({
        id: "successor",
        kind: "main",
        state: "idle",
        mode: "default",
        usage,
        capabilities: { image: false, manualCompaction: false, modelSelection: false },
      });
    if (path === "/api/v1/sessions/successor/transcript")
      return json({ complete: true, sessionId: "successor", messages: [] });
    throw new Error(`Unexpected ${request.method} ${path}`);
  };
  calls(path: string) {
    return this.requests.filter((request) => request.path === path);
  }
}

async function mount(bff: Bff, id = "child") {
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

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("session inspection", () => {
  it.each(["awaiting_approval", "active_elsewhere"])(
    "keeps a temporarily blocked main chat visible for %s",
    async (reason) => {
      const bff = new Bff();
      bff.rows = [
        {
          ...row("child", false),
          capabilities: {
            ...row("child", false).capabilities,
            fork: false,
            forkReason: reason,
            publicChat: false,
            publicChatReason: reason,
          },
        },
      ];
      await mount(bff);
      expect(await screen.findByText("First saved row")).toBeTruthy();
      const composer = screen.getByRole("textbox", { name: "Message Mecatl" });
      expect(composer).toHaveProperty("disabled", true);
      expect(screen.queryByText("This session is unavailable as a chat.")).toBeNull();
      expect(bff.calls("/api/v1/sessions/child/transcript")).toHaveLength(1);
    },
  );

  it("opens inspect only session without a composer", async () => {
    const bff = new Bff();
    await mount(bff);
    const details = await screen.findByRole("dialog", { name: "Session details" });
    expect(
      screen.getByRole("heading", { name: "Inspect-only sessions", hidden: true }),
    ).toBeTruthy();
    expect(within(details).getAllByText("child").length).toBeGreaterThan(0);
    expect(screen.queryByRole("textbox", { name: "Message Mecatl", hidden: true })).toBeNull();
    for (const name of [
      "Send",
      "Retry",
      "Stop",
      "Fork chat",
      "Clear conversation",
      "Rename",
      "Delete",
    ]) {
      expect(screen.queryByRole("button", { name, hidden: true })).toBeNull();
    }
    expect(bff.calls("/api/v1/sessions/child/transcript")).toHaveLength(0);
    fireEvent.click(within(details).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("textbox", { name: "Message Mecatl", hidden: true })).toBeNull();

    cleanup();
    await mount(new Bff(), "unclassified");
    expect(screen.queryByRole("textbox", { name: "Message Mecatl", hidden: true })).toBeNull();
  });

  it("gates read only transcript and details by capability", async () => {
    const bff = new Bff();
    const mounted = await mount(bff);
    const details = await screen.findByRole("dialog", { name: "Session details" });
    for (const value of ["child", "completed", "model-a", "Feature", "1", "2", "3", "4", "5"]) {
      expect(within(details).getAllByText(value).length).toBeGreaterThan(0);
    }
    fireEvent.click(within(details).getByRole("button", { name: "View transcript" }));
    const transcript = await screen.findByRole("dialog", { name: "Saved transcript" });
    const first = await within(transcript).findByText("First saved row");
    const second = within(transcript).getByText("Second saved row");
    expect(first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(transcript).getByText(/incomplete/i)).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "Message Mecatl" })).toBeNull();
    bff.transcriptStatus = 503;
    await act(async () => {
      await mounted.queryClient.invalidateQueries({
        queryKey: getSessionTranscriptOptions({ path: { sessionId: "child" } }).queryKey,
      });
    });
    expect(bff.calls("/api/v1/sessions/child/transcript")).toHaveLength(2);
    expect(await within(transcript).findByText("Saved transcript could not be read.")).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "Message Mecatl", hidden: true })).toBeNull();
  });

  it("explains unavailable inspection and debug capabilities", async () => {
    const bff = new Bff();
    bff.capabilities = { soul: false, worktrees: false, sessionDebug: false };
    bff.rows[0] = {
      ...row("child"),
      capabilities: {
        ...row("child").capabilities,
        viewTranscript: false,
        viewTranscriptReason: "transcript_unsupported",
      },
    };
    await mount(bff);
    const details = await screen.findByRole("dialog", { name: "Session details" });
    expect(within(details).getByText("transcript_unsupported")).toBeTruthy();
    expect(within(details).getByText(/soul.*unavailable/i)).toBeTruthy();
    expect(within(details).getByText(/worktrees.*unavailable/i)).toBeTruthy();
    expect(within(details).getByText(/debug.*unavailable/i)).toBeTruthy();
    expect(bff.calls("/api/v1/soul")).toHaveLength(0);
    expect(bff.calls("/api/v1/sessions/child/worktrees")).toHaveLength(0);
    expect(bff.calls("/api/v1/sessions/child/transcript")).toHaveLength(0);
  });

  it("confirms a selected worktree and relists after a stale selector", async () => {
    const bff = new Bff();
    bff.rows = [row("child", false)];
    bff.forkStatus = 409;
    const mounted = await mount(bff);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Chat options" }));
    await user.click(screen.getByRole("menuitem", { name: "Inspect session" }));
    const details = await screen.findByRole("dialog", { name: "Session details" });
    fireEvent.click(within(details).getByRole("button", { name: "Choose worktree" }));
    const picker = await screen.findByRole("dialog", { name: "Choose successor worktree" });
    fireEvent.click(await within(picker).findByRole("radio", { name: /Feature/ }));
    expect(bff.calls("/api/v1/sessions/child/fork")).toHaveLength(0);
    fireEvent.click(within(picker).getByRole("button", { name: "Fork in selected worktree" }));
    await waitFor(() => expect(bff.calls("/api/v1/sessions/child/fork")).toHaveLength(1));
    expect(bff.calls("/api/v1/sessions/child/fork")[0]?.body).toMatchObject({
      worktreeSelector: "opaque-selector",
    });
    await waitFor(() => expect(bff.worktreeCalls).toBe(2));
    expect(screen.getByText(/selection is stale/i)).toBeTruthy();
    expect(within(picker).getByRole("radio", { name: /Feature/ })).toHaveProperty("checked", false);
    expect(
      within(picker).getByRole("button", { name: "Fork in selected worktree" }),
    ).toHaveProperty("disabled", true);
    expect(
      within(picker).getByRole("button", { name: "Clear in selected worktree" }),
    ).toHaveProperty("disabled", true);
    expect(bff.calls("/api/v1/sessions/child/fork")).toHaveLength(1);
    expect(mounted.router.state.location.search).toMatchObject({ sessionId: "child" });
  });

  it("opens a confirmed successor without changing its source", async () => {
    const bff = new Bff();
    bff.rows = [row("child", false), { ...row("successor", false), title: "Successor" }];
    await mount(bff);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Chat options" }));
    await user.click(screen.getByRole("menuitem", { name: "Inspect session" }));
    fireEvent.click(
      within(await screen.findByRole("dialog", { name: "Session details" })).getByRole("button", {
        name: "Choose worktree",
      }),
    );
    const picker = await screen.findByRole("dialog", { name: "Choose successor worktree" });
    fireEvent.click(await within(picker).findByRole("radio", { name: /Feature/ }));
    fireEvent.click(within(picker).getByRole("button", { name: "Clear in selected worktree" }));
    await waitFor(() => expect(bff.calls("/api/v1/sessions/child/clear")).toHaveLength(1));
    expect(bff.calls("/api/v1/sessions/child/clear")[0]?.body).toEqual({
      worktreeSelector: "opaque-selector",
    });
    await screen.findByRole("heading", { name: "Successor" });
    expect(bff.calls("/api/v1/sessions/child/clear")).toHaveLength(1);
    expect(bff.calls("/api/v1/sessions/child/fork")).toHaveLength(0);
  });

  it("keeps soul connection scoped and the active transcript view stable", async () => {
    const bff = new Bff();
    const mounted = await mount(bff);
    const details = await screen.findByRole("dialog", { name: "Session details" });
    fireEvent.click(within(details).getByRole("button", { name: "Inspect soul" }));
    expect(await screen.findByText("Soul text")).toBeTruthy();
    expect(bff.calls("/api/v1/soul")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "View transcript" }));
    const transcript = await screen.findByRole("dialog", { name: "Saved transcript" });
    await within(transcript).findByText("First saved row");
    const scrollport = screen.getByTestId("inspection-scrollport");
    scrollport.scrollTop = 75;
    screen.getByRole("button", { name: "Details" }).focus();
    await act(async () => {
      mounted.queryClient.setQueryData(
        // Same selected owned session; an ordinary transcript update does not reset the dialog.
        getSessionTranscriptOptions({ path: { sessionId: "child" } }).queryKey,
        {
          complete: false,
          sessionId: "child",
          messages: [{ role: "user", text: "Updated saved row", images: [], toolCalls: [] }],
        },
      );
    });
    expect(screen.getByRole("dialog", { name: "Saved transcript" })).toBe(transcript);
    expect(screen.getByTestId("inspection-scrollport")).toBe(scrollport);
    expect(scrollport.scrollTop).toBe(75);
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Details" }));
    expect(await screen.findByText("Updated saved row")).toBeTruthy();
  });

  it("withholds inspection reads while offline and clears the selected view on a session switch", async () => {
    const bff = new Bff();
    bff.connection = "offline";
    const mounted = await mount(bff);
    const details = await screen.findByRole("dialog", { name: "Session details" });
    expect(within(details).getByText("Transcript unavailable while offline.")).toBeTruthy();
    expect(within(details).getByText("Soul inspection unavailable while offline.")).toBeTruthy();
    expect(within(details).getByText("Worktrees unavailable while offline.")).toBeTruthy();
    expect(bff.calls("/api/v1/soul")).toHaveLength(0);
    expect(bff.calls("/api/v1/sessions/child/worktrees")).toHaveLength(0);
    expect(bff.calls("/api/v1/sessions/child/transcript")).toHaveLength(0);
    await act(async () =>
      mounted.router.navigate({ search: { sessionId: "unclassified" }, to: "/workspace/chat" }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Session details" })).toBeNull(),
    );
    expect(screen.queryByText("Feature")).toBeNull();
    expect(screen.queryByRole("textbox", { name: "Message Mecatl", hidden: true })).toBeNull();
  });

  it("requires evidence disclosure before creating debug for the selected owned ID", async () => {
    const bff = new Bff();
    bff.rows.push({ ...row("successor", false), title: "Successor" });
    await mount(bff);
    const details = await screen.findByRole("dialog", { name: "Session details" });
    fireEvent.click(within(details).getByRole("button", { name: "Debug with AI" }));
    const consent = await screen.findByRole("alertdialog", { name: /Debug with AI/ });
    expect(within(consent).getAllByText(/evidence/i).length).toBeGreaterThan(0);
    expect(
      bff.calls("/api/v1/sessions").filter((request) => request.method === "POST"),
    ).toHaveLength(0);
    fireEvent.click(within(consent).getByRole("button", { name: "Send evidence & debug" }));
    await waitFor(() =>
      expect(
        bff.calls("/api/v1/sessions").filter((request) => request.method === "POST"),
      ).toHaveLength(1),
    );
    expect(
      bff.calls("/api/v1/sessions").find((request) => request.method === "POST")?.body,
    ).toMatchObject({ debugTargetSessionId: "child" });
  });
});
