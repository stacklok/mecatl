import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type {
  SessionCleanupJob,
  SessionCleanupPlan,
  StorageMigrationJob,
  StorageMigrationPlan,
} from "@/lib/harness/storage";
import {
  JOB_POLL_INTERVAL_MS,
  useStorageMaintenance,
} from "./use-storage-maintenance";

/**
 * The maintenance state machines: plan → planned; apply → running polls the
 * job every 2 s until a terminal state, then refreshes health (and, for a
 * cleanup that deleted rows, announces the sidebar change); cancel adopts
 * the cancelled job and stops polling; resume re-drives a cancelled job;
 * a stale cleanup plan flips to `stale` and re-plans with the same kinds;
 * unmount clears the timer.
 */

const mocks = vi.hoisted(() => ({
  planStorageMigration: vi.fn(),
  applyStorageMigration: vi.fn(),
  resumeStorageMigration: vi.fn(),
  cancelStorageMigration: vi.fn(),
  fetchStorageMigrationJob: vi.fn(),
  planSessionCleanup: vi.fn(),
  applySessionCleanup: vi.fn(),
  cancelSessionCleanup: vi.fn(),
  fetchSessionCleanupJob: vi.fn(),
  notifySessionsChanged: vi.fn(),
}));

vi.mock("@/lib/harness/storage", async () => {
  const actual = await vi.importActual<typeof import("@/lib/harness/storage")>(
    "@/lib/harness/storage",
  );
  return {
    ...actual,
    planStorageMigration: mocks.planStorageMigration,
    applyStorageMigration: mocks.applyStorageMigration,
    resumeStorageMigration: mocks.resumeStorageMigration,
    cancelStorageMigration: mocks.cancelStorageMigration,
    fetchStorageMigrationJob: mocks.fetchStorageMigrationJob,
    planSessionCleanup: mocks.planSessionCleanup,
    applySessionCleanup: mocks.applySessionCleanup,
    cancelSessionCleanup: mocks.cancelSessionCleanup,
    fetchSessionCleanupJob: mocks.fetchSessionCleanupJob,
  };
});

vi.mock("../sessions-changed", () => ({
  notifySessionsChanged: mocks.notifySessionsChanged,
}));

const migrationPlan: StorageMigrationPlan = {
  planId: "plan-1",
  available: true,
  unavailableReason: "",
  v1Families: 2,
  v2Families: 3,
  invalidFamilies: 0,
  skippedFamilies: 0,
  currentBytes: 20_480,
  reclaimableBytes: 4_096,
  temporaryBytes: 8_192,
};

const migrationJob = (
  overrides: Partial<StorageMigrationJob> = {},
): StorageMigrationJob => ({
  jobId: "job-1",
  state: "running",
  v1Families: 2,
  v2Families: 3,
  invalidFamilies: 0,
  skippedFamilies: 0,
  currentBytes: 20_480,
  reclaimableBytes: 4_096,
  temporaryBytes: 8_192,
  processed: 0,
  migrated: 0,
  failed: 0,
  errors: [],
  ...overrides,
});

const cleanupPlan: SessionCleanupPlan = {
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
      modifiedAt: 1_754_000_000_000,
      estimatedBytes: 2_048,
    },
  ],
  eligibleCounts: {
    total: 1,
    byKind: { subagent: 1 },
    byState: { completed: 1 },
    byReason: { age: 1 },
  },
  protected: {
    total: 1,
    byKind: { main: 1 },
    byState: {},
    byReason: { live: 1 },
  },
  estimatedBytes: 2_048,
  plannedJobId: "cleanup-1",
};

const cleanupJob = (
  overrides: Partial<SessionCleanupJob> = {},
): SessionCleanupJob => ({
  jobId: "cleanup-1",
  state: "running",
  processed: 0,
  deleted: 0,
  skipped: 0,
  stale: 0,
  failed: 0,
  errors: [],
  ...overrides,
});

const refreshHealth = vi.fn();

const advance = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

