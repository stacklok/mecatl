// @vitest-environment happy-dom
// SPDX-License-Identifier: Apache-2.0

import { type RunStreamEvent, runStreamEventSchema } from "@mecatl-studio/contracts";
import { act, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it } from "vitest";
import { type ContentPreview, ContentPreviewPanel } from "./content-preview-panel";
import type { DelegationFocus } from "./delegation-card";
import { DelegationCardRow } from "./delegation-card";
import type { ParallelGroupActivity, SubagentActivity, TeamActivity } from "./delegation-fleet";
import { applyDelegationDelivery, createDelegationFleet } from "./delegation-fleet";
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

const leadMemberKey = '["session-a","run-a","team","call-t","team-a","lead"]';
const workerMemberKey = '["session-a","run-a","team","call-t","team-a","worker"]';

const team: TeamActivity = {
  key: '["session-a","run-a","team","call-t","team-a"]',
  family: "team",
  sessionId: "session-a",
  runId: "run-a",
  parentCallId: "call-t",
  teamId: "team-a",
  startObserved: true,
  historyIncomplete: false,
  state: "finished",
  stop: "error",
  members: [
    {
      key: leadMemberKey,
      name: "lead",
      role: "Coordinator <img src=x onerror=alert(1)>",
      lead: true,
      state: "finished",
      disposition: "done",
      roundState: "completed",
      errorRounds: 0,
      trace: {
        entries: [{ kind: "message.delta", text: "Checked <script>bad()</script>" }],
        omitted: 3,
      },
    },
    {
      key: workerMemberKey,
      name: "worker",
      role: "Reviewer",
      state: "finished",
      disposition: "stopped",
      reason: "budget",
      roundState: "failed",
      errorRounds: 2,
      trace: { entries: [], omitted: 0 },
    },
  ],
  tasks: [
    {
      id: "task-1",
      description: "Review <a href=javascript:alert(1)>files</a>",
      state: "blocked",
      assignee: "worker",
      deps: ["task-0"],
    },
  ],
  findings: [{ member: "lead", body: "Found <img src=x onerror=alert(2)>" }],
};

const subagent: SubagentActivity = {
  key: '["session-a","run-a","subagent","call-s","child-s"]',
  family: "subagent",
  sessionId: "session-a",
  runId: "run-a",
  parentCallId: "call-s",
  childId: "child-s",
  goal: "Inspect <a href=javascript:alert(3)>this</a>",
  startObserved: true,
  historyIncomplete: false,
  state: "finished",
  stop: "error <script>bad()</script>",
  cause: "Failure <img src=x onerror=alert(4)>",
  trace: {
    entries: [{ kind: "tool.result", detail: "<img src=x onerror=alert(5)>" }],
    omitted: 2,
  },
};

const parallel: ParallelGroupActivity = {
  key: '["session-a","run-a","parallel","call-p"]',
  family: "parallel",
  sessionId: "session-a",
  runId: "run-a",
  parentCallId: "call-p",
  startObserved: true,
  historyIncomplete: false,
  state: "running",
  join: "first",
  branchCount: 1,
  branches: [
    {
      key: '["session-a","run-a","parallel","call-p",0]',
      branchIndex: 0,
      startObserved: true,
      historyIncomplete: false,
      state: "running",
      trace: { entries: [], omitted: 0 },
    },
  ],
};

