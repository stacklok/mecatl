// SPDX-License-Identifier: Apache-2.0

import type { Locator } from "@playwright/test";
import { expect, test } from "./fixtures";

test("Writer opens a local document as a new snapshot without attaching it or analyzing automatically", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });
  offlineBff.json("POST", "/api/v1/writer/discuss", {
    mode: "reply",
    text: "Current document only",
  });
  await page.goto("/workspace/writer");
  const editor = page.getByRole("textbox", { name: "Writer document" });
  await editor.fill("Old draft");
  const chooser = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Open document…" }).click();
  await (await chooser).setFiles({
    name: "bad.md",
    mimeType: "text/markdown",
    buffer: Buffer.from([0xff]),
  });
  await expect(page.getByRole("alert")).toContainText("valid UTF-8");
  await expect(editor).toHaveText("Old draft");
  page.once("dialog", (dialog) => void dialog.dismiss());
  const cancelled = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Open document…" }).click();
  await (await cancelled).setFiles({
    name: "new.md",
    mimeType: "text/markdown",
    buffer: Buffer.from("# New document"),
  });
  await expect(editor).toHaveText("Old draft");
  page.once("dialog", (dialog) => void dialog.accept());
  const accepted = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Open document…" }).click();
  await (await accepted).setFiles({
    name: "new.md",
    mimeType: "text/markdown",
    buffer: Buffer.from("# New document"),
  });
  await expect(editor).toHaveText("# New document");
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor).toHaveText("# New document");
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/observe")).toHaveLength(0);
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/discuss")).toHaveLength(0);
  await page.getByRole("textbox", { name: "Ask Writer" }).fill("What is here?");
  await page.getByRole("textbox", { name: "Ask Writer" }).press("Enter");
  await expect(page.getByText("Current document only")).toBeVisible();
  const body = offlineBff.requestsFor("POST", "/api/v1/writer/discuss")[0]?.postDataJSON();
  expect(body).toMatchObject({ document: { content: "# New document" }, discussion: [] });
  expect(body).not.toHaveProperty("references");
});

