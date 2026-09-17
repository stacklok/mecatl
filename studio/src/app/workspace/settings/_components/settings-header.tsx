"use client";

import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { Button } from "@/components/ui/button";
import { pageTitleClass } from "@/lib/typography";
import { cn } from "@/lib/utils";
import { settingsSectionFor } from "./settings-sections";

/**
 * Mobile-only subpage header, styled like the chat header: a full-bleed
 * h-14 bar with a back arrow and the section title. On a section's deeper
 * pages (e.g. a memory entry) the arrow goes up to the section; on the
 * section page itself it goes back to the settings index. Renders nothing
 * on the index or from 500px up.
 */
export function SettingsMobileBar() {
  const pathname = usePathname() ?? "";
  const section = settingsSectionFor(pathname);
  if (!section) return null;

  const backHref =
    pathname === section.href ? "/workspace/settings" : section.href;

  return (
    <div className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-3 min-[500px]:hidden">
      <Button
        variant="ghost"
        size="icon"
        className="size-7 shrink-0 text-muted-foreground"
        asChild
      >
        <Link href={backHref} aria-label="Back">
          <ArrowLeft className="size-4" />
        </Link>
      </Button>
      <h1 className="min-w-0 flex-1 truncate text-sm font-semibold">
        {section.label}
      </h1>
    </div>
  );
}

/**
 * The big serif page title. On a mobile subpage the SettingsMobileBar
 * replaces it; everywhere else (desktop, and the mobile index) it renders.
 */
export function SettingsTitle() {
  const pathname = usePathname() ?? "";
  const section = settingsSectionFor(pathname);

  return (
    <h1
      className={cn(
        pageTitleClass("truncate pb-0 text-3xl leading-tight"),
        section && "max-[499px]:hidden",
      )}
    >
      Settings
    </h1>
  );
}
