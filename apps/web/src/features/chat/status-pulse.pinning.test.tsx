// @vitest-environment happy-dom
// SPDX-License-Identifier: Apache-2.0

/**
 * Pins the `animate-pulse` call sites #2337 reviewed in the delegation card,
 * the fleet chip, the session details view, the activity panel's roster, and
 * the reasoning disclosure. Each one is a status indicator (a child, a
 * family, a session, or the model's reasoning is running), not a loading
 * placeholder, so none of them becomes a Skeleton: the pulse shows only
 * while running, and the state itself is still spoken as text. The dots are
 * decorative (`aria-hidden`); the reasoning pulse is the "Reasoning…" label
 * itself, beside the live preview it labels. The details view's real loading
 * state is a spinner beside "Loading details…" in a status region.
 */

import type { SessionDetailResponse, SessionSummaryResponse } from "@mecatl-studio/contracts";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { DelegationCard } from "./delegation-card";
import type { DelegationState, SubagentActivity } from "./delegation-fleet";
import { createDelegationFleet } from "./delegation-fleet";
import { SessionActivityContent } from "./delegation-panel";
import { FleetStatusChip } from "./fleet-status-chip";
import { ReasoningDisclosure } from "./reasoning-disclosure";
import { SessionDetailsView } from "./session-details-dialog";

function dom(element: React.ReactElement): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = renderToStaticMarkup(element);
  return host;
}

function pulses(node: HTMLElement): Element[] {
  return [...node.querySelectorAll(".animate-pulse")];
}

function expectStatusPulse(node: HTMLElement) {
  const found = pulses(node);
  expect(found).toHaveLength(1);
  const pulse = found[0];
  expect(pulse?.getAttribute("aria-hidden")).toBe("true");
  expect(pulse?.classList.contains("rounded-full")).toBe(true);
  expect(pulse?.classList.contains("bg-brand")).toBe(true);
  expect(pulse?.textContent).toBe("");
  // A status dot, not the Skeleton primitive.
  expect(node.querySelector('[data-slot="skeleton"]')).toBeNull();
}

function subagent(state: DelegationState, stop?: string): SubagentActivity {
  return {
    key: `["session-a","run-a","subagent","call-a","child-${state}"]`,
    family: "subagent",
    sessionId: "session-a",
    runId: "run-a",
    parentCallId: "call-a",
    childId: `child-${state}`,
    goal: "Review the change",
    startObserved: true,
    historyIncomplete: false,
    state,
    stop,
    trace: { entries: [], omitted: 0 },
  };
}

const session: SessionSummaryResponse = {
  capabilities: {
    copyId: true,
    copyIdReason: "",
    delete: true,
    deleteReason: "",
    fork: true,
    forkReason: "",
    inspect: true,
    inspectReason: "",
    publicChat: true,
    publicChatReason: "",
    rename: true,
    renameReason: "",
    viewTranscript: true,
    viewTranscriptReason: "",
  },
  createdAt: "2026-09-24T12:00:00.000Z",
  debugTargetSessionId: "",
  id: "session-a",
  kind: "main",
  modelId: "model-a",
  state: "idle",
  title: "Main chat",
  titleProvenance: "",
  titleRevision: "0",
  turns: 2,
  updatedAt: "2026-09-24T12:00:00.000Z",
};

function detail(state: string): SessionDetailResponse {
  return {
    capabilities: {
      image: false,
      audio: false,
      executionFiles: false,
      builtInShell: false,
      manualCompaction: false,
      modelSelection: false,
    },
    id: "session-a",
    mode: "default",
    state,
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  } as SessionDetailResponse;
}

function details(props: { detail?: SessionDetailResponse; detailPending?: boolean }) {
  return dom(
    <SessionDetailsView
      canInspect
      detail={props.detail}
      detailError={false}
      detailPending={props.detailPending ?? false}
      inspectUnavailable="Unavailable here."
      notes={[]}
      onRetry={() => {}}
      session={session}
    />,
  );
}

