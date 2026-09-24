// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { GetAuthSessionResponse } from "@mecatl-studio/contracts/generated";
import { getAuthSessionOptions, listSessionsQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { useDisabledModels } from "../../lib/model-preferences";
import { usePanelWidth } from "../../lib/panel-width";
import {
  initializeProfilePreferences,
  useAgentAvatar,
  useAgentDisplayName,
  useUiScale,
  useUserAvatar,
  useUserDisplayName,
} from "../../lib/profile-preferences";
import { useChatFolders } from "../chat/chat-folders";
import { useLocalCanvas } from "../chat/local-canvas";
import { registerThreadSession, useThreadMap, useThreadSessionIds } from "../chat/thread-map";
import { AuthGate } from "./auth-gate";

afterEach(() => {
  vi.restoreAllMocks();
  cleanup();
  clearUserScopedStorage();
  document.documentElement.style.removeProperty("--ui-scale");
});

function AccountData() {
  return (
    <span data-testid="account-data">
      {window.localStorage.getItem("studio.chat.folders") ?? "cleared"}
    </span>
  );
}

function ScopedAccountData() {
  const userName = useUserDisplayName();
  const userAvatar = useUserAvatar();
  const agentName = useAgentDisplayName();
  const agentAvatar = useAgentAvatar();
  const draftCanvas = useLocalCanvas("draft");
  const sessionCanvas = useLocalCanvas("same");
  const threads = useThreadMap("same");
  const threadIds = useThreadSessionIds();
  const disabledModels = useDisabledModels();
  const chatListWidth = usePanelWidth("chatList");
  const contentWidth = usePanelWidth("contentPreview");
  const folders = useChatFolders();
  const uiScale = useUiScale();

  return (
    <>
      <pre data-testid="scoped-data">
        {JSON.stringify({
          agentAvatar: agentAvatar.value,
          agentName: agentName.value,
          chatListWidth: chatListWidth.value,
          contentWidth: contentWidth.value,
          disabledModels: [...disabledModels.disabled].sort(),
          draftCanvas: draftCanvas.value,
          folderNames: folders.state.folders.map((folder) => folder.name),
          sessionCanvas: sessionCanvas.value,
          threadIds: [...threadIds].sort(),
          threads,
          uiScale: uiScale.value,
          appliedScale: document.documentElement.style.getPropertyValue("--ui-scale"),
          userAvatar: userAvatar.value,
          userName: userName.value,
        })}
      </pre>
      <button
        onClick={() => {
          agentAvatar.setValue("bob-agent-avatar");
          agentName.setValue("Bob's agent");
          userAvatar.setValue("bob-avatar");
          userName.setValue("Bob");
          draftCanvas.setValue("Bob draft");
          sessionCanvas.setValue("Bob session canvas");
          registerThreadSession("same", "root", "bob-thread");
          disabledModels.setModelEnabled("bob-model", false);
          chatListWidth.setValue(355);
          contentWidth.setValue(344);
          folders.create("Bob folder");
        }}
        type="button"
      >
        Write Bob data
      </button>
    </>
  );
}

function scopedData() {
  return JSON.parse(screen.getByTestId("scoped-data").textContent ?? "null") as Record<
    string,
    unknown
  >;
}

function authenticated(account: string): GetAuthSessionResponse {
  return { account, mode: "oidc", status: "authenticated" };
}

it("drops account data and prior BFF snapshots before the next account renders", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const authKey = getAuthSessionOptions().queryKey;
  client.setQueryData(authKey, authenticated("alice"));
  client.setQueryData(listSessionsQueryKey(), {
    complete: true,
    items: [{ id: "same", title: "Alice" }],
  });
  window.localStorage.setItem("studio.account", "alice");
  window.sessionStorage.setItem("studio.account", "alice");
  window.localStorage.setItem("studio.chat.folders", "Alice's folder");
  window.sessionStorage.setItem("studio.chat.failed.same", "Alice's failed prompt");
  window.localStorage.setItem("theme", "dark");

  render(
    <QueryClientProvider client={client}>
      <AuthGate>
        <AccountData />
      </AuthGate>
    </QueryClientProvider>,
  );
  expect(screen.getByTestId("account-data").textContent).toBe("Alice's folder");

  client.setQueryData(authKey, authenticated("bob"));
  await waitFor(() => expect(screen.getByTestId("account-data").textContent).toBe("cleared"));
  expect(window.sessionStorage.getItem("studio.chat.failed.same")).toBeNull();
  expect(client.getQueryData(listSessionsQueryKey())).toBeUndefined();
  expect(window.localStorage.getItem("theme")).toBe("dark");
  client.clear();
});

