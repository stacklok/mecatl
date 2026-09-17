/**
 * Storage health and maintenance (ADR 0226) over the SDK's `client.storage`
 * namespace, gated by the daemon's compatibility capabilities:
 *
 * - `GET /v1/storage/health` (`storage_health`): the degraded-store banner
 *   that explains why sessions may be missing from the sidebar, the Storage
 *   page's always-visible health card, and its effective retention policy —
 *   the limits and sweep schedule AS THE DAEMON APPLIES THEM (whatever flags
 *   or settings file produced them), with the per-family counts they act on.
 * - The migration job family (`storage_migration`): plan → apply → poll /
 *   cancel / resume, the Storage page's "Optimize storage" flow.
 * - The cleanup job family (`storage_cleanup`): plan (eligible vs protected)
 *   → typed-confirm apply with the plan's confirmation token → poll / cancel,
 *   the bulk "Clean up sessions" flow.
 *
 * Every function here is a thin projection: bigint counts become numbers,
 * unix seconds become epoch millis, proto maps become plain records. Flow
 * control branches on the `HarnessApiError.code` these throw
 * (`cleanup_plan_stale`, `migration_conflict`, `management_unauthorized`,
 * …), never on message prose.
 */

import type {
  CleanupCounts as CleanupCountsWire,
  CleanupJob as CleanupJobWire,
  GetStorageHealthResponse,
  PlanSessionCleanupResponse,
  SessionMigrationJob as SessionMigrationJobWire,
  SessionMigrationPlan as SessionMigrationPlanWire,
} from "@stacklok-oss/mecatl-sdk/gen";

import { getHarnessClient, harness } from "./sdk";

/**
 * The effective retention policy, in seconds and counts; 0 means that pass
 * is off (mecated's own "0 disables" convention, preserved rather than
 * translated so the form's "0" and the table's "Off" agree).
 */
interface StorageRetentionPolicy {
  mainMaxAgeSeconds: number;
  mainMaxCount: number;
  childMaxAgeSeconds: number;
  childMaxCount: number;
  scheduledMaxAgeSeconds: number;
  scheduledMaxCount: number;
  /** How often the GC re-sweeps after the startup sweep; 0 = startup only. */
  sweepCadenceSeconds: number;
}

export interface StorageHealth {
  /** False when the session store itself cannot be read. */
  available: boolean;
  unavailableReason: string;
  sessionCount: number;
  /** Session families the store holds but cannot load — "missing" sessions. */
  corruptCount: number;
  /**
   * Legacy-layout families awaiting migration. Plain unmigrated v1
   * sessions still list, so this never contributes to `isStorageDegraded`.
   */
  v1Count: number;
  v2Count: number;
  /** Per-family counts the retention passes act on. */
  mainCount: number;
  childCount: number;
  scheduledCount: number;
  /** Families of no known kind — protected from every retention pass. */
  unknownCount: number;
  fileCount: number;
  /** Bytes on disk; null when the store cannot size itself. */
  currentBytes: number | null;
  /** Bytes a cleanup could reclaim; null when unknown. */
  reclaimableBytes: number | null;
  /** The effective retention policy; null when the daemon reports none. */
  policy: StorageRetentionPolicy | null;
  /** Epoch millis of the last completed sweep; null = never. */
  lastSweepAt: number | null;
  /** Epoch millis of the next scheduled sweep; null = not scheduled. */
  nextSweepAt: number | null;
  /** The last background sweep/migration failure, "" when none. */
  lastFailure: string;
  /**
   * The maintenance job kinds running right now, comma-joined as the daemon
   * reports them (`migration`, `cleanup`, `cleanup(2)`); "" when idle. A
   * kind label, never a job id — a job started from another client is
   * followed through a health refresh, not attached to.
   */
  activeJob: string;
  /**
   * The ownerless-record preflight (sessions and schedules no caller owns),
   * when the wire carries it; absent on a projection built from the health
   * subset alone. A null count means the daemon could not compute it.
   */
  ownerless?: {
    sessions: number | null;
    sessionsUnavailableReason: string;
    schedules: number | null;
    schedulesUnavailableReason: string;
  };
}

