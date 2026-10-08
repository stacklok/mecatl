// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type {
  DecideMemoryConsolidationPlanResponse,
  GenerateMemoryConsolidationPlanResponse,
} from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ConsolidateMemoryCard,
  describeMemoryConsolidationReceipt,
  formatUntilTime,
  isDreamInProgress,
  isStaleMemoryPlan,
} from "./consolidate-memory-card";

/**
 * The Consolidate memory card: a target the agent cannot consolidate
 * offered disabled; one confirmed button that generates the suggestion; the
 * change list with current values; whole-plan Apply/Dismiss; an open
 * decision (`dream_in_progress` or a dropped connection) locked to retrying
 * the SAME decision; a no-longer-valid plan (`dream_conflict`, vanished)
 * cleared with the consolidate-again notice; the plain result line.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

type ManualDream = Record<
  "project_memory" | "user_model",
  { decide: boolean; generate: boolean; unavailableReason?: string }
>;

const BOTH_AVAILABLE: ManualDream = {
  project_memory: { decide: true, generate: true },
  user_model: { decide: true, generate: true },
};

const mutationState = vi.hoisted(() => ({
  decideAnswers: [] as Array<() => unknown>,
  decideCalls: [] as Array<{ decision: string; planId: string }>,
  decideIndex: 0,
  generateCalls: [] as Array<{ target: string }>,
}));

function plan(): GenerateMemoryConsolidationPlanResponse {
  return {
    expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
    id: "plan1",
    operations: [
      {
        exactDuplicateEligible: true,
        kind: "merge",
        reason: "near-duplicates",
        replacement: {
          description: "Merged editor preference",
          value: "Uses vim with vim keybindings",
        },
        sources: [
          {
            description: "Older note",
            key: "editor_2",
            value: "Prefers vim keybindings",
          },
        ],
        survivor: {
          description: "Editor preference",
          key: "editor",
          value: "Uses vim",
        },
      },
    ],
    plannedOperationCount: 1,
    plannedSourceCount: 2,
    target: "user_model",
  };
}

function receipt(
  overrides: Partial<DecideMemoryConsolidationPlanResponse> = {},
): DecideMemoryConsolidationPlanResponse {
  return {
    applied: 1,
    conflicted: 1,
    disposition: "apply",
    failed: 0,
    id: "plan1",
    planned: 2,
    skipped: 0,
    target: "user_model",
    ...overrides,
  };
}

/** Routes each decision call, in order, to the given answers (the last one
 *  repeats). */
function stubDecide(...answers: Array<() => unknown>) {
  mutationState.decideAnswers = answers;
  mutationState.decideIndex = 0;
}

vi.mock("@mecatl-studio/contracts/query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@mecatl-studio/contracts/query")>()),
  decideMemoryConsolidationPlanMutation: () => ({
    mutationFn: async (variables: { body: { decision: string }; path: { planId: string } }) => {
      mutationState.decideCalls.push({
        decision: variables.body.decision,
        planId: variables.path.planId,
      });
      const answers = mutationState.decideAnswers;
      const answer = answers[Math.min(mutationState.decideIndex, answers.length - 1)];
      mutationState.decideIndex += 1;
      const result = answer?.();
      if (result instanceof Error) throw result;
      return result;
    },
  }),
  generateMemoryConsolidationPlanMutation: () => ({
    mutationFn: async (variables: { body: { target: string } }) => {
      mutationState.generateCalls.push(variables.body);
      return plan();
    },
  }),
  listUserMemoryQueryKey: () => ["memory-test"],
}));

let runtime: { capabilities: { manualDream?: unknown }; connection: string };

function setManualDream(manualDream: Partial<ManualDream>) {
  runtime = {
    capabilities: {
      manualDream: {
        projectMemory: manualDream.project_memory,
        userModel: manualDream.user_model,
      },
    },
    connection: "online",
  };
}

function renderCard() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, retryOnMount: false, staleTime: Number.POSITIVE_INFINITY },
    },
  });
  client.setQueryData(getRuntimeQueryKey(), runtime);
  return render(
    <QueryClientProvider client={client}>
      <ConsolidateMemoryCard />
    </QueryClientProvider>,
  );
}

/** The tests use plain DOM checks; Studio has no jest-dom matchers. */
const isDisabled = (element: Element) => (element as HTMLButtonElement).disabled;
const text = (element: Element | null) => element?.textContent ?? "";

const consolidateButton = () => screen.findByRole("button", { name: "Consolidate memory" });

