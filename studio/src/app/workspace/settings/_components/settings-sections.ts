import {
  Bot,
  Brain,
  FlaskConical,
  GraduationCap,
  Network,
  Palette,
  Route,
  Server,
  UserRound,
} from "lucide-react";

export interface SettingsSection {
  href: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
}

/**
 * The settings information architecture, shared by the desktop secondary nav,
 * the mobile drill-down list on the settings index, and the mobile subpage
 * back-header. One entry per subpage.
 */
export const SETTINGS_GROUPS: Array<{
  label: string;
  items: SettingsSection[];
}> = [
  {
    label: "Preferences",
    items: [
      {
        href: "/workspace/settings/profile",
        label: "You",
        icon: UserRound,
      },
      {
        href: "/workspace/settings/appearance",
        label: "Personalize",
        icon: Palette,
      },
      // After Appearance: identity, then how the app looks, then how the chat
      // behaves, then when it interrupts you.
    ],
  },
  {
    label: "Agent runtime",
    items: [
      {
        href: "/workspace/settings/agent",
        label: "Agent",
        icon: Bot,
      },
      {
        href: "/workspace/settings/memory",
        label: "Memory",
        icon: Brain,
      },
      {
        href: "/workspace/settings/learning",
        label: "Learning",
        icon: GraduationCap,
      },
      {
        href: "/workspace/settings/provider",
        label: "Provider",
        icon: Server,
      },
      {
        href: "/workspace/settings/model-router",
        label: "Model router",
        icon: Route,
      },
      {
        href: "/workspace/settings/gateway",
        label: "MCP gateway",
        icon: Network,
      },
    ],
  },
  {
    label: "Experimental",
    items: [
      {
        href: "/workspace/settings/labs",
        label: "Labs",
        icon: FlaskConical,
      },
    ],
  },
];

/**
 * The section a pathname belongs to, for the mobile back-header and the
 * desktop nav active state. Prefix-aware so a section's deeper pages
 * (e.g. a memory entry) still resolve to their section.
 */
export function settingsSectionFor(
  pathname: string,
): SettingsSection | undefined {
  for (const group of SETTINGS_GROUPS) {
    const hit = group.items.find(
      (item) => item.href === pathname || pathname.startsWith(`${item.href}/`),
    );
    if (hit) return hit;
  }
  return undefined;
}
