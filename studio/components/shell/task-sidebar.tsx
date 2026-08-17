"use client";

import { Ellipsis, Plus } from "lucide-react";

import { FOOTER_ITEM, NAV_ITEMS, type NavItem, type ViewKey } from "@/components/shell/nav-items";
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
 * with a 3px left accent, which fights the primitive's rounded, inset buttons.
 *
 * Fixed `w-64`, no collapse. Below `md` it is hidden and the navbar's drawer
 * renders this same component with `h-full w-full border-r-0`.
 *
 * The task list is nested UNDER the Chat destination rather than sitting in its
 * own section: a task IS a chat, so indenting them shows that relationship and
 * keeps one list rather than two competing ones.
 */
export function TaskSidebar({
  view,
  onNavigate,
  tasks,
  activeId,
  connection,
  relativeTime,
  onSelectTask,
  onNewTask,
  className,
  onAfterNavigate,
}: {
  view: ViewKey;
  onNavigate: (view: ViewKey) => void;
  tasks: readonly TaskSummary[];
  activeId: string;
  connection: "checking" | "online" | "offline";
  relativeTime: (timestamp: number) => string;
  onSelectTask: (id: string) => void;
  onNewTask: () => void;
  className?: string;
  onAfterNavigate?: () => void;
}) {
  const ordered = [...tasks].sort((a, b) => b.updatedAt - a.updatedAt);

  const renderNavLink = (item: NavItem) => {
    const isActive = item.key === view;
    const Icon = item.icon;
    return (
      <button
        key={item.key}
        type="button"
        onClick={() => {
          onNavigate(item.key);
          onAfterNavigate?.();
        }}
        aria-current={isActive ? "page" : undefined}
        className={cn(
          "flex items-center gap-[0.65rem] border-l-[3px] border-transparent px-5 py-2 text-left text-[0.85rem] font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
          isActive && "border-brand-ink bg-accent text-brand-ink hover:text-brand-ink",
        )}
      >
        <Icon className="size-[17px] shrink-0" />
        {item.label}
      </button>
    );
  };

  return (
    <nav
      aria-label="Main navigation"
      className={cn(
        "flex w-64 shrink-0 flex-col border-r border-border bg-sidebar",
        className,
      )}
    >
      <div className="flex h-16 shrink-0 items-center gap-2.5 border-b border-border px-5">
        <span className="grid size-7 shrink-0 place-items-center rounded-md bg-brand font-serif text-[0.95rem] font-bold text-brand-foreground shadow-[inset_0_0_0_1px_rgba(255,255,255,.16)]">
          M
        </span>
        <span className="truncate text-[0.95rem] font-semibold text-foreground">
          Mecatl{" "}
          <span className="font-normal tracking-wide text-muted-foreground">
            STUDIO
          </span>
        </span>
      </div>

      <div className="flex flex-1 flex-col gap-1 overflow-y-auto py-4">
        {renderNavLink(NAV_ITEMS[0])}

        <button
          type="button"
          onClick={() => {
            onNewTask();
            onAfterNavigate?.();
          }}
          className="flex w-full items-center gap-2 border-l-[3px] border-transparent py-2 pr-3 pl-[2.55rem] text-left text-[0.8rem] font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <Plus className="size-3.5 shrink-0" />
          New task
          <kbd className="pointer-events-none ml-auto hidden select-none items-center gap-0.5 rounded border border-border bg-muted px-1.5 font-mono text-[0.65rem] font-medium text-muted-foreground sm:inline-flex">
            <span className="text-xs">⌘</span>K
          </kbd>
        </button>

        {ordered.map((task) => {
          const isSelected = view === "chat" && task.id === activeId;
          return (
            <div
              key={task.id}
              className={cn(
                "group flex items-center border-l-[3px] border-transparent py-1.5 pr-3 pl-[2.55rem] transition-colors",
                isSelected ? "border-brand-ink bg-accent" : "hover:bg-accent",
              )}
            >
              <button
                type="button"
                onClick={() => {
                  onSelectTask(task.id);
                  onNavigate("chat");
                  onAfterNavigate?.();
                }}
                className="min-w-0 flex-1 text-left"
              >
                <span
                  className={cn(
                    "block truncate text-[0.8rem] font-medium",
                    isSelected
                      ? "text-brand-ink"
                      : "text-muted-foreground group-hover:text-foreground",
                  )}
                >
                  {task.title || "Untitled"}
                </span>
              </button>
              {/* One grid cell holds both the timestamp and the overflow
                  affordance so the hover swap cannot reflow the row. */}
              <div className="ml-2 grid w-8 shrink-0 items-center justify-items-center [grid-template-areas:'slot']">
                {task.running ? (
                  <span
                    className="size-2 rounded-full bg-brand [grid-area:slot]"
                    title="Running"
                  />
                ) : (
                  <span className="text-xs tabular-nums text-muted-foreground/50 [grid-area:slot] lg:group-hover:invisible">
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

        {NAV_ITEMS.slice(1).map((item, index) => {
          const previous = NAV_ITEMS[index];
          const showHeading = item.group !== undefined && item.group !== previous.group;
          if (!showHeading) return renderNavLink(item);
          return (
            // `contents` keeps the heading and its first item in the parent's
            // gap-1 rhythm instead of introducing a nested box.
            <div key={item.key} className="contents">
              <div className="px-5 pt-7 pb-1 text-[0.7rem] font-medium tracking-wider text-muted-foreground uppercase">
                {item.group}
              </div>
              {renderNavLink(item)}
            </div>
          );
        })}
      </div>

      <div className="border-t border-border py-1">{renderNavLink(FOOTER_ITEM)}</div>

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