async function generatePlan(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await consolidateButton());
  const dialog = await screen.findByRole("alertdialog");
  await user.click(within(dialog).getByRole("button", { name: "Continue" }));
  await screen.findByTestId("dream-plan-summary");
}

async function applyPlan(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "Apply" }));
  const dialog = await screen.findByRole("alertdialog");
  await user.click(within(dialog).getByRole("button", { name: "Apply" }));
}

beforeEach(() => {
  setManualDream(BOTH_AVAILABLE);
  mutationState.decideCalls = [];
  mutationState.generateCalls = [];
  stubDecide(() => receipt());
});

afterEach(cleanup);

describe("describeMemoryConsolidationReceipt", () => {
  const base = receipt({ applied: 2, conflicted: 1, planned: 4, skipped: 1 });

  it("says what merged, then only the buckets that are not empty", () => {
    expect(describeMemoryConsolidationReceipt(base)).toBe(
      "2 of 4 memories merged · 1 left alone · 1 skipped",
    );
    expect(
      describeMemoryConsolidationReceipt({
        ...base,
        conflicted: 0,
        failed: 2,
        skipped: 0,
      }),
    ).toBe("2 of 4 memories merged · 2 failed");
  });

  it("singularises one memory", () => {
    expect(
      describeMemoryConsolidationReceipt({
        ...base,
        applied: 1,
        conflicted: 0,
        planned: 1,
        skipped: 0,
      }),
    ).toBe("1 of 1 memory merged");
  });
});

describe("formatUntilTime", () => {
  const now = Date.parse("2026-10-08T12:00:00Z");

  it("names the remaining time in its largest whole unit", () => {
    expect(formatUntilTime(now + 30_000, now)).toBe("<1m");
    expect(formatUntilTime(now + 14 * 60_000 + 59_000, now)).toBe("14m");
    expect(formatUntilTime(now + 3 * 3_600_000 + 60_000, now)).toBe("3h");
    expect(formatUntilTime(now + 50 * 3_600_000, now)).toBe("2d");
  });

  it("is empty for a missing timestamp", () => {
    expect(formatUntilTime(0, now)).toBe("");
  });
});

describe("plan error classification", () => {
  it("treats a vanished or already-decided plan as stale, covering dream_conflict", () => {
    expect(isStaleMemoryPlan({ code: "dream_not_found", status: 404 })).toBe(true);
    expect(isStaleMemoryPlan({ code: "dream_conflict", status: 409 })).toBe(true);
    expect(isStaleMemoryPlan({ code: "dream_terminal_conflict", status: 409 })).toBe(true);
    expect(isStaleMemoryPlan({ status: 410 })).toBe(true);
    expect(isStaleMemoryPlan({ code: "internal_error", status: 500 })).toBe(false);
    expect(isStaleMemoryPlan(new TypeError("fetch failed"))).toBe(false);
  });

  it("treats dream_in_progress and a missing answer as still open", () => {
    expect(isDreamInProgress({ code: "dream_in_progress", status: 409 })).toBe(true);
    expect(isDreamInProgress(new TypeError("fetch failed"))).toBe(true);
    expect(isDreamInProgress(undefined)).toBe(true);
    expect(isDreamInProgress({ code: "internal_error", status: 500 })).toBe(false);
  });
});