test("Writer previews conversational starting points and selected revisions before explicit adoption", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/discuss", {
    mode: "proposal",
    text: "An outline",
    candidate: "# Outline\n\n[Evidence needed]",
  });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });
  await page.goto("/workspace/writer");
  const editor = page.getByRole("textbox", { name: "Writer document" });
  const composer = page.getByRole("textbox", { name: "Ask Writer" });
  const addContext = page.getByRole("button", { name: "Add context" });
  await composer.focus();
  await composer.press("Tab");
  await expect(addContext).toBeFocused();
  await page.keyboard.press("Enter");
  const attachReferences = page.getByRole("menuitem", { name: "Attach reference files" });
  await expect(attachReferences).toBeFocused();
  const chooser = page.waitForEvent("filechooser");
  await page.keyboard.press("Enter");
  await (await chooser).setFiles({
    name: "notes.md",
    mimeType: "text/markdown",
    buffer: Buffer.from("<notes> & evidence"),
  });
  await expect(page.getByRole("button", { name: "Remove notes.md" })).toBeVisible();
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/discuss")).toHaveLength(0);
  await composer.fill("Outline this idea; evidence is unverified");
  await composer.press("Enter");
  const preview = page.getByRole("region", { name: "Writer preview" });
  await expect(preview).toBeVisible();
  await expect(editor).toHaveText("What would you like to write?");
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/discuss")[0]?.postDataJSON()).toMatchObject(
    {
      message: "Outline this idea; evidence is unverified",
      document: { content: "" },
      references: [{ name: "notes.md", content: "<notes> & evidence" }],
    },
  );
  await page.getByRole("button", { name: "Remove notes.md" }).click();
  await expect(preview).not.toBeVisible();
  await composer.fill("Outline this idea; evidence is unverified");
  await composer.press("Enter");
  await expect(preview).toBeVisible();
  await page.getByRole("button", { name: "Use this starting point" }).click();
  await expect(editor).toHaveText("# Outline[Evidence needed]");
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor).toHaveText("What would you like to write?");
  offlineBff.json("POST", "/api/v1/writer/discuss", {
    mode: "proposal",
    text: "More precise",
    candidate: "Clearer",
  });
  await editor.fill("Same. Same.");
  await editor.focus();
  await page.keyboard.press("Home");
  await page.keyboard.press("End");
  await page.keyboard.down("Shift");
  await page.keyboard.press("ArrowLeft");
  await page.keyboard.press("ArrowLeft");
  await page.keyboard.press("ArrowLeft");
  await page.keyboard.press("ArrowLeft");
  await page.keyboard.up("Shift");
  await page.getByRole("button", { name: "Add context" }).click();
  await page.getByRole("menuitem", { name: "Use selected passage" }).click();
  await expect(page.getByRole("button", { name: "Remove selected passage" })).toBeVisible();
  await composer.fill("Rewrite selected passage");
  await composer.press("Enter");
  await expect(preview).toBeVisible();
  await expect(editor).toHaveText("Same. Same.");
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/discuss")[2]?.postDataJSON()).toMatchObject(
    { passage: { text: "ame." } },
  );
  await page.getByRole("textbox", { name: "After (editable)" }).fill("new");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(editor).toHaveText("Same. Snew");
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor).toHaveText("Same. Same.");
});

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
  offlineBff.json("POST", "/api/v1/writer/discuss", {
    mode: "reply",
    text: "Keep your own voice.",
  });

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
  await page.getByRole("button", { name: "Add brief" }).click();
  await page
    .getByRole("textbox", { name: /Writing brief/ })
    .fill("Audience: maintainers; focus on assumptions");
  await page.getByRole("button", { name: "Apply brief" }).click();
  await editor.click();
  await editor.fill("First draft");
  await expect(editor).toHaveText("First draft");
  await page.getByRole("button", { name: "Read this now" }).click();
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/observe").length).toBe(1);
  const observe = offlineBff.requestsFor("POST", "/api/v1/writer/observe")[0];
  expect(observe?.postDataJSON()).toMatchObject({
    document: { content: "First draft", revision: 1 },
    model: { id: "one", providerId: "offline" },
    brief: "Audience: maintainers; focus on assumptions",
  });
  await expect(page.getByText("What evidence supports this assumption?")).toBeVisible();
  await expect(page.getByText("Checked", { exact: true })).toBeVisible();
  await expect(page.getByText(/Revision \d/)).toHaveCount(0);
  await expect(page.getByText("Open", { exact: true })).toBeVisible();
  await expect(editor).toHaveText("First draft");
  await page.getByRole("button", { name: "Open thread" }).click();
  await expect(page.getByRole("heading", { name: "Observation discussion" })).toBeVisible();
  // CodeMirror groups nearby author edits into one undo event.
  await page.waitForTimeout(600);
  await editor.fill("Second draft");
  await expect(editor).toHaveText("Second draft");
  await page.getByRole("textbox", { name: "Ask Writer" }).fill("Why?");
  await page.getByRole("textbox", { name: "Ask Writer" }).press("Enter");
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/discuss").length).toBe(1);
  const discuss = offlineBff.requestsFor("POST", "/api/v1/writer/discuss")[0];
  expect(discuss?.postDataJSON()).toMatchObject({
    document: { content: "Second draft" },
    model: { id: "one", providerId: "offline" },
    message: "Why?",
  });
  await expect(page.getByText("Keep your own voice.")).toBeVisible();
  const decision = page.getByRole("textbox", { name: "Decision for this draft" });
  await decision.fill("Costs outside scope");
  await decision.press("Enter");
  await expect(page.getByText("Decision saved.")).toBeVisible();
  await page.getByRole("button", { name: "Addressed" }).click();
  await page.getByRole("button", { name: "← Back to conversation" }).click();
  await page.getByText("History · 1 closed threads").click();
  await page.getByRole("button", { name: "Open thread" }).click();
  await expect(decision).toHaveValue("Costs outside scope");
  await decision.fill("Saved with the button");
  await page.getByRole("button", { name: "Save decision" }).click();
  await expect(page.getByText("Decision saved.")).toBeVisible();
  await expect(decision).toHaveValue("Saved with the button");
  await decision.fill("Keep costs for the next draft");
  await decision.press("Enter");
  await expect(page.getByText("Decision saved.")).toBeVisible();
  await expect(decision).toHaveValue("Keep costs for the next draft");
  await expect(page.getByText(/^Addressed · earlier draft$/)).toBeVisible();
  await expect(editor).toHaveText("Second draft");
  const automaticFeedback = page.getByRole("switch", { name: "Automatic feedback" });
  await expect(automaticFeedback).toBeChecked();
  await automaticFeedback.click();
  await expect(automaticFeedback).not.toBeChecked();
  await page.getByRole("button", { name: "Read this now" }).click();
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/observe").length).toBe(2);
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/observe")[1]?.postDataJSON()).toMatchObject(
    {
      brief: "Audience: maintainers; focus on assumptions",
      observations: [{ status: "addressed", decision: "Keep costs for the next draft" }],
      decisions: [
        {
          text: "What evidence supports this assumption?",
          decision: "Keep costs for the next draft",
        },
      ],
    },
  );
  await expect(editor).toHaveText("Second draft");
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor).toHaveText("First draft");
});

