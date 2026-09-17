"use client";

import { CalendarClock } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { DeliveryNoteInfo } from "@/lib/protocol/delivery-note";
import { cn } from "@/lib/utils";

/** Bodies longer than this are clamped behind "Show more". */
const CLAMP_LINES = 8;
const CLAMP_CHARS = 800;

export type DeliveryBadge = {
  label: string;
  variant: "secondary" | "success" | "destructive";
};

/**
 * The outcome badge: a start note reads "started"; a completed note maps the
 * daemon's stop reason — `end_turn` is the clean end ("completed"), `error`
 * is "failed", any other stop (max_turns, budget, cancelled, …) shows the
 * daemon's own word rather than a guess.
 */
export function deliveryBadge(delivery: DeliveryNoteInfo): DeliveryBadge {
  if (delivery.kind === "started") {
    return { label: "started", variant: "secondary" };
  }
  const stop = delivery.stop ?? "";
  if (stop === "" || stop === "end_turn") {
    return { label: "completed", variant: "success" };
  }
  if (stop === "error") return { label: "failed", variant: "destructive" };
  return { label: stop, variant: "secondary" };
}

/** The schedule detail route; the segment is the schedule NAME. */
export function scheduleHref(scheduleName: string): string {
  return `/workspace/schedules/${encodeURIComponent(scheduleName)}`;
}

/**
 * A scheduled-task delivery note in the chat: who (the schedule, linked to
 * its detail page), which fire, how it ended, and — for a completed fire —
 * the outcome text. The body is model-authored, untrusted content: it renders
 * as PLAIN text (never through markdown), clamped past eight lines.
 */
export function DeliveryNoteCard({
  delivery,
  body,
}: {
  delivery: DeliveryNoteInfo;
  body: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const badge = deliveryBadge(delivery);
  const long =
    body.split("\n").length > CLAMP_LINES || body.length > CLAMP_CHARS;
  return (
    <article
      aria-label={`Scheduled task ${delivery.scheduleName}`}
      className="rounded-lg border border-border bg-muted/30 px-3 py-2"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <CalendarClock
          className="size-4 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
        <span className="text-muted-foreground">Scheduled task</span>
        <Link
          href={scheduleHref(delivery.scheduleName)}
          className="rounded-sm font-medium text-foreground underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {delivery.scheduleName}
        </Link>
        <span className="font-mono text-[11px] text-muted-foreground">
          fire {delivery.fireId}
        </span>
        <Badge variant={badge.variant} className="ml-auto">
          {badge.label}
        </Badge>
      </div>
      {delivery.kind === "completed" && (
        <>
          <div
            className={cn(
              "mt-1.5 whitespace-pre-wrap break-words text-sm leading-relaxed text-foreground/80",
              long && !expanded && "line-clamp-8",
            )}
          >
            {body}
          </div>
          {long && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="mt-1 h-7 px-2 text-xs"
              aria-expanded={expanded}
              onClick={() => setExpanded((value) => !value)}
            >
              {expanded ? "Show less" : "Show more"}
            </Button>
          )}
        </>
      )}
    </article>
  );
}
