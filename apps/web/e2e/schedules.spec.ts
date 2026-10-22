// SPDX-License-Identifier: Apache-2.0

import { expect, type OfflineBff, test } from "./fixtures";

type ScheduleFixture = {
  enabled: boolean;
  fireCount: number;
  lastFireAt: string;
  lastFireSessionId: string;
  maxFires: number;
  mode: "acceptEdits" | "default" | "plan";
  modelId: string;
  mutating: boolean;
  name: string;
  nextFireAt: string;
  oneShotMaxRetries: number;
  oneShotRetry: boolean;
  owner: string;
  profile: "all" | "noFilesystem";
  prompt: string;
  providerId: string;
  status: "claimed" | "completed" | "paused" | "running" | "scheduled";
  trigger: { expression: string; kind: "cron"; timezone: string } | { at: string; kind: "once" };
};

function schedule(overrides: Partial<ScheduleFixture> = {}): ScheduleFixture {
  return {
    enabled: true,
    fireCount: 0,
    lastFireAt: "",
    lastFireSessionId: "",
    maxFires: 0,
    mode: "plan",
    modelId: "",
    mutating: false,
    name: "daily-report",
    nextFireAt: "2026-10-08T08:00:00Z",
    oneShotMaxRetries: 0,
    oneShotRetry: false,
    owner: "operator",
    profile: "all",
    prompt: "Summarize project activity",
    providerId: "",
    status: "scheduled",
    trigger: { expression: "0 8 * * *", kind: "cron", timezone: "Europe/Rome" },
    ...overrides,
  };
}

function stubWorkspace(offlineBff: OfflineBff) {
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
    capabilities: { posture: "managed", scheduling: true },
    connection: "online",
  });
}

test("the schedule time field is wide enough to show its meridiem on a phone", async ({
  offlineBff,
  page,
}) => {
  stubWorkspace(offlineBff);
  offlineBff.json("GET", "/api/v1/schedules", { items: [], reason: "", supported: true });
  // The field uses larger text below the md breakpoint. At 120px Chrome cut
  // "11:59 PM" to "11:59 P" there; 136px was the narrowest that showed it whole.
  await page.setViewportSize({ height: 800, width: 390 });
  await page.goto("/workspace/schedules");
  await page.getByRole("button", { name: "New scheduled task" }).first().click();
  const time = page.locator("#schedule-time");
  await expect(time).toBeVisible();
  const box = await time.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThanOrEqual(136);
});

test("schedule inventory uses the desktop table and compact mobile list", async ({
  offlineBff,
  page,
}, testInfo) => {
  stubWorkspace(offlineBff);
  offlineBff.json("GET", "/api/v1/schedules", {
    items: [
      schedule({ fireCount: 2, lastFireAt: "2026-10-07T08:00:00Z", name: "Daily report" }),
      schedule({
        enabled: false,
        fireCount: 1,
        lastFireAt: "2026-10-06T08:00:00Z",
        maxFires: 1,
        name: "Finished once",
        nextFireAt: "",
        prompt: "Send the launch reminder",
        status: "completed",
        trigger: { at: "2026-10-06T08:00:00Z", kind: "once" },
      }),
    ],
    reason: "",
    supported: true,
  });

  await page.goto("/workspace/schedules");
  await expect(page.getByRole("heading", { name: "Scheduled" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "Filter scheduled tasks" })).toBeVisible();

  const table = page.getByRole("table");
  const mobileList = page.locator("[data-schedule-mobile-list]");
  if (testInfo.project.name === "chromium-mobile") {
    await expect(table).toBeHidden();
    await expect(mobileList.getByRole("link", { name: /Daily report/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /Actions for/ })).toHaveCount(0);
  } else {
    await expect(table).toBeVisible();
    await expect(table.getByRole("columnheader", { name: /Next run/ })).toBeVisible();
    await expect(page.getByRole("button", { name: "Actions for Daily report" })).toBeVisible();
    await expect(mobileList).toBeHidden();
  }

  await page.keyboard.press("/");
  await expect(page.getByRole("textbox", { name: "Filter scheduled tasks" })).toBeFocused();
  await page.keyboard.type("launch reminder");
  await expect(page.getByText("Daily report", { exact: true })).toHaveCount(0);
  const filteredRow =
    testInfo.project.name === "chromium-mobile"
      ? mobileList.getByText("Finished once", { exact: true })
      : table.getByText("Finished once", { exact: true });
  await expect(filteredRow).toBeVisible();
});

