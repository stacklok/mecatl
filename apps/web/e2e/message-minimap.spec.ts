// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "./fixtures";

const capabilities = {
  copyId: true,
  copyIdReason: "",
  delete: true,
  deleteReason: "",
  fork: true,
  forkReason: "",
  inspect: true,
  inspectReason: "",
  publicChat: true,
  publicChatReason: "",
  rename: true,
  renameReason: "",
  viewTranscript: true,
  viewTranscriptReason: "",
};

test("an early minimap marker reaches its own row without moving the page", async ({
  offlineBff,
  page,
}, testInfo) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", {
    connection: "reachable",
    signInRequired: false,
  });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { models: [], modelsSupported: false });
  offlineBff.json("GET", "/api/v1/sessions", {
    complete: true,
    items: [
      {
        capabilities,
        createdAt: "2026-09-25T12:00:00Z",
        debugTargetSessionId: "",
        id: "long-chat",
        kind: "main",
        modelId: "offline",
        state: "idle",
        title: "Long chat",
        titleProvenance: "",
        titleRevision: "0",
        turns: 60,
        updatedAt: "2026-09-25T12:00:00Z",
      },
    ],
  });
  offlineBff.json("GET", "/api/v1/sessions/long-chat", {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: "long-chat",
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
  offlineBff.json("GET", "/api/v1/sessions/long-chat/transcript", {
    complete: true,
    messages: Array.from({ length: 60 }, (_, index) => ({
      images: [],
      role: index % 2 === 0 ? "user" : "assistant",
      text: `Recorded message ${index + 1} with enough text to occupy its own transcript row.`,
      toolCalls: [],
    })),
    sessionId: "long-chat",
  });
  offlineBff.on("GET", "/api/v1/sessions/long-chat/activity", () => ({
    body: "",
    contentType: "text/event-stream",
  }));

  await page.goto("/workspace/chat?sessionId=long-chat");
  const transcript = page.getByRole("region", { name: "Conversation transcript" });
  const minimap = page.getByRole("navigation", { name: "Message minimap" });
  await expect(minimap.getByRole("button")).toHaveCount(60);
  await expect.poll(() => transcript.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);

  const pageTop = await page.evaluate(() => document.scrollingElement?.scrollTop ?? 0);
  const first = minimap.getByRole("button", { name: /^Jump to message 1:/ });
  await first.click();
  await expect(first).toHaveAttribute("aria-current", "location");
  await expect.poll(() => transcript.evaluate((element) => element.scrollTop)).toBeLessThan(200);
  const row = transcript.getByRole("article").first();
  await expect(row).toContainText("Recorded message 1 with enough text");
  const rowBounds = await row.boundingBox();
  const portBounds = await transcript.boundingBox();
  expect(rowBounds).not.toBeNull();
  expect(portBounds).not.toBeNull();
  expect(rowBounds?.y ?? -1).toBeGreaterThanOrEqual((portBounds?.y ?? 0) - 2);
  expect((rowBounds?.y ?? 0) + (rowBounds?.height ?? 0)).toBeLessThanOrEqual(
    (portBounds?.y ?? 0) + (portBounds?.height ?? 0) + 2,
  );
  expect(await page.evaluate(() => document.scrollingElement?.scrollTop ?? 0)).toBe(pageTop);

  const second = minimap.getByRole("button", { name: /^Jump to message 2:/ });
  await second.focus();
  await second.press("Space");
  await expect(second).toHaveAttribute("aria-current", "location");
  for (const width of [320, 500, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    const mobileTarget = minimap.getByRole("button", { name: /^Jump to message 3:/ });
    await mobileTarget.focus();
    await mobileTarget.press("Space");
    await expect(mobileTarget).toHaveAttribute("aria-current", "location");
    const composer = page.getByRole("textbox", { name: "Message Mecatl" });
    await expect(composer).toBeVisible();
    const minimapBounds = await minimap.boundingBox();
    const composerBounds = await composer.boundingBox();
    expect(minimapBounds).not.toBeNull();
    expect(composerBounds).not.toBeNull();
    expect((minimapBounds?.y ?? 0) + (minimapBounds?.height ?? 0)).toBeLessThanOrEqual(
      (composerBounds?.y ?? 0) + 2,
    );
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
    await page.screenshot({ path: testInfo.outputPath(`minimap-${width}.png`) });
  }
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveClass(/dark/);
  for (const width of [320, 500, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    await page.screenshot({ path: testInfo.outputPath(`minimap-dark-${width}.png`) });
  }
});
