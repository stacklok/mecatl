"use client";

import { Badge } from "@/components/ui/badge";
import type { ScheduleRow } from "@/lib/protocol";
import { cn } from "@/lib/utils";

export interface ScheduleStatusMeta {
  label: string;
  variant: "default" | "secondary" | "success" | "warning" | "destructive";
  /** A fire is live right now (claimed or running) — rendered with a pulse. */
  live: boolean;
}

/**
 * fireStage outranks enabled: a paused schedule can still be mid-fire (pause
 * stops future fires, not the one already claimed), and the live fire is the
 * more urgent fact.
 */
export function scheduleStatusOf(row: ScheduleRow): ScheduleStatusMeta {
  if (row.fireStage === "running")
    return { label: "Running", variant: "success", live: true };
  if (row.fireStage === "claimed")
    return { label: "Claimed", variant: "warning", live: true };
  return row.enabled
    ? { label: "Scheduled", variant: "success", live: false }
    : { label: "Paused", variant: "secondary", live: false };
}

function dotClass(variant: ScheduleStatusMeta["variant"]): string {
  switch (variant) {
    case "success":
      return "bg-success";
    case "warning":
      return "bg-warning";
    default:
      return "bg-muted-foreground/50";
  }
}

/**
 * Quiet status dot for the card grid: color carries the state, with the
 * label kept for screen readers and hover.
 */
export function ScheduleStatusDot({ row }: { row: ScheduleRow }) {
  const status = scheduleStatusOf(row);
  return (
    <span title={status.label} className="inline-flex shrink-0 items-center">
      <span
        aria-hidden="true"
        className={cn(
          "size-1.5 rounded-full",
          dotClass(status.variant),
          status.live && "animate-pulse",
        )}
      />
      <span className="sr-only">{status.label}</span>
    </span>
  );
}

/** Status as a badge, for the detail header's pill row. */
export function ScheduleStatusBadge({ row }: { row: ScheduleRow }) {
  const status = scheduleStatusOf(row);
  return (
    <Badge variant={status.variant}>
      {status.live && (
        <span
          aria-hidden="true"
          className="size-1.5 animate-pulse rounded-full bg-current"
        />
      )}
      {status.label}
    </Badge>
  );
}
