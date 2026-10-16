// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the session activity panel's family tabs and roster rows before they
 * become the shadcn Tabs and Toggle primitives. The tablist and its tabs keep
 * their names; the selected tab labels and controls the panel; a click, or
 * the arrow, Home, and End keys, chooses a family and clears the chosen
 * activity, and a click on the selected tab clears it too; Tab enters the
 * tablist on the selected tab and leaves it for the panel's first row; a
 * roster row is pressed while its activity is chosen. Roles, names, and ARIA
 * state only, so these hold on both implementations.
 */

import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DelegationFocus } from "./delegation-card";
import {
  createDelegationFleet,
  type DelegationFleet,
  type ParallelGroupActivity,
  type SubagentActivity,
  type TeamActivity,
} from "./delegation-fleet";
import { SessionActivityContent } from "./delegation-panel";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

const noTrace = { entries: [], omitted: 0 };

const subagent: SubagentActivity = {
  childId: "child-s",
  family: "subagent",
  historyIncomplete: false,
  key: "subagent-s",
  parentCallId: "call-s",
  runId: "run-a",
  sessionId: "session-a",
  startObserved: true,
  state: "finished",
  stop: "error",
  trace: noTrace,
};

const parallel: ParallelGroupActivity = {
  branchCount: 2,
  branches: [
    {
      branchIndex: 0,
      historyIncomplete: false,
      key: "branch-0",
      startObserved: true,
      state: "running",
      trace: noTrace,
    },
    {
      branchIndex: 1,
      historyIncomplete: false,
      key: "branch-1",
      startObserved: true,
      state: "finished",
      trace: noTrace,
    },
  ],
  family: "parallel",
  historyIncomplete: false,
  join: "all",
  key: "parallel-p",
  parentCallId: "call-p",
  runId: "run-a",
  sessionId: "session-a",
  startObserved: true,
  state: "running",
};

const team: TeamActivity = {
  family: "team",
  findings: [],
  historyIncomplete: false,
  key: "team-t",
  members: [
    {
      disposition: "done",
      key: "member-lead",
      lead: true,
      name: "lead",
      state: "finished",
      trace: noTrace,
    },
    {
      disposition: "stopped",
      key: "member-worker",
      name: "worker",
      reason: "budget",
      state: "finished",
      trace: noTrace,
    },
  ],
  parentCallId: "call-t",
  runId: "run-a",
  sessionId: "session-a",
  startObserved: true,
  state: "finished",
  tasks: [],
  teamId: "team-a",
};

const fleet: DelegationFleet = {
  ...createDelegationFleet("session-a"),
  parallelGroups: [parallel],
  subagents: [subagent],
  teams: [team],
};

/** Holds the chosen activity the way the chat surface does, and records every change. */
function renderPanel({
  family,
  focus: initialFocus,
}: {
  family?: DelegationFocus["family"];
  focus?: DelegationFocus;
} = {}) {
  const onFocusChange = vi.fn<(focus?: DelegationFocus) => void>();
  function Harness() {
    const [focus, setFocus] = useState(initialFocus);
    return (
      <>
        <button type="button">Before the panel</button>
        <SessionActivityContent
          family={family}
          fleet={fleet}
          focus={focus}
          onFocusChange={(next) => {
            onFocusChange(next);
            setFocus(next);
          }}
        />
      </>
    );
  }
  render(<Harness />);
  return { onFocusChange, user: userEvent.setup() };
}

const tab = (name: string) => screen.getByRole("tab", { name });
const selectedTabs = () =>
  screen.getAllByRole("tab", { selected: true }).map((item) => item.getAttribute("aria-label"));
const row = (name: string) => screen.getByRole("button", { name });
const pressed = (element: HTMLElement) => element.getAttribute("aria-pressed");

