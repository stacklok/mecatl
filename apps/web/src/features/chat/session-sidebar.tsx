// SPDX-License-Identifier: Apache-2.0

import type { SessionSummaryResponse } from "@mecatl-studio/contracts";
import {
  Ellipsis,
  Eye,
  EyeOff,
  FolderInput,
  Menu,
  Pencil,
  Plus,
  SquarePen,
  Trash2,
  X,
} from "lucide-react";
import {
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  useRef,
  useState,
} from "react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { Input } from "../../components/ui/input";
import { formatRelativeTime } from "../../lib/formatters";
import { maxPanelWidth, minPanelWidth, usePanelWidth } from "../../lib/panel-width";
import { capabilityReasonLabel, sessionKindLabel } from "../../lib/session-kinds";
import { cn } from "../../lib/utils";
import { type ChatFolder, type ChatFolderState, groupSessions } from "./chat-folders";
import { CopyDebugTargetMenuItem, CopySessionIdMenuItem } from "./session-copy-menu-items";
import { isInspectOnlySession } from "./session-inspection";
import { ForkChatMenuItem, ViewTranscriptMenuItem } from "./session-row-action-items";

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
  /** Forks a row as-is, without opening it first; absent hides the row's Fork chat. */
  onFork?: (session: SessionSummaryResponse) => void;
  onMoveToFolder: (sessionId: string, folderId?: string) => void;
  onRename: (session: SessionSummaryResponse, title: string) => void;
  onRenameFolder: (folder: ChatFolder) => void;
  onSelect: (id: string) => void;
  /** Opens a row's saved transcript read-only; absent hides the row's View transcript. */
  onViewTranscript?: (session: SessionSummaryResponse) => void;
  open: boolean;
  renamingId?: string;
  selectedId?: string;
  side: "left" | "right";
}

