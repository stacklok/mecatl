"use client";

import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { SessionInventoryWalk } from "@/features/agent/hooks/use-agent-sessions";

/**
 * The one-line status of the sidebar's inventory walk (the TUI's paged
 * `/sessions` status line): progress with Cancel while the visible walk runs,
 * the error with Retry when a page failed, and an honest "the inventory is
 * larger" / "loading stopped" hint with Load again after a bounded or
 * cancelled walk. Background polls never show here — only the first load, a
 * reconnect, or an explicit retry is visible. Renders nothing when there is
 * nothing to say.
 */
export function SessionInventoryStatus({
  walk,
  error,
  onCancel,
  onRetry,
}: {
  walk: SessionInventoryWalk;
  error: string | null;
  onCancel: () => void;
  onRetry: () => void;
}) {
  if (walk.inFlight) {
    return (
      <div
        role="status"
        aria-live="polite"
        className="flex items-center gap-2 px-4 pb-2 text-xs text-muted-foreground"
      >
        <Loader2 className="size-3 shrink-0 animate-spin" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">
          {walk.pages > 0
            ? `Loading chats… ${walk.rows} so far (page ${walk.pages})`
            : "Loading chats…"}
        </span>
        <InlineAction
          label="Cancel"
          title="Stop loading chats"
          onClick={onCancel}
        />
      </div>
    );
  }

  if (error) {
    return (
      <div
        role="alert"
        className="flex items-start gap-2 px-4 pb-2 text-xs text-destructive"
      >
        <span className="min-w-0 flex-1 break-words">{error}</span>
        <InlineAction
          label="Retry"
          title="Retry loading chats"
          onClick={onRetry}
        />
      </div>
    );
  }

  if (walk.complete === false) {
    return (
      <div
        role="status"
        className="flex items-start gap-2 px-4 pb-2 text-xs text-muted-foreground"
      >
        <span className="min-w-0 flex-1 break-words">
          {walk.cancelled
            ? `Loading stopped — ${walk.rows} ${plural(walk.rows, "chat")} loaded so far.`
            : `Loaded the first ${walk.rows} ${plural(walk.rows, "chat")} — the inventory is larger.`}
        </span>
        <InlineAction
          label="Load again"
          title="Load chats again"
          onClick={onRetry}
        />
      </div>
    );
  }

  return null;
}

function InlineAction({
  label,
  title,
  onClick,
}: {
  label: string;
  title: string;
  onClick: () => void;
}) {
  return (
    <Button
      type="button"
      variant="link"
      size="sm"
      className="h-auto shrink-0 px-0 text-xs"
      aria-label={title}
      title={title}
      onClick={onClick}
    >
      {label}
    </Button>
  );
}

const plural = (count: number, noun: string) =>
  count === 1 ? noun : `${noun}s`;
