"use client";

import { RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatBytes } from "@/lib/formatters";
import type { StorageHealth } from "@/lib/harness/storage";
import { Note, OfflineNote, SettingsCard } from "./settings-card";

/**
 * The Storage page's one card: a plain-words summary of what the agent has
 * saved — a status pill with a one-line reason, how many chats and runs,
 * the space they use and how much a clean-up could free — plus a Refresh,
 * with the clean-up block passed in as `children` beneath it. Reads the
 * same `StorageHealth` the workspace banner does, but shows a healthy store
 * too. Files, layout generations, job ids and the last raw failure are not
 * shown; the status line covers what a person needs to know.
 */

export interface StorageHealthCardProps {
  /** The runtime is reachable. */
  live: boolean;
  /** `capabilities.storage_health === true` on the connected daemon. */
  supported: boolean;
  health: StorageHealth | null;
  onRefresh: () => void;
  /** The clean-up block, rendered under the summary while the runtime is live. */
  children?: React.ReactNode;
}

/** The status pill and its one-line detail; exported for its vitest. */
export function storageHealthStatus(health: StorageHealth): {
  label: "Healthy" | "Needs attention";
  detail: string;
} {
  if (!health.available) {
    return {
      label: "Needs attention",
      detail: "The agent cannot read its saved chats right now.",
    };
  }
  if (health.corruptCount > 0) {
    return {
      label: "Needs attention",
      detail: `${health.corruptCount.toLocaleString()} saved ${
        health.corruptCount === 1 ? "item" : "items"
      } can no longer be opened.`,
    };
  }
  if (health.lastFailure) {
    return {
      label: "Needs attention",
      detail: "A recent automatic clean-up did not finish.",
    };
  }
  return {
    label: "Healthy",
    detail: health.activeJob ? "Storage maintenance is running." : "",
  };
}

/** "5 chats · 4 agent runs · 2 scheduled runs · 1 other", zeros omitted.
 *  Exported for its vitest. */
export function describeSessionMix(health: StorageHealth): string {
  const parts: string[] = [];
  const add = (count: number, singular: string, plural: string) => {
    if (count > 0) {
      parts.push(
        `${count.toLocaleString()} ${count === 1 ? singular : plural}`,
      );
    }
  };
  add(health.mainCount, "chat", "chats");
  add(health.childCount, "agent run", "agent runs");
  add(health.scheduledCount, "scheduled run", "scheduled runs");
  add(health.unknownCount, "other", "other");
  return parts.join(" · ");
}

function Stat({
  label,
  value,
  testId,
}: {
  label: string;
  value: React.ReactNode;
  testId: string;
}) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="tabular-nums" data-testid={testId}>
        {value}
      </dd>
    </>
  );
}

export function StorageHealthCard({
  live,
  supported,
  health,
  onRefresh,
  children,
}: StorageHealthCardProps) {
  const title = "Storage";
  if (!live) {
    return (
      <SettingsCard title={title}>
        <OfflineNote />
      </SettingsCard>
    );
  }

  const refreshButton = (
    <Button
      variant="outline"
      size="sm"
      className="rounded-full"
      onClick={onRefresh}
    >
      <RefreshCw aria-hidden="true" className="size-3.5" />
      Refresh
    </Button>
  );

  let summary: React.ReactNode;
  if (!supported) {
    summary = <Note>This agent cannot report on its storage.</Note>;
  } else if (health === null) {
    summary = (
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Note>Storage details are not available right now.</Note>
        {refreshButton}
      </div>
    );
  } else {
    const status = storageHealthStatus(health);
    const mix = describeSessionMix(health);
    summary = (
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <Badge
              variant={status.label === "Healthy" ? "success" : "warning"}
              data-testid="storage-health-status"
            >
              {status.label}
            </Badge>
            {status.detail && (
              <span
                className="text-sm text-muted-foreground"
                data-testid="storage-health-detail"
              >
                {status.detail}
              </span>
            )}
          </div>
          {refreshButton}
        </div>

        {health.available && (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
            <Stat
              label="Saved chats and runs"
              testId="storage-health-sessions"
              value={
                <>
                  {health.sessionCount.toLocaleString()}
                  {mix && (
                    <span className="ml-2 text-muted-foreground">{mix}</span>
                  )}
                </>
              }
            />
            <Stat
              label="Space used"
              testId="storage-health-size"
              value={
                health.currentBytes === null
                  ? "Not available"
                  : formatBytes(health.currentBytes)
              }
            />
            {health.reclaimableBytes !== null && (
              <Stat
                label="Can be freed"
                testId="storage-health-reclaimable"
                value={formatBytes(health.reclaimableBytes)}
              />
            )}
          </dl>
        )}
      </div>
    );
  }

  return (
    <SettingsCard
      title={title}
      description="What the agent has saved, and a way to clear out old runs."
    >
      <div className="flex flex-col gap-4">
        {summary}
        {children}
      </div>
    </SettingsCard>
  );
}
