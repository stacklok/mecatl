"use client";

import { Ellipsis, Hourglass, ListEnd, Pause } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type {
  PendingSteer,
  QueuedMessage,
  QueuePause,
} from "@/features/agent/hooks/use-agent-chat";

export interface QueuedMessageStripProps {
  /** Messages held for the next run, in send order. */
  queued: QueuedMessage[];
  /** Steers the daemon accepted but has not yet drained into the run. */
  pendingSteers?: PendingSteer[];
  /** Non-null while the queue is held after a non-clean stop (cancel, a
   *  failed turn, a lost connection): the header turns loud and offers
   *  Send now / Edit all / Clear all. */
  paused?: QueuePause | null;
  /** A run is live: queued rows may be steered into it. */
  isStreaming: boolean;
  /** Steers a queued row into the live run. Absent when steering is off —
   *  the daemon does not support it, or the user chose "Queue only" — and
   *  the row menu then offers no Steer. */
  onSteer?: (id: string) => void;
  onEdit: (id: string) => void;
  onDelete: (id: string) => void;
  /** Sends the whole held queue as one prompt (the paused header's Send now). */
  onResume?: () => void;
  /** Pulls the whole queue back into the composer for editing. */
  onEditAll?: () => void;
  /** Drops every held message. */
  onClearAll?: () => void;
  /** Retracts the pending steer bundle (the daemon retracts bundles, never a
   *  single steer) and hands the texts back to the composer. */
  onRetractSteers?: () => void;
}

/** What a held row reads as: its text, or the attachment count for a
 *  files-only row (a steer or queued message can carry images alone). */
function rowLabel(row: { text: string; files?: File[] }): string {
  if (row.text) return row.text;
  const count = row.files?.length ?? 0;
  return count === 1 ? "1 attachment" : `${count} attachments`;
}

/**
 * Messages held above the composer, as ONE divided group (never a stack of
 * panels): pending steers first — a pulsing dot, "steering…", the text, and
 * a single Retract for the bundle — then the queue under a header that reads
 * muted while a run is live ("N queued · ↑ edit") and loud when the queue is
 * paused ("N queued · paused: <reason>", with Send now / Edit all / Clear
 * all). Queued rows offer Steer (only while a run is live AND `onSteer` is
 * wired — steering off withdraws it), Edit (back into the composer,
 * attachments included), and Delete. Renders nothing when nothing is held.
 */
export function QueuedMessageStrip({
  queued,
  pendingSteers,
  paused,
  isStreaming,
  onSteer,
  onEdit,
  onDelete,
  onResume,
  onEditAll,
  onClearAll,
  onRetractSteers,
}: QueuedMessageStripProps) {
  const steers = pendingSteers ?? [];
  if (queued.length === 0 && steers.length === 0) return null;
  const count = queued.length;
  return (
    <div className="max-[499px]:mx-3">
      <div className="divide-y overflow-hidden rounded-xl border bg-background">
        {steers.map((steer, index) => (
          <div
            key={steer.id}
            className="flex items-center gap-2 py-1 pr-1 pl-3"
            data-testid="pending-steer-row"
          >
            <span
              aria-hidden="true"
              className="size-2 shrink-0 animate-pulse rounded-full bg-brand"
            />
            <span className="shrink-0 text-xs text-muted-foreground">
              steering…
            </span>
            <span
              className="min-w-0 flex-1 truncate text-sm"
              title={rowLabel(steer)}
            >
              {rowLabel(steer)}
            </span>
            {index === 0 && onRetractSteers && (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 shrink-0 text-muted-foreground"
                onClick={onRetractSteers}
                aria-label="Retract pending steers"
              >
                Retract
              </Button>
            )}
          </div>
        ))}
        {count > 0 &&
          (paused ? (
            <div
              role="status"
              className="flex flex-wrap items-center gap-x-2 gap-y-1 bg-warning/5 py-1 pr-1 pl-3 text-sm text-warning"
            >
              <Pause aria-hidden="true" className="size-3.5 shrink-0" />
              <span className="min-w-0 flex-1">
                {count} queued · paused: {paused.reason}
              </span>
              <div className="flex shrink-0 items-center gap-1">
                {onResume && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-7 border-warning/30 text-warning hover:bg-warning/10 hover:text-warning"
                    onClick={onResume}
                  >
                    Send now
                  </Button>
                )}
                {onEditAll && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 text-warning hover:text-warning"
                    onClick={onEditAll}
                  >
                    Edit all
                  </Button>
                )}
                {onClearAll && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 text-warning hover:text-warning"
                    onClick={onClearAll}
                  >
                    Clear all
                  </Button>
                )}
              </div>
            </div>
          ) : (
            <div
              role="status"
              className="flex items-center gap-2 px-3 py-1 text-xs text-muted-foreground"
            >
              <Hourglass aria-hidden="true" className="size-3.5 shrink-0" />
              <span>{count} queued · ↑ edit</span>
            </div>
          ))}
        {queued.map((message) => (
          <div
            key={message.id}
            className="flex items-center gap-2 py-1 pr-1 pl-3"
          >
            <ListEnd className="size-4 shrink-0 text-muted-foreground" />
            <span
              className="min-w-0 flex-1 truncate text-sm"
              title={rowLabel(message)}
            >
              {rowLabel(message)}
            </span>
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-7 shrink-0 text-muted-foreground"
                  aria-label="Actions for queued message"
                >
                  <Ellipsis className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {isStreaming && onSteer && (
                  <DropdownMenuItem onClick={() => onSteer(message.id)}>
                    Steer
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem onClick={() => onEdit(message.id)}>
                  Edit
                </DropdownMenuItem>
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => onDelete(message.id)}
                >
                  Delete
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        ))}
      </div>
    </div>
  );
}
