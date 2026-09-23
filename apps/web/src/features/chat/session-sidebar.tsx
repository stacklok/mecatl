// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import { Check, FolderInput, Menu, Pencil, Plus, SquarePen, Trash2, X } from "lucide-react";
import {
  type CSSProperties,
  type MouseEvent,
  type PointerEvent as ReactPointerEvent,
  useState,
} from "react";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { maxPanelWidth, minPanelWidth, usePanelWidth } from "../../lib/panel-width";
import { cn } from "../../lib/utils";
import { type ChatFolder, type ChatFolderState, groupSessions } from "./chat-folders";

interface SessionSidebarProps {
  collapsed: boolean;
  creating: boolean;
  deletingId?: string;
  folders: ChatFolderState;
  items: SessionSummaryResponse[];
  onClose: () => void;
  onCreate: () => void;
  onCreateFolder: (sessionId: string) => void;
  onDelete: (session: SessionSummaryResponse) => void;
  onDeleteFolder: (folder: ChatFolder) => void;
  onMoveToFolder: (sessionId: string, folderId?: string) => void;
  onRename: (session: SessionSummaryResponse, title: string) => void;
  onRenameFolder: (folder: ChatFolder) => void;
  onSelect: (id: string) => void;
  open: boolean;
  renamingId?: string;
  selectedId?: string;
  side: "left" | "right";
}

