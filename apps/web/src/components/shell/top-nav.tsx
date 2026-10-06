// SPDX-License-Identifier: Apache-2.0

import { Link, useRouterState } from "@tanstack/react-router";
import { GlobalSearch } from "../../features/search/global-search";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";
import { navItems } from "./nav-items";

/**
 * The workspace top navigation bar. Carries only the logo, the primary nav
 * pills, and global search — no connection/theme/auth status chips (those
 * live in Settings or a conditional banner instead), matching Studio's
 * "the top nav carries no status chips" rule.
 */
export function TopNav() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });

  return (
    <header className="flex min-h-16 shrink-0 items-center justify-between gap-1 px-2.5 min-[500px]:gap-3 min-[500px]:px-5">
      <Link
        aria-label="Stacklok — go to Chats"
        className="flex size-11 shrink-0 items-center justify-center rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--shell-gradient-mid)]"
        search={{ sessionId: undefined }}
        to="/workspace/chat"
      >
        <span
          aria-hidden="true"
          className="block h-[21px] w-6 bg-white [mask-image:url(/stacklok-logo-mark.svg)] [mask-position:center] [mask-repeat:no-repeat] [mask-size:contain]"
        />
      </Link>

      <div className="flex min-w-0 items-center gap-1 min-[500px]:gap-3 min-[900px]:gap-5">
        <nav aria-label="Main navigation" className="flex min-w-0 items-center gap-0.5">
          {navItems.map((item) => {
            const active = pathname === item.to || pathname.startsWith(`${item.to}/`);
            const Icon = item.icon;

            if (active) {
              return (
                <Link
                  aria-current="page"
                  className="flex size-11 shrink-0 items-center justify-center gap-1.5 rounded-full bg-nav-pill-bg text-[13px] font-semibold text-nav-pill-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--shell-gradient-mid)] min-[500px]:w-auto min-[500px]:px-4"
                  key={item.key}
                  to={item.to}
                >
                  <Icon aria-hidden="true" className="size-[17px] shrink-0" />
                  <span className="sr-only min-[500px]:not-sr-only">{item.label}</span>
                </Link>
              );
            }

            return (
              <Tooltip key={item.key}>
                <TooltipTrigger asChild>
                  <Link
                    className="flex size-11 shrink-0 items-center justify-center rounded-full text-nav-icon transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--shell-gradient-mid)] min-[500px]:w-12"
                    to={item.to}
                  >
                    <Icon aria-hidden="true" className="size-[17px] shrink-0" />
                    <span className="sr-only">{item.label}</span>
                  </Link>
                </TooltipTrigger>
                <TooltipContent side="bottom">{item.label}</TooltipContent>
              </Tooltip>
            );
          })}
        </nav>

        <div className="shrink-0 [&_kbd]:rounded-full [&_kbd]:border-transparent [&_kbd]:bg-nav-kbd-bg [&_kbd]:text-brand-foreground [&>button]:min-h-11 [&>button]:min-w-11 [&>button]:rounded-full [&>button]:border-nav-search-border [&>button]:bg-transparent [&>button]:text-nav-search-text [&>button]:hover:bg-white/10 [&>button]:hover:text-white [&>button]:focus-visible:ring-nav-search-text min-[900px]:[&>button]:w-[214px]">
          <GlobalSearch />
        </div>
      </div>
    </header>
  );
}
