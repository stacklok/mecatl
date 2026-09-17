import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  type DelegationFleet,
  type DelegationInfo,
  emptyFleet,
  type ParallelGroupState,
  type TeamBoardState,
} from "@/features/agent";
import { resetHarnessClient } from "@/lib/harness/sdk";
import { sessionSnapshot, stubHarnessFetch } from "@/lib/harness/sdk-test-stub";
import { memoryStorage } from "@/test/memory-storage";
import {
  type DelegationFocus,
  DelegationPanel,
  type DelegationTab,
  focusForDelegationCard,
  preferredDelegationTab,
  teamTaskBlocked,
  teamTaskGlyph,
} from "./delegation-panel";

/**
 * The Agents panel is the web analogue of the TUI's f6 overlay: three tabs
 * over the session-scoped fleet, a context-sensitive default tab, per-child
 * focus panes with the redacted trace and a transcript, the parallel group
 * with its ★ winner and run stop, and the team roster (lead-first, live
 * state, context meter) with the task board and findings ledger. Every
 * model-influenced preview renders as literal text — never HTML.
 *
 * Esc layering lives in chat-view (a focused pane steps back to its roster
 * before the panel closes); the seam it drives is `onFocus(null)`, which the
 * focus pane's Back button also fires — pinned here. On mobile the Radix
 * Sheet owns Escape, so that step-back applies to the desktop side panel.
 */

const card = (partial: Partial<DelegationInfo> = {}): DelegationInfo => ({
  kind: "subagent",
  label: "explore",
  detail: "",
  ...partial,
});

const group = (
  partial: Partial<ParallelGroupState> = {},
): ParallelGroupState => ({
  parentCallId: "call-p",
  join: "first",
  branchCount: 2,
  winner: -1,
  stop: "",
  done: false,
  branches: [],
  ...partial,
});

const team = (partial: Partial<TeamBoardState> = {}): TeamBoardState => ({
  parentCallId: "call-t",
  teamId: "t1",
  lanes: [],
  tasks: [],
  findings: [],
  done: false,
  rounds: 0,
  stop: "",
  ...partial,
});

const fleetOf = (partial: Partial<DelegationFleet> = {}): DelegationFleet => ({
  ...emptyFleet(),
  ...partial,
});

const runningSubagent = card({
  childId: "subagent-abc123def",
  label: "explore auth",
  detail: "explorer → gpt-5",
  lastTool: "Grep",
  toolCount: 3,
  inputTokens: 1000,
  outputTokens: 200,
  trace: [
    { kind: "tool", name: "Read", detail: "src/auth.ts", isError: false },
    { kind: "tool", name: "Grep", detail: "token", pending: true },
    { kind: "message", text: "Looking at <b>x</b>" },
  ],
});

const doneSubagent = card({
  childId: "subagent-zzz999",
  label: "write tests",
  stop: "max_turns",
  durationMs: 12_000,
  toolCount: 8,
  background: true,
});

const parallelGroup = group({
  winner: 1,
  stop: "end_turn",
  done: true,
  branches: [
    card({
      kind: "parallel",
      label: "slow",
      branchIndex: 0,
      parentCallId: "call-p",
      childId: "parallel-1",
      stop: "cancelled",
      durationMs: 3000,
    }),
    card({
      kind: "parallel",
      label: "fast",
      branchIndex: 1,
      parentCallId: "call-p",
      childId: "parallel-2",
      stop: "end_turn",
      winner: true,
      durationMs: 1500,
      toolCount: 2,
    }),
  ],
});

const teamBoard = team({
  lanes: [
    card({
      kind: "team",
      label: "coder (implementer)",
      memberName: "coder",
      parentCallId: "call-t",
      teamId: "t1",
      childId: "team-t1-coder",
      lastTool: "Edit",
      mutating: true,
      toolCount: 4,
      contextUsed: 42_000,
      contextWindow: 100_000,
    }),
    card({
      kind: "team",
      label: "lead (lead)",
      memberName: "lead",
      lead: true,
      parentCallId: "call-t",
      teamId: "t1",
      idle: true,
    }),
  ],
  tasks: [
    {
      id: "t1",
      state: "in_progress",
      assignee: "coder",
      deps: [],
      description: "Write the tests",
    },
    {
      id: "t2",
      state: "pending",
      assignee: "",
      deps: ["t1"],
      description: "Review <b>x</b>",
    },
  ],
  findings: [{ member: "coder", body: "Auth uses <b>x</b> tokens" }],
});

