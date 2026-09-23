// SPDX-License-Identifier: Apache-2.0

import { Outlet } from "@tanstack/react-router";
import { ShortcutProvider } from "../../features/shortcuts/shortcut-provider";
import { TooltipProvider } from "../ui/tooltip";
import { ConnectionStatusBanner } from "./connection-status-banner";
import { TopNav } from "./top-nav";

export function WorkspaceShell() {
  return (
    <ShortcutProvider>
      <TooltipProvider>
        <div className="flex h-dvh min-w-0 flex-col bg-[radial-gradient(120%_140%_at_20%_30%,var(--shell-gradient-start)_0%,var(--shell-gradient-mid)_50%,var(--shell-gradient-end)_100%)] pt-[env(safe-area-inset-top)]">
          <ConnectionStatusBanner />
          <TopNav />
          <main className="relative min-h-0 flex-1 overflow-hidden rounded-t-xl bg-background text-foreground min-[500px]:mx-3 min-[500px]:mb-3 min-[500px]:rounded-[20px]">
            <Outlet />
          </main>
        </div>
      </TooltipProvider>
    </ShortcutProvider>
  );
}