describe("activity family tab pins", () => {
  it("names the tablist and its tabs, and opens on the busiest family", () => {
    renderPanel();
    const tablist = screen.getByRole("tablist", { name: "Activity families" });
    expect(
      within(tablist)
        .getAllByRole("tab")
        .map((item) => item.getAttribute("aria-label")),
    ).toEqual(["Subagents (1)", "Parallel (1)", "Teams (1)"]);
    expect(tab("Subagents (1)").textContent).toBe("Subagents(1)");
    expect(selectedTabs()).toEqual(["Parallel (1)"]);
    expect(tab("Subagents (1)").getAttribute("aria-selected")).toBe("false");
    expect(tab("Teams (1)").getAttribute("aria-selected")).toBe("false");
  });

  it("labels the one panel with the selected tab, which controls it", () => {
    renderPanel({ family: "team" });
    const panels = screen.getAllByRole("tabpanel");
    expect(panels).toHaveLength(1);
    const panel = screen.getByRole("tabpanel", { name: "Teams (1)" });
    expect(tab("Teams (1)").getAttribute("aria-controls")).toBe(panel.id);
    expect(within(panel).getByRole("heading", { name: "Roster" })).toBeTruthy();
    expect(within(panel).getByRole("button", { name: "Team team-a · Finished" })).toBeTruthy();
    expect(within(panel).getByText("Choose an activity to see its details.")).toBeTruthy();
  });

  it("chooses a family on click, shows its roster, and clears the chosen activity", async () => {
    const { onFocusChange, user } = renderPanel({
      focus: { family: "parallel", key: parallel.key },
    });
    expect(screen.getByRole("heading", { name: "Parallel group call-p" })).toBeTruthy();

    await user.click(tab("Subagents (1)"));
    expect(selectedTabs()).toEqual(["Subagents (1)"]);
    const panel = screen.getByRole("tabpanel", { name: "Subagents (1)" });
    expect(within(panel).getByRole("button", { name: "Subagent child-s · Failed" })).toBeTruthy();
    expect(within(panel).queryByRole("button", { name: /Parallel group/ })).toBeNull();
    expect(onFocusChange).toHaveBeenLastCalledWith(undefined);
    expect(screen.queryByRole("heading", { name: "Parallel group call-p" })).toBeNull();
    expect(within(panel).getByText("Choose an activity to see its details.")).toBeTruthy();

    await user.click(tab("Teams (1)"));
    expect(selectedTabs()).toEqual(["Teams (1)"]);
    expect(
      within(screen.getByRole("tabpanel", { name: "Teams (1)" })).getByRole("button", {
        name: "Team team-a · Finished",
      }),
    ).toBeTruthy();
  });

  it("clears the chosen activity when the selected tab is clicked again", async () => {
    const { onFocusChange, user } = renderPanel({ focus: { family: "team", key: team.key } });
    expect(screen.getByRole("heading", { name: "Team team-a" })).toBeTruthy();
    expect(selectedTabs()).toEqual(["Teams (1)"]);

    await user.click(tab("Teams (1)"));
    expect(selectedTabs()).toEqual(["Teams (1)"]);
    expect(onFocusChange).toHaveBeenCalledTimes(1);
    expect(onFocusChange).toHaveBeenLastCalledWith(undefined);
    expect(screen.queryByRole("heading", { name: "Team team-a" })).toBeNull();
    expect(screen.getByText("Choose an activity to see its details.")).toBeTruthy();
  });

  it("moves the selection and focus with the arrow keys, wrapping, and with Home and End", async () => {
    const { onFocusChange, user } = renderPanel({
      focus: { family: "parallel", key: parallel.key },
    });
    act(() => tab("Parallel (1)").focus());

    await user.keyboard("{ArrowRight}");
    expect(selectedTabs()).toEqual(["Teams (1)"]);
    expect(document.activeElement).toBe(tab("Teams (1)"));
    expect(onFocusChange).toHaveBeenLastCalledWith(undefined);
    expect(screen.getByRole("tabpanel", { name: "Teams (1)" })).toBeTruthy();

    await user.keyboard("{ArrowRight}");
    expect(selectedTabs()).toEqual(["Subagents (1)"]);
    expect(document.activeElement).toBe(tab("Subagents (1)"));

    await user.keyboard("{ArrowLeft}");
    expect(selectedTabs()).toEqual(["Teams (1)"]);
    expect(document.activeElement).toBe(tab("Teams (1)"));

    await user.keyboard("{Home}");
    expect(selectedTabs()).toEqual(["Subagents (1)"]);
    expect(document.activeElement).toBe(tab("Subagents (1)"));

    await user.keyboard("{End}");
    expect(selectedTabs()).toEqual(["Teams (1)"]);
    expect(document.activeElement).toBe(tab("Teams (1)"));
    expect(screen.getByRole("tabpanel", { name: "Teams (1)" })).toBeTruthy();
  });

  it("leaves the arrow keys that do not move between tabs alone", async () => {
    const { user } = renderPanel();
    act(() => tab("Parallel (1)").focus());
    await user.keyboard("{ArrowUp}{ArrowDown}");
    expect(selectedTabs()).toEqual(["Parallel (1)"]);
    expect(document.activeElement).toBe(tab("Parallel (1)"));
  });

  it("enters the tablist on the selected tab and leaves it for the panel's first row", async () => {
    const { user } = renderPanel();
    act(() => screen.getByRole("button", { name: "Before the panel" }).focus());

    await user.tab();
    expect(document.activeElement).toBe(tab("Parallel (1)"));
    await user.tab();
    expect(document.activeElement).toBe(row("Parallel group call-p · Running"));
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(tab("Parallel (1)"));
  });
});