const fullFleet = fleetOf({
  subagents: [runningSubagent, doneSubagent],
  parallelGroups: [parallelGroup],
  teams: [teamBoard],
});

/** A controlled owner like chat-view: tab + focus state, spies on both. */
function Harness({
  fleet,
  initialTab = "subagents",
  initialFocus = null,
  teamsSupported,
  onFocusSpy,
  onCancelChild,
}: {
  fleet: DelegationFleet | undefined;
  initialTab?: DelegationTab;
  initialFocus?: DelegationFocus | null;
  teamsSupported?: boolean;
  onFocusSpy?: (focus: DelegationFocus | null) => void;
  onCancelChild?: (childId: string) => void;
}) {
  const [tab, setTab] = useState<DelegationTab>(initialTab);
  const [focus, setFocus] = useState<DelegationFocus | null>(initialFocus);
  return (
    <DelegationPanel
      fleet={fleet}
      tab={tab}
      focus={focus}
      onTabChange={(next) => {
        setTab(next);
        setFocus(null);
      }}
      onFocus={(next) => {
        onFocusSpy?.(next);
        setFocus(next);
      }}
      teamsSupported={teamsSupported}
      onCancelChild={onCancelChild}
      onClose={() => {}}
      maximized={false}
      onToggleMaximize={() => {}}
    />
  );
}

// The panel frame (SidePanel) persists its width in localStorage; this vitest
// environment's storage shim is method-less, so a real in-memory Storage is
// stubbed per test (the global afterEach unstubs it).
beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

afterEach(async () => {
  await resetHarnessClient();
});

describe("preferredDelegationTab", () => {
  it("prefers a live team, then a live fan-out, then whichever family has history", () => {
    expect(preferredDelegationTab(undefined)).toBe("subagents");
    expect(preferredDelegationTab(emptyFleet())).toBe("subagents");
    // A live team beats a live fan-out and any subagent history.
    expect(
      preferredDelegationTab(
        fleetOf({
          subagents: [runningSubagent],
          parallelGroups: [group()],
          teams: [team()],
        }),
      ),
    ).toBe("teams");
    // A finished team yields to a live fan-out.
    expect(
      preferredDelegationTab(
        fleetOf({
          subagents: [runningSubagent],
          parallelGroups: [group()],
          teams: [team({ done: true })],
        }),
      ),
    ).toBe("parallel");
    // Nothing live: subagent history first…
    expect(
      preferredDelegationTab(
        fleetOf({
          subagents: [doneSubagent],
          parallelGroups: [parallelGroup],
          teams: [team({ done: true })],
        }),
      ),
    ).toBe("subagents");
    // …then parallel history…
    expect(
      preferredDelegationTab(
        fleetOf({
          parallelGroups: [parallelGroup],
          teams: [team({ done: true })],
        }),
      ),
    ).toBe("parallel");
    // …then a finished team.
    expect(
      preferredDelegationTab(fleetOf({ teams: [team({ done: true })] })),
    ).toBe("teams");
  });
});

describe("teamTaskGlyph", () => {
  it("maps the task states to their glyphs, blocked-ness for pending", () => {
    expect(teamTaskGlyph("completed")).toBe("✓");
    expect(teamTaskGlyph("in_progress")).toBe("◆");
    expect(teamTaskGlyph("pending")).toBe("○");
    expect(teamTaskGlyph("pending", true)).toBe("⊘");
    // An unknown state reads as not-yet-done rather than vanishing.
    expect(teamTaskGlyph("weird")).toBe("○");
  });

  it("derives blocked from an unmet dependency of a pending task", () => {
    const tasks = teamBoard.tasks;
    expect(teamTaskBlocked(tasks[1], tasks)).toBe(true);
    expect(teamTaskBlocked(tasks[0], tasks)).toBe(false);
    expect(
      teamTaskBlocked(
        {
          id: "t3",
          state: "pending",
          assignee: "",
          deps: ["t0"],
          description: "",
        },
        [
          {
            id: "t0",
            state: "completed",
            assignee: "",
            deps: [],
            description: "",
          },
        ],
      ),
    ).toBe(false);
  });
});

