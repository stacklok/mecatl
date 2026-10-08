// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";

/**
 * Card shell for Settings sections in the prototype's grammar: an uppercase
 * eyebrow title and no icon tile. On narrow screens the card chrome dissolves
 * into the page, because the section is already named by the navigation.
 */
export function SettingsCard({ children, title }: { children: ReactNode; title: string }) {
  return (
    <section className="rounded-xl border bg-card p-5 max-[499px]:rounded-none max-[499px]:border-0 max-[499px]:bg-transparent max-[499px]:p-0">
      <h2 className="mb-4 text-sm font-semibold tracking-wide text-muted-foreground uppercase max-[499px]:hidden">
        {title}
      </h2>
      {children}
    </section>
  );
}

/** Explanatory copy inside a settings card. */
export function Note({ children, role }: { children: ReactNode; role?: "status" | "alert" }) {
  return (
    <p className="text-sm text-muted-foreground" role={role}>
      {children}
    </p>
  );
}