describe("activity roster row pins", () => {
  it("presses the row whose activity is chosen", async () => {
    const { onFocusChange, user } = renderPanel({ family: "subagent" });
    const subagentRow = row("Subagent child-s · Failed");
    expect(pressed(subagentRow)).toBe("false");

    await user.click(subagentRow);
    expect(onFocusChange).toHaveBeenLastCalledWith({ family: "subagent", key: subagent.key });
    expect(pressed(row("Subagent child-s · Failed"))).toBe("true");
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Subagent child-s" }));
  });

  it("keeps the chosen row pressed when it is pressed again", async () => {
    const focus: DelegationFocus = { family: "subagent", key: subagent.key };
    const { onFocusChange, user } = renderPanel({ focus });
    expect(pressed(row("Subagent child-s · Failed"))).toBe("true");

    await user.click(row("Subagent child-s · Failed"));
    expect(onFocusChange).toHaveBeenCalledTimes(1);
    expect(onFocusChange).toHaveBeenLastCalledWith(focus);
    expect(pressed(row("Subagent child-s · Failed"))).toBe("true");
  });

  it("chooses a row from the keyboard with Enter and Space", async () => {
    const { onFocusChange, user } = renderPanel({ family: "parallel" });
    act(() => row("Parallel group call-p · Running").focus());
    await user.keyboard("{Enter}");
    expect(onFocusChange).toHaveBeenLastCalledWith({ family: "parallel", key: parallel.key });
    expect(pressed(row("Parallel group call-p · Running"))).toBe("true");

    const secondBranch = row("Branch 2 Finished");
    expect(pressed(secondBranch)).toBe("false");
    act(() => secondBranch.focus());
    await user.keyboard(" ");
    expect(onFocusChange).toHaveBeenLastCalledWith({
      branchKey: "branch-1",
      family: "parallel",
      key: parallel.key,
    });
    expect(pressed(row("Branch 2 Finished"))).toBe("true");
    expect(pressed(row("Branch 1 Running"))).toBe("false");
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Branch 2" }));
  });

  it("presses a team member's row while that member is chosen", async () => {
    const { onFocusChange, user } = renderPanel({ focus: { family: "team", key: team.key } });
    const lead = row("lead · Lead Done");
    const worker = row("worker Stopped: budget");
    expect([pressed(lead), pressed(worker)]).toEqual(["false", "false"]);

    await user.click(worker);
    expect(onFocusChange).toHaveBeenLastCalledWith({
      family: "team",
      key: team.key,
      memberKey: "member-worker",
    });
    expect(pressed(row("worker Stopped: budget"))).toBe("true");
    expect(pressed(row("lead · Lead Done"))).toBe("false");
    expect(pressed(row("Team team-a · Finished"))).toBe("true");
    expect(screen.getByRole("heading", { name: "Member worker" })).toBeTruthy();
  });
});
