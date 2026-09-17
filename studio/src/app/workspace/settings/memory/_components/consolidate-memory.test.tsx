import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "@/lib/harness/sdk";
import {
  type FetchStub,
  problemResponse,
  type RecordedRequest,
  stubHarnessFetch,
} from "@/lib/harness/sdk-test-stub";
import {
  ConsolidateMemoryCard,
  describeDreamReceipt,
} from "./consolidate-memory";

/**
 * The Consolidate memory card over the SDK-backed dream module: memories the
 * agent cannot consolidate offered disabled; one confirmed button that
 * generates the suggestion; the change list with current values; whole-plan
 * Apply / Dismiss; an open decision (`dream_in_progress` or a dropped
 * connection) locked to retrying the SAME decision; a no-longer-valid plan
 * (`dream_conflict`, vanished) cleared with the consolidate-again notice;
 * the plain result line. Nothing on the card names the daemon.
 */

const runtime = vi.hoisted(() => ({
  status: {
    connected: true,
    serverCapabilities: {} as Record<string, unknown>,
  },
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime.status,
}));

type TargetCapability = {
  generate: boolean;
  decide: boolean;
  unavailable_reason?: string;
};

function setManualDream(
  manualDream: Partial<
    Record<"user_model" | "project_memory", TargetCapability>
  >,
) {
  runtime.status = {
    connected: true,
    serverCapabilities: { manual_dream: manualDream },
  };
}

const BOTH_AVAILABLE = {
  user_model: { generate: true, decide: true },
  project_memory: { generate: true, decide: true },
};

/** A merge plan expiring a quarter of an hour out, as the agent sends it. */
const planBody = () => ({
  plan: {
    id: "plan1",
    target: "user_model",
    expires_at: { seconds: Math.floor(Date.now() / 1000) + 15 * 60 },
    planned_operation_count: 1,
    planned_source_count: 2,
    operations: [
      {
        kind: "merge",
        survivor: {
          key: "editor",
          value: "Uses vim",
          description: "Editor preference",
        },
        sources: [
          {
            key: "editor_2",
            value: "Prefers vim keybindings",
            description: "Older note",
          },
        ],
        replacement: {
          value: "Uses vim with vim keybindings",
          description: "Merged editor preference",
        },
        reason: "near-duplicates",
        exact_duplicate_eligible: true,
      },
    ],
  },
});

const receiptBody = (overrides: Record<string, unknown> = {}) => ({
  receipt: {
    id: "plan1",
    target: "user_model",
    disposition: "apply",
    planned_source_count: 2,
    applied_source_count: 1,
    conflicted_source_count: 1,
    skipped_source_count: 0,
    failed_source_count: 0,
    ...overrides,
  },
});

const isDecision = (request: RecordedRequest) =>
  request.path === "/v1/dream/plans/plan1/decision";

/** Answers generate with the plan and routes each decision POST, in order,
 *  to the given answers (the last one repeats). */
function stubDream(...decisionAnswers: Array<() => unknown>): FetchStub {
  let decisions = 0;
  return stubHarnessFetch((request) => {
    if (request.path === "/v1/dream/plans") return planBody();
    if (isDecision(request)) {
      const answer =
        decisionAnswers[Math.min(decisions, decisionAnswers.length - 1)];
      decisions += 1;
      return answer?.();
    }
    return undefined;
  });
}

const consolidateButton = () =>
  screen.getByRole("button", { name: "Consolidate memory" });

async function generatePlan(user: ReturnType<typeof userEvent.setup>) {
  await user.click(consolidateButton());
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
});

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("describeDreamReceipt", () => {
  const receipt = {
    id: "p",
    target: "user_model",
    disposition: "apply",
    planned: 4,
    applied: 2,
    conflicted: 1,
    skipped: 1,
    failed: 0,
  };

  it("says what merged, then only the buckets that are not empty", () => {
    expect(describeDreamReceipt(receipt)).toBe(
      "2 of 4 memories merged · 1 left alone · 1 skipped",
    );
    expect(
      describeDreamReceipt({
        ...receipt,
        conflicted: 0,
        skipped: 0,
        failed: 2,
      }),
    ).toBe("2 of 4 memories merged · 2 failed");
  });

  it("singularises one memory", () => {
    expect(
      describeDreamReceipt({
        ...receipt,
        planned: 1,
        applied: 1,
        conflicted: 0,
        skipped: 0,
      }),
    ).toBe("1 of 1 memory merged");
  });
});

