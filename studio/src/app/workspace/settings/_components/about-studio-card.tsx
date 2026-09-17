"use client";

import { BookOpen, ExternalLink, Keyboard, LifeBuoy } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { studioVersion } from "@/lib/studio-version";
import { SHORTCUTS_PAGE_HREF } from "@/lib/workspace-pages";
import { SettingsCard } from "./settings-card";

/** The public documentation site. */
const DOCS_URL = "https://mecatl.dev/docs/";
/** Where a problem is reported. */
const SUPPORT_URL = "https://github.com/stacklok/mecatl/issues";

/**
 * Studio's own version and the help entry points. The version is a
 * build-time fact inlined at `next build` (studio-version.ts), so the card is
 * complete before any request; the agent's half is the card beneath it.
 *
 * The links: the documentation site and the support page (both external, in
 * a new tab), and the in-app keyboard shortcuts reference.
 */
export function AboutStudioCard() {
  return (
    <SettingsCard
      title="About Studio"
      description="Which version of Studio you are using, and where to find help."
    >
      <div className="flex flex-col gap-3">
        <div className="divide-y rounded-lg border bg-background">
          <div className="flex items-center justify-between gap-3 px-4 py-3">
            <span className="text-sm">Version</span>
            <span
              className="break-all text-right text-sm text-muted-foreground"
              data-testid="about-studio-version"
            >
              {studioVersion()}
            </span>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" className="rounded-full" asChild>
            <a href={DOCS_URL} target="_blank" rel="noreferrer">
              <BookOpen className="size-4" />
              Documentation
              <ExternalLink className="size-3 text-muted-foreground" />
            </a>
          </Button>
          <Button variant="outline" size="sm" className="rounded-full" asChild>
            <a href={SUPPORT_URL} target="_blank" rel="noreferrer">
              <LifeBuoy className="size-4" />
              Report a problem
              <ExternalLink className="size-3 text-muted-foreground" />
            </a>
          </Button>
          <Button variant="outline" size="sm" className="rounded-full" asChild>
            <Link href={SHORTCUTS_PAGE_HREF}>
              <Keyboard className="size-4" />
              Keyboard shortcuts
            </Link>
          </Button>
        </div>
      </div>
    </SettingsCard>
  );
}