describe("session activity content", () => {
  it("keeps activity usable at mobile widths and bounds trace rows", async () => {
    function delivery(kind: string, seq: number, payload: unknown): RunStreamEvent {
      return {
        event: {
          kind,
          payload,
          runId: "run-a",
          seq: String(seq),
          text: "",
          turn: 1,
          unknown: false,
        },
        type: "run.event",
      };
    }
    let fleet = createDelegationFleet("session-a");
    fleet = applyDelegationDelivery(
      fleet,
      delivery("subagent.start", 1, {
        childId: "child-s",
        goal: "Read",
        parentCallId: "call-s",
      }),
    );
    for (let index = 0; index < 16; index += 1) {
      fleet = applyDelegationDelivery(
        fleet,
        delivery("subagent.tool", index + 2, {
          childId: "child-s",
          detail: `trace-${index} ${"🧪".repeat(250)}`,
          innerKind: "tool.result",
          parentCallId: "call-s",
          toolCount: index + 1,
        }),
      );
    }
    const activity = fleet.subagents[0];
    expect(activity).toBeDefined();
    if (!activity) throw new Error("subagent activity missing");
    const node = await mount(
      <SessionActivityContent
        fleet={fleet}
        focus={{ family: "subagent", key: activity.key }}
        onFocusChange={() => {}}
      />,
    );
    const panel = node.querySelector('[role="tabpanel"]');
    const trace = node.querySelector('[aria-label="Recent trace"]');
    expect(node.querySelector('[role="tablist"]')).not.toBeNull();
    const subagentsTab = node.querySelector('[role="tab"]') as HTMLButtonElement;
    expect(subagentsTab.getAttribute("aria-label")).toBe("Subagents (1)");
    expect(subagentsTab.children).toHaveLength(2);
    expect(subagentsTab.children[0]?.textContent).toBe("Subagents");
    expect(subagentsTab.children[1]?.textContent).toBe("(1)");
    expect(panel?.textContent).toContain("Roster");
    expect(panel?.textContent).toContain("Subagent child-s");
    expect(trace?.getAttribute("aria-live")).toBe("off");
    expect(node.querySelector('[aria-live="polite"]')?.textContent).toContain("Running");
    expect(trace?.textContent).toContain("Older entries omitted: 4");
    const rows = trace?.querySelectorAll("ol > li") ?? [];
    expect(rows).toHaveLength(12);
    expect(rows[0]?.textContent).toContain("trace-4");
    expect(rows[11]?.textContent).toContain("trace-15");
    expect(trace?.textContent).not.toContain("trace-3");
    for (const row of rows) {
      const detail = row.querySelector("p:last-child")?.textContent ?? "";
      expect(Array.from(detail.replace(/^Detail: /, ""))).toHaveLength(201);
      expect(detail.endsWith("…")).toBe(true);
    }
  });

  it("opens activity from a card and restores focus on close", async () => {
    const fleet = {
      ...createDelegationFleet("session-a"),
      subagents: [subagent],
      parallelGroups: [parallel],
      teams: [team],
    };
    function Journey() {
      const [preview, setPreview] = useState<ContentPreview>();
      const [focus, setFocus] = useState<DelegationFocus>();
      const [focusRequest, setFocusRequest] = useState(0);
      const [revision, setRevision] = useState(0);
      const opener = useRef<HTMLButtonElement>(null);
      const openerFocus = useRef<DelegationFocus>(undefined);
      const sessionControl = useRef<HTMLButtonElement>(null);
      return (
        <>
          <button
            onClick={(event) => {
              opener.current = event.currentTarget;
              openerFocus.current = undefined;
              setFocus(undefined);
              setFocusRequest((value) => value + 1);
              setPreview({ kind: "activity" });
            }}
            ref={sessionControl}
            type="button"
          >
            Session activity
          </button>
          <DelegationCardRow
            activities={[subagent, parallel, team]}
            key={revision}
            onOpen={(next, button) => {
              opener.current = button;
              openerFocus.current = next;
              setFocus(next);
              setFocusRequest((value) => value + 1);
              setPreview({ kind: "activity" });
            }}
          />
          <button onClick={() => setRevision((value) => value + 1)} type="button">
            Refresh transcript rows
          </button>
          {preview && (
            <ContentPreviewPanel
              activity={{
                fallbackOpener: sessionControl.current,
                fleet,
                focus,
                focusRequest,
                onFocusChange: setFocus,
                opener: opener.current,
                openerFocus: openerFocus.current,
              }}
              canvas=""
              onCanvasChange={() => {}}
              onClose={() => setPreview(undefined)}
              preview={preview}
            />
          )}
        </>
      );
    }
    const node = await mount(<Journey />);
    const card = node.querySelector("fieldset button") as HTMLButtonElement;
    card.focus();
    await act(async () => card.click());
    expect(node.querySelector('aside[aria-label="Session activity"]')).not.toBeNull();
    expect(document.activeElement?.textContent).toBe("Subagent child-s");
    const teamTab = [...node.querySelectorAll('[role="tab"]')].find((tab) =>
      tab.textContent?.includes("Teams"),
    ) as HTMLButtonElement;
    await act(async () => teamTab.click());
    expect(teamTab.getAttribute("aria-selected")).toBe("true");
    await act(async () =>
      node
        .querySelector('aside[aria-label="Session activity"]')
        ?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" })),
    );
    expect(node.querySelector('aside[aria-label="Session activity"]')).toBeNull();
    expect(document.activeElement).toBe(card);

    await act(async () => card.click());
    const closeButton = node.querySelector(
      'button[aria-label="Close preview"]',
    ) as HTMLButtonElement;
    await act(async () => closeButton.click());
    expect(document.activeElement).toBe(card);

    await act(async () => card.click());
    const refresh = [...node.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("Refresh transcript rows"),
    ) as HTMLButtonElement;
    await act(async () => refresh.click());
    expect(card.isConnected).toBe(false);
    const replacement = node.querySelector("fieldset button") as HTMLButtonElement;
    await act(async () =>
      (
        node.querySelector(
          'aside[aria-label="Session activity"] button[aria-label="Close preview"]',
        ) as HTMLButtonElement
      ).click(),
    );
    expect(document.activeElement).toBe(replacement);

    await act(async () => replacement.click());

    const control = [...node.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("Session activity"),
    ) as HTMLButtonElement;
    control.focus();
    await act(async () => control.click());
    expect(
      node.querySelector('aside[aria-label="Session activity"]')?.contains(document.activeElement),
    ).toBe(true);
    expect(document.activeElement?.textContent).toBe("Session activity");
    await act(async () =>
      node
        .querySelector('aside[aria-label="Session activity"]')
        ?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" })),
    );
    expect(document.activeElement).toBe(control);

    const parallelCard = [...node.querySelectorAll("fieldset button")].find((button) =>
      button.textContent?.includes("Parallel group"),
    ) as HTMLButtonElement;
    parallelCard.focus();
    await act(async () => parallelCard.click());
    expect(document.activeElement?.textContent).toBe("Parallel group call-p");
    expect(node.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toContain(
      "Parallel",
    );
    await act(async () =>
      node
        .querySelector('aside[aria-label="Session activity"]')
        ?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" })),
    );
    expect(document.activeElement).toBe(parallelCard);

    const workerCard = [...node.querySelectorAll("fieldset button")].find((button) =>
      button.textContent?.includes("Member worker"),
    ) as HTMLButtonElement;
    workerCard.focus();
    await act(async () => workerCard.click());
    expect(document.activeElement?.textContent).toBe("Member worker");
    expect(node.querySelector('[role="tab"][aria-selected="true"]')?.textContent).toContain(
      "Teams",
    );
    expect(node.querySelector('aside[aria-label="Session activity"]')?.textContent).toContain(
      "Stopped: budget",
    );
    await act(async () =>
      node
        .querySelector('aside[aria-label="Session activity"]')
        ?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" })),
    );
    expect(document.activeElement).toBe(workerCard);
  });

  it("shows only observed Parallel and Team facts after missing starts", () => {
    let fleet = createDelegationFleet("session-a");
    for (const delivery of [
      {
        kind: "parallel.branch",
        payload: {
          branchIndex: 0,
          detail: "Observed branch detail",
          innerKind: "tool.result",
          kind: "branch_tool",
          parentCallId: "call-p",
        },
      },
      {
        kind: "team.member",
        payload: {
          innerKind: "tool.call",
          member: "worker",
          parentCallId: "call-t",
          teamId: "team-a",
          toolName: "Read",
        },
      },
      {
        kind: "team.tasks",
        payload: {
          parentCallId: "call-t",
          tasks: [
            {
              assignee: "worker",
              deps: [],
              description: "Observed task",
              id: "task-1",
              state: "pending",
            },
          ],
          teamId: "team-a",
        },
      },
      {
        kind: "team.findings",
        payload: {
          findings: [{ body: "Observed finding", member: "worker" }],
          parentCallId: "call-t",
          teamId: "team-a",
        },
      },
    ].map(
      (item, index): RunStreamEvent => ({
        type: "run.event",
        event: {
          ...item,
          runId: "run-a",
          seq: String(index + 1),
          text: "",
          turn: 1,
          unknown: false,
        },
      }),
    )) {
      fleet = applyDelegationDelivery(fleet, delivery);
    }
    fleet = applyDelegationDelivery(fleet, { cursor: "", reason: "gap", type: "run.truncated" });
    const group = fleet.parallelGroups[0];
    const branch = group?.branches[0];
    const teamActivity = fleet.teams[0];
    const member = teamActivity?.members[0];
    if (!group || !branch || !teamActivity || !member) throw new Error("partial activity missing");
    const parallelHtml = renderToStaticMarkup(
      <SessionActivityContent
        fleet={fleet}
        focus={{ family: "parallel", key: group.key, branchKey: branch.key }}
        onFocusChange={() => {}}
      />,
    );
    expect(parallelHtml).toContain("History incomplete: start event not observed");
    expect(parallelHtml).toContain("Outcome unknown");
    expect(parallelHtml).toContain("Observed branch detail");
    expect(parallelHtml).not.toContain("Winner:");
    expect(parallelHtml).not.toContain("Join:");
    const teamHtml = renderToStaticMarkup(
      <SessionActivityContent
        fleet={fleet}
        focus={{ family: "team", key: teamActivity.key, memberKey: member.key }}
        onFocusChange={() => {}}
      />,
    );
    expect(teamHtml).toContain("History incomplete: start event not observed");
    expect(teamHtml).toContain("Outcome unknown");
    expect(teamHtml).toContain("Observed task");
    expect(teamHtml).toContain("Observed finding");
    expect(teamHtml).toContain("Current tool: Read");
    expect(teamHtml).not.toContain("Done");
    expect(teamHtml).not.toContain("Stopped:");
  });

  it("omits raw event and child location fields from cards and panel", () => {
    const rawSentinel = "RAW_ONLY_SENTINEL_1849";
    const workspaceSentinel = "CHILD_WORKSPACE_SENTINEL_1849";
    const pathSentinel = "CHILD_PATH_SENTINEL_1849";
    const delivery = runStreamEventSchema.parse({
      type: "run.event",
      event: {
        kind: "subagent.start",
        runId: "run-a",
        seq: "1",
        text: "",
        turn: 1,
        unknown: false,
        raw: { secret: rawSentinel },
        payload: {
          childId: "child-a",
          goal: "Observed goal",
          parentCallId: "call-a",
          childWorkspace: workspaceSentinel,
          childPath: pathSentinel,
        },
      },
    });
    const fleet = applyDelegationDelivery(createDelegationFleet("session-a"), delivery);
    const child = fleet.subagents[0];
    if (!child) throw new Error("subagent activity missing");
    const card = renderToStaticMarkup(<DelegationCardRow activities={[child]} onOpen={() => {}} />);
    const panel = renderToStaticMarkup(
      <SessionActivityContent
        fleet={fleet}
        focus={{ family: "subagent", key: child.key }}
        onFocusChange={() => {}}
      />,
    );
    for (const sentinel of [rawSentinel, workspaceSentinel, pathSentinel]) {
      expect(JSON.stringify(fleet)).not.toContain(sentinel);
      expect(card).not.toContain(sentinel);
      expect(panel).not.toContain(sentinel);
    }
    expect(card).toContain("Observed goal");
    expect(panel).toContain("Observed goal");
  });
  it("renders team roster tasks and findings as plain text", async () => {
    const fleet = { ...createDelegationFleet("session-a"), teams: [team] };
    let focused: DelegationFocus | undefined;
    const node = await mount(
      <SessionActivityContent
        fleet={fleet}
        focus={{ family: "team", key: team.key, memberKey: leadMemberKey }}
        onFocusChange={(focus) => {
          focused = focus;
        }}
      />,
    );
    expect(node.textContent).toContain("Team team-a");
    expect(document.activeElement?.textContent).toBe("Member lead");
    expect(node.textContent).toContain("Roster");
    expect(node.textContent).toContain("lead");
    expect(node.textContent).toContain("Coordinator <img src=x onerror=alert(1)>");
    expect(node.textContent).toContain("Lead");
    expect(node.textContent).toContain("worker");
    expect(node.textContent).toContain("Stopped: budget");
    expect(node.textContent).toContain("Tasks");
    expect(node.textContent).toContain("Review <a href=javascript:alert(1)>files</a>");
    expect(node.textContent).toContain("Dependencies: task-0");
    expect(node.textContent).toContain("Findings");
    expect(node.textContent).toContain("Found <img src=x onerror=alert(2)>");
    expect(node.textContent).toContain("Stop: error");
    expect(node.querySelector("img")).toBeNull();
    expect(node.querySelector("a")).toBeNull();
    expect(node.querySelector("script")).toBeNull();

    const workerRow = [...node.querySelectorAll("button")].find((button) =>
      button.textContent?.includes("worker"),
    );
    expect(workerRow).toBeDefined();
    await act(async () => workerRow?.click());
    expect(focused).toEqual({ family: "team", key: team.key, memberKey: workerMemberKey });
    await act(async () =>
      root?.render(
        <SessionActivityContent fleet={fleet} focus={focused} onFocusChange={() => {}} />,
      ),
    );
    expect(document.activeElement?.textContent).toBe("Member worker");
    workerRow?.focus();
    await act(async () =>
      root?.render(
        <SessionActivityContent
          fleet={{ ...fleet, teams: [{ ...team, rounds: 4 }] }}
          focus={focused}
          onFocusChange={() => {}}
        />,
      ),
    );
    expect(document.activeElement).toBe(workerRow);
  });

  it("does not render child cancellation or execute model influenced markup", () => {
    const fleet = {
      ...createDelegationFleet("session-a"),
      subagents: [subagent],
      teams: [team],
    };
    for (const focus of [
      { family: "subagent", key: subagent.key },
      { family: "team", key: team.key, memberKey: leadMemberKey },
    ] as DelegationFocus[]) {
      const html = renderToStaticMarkup(
        <SessionActivityContent fleet={fleet} focus={focus} onFocusChange={() => {}} />,
      );
      expect(html).not.toMatch(/<(?:a|img|script)\b/i);
      const parsed = document.createElement("div");
      parsed.innerHTML = html;
      const childControls = [...parsed.querySelectorAll("button, a, [role=button]")].filter(
        (node) =>
          /(?:cancel|stop)\s+(?:child|subagent|branch|team|member)/i.test(
            [node.textContent, node.getAttribute("aria-label"), node.getAttribute("title")].join(
              " ",
            ),
          ),
      );
      expect(childControls).toHaveLength(0);
      expect(html).toContain("&lt;");
    }
    const subagentHtml = renderToStaticMarkup(
      <SessionActivityContent
        fleet={fleet}
        focus={{ family: "subagent", key: subagent.key }}
        onFocusChange={() => {}}
      />,
    );
    expect(subagentHtml).toContain("Older entries omitted: 2");
    expect(subagentHtml).toContain("&lt;img src=x onerror=alert(5)&gt;");
  });
});
