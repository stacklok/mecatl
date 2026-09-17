"use client";

import { AlertCircle, CirclePlus, Pencil, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";

/**
 * The inline strip above the composer after a failed turn. Retry is offered
 * for every failure the daemon might re-drive (the hook asks it first); a
 * PERMANENT failure withholds it — the identical request is rejected, so a
 * retry can only fail the same way — and offers New chat instead. Edit (the
 * TUI's esc after a run-entry failure) puts the refused prompt back in the
 * composer to change before sending again; it is offered whenever the
 * caller holds one, permanent or not — changing the request is the one
 * recovery a permanent failure leaves open.
 */
export function TurnErrorStrip({
  error,
  permanent = false,
  onRetry,
  onNewChat,
  onEdit,
}: {
  error: string;
  permanent?: boolean;
  onRetry?: () => void;
  onNewChat?: () => void;
  /** Put the failed message back in the composer for editing. */
  onEdit?: () => void;
}) {
  const buttonClass =
    "h-7 shrink-0 border-destructive/30 text-destructive hover:bg-destructive/10 hover:text-destructive";
  return (
    <div
      role="alert"
      className="flex items-center gap-2 rounded-lg border border-destructive/40 bg-background bg-gradient-to-b from-destructive/5 to-destructive/5 px-3 py-2"
    >
      <AlertCircle className="size-4 shrink-0 text-destructive" />
      <p className="min-w-0 flex-1 text-sm text-destructive break-words">
        {error}
      </p>
      {onEdit && (
        <Button
          size="sm"
          variant="outline"
          className={buttonClass}
          onClick={onEdit}
          aria-label="Edit the failed message"
          title="Put the message back in the composer to change it before sending again."
        >
          <Pencil className="size-3.5" />
          Edit
        </Button>
      )}
      {permanent
        ? onNewChat && (
            <Button
              size="sm"
              variant="outline"
              className={buttonClass}
              onClick={onNewChat}
              title="This failure is permanent: retrying the identical request cannot succeed."
            >
              <CirclePlus className="size-3.5" />
              New chat
            </Button>
          )
        : onRetry && (
            <Button
              size="sm"
              variant="outline"
              className={buttonClass}
              onClick={onRetry}
            >
              <RotateCcw className="size-3.5" />
              Retry
            </Button>
          )}
    </div>
  );
}
