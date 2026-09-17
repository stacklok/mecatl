"use client";

import {
  FolderPlus,
  Loader2,
  PanelLeft,
  PanelRight,
  SquarePen,
} from "lucide-react";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  type AgentSession,
  type RosterAgent,
  useAgentChat,
  useAgentRoster,
  useAgentSessions,
} from "@/features/agent";
import { deriveChatPhase } from "@/features/agent/chat-phase";
import { TranscriptDialog } from "@/features/agent/components/transcript-dialog";
import type {
  BuiltinGates,
  BuiltinOutcome,
  StudioBuiltinCommand,
} from "@/features/agent/composer-builtins";
import { DEBUG_ASK_ALREADY_PENDING } from "@/features/agent/debug-ask";
import type { SessionInventoryWalk } from "@/features/agent/hooks/use-agent-sessions";
import { useAwayNotice } from "@/features/agent/hooks/use-away-notice";
import { useDeliveryFollow } from "@/features/agent/hooks/use-delivery-follow";
import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { useLearnedSkillChangeNotices } from "@/features/agent/hooks/use-learned-skill-change-notices";
import { useSessionMode } from "@/features/agent/hooks/use-session-mode";
import { useWorkspaceEnrollment } from "@/features/agent/hooks/use-workspace-enrollment";
import { pickLatestEligibleChat } from "@/features/agent/latest-chat";
import {
  isMockTourSession,
  MOCK_TOUR_MESSAGES,
  MOCK_TOUR_SESSION,
} from "@/features/agent/mock-tour";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { useBeforeUnloadGuard } from "@/hooks/use-beforeunload-guard";
import { useConfirm } from "@/hooks/use-confirm";
import { useIsCompact, useIsMobile } from "@/hooks/use-mobile";
import { useNavReopenSidebar } from "@/hooks/use-nav-reopen-sidebar";
import { usePanelWidth } from "@/hooks/use-panel-width";
import { usePrompt } from "@/hooks/use-prompt";
import {
  type MediaCapabilities,
  resolveMediaCapabilities,
} from "@/lib/attachment-inline";
import {
  MAX_FOLDERS,
  partitionByFolder,
  useChatFolders,
} from "@/lib/chat-folders";
import type { ChatSeed } from "@/lib/chat-seed";
import { composeDocumentTitle, useDocumentTitle } from "@/lib/document-title";
import {
  compactHarnessSession,
  forkHarnessSessionToModel,
  forkHarnessSessionToSelection,
  ThreadSourceBusyError,
} from "@/lib/harness/client";
import {
  createHarnessDebugSession,
  debugOpeningPrompt,
} from "@/lib/harness/debug";
import { useDefaultModel, useDisabledModels } from "@/lib/model-preferences";
import { takePendingDraft } from "@/lib/pending-draft";
import {
  type SessionListSide,
  useAgentDisplayName,
  useDeveloperTools,
  useLaunchTarget,
  useMockFeatures,
  useSessionListSide,
  useShowStarterPrompts,
} from "@/lib/profile-preferences";
import type { SessionPermissionMode } from "@/lib/protocol";
import { effortLabel } from "@/lib/reasoning-effort";
import {
  capabilityReasonLabel,
  describeRelationship,
  inspectRowTitle,
  sessionTabFor,
} from "@/lib/session-kinds";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { useThreadSessionIds } from "@/lib/thread-map";
import type { SessionToolProfile } from "@/lib/tool-profile";
import { cn } from "@/lib/utils";
import {
  ChatInput,
  type ComposerModelOption,
} from "../../_components/chat-input";
import { resolveDraftModel } from "../../_components/draft-model";
import { ResizeHandle } from "../../_components/resize-handle";
import { ChatView } from "./chat-view";
import {
  CLEARING_PLACEHOLDER,
  clearConversationGate,
  NOTHING_TO_CLEAR,
} from "./clear-conversation";
import {
  ContinueLatestChip,
  type LatestChatSummary,
} from "./continue-latest-chip";
import { useDebugSessionDialog } from "./debug-session-dialog";
import { DraftGreeting } from "./draft-greeting";
import { InspectQueryWatcher } from "./inspect-query-watcher";
import { InspectSessionGroups, inspectRowDomId } from "./inspect-session-list";
import { SessionInventoryStatus } from "./session-inventory-status";
import { StorageMaintenanceLink } from "./session-kind-tabs";
import {
  AgentList,
  type ChatFolderActions,
  FolderGroupMenu,
  MockProjectList,
  type SessionActions,
  SessionList,
  SidebarGroup,
} from "./session-sidebar";
import { TurnErrorStrip } from "./turn-error-strip";
import { useBuiltinSlashCommands } from "./use-builtin-slash-commands";
import { useClearConversation } from "./use-clear-conversation";
import { useComposerEscape } from "./use-composer-escape";
import { useDebugOpeningPrompt } from "./use-debug-opening-prompt";
import { useForkSessionCopy } from "./use-fork-session-copy";
import { useLatestChatAutoOpen } from "./use-latest-chat-auto-open";
import { seedSendWaitReason, useSeedPrompt } from "./use-seed-prompt";
import { useSessionRowShortcuts } from "./use-session-row-shortcuts";
import { useWorktreeSwitch } from "./use-worktree-switch";

const EMPTY_INSPECT_GROUPS: never[] = [];

/** Route for a chat, or the base (a new draft) when none is selected. */
const chatHref = (id?: string) =>
  id ? `/workspace/chat/${id}` : "/workspace/chat";

const DAY_MS = 86_400_000;

/**
 * One sidebar section: a recency bucket, or — with `folderId` — one of the
 * user's chat folders (`lib/chat-folders`), whose header carries the folder
 * menu and which is listed even when empty.
 */
interface SidebarSessionGroup {
  label: string;
  sessions: AgentSession[];
  folderId?: string;
}

/**
 * Bucket recency-sorted sessions into Today / This week / Earlier for the
 * flat sidebar list. Empty buckets are dropped.
 */
function groupSessionsByRecency(
  sessions: AgentSession[],
): SidebarSessionGroup[] {
  const startOfToday = new Date();
  startOfToday.setHours(0, 0, 0, 0);
  const t0 = startOfToday.getTime();
  const order = ["Today", "This week", "Earlier"] as const;
  const buckets: Record<(typeof order)[number], AgentSession[]> = {
    Today: [],
    "This week": [],
    Earlier: [],
  };
  for (const s of sessions) {
    const ts = s.updatedAt ?? 0;
    const label =
      ts >= t0 ? "Today" : ts >= t0 - 7 * DAY_MS ? "This week" : "Earlier";
    buckets[label].push(s);
  }
  return order
    .filter((l) => buckets[l].length > 0)
    .map((l) => ({ label: l, sessions: buckets[l] }));
}

function SidebarContent({
  onNewChat,
  isLoading,
  error,
  walk,
  onCancelLoad,
  onRetryLoad,
  groups,
  agents,
  selectedId,
  onSelect,
  actions,
  showMockProjects,
  inspectGroups,
  onInspect,
  emptyLabel,
  footer,
}: {
  onNewChat: () => void;
  isLoading: boolean;
  error: string | null;
  /** The inventory walk's progress/outcome for the status line. */
  walk: SessionInventoryWalk;
  onCancelLoad: () => void;
  onRetryLoad: () => void;
  groups: SidebarSessionGroup[];
  agents: RosterAgent[];
  selectedId: string;
  onSelect: (id: string) => void;
  actions: SessionActions;
  /** Labs mock features: list the demo project-grouped chats. */
  showMockProjects: boolean;
  /** The kind tabs under the header (Chats / Runs / Scheduled / Drafts / Other). */
  /** Recency groups of the active READ-ONLY tab's rows; empty on a chat tab. */
  inspectGroups: SidebarSessionGroup[];
  /** Opens a run's read-only transcript (never rebinds the live chat). */
  onInspect: (id: string) => void;
  /** The active tab's empty-state line. */
  emptyLabel: string;
  /** Rendered after the lists (the storage-maintenance link). */
  footer?: React.ReactNode;
}) {
  return (
    <>
      <div className="flex h-[60px] shrink-0 items-center gap-0.5 border-b border-border px-3 max-[499px]:h-14 lg:px-4">
        <h2 className="min-w-0 flex-1 truncate text-sm font-medium">
          Chat History
        </h2>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="size-8 shrink-0 text-muted-foreground"
              onClick={onNewChat}
              aria-label="New chat"
            >
              <SquarePen className="size-4" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="bottom">New chat</TooltipContent>
        </Tooltip>
        {/* Part of the Labs mock Projects demo: presentational only — there
            is no project system to create into, so the button is inert. */}
        {showMockProjects && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 shrink-0 text-muted-foreground"
                aria-label="New project"
              >
                <FolderPlus className="size-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom">New project</TooltipContent>
          </Tooltip>
        )}
      </div>

      <div className="flex-1 overflow-y-auto py-3">
        <SessionInventoryStatus
          walk={walk}
          error={error}
          onCancel={onCancelLoad}
          onRetry={onRetryLoad}
        />
        {/* The Labs mock Projects section leads the list: local demo
            content, never daemon rows — same gate and labeling discipline
            as the mock tour group. */}
        {!isLoading && showMockProjects && (
          <SidebarGroup label="Projects">
            <MockProjectList />
          </SidebarGroup>
        )}
        {/* A read-only tab (Runs / Scheduled / Other): inspect rows in the
            same recency groups; a click opens the transcript dialog. */}
        {!isLoading && inspectGroups.length > 0 && (
          <InspectSessionGroups groups={inspectGroups} onInspect={onInspect} />
        )}
        {isLoading ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="size-5 animate-spin text-muted-foreground" />
          </div>
        ) : groups.length > 0 ? (
          showMockProjects ? (
            // Mock mode flattens the date groups under one "Chat" header so
            // the demo's Projects/Chat split reads like the reference UI.
            <div className="pt-3">
              <SidebarGroup label="Chat">
                <SessionList
                  sessions={groups.flatMap((group) => group.sessions)}
                  selectedId={selectedId}
                  onSelect={onSelect}
                  actions={actions}
                  cap={7}
                />
              </SidebarGroup>
            </div>
          ) : (
            <div className="flex flex-col gap-3">
              {groups.map((group) => (
                <SidebarGroup
                  // A folder may share a recency label ("Today"); key by id.
                  key={
                    group.folderId ? `folder:${group.folderId}` : group.label
                  }
                  label={group.label}
                  menu={
                    group.folderId && actions.folders ? (
                      <FolderGroupMenu
                        folderId={group.folderId}
                        name={group.label}
                        actions={actions.folders}
                      />
                    ) : undefined
                  }
                >
                  {group.folderId && group.sessions.length === 0 ? (
                    <p className="px-4 py-1 text-xs text-muted-foreground/60">
                      No chats in this folder yet.
                    </p>
                  ) : (
                    <SessionList
                      sessions={group.sessions}
                      selectedId={selectedId}
                      onSelect={onSelect}
                      actions={actions}
                    />
                  )}
                </SidebarGroup>
              ))}
            </div>
          )
        ) : (
          // A walk still running or stopped early has not proven the
          // inventory empty; the status line above says what happened.
          !error &&
          !walk.inFlight &&
          !walk.cancelled &&
          inspectGroups.length === 0 && (
            <p className="text-center text-sm text-muted-foreground/50 py-8">
              {emptyLabel}
            </p>
          )
        )}
        {!isLoading && agents.length > 0 && (
          <div
            className={
              groups.length > 0 || inspectGroups.length > 0 || showMockProjects
                ? "pt-3"
                : undefined
            }
          >
            <SidebarGroup label="Agents">
              <AgentList agents={agents} onStartChat={onNewChat} />
            </SidebarGroup>
          </div>
        )}
        {!isLoading && footer}
      </div>
    </>
  );
}