test("schedule detail opens each fire's saved transcript in a read-only dialog", async ({
  offlineBff,
  page,
}, testInfo) => {
  stubWorkspace(offlineBff);
  offlineBff.json("GET", "/api/v1/schedules", {
    items: [schedule({ fireCount: 3, lastFireAt: "2026-10-08T08:00:00Z" })],
    reason: "",
    supported: true,
  });
  offlineBff.json("GET", "/api/v1/schedules/daily-report/fires", {
    items: [
      {
        deadline: "2026-10-08T08:05:00Z",
        error: "",
        firedAt: "2026-10-08T08:00:00Z",
        id: "fire-live",
        inFlight: true,
        outcome: "in_flight",
        progressAt: "",
        scheduleName: "daily-report",
        sessionId: "",
        startedAt: "2026-10-08T08:00:01Z",
        stop: "",
      },
      {
        deadline: "2026-10-07T08:05:00Z",
        error: "",
        firedAt: "2026-10-07T08:00:00Z",
        id: "fire-1",
        inFlight: false,
        outcome: "completed",
        progressAt: "2026-10-07T08:00:02Z",
        scheduleName: "daily-report",
        sessionId: "scheduled-session",
        startedAt: "2026-10-07T08:00:00Z",
        stop: "end_turn",
      },
      {
        deadline: "2026-10-06T08:05:00Z",
        error: "Provider unavailable",
        firedAt: "2026-10-06T08:00:00Z",
        id: "fire-failed",
        inFlight: false,
        outcome: "failed",
        progressAt: "2026-10-06T08:00:02Z",
        scheduleName: "daily-report",
        sessionId: "",
        startedAt: "2026-10-06T08:00:00Z",
        stop: "error",
      },
    ],
  });

  await page.goto("/workspace/schedules/daily-report");
  await expect(page.getByRole("heading", { name: "daily-report" })).toBeVisible();
  const historyTable = page.getByRole("table");
  const mobileHistory = page.locator("[data-schedule-fire-mobile-list]");
  const visibleHistory = testInfo.project.name === "chromium-mobile" ? mobileHistory : historyTable;
  if (testInfo.project.name === "chromium-mobile") {
    await expect(historyTable).toBeHidden();
  } else {
    await expect(historyTable).toBeVisible();
  }
  for (const outcome of ["Completed", "In flight", "Failed"]) {
    await expect(visibleHistory.getByText(outcome, { exact: true })).toBeVisible();
  }
  await expect(visibleHistory.getByText("Provider unavailable", { exact: true })).toBeVisible();

  offlineBff.json("GET", "/api/v1/sessions", {
    complete: true,
    items: [
      {
        capabilities: {
          copyId: true,
          copyIdReason: "",
          delete: false,
          deleteReason: "",
          fork: false,
          forkReason: "",
          inspect: true,
          inspectReason: "",
          publicChat: false,
          publicChatReason: "inspect_only_kind",
          rename: false,
          renameReason: "",
          viewTranscript: true,
          viewTranscriptReason: "",
        },
        createdAt: "2026-10-07T08:00:00Z",
        debugTargetSessionId: "",
        id: "scheduled-session",
        kind: "scheduled",
        modelId: "",
        state: "idle",
        title: "daily-report · Oct 7",
        titleProvenance: "operator",
        titleRevision: "1",
        turns: 1,
        updatedAt: "2026-10-07T08:00:02Z",
      },
    ],
  });
  offlineBff.json("GET", "/api/v1/sessions/scheduled-session", {
    capabilities: {
      audio: false,
      builtInShell: false,
      executionFiles: false,
      image: false,
      manualCompaction: false,
      modelSelection: false,
    },
    id: "scheduled-session",
    kind: "scheduled",
    mode: "plan",
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  });
  offlineBff.json("GET", "/api/v1/sessions/scheduled-session/transcript", {
    complete: true,
    messages: [
      { images: [], role: "user", text: "Summarize project activity", toolCalls: [] },
      { images: [], role: "assistant", text: "Two PRs merged yesterday.", toolCalls: [] },
    ],
    sessionId: "scheduled-session",
  });

  // Only the fire with a session offers its transcript, and it stays on this page.
  const viewTranscript = visibleHistory.getByRole("button", { name: "View transcript" });
  await expect(viewTranscript).toHaveCount(1);
  await viewTranscript.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Two PRs merged yesterday.", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Saved transcript · scheduled-session")).toBeVisible();
  expect(new URL(page.url()).pathname).toBe("/workspace/schedules/daily-report");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(viewTranscript).toBeFocused();
});

