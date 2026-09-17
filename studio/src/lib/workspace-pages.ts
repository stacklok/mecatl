import { CircleHelp, Keyboard } from "lucide-react";
import type { ComponentType } from "react";

/**
 * Static workspace pages the global search (⌘K) can open: the help surfaces
 * that otherwise have no visible entry point beyond a keystroke — Help &
 * about and the keyboard shortcuts reference. Lives in lib/ so the shell's
 * search data does not import from app/; the settings IA
 * (settings-sections.ts) links the same hrefs, and
 * atrium-search-data.test.ts pins that the two agree.
 */
export interface WorkspacePage {
  readonly href: string;
  readonly label: string;
  /** The muted second line in a search result. */
  readonly description: string;
  readonly icon: ComponentType<{ className?: string }>;
  /** Extra words a search matches on but does not display. */
  readonly keywords: readonly string[];
}

/** The keyboard shortcuts reference (`?`, ⌘/, `/help`). */
export const SHORTCUTS_PAGE_HREF = "/workspace/shortcuts";

export const WORKSPACE_PAGES: readonly WorkspacePage[] = [
  {
    href: "/workspace/settings/help",
    label: "Help & about",
    description:
      "Studio's version, the documentation, and the configuration reference",
    icon: CircleHelp,
    keywords: [
      "help",
      "about",
      "version",
      "docs",
      "documentation",
      "configuration",
      "environment",
      "support",
    ],
  },
  {
    href: SHORTCUTS_PAGE_HREF,
    label: "Keyboard shortcuts",
    description: "Every shortcut, and the features this daemon enables",
    icon: Keyboard,
    keywords: ["help", "keys", "keymap", "hotkeys", "reference", "features"],
  },
];
