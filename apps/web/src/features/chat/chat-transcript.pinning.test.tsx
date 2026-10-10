// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApprovalRequest } from "./approval-panel";
import type { ChatMessage } from "./chat-state";
import { ChatTranscript } from "./chat-transcript";
import { ContentPreviewPanel } from "./content-preview-panel";
import {
  type DelegationActivity,
  DelegationCardRow,
  type DelegationFocus,
} from "./delegation-card";
import type {
  ParallelGroupActivity,
  SubagentActivity,
  TeamActivity,
  TeamMemberActivity,
} from "./delegation-fleet";
import type { ToolActivity } from "./tool-activity";

// Pins the transcript's observable contract before the prototype restyle:
// what a reader can find for each tool call, delegated child, and scheduled
// delivery. They assert names, text, and wiring, never markup or classes, so
// the same file passes on the old rows and on the ported ones.

afterEach(cleanup);

const read: ToolActivity = {
  args: '{"path":"notes.md"}',
  id: "call-read",
  name: "Read",
  output: "file contents",
  runId: "run-a",
};
const shell: ToolActivity = {
  args: '{"command":"rm -rf build"}',
  id: "call-shell",
  isError: true,
  name: "Shell",
  output: "permission denied",
  runId: "run-a",
};
const grep: ToolActivity = {
  args: '{"pattern":"TODO"}',
  id: "call-grep",
  name: "Grep",
  runId: "run-a",
};
const edit: ToolActivity = {
  args: JSON.stringify({ new_string: "b := 2", old_string: "a := 1", path: "pkg/main.go" }),
  id: "call-edit",
  name: "Edit",
  output: "Edited pkg/main.go",
  runId: "run-a",
};

function toolTurn(tools: ToolActivity[]): ChatMessage[] {
  return [
    { content: "Check the notes", id: "prompt", role: "user" },
    { content: "Looking now.", id: "answer", role: "assistant", tools },
  ];
}

function toolRow(name: string): HTMLElement {
  const row = screen.getByText(`Tool: ${name}`).closest("li");
  if (!row) throw new Error(`The ${name} tool row is missing`);
  return row;
}