test("schedule mutations preserve backend-owned fields and surface failures", async ({
  offlineBff,
  page,
}, testInfo) => {
  stubWorkspace(offlineBff);
  let existing = schedule({
    fireCount: 2,
    lastFireAt: "2026-10-07T08:00:00Z",
    maxFires: 7,
    mode: "default",
    modelId: "model-a",
    mutating: true,
    name: "mutable-report",
    profile: "noFilesystem",
    prompt: "Original prompt",
    providerId: "provider-a",
  });
  let created: ScheduleFixture | undefined;
  let failNextAction = false;
  const response = (body: unknown, status = 200) => ({
    body: JSON.stringify(body),
    contentType: "application/json",
    status,
  });

  offlineBff.on("GET", "/api/v1/schedules", () =>
    response({ items: created ? [existing, created] : [existing], reason: "", supported: true }),
  );
  offlineBff.on("POST", "/api/v1/schedules", (request) => {
    const body = request.postDataJSON() as ScheduleFixture;
    created = schedule({
      ...body,
      fireCount: 0,
      lastFireAt: "",
      lastFireSessionId: "",
      name: body.name,
      nextFireAt: "2026-10-09T09:00:00Z",
      owner: "operator",
      status: "scheduled",
    });
    return response(created, 201);
  });
  offlineBff.on("PUT", "/api/v1/schedules/mutable-report", (request) => {
    const body = request.postDataJSON() as Partial<ScheduleFixture>;
    existing = { ...existing, ...body, name: existing.name };
    return response(existing);
  });
  offlineBff.on("POST", "/api/v1/schedules/mutable-report/actions", (request) => {
    if (failNextAction) {
      failNextAction = false;
      return {
        body: JSON.stringify({ detail: "Scheduler conflict.", status: 409, title: "Conflict" }),
        contentType: "application/problem+json",
        status: 409,
      };
    }
    const action = (request.postDataJSON() as { action: string }).action;
    existing = {
      ...existing,
      enabled: action === "resume" ? true : action === "pause" ? false : existing.enabled,
      status: action === "resume" ? "scheduled" : action === "pause" ? "paused" : existing.status,
    };
    return { status: 204 };
  });
  offlineBff.on("DELETE", "/api/v1/schedules/new-report", () => {
    created = undefined;
    return { status: 204 };
  });
  offlineBff.json("GET", "/api/v1/schedules/mutable-report/fires", { items: [] });
  offlineBff.json("GET", "/api/v1/schedules/new-report/fires", { items: [] });

  await page.goto("/workspace/schedules");
  const mobile = testInfo.project.name === "chromium-mobile";
  await page.getByRole("button", { name: "New scheduled task" }).click();
  const createDialog = page.getByRole("dialog", { name: "Schedule a task" });
  await createDialog.getByRole("textbox", { name: "Name" }).fill("new-report");
  await createDialog.getByRole("textbox", { name: "Prompt" }).fill("Create from the browser");
  await createDialog.getByRole("button", { name: "Create schedule" }).click();
  const createdName = mobile
    ? page.locator("[data-schedule-mobile-list]").getByText("new-report", { exact: true })
    : page.getByRole("table").getByRole("link", { name: "new-report" });
  await expect(createdName).toBeVisible();

  const createBody = offlineBff.requestsFor("POST", "/api/v1/schedules").at(-1)?.postDataJSON() as {
    maxFires: number;
    name: string;
    oneShotRetry: boolean;
    prompt: string;
    trigger: { expression: string; kind: string; timezone: string };
  };
  expect(createBody).toMatchObject({
    maxFires: 0,
    name: "new-report",
    oneShotRetry: false,
    prompt: "Create from the browser",
    trigger: { expression: "0 9 * * *", kind: "cron" },
  });
  expect(createBody.trigger.timezone).not.toBe("");

  if (mobile) {
    await page.getByRole("link", { name: /mutable-report/ }).click();
    await expect(page.getByRole("heading", { name: "mutable-report" })).toBeVisible();
  }
  const openActions = async (name: string) => {
    await page.getByRole("button", { name: `Actions for ${name}` }).click();
  };
  const expectExistingStatus = async (status: string) => {
    // The Title-case label also names the "Scheduled" navigation link, so look in the page.
    const statusLabel = mobile
      ? page.getByRole("main").getByText(status, { exact: true })
      : page
          .getByRole("row")
          .filter({ hasText: "mutable-report" })
          .getByText(status, { exact: true });
    await expect(statusLabel).toBeVisible();
  };

  await openActions("mutable-report");
  await page.getByRole("menuitem", { name: "Edit" }).click();
  const editDialog = page.getByRole("dialog", { name: "Edit scheduled task" });
  await expect(editDialog.getByRole("textbox", { name: "Name" })).toBeDisabled();
  await editDialog.getByRole("textbox", { name: "Prompt" }).fill("Updated prompt");
  await editDialog.getByRole("button", { name: "Save changes" }).click();
  const updatedPrompt = mobile
    ? page.locator("p").filter({ hasText: /^Updated prompt$/ })
    : page.getByRole("row").filter({ hasText: "mutable-report" }).getByText("Updated prompt", {
        exact: true,
      });
  await expect(updatedPrompt).toBeVisible();

  const updateBody = offlineBff
    .requestsFor("PUT", "/api/v1/schedules/mutable-report")
    .at(-1)
    ?.postDataJSON() as {
    maxFires: number;
    oneShotMaxRetries: number;
    oneShotRetry: boolean;
    profile: string;
    trigger: unknown;
  };
  expect(updateBody).toMatchObject({
    maxFires: 7,
    oneShotMaxRetries: 0,
    oneShotRetry: false,
    profile: "noFilesystem",
    trigger: { expression: "0 8 * * *", kind: "cron", timezone: "Europe/Rome" },
  });

  await openActions("mutable-report");
  await page.getByRole("menuitem", { name: "Pause" }).click();
  await expectExistingStatus("Paused");
  await openActions("mutable-report");
  await page.getByRole("menuitem", { name: "Resume" }).click();
  await expectExistingStatus("Scheduled");

  failNextAction = true;
  await openActions("mutable-report");
  await page.getByRole("menuitem", { name: "Pause" }).click();
  await expect(page.getByText("Scheduler conflict.", { exact: true })).toBeVisible();
  await expectExistingStatus("Scheduled");

  if (mobile) {
    await page.getByRole("link", { name: "Back to scheduled" }).click();
    await page.getByRole("link", { name: /new-report/ }).click();
    await expect(page.getByRole("heading", { name: "new-report" })).toBeVisible();
  }
  const deleteActionTrigger = page.getByRole("button", { name: "Actions for new-report" });
  await openActions("new-report");
  await page.getByRole("menuitem", { name: "Delete" }).click();
  let deleteDialog = page.getByRole("alertdialog", { name: /Delete “new-report”/ });
  await deleteDialog.getByRole("button", { name: "Cancel" }).click();
  await expect(deleteActionTrigger).toBeFocused();

  await openActions("new-report");
  await page.getByRole("menuitem", { name: "Delete" }).click();
  deleteDialog = page.getByRole("alertdialog", { name: /Delete “new-report”/ });
  await deleteDialog.getByRole("button", { name: "Delete" }).click();
  await expect(page.getByText("new-report", { exact: true })).toHaveCount(0);

  const actions = offlineBff
    .requestsFor("POST", "/api/v1/schedules/mutable-report/actions")
    .map((request) => (request.postDataJSON() as { action: string }).action);
  expect(actions).toEqual(["pause", "resume", "pause"]);
  expect(offlineBff.requestsFor("DELETE", "/api/v1/schedules/new-report")).toHaveLength(1);
  expect(offlineBff.requestsFor("GET", "/api/v1/schedules").length).toBeGreaterThan(4);
});