test("Writer feedback controls fit without clipping at desktop and mobile widths", async ({
  offlineBff,
  page,
}, testInfo) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });

  await page.goto("/workspace/writer");
  const saveStatus = page
    .getByRole("status")
    .filter({ hasText: "Clear · not saved in this browser" });
  const privacy = page.locator("details").filter({
    has: page.getByText("Storage and privacy", { exact: true }),
  });
  const privacySummary = privacy.getByText("Storage and privacy", { exact: true });
  const privacyPolicy = privacy.getByText("Browser storage is not a backup or cross-device sync.", {
    exact: false,
  });
  await expect(saveStatus).toBeVisible();
  await expect(privacy).not.toHaveAttribute("open", "");
  await expect(privacyPolicy).not.toBeVisible();
  await privacySummary.focus();
  await page.keyboard.press("Enter");
  await expect(privacyPolicy).toBeVisible();
  await privacySummary.click();
  await expect(privacyPolicy).not.toBeVisible();
  await privacySummary.click();
  await expect(privacyPolicy).toBeVisible();
  const sidebar = page.getByRole("complementary", { name: "Writer conversation" });
  const title = sidebar.getByRole("heading", { name: "Writer" });
  const automaticFeedback = sidebar.getByRole("switch", { name: "Automatic feedback" });
  const readNow = sidebar.getByRole("button", { name: "Read this now" });
  const addBrief = page.getByRole("button", { name: "Add brief" });
  await expect(automaticFeedback).toBeChecked();
  await expect(addBrief).toBeVisible();
  await addBrief.focus();
  await expect(addBrief).toBeFocused();
  await addBrief.click();
  await expect(page.getByRole("textbox", { name: /Writing brief/ })).toBeVisible();

  const sidebarBox = await sidebar.boundingBox();
  const [titleBox, feedbackBox, readNowBox] = await Promise.all([
    title.boundingBox(),
    automaticFeedback.boundingBox(),
    readNow.boundingBox(),
  ]);
  if (!sidebarBox || !titleBox || !feedbackBox || !readNowBox)
    throw new Error("Writer feedback controls are not laid out");
  if (testInfo.project.name === "chromium-desktop") expect(sidebarBox.width).toBe(360);
  for (const box of [titleBox, feedbackBox, readNowBox]) {
    expect(box.x).toBeGreaterThanOrEqual(sidebarBox.x);
    expect(box.x + box.width).toBeLessThanOrEqual(sidebarBox.x + sidebarBox.width);
    expect(box.y).toBeGreaterThanOrEqual(sidebarBox.y);
    expect(box.y + box.height).toBeLessThanOrEqual(sidebarBox.y + sidebarBox.height);
  }
  expect(titleBox.y + titleBox.height).toBeLessThanOrEqual(feedbackBox.y);
  expect(feedbackBox.y + feedbackBox.height).toBeLessThanOrEqual(readNowBox.y);
  await expect
    .poll(() => sidebar.evaluate((element) => element.scrollWidth <= element.clientWidth))
    .toBe(true);
});