export function SessionSidebar(props: SessionSidebarProps) {
  const groups = groupSessions(props.items, props.folders);
  const width = usePanelWidth("chatList");

  function startResize(event: ReactPointerEvent<HTMLButtonElement>) {
    event.currentTarget.focus();
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = width.value;
    const direction = props.side === "left" ? 1 : -1;
    const resize = (moveEvent: PointerEvent) =>
      width.setValue(startWidth + (moveEvent.clientX - startX) * direction);
    const finish = () => {
      window.removeEventListener("pointermove", resize);
      window.removeEventListener("pointerup", finish);
    };
    window.addEventListener("pointermove", resize);
    window.addEventListener("pointerup", finish);
  }

  return (
    <>
      {props.open && (
        <button
          aria-label="Close chats"
          className="absolute inset-0 z-20 bg-black/30 min-[760px]:hidden"
          onClick={props.onClose}
          type="button"
        />
      )}
      <aside
        className={cn(
          "absolute inset-y-0 left-0 z-30 flex w-[min(84vw,18rem)] flex-col border-r bg-card transition-[transform,width] min-[760px]:relative min-[760px]:z-auto min-[760px]:w-[var(--session-list-width)]",
          props.side === "right" &&
            "min-[760px]:order-2 min-[760px]:border-l min-[760px]:border-r-0",
          props.open ? "translate-x-0" : "max-[759px]:-translate-x-full",
          props.collapsed &&
            (props.side === "right"
              ? "min-[760px]:pointer-events-none min-[760px]:w-0 min-[760px]:translate-x-full min-[760px]:overflow-hidden min-[760px]:border-l-0"
              : "min-[760px]:pointer-events-none min-[760px]:w-0 min-[760px]:-translate-x-full min-[760px]:overflow-hidden min-[760px]:border-r-0"),
        )}
        style={{ "--session-list-width": `${width.value}px` } as CSSProperties}
      >
        <button
          aria-label="Resize chat list"
          className={cn(
            "absolute inset-y-0 z-10 hidden w-2 cursor-col-resize touch-none border-0 bg-transparent p-0 hover:bg-brand/20 min-[760px]:block",
            props.side === "right" ? "-left-1" : "-right-1",
          )}
          onKeyDown={(event) => {
            if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
            event.preventDefault();
            const physicalDirection = event.key === "ArrowRight" ? 1 : -1;
            width.setValue(width.value + physicalDirection * (props.side === "left" ? 12 : -12));
          }}
          onPointerDown={startResize}
          title={`Resize chat list (${minPanelWidth}–${maxPanelWidth}px)`}
          type="button"
        />
        <div className="flex h-16 items-center justify-between gap-3 border-b px-4">
          <span className="font-semibold">Chat History</span>
          <div className="flex items-center gap-1">
            <Button
              aria-label="New chat"
              disabled={props.creating}
              onClick={props.onCreate}
              size="icon"
              title="New chat"
              variant="ghost"
            >
              <SquarePen aria-hidden="true" />
            </Button>
            <Button
              aria-label="Close chats"
              className="min-[760px]:hidden"
              onClick={props.onClose}
              size="icon"
              variant="ghost"
            >
              <X aria-hidden="true" />
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-2 pt-3 pb-3">
          {props.items.length === 0 && props.folders.folders.length === 0 ? (
            <p className="px-3 py-8 text-center text-sm text-muted-foreground">No chats yet</p>
          ) : (
            groups.map((group) => {
              const folder = group.id.startsWith("folder:")
                ? props.folders.folders.find(
                    (candidate) => candidate.id === group.id.slice("folder:".length),
                  )
                : undefined;
              return (
                <section className="pb-3" key={group.id}>
                  <div className="flex h-7 items-center gap-1 px-2">
                    <h2 className="min-w-0 flex-1 truncate text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      {group.label}
                    </h2>
                    {folder && (
                      <>
                        <button
                          aria-label={`Rename folder ${folder.name}`}
                          className="rounded-full p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
                          onClick={() => props.onRenameFolder(folder)}
                          type="button"
                        >
                          <Pencil aria-hidden="true" className="size-3" />
                        </button>
                        <button
                          aria-label={`Delete folder ${folder.name}`}
                          className="rounded-full p-1 text-muted-foreground hover:bg-accent hover:text-destructive"
                          onClick={() => props.onDeleteFolder(folder)}
                          type="button"
                        >
                          <Trash2 aria-hidden="true" className="size-3" />
                        </button>
                      </>
                    )}
                  </div>
                  {group.items.length === 0 ? (
                    <p className="px-3 py-2 text-xs text-muted-foreground">
                      No chats in this folder
                    </p>
                  ) : (
                    group.items.map((session) => (
                      <SessionRow
                        deleting={props.deletingId === session.id}
                        folders={props.folders}
                        key={session.id}
                        onCreateFolder={() => props.onCreateFolder(session.id)}
                        onDelete={() => props.onDelete(session)}
                        onMoveToFolder={(folderId) => props.onMoveToFolder(session.id, folderId)}
                        onRename={(title) => props.onRename(session, title)}
                        onSelect={() => props.onSelect(session.id)}
                        renaming={props.renamingId === session.id}
                        selected={props.selectedId === session.id}
                        session={session}
                      />
                    ))
                  )}
                </section>
              );
            })
          )}
        </div>
      </aside>
    </>
  );
}

