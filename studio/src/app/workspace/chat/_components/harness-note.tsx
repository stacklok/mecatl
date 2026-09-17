"use client";

import { ClipboardCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { AgentMessage } from "@/features/agent";
import { formatMessageTime } from "@/lib/formatters";

/**
 * A user-role turn the HARNESS authored on the operator's behalf — the
 * "Plan approved by operator. Proceed with execution." prompt that starts
 * the execution run after a plan review. It is a real recorded user turn on
 * the daemon (every client sends the same text), but presenting it as the
 * user's own words would misattribute it, so it renders as a muted note in
 * the message-row geometry with a small "harness" chip.
 */
export function HarnessNote({ message }: { message: AgentMessage }) {
  return (
    <div
      className="flex gap-2 rounded-lg px-2 py-2 -mx-2 lg:gap-3 lg:px-3 lg:-mx-3"
      data-testid="harness-note"
    >
      <div className="pt-0.5">
        <span
          className="flex size-7 items-center justify-center rounded-full bg-muted text-muted-foreground lg:size-9"
          aria-hidden="true"
        >
          <ClipboardCheck className="size-4" />
        </span>
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <Badge
            variant="outline"
            className="h-5 px-1.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground"
          >
            harness
          </Badge>
          <span
            suppressHydrationWarning
            className="text-[11px] text-muted-foreground tabular-nums"
          >
            {formatMessageTime(message.timestamp)}
          </span>
        </div>
        <p className="mt-0.5 text-sm text-muted-foreground lg:text-[15px]">
          {message.content}
        </p>
      </div>
    </div>
  );
}
