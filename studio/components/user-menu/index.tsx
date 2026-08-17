"use client";

import {
  BookOpenCheck,
  Clock3,
  Plug,
  Shuffle,
  Sparkles,
  Zap,
} from "lucide-react";
import { useState } from "react";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

import { ThemeMenuItems } from "./theme-menu-items";
import { UserAvatar } from "./user-avatar";
import { UserMenuButton } from "./user-menu-button";

export type SettingsPanel =
  | "provider"
  | "router"
  | "mcp"
  | "skills"
  | "memory"
  | "schedules";

type PanelEntry = {
  key: SettingsPanel;
  label: string;
  description: string;
  icon: typeof Zap;
  /** Panels the controller owns cannot be edited against an external daemon. */
  managedOnly?: boolean;
};

const PANELS: readonly PanelEntry[] = [
  { key: "provider", label: "Provider", description: "Model provider and credentials", icon: Zap },
  { key: "router", label: "Model Router", description: "Semantic routing by intent", icon: Shuffle, managedOnly: true },
  { key: "mcp", label: "MCP Gateway", description: "Connect external tools", icon: Plug, managedOnly: true },
  { key: "skills", label: "Skills", description: "Instruction bundles in this workspace", icon: Sparkles },
  { key: "memory", label: "Memory", description: "User model and project notes", icon: BookOpenCheck },
  { key: "schedules", label: "Schedules", description: "Recurring and one-shot runs", icon: Clock3 },
];

/**
 * The profile control: workspace identity, the six configuration panels, and the
 * theme switch. Consolidating the panels here is what empties the top bar — the
 * console's navbar carries only search and this menu, and six always-visible
 * config buttons is not a navigation surface, it is a settings tray.
 *
 * Each row still opens the SAME dialog it always did; this changes where the
 * affordance lives, never what it does.
 */
export function UserMenu({
  workspaceName,
  subLabel,
  connection,
  providerName,
  controllerMode,
  onOpenPanel,
}: {
  workspaceName: string;
  subLabel: string;
  connection: "checking" | "online" | "offline";
  providerName: string;
  controllerMode: "managed" | "external";
  onOpenPanel: (panel: SettingsPanel) => void;
}) {
  const [isOpen, setIsOpen] = useState(false);

  return (
    <DropdownMenu onOpenChange={setIsOpen}>
      <DropdownMenuTrigger asChild>
        <UserMenuButton
          workspaceName={workspaceName}
          subLabel={subLabel}
          isOpen={isOpen}
        />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        className="w-72 overflow-hidden border-0 p-0 shadow-lg"
        align="end"
        sideOffset={8}
      >
        <div className="flex flex-col items-center gap-1 px-6 pt-6 pb-5">
          <UserAvatar name={workspaceName} className="size-14" />
          <div className="mt-2 text-center">
            <p className="text-base font-bold text-foreground">{workspaceName}</p>
            <p className="text-sm text-muted-foreground">{subLabel}</p>
          </div>
          <div className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
            <span
              className={cn(
                "size-2 rounded-full",
                connection === "online"
                  ? "bg-success"
                  : connection === "checking"
                    ? "bg-warning"
                    : "bg-destructive",
              )}
            />
            {connection === "online"
              ? `Connected · ${providerName}`
              : connection === "checking"
                ? "Checking connection"
                : "Mecatl offline"}
          </div>
        </div>

        <DropdownMenuSeparator className="mx-0 my-0" />

        <div className="py-1">
          <DropdownMenuLabel className="px-5 pt-2 pb-1 text-xs font-medium text-muted-foreground">
            Configuration
          </DropdownMenuLabel>
          {PANELS.map(({ key, label, description, icon: Icon, managedOnly }) => {
            const disabled = managedOnly === true && controllerMode === "external";
            return (
              <DropdownMenuItem
                key={key}
                disabled={disabled}
                onSelect={() => onOpenPanel(key)}
                title={disabled ? "Managed by the external deployment" : undefined}
                className="flex cursor-pointer items-center gap-4 rounded-none px-5 py-3 font-medium text-foreground"
              >
                <Icon className="size-5 shrink-0 text-foreground/60" />
                <div className="flex flex-col">
                  <span className="text-sm">{label}</span>
                  <span className="text-xs font-normal text-muted-foreground">
                    {disabled ? "Managed by the external deployment" : description}
                  </span>
                </div>
              </DropdownMenuItem>
            );
          })}
        </div>

        <DropdownMenuSeparator className="mx-0 my-0" />
        <ThemeMenuItems />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
