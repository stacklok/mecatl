// SPDX-License-Identifier: Apache-2.0

import { Bot, Copy, MessageSquareText, User } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * The prototype's message row pieces (`message-bubble.tsx` on `stack-08`),
 * shared by the main transcript and the side thread: one left-aligned turn
 * with an avatar for both roles, the author's name, the body, and a small
 * actions column. These are layout only; what a row shows stays with its
 * transcript.
 */

/** The row frame: avatar column, body, actions column. */
export const messageRowClass =
  "group/msg -mx-2 flex min-w-0 max-w-full gap-2 rounded-lg px-2 py-2 break-words hover:bg-muted/40 lg:-mx-3 lg:gap-3 lg:px-3";

/** The author line above a turn's body. */
export const messageAuthorClass = "text-sm font-bold text-foreground lg:text-[15px]";

/** The prose body of a turn. */
export const messageBodyClass =
  "mt-0.5 min-w-0 text-sm leading-[1.75] text-foreground/80 lg:text-[15px]";

export function MessageAvatar({
  avatarUrl,
  fallback,
  name,
}: {
  avatarUrl: string;
  fallback: "agent" | "user";
  name: string;
}) {
  const frame = "size-7 shrink-0 lg:size-9";
  if (avatarUrl) {
    return <img alt={name} className={cn(frame, "rounded-full object-cover")} src={avatarUrl} />;
  }
  return (
    <span
      aria-hidden="true"
      className={cn(
        frame,
        "flex items-center justify-center rounded-full",
        fallback === "agent" ? "bg-brand text-white" : "bg-muted text-muted-foreground",
      )}
    >
      {fallback === "agent" ? (
        <Bot aria-hidden="true" className="size-4 lg:size-5" />
      ) : (
        <User aria-hidden="true" className="size-4 lg:size-5" />
      )}
    </span>
  );
}

const actionClass =
  "flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-50";

/**
 * The row's actions: copy the turn, and open or start its side thread. With
 * a pointer that can hover they show on hover or keyboard focus; on touch
 * they stay visible, since there is no hover to reveal them.
 */
export function MessageActions({
  onCopy,
  onOpenThread,
  threadDisabled,
  threadOpen,
}: {
  onCopy?: () => void;
  onOpenThread?: () => void;
  threadDisabled: boolean;
  /** True when the turn already has a side thread. */
  threadOpen: boolean;
}) {
  if (!onCopy && !onOpenThread) return null;
  return (
    <div className="flex items-center gap-1 transition-opacity [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-hover/msg:opacity-100 [@media(hover:hover)]:group-focus-within/msg:opacity-100">
      {onCopy && (
        <button
          aria-label="Copy to clipboard"
          className={actionClass}
          onClick={onCopy}
          type="button"
        >
          <Copy aria-hidden="true" className="size-3.5" />
        </button>
      )}
      {onOpenThread && (
        <button
          aria-label={threadOpen ? "Open side thread" : "Reply in side thread"}
          className={actionClass}
          disabled={threadDisabled}
          onClick={onOpenThread}
          title={threadOpen ? "Open thread" : "Reply in thread"}
          type="button"
        >
          <MessageSquareText aria-hidden="true" className="size-3.5" />
        </button>
      )}
    </div>
  );
}

/**
 * One turn: the avatar, then the author line with the row's actions at its
 * end, then the body. The actions sit on the author line rather than in a
 * column of their own, so a phone's narrow row keeps its text width.
 */
export function MessageRow({
  actions,
  author,
  avatar,
  children,
}: {
  actions?: ReactNode;
  author: ReactNode;
  avatar: ReactNode;
  children: ReactNode;
}) {
  return (
    <>
      <div className="shrink-0 pt-0.5">{avatar}</div>
      <div className="min-w-0 flex-1">
        <div className="flex min-h-6 items-center justify-between gap-2">
          {author}
          {actions}
        </div>
        {children}
      </div>
    </>
  );
}