test("Reveal passage scrolls to the first of reversed quotes only on author request", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", {
    status: "observe",
    text: "How do these sections relate?",
    quotes: ["Later", "First"],
  });

  await page.goto("/workspace/writer");
  const editor = page.getByRole("textbox", { name: "Writer document" });
  await editor.fill(`First\n${"intervening line\n".repeat(80)}Later`);
  await editor.press("ControlOrMeta+Home");
  const scroller = page.locator(".cm-scroller");
  const before = await scroller.evaluate((node) => node.scrollTop);
  await page.getByRole("button", { name: "Read this now" }).click();
  await expect(page.getByText("How do these sections relate?")).toBeVisible();
  await expect.poll(() => scroller.evaluate((node) => node.scrollTop)).toBe(before);
  await page.getByRole("button", { name: "Reveal passage" }).click();
  await expect(page.locator(".cm-writer-passage")).toHaveCount(2);
  await expect.poll(() => scroller.evaluate((node) => node.scrollTop)).toBeGreaterThan(before);
});

test("Writer keeps the native gutter aligned with a bounded document through editing, undo, and scrolling", async ({
  offlineBff,
  page,
}, testInfo) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });

  if (testInfo.project.name === "chromium-desktop")
    await page.setViewportSize({ width: 1720, height: 900 });
  await page.goto("/workspace/writer");
  const editor = page.getByRole("textbox", { name: "Writer document" });
  const scroller = page.locator(".cm-scroller");
  const lineNumbers = page.locator(".cm-lineNumbers .cm-gutterElement:visible");
  const draftLines = [
    `A Markdown line that is deliberately long enough to wrap in the Writer surface. ${"More text ".repeat(20)}`,
    "Second line",
    "Third line",
  ];
  const draft = draftLines.join("\n");
  await editor.fill(draft);
  await expect(editor.locator(".cm-line")).toHaveText(draftLines);
  await expect(lineNumbers).toHaveText(["1", "2", "3"]);
  const [firstGutterBox, firstLineBox] = await Promise.all([
    lineNumbers.first().boundingBox(),
    editor.locator(".cm-line").first().boundingBox(),
  ]);
  if (!firstGutterBox || !firstLineBox)
    throw new Error("Writer gutter or document line is missing");
  expect(firstGutterBox.x).toBeLessThan(firstLineBox.x);
  expect(firstLineBox.x - (firstGutterBox.x + firstGutterBox.width)).toBeLessThan(64);
  expect(Math.abs(firstGutterBox.y - firstLineBox.y)).toBeLessThan(2);
  await expect
    .poll(() => scroller.evaluate((node) => node.scrollWidth <= node.clientWidth))
    .toBe(true);

  // Let CodeMirror close the initial input history event before adding a line.
  await page.waitForTimeout(600);
  await editor.press("End");
  await editor.press("Enter");
  await editor.pressSequentially("Fourth line");
  await expect(lineNumbers).toHaveText(["1", "2", "3", "4"]);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor.locator(".cm-line")).toHaveText(draftLines);
  await expect(lineNumbers).toHaveText(["1", "2", "3"]);

  const longDraft = Array.from({ length: 120 }, (_, index) => `Line ${index + 1}`).join("\n");
  await editor.fill(longDraft);
  await scroller.evaluate((node) => {
    node.scrollTop = node.scrollHeight;
  });
  await expect.poll(() => scroller.evaluate((node) => node.scrollTop)).toBeGreaterThan(0);
  await expect(lineNumbers.last()).toHaveText("120");
  await expect(editor.locator(".cm-line").last()).toHaveText("Line 120");
  const [lastGutterBox, lastLineBox] = await Promise.all([
    lineNumbers.last().boundingBox(),
    editor.locator(".cm-line").last().boundingBox(),
  ]);
  if (!lastGutterBox || !lastLineBox) throw new Error("Writer long-document layout is missing");
  expect(Math.abs(lastGutterBox.y - lastLineBox.y)).toBeLessThan(2);
});