/** The wire fields Studio projects; the ownerless preflight is optional so
 *  older fixtures and partial projections still decode. */
export type StorageHealthWire = Pick<
  GetStorageHealthResponse,
  | "available"
  | "unavailableReason"
  | "sessionCount"
  | "corruptCount"
  | "v1Count"
  | "v2Count"
  | "mainCount"
  | "childCount"
  | "scheduledCount"
  | "unknownCount"
  | "fileCount"
  | "currentBytes"
  | "currentBytesAvailable"
  | "reclaimableBytes"
  | "reclaimableBytesAvailable"
  | "policy"
  | "lastSweepUnix"
  | "lastSweepAvailable"
  | "nextSweepUnix"
  | "nextSweepAvailable"
  | "lastFailure"
  | "activeJob"
> &
  Partial<
    Pick<
      GetStorageHealthResponse,
      | "ownerlessSessionsAvailable"
      | "ownerlessSessionsUnavailableReason"
      | "ownerlessSessionCount"
      | "ownerlessSchedulesAvailable"
      | "ownerlessSchedulesUnavailableReason"
      | "ownerlessScheduleCount"
    >
  >;

const unixToMillis = (unix: bigint | number, available: boolean) =>
  available ? Number(unix) * 1000 : null;

/** The ownerless preflight, only when the wire carried either half. */
function decodeOwnerless(
  response: StorageHealthWire,
): StorageHealth["ownerless"] {
  if (
    response.ownerlessSessionsAvailable === undefined &&
    response.ownerlessSchedulesAvailable === undefined
  ) {
    return undefined;
  }
  return {
    sessions: response.ownerlessSessionsAvailable
      ? Number(response.ownerlessSessionCount ?? 0)
      : null,
    sessionsUnavailableReason:
      response.ownerlessSessionsUnavailableReason ?? "",
    schedules: response.ownerlessSchedulesAvailable
      ? Number(response.ownerlessScheduleCount ?? 0)
      : null,
    schedulesUnavailableReason:
      response.ownerlessSchedulesUnavailableReason ?? "",
  };
}

/** Projects the SDK response: int64 → number, unavailable → null, unix
 *  seconds → epoch millis. */
export function decodeStorageHealth(
  response: StorageHealthWire,
): StorageHealth {
  const policy = response.policy;
  const ownerless = decodeOwnerless(response);
  return {
    ...(ownerless ? { ownerless } : {}),
    available: response.available,
    unavailableReason: response.unavailableReason,
    sessionCount: Number(response.sessionCount),
    corruptCount: Number(response.corruptCount),
    v1Count: Number(response.v1Count),
    v2Count: Number(response.v2Count),
    mainCount: Number(response.mainCount),
    childCount: Number(response.childCount),
    scheduledCount: Number(response.scheduledCount),
    unknownCount: Number(response.unknownCount),
    fileCount: Number(response.fileCount),
    currentBytes: response.currentBytesAvailable
      ? Number(response.currentBytes)
      : null,
    reclaimableBytes: response.reclaimableBytesAvailable
      ? Number(response.reclaimableBytes)
      : null,
    policy: policy
      ? {
          mainMaxAgeSeconds: Number(policy.mainMaxAgeSeconds),
          mainMaxCount: Number(policy.mainMaxCount),
          childMaxAgeSeconds: Number(policy.childMaxAgeSeconds),
          childMaxCount: Number(policy.childMaxCount),
          scheduledMaxAgeSeconds: Number(policy.scheduledMaxAgeSeconds),
          scheduledMaxCount: Number(policy.scheduledMaxCount),
          sweepCadenceSeconds: Number(policy.sweepCadenceSeconds),
        }
      : null,
    lastSweepAt: unixToMillis(
      response.lastSweepUnix,
      response.lastSweepAvailable,
    ),
    nextSweepAt: unixToMillis(
      response.nextSweepUnix,
      response.nextSweepAvailable,
    ),
    lastFailure: response.lastFailure,
    activeJob: response.activeJob,
  };
}

