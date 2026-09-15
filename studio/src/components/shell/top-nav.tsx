"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { buildUserNav } from "@/components/app/nav-items";
import { GlobalSearch } from "@/components/shell/global-search";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";

/**
 * The workspace top navigation bar. It sits directly on the fixed dark-green
 * gradient (see `workspace/layout.tsx`), so every colour here is a fixed
 * brand colour, identical in light and dark themes — only the card below it
 * follows the theme.
 *
 * Left: the Stacklok logo mark, painted white via a CSS mask over the brand
 * SVG (the asset itself is never recoloured). Centre-right: the pill nav from
 * `buildUserNav()` — the active route renders as a light pill with icon +
 * label, inactive routes are icon-only round buttons with a tooltip.
 * Far right: the global search trigger (the existing ⌘K `GlobalSearch`
 * palette), restyled from the outside for the dark band.
 *
 * A Client Component: it needs `usePathname()` for the active state, and the
 * nav config carries icon component references that must stay inside the
 * client module graph.
 */
export function TopNav() {
  const pathname = usePathname() ?? "";
  const nav = buildUserNav();

  return (
    <header className="flex h-16 shrink-0 items-center justify-between gap-3 px-2.5 min-[500px]:px-5">
      <Link
        href={nav.logoHref ?? nav.homeHref}
        aria-label="Stacklok — go to Chats"
        className="flex shrink-0 items-center rounded-sm pl-1.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60"
      >
        <span
          aria-hidden="true"
          className="block h-[21px] w-6 bg-white [mask-image:url(/stacklok-logo-mark.svg)] [mask-position:center] [mask-repeat:no-repeat] [mask-size:contain]"
        />
      </Link>

      <div className="flex min-w-0 items-center gap-3 min-[500px]:gap-5">
        <nav
          aria-label={nav.navLabel}
          className="flex min-w-0 items-center gap-1 overflow-x-auto"
        >
          {nav.items.map((item) => {
            const active =
              pathname === item.href || pathname.startsWith(`${item.href}/`);
            const Icon = item.icon;

            if (active) {
              return (
                <Link
                  key={item.key}
                  href={item.href}
                  aria-current="page"
                  className="flex h-9 shrink-0 items-center justify-center gap-1.5 rounded-full bg-[#cadfd8] px-0 text-[13px] font-semibold text-[#03433e] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60 max-[499px]:w-12 min-[500px]:px-4"
                >
                  <Icon className="size-[17px] shrink-0" />
                  <span className="hidden min-[500px]:inline">
                    {item.label}
                  </span>
                  <span className="sr-only min-[500px]:hidden">
                    {item.label}
                  </span>
                </Link>
              );
            }

            return (
              <Tooltip key={item.key}>
                <TooltipTrigger asChild>
                  <Link
                    href={item.href}
                    className="flex h-9 w-12 shrink-0 items-center justify-center rounded-full text-[#a5b8b4] transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60"
                  >
                    <Icon className="size-[17px] shrink-0" />
                    <span className="sr-only">{item.label}</span>
                  </Link>
                </TooltipTrigger>
                <TooltipContent side="bottom">{item.label}</TooltipContent>
              </Tooltip>
            );
          })}
        </nav>

        {/* GlobalSearch owns its trigger, dialog, and the ⌘K shortcut; it is
            styled for a light surface, so restyle the trigger (and its kbd
            chip) from outside for the dark gradient band. Below `sm` the
            trigger already collapses to an icon-only button. */}
        <div className="shrink-0 [&_kbd]:rounded-full [&_kbd]:border-transparent [&_kbd]:bg-[#3f605a] [&_kbd]:text-[#a5b8b4] [&>button]:rounded-full [&>button]:border-[#6e807d] [&>button]:bg-transparent [&>button]:text-[#b4c0c1] [&>button]:hover:bg-white/10 [&>button]:hover:text-white [&>button]:focus-visible:ring-white/60 [&>button]:min-[500px]:w-[214px]">
          <GlobalSearch />
        </div>
      </div>
    </header>
  );
}
