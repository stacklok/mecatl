// SPDX-License-Identifier: Apache-2.0

import { createServer, type Server } from "node:http";
import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { expect, type OfflineBff, test } from "./fixtures";

const sessionId = "s1";
const prefix = `/api/v1/sessions/${sessionId}`;
const frames: RunStreamEvent[] = [{ runId: "run-a", sessionId, type: "run.started" }];

let activityServer: Server | undefined;
let activityPort = 0;

test.beforeAll(async () => {
  // Writes the run frames and keeps the stream open, so the chat stays on a live run.
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

function runningSession(offlineBff: OfflineBff) {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "stop-proof",
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
        title: "A running chat with a long title that should truncate first",
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
}

test("the running chat keeps Stop inside the header at phone widths", async ({
  offlineBff,
  page,
}) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 844, width: 390 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  await expect(page.getByRole("textbox", { name: "Message Mecatl" })).toBeVisible();
  const header = page.locator("header").filter({ has: page.getByRole("button", { name: "Stop" }) });
  const stop = header.getByRole("button", { exact: true, name: "Stop" });
  await expect(stop).toBeVisible();
  // The run status pill is present, so the row carries its widest running content.
  await expect(header.getByText("Reconnected to run")).toBeAttached();

  for (const width of [320, 360, 390, 430]) {
    await page.setViewportSize({ height: 844, width });
    await expect(stop).toBeVisible();
    const box = await stop.boundingBox();
    expect(box, `Stop has a box at ${width}px`).not.toBeNull();
    if (!box) continue;
    expect(box.x, `Stop starts inside the viewport at ${width}px`).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width, `Stop ends inside the viewport at ${width}px`).toBeLessThanOrEqual(
      width,
    );
    expect(box.width, `Stop is at least 44px wide at ${width}px`).toBeGreaterThanOrEqual(44);
    expect(box.height, `Stop is at least 44px tall at ${width}px`).toBeGreaterThanOrEqual(44);
    // Nothing else in the header covers the control's centre.
    const hit = await page.evaluate(
      ({ x, y }) => document.elementFromPoint(x, y)?.closest("button")?.textContent?.trim(),
      { x: box.x + box.width / 2, y: box.y + box.height / 2 },
    );
    expect(hit, `Stop is the hit target at ${width}px`).toBe("Stop");
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
  }
});

test("the running chat keeps Activity and Canvas reachable on a phone", async ({
  offlineBff,
  page,
}) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 844, width: 390 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  await expect(page.getByRole("button", { name: "Stop" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open session activity" })).toBeVisible();

  // On a phone, Canvas moves from the header into the chat options menu.
  await expect(page.getByRole("button", { name: "Open local canvas" })).toHaveCount(0);
  await page.getByRole("button", { name: "Chat options" }).click();
  await page.getByRole("menuitem", { name: "Open local canvas" }).click();
  const canvas = page.getByRole("complementary", { name: "Local canvas" });
  await expect(canvas).toBeVisible();
  await expect(page.getByRole("button", { name: "Close panel" })).toBeFocused();
  await page.getByRole("button", { name: "Close panel" }).click();
  await expect(canvas).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Chat options" })).toBeFocused();
});

test("the running chat header keeps its desktop layout", async ({ offlineBff, page }) => {
  runningSession(offlineBff);
  await page.setViewportSize({ height: 800, width: 1280 });
  await page.goto(`/workspace/chat?sessionId=${sessionId}`);
  const header = page.locator("header").filter({ has: page.getByRole("button", { name: "Stop" }) });
  // Desktop shows the labelled Stop, the status pill and the keyboard hint, as before.
  await expect(header.getByRole("button", { exact: true, name: "Stop" })).toHaveText("Stop", {
    useInnerText: true,
  });
  await expect(header.getByText("Reconnected to run")).toBeVisible();
  await expect(header.getByText("Esc to Stop")).toBeVisible();
  await expect(header.getByRole("button", { name: "Open session activity" })).toHaveText(
    "Activity",
    { useInnerText: true },
  );
  // Canvas stays a header button, and the chat options menu does not repeat it.
  await expect(header.getByRole("button", { name: "Open local canvas" })).toHaveText("Canvas", {
    useInnerText: true,
  });
  await header.getByRole("button", { name: "Chat options" }).click();
  await expect(page.getByRole("menuitem", { name: "Inspect session" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Open local canvas" })).toHaveCount(0);
});