test("a schedule row opens on a plain click, not on a modified click or a text selection", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(
    testInfo.project.name === "chromium-mobile",
    "The clickable row is the desktop table's.",
  );
  stubWorkspace(offlineBff);
  offlineBff.json("GET", "/api/v1/schedules", {
    items: [schedule({ prompt: "Summarize project activity for the weekly review" })],
    reason: "",
    supported: true,
  });
  offlineBff.json("GET", "/api/v1/schedules/daily-report/fires", { items: [] });

  await page.goto("/workspace/schedules");
  const prompt = page
    .getByRole("table")
    .getByText("Summarize project activity for the weekly review");
  await expect(prompt).toBeVisible();

  // Dragging across the prompt selects it; the click that ends the drag stays on the list.
  const box = await prompt.boundingBox();
  if (!box) throw new Error("no prompt box");
  await page.mouse.move(box.x + 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width - 2, box.y + box.height / 2, { steps: 8 });
  await page.mouse.up();
  expect(await page.evaluate(() => window.getSelection()?.toString() ?? "")).not.toBe("");
  await page.waitForTimeout(200);
  await expect(page).toHaveURL(/\/workspace\/schedules$/);

  // A modified click on the row body is the browser's, not the row's.
  await page.evaluate(() => window.getSelection()?.removeAllRanges());
  await prompt.click({ modifiers: ["ControlOrMeta"] });
  await prompt.click({ modifiers: ["Shift"] });
  await page.evaluate(() => window.getSelection()?.removeAllRanges());
  await page.waitForTimeout(200);
  await expect(page).toHaveURL(/\/workspace\/schedules$/);

  await prompt.click();
  await expect(page).toHaveURL(/\/workspace\/schedules\/daily-report$/);
});

