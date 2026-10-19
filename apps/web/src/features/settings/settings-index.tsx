// SPDX-License-Identifier: Apache-2.0

import { Link, useNavigate } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useEffect, useId } from "react";
import { PageShell } from "../../components/shell/page-shell";
import { pageTitleClass } from "../../lib/typography";
import { useIsDesktop } from "../../lib/use-mobile";
import { cn } from "../../lib/utils";
import { settingsGroups } from "./settings-sections";

/**
 * The settings index at the bare `/workspace/settings`. On a phone it is the
 * first level of the prototype's drill-down, in its inset-grouped list look:
 * filled cards without a border, a neutral glyph, and hairline dividers inset
 * to the text edge. Each section page's back bar returns here. From the 500px
 * pivot up, Settings opens on You beside the section nav instead: the route
 * redirects there before it renders, and a window widened past the pivot while
 * the index is open follows.
 */
export function SettingsIndex() {
  const desktop = useIsDesktop();
  const navigate = useNavigate();
  useEffect(() => {
    if (!desktop) return;
    void navigate({
      params: { section: "profile" },
      replace: true,
      search: { item: undefined },
      to: "/workspace/settings/$section",
    });
  }, [desktop, navigate]);

  return (
    <PageShell>
      <div className="space-y-6">
        <h1 className={pageTitleClass()}>Settings</h1>
        <nav aria-label="Settings sections" className="space-y-7 min-[500px]:hidden">
          {settingsGroups.map((group) => (
            <SettingsIndexGroup group={group} key={group.title} />
          ))}
        </nav>
      </div>
    </PageShell>
  );
}

function SettingsIndexGroup({ group }: { group: (typeof settingsGroups)[number] }) {
  const titleId = useId();
  return (
    <div>
      <p className="px-4 pb-2 text-[13px] font-medium text-muted-foreground" id={titleId}>
        {group.title}
      </p>
      <ul aria-labelledby={titleId} className="overflow-hidden rounded-2xl bg-muted/50">
        {group.items.map((item, index) => {
          const Icon = item.icon;
          return (
            <li key={item.value}>
              <Link
                className="flex items-center gap-3 pl-4 transition-colors focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand active:bg-black/[0.04] dark:active:bg-white/[0.06]"
                params={{ section: item.value }}
                search={{ item: undefined }}
                to="/workspace/settings/$section"
              >
                {/* A bare glyph, sized explicitly so the mobile size-4 bump leaves it alone. */}
                <Icon aria-hidden="true" className="size-[18px] shrink-0 text-muted-foreground" />
                {/* The divider hangs off the row body, so it stays inset to the text edge.
                    Its fixed 46px height, the prototype's, clears the 44px touch minimum
                    at any root size. */}
                <span
                  className={cn(
                    "flex min-h-[46px] min-w-0 flex-1 items-center gap-3 pr-4",
                    index > 0 && "border-t border-border/60",
                  )}
                >
                  <span className="min-w-0 flex-1 truncate text-[15px] leading-normal">
                    {item.label}
                  </span>
                  <ChevronRight
                    aria-hidden="true"
                    className="size-4 shrink-0 text-muted-foreground/50"
                  />
                </span>
              </Link>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
