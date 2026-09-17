import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
  type DelegationFleet,
  type DelegationInfo,
  emptyFleet,
  type ParallelGroupState,
  type TeamBoardState,
} from "@/features/agent";
import { FleetStatusChip, fleetSegments } from "./fleet-status-chip";

/**
 * The composer-strip fleet chip: the persistent aggregate glance mecatui's
 * footer segments give. `fleetSegments` decides what shows (per family,
 * from the shared `fleetCounts`); the component renders one button per
 * segment, each opening the Agents panel on its tab.
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
  branchCount: 3,
  winner: -1,
  stop: "",
  done: false,
  branches: [
    card({ kind: "parallel", branchIndex: 0, childId: "parallel-0" }),
    card({ kind: "parallel", branchIndex: 1, childId: "parallel-1" }),
    card({ kind: "parallel", branchIndex: 2, childId: "parallel-2" }),
  ],
  ...partial,
});

const team = (partial: Partial<TeamBoardState> = {}): TeamBoardState => ({
  parentCallId: "call-t",
  teamId: "t1",
  lanes: [
    card({ kind: "team", memberName: "lead", lead: true, lastTool: "Read" }),
    card({ kind: "team", memberName: "coder", idle: true }),
    card({ kind: "team", memberName: "tester", stopped: true }),
  ],
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

describe("fleetSegments", () => {
  it("is empty without a fleet or with nothing started", () => {
    expect(fleetSegments(undefined)).toEqual([]);
    expect(fleetSegments(emptyFleet())).toEqual([]);
  });

  it("counts a background subagent as running until its end frame", () => {
    const fleet = fleetOf({
      subagents: [
        card({ childId: "subagent-bg", background: true }),
        card({ childId: "subagent-done", stop: "end_turn" }),
      ],
    });
    expect(fleetSegments(fleet)).toEqual([
      {
        tab: "subagents",
        icon: "subagents",
        name: "subagents",
        running: 1,
        done: 1,
        label: "1 running · 1 done",
        compact: "1◐ 1✓",
      },
    ]);
  });

  it("counts parallel by fan-out group, not by branch", () => {
    const [live] = fleetSegments(fleetOf({ parallelGroups: [group()] }));
    expect(live).toMatchObject({
      tab: "parallel",
      icon: "parallel",
      running: 1,
      done: 0,
      label: "1 running · 0 done",
      compact: "1◐ 0✓",
    });
    const [ended] = fleetSegments(
      fleetOf({
        parallelGroups: [group({ done: true, stop: "end_turn", winner: 1 })],
      }),
    );
    expect(ended).toMatchObject({
      running: 0,
      done: 1,
      label: "0 running · 1 done",
    });
  });

  it("shows a live team as k/N working and sheds a finished one", () => {
    expect(fleetSegments(fleetOf({ teams: [team()] }))).toEqual([
      {
        tab: "teams",
        icon: "team",
        name: "team",
        running: 1,
        done: 0,
        label: "1/3 working",
        compact: "1/3",
      },
    ]);
    expect(
      fleetSegments(
        fleetOf({ teams: [team({ done: true, stop: "end_turn" })] }),
      ),
    ).toEqual([]);
  });

  it("orders the families subagents, parallel, team", () => {
    const fleet = fleetOf({
      subagents: [card({ childId: "s1" })],
      parallelGroups: [group()],
      teams: [team()],
    });
    expect(fleetSegments(fleet).map((segment) => segment.tab)).toEqual([
      "subagents",
      "parallel",
      "teams",
    ]);
  });
});

describe("FleetStatusChip", () => {
  it("renders nothing for an empty fleet", () => {
    const { container } = render(
      <FleetStatusChip fleet={emptyFleet()} onOpen={() => {}} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("renders one labelled segment per family and opens the clicked tab", () => {
    const onOpen = vi.fn();
    render(
      <FleetStatusChip
        fleet={fleetOf({
          subagents: [card({ childId: "s1", stop: "end_turn" })],
          parallelGroups: [group()],
          teams: [team()],
        })}
        onOpen={onOpen}
      />,
    );
    const subagents = screen.getByRole("button", {
      name: "subagents · 0 running · 1 done",
    });
    const parallel = screen.getByRole("button", {
      name: "parallel · 1 running · 0 done",
    });
    const teams = screen.getByRole("button", { name: "team · 1/3 working" });
    expect(screen.getAllByRole("button")).toHaveLength(3);
    // Both the long label and the compact glyphs are in the DOM; CSS picks
    // one per width, so the accessible name is the long form.
    expect(subagents).toHaveTextContent("0 running · 1 done");
    expect(subagents).toHaveTextContent("0◐ 1✓");
    // The pulse marks only segments with something still running.
    expect(subagents).not.toHaveAttribute("data-running");
    expect(parallel).toHaveAttribute("data-running", "true");
    expect(teams).toHaveAttribute("data-running", "true");

    fireEvent.click(parallel);
    expect(onOpen).toHaveBeenCalledWith("parallel");
    fireEvent.click(teams);
    expect(onOpen).toHaveBeenLastCalledWith("teams");
    fireEvent.click(subagents);
    expect(onOpen).toHaveBeenLastCalledWith("subagents");
  });
});
