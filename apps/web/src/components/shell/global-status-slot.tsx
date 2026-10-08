// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";

/** Reserves the full-width status position; callers own banner facts and actions. */
export function GlobalStatusSlot({ children }: { children?: ReactNode }) {
  return (
    <div className="w-full shrink-0" data-shell-global-status="">
      {children}
    </div>
  );
}
