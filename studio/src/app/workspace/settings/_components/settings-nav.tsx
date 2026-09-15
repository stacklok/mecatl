"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { SETTINGS_GROUPS } from "./settings-sections";

/**
 * The settings sections as a left secondary menu; one subpage per section.
 * Hidden on mobile, where the settings index renders the same sections as a
 * drill-down list instead (see `settings/page.tsx`).
 */
export function SettingsNav() {
  const pathname = usePathname();
  return (
    <nav
      aria-label="Settings sections"
      // hidden + min-[500px]:flex (not max-[499px]:hidden): both this nav and
      // the index drill-down list pivot on the SAME 500px edge, so a
      // fractional viewport width can never render both at once.
      className="hidden w-44 shrink-0 flex-col gap-5 min-[500px]:flex"
    >
      {SETTINGS_GROUPS.map((group) => (
        <div key={group.label} className="space-y-1">
          <p className="px-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
            {group.label}
          </p>
          <ul className="flex flex-col gap-1">
            {group.items.map((item) => {
              const isActive =
                pathname === item.href || pathname?.startsWith(`${item.href}/`);
              return (
                <li key={item.href}>
                  <Link
                    href={item.href}
                    aria-current={isActive ? "page" : undefined}
                    className={cn(
                      "block rounded-lg px-2 py-1.5 text-sm whitespace-nowrap transition-colors",
                      isActive
                        ? "bg-muted font-medium text-foreground"
                        : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
                    )}
                  >
                    {item.label}
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
    </nav>
  );
}
