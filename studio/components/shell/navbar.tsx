"use client";

import { Menu, Shuffle } from "lucide-react";
import { useState } from "react";

import { TaskSidebar, type TaskSummary } from "@/components/shell/task-sidebar";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { UserMenu, type SettingsPanel } from "@/components/user-menu";
import { cn } from "@/lib/utils";

/**
 * The top bar. Matches the console's navbar exactly — `h-16`, `bg-sidebar`, one
 * bottom border, so its seam lines up with the sidebar's wordmark block.
 *
 * It carries the task heading and the profile menu, and NOT a row of
 * configuration buttons: those moved into the profile menu, which is where the
 * console keeps everything of that kind.
 */
export function Navbar({
  title,
  running,
  routerStatus,
  onOpenRouter,
  tasks,
  activeId,
  connection,
  relativeTime,
  onSelectTask,
  onNewTask,
  workspaceName,
  workspaceSubLabel,
  providerName,
  controllerMode,
  onOpenPanel,
}: {
  title: string;
  running: boolean;
  routerStatus: { enabled: boolean; categories: number } | null;
  onOpenRouter: () => void;
  tasks: readonly TaskSummary[];
  activeId: string;
  connection: "checking" | "online" | "offline";
  relativeTime: (timestamp: number) => string;
  onSelectTask: (id: string) => void;
  onNewTask: () => void;
  workspaceName: string;
  workspaceSubLabel: string;
  providerName: string;
  controllerMode: "managed" | "external";
  onOpenPanel: (panel: SettingsPanel) => void;
}) {
  const [drawerOpen, setDrawerOpen] = useState(false);

  return (
    <header className="flex h-16 shrink-0 items-center justify-between gap-3 border-b border-border bg-sidebar px-4 sm:px-6">
      <div className="flex min-w-0 items-center gap-2 sm:gap-3">
        <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
          <SheetTrigger
            className="flex size-9 items-center justify-center rounded-md text-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring md:hidden"
            aria-label="Open navigation"
          >
            <Menu className="size-5" />
          </SheetTrigger>
          <SheetContent side="left" className="w-64 max-w-[80vw] gap-0 p-0 md:hidden">
            <SheetTitle className="sr-only">Tasks</SheetTitle>
            <TaskSidebar
              tasks={tasks}
              activeId={activeId}
              connection={connection}
              relativeTime={relativeTime}
              onSelect={onSelectTask}
              onNewTask={onNewTask}
              onNavigate={() => setDrawerOpen(false)}
              className="h-full w-full border-r-0"
            />
          </SheetContent>
        </Sheet>

        <div className="flex min-w-0 items-center gap-2.5">
          <h1 className="truncate text-[0.95rem] font-semibold text-foreground">
            {title}
          </h1>
          <span
            className={cn(
              "shrink-0 rounded-full px-2 py-0.5 text-[0.7rem] font-medium",
              running
                ? "bg-brand/15 text-brand-ink"
                : "bg-muted text-muted-foreground",
            )}
          >
            {running ? "Working" : "Local"}
          </span>
          {routerStatus && (
            <button
              type="button"
              onClick={onOpenRouter}
              title="Open semantic model routing settings"
              className={cn(
                "hidden shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[0.7rem] font-medium transition-colors sm:inline-flex",
                routerStatus.enabled
                  ? "bg-info/15 text-info hover:bg-info/25"
                  : "bg-muted text-muted-foreground hover:bg-accent",
              )}
            >
              <Shuffle className="size-3" />
              {routerStatus.enabled
                ? `Routing on · ${routerStatus.categories} tiers`
                : "Routing off"}
            </button>
          )}
        </div>
      </div>

      <UserMenu
        workspaceName={workspaceName}
        subLabel={workspaceSubLabel}
        connection={connection}
        providerName={providerName}
        controllerMode={controllerMode}
        onOpenPanel={onOpenPanel}
      />
    </header>
  );
}