function SessionRow({
  deleting,
  folders,
  onCreateFolder,
  onDelete,
  onMoveToFolder,
  onRename,
  onSelect,
  renaming,
  selected,
  session,
}: {
  deleting: boolean;
  folders: ChatFolderState;
  onCreateFolder: () => void;
  onDelete: () => void;
  onMoveToFolder: (folderId?: string) => void;
  onRename: (title: string) => void;
  onSelect: () => void;
  renaming: boolean;
  selected: boolean;
  session: SessionSummaryResponse;
}) {
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(session.title);

  if (editing) {
    return (
      <form
        className="my-1 flex items-center gap-1 px-1"
        onSubmit={(event) => {
          event.preventDefault();
          if (title.trim()) {
            onRename(title.trim());
            setEditing(false);
          }
        }}
      >
        <Input
          aria-label="Chat title"
          autoFocus
          className="h-8"
          disabled={renaming}
          onBlur={() => setEditing(false)}
          onChange={(event) => setTitle(event.target.value)}
          value={title}
        />
      </form>
    );
  }

  return (
    <div
      className={cn(
        "group relative my-0.5 flex items-center rounded-lg border-l-2 pr-1 transition-colors",
        selected ? "border-brand-ink bg-brand/10" : "border-transparent hover:bg-accent",
      )}
    >
      <button
        aria-current={selected ? "page" : undefined}
        className="min-w-0 flex-1 truncate px-2.5 py-2 text-left text-sm"
        onClick={onSelect}
        type="button"
      >
        <span className="block truncate font-medium">{session.title}</span>
        <span className="mt-0.5 block truncate text-xs text-muted-foreground">
          {session.state || "idle"}
        </span>
      </button>
      <div className="flex opacity-100 min-[760px]:opacity-0 min-[760px]:group-hover:opacity-100 min-[760px]:group-focus-within:opacity-100">
        <FolderPicker
          folders={folders}
          onCreate={onCreateFolder}
          onMove={onMoveToFolder}
          session={session}
        />
        <button
          aria-label={`Rename ${session.title}`}
          className="rounded-full p-1.5 text-muted-foreground hover:bg-background hover:text-foreground disabled:opacity-40"
          disabled={!session.capabilities.rename || renaming}
          onClick={() => setEditing(true)}
          title={session.capabilities.renameReason || "Rename chat"}
          type="button"
        >
          <Pencil aria-hidden="true" className="size-3.5" />
        </button>
        <button
          aria-label={`Delete ${session.title}`}
          className="rounded-full p-1.5 text-muted-foreground hover:bg-background hover:text-destructive disabled:opacity-40"
          disabled={!session.capabilities.delete || deleting}
          onClick={onDelete}
          title={session.capabilities.deleteReason || "Delete chat"}
          type="button"
        >
          <Trash2 aria-hidden="true" className="size-3.5" />
        </button>
      </div>
    </div>
  );
}

function FolderPicker({
  folders,
  onCreate,
  onMove,
  session,
}: {
  folders: ChatFolderState;
  onCreate: () => void;
  onMove: (folderId?: string) => void;
  session: SessionSummaryResponse;
}) {
  const current = folders.assignments[session.id];
  const choose = (event: MouseEvent<HTMLButtonElement>, folderId?: string) => {
    onMove(folderId);
    event.currentTarget.closest("details")?.removeAttribute("open");
  };

  return (
    <details className="relative">
      <summary
        aria-label={`Move ${session.title} to folder`}
        className="flex cursor-pointer list-none rounded-full p-1.5 text-muted-foreground hover:bg-background hover:text-foreground"
        title="Move to folder"
      >
        <FolderInput aria-hidden="true" className="size-3.5" />
      </summary>
      <div className="absolute right-0 top-8 z-50 w-52 rounded-xl border bg-popover p-1 text-popover-foreground shadow-xl">
        <p className="px-2 py-1.5 text-xs font-medium text-muted-foreground">Move to folder</p>
        {folders.folders.map((folder) => (
          <button
            className="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm hover:bg-accent"
            key={folder.id}
            onClick={(event) => choose(event, folder.id)}
            type="button"
          >
            <Check
              aria-hidden="true"
              className={cn("size-3.5", current !== folder.id && "invisible")}
            />
            <span className="truncate">{folder.name}</span>
          </button>
        ))}
        <button
          className="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm hover:bg-accent"
          onClick={(event) => choose(event)}
          type="button"
        >
          <Check aria-hidden="true" className={cn("size-3.5", current && "invisible")} />
          No folder
        </button>
        <div className="my-1 border-t" />
        <button
          className="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm hover:bg-accent"
          onClick={(event) => {
            event.currentTarget.closest("details")?.removeAttribute("open");
            onCreate();
          }}
          type="button"
        >
          <Plus aria-hidden="true" className="size-3.5" />
          New folder…
        </button>
      </div>
    </details>
  );
}

export function ChatsMenuButton({
  onClick,
  showOnDesktop,
}: {
  onClick: () => void;
  showOnDesktop: boolean;
}) {
  return (
    <Button
      aria-label="Open chats"
      className={showOnDesktop ? undefined : "min-[760px]:hidden"}
      onClick={onClick}
      size="icon"
      variant="ghost"
    >
      <Menu aria-hidden="true" />
    </Button>
  );
}
