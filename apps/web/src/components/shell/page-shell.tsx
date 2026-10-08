// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";

/**
 * Scroll container for every workspace page except chat. Content is full width and
 * left-aligned with the prototype's gutters; a page that needs a reading width caps
 * its own content through `className` instead of centering a column.
 */
export function PageShell({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className="h-full overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
      <div className={className}>{children}</div>
    </div>
  );
}
