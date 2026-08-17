import {
  BookOpenCheck,
  Clock3,
  MessageCircle,
  Settings,
  Sparkles,
  type LucideIcon,
} from "lucide-react";

/**
 * The left rail's destinations.
 *
 * These are VIEWS, not routes: page.tsx owns the live SSE stream, the session
 * id, and the task transcripts, so navigating by URL would mean lifting all of
 * that into a layout and remounting the stream on every move. Switching a view
 * keeps the run alive while the operator reads their skills list.
 *
 * Icon references are component values, which is also why this file is imported
 * by client components only — they cannot cross a server/client prop boundary.
 */
export type ViewKey = "chat" | "skills" | "memory" | "schedules" | "settings";

export type NavItem = {
  readonly key: ViewKey;
  readonly label: string;
  readonly icon: LucideIcon;
  readonly group?: string;
};

export const NAV_ITEMS: readonly NavItem[] = [
  { key: "chat", label: "Chat", icon: MessageCircle },
  { key: "skills", label: "Skills", icon: Sparkles, group: "Workspace" },
  { key: "memory", label: "Memory", icon: BookOpenCheck, group: "Workspace" },
  { key: "schedules", label: "Scheduled", icon: Clock3, group: "Workspace" },
  { key: "settings", label: "Settings", icon: Settings },
];
