// @vitest-environment happy-dom
// SPDX-License-Identifier: Apache-2.0

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it } from "vitest";
import { DelegationCard, DelegationCardRow, type DelegationFocus } from "./delegation-card";
import type { ParallelGroupActivity, SubagentActivity } from "./delegation-fleet";
import { createDelegationFleet } from "./delegation-fleet";
import { SessionActivityContent } from "./delegation-panel";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let container: HTMLDivElement | undefined;
let root: Root | undefined;

afterEach(async () => {
  if (root) await act(async () => root?.unmount());
  container?.remove();
  root = undefined;
  container = undefined;
});

async function mount(element: React.ReactNode) {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => root?.render(element));
  return container;
}

const subagent: SubagentActivity = {
  key: '["session-a","run-a","subagent","call-a","child-a"]',
  family: "subagent",
  sessionId: "session-a",
  runId: "run-a",
  parentCallId: "call-a",
  childId: "child-a",
  goal: "Review the change",
  startObserved: true,
  historyIncomplete: false,
  state: "finished",
  stop: "error",
  cause: "Tool failed",
  toolCount: 2,
  currentTool: "Read",
  trace: { entries: [], omitted: 0 },
};

const parallel: ParallelGroupActivity = {
  key: '["session-a","run-a","parallel","call-p"]',
  family: "parallel",
  sessionId: "session-a",
  runId: "run-a",
  parentCallId: "call-p",
  startObserved: true,
  historyIncomplete: false,
  state: "finished",
  join: "first",
  branchCount: 2,
  winner: 0,
  branches: [
    {
      key: '["session-a","run-a","parallel","call-p",0]',
      branchIndex: 0,
      startObserved: true,
      historyIncomplete: false,
      state: "finished",
      label: "Fast path",
      stop: "end_turn",
      failed: false,
      toolCount: 1,
      currentTool: "Read",
      trace: { entries: [], omitted: 0 },
    },
    {
      key: '["session-a","run-a","parallel","call-p",1]',
      branchIndex: 1,
      startObserved: true,
      historyIncomplete: false,
      state: "finished",
      label: "Fallback",
      stop: "error",
      failed: true,
      trace: { entries: [], omitted: 0 },
    },
  ],
};

describe("delegation cards", () => {
  it("renders a subagent card with its observed outcome", async () => {
    let focused: DelegationFocus | undefined;
    let opener: HTMLButtonElement | undefined;
    const node = await mount(
      <DelegationCard
        activity={subagent}
        onOpen={(focus, button) => {
          focused = focus;
          opener = button;
        }}
      />,
    );
    const button = node.querySelector("button");
    expect(button).not.toBeNull();
    expect(button?.getAttribute("aria-label")).toBeNull();
    expect(button?.textContent).toContain("Subagent");
    expect(button?.textContent).toContain("child-a");
    expect(button?.textContent).toContain("Failed");
    expect(button?.textContent).toContain("Stop: error");
    expect(button?.textContent).toContain("Tools: 2");
    expect(button?.textContent).toContain("Read");

    await act(async () => button?.click());
    expect(focused).toEqual({ family: "subagent", key: subagent.key });
    expect(opener).toBe(button);
    const fleet = { ...createDelegationFleet("session-a"), subagents: [subagent] };
    const detail = renderToStaticMarkup(
      <SessionActivityContent fleet={fleet} focus={focused} onFocusChange={() => {}} />,
    );
    expect(detail).toContain("Tool failed");
    expect(detail).toContain("Stop: error");

    await act(async () =>
      root?.render(
        <DelegationCard
          activity={{ ...subagent, cause: undefined, stop: "end_turn" }}
          onOpen={() => {}}
        />,
      ),
    );
    expect(node.querySelector("button")?.textContent).toContain("Finished");
    expect(node.querySelector("button")?.textContent).toContain("Stop: end_turn");
    expect(node.querySelector("button")?.textContent).not.toContain("Failed");
  });

  it("renders parallel group and branch status distinctly from a subagent", () => {
    const card = renderToStaticMarkup(
      <DelegationCardRow activities={[parallel]} onOpen={() => {}} />,
    );
    expect(card).toContain("Parallel");
    expect(card).toContain("Join: first");
    expect(card).toContain("Winner: Branch 1");
    expect(card).toContain("Branch 1");
    expect(card).toContain("Fast path");
    expect(card).toContain("Tools: 1");
    expect(card).toContain("Current tool: Read");
    expect(card).toContain("Branch 2");
    expect(card).toContain("Fallback");
    expect(card).toContain("Failed");
    expect(card).toContain("Finished");
    expect(card).not.toContain("Subagent");

    const fleet = { ...createDelegationFleet("session-a"), parallelGroups: [parallel] };
    const detail = renderToStaticMarkup(
      <SessionActivityContent
        fleet={fleet}
        focus={{ family: "parallel", key: parallel.key }}
        onFocusChange={() => {}}
      />,
    );
    expect(detail).toContain("Join: first");
    expect(detail).toContain("Winner: Branch 1");
    expect(detail).toContain("Fast path");
    expect(detail).toContain("Fallback");
    expect(detail).toContain("Failed");
    expect(detail).toContain("Finished");
    expect(detail).not.toContain("Subagent child");

    const undecided = renderToStaticMarkup(
      <DelegationCard
        activity={{ ...parallel, state: "running", winner: undefined }}
        onOpen={() => {}}
      />,
    );
    expect(undecided).not.toContain("Winner:");
  });
});
