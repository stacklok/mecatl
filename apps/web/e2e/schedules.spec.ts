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

test("schedule detail shows responsive history and a read-only transcript", async ({
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
        progressAt: "2026-10-06T08:00:02Z",
        scheduleName: "daily-report",
        sessionId: "",
        startedAt: "2026-10-06T08:00:00Z",
        stop: "error",
      },
    ],
  });
  offlineBff.json("GET", "/api/v1/sessions/scheduled-session/transcript", {
    complete: true,
    messages: [
      {
        images: [],
        role: "assistant",
        text: "The report is ready.",
        toolCalls: [{ args: '{"path":"report.txt"}', id: "tool-1", name: "Write" }],
        toolResult: { callId: "tool-1", content: "saved", isError: false },
      },
    ],
    sessionId: "scheduled-session",
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

  const viewTranscript = page.getByRole("button", { name: "View transcript" });
  await viewTranscript.click();
  const dialog = page.getByRole("dialog", { name: /daily-report/ });
  await expect(dialog).toContainText("The report is ready.");
  await expect(dialog).toContainText("Tool: Write");
  await expect(dialog.getByRole("textbox")).toHaveCount(0);
  for (const name of ["Retry", "Fork", "Rename", "Delete", "Send message"]) {
    await expect(dialog.getByRole("button", { name })).toHaveCount(0);
  }
  await dialog.getByRole("button", { name: "Close" }).click();
  await expect(viewTranscript).toBeFocused();
  expect(
    offlineBff.requestsFor("GET", "/api/v1/sessions/scheduled-session/transcript"),
  ).toHaveLength(1);
  expect(offlineBff.requestsFor("POST", "/api/v1/sessions/scheduled-session/runs")).toHaveLength(0);
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
  await page.getByRole("button", { name: "Schedule task" }).click();
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
    const statusLabel = mobile
      ? page.getByText(status, { exact: true })
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
  await expectExistingStatus("paused");
  await openActions("mutable-report");
  await page.getByRole("menuitem", { name: "Resume" }).click();
  await expectExistingStatus("scheduled");

  failNextAction = true;
  await openActions("mutable-report");
  await page.getByRole("menuitem", { name: "Pause" }).click();
  await expect(page.getByText("Scheduler conflict.", { exact: true })).toBeVisible();
  await expectExistingStatus("scheduled");

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