test("Writer accepts repeated Enter presses as Markdown lines and preserves them through undo and redo", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });

  await page.goto("/workspace/writer");
  const automaticFeedback = page.getByRole("switch", { name: "Automatic feedback" });
  await automaticFeedback.click();
  await expect(automaticFeedback).not.toBeChecked();
  const editor = page.getByRole("textbox", { name: "Writer document" });
  await editor.focus();
  await editor.press("Enter");
  await editor.press("Enter");
  await editor.press("Enter");
  await expect(editor.locator(".cm-line")).toHaveCount(4);
  await editor.pressSequentially("Opening");
  // Let CodeMirror close its 500 ms history group before the next paragraph.
  await page.waitForTimeout(600);
  await editor.press("End");
  await editor.press("Enter");
  await editor.press("Enter");
  await editor.pressSequentially("Next paragraph");
  const expected = "\n\n\nOpening\n\nNext paragraph";
  await page.waitForTimeout(600);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor.locator(".cm-line")).toHaveCount(5);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor.locator(".cm-line")).toHaveCount(4);
  await page.getByRole("button", { name: "Redo" }).click();
  await expect(editor.locator(".cm-line")).toHaveCount(5);
  await page.getByRole("button", { name: "Redo" }).click();
  await expect(editor.locator(".cm-line")).toHaveCount(6);
  await page.getByRole("button", { name: "Read this now" }).click();
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/observe").length).toBe(1);
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/observe")[0]?.postDataJSON()).toMatchObject(
    {
      document: { content: expected },
    },
  );
});

