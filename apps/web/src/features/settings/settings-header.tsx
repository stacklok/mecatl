// SPDX-License-Identifier: Apache-2.0

import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import type { ReactNode } from "react";
import { PageShell } from "../../components/shell/page-shell";
import { Button } from "../../components/ui/button";
import { pageTitleClass } from "../../lib/typography";
import { useIsDesktop } from "../../lib/use-mobile";
import { type SettingsSection, settingsItem } from "./settings-sections";

/**
 * A section page's frame. On a phone, the prototype's back bar sits above the
 * scrolling page, full-bleed so its border spans the card, and returns to the
 * section index; the page below keeps PageShell's padding. From the 500px
 * pivot up the section nav does that job, so there is no bar. The frame's
 * shape is the same on both sides, so crossing the pivot keeps the section's
 * state.
 */
export function SettingsSectionFrame({
  children,
  section,
}: {
  children: ReactNode;
  section: SettingsSection;
}) {
  const desktop = useIsDesktop();
  return (
    <div className="flex h-full flex-col">
      {!desktop && <SettingsMobileBar section={section} />}
      <div className="min-h-0 flex-1">
        <PageShell>{children}</PageShell>
      </div>
    </div>
  );
}

/**
 * The page title beside the section nav. On a phone the back bar's heading
 * names the section instead, so a page always has exactly one `h1`.
 */
export function SettingsTitle() {
  return useIsDesktop() ? <h1 className={pageTitleClass()}>Settings</h1> : null;
}

/** The prototype's chat-style subpage header: a back arrow and the section's name. */
function SettingsMobileBar({ section }: { section: SettingsSection }) {
  return (
    <div className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-3 min-[500px]:hidden">
      {/* A 44px target, pulled 8px into the gutter on each side, so the arrow
          and the title sit where the prototype's 28px button puts them. */}
      <Button
        asChild
        className="-mx-2 size-11 shrink-0 text-muted-foreground"
        size="icon"
        variant="ghost"
      >
        <Link aria-label="Back to Settings" to="/workspace/settings">
          <ArrowLeft aria-hidden="true" className="size-4" />
        </Link>
      </Button>
      <h1 className="min-w-0 flex-1 truncate text-sm font-semibold">
        {settingsItem(section).label}
      </h1>
    </div>
  );
}
