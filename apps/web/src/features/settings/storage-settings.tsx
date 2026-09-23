// SPDX-License-Identifier: Apache-2.0

import type { GetStorageHealthResponse } from "@mecatl-studio/contracts/generated";
import { getStorageHealthOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { StateCard } from "../knowledge/knowledge-workspace";

/**
 * Settings → Storage: a plain-words summary of what the agent has saved —
 * a status pill, how many chats and runs, and the space they use. Read-only
 * for now; Studio's own "Clean up old runs" action is a separate follow-up.
 */
export function StorageSettings() {
  const query = useQuery(getStorageHealthOptions());

  if (query.isPending) return <StateCard text="Loading storage details…" />;
  if (query.isError) return <StateCard error text={errorMessage(query.error)} />;
  if (!query.data.supported) {
    return <StateCard text="This agent cannot report on its storage." title="Storage" />;
  }

  return (
    <section className="rounded-2xl border bg-card p-5 sm:p-6">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">Storage</h2>
        <Button onClick={() => void query.refetch()} size="sm" variant="outline">
          <RefreshCw aria-hidden="true" className="size-3.5" />
          Refresh
        </Button>
      </div>
      <div className="mt-4">
        <StorageHealthSummary health={query.data} />
      </div>
    </section>
  );
}

function StorageHealthSummary({ health }: { health: GetStorageHealthResponse }) {
  const status = storageHealthStatus(health);
  const mix = describeSessionMix(health);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={status.label === "Healthy" ? "success" : "warning"}>{status.label}</Badge>
        {status.detail && <span className="text-sm text-muted-foreground">{status.detail}</span>}
      </div>
      {health.available && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Saved chats and runs</dt>
          <dd className="tabular-nums">
            {formatCount(health.sessionCount)}
            {mix && <span className="ml-2 text-muted-foreground"> {mix}</span>}
          </dd>
          <dt className="text-muted-foreground">Space used</dt>
          <dd className="tabular-nums">
            {health.currentBytes === null ? "Not available" : formatBytes(health.currentBytes)}
          </dd>
          {health.reclaimableBytes !== null && (
            <>
              <dt className="text-muted-foreground">Can be freed</dt>
              <dd className="tabular-nums">{formatBytes(health.reclaimableBytes)}</dd>
            </>
          )}
        </dl>
      )}
    </div>
  );
}

/** The status pill and its one-line detail. Exported for its unit test. */
export function storageHealthStatus(health: GetStorageHealthResponse): {
  label: "Healthy" | "Needs attention";
  detail: string;
} {
  if (!health.available) {
    return { detail: "The agent cannot read its saved chats right now.", label: "Needs attention" };
  }
  const corruptCount = Number(health.corruptCount);
  if (corruptCount > 0) {
    return {
      detail: `${formatCount(health.corruptCount)} saved ${corruptCount === 1 ? "item" : "items"} can no longer be opened.`,
      label: "Needs attention",
    };
  }
  if (health.lastFailure) {
    return { detail: "A recent automatic clean-up did not finish.", label: "Needs attention" };
  }
  return { detail: health.activeJob ? "Storage maintenance is running." : "", label: "Healthy" };
}

/** "5 chats · 4 agent runs · 2 scheduled runs · 1 other", zeros omitted. Exported for its unit test. */
export function describeSessionMix(health: GetStorageHealthResponse): string {
  const parts: string[] = [];
  const add = (count: string, singular: string, plural: string) => {
    const n = Number(count);
    if (n > 0) parts.push(`${formatCount(count)} ${n === 1 ? singular : plural}`);
  };
  add(health.mainCount, "chat", "chats");
  add(health.childCount, "agent run", "agent runs");
  add(health.scheduledCount, "scheduled run", "scheduled runs");
  add(health.unknownCount, "other", "other");
  return parts.join(" · ");
}

function formatCount(value: string): string {
  const n = Number(value);
  return Number.isFinite(n) ? n.toLocaleString() : value;
}

/** "512 B", "3.4 KB", "1.2 GB". Exported for its unit test. */
export function formatBytes(value: string): string {
  const bytes = Number(value);
  if (!Number.isFinite(bytes) || bytes < 0) return value;
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let scaled = bytes / 1024;
  let unitIndex = 0;
  while (scaled >= 1024 && unitIndex < units.length - 1) {
    scaled /= 1024;
    unitIndex += 1;
  }
  return `${scaled.toFixed(scaled < 10 ? 1 : 0)} ${units[unitIndex]}`;
}

function errorMessage(error: unknown) {
  if (typeof error === "object" && error !== null && "detail" in error) return String(error.detail);
  return error instanceof Error ? error.message : "Storage details could not be loaded.";
}