beforeEach(() => {
  vi.useFakeTimers();
  for (const mock of Object.values(mocks)) mock.mockReset();
  refreshHealth.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

const renderMaintenance = () =>
  renderHook(() => useStorageMaintenance({ refreshHealth }));

describe("useStorageMaintenance — migration", () => {
  it("estimate → planned with the plan; a failed estimate keeps the code", async () => {
    mocks.planStorageMigration.mockResolvedValueOnce(migrationPlan);
    const { result } = renderMaintenance();
    expect(result.current.migration.phase).toBe("idle");

    await act(() => result.current.migration.estimate());
    expect(result.current.migration.phase).toBe("planned");
    expect(result.current.migration.plan).toEqual(migrationPlan);

    mocks.planStorageMigration.mockRejectedValueOnce(
      new HarnessApiError(501, "migration_unsupported", "nope"),
    );
    await act(() => result.current.migration.estimate());
    expect(result.current.migration.phase).toBe("failed");
    expect(result.current.migration.error).toEqual({
      code: "migration_unsupported",
      message: "nope",
    });
  });

  it("apply → running; polls every 2 s until terminal, then refreshes health and stops", async () => {
    mocks.planStorageMigration.mockResolvedValueOnce(migrationPlan);
    mocks.applyStorageMigration.mockResolvedValueOnce(migrationJob());
    mocks.fetchStorageMigrationJob
      .mockResolvedValueOnce(migrationJob({ processed: 1, migrated: 1 }))
      .mockResolvedValueOnce(
        migrationJob({ state: "completed", processed: 2, migrated: 2 }),
      );
    const { result } = renderMaintenance();
    await act(() => result.current.migration.estimate());
    await act(() => result.current.migration.apply(25));
    expect(mocks.applyStorageMigration).toHaveBeenCalledWith("plan-1", 25);
    expect(result.current.migration.phase).toBe("running");
    expect(refreshHealth).not.toHaveBeenCalled();

    await advance(JOB_POLL_INTERVAL_MS);
    expect(mocks.fetchStorageMigrationJob).toHaveBeenCalledTimes(1);
    expect(mocks.fetchStorageMigrationJob.mock.calls[0]?.[0]).toBe("job-1");
    expect(result.current.migration.job?.processed).toBe(1);
    expect(result.current.migration.phase).toBe("running");

    await advance(JOB_POLL_INTERVAL_MS);
    expect(mocks.fetchStorageMigrationJob).toHaveBeenCalledTimes(2);
    expect(result.current.migration.phase).toBe("done");
    expect(result.current.migration.job?.state).toBe("completed");
    expect(refreshHealth).toHaveBeenCalledTimes(1);

    // Terminal: no further polls.
    await advance(JOB_POLL_INTERVAL_MS * 3);
    expect(mocks.fetchStorageMigrationJob).toHaveBeenCalledTimes(2);
  });

  it("cancel adopts the cancelled job and stops polling; resume re-drives it", async () => {
    mocks.planStorageMigration.mockResolvedValueOnce(migrationPlan);
    mocks.applyStorageMigration.mockResolvedValueOnce(migrationJob());
    mocks.cancelStorageMigration.mockResolvedValueOnce(
      migrationJob({ state: "cancelled", processed: 1 }),
    );
    mocks.resumeStorageMigration.mockResolvedValueOnce(
      migrationJob({ processed: 1 }),
    );
    const { result } = renderMaintenance();
    await act(() => result.current.migration.estimate());
    await act(() => result.current.migration.apply(50));

    await act(() => result.current.migration.cancel());
    expect(mocks.cancelStorageMigration).toHaveBeenCalledWith("job-1");
    expect(result.current.migration.phase).toBe("done");
    expect(result.current.migration.job?.state).toBe("cancelled");
    expect(refreshHealth).toHaveBeenCalledTimes(1);
    await advance(JOB_POLL_INTERVAL_MS * 2);
    expect(mocks.fetchStorageMigrationJob).not.toHaveBeenCalled();

    await act(() => result.current.migration.resume(10));
    expect(mocks.resumeStorageMigration).toHaveBeenCalledWith("job-1", 10);
    expect(result.current.migration.phase).toBe("running");
  });

  it("a refused apply keeps the daemon's code (migration_conflict)", async () => {
    mocks.planStorageMigration.mockResolvedValueOnce(migrationPlan);
    mocks.applyStorageMigration.mockRejectedValueOnce(
      new HarnessApiError(409, "migration_conflict", "job running"),
    );
    const { result } = renderMaintenance();
    await act(() => result.current.migration.estimate());
    await act(() => result.current.migration.apply(50));
    expect(result.current.migration.phase).toBe("failed");
    expect(result.current.migration.error?.code).toBe("migration_conflict");
    expect(result.current.migration.job).toBeNull();
  });

  it("unmount clears the poll timer", async () => {
    mocks.planStorageMigration.mockResolvedValueOnce(migrationPlan);
    mocks.applyStorageMigration.mockResolvedValueOnce(migrationJob());
    const { result, unmount } = renderMaintenance();
    await act(() => result.current.migration.estimate());
    await act(() => result.current.migration.apply(50));
    unmount();
    await advance(JOB_POLL_INTERVAL_MS * 2);
    expect(mocks.fetchStorageMigrationJob).not.toHaveBeenCalled();
  });
});

describe("useStorageMaintenance — cleanup", () => {
  it("plan → planned; confirm brackets; apply with the token → done refreshes health and announces the deletion", async () => {
    mocks.planSessionCleanup.mockResolvedValueOnce(cleanupPlan);
    mocks.applySessionCleanup.mockResolvedValueOnce(
      cleanupJob({ state: "completed", processed: 1, deleted: 1 }),
    );
    const { result } = renderMaintenance();

    await act(() =>
      result.current.cleanup.planCleanup(["subagent", "scheduled"]),
    );
    expect(mocks.planSessionCleanup).toHaveBeenCalledWith([
      "subagent",
      "scheduled",
    ]);
    expect(result.current.cleanup.phase).toBe("planned");
    expect(result.current.cleanup.kinds).toEqual(["subagent", "scheduled"]);

    act(() => result.current.cleanup.beginConfirm());
    expect(result.current.cleanup.phase).toBe("confirming");
    act(() => result.current.cleanup.abortConfirm());
    expect(result.current.cleanup.phase).toBe("planned");

    await act(() => result.current.cleanup.apply());
    expect(mocks.applySessionCleanup).toHaveBeenCalledWith("token-1");
    expect(result.current.cleanup.phase).toBe("done");
    expect(result.current.cleanup.job?.deleted).toBe(1);
    expect(refreshHealth).toHaveBeenCalledTimes(1);
    expect(mocks.notifySessionsChanged).toHaveBeenCalledTimes(1);
  });

  it("a running cleanup polls until completed; nothing deleted → no sidebar signal", async () => {
    mocks.planSessionCleanup.mockResolvedValueOnce(cleanupPlan);
    mocks.applySessionCleanup.mockResolvedValueOnce(cleanupJob());
    mocks.fetchSessionCleanupJob.mockResolvedValueOnce(
      cleanupJob({ state: "completed", processed: 1, skipped: 1 }),
    );
    const { result } = renderMaintenance();
    await act(() => result.current.cleanup.planCleanup(["subagent"]));
    await act(() => result.current.cleanup.apply());
    expect(result.current.cleanup.phase).toBe("running");

    await advance(JOB_POLL_INTERVAL_MS);
    expect(mocks.fetchSessionCleanupJob).toHaveBeenCalledWith(
      "cleanup-1",
      expect.any(AbortSignal),
    );
    expect(result.current.cleanup.phase).toBe("done");
    expect(refreshHealth).toHaveBeenCalledTimes(1);
    expect(mocks.notifySessionsChanged).not.toHaveBeenCalled();

    await advance(JOB_POLL_INTERVAL_MS * 2);
    expect(mocks.fetchSessionCleanupJob).toHaveBeenCalledTimes(1);
  });

  it("cleanup_plan_stale flips to stale; replan re-plans with the same kinds", async () => {
    mocks.planSessionCleanup.mockResolvedValue(cleanupPlan);
    mocks.applySessionCleanup.mockRejectedValueOnce(
      new HarnessApiError(409, "cleanup_plan_stale", "stale"),
    );
    const { result } = renderMaintenance();
    await act(() => result.current.cleanup.planCleanup(["team_member"]));
    await act(() => result.current.cleanup.apply());
    expect(result.current.cleanup.phase).toBe("stale");
    expect(result.current.cleanup.error?.code).toBe("cleanup_plan_stale");

    await act(() => result.current.cleanup.replan());
    expect(mocks.planSessionCleanup).toHaveBeenCalledTimes(2);
    expect(mocks.planSessionCleanup.mock.calls[1]?.[0]).toEqual([
      "team_member",
    ]);
    expect(result.current.cleanup.phase).toBe("planned");
    expect(result.current.cleanup.error).toBeNull();
  });

  it("cancel adopts the cancelled job; reset returns to idle", async () => {
    mocks.planSessionCleanup.mockResolvedValueOnce(cleanupPlan);
    mocks.applySessionCleanup.mockResolvedValueOnce(cleanupJob());
    mocks.cancelSessionCleanup.mockResolvedValueOnce(
      cleanupJob({ state: "cancelled" }),
    );
    const { result } = renderMaintenance();
    await act(() => result.current.cleanup.planCleanup(["subagent"]));
    await act(() => result.current.cleanup.apply());
    await act(() => result.current.cleanup.cancel());
    expect(mocks.cancelSessionCleanup).toHaveBeenCalledWith("cleanup-1");
    expect(result.current.cleanup.phase).toBe("done");
    expect(result.current.cleanup.job?.state).toBe("cancelled");

    act(() => result.current.cleanup.reset());
    expect(result.current.cleanup.phase).toBe("idle");
    expect(result.current.cleanup.plan).toBeNull();
    expect(result.current.cleanup.job).toBeNull();
  });
});
