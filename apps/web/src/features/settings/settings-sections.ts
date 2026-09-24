// SPDX-License-Identifier: Apache-2.0

export type SettingsSection =
  | "about"
  | "agent"
  | "appearance"
  | "diagnostics"
  | "labs"
  | "learning"
  | "mcp-tools"
  | "memory"
  | "models"
  | "permissions"
  | "profile"
  | "providers"
  | "storage";

export function isSettingsSection(value: string): value is SettingsSection {
  return (
    value === "profile" ||
    value === "agent" ||
    value === "appearance" ||
    value === "permissions" ||
    value === "providers" ||
    value === "mcp-tools" ||
    value === "memory" ||
    value === "learning" ||
    value === "models" ||
    value === "storage" ||
    value === "diagnostics" ||
    value === "labs" ||
    value === "about"
  );
}
