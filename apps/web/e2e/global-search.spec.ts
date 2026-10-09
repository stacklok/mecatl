// SPDX-License-Identifier: Apache-2.0

import type { Page } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

function workspace(offlineBff: OfflineBff) {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
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
        createdAt: "2026-09-24T00:00:00Z",
        debugTargetSessionId: "",
        id: "s-release",
        kind: "main",
        modelId: "offline-model",
        state: "idle",
        title: "Release notes review",
        titleProvenance: "",
        titleRevision: "0",
        turns: 0,
        updatedAt: "2026-09-24T00:00:00Z",
      },
    ],
  });
  offlineBff.json("GET", "/api/v1/schedules", { items: [], reason: "", supported: true });
  offlineBff.json("GET", "/api/v1/skills", { items: [], reason: "", supported: true });
  offlineBff.json("GET", "/api/v1/learned-skills", {
    complete: true,
    items: [],
    reason: "",
    supported: true,
  });
  offlineBff.json("GET", "/api/v1/user-memory", {
    items: [{ description: "Prefers short release notes", key: "release-style" }],
    reason: "",
    sha256: "",
    sizeBytes: "0",
    supported: true,
  });
}

/** The option the combobox names as active, which must be the highlighted one. */
async function activeOptionName(page: Page) {
  return page.evaluate(() => {
    const input = document.querySelector('[role="combobox"]');
    const id = input?.getAttribute("aria-activedescendant");
    const option = id ? document.getElementById(id) : null;
    return {
      highlighted: option?.getAttribute("aria-selected") === "true",
      text: option?.textContent ?? null,
    };
  });
}

test("search palette opens a result from the keyboard and restores focus", async ({
  offlineBff,
  page,
}) => {
  workspace(offlineBff);
  await page.goto("/workspace/chat");
  const trigger = page.getByRole("button", { name: "Search", exact: true });
  await expect(trigger).toBeVisible();

  await trigger.click();
  const dialog = page.getByRole("dialog", { name: "Search Mecatl" });
  const input = dialog.getByRole("combobox", {
    name: "Search chats, schedules, skills, and memory",
  });
  await expect(input).toBeFocused();
  await input.fill("release");
  const results = dialog.getByRole("listbox", { name: "Search results" });
  await expect(results.getByRole("option")).toHaveCount(2);
  await expect(dialog.getByText("2 results")).toBeVisible();
  // The first result is highlighted and announced before any key is pressed.
  await expect
    .poll(() => activeOptionName(page))
    .toEqual({
      highlighted: true,
      text: "Release notes reviewoffline-model",
    });
  await input.press("ArrowDown");
  await expect
    .poll(() => activeOptionName(page))
    .toEqual({
      highlighted: true,
      text: "release-stylePrefers short release notes",
    });
  await input.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();

  await page.keyboard.press("ControlOrMeta+k");
  await expect(input).toBeFocused();
  await input.fill("shortcuts");
  await expect(results.getByRole("option")).toHaveText([
    "Keyboard shortcutsEvery keyboard shortcut in the app.",
  ]);
  await input.press("Enter");
  await expect(page).toHaveURL(/\/workspace\/shortcuts$/);
  await expect(dialog).toHaveCount(0);
});

test("search palette fits the viewport and keeps its close control reachable", async ({
  offlineBff,
  page,
}) => {
  workspace(offlineBff);
  await page.goto("/workspace/chat");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Search Mecatl" });
  await expect(dialog).toBeVisible();
  const viewport = page.viewportSize();
  if (!viewport) throw new Error("no viewport");
  const bounds = await dialog.boundingBox();
  expect(bounds).not.toBeNull();
  expect(bounds?.x ?? -1).toBeGreaterThanOrEqual(0);
  expect((bounds?.x ?? 0) + (bounds?.width ?? 0)).toBeLessThanOrEqual(viewport.width + 1);
  expect((bounds?.y ?? 0) + (bounds?.height ?? 0)).toBeLessThanOrEqual(viewport.height + 1);
  const close = dialog.getByRole("button", { name: "Close search" });
  const closeBounds = await close.boundingBox();
  expect(closeBounds?.width ?? 0).toBeGreaterThanOrEqual(44);
  expect(closeBounds?.height ?? 0).toBeGreaterThanOrEqual(44);
  await close.click();
  await expect(dialog).toHaveCount(0);
});

test("top nav keeps every target reachable from 336 to 1280 px", async ({ offlineBff, page }) => {
  workspace(offlineBff);
  await page.goto("/workspace/chat");
  const nav = page.getByRole("navigation", { name: "Main navigation" });
  const targets = [
    page.getByRole("link", { name: "Stacklok — go to Chats" }),
    nav.getByRole("link", { name: "Chats" }),
    nav.getByRole("link", { name: "Scheduled" }),
    nav.getByRole("link", { name: "Skills" }),
    nav.getByRole("link", { name: "Settings" }),
    page.getByRole("button", { name: "Search", exact: true }),
  ];
  await page.setViewportSize({ width: 320, height: 700 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(320);
  // Below 336px, six 44px targets at the 18px mobile root size cannot sit
  // side by side; that predates this layout. Every wider phone fits.
  for (const width of [336, 360, 390, 499, 500, 560, 640, 768, 900, 1280]) {
    await page.setViewportSize({ width, height: 700 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
    const boxes = [];
    for (const target of targets) {
      const box = await target.boundingBox();
      if (!box) throw new Error(`target hidden at ${width}px`);
      expect(box.x, `${width}px`).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width, `${width}px`).toBeLessThanOrEqual(width);
      expect(box.width, `${width}px`).toBeGreaterThanOrEqual(44);
      expect(box.height, `${width}px`).toBeGreaterThanOrEqual(44);
      boxes.push(box);
    }
    // No two targets overlap.
    for (let index = 1; index < boxes.length; index += 1) {
      const previous = boxes[index - 1];
      const current = boxes[index];
      if (previous && current) {
        expect(current.x, `${width}px`).toBeGreaterThanOrEqual(previous.x + previous.width);
      }
    }
    // The trigger becomes a labelled search field from 500px.
    const label = page.getByRole("button", { name: "Search", exact: true }).getByText("Search…");
    if (width < 500) await expect(label).toBeHidden();
    else await expect(label).toBeVisible();
  }
});
