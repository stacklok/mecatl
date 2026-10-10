// SPDX-License-Identifier: Apache-2.0

import { X } from "lucide-react";
import { Button } from "../../components/ui/button";
import { cn } from "../../lib/utils";

/** The accessible name of the composer's × button. */
export const CLEAR_DRAFT_LABEL = "Clear draft";

/**
 * The composer's one-shot draft clear, from the prototype (`stack-08`). It
 * renders only while there is something to clear, text or a staged
 * attachment, so an empty composer never shows a dead control. It empties the
 * field and never touches a run. The same clear backs a double Escape.
 */
export function ClearDraftButton({
  className,
  disabled = false,
  hasDraft,
  onClear,
}: {
  className?: string;
  /** The composer is read-only. */
  disabled?: boolean;
  /** Text or a staged attachment is present. */
  hasDraft: boolean;
  /** Empties the draft and refocuses the field. */
  onClear: () => void;
}) {
  if (!hasDraft) return null;
  return (
    <Button
      aria-label={CLEAR_DRAFT_LABEL}
      className={cn(
        "size-8 rounded-full border-0 bg-transparent text-muted-foreground shadow-none hover:bg-muted/60 hover:text-foreground",
        className,
      )}
      data-testid="composer-clear-draft"
      disabled={disabled}
      onClick={onClear}
      size="icon"
      title={`${CLEAR_DRAFT_LABEL} (Esc twice)`}
      type="button"
      variant="ghost"
    >
      <X aria-hidden="true" className="size-4" />
    </Button>
  );
}