/**
 * True when the store's state can explain sessions missing from the sidebar:
 * the store is unreadable, some families no longer load, or a background job
 * failed. Plain unmigrated v1 sessions still list, so they are NOT degraded.
 */
export function isStorageDegraded(health: StorageHealth): boolean {
  return (
    !health.available || health.corruptCount > 0 || health.lastFailure !== ""
  );
}

export async function fetchStorageHealth(
  signal?: AbortSignal,
): Promise<StorageHealth> {
  const response = await harness(() =>
    getHarnessClient().storage.getHealth(
      { $typeName: "mecatl.v1.GetStorageHealthRequest" },
      { signal },
    ),
  );
  return decodeStorageHealth(response);
}

// ---------------------------------------------------------------------------
// Maintenance jobs: migration ("Optimize storage") and cleanup.
// ---------------------------------------------------------------------------

/** One sanitized per-item failure a job reports (never a path or a raw
 *  backend error — the daemon strips those before they reach the wire). */
interface StorageJobItemError {
  itemHandle: string;
  reasonCode: string;
  message: string;
}

/** Job states a client keeps polling; everything else is terminal. */
const ACTIVE_JOB_STATES = new Set(["planned", "pending", "running"]);

/** True while a migration or cleanup job is still doing work. */
export function isStorageJobActive(state: string): boolean {
  return ACTIVE_JOB_STATES.has(state);
}

/** The read-only estimate `POST /v1/storage/migrations/plan` answers. */
export interface StorageMigrationPlan {
  planId: string;
  /** False when the store cannot be migrated right now; see the reason. */
  available: boolean;
  unavailableReason: string;
  /** Legacy-layout families the job would migrate. */
  v1Families: number;
  /** Families already in the current layout. */
  v2Families: number;
  /** Families the plan cannot read; they are skipped, never touched. */
  invalidFamilies: number;
  /** Families deliberately left out (e.g. still running). */
  skippedFamilies: number;
  currentBytes: number;
  reclaimableBytes: number;
  /** Extra disk the migration needs while it runs. */
  temporaryBytes: number;
}

/** A durable migration job as apply / get / cancel / resume report it. */
export interface StorageMigrationJob {
  jobId: string;
  /** `planned` | `running` | `completed` | `cancelled` | `failed`. */
  state: string;
  v1Families: number;
  v2Families: number;
  invalidFamilies: number;
  skippedFamilies: number;
  currentBytes: number;
  reclaimableBytes: number;
  temporaryBytes: number;
  processed: number;
  migrated: number;
  failed: number;
  errors: StorageJobItemError[];
}

/** A count partition: total plus per-kind / per-state / per-reason maps. */
interface CleanupCounts {
  total: number;
  byKind: Record<string, number>;
  byState: Record<string, number>;
  byReason: Record<string, number>;
}

interface CleanupCandidate {
  sessionId: string;
  kind: string;
  state: string;
  /** Why the policy makes this session eligible. */
  reason: string;
  /** Epoch millis of the last modification; null when the daemon sent 0. */
  modifiedAt: number | null;
  estimatedBytes: number;
}

/** The preview `POST /v1/storage/cleanup:plan` answers. */
export interface SessionCleanupPlan {
  /** The one-shot token `applySessionCleanup` needs; bound to the store
   *  generation and policy version, so a changed store makes it stale. */
  confirmationToken: string;
  available: boolean;
  unavailableReason: string;
  generation: string;
  policyVersion: string;
  eligible: CleanupCandidate[];
  eligibleCounts: CleanupCounts;
  protected: CleanupCounts;
  estimatedBytes: number;
  plannedJobId: string;
}

/** A durable cleanup job as apply / get / cancel report it. */
export interface SessionCleanupJob {
  jobId: string;
  /** `planned` | `running` | `completed` | `cancelled` | `stale`. */
  state: string;
  processed: number;
  deleted: number;
  skipped: number;
  /** Candidates that changed between plan and apply — left alone. */
  stale: number;
  failed: number;
  errors: StorageJobItemError[];
}

