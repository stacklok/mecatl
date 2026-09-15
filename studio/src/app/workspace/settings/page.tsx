"use client";

import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { cn } from "@/lib/utils";
import { SETTINGS_GROUPS } from "./_components/settings-sections";

/**
 * The settings index. On mobile it is the first level of a native-style
 * drill-down, following the inset-grouped-list convention: filled cards
 * (no border), a neutral icon square, and hairline dividers inset to the
 * text edge. Desktop keeps the old
 * behaviour — land on the first section, with the left secondary nav for
 * switching — via a client redirect (the split is a viewport question,
 * so the server cannot decide it).
 */
export default function SettingsIndexPage() {
  const router = useRouter();

  useEffect(() => {
    if (window.matchMedia("(min-width: 500px)").matches) {
      router.replace("/workspace/settings/profile");
    }
  }, [router]);

  return (
    <div className="space-y-7 min-[500px]:hidden">
      {SETTINGS_GROUPS.map((group) => (
        <section key={group.label}>
          <p className="px-4 pb-2 text-[13px] font-medium text-muted-foreground">
            {group.label}
          </p>
          <div className="overflow-hidden rounded-2xl bg-muted/50">
            {group.items.map((item, index) => {
              const Icon = item.icon;
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className="group flex items-center gap-3 pl-4 transition-colors active:bg-black/[0.04] dark:active:bg-white/[0.06]"
                >
                  {/* Bare glyph, sized explicitly so the global mobile
                      size-4 bump doesn't inflate it. */}
                  <Icon className="size-[18px] shrink-0 text-muted-foreground" />
                  {/* The divider hangs off the row body so it stays inset
                      to the text edge, native-list style. */}
                  <span
                    className={cn(
                      "flex min-h-[46px] min-w-0 flex-1 items-center gap-3 pr-4",
                      index > 0 && "border-t border-border/60",
                    )}
                  >
                    <span className="min-w-0 flex-1 truncate text-[15px] leading-normal">
                      {item.label}
                    </span>
                    <ChevronRight className="size-4 shrink-0 text-muted-foreground/50" />
                  </span>
                </Link>
              );
            })}
          </div>
        </section>
      ))}
    </div>
  );
}
