"use client";

import {
  Bot,
  Bug,
  ChevronDown,
  ChevronUp,
  Ellipsis,
  FolderClosed,
  FolderOpen,
  Pencil,
  Trash2,
} from "lucide-react";
import { useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import type { AgentSession, RosterAgent } from "@/features/agent";
import {
  MOCK_PROJECTS,
  MOCK_PROJECTS_DEFAULT_OPEN_ID,
  type MockProject,
  type MockProjectChat,
} from "@/features/agent/mock-projects";
import { isMockTourSession } from "@/features/agent/mock-tour";
import { formatRelativeTime } from "@/lib/formatters";
import { cn } from "@/lib/utils";

export interface SessionActions {
  onRename: (id: string) => void;
  onDelete: (id: string) => void;
  /**
   * "Debug with AI" (ADR 0254): present only when the daemon reports
   * `capabilities.session_debug` — the caller gates it, the menus render it.
   * The handler owns the consent dialog; a row that already IS a debug
   * session never offers it (no debugging the debugger).
   */
  onDebug?: (id: string) => void;
}

export function SidebarGroup({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col pb-3">
      <span className="mb-1 truncate px-4 py-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      {children}
    </div>
  );
}

function SessionContextMenu({
  session,
  actions,
  children,
  onOpenChange,
}: {
  session: AgentSession;
  actions: SessionActions;
  children: React.ReactNode;
  onOpenChange?: (open: boolean) => void;
}) {
  // Eligibility comes from the daemon row's capabilities; an omitted
  // capability is a denial. A disabled item shows the daemon's reason inline
  // (disabled menu items swallow pointer events, so a tooltip can't open).
  const canRename = session.canRename === true;
  const canDelete = session.canDelete === true;
  const offerDebug =
    actions.onDebug !== undefined &&
    !session.debugTargetSessionId &&
    !isMockTourSession(session.id);

  return (
    <DropdownMenu modal={false} onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>{children}</DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        side="bottom"
        sideOffset={4}
        className="w-56"
      >
        {offerDebug && (
          <DropdownMenuItem onClick={() => actions.onDebug?.(session.id)}>
            <Bug className="size-4 mr-2 shrink-0 text-muted-foreground" />
            <span className="min-w-0">Debug with AI</span>
          </DropdownMenuItem>
        )}
        <DropdownMenuItem
          disabled={!canRename}
          onClick={() => actions.onRename(session.id)}
          title={!canRename ? session.renameReason : undefined}
        >
          <Pencil className="size-4 mr-2 shrink-0 text-muted-foreground" />
          <span className="min-w-0">
            Rename
            {!canRename && session.renameReason && (
              <span className="block truncate text-xs text-muted-foreground">
                {session.renameReason}
              </span>
            )}
          </span>
        </DropdownMenuItem>
        <DropdownMenuItem
          disabled={!canDelete}
          onClick={() => actions.onDelete(session.id)}
          title={!canDelete ? session.deleteReason : undefined}
        >
          <Trash2 className="size-4 mr-2 shrink-0" />
          <span className="min-w-0">
            Delete
            {!canDelete && session.deleteReason && (
              <span className="block truncate text-xs text-muted-foreground">
                {session.deleteReason}
              </span>
            )}
          </span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * Touch long-press detection for a row. Mouse pointers are ignored (desktop
 * has the hover "…" menu); a hold of ~450ms without moving past the slop
 * fires, and the click that follows the release is swallowed by the caller
 * via `firedRef`. Android's native long-press contextmenu is suppressed for
 * touch so the sheet is the one menu.
 */
function useLongPress(onLongPress: () => void) {
  const firedRef = useRef(false);
  const state = useRef<{
    timer: ReturnType<typeof setTimeout> | null;
    startX: number;
    startY: number;
    touch: boolean;
  }>({ timer: null, startX: 0, startY: 0, touch: false });

  const clear = () => {
    if (state.current.timer) {
      clearTimeout(state.current.timer);
      state.current.timer = null;
    }
  };

  const handlers = {
    onPointerDown: (event: React.PointerEvent) => {
      state.current.touch = event.pointerType !== "mouse";
      if (!state.current.touch) return;
      firedRef.current = false;
      state.current.startX = event.clientX;
      state.current.startY = event.clientY;
      clear();
      state.current.timer = setTimeout(() => {
        state.current.timer = null;
        firedRef.current = true;
        onLongPress();
      }, 450);
    },
    onPointerMove: (event: React.PointerEvent) => {
      if (
        state.current.timer &&
        Math.hypot(
          event.clientX - state.current.startX,
          event.clientY - state.current.startY,
        ) > 10
      ) {
        clear();
      }
    },
    onPointerUp: clear,
    onPointerCancel: clear,
    onPointerLeave: clear,
    onContextMenu: (event: React.MouseEvent) => {
      if (state.current.touch) event.preventDefault();
    },
  };

  return { firedRef, handlers };
}

/** The long-press bottom sheet: the same rename/delete actions as the
 *  hover "…" menu, honoring the daemon row's capabilities and reasons. */
function SessionActionsSheet({
  session,
  actions,
  onClose,
}: {
  session: AgentSession;
  actions: SessionActions;
  onClose: () => void;
}) {
  const canRename = session.canRename === true;
  const canDelete = session.canDelete === true;
  const offerDebug =
    actions.onDebug !== undefined &&
    !session.debugTargetSessionId &&
    !isMockTourSession(session.id);
  const row =
    "flex w-full items-center gap-3 px-4 py-3 text-sm transition-colors hover:bg-muted/50 disabled:opacity-50";

  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <SheetContent side="bottom" className="p-0">
        <SheetTitle className="sr-only">Chat options</SheetTitle>
        <div className="py-2">
          <p className="truncate px-4 pt-1 pb-2 text-sm font-semibold">
            {session.title || "Untitled"}
          </p>
          {offerDebug && (
            <button
              type="button"
              className={row}
              onClick={() => {
                onClose();
                actions.onDebug?.(session.id);
              }}
            >
              <Bug className="size-4 shrink-0 text-muted-foreground" />
              <span className="min-w-0 text-left">Debug with AI</span>
            </button>
          )}
          <button
            type="button"
            className={row}
            disabled={!canRename}
            onClick={() => {
              onClose();
              actions.onRename(session.id);
            }}
          >
            <Pencil className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 text-left">
              Rename
              {!canRename && session.renameReason && (
                <span className="block truncate text-xs text-muted-foreground">
                  {session.renameReason}
                </span>
              )}
            </span>
          </button>
          <button
            type="button"
            className={row}
            disabled={!canDelete}
            onClick={() => {
              onClose();
              actions.onDelete(session.id);
            }}
          >
            <Trash2 className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 text-left">
              Delete
              {!canDelete && session.deleteReason && (
                <span className="block truncate text-xs text-muted-foreground">
                  {session.deleteReason}
                </span>
              )}
            </span>
          </button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function SessionRow({
  session,
  isSelected,
  onSelect,
  actions,
}: {
  session: AgentSession;
  isSelected: boolean;
  onSelect: (id: string) => void;
  actions: SessionActions;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const [sheetOpen, setSheetOpen] = useState(false);
  const isRunning = session.isStreaming || session.state === "running";
  const longPress = useLongPress(() => setSheetOpen(true));

  return (
    <div
      className={cn(
        "group flex items-center border-l-[3px] py-2 pr-3 lg:pr-2 pl-3 transition-colors",
        isSelected
          ? "border-brand bg-brand/10"
          : "border-transparent hover:bg-accent",
      )}
    >
      <button
        type="button"
        onClick={() => {
          // A click that ends a long-press is the release, not a selection.
          if (longPress.firedRef.current) {
            longPress.firedRef.current = false;
            return;
          }
          onSelect(session.id);
        }}
        onDoubleClick={
          session.canRename === true
            ? () => actions.onRename(session.id)
            : undefined
        }
        {...longPress.handlers}
        aria-current={isSelected ? "true" : undefined}
        aria-label={`Open chat: ${session.title || "Untitled"}${
          isRunning ? " (running)" : ""
        }`}
        className="flex-1 min-w-0 select-none text-left [-webkit-touch-callout:none]"
      >
        <span className="flex min-w-0 items-center gap-1.5">
          <span
            className={cn(
              "truncate text-[0.85rem] block select-none font-medium",
              isSelected
                ? "text-brand-ink"
                : "text-muted-foreground group-hover:text-foreground",
            )}
          >
            {session.title || "Untitled"}
          </span>
          {isMockTourSession(session.id) && (
            <Badge
              variant="outline"
              className="h-4 shrink-0 px-1.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground"
            >
              Mock
            </Badge>
          )}
          {/* An AI-debug session (ADR 0254) reads as an ordinary chat except
              for this label — the relationship is the daemon's signal. */}
          {session.debugTargetSessionId && (
            <Badge
              variant="outline"
              className="h-4 shrink-0 px-1.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground"
              title={`Debugging session ${session.debugTargetSessionId}`}
            >
              Debug
            </Badge>
          )}
        </span>
      </button>
      <div className="shrink-0 ml-2 grid w-8 items-center justify-items-center [grid-template-areas:'slot']">
        {isRunning ? (
          <span
            role="img"
            aria-label="Running"
            className={cn(
              "[grid-area:slot] size-2 rounded-full bg-brand animate-pulse",
              menuOpen
                ? "min-[500px]:invisible"
                : "min-[500px]:group-hover:invisible",
            )}
          />
        ) : (
          <span
            suppressHydrationWarning
            className={cn(
              "[grid-area:slot] text-xs text-muted-foreground/50 tabular-nums",
              menuOpen
                ? "min-[500px]:invisible"
                : "min-[500px]:group-hover:invisible",
            )}
          >
            {formatRelativeTime(session.updatedAt)}
          </span>
        )}
        <SessionContextMenu
          session={session}
          actions={actions}
          onOpenChange={setMenuOpen}
        >
          <button
            type="button"
            aria-label="Chat options"
            className={cn(
              "[grid-area:slot] flex items-center justify-center w-7 rounded text-muted-foreground hover:text-foreground",
              menuOpen
                ? "opacity-100"
                : "opacity-0 pointer-events-none min-[500px]:group-hover:opacity-100 min-[500px]:group-hover:pointer-events-auto",
            )}
            onClick={(e) => e.stopPropagation()}
          >
            <Ellipsis className="size-4" />
          </button>
        </SessionContextMenu>
      </div>
      {sheetOpen && (
        <SessionActionsSheet
          session={session}
          actions={actions}
          onClose={() => setSheetOpen(false)}
        />
      )}
    </div>
  );
}

export function SessionList({
  sessions,
  selectedId,
  onSelect,
  actions,
  cap = 8,
}: {
  sessions: AgentSession[];
  selectedId: string;
  onSelect: (id: string) => void;
  actions: SessionActions;
  /** Rows shown before the Show-more expander (which reveals ALL rows). */
  cap?: number;
}) {
  const [showAll, setShowAll] = useState(false);
  const visible = showAll ? sessions : sessions.slice(0, cap);

  return (
    <div className="flex flex-col">
      {visible.map((session) => (
        <SessionRow
          key={session.id}
          session={session}
          isSelected={session.id === selectedId}
          onSelect={onSelect}
          actions={actions}
        />
      ))}
      {sessions.length > cap && (
        <button
          type="button"
          onClick={() => setShowAll((v) => !v)}
          className="flex w-full items-center gap-2 border-l-[3px] border-transparent py-2 pr-3 pl-3 text-left text-[0.85rem] font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          {showAll ? (
            <ChevronUp className="size-4 shrink-0" />
          ) : (
            <ChevronDown className="size-4 shrink-0" />
          )}
          {showAll ? "Show less" : "Show more"}
        </button>
      )}
    </div>
  );
}

/**
 * The def color hint is frontmatter relayed verbatim, so only a small safe
 * subset is honored as a CSS color: common named colors or a hex literal.
 * Anything else falls back to the default muted tint.
 */
const AGENT_COLOR_NAMES = new Set([
  "red",
  "orange",
  "amber",
  "yellow",
  "lime",
  "green",
  "emerald",
  "teal",
  "cyan",
  "sky",
  "blue",
  "indigo",
  "violet",
  "purple",
  "magenta",
  "fuchsia",
  "pink",
  "rose",
  "brown",
  "gray",
  "grey",
]);

function safeAgentColor(color: string): string | undefined {
  const value = color.trim().toLowerCase();
  if (AGENT_COLOR_NAMES.has(value)) return value;
  return /^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/.test(value) ? value : undefined;
}

/** Title-attribute tooltip: description, then the def's scope details. */
function agentRowTooltip(agent: RosterAgent): string | undefined {
  const lines: string[] = [];
  if (agent.description) lines.push(agent.description);
  const details: string[] = [];
  details.push(`permissions: ${agent.permissionMode || "default"}`);
  if (agent.tools.length > 0) details.push(`tools: ${agent.tools.join(", ")}`);
  lines.push(details.join(" · "));
  return lines.join("\n");
}

/**
 * The daemon's real agent roster, listed below the chat groups. Agents are
 * not chat containers — selecting one simply starts a new chat draft. Each
 * row shows the def's pinned model ("auto" when it inherits), tints its
 * avatar with the def's color hint, and carries tools + permission mode in
 * the tooltip (D2.2).
 */
export function AgentList({
  agents,
  onStartChat,
}: {
  agents: RosterAgent[];
  onStartChat: () => void;
}) {
  return (
    <div className="flex flex-col">
      {agents.map((agent) => {
        const tint = safeAgentColor(agent.color);
        return (
          <button
            key={agent.name}
            type="button"
            onClick={onStartChat}
            title={agentRowTooltip(agent)}
            aria-label={`New chat with ${agent.name}`}
            className="group flex items-center gap-2.5 border-l-[3px] border-transparent py-2 pr-3 pl-3 text-left transition-colors hover:bg-accent"
          >
            <span
              className="flex size-6 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground"
              style={tint ? { color: tint } : undefined}
            >
              <Bot className="size-3.5" />
            </span>
            <span className="min-w-0 flex-1 truncate text-[0.85rem] font-medium text-muted-foreground group-hover:text-foreground select-none">
              {agent.name}
            </span>
            <span className="max-w-[45%] shrink-0 truncate font-mono text-[10px] text-muted-foreground/50 select-none">
              {agent.model || "auto"}
            </span>
          </button>
        );
      })}
    </div>
  );
}

/**
 * Remembers each mock project's expanded/collapsed state by id, module-wide.
 * Selecting a chat can remount the workspace (the route re-keys), which would
 * otherwise reset every item's local `open` state; seeding from — and writing
 * back to — this store keeps folders as the user left them. Seeded with one
 * project open so the section demonstrates the expanded look immediately.
 */
const mockProjectOpenStore = new Map<string, boolean>([
  [MOCK_PROJECTS_DEFAULT_OPEN_ID, true],
]);

/**
 * One chat row inside an expanded mock project. Presentational only: the
 * Labs mock machinery has exactly one canned transcript (the feature tour),
 * and these rows deliberately do not mint a second mock-session system — so
 * there is nothing to open and the row is inert except for its hover state.
 */
function MockProjectChatRow({ chat }: { chat: MockProjectChat }) {
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    // py-2 matches the real SessionRow height so the two lists read as one
    // rhythm. Never styled selected: selection lives on real sessions only —
    // these rows are inert, so a highlight here would always be a lie.
    <div className="group flex items-center border-l-[3px] border-transparent py-2 pr-3 pl-8 transition-colors hover:bg-accent">
      <span className="min-w-0 flex-1 truncate text-[0.85rem] font-medium text-muted-foreground select-none group-hover:text-foreground">
        {chat.title}
      </span>
      {/* The real SessionRow's right-slot grammar: dot/age yields to the
          hover "…" menu. The items decline with a reason, the same disabled
          treatment a daemon row without the capability gets. */}
      <div className="shrink-0 ml-2 grid w-8 items-center justify-items-center [grid-template-areas:'slot']">
        {chat.unread ? (
          <span
            role="img"
            aria-label="Unread"
            className={cn(
              "[grid-area:slot] size-2 rounded-full bg-brand",
              menuOpen
                ? "min-[500px]:invisible"
                : "min-[500px]:group-hover:invisible",
            )}
          />
        ) : (
          <span
            className={cn(
              "[grid-area:slot] text-xs text-muted-foreground/50 tabular-nums",
              menuOpen
                ? "min-[500px]:invisible"
                : "min-[500px]:group-hover:invisible",
            )}
          >
            {formatRelativeTime(chat.updatedAt)}
          </span>
        )}
        <DropdownMenu modal={false} onOpenChange={setMenuOpen}>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label="Chat options"
              className={cn(
                "[grid-area:slot] flex items-center justify-center w-7 rounded text-muted-foreground hover:text-foreground",
                menuOpen
                  ? "opacity-100"
                  : "opacity-0 pointer-events-none min-[500px]:group-hover:opacity-100 min-[500px]:group-hover:pointer-events-auto",
              )}
            >
              <Ellipsis className="size-4" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="end"
            side="bottom"
            sideOffset={4}
            className="w-56"
          >
            <DropdownMenuItem disabled>
              <Pencil className="size-4 mr-2 shrink-0 text-muted-foreground" />
              <span className="min-w-0">
                Rename
                <span className="block truncate text-xs text-muted-foreground">
                  Demo content — not a real chat
                </span>
              </span>
            </DropdownMenuItem>
            <DropdownMenuItem disabled>
              <Trash2 className="size-4 mr-2 shrink-0" />
              <span className="min-w-0">
                Delete
                <span className="block truncate text-xs text-muted-foreground">
                  Demo content — not a real chat
                </span>
              </span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}

/** A collapsible mock project folder; clicking the row toggles it open.
 *  The header itself never takes the green treatment — the highlight
 *  belongs to exactly one row, the SELECTED chat inside; openness is
 *  carried by the folder icon alone. */
function MockProjectItem({ project }: { project: MockProject }) {
  const [open, setOpenState] = useState(
    () => mockProjectOpenStore.get(project.id) ?? false,
  );
  const toggle = () =>
    setOpenState((prev) => {
      const next = !prev;
      mockProjectOpenStore.set(project.id, next);
      return next;
    });

  return (
    <div className="flex flex-col">
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        aria-label={`${open ? "Collapse" : "Expand"} project: ${project.name}`}
        className="group flex items-center gap-2 border-l-[3px] border-transparent py-2 pr-3 pl-3 text-left transition-colors hover:bg-accent"
      >
        {open ? (
          <FolderOpen className="size-4 shrink-0 text-muted-foreground" />
        ) : (
          <FolderClosed className="size-4 shrink-0 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1 truncate text-[0.85rem] font-medium text-muted-foreground select-none group-hover:text-foreground">
          {project.name}
        </span>
      </button>
      {open && (
        <div className="flex flex-col">
          {project.chats.map((chat) => (
            <MockProjectChatRow key={chat.id} chat={chat} />
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * The Labs mock "Projects" section: project-grouped chats, presentation
 * only. The caller gates it on the mock-features preference, exactly like
 * the feature tour row — never rendered outside Settings → Labs.
 */
export function MockProjectList() {
  return (
    <div className="flex flex-col">
      {MOCK_PROJECTS.map((project) => (
        <MockProjectItem key={project.id} project={project} />
      ))}
    </div>
  );
}
