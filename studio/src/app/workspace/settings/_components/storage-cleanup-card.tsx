"use client";

import { TriangleAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type {
  CleanupController,
  MaintenanceError,
} from "@/features/agent/hooks/use-storage-maintenance";
import { useTypedConfirm } from "@/hooks/use-typed-confirm";
import { formatBytes } from "@/lib/formatters";
import {
  CLEANUP_KINDS,
  type CleanupKind,
  type SessionCleanupJob,
  type SessionCleanupPlan,
} from "@/lib/harness/storage";
import { Note } from "./settings-card";

/**
 * "Clean up old runs", the Storage card's action block. Plans a bulk
 * clean-up of every run kind but the person's own chats (the agent's own
 * retention policy decides which runs are old), says how many would go and
 * how many stay, and applies only after the typed CLEAN UP confirmation.
 * The daemon calls are those of the fuller card this replaced; the kind
 * picker, the candidate table and the per-item error table are gone. Gated
 * on `capabilities.storage_cleanup`; without it renders nothing.
 */

/** The typed phrase; case-sensitive, matched exactly. */
const CLEANUP_PHRASE = "CLEAN UP";

/** The kinds a clean-up covers: every run kind, never the person's chats. */
export const RUN_CLEANUP_KINDS: readonly CleanupKind[] = CLEANUP_KINDS.filter(
  (kind) => kind !== "main",
);

const runs = (count: number) => (count === 1 ? "old run" : "old runs");
const items = (count: number) => (count === 1 ? "item" : "items");

/** The typed-confirm dialog's body; exported for its vitest. */
export function cleanupConfirmDescription(plan: SessionCleanupPlan): string {
  const total = plan.eligible.length;
  const freeing =
    plan.estimatedBytes > 0
      ? `, freeing about ${formatBytes(plan.estimatedBytes)}`
      : "";
  const kept =
    plan.protected.total > 0
      ? `, and ${plan.protected.total.toLocaleString()} ${items(
          plan.protected.total,
        )} still needed will be kept`
      : "";
  return `${total.toLocaleString()} ${runs(total)} will be deleted for good${freeing}. Your chats are not affected${kept}. This cannot be undone.`;
}

/** "3 deleted · 1 skipped · 1 could not be deleted"; exported for its vitest. */
export function describeProgress(job: SessionCleanupJob): string {
  const parts = [`${job.deleted.toLocaleString()} deleted`];
  const skipped = job.skipped + job.stale;
  if (skipped > 0) parts.push(`${skipped.toLocaleString()} skipped`);
  if (job.failed > 0) {
    parts.push(`${job.failed.toLocaleString()} could not be deleted`);
  }
  return parts.join(" · ");
}

/** Plain labels for the daemon's job states; unknown states show as-is. */
const JOB_STATE_LABEL: Record<string, string> = {
  planned: "Starting",
  pending: "Starting",
  running: "Running",
  completed: "Completed",
  cancelled: "Cancelled",
  failed: "Failed",
  stale: "Out of date",
  paused: "Paused",
};

type BadgeVariant = "info" | "success" | "warning" | "destructive" | "muted";

const JOB_STATE_VARIANT: Record<string, BadgeVariant> = {
  planned: "info",
  pending: "info",
  running: "info",
  completed: "success",
  cancelled: "warning",
  paused: "warning",
  stale: "warning",
  failed: "destructive",
};

/** Plain words per stable error code; the daemon's own message otherwise. */
const ERROR_FRAMING: Record<string, string> = {
  cleanup_unsupported: "This agent cannot clean up its storage from here.",
  cleanup_plan_stale: "Storage changed since you checked. Check again first.",
  cleanup_backend: "The agent could not clean up its storage. Try again later.",
};

/** Plain words for the plan's `unavailable_reason` tokens. */
const UNAVAILABLE_FRAMING: Record<string, string> = {
  maintenance_exclusion_unavailable:
    "Storage is busy right now. Try again in a moment.",
  backend_unsupported: "This agent cannot clean up its storage from here.",
};

const describeUnavailable = (reason: string) =>
  UNAVAILABLE_FRAMING[reason] ?? "Clean-up is not available right now.";

function ErrorLine({ error }: { error: MaintenanceError }) {
  return (
    <p role="alert" className="flex items-start gap-2 text-sm text-destructive">
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <span>{ERROR_FRAMING[error.code] ?? error.message}</span>
    </p>
  );
}

function Actions({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-end gap-2">
      {children}
    </div>
  );
}

function PlanSummary({ plan }: { plan: SessionCleanupPlan }) {
  const total = plan.eligible.length;
  const freeing =
    plan.estimatedBytes > 0
      ? `, freeing about ${formatBytes(plan.estimatedBytes)}`
      : "";
  return (
    <div className="flex flex-col gap-1 text-sm">
      <p data-testid="storage-cleanup-eligible">
        {total.toLocaleString()} {runs(total)} can be deleted{freeing}.
      </p>
      {plan.protected.total > 0 && (
        <p
          className="text-muted-foreground"
          data-testid="storage-cleanup-protected"
        >
          {plan.protected.total.toLocaleString()} {items(plan.protected.total)}{" "}
          still needed will be kept.
        </p>
      )}
    </div>
  );
}

function JobPanel({
  job,
  cleanup,
}: {
  job: SessionCleanupJob;
  cleanup: CleanupController;
}) {
  const running = cleanup.phase === "running";
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Badge
          variant={JOB_STATE_VARIANT[job.state] ?? "muted"}
          data-testid="storage-cleanup-job-state"
        >
          {JOB_STATE_LABEL[job.state] ?? job.state}
        </Badge>
        <span
          className="text-sm tabular-nums"
          data-testid="storage-cleanup-progress"
        >
          {describeProgress(job)}
        </span>
      </div>
      {cleanup.error && <ErrorLine error={cleanup.error} />}
      <Actions>
        {running ? (
          <Button
            variant="outline"
            className="rounded-full"
            onClick={() => void cleanup.cancel()}
          >
            Cancel
          </Button>
        ) : (
          <Button
            variant="outline"
            className="rounded-full"
            onClick={cleanup.reset}
          >
            Done
          </Button>
        )}
      </Actions>
    </>
  );
}

