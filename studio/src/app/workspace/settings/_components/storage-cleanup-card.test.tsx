import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { CleanupController } from "@/features/agent/hooks/use-storage-maintenance";
import type {
  SessionCleanupJob,
  SessionCleanupPlan,
} from "@/lib/harness/storage";
import {
  cleanupConfirmDescription,
  describeProgress,
  RUN_CLEANUP_KINDS,
  StorageCleanup,
} from "./storage-cleanup-card";

/**
 * The Clean up old runs block over a fake controller: capability-gated;
 * Find old runs plans every run kind but the person's chats (no picker) →
 * a plain summary of what goes and what stays (no candidate table) → the
 * destructive button opens the typed CLEAN UP confirmation, which gates
 * apply → the job panel with a plain progress line (no per-item error
 * table); an out-of-date plan offers Check again; a management refusal
 * renders read-only copy.
 */

const plan: SessionCleanupPlan = {
  confirmationToken: "token-1",
  available: true,
  unavailableReason: "",
  generation: "g1",
  policyVersion: "p1",
  eligible: [
    {
      sessionId: "subagent-1",
      kind: "subagent",
      state: "completed",
      reason: "age",
      modifiedAt: Date.now() - 3 * 86_400_000,
      estimatedBytes: 2_048,
    },
    {
      sessionId: "sched-1",
      kind: "scheduled",
      state: "completed",
      reason: "cap",
      modifiedAt: null,
      estimatedBytes: 1_024,
    },
  ],
  eligibleCounts: {
    total: 2,
    byKind: { subagent: 1, scheduled: 1 },
    byState: { completed: 2 },
    byReason: { age: 1, cap: 1 },
  },
  protected: {
    total: 3,
    byKind: { main: 2, subagent: 1 },
    byState: { idle: 2, running: 1 },
    byReason: { live: 2, active_state: 1 },
  },
  estimatedBytes: 3_072,
  plannedJobId: "cleanup-1",
};

const job = (
  overrides: Partial<SessionCleanupJob> = {},
): SessionCleanupJob => ({
  jobId: "cleanup-1",
  state: "running",
  processed: 1,
  deleted: 1,
  skipped: 0,
  stale: 0,
  failed: 0,
  errors: [],
  ...overrides,
});

function controller(
  overrides: Partial<CleanupController> = {},
): CleanupController {
  return {
    phase: "idle",
    kinds: [],
    plan: null,
    job: null,
    error: null,
    planCleanup: vi.fn(async () => {}),
    beginConfirm: vi.fn(),
    abortConfirm: vi.fn(),
    apply: vi.fn(async () => {}),
    cancel: vi.fn(async () => {}),
    replan: vi.fn(async () => {}),
    reset: vi.fn(),
    ...overrides,
  };
}

const renderBlock = (
  cleanup: CleanupController,
  props: Partial<{ supported: boolean }> = {},
) =>
  render(
    <StorageCleanup supported={props.supported ?? true} cleanup={cleanup} />,
  );

describe("helpers", () => {
  it("RUN_CLEANUP_KINDS is every kind but main", () => {
    expect([...RUN_CLEANUP_KINDS].sort()).toEqual([
      "parallel_branch",
      "scheduled",
      "subagent",
      "team_member",
    ]);
  });

  it("cleanupConfirmDescription says what goes, that chats are safe, and what stays", () => {
    expect(cleanupConfirmDescription(plan)).toBe(
      "2 old runs will be deleted for good, freeing about 3 KB. Your chats are not affected, and 3 items still needed will be kept. This cannot be undone.",
    );
    expect(
      cleanupConfirmDescription({
        ...plan,
        eligible: [plan.eligible[0]],
        estimatedBytes: 0,
        protected: { total: 0, byKind: {}, byState: {}, byReason: {} },
      }),
    ).toBe(
      "1 old run will be deleted for good. Your chats are not affected. This cannot be undone.",
    );
  });

  it("describeProgress leads with the deletions and adds skipped / could-not-delete only when non-zero", () => {
    expect(describeProgress(job())).toBe("1 deleted");
    expect(
      describeProgress(job({ deleted: 2, skipped: 1, stale: 1, failed: 1 })),
    ).toBe("2 deleted · 2 skipped · 1 could not be deleted");
  });
});

