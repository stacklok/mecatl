import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AgentSession } from "@/features/agent";
import {
  InspectSessionGroups,
  InspectSessionRow,
  inspectRowDomId,
} from "./inspect-session-list";

/**
 * The read-only inventory rows (Runs / Scheduled / Other tabs): a row names
 * the run by title or by what it is, chips its kind and its read-only
 * posture with the daemon's reason, hands its id to `onInspect` on click
 * (never a chat rebind), and a row whose transcript the daemon withholds
 * stays listed and says so.
 */

const run = (partial: Partial<AgentSession> = {}): AgentSession => ({
  id: "subagent-1",
  title: "",
  projectId: null,
  model: "m",
  createdAt: 0,
  updatedAt: Date.now() - 60_000,
  pinned: false,
  archived: false,
  messageCount: 3,
  isStreaming: false,
  inputTokens: 0,
  outputTokens: 0,
  unread: false,
  estimatedCost: null,
  contextLength: null,
  lastPromptTokens: null,
  thresholdTokens: null,
  state: "completed",
  kind: "subagent",
  isChat: false,
  canInspect: true,
  canViewTranscript: true,
  publicChatReason: "inspect_only_kind",
  viewTranscriptReason: "",
  relationship: {
    parentSessionId: "main-1",
    callId: "c7",
    branchIndex: null,
    scheduleName: "",
    originSessionId: "",
    teamId: "",
    memberName: "",
  },
  ...partial,
});

describe("InspectSessionRow", () => {
  it("names an untitled run by its relationship, chips its kind and read-only reason, and inspects on click", () => {
    const onInspect = vi.fn();
    render(<InspectSessionRow session={run()} onInspect={onInspect} />);
    const button = screen.getByRole("button", {
      name: "Inspect run: Subagent of main-1 · call c7",
    });
    expect(button).toHaveAttribute("id", inspectRowDomId("subagent-1"));
    expect(screen.getByText("Subagent")).toBeInTheDocument();
    expect(screen.getByText("Read-only")).toHaveAttribute(
      "title",
      "Read-only run",
    );
    fireEvent.click(button);
    expect(onInspect).toHaveBeenCalledWith("subagent-1");
  });

  it("shows a titled run's title with the relationship as a trailer", () => {
    render(
      <InspectSessionRow
        session={run({ title: "Scan the tests" })}
        onInspect={() => {}}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Inspect run: Scan the tests" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Subagent of main-1 · call c7"),
    ).toBeInTheDocument();
  });

  it("keeps a transcript-less row listed and says why in its label and tooltip", () => {
    render(
      <InspectSessionRow
        session={run({
          id: "fire-1",
          kind: "scheduled",
          canViewTranscript: false,
          viewTranscriptReason: "transcript_unavailable",
          relationship: {
            parentSessionId: "",
            callId: "",
            branchIndex: null,
            scheduleName: "nightly",
            originSessionId: "",
            teamId: "",
            memberName: "",
          },
        })}
        onInspect={() => {}}
      />,
    );
    const button = screen.getByRole("button", {
      name: "Inspect run: Fire of schedule nightly (transcript unavailable)",
    });
    expect(button).toHaveAttribute("title", "Transcript unavailable");
    expect(screen.getByText("Scheduled run")).toBeInTheDocument();
  });

  it("marks a running child with the activity dot instead of the age", () => {
    render(
      <InspectSessionRow
        session={run({ state: "running" })}
        onInspect={() => {}}
      />,
    );
    expect(screen.getByRole("img", { name: "Running" })).toBeInTheDocument();
  });
});

describe("InspectSessionGroups", () => {
  it("renders each recency group under its label", () => {
    render(
      <InspectSessionGroups
        groups={[
          { label: "Today", sessions: [run({ id: "a", title: "A" })] },
          { label: "Earlier", sessions: [run({ id: "b", title: "B" })] },
        ]}
        onInspect={() => {}}
      />,
    );
    expect(screen.getByText("Today")).toBeInTheDocument();
    expect(screen.getByText("Earlier")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /Inspect run/ })).toHaveLength(
      2,
    );
  });
});
