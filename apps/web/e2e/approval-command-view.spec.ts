// SPDX-License-Identifier: Apache-2.0

import { createServer, type Server } from "node:http";
import type { RunStreamEvent } from "@mecatl-studio/contracts";
import type { Locator, Page } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

const sessionId = "s1";
const prefix = `/api/v1/sessions/${sessionId}`;
let frames: RunStreamEvent[] = [];
let activityServer: Server | undefined;
let activityPort = 0;

test.beforeAll(async () => {
  // Writes the run frames and keeps the stream open, so the ask stays pending.
  activityServer = createServer((_request, response) => {
    response.writeHead(200, { "Cache-Control": "no-store", "Content-Type": "text/event-stream" });
    response.write(
      frames.map((frame, index) => `id: ${index + 1}\ndata: ${JSON.stringify(frame)}\n\n`).join(""),
    );
  });
  await new Promise<void>((done) => activityServer?.listen(0, "127.0.0.1", done));
  const address = activityServer.address();
  if (!address || typeof address === "string") throw new Error("activity server has no port");
  activityPort = address.port;
});

test.afterAll(() => {
  activityServer?.closeAllConnections();
  activityServer?.close();
});

function lines(count: number): string {
  return Array.from({ length: count }, (_, index) => `echo step ${index + 1}`).join("\n");
}

/** Opens a running chat parked on one Shell ask with these arguments; returns its card. */
async function openAsk(offlineBff: OfflineBff, page: Page, args: string): Promise<Locator> {
  frames = [
    { runId: "run-a", sessionId, type: "run.started" },
    {
      event: {
        kind: "permission.ask",
        payload: { args, askId: "ask-1", reason: "Run a command", tool: "Shell" },
        runId: "run-a",
        seq: "1",
        text: "",
        turn: 1,
        unknown: false,
      },
      type: "run.event",
    },
  ] as RunStreamEvent[];
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "command-view-proof",
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
          delete: false,
          deleteReason: "",
          publicChat: true,
          publicChatReason: "",
          rename: false,
          renameReason: "",
        },
        createdAt: "2026-09-24T00:00:00Z",
        debugTargetSessionId: "",
        id: sessionId,
        modelId: "offline",
        state: "running",
        title: "A chat waiting on a command",
        turns: 1,
        updatedAt: "2026-09-24T00:00:00Z",
      },
    ],
  });
  offlineBff.json("GET", prefix, {
    capabilities: { image: false, manualCompaction: false, modelSelection: false },
    id: sessionId,
    mode: "default",
    state: "running",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  });
  offlineBff.json("GET", `${prefix}/transcript`, { complete: true, messages: [], sessionId });
  offlineBff.proxyLoopback(
    "GET",
    `${prefix}/activity`,
    `http://127.0.0.1:${activityPort}${prefix}/activity`,
  );
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const card = page.getByRole("region", { name: "Permission required: Shell" });
  await expect(card).toBeVisible();
  return card;
}

/** The text a selection of the whole element puts on the clipboard. */
async function copied(page: Page, element: Locator): Promise<string> {
  await element.evaluate((node) => {
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.selectAllChildren(node);
  });
  await page.keyboard.press("ControlOrMeta+C");
  return page.evaluate(() => navigator.clipboard.readText());
}

test("a long command shows its line count and expands to its last line", async ({
  offlineBff,
  page,
}) => {
  const card = await openAsk(offlineBff, page, JSON.stringify({ command: lines(60) }));
  const command = card.locator("pre");
  const toggle = card.getByRole("button", { name: "Show 48 more lines" });
  await card.scrollIntoViewIfNeeded();
  // The count and the toggle sit outside the command's scroll box, in view.
  await expect(card.getByText("60 lines", { exact: true })).toBeInViewport();
  await expect(toggle).toBeInViewport();
  await expect(command).toContainText("echo step 12");
  await expect(command).not.toContainText("echo step 13");
  await toggle.click();
  await expect(command).toContainText("echo step 60");
  await expect(card.getByRole("button", { name: "Show first 12 lines" })).toHaveAttribute(
    "aria-expanded",
    "true",
  );
});

test("a long wrapped command offers the whole command", async ({ offlineBff, page }) => {
  const card = await openAsk(
    offlineBff,
    page,
    JSON.stringify({ command: `echo ${"0123456789".repeat(400)}` }),
  );
  const command = card.locator("pre");
  await expect(card.getByText("1 line", { exact: true })).toBeVisible();
  // Capped to the compact box (its height follows the root font size) until expanded.
  await expect(command).not.toHaveCSS("max-height", "none");
  await card.getByRole("button", { name: "Show full command" }).click();
  await expect(command).toHaveCSS("max-height", "none");
  await expect(card.getByRole("button", { name: "Show less" })).toBeVisible();
});

test("invisible characters show as named badges, keep the text in order, and copy as sent", async ({
  context,
  offlineBff,
  page,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const codePoints = [0x202e, 0x202c, 0x200b];
  const [rlo, pdf, zwsp] = codePoints.map((codePoint) => String.fromCodePoint(codePoint));
  const text = `echo one ${rlo}two three${pdf} four${zwsp}five`;
  const args = JSON.stringify({ command: text });
  const card = await openAsk(offlineBff, page, args);
  await expect(card.getByText("Contains invisible characters", { exact: true })).toBeVisible();
  const command = card.locator("pre");
  for (const codePoint of codePoints) {
    const name = `U+${codePoint.toString(16).toUpperCase()}`;
    const badge = command.getByRole("img", { name: `invisible character ${name}` });
    await expect(badge).toBeVisible();
    expect(
      await badge.evaluate((node) =>
        getComputedStyle(node, "::before").getPropertyValue("content"),
      ),
    ).toBe(`"⟨${name}⟩"`);
  }
  // Isolated in its badge, the override no longer reverses the words after it.
  const lefts = await command.evaluate((node) => {
    const left = (word: string) => {
      const walker = document.createTreeWalker(node, NodeFilter.SHOW_TEXT);
      for (let text = walker.nextNode(); text; text = walker.nextNode()) {
        const at = (text.textContent ?? "").indexOf(word);
        if (at < 0) continue;
        const range = document.createRange();
        range.setStart(text, at);
        range.setEnd(text, at + word.length);
        return range.getBoundingClientRect().left;
      }
      throw new Error(`no ${word}`);
    };
    return { three: left("three"), two: left("two") };
  });
  expect(lefts.two).toBeLessThan(lefts.three);
  expect(await copied(page, command)).toBe(text);
  await card.getByRole("button", { name: "Raw arguments" }).click();
  await expect(command.getByRole("img")).toHaveCount(3);
  expect(await copied(page, command)).toBe(args);
});
