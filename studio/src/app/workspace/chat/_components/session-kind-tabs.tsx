"use client";

import { ArrowRight } from "lucide-react";
import Link from "next/link";

/**
 * The sidebar's foot when the daemon reports storage health (the TUI's
 * Maintenance tab analogue): the inventory's maintenance lives on the
 * Storage settings page, so the list points there instead of duplicating it.
 */
export function StorageMaintenanceLink() {
  return (
    <div className="px-4 pt-3">
      <Link
        href="/workspace/settings/storage"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        Storage &amp; maintenance
        <ArrowRight className="size-3" aria-hidden="true" />
      </Link>
    </div>
  );
}
