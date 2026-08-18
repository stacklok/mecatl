"use client";

import { Ellipsis, FolderPlus, Pencil, SquarePen, Trash2 } from "lucide-react";
import { useRef, useState } from "react";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

/**
 * Compact ages, matching the design: `now`, `5m`, `2h`, `3d`. The slot beside a
 * task title is one glyph-pair wide because it shares that cell with the hover
 * affordance, so a spelled-out "Just now" wraps to two lines there.
 */
function compactAge(timestamp: number): string {
  if (timestamp === 0) return "now";
  const seconds = Math.floor((Date.now() - timestamp) / 1000);
  if (seconds < 60) return "now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  return `${Math.floor(days / 7)}w`;
}

export type TaskSummary = {
  id: string;
  title: string;
  updatedAt: number;
  running?: boolean;
  projectId?: string;
  /**
   * Whether the daemon will accept a rename or a delete for this chat. A chat
   * that is running, awaiting an approval, or stored somewhere that cannot be
   * pruned is refused server-side, so the menu says so up front instead of
   * offering an action that will fail. Undefined means a local draft, which this
   * client owns outright.
   */
  canRename?: boolean;
  canDelete?: boolean;
  /** Why the action is unavailable, already phrased for a person. */
  renameReason?: string;
  deleteReason?: string;
};

export type ProjectSummary = { id: string; name: string; workspace: string };

/**
 * The chat-history panel — the inner of the two navigation levels.
 *
 * Deliberately shows ONE section. The design this follows also carries Projects
 * and Agents groups, but Studio has no project container and no agent picker, so
 * those headings would be furniture with nothing behind them.
 */
