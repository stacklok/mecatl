// SPDX-License-Identifier: Apache-2.0

import { Loader2 } from "lucide-react";
import type { ReactNode } from "react";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

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

/**
 * The prototype's settings row: label and optional description on the left,
 * the control on the right. Wrap consecutive rows in
 * `<div className="divide-y divide-border/60">` for hairline separators.
 */
export function SettingsRow({
  children,
  className,
  description,
  htmlFor,
  label,
}: {
  children: ReactNode;
  className?: string;
  description?: ReactNode;
  /** Ties the label to a form control. */
  htmlFor?: string;
  label: ReactNode;
}) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center justify-between gap-x-4 gap-y-2 py-4 first:pt-0 last:pb-0",
        className,
      )}
    >
      <div className="min-w-0 space-y-0.5">
        {htmlFor ? (
          <Label className="text-sm font-medium" htmlFor={htmlFor}>
            {label}
          </Label>
        ) : (
          <h3 className="text-sm font-medium">{label}</h3>
        )}
        {description ? (
          <p className="max-w-md text-xs text-muted-foreground">{description}</p>
        ) : null}
      </div>
      <div className="flex shrink-0 items-center gap-2">{children}</div>
    </div>
  );
}

/** A bordered list of read-only facts, in the prototype's About-card grammar. */
export function FactList({ children }: { children: ReactNode }) {
  return <dl className="divide-y rounded-lg border bg-background">{children}</dl>;
}

export function FactRow({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="flex items-start justify-between gap-3 px-4 py-3">
      <dt className="shrink-0 text-sm">{label}</dt>
      <dd className="min-w-0 break-words text-right text-sm text-muted-foreground">{children}</dd>
    </div>
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

export type SettingsState = { kind: "loading" | "notice" | "error"; text: string };

/**
 * The runtime status line: a loading, offline, or failure note in place of a
 * section's facts. Loading and notices are polite `status` regions; a failed
 * read is an `alert`.
 */
export function RuntimeStatusLine({ state }: { state: SettingsState }) {
  if (state.kind === "error") {
    return (
      <p
        className="rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive"
        role="alert"
      >
        {state.text}
      </p>
    );
  }
  return (
    <p className="flex items-center gap-2 text-sm text-muted-foreground" role="status">
      {state.kind === "loading" && (
        <Loader2 aria-hidden="true" className="size-3.5 shrink-0 animate-spin" />
      )}
      <span>{state.text}</span>
    </p>
  );
}
