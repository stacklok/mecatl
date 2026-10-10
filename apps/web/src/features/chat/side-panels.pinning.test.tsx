// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the side panels' focus and Escape order, the file preview's data
 * paths, and the thread's copy action before their frames take the
 * prototype's look. Outcomes only: which control holds focus, when Escape
 * closes, what text and links each preview renders, and what reaches the
 * clipboard.
 */

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
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import { AuthRecoveryContext } from "../auth/auth-recovery-context";
import { type ContentPreview, ContentPreviewPanel } from "./content-preview-panel";
import type { LocalFilePreview } from "./local-file-preview";
import { SideThreadPanel } from "./side-thread-panel";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

beforeEach(() => clearUserScopedStorage());
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function Harness({
  escapeManagedExternally = false,
  initial,
  onClose,
}: {
  escapeManagedExternally?: boolean;
  initial: ContentPreview;
  onClose?: () => void;
}) {
  const [preview, setPreview] = useState<ContentPreview | undefined>();
  const [canvas, setCanvas] = useState("");
  return (
    <>
      <button onClick={() => setPreview(initial)} type="button">
        Open preview
      </button>
      {preview && (
        <ContentPreviewPanel
          canvas={canvas}
          escapeManagedExternally={escapeManagedExternally}
          onCanvasChange={setCanvas}
          onClose={() => {
            onClose?.();
            setPreview(undefined);
          }}
          preview={preview}
        />
      )}
    </>
  );
}

function openFrom(initial: ContentPreview, extra: Partial<Parameters<typeof Harness>[0]> = {}) {
  render(<Harness initial={initial} {...extra} />);
  const trigger = screen.getByRole("button", { name: "Open preview" });
  trigger.focus();
  fireEvent.click(trigger);
  return trigger;
}

function file(overrides: Partial<LocalFilePreview>): LocalFilePreview {
  return { kind: "text", name: "notes.txt", size: 1200, type: "text/plain", ...overrides };
}

