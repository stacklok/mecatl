/**
 * The Atrium workspace navigation configuration — the single canonical
 * nav-items module. The top nav is generic over the `ShellNav` built here:
 * a single row of workspace destinations — the in-app chat plus the state
 * that outlives a turn (Skills, Memory, Scheduled). The wordmark and home
 * both point at Chats.
 */

import { Clock, GraduationCap, MessageCircle, Settings } from "lucide-react";
import type { ComponentType } from "react";
import { ATRIUM_WORKSPACE_HOME } from "@/lib/feature-flags";

interface NavItem {
  /** Stable key for React keys and test selectors. */
  readonly key: string;
  /** Nav label (pill text when active, tooltip when inactive). */
  readonly label: string;
  /** Absolute route this destination points at. */
  readonly href: string;
  /** Nav icon. */
  readonly icon: ComponentType<{ className?: string }>;
}

/**
 * The console's navigation configuration. One value drives the whole shell:
 * the top-nav destinations and the wordmark's home link.
 */
export interface ShellNav {
  /** The console's root route (used for the "home" active-state rule). */
  readonly homeHref: string;
  /** Where the wordmark links, if different from `homeHref` (e.g. Atrium
      sends the logo to the workspace chats rather than the gateway home). */
  readonly logoHref?: string;
  /** `aria-label` for the `<nav>` landmark. */
  readonly navLabel: string;
  /** Ordered nav destinations. */
  readonly items: readonly NavItem[];
}

/** Builds the Atrium workspace `ShellNav`. */
export function buildUserNav(): ShellNav {
  return {
    // The workspace root (redirects to chat). Kept distinct from the Chats
    // item's href so Chats highlights on any /workspace/chat/* route rather
    // than only the exact base path (the "console root" exact-match rule).
    homeHref: "/workspace",
    logoHref: ATRIUM_WORKSPACE_HOME,
    navLabel: "Main navigation",
    items: [
      {
        key: "chat",
        label: "Chats",
        href: "/workspace/chat",
        icon: MessageCircle,
      },
      {
        key: "agent-schedules",
        label: "Scheduled",
        href: "/workspace/schedules",
        icon: Clock,
      },
      {
        key: "agent-skills",
        label: "Skills",
        href: "/workspace/skills",
        icon: GraduationCap,
      },
      {
        key: "settings",
        label: "Settings",
        href: "/workspace/settings",
        icon: Settings,
      },
    ],
  };
}
