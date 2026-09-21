// SPDX-License-Identifier: Apache-2.0

import { LayoutDashboard } from "lucide-react";

/**
 * Primary navigation. The bootstrap ships only the empty workspace; each
 * feature plan (chat, schedules, skills, settings) appends its own entry.
 */
export const navItems = [
  {
    key: "workspace",
    label: "Workspace",
    to: "/workspace",
    icon: LayoutDashboard,
  },
] as const;
