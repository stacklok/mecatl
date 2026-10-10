// SPDX-License-Identifier: Apache-2.0

import { type RefObject, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import type { ApprovalRequest } from "./approval-panel";
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

/** Dash width by distance from the hovered dash: the prototype's dock-style falloff. */
const MAGNIFY_WIDTHS_PX = [28, 24, 20, 16];
const BASE_WIDTH_PX = 12;
const PREVIEW_MAX_CHARS = 96;

function dashWidth(distance: number | undefined): number {
  if (distance === undefined) return BASE_WIDTH_PX;
  return MAGNIFY_WIDTHS_PX[distance] ?? BASE_WIDTH_PX;
}

function previewText(content: string): string {
  const collapsed = content.replace(/\s+/gu, " ").trim();
  if (!collapsed) return "(no text)";
  return collapsed.length > PREVIEW_MAX_CHARS
    ? `${collapsed.slice(0, PREVIEW_MAX_CHARS).trimEnd()}…`
    : collapsed;
}

/**
 * One short dash per visible transcript row, in the prototype's look: at
 * rest every dash is the same dim width, so the rail stays out of the way;
 * hovering or focusing one widens it and its neighbours and previews that
 * message beside the rail. The row a reader jumped to stays marked. Each
 * dash is a full-height button so it stays a real tap target, and a long
 * conversation scrolls the rail instead of shrinking it.
 */
export function MessageMinimap({
  approvals,
  agentName = "Mecatl",
  delegationsByMessageId,
  messages,
  onNavigate,
  scrollportRef,
  sessionId = "",
  showToolCalls = true,
  streamingMessageId,
  userName = "You",
}: {
  approvals: ApprovalRequest[];
  agentName?: string;
  delegationsByMessageId?: Record<string, DelegationActivity[]>;
  messages: ChatMessage[];
  onNavigate: (messageId: string) => void;
  scrollportRef: RefObject<HTMLElement | null>;
  sessionId?: string;
  showToolCalls?: boolean;
  streamingMessageId?: string;
  userName?: string;
}) {
  const [selected, setSelected] = useState<{ messageId: string; sessionId: string }>();
  const [hovered, setHovered] = useState<{ index: number; top: number }>();
  const rail = useRef<HTMLDivElement>(null);
  const visible = messages.filter(
    (message) =>
      (message.role === "user" || message.role === "assistant") &&
      isVisibleTranscriptMessage(
        message,
        showToolCalls,
        message.id === streamingMessageId,
        delegationsByMessageId?.[message.id],
        approvals,
      ),
  );
  const currentId =
    selected?.sessionId === sessionId &&
    visible.some((message) => message.id === selected.messageId)
      ? selected.messageId
      : undefined;
  if (visible.length === 0) return null;

  function hover(index: number, target: HTMLElement) {
    const frame = rail.current?.getBoundingClientRect();
    const box = target.getBoundingClientRect();
    setHovered({ index, top: frame ? box.top + box.height / 2 - frame.top : 0 });
  }

  const preview = hovered ? visible[hovered.index] : undefined;

  return (
    <div className="absolute inset-y-2 right-1 z-10 flex w-7" ref={rail}>
      {preview && hovered && (
        <div
          className="pointer-events-none absolute right-full mr-2 w-64 max-w-[calc(100vw-4rem)] -translate-y-1/2 rounded-lg border border-border bg-popover p-2.5 text-xs shadow-lg"
          role="tooltip"
          style={{ top: hovered.top }}
        >
          <p className="truncate font-medium text-popover-foreground">
            {previewText(preview.content)}
          </p>
          <p className="mt-0.5 text-muted-foreground">
            {preview.delivery
              ? `Scheduled task ${preview.delivery.scheduleName}`
              : preview.role === "user"
                ? userName
                : agentName}
          </p>
        </div>
      )}
      <nav
        aria-label="Message minimap"
        className="flex h-full w-7 max-w-[calc(100vw-1rem)] flex-col overflow-x-hidden overflow-y-auto overscroll-contain [scrollbar-width:none]"
        onScroll={() => setHovered(undefined)}
      >
        <div className="my-auto flex flex-col">
          {visible.map((message, index) => {
            const snippet = message.content.replace(/\s+/gu, " ").trim().slice(0, 40);
            const label = `Jump to message ${index + 1}: ${message.role}${snippet ? ` — ${snippet}` : ""}`;
            const current = message.id === currentId;
            const distance = hovered === undefined ? undefined : Math.abs(index - hovered.index);
            return (
              <button
                aria-current={current ? "location" : undefined}
                aria-label={label}
                className="group/dash flex h-3 w-7 shrink-0 items-center justify-end rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
                data-current={current}
                key={message.id}
                onBlur={() => setHovered(undefined)}
                onClick={() => {
                  const scrollport = scrollportRef.current;
                  if (!scrollport || !scrollToTranscriptRow(scrollport, message.id)) return;
                  setSelected({ messageId: message.id, sessionId });
                  onNavigate(message.id);
                }}
                onFocus={(event) => hover(index, event.currentTarget)}
                onMouseEnter={(event) => hover(index, event.currentTarget)}
                onMouseLeave={() => setHovered(undefined)}
                type="button"
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    "h-[3px] rounded-full transition-all",
                    distance === 0
                      ? "bg-foreground"
                      : current
                        ? "bg-brand"
                        : "bg-muted-foreground/30",
                  )}
                  style={{ width: dashWidth(distance) }}
                />
              </button>
            );
          })}
        </div>
      </nav>
    </div>
  );
}
