// SPDX-License-Identifier: Apache-2.0

import { CalendarClock } from "lucide-react";
import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { ChatMessage } from "./chat-state";

export type ChatDelivery = NonNullable<ChatMessage["delivery"]>;

/** Bodies longer than this are clamped behind "Show more". */
const CLAMP_LINES = 8;
const CLAMP_CHARS = 800;

export interface DeliveryBadge {
  label: string;
  variant: "secondary" | "success" | "destructive";
}

/**
 * The outcome badge: a start note reads "started"; a completed note maps the
 * daemon's stop reason. `end_turn` (or none) is the clean end, "completed";
 * `error` is "failed"; any other stop shows the daemon's own word.
 */
export function deliveryBadge(delivery: ChatDelivery): DeliveryBadge {
  if (delivery.kind === "started") return { label: "started", variant: "secondary" };
  const stop = delivery.stop ?? "";
  if (stop === "" || stop === "end_turn") return { label: "completed", variant: "success" };
  if (stop === "error") return { label: "failed", variant: "destructive" };
  return { label: stop, variant: "secondary" };
}

/**
 * A scheduled-task delivery note, ported from the prototype: the schedule,
 * the fire, how it ended, and the note's text. The text is model-authored
 * and untrusted (the BFF already stripped the daemon's fence), so it renders
 * as plain text, never through markdown, clamped past eight lines.
 */
export function DeliveryNoteCard({ body, delivery }: { body: string; delivery: ChatDelivery }) {
  const [expanded, setExpanded] = useState(false);
  const badge = deliveryBadge(delivery);
  const long = body.split("\n").length > CLAMP_LINES || body.length > CLAMP_CHARS;
  return (
    <div className="min-w-0 rounded-lg border bg-muted/30 px-3 py-2" data-delivery-note>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <CalendarClock aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        <span className="text-muted-foreground">Scheduled task</span>
        <span className="min-w-0 break-words font-medium text-foreground">
          {delivery.scheduleName}
        </span>
        <span className="min-w-0 break-all font-mono text-[11px] text-muted-foreground">
          fire {delivery.fireId}
        </span>
        <Badge
          className="ml-auto"
          title={delivery.stop ? `Stop reason: ${delivery.stop}` : undefined}
          variant={badge.variant}
        >
          {badge.label}
        </Badge>
      </div>
      {body && (
        <>
          <p
            className={cn(
              "mt-1.5 whitespace-pre-wrap break-words text-sm leading-relaxed text-foreground/80",
              long && !expanded && "line-clamp-8",
            )}
          >
            {body}
          </p>
          {long && (
            <Button
              aria-expanded={expanded}
              className="mt-1 h-7 px-2 text-xs"
              onClick={() => setExpanded((value) => !value)}
              size="sm"
              type="button"
              variant="ghost"
            >
              {expanded ? "Show less" : "Show more"}
            </Button>
          )}
        </>
      )}
    </div>
  );
}
