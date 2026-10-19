// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

// Pins for #2299's dialog changes: what the create and edit dialogs must keep
// while the one-shot input splits into date and time and the submit button
// learns to disable itself. Each test passes before and after the change.

import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { type FakeRequest, mountSchedules, resetApi, schedule } from "./schedules-test-kit";

afterEach(() => {
  cleanup();
  resetApi();
});

function posts(requests: FakeRequest[], method: string, path: string) {
  return requests.filter((request) => request.method === method && request.path === path);
}

/** Sets the one-shot wall clock through whichever inputs the dialog offers. */
function setRunAt(dialog: HTMLElement, date: string, time: string) {
  const combined = dialog.querySelector<HTMLInputElement>('input[type="datetime-local"]');
  if (combined) {
    fireEvent.change(combined, { target: { value: `${date}T${time}` } });
    return;
  }
  const dateInput = dialog.querySelector<HTMLInputElement>('input[type="date"]');
  const timeInput = dialog.querySelector<HTMLInputElement>('input[type="time"]');
  if (!dateInput || !timeInput) throw new Error("no one-shot inputs");
  fireEvent.change(dateInput, { target: { value: date } });
  fireEvent.change(timeInput, { target: { value: time } });
}

describe("the create dialog keeps its contract", () => {
  it("creates a recurring task in the browser's time zone and closes", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await user.click(
      await screen.findByRole("button", { name: /^(Schedule task|New scheduled task)$/ }),
    );
    const dialog = screen.getByRole("dialog", { name: "Schedule a task" });
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "nightly");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Summarize");
    await user.click(within(dialog).getByRole("button", { name: "Create schedule" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const [created] = posts(requests, "POST", "/api/v1/schedules");
    expect(created?.body).toMatchObject({
      maxFires: 0,
      mode: "plan",
      mutating: false,
      name: "nightly",
      oneShotRetry: false,
      profile: "all",
      prompt: "Summarize",
      trigger: {
        expression: "0 9 * * *",
        kind: "cron",
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      },
    });
  });

  it("sends the chosen one-shot wall clock as its absolute instant", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await user.click(
      await screen.findByRole("button", { name: /^(Schedule task|New scheduled task)$/ }),
    );
    const dialog = screen.getByRole("dialog", { name: "Schedule a task" });
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "launch");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Remind me");
    await user.click(within(dialog).getByRole("tab", { name: "Run once" }));
    setRunAt(dialog, "2030-01-02", "15:30");
    expect(within(dialog).getByText(/^Saves as /)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Create schedule" }));

    await waitFor(() => expect(posts(requests, "POST", "/api/v1/schedules")).toHaveLength(1));
    expect(posts(requests, "POST", "/api/v1/schedules")[0]?.body).toMatchObject({
      name: "launch",
      trigger: { at: new Date("2030-01-02T15:30").toISOString(), kind: "once" },
    });
  });

  it("keeps the dialog open and shows the refusal when the BFF rejects the task", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules", [], (request) =>
      request.method === "POST"
        ? new Response(
            JSON.stringify({
              code: "",
              detail: "cron too frequent",
              instance: "",
              status: 400,
              title: "",
              type: "",
            }),
            { headers: { "content-type": "application/problem+json" }, status: 400 },
          )
        : undefined,
    );
    await user.click(
      await screen.findByRole("button", { name: /^(Schedule task|New scheduled task)$/ }),
    );
    const dialog = screen.getByRole("dialog", { name: "Schedule a task" });
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "nightly");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Summarize");
    await user.click(within(dialog).getByRole("button", { name: "Create schedule" }));

    expect(await within(dialog).findByText("cron too frequent")).toBeTruthy();
    expect(screen.getByRole("dialog", { name: "Schedule a task" })).toBe(dialog);
  });

  it("marks the name and prompt as required", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await user.click(
      await screen.findByRole("button", { name: /^(Schedule task|New scheduled task)$/ }),
    );
    const dialog = screen.getByRole("dialog", { name: "Schedule a task" });
    for (const name of ["Name", "Prompt"]) {
      expect(within(dialog).getByRole("textbox", { name }).hasAttribute("required")).toBe(true);
    }
  });
});