describe("ConsolidateMemoryCard", () => {
  it("renders nothing without the manual_dream capability", () => {
    runtime = { capabilities: {}, connection: "online" };
    const { container } = renderCard();
    expect(container.innerHTML).toBe("");
  });

  it("offers a memory the agent cannot consolidate as disabled, without its reason, and picks a usable one by default", async () => {
    setManualDream({
      project_memory: { decide: true, generate: true },
      user_model: {
        decide: false,
        generate: false,
        unavailableReason: "target store is unavailable",
      },
    });
    renderCard();

    const picker = await screen.findByRole("combobox", { name: "Memory to consolidate" });
    expect((picker as HTMLSelectElement).value).toBe("project_memory");
    expect(screen.queryByTestId("dream-unavailable")).toBeNull();
    expect(isDisabled(await consolidateButton())).toBe(false);

    const disabled = within(picker).getByRole("option", {
      name: "Facts about you (unavailable)",
    });
    expect(isDisabled(disabled)).toBe(true);
    expect(isDisabled(within(picker).getByRole("option", { name: "Project memory" }))).toBe(false);
    expect(document.body.textContent).not.toMatch(/daemon|store/i);
  });

  it("submits the chosen target from the picker", async () => {
    const user = userEvent.setup();
    renderCard();
    const picker = await screen.findByRole("combobox", { name: "Memory to consolidate" });
    expect((picker as HTMLSelectElement).value).toBe("user_model");
    await user.selectOptions(picker, "project_memory");
    await generatePlan(user);
    expect(mutationState.generateCalls).toEqual([{ target: "project_memory" }]);
  });

  it("disables the button with a plain sentence when the only memory cannot be consolidated", async () => {
    setManualDream({
      user_model: {
        decide: false,
        generate: false,
        unavailableReason: "dream planner is unavailable",
      },
    });
    renderCard();
    expect(text(await screen.findByTestId("dream-unavailable"))).toContain(
      "Consolidation isn't available for this memory right now.",
    );
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.getByText("Facts about you")).toBeTruthy();
    expect(isDisabled(await consolidateButton())).toBe(true);
    expect(document.body.textContent).not.toMatch(/daemon|planner/i);
  });

  it("disables the button while the agent is offline", async () => {
    runtime = { ...runtime, connection: "offline" };
    renderCard();
    expect(isDisabled(await consolidateButton())).toBe(true);
  });

  it("confirms before generating, then submits the target; the shown suggestion replaces the button", async () => {
    const user = userEvent.setup();
    renderCard();

    await user.click(await consolidateButton());
    const dialog = await screen.findByRole("alertdialog");
    expect(text(dialog)).toContain("Consolidate memory?");
    expect(text(dialog)).toContain(
      "The agent reads everything it remembers and suggests which entries to merge. Nothing changes until you approve.",
    );
    expect(mutationState.generateCalls).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(mutationState.generateCalls).toHaveLength(0);

    await generatePlan(user);
    expect(mutationState.generateCalls[0]).toEqual({ target: "user_model" });
    expect(text(screen.getByTestId("dream-plan-summary"))).toMatch(
      /1 suggested change across 2 memories\. Apply or dismiss them all together\. Expires in 1[45]m\./,
    );
    expect(screen.queryByRole("button", { name: "Consolidate memory" })).toBeNull();
    expect(isDisabled(screen.getByRole("button", { name: "Apply" }))).toBe(false);
    expect(isDisabled(screen.getByRole("button", { name: "Dismiss" }))).toBe(false);
  });

  it("shows each change: the exact-duplicate badge, the merged text, and the current values it keeps and merges in", async () => {
    const user = userEvent.setup();
    renderCard();
    await generatePlan(user);

    const change = screen.getByRole("listitem");
    expect(within(change).getByText("exact duplicate")).toBeTruthy();
    expect(text(change)).toContain("near-duplicates");
    expect(text(change)).toContain("Keeps editor and merges in editor_2");
    expect(text(change)).toContain("Uses vim with vim keybindings");
    expect(text(change)).toContain("Merged editor preference");

    const summary = within(change).getByText("Show current values");
    const details = summary.closest("details");
    expect(details).not.toBeNull();
    await user.click(summary);
    expect(text(details)).toContain("Keeps editor");
    expect(text(details)).toContain("Uses vim");
    expect(text(details)).toContain("Merges in editor_2");
    expect(text(details)).toContain("Prefers vim keybindings");
  });

  it("confirms before applying and reports the receipt", async () => {
    const user = userEvent.setup();
    stubDecide(() => receipt({ applied: 2, conflicted: 0, failed: 1, planned: 3 }));
    renderCard();
    await generatePlan(user);

    await user.click(screen.getByRole("button", { name: "Apply" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(text(dialog)).toContain("Apply these changes?");
    expect(text(dialog)).toContain(
      "The memories are merged exactly as shown. Anything that changed in the meantime is left alone.",
    );
    expect(mutationState.decideCalls).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Apply" }));

    const receiptEl = await screen.findByTestId("dream-receipt");
    expect(mutationState.decideCalls).toEqual([{ decision: "apply", planId: "plan1" }]);
    expect(text(receiptEl)).toContain("Done: 2 of 3 memories merged · 1 failed.");
    expect(text(receiptEl)).toContain(
      "Some memories couldn’t be merged. The counts above are the final result.",
    );
  });

  it("dream_in_progress keeps the suggestion, disables the other decision, and Try again re-submits the identical decision", async () => {
    const user = userEvent.setup();
    stubDecide(
      () => Object.assign(new Error("still running"), { code: "dream_in_progress" }),
      () => receipt(),
    );
    renderCard();
    await generatePlan(user);

    await applyPlan(user);
    const notice = await screen.findByRole("status");
    expect(text(notice)).toContain(
      "The agent is still working on that. Press Try again to see the result.",
    );
    expect(screen.getByTestId("dream-plan-summary")).toBeTruthy();
    expect(isDisabled(screen.getByRole("button", { name: "Dismiss" }))).toBe(true);
    expect(mutationState.decideCalls).toHaveLength(1);

    // The retry: no confirmation dialog, the same {decision} to the same id.
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    const receiptEl = await screen.findByTestId("dream-receipt");
    expect(mutationState.decideCalls).toHaveLength(2);
    expect(mutationState.decideCalls[1]).toEqual({ decision: "apply", planId: "plan1" });

    expect(text(receiptEl)).toContain("Done: 1 of 2 memories merged · 1 left alone.");
    expect(text(receiptEl)).toContain(
      "Some memories changed while you were reviewing, so they were left alone.",
    );
    expect(screen.queryByTestId("dream-plan-summary")).toBeNull();
    expect(isDisabled(await consolidateButton())).toBe(false);
  });

  it("a connection dropped mid-decision locks to the same decision and Try again fetches the result", async () => {
    const user = userEvent.setup();
    stubDecide(
      () => new TypeError("fetch failed"),
      () => receipt({ disposition: "dismiss" }),
    );
    renderCard();
    await generatePlan(user);

    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    const notice = await screen.findByRole("status");
    expect(text(notice)).toContain("The agent is still working on that.");
    expect(isDisabled(screen.getByRole("button", { name: "Apply" }))).toBe(true);
    await user.click(screen.getByRole("button", { name: "Try again" }));
    const receiptEl = await screen.findByTestId("dream-receipt");
    expect(text(receiptEl)).toContain("Dismissed. Nothing changed.");
    expect(text(receiptEl)).not.toContain("Some memories changed");
    expect(screen.queryByTestId("dream-plan-summary")).toBeNull();
    expect(isDisabled(await consolidateButton())).toBe(false);
  });

  it("dream_conflict clears the suggestion with the consolidate-again notice", async () => {
    const user = userEvent.setup();
    stubDecide(() => Object.assign(new Error("conflict"), { code: "dream_conflict" }));
    renderCard();
    await generatePlan(user);

    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(text(await screen.findByRole("status"))).toContain(
      "That suggestion is no longer valid, so nothing changed. Consolidate again for a new one.",
    );
    expect(screen.queryByTestId("dream-plan-summary")).toBeNull();
    expect(isDisabled(await consolidateButton())).toBe(false);
  });

  it("a vanished plan (dream_not_found) clears with the same notice", async () => {
    const user = userEvent.setup();
    stubDecide(() => Object.assign(new Error("unknown plan"), { code: "dream_not_found" }));
    renderCard();
    await generatePlan(user);
    await applyPlan(user);
    expect(text(await screen.findByRole("status"))).toContain(
      "That suggestion is no longer valid, so nothing changed.",
    );
    expect(screen.queryByTestId("dream-plan-summary")).toBeNull();
  });

  it("an unclassified decide error is shown verbatim with the suggestion kept and both decisions available", async () => {
    const user = userEvent.setup();
    stubDecide(() => Object.assign(new Error("boom"), { code: "internal", status: 500 }));
    renderCard();
    await generatePlan(user);
    await applyPlan(user);
    expect(text(await screen.findByRole("alert"))).toContain("boom");
    expect(screen.getByTestId("dream-plan-summary")).toBeTruthy();
    expect(isDisabled(screen.getByRole("button", { name: "Apply" }))).toBe(false);
    expect(isDisabled(screen.getByRole("button", { name: "Dismiss" }))).toBe(false);
  });

  it("decide:false disables Apply with a plain sentence and notes it on the card", async () => {
    const user = userEvent.setup();
    setManualDream({
      user_model: {
        decide: false,
        generate: true,
        unavailableReason: "target store lacks reviewed atomic consolidation",
      },
    });
    renderCard();
    expect(text(await screen.findByTestId("dream-unavailable"))).toContain(
      "Suggestions for this memory can be viewed but not applied.",
    );
    await generatePlan(user);
    const apply = screen.getByRole("button", { name: "Apply" });
    expect(isDisabled(apply)).toBe(true);
    expect(document.body.textContent).not.toMatch(/daemon|atomic/i);
  });

  it("never names the daemon", () => {
    renderCard();
    expect(document.body.textContent).not.toMatch(/daemon|mecated/i);
  });
});