describe("ConsolidateMemoryCard", () => {
  it("renders nothing without the manual_dream capability", () => {
    runtime.status = { connected: true, serverCapabilities: {} };
    const { container } = render(<ConsolidateMemoryCard />);
    expect(container).toBeEmptyDOMElement();
  });

  it("offers a memory the agent cannot consolidate as disabled, without its reason, and picks a usable one by default", async () => {
    const user = userEvent.setup();
    setManualDream({
      user_model: {
        generate: false,
        decide: false,
        unavailable_reason: "target store is unavailable",
      },
      project_memory: { generate: true, decide: true },
    });
    stubHarnessFetch(() => undefined);
    render(<ConsolidateMemoryCard />);

    const picker = screen.getByRole("combobox", {
      name: "Memory to consolidate",
    });
    expect(picker).toHaveTextContent("Project memory");
    expect(screen.queryByTestId("dream-unavailable")).not.toBeInTheDocument();
    expect(consolidateButton()).toBeEnabled();

    await user.click(picker);
    const disabled = await screen.findByRole("option", {
      name: "Facts about you (unavailable)",
    });
    expect(disabled).toHaveAttribute("aria-disabled", "true");
    expect(disabled).not.toHaveAttribute("title");
    expect(
      screen.getByRole("option", { name: "Project memory" }),
    ).not.toHaveAttribute("aria-disabled");
    expect(document.body.textContent).not.toMatch(/daemon|store/i);
  });

  it("disables the button with a plain sentence when the only memory cannot be consolidated", () => {
    setManualDream({
      user_model: {
        generate: false,
        decide: false,
        unavailable_reason: "dream planner is unavailable",
      },
    });
    render(<ConsolidateMemoryCard />);
    expect(screen.getByTestId("dream-unavailable")).toHaveTextContent(
      "Consolidation isn't available for this memory right now.",
    );
    expect(screen.getByText("Facts about you")).toBeInTheDocument();
    const button = consolidateButton();
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute(
      "title",
      "Consolidation isn't available for this memory right now.",
    );
    expect(document.body.textContent).not.toMatch(/daemon|planner/i);
  });

  it("confirms before generating, then POSTs the target; the shown suggestion replaces the button", async () => {
    const user = userEvent.setup();
    setManualDream({ user_model: { generate: true, decide: true } });
    const stub = stubDream();
    render(<ConsolidateMemoryCard />);

    await user.click(consolidateButton());
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Consolidate memory?");
    expect(dialog).toHaveTextContent("Nothing changes until you approve.");
    expect(stub.requests).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(stub.requests).toHaveLength(0);

    await generatePlan(user);
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/dream/plans",
      body: { target: "user_model" },
    });
    expect(screen.getByTestId("dream-plan-summary")).toHaveTextContent(
      /1 suggested change across 2 memories\. Apply or dismiss them all together\. Expires in 1[45]m\./,
    );
    expect(
      screen.queryByRole("button", { name: "Consolidate memory" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeEnabled();
  });

  it("shows each change: the exact-duplicate badge, the merged text, and the current values it keeps and merges in", async () => {
    const user = userEvent.setup();
    stubDream();
    render(<ConsolidateMemoryCard />);
    await generatePlan(user);

    const change = screen.getByRole("listitem");
    expect(within(change).getByText("exact duplicate")).toBeInTheDocument();
    expect(change).toHaveTextContent("near-duplicates");
    expect(change).toHaveTextContent("Keeps editor and merges in editor_2");
    expect(change).toHaveTextContent("Uses vim with vim keybindings");
    expect(change).toHaveTextContent("Merged editor preference");

    const summary = within(change).getByText("Show current values");
    const details = summary.closest("details");
    expect(details).not.toBeNull();
    await user.click(summary);
    expect(details).toHaveTextContent("Keeps editor");
    expect(details).toHaveTextContent("Uses vim");
    expect(details).toHaveTextContent("Editor preference");
    expect(details).toHaveTextContent("Merges in editor_2");
    expect(details).toHaveTextContent("Prefers vim keybindings");
    expect(details).toHaveTextContent("Older note");
  });

  it("dream_in_progress keeps the suggestion, disables the other decision, and Try again re-POSTs the identical decision", async () => {
    const user = userEvent.setup();
    const stub = stubDream(
      () => problemResponse(409, "dream_in_progress", "still running"),
      () => receiptBody(),
    );
    render(<ConsolidateMemoryCard />);
    await generatePlan(user);

    await applyPlan(user);
    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent(
      "The agent is still working on that. Press Try again to see the result.",
    );
    expect(screen.getByTestId("dream-plan-summary")).toBeInTheDocument();
    const dismiss = screen.getByRole("button", { name: "Dismiss" });
    expect(dismiss).toBeDisabled();
    expect(dismiss).toHaveAttribute(
      "title",
      "Waiting for the other decision to finish.",
    );
    expect(stub.requests.filter(isDecision)).toHaveLength(1);

    // The retry: no confirmation dialog, the same {decision} to the same id.
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    const receipt = await screen.findByTestId("dream-receipt");
    const retried = stub.requests.filter(isDecision);
    expect(retried).toHaveLength(2);
    expect(retried[1]).toMatchObject({
      method: "POST",
      path: "/v1/dream/plans/plan1/decision",
      body: { decision: "apply" },
    });
    expect(retried[1]?.body).toEqual(retried[0]?.body);

    expect(receipt).toHaveTextContent(
      "Done: 1 of 2 memories merged · 1 left alone.",
    );
    expect(receipt).toHaveTextContent(
      "Some memories changed while you were reviewing, so they were left alone.",
    );
    expect(screen.queryByTestId("dream-plan-summary")).not.toBeInTheDocument();
    expect(consolidateButton()).toBeEnabled();
  });

  it("a connection dropped mid-decision locks to the same decision and Try again fetches the result", async () => {
    const user = userEvent.setup();
    stubDream(
      () => {
        throw new TypeError("fetch failed");
      },
      () => receiptBody({ disposition: "dismiss" }),
    );
    render(<ConsolidateMemoryCard />);
    await generatePlan(user);

    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    const notice = await screen.findByRole("status");
    expect(notice).toHaveTextContent("The agent is still working on that.");
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    const receipt = await screen.findByTestId("dream-receipt");
    expect(receipt).toHaveTextContent("Dismissed. Nothing changed.");
    // A dismissal carries no per-memory outcome notes.
    expect(receipt).not.toHaveTextContent("Some memories changed");
    expect(screen.queryByTestId("dream-plan-summary")).not.toBeInTheDocument();
    expect(consolidateButton()).toBeEnabled();
  });

  it("dream_conflict clears the suggestion with the consolidate-again notice", async () => {
    const user = userEvent.setup();
    stubDream(() => problemResponse(412, "dream_conflict", "conflict"));
    render(<ConsolidateMemoryCard />);
    await generatePlan(user);

    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(await screen.findByRole("status")).toHaveTextContent(
      "That suggestion is no longer valid, so nothing changed. Consolidate again for a new one.",
    );
    expect(screen.queryByTestId("dream-plan-summary")).not.toBeInTheDocument();
    expect(consolidateButton()).toBeEnabled();
  });

  it("a vanished plan (dream_not_found) clears with the same notice", async () => {
    const user = userEvent.setup();
    stubDream(() => problemResponse(404, "dream_not_found", "unknown plan"));
    render(<ConsolidateMemoryCard />);
    await generatePlan(user);
    await applyPlan(user);
    expect(await screen.findByRole("status")).toHaveTextContent(
      "That suggestion is no longer valid, so nothing changed.",
    );
    expect(screen.queryByTestId("dream-plan-summary")).not.toBeInTheDocument();
  });

  it("an unclassified decide error is shown verbatim with the suggestion kept and both decisions available", async () => {
    const user = userEvent.setup();
    stubDream(() => problemResponse(500, "internal", "boom"));
    render(<ConsolidateMemoryCard />);
    await generatePlan(user);
    await applyPlan(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("boom");
    expect(screen.getByTestId("dream-plan-summary")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Apply" })).toBeEnabled(),
    );
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeEnabled();
  });

  it("decide:false disables Apply with a plain sentence and notes it on the card", async () => {
    const user = userEvent.setup();
    setManualDream({
      user_model: {
        generate: true,
        decide: false,
        unavailable_reason: "target store lacks reviewed atomic consolidation",
      },
    });
    stubDream();
    render(<ConsolidateMemoryCard />);
    expect(screen.getByTestId("dream-unavailable")).toHaveTextContent(
      "Suggestions for this memory can be viewed but not applied.",
    );
    await generatePlan(user);
    const apply = screen.getByRole("button", { name: "Apply" });
    expect(apply).toBeDisabled();
    expect(apply).toHaveAttribute(
      "title",
      "Suggestions for this memory can be viewed but not applied.",
    );
    expect(document.body.textContent).not.toMatch(/daemon|atomic/i);
  });
});
