"use client";

import { Menu, Shuffle } from "lucide-react";
import { useState } from "react";

import { ChatPanel, type ProjectSummary, type TaskSummary } from "@/components/shell/chat-panel";
import { IconRail } from "@/components/shell/icon-rail";
import type { ViewKey } from "@/components/shell/nav-items";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { UserMenu } from "@/components/user-menu";
import { cn } from "@/lib/utils";

/**
 * The top bar over the content column: `h-16 bg-sidebar` with one bottom
 * border, so its seam lines up with the icon rail's brand block and the chat
 * panel's header.
 *
 * Below `md` both navigation levels collapse into the Sheet this opens, since
 * 64px + 288px of chrome leaves nothing for a conversation on a phone.
 */
export function Navbar({
  title,
  running,
  routerStatus,
  onOpenRouter,
  view,
  onNavigate,
  tasks,
  activeId,
  projects,
  connection,
  onSelectTask,
  onNewTask,
  onNewProject,
  onRenameTask,
  onDeleteTask,
  workspaceName,
  workspaceSubLabel,
  providerName,
}: {
  title: string;
  running: boolean;
  routerStatus: { enabled: boolean; categories: number } | null;
  onOpenRouter: () => void;
  view: ViewKey;
  onNavigate: (view: ViewKey) => void;
  tasks: readonly TaskSummary[];
  activeId: string;
  projects: readonly ProjectSummary[];
  connection: "checking" | "online" | "offline";
  onSelectTask: (id: string) => void;
  onNewTask: (projectId?: string) => void;
  onNewProject: () => void;
  onRenameTask: (id: string, title: string) => void;
  onDeleteTask: (id: string) => void;
  workspaceName: string;
  workspaceSubLabel: string;
  providerName: string;
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
          <SheetContent side="left" className="flex w-auto max-w-[92vw] flex-row gap-0 p-0 md:hidden">
            <SheetTitle className="sr-only">Navigation</SheetTitle>
            <IconRail
              view={view}
              onNavigate={(next) => {
                onNavigate(next);
                if (next !== "chat") setDrawerOpen(false);
              }}
            />
            {view === "chat" && (
              <ChatPanel
                tasks={tasks}
                activeId={activeId}
                projects={projects}
                onSelectTask={onSelectTask}
                onNewTask={onNewTask}
                onNewProject={onNewProject}
                onRenameTask={onRenameTask}
                onDeleteTask={onDeleteTask}
                onAfterSelect={() => setDrawerOpen(false)}
                className="w-[min(18rem,60vw)] border-r-0"
              />
            )}
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
      />
    </header>
  );
}