test("Writer Vim bindings keep editor history and document context separate", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });
  offlineBff.json("POST", "/api/v1/writer/discuss", {
    mode: "proposal",
    text: "Revision",
    candidate: "new",
  });

  await page.goto("/workspace/writer");
  await page.getByRole("switch", { name: "Automatic feedback" }).click();
  const editor = page.getByRole("textbox", { name: "Writer document" });
  const composer = page.getByRole("textbox", { name: "Ask Writer" });
  const settings = page.getByRole("button", { name: "Document key bindings" });
  await expect(page.getByRole("status", { name: /Document Vim mode/ })).toHaveCount(0);
  await editor.focus();
  await editor.pressSequentially("alpha beta");
  await editor.press("Enter");
  await editor.press("Enter");
  await expect(editor.locator(".cm-line")).toHaveCount(3);
  await settings.focus();
  await settings.press("Enter");
  await expect(page.getByRole("menuitemradio", { name: "Standard" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await page.getByRole("menuitemradio", { name: "Vim" }).focus();
  await page.keyboard.press("Enter");
  const mode = page.getByRole("status", { name: /Document Vim mode/ });
  await expect(mode).toHaveText("NORMAL");
  await editor.focus();
  await expect(page.locator(".cm-vimCursorLayer .cm-fat-cursor")).toHaveCount(1);
  await editor.press("Enter");
  await expect(editor.locator(".cm-line")).toHaveCount(3);
  await editor.press("i");
  await expect(mode).toHaveText("INSERT");
  await editor.pressSequentially("gamma");
  await editor.press("Enter");
  await editor.press("Enter");
  await expect(editor.locator(".cm-line")).toHaveCount(5);
  await editor.press("Escape");
  await expect(mode).toHaveText("NORMAL");
  await editor.press("u");
  await editor.press("u");
  await editor.press("u");
  await expect(editor).not.toContainText("gamma");
  await editor.press("Control+r");
  await editor.press("Control+r");
  await editor.press("Control+r");
  await expect(editor).toContainText("gamma");
  await expect(editor.locator(".cm-line")).toHaveCount(5);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor.locator(".cm-line")).toHaveCount(4);
  await page.getByRole("button", { name: "Redo" }).click();
  await expect(editor.locator(".cm-line")).toHaveCount(5);
  await editor.focus();
  await editor.press("Escape");
  await editor.press("g");
  await editor.press("g");
  await editor.press("v");
  await expect(mode).toHaveText("VISUAL");
  await editor.press("e");
  await page.getByRole("button", { name: "Add context" }).click();
  await expect(page.getByRole("menuitem", { name: "Use selected passage" })).toBeVisible();
  await page.getByRole("menuitem", { name: "Use selected passage" }).click();
  await expect(page.getByRole("button", { name: "Remove selected passage" })).toBeVisible();
  await composer.fill("Rewrite the selected passage");
  await composer.press("Enter");
  await expect(page.getByRole("region", { name: "Writer preview" })).toBeVisible();
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/discuss")[0]?.postDataJSON()).toMatchObject(
    { passage: { text: "alpha" } },
  );
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(editor).toContainText("new");
  await editor.focus();
  await editor.press("Escape");
  await editor.press("u");
  await expect(editor).toContainText("alpha");
  await editor.press("Control+r");
  await expect(editor).toContainText("new");
  await settings.focus();
  await settings.press("Enter");
  await page.getByRole("menuitemradio", { name: "Standard" }).focus();
  await page.keyboard.press("Enter");
  await expect(mode).toHaveCount(0);
  await editor.focus();
  await editor.pressSequentially("z");
  await expect(editor).toContainText("z");
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(editor).not.toContainText("z");
  await page.getByRole("button", { name: "Remove selected passage" }).click();
  await composer.fill("A question");
  await composer.press("Enter");
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/discuss").length).toBe(2);
});

test("Writer Vim visual block edits all selected rows with native delete and change history", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });
  await page.goto("/workspace/writer");
  await page.getByRole("switch", { name: "Automatic feedback" }).click();
  const editor = page.getByRole("textbox", { name: "Writer document" });
  await editor.fill("abcde\nfghij\nklmno");
  await page.getByRole("button", { name: "Document key bindings" }).click();
  await page.getByRole("menuitemradio", { name: "Vim" }).click();
  const mode = page.getByRole("status", { name: /Document Vim mode/ });
  await editor.focus();
  await editor.press("g");
  await editor.press("g");
  await editor.press("0");
  await editor.press("Control+v");
  await editor.press("j");
  await editor.press("j");
  await editor.press("l");
  await expect(mode).toHaveText("VISUAL BLOCK");
  await expect.poll(() => page.locator(".cm-selectionBackground").count()).toBeGreaterThan(1);
  await editor.press("d");
  await expect(editor).toHaveText("cdehijmno");
  await editor.press("u");
  await expect(editor).toHaveText("abcdefghijklmno");
  await editor.press("Control+r");
  await expect(editor).toHaveText("cdehijmno");
  await editor.press("g");
  await editor.press("g");
  await editor.press("0");
  await editor.press("Control+v");
  await editor.press("j");
  await editor.press("j");
  await expect(mode).toHaveText("VISUAL BLOCK");
  await editor.press("c");
  await expect(mode).toHaveText("INSERT");
  await editor.pressSequentially("X");
  await editor.press("Escape");
  await expect(editor).toHaveText("XdeXijXno");
  await editor.press("u");
  await expect(editor).toHaveText("deijno");
  await editor.press("u");
  await expect(editor).toHaveText("cdehijmno");
  await editor.press("Control+r");
  await editor.press("Control+r");
  await expect(editor).toHaveText("XdeXijXno");
  await editor.press("g");
  await editor.press("g");
  await editor.press("0");
  await editor.press("Control+v");
  await editor.press("j");
  await editor.press("j");
  await expect.poll(() => page.locator(".cm-selectionBackground").count()).toBeGreaterThan(1);
  await page.getByRole("button", { name: "Document key bindings" }).click();
  await page.getByRole("menuitemradio", { name: "Standard" }).click();
  await expect(mode).toHaveCount(0);
  await expect.poll(() => page.locator(".cm-selectionBackground").count()).toBeGreaterThan(1);
  await expect(editor).toHaveText("XdeXijXno");
});

