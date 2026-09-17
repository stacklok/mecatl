"use client";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/**
 * The one-click confirmation behind a `?prompt=…&send=1` deep link (see
 * `lib/chat-seed.ts` for why a URL never sends on its own): the exact text
 * the link carried, where it would go, and two ways out — Send, or Edit
 * first, which drops the text into the composer instead. Closing the dialog
 * any other way (Esc, the backdrop) is Edit first: the text is never lost
 * and never sent behind the user's back. Send is disabled, with the reason,
 * while the daemon is not reachable or the chat has a run in flight.
 */

export const SEED_PROMPT_TITLE = "Send this prompt?";

export const SEED_PROMPT_NEW_CHAT_NOTE =
  "This link arrived with a prompt and asked for it to be sent. Nothing is sent until you choose Send; it starts a new chat.";

export const SEED_PROMPT_CURRENT_CHAT_NOTE =
  "This link arrived with a prompt and asked for it to be sent. Nothing is sent until you choose Send; it goes to this chat.";

export const SEED_PROMPT_SEND_LABEL = "Send";

export const SEED_PROMPT_EDIT_LABEL = "Edit first";

export interface SeedPromptDialogProps {
  /** The exact prompt the link carried. */
  prompt: string;
  /** Whether Send starts a new chat or posts into the open one. */
  target: "new" | "current";
  /** Why Send must wait (shown under the prompt); null when it can go now. */
  waitReason: string | null;
  onSend: () => void;
  /** Puts the prompt in the composer instead of sending it. */
  onEdit: () => void;
}

export function SeedPromptDialog({
  prompt,
  target,
  waitReason,
  onSend,
  onEdit,
}: SeedPromptDialogProps) {
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onEdit();
      }}
    >
      <DialogContent showCloseButton={false} className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{SEED_PROMPT_TITLE}</DialogTitle>
          <DialogDescription>
            {target === "new"
              ? SEED_PROMPT_NEW_CHAT_NOTE
              : SEED_PROMPT_CURRENT_CHAT_NOTE}
          </DialogDescription>
        </DialogHeader>
        {/* A labelled, focusable region (the axe rule for scrollable
            content) so a keyboard user can scroll a long prompt; Radix lands
            the initial focus here, where Enter does nothing. */}
        <section
          // biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable region must be keyboard reachable
          tabIndex={0}
          aria-label="Prompt to send"
          data-testid="seed-prompt-text"
          className="max-h-64 overflow-auto rounded-md border border-border bg-muted p-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <pre className="m-0 whitespace-pre-wrap break-words font-sans text-sm">
            {prompt}
          </pre>
        </section>
        {waitReason && (
          <p role="status" className="text-xs text-muted-foreground">
            {waitReason}
          </p>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onEdit}>
            {SEED_PROMPT_EDIT_LABEL}
          </Button>
          <Button type="button" onClick={onSend} disabled={waitReason !== null}>
            {SEED_PROMPT_SEND_LABEL}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
