"use client";

import { NAV_ITEMS, type NavItem, type ViewKey } from "@/components/shell/nav-items";
import { cn } from "@/lib/utils";

/**
 * The narrow icon rail — the outer of the two navigation levels.
 *
 * Icon-only, so every item carries both `aria-label` and `title`: the label
 * names it for assistive tech, the title gives a pointer user the same name on
 * hover. A Radix tooltip would be prettier but would re-add a dependency for
 * five static strings.
 *
 * Same row grammar as the wider rail it replaces — a 3px left accent that turns
 * `brand-ink` when active — so the two navigation levels read as one system.
 */
export function IconRail({
  view,
  onNavigate,
  connection,
  className,
}: {
  view: ViewKey;
  onNavigate: (view: ViewKey) => void;
  connection: "checking" | "online" | "offline";
  className?: string;
}) {
  const connectionLabel =
    connection === "online"
      ? "Mecatl connected"
      : connection === "checking"
        ? "Checking connection"
        : "Mecatl offline";
  const renderItem = (item: NavItem) => {
    const isActive = item.key === view;
    const Icon = item.icon;
    return (
      <button
        key={item.key}
        type="button"
        onClick={() => onNavigate(item.key)}
        aria-label={item.label}
        aria-current={isActive ? "page" : undefined}
        title={item.label}
        className={cn(
          "flex h-11 w-full items-center justify-center border-l-[3px] border-transparent text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
          isActive && "border-brand-ink bg-accent text-brand-ink hover:text-brand-ink",
        )}
      >
        <Icon className="size-5 shrink-0" />
      </button>
    );
  };

  return (
    <nav
      aria-label="Sections"
      className={cn(
        "flex w-16 shrink-0 flex-col border-r border-border bg-sidebar",
        className,
      )}
    >
      {/* The brand block is h-16 so its seam lines up with the list panel's
          header and the navbar to its right. */}
      <div className="flex h-16 shrink-0 items-center justify-center border-b border-border">
        <span
          className="grid size-7 place-items-center rounded-md bg-brand font-serif text-[0.95rem] font-bold text-brand-foreground shadow-[inset_0_0_0_1px_rgba(255,255,255,.16)]"
          title="Mecatl Studio"
        >
          M
        </span>
      </div>

      <div className="flex flex-1 flex-col gap-1 py-3">
        {NAV_ITEMS.map(renderItem)}
      </div>

      {/* Daemon reachability stays visible at all times. It used to sit in the
          old wide rail's footer; burying it in the profile menu would mean an
          operator only discovers a dead daemon by sending a prompt. */}
      <div
        className="flex h-8 shrink-0 items-center justify-center border-t border-border"
        title={connectionLabel}
      >
        <span className="sr-only">{connectionLabel}</span>
        <span
          aria-hidden="true"
          className={cn(
            "size-2 rounded-full",
            connection === "online"
              ? "bg-success"
              : connection === "checking"
                ? "bg-warning"
                : "bg-destructive",
          )}
        />
      </div>
    </nav>
  );
}
