import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

/** Shared card shell for Settings sections, so runtime and preference cards
 * read as one page. Follows the app's sectioned-settings grammar (see the
 * keyboard-shortcuts page): `rounded-xl border bg-card p-5` with an uppercase
 * eyebrow title. */
export function SettingsCard({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    // On mobile the card chrome is redundant — the back bar already names
    // the section — so the box and header dissolve into the page.
    <section className="rounded-xl border bg-card p-5 max-[499px]:rounded-none max-[499px]:border-0 max-[499px]:bg-transparent max-[499px]:p-0">
      <div className="mb-4 space-y-1 max-[499px]:hidden">
        <h2 className="text-sm font-semibold tracking-wide text-muted-foreground uppercase">
          {title}
        </h2>
        {description ? (
          <p className="text-xs text-muted-foreground">{description}</p>
        ) : null}
      </div>
      {children}
    </section>
  );
}

/**
 * The shared settings row idiom: label (and optional description) on the
 * left, the control on the right. Wrap consecutive rows in
 * `<div className="divide-y divide-border/60">` for hairline separators —
 * the first/last padding collapses so a lone row sits flush in its card.
 */
export function SettingsRow({
  label,
  description,
  htmlFor,
  className,
  children,
}: {
  label: React.ReactNode;
  description?: React.ReactNode;
  /** Ties the label to a form control (e.g. a Switch or Input id). */
  htmlFor?: string;
  className?: string;
  children: React.ReactNode;
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
          <Label htmlFor={htmlFor} className="text-sm font-medium">
            {label}
          </Label>
        ) : (
          <p className="text-sm font-medium">{label}</p>
        )}
        {description ? (
          <p className="max-w-md text-xs text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      <div className="flex shrink-0 items-center gap-2">{children}</div>
    </div>
  );
}

export function Note({ children }: { children: React.ReactNode }) {
  return <p className="text-sm text-muted-foreground">{children}</p>;
}

/** The ONE restart sentence every settings surface uses — pending lines,
 * confirm dialogs and card notes alike — so the warning reads the same
 * wherever a save restarts the agent. */
export const RESTART_SENTENCE =
  "Changes restart the agent. Anything running will stop.";

/** Shown in place of a form when the configuration is owned elsewhere: the
 * controller answers 409 for every write in external mode, so offering the
 * form would only manufacture errors. */
export function ExternalManagedNote() {
  return (
    <Note>
      The agent is run somewhere else, so these settings can&rsquo;t be changed
      here.
    </Note>
  );
}

export function OfflineNote() {
  return (
    <Note>
      The agent is offline, so these settings can&rsquo;t be read right now.
    </Note>
  );
}
