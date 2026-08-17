import {
  Clock,
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
export type ViewKey = "chat" | "skills" | "schedules" | "settings";

export type NavItem = {
  readonly key: ViewKey;
  readonly label: string;
  readonly icon: LucideIcon;
  readonly group?: string;
};

export const NAV_ITEMS: readonly NavItem[] = [
  { key: "chat", label: "Chats", icon: MessageCircle, group: "Workspace" },
  { key: "schedules", label: "Scheduled", icon: Clock, group: "Workspace" },
  { key: "skills", label: "Skills", icon: Sparkles, group: "Workspace" },
  { key: "settings", label: "Settings", icon: Settings },
];
