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
import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { useSessionMode } from "@/features/agent/hooks/use-session-mode";
import {
  isMockTourSession,
  MOCK_TOUR_MESSAGES,
  MOCK_TOUR_SESSION,
} from "@/features/agent/mock-tour";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { useConfirm } from "@/hooks/use-confirm";
import { useIsCompact, useIsMobile } from "@/hooks/use-mobile";
import { useNavReopenSidebar } from "@/hooks/use-nav-reopen-sidebar";
import { usePanelWidth } from "@/hooks/use-panel-width";
import { usePrompt } from "@/hooks/use-prompt";
import {
  compactHarnessSession,
  fetchHarnessSessionDetail,
  forkHarnessSessionToModel,
  type HarnessResolvedModel,
  ThreadSourceBusyError,
} from "@/lib/harness/client";
import { createHarnessDebugSession } from "@/lib/harness/debug";
import { useDisabledModels } from "@/lib/model-preferences";
import {
  type SessionListSide,
  useAgentDisplayName,
  useMockFeatures,
  useSessionListSide,
} from "@/lib/profile-preferences";
import type { SessionPermissionMode } from "@/lib/protocol";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { useThreadSessionIds } from "@/lib/thread-map";
import { pageTitleClass } from "@/lib/typography";
import { cn } from "@/lib/utils";
import {
  ChatInput,
  type ComposerModelOption,
} from "../../_components/chat-input";
import { ResizeHandle } from "../../_components/resize-handle";
import { ChatView } from "./chat-view";
import {
  AgentList,
  MockProjectList,
  type SessionActions,
  SessionList,
  SidebarGroup,
} from "./session-sidebar";

/** Route for a chat, or the base (a new draft) when none is selected. */
const chatHref = (id?: string) =>
  id ? `/workspace/chat/${id}` : "/workspace/chat";

/** One-click prompts on the draft state, to seed the first message. */
const STARTER_PROMPTS = [
  "Summarise what changed in the repo this week",
  "Draft a plan for a new feature",
  "Review my open pull requests",
  "Find and explain a bug in the codebase",
] as const;

const DAY_MS = 86_400_000;

/**
 * Bucket recency-sorted sessions into Today / This week / Earlier for the
 * flat sidebar list. Empty buckets are dropped.
 */