test("the one-shot date and time stay independent while the date is edited", async ({
  isMobile,
  offlineBff,
  page,
}) => {
  stubWorkspace(offlineBff);
  offlineBff.json("GET", "/api/v1/schedules", { items: [], reason: "", supported: true });
  offlineBff.on("POST", "/api/v1/schedules", (request) => ({
    body: JSON.stringify(schedule(request.postDataJSON() as Partial<ScheduleFixture>)),
    contentType: "application/json",
    status: 201,
  }));

  await page.goto("/workspace/schedules");
  await page.getByRole("button", { name: "New scheduled task" }).first().click();
  const dialog = page.getByRole("dialog", { name: "Schedule a task" });
  await dialog.getByRole("textbox", { name: "Name" }).fill("launch");
  await dialog.getByRole("textbox", { name: "Prompt" }).fill("Remind me");
  await dialog.getByRole("tab", { name: "Run once" }).click();
  const date = dialog.getByLabel("Date", { exact: true });
  const time = dialog.getByLabel("Time", { exact: true });
  const create = dialog.getByRole("button", { name: "Create schedule" });

  // A time chosen before the date is kept, and the reason Create is disabled shows.
  await time.fill("15:30");
  await expect(time).toHaveValue("15:30");
  await expect(create).toBeDisabled();
  await expect(create).toHaveAccessibleDescription("Run time is required.");
  await date.fill("2030-01-02");
  await expect(time).toHaveValue("15:30");
  await expect(create).toBeEnabled();

  // Mobile uses a native date picker, not the desktop's keyboard-editable segments.
  if (isMobile) {
    await date.clear();
  } else {
    await date.focus();
    await page.keyboard.press("Backspace");
  }
  await expect(date).toHaveValue("");
  await expect(time).toHaveValue("15:30");
  await expect(create).toBeDisabled();
  await expect(create).toHaveAccessibleDescription("Run time is required.");

  await date.fill("2030-02-03");
  await expect(time).toHaveValue("15:30");
  await create.click();
  await expect(dialog).toBeHidden();
  const body = offlineBff.requestsFor("POST", "/api/v1/schedules").at(-1)?.postDataJSON() as {
    trigger: { at: string; kind: string };
  };
  const expected = await page.evaluate(() => new Date("2030-02-03T15:30").toISOString());
  expect(body.trigger).toEqual({ at: expected, kind: "once" });
});
