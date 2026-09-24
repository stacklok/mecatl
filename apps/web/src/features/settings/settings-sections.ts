// SPDX-License-Identifier: Apache-2.0

/** The route guard and both navigation modes use this same section inventory. */
export const settingsGroups = [
  {
    title: "Preferences",
    items: [
      { label: "Profile", value: "profile" },
      { label: "Appearance", value: "appearance" },
    ],
  },
  {
    title: "Agent runtime",
    items: [
      { label: "Agent", value: "agent" },
      { label: "Permissions", value: "permissions" },
      { label: "Providers", value: "providers" },
      { label: "Models", value: "models" },
      { label: "MCP tools", value: "mcp-tools" },
      { label: "Storage", value: "storage" },
      { label: "Memory", value: "memory" },
      { label: "Learning", value: "learning" },
      { label: "Diagnostics", value: "diagnostics" },
      { label: "Labs", value: "labs" },
    ],
  },
  { title: "Support", items: [{ label: "About", value: "about" }] },
] as const;

export type SettingsSection = (typeof settingsGroups)[number]["items"][number]["value"];

const settingsSections: ReadonlySet<string> = new Set(
  settingsGroups.flatMap((group) => group.items.map((item) => item.value)),
);

export function isSettingsSection(value: string): value is SettingsSection {
  return settingsSections.has(value);
}
