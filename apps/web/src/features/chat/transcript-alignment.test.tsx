// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import type { ApprovalRequest } from "./approval-panel";
import type { ChatMessage } from "./chat-state";
import { ChatTranscript } from "./chat-transcript";
import { ContentPreviewPanel } from "./content-preview-panel";
import { createDelegationFleet, type DelegationFleet } from "./delegation-fleet";
import { SessionActivityContent } from "./delegation-panel";
import { DeliveryNoteCard, deliveryBadge } from "./delivery-note-card";
import { FleetStatusChip, fleetSegments } from "./fleet-status-chip";
import { MessageMinimap } from "./message-minimap";
import { SteerTrace } from "./steer-trace";
import type { ToolActivity } from "./tool-activity";
import { activitySummary, ToolCallList } from "./tool-call-list";

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
});

const read: ToolActivity = {
  args: JSON.stringify({ path: "src/notes.md" }),
  id: "call-read",
  name: "Read",
  output: JSON.stringify({ content: "hello" }),
  runId: "run-a",
};
const failed: ToolActivity = {
  args: JSON.stringify({ command: "false" }),
  id: "call-shell",
  isError: true,
  name: "Shell",
  output: "exit 1",
  runId: "run-a",
};
const edit: ToolActivity = {
  args: JSON.stringify({ new_string: "b", old_string: "a", path: "src/edited.ts" }),
  id: "call-edit",
  name: "Edit",
  output: "ok",
  runId: "run-a",
};
const mcp: ToolActivity = {
  args: "{}",
  id: "call-mcp",
  name: "mcp__github__issue_write",
  runId: "run-a",
};

describe("tool call list", () => {
  it("summarises the calls, and names failures only when there are some", () => {
    expect(activitySummary([read])).toEqual({ failed: null, tools: "1 tool" });
    expect(activitySummary([read, failed])).toEqual({ failed: "1 failed", tools: "2 tools" });
  });

  it("starts closed under the default preference and opens on its toggle", () => {
    render(<ToolCallList tools={[read, failed, mcp]} />);
    const toggle = screen.getByRole("button", { expanded: false });
    expect(toggle.textContent).toContain("Activity: 3 tools");
    expect(toggle.textContent).toContain("1 failed");
    expect(toggle.textContent).toContain("Read · Shell · GitHub · Issue write");
    expect(screen.getByRole("list", { hidden: true }).hidden).toBe(true);
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const list = screen.getByRole("list");
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    expect(within(list).getByText("path: src/notes.md")).toBeTruthy();
    expect(within(list).getByText("keys: content")).toBeTruthy();
    expect(within(list).getByText("exit 1").className).toContain("text-destructive");
    expect(within(list).getByTitle("mcp__github__issue_write").textContent).toBe(
      "GitHub · Issue write",
    );
  });

  it("starts open under Expand details", () => {
    window.localStorage.setItem("studio.profile.expand-details", "expanded");
    render(<ToolCallList tools={[read]} />);
    expect(screen.getByRole("button", { expanded: true })).toBeTruthy();
    expect(screen.getByRole("list").hidden).toBe(false);
  });

  it("stays open while a call carries an ask", () => {
    const approval: ApprovalRequest = {
      args: failed.args,
      askId: "ask-a",
      callId: "call-shell",
      controlTarget: { askId: "ask-a", runId: "run-a", sessionId: "chat-a" },
      reason: "",
      tool: "Shell",
    };
    render(
      <ToolCallList approvals={[approval]} tools={[read, { ...failed, output: undefined }]} />,
    );
    const toggle = screen.getByRole("button", { expanded: true });
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("region", { name: "Permission required: Shell" })).toBeTruthy();
  });

  it("shows a finished edit's diff, never a pending or failed one's", () => {
    window.localStorage.setItem("studio.profile.expand-details", "expanded");
    const view = render(<ToolCallList tools={[edit]} />);
    expect(screen.getByTestId("edit-diff")).toBeTruthy();
    view.rerender(<ToolCallList tools={[{ ...edit, output: undefined }]} />);
    expect(screen.queryByTestId("edit-diff")).toBeNull();
    view.rerender(<ToolCallList tools={[{ ...edit, isError: true }]} />);
    expect(screen.queryByTestId("edit-diff")).toBeNull();
  });

  it("opens a call's detail from its head, labelled with the friendly name", () => {
    window.localStorage.setItem("studio.profile.expand-details", "expanded");
    const preview = vi.fn();
    render(<ToolCallList onPreview={preview} tools={[mcp]} />);
    fireEvent.click(screen.getByRole("button", { name: "Open GitHub · Issue write details" }));
    expect(preview).toHaveBeenCalledWith(mcp);
  });
});

