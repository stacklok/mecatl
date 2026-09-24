// @vitest-environment happy-dom
// SPDX-License-Identifier: Apache-2.0

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it } from "vitest";
import type { DelegationFocus } from "./delegation-card";
import type { SubagentActivity, TeamActivity } from "./delegation-fleet";
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

describe("session activity content", () => {
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
