import type { Metadata } from "next";
import { keycaps, SHORTCUT_GROUPS, SHORTCUTS } from "@/lib/shortcuts/registry";
import { pageTitleClass } from "@/lib/typography";

export const metadata: Metadata = { title: "Keyboard shortcuts — Workspace" };

/** A single keycap. */
function Key({ children }: { children: React.ReactNode }) {
  return (
    <kbd className="inline-flex min-w-[1.75rem] items-center justify-center rounded-md border border-border bg-muted px-2 py-1 font-mono text-xs font-medium text-foreground shadow-sm">
      {children}
    </kbd>
  );
}

/**
 * Reference page listing every keyboard shortcut, rendered directly from the
 * shortcut registry so it can't drift from the live bindings. Reached from the
 * profile menu and by pressing `?`.
 */
export default function KeyboardShortcutsPage() {
  return (
    <div className="h-full overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
      <div className="max-w-3xl space-y-6">
        <div className="space-y-1">
          <h1 className={pageTitleClass("pb-0 text-3xl leading-tight")}>
            Keyboard shortcuts
          </h1>
          <p className="text-sm text-muted-foreground">
            Work faster with the keyboard. On Windows and Linux, use Ctrl
            wherever ⌘ is shown.
          </p>
        </div>

        <div className="grid gap-6 sm:grid-cols-2">
          {SHORTCUT_GROUPS.map((group) => (
            <section key={group} className="rounded-xl border bg-card p-5">
              <h2 className="mb-3 text-sm font-semibold text-muted-foreground uppercase tracking-wide">
                {group}
              </h2>
              <ul className="space-y-2.5">
                {SHORTCUTS.filter((s) => s.group === group).map((s) => (
                  <li
                    key={s.id}
                    className="flex items-center justify-between gap-4"
                  >
                    <span className="text-sm text-foreground">
                      {s.description}
                    </span>
                    <span className="flex shrink-0 items-center gap-1">
                      {keycaps(s.combo).map((k, i) => (
                        // biome-ignore lint/suspicious/noArrayIndexKey: positional keycaps
                        <Key key={i}>{k}</Key>
                      ))}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