describe("transcript tool calls (pinned)", () => {
  it("lists every call by name, in order, with its arguments and result", () => {
    render(<ChatTranscript messages={toolTurn([read, grep, edit])} showToolCalls />);
    const rows = ["Read", "Grep", "Edit"].map(toolRow);
    expect(rows[0]?.compareDocumentPosition(rows[1] as Node)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(rows[1]?.compareDocumentPosition(rows[2] as Node)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(rows[0]?.textContent).toContain("notes.md");
    expect(rows[0]?.textContent).toContain("file contents");
    expect(rows[1]?.textContent).toContain("TODO");
    expect(rows[2]?.textContent).toContain("pkg/main.go");
    expect(rows[2]?.textContent).toContain("b := 2");
  });

  it("marks a failed call and keeps its error output", () => {
    render(<ChatTranscript messages={toolTurn([read, shell])} showToolCalls />);
    const failed = toolRow("Shell");
    expect(failed.textContent).toMatch(/failed/iu);
    expect(failed.textContent).toContain("permission denied");
    expect(toolRow("Read").textContent).not.toMatch(/failed/iu);
  });

  it("shows a running call without a result", () => {
    render(<ChatTranscript messages={toolTurn([grep])} showToolCalls />);
    const running = toolRow("Grep");
    expect(running.textContent).toContain("TODO");
    expect(running.textContent).not.toMatch(/failed/iu);
    expect(running.textContent).not.toContain("No output");
  });

  it("hides the calls with Show Tools off, unless an ask needs one", () => {
    const approval: ApprovalRequest = {
      args: '{"command":"rm -rf build"}',
      askId: "ask-shell",
      callId: "call-shell",
      controlTarget: { askId: "ask-shell", runId: "run-a", sessionId: "chat-a" },
      reason: "Deletes the build",
      tool: "Shell",
    };
    const view = render(
      <ChatTranscript messages={toolTurn([read, shell])} showToolCalls={false} />,
    );
    expect(screen.queryByText("Tool: Read")).toBeNull();
    expect(screen.queryByText("Tool: Shell")).toBeNull();
    view.rerender(
      <ChatTranscript
        approvals={[approval]}
        messages={toolTurn([read, shell])}
        onRespondToApproval={() => {}}
        showToolCalls={false}
      />,
    );
    const row = toolRow("Shell");
    expect(within(row).getByRole("region", { name: "Permission required: Shell" })).toBeTruthy();
  });

  it("opens a finished call in the side panel from its row", () => {
    const preview = vi.fn();
    render(<ChatTranscript messages={toolTurn([read])} onPreviewTool={preview} showToolCalls />);
    // `hidden: true`: the action may sit behind a closed disclosure.
    const open = within(toolRow("Read"))
      .getAllByRole("button", { hidden: true })
      .find((button) =>
        /open/iu.test(button.getAttribute("aria-label") ?? button.textContent ?? ""),
      );
    if (!open) throw new Error("The row has no open action");
    fireEvent.click(open);
    expect(preview).toHaveBeenCalledWith(read);
  });

  it("shows a call's full input and output in the tool panel", () => {
    const common = { canvas: "", onCanvasChange: vi.fn(), onClose: vi.fn() };
    const view = render(<ContentPreviewPanel {...common} preview={{ kind: "tool", tool: read }} />);
    const panel = screen.getByRole("complementary", { name: "Read result" });
    expect(panel.textContent).toContain('"path": "notes.md"');
    expect(panel.textContent).toContain("file contents");
    view.rerender(<ContentPreviewPanel {...common} preview={{ kind: "tool", tool: shell }} />);
    const failed = screen.getByRole("complementary", { name: "Shell result" });
    expect(within(failed).getByText("Failed output")).toBeTruthy();
    expect(failed.textContent).toContain("permission denied");
  });
});

const trace = { entries: [], omitted: 0 };

function subagent(overrides: Partial<SubagentActivity>): SubagentActivity {
  return {
    childId: "child-a",
    family: "subagent",
    goal: "Diff the contracts",
    historyIncomplete: false,
    key: '["chat-a","run-a","subagent","call-a","child-a"]',
    parentCallId: "call-a",
    runId: "run-a",
    sessionId: "chat-a",
    startObserved: true,
    state: "running",
    trace,
    ...overrides,
  };
}

function parallel(overrides: Partial<ParallelGroupActivity>): ParallelGroupActivity {
  return {
    branchCount: 2,
    branches: [
      {
        branchIndex: 0,
        historyIncomplete: false,
        key: "branch-0",
        label: "Sessions",
        startObserved: true,
        state: "running",
        toolCount: 3,
        currentTool: "Read",
        trace,
      },
      {
        branchIndex: 1,
        historyIncomplete: false,
        key: "branch-1",
        label: "Schedules",
        startObserved: true,
        state: "running",
        trace,
      },
    ],
    family: "parallel",
    historyIncomplete: false,
    join: "all",
    key: '["chat-a","run-a","parallel","call-p"]',
    parentCallId: "call-p",
    runId: "run-a",
    sessionId: "chat-a",
    startObserved: true,
    state: "running",
    ...overrides,
  };
}

function team(member: Partial<TeamMemberActivity>, overrides: Partial<TeamActivity> = {}) {
  return {
    family: "team",
    findings: [],
    historyIncomplete: false,
    key: '["chat-a","run-a","team","call-t"]',
    members: [
      {
        key: "member-worker",
        name: "worker",
        role: "Reviews handlers",
        state: "running",
        trace,
        ...member,
      },
    ],
    parentCallId: "call-t",
    runId: "run-a",
    sessionId: "chat-a",
    startObserved: true,
    state: "running",
    tasks: [],
    teamId: "team-a",
    ...overrides,
  } satisfies TeamActivity;
}

function cards(activity: DelegationActivity) {
  const opened: DelegationFocus[] = [];
  render(
    <DelegationCardRow
      activities={[activity]}
      onOpen={(focus, opener) => {
        expect(opener.tagName).toBe("BUTTON");
        opened.push(focus);
      }}
    />,
  );
  return { buttons: screen.getAllByRole("button"), opened };
}

describe("delegation cards (pinned)", () => {
  it.each([
    ["running", subagent({ currentTool: "Grep", toolCount: 2 }), ["Running", "Tools: 2", "Grep"]],
    [
      "done",
      subagent({ state: "finished", stop: "end_turn", toolCount: 6 }),
      ["Finished", "Stop: end_turn", "Tools: 6"],
    ],
    [
      "failed",
      subagent({ cause: "Tool failed", state: "finished", stop: "error" }),
      ["Failed", "Stop: error"],
    ],
    ["unknown", subagent({ state: "unknown" }), ["Outcome unknown"]],
  ] as const)("names a %s subagent and its outcome", (_state, activity, expected) => {
    const { buttons, opened } = cards(activity);
    expect(buttons).toHaveLength(1);
    const card = buttons[0] as HTMLElement;
    expect(card.textContent).toContain("Subagent child-a");
    expect(card.textContent).toContain("Diff the contracts");
    for (const text of expected) expect(card.textContent).toContain(text);
    expect(card.getAttribute("data-delegation-focus")).toBe(
      JSON.stringify({ family: "subagent", key: activity.key }),
    );
    fireEvent.click(card);
    expect(opened).toEqual([{ family: "subagent", key: activity.key }]);
  });

  it.each([
    ["running", parallel({}), ["Running", "Join: all", "Branches: 2", "Current tool: Read"]],
    [
      "done",
      parallel({
        branches: parallel({}).branches.map((branch) => ({
          ...branch,
          state: "finished" as const,
          stop: "end_turn",
        })),
        state: "finished",
        stop: "end_turn",
        winner: 1,
      }),
      ["Finished", "Winner: Branch 2", "Stop: end_turn"],
    ],
    [
      "failed",
      parallel({
        branches: parallel({}).branches.map((branch) => ({
          ...branch,
          failed: true,
          state: "finished" as const,
          stop: "error",
        })),
        state: "finished",
        stop: "error",
      }),
      ["Failed", "Stop: error"],
    ],
  ] as const)("names a %s parallel group with its branches", (_state, activity, expected) => {
    const { buttons, opened } = cards(activity);
    expect(buttons).toHaveLength(1);
    const card = buttons[0] as HTMLElement;
    expect(card.textContent).toContain("Parallel group");
    expect(card.textContent).toContain("Branch 1");
    expect(card.textContent).toContain("Sessions");
    expect(card.textContent).toContain("Branch 2");
    expect(card.textContent).toContain("Schedules");
    expect(card.textContent).not.toContain("Subagent");
    for (const text of expected) expect(card.textContent).toContain(text);
    fireEvent.click(card);
    expect(opened).toEqual([{ family: "parallel", key: activity.key }]);
  });

  it.each([
    ["running", team({ currentTool: "Read" }), ["Running", "Current tool: Read"]],
    ["done", team({ disposition: "done", state: "finished" }, { state: "finished" }), ["Done"]],
    [
      "stopped",
      team({ disposition: "stopped", reason: "budget", state: "finished" }, { state: "finished" }),
      ["Stopped: budget"],
    ],
    [
      "failed",
      team({ cause: "Model error", state: "finished" }, { state: "finished", stop: "error" }),
      ["Failed", "Stop: error"],
    ],
  ] as const)("names a %s team member", (_state, activity, expected) => {
    const { buttons, opened } = cards(activity);
    expect(buttons).toHaveLength(1);
    const card = buttons[0] as HTMLElement;
    expect(card.textContent).toContain("Team team-a");
    expect(card.textContent).toContain("Member worker");
    expect(card.textContent).toContain("Reviews handlers");
    for (const text of expected) expect(card.textContent).toContain(text);
    fireEvent.click(card);
    expect(opened).toEqual([{ family: "team", key: activity.key, memberKey: "member-worker" }]);
  });

  it("gives a lead its own card and marks it", () => {
    const activity = team({});
    activity.members.unshift({
      key: "member-lead",
      lead: true,
      name: "lead",
      state: "running",
      trace,
    });
    const { buttons } = cards(activity);
    expect(buttons).toHaveLength(2);
    expect(buttons[0]?.textContent).toContain("Member lead");
    expect(buttons[0]?.textContent).toContain("Lead");
    expect(buttons[1]?.textContent).toContain("Member worker");
  });
});

describe("scheduled delivery notes (pinned)", () => {
  function delivered(
    kind: "started" | "completed",
    stop: string | undefined,
    content: string,
  ): ChatMessage[] {
    return [
      {
        content,
        delivery: { fireId: "fire-0925", kind, scheduleName: "nightly-digest", stop },
        id: `delivery-${kind}`,
        role: "user",
      },
    ];
  }

  it("attributes a started fire to its schedule", () => {
    render(<ChatTranscript messages={delivered("started", undefined, "")} showToolCalls />);
    const note = screen.getByRole("article", { name: "Scheduled task nightly-digest message" });
    expect(note.querySelector("[data-delivery-note]")).not.toBeNull();
    expect(note.textContent).toContain("nightly-digest");
    expect(note.textContent).toContain("fire-0925");
    expect(note.textContent).toContain("started");
  });

  it("shows a completed fire's outcome as plain text", () => {
    render(
      <ChatTranscript
        messages={delivered("completed", "end_turn", "Nightly **digest** done\n\n- one")}
        onOpenThread={() => {}}
        showToolCalls
      />,
    );
    const note = screen.getByRole("article", { name: "Scheduled task nightly-digest message" });
    expect(note.textContent).toContain("completed");
    expect(note.textContent).toContain("fire-0925");
    expect(note.textContent).toContain("Nightly **digest** done");
    expect(note.querySelector("strong, ul, li")).toBeNull();
    expect(within(note).queryByRole("button", { name: /side thread/u })).toBeNull();
  });

  it("names a stopped fire's stop reason", () => {
    render(
      <ChatTranscript messages={delivered("completed", "max_turns", "Partial")} showToolCalls />,
    );
    const note = screen.getByRole("article", { name: "Scheduled task nightly-digest message" });
    expect(note.textContent).toContain("max_turns");
    expect(note.textContent).toContain("Partial");
  });
});
