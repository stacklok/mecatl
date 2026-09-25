// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import {
  getRuntimeSettingsOptions,
  getSessionDetailOptions,
  getSessionTranscriptOptions,
  listSessionsOptions,
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage, readUserScopedItem } from "../../lib/account-storage";
import { AuthRecoveryContext } from "../auth/auth-recovery-context";
import { SidePanelShell } from "./side-panel-shell";
import { SideThreadPanel } from "./side-thread-panel";

beforeEach(() => clearUserScopedStorage());
afterEach(() => cleanup());

describe("shared side panel frame", () => {
  it("preserves the active panel and width during stream updates", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <SidePanelShell maximizable onClose={onClose} title="Tool result">
        <button type="button">Selected output</button>
        <p>First delivery</p>
      </SidePanelShell>,
    );
    const panel = screen.getByRole("complementary", { name: "Tool result" });
    const body = screen.getByTestId("side-panel-body");
    fireEvent.keyDown(screen.getByRole("button", { name: "Resize panel" }), {
      key: "ArrowLeft",
    });
    fireEvent.click(screen.getByRole("button", { name: "Maximize panel" }));
    screen.getByRole("button", { name: "Selected output" }).focus();
    body.scrollTop = 74;

    rerender(
      <SidePanelShell maximizable onClose={onClose} title="Tool result">
        <button type="button">Selected output</button>
        <p>Second delivery</p>
      </SidePanelShell>,
    );

    expect(screen.getByRole("complementary", { name: "Tool result" })).toBe(panel);
    expect(screen.getByTestId("side-panel-body")).toBe(body);
    expect(body.scrollTop).toBe(74);
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Selected output" }));
    expect(screen.getByRole("button", { name: "Restore panel" })).toBeTruthy();
    expect(readUserScopedItem("studio.chat.contentPanelWidth")).toBe("268");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("resizes and closes the panel accessibly", () => {
    const onClose = vi.fn();
    const trigger = document.createElement("button");
    trigger.textContent = "Open panel";
    document.body.append(trigger);
    trigger.focus();
    const { unmount } = render(
      <SidePanelShell onClose={onClose} title="Local canvas">
        <p>Notes</p>
      </SidePanelShell>,
    );
    const panel = screen.getByRole("complementary", { name: "Local canvas" });
    expect(panel.style.getPropertyValue("--content-panel-width")).toBe("256px");
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Close panel" }));
    const resize = screen.getByRole("button", { name: "Resize panel" });
    fireEvent.keyDown(resize, { key: "ArrowLeft" });
    expect(panel.style.getPropertyValue("--content-panel-width")).toBe("268px");
    fireEvent.pointerDown(resize, { clientX: 300 });
    fireEvent.pointerMove(window, { clientX: 250 });
    fireEvent.pointerUp(window);
    expect(panel.style.getPropertyValue("--content-panel-width")).toBe("318px");
    fireEvent.click(screen.getByRole("button", { name: "Close panel" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    unmount();
    expect(document.activeElement).toBe(trigger);
    trigger.remove();
  });

  it("hosts activity and authorization without owning their content", () => {
    function OwnedContent({ delivery }: { delivery: string }) {
      const [selection, setSelection] = useState("activity-a");
      const [decision, setDecision] = useState("pending");
      return (
        <>
          <button onClick={() => setSelection("activity-b")} type="button">
            {selection}
          </button>
          <button onClick={() => setDecision("reviewing")} type="button">
            {decision}
          </button>
          <p>{delivery}</p>
        </>
      );
    }
    const { rerender } = render(
      <SidePanelShell onClose={() => {}} title="Activity">
        <OwnedContent delivery="First" />
      </SidePanelShell>,
    );
    fireEvent.click(screen.getByRole("button", { name: "activity-a" }));
    fireEvent.click(screen.getByRole("button", { name: "pending" }));
    rerender(
      <SidePanelShell onClose={() => {}} title="Activity">
        <OwnedContent delivery="Second" />
      </SidePanelShell>,
    );
    expect(screen.getByRole("button", { name: "activity-b" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "reviewing" })).toBeTruthy();
    expect(screen.getByText("Second")).toBeTruthy();
  });

  it("mounts a reply thread composer inside the shared frame", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
    client.setQueryData(listSessionsOptions().queryKey, { complete: true, items: [] });
    client.setQueryData(getRuntimeSettingsOptions().queryKey, {
      buildId: "test",
      management: {
        providerConfiguration: false,
        providerConfigurationReason: "",
        routingConfiguration: false,
        routingConfigurationReason: "",
      },
      models: [],
      modelsReason: "",
      modelsSupported: false,
      providerEndpoint: "",
      providers: [],
      serverImplementation: "test",
    });
    client.setQueryData(getSessionDetailOptions({ path: { sessionId: "thread-1" } }).queryKey, {
      capabilities: { image: false, manualCompaction: false, modelSelection: false },
      id: "thread-1",
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
    client.setQueryData(getSessionTranscriptOptions({ path: { sessionId: "thread-1" } }).queryKey, {
      complete: true,
      messages: [],
      sessionId: "thread-1",
    });
    const route = createRootRoute({
      component: () => (
        <SideThreadPanel
          messageKey="occurrence"
          onClose={() => {}}
          parentSessionId="parent-1"
          sessionId="thread-1"
        />
      ),
    });
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/"] }),
      routeTree: route,
    });
    render(
      <QueryClientProvider client={client}>
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
    expect(await screen.findByRole("complementary", { name: "Thread" })).toBeTruthy();
    expect(screen.getByRole("textbox", { name: "Reply in thread" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Resize panel" })).toBeTruthy();
    client.clear();
  });
});