it("quarantines every account-scoped caller after partial removal, including storage events", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const authKey = getAuthSessionOptions().queryKey;
  const oldFolders = JSON.stringify({
    assignments: { same: "alice-folder" },
    folders: [{ id: "alice-folder", name: "Alice folder" }],
  });
  const oldThreads = JSON.stringify({ root: { sessionId: "alice-thread" } });
  client.setQueryData(authKey, authenticated("alice"));
  for (const [key, value] of Object.entries({
    "studio.account": "alice",
    "studio.profile.user-name": "Alice",
    "studio.profile.user-avatar": "alice-avatar",
    "studio.profile.agent-name": "Alice's agent",
    "studio.profile.agent-avatar": "alice-agent-avatar",
    "studio.chat.canvas.draft": "Alice draft",
    "studio.chat.canvas.same": "Alice session canvas",
    "studio.chat.threads.same": oldThreads,
    "studio.chat.threadSessions": "[]",
    "studio.chat.models.disabled": '["alice-model"]',
    "studio.chat.panelWidth": "311",
    "studio.chat.contentPanelWidth": "333",
    "studio.chat.folders": oldFolders,
    "studio.profile.ui-scale": "1.3",
  })) {
    window.localStorage.setItem(key, value);
  }
  window.sessionStorage.setItem("studio.account", "alice");
  window.localStorage.setItem("theme", "dark");
  initializeProfilePreferences();

  render(
    <QueryClientProvider client={client}>
      <AuthGate>
        <ScopedAccountData />
      </AuthGate>
    </QueryClientProvider>,
  );
  expect(scopedData()).toMatchObject({
    agentAvatar: "alice-agent-avatar",
    agentName: "Alice's agent",
    userName: "Alice",
    userAvatar: "alice-avatar",
    draftCanvas: "Alice draft",
    sessionCanvas: "Alice session canvas",
    folderNames: ["Alice folder"],
    threadIds: ["alice-thread"],
    threads: { root: { sessionId: "alice-thread" } },
    chatListWidth: 311,
    contentWidth: 333,
    disabledModels: ["alice-model"],
    uiScale: 1.3,
    appliedScale: "1.3",
  });

  const remove = window.localStorage.removeItem.bind(window.localStorage);
  vi.spyOn(window.localStorage, "removeItem").mockImplementation((key) => {
    if (key.startsWith("studio.") && key !== "studio.account") {
      throw new Error("partial removal blocked");
    }
    remove(key);
  });
  client.setQueryData(authKey, authenticated("bob"));
  await waitFor(() => expect(scopedData().userName).toBe(""));
  expect(scopedData()).toMatchObject({
    agentAvatar: "",
    agentName: "Mecatl",
    chatListWidth: 256,
    contentWidth: 256,
    disabledModels: [],
    uiScale: 1,
    appliedScale: "",
    draftCanvas: "",
    folderNames: [],
    sessionCanvas: "",
    threadIds: [],
    threads: {},
    userAvatar: "",
    userName: "",
  });
  expect(window.localStorage.getItem("studio.profile.user-name")).toBe("Alice");
  expect(window.localStorage.getItem("studio.account")).not.toBe("bob");
  expect(window.localStorage.getItem("theme")).toBe("dark");

  act(() => {
    window.dispatchEvent(
      new StorageEvent("storage", { key: "studio.chat.folders", newValue: oldFolders }),
    );
  });
  expect(scopedData().folderNames).toEqual([]);

  fireEvent.click(screen.getByRole("button", { name: "Write Bob data" }));
  expect(scopedData()).toMatchObject({
    agentAvatar: "bob-agent-avatar",
    agentName: "Bob's agent",
    chatListWidth: 355,
    contentWidth: 344,
    disabledModels: ["bob-model"],
    draftCanvas: "Bob draft",
    folderNames: ["Bob folder"],
    sessionCanvas: "Bob session canvas",
    threadIds: ["bob-thread"],
    threads: { root: { sessionId: "bob-thread" } },
    userAvatar: "bob-avatar",
    userName: "Bob",
  });
  expect(window.localStorage.getItem("studio.profile.agent-name")).toBe("Alice's agent");
  expect(window.localStorage.getItem("studio.profile.agent-avatar")).toBe("alice-agent-avatar");
  expect(window.localStorage.getItem("studio.profile.user-name")).toBe("Alice");
  expect(window.localStorage.getItem("studio.profile.user-avatar")).toBe("alice-avatar");
  expect(window.localStorage.getItem("studio.chat.canvas.draft")).toBe("Alice draft");
  expect(window.localStorage.getItem("studio.chat.canvas.same")).toBe("Alice session canvas");
  expect(window.localStorage.getItem("studio.chat.threads.same")).toBe(oldThreads);
  expect(window.localStorage.getItem("studio.chat.threadSessions")).toBe("[]");
  expect(window.localStorage.getItem("studio.chat.models.disabled")).toBe('["alice-model"]');
  expect(window.localStorage.getItem("studio.chat.panelWidth")).toBe("311");
  expect(window.localStorage.getItem("studio.chat.contentPanelWidth")).toBe("333");
  expect(window.localStorage.getItem("studio.chat.folders")).toBe(oldFolders);
  expect(window.localStorage.getItem("studio.profile.ui-scale")).toBe("1.3");
  client.clear();
});