describe("focusForDelegationCard", () => {
  it("lands a card click on its child, group, or member", () => {
    expect(focusForDelegationCard(runningSubagent)).toEqual({
      tab: "subagents",
      focus: { kind: "subagent", childId: "subagent-abc123def" },
    });
    expect(focusForDelegationCard(parallelGroup.branches[0])).toEqual({
      tab: "parallel",
      focus: { kind: "parallel-group", parentCallId: "call-p" },
    });
    expect(focusForDelegationCard(teamBoard.lanes[0])).toEqual({
      tab: "teams",
      focus: {
        kind: "team-member",
        parentCallId: "call-t",
        teamId: "t1",
        member: "coder",
      },
    });
    // A pre-start badge with no handle opens the family's roster.
    expect(focusForDelegationCard(card({ label: "pending" }))).toEqual({
      tab: "subagents",
      focus: null,
    });
  });
});

describe("DelegationPanel — subagents", () => {
  it("renders the empty state without a fleet", () => {
    render(<Harness fleet={undefined} />);
    expect(screen.getByRole("heading", { name: "Agents" })).toBeInTheDocument();
    expect(
      screen.getByText("No subagents have run in this chat yet."),
    ).toBeInTheDocument();
  });

  it("lists the fleet roster with running/done counts, #hash and the bg marker", () => {
    render(<Harness fleet={fullFleet} />);
    expect(
      screen.getByText("subagents · 1 running · 1 done"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("explore auth · #123def · Grep · 3 tools · 1.2k tok"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "write tests · #zzz999 · bg · done (max-turns) · 12s · 8 tools",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "running" })).toHaveClass(
      "animate-pulse",
    );
  });

  it("opens a focus pane with the id, trace chips and transcript on row click", () => {
    const onFocus = vi.fn();
    render(<Harness fleet={fullFleet} onFocusSpy={onFocus} />);
    fireEvent.click(
      screen.getByRole("button", { name: "Open subagent explore auth" }),
    );
    expect(onFocus).toHaveBeenLastCalledWith({
      kind: "subagent",
      childId: "subagent-abc123def",
    });
    expect(
      screen.getByRole("heading", { name: "subagent · explore auth" }),
    ).toBeInTheDocument();
    expect(screen.getByText("subagent-abc123def")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Copy child id" }),
    ).toBeInTheDocument();
    const trace = screen.getByRole("list", { name: "Activity trace" });
    const chips = within(trace).getAllByRole("listitem");
    expect(chips[0]).toHaveTextContent("✓ Read — src/auth.ts");
    expect(chips[1]).toHaveTextContent("… Grep — token");
    // A model-authored line renders literally: no <b> element is created.
    expect(chips[2]).toHaveTextContent("Looking at <b>x</b>");
    expect(trace.querySelector("b")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Open transcript" }),
    ).toBeInTheDocument();
  });

  it("titles an MCP tool Server · Tool on the roster row and its trace chip, exact id on hover", () => {
    const mcpSubagent = card({
      childId: "subagent-aaa111bbb",
      label: "triage",
      lastTool: "mcp__github__issue_write",
      toolCount: 1,
      trace: [
        {
          kind: "tool",
          name: "mcp__github__issue_write",
          detail: "#12",
          isError: false,
        },
      ],
    });
    render(<Harness fleet={fleetOf({ subagents: [mcpSubagent] })} />);
    expect(
      screen.getByText(/^triage · #\w+ · GitHub · Issue write · 1 tool$/),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Open subagent triage" }),
    );
    expect(
      screen.getByText(/running · GitHub · Issue write/),
    ).toBeInTheDocument();
    const trace = screen.getByRole("list", { name: "Activity trace" });
    const chip = within(trace).getAllByRole("listitem")[0];
    expect(chip).toHaveTextContent("✓ GitHub · Issue write — #12");
    expect(
      within(chip).getByTitle("mcp__github__issue_write"),
    ).toBeInTheDocument();
  });

  it("steps back to the roster through onFocus(null) — the Esc seam", () => {
    const onFocus = vi.fn();
    render(
      <Harness
        fleet={fullFleet}
        initialFocus={{ kind: "subagent", childId: "subagent-zzz999" }}
        onFocusSpy={onFocus}
      />,
    );
    // A finished background child names its delivery channel honestly.
    expect(
      screen.getByText(
        "background: done — result ready for the agent (SubagentStatus)",
      ),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back to roster" }));
    expect(onFocus).toHaveBeenLastCalledWith(null);
    expect(
      screen.getByText("subagents · 1 running · 1 done"),
    ).toBeInTheDocument();
  });

  it("offers Cancel for a running child only, and never without a handler", () => {
    const onCancelChild = vi.fn();
    const { unmount } = render(
      <Harness
        fleet={fullFleet}
        initialFocus={{ kind: "subagent", childId: "subagent-abc123def" }}
        onCancelChild={onCancelChild}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Cancel subagent explore auth" }),
    );
    expect(onCancelChild).toHaveBeenCalledWith("subagent-abc123def");
    unmount();

    render(
      <Harness
        fleet={fullFleet}
        initialFocus={{ kind: "subagent", childId: "subagent-zzz999" }}
        onCancelChild={onCancelChild}
      />,
    );
    expect(screen.queryByRole("button", { name: /^Cancel/ })).toBeNull();
    // The background note reads as done, so the pane has no cancel.
  });

  it("puts a cancel control on a running roster row that never opens the pane, disabled while a cancel is in flight", () => {
    const onCancelChild = vi.fn();
    const onFocus = vi.fn();
    const { unmount } = render(
      <Harness
        fleet={fullFleet}
        onCancelChild={onCancelChild}
        onFocusSpy={onFocus}
      />,
    );
    const rows = within(
      screen.getByRole("list", { name: "Subagents" }),
    ).getAllByRole("listitem");
    fireEvent.click(
      within(rows[0]).getByRole("button", {
        name: "Cancel subagent explore auth",
      }),
    );
    expect(onCancelChild).toHaveBeenCalledWith("subagent-abc123def");
    // The control is a sibling of the row button: cancelling opens nothing.
    expect(onFocus).not.toHaveBeenCalled();
    expect(
      within(rows[1]).queryByRole("button", { name: /^Cancel/ }),
    ).toBeNull();
    unmount();

    render(
      <Harness
        fleet={fleetOf({
          subagents: [{ ...runningSubagent, cancelling: true }],
        })}
        onCancelChild={onCancelChild}
      />,
    );
    const row = within(
      screen.getByRole("list", { name: "Subagents" }),
    ).getAllByRole("listitem")[0];
    expect(row).toHaveTextContent("cancelling…");
    expect(
      within(row).getByRole("button", { name: "Cancel subagent explore auth" }),
    ).toBeDisabled();
  });

  it("opens the child's transcript dialog on its session id", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/subagent-zzz999") {
        return sessionSnapshot("subagent-zzz999");
      }
      if (request.path === "/v1/sessions/subagent-zzz999/transcript") {
        return {
          session_id: "subagent-zzz999",
          complete: true,
          messages: [{ role: "assistant", text: "the child's summary" }],
        };
      }
      return undefined;
    });
    render(
      <Harness
        fleet={fullFleet}
        initialFocus={{ kind: "subagent", childId: "subagent-zzz999" }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("heading", { name: "write tests" }),
    ).toBeInTheDocument();
    expect(
      await within(dialog).findByText("the child's summary"),
    ).toBeInTheDocument();
  });
});

