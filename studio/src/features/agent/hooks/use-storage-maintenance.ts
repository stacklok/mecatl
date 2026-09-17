"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { HarnessApiError } from "@/lib/harness/errors";
import {
  applySessionCleanup,
  applyStorageMigration,
  cancelSessionCleanup,
  cancelStorageMigration,
  fetchSessionCleanupJob,
  fetchStorageMigrationJob,
  isStorageJobActive,
  planSessionCleanup,
  planStorageMigration,
  resumeStorageMigration,
  type SessionCleanupJob,
  type SessionCleanupPlan,
  type StorageMigrationJob,
  type StorageMigrationPlan,
} from "@/lib/harness/storage";
import { notifySessionsChanged } from "../sessions-changed";

/**
 * The Storage page's two maintenance flows as explicit state machines over
 * the daemon's durable jobs (ADR 0226):
 *
 *   migration: idle → planning → planned → applying → running → done | failed
 *   cleanup:   idle → planning → planned → confirming → applying → running
 *              → done | failed | stale (the store changed — plan again)
 *
 * While a job is `running` the hook polls it every `JOB_POLL_INTERVAL_MS`,
 * stops on the first terminal state, clears the timer on unmount, and calls
 * `refreshHealth` once the job settles so the health card shows the store
 * the job left behind. A finished cleanup also announces the deleted rows
 * (`notifySessionsChanged`) so the sidebar re-walks its inventory now.
 *
 * Errors keep the daemon's stable machine `code`; the cards branch on it —
 * `management_unauthorized` renders read-only copy, `migration_conflict`
 * points at the job already running, `cleanup_plan_stale` asks for a
 * re-plan — and show the message otherwise.
 */

export const JOB_POLL_INTERVAL_MS = 2_000;

export type MigrationPhase =
  | "idle"
  | "planning"
  | "planned"
  | "applying"
  | "running"
  | "done"
  | "failed";

export type CleanupPhase =
  | "idle"
  | "planning"
  | "planned"
  | "confirming"
  | "applying"
  | "running"
  | "done"
  | "stale"
  | "failed";

export interface MaintenanceError {
  /** The daemon's machine code ("" for a transport or unknown failure). */
  code: string;
  message: string;
}

export interface MigrationController {
  phase: MigrationPhase;
  plan: StorageMigrationPlan | null;
  job: StorageMigrationJob | null;
  error: MaintenanceError | null;
  /** The read-only estimate; safe to repeat. */
  estimate(): Promise<void>;
  /** Starts the planned job with the given batch size. */
  apply(batchSize: number): Promise<void>;
  /** Forward-cancels the running job (the current batch finishes). */
  cancel(): Promise<void>;
  /** Continues a cancelled or failed job from its checkpoint. */
  resume(batchSize: number): Promise<void>;
  /** Back to idle, forgetting the plan, job and error. */
  reset(): void;
}

export interface CleanupController {
  phase: CleanupPhase;
  /** The kinds the current (or last) plan scoped to; what `replan` reuses. */
  kinds: readonly string[];
  plan: SessionCleanupPlan | null;
  job: SessionCleanupJob | null;
  error: MaintenanceError | null;
  planCleanup(kinds: readonly string[]): Promise<void>;
  /** The typed confirmation is open; blocks a concurrent re-plan. */
  beginConfirm(): void;
  /** The typed confirmation was dismissed without confirming. */
  abortConfirm(): void;
  /** Applies the plan with its confirmation token. */
  apply(): Promise<void>;
  cancel(): Promise<void>;
  /** Plans again with the same kinds (the `cleanup_plan_stale` path). */
  replan(): Promise<void>;
  reset(): void;
}

export function toMaintenanceError(error: unknown): MaintenanceError {
  if (error instanceof HarnessApiError) {
    return { code: error.code, message: error.message };
  }
  return {
    code: "",
    message: error instanceof Error ? error.message : String(error),
  };
}