function groupSessionsByRecency(
  sessions: AgentSession[],
): { label: string; sessions: AgentSession[] }[] {
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
  groups,
  agents,
  selectedId,
  onSelect,
  actions,
  showMockProjects,
}: {
  onNewChat: () => void;
  isLoading: boolean;
  error: string | null;
  groups: { label: string; sessions: AgentSession[] }[];
  agents: RosterAgent[];
  selectedId: string;
  onSelect: (id: string) => void;
  actions: SessionActions;
  /** Labs mock features: list the demo project-grouped chats. */
  showMockProjects: boolean;
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
        {error && (
          <p className="px-4 pb-2 text-xs text-destructive break-words">
            {error}
          </p>
        )}
        {/* The Labs mock Projects section leads the list: local demo
            content, never daemon rows — same gate and labeling discipline
            as the mock tour group. */}
        {!isLoading && showMockProjects && (
          <SidebarGroup label="Projects">
            <MockProjectList />
          </SidebarGroup>
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
                <SidebarGroup key={group.label} label={group.label}>
                  <SessionList
                    sessions={group.sessions}
                    selectedId={selectedId}
                    onSelect={onSelect}
                    actions={actions}
                  />
                </SidebarGroup>
              ))}
            </div>
          )
        ) : (
          !error && (
            <p className="text-center text-sm text-muted-foreground/50 py-8">
              No chats yet
            </p>
          )
        )}
        {!isLoading && agents.length > 0 && (
          <div
            className={
              groups.length > 0 || showMockProjects ? "pt-3" : undefined
            }
          >
            <SidebarGroup label="Agents">
              <AgentList agents={agents} onStartChat={onNewChat} />
            </SidebarGroup>
          </div>
        )}
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
  error,
  showSidebarButton,
  sidebarSide,
  onShowSidebar,
  mode,
  onModeChange,
  models,
  autoModelLabel,
  onModelChange,
}: {
  onSend: (content: string, files?: File[]) => void;
  seed: string | null;
  onSeedConsumed: () => void;
  onPickSeed: (text: string) => void;
  error: string | null;
  showSidebarButton: boolean;
  sidebarSide: SessionListSide;
  onShowSidebar: () => void;
  /** Pending permission mode, applied when the first send mints the session. */
  mode: SessionPermissionMode;
  onModeChange: (mode: SessionPermissionMode) => void;
  /** Live daemon models for the picker ("" = auto-routed). */
  models: ComposerModelOption[];
  autoModelLabel: string;
  onModelChange: (id: string) => void;
}) {
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
            <h1
              className={pageTitleClass(
                "pb-0 text-center text-3xl leading-tight",
              )}
            >
              What can I help you with?
            </h1>
            <div className="flex flex-wrap justify-center gap-2">
              {STARTER_PROMPTS.map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => onPickSeed(p)}
                  className="rounded-full border border-border bg-background px-3.5 py-1.5 text-sm text-muted-foreground transition-colors hover:border-foreground/20 hover:text-foreground"
                >
                  {p}
                </button>
              ))}
            </div>
          </div>
        </div>
        <div className="absolute bottom-0 left-0 right-0 px-3 lg:px-4 pb-4 max-[499px]:px-0 max-[499px]:pb-0">
          <div className="max-w-[768px] space-y-1.5 max-[499px]:max-w-none">
            {error && <p className="px-1 text-sm text-destructive">{error}</p>}
            <ChatInput
              rows={1}
              onSend={onSend}
              initialText={seed}
              onInitialTextConsumed={onSeedConsumed}
              placeholder="Start a new chat..."
              mobileDocked
              mode={mode}
              onModeChange={onModeChange}
              models={models}
              autoModelLabel={autoModelLabel}
              onModelChange={onModelChange}
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
export function ChatWorkspace({ sessionId }: { sessionId?: string }) {
  const {
    sessions,
    isLoading: sessionsLoading,
    error: sessionsError,
    deleteSession,
    renameSession,
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
  const { mode, modeRef, changeMode } = useSessionMode(
    isMockSelected ? null : selectedId || null,
  );
  const getCreateMode = useCallback(() => modeRef.current, [modeRef]);

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
        })),
    [liveModels, disabledModels],
  );
  // "Auto-routed" is only an honest name for the empty pick while the model
  // router is actually on; otherwise the daemon just uses its default model.
  const routingEnabled = Boolean(runtimeStatus?.modelRouter?.enabled);
  const draftModelRef = useRef("");
  const handleDraftModelChange = useCallback((id: string) => {
    draftModelRef.current = id;
  }, []);
  const modelOptionsRef = useRef(modelOptions);
  modelOptionsRef.current = modelOptions;
  const getCreateModel = useCallback(() => {
    const id = draftModelRef.current;
    if (!id) return null;
    const option = modelOptionsRef.current.find((m) => m.id === id);
    // A pick that fell out of the inventory degrades to auto rather than
    // sending a bare model_id the daemon would reject.
    return option?.providerId
      ? { modelId: id, providerId: option.providerId }
      : null;
  }, []);

  const {
    messages,
    isStreaming,
    status,
    error: chatError,
    harnessLive,
    sendMessage,
    retryLast,
    refreshTranscript,
    pendingApproval,
    respondToApproval,
    pendingClarification,
    respondToClarification,
    usage,
    queuedMessages,
    queueMessage,
    deleteQueued,
    takeQueued,
    steerQueued,
    steerMessage,
    steerSupported,
    cancelChat,
  } = useAgentChat(hookSessionId, {
    onSessionCreated: handleSessionCreated,
    createMode: getCreateMode,
    createModel: getCreateModel,
    // The inventory poll's lifecycle state: running/awaiting attaches the
    // durable watch so an externally-driven run renders live (ADR 0250).
    sessionState: hookSessionId
      ? sessions.find((s) => s.id === hookSessionId)?.state
      : undefined,
  });

  /** Esc with nothing else open interrupts the in-flight run (close.esc). */
  const handleCancelRun = useCallback(() => {
    void cancelChat();
  }, [cancelChat]);

  // The daemon's operator-enabled capabilities (A3 caches /v1/compatibility).
  const { connected, serverCapabilities } = useRuntimeStatus();

  // The session's effective model + context window (B1.1): GET-session's
  // resolved_model echo. Per selected chat; a fetch failure just hides the
  // meter (it is an approximation, never load-bearing).
  const [resolvedModel, setResolvedModel] =
    useState<HarnessResolvedModel | null>(null);
  useEffect(() => {
    setResolvedModel(null);
    if (!selectedId || isMockTourSession(selectedId) || !connected) return;
    const controller = new AbortController();
    void fetchHarnessSessionDetail(selectedId, controller.signal)
      .then((detail) => {
        if (!controller.signal.aborted) setResolvedModel(detail.resolvedModel);
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, [selectedId, connected]);

  // Manual compaction (B1.2-B1.4): gated on the daemon's manual_compaction
  // capability (the compatibility document is the live source; the GET-session
  // echo is its per-session sibling once daemons stamp it).
  const compactSupported = serverCapabilities.manual_compaction === true;
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
  const groups = useMemo(() => {
    const recency = groupSessionsByRecency(orderedSessions);
    // The Labs mock tour pins atop the list under its own clearly-labeled
    // group — local demo content, never a daemon row.
    return mockFeatures
      ? [{ label: "Mock", sessions: [MOCK_TOUR_SESSION] }, ...recency]
      : recency;
  }, [orderedSessions, mockFeatures]);

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

  const handleSelectSession = useCallback(
    (id: string) => {
      selectId(id);
      if (isMobile || isCompact) setSidebarOpen(false);
    },
    [selectId, isMobile, isCompact],
  );

  /** "New chat" opens the draft route; the daemon session is minted on send. */
  const handleNewChat = useCallback(() => {
    setSelectedIdState("");
    router.push(chatHref());
    if (isMobile || isCompact) setSidebarOpen(false);
  }, [router, isMobile, isCompact]);

  const deselectIfActive = useCallback(
    (id: string) => {
      if (id === selectedId) {
        setSelectedIdState("");
        router.push(chatHref());
      }
    },
    [selectedId, router],
  );

  // "Debug with AI" (F1, ADR 0254): creates a SEPARATE no-fs diagnostic
  // session bound to the picked chat, after the mandated consent dialog —
  // invoking the debugger sends the target's STORED transcript and event
  // evidence (secrets included) to the model, even though the target itself
  // can never be modified. Gated on the daemon's session_debug capability.
  const debugSupported = serverCapabilities.session_debug === true;
  const handleDebugSession = useCallback(
    async (id: string) => {
      // The Labs mock row is local demo content — never a daemon target.
      if (isMockTourSession(id)) return;
      const ok = await confirm({
        title: "Debug with AI",
        description:
          "This creates a separate diagnostic chat bound to this session. " +
          "The session's stored transcript and event evidence — including " +
          "anything sensitive it contains — will be sent to the model as " +
          "debugging evidence. The session itself is read-only to the " +
          "debugger and is never modified.",
        confirmText: "Send evidence & debug",
      });
      if (!ok) return;
      try {
        const debugId = await createHarnessDebugSession(id);
        await refreshSessions();
        handleSelectSession(debugId);
        toast.success("Debug session created");
      } catch (caught) {
        toast.error(caught instanceof Error ? caught.message : String(caught));
      }
    },
    [confirm, refreshSessions, handleSelectSession],
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

  // Flattened, in-display-order chat ids for keyboard navigation.
  const navOrder = useMemo(
    () => groups.flatMap((g) => g.sessions.map((s) => s.id)),
    [groups],
  );

  const navigateBy = useCallback(
    (forward: boolean) => {
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
  useShortcut("chat.toggleList", () => setSidebarOpen((o) => !o));
  useShortcut("chat.next", () => navigateBy(true));
  useShortcut("chat.next.vim", () => navigateBy(true));
  useShortcut("chat.prev", () => navigateBy(false));
  useShortcut("chat.prev.vim", () => navigateBy(false));

  const sidebarContentProps = {
    onNewChat: handleNewChat,
    isLoading: sessionsLoading,
    error: sessionsError,
    groups,
    agents,
    selectedId,
    onSelect: handleSelectSession,
    actions: sessionActions,
    showMockProjects: mockFeatures,
  };

  const dialogs = (
    <>
      {ConfirmDialog}
      {PromptDialog}
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
        toast.error(
          caught instanceof ThreadSourceBusyError
            ? "Wait for the current response to finish, then switch models."
            : caught instanceof Error
              ? caught.message
              : String(caught),
        );
      }
    },
    [selectedSession, refreshSessions, handleSelectSession],
  );

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
          onRetry={retryLast}
          onSend={sendMessage}
          queuedMessages={queuedMessages}
          onQueueMessage={queueMessage}
          onOpenSession={handleSelectSession}
          onSteerQueued={steerQueued}
          onDeleteQueued={deleteQueued}
          onTakeQueued={takeQueued}
          // Steer is capability-gated (C1.2): absent, mid-run sends queue and
          // the composer's steer action degrades to queue.
          onSteerMessage={steerSupported ? steerMessage : undefined}
          onCancelRun={handleCancelRun}
          onCompact={compactSupported ? handleCompact : undefined}
          contextInfo={
            resolvedModel && resolvedModel.contextWindow > 0
              ? {
                  modelLabel: resolvedModel.modelId,
                  contextWindow: resolvedModel.contextWindow,
                }
              : null
          }
          botName={agentName}
          sidebarOpen={open}
          sidebarSide={sidebarSide}
          onToggleSidebar={onToggle}
          pendingApproval={pendingApproval}
          onRespondApproval={respondToApproval}
          pendingClarification={pendingClarification}
          onRespondClarification={respondToClarification}
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
          onSidePanelOpenChange={
            isMobile ? undefined : handleSidePanelOpenChange
          }
          mode={mode}
          onModeChange={changeMode}
          models={modelOptions}
          autoModelLabel={routingEnabled ? "Auto-routed" : "Default model"}
          onSwitchModel={handleSwitchModel}
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
            onSend={sendMessage}
            seed={draftSeed}
            onSeedConsumed={clearDraftSeed}
            onPickSeed={setDraftSeed}
            error={turnError}
            showSidebarButton
            sidebarSide={sidebarSide}
            onShowSidebar={() => setSidebarOpen(true)}
            mode={mode}
            onModeChange={changeMode}
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
            onSend={sendMessage}
            seed={draftSeed}
            onSeedConsumed={clearDraftSeed}
            onPickSeed={setDraftSeed}
            error={turnError}
            showSidebarButton={!sidebarOpen}
            sidebarSide={sidebarSide}
            onShowSidebar={() => setSidebarOpen(true)}
            mode={mode}
            onModeChange={changeMode}
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
