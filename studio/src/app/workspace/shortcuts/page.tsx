import type { Metadata } from "next";
import { pageTitleClass } from "@/lib/typography";
import { ShortcutsReference } from "./_components/shortcuts-reference";

export const metadata: Metadata = { title: "Keyboard shortcuts — Workspace" };

/**
 * The help reference: every keyboard shortcut rendered from the shortcut
 * registry (so it can't drift from the live bindings), Studio's `/help`
 * command, the features the connected daemon enables, and the token legend.
 * Reached by pressing `?` (outside a text field), ⌘/ (anywhere, also while
 * typing), typing `/help` in the composer, from the chat ··· menu, from the
 * ⌘K search's Pages group, and from Settings → Keyboard and Settings → Help &
 * about (`src/lib/workspace-pages.ts` lists the visible entry points).
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
            Work faster with the keyboard, and see which features the connected
            daemon enables. On Windows and Linux, use Ctrl wherever ⌘ is shown.
          </p>
        </div>

        <ShortcutsReference />
      </div>
    </div>
  );
}
