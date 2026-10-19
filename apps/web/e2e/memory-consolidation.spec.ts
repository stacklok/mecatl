// SPDX-License-Identifier: Apache-2.0

import type { Page } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

/**
 * Settings → Memory → Consolidate memory, in a real browser: each suggested
 * change's "Show current values" disclosure is reachable with Tab and opens
 * and closes with Enter and Space, the browser's find-in-page reaches the
 * closed values and opens the change it matched, and the disclosure adds no
 * heading or region. These hold for `<details>` and for the Collapsible.
 */

const plan = {
  expiresAt: "2099-01-01T00:00:00Z",
  id: "plan1",
  operations: [
    {
      exactDuplicateEligible: true,
      kind: "merge",
      reason: "near-duplicates",
      replacement: { description: "Merged editor preference", value: "Uses vim with keybindings" },
      sources: [{ description: "Older note", key: "editor_2", value: "Prefers vim keybindings" }],
      survivor: { description: "Editor preference", key: "editor", value: "Uses vim" },
    },
    {
      exactDuplicateEligible: false,
      kind: "merge",
      reason: "same fact",
      replacement: { description: "", value: "Works in Europe/Rome" },
      sources: [{ description: "", key: "tz", value: "CET" }],
      survivor: { description: "Time zone", key: "timezone", value: "Europe/Rome" },
    },
  ],
  plannedOperationCount: 2,
  plannedSourceCount: 4,
  target: "user_model",
};

function arrangeMemory(offlineBff: OfflineBff) {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: {
      manualDream: { userModel: { decide: true, generate: true } },
      memory: true,
      posture: "managed",
      userModel: true,
    },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { models: [], modelsSupported: false });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("GET", "/api/v1/user-memory", {
    items: [{ description: "Prefers vim keybindings", key: "editor" }],
    reason: "",
    sha256: "x",
    sizeBytes: "64",
    supported: true,
  });
  offlineBff.json("POST", "/api/v1/user-memory/consolidation/plans", plan, 201);
}

/** `window.find` is non-standard, so TypeScript's DOM types leave it out. */
type WindowWithFind = { find(text: string, caseSensitive?: boolean): boolean };

/** Generates the suggestion and returns its two changes. */
async function showPlan(page: Page) {
  await page.goto("/workspace/settings/memory");
  await page.getByRole("button", { name: "Consolidate memory" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Continue" }).click();
  return {
    editor: page.getByRole("listitem").filter({ hasText: "merges in editor_2" }),
    timezone: page.getByRole("listitem").filter({ hasText: "merges in tz" }),
  };
}

test("opens and closes a suggested change's current values from the keyboard", async ({
  offlineBff,
  page,
}) => {
  arrangeMemory(offlineBff);
  const { editor, timezone } = await showPlan(page);
  const editorTrigger = editor.getByText("Show current values");
  const timezoneTrigger = timezone.getByText("Show current values");
  const editorValue = editor.getByText("Prefers vim keybindings");
  const timezoneValue = timezone.getByText("CET", { exact: true });
  await expect(editorTrigger).toBeVisible();
  await expect(editorValue).toBeHidden();
  await expect(timezoneValue).toBeHidden();

  await editorTrigger.focus();
  await page.keyboard.press("Enter");
  await expect(editorValue).toBeVisible();
  await expect(editor.getByText("Older note")).toBeVisible();
  await expect(timezoneValue).toBeHidden();
  await expect(editorTrigger).toBeFocused();

  await page.keyboard.press("Space");
  await expect(editorValue).toBeHidden();
  await expect(editorTrigger).toBeFocused();

  await page.keyboard.press("Tab");
  await expect(timezoneTrigger).toBeFocused();
  await page.keyboard.press("Space");
  await expect(timezoneValue).toBeVisible();
  await expect(editorValue).toBeHidden();
  await page.keyboard.press("Shift+Tab");
  await expect(editorTrigger).toBeFocused();
});

test("find-in-page reaches a closed change's current values and opens that change", async ({
  offlineBff,
  page,
}) => {
  arrangeMemory(offlineBff);
  const { editor, timezone } = await showPlan(page);
  const olderNote = editor.getByText("Older note");
  const timezoneValue = timezone.getByText("CET", { exact: true });
  await expect(editor.getByText("Show current values")).toBeVisible();
  await expect(olderNote).toBeHidden();

  // The page's text search finds the closed values, from the top each time.
  for (const value of ["Older note", "Prefers vim keybindings", "CET"]) {
    const found = await page.evaluate((text) => {
      window.getSelection()?.removeAllRanges();
      return (window as unknown as WindowWithFind).find(text, true);
    }, value);
    expect(found, value).toBe(true);
  }

  // A text-fragment navigation runs the browser's find-in-page match, which
  // reveals the matched change (and only that one).
  await page.evaluate(() => window.getSelection()?.removeAllRanges());
  await page.goto(`${page.url().split("#")[0]}#:~:text=Older%20note`);
  await expect(olderNote).toBeVisible();
  await expect(editor.getByText("Prefers vim keybindings")).toBeVisible();
  await expect(timezoneValue).toBeHidden();

  // The match opened the disclosure itself, not just its text: the next
  // press of its trigger closes it.
  await editor.getByText("Show current values").click();
  await expect(olderNote).toBeHidden();
});

test("a change's disclosure adds no heading or region", async ({ offlineBff, page }) => {
  arrangeMemory(offlineBff);
  const { editor, timezone } = await showPlan(page);
  for (const change of [editor, timezone]) {
    await expect(change.getByRole("heading", { includeHidden: true })).toHaveCount(0);
    await expect(change.getByRole("region", { includeHidden: true })).toHaveCount(0);
  }
  await editor.getByText("Show current values").click();
  await expect(editor.getByText("Older note")).toBeVisible();
  await expect(editor.getByRole("heading", { includeHidden: true })).toHaveCount(0);
  await expect(editor.getByRole("region", { includeHidden: true })).toHaveCount(0);
});