describe("DelegationPanel — parallel", () => {
  it("cancels a running branch from the group focus, and waits for a branch that has no child id yet", () => {
    const onCancelChild = vi.fn();
    const live = group({
      branches: [
        card({
          kind: "parallel",
          label: "slow",
          branchIndex: 0,
          parentCallId: "call-p",
          childId: "parallel-1",
        }),
        card({
          kind: "parallel",
          label: "fast",
          branchIndex: 1,
          parentCallId: "call-p",
        }),
      ],
    });
    render(
      <Harness
        fleet={fleetOf({ parallelGroups: [live] })}
        initialTab="parallel"
        initialFocus={{ kind: "parallel-group", parentCallId: "call-p" }}
        onCancelChild={onCancelChild}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Cancel parallel slow" }),
    );
    expect(onCancelChild).toHaveBeenCalledWith("parallel-1");
    const pending = screen.getByRole("button", {
      name: "Cancel parallel fast",
    });
    expect(pending).toBeDisabled();
    expect(pending.parentElement).toHaveAttribute(
      "title",
      "Branch id not known yet",
    );
  });

  it("switches tabs and lists each fan-out group with join, tally and ★ winner", () => {
    render(<Harness fleet={fullFleet} />);
    // Radix tabs activate on pointer-down (and Enter/Space for a keyboard).
    fireEvent.mouseDown(screen.getByRole("tab", { name: "Parallel" }));
    expect(
      screen.getByText("parallel · 0 running · 1 done"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("parallel 1 · join first · 2/2 branches done · ★ fast"),
    ).toBeInTheDocument();
  });

  it("opens the group focus with the run stop and every branch inline", () => {
    render(<Harness fleet={fullFleet} initialTab="parallel" />);
    fireEvent.click(
      screen.getByRole("button", { name: "Open parallel group 1" }),
    );
    expect(
      screen.getByRole("heading", { name: "parallel 1" }),
    ).toBeInTheDocument();
    expect(screen.getByText("run stop")).toBeInTheDocument();
    expect(screen.getByText("done")).toBeInTheDocument();
    const branches = within(
      screen.getByRole("list", { name: "Branches" }),
    ).getAllByRole("listitem");
    expect(branches).toHaveLength(2);
    // A cancelled branch keeps the compact stop label the inline card uses,
    // while its glyph marks the bad stop family.
    expect(branches[0]).toHaveTextContent("slow · done (cancelled) · 3s");
    expect(
      within(branches[0]).getByRole("img", { name: "failed" }),
    ).toBeTruthy();
    expect(branches[1]).toHaveTextContent("★ fast · done in 2s · 2 tools");
    // Each branch with a known child id can be opened.
    expect(
      within(branches[1]).getByRole("button", { name: "Open transcript" }),
    ).toBeInTheDocument();
  });
});

describe("DelegationPanel — teams", () => {
  it("renders the roster lead-first with live state, cues and a context meter", () => {
    render(<Harness fleet={fullFleet} initialTab="teams" />);
    expect(screen.getByText("team t1 · 1/2 working")).toBeInTheDocument();
    const rows = within(
      screen.getByRole("list", { name: "Team roster" }),
    ).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("lead (lead) [lead] · idle");
    expect(rows[1]).toHaveTextContent("coder (implementer) · Edit… · 4 tools");
    expect(rows[1]).toHaveTextContent("~42%");
    expect(
      within(rows[1]).getByRole("img", { name: "read-write" }),
    ).toBeInTheDocument();
    expect(
      within(rows[0]).getByRole("status", { name: "idle" }),
    ).not.toHaveClass("animate-pulse");
  });

  it("flips to the task board and the findings ledger, rendering previews literally", () => {
    const onFocus = vi.fn();
    render(
      <Harness fleet={fullFleet} initialTab="teams" onFocusSpy={onFocus} />,
    );
    fireEvent.click(screen.getByRole("radio", { name: "Tasks (2)" }));
    expect(onFocus).toHaveBeenLastCalledWith({
      kind: "team-view",
      parentCallId: "call-t",
      teamId: "t1",
      view: "tasks",
    });
    const tasks = within(
      screen.getByRole("list", { name: "Team tasks" }),
    ).getAllByRole("listitem");
    expect(tasks[0]).toHaveTextContent("◆ t1 · Write the tests · coder");
    // t2 is pending behind the in-progress t1: blocked, unassigned.
    expect(tasks[1]).toHaveTextContent("⊘ t2 · Review <b>x</b> · — · deps: t1");
    expect(tasks[1].querySelector("b")).toBeNull();

    fireEvent.click(screen.getByRole("radio", { name: "Findings (1)" }));
    const findings = within(
      screen.getByRole("list", { name: "Team findings" }),
    ).getAllByRole("listitem");
    expect(findings[0]).toHaveTextContent("coder: Auth uses <b>x</b> tokens");
    expect(findings[0].querySelector("b")).toBeNull();

    // Roster is the un-focused default: flipping back clears the focus.
    fireEvent.click(screen.getByRole("radio", { name: "Roster" }));
    expect(onFocus).toHaveBeenLastCalledWith(null);
    expect(
      screen.getByRole("list", { name: "Team roster" }),
    ).toBeInTheDocument();
  });

  it("opens a member focus with its context, trace and transcript", () => {
    render(<Harness fleet={fullFleet} initialTab="teams" />);
    fireEvent.click(
      screen.getByRole("button", { name: "Open team member coder" }),
    );
    expect(
      screen.getByRole("heading", { name: "member · coder (implementer)" }),
    ).toBeInTheDocument();
    expect(screen.getByText("team-t1-coder")).toBeInTheDocument();
    expect(screen.getByText("42.0k / 100.0k · 42%")).toBeInTheDocument();
    expect(screen.getByText("read-write")).toBeInTheDocument();
    expect(screen.getByText("(no activity yet)")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Open transcript" }),
    ).toBeInTheDocument();
  });

  it("summarises a finished team and its benched member", () => {
    const finished = team({
      done: true,
      rounds: 3,
      stop: "end_turn",
      inputTokens: 20_000,
      outputTokens: 5_000,
      lanes: [
        card({
          kind: "team",
          label: "tester",
          memberName: "tester",
          parentCallId: "call-t",
          stopped: true,
          stopReason: "error",
          stop: "error",
          cause: "provider 500",
        }),
      ],
    });
    render(
      <Harness fleet={fleetOf({ teams: [finished] })} initialTab="teams" />,
    );
    expect(
      screen.getByText("team t1 · 3 rounds · done · 25.0k tok · 1 stopped"),
    ).toBeInTheDocument();
    expect(screen.getByText(/tester · stopped — error/)).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Open team member tester" }),
    );
    expect(screen.getByText("provider 500")).toBeInTheDocument();
  });

  it("cancels a live member from the roster and its focus, waiting for the member id before enabling the control", () => {
    const onCancelChild = vi.fn();
    const onFocus = vi.fn();
    render(
      <Harness
        fleet={fullFleet}
        initialTab="teams"
        onCancelChild={onCancelChild}
        onFocusSpy={onFocus}
      />,
    );
    const rows = within(
      screen.getByRole("list", { name: "Team roster" }),
    ).getAllByRole("listitem");
    // The lead is idle between rounds yet its team is live, so it is
    // cancellable — but no team.member frame has named its session yet.
    const lead = within(rows[0]).getByRole("button", {
      name: "Cancel team lead (lead)",
    });
    expect(lead).toBeDisabled();
    expect(lead.parentElement).toHaveAttribute(
      "title",
      "Member id not known yet",
    );
    fireEvent.click(
      within(rows[1]).getByRole("button", {
        name: "Cancel team coder (implementer)",
      }),
    );
    expect(onCancelChild).toHaveBeenCalledWith("team-t1-coder");
    expect(onFocus).not.toHaveBeenCalled();

    fireEvent.click(
      screen.getByRole("button", { name: "Open team member lead" }),
    );
    expect(
      screen.getByRole("button", { name: "Cancel team lead (lead)" }),
    ).toBeDisabled();
    // No session id → no transcript to open either.
    expect(
      screen.queryByRole("button", { name: "Open transcript" }),
    ).toBeNull();
  });

  it("offers no member cancel once the team has ended", () => {
    const finished = team({
      done: true,
      stop: "end_turn",
      lanes: [
        card({
          kind: "team",
          label: "tester",
          memberName: "tester",
          parentCallId: "call-t",
          childId: "team-t1-tester",
          stop: "end_turn",
        }),
      ],
    });
    render(
      <Harness
        fleet={fleetOf({ teams: [finished] })}
        initialTab="teams"
        onCancelChild={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: /^Cancel/ })).toBeNull();
  });

  it("disables the Teams tab when the daemon has teams off and none ran", () => {
    render(<Harness fleet={fleetOf()} teamsSupported={false} />);
    const trigger = screen.getByRole("tab", { name: "Teams" });
    expect(trigger).toBeDisabled();
    expect(trigger.parentElement).toHaveAttribute(
      "title",
      "Teams are disabled on this daemon",
    );
  });

  it("keeps a team that did run openable even when teams are now off", () => {
    render(
      <Harness fleet={fullFleet} teamsSupported={false} initialTab="teams" />,
    );
    expect(screen.getByRole("tab", { name: "Teams" })).toBeEnabled();
    expect(screen.getByText("team t1 · 1/2 working")).toBeInTheDocument();
  });
});