export function SessionSidebar(props: SessionSidebarProps) {
  const chats = props.items.filter((item) => !isInspectOnlySession(item));
  const inspectOnly = props.items.filter(isInspectOnlySession);
  const groups = groupSessions(chats, props.folders);
  if (inspectOnly.length > 0) {
    groups.push({ id: "inspect-only", items: inspectOnly, label: "Inspect-only sessions" });
  }
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
                    group.items.map((session) =>
                      isInspectOnlySession(session) ? (
                        <InspectSessionRow
                          key={session.id}
                          onSelect={() => props.onSelect(session.id)}
                          selected={props.selectedId === session.id}
                          session={session}
                        />
                      ) : (
                        <SessionRow
                          deleting={props.deletingId === session.id}
                          folders={props.folders}
                          key={session.id}
                          onCreateFolder={() => props.onCreateFolder(session.id)}
                          onDelete={() => props.onDelete(session)}
                          onFork={props.onFork && (() => props.onFork?.(session))}
                          onMoveToFolder={(folderId) => props.onMoveToFolder(session.id, folderId)}
                          onRename={(title) => props.onRename(session, title)}
                          onSelect={() => props.onSelect(session.id)}
                          onViewTranscript={
                            props.onViewTranscript && (() => props.onViewTranscript?.(session))
                          }
                          renaming={props.renamingId === session.id}
                          selected={props.selectedId === session.id}
                          session={session}
                        />
                      ),
                    )
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

/** A disabled item's reason: its description, so the item's name stays the action. */
function ReasonLine({ reason }: { reason: string }) {
  return (
    <span aria-hidden="true" className="block truncate text-xs text-muted-foreground">
      {reason}
    </span>
  );
}

function SessionRow({
  deleting,
  folders,
  onCreateFolder,
  onDelete,
  onFork,
  onMoveToFolder,
  onRename,
  onSelect,
  onViewTranscript,
  renaming,
  selected,
  session,
}: {
  deleting: boolean;
  folders: ChatFolderState;
  onCreateFolder: () => void;
  onDelete: () => void;
  onFork?: () => void;
  onMoveToFolder: (folderId?: string) => void;
  onRename: (title: string) => void;
  onSelect: () => void;
  onViewTranscript?: () => void;
  renaming: boolean;
  selected: boolean;
  session: SessionSummaryResponse;
}) {
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(session.title);
  const [menuOpen, setMenuOpen] = useState(false);
  // The inline editor takes focus when Rename closes the menu; the menu must not take it back.
  const keepFocus = useRef(false);
  const current = folders.assignments[session.id];
  const canRename = session.capabilities.rename && !renaming;
  const canDelete = session.capabilities.delete && !deleting;

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
      <DropdownMenu modal={false} onOpenChange={setMenuOpen}>
        <DropdownMenuTrigger asChild>
          <button
            aria-label={`Options for chat: ${session.title}`}
            className={cn(
              "flex size-7 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-background hover:text-foreground",
              menuOpen
                ? "opacity-100"
                : "opacity-100 min-[760px]:opacity-0 min-[760px]:group-hover:opacity-100 min-[760px]:group-focus-within:opacity-100",
            )}
            type="button"
          >
            <Ellipsis aria-hidden="true" className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="end"
          className="w-56"
          onCloseAutoFocus={(event) => {
            if (!keepFocus.current) return;
            keepFocus.current = false;
            event.preventDefault();
          }}
          side="bottom"
          sideOffset={4}
        >
          <CopySessionIdMenuItem session={session} />
          <CopyDebugTargetMenuItem session={session} />
          {onViewTranscript && (
            <ViewTranscriptMenuItem onSelect={onViewTranscript} session={session} />
          )}
          {onFork && <ForkChatMenuItem onSelect={onFork} session={session} />}
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <FolderInput aria-hidden="true" />
              Move to folder
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="w-52">
              {folders.folders.map((folder) => (
                <DropdownMenuCheckboxItem
                  checked={current === folder.id}
                  key={folder.id}
                  onSelect={() => onMoveToFolder(folder.id)}
                >
                  <span className="truncate">{folder.name}</span>
                </DropdownMenuCheckboxItem>
              ))}
              <DropdownMenuCheckboxItem checked={!current} onSelect={() => onMoveToFolder()}>
                No folder
              </DropdownMenuCheckboxItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={onCreateFolder}>
                <Plus aria-hidden="true" />
                New folder…
              </DropdownMenuItem>
            </DropdownMenuSubContent>
          </DropdownMenuSub>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            aria-description={
              !session.capabilities.rename ? session.capabilities.renameReason : undefined
            }
            disabled={!canRename}
            onSelect={() => {
              keepFocus.current = true;
              setTitle(session.title);
              setEditing(true);
            }}
            title={session.capabilities.renameReason || undefined}
          >
            <Pencil aria-hidden="true" />
            <span className="min-w-0">
              Rename
              {!session.capabilities.rename && session.capabilities.renameReason && (
                <ReasonLine reason={capabilityReasonLabel(session.capabilities.renameReason)} />
              )}
            </span>
          </DropdownMenuItem>
          <DropdownMenuItem
            aria-description={
              !session.capabilities.delete ? session.capabilities.deleteReason : undefined
            }
            className="text-destructive focus:text-destructive"
            disabled={!canDelete}
            onSelect={onDelete}
            title={session.capabilities.deleteReason || undefined}
          >
            <Trash2 aria-hidden="true" />
            <span className="min-w-0">
              Delete
              {!session.capabilities.delete && session.capabilities.deleteReason && (
                <ReasonLine reason={capabilityReasonLabel(session.capabilities.deleteReason)} />
              )}
            </span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

/**
 * One inspect-only row (a subagent, parallel branch, team member, scheduled
 * fire, or unknown kind), ported from the prototype's
 * `inspect-session-list.tsx`: the title, a kind chip, a Read-only badge
 * whose tooltip carries the daemon's reason, and the relative time. It has
 * no chat actions; opening it shows the session read-only with its details.
 */
function InspectSessionRow({
  onSelect,
  selected,
  session,
}: {
  onSelect: () => void;
  selected: boolean;
  session: SessionSummaryResponse;
}) {
  const title = session.title || sessionKindLabel(session.kind);
  const readOnlyReason =
    capabilityReasonLabel(session.capabilities.publicChatReason) || "Read-only run";
  const transcriptDenied = !session.capabilities.viewTranscript;
  const updated = Date.parse(session.updatedAt);
  const Icon = transcriptDenied ? EyeOff : Eye;
  return (
    <div
      className={cn(
        "group my-0.5 flex items-center rounded-lg border-l-2 pr-2 transition-colors",
        selected ? "border-brand-ink bg-brand/10" : "border-transparent hover:bg-accent",
      )}
    >
      <button
        aria-current={selected ? "page" : undefined}
        aria-label={`Inspect run: ${title}`}
        className="min-w-0 flex-1 px-2.5 py-2 text-left"
        onClick={onSelect}
        title={
          transcriptDenied
            ? capabilityReasonLabel(session.capabilities.viewTranscriptReason) ||
              "Transcript unavailable"
            : "Open read-only"
        }
        type="button"
      >
        <span className="flex min-w-0 items-center gap-1.5">
          <Icon aria-hidden="true" className="size-3 shrink-0 text-muted-foreground/60" />
          <span
            className={cn(
              "block truncate text-sm font-medium",
              selected ? "text-foreground" : "text-muted-foreground group-hover:text-foreground",
            )}
          >
            {title}
          </span>
        </span>
        <span className="mt-0.5 flex min-w-0 items-center gap-1.5 pl-[18px]">
          <Badge
            className="h-4 shrink-0 px-1.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground"
            variant="outline"
          >
            {sessionKindLabel(session.kind)}
          </Badge>
          <Badge
            className="h-4 shrink-0 px-1.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground"
            title={readOnlyReason}
            variant="outline"
          >
            Read-only
          </Badge>
        </span>
      </button>
      {!Number.isNaN(updated) && (
        <span className="ml-1 shrink-0 text-xs tabular-nums text-muted-foreground/60">
          {formatRelativeTime(updated)}
        </span>
      )}
    </div>
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
