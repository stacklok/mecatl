// SPDX-License-Identifier: Apache-2.0

export type SettingsSection =
  | "about"
  | "agent"
  | "appearance"
  | "learning"
  | "memory"
  | "models"
  | "profile"
  | "storage";

export function isSettingsSection(value: string): value is SettingsSection {
  return (
    value === "profile" ||
    value === "agent" ||
    value === "appearance" ||
    value === "memory" ||
    value === "learning" ||
    value === "models" ||
    value === "storage" ||
    value === "about"
  );
}
