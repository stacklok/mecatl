"use client";

import { NAV_ITEMS, type NavItem, type ViewKey } from "@/components/shell/nav-items";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/**
 * The narrow icon rail — the outer of the two navigation levels.
 *
 * Metrics and states are the console's own icon-only rail: a 3px left accent
 * that turns `brand-ink` when active, `justify-center py-2.5` rows, and 19px
 * icons (its expanded rail uses 17px — an icon-only row has no label to sit
 * against, so it carries slightly more weight).
 *
 * Icon-only rows get a real tooltip rather than a `title`: the console does the
 * same, and a native tooltip's delay makes an unlabelled rail feel unresponsive.
 * The accessible name comes from the sr-only label inside the button, so the
 * name and the visible tooltip can never disagree.
 */
export function IconRail({
  view,
  onNavigate,
  className,
}: {
  view: ViewKey;
  onNavigate: (view: ViewKey) => void;
  className?: string;
}) {
  const renderItem = (item: NavItem) => {
    const isActive = item.key === view;
    const Icon = item.icon;
    return (
      <Tooltip key={item.key}>
        <TooltipTrigger asChild>
          <button
            type="button"
            onClick={() => onNavigate(item.key)}
            aria-current={isActive ? "page" : undefined}
            className={cn(
              "flex w-full items-center justify-center border-l-[3px] border-transparent py-2.5 font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground",
              isActive && "border-brand-ink bg-accent text-brand-ink hover:text-brand-ink",
            )}
          >
            <Icon className="size-[19px] shrink-0" />
            <span className="sr-only">{item.label}</span>
          </button>
        </TooltipTrigger>
        <TooltipContent side="right" sideOffset={6}>
          {item.label}
        </TooltipContent>
      </Tooltip>
    );
  };

  return (
    <TooltipProvider>
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

        <div className="flex flex-1 flex-col py-3">
          {NAV_ITEMS.map((item, index) => {
            // Icon-only: no heading text, but a top gap at each group boundary so
            // the runs still read as groups — the console's own approach.
            const previous = NAV_ITEMS[index - 1];
            const needsGap = index > 0 && item.group !== previous?.group;
            return (
              <div key={item.key} className={needsGap ? "pt-5" : undefined}>
                {renderItem(item)}
              </div>
            );
          })}
        </div>

      </nav>
    </TooltipProvider>
  );
}