describe("tool panel", () => {
  const common = { canvas: "", onCanvasChange: vi.fn(), onClose: vi.fn() };

  it("shows an edit's input as its diff, with Raw back to the arguments", () => {
    render(<ContentPreviewPanel {...common} preview={{ kind: "tool", tool: edit }} />);
    const panel = screen.getByRole("complementary", { name: "Edit result" });
    expect(within(panel).getByText("completed")).toBeTruthy();
    expect(within(panel).getByText("Input — diff")).toBeTruthy();
    expect(within(panel).getByTestId("edit-diff")).toBeTruthy();
    fireEvent.click(within(panel).getByRole("button", { name: "Raw" }));
    expect(within(panel).queryByTestId("edit-diff")).toBeNull();
    expect(within(panel).getByText("Input")).toBeTruthy();
    expect(panel.textContent).toContain('"old_string": "a"');
  });

  it("says a running call has no output yet, and names an MCP tool exactly", () => {
    render(<ContentPreviewPanel {...common} preview={{ kind: "tool", tool: mcp }} />);
    const panel = screen.getByRole("complementary", { name: "mcp__github__issue_write result" });
    expect(within(panel).getByText("running")).toBeTruthy();
    expect(within(panel).getByTitle("Exact tool name").textContent).toContain(
      "mcp__github__issue_write",
    );
    expect(panel.textContent).toContain("(still running)");
  });
});

describe("message rows", () => {
  const turn: ChatMessage[] = [
    { content: "Question", id: "u1", role: "user" },
    { content: "Answer", id: "a1", role: "assistant" },
  ];

  it("offers Copy and the side thread as row actions, only when wired", () => {
    const copy = vi.fn();
    const thread = vi.fn();
    const view = render(<ChatTranscript messages={turn} showToolCalls />);
    expect(screen.queryByRole("button", { name: "Copy to clipboard" })).toBeNull();
    view.rerender(
      <ChatTranscript
        messages={turn}
        onCopyMessage={copy}
        onOpenThread={thread}
        showToolCalls
        threadSessionIdForMessage={(message) => (message.id === "a1" ? "thread-a" : undefined)}
      />,
    );
    const answer = screen.getByRole("article", { name: "Mecatl message" });
    fireEvent.click(within(answer).getByRole("button", { name: "Copy to clipboard" }));
    expect(copy).toHaveBeenCalledWith(turn[1]);
    fireEvent.click(within(answer).getByRole("button", { name: "Open side thread" }));
    expect(thread).toHaveBeenCalledWith(turn[1]);
    const question = screen.getByRole("article", { name: "You message" });
    expect(within(question).getByRole("button", { name: "Reply in side thread" })).toBeTruthy();
  });

  it("shows the configured avatars beside each speaker", () => {
    render(
      <ChatTranscript
        agentAvatar="data:image/png;base64,YQ=="
        messages={turn}
        showToolCalls
        userAvatar="data:image/png;base64,Yg=="
        userName="Sam"
      />,
    );
    expect(screen.getByRole("img", { name: "Sam" }).getAttribute("src")).toBe(
      "data:image/png;base64,Yg==",
    );
    expect(screen.getByRole("img", { name: "Mecatl" }).getAttribute("src")).toBe(
      "data:image/png;base64,YQ==",
    );
  });
});

describe("delivery note card", () => {
  it("maps the fire's stop to the prototype's badge", () => {
    const base = { fireId: "f", scheduleName: "nightly" };
    expect(deliveryBadge({ ...base, kind: "started" })).toEqual({
      label: "started",
      variant: "secondary",
    });
    expect(deliveryBadge({ ...base, kind: "completed" }).label).toBe("completed");
    expect(deliveryBadge({ ...base, kind: "completed", stop: "end_turn" }).variant).toBe("success");
    expect(deliveryBadge({ ...base, kind: "completed", stop: "error" })).toEqual({
      label: "failed",
      variant: "destructive",
    });
    expect(deliveryBadge({ ...base, kind: "completed", stop: "max_turns" }).label).toBe(
      "max_turns",
    );
  });

  it("clamps a long body behind Show more", () => {
    const body = Array.from({ length: 12 }, (_, index) => `line ${index + 1}`).join("\n");
    render(
      <DeliveryNoteCard
        body={body}
        delivery={{ fireId: "fire-1", kind: "completed", scheduleName: "nightly" }}
      />,
    );
    expect(screen.getByText(/^line 1 line 2/u).className).toContain("line-clamp-8");
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(screen.getByText(/^line 1 line 2/u).className).not.toContain("line-clamp-8");
    expect(screen.getByRole("button", { name: "Show less" }).getAttribute("aria-expanded")).toBe(
      "true",
    );
  });
});

