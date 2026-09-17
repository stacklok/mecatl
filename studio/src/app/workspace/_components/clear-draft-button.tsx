"use client";

import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { keycaps } from "@/lib/shortcuts/registry";
import { cn } from "@/lib/utils";
import { CLEAR_DRAFT_COMBO } from "./composer-keys";

/** The accessible name of the composer's × button. */
export const CLEAR_DRAFT_LABEL = "Clear draft";

/** The hover hint: the label plus the chord that does the same thing. */
export const CLEAR_DRAFT_TITLE = `${CLEAR_DRAFT_LABEL} (${keycaps(CLEAR_DRAFT_COMBO).join("")})`;

/**
 * The composer's one-shot draft clear as a button (the TUI's ctrl+u
 * `ClearPrompt`, for the mouse and for touch). Renders only while there is
 * something to clear — text, a `[Pasted text #N]` chip or a staged file —
 * so an empty composer never shows a dead control. Idle or streaming alike:
 * it empties the field and never touches the run (Esc's cancel arm is the
 * only key that does). The same `onClear` backs ⌘⇧U and the double-Esc guard.
 */
export function ClearDraftButton({
  hasDraft,
  disabled = false,
  onClear,
  className,
}: {
  /** Text, paste chips or attachments are staged. */
  hasDraft: boolean;
  /** The composer is read-only (a disabled surface). */
  disabled?: boolean;
  /** Empties the draft (and typically refocuses the field). */
  onClear: () => void;
  className?: string;
}) {
  if (!hasDraft) return null;
  return (
    <Button
      type="button"
      size="icon"
      variant="ghost"
      className={cn(
        "size-8 rounded-full border-0 shadow-none bg-transparent text-muted-foreground hover:bg-muted/60 hover:text-foreground",
        className,
      )}
      onClick={onClear}
      disabled={disabled}
      aria-label={CLEAR_DRAFT_LABEL}
      title={CLEAR_DRAFT_TITLE}
      data-testid="composer-clear-draft"
    >
      <X className="size-4" aria-hidden="true" />
    </Button>
  );
}
