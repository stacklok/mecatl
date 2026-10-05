// SPDX-License-Identifier: Apache-2.0

import type { Locator } from "@playwright/test";
import { expect, test } from "./fixtures";

test("Writer stays author-owned through observation, discussion, and undo", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
    experimentalWriter: true,
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", {
    modelsSupported: true,
    models: [
      { id: "one", providerId: "offline", displayName: "One", image: false },
      {
        id: "model-two",
        providerId: "second-provider",
        displayName: "Experimental target",
        image: false,
      },
    ],
  });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", {
    status: "observe",
    text: "What evidence supports this assumption?",
  });
  offlineBff.json("POST", "/api/v1/writer/discuss", { text: "Keep your own voice." });

  await page.goto("/workspace/writer");
  const editor = page.getByRole("textbox", { name: "Writer document" });
  await expect(editor).toBeVisible();
  const modelPicker = page.getByRole("button", { name: "Writer model" });
  await modelPicker.click();
  await expect(page.getByRole("menuitem", { name: "Deployment default" })).toBeVisible();
  const modelFilter = page.getByRole("textbox", { name: "Filter models" });
  await modelFilter.fill("model-two");
  await modelFilter.press("ArrowDown");
  await expect(page.getByRole("menuitem", { name: "Experimental target" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(modelPicker).toHaveText("Experimental target");
  await modelPicker.click();
  await modelFilter.press("Escape");
  await expect(modelPicker).toBeFocused();
  await modelPicker.click();
  await modelFilter.fill("one");
  await modelFilter.press("ArrowDown");
  await expect(page.getByRole("menuitem", { name: "One" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(modelPicker).toHaveText("One");
  await editor.fill("First draft");
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/observe").length).toBe(1);
  const observe = offlineBff.requestsFor("POST", "/api/v1/writer/observe")[0];
  expect(observe?.postDataJSON()).toMatchObject({
    document: { content: "First draft", revision: 1 },
    model: { id: "one", providerId: "offline" },
  });
  await expect(page.getByText("What evidence supports this assumption?")).toBeVisible();
  await expect(editor).toHaveText("First draft");
  await page.getByRole("button", { name: "Discuss", exact: true }).click();
  await editor.fill("Second draft");
  await page.getByRole("textbox", { name: "Message Mecatl" }).fill("Why?");
  await page.getByRole("textbox", { name: "Message Mecatl" }).press("Enter");
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/discuss").length).toBe(1);
  const discuss = offlineBff.requestsFor("POST", "/api/v1/writer/discuss")[0];
  expect(discuss?.postDataJSON()).toMatchObject({
    document: { content: "Second draft" },
    model: { id: "one", providerId: "offline" },
    message: "Why?",
  });
  await expect(page.getByText("Keep your own voice.")).toBeVisible();
  await expect(editor).toHaveText("Second draft");
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor).toHaveText("First draft");
});

test("Writer draws one empty-editor caret on the placeholder line in light and dark themes", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
    experimentalWriter: true,
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: true, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });

  const emptyCaretGeometry = (editor: Locator) =>
    editor.evaluate((content) => {
      const root = content.closest(".cm-editor");
      const placeholder = root?.querySelector<HTMLElement>(".cm-placeholder");
      if (!placeholder) throw new Error("CodeMirror empty placeholder is missing");
      const range = document.createRange();
      range.selectNodeContents(placeholder);
      const placeholderBox = range.getBoundingClientRect();
      const cursors = root?.querySelectorAll<HTMLElement>(".cm-cursorLayer .cm-cursor");
      const cursor = cursors?.[0];
      if (!cursor || cursors?.length !== 1)
        throw new Error("Expected one drawn caret in the cursor layer");
      const cursorBox = cursor.getBoundingClientRect();
      return {
        caretColor: getComputedStyle(content).caretColor,
        cursorHeight: cursorBox.height,
        cursorLeft: cursorBox.left,
        cursorTop: cursorBox.top,
        placeholderLeft: placeholderBox.left,
        placeholderTop: placeholderBox.top,
      };
    });

  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    await page.goto("/workspace/writer");
    const editor = page.getByRole("textbox", { name: "Writer document" });
    await editor.focus();
    await expect(page.locator(".cm-cursorLayer .cm-cursor")).toHaveCount(1);

    const initial = await emptyCaretGeometry(editor);
    expect(Math.abs(initial.cursorLeft - initial.placeholderLeft)).toBeLessThan(1);
    expect(Math.abs(initial.cursorTop - initial.placeholderTop)).toBeLessThan(1);
    expect(initial.cursorHeight).toBeGreaterThan(0);
    expect(initial.caretColor).toBe("rgba(0, 0, 0, 0)");

    await editor.press("x");
    await expect(editor).toHaveText("x");
    await page.waitForTimeout(800);
    await editor.press("Backspace");
    await expect(page.getByText("Start writing in Markdown…")).toBeVisible();
    await editor.focus();
    const afterDelete = await emptyCaretGeometry(editor);
    expect(Math.abs(afterDelete.cursorTop - afterDelete.placeholderTop)).toBeLessThan(1);

    await page.getByRole("button", { name: "Undo" }).click();
    await expect(editor).toHaveText("x");
    await page.getByRole("button", { name: "Undo" }).click();
    await expect(page.getByText("Start writing in Markdown…")).toBeVisible();
    await editor.focus();
  }
});