export interface StorageCleanupProps {
  /** `capabilities.storage_cleanup === true`. */
  supported: boolean;
  cleanup: CleanupController;
}

export function StorageCleanup({ supported, cleanup }: StorageCleanupProps) {
  const { confirmTyped, TypedConfirmDialog } = useTypedConfirm();
  if (!supported) return null;

  const { phase, plan, job, error } = cleanup;

  const runCleanup = async () => {
    if (!plan) return;
    cleanup.beginConfirm();
    const total = plan.eligible.length;
    const confirmed = await confirmTyped({
      title: `Delete ${total.toLocaleString()} ${runs(total)} for good?`,
      description: cleanupConfirmDescription(plan),
      phrase: CLEANUP_PHRASE,
      confirmText: "Clean up",
      destructive: true,
    });
    if (!confirmed) {
      cleanup.abortConfirm();
      return;
    }
    await cleanup.apply();
  };

  let body: React.ReactNode;
  if (error?.code === "management_unauthorized") {
    body = (
      <Note>
        Storage clean-up is not allowed from here. Ask the person who set up the
        agent.
      </Note>
    );
  } else if (
    job &&
    (phase === "running" || phase === "done" || phase === "failed")
  ) {
    body = <JobPanel job={job} cleanup={cleanup} />;
  } else if (phase === "stale") {
    body = (
      <>
        <Note>
          Storage changed since you checked, so check again to see what can be
          deleted.
        </Note>
        <Actions>
          <Button
            variant="action"
            className="rounded-full"
            onClick={() => void cleanup.replan()}
          >
            Check again
          </Button>
        </Actions>
      </>
    );
  } else if (
    plan &&
    (phase === "planned" || phase === "confirming" || phase === "applying")
  ) {
    const busy = phase !== "planned";
    const actionable = plan.available && plan.eligible.length > 0;
    body = (
      <>
        {!plan.available ? (
          <Note>{describeUnavailable(plan.unavailableReason)}</Note>
        ) : actionable ? (
          <PlanSummary plan={plan} />
        ) : (
          <Note>Nothing to clean up right now.</Note>
        )}
        <Actions>
          <Button
            variant="outline"
            className="rounded-full"
            onClick={cleanup.reset}
            disabled={busy}
          >
            {actionable ? "Cancel" : "Done"}
          </Button>
          {actionable && (
            <Button
              variant="destructive"
              className="rounded-full"
              disabled={busy}
              onClick={() => void runCleanup()}
            >
              {phase === "applying" ? "Cleaning up…" : "Clean up…"}
            </Button>
          )}
        </Actions>
      </>
    );
  } else {
    // idle, planning, or a plan failure with nothing to show
    const planning = phase === "planning";
    body = (
      <>
        {phase === "failed" && error && <ErrorLine error={error} />}
        <Actions>
          <Button
            variant="action"
            className="rounded-full"
            disabled={planning}
            onClick={() => void cleanup.planCleanup([...RUN_CLEANUP_KINDS])}
          >
            {planning ? "Looking…" : "Find old runs"}
          </Button>
        </Actions>
      </>
    );
  }

  return (
    <div
      className="flex flex-col gap-3 border-t pt-4"
      data-testid="storage-cleanup"
    >
      <div className="space-y-1">
        <h3 className="text-sm font-medium">Clean up old runs</h3>
        <p className="max-w-xl text-xs text-muted-foreground">
          Deletes finished runs the agent no longer needs, leaving your chats
          untouched.
        </p>
      </div>
      {body}
      {TypedConfirmDialog}
    </div>
  );
}