/** The session kinds a cleanup plan can scope to (engine/session/kind.go). */
export const CLEANUP_KINDS = [
  "main",
  "subagent",
  "parallel_branch",
  "team_member",
  "scheduled",
] as const;
export type CleanupKind = (typeof CLEANUP_KINDS)[number];

const num = (value: bigint | number) => Number(value);

const decodeItemErrors = (
  errors: readonly {
    itemHandle: string;
    reasonCode: string;
    message: string;
  }[],
): StorageJobItemError[] =>
  errors.map((error) => ({
    itemHandle: error.itemHandle,
    reasonCode: error.reasonCode,
    message: error.message,
  }));

export function decodeMigrationPlan(
  plan: SessionMigrationPlanWire,
): StorageMigrationPlan {
  return {
    planId: plan.planId,
    available: plan.available,
    unavailableReason: plan.unavailableReason,
    v1Families: num(plan.v1Families),
    v2Families: num(plan.v2Families),
    invalidFamilies: num(plan.invalidFamilies),
    skippedFamilies: num(plan.skippedFamilies),
    currentBytes: num(plan.currentBytes),
    reclaimableBytes: num(plan.reclaimableBytes),
    temporaryBytes: num(plan.temporaryBytes),
  };
}

export function decodeMigrationJob(
  job: SessionMigrationJobWire,
): StorageMigrationJob {
  return {
    jobId: job.jobId,
    state: job.state,
    v1Families: num(job.v1Families),
    v2Families: num(job.v2Families),
    invalidFamilies: num(job.invalidFamilies),
    skippedFamilies: num(job.skippedFamilies),
    currentBytes: num(job.currentBytes),
    reclaimableBytes: num(job.reclaimableBytes),
    temporaryBytes: num(job.temporaryBytes),
    processed: num(job.processed),
    migrated: num(job.migrated),
    failed: num(job.failed),
    errors: decodeItemErrors(job.errors),
  };
}

const EMPTY_COUNTS: CleanupCounts = {
  total: 0,
  byKind: {},
  byState: {},
  byReason: {},
};

function decodeCounts(counts: CleanupCountsWire | undefined): CleanupCounts {
  if (!counts) return EMPTY_COUNTS;
  return {
    total: counts.total,
    byKind: { ...counts.byKind },
    byState: { ...counts.byState },
    byReason: { ...counts.byReason },
  };
}

export function decodeCleanupPlan(
  plan: PlanSessionCleanupResponse,
): SessionCleanupPlan {
  return {
    confirmationToken: plan.confirmationToken,
    available: plan.available,
    unavailableReason: plan.unavailableReason,
    generation: plan.generation,
    policyVersion: plan.policyVersion,
    eligible: plan.eligible.map((candidate) => ({
      sessionId: candidate.sessionId,
      kind: candidate.kind,
      state: candidate.state,
      reason: candidate.reason,
      modifiedAt:
        num(candidate.modifiedAtUnix) > 0
          ? num(candidate.modifiedAtUnix) * 1000
          : null,
      estimatedBytes: num(candidate.estimatedBytes),
    })),
    eligibleCounts: decodeCounts(plan.eligibleCounts),
    protected: decodeCounts(plan.protected),
    estimatedBytes: num(plan.estimatedBytes),
    plannedJobId: plan.plannedJobId,
  };
}

export function decodeCleanupJob(job: CleanupJobWire): SessionCleanupJob {
  return {
    jobId: job.jobId,
    state: job.state,
    processed: job.processed,
    deleted: job.deleted,
    skipped: job.skipped,
    stale: job.stale,
    failed: job.failed,
    errors: decodeItemErrors(job.errors),
  };
}

/** The batch size Studio proposes; the daemon clamps out-of-range values. */
const DEFAULT_MIGRATION_BATCH_SIZE = 50;

/** `POST /v1/storage/migrations/plan`: a read-only estimate, safe to repeat. */
export async function planStorageMigration(
  signal?: AbortSignal,
): Promise<StorageMigrationPlan> {
  const response = await harness(() =>
    getHarnessClient().storage.planMigration(
      { $typeName: "mecatl.v1.PlanSessionMigrationRequest" },
      { signal },
    ),
  );
  return decodeMigrationPlan(response);
}

