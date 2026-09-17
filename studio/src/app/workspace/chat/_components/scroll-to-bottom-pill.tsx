"use client";

import { ArrowDown } from "lucide-react";
import { Button } from "@/components/ui/button";

/**
 * The floating "back to the latest" control shown while the transcript is
 * unpinned from the bottom. It doubles as the TUI's `↑ NN%` header cue: how
 * far through the conversation the view sits, so someone paging with the
 * keyboard knows where they are. Clicking it (or pressing ⇧PgDn) jumps to
 * the bottom, which resumes auto-follow.
 */
export function ScrollToBottomPill({
  percent,
  onClick,
}: {
  /** Scroll position, 0 (top) to 100 (bottom); see `scrollPositionPercent`. */
  percent: number;
  onClick: () => void;
}) {
  return (
    <Button
      size="sm"
      onClick={onClick}
      aria-label={`Scroll to bottom — ${percent}% through the conversation`}
      className="pointer-events-auto gap-1.5 rounded-full border border-border bg-background text-muted-foreground shadow-md hover:bg-muted"
    >
      <span className="tabular-nums">↑ {percent}%</span>
      <ArrowDown className="size-4" />
    </Button>
  );
}