test("Writer Vim visual line context works while block context is rejected", async ({
  offlineBff,
  page,
}) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "offline-writer",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  offlineBff.json("GET", "/api/v1/runtime", { experimentalWriter: true });
  offlineBff.json("GET", "/api/v1/settings/runtime", { modelsSupported: false, models: [] });
  offlineBff.json("GET", "/api/v1/storage/health", { status: "healthy" });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  offlineBff.json("POST", "/api/v1/writer/observe", { status: "silent" });
  offlineBff.json("POST", "/api/v1/writer/discuss", { mode: "reply", text: "Noted" });
  await page.goto("/workspace/writer");
  await page.getByRole("switch", { name: "Automatic feedback" }).click();
  const editor = page.getByRole("textbox", { name: "Writer document" });
  await editor.fill("first line\nsecond line\nthird line");
  const settings = page.getByRole("button", { name: "Document key bindings" });
  await settings.click();
  await page.getByRole("menuitemradio", { name: "Vim" }).click();
  const mode = page.getByRole("status", { name: /Document Vim mode/ });
  await editor.focus();
  await editor.press("Control+b");
  await editor.press("Control+i");
  await expect(editor).toContainText("first line");
  await expect(editor).not.toContainText("**");
  await editor.press("Control+Home");
  await editor.press("Control+v");
  await expect(mode).toHaveText("VISUAL BLOCK");
  await editor.press("j");
  const context = page.getByRole("button", { name: "Add context" });
  await context.click();
  await expect(page.getByRole("menuitem", { name: "Use selected passage" })).toHaveCount(0);
  await expect(page.getByText(/Select one contiguous passage/)).toBeVisible();
  await page.keyboard.press("Escape");
  await editor.focus();
  await editor.press("Escape");
  await editor.press("g");
  await editor.press("g");
  await editor.press("V");
  await expect(mode).toHaveText("VISUAL LINE");
  await context.click();
  await page.getByRole("menuitem", { name: "Use selected passage" }).click();
  await expect(page.getByText("Passage: first line", { exact: false })).toBeVisible();
  const composer = page.getByRole("textbox", { name: "Ask Writer" });
  await composer.fill("Review this line");
  await composer.press("Enter");
  await expect.poll(() => offlineBff.requestsFor("POST", "/api/v1/writer/discuss").length).toBe(1);
  expect(offlineBff.requestsFor("POST", "/api/v1/writer/discuss")[0]?.postDataJSON()).toMatchObject(
    { passage: { text: "first line" } },
  );
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
    await expect(page.getByText("What would you like to write?")).toBeVisible();
    await editor.focus();
    const afterDelete = await emptyCaretGeometry(editor);
    expect(Math.abs(afterDelete.cursorTop - afterDelete.placeholderTop)).toBeLessThan(1);

    await page.getByRole("button", { name: "Undo" }).click();
    await expect(editor).toHaveText("x");
    await page.getByRole("button", { name: "Undo" }).click();
    await expect(page.getByText("What would you like to write?")).toBeVisible();
    await editor.focus();
  }
});
