"use client";

import { useState } from "react";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

import { FontScaleMenuItems } from "./font-scale-menu-items";
import { ThemeMenuItems } from "./theme-menu-items";
import { UserAvatar } from "./user-avatar";
import { UserMenuButton } from "./user-menu-button";

/**
 * The profile control: workspace identity, connection state, and the two
 * appearance preferences.
 *
 * The configuration panels are NOT here — they are left-rail destinations, so
 * this menu is only what a profile menu should be: who you are and how the app
 * looks. Both preferences are browser-local and affect nothing on the daemon.
 */
export function UserMenu({
  workspaceName,
  subLabel,
  connection,
  providerName,
}: {
  workspaceName: string;
  subLabel: string;
  connection: "checking" | "online" | "offline";
  providerName: string;
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
        <ThemeMenuItems />
        <DropdownMenuSeparator className="mx-0 my-0" />
        <FontScaleMenuItems />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