describe("status pulses stay status indicators (#2337)", () => {
  it("pulses a delegation card's state dot only while the child runs", () => {
    const running = dom(<DelegationCard activity={subagent("running")} onOpen={() => {}} />);
    expectStatusPulse(running);
    expect(running.querySelector("button")?.textContent).toContain("Running");
    expect(running.querySelector('[aria-live="polite"]')?.textContent).toContain("Running");

    const finished = dom(
      <DelegationCard activity={subagent("finished", "end_turn")} onOpen={() => {}} />,
    );
    expect(pulses(finished)).toHaveLength(0);
    expect(finished.querySelector("button")?.textContent).toContain("Finished");
  });

  it("pulses a fleet chip segment only while that family has running work", () => {
    const fleet = {
      ...createDelegationFleet("session-a"),
      subagents: [subagent("running"), subagent("finished", "end_turn")],
    };
    const live = dom(<FleetStatusChip fleet={fleet} onOpen={() => {}} />);
    expect(live.querySelector("ul")?.getAttribute("aria-label")).toBe("Agents in this chat");
    const button = live.querySelector("button");
    expect(button?.getAttribute("aria-label")).toBe("Subagents · 1 running · 1 done");
    expect(button?.getAttribute("data-running")).toBe("true");
    expectStatusPulse(live);

    const settled = dom(
      <FleetStatusChip
        fleet={{ ...fleet, subagents: [subagent("finished", "end_turn")] }}
        onOpen={() => {}}
      />,
    );
    expect(settled.querySelector("button")?.getAttribute("aria-label")).toBe(
      "Subagents · 0 running · 1 done",
    );
    expect(settled.querySelector("button")?.hasAttribute("data-running")).toBe(false);
    expect(pulses(settled)).toHaveLength(0);
  });

  it("pulses the session details state dot only for a running session", () => {
    const running = details({ detail: detail("running") });
    expectStatusPulse(running);
    expect(running.textContent).toContain("running");

    for (const state of ["idle", "completed", "failed", "awaiting", "cancelled"]) {
      expect(pulses(details({ detail: detail(state) }))).toHaveLength(0);
    }
  });

  it("keeps the details loading state a spinner in a status region, not a pulse", () => {
    const pending = details({ detailPending: true });
    const status = pending.querySelector('[role="status"]');
    expect(status?.textContent).toBe("Loading details…");
    expect(status?.querySelector("svg.animate-spin")?.getAttribute("aria-hidden")).toBe("true");
    expect(pulses(pending)).toHaveLength(0);
    expect(pending.querySelector('[data-slot="skeleton"]')).toBeNull();
    expect(pending.querySelector("dl")).toBeNull();
  });

  it("pulses an activity roster row's dot only while that activity runs", () => {
    const fleet = {
      ...createDelegationFleet("session-a"),
      subagents: [subagent("running"), subagent("finished", "end_turn")],
    };
    const panel = dom(<SessionActivityContent fleet={fleet} onFocusChange={() => {}} />);
    const rows = [...panel.querySelectorAll("button[aria-pressed]")];
    expect(rows.map((row) => row.textContent)).toEqual([
      "Subagent child-running · Running",
      "Subagent child-finished · Finished",
    ]);
    const [running, finished] = rows as HTMLElement[];
    if (!running || !finished) throw new Error("expected two roster rows");
    expectStatusPulse(running);
    expect(pulses(finished)).toHaveLength(0);
    expect(pulses(panel)).toHaveLength(1);
    expect(panel.querySelector('[data-slot="skeleton"]')).toBeNull();

    const settled = dom(
      <SessionActivityContent
        fleet={{ ...fleet, subagents: [subagent("finished", "end_turn")] }}
        onFocusChange={() => {}}
      />,
    );
    expect(settled.querySelectorAll("button[aria-pressed]")).toHaveLength(1);
    expect(pulses(settled)).toHaveLength(0);
  });

  it("pulses the reasoning label only while reasoning streams, beside its live preview", () => {
    const text = "Check the config first.\nThen read the logs.";
    const streaming = dom(<ReasoningDisclosure streaming text={text} />);
    const found = pulses(streaming);
    expect(found).toHaveLength(1);
    const label = found[0];
    // A text label, read with the preview, not a decorative placeholder shape.
    expect(label?.textContent).toBe("Reasoning…");
    expect(label?.hasAttribute("aria-hidden")).toBe(false);
    expect(label?.classList.contains("font-medium")).toBe(true);
    expect(label?.nextElementSibling?.textContent).toBe("Then read the logs.");
    expect(streaming.querySelector('[data-slot="skeleton"]')).toBeNull();

    const settled = dom(<ReasoningDisclosure streaming={false} text={text} />);
    expect(pulses(settled)).toHaveLength(0);
    expect(settled.textContent).toContain("Reasoning summary · 2 lines");

    // No reasoning text yet: nothing renders, so there is no placeholder either.
    expect(dom(<ReasoningDisclosure streaming text="" />).innerHTML).toBe("");
  });
});