/** `POST /v1/storage/migrations/apply`: starts the planned job. Throws
 *  `migration_conflict` when another job is already running. */
export async function applyStorageMigration(
  planId: string,
  batchSize: number = DEFAULT_MIGRATION_BATCH_SIZE,
): Promise<StorageMigrationJob> {
  const response = await harness(() =>
    getHarnessClient().storage.applyMigration({
      $typeName: "mecatl.v1.ApplySessionMigrationRequest",
      planId,
      batchSize,
    }),
  );
  return decodeMigrationJob(response);
}

/** `POST /v1/storage/migrations/{id}/resume`: continues a cancelled or
 *  failed job from its checkpoint. */
export async function resumeStorageMigration(
  jobId: string,
  batchSize: number = DEFAULT_MIGRATION_BATCH_SIZE,
): Promise<StorageMigrationJob> {
  const response = await harness(() =>
    getHarnessClient().storage.resumeMigration({
      $typeName: "mecatl.v1.ResumeSessionMigrationRequest",
      jobId,
      batchSize,
    }),
  );
  return decodeMigrationJob(response);
}

/** `POST /v1/storage/migrations/{id}/cancel`: a forward cancel — the
 *  current batch finishes, nothing is rolled back. */
export async function cancelStorageMigration(
  jobId: string,
): Promise<StorageMigrationJob> {
  const response = await harness(() =>
    getHarnessClient().storage.cancelMigration({
      $typeName: "mecatl.v1.CancelSessionMigrationRequest",
      jobId,
    }),
  );
  return decodeMigrationJob(response);
}

/** `GET /v1/storage/migrations/{id}`. */
export async function fetchStorageMigrationJob(
  jobId: string,
  signal?: AbortSignal,
): Promise<StorageMigrationJob> {
  const response = await harness(() =>
    getHarnessClient().storage.getMigrationJob(
      { $typeName: "mecatl.v1.GetSessionMigrationJobRequest", jobId },
      { signal },
    ),
  );
  return decodeMigrationJob(response);
}

/** `POST /v1/storage/cleanup:plan`: the eligible/protected partition for
 *  the given kinds (empty = every kind), plus the token apply needs. */
export async function planSessionCleanup(
  kinds: readonly string[],
  signal?: AbortSignal,
): Promise<SessionCleanupPlan> {
  const response = await harness(() =>
    getHarnessClient().storage.planCleanup(
      { $typeName: "mecatl.v1.PlanSessionCleanupRequest", kinds: [...kinds] },
      { signal },
    ),
  );
  return decodeCleanupPlan(response);
}

/** `POST /v1/storage/cleanup:apply`: deletes the planned candidates.
 *  Throws `cleanup_plan_stale` when the store changed since the plan. */
export async function applySessionCleanup(
  confirmationToken: string,
): Promise<SessionCleanupJob> {
  const response = await harness(() =>
    getHarnessClient().storage.applyCleanup({
      $typeName: "mecatl.v1.ApplySessionCleanupRequest",
      confirmationToken,
    }),
  );
  return decodeCleanupJob(response);
}

/** `POST /v1/storage/cleanup/jobs/{id}/cancel`. */
export async function cancelSessionCleanup(
  jobId: string,
): Promise<SessionCleanupJob> {
  const response = await harness(() =>
    getHarnessClient().storage.cancelCleanup({
      $typeName: "mecatl.v1.CancelSessionCleanupRequest",
      jobId,
    }),
  );
  return decodeCleanupJob(response);
}

/** `GET /v1/storage/cleanup/jobs/{id}`. */
export async function fetchSessionCleanupJob(
  jobId: string,
  signal?: AbortSignal,
): Promise<SessionCleanupJob> {
  const response = await harness(() =>
    getHarnessClient().storage.getCleanupJob(
      { $typeName: "mecatl.v1.GetSessionCleanupJobRequest", jobId },
      { signal },
    ),
  );
  return decodeCleanupJob(response);
}