/** Draft chat (no daemon session yet): greeting + starter chips, composer docked. */
function DraftView({
  onSend,
  seed,
  onSeedConsumed,
  onPickSeed,
  showStarterPrompts,
  error,
  onRetry,
  onEdit,
  showSidebarButton,
  sidebarSide,
  onShowSidebar,
  mode,
  onModeChange,
  profile,
  onProfileChange,
  models,
  autoModelLabel,
  onModelChange,
  onEffortChange,
  effortSupported,
  onLocalCommand,
  builtinGates,
  mediaCapabilities,
  onDraftChange,
  latest,
  onContinueLatest,
}: {
  onSend: (content: string, files?: File[]) => void;
  seed: string | null;
  onSeedConsumed: () => void;
  onPickSeed: (text: string) => void;
  /** Starter-prompt chips — hideable in Settings (the --no-banner analogue). */
  showStarterPrompts: boolean;
  error: string | null;
  /** Re-sends the refused first prompt verbatim (files included); offered
      only while the hook still holds it. */
  onRetry?: () => void;
  /** Puts the refused first prompt back in the composer for editing. */
  onEdit?: () => void;
  showSidebarButton: boolean;
  sidebarSide: SessionListSide;
  onShowSidebar: () => void;
  /** Pending permission mode, applied when the first send mints the session. */
  mode: SessionPermissionMode;
  onModeChange: (mode: SessionPermissionMode) => void;
  /** Pending tool profile ("" | "no-fs"), applied when the first send mints
      the session (the daemon's `CreateSessionRequest.profile`, ADR 0291). */
  profile: SessionToolProfile;
  onProfileChange: (profile: SessionToolProfile) => void;
  /** Live daemon models for the picker ("" = auto-routed). */
  models: ComposerModelOption[];
  autoModelLabel: string;
  /** Pending model pick ("" = auto row); null = reset to untouched. */
  onModelChange: (id: string | null) => void;
  /** Pending reasoning-effort tier (wire value; "" = auto), applied when the
   *  first send mints the session. */
  onEffortChange: (wire: string) => void;
  /** False when the daemon's model_selection capability is off. */
  effortSupported: boolean;
  /** Answers a Studio built-in typed in the draft composer (`/help`,
      `/diagnostics`, a draft `/clear`; the session-bound ones refuse). */
  onLocalCommand?: (
    command: StudioBuiltinCommand,
  ) => BuiltinOutcome | undefined;
  builtinGates?: BuiltinGates;
  /** The deployment's media modalities — a draft has no session detail yet. */
  mediaCapabilities?: MediaCapabilities;
  /** Fires when the composer's "holds unsent text" state flips (and `false`
      when it unmounts); the workspace arms its leave guard from it. */
  onDraftChange?: (hasText: boolean) => void;
  /** The most recent eligible chat for the Continue chip (null = no chip). */
  latest: LatestChatSummary | null;
  onContinueLatest: (id: string) => void;
}) {
  // The draft view has no panel, selection or run, so Esc goes straight to
  // the composer's double-Esc clear once there is text to clear. Only one of
  // ChatView / DraftView is mounted, so close.esc has a single owner.
  const [hasDraft, setHasDraft] = useState(false);
  const handleDraftChange = useCallback(
    (hasText: boolean) => {
      setHasDraft(hasText);
      onDraftChange?.(hasText);
    },
    [onDraftChange],
  );
  const { escapePress } = useComposerEscape({
    hasDraft,
    panelOpen: false,
    isStreaming: false,
  });
  return (
    <div className="flex h-full flex-col">
      {/* Same header bar as an open chat, so a draft doesn't lose the title
          row and its controls. */}
      <div className="flex h-[60px] shrink-0 items-center gap-2 border-b border-border px-3 max-[499px]:h-14 lg:gap-3 lg:px-6">
        <h2 className="min-w-0 flex-1 truncate text-sm font-semibold select-none">
          New chat
        </h2>
        {showSidebarButton && (
          <Button
            variant="ghost"
            size="icon"
            className="size-8 shrink-0 text-muted-foreground"
            onClick={onShowSidebar}
            aria-label="Show sidebar"
          >
            {sidebarSide === "left" ? (
              <PanelLeft className="size-4" />
            ) : (
              <PanelRight className="size-4" />
            )}
          </Button>
        )}
      </div>
      {/* The composer docks at the bottom exactly like an open chat, so the
          draft-to-chat transition doesn't move the input under your hands. */}
      <div className="relative min-h-0 flex-1">
        <div className="flex h-full flex-col items-center justify-center gap-6 overflow-y-auto px-4 pb-40 max-[499px]:pb-24 lg:px-8">
          <div className="w-full max-w-xl space-y-4">
            <DraftGreeting
              showStarterPrompts={showStarterPrompts}
              onPickSeed={onPickSeed}
            />
            {/* The `--resume-latest` chip: continue the newest quiet chat
                without any preference set. */}
            <ContinueLatestChip latest={latest} onContinue={onContinueLatest} />
          </div>
        </div>
        <div className="absolute bottom-0 left-0 right-0 px-3 lg:px-4 pb-4 max-[499px]:px-0 max-[499px]:pb-0">
          <div className="max-w-[768px] space-y-1.5 max-[499px]:max-w-none">
            {error && (
              <TurnErrorStrip error={error} onRetry={onRetry} onEdit={onEdit} />
            )}
            <ChatInput
              onSend={onSend}
              initialText={seed}
              onInitialTextConsumed={onSeedConsumed}
              draftKey="new"
              escapePress={escapePress}
              onDraftChange={handleDraftChange}
              placeholder="Start a new chat..."
              mobileDocked
              mode={mode}
              onModeChange={onModeChange}
              profile={profile}
              onProfileChange={onProfileChange}
              models={models}
              autoModelLabel={autoModelLabel}
              onModelChange={onModelChange}
              onEffortChange={onEffortChange}
              effortSupported={effortSupported}
              onLocalCommand={onLocalCommand}
              builtinGates={builtinGates}
              mediaCapabilities={mediaCapabilities}
            />
          </div>
        </div>
      </div>
    </div>
  );
}

/**
 * The chat workspace: one flat, daemon-backed session list plus the open
 * conversation. Selection is driven by the URL (`/workspace/chat/<sessionId>`),
 * so deep-links, back/forward, and hard reloads resolve to the same chat.
 * `/workspace/chat` with no id is a draft whose daemon session is minted on
 * the first send.
 */
