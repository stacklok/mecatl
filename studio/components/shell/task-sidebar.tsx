"use client";

import { Ellipsis, MessageSquare, Plus } from "lucide-react";

import { cn } from "@/lib/utils";

export type TaskSummary = {
  id: string;
  title: string;
  updatedAt: number;
  running?: boolean;
};

/**
 * The left rail. Deliberately NOT a shadcn `ui/sidebar` — the console's own rail
 * is a hand-rolled `<nav>` for the same reason: the design is full-bleed rows
 * with a 3px left accent, which fights the primitive's rounded, inset menu
 * buttons.
 *
 * Fixed `w-64`, no collapse. Below `md` it is hidden and the navbar's drawer
 * renders this same component with `h-full w-full border-r-0`.
 */
export function TaskSidebar({
  tasks,
  activeId,
  connection,
  relativeTime,
  onSelect,
  onNewTask,
  className,
  onNavigate,
}: {
  tasks: readonly TaskSummary[];
  activeId: string;
  connection: "checking" | "online" | "offline";
  relativeTime: (timestamp: number) => string;
  onSelect: (id: string) => void;
  onNewTask: () => void;
  className?: string;
  onNavigate?: () => void;
}) {
  const ordered = [...tasks].sort((a, b) => b.updatedAt - a.updatedAt);

  return (
    <nav
      aria-label="Tasks"
      className={cn(
        "flex w-64 shrink-0 flex-col border-r border-border bg-sidebar",
        className,
      )}
    >
      <div className="flex h-16 shrink-0 items-center gap-2.5 border-b border-border px-5">
        <span className="grid size-7 shrink-0 place-items-center rounded-md bg-brand font-serif text-[15px] font-bold text-brand-foreground shadow-[inset_0_0_0_1px_rgba(255,255,255,.16)]">
          M
        </span>
        <span className="truncate text-[0.95rem] font-semibold text-foreground">
          Mecatl{" "}
          <span className="font-normal tracking-wide text-muted-foreground">
            STUDIO
          </span>
        </span>
      </div>

      <div className="flex flex-1 flex-col overflow-y-auto py-4">
        <button
          type="button"
          onClick={() => {
            onNewTask();
            onNavigate?.();
          }}
          className="flex w-full items-center gap-2 border-l-[3px] border-transparent px-5 py-2 text-left text-[0.85rem] font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <Plus className="size-3.5 shrink-0" />
          New task
          <kbd className="pointer-events-none ml-auto hidden select-none items-center gap-0.5 rounded border border-border bg-muted px-1.5 font-mono text-[0.65rem] font-medium text-muted-foreground sm:inline-flex">
            <span className="text-xs">⌘</span>K
          </kbd>
        </button>

        <div className="px-5 pt-7 pb-1 text-[0.7rem] font-medium tracking-wider text-muted-foreground uppercase">
          Tasks
        </div>

        {ordered.map((task) => {
          const isSelected = task.id === activeId;
          return (
            <div
              key={task.id}
              className={cn(
                "group flex items-center border-l-[3px] border-transparent py-2 pr-3 pl-5 transition-colors",
                isSelected ? "border-brand-ink bg-accent" : "hover:bg-accent",
              )}
            >
              <button
                type="button"
                onClick={() => {
                  onSelect(task.id);
                  onNavigate?.();
                }}
                className="flex min-w-0 flex-1 items-center gap-2 text-left"
              >
                <MessageSquare
                  className={cn(
                    "size-3.5 shrink-0",
                    isSelected ? "text-brand-ink" : "text-muted-foreground",
                  )}
                />
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
              {/* One grid cell holds both the timestamp and the overflow button
                  so the hover swap cannot reflow the row. */}
              <div className="ml-2 grid w-8 shrink-0 items-center justify-items-center [grid-template-areas:'slot']">
                {task.running ? (
                  <span className="size-2 rounded-full bg-brand [grid-area:slot]" />
                ) : (
                  <span className="text-xs text-muted-foreground/50 tabular-nums [grid-area:slot] lg:group-hover:invisible">
                    {relativeTime(task.updatedAt)}
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

      <div className="flex items-center gap-1.5 border-t border-border px-5 py-3 text-xs text-muted-foreground">
        <span
          className={cn(
            "size-2 shrink-0 rounded-full",
            connection === "online"
              ? "bg-success"
              : connection === "checking"
                ? "bg-warning"
                : "bg-destructive",
          )}
        />
        {connection === "online"
          ? "Mecatl connected"
          : connection === "checking"
            ? "Checking connection"
            : "Mecatl offline"}
      </div>
    </nav>
  );
}
