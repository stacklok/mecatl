// SPDX-License-Identifier: Apache-2.0

import type { Page } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

/**
 * A turn's tool-call disclosure in the transcript, in a real browser: the
 * toggle opens and closes from the keyboard, the closed list stays out of
 * sight while the tool names stay findable with the page's text search, and
 * the disclosure adds no heading or region.
 */

function arrangeChat(offlineBff: OfflineBff) {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { models: [], modelsSupported: false });
  offlineBff.json("GET", "/api/v1/sessions", {
    complete: true,
    items: [
      {
        capabilities: {
          copyId: true,
          copyIdReason: "",
          delete: false,
          deleteReason: "",
          fork: true,
          forkReason: "",
          inspect: true,
          inspectReason: "",
          publicChat: true,
          publicChatReason: "",
          rename: false,
          renameReason: "",
          viewTranscript: true,
          viewTranscriptReason: "",
        },
        createdAt: "2026-09-24T12:00:00Z",
        debugTargetSessionId: "",
        id: "chat-a",
        kind: "main",
        modelId: "offline",
        state: "idle",
        title: "Tool chat",
        titleProvenance: "",
        titleRevision: "0",
        turns: 1,
        updatedAt: "2026-09-24T12:00:00Z",
      },
    ],
  });
  offlineBff.json("GET", "/api/v1/sessions/chat-a", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "chat-a",
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
  offlineBff.json("GET", "/api/v1/sessions/chat-a/transcript", {
    complete: true,
    messages: [
      { images: [], role: "user", text: "Check the notes", toolCalls: [] },
      {
        images: [],
        role: "assistant",
        text: "",
        toolCalls: [
          { args: JSON.stringify({ path: "src/quokka-notes.md" }), id: "call-read", name: "Read" },
          { args: "{}", id: "call-mcp", name: "mcp__github__issue_write" },
        ],
      },
      {
        images: [],
        role: "tool",
        text: "",
        toolCalls: [],
        toolResult: { callId: "call-read", content: "hello", isError: false },
      },
      {
        images: [],
        role: "tool",
        text: "",
        toolCalls: [],
        toolResult: { callId: "call-mcp", content: "denied", isError: true },
      },
      { images: [], role: "assistant", text: "The notes are fine.", toolCalls: [] },
    ],
    sessionId: "chat-a",
  });
  offlineBff.on("GET", "/api/v1/sessions/chat-a/activity", () => ({
    body: "",
    contentType: "text/event-stream",
  }));
}

/** Searches the page's text with the non-standard `window.find`, from the top. */
function findInPage(page: Page, text: string) {
  return page.evaluate((query) => {
    window.getSelection()?.removeAllRanges();
    return (window as unknown as { find(text: string): boolean }).find(query);
  }, text);
}

async function showChat(page: Page) {
  // Tool calls show in the transcript only with the Interface preference on.
  await page.addInitScript(() => {
    localStorage.setItem("studio.account", "offline");
    localStorage.setItem("studio.profile.show-tool-calls", "visible");
  });
  await page.goto("/workspace/chat?sessionId=chat-a");
  const toggle = page.getByRole("button", { name: /^Activity: 2 tools/ });
  await expect(toggle).toBeVisible();
  return { group: toggle.locator(".."), toggle };
}

test("the tool-call disclosure opens and closes from the keyboard", async ({
  offlineBff,
  page,
}) => {
  arrangeChat(offlineBff);
  const { group, toggle } = await showChat(page);
  const args = group.getByText("path: src/quokka-notes.md");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(toggle).toContainText("Activity: 2 tools · 1 failed Read · GitHub · Issue write");
  await expect(args).toBeHidden();

  await toggle.focus();
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(args).toBeVisible();
  await expect(group.getByRole("listitem")).toHaveCount(2);
  await expect(toggle).toBeFocused();

  await page.keyboard.press("Space");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(args).toBeHidden();
  await expect(group.getByRole("listitem")).toHaveCount(0);
});

test("a closed tool-call list keeps the tool names findable", async ({ offlineBff, page }) => {
  arrangeChat(offlineBff);
  const { group, toggle } = await showChat(page);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(group.getByText("path: src/quokka-notes.md")).toBeHidden();
  expect(await findInPage(page, "GitHub · Issue write")).toBe(true);
});

test("the tool-call disclosure adds no heading or region", async ({ offlineBff, page }) => {
  arrangeChat(offlineBff);
  const { group, toggle } = await showChat(page);
  await expect(group.getByRole("heading", { includeHidden: true })).toHaveCount(0);
  await expect(group.getByRole("region", { includeHidden: true })).toHaveCount(0);
  await toggle.click();
  await expect(group.getByText("path: src/quokka-notes.md")).toBeVisible();
  await expect(group.getByRole("heading", { includeHidden: true })).toHaveCount(0);
  await expect(group.getByRole("region", { includeHidden: true })).toHaveCount(0);
});