export function ChatWorkspace({
  sessionId,
  seed,
}: {
  sessionId?: string;
  /** The route's `?prompt=` arrival, resolved by page.tsx (lib/chat-seed). */
  seed?: ChatSeed | null;
}) {
  const {
    sessions,
    runs,
    isLoading: sessionsLoading,
    error: sessionsError,
    walk: sessionsWalk,
    cancelLoad: cancelSessionsLoad,
    retry: retrySessionsLoad,
    deleteSession,
    renameSession,
    applyTitle,
    refreshSessions,
  } = useAgentSessions();
  const { agents } = useAgentRoster();
  const router = useRouter();
  const { name: agentName } = useAgentDisplayName();
  const { side: sidebarSide } = useSessionListSide();
  // Labs preference: list the mock feature tour. The mock id is treated as
  // mock UNCONDITIONALLY below (never handed to the daemon) — the toggle
  // only controls whether the row is offered.
  const { enabled: mockFeatures } = useMockFeatures();
  // Chat folders: Studio-owned, browser-local grouping of the sidebar rows
  // (`lib/chat-folders`); the daemon never hears of them.
  const {
    folders,
    assignments: folderAssignments,
    createFolder,
    renameFolder,
    deleteFolder,
    moveChat,
  } = useChatFolders();
  // Labs preference: the developer tools (mecatui's client debug mode) —
  // the `/debug-ask` built-in + menu item that park a FAKE permission ask
  // (never sent to the daemon) and the steer trace under the queue strip.
  const { enabled: developerTools } = useDeveloperTools();
  // Hideable starter prompts (Settings → Personalize), browser-local.
  const { show: showStarterPrompts } = useShowStarterPrompts();
  const isMobile = useIsMobile();
  const isCompact = useIsCompact();
  const { confirm, ConfirmDialog } = useConfirm();
  const { prompt, PromptDialog } = usePrompt();

  // Selection is URL-driven; the state mirror keeps it in sync while also
  // allowing an optimistic update before the client navigation settles.
  const [selectedId, setSelectedIdState] = useState(sessionId ?? "");
  // Mirror for effects that must read the current selection without
  // re-firing on every selection change.
  const selectedIdRef = useRef(selectedId);
  useEffect(() => {
    selectedIdRef.current = selectedId;
  }, [selectedId]);
  // The id minted for a draft on first send. While the URL settles on that id
  // the chat hook keeps its draft binding (it already owns the live stream);
  // re-keying it would wipe the in-flight messages with a transcript refetch.
  const draftMintedIdRef = useRef<string | null>(null);
  useEffect(() => {
    setSelectedIdState(sessionId ?? "");
    if (sessionId !== draftMintedIdRef.current) draftMintedIdRef.current = null;
  }, [sessionId]);

  /**
   * Select a chat: optimistic state + a native history push. Deliberately not
   * router.push — moving the optional catch-all between zero and one segments
   * changes the route shape, which remounts this page; the remount re-runs
   * the pre-measurement useState inits (sidebarOpen assumes desktop until the
   * viewport is measured), so on a phone the list rendered straight over the
   * chat that was just tapped. The App Router syncs its state from native
   * history updates without remounting.
   */
  const selectId = useCallback((id: string) => {
    setSelectedIdState(id);
    window.history.pushState(null, "", chatHref(id));
  }, []);

  const [sidebarOpen, setSidebarOpen] = useState(!isMobile && !isCompact);
  // The init above runs before the viewport is measured (useIsMobile is
  // undefined on the first render), so a phone mounts with the list open even
  // over a deep-linked chat. Once mobile is a measured fact, the selection
  // wins; a list the user opens later is untouched.
  useEffect(() => {
    if (isMobile && selectedIdRef.current) setSidebarOpen(false);
  }, [isMobile]);
  const wasCompactRef = useRef(isCompact);
  // Mirrors `sidebarOpen` for reads inside the stable side-panel handler.
  const sidebarOpenRef = useRef(sidebarOpen);
  useEffect(() => {
    sidebarOpenRef.current = sidebarOpen;
  }, [sidebarOpen]);
  // Remembers whether the chat list was open before a side panel forced it
  // closed, so closing the panel restores the user's prior choice.
  const sidebarBeforeSidePanelRef = useRef<boolean | null>(null);

  const handleSidePanelOpenChange = useCallback((open: boolean) => {
    if (open) {
      if (sidebarBeforeSidePanelRef.current === null) {
        sidebarBeforeSidePanelRef.current = sidebarOpenRef.current;
        setSidebarOpen(false);
      }
    } else if (sidebarBeforeSidePanelRef.current !== null) {
      setSidebarOpen(sidebarBeforeSidePanelRef.current);
      sidebarBeforeSidePanelRef.current = null;
    }
  }, []);

  // Sync sidebar visibility with viewport transitions: entering compact
  // closes it; leaving compact reopens it.
  useEffect(() => {
    const wasCompact = wasCompactRef.current;
    if (!wasCompact && isCompact) {
      setSidebarOpen(false);
    } else if (wasCompact && !isCompact) {
      setSidebarOpen(true);
    }
    wasCompactRef.current = isCompact;
  }, [isCompact]);
  const [sidebarWidth, setSidebarWidth] = usePanelWidth();

  const handleSessionCreated = useCallback(
    (id: string) => {
      draftMintedIdRef.current = id;
      setSelectedIdState(id);
      // Native replaceState, deliberately not router.replace: moving the
      // optional catch-all from zero segments to one changes the route
      // shape, which remounts this page — and a remount replaces the chat
      // hook instance, so the in-flight stream would render into dead
      // state and the pane would sit empty until a reload. The App Router
      // syncs its state from native history updates without remounting.
      window.history.replaceState(null, "", chatHref(id));
      void refreshSessions();
    },
    [refreshSessions],
  );

  // The Labs mock chat never talks to the daemon: its transcript is a local
  // constant and its id must never reach the chat hook.
  const isMockSelected = isMockTourSession(selectedId);

  // The draft keeps a null hook id even after its session is minted and the
  // URL updates — the hook already streams against the minted id internally.
  const hookSessionId =
    selectedId && !isMockSelected && selectedId !== draftMintedIdRef.current
      ? selectedId
      : null;

  // The composer's permission mode. For an open chat this reads/writes the
  // live session (POST /mode); for a draft it is pending local state, read
  // via modeRef when the first send mints the daemon session below.
  // A mode change made mid-run is HELD by the hook (the daemon rejects it)
  // and lands when the run ends; `busy` mirrors the chat hook's isStreaming,
  // which is declared below this call, hence the state bridge.
  const [modeBusy, setModeBusy] = useState(false);
  const {
    mode,
    modeRef,
    changeMode,
    refreshMode,
    pendingMode,
    profile,
    profileRef,
    profileKnown,
    changeProfile,
  } = useSessionMode(isMockSelected ? null : selectedId || null, {
    busy: modeBusy,
  });
  const getCreateMode = useCallback(() => modeRef.current, [modeRef]);
  // The tool profile rides the same draft→mint path as the mode: pending
  // local state read via ref when the first send mints the session.
  const getCreateProfile = useCallback(() => profileRef.current, [profileRef]);

  // The composer's model picker: live daemon models minus the Studio-side
  // disabled set; "" = auto-routed. Like mode, the pick is pending local
  // state read via ref when the first send mints the session.
  const { models: liveModels, status: runtimeStatus } = useHarnessRuntime();
  const { disabled: disabledModels } = useDisabledModels();
  const modelOptions = useMemo(
    () =>
      liveModels
        .filter((m) => !disabledModels.has(m.id))
        .map((m) => ({
          id: m.id,
          label: m.displayName || m.id,
          // The daemon requires provider_id whenever model_id rides a create.
          providerId: m.providerId,
          // The Effort list warns when the picked model reports no reasoning.
          reasoning: m.reasoning,
          // The picker rows' capability glyph and context-window label.
          image: m.image,
          contextLimit: m.contextLimit,
        })),
    [liveModels, disabledModels],
  );
  // "Auto-routed" is only an honest name for the empty pick while the model
  // router is actually on; otherwise the daemon just uses its default model.
  const routingEnabled = Boolean(runtimeStatus?.modelRouter?.enabled);
  // Draft pick: null = untouched (the browser-local Studio default for new
  // chats applies when the daemon lists it), "" = the auto row on purpose.
  const draftModelRef = useRef<string | null>(null);
  const handleDraftModelChange = useCallback((id: string | null) => {
    draftModelRef.current = id;
  }, []);
  const modelOptionsRef = useRef(modelOptions);
  modelOptionsRef.current = modelOptions;
  const { defaultModel: studioDefault } = useDefaultModel();
  const studioDefaultRef = useRef(studioDefault);
  studioDefaultRef.current = studioDefault;
  const getCreateModel = useCallback(() => {
    // The SAME resolver the picker labels itself with: a pick wins, else the
    // Studio default when the inventory lists it, else the daemon default —
    // a pick or default that fell out of the inventory never sends a bare
    // model_id the daemon would reject.
    const resolved = resolveDraftModel(
      draftModelRef.current,
      studioDefaultRef.current,
      modelOptionsRef.current,
    );
    return resolved.id && resolved.providerId
      ? { modelId: resolved.id, providerId: resolved.providerId }
      : null;
  }, []);
  // Launch reconciliation (the TUI's saved-model-rejected warning): a Studio
  // default the LOADED inventory does not list is announced once per default
  // and left in storage untouched, so it applies again when the model comes
  // back; new chats meanwhile use the daemon default.
  const staleDefaultWarnedRef = useRef<string | null>(null);
  useEffect(() => {
    if (!studioDefault) {
      staleDefaultWarnedRef.current = null;
      return;
    }
    const key = `${studioDefault.providerId}/${studioDefault.modelId}`;
    if (!resolveDraftModel(null, studioDefault, modelOptions).staleDefault) {
      staleDefaultWarnedRef.current = null;
      return;
    }
    if (staleDefaultWarnedRef.current === key) return;
    staleDefaultWarnedRef.current = key;
    const hidden = liveModels.some(
      (m) =>
        m.id === studioDefault.modelId &&
        m.providerId === studioDefault.providerId,
    );
    toast.warning(
      hidden
        ? `Your default model ${key} is hidden in Studio — new chats use the daemon default until you show it again on its provider page`
        : `Your default model ${key} is not available on this daemon — new chats use the daemon default`,
    );
  }, [studioDefault, modelOptions, liveModels]);
  // The composer's pending reasoning-effort tier (wire value; "" = auto, the
  // field is omitted so the operator's --reasoning-effort default applies).
  // Like the model pick: a ref read when the first send mints the session.
  const draftEffortRef = useRef("");
  const handleDraftEffortChange = useCallback((wire: string) => {
    draftEffortRef.current = wire;
  }, []);
  const getCreateEffort = useCallback(() => draftEffortRef.current, []);

  // A run terminal may have flipped the daemon-owned mode (a plan approved
  // → Manual / Accept edits): re-adopt it so the Mode pill never lies. The
  // inventory is re-walked on the same terminal because the daemon's title
  // (the first-prompt seed, an auto-rename) lands with the run, not the
  // create — until then a fresh chat's header and tab title read the
  // "Untitled chat" stand-in, and the next poll is up to 20 s away. The
  // resolved model/context window already re-reads via the hook's
  // GET-session detail on the same terminal.
  const handleRunEnded = useCallback(() => {
    refreshMode();
    void refreshSessions();
  }, [refreshMode, refreshSessions]);

  // The idle chat's metadata watch heard a run start elsewhere (a schedule,
  // another tab): re-walk the inventory now so the row flips to running and
  // the full watch renders the run live, instead of up to 20 s later.
  const handleExternalRunDetected = useCallback(() => {
    void refreshSessions();
  }, [refreshSessions]);

  const {
    messages,
    isStreaming,
    status,
    error: chatError,
    statusMessage,
    harnessLive,
    sendMessage,
    retryLast,
    lastFailurePermanent,
    recoverDraft,
    consumeRecoverDraft,
    failedPrompt,
    takeFailedPrompt,
    refreshTranscript,
    pendingApproval,
    approvalQueueLength,
    respondToApproval,
    pendingClarification,
    respondToClarification,
    usage,
    fleet,
    contextOccupancy,
    sessionDetail,
    sessionDetailStatus,
    providerRoute,
    queuedMessages,
    queueMessage,
    deleteQueued,
    takeQueued,
    takeAllQueued,
    clearQueue,
    queuePaused,
    resumeQueue,
    steerQueued,
    steerMessage,
    steerSupported,
    pendingSteers,
    steerTrace,
    cancelPendingSteers,
    injectDebugApproval,
    cancelChat,
    drivingRun,
    cancelChild,
    pendingAuthorization,
    openAuthorization,
    copyAuthorizationLink,
    recheckAuthorization,
    cancelAuthorization,
  } = useAgentChat(hookSessionId, {
    onSessionCreated: handleSessionCreated,
    createMode: getCreateMode,
    createModel: getCreateModel,
    createEffort: getCreateEffort,
    createProfile: getCreateProfile,
    // The inventory poll's lifecycle state: running/awaiting attaches the
    // durable watch so an externally-driven run renders live (ADR 0250).
    sessionState: hookSessionId
      ? sessions.find((s) => s.id === hookSessionId)?.state
      : undefined,
    // Mode re-adopt + inventory re-walk on the run terminal (handleRunEnded).
    onRunEnded: handleRunEnded,
    // Live `session.title` events land on the inventory row at once (header,
    // sidebar, tab title), revision-guarded against replayed older titles.
    onTitle: applyTitle,
    onExternalRunDetected: handleExternalRunDetected,
  });

  // Bridges the chat hook's isStreaming (declared above) into the mode hook
  // (declared before it): a run parked on an approval is still busy.
  useEffect(() => {
    setModeBusy(isStreaming);
  }, [isStreaming]);

  /** Esc with nothing else open interrupts the in-flight run (close.esc). */
  const handleCancelRun = useCallback(() => {
    void cancelChat();
  }, [cancelChat]);

  // The leave guard (the TUI's two-step quit, as the browser's confirm): an
  // unsent draft in whichever composer is mounted (lifted via onDraftChange),
  // or a run THIS tab drives — the `POST /prompt` stream ends its run on
  // disconnect (the CLAUDE.md residual). A watched run driven elsewhere
  // survives the tab, so it never arms the guard; cancel-on-exit is
  // deliberately not implemented (runs outlive the client, ADR 0250).
  const [hasDraft, setHasDraft] = useState(false);
  useBeforeUnloadGuard(hasDraft || (drivingRun && isStreaming));

  // The daemon's operator-enabled capabilities (A3 caches /v1/compatibility)
  // and the connection state the tab title's Offline/Connecting word reads.
  const {
    serverCapabilities,
    posture,
    state: connection,
    refresh: refreshRuntime,
  } = useRuntimeStatus();

  // The session's effective model + context window: the chat hook's
  // GET-session detail (read on open and re-read on every run terminal), so
  // the meter's denominator follows the daemon instead of a once-per-chat
  // fetch. Null against an older daemon — the meter then shows the bare size.
  const resolvedModel = sessionDetail?.resolvedModel ?? null;

  // An AI-debug chat (ADR 0254) keeps its binding: no fork (a model/effort
  // switch forks), no mode change, no compaction from the UI — the TUI hides
  // the same controls. Read off the inventory row AND the snapshot, so a
  // row that has not landed yet cannot expose the controls.
  const debugChat = Boolean(
    sessionDetail?.debugTargetSessionId ||
      sessions.find((s) => s.id === selectedId)?.debugTargetSessionId,
  );

  // Workspace-services enrollment (the TUI's /tools-connect notice): gated on
  // the daemon's `workspace_enrollment` capability inside the hook, hidden on
  // the mock tour and on an AI-debug chat. The daemon accepts the controls
  // only while the session is idle, so a run in flight disables them.
  const enrollment = useWorkspaceEnrollment(
    isMockSelected ? null : selectedId || null,
    {
      idle: !isStreaming && (status === "idle" || status === "error"),
      debugSession: debugChat,
    },
  );

  // Manual compaction (B1.2-B1.4): gated on the daemon's manual_compaction
  // capability (the compatibility document is the live source; the GET-session
  // echo is its per-session sibling once daemons stamp it).
  const compactSupported = serverCapabilities.manual_compaction === true;

  // What the composer may stage as media (the TUI's `attach:` gate): the
  // open session's resolved modalities off the GET-session detail, else the
  // deployment's compatibility echo (a draft, or an older daemon). One value
  // for the live composer and the draft's — a draft has no detail yet.
  const sessionMedia = sessionDetail?.sessionCapabilities ?? null;
  const mediaCapabilities = useMemo(
    () => resolveMediaCapabilities(sessionMedia, serverCapabilities),
    [sessionMedia, serverCapabilities],
  );
  const handleCompact = useCallback(async () => {
    const id = selectedIdRef.current;
    if (!id || isMockTourSession(id)) return;
    try {
      const compacted = await compactHarnessSession(id);
      toast.success(
        compacted ? "Conversation compacted" : "Nothing to compact",
      );
      if (compacted) {
        // The model history was rewritten; the transcript must refetch.
        await refreshTranscript();
      }
    } catch (caught) {
      toast.error(caught instanceof Error ? caught.message : String(caught));
    }
  }, [refreshTranscript]);

  useNavReopenSidebar(setSidebarOpen);

  // Seed for the draft composer, set when a starter prompt is picked.
  const [draftSeed, setDraftSeed] = useState<string | null>(null);
  const clearDraftSeed = useCallback(() => setDraftSeed(null), []);
  // A draft's first send the daemon refused (the session mint or the run
  // entry itself) hands its text back to the draft composer, exactly as the
  // live chat's composer takes a recovered prompt — a failed first send must
  // never lose the text. Once the composer owns it the hook no longer holds
  // it to re-send, so the draft strip withholds Retry and keeps Edit.
  const [draftRecovered, setDraftRecovered] = useState(false);
  useEffect(() => {
    if (!recoverDraft || selectedId) return;
    setDraftSeed(recoverDraft.text);
    consumeRecoverDraft();
    setDraftRecovered(true);
  }, [recoverDraft, selectedId, consumeRecoverDraft]);
  useEffect(() => {
    if (failedPrompt === null) setDraftRecovered(false);
  }, [failedPrompt]);
  const draftCanRetry = failedPrompt !== null && !draftRecovered;
  const handleDraftEditFailed = useCallback(() => {
    const text = takeFailedPrompt();
    if (text) setDraftSeed(text);
  }, [takeFailedPrompt]);
  // Text handed over from another route (Settings → About → "Send to a new
  // chat" stashes the diagnostics report) lands in the draft composer once,
  // on mount; the user reviews it and presses Enter — nothing is sent on
  // their behalf.
  useEffect(() => {
    const pending = takePendingDraft();
    if (pending) setDraftSeed(pending);
  }, []);
  // The route's arrival prompt (`?prompt=…`, `&send=1`, the PWA share
  // target — the web analogue of `mecatui -p`): consumed once per mount by
  // the hook, which strips the query. A prefill goes to whichever composer
  // the route shows (the draft's, or the open chat's via initialDraft); a
  // send=1 seed waits behind the confirmation dialog and then takes the
  // composer's own send path, so a draft mints its session exactly as Enter
  // would. Send is held while offline, mid-run, or on the mock tour.
  const [sessionSeed, setSessionSeed] = useState<string | null>(null);
  const clearSessionSeed = useCallback(() => setSessionSeed(null), []);
  const { seedPromptDialog } = useSeedPrompt({
    seed,
    sessionId,
    onPrefill: sessionId ? setSessionSeed : setDraftSeed,
    onSend: sendMessage,
    waitReason: seedSendWaitReason({
      connected: connection === "connected",
      isStreaming,
      status,
      isMock: isMockSelected,
    }),
  });

  // Sessions minted to back message threads. Filtered HERE, at the
  // presentation seam, deliberately not inside use-agent-sessions: rule 9's
  // test pins that hook to the daemon store verbatim (rows obey the store),
  // and a thread session IS a real store row — it is only this list, the
  // keyboard order derived from it, and search that hide it. Its sole entry
  // point is the reply indicator on its parent message; deep-linking to
  // /workspace/chat/<threadId> still works (selectedSession reads the
  // unfiltered `sessions`), which stays the escape hatch for parked
  // approvals. The registry — never the "Thread: " title — decides, so a
  // user's own chat named "Thread: …" is never hidden.
  const threadSessionIds = useThreadSessionIds();
  const orderedSessions = useMemo(
    () =>
      sessions
        .filter((s) => !threadSessionIds.has(s.id))
        .sort((a, b) => (b.updatedAt ?? 0) - (a.updatedAt ?? 0)),
    [sessions, threadSessionIds],
  );
  // The `--resume-latest` pick — the newest QUIET chat: running/awaiting,
  // AI-debug, thread-backing and mock rows are skipped (`latest-chat.ts`).
  // Feeds the draft's Continue chip, the `chat.latest` shortcut and the
  // "Most recent chat" launch preference below.
  const latestChat = useMemo(
    () => pickLatestEligibleChat(sessions, threadSessionIds),
    [sessions, threadSessionIds],
  );
  // Every chat lists under Chats, drafts included (the sidebar has no kind
  // tabs); non-chat rows never reach the list.
  const groups = useMemo(() => {
    const chats = orderedSessions.filter(
      (s) => sessionTabFor(s, false) === "chats",
    );
    // The user's folders lead, in creation order — an empty folder keeps
    // its header so it can be renamed or deleted; every other chat keeps
    // today's recency buckets.
    const { filed, unfiled } = partitionByFolder(
      chats,
      folders,
      folderAssignments,
    );
    const folderGroups: SidebarSessionGroup[] = filed.map(
      ({ folder, sessions }) => ({
        label: folder.name,
        folderId: folder.id,
        sessions,
      }),
    );
    const recency = groupSessionsByRecency(unfiled);
    // The Labs mock tour pins atop the list under its own clearly-labeled
    // group — local demo content, never a daemon row.
    return mockFeatures
      ? [
          { label: "Mock", sessions: [MOCK_TOUR_SESSION] },
          ...folderGroups,
          ...recency,
        ]
      : [...folderGroups, ...recency];
  }, [orderedSessions, mockFeatures, folders, folderAssignments]);

  const selectedSession = useMemo<AgentSession | undefined>(() => {
    if (!selectedId) return undefined;
    if (isMockTourSession(selectedId)) return MOCK_TOUR_SESSION;
    const found = sessions.find((s) => s.id === selectedId);
    if (found) return found;
    // A just-minted draft's row may not have landed in the polled list yet;
    // a stand-in keeps the conversation rendered until the walk catches up.
    return {
      id: selectedId,
      title: "Untitled chat",
      projectId: null,
      model: "",
      createdAt: 0,
      updatedAt: 0,
      pinned: false,
      archived: false,
      messageCount: 0,
      isStreaming: false,
      inputTokens: 0,
      outputTokens: 0,
      unread: false,
      estimatedCost: null,
      contextLength: null,
      lastPromptTokens: null,
      thresholdTokens: null,
    };
  }, [selectedId, sessions]);

  // The browser tab title — mecatui's window title (wintitle.go): the chat
  // title leads, a STATIC phase word trails (Working / ⚠ Approval, or the
  // connection's Offline / Connecting), then the app name. The phase folds
  // this tab's own status with the inventory row's daemon state, so a run
  // driven elsewhere (a schedule, another tab — ADR 0250) reaches the tab
  // too. It changes only on a phase transition, never per token.
  const phase = deriveChatPhase(status, selectedSession?.state);
  useDocumentTitle(
    composeDocumentTitle({
      chatTitle: selectedSession ? selectedSession.title : "New chat",
      phase,
      connection,
    }),
  );

  // The "while you were away" resume notice — mecatui's resumeNotice: one
  // toast on tab return (after ≥20 s hidden) naming the phase that was in
  // flight when the user left and what it did meanwhile. The daemon probe
  // and the inventory are refreshed FIRST (a hidden tab's timers are
  // throttled, so both can be a minute stale), then the line is composed
  // from the same phase fold the tab title reads.
  useAwayNotice({
    phase,
    chatTitle: selectedSession ? selectedSession.title : "New chat",
    connected: connection === "connected",
    refresh: () => Promise.all([refreshRuntime(), refreshSessions()]),
  });

  // Scheduled-task delivery notes that land while this chat sits idle (a
  // fire that completed between two inventory polls): pick them up from the
  // transcript, and notify a hidden tab of each newly completed one.
  useDeliveryFollow({
    sessionId: hookSessionId,
    updatedAt: selectedSession?.updatedAt,
    state: selectedSession?.state,
    isStreaming,
    connected: harnessLive,
    messages,
    refreshTranscript,
  });

  // Learned-skill change receipts after a run (mecatui's post-ResultMsg
  // ListSkillChanges + status line): one toast per run that produced new
  // receipts, pointing at the Skills page's Learned view. Capability-gated.
  useLearnedSkillChangeNotices();

  const handleSelectSession = useCallback(
    (id: string) => {
      selectId(id);
      if (isMobile || isCompact) setSidebarOpen(false);
    },
    [selectId, isMobile, isCompact],
  );

  // The `--resume-latest` landing (Settings → Personalize → "Start on"): a
  // bare-route arrival under "Most recent chat" opens `latestChat` once the
  // daemon is connected and the inventory has loaded — unless the user asked
  // for a draft ("New chat"), the draft already holds work (an arrival
  // prompt, a picked starter, typed text), or nothing is eligible. Native
  // replaceState, deliberately not a push: Back must return to wherever the
  // user came from, never bounce to the draft (the `handleSessionCreated`
  // reasoning about the App Router applies here too).
  const { target: launchTarget } = useLaunchTarget();
  const openLatestOnLanding = useCallback(
    (id: string) => {
      setSelectedIdState(id);
      window.history.replaceState(null, "", chatHref(id));
      // A phone mounts with the list open (pre-measurement init); the opened
      // chat must not sit underneath it, as after a tap on a row.
      if (isMobile || isCompact) setSidebarOpen(false);
    },
    [isMobile, isCompact],
  );
  const { requestDraft: requestExplicitDraft } = useLatestChatAutoOpen({
    selectedId,
    launchTarget,
    connected: connection === "connected",
    sessionsLoading,
    hold: Boolean(seed) || draftSeed !== null || hasDraft,
    latestChatId: latestChat?.id ?? null,
    onOpen: openLatestOnLanding,
  });

  /** "New chat" opens the draft route; the daemon session is minted on send. */
  const handleNewChat = useCallback(() => {
    // An explicit draft: the launch preference must not re-open the most
    // recent chat over it, whether or not the route change remounts.
    requestExplicitDraft();
    setSelectedIdState("");
    router.push(chatHref());
    if (isMobile || isCompact) setSidebarOpen(false);
  }, [requestExplicitDraft, router, isMobile, isCompact]);

  const deselectIfActive = useCallback(
    (id: string) => {
      if (id === selectedId) {
        requestExplicitDraft();
        setSelectedIdState("");
        router.push(chatHref());
      }
    },
    [selectedId, router, requestExplicitDraft],
  );

  // "Debug with AI" (F1, ADR 0254): creates a SEPARATE no-fs diagnostic
  // session bound to the picked chat, after the mandated consent dialog —
  // invoking the debugger sends the target's STORED transcript and event
  // evidence (secrets included) to the model, even though the target itself
  // can never be modified. Gated on the daemon's session_debug capability.
  const debugSupported = serverCapabilities.session_debug === true;
  // The consent dialog (DebugSessionDialog) also offers the daemon's
  // configured MCP servers when its debug_mcp capability is on — the TUI's
  // `--debug-mcp NAME`: every call the debugger makes on them still asks for
  // approval one call at a time, and Always allow is never learned.
  // The debug chat's opening objective (the TUI submits `defaultDebugPrompt`
  // the moment its debug chat opens; the daemon's create starts no run):
  // armed on create, sent ONCE by the chat hook once it has re-keyed onto the
  // new session and reads live + idle — never into a draft or another chat.
  const { arm: armDebugOpeningPrompt } = useDebugOpeningPrompt({
    sessionId: hookSessionId,
    live: harnessLive,
    status,
    sendMessage,
  });
  const createDebugSession = useCallback(
    async (id: string, mcpServers: string[], runtimeContext: string | null) => {
      try {
        const debugId = await createHarnessDebugSession(id, { mcpServers });
        await refreshSessions();
        // Arm before selecting: the hook re-keys on the selection and sends
        // the objective on its first live + idle render there.
        armDebugOpeningPrompt(
          debugId,
          debugOpeningPrompt({ servers: mcpServers, runtimeContext }),
        );
        handleSelectSession(debugId);
        toast.success(
          mcpServers.length > 0
            ? `Debug session created with MCP: ${mcpServers.join(", ")} — sending the diagnostic objective`
            : "Debug session created — sending the diagnostic objective",
        );
      } catch (caught) {
        toast.error(caught instanceof Error ? caught.message : String(caught));
      }
    },
    [refreshSessions, handleSelectSession, armDebugOpeningPrompt],
  );
  const { requestDebugSession, debugSessionDialog } = useDebugSessionDialog({
    onCreate: (id, request) =>
      void createDebugSession(id, request.mcpServers, request.runtimeContext),
  });
  const handleDebugSession = useCallback(
    (id: string) => {
      // The Labs mock row is local demo content — never a daemon target.
      if (isMockTourSession(id)) return;
      // The title names the target in the consent dialog (with its handle).
      requestDebugSession(id, sessions.find((s) => s.id === id)?.title ?? "");
    },
    [requestDebugSession, sessions],
  );

  const sessionActions: SessionActions = useMemo(
    () => ({
      onDebug: debugSupported ? handleDebugSession : undefined,
      onRename: async (id: string) => {
        const s = sessions.find((x) => x.id === id);
        const name = await prompt({
          title: "Rename chat",
          placeholder: "Chat name",
          defaultValue: s?.title ?? "",
          confirmText: "Rename",
        });
        if (name) await renameSession(id, name);
      },
      onDelete: async (id: string) => {
        const ok = await confirm({
          title: "Delete chat",
          description:
            "This chat and its messages will be permanently deleted.",
          confirmText: "Delete",
          destructive: true,
        });
        if (!ok) return;
        await deleteSession(id);
        deselectIfActive(id);
      },
    }),
    [
      sessions,
      prompt,
      renameSession,
      confirm,
      deleteSession,
      deselectIfActive,
      debugSupported,
      handleDebugSession,
    ],
  );

  // Only chats list in the sidebar. Child runs and scheduled fires stay
  // reachable through the read-only transcript dialog (deep links, the
  // delegation panel, the schedules page) — never as sidebar rows.
  const visibleGroups = groups;
  const inspectGroups: typeof groups = EMPTY_INSPECT_GROUPS;

  // The read-only transcript dialog (the TUI's Inspect / `v` viewer): a run
  // whose transcript the daemon withholds toasts the reason instead. A deep
  // link may name a run the walk has not landed yet — the dialog then loads
  // by id and shows the daemon's own refusal, with Retry.
  const [inspecting, setInspecting] = useState<{
    sessionId: string;
    label: string;
    subtitle: string;
    parentSessionId: string;
  } | null>(null);
  const handleInspectSession = useCallback(
    (id: string) => {
      const run = runs.find((r) => r.id === id);
      if (run && run.canViewTranscript === false) {
        toast.info(
          capabilityReasonLabel(run.viewTranscriptReason) ||
            "Transcript unavailable",
        );
        return;
      }
      setInspecting({
        sessionId: id,
        label: run ? inspectRowTitle(run) : id,
        subtitle: run ? describeRelationship(run.relationship) : "",
        parentSessionId: run?.relationship?.parentSessionId ?? "",
      });
    },
    [runs],
  );
  // From a delegation card in THIS chat: the parent is the open chat, so
  // the dialog offers no parent link.
  const handleInspectChild = useCallback(
    (childId: string, label: string) => {
      const run = runs.find((r) => r.id === childId);
      setInspecting({
        sessionId: childId,
        label: run?.title || label,
        subtitle: run ? describeRelationship(run.relationship) : "",
        parentSessionId: "",
      });
    },
    [runs],
  );
  // The row menu's "View transcript" (the TUI's `v`) for a CHAT row: the same
  // read-only dialog the Runs tab opens, without making the row the live
  // chat. A row the daemon withholds toasts its reason (the item is already
  // disabled with it); an id not in the chat inventory is a run.
  const handleViewTranscript = useCallback(
    (id: string) => {
      const row = sessions.find((s) => s.id === id);
      if (!row) {
        handleInspectSession(id);
        return;
      }
      if (row.canViewTranscript !== true) {
        toast.info(
          capabilityReasonLabel(row.viewTranscriptReason) ||
            "Transcript unavailable",
        );
        return;
      }
      setInspecting({
        sessionId: id,
        label: row.title || "Untitled chat",
        subtitle: "",
        parentSessionId: "",
      });
    },
    [sessions, handleInspectSession],
  );
  // The row menu's "Fork chat" (the TUI's `f`): a copy of the chat as-is on
  // the same model/effort/placement; the UI moves there once the daemon
  // answered and the source stays in the list.
  const forkSessionCopy = useForkSessionCopy({
    onForked: async (newId) => {
      await refreshSessions();
      handleSelectSession(newId);
    },
  });
  const handleForkSession = useCallback(
    (id: string) => {
      const row = sessions.find((s) => s.id === id);
      void forkSessionCopy(id, row?.title ?? "");
    },
    [sessions, forkSessionCopy],
  );
  // Chat folders (browser-local, `lib/chat-folders`): the row menu's "Move
  // to folder" and the folder headers' rename/delete. Deleting a folder only
  // unfiles its chats — no chat is ever deleted or changed from here.
  const folderActions: ChatFolderActions = useMemo(
    () => ({
      folders,
      assignments: folderAssignments,
      onMove: moveChat,
      onMoveToNew: async (sessionId: string) => {
        const name = await prompt({
          title: "New folder",
          description:
            "Folders only group your chats in the list — nothing about a chat changes.",
          placeholder: "Folder name",
          confirmText: "Create",
        });
        if (!name) return;
        const folderId = createFolder(name);
        if (!folderId) {
          toast.error(`You can have up to ${MAX_FOLDERS} folders.`);
          return;
        }
        moveChat(sessionId, folderId);
      },
      onRename: async (folderId: string) => {
        const folder = folders.find((f) => f.id === folderId);
        if (!folder) return;
        const name = await prompt({
          title: "Rename folder",
          placeholder: "Folder name",
          defaultValue: folder.name,
          confirmText: "Rename",
        });
        if (!name) return;
        if (!renameFolder(folderId, name)) {
          toast.error("There is already a folder with that name.");
        }
      },
      onDelete: async (folderId: string) => {
        const folder = folders.find((f) => f.id === folderId);
        if (!folder) return;
        const ok = await confirm({
          title: "Delete folder",
          description: `"${folder.name}" will be removed. The chats in it stay in your chat history.`,
          confirmText: "Delete folder",
          destructive: true,
        });
        if (ok) deleteFolder(folderId);
      },
    }),
    [
      folders,
      folderAssignments,
      prompt,
      confirm,
      createFolder,
      renameFolder,
      deleteFolder,
      moveChat,
    ],
  );
  const rowActions: SessionActions = useMemo(
    () => ({
      ...sessionActions,
      onViewTranscript: handleViewTranscript,
      onFork: handleForkSession,
      folders: folderActions,
    }),
    [sessionActions, handleViewTranscript, handleForkSession, folderActions],
  );

  // Flattened, in-display-order chat ids for keyboard navigation (the
  // active tab's rows).
  const navOrder = useMemo(
    () => visibleGroups.flatMap((g) => g.sessions.map((s) => s.id)),
    [visibleGroups],
  );
  // No inspect rows list in the sidebar any more (see inspectGroups).
  const inspectOrder: string[] = EMPTY_INSPECT_GROUPS;

  const navigateBy = useCallback(
    (forward: boolean) => {
      if (inspectOrder.length > 0) {
        // A read-only tab: ↑/↓ move focus across the inspect rows (Enter
        // then opens the one in focus); nothing is selected or opened.
        const activeId = document.activeElement?.id ?? "";
        const cur = inspectOrder.findIndex(
          (id) => inspectRowDomId(id) === activeId,
        );
        const idx =
          cur === -1
            ? forward
              ? 0
              : inspectOrder.length - 1
            : forward
              ? Math.min(cur + 1, inspectOrder.length - 1)
              : Math.max(cur - 1, 0);
        document.getElementById(inspectRowDomId(inspectOrder[idx]))?.focus();
        return;
      }
      if (navOrder.length === 0) return;
      const cur = navOrder.indexOf(selectedId);
      const idx =
        cur === -1
          ? forward
            ? 0
            : navOrder.length - 1
          : forward
            ? Math.min(cur + 1, navOrder.length - 1)
            : Math.max(cur - 1, 0);
      handleSelectSession(navOrder[idx]);
    },
    [navOrder, selectedId, handleSelectSession],
  );

  useShortcut("chat.new", handleNewChat);
  // Jump to the most recent quiet chat (the `--resume-latest` analogue).
  useShortcut("chat.latest", () => {
    if (latestChat) handleSelectSession(latestChat.id);
  });
  useShortcut("chat.toggleList", () => setSidebarOpen((o) => !o));
  useShortcut("chat.next", () => navigateBy(true));
  useShortcut("chat.next.vim", () => navigateBy(true));
  useShortcut("chat.prev", () => navigateBy(false));
  useShortcut("chat.prev.vim", () => navigateBy(false));
  // "Debug with AI" for the selected chat (the TUI's F1): the same consent
  // dialog the row menu opens. Nothing on a draft, the mock tour, a chat
  // that already IS a debug session, or a daemon without session_debug.
  useShortcut("debug.open", () => {
    if (!selectedId || !debugSupported || debugChat || isMockSelected) return;
    handleDebugSession(selectedId);
  });

  const sidebarContentProps = {
    onNewChat: handleNewChat,
    isLoading: sessionsLoading,
    error: sessionsError,
    walk: sessionsWalk,
    onCancelLoad: cancelSessionsLoad,
    onRetryLoad: retrySessionsLoad,
    groups: visibleGroups,
    agents,
    selectedId,
    onSelect: handleSelectSession,
    actions: rowActions,
    showMockProjects: mockFeatures,
    inspectGroups,
    onInspect: handleInspectSession,
    emptyLabel: "No chats yet",
    // The TUI's Maintenance tab lives on the Storage settings page here.
    footer:
      serverCapabilities.storage_health === true ? (
        <StorageMaintenanceLink />
      ) : undefined,
  };

  // Clear conversation (the TUI's /clear handoff): the daemon cancels a
  // running source itself and mints an empty-history successor with the same
  // placement/model/mode; the composer blocks until the UI has moved there.
  // Shared by the header menu, ⌘⇧X and the `/clear` built-in below.
  const { clearing, clearConversation } = useClearConversation({
    sessionId: isMockSelected ? null : selectedId || null,
    onClearQueue: clearQueue,
    onSessionCleared: async (successorId) => {
      await refreshSessions();
      handleSelectSession(successorId);
    },
  });
  const clearGate = clearConversationGate(
    isMockSelected ? undefined : selectedSession,
    isStreaming,
  );

  // The composer's Studio built-ins (`/clear /help /session /retry
  // /diagnostics /compact`), dispatched here where the chat state lives. The
  // same dispatcher serves the draft composer: the session-bound built-ins
  // refuse there with a warning, `/clear` just drops the draft's queue.
  const {
    builtinGates,
    handleSlashBuiltin,
    sessionDetailsDialog,
    soulDialog,
    openSessionDetails,
  } = useBuiltinSlashCommands({
    sessionId: isMockSelected ? null : selectedId || null,
    isStreaming,
    // Live or rehydrated (`failed` inventory state), the only state with a
    // failed step for the daemon to re-drive.
    hasFailedStep: status === "error",
    compactSupported,
    developerTools,
    // The capability-gated built-ins (`/mcp /agents /skills /models …`)
    // read the daemon's document; `/posture` its reported tier.
    serverCapabilities,
    posture,
    // `/title`: the same Rename prompt as the chat menu, only where the
    // row's `rename` capability allows it (never on a draft or the mock).
    onRename:
      selectedSession && !isMockSelected && selectedSession.canRename === true
        ? () => sessionActions.onRename(selectedSession.id)
        : undefined,
    // `/tools-connect` and `/tools-cancel` drive the enrollment notice's
    // own connect / retry / cancel.
    enrollment,
    onInjectDebugAsk: injectDebugApproval,
    onCompact: () => void handleCompact(),
    onRetry: () => void retryLast(),
    onSend: (content) => void sendMessage(content),
    onClearQueue: clearQueue,
    onClearConversation: clearConversation,
    resolvedModel,
    permissionMode: mode,
    // The inventory row's copy_id capability and last write, which the
    // snapshot the dialog reads does not carry.
    sessionDetails: {
      canCopyId: selectedSession?.canCopyId,
      copyIdReason: selectedSession?.copyIdReason,
      updatedAt: selectedSession?.updatedAt,
    },
  });
  useShortcut("chat.details", openSessionDetails);
  // Developer tools: the ··· menu's "Inject fake approval" (the `/debug-ask`
  // built-in's twin). A refusal has no composer to warn in, so it toasts.
  const handleInjectDebugAsk = useCallback(() => {
    if (!injectDebugApproval()) toast.info(DEBUG_ASK_ALREADY_PENDING);
  }, [injectDebugApproval]);
  // ⌘⇧X clears from anywhere in the chat; on a draft the hook says there is
  // nothing to clear, and a row the daemon marks unclearable is refused
  // the same way the menu item is disabled.
  useShortcut("chat.clear", () => {
    if (clearGate.kind === "enabled") void clearConversation();
    else if (clearGate.kind === "disabled") toast.info(clearGate.reason);
    else toast.info(NOTHING_TO_CLEAR);
  });
  // The per-chat keys for the OPEN chat: copy its session ID (the TUI's `c`
  // on /session) and fork it as-is (the TUI's `f`); a draft, the mock tour or
  // a refusing row toasts why instead.
  useSessionRowShortcuts({
    session: isMockSelected ? undefined : selectedSession,
    onFork: handleForkSession,
  });

  // Switch worktree (the TUI's /worktrees picker): a clear (default) or a
  // fork of this chat rooted at a sibling git worktree the daemon lists by
  // opaque selector (ADR 0291); the UI moves to the new chat once the daemon
  // answered. Offered only when the daemon advertises `worktrees` and the
  // row may mint a successor; never on a draft, the mock tour or a debug chat.
  const { openWorktreePicker, worktreePickerDialog } = useWorktreeSwitch({
    session:
      isMockSelected || debugChat || !selectedSession ? null : selectedSession,
    supported: serverCapabilities.worktrees === true,
    onSwitched: async (newId) => {
      await refreshSessions();
      handleSelectSession(newId);
    },
  });

  const dialogs = (
    <>
      {ConfirmDialog}
      {PromptDialog}
      {sessionDetailsDialog}
      {soulDialog}
      {debugSessionDialog}
      {worktreePickerDialog}
      {seedPromptDialog}
      {inspecting && (
        <TranscriptDialog
          sessionId={inspecting.sessionId}
          label={inspecting.label}
          subtitle={inspecting.subtitle || undefined}
          parentSessionId={inspecting.parentSessionId || undefined}
          onOpenParent={(parentId) => {
            setInspecting(null);
            handleSelectSession(parentId);
          }}
          onClose={() => setInspecting(null)}
        />
      )}
      {/* The search's run deep link (`?inspect=<id>`) opens the dialog. */}
      <InspectQueryWatcher onInspect={handleInspectSession} />
    </>
  );

  const turnError =
    status === "error" ? (chatError ?? "The last turn failed.") : null;

  // Mid-chat model switch: the daemon fixes a session's model at create, so
  // a pick FORKS the chat — a new session seeded from this one's history on
  // the new model, carrying the title — and the UI moves there. The old chat
  // stays in the list (nothing is destroyed); a mid-run source answers 412.
  const handleSwitchModel = useCallback(
    async (option: ComposerModelOption | null) => {
      const source = selectedSession;
      if (!source) return;
      const attempt = async (): Promise<void> => {
        try {
          const newId = await forkHarnessSessionToModel(
            source.id,
            option?.providerId
              ? { modelId: option.id, providerId: option.providerId }
              : null,
            source.title || "",
          );
          await refreshSessions();
          handleSelectSession(newId);
          toast.success(
            `Continuing on ${option?.label ?? "the auto-routed model"} in a copy of this chat`,
          );
        } catch (caught) {
          // A busy source (412) is remedied by waiting, not retrying; every
          // other failure offers the TUI's enter-to-retry as a toast action.
          if (caught instanceof ThreadSourceBusyError) {
            toast.error(
              "Wait for the current response to finish, then switch models.",
            );
            return;
          }
          toast.error(
            caught instanceof Error ? caught.message : String(caught),
            { action: { label: "Retry", onClick: () => void attempt() } },
          );
        }
      };
      await attempt();
    },
    [selectedSession, refreshSessions, handleSelectSession],
  );

  // Mid-chat effort switch: the same handoff as a model switch — the daemon
  // fixes a session's reasoning-effort tier at create, so a pick FORKS the
  // chat onto the tier (model unchanged, title carried) and the UI moves
  // there. "" = back to the operator default (the field is omitted).
  const handleSwitchEffort = useCallback(
    async (wire: string) => {
      const source = selectedSession;
      if (!source) return;
      const attempt = async (): Promise<void> => {
        try {
          const newId = await forkHarnessSessionToSelection(
            source.id,
            { reasoningEffort: wire },
            source.title || "",
          );
          await refreshSessions();
          handleSelectSession(newId);
          toast.success(
            `Continuing at ${effortLabel(wire)} effort in a copy of this chat`,
          );
        } catch (caught) {
          // Same retry shape as the model switch: waiting cures a 412, a
          // Retry action covers everything else.
          if (caught instanceof ThreadSourceBusyError) {
            toast.error(
              "Wait for the current response to finish, then switch effort.",
            );
            return;
          }
          toast.error(
            caught instanceof Error ? caught.message : String(caught),
            { action: { label: "Retry", onClick: () => void attempt() } },
          );
        }
      };
      await attempt();
    },
    [selectedSession, refreshSessions, handleSelectSession],
  );

  // The picked/effective model's `reasoning` flag for the Effort list's
  // warning: keyed on the daemon's resolved model (an auto-routed session
  // has "" for its own model), undefined when the inventory lacks it.
  const currentModelReasoning = useMemo(() => {
    const id = resolvedModel?.modelId || selectedSession?.model || "";
    if (!id) return undefined;
    return liveModels.find((m) => m.id === id)?.reasoning;
  }, [resolvedModel, selectedSession, liveModels]);
  // The effort picker follows the model picker's gate: a daemon with model
  // selection off lists no models, so it takes no tier either.
  const effortSupported = serverCapabilities.model_selection !== false;

  const chatView = (open: boolean, onToggle: () => void) =>
    selectedSession ? (
      isMockSelected ? (
        // The mock feature tour: a canned local transcript, a disabled
        // composer, and zero daemon traffic (the hook id above is null).
        <ChatView
          session={selectedSession}
          messages={MOCK_TOUR_MESSAGES}
          isStreaming={false}
          onSend={() => {}}
          readOnlyPlaceholder="Mock chat — read-only"
          botName={agentName}
          sidebarOpen={open}
          sidebarSide={sidebarSide}
          onToggleSidebar={onToggle}
          pendingApproval={null}
          onRespondApproval={() => {}}
          pendingClarification={null}
          onRespondClarification={() => {}}
          onSidePanelOpenChange={
            isMobile ? undefined : handleSidePanelOpenChange
          }
        />
      ) : (
        <ChatView
          session={selectedSession}
          messages={messages}
          isStreaming={isStreaming}
          live={harnessLive}
          usage={usage}
          error={turnError}
          statusMessage={statusMessage}
          onRetry={retryLast}
          lastFailurePermanent={lastFailurePermanent}
          onNewChat={handleNewChat}
          recoverDraft={recoverDraft}
          onRecoverDraftConsumed={consumeRecoverDraft}
          onEditFailed={failedPrompt !== null ? takeFailedPrompt : undefined}
          onSend={sendMessage}
          // The `?prompt=` arrival on an open chat, prefilled once.
          initialDraft={sessionSeed}
          onInitialDraftConsumed={clearSessionSeed}
          queuedMessages={queuedMessages}
          onQueueMessage={queueMessage}
          onOpenSession={handleSelectSession}
          onSteerQueued={steerQueued}
          onDeleteQueued={deleteQueued}
          onTakeQueued={takeQueued}
          onTakeAllQueued={takeAllQueued}
          onClearQueue={clearQueue}
          queuePaused={queuePaused}
          onResumeQueue={resumeQueue}
          // Steer is capability-gated (C1.2): absent, mid-run sends queue and
          // the composer's steer action degrades to queue.
          onSteerMessage={steerSupported ? steerMessage : undefined}
          pendingSteers={steerSupported ? pendingSteers : undefined}
          onRetractSteers={steerSupported ? cancelPendingSteers : undefined}
          // Developer tools (Settings → Labs): the steer trace and the fake
          // ask are offered only while the preference is on.
          steerTrace={developerTools ? steerTrace : undefined}
          onInjectDebugAsk={developerTools ? handleInjectDebugAsk : undefined}
          onCancelRun={handleCancelRun}
          onDraftChange={setHasDraft}
          onCompact={compactSupported && !debugChat ? handleCompact : undefined}
          // Clear conversation: the daemon's row verdict gates the item; the
          // composer is blocked while the successor handoff is in flight.
          onClear={
            clearGate.kind === "hidden"
              ? undefined
              : () => void clearConversation()
          }
          clearDisabledReason={
            clearGate.kind === "disabled"
              ? clearGate.reason
              : clearing
                ? CLEARING_PLACEHOLDER
                : undefined
          }
          readOnlyPlaceholder={clearing ? CLEARING_PLACEHOLDER : undefined}
          onSwitchWorktree={openWorktreePicker}
          onLocalCommand={handleSlashBuiltin}
          builtinGates={builtinGates}
          // The Agents panel's model; Teams is gated on the daemon's `teams`
          // capability (absent on a daemon that never enabled teams).
          fleet={fleet}
          teamsSupported={serverCapabilities.teams === true}
          mediaCapabilities={mediaCapabilities}
          onCancelChild={cancelChild}
          onInspectChild={handleInspectChild}
          enrollment={enrollment}
          contextInfo={
            resolvedModel
              ? {
                  modelLabel: resolvedModel.modelId,
                  contextWindow: resolvedModel.contextWindow,
                  effort: resolvedModel.reasoningEffort,
                }
              : null
          }
          contextOccupancy={contextOccupancy}
          botName={agentName}
          sidebarOpen={open}
          sidebarSide={sidebarSide}
          onToggleSidebar={onToggle}
          pendingApproval={pendingApproval}
          approvalQueueLength={approvalQueueLength}
          onRespondApproval={respondToApproval}
          pendingClarification={pendingClarification}
          onRespondClarification={respondToClarification}
          pendingAuthorization={pendingAuthorization}
          onOpenAuthorization={openAuthorization}
          onCopyAuthorizationLink={copyAuthorizationLink}
          onRecheckAuthorization={recheckAuthorization}
          onCancelAuthorization={cancelAuthorization}
          onOpenDetails={openSessionDetails}
          onRename={
            selectedSession.canRename === true
              ? () => sessionActions.onRename(selectedSession.id)
              : undefined
          }
          onDelete={
            selectedSession.canDelete === true
              ? () => sessionActions.onDelete(selectedSession.id)
              : undefined
          }
          // Fork as-is: the header item gates itself on the row's `fork`
          // capability (disabled with the daemon's reason); a debug chat's
          // successor would lose its binding, so it is not offered there.
          onFork={
            debugChat ? undefined : () => handleForkSession(selectedSession.id)
          }
          onSidePanelOpenChange={
            isMobile ? undefined : handleSidePanelOpenChange
          }
          // `mode` is the daemon-CONFIRMED mode; a mid-run switch rides
          // `pendingMode` separately so the pill, the header badge and the
          // status strip can each show the pick as "pending" until the daemon
          // confirms it (passing the pick AS `mode` hid the pending state).
          mode={mode}
          onModeChange={debugChat ? undefined : changeMode}
          // Display-only: the profile is fixed at create and the daemon never
          // reports it, so only a chat Studio minted (remembered) shows one.
          profile={profileKnown ? profile : undefined}
          pendingMode={pendingMode}
          modeSwitchDeferred
          providerRoute={providerRoute}
          modelResolution={sessionDetailStatus}
          debugMcpServers={sessionDetail?.debugMcpServers}
          debugMcpTools={sessionDetail?.debugMcpTools}
          models={modelOptions}
          autoModelLabel={routingEnabled ? "Auto-routed" : "Default model"}
          onSwitchModel={debugChat ? undefined : handleSwitchModel}
          onSwitchEffort={debugChat ? undefined : handleSwitchEffort}
          currentEffort={resolvedModel?.reasoningEffort ?? ""}
          currentModelReasoning={currentModelReasoning}
          effortSupported={effortSupported}
        />
      )
    ) : null;

  if (isMobile) {
    return (
      <div className="flex h-full flex-col">
        {dialogs}
        {sidebarOpen ? (
          <div className="flex h-full flex-col bg-background">
            <SidebarContent {...sidebarContentProps} />
          </div>
        ) : selectedSession ? (
          chatView(false, () => setSidebarOpen(true))
        ) : (
          <DraftView
            models={modelOptions}
            autoModelLabel={routingEnabled ? "Auto-routed" : "Default model"}
            onModelChange={handleDraftModelChange}
            onEffortChange={handleDraftEffortChange}
            effortSupported={effortSupported}
            mediaCapabilities={mediaCapabilities}
            onSend={sendMessage}
            seed={draftSeed}
            onSeedConsumed={clearDraftSeed}
            onPickSeed={setDraftSeed}
            showStarterPrompts={showStarterPrompts}
            error={turnError}
            onRetry={draftCanRetry ? retryLast : undefined}
            onEdit={failedPrompt !== null ? handleDraftEditFailed : undefined}
            showSidebarButton
            sidebarSide={sidebarSide}
            onShowSidebar={() => setSidebarOpen(true)}
            mode={mode}
            onModeChange={changeMode}
            profile={profile}
            onProfileChange={changeProfile}
            onLocalCommand={handleSlashBuiltin}
            builtinGates={builtinGates}
            onDraftChange={setHasDraft}
            latest={latestChat}
            onContinueLatest={handleSelectSession}
          />
        )}
      </div>
    );
  }

  return (
    <div className="relative flex h-full">
      {dialogs}
      <div className="flex-1 overflow-hidden">
        {selectedSession ? (
          chatView(sidebarOpen, () => setSidebarOpen((o) => !o))
        ) : (
          <DraftView
            models={modelOptions}
            autoModelLabel={routingEnabled ? "Auto-routed" : "Default model"}
            onModelChange={handleDraftModelChange}
            onEffortChange={handleDraftEffortChange}
            effortSupported={effortSupported}
            mediaCapabilities={mediaCapabilities}
            onSend={sendMessage}
            seed={draftSeed}
            onSeedConsumed={clearDraftSeed}
            onPickSeed={setDraftSeed}
            showStarterPrompts={showStarterPrompts}
            error={turnError}
            onRetry={draftCanRetry ? retryLast : undefined}
            onEdit={failedPrompt !== null ? handleDraftEditFailed : undefined}
            showSidebarButton={!sidebarOpen}
            sidebarSide={sidebarSide}
            onShowSidebar={() => setSidebarOpen(true)}
            mode={mode}
            onModeChange={changeMode}
            profile={profile}
            onProfileChange={changeProfile}
            onLocalCommand={handleSlashBuiltin}
            builtinGates={builtinGates}
            onDraftChange={setHasDraft}
            latest={latestChat}
            onContinueLatest={handleSelectSession}
          />
        )}
      </div>

      {sidebarOpen &&
        (isCompact ? (
          <>
            {/* Backdrop closes the overlay sidebar on outside click */}
            <button
              type="button"
              aria-label="Close sidebar"
              className="absolute inset-0 z-30 bg-black/20"
              onClick={() => setSidebarOpen(false)}
            />
            <div
              className={cn(
                "absolute inset-y-0 z-40 flex flex-col border-border bg-background shadow-lg",
                sidebarSide === "left" ? "left-0 border-r" : "right-0 border-l",
              )}
              style={{ width: sidebarWidth }}
            >
              {/* Handle on the panel's inner edge: dragging inward widens */}
              <ResizeHandle
                direction={sidebarSide === "left" ? "right" : "left"}
                width={sidebarWidth}
                onWidthChange={setSidebarWidth}
              />
              <SidebarContent {...sidebarContentProps} />
            </div>
          </>
        ) : (
          <div
            className={cn(
              "relative flex shrink-0 flex-col border-border bg-background",
              sidebarSide === "left" ? "order-first border-r" : "border-l",
            )}
            style={{ width: sidebarWidth }}
          >
            {/* Handle on the panel's inner edge: dragging inward widens */}
            <ResizeHandle
              direction={sidebarSide === "left" ? "right" : "left"}
              width={sidebarWidth}
              onWidthChange={setSidebarWidth}
            />
            <SidebarContent {...sidebarContentProps} />
          </div>
        ))}
    </div>
  );
}