describe("the edit dialog keeps its contract", () => {
  const stored = schedule({
    maxFires: 7,
    mode: "default",
    mutating: true,
    name: "Alpha",
    oneShotMaxRetries: 0,
    profile: "noFilesystem",
    prompt: "Original",
    trigger: { expression: "0 8 * * *", kind: "cron", timezone: "Europe/Rome" },
  });

  it("locks the name and trigger and sends the stored trigger and limits with the new prompt", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules([stored], "/workspace/schedules/Alpha");
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Edit" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit scheduled task" });
    expect(within(dialog).getByRole<HTMLInputElement>("textbox", { name: "Name" }).disabled).toBe(
      true,
    );
    expect(within(dialog).getByText(/trigger of an existing task can't be changed/)).toBeTruthy();
    const prompt = within(dialog).getByRole("textbox", { name: "Prompt" });
    await user.clear(prompt);
    await user.type(prompt, "Updated");
    await user.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(posts(requests, "PUT", "/api/v1/schedules/Alpha")).toHaveLength(1));
    const body = posts(requests, "PUT", "/api/v1/schedules/Alpha")[0]?.body as Record<
      string,
      unknown
    >;
    expect(body).not.toHaveProperty("name");
    expect(body).toMatchObject({
      maxFires: 7,
      mode: "default",
      mutating: true,
      profile: "noFilesystem",
      prompt: "Updated",
      trigger: { expression: "0 8 * * *", kind: "cron", timezone: "Europe/Rome" },
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("lets a stored one-shot be saved unchanged", async () => {
    const user = userEvent.setup();
    const at = new Date(Date.now() + 86_400_000).toISOString();
    const { requests } = mountSchedules(
      [schedule({ name: "Alpha", trigger: { at, kind: "once" } })],
      "/workspace/schedules/Alpha",
    );
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Edit" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit scheduled task" });
    await user.click(within(dialog).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(posts(requests, "PUT", "/api/v1/schedules/Alpha")).toHaveLength(1));
    expect(posts(requests, "PUT", "/api/v1/schedules/Alpha")[0]?.body).toMatchObject({
      trigger: { at, kind: "once" },
    });
  });
});

// Pins for the #2370 review (independent Date and Time, the visible submit
// problem): what the one-shot inputs already do and must keep doing.
describe("the one-shot inputs keep their contract", () => {
  async function openOnce(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByRole("button", { name: "New scheduled task" }));
    const dialog = screen.getByRole("dialog", { name: "Schedule a task" });
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "launch");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Remind me");
    await user.click(within(dialog).getByRole("tab", { name: "Run once" }));
    return dialog;
  }

  it("keeps the time when the date is changed to another day", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openOnce(user);
    setRunAt(dialog, "2030-01-02", "15:30");
    const date = within(dialog).getByLabelText<HTMLInputElement>("Date");
    fireEvent.change(date, { target: { value: "2030-02-03" } });
    expect(within(dialog).getByLabelText<HTMLInputElement>("Time").value).toBe("15:30");
    await user.click(within(dialog).getByRole("button", { name: "Create schedule" }));
    await waitFor(() => expect(posts(requests, "POST", "/api/v1/schedules")).toHaveLength(1));
    expect(posts(requests, "POST", "/api/v1/schedules")[0]?.body).toMatchObject({
      trigger: { at: new Date("2030-02-03T15:30").toISOString(), kind: "once" },
    });
  });

  it("fills the date and time from a one-shot phrase", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openOnce(user);
    fireEvent.change(within(dialog).getByRole("textbox", { name: "Describe the schedule" }), {
      target: { value: "in 2 hours" },
    });
    const date = within(dialog).getByLabelText<HTMLInputElement>("Date");
    const time = within(dialog).getByLabelText<HTMLInputElement>("Time");
    expect(date.value).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(time.value).toMatch(/^\d{2}:\d{2}$/);
  });

  it("shows a stored one-shot's date and time, locked, in the edit dialog", async () => {
    const user = userEvent.setup();
    const at = "2030-01-02T14:30:00.000Z";
    mountSchedules(
      [schedule({ name: "Alpha", trigger: { at, kind: "once" } })],
      "/workspace/schedules/Alpha",
    );
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Edit" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit scheduled task" });
    const date = within(dialog).getByLabelText<HTMLInputElement>("Date");
    const time = within(dialog).getByLabelText<HTMLInputElement>("Time");
    const local = new Date(new Date(at).getTime() - new Date(at).getTimezoneOffset() * 60_000)
      .toISOString()
      .slice(0, 16);
    expect([date.value, time.value]).toEqual([local.slice(0, 10), local.slice(11, 16)]);
    expect([date.disabled, time.disabled]).toEqual([true, true]);
  });

  it("names no problem and describes nothing once the form could be sent", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openOnce(user);
    setRunAt(dialog, "2030-01-02", "15:30");
    const create = within(dialog).getByRole("button", { name: "Create schedule" });
    expect(create.hasAttribute("aria-describedby")).toBe(false);
    expect(within(dialog).queryByText(/is required\.|not a valid date\./)).toBeNull();
  });
});
