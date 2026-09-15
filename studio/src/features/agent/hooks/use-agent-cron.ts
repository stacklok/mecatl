"use client";

import { useCallback, useEffect, useState } from "react";
import {
  harnessScheduleAction,
  listScheduleRows,
  saveHarnessSchedule,
} from "@/lib/harness/client";
import {
  PERMISSION_MODES,
  type ScheduleCarriedSpec,
  type ScheduleRow,
  type ScheduleSpecDraft,
} from "@/lib/protocol";
import { useRuntimeStatus } from "../runtime-status";
import type { CreateCronOpts, CronJob } from "../types";

/** Fields the create-schedule form supplies on top of the shared opts. */
export type CreateJobInput = CreateCronOpts & { enabled?: boolean };

function toCronJob(row: ScheduleRow): CronJob {
  return {
    id: row.name,
    name: row.name,
    schedule: row.cron || "one-shot",
    instruction: row.prompt,
    enabled: row.enabled,
    status: row.fireStage === "idle" ? "idle" : "running",
    lastRunAt: row.lastFireAt,
    output:
      row.fireCount > 0
        ? `${row.fireCount} fire${row.fireCount === 1 ? "" : "s"} so far`
        : null,
    lastRunSessionId: row.lastFireSessionId || undefined,
    prompt: row.prompt,
  };
}

/**
 * Scheduled agent runs, backed by the daemon's schedule registry.
 *
 * A live schedule fires an agent run unattended, so this hook reads the
 * daemon's durable state back after every action rather than predicting what
 * an action produced.
 *
 * A deployment without a schedule store answers the list with an error
 * (there is no scheduler to be empty): that is the distinct NOT-WIRED state,
 * never conflated with an empty registry.
 */
export function useAgentCron() {
  const { connected } = useRuntimeStatus();
  const [rows, setRows] = useState<ScheduleRow[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [notWired, setNotWired] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const schedules = await listScheduleRows(signal);
      if (signal?.aborted) return;
      setRows(schedules);
      setNotWired(null);
    } catch (caught) {
      if (signal?.aborted) return;
      setRows([]);
      setNotWired(caught instanceof Error ? caught.message : String(caught));
    } finally {
      if (!signal?.aborted) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [connected, load]);

  const refresh = useCallback(async () => {
    await load();
  }, [load]);

  /** Runs one action then re-reads durable state; refusals surface verbatim. */
  const perform = useCallback(
    async (action: () => Promise<void>) => {
      setError(null);
      try {
        await action();
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        throw caught;
      } finally {
        await load();
      }
    },
    [load],
  );

  const createJob = useCallback(
    async (opts: CreateJobInput) => {
      // The quick-create path builds a read-only schedule: plan mode, not
      // mutating — the pairing the daemon accepts without a write opt-in.
      const draft: ScheduleSpecDraft = {
        name: opts.name,
        prompt: opts.instruction,
        trigger: { kind: "cron", cron: opts.schedule, timezone: "" },
        profile: "",
        mode: PERMISSION_MODES.PERMISSION_MODE_PLAN,
        mutating: false,
        maxFires: 0,
        limits: { maxTurns: 0, maxToolCalls: 0, maxConsecutiveFailures: 0 },
        oneShotRetry: false,
        oneShotMaxRetries: 0,
      };
      await perform(() => saveHarnessSchedule(draft, { update: false }));
      const job = rows.find((row) => row.name === opts.name);
      return job ? toCronJob(job) : undefined;
    },
    [perform, rows],
  );

  /** Full authoring path: the dialog builds the draft, the daemon judges it. */
  const createFromDraft = useCallback(
    async (draft: ScheduleSpecDraft) => {
      await perform(() => saveHarnessSchedule(draft, { update: false }));
    },
    [perform],
  );

  /**
   * PUT replaces the whole spec, so the row's carried fields must ride along
   * or every field this UI has no control for would be silently deleted.
   */
  const updateFromDraft = useCallback(
    async (draft: ScheduleSpecDraft, carried: ScheduleCarriedSpec) => {
      await perform(() =>
        saveHarnessSchedule(draft, { update: true, carried }),
      );
    },
    [perform],
  );

  /**
   * Save an edit under a NEW name. The daemon has no rename — PUT force-stamps
   * the path name onto the body and fire history is keyed by name — so a
   * rename is re-create + delete, in fail-safe order: create the new name
   * first (carrying the stored spec fields), mirror a paused state onto it,
   * then delete the old entry. A failed create leaves the old schedule
   * untouched; a failure after the create is reported honestly as "both
   * entries now exist" rather than pretending success. Run history stays with
   * the old name and is deleted with it.
   */
  const renameAndUpdateFromDraft = useCallback(
    async (draft: ScheduleSpecDraft, previous: ScheduleRow) => {
      const errorDetail = (caught: unknown) =>
        caught instanceof Error ? caught.message : String(caught);
      await perform(async () => {
        await saveHarnessSchedule(draft, {
          update: false,
          carried: previous.carried,
        });
        if (!previous.enabled) {
          try {
            await harnessScheduleAction(draft.name, "pause");
          } catch (caught) {
            throw new Error(
              `Created "${draft.name}" but pausing it failed — both entries exist now; ` +
                `pause "${draft.name}" and delete "${previous.name}" manually. (${errorDetail(caught)})`,
            );
          }
        }
        try {
          await harnessScheduleAction(previous.name, "delete");
        } catch (caught) {
          throw new Error(
            `Created "${draft.name}" but deleting the old "${previous.name}" failed — ` +
              `both entries exist now; delete "${previous.name}" manually. (${errorDetail(caught)})`,
          );
        }
      });
    },
    [perform],
  );

  const runJob = useCallback(
    async (jobId: string) => {
      // FireNow is synchronous on the daemon: this await lasts the whole run.
      await perform(() => harnessScheduleAction(jobId, "fire"));
    },
    [perform],
  );

  const deleteJob = useCallback(
    async (jobId: string) => {
      await perform(() => harnessScheduleAction(jobId, "delete"));
    },
    [perform],
  );

  const pauseJob = useCallback(
    async (jobId: string) => {
      await perform(() => harnessScheduleAction(jobId, "pause"));
    },
    [perform],
  );

  const resumeJob = useCallback(
    async (jobId: string) => {
      await perform(() => harnessScheduleAction(jobId, "resume"));
    },
    [perform],
  );

  return {
    jobs: rows.map(toCronJob),
    /** Full decoded rows: mode/mutating badges, carried spec for edits. */
    rows,
    isLoading: isLoading && connected,
    isSupported: notWired === null,
    /** The daemon's own words for why scheduling is unavailable, when it is. */
    notWired,
    error,
    harnessLive: connected,
    createJob,
    createFromDraft,
    updateFromDraft,
    renameAndUpdateFromDraft,
    runJob,
    deleteJob,
    pauseJob,
    resumeJob,
    refresh,
  };
}
