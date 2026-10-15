// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

// #2299: the create and edit dialogs in the prototype's shape — separate date
// and time inputs, a submit button gated by scheduleFormProblem, and a toast
// once the task is saved.

import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { mountSchedules, problem, resetApi, schedule } from "./schedules-test-kit";

afterEach(() => {
  cleanup();
  resetApi();
});

async function openCreate(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "New scheduled task" }));
  return screen.getByRole("dialog", { name: "Schedule a task" });
}

describe("the schedule dialog", () => {
  it("keeps Create disabled until the name and prompt are filled", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openCreate(user);
    const create = within(dialog).getByRole<HTMLButtonElement>("button", {
      name: "Create schedule",
    });
    expect(create.disabled).toBe(true);
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "nightly");
    expect(create.disabled).toBe(true);
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "   ");
    expect(create.disabled).toBe(true);
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Summarize");
    expect(create.disabled).toBe(false);
  });

  it("sends nothing when Enter is pressed in an incomplete form", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "nightly{Enter}");
    fireEvent.submit(dialog.querySelector("form") as HTMLFormElement);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(requests.filter((request) => request.method === "POST")).toEqual([]);
    expect(screen.getByRole("dialog", { name: "Schedule a task" })).toBe(dialog);
  });

  it("splits the one-shot run time into a Date and a Time input under Run at", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "launch");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Remind me");
    await user.click(within(dialog).getByRole("tab", { name: "Run once" }));

    const group = within(dialog).getByRole("group", { name: "Run at" });
    const date = within(group).getByLabelText<HTMLInputElement>("Date");
    const time = within(group).getByLabelText<HTMLInputElement>("Time");
    expect([date.type, time.type]).toEqual(["date", "time"]);
    expect(dialog.querySelector('input[type="datetime-local"]')).toBeNull();

    const create = within(dialog).getByRole<HTMLButtonElement>("button", {
      name: "Create schedule",
    });
    // No date yet, so there is no run time: a time alone cannot enable Create.
    fireEvent.change(time, { target: { value: "15:30" } });
    expect(create.disabled).toBe(true);
    // A date on its own runs at 9:00.
    fireEvent.change(date, { target: { value: "2030-01-02" } });
    expect(time.value).toBe("09:00");
    expect(create.disabled).toBe(false);
    // A cleared time segment keeps the last time rather than falling back to 9:00.
    fireEvent.change(time, { target: { value: "15:30" } });
    fireEvent.change(time, { target: { value: "" } });
    expect(time.value).toBe("15:30");
    await user.click(create);

    await waitFor(() =>
      expect(requests.filter((request) => request.method === "POST")).toHaveLength(1),
    );
    expect(requests.find((request) => request.method === "POST")?.body).toMatchObject({
      trigger: { at: new Date("2030-01-02T15:30").toISOString(), kind: "once" },
    });
  });

  it("disables Create again when the date is cleared", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "launch");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Remind me");
    await user.click(within(dialog).getByRole("tab", { name: "Run once" }));
    const date = within(dialog).getByLabelText<HTMLInputElement>("Date");
    fireEvent.change(date, { target: { value: "2030-01-02" } });
    const create = within(dialog).getByRole<HTMLButtonElement>("button", {
      name: "Create schedule",
    });
    expect(create.disabled).toBe(false);
    fireEvent.change(date, { target: { value: "" } });
    expect(create.disabled).toBe(true);
  });

  it("is sm:max-w-lg wide, as in the prototype", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openCreate(user);
    expect(dialog.className).toContain("sm:max-w-lg");
  });
});

describe("schedule toasts", () => {
  async function toastText() {
    const toast = await waitFor(() => {
      const node = document.querySelector("[data-sonner-toast]");
      if (!node) throw new Error("no toast yet");
      return node;
    });
    return toast.textContent;
  }

  it("confirms a created task", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "nightly");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Summarize");
    await user.click(within(dialog).getByRole("button", { name: "Create schedule" }));
    expect(await toastText()).toContain("Scheduled task “nightly” created");
  });

  it("confirms an edit saved from the inventory", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Save changes" }));
    expect(await toastText()).toContain("Scheduled task “Alpha” updated");
  });

  it("confirms an edit saved from the detail page", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules/Alpha");
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Save changes" }));
    expect(await toastText()).toContain("Scheduled task “Alpha” updated");
  });

  it("raises no toast when the BFF refuses the task", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules", [], (request) =>
      request.method === "POST" ? problem("cron too frequent", 400) : undefined,
    );
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "nightly");
    await user.type(within(dialog).getByRole("textbox", { name: "Prompt" }), "Summarize");
    await user.click(within(dialog).getByRole("button", { name: "Create schedule" }));
    expect(await within(dialog).findByText("cron too frequent")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(document.querySelector("[data-sonner-toast]")).toBeNull();
  });
});
