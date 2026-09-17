"use client";

import { ArrowRight } from "lucide-react";
import { formatRelativeTime } from "@/lib/formatters";

/** The draft view's summary of the most recent eligible chat. */
export interface LatestChatSummary {
  id: string;
  title: string;
  updatedAt: number;
}

/**
 * The always-available half of `--resume-latest` on the draft: one pill
 * that continues the most recent eligible chat, so landing on the newest
 * conversation needs no preference. Rendered only when the caller has a
 * pick (the inventory is daemon-backed; nothing eligible = no chip). The
 * whole label is the button's name, so "Continue …" is reachable by role.
 */
export function ContinueLatestChip({
  latest,
  onContinue,
}: {
  latest: LatestChatSummary | null | undefined;
  onContinue: (id: string) => void;
}) {
  if (!latest) return null;
  const title = latest.title.trim() || "Untitled chat";
  const when = formatRelativeTime(latest.updatedAt);
  return (
    <div className="flex justify-center">
      <button
        type="button"
        onClick={() => onContinue(latest.id)}
        title="Continue the most recent chat"
        className="inline-flex max-w-full items-center gap-2 rounded-full border border-border bg-background px-3.5 py-1.5 text-sm text-muted-foreground transition-colors hover:border-foreground/20 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ArrowRight className="size-4 shrink-0" aria-hidden="true" />
        <span className="min-w-0 truncate">
          Continue <span className="text-foreground">“{title}”</span>
          {when && <span className="text-muted-foreground"> · {when}</span>}
        </span>
      </button>
    </div>
  );
}