function fleetWith(overrides: Partial<DelegationFleet>): DelegationFleet {
  return { ...createDelegationFleet("chat-a"), ...overrides };
}

const trace = { entries: [], omitted: 0 };
const base = {
  historyIncomplete: false,
  parentCallId: "call",
  runId: "run-a",
  sessionId: "chat-a",
  startObserved: true,
};

describe("fleet status chip", () => {
  const fleet = fleetWith({
    parallelGroups: [{ ...base, branches: [], family: "parallel", key: "p", state: "finished" }],
    subagents: [
      { ...base, childId: "a", family: "subagent", key: "a", state: "running", trace },
      { ...base, childId: "b", family: "subagent", key: "b", state: "finished", trace },
    ],
    teams: [
      {
        ...base,
        family: "team",
        findings: [],
        key: "t",
        members: [
          { key: "m1", name: "lead", state: "running", trace },
          { disposition: "done", key: "m2", name: "worker", state: "running", trace },
        ],
        state: "running",
        tasks: [],
        teamId: "team-a",
      },
    ],
  });

  it("renders nothing for an empty fleet", () => {
    expect(fleetSegments(undefined)).toEqual([]);
    expect(fleetSegments(createDelegationFleet("chat-a"))).toEqual([]);
    const { container } = render(
      <FleetStatusChip fleet={createDelegationFleet("chat-a")} onOpen={() => {}} />,
    );
    expect(container.childElementCount).toBe(0);
  });

  it("counts each family and opens the panel on the one clicked", () => {
    expect(fleetSegments(fleet).map((segment) => segment.label)).toEqual([
      "1 running · 1 done",
      "0 running · 1 done",
      "1/2 working",
    ]);
    const open = vi.fn();
    render(<FleetStatusChip fleet={fleet} onOpen={open} />);
    const chip = screen.getByRole("list", { name: "Agents in this chat" });
    fireEvent.click(within(chip).getByRole("button", { name: "Parallel · 0 running · 1 done" }));
    expect(open).toHaveBeenCalledWith("parallel", expect.any(HTMLButtonElement));
  });

  it("lets the activity panel open on the requested family", () => {
    render(<SessionActivityContent family="team" fleet={fleet} onFocusChange={() => {}} />);
    expect(screen.getByRole("tab", { selected: true }).getAttribute("aria-label")).toBe(
      "Teams (1)",
    );
  });
});

describe("minimap preview", () => {
  it("previews a hovered message beside the rail and widens its dash", () => {
    const messages: ChatMessage[] = [
      { content: "First question", id: "u1", role: "user" },
      { content: "First   answer\nwith lines", id: "a1", role: "assistant" },
    ];
    render(
      <MessageMinimap
        agentName="Mecatl"
        approvals={[]}
        messages={messages}
        onNavigate={() => {}}
        scrollportRef={createRef()}
        userName="Sam"
      />,
    );
    const dash = screen.getByRole("button", { name: /^Jump to message 2:/u });
    act(() => {
      fireEvent.mouseEnter(dash);
    });
    const preview = screen.getByRole("tooltip");
    expect(preview.textContent).toContain("First answer with lines");
    expect(preview.textContent).toContain("Mecatl");
    expect((dash.firstElementChild as HTMLElement).style.width).toBe("28px");
    fireEvent.mouseLeave(dash);
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect((dash.firstElementChild as HTMLElement).style.width).toBe("12px");
  });
});

describe("steer trace", () => {
  it("lists each observed steer event on its own line", () => {
    render(
      <SteerTrace
        entries={[
          { kind: "steer", messageId: "m-1", runId: "run-a" },
          {
            kind: "steer.outcome",
            messageId: "m-1",
            outcome: "accepted",
            promoted: false,
            runId: "run-a",
          },
        ]}
      />,
    );
    const items = within(
      screen.getByRole("region", { name: "Developer steer trace" }),
    ).getAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual([
      "Run run-a · Message m-1 · Event steer · Outcome unknown",
      "Run run-a · Message m-1 · Event steer.outcome · Outcome accepted · Promoted no",
    ]);
    expect(items[1]?.getAttribute("title")).toBe(items[1]?.textContent);
  });
});