export function ChatPanel({
  tasks,
  activeId,
  projects,
  onSelectTask,
  onNewTask,
  onNewProject,
  onRenameTask,
  onDeleteTask,
  className,
  onAfterSelect,
}: {
  tasks: readonly TaskSummary[];
  activeId: string;
  projects: readonly ProjectSummary[];
  onSelectTask: (id: string) => void;
  onNewTask: (projectId?: string) => void;
  onNewProject: () => void;
  onRenameTask: (id: string, title: string) => void;
  onDeleteTask: (id: string) => void;
  className?: string;
  onAfterSelect?: () => void;
}) {
  const ordered = [...tasks].sort((a, b) => b.updatedAt - a.updatedAt);
  const forProject = (projectId?: string) =>
    ordered.filter((task) => task.projectId === projectId);
  // Tasks with no project predate the feature (or were started before one
  // existed) and belong to the controller's own root, so they keep a home rather
  // than disappearing from the list.
  const unassigned = forProject(undefined);
  // Which row is in rename mode, and which row's menu is open. Both are row
  // state rather than per-row component state so only one can be active at a
  // time — two open menus or two edit fields would be a bug, not a feature.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [menuId, setMenuId] = useState<string | null>(null);
  const [confirmingId, setConfirmingId] = useState<string | null>(null);
  // Escape has to tell the blur handler not to save. It is a ref, not state,
  // because blur fires in the same tick and must observe the flag synchronously.
  const cancelledRef = useRef(false);

  const beginRename = (id: string) => {
    cancelledRef.current = false;
    setEditingId(id);
  };

  // Undefined is a local draft: nothing server-side can refuse it.
  const allows = (capability?: boolean) => capability !== false;

  /**
   * Blur is the ONLY commit path — Enter and Escape just blur the field.
   * Committing from the key handler as well means two paths racing over one
   * edit, each needing to know whether the other already ran.
   */
  const finishRename = (id: string, value: string) => {
    setEditingId(null);
    if (cancelledRef.current) {
      cancelledRef.current = false;
      return;
    }
    const next = value.trim();
    // An empty title is a slip, not an instruction to erase the label; keep the
    // existing one rather than leaving the row unidentifiable.
    if (next) onRenameTask(id, next);
  };

  const renderRow = (task: TaskSummary) => {
        const isSelected = task.id === activeId;
        const isEditing = task.id === editingId;
        const menuOpen = task.id === menuId;

        return (
          <div
            key={task.id}
            className={cn(
              "group flex items-center border-l-[3px] border-transparent py-2 pr-3 pl-4 transition-colors",
              isSelected ? "border-brand-ink bg-accent" : "hover:bg-accent",
            )}
          >
            {isEditing ? (
              <input
                // Focused imperatively rather than with autoFocus: this is a
                // response to the operator choosing Rename, not a focus grab
                // on mount, and selecting the text means typing replaces the
                // old title instead of appending to it.
                //
                // Guarded on activeElement because an inline ref callback
                // re-runs on every parent render — and the parent re-renders
                // on every streamed chunk, which would re-select the field
                // under the operator mid-keystroke.
                ref={(node) => {
                  if (node && document.activeElement !== node) {
                    node.focus();
                    node.select();
                  }
                }}
                defaultValue={task.title}
                aria-label={`Rename ${task.title}`}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    event.currentTarget.blur();
                  }
                  if (event.key === "Escape") {
                    event.preventDefault();
                    cancelledRef.current = true;
                    event.currentTarget.blur();
                  }
                }}
                onBlur={(event) => finishRename(task.id, event.currentTarget.value)}
                className="min-w-0 flex-1 rounded border border-input bg-background px-1.5 py-0.5 text-[0.85rem] font-medium text-foreground outline-none focus:border-ring"
              />
            ) : (
              <button
                type="button"
                onClick={() => {
                  onSelectTask(task.id);
                  onAfterSelect?.();
                }}
                onDoubleClick={() => beginRename(task.id)}
                className="min-w-0 flex-1 text-left"
              >
                <span
                  className={cn(
                    "block truncate text-[0.85rem] font-medium",
                    isSelected
                      ? "text-brand-ink"
                      : "text-muted-foreground group-hover:text-foreground",
                  )}
                >
                  {task.title || "Untitled"}
                </span>
              </button>
            )}

            {!isEditing && (
              // A single grid cell stacks the status/time and the overflow
              // affordance, so the hover swap cannot reflow the row.
              <div className="ml-2 grid w-8 shrink-0 items-center justify-items-center [grid-template-areas:'slot']">
                {task.running ? (
                  <span
                    className="size-2 rounded-full bg-brand [grid-area:slot]"
                    title="Running"
                  />
                ) : (
                  <span
                    className={cn(
                      "text-xs tabular-nums text-muted-foreground/50 [grid-area:slot]",
                      menuOpen ? "invisible" : "group-hover:invisible",
                    )}
                  >
                    {compactAge(task.updatedAt)}
                  </span>
                )}

                <DropdownMenu
                  open={menuOpen}
                  onOpenChange={(open) => {
                    setMenuId(open ? task.id : null);
                    if (!open) setConfirmingId(null);
                  }}
                >
                  <DropdownMenuTrigger asChild>
                    <button
                      type="button"
                      aria-label={`Actions for ${task.title || "Untitled"}`}
                      className={cn(
                        "flex w-7 items-center justify-center rounded text-muted-foreground [grid-area:slot] hover:text-foreground",
                        // Also revealed on keyboard focus, so the
                        // menu is reachable without a pointer at all.
                        menuOpen
                          ? "opacity-100"
                          : "pointer-events-none opacity-0 focus-visible:pointer-events-auto focus-visible:opacity-100 group-hover:pointer-events-auto group-hover:opacity-100",
                      )}
                    >
                      <Ellipsis className="size-4" />
                    </button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" side="bottom" sideOffset={4} className="w-48">
                    <DropdownMenuItem
                      disabled={!allows(task.canRename)}
                      title={allows(task.canRename) ? undefined : task.renameReason}
                      onSelect={() => beginRename(task.id)}
                      className="cursor-pointer"
                    >
                      <Pencil className="mr-2 size-4 text-muted-foreground" />
                      Rename
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      // A running task is mid-stream into this transcript;
                      // deleting it would leave the run writing to a task the
                      // list no longer has. Stop it first. The daemon refuses
                      // the same case — and others this client cannot see, like
                      // a run owned by another browser — so both gates apply.
                      disabled={task.running || !allows(task.canDelete)}
                      title={allows(task.canDelete) ? undefined : task.deleteReason}
                      // Two-step rather than a dialog: the first select arms
                      // the confirm and keeps the menu open, so a mis-click
                      // cannot destroy a transcript.
                      onSelect={(event) => {
                        if (confirmingId !== task.id) {
                          event.preventDefault();
                          setConfirmingId(task.id);
                          return;
                        }
                        onDeleteTask(task.id);
                        setConfirmingId(null);
                      }}
                      className={cn(
                        "cursor-pointer",
                        confirmingId === task.id && "text-destructive",
                      )}
                    >
                      <Trash2
                        className={cn(
                          "mr-2 size-4",
                          confirmingId === task.id
                            ? "text-destructive"
                            : "text-muted-foreground",
                        )}
                      />
                      {task.running
                        ? "Delete (stop it first)"
                        : !allows(task.canDelete)
                          ? "Delete unavailable"
                          : confirmingId === task.id
                            ? "Click again to delete"
                            : "Delete"}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            )}
          </div>
        );
  };

  const section = (label: string, rows: readonly TaskSummary[], onAdd?: () => void) => (
    <div key={label} className="pb-2">
      <div className="flex items-center gap-1 px-4 pt-3 pb-1">
        <span className="flex-1 truncate text-[0.7rem] font-medium tracking-wider text-muted-foreground uppercase">
          {label}
        </span>
        {onAdd && (
          <button
            type="button"
            onClick={onAdd}
            aria-label={`New task in ${label}`}
            title={`New task in ${label}`}
            className="flex size-5 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <SquarePen className="size-3" />
          </button>
        )}
      </div>
      {rows.length === 0 ? (
        <p className="px-4 py-1.5 text-xs text-muted-foreground/40">No tasks yet</p>
      ) : (
        rows.map(renderRow)
      )}
    </div>
  );

  const listing = (
    <>
      {projects.map((project) =>
        section(project.name, forProject(project.id), () => {
          onNewTask(project.id);
          onAfterSelect?.();
        }),
      )}
      {(unassigned.length > 0 || projects.length === 0) &&
        section(projects.length === 0 ? "Tasks" : "No project", unassigned)}
    </>
  );

  return (
    <div
      className={cn(
        "flex w-72 shrink-0 flex-col border-r border-border bg-background",
        className,
      )}
    >
      <div className="flex h-16 shrink-0 items-center gap-2 border-b border-border px-4">
        <h2 className="flex-1 truncate text-base font-semibold text-foreground">
          Chats
        </h2>
        <button
          type="button"
          onClick={() => {
            onNewTask();
            onAfterSelect?.();
          }}
          aria-label="New task"
          title="New task"
          className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <SquarePen className="size-[18px]" />
        </button>
        <button
          type="button"
          onClick={onNewProject}
          aria-label="New project"
          title="New project — point Mecatl at a folder"
          className="flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          <FolderPlus className="size-[18px]" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto py-3">
        {listing}
      </div>
    </div>
  );
}
