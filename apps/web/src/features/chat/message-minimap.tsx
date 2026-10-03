// SPDX-License-Identifier: Apache-2.0

import { type RefObject, useState } from "react";
import type { ChatMessage } from "./chat-state";
import { isVisibleTranscriptMessage } from "./chat-transcript";
import type { DelegationActivity } from "./delegation-card";

/** Scroll against the transcript's own coordinates; the page never participates. */
export function scrollToTranscriptRow(scrollport: HTMLElement, messageId: string): boolean {
  const row = Array.from(scrollport.querySelectorAll("article[id]")).find(
    (candidate) => candidate.id === `chat-message-${messageId}`,
  );
  if (!row) return false;
  const offset =
    row.getBoundingClientRect().top - scrollport.getBoundingClientRect().top + scrollport.scrollTop;
  const maxTop = Math.max(0, scrollport.scrollHeight - scrollport.clientHeight);
  scrollport.scrollTo({ behavior: "smooth", top: Math.min(maxTop, Math.max(0, offset)) });
  return true;
}

export function MessageMinimap({
  delegationsByMessageId,
  messages,
  onNavigate,
  scrollportRef,
  sessionId = "",
  showToolCalls = true,
  streamingMessageId,
}: {
  delegationsByMessageId?: Record<string, DelegationActivity[]>;
  messages: ChatMessage[];
  onNavigate: (messageId: string) => void;
  scrollportRef: RefObject<HTMLElement | null>;
  sessionId?: string;
  showToolCalls?: boolean;
  streamingMessageId?: string;
}) {
  const [selected, setSelected] = useState<{ messageId: string; sessionId: string }>();
  const visible = messages.filter(
    (message) =>
      (message.role === "user" || message.role === "assistant") &&
      isVisibleTranscriptMessage(
        message,
        showToolCalls,
        message.id === streamingMessageId,
        delegationsByMessageId?.[message.id],
      ),
  );
  const currentId =
    selected?.sessionId === sessionId &&
    visible.some((message) => message.id === selected.messageId)
      ? selected.messageId
      : undefined;
  if (visible.length === 0) return null;

  return (
    <nav
      aria-label="Message minimap"
      className="absolute inset-y-2 right-1 z-10 flex w-9 max-w-[calc(100%-0.5rem)] flex-col items-center gap-1 overflow-x-hidden overflow-y-auto overscroll-contain rounded-lg border bg-background/95 py-2 shadow-sm"
    >
      {visible.map((message, index) => {
        const snippet = message.content.replace(/\s+/gu, " ").trim().slice(0, 40);
        const label = `Jump to message ${index + 1}: ${message.role}${snippet ? ` — ${snippet}` : ""}`;
        return (
          <button
            aria-current={message.id === currentId ? "location" : undefined}
            aria-label={label}
            className="flex size-7 shrink-0 items-center justify-center rounded border border-border text-[10px] tabular-nums text-muted-foreground hover:bg-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand data-[current=true]:border-brand data-[current=true]:font-bold data-[current=true]:text-foreground"
            data-current={message.id === currentId}
            key={message.id}
            onClick={() => {
              const scrollport = scrollportRef.current;
              if (!scrollport || !scrollToTranscriptRow(scrollport, message.id)) return;
              setSelected({ messageId: message.id, sessionId });
              onNavigate(message.id);
            }}
            title={label}
            type="button"
          >
            {index + 1}
          </button>
        );
      })}
    </nav>
  );
}
