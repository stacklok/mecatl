// SPDX-License-Identifier: Apache-2.0

import { Copy, MessageCircle, NotebookPen } from "lucide-react";
import { Button } from "@/components/ui/button";

const actionClass = "h-auto gap-1.5 px-2.5 py-1.5 text-sm font-normal";

/**
 * The floating toolbar over a transcript selection, in the prototype's look:
 * Add to chat (the selection joins the composer's draft), Copy, and Add to
 * canvas. The caller places it at the selection, in viewport coordinates,
 * and owns what each action does.
 */
export function TextSelectionToolbar({
  left,
  onAddToCanvas,
  onAddToChat,
  onCopy,
  top,
}: {
  left: number;
  onAddToCanvas: () => void;
  onAddToChat: () => void;
  onCopy: () => void;
  top: number;
}) {
  return (
    <div
      className="fixed z-50 flex -translate-x-1/2 -translate-y-full items-center gap-0.5 rounded-lg border bg-popover px-1 py-0.5 text-popover-foreground shadow-lg"
      style={{ left, top }}
    >
      <Button className={actionClass} onClick={onAddToChat} size="sm" variant="ghost">
        <MessageCircle aria-hidden="true" className="size-3.5" />
        Add to chat
      </Button>
      <Button className={actionClass} onClick={onCopy} size="sm" variant="ghost">
        <Copy aria-hidden="true" className="size-3.5" />
        Copy
      </Button>
      <Button className={actionClass} onClick={onAddToCanvas} size="sm" variant="ghost">
        <NotebookPen aria-hidden="true" className="size-3.5" />
        Add to canvas
      </Button>
    </div>
  );
}
