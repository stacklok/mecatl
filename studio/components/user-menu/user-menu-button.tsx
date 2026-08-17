"use client";

import { forwardRef } from "react";

import { Button } from "@/components/ui/button";
import { ChevronIndicator } from "@/components/ui/chevron-indicator";
import { cn } from "@/lib/utils";

import { UserAvatar } from "./user-avatar";

// The console's pill trigger: avatar-only under `md`, stacked identity plus a
// chevron from `md` up. Studio's "identity" is the WORKSPACE it is pointed at —
// the honest analogue in a local, unauthenticated app, and the fact an operator
// most needs confirmed before sending a mutating prompt.
export const UserMenuButton = forwardRef<
  HTMLButtonElement,
  {
    workspaceName: string;
    subLabel: string;
    isOpen: boolean;
  } & React.ComponentPropsWithoutRef<typeof Button>
>(({ workspaceName, subLabel, isOpen, ...props }, ref) => (
  <Button
    ref={ref}
    variant="ghost"
    className={cn(
      "flex h-11 cursor-pointer items-center gap-2 rounded-full px-1.5 text-foreground md:pr-2.5",
      isOpen
        ? "bg-accent text-accent-foreground"
        : "hover:bg-accent hover:text-accent-foreground",
    )}
    aria-label={`Workspace and settings — ${workspaceName}`}
    {...props}
  >
    <UserAvatar name={workspaceName} />
    <span className="hidden flex-col items-start leading-tight md:flex">
      <span className="text-sm font-medium">{workspaceName}</span>
      <span className="text-xs font-normal text-muted-foreground">{subLabel}</span>
    </span>
    <span className="hidden text-muted-foreground md:inline-flex">
      <ChevronIndicator isOpen={isOpen} />
    </span>
  </Button>
));

UserMenuButton.displayName = "UserMenuButton";