describe("StorageCleanup", () => {
  it("renders nothing without the storage_cleanup capability", () => {
    const { container } = renderBlock(controller(), { supported: false });
    expect(container).toBeEmptyDOMElement();
  });

  it("idle: no kind picker; Find old runs plans every run kind but main chats", async () => {
    const user = userEvent.setup();
    const cleanup = controller();
    renderBlock(cleanup);
    expect(
      screen.getByRole("heading", { name: "Clean up old runs" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Find old runs" }));
    expect(cleanup.planCleanup).toHaveBeenCalledTimes(1);
    const kinds = (cleanup.planCleanup as ReturnType<typeof vi.fn>).mock
      .calls[0]?.[0] as string[];
    expect([...kinds].sort()).toEqual([
      "parallel_branch",
      "scheduled",
      "subagent",
      "team_member",
    ]);
  });

  it("planning: the button reads Looking… and is disabled", () => {
    renderBlock(controller({ phase: "planning" }));
    expect(screen.getByRole("button", { name: "Looking…" })).toBeDisabled();
  });

  it("planned: a plain summary, no table, then the typed CLEAN UP gates apply", async () => {
    const user = userEvent.setup();
    const cleanup = controller({ phase: "planned", plan, kinds: ["subagent"] });
    renderBlock(cleanup);
    expect(screen.getByTestId("storage-cleanup-eligible")).toHaveTextContent(
      "2 old runs can be deleted, freeing about 3 KB.",
    );
    expect(screen.getByTestId("storage-cleanup-protected")).toHaveTextContent(
      "3 items still needed will be kept.",
    );
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByText("subagent-1")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Clean up…" }));
    expect(cleanup.beginConfirm).toHaveBeenCalledTimes(1);
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Delete 2 old runs for good?");
    expect(dialog).toHaveTextContent("Your chats are not affected");
    const confirm = within(dialog).getByRole("button", { name: "Clean up" });
    expect(confirm).toBeDisabled();
    await user.type(within(dialog).getByRole("textbox"), "clean up");
    expect(confirm).toBeDisabled();
    expect(cleanup.apply).not.toHaveBeenCalled();

    await user.clear(within(dialog).getByRole("textbox"));
    await user.type(within(dialog).getByRole("textbox"), "CLEAN UP");
    await user.click(confirm);
    expect(cleanup.apply).toHaveBeenCalledTimes(1);
    expect(cleanup.abortConfirm).not.toHaveBeenCalled();
  });

  it("cancelling the typed confirmation aborts without applying", async () => {
    const user = userEvent.setup();
    const cleanup = controller({ phase: "planned", plan });
    renderBlock(cleanup);
    await user.click(screen.getByRole("button", { name: "Clean up…" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(cleanup.abortConfirm).toHaveBeenCalledTimes(1);
    expect(cleanup.apply).not.toHaveBeenCalled();
  });

  it("Cancel on a plan discards it; applying reads Cleaning up… and blocks both buttons", async () => {
    const user = userEvent.setup();
    const cleanup = controller({ phase: "planned", plan });
    const { unmount } = renderBlock(cleanup);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cleanup.reset).toHaveBeenCalledTimes(1);
    unmount();

    renderBlock(controller({ phase: "applying", plan }));
    expect(screen.getByRole("button", { name: "Cleaning up…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
  });

  it("an empty or unavailable plan offers no destructive button, only Done", async () => {
    const user = userEvent.setup();
    const empty = controller({
      phase: "planned",
      plan: {
        ...plan,
        eligible: [],
        eligibleCounts: { total: 0, byKind: {}, byState: {}, byReason: {} },
      },
    });
    const { unmount } = renderBlock(empty);
    expect(
      screen.getByText("Nothing to clean up right now."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clean up…" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(empty.reset).toHaveBeenCalledTimes(1);
    unmount();

    renderBlock(
      controller({
        phase: "planned",
        plan: {
          ...plan,
          available: false,
          unavailableReason: "backend_unsupported",
        },
      }),
    );
    expect(
      screen.getByText("This agent cannot clean up its storage from here."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clean up…" })).toBeNull();
    expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument();
  });

  it("a busy store reads plainly, and an unknown reason falls back to a plain sentence", () => {
    const { unmount } = renderBlock(
      controller({
        phase: "planned",
        plan: {
          ...plan,
          available: false,
          unavailableReason: "maintenance_exclusion_unavailable",
        },
      }),
    );
    expect(
      screen.getByText("Storage is busy right now. Try again in a moment."),
    ).toBeInTheDocument();
    unmount();

    renderBlock(
      controller({
        phase: "planned",
        plan: { ...plan, available: false, unavailableReason: "mystery" },
      }),
    );
    expect(
      screen.getByText("Clean-up is not available right now."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/mystery/)).toBeNull();
  });

  it("stale: explains the change and Check again re-plans", async () => {
    const user = userEvent.setup();
    const cleanup = controller({
      phase: "stale",
      plan,
      error: { code: "cleanup_plan_stale", message: "stale" },
    });
    renderBlock(cleanup);
    expect(
      screen.getByText(/Storage changed since you checked/),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Check again" }));
    expect(cleanup.replan).toHaveBeenCalledTimes(1);
  });

  it("running shows a plain progress line and Cancel; done shows the outcome and Done", async () => {
    const user = userEvent.setup();
    const running = controller({ phase: "running", plan, job: job() });
    const { unmount } = renderBlock(running);
    expect(screen.getByTestId("storage-cleanup-job-state")).toHaveTextContent(
      "Running",
    );
    expect(screen.getByTestId("storage-cleanup-progress")).toHaveTextContent(
      "1 deleted",
    );
    expect(screen.queryByText("cleanup-1")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(running.cancel).toHaveBeenCalledTimes(1);
    unmount();

    const done = controller({
      phase: "done",
      plan,
      job: job({
        state: "completed",
        processed: 3,
        deleted: 1,
        stale: 1,
        failed: 1,
        errors: [
          {
            itemHandle: "sched-1",
            reasonCode: "stale",
            message: "changed since the plan",
          },
        ],
      }),
    });
    renderBlock(done);
    expect(screen.getByTestId("storage-cleanup-job-state")).toHaveTextContent(
      "Completed",
    );
    expect(screen.getByTestId("storage-cleanup-progress")).toHaveTextContent(
      "1 deleted · 1 skipped · 1 could not be deleted",
    );
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByText("sched-1")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(done.reset).toHaveBeenCalledTimes(1);
  });

  it("a failed job shows its plain error above Done", () => {
    renderBlock(
      controller({
        phase: "failed",
        plan,
        job: job({ state: "failed" }),
        error: { code: "cleanup_backend", message: "boom" },
      }),
    );
    expect(screen.getByTestId("storage-cleanup-job-state")).toHaveTextContent(
      "Failed",
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "The agent could not clean up its storage. Try again later.",
    );
    expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument();
  });

  it("a plan failure shows the error above Find old runs; a management refusal is read-only", () => {
    const { unmount } = renderBlock(
      controller({
        phase: "failed",
        error: { code: "cleanup_backend", message: "boom" },
      }),
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "The agent could not clean up its storage. Try again later.",
    );
    expect(screen.getByRole("button", { name: "Find old runs" })).toBeEnabled();
    unmount();

    renderBlock(
      controller({
        phase: "failed",
        error: { code: "management_unauthorized", message: "forbidden" },
      }),
    );
    expect(
      screen.getByText(/Storage clean-up is not allowed from here/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
