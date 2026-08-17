"use client";

import { Ellipsis, SquarePen } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * Compact ages, matching the design: `now`, `5m`, `2h`, `3d`. The slot beside a
 * task title is one glyph-pair wide because it shares that cell with the hover
 * affordance, so a spelled-out "Just now" wraps to two lines there.
 */
function compactAge(timestamp: number): string {
  if (timestamp === 0) return "now";
  const seconds = Math.floor((Date.now() - timestamp) / 1000);
  if (seconds < 60) return "now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  return `${Math.floor(days / 7)}w`;
}

export type TaskSummary = {
  id: string;
  title: string;
  updatedAt: number;
  running?: boolean;
};

/**
 * The chat-history panel — the inner of the two navigation levels.
 *
 * Deliberately shows ONE section. The design this follows also carries Projects
 * and Agents groups, but Studio has no project container and no agent picker, so
 * those headings would be furniture with nothing behind them.
 */
export function ChatPanel({
  tasks,
  activeId,
  onSelectTask,
  onNewTask,
  className,
  onAfterSelect,
}: {
  tasks: readonly TaskSummary[];
  activeId: string;
  onSelectTask: (id: string) => void;
  onNewTask: () => void;
  className?: string;
  onAfterSelect?: () => void;
}) {
  const ordered = [...tasks].sort((a, b) => b.updatedAt - a.updatedAt);

  return (
    <div
      className={cn(
        "flex w-72 shrink-0 flex-col border-r border-border bg-background",
        className,
      )}
    >
      <div className="flex h-16 shrink-0 items-center gap-2 border-b border-border px-4">
        <h2 className="flex-1 truncate text-base font-semibold text-foreground">
          Chats
        </h2>
        <button
          type="button"
          onClick={() => {
            onNewTask();
            onAfterSelect?.();
          }}
          aria-label="New task"
          title="New task"
          className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <SquarePen className="size-[18px]" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto py-3">
        <div className="px-4 pb-1 text-[0.7rem] font-medium tracking-wider text-muted-foreground uppercase">
          Tasks
        </div>

        {ordered.length === 0 && (
          <p className="px-4 py-2 text-xs text-muted-foreground/50">
            No tasks yet
          </p>
        )}

        {ordered.map((task) => {
          const isSelected = task.id === activeId;
          return (
            <div
              key={task.id}
              className={cn(
                "group flex items-center border-l-[3px] border-transparent py-2 pr-3 pl-4 transition-colors",
                isSelected ? "border-brand-ink bg-accent" : "hover:bg-accent",
              )}
            >
              <button
                type="button"
                onClick={() => {
                  onSelectTask(task.id);
                  onAfterSelect?.();
                }}
                className="min-w-0 flex-1 text-left"
              >
                <span
                  className={cn(
                    "block truncate text-[0.85rem] font-medium",
                    isSelected
                      ? "text-brand-ink"
                      : "text-muted-foreground group-hover:text-foreground",
                  )}
                >
                  {task.title || "Untitled"}
                </span>
              </button>
              {/* A single grid cell stacks the status/time and the overflow
                  affordance, so the hover swap cannot reflow the row. */}
              <div className="ml-2 grid w-8 shrink-0 items-center justify-items-center [grid-template-areas:'slot']">
                {task.running ? (
                  <span
                    className="size-2 rounded-full bg-brand [grid-area:slot]"
                    title="Running"
                  />
                ) : (
                  <span className="text-xs tabular-nums text-muted-foreground/50 [grid-area:slot] lg:group-hover:invisible">
                    {compactAge(task.updatedAt)}
                  </span>
                )}
                <span
                  aria-hidden="true"
                  className="flex w-7 items-center justify-center rounded text-muted-foreground opacity-0 [grid-area:slot] lg:group-hover:opacity-100"
                >
                  <Ellipsis className="size-4" />
                </span>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
