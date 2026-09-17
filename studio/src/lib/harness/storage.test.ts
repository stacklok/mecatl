import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";
import {
  applySessionCleanup,
  applyStorageMigration,
  cancelSessionCleanup,
  cancelStorageMigration,
  decodeCleanupJob,
  decodeCleanupPlan,
  decodeMigrationJob,
  decodeMigrationPlan,
  decodeStorageHealth,
  fetchSessionCleanupJob,
  fetchStorageHealth,
  fetchStorageMigrationJob,
  isStorageDegraded,
  isStorageJobActive,
  planSessionCleanup,
  planStorageMigration,
  resumeStorageMigration,
  type StorageHealthWire,
} from "./storage";

/**
 * Pins the storage-health contract (ADR 0226) over the SDK: the route, the
 * projection off the daemon's proto-JSON body (banner subset + the effective
 * retention policy, sweep timestamps, family counts and sizes), and the
 * degraded classification.
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const healthy: StorageHealthWire = {
  available: true,
  unavailableReason: "",
  sessionCount: BigInt(4),
  corruptCount: BigInt(0),
  v1Count: BigInt(0),
  v2Count: BigInt(4),
  mainCount: BigInt(2),
  childCount: BigInt(1),
  scheduledCount: BigInt(1),
  unknownCount: BigInt(0),
  fileCount: BigInt(9),
  currentBytes: BigInt(20_480),
  currentBytesAvailable: true,
  reclaimableBytes: BigInt(0),
  reclaimableBytesAvailable: false,
  policy: {
    $typeName: "mecatl.v1.RetentionPolicy",
    mainMaxAgeSeconds: BigInt(0),
    mainMaxCount: 0,
    childMaxAgeSeconds: BigInt(604_800),
    childMaxCount: 500,
    scheduledMaxAgeSeconds: BigInt(604_800),
    scheduledMaxCount: 0,
    sweepCadenceSeconds: BigInt(3600),
  },
  lastSweepUnix: BigInt(1_755_003_000),
  lastSweepAvailable: true,
  nextSweepUnix: BigInt(0),
  nextSweepAvailable: false,
  lastFailure: "",
  activeJob: "",
};

describe("decodeStorageHealth", () => {
  it("projects the banner subset, converting int64 counts to numbers", () => {
    expect(
      decodeStorageHealth({
        ...healthy,
        sessionCount: BigInt(12),
        corruptCount: BigInt(2),
        lastFailure: "sweep: disk full",
      }),
    ).toMatchObject({
      available: true,
      unavailableReason: "",
      sessionCount: 12,
      corruptCount: 2,
      lastFailure: "sweep: disk full",
      activeJob: "",
    });
  });

  it("projects the effective policy, family counts and sizes; unix seconds become epoch millis", () => {
    const health = decodeStorageHealth(healthy);
    expect(health.policy).toEqual({
      mainMaxAgeSeconds: 0,
      mainMaxCount: 0,
      childMaxAgeSeconds: 604_800,
      childMaxCount: 500,
      scheduledMaxAgeSeconds: 604_800,
      scheduledMaxCount: 0,
      sweepCadenceSeconds: 3600,
    });
    expect(health).toMatchObject({
      v1Count: 0,
      v2Count: 4,
      mainCount: 2,
      childCount: 1,
      scheduledCount: 1,
      unknownCount: 0,
      fileCount: 9,
      currentBytes: 20_480,
      lastSweepAt: 1_755_003_000_000,
    });
  });

  it("reports unavailable sizes and sweeps as null rather than 0", () => {
    const health = decodeStorageHealth(healthy);
    expect(health.reclaimableBytes).toBeNull();
    expect(health.nextSweepAt).toBeNull();
    expect(
      decodeStorageHealth({
        ...healthy,
        currentBytesAvailable: false,
        lastSweepAvailable: false,
        nextSweepUnix: BigInt(1_755_006_600),
        nextSweepAvailable: true,
      }),
    ).toMatchObject({
      currentBytes: null,
      lastSweepAt: null,
      nextSweepAt: 1_755_006_600_000,
    });
  });

  it("reports a daemon that sends no policy as null, never a zeroed table", () => {
    expect(decodeStorageHealth({ ...healthy, policy: undefined }).policy).toBe(
      null,
    );
  });
});

describe("isStorageDegraded", () => {
  const health = decodeStorageHealth(healthy);
  it("healthy store → not degraded", () => {
    expect(isStorageDegraded(health)).toBe(false);
  });
  it("unavailable, corrupt families, or a recorded failure → degraded", () => {
    expect(isStorageDegraded({ ...health, available: false })).toBe(true);
    expect(isStorageDegraded({ ...health, corruptCount: 1 })).toBe(true);
    expect(isStorageDegraded({ ...health, lastFailure: "boom" })).toBe(true);
  });
  it("plain unmigrated v1 sessions still list, so they are not degraded", () => {
    expect(isStorageDegraded({ ...health, v1Count: 9 })).toBe(false);
    expect(
      isStorageDegraded(
        decodeStorageHealth({ ...healthy, v1Count: BigInt(9) }),
      ),
    ).toBe(false);
  });
});

describe("fetchStorageHealth", () => {
  it("GETs /v1/storage/health and decodes the daemon's snake_case answer", async () => {
    const stub = stubHarnessFetch(() => ({
      available: true,
      session_count: 2,
      corrupt_count: 1,
      main_count: 2,
      last_failure: "",
      active_job: "migrate",
      policy: { child_max_age_seconds: 86400, sweep_cadence_seconds: 600 },
      last_sweep_unix: 1_755_003_000,
      last_sweep_available: true,
    }));
    const health = await fetchStorageHealth();
    expect(stub.last()).toMatchObject({
      method: "GET",
      url: "/api/mecatl/v1/storage/health",
    });
    expect(health).toMatchObject({
      available: true,
      sessionCount: 2,
      corruptCount: 1,
      mainCount: 2,
      activeJob: "migrate",
      policy: { childMaxAgeSeconds: 86400, sweepCadenceSeconds: 600 },
      lastSweepAt: 1_755_003_000_000,
      nextSweepAt: null,
      currentBytes: null,
    });
    expect(isStorageDegraded(health)).toBe(true);
  });

  it("throws the typed error when the daemon refuses (management auth)", async () => {
    stubHarnessFetch(() =>
      jsonResponse(403, {
        code: "management_unauthorized",
        error: "management authorization required",
      }),
    );
    await expect(fetchStorageHealth()).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "management_unauthorized",
      status: 403,
    });
  });

  it("projects the ownerless preflight when the wire carries it, null counts when unavailable", () => {
    expect(decodeStorageHealth(healthy).ownerless).toBeUndefined();
    expect(
      decodeStorageHealth({
        ...healthy,
        ownerlessSessionsAvailable: true,
        ownerlessSessionCount: BigInt(2),
        ownerlessSessionsUnavailableReason: "",
        ownerlessSchedulesAvailable: false,
        ownerlessScheduleCount: BigInt(0),
        ownerlessSchedulesUnavailableReason: "no schedule store",
      }).ownerless,
    ).toEqual({
      sessions: 2,
      sessionsUnavailableReason: "",
      schedules: null,
      schedulesUnavailableReason: "no schedule store",
    });
  });
});

// ---------------------------------------------------------------------------
// Maintenance jobs
// ---------------------------------------------------------------------------

describe("job state classification", () => {
  it("planned/pending/running are active; everything else is terminal", () => {
    for (const state of ["planned", "pending", "running"]) {
      expect(isStorageJobActive(state)).toBe(true);
    }
    for (const state of ["completed", "cancelled", "failed", "stale", ""]) {
      expect(isStorageJobActive(state)).toBe(false);
    }
  });
});

describe("maintenance decoders", () => {
  it("decodeMigrationPlan / decodeMigrationJob turn int64 bigints into numbers", () => {
    expect(
      decodeMigrationPlan({
        $typeName: "mecatl.v1.SessionMigrationPlan",
        planId: "plan-1",
        available: true,
        unavailableReason: "",
        v1Families: BigInt(2),
        v2Families: BigInt(3),
        invalidFamilies: BigInt(1),
        skippedFamilies: BigInt(0),
        currentBytes: BigInt(20_480),
        reclaimableBytes: BigInt(4_096),
        temporaryBytes: BigInt(8_192),
      }),
    ).toEqual({
      planId: "plan-1",
      available: true,
      unavailableReason: "",
      v1Families: 2,
      v2Families: 3,
      invalidFamilies: 1,
      skippedFamilies: 0,
      currentBytes: 20_480,
      reclaimableBytes: 4_096,
      temporaryBytes: 8_192,
    });
    expect(
      decodeMigrationJob({
        $typeName: "mecatl.v1.SessionMigrationJob",
        jobId: "job-1",
        state: "running",
        v1Families: BigInt(2),
        v2Families: BigInt(3),
        invalidFamilies: BigInt(0),
        skippedFamilies: BigInt(0),
        currentBytes: BigInt(0),
        reclaimableBytes: BigInt(0),
        temporaryBytes: BigInt(0),
        processed: BigInt(1),
        migrated: BigInt(0),
        failed: BigInt(1),
        errors: [
          {
            $typeName: "mecatl.v1.SessionMigrationItemError",
            itemHandle: "family-7",
            reasonCode: "invalid_family",
            message: "family cannot be read",
          },
        ],
      }),
    ).toMatchObject({
      jobId: "job-1",
      state: "running",
      processed: 1,
      failed: 1,
      errors: [
        {
          itemHandle: "family-7",
          reasonCode: "invalid_family",
          message: "family cannot be read",
        },
      ],
    });
  });

  it("decodeCleanupPlan copies the count maps, converts unix seconds to millis (0 → null), and defaults missing partitions to zero", () => {
    const plan = decodeCleanupPlan({
      $typeName: "mecatl.v1.PlanSessionCleanupResponse",
      confirmationToken: "token-1",
      available: true,
      unavailableReason: "",
      generation: "g1",
      policyVersion: "p1",
      eligible: [
        {
          $typeName: "mecatl.v1.CleanupCandidate",
          sessionId: "subagent-1",
          kind: "subagent",
          state: "completed",
          reason: "age",
          modifiedAtUnix: BigInt(1_754_000_000),
          estimatedBytes: BigInt(2_048),
        },
        {
          $typeName: "mecatl.v1.CleanupCandidate",
          sessionId: "subagent-2",
          kind: "subagent",
          state: "failed",
          reason: "cap",
          modifiedAtUnix: BigInt(0),
          estimatedBytes: BigInt(0),
        },
      ],
      protected: {
        $typeName: "mecatl.v1.CleanupCounts",
        total: 2,
        byKind: { main: 2 },
        byState: { idle: 2 },
        byReason: { live: 1, foreign_owner: 1 },
      },
      eligibleCounts: undefined,
      estimatedBytes: BigInt(2_048),
      plannedJobId: "cleanup-1",
    });
    expect(plan).toMatchObject({
      confirmationToken: "token-1",
      generation: "g1",
      policyVersion: "p1",
      estimatedBytes: 2_048,
      plannedJobId: "cleanup-1",
      protected: {
        total: 2,
        byKind: { main: 2 },
        byState: { idle: 2 },
        byReason: { live: 1, foreign_owner: 1 },
      },
      eligibleCounts: { total: 0, byKind: {}, byState: {}, byReason: {} },
    });
    expect(plan.eligible).toEqual([
      {
        sessionId: "subagent-1",
        kind: "subagent",
        state: "completed",
        reason: "age",
        modifiedAt: 1_754_000_000_000,
        estimatedBytes: 2_048,
      },
      {
        sessionId: "subagent-2",
        kind: "subagent",
        state: "failed",
        reason: "cap",
        modifiedAt: null,
        estimatedBytes: 0,
      },
    ]);
  });

  it("decodeCleanupJob keeps the int32 counters and the sanitized errors", () => {
    expect(
      decodeCleanupJob({
        $typeName: "mecatl.v1.CleanupJob",
        jobId: "cleanup-1",
        state: "completed",
        processed: 3,
        deleted: 1,
        skipped: 1,
        stale: 1,
        failed: 0,
        errors: [],
      }),
    ).toEqual({
      jobId: "cleanup-1",
      state: "completed",
      processed: 3,
      deleted: 1,
      skipped: 1,
      stale: 1,
      failed: 0,
      errors: [],
    });
  });
});

describe("migration RPCs", () => {
  const jobBody = {
    job_id: "job-1",
    state: "running",
    v1_families: 2,
    processed: 1,
    migrated: 1,
    failed: 0,
  };

  it("planStorageMigration POSTs /v1/storage/migrations/plan with no body", async () => {
    const stub = stubHarnessFetch(() => ({
      plan_id: "plan-1",
      available: true,
      v1_families: 2,
      v2_families: 3,
      temporary_bytes: 8192,
    }));
    const plan = await planStorageMigration();
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/storage/migrations/plan",
      body: undefined,
    });
    expect(plan).toMatchObject({
      planId: "plan-1",
      available: true,
      v1Families: 2,
      v2Families: 3,
      temporaryBytes: 8192,
    });
  });

  it("applyStorageMigration POSTs /apply with {plan_id, batch_size} (default 50)", async () => {
    const stub = stubHarnessFetch(() => jobBody);
    const job = await applyStorageMigration("plan-1");
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/storage/migrations/apply",
      body: { plan_id: "plan-1", batch_size: 50 },
    });
    expect(job).toMatchObject({
      jobId: "job-1",
      state: "running",
      processed: 1,
    });

    await applyStorageMigration("plan-1", 10);
    expect(stub.last().body).toEqual({ plan_id: "plan-1", batch_size: 10 });
  });

  it("fetch / cancel / resume address the job by id", async () => {
    const stub = stubHarnessFetch((request) =>
      request.path.endsWith("/cancel")
        ? { ...jobBody, state: "cancelled" }
        : jobBody,
    );
    await fetchStorageMigrationJob("job-1");
    expect(stub.last()).toMatchObject({
      method: "GET",
      path: "/v1/storage/migrations/job-1",
    });

    const cancelled = await cancelStorageMigration("job-1");
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/storage/migrations/job-1/cancel",
    });
    expect(cancelled.state).toBe("cancelled");

    // The job id binds the path; only the batch size rides the body.
    await resumeStorageMigration("job-1", 25);
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/storage/migrations/job-1/resume",
      body: { batch_size: 25 },
    });
  });

  it("a conflicting apply surfaces the daemon's code", async () => {
    stubHarnessFetch(() =>
      problemResponse(409, "migration_conflict", "a migration job is running"),
    );
    await expect(applyStorageMigration("plan-1")).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "migration_conflict",
      status: 409,
    });
  });
});

describe("cleanup RPCs", () => {
  const jobBody = {
    job_id: "cleanup-1",
    state: "completed",
    processed: 1,
    deleted: 1,
  };

  it("planSessionCleanup POSTs /v1/storage/cleanup:plan with {kinds}", async () => {
    const stub = stubHarnessFetch(() => ({
      confirmation_token: "token-1",
      available: true,
      generation: "g1",
      policy_version: "p1",
      eligible: [
        {
          session_id: "subagent-1",
          kind: "subagent",
          state: "completed",
          reason: "age",
          modified_at_unix: 1_754_000_000,
          estimated_bytes: 2048,
        },
      ],
      protected: { total: 1, by_kind: { main: 1 }, by_reason: { live: 1 } },
      eligible_counts: { total: 1, by_kind: { subagent: 1 } },
      estimated_bytes: 2048,
      planned_job_id: "cleanup-1",
    }));
    const plan = await planSessionCleanup(["subagent", "scheduled"]);
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/storage/cleanup:plan",
      body: { kinds: ["subagent", "scheduled"] },
    });
    expect(plan).toMatchObject({
      confirmationToken: "token-1",
      eligible: [
        {
          sessionId: "subagent-1",
          modifiedAt: 1_754_000_000_000,
          reason: "age",
        },
      ],
      eligibleCounts: { total: 1, byKind: { subagent: 1 } },
      protected: { total: 1, byReason: { live: 1 } },
      estimatedBytes: 2048,
      plannedJobId: "cleanup-1",
    });
  });

  it("applySessionCleanup POSTs cleanup:apply with {confirmation_token}", async () => {
    const stub = stubHarnessFetch(() => jobBody);
    const job = await applySessionCleanup("token-1");
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/storage/cleanup:apply",
      body: { confirmation_token: "token-1" },
    });
    expect(job).toMatchObject({ jobId: "cleanup-1", deleted: 1 });
  });

  it("fetch / cancel address the job under /v1/storage/cleanup/jobs/{id}", async () => {
    const stub = stubHarnessFetch(() => jobBody);
    await fetchSessionCleanupJob("cleanup-1");
    expect(stub.last()).toMatchObject({
      method: "GET",
      path: "/v1/storage/cleanup/jobs/cleanup-1",
    });
    await cancelSessionCleanup("cleanup-1");
    expect(stub.last()).toMatchObject({
      method: "POST",
      path: "/v1/storage/cleanup/jobs/cleanup-1/cancel",
    });
  });

  it("a stale plan surfaces cleanup_plan_stale for the re-plan branch", async () => {
    stubHarnessFetch(() =>
      problemResponse(409, "cleanup_plan_stale", "plan is stale"),
    );
    await expect(applySessionCleanup("token-1")).rejects.toMatchObject({
      name: "HarnessApiError",
      code: "cleanup_plan_stale",
      status: 409,
    });
  });
});
