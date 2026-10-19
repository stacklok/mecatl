// SPDX-License-Identifier: Apache-2.0

import {
  Activity,
  Bot,
  Boxes,
  Brain,
  CircleHelp,
  Database,
  FlaskConical,
  GraduationCap,
  Network,
  Palette,
  Server,
  ShieldCheck,
  UserRound,
} from "lucide-react";

/**
 * The route guard and both navigation modes use this same section inventory.
 * Groups, labels, and icons follow the prototype's `SETTINGS_GROUPS`; the
 * values are Studio's own slugs, and their order is part of the route contract.
 */
export const settingsGroups = [
  {
    title: "Preferences",
    items: [
      { icon: UserRound, label: "You", value: "profile" },
      { icon: Palette, label: "Personalise", value: "appearance" },
    ],
  },
  {
    title: "Agent runtime",
    items: [
      { icon: Bot, label: "Agent", value: "agent" },
      { icon: ShieldCheck, label: "Permissions", value: "permissions" },
      { icon: Server, label: "Providers", value: "providers" },
      { icon: Boxes, label: "Models", value: "models" },
      { icon: Network, label: "MCP tools", value: "mcp-tools" },
      { icon: Database, label: "Storage", value: "storage" },
      { icon: Brain, label: "Memory", value: "memory" },
      { icon: GraduationCap, label: "Learning", value: "learning" },
      { icon: Activity, label: "Diagnostics", value: "diagnostics" },
    ],
  },
  {
    title: "Experimental",
    items: [{ icon: FlaskConical, label: "Labs", value: "labs" }],
  },
  { title: "Support", items: [{ icon: CircleHelp, label: "About", value: "about" }] },
] as const;

export type SettingsSection = (typeof settingsGroups)[number]["items"][number]["value"];

type SettingsItem = (typeof settingsGroups)[number]["items"][number];

const settingsItems: ReadonlyMap<string, SettingsItem> = new Map(
  settingsGroups.flatMap((group) => group.items.map((item) => [item.value, item] as const)),
);

export function isSettingsSection(value: string): value is SettingsSection {
  return settingsItems.has(value);
}

/** The navigation entry for a section, for its label and icon. */
export function settingsItem(section: SettingsSection): SettingsItem {
  const item = settingsItems.get(section);
  if (!item) throw new Error(`Unknown settings section: ${section}`);
  return item;
}