describe("side panel focus and Escape pins", () => {
  it("focuses the close control, closes on Escape, and returns focus to the opener", () => {
    const trigger = openFrom({ kind: "canvas" });
    const panel = screen.getByRole("complementary", { name: "Local canvas" });
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Close panel" }));
    fireEvent.keyDown(panel, { key: "Escape" });
    expect(screen.queryByRole("complementary", { name: "Local canvas" })).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("leaves Escape to an open menu or dialog first", () => {
    openFrom({ kind: "canvas" });
    const panel = screen.getByRole("complementary", { name: "Local canvas" });
    const menu = document.createElement("div");
    menu.setAttribute("role", "menu");
    menu.setAttribute("data-state", "open");
    document.body.append(menu);
    fireEvent.keyDown(panel, { key: "Escape" });
    expect(screen.getByRole("complementary", { name: "Local canvas" })).toBe(panel);
    menu.remove();
    fireEvent.keyDown(panel, { key: "Escape" });
    expect(screen.queryByRole("complementary", { name: "Local canvas" })).toBeNull();
  });

  it("leaves Escape to the chat surface when it manages the order", () => {
    const onClose = vi.fn();
    openFrom({ kind: "canvas" }, { escapeManagedExternally: true, onClose });
    fireEvent.keyDown(screen.getByRole("complementary", { name: "Local canvas" }), {
      key: "Escape",
    });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("closes from its close control and returns focus to the opener", () => {
    const trigger = openFrom({ file: file({ content: "hello" }), kind: "file" });
    fireEvent.click(screen.getByRole("button", { name: "Close panel" }));
    expect(screen.queryByRole("complementary", { name: "notes.txt" })).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it("keeps the canvas editable, previewable, and local", () => {
    openFrom({ kind: "canvas" });
    const editor = screen.getByRole("textbox", { name: "Local canvas" });
    fireEvent.change(editor, { target: { value: "# Plan\n\n- one" } });
    expect(screen.getByText("Saved only in this browser.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(screen.getByRole("heading", { name: "Plan" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(
      (screen.getByRole("textbox", { name: "Local canvas" }) as HTMLTextAreaElement).value,
    ).toBe("# Plan\n\n- one");
  });
});

describe("file preview data pins", () => {
  it("shows a code file's text, a markdown file's render, and an image", () => {
    const { unmount } = render(
      <ContentPreviewPanel
        canvas=""
        onCanvasChange={() => {}}
        onClose={() => {}}
        preview={{
          file: file({ content: "const a = 1;\nconst b = 2;", kind: "code", name: "a.ts" }),
          kind: "file",
        }}
      />,
    );
    const code = screen.getByRole("complementary", { name: "a.ts" });
    expect(code.textContent).toContain("const a = 1;");
    expect(code.textContent).toContain("const b = 2;");
    expect(code.textContent).toMatch(/Local preview/);
    expect(code.textContent).toMatch(/not sent to Mecatl/);
    unmount();

    const markdown = render(
      <ContentPreviewPanel
        canvas=""
        onCanvasChange={() => {}}
        onClose={() => {}}
        preview={{
          file: file({ content: "# Guide\n\nBody text", kind: "markdown", name: "guide.md" }),
          kind: "file",
        }}
      />,
    );
    expect(screen.getByRole("heading", { name: "Guide" })).toBeTruthy();
    expect(screen.getByText("Body text")).toBeTruthy();
    markdown.unmount();

    render(
      <ContentPreviewPanel
        canvas=""
        onCanvasChange={() => {}}
        onClose={() => {}}
        preview={{
          file: file({
            dataUrl: "data:image/png;base64,iVBORw0KGgo=",
            kind: "image",
            name: "chart.png",
            sent: true,
            type: "image/png",
          }),
          kind: "file",
        }}
      />,
    );
    expect(screen.getByRole("img", { name: "chart.png" }).getAttribute("src")).toBe(
      "data:image/png;base64,iVBORw0KGgo=",
    );
    expect(screen.getByRole("complementary", { name: "chart.png" }).textContent).toMatch(
      /Conversation image/,
    );
  });

  it("shows plain text as text, and explains a file with no preview", () => {
    const { unmount } = render(
      <ContentPreviewPanel
        canvas=""
        onCanvasChange={() => {}}
        onClose={() => {}}
        preview={{ file: file({ content: "line one" }), kind: "file" }}
      />,
    );
    expect(screen.getByText("line one")).toBeTruthy();
    unmount();
    render(
      <ContentPreviewPanel
        canvas=""
        onCanvasChange={() => {}}
        onClose={() => {}}
        preview={{ file: file({ kind: "unsupported", name: "blob.bin" }), kind: "file" }}
      />,
    );
    expect(screen.getByText(/no browser preview is available/i)).toBeTruthy();
  });
});

describe("side thread copy pin", () => {
  it("copies the thread as speaker-labelled paragraphs", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    // Offline: every read this panel starts stays pending; the test seeds the cache.
    vi.stubGlobal("fetch", () => new Promise<Response>(() => {}));
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
      kind: "main",
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
    await act(async () => {
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
      await router.load();
    });
    // The transcript lands after the panel mounts, as a fetched one does.
    await act(async () => {
      client.setQueryData(
        getSessionTranscriptOptions({ path: { sessionId: "thread-1" } }).queryKey,
        {
          complete: true,
          messages: [
            { images: [], role: "user", text: "Why a buffer?", toolCalls: [] },
            { images: [], role: "assistant", text: "It stops a leak.", toolCalls: [] },
          ],
          sessionId: "thread-1",
        },
      );
    });
    expect(await screen.findByText("It stops a leak.")).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Close thread" }));
    await user.click(screen.getByRole("button", { name: "Thread options" }));
    await user.click(await screen.findByRole("menuitem", { name: "Copy thread" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("You: Why a buffer?\n\nMecatl: It stops a leak."),
    );
    client.clear();
  });
});