export function useStorageMaintenance({
  refreshHealth,
}: {
  refreshHealth: () => void;
}): { migration: MigrationController; cleanup: CleanupController } {
  // The latest refresh callback, read at settle time so the poll effects do
  // not re-subscribe every time the caller re-renders.
  const refreshRef = useRef(refreshHealth);
  useEffect(() => {
    refreshRef.current = refreshHealth;
  }, [refreshHealth]);

  // --- migration -----------------------------------------------------------

  const [migrationPhase, setMigrationPhase] = useState<MigrationPhase>("idle");
  const [migrationPlan, setMigrationPlan] =
    useState<StorageMigrationPlan | null>(null);
  const [migrationJob, setMigrationJob] = useState<StorageMigrationJob | null>(
    null,
  );
  const [migrationError, setMigrationError] = useState<MaintenanceError | null>(
    null,
  );

  const adoptMigrationJob = useCallback((job: StorageMigrationJob) => {
    setMigrationJob(job);
    if (isStorageJobActive(job.state)) {
      setMigrationPhase("running");
      return;
    }
    setMigrationPhase(job.state === "failed" ? "failed" : "done");
    refreshRef.current();
  }, []);

  const estimate = useCallback(async () => {
    setMigrationPhase("planning");
    setMigrationError(null);
    setMigrationJob(null);
    try {
      setMigrationPlan(await planStorageMigration());
      setMigrationPhase("planned");
    } catch (error) {
      setMigrationError(toMaintenanceError(error));
      setMigrationPhase("failed");
    }
  }, []);

  const applyMigration = useCallback(
    async (batchSize: number) => {
      if (!migrationPlan) return;
      setMigrationPhase("applying");
      setMigrationError(null);
      try {
        adoptMigrationJob(
          await applyStorageMigration(migrationPlan.planId, batchSize),
        );
      } catch (error) {
        setMigrationError(toMaintenanceError(error));
        setMigrationPhase("failed");
      }
    },
    [migrationPlan, adoptMigrationJob],
  );

  const resumeMigration = useCallback(
    async (batchSize: number) => {
      if (!migrationJob) return;
      setMigrationPhase("applying");
      setMigrationError(null);
      try {
        adoptMigrationJob(
          await resumeStorageMigration(migrationJob.jobId, batchSize),
        );
      } catch (error) {
        setMigrationError(toMaintenanceError(error));
        setMigrationPhase("failed");
      }
    },
    [migrationJob, adoptMigrationJob],
  );

  const cancelMigration = useCallback(async () => {
    if (!migrationJob) return;
    try {
      adoptMigrationJob(await cancelStorageMigration(migrationJob.jobId));
    } catch (error) {
      setMigrationError(toMaintenanceError(error));
    }
  }, [migrationJob, adoptMigrationJob]);

  const resetMigration = useCallback(() => {
    setMigrationPhase("idle");
    setMigrationPlan(null);
    setMigrationJob(null);
    setMigrationError(null);
  }, []);

  const migrationJobId = migrationJob?.jobId ?? null;
  const migrationActive =
    migrationJob !== null && isStorageJobActive(migrationJob.state);
  useEffect(() => {
    if (!migrationJobId || !migrationActive) return;
    const controller = new AbortController();
    const timer = setInterval(() => {
      fetchStorageMigrationJob(migrationJobId, controller.signal)
        .then((job) => {
          if (!controller.signal.aborted) adoptMigrationJob(job);
        })
        .catch((error) => {
          if (controller.signal.aborted) return;
          setMigrationError(toMaintenanceError(error));
          setMigrationPhase("failed");
        });
    }, JOB_POLL_INTERVAL_MS);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [migrationJobId, migrationActive, adoptMigrationJob]);

  // --- cleanup -------------------------------------------------------------

  const [cleanupPhase, setCleanupPhase] = useState<CleanupPhase>("idle");
  const [cleanupKinds, setCleanupKinds] = useState<readonly string[]>([]);
  const [cleanupPlan, setCleanupPlan] = useState<SessionCleanupPlan | null>(
    null,
  );
  const [cleanupJob, setCleanupJob] = useState<SessionCleanupJob | null>(null);
  const [cleanupError, setCleanupError] = useState<MaintenanceError | null>(
    null,
  );

  const adoptCleanupJob = useCallback((job: SessionCleanupJob) => {
    setCleanupJob(job);
    if (isStorageJobActive(job.state)) {
      setCleanupPhase("running");
      return;
    }
    setCleanupPhase(job.state === "failed" ? "failed" : "done");
    refreshRef.current();
    if (job.deleted > 0) notifySessionsChanged();
  }, []);

  const planCleanupFor = useCallback(async (kinds: readonly string[]) => {
    setCleanupKinds([...kinds]);
    setCleanupPhase("planning");
    setCleanupError(null);
    setCleanupJob(null);
    try {
      setCleanupPlan(await planSessionCleanup(kinds));
      setCleanupPhase("planned");
    } catch (error) {
      setCleanupPlan(null);
      setCleanupError(toMaintenanceError(error));
      setCleanupPhase("failed");
    }
  }, []);

  const replan = useCallback(
    () => planCleanupFor(cleanupKinds),
    [planCleanupFor, cleanupKinds],
  );

  const beginConfirm = useCallback(() => {
    setCleanupPhase((phase) => (phase === "planned" ? "confirming" : phase));
  }, []);

  const abortConfirm = useCallback(() => {
    setCleanupPhase((phase) => (phase === "confirming" ? "planned" : phase));
  }, []);

  const applyCleanup = useCallback(async () => {
    if (!cleanupPlan) return;
    setCleanupPhase("applying");
    setCleanupError(null);
    try {
      adoptCleanupJob(await applySessionCleanup(cleanupPlan.confirmationToken));
    } catch (error) {
      const failure = toMaintenanceError(error);
      setCleanupError(failure);
      setCleanupPhase(
        failure.code === "cleanup_plan_stale" ? "stale" : "failed",
      );
    }
  }, [cleanupPlan, adoptCleanupJob]);

  const cancelCleanup = useCallback(async () => {
    if (!cleanupJob) return;
    try {
      adoptCleanupJob(await cancelSessionCleanup(cleanupJob.jobId));
    } catch (error) {
      setCleanupError(toMaintenanceError(error));
    }
  }, [cleanupJob, adoptCleanupJob]);

  const resetCleanup = useCallback(() => {
    setCleanupPhase("idle");
    setCleanupPlan(null);
    setCleanupJob(null);
    setCleanupError(null);
  }, []);

  const cleanupJobId = cleanupJob?.jobId ?? null;
  const cleanupActive =
    cleanupJob !== null && isStorageJobActive(cleanupJob.state);
  useEffect(() => {
    if (!cleanupJobId || !cleanupActive) return;
    const controller = new AbortController();
    const timer = setInterval(() => {
      fetchSessionCleanupJob(cleanupJobId, controller.signal)
        .then((job) => {
          if (!controller.signal.aborted) adoptCleanupJob(job);
        })
        .catch((error) => {
          if (controller.signal.aborted) return;
          setCleanupError(toMaintenanceError(error));
          setCleanupPhase("failed");
        });
    }, JOB_POLL_INTERVAL_MS);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [cleanupJobId, cleanupActive, adoptCleanupJob]);

  return {
    migration: {
      phase: migrationPhase,
      plan: migrationPlan,
      job: migrationJob,
      error: migrationError,
      estimate,
      apply: applyMigration,
      cancel: cancelMigration,
      resume: resumeMigration,
      reset: resetMigration,
    },
    cleanup: {
      phase: cleanupPhase,
      kinds: cleanupKinds,
      plan: cleanupPlan,
      job: cleanupJob,
      error: cleanupError,
      planCleanup: planCleanupFor,
      beginConfirm,
      abortConfirm,
      apply: applyCleanup,
      cancel: cancelCleanup,
      replan,
      reset: resetCleanup,
    },
  };
}
