"use client";

import {
  AlertCircle,
  ArrowLeft,
  Bug,
  CirclePlus,
  Copy,
  Ellipsis,
  FileText,
  FoldVertical,
  ListTree,
  Loader2,
  MessageCircle,
  MessageSquareText,
  Network,
  PanelLeftClose,
  PanelLeftOpen,
  PanelRightClose,
  PanelRightOpen,
  Pencil,
  Plug,
  Trash2,
  Wrench,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  type AgentMessage,
  type AgentSession,
  type ApprovalChoice,
  type ApprovalRequest,
  type Artifact,
  type Attachment,
  type AuthorizationRequest,
  type ClarificationRequest,
  type DelegationFleet,
  type DelegationInfo,
  fleetCounts,
  type ToolCallInfo,
  useAgentChat,
} from "@/features/agent";
import type {
  BuiltinGates,
  BuiltinOutcome,
  StudioBuiltinCommand,
} from "@/features/agent/composer-builtins";
import {
  mergeQueued,
  type PendingSteer,
  type QueuedMessage,
  type QueuePause,
} from "@/features/agent/hooks/use-agent-chat";
import type { WorkspaceEnrollmentView } from "@/features/agent/hooks/use-workspace-enrollment";
import { isMockTourSession } from "@/features/agent/mock-tour";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import type { SteerTraceEntry } from "@/features/agent/steer-trace";
import type { StatusMessage } from "@/features/agent/stop-reason";
import { cacheHitRate, formatPercent } from "@/features/agent/turn-stats";
import { useIsMobile } from "@/hooks/use-mobile";
import type { MediaCapabilities } from "@/lib/attachment-inline";
import { formatTokens } from "@/lib/formatters";
import {
  createThreadHarnessSession,
  ThreadSourceBusyError,
} from "@/lib/harness/client";
import {
  type SessionListSide,
  useEnterSendBehavior,
  useExpandDetails,
  useShowToolCalls,
} from "@/lib/profile-preferences";
import type { SessionPermissionMode } from "@/lib/protocol";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { buildStatusFacts } from "@/lib/statusline/facts";
import {
  composeThreadPrompt,
  getThreadSession,
  registerThreadSession,
  sliceThreadReplies,
  stripRootQuote,
  syncThreadActivity,
  threadKeyForMessage,
  threadTitleFromRoot,
  unregisterThreadSession,
  useThreadMap,
} from "@/lib/thread-map";
import { toolDisplayName } from "@/lib/tool-names";
import type { SessionToolProfile } from "@/lib/tool-profile";
import { type ChangedFile, changedFilesFromMessages } from "@/lib/tool-summary";
import { cn } from "@/lib/utils";
import {
  ChatInput,
  type ComposerModelOption,
} from "../../_components/chat-input";
import { ApprovalDetailPanel } from "./approval-detail-panel";
import { ApprovalPanel } from "./approval-panel";
import { AuthorizationPanel } from "./authorization-panel";
import { ChangedFilesPanel } from "./changed-files-panel";
import { ChatStatusStrip, type ModelResolution } from "./chat-status-strip";
import {
  type AwaitingPhase,
  awaitingPhaseFor,
  awaitingPhaseLabel,
  streamingSpinnerLabel,
} from "./chat-view-phase";
import { ClarificationPanel } from "./clarification-panel";
import {
  ClearConversationMenuItem,
  ClearConversationSheetItem,
} from "./clear-conversation-menu-item";
import {
  type DelegationFocus,
  DelegationPanel,
  type DelegationTab,
  focusForDelegationCard,
  preferredDelegationTab,
} from "./delegation-panel";
import { FilePreview } from "./file-preview";
import { FleetStatusChip } from "./fleet-status-chip";
import { MarkdownCanvasPanel } from "./markdown-canvas-panel";
import {
  MCP_PANEL_TITLE,
  McpPanel,
  mcpPanelAvailable,
  useOpenMcpPanelRequests,
} from "./mcp-panel";
import { MessageBubble } from "./message-bubble";
import { MockProviderNotice } from "./mock-provider-notice";
import { PermissionModeBadge } from "./permission-mode-badge";
import { QueuedMessageStrip } from "./queued-message-strip";
import { PAGE_FRACTION, scrollPositionPercent } from "./scroll-position";
import { ScrollToBottomPill } from "./scroll-to-bottom-pill";
import {
  CopySessionIdMenuItem,
  CopySessionIdSheetItem,
} from "./session-copy-menu-items";
import {
  SessionDetailsMenuItem,
  SessionDetailsSheetItem,
} from "./session-details-menu-item";
import {
  ForkChatMenuItem,
  ForkChatSheetItem,
} from "./session-row-action-items";
import { SidePanel } from "./side-panel";
import { StatusLine } from "./status-line";
import { SteerTraceLine } from "./steer-trace-line";
import { streamingPhaseLabel } from "./streaming-phase";
import {
  SwitchWorktreeMenuItem,
  SwitchWorktreeSheetItem,
} from "./switch-worktree-menu-item";
import { TemplatedStatusLine } from "./templated-status-line";
import { ToolCallPanel } from "./tool-call-panel";
import {
  CopyTranscriptMenuItem,
  type TranscriptActionProps,
  TranscriptMenuItems,
  TranscriptSheetItems,
} from "./transcript-actions";
import { isSelectAllChord, selectElementContents } from "./transcript-text";
import { TurnErrorStrip } from "./turn-error-strip";
import { useApprovalShortcuts } from "./use-approval-shortcuts";
import { useComposerEscape } from "./use-composer-escape";
import { WorkspaceEnrollmentNotice } from "./workspace-enrollment-notice";

/** The authorization card's fallback when a caller wires no handler. */
const noAuthorizationAction = async () => {};

/** The single right-hand panel: exactly one kind is open at a time, or none. */
type ActivePanel =
  | { kind: "artifact"; artifact: Artifact }
  | { kind: "attachment"; attachment: Attachment }
  | { kind: "thread"; message: AgentMessage }
  | { kind: "toolcall"; call: ToolCallInfo }
  // The full-height view of the pending ask (the TUI's ctrl+t args view).
  | { kind: "approval"; approval: ApprovalRequest }
  // Every path this conversation's Edit/Write calls touched (the TUI's
  // "N files changed" appendix); the list is derived live from `messages`.
  | { kind: "changed-files" }
  // The Agents panel reads the live `fleet` prop; it keeps only its tab and
  // the child/group/view it is drilled into (Esc steps a focus back first).
  | { kind: "delegation"; tab: DelegationTab; focus: DelegationFocus | null }
  // The MCP tools inventory (the TUI's /mcp panel): this chat's broker
  // connectors and the daemon's resolved sources; it keeps no state of its own.
  | { kind: "mcp" };

/**
 * Bottom-of-transcript activity line while a turn is running: three
 * staggered pulsing dots, a phase label derived from the streaming
 * assistant message (thinking / running tools / writing), and elapsed time.
 */
function StreamingIndicator({
  message,
  awaiting,
}: {
  message?: AgentMessage;
  /** The run is parked on a permission ask: the phase names the wait. */
  awaiting?: AwaitingPhase;
}) {
  const [elapsed, setElapsed] = useState(0);
  const startedAt = message?.timestamp;
  useEffect(() => {
    if (!startedAt) return;
    const tick = () =>
      setElapsed(Math.max(0, Math.round((Date.now() - startedAt) / 1000)));
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [startedAt]);

  // Names the running tool ("Running Read"), like the TUI footer — unless
  // the run is parked on the operator, when the wait is what to say.
  const phase = awaitingPhaseLabel(awaiting) ?? streamingPhaseLabel(message);
  const time =
    elapsed >= 60
      ? `${Math.floor(elapsed / 60)}m ${elapsed % 60}s`
      : `${elapsed}s`;

  return (
    // Mirrors the message-row geometry (avatar column + gap) so the label
    // lines up with message text; the dots sit centered in the avatar slot.
    <div className="flex items-center gap-2 py-2 lg:gap-3">
      <span
        className="flex w-7 shrink-0 items-center justify-center gap-0.5 lg:w-9"
        aria-hidden="true"
      >
        {[0, 1, 2].map((i) => (
          <span
            key={i}
            className="size-1 animate-[thinking-bounce_0.9s_infinite] rounded-full bg-brand"
            style={{ animationDelay: `${i * 160}ms` }}
          />
        ))}
      </span>
      <span className="text-sm text-muted-foreground lg:text-[15px]">
        {phase}
        <span className="mx-1.5 text-muted-foreground/50">·</span>
        <span className="tabular-nums text-muted-foreground/70">{time}</span>
      </span>
    </div>
  );
}

/** The session's token figures the menus and the context strip render. */
type UsageFigures = {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  reasoningTokens?: number;
};

/**
 * Token counts as a read-only info row inside the chat context menus: every
 * non-zero facet (input, output, cache read, cache write, reasoning) plus
 * the cache-hit rate once anything was read from cache.
 */
function UsageMenuRow({ usage }: { usage?: UsageFigures | null }) {
  if (!usage || usage.inputTokens + usage.outputTokens <= 0) return null;
  const facets: [string, number][] = [
    ["input", usage.inputTokens],
    ["output", usage.outputTokens],
    ["cache read", usage.cacheReadTokens ?? 0],
    ["cache write", usage.cacheWriteTokens ?? 0],
    ["reasoning", usage.reasoningTokens ?? 0],
  ];
  const hitRate = cacheHitRate(usage);
  return (
    <div className="mb-1 border-b border-border/60 px-3 py-2">
      <p className="text-xs font-medium text-muted-foreground">Token usage</p>
      <div className="mt-1">
        {facets
          .filter(([, count]) => count > 0)
          .map(([label, count]) => (
            <p key={label} className="text-sm tabular-nums">
              {formatTokens(count)} {label}
            </p>
          ))}
        {hitRate > 0 && (
          <p className="text-sm tabular-nums">
            {formatPercent(hitRate)} cache hit rate
          </p>
        )}
      </div>
    </div>
  );
}

/** A composer re-seed: text plus the staged files that travel with it. */
type ComposerSeed = { text: string; files?: File[] };

function MobileChatMenu({
  showActivity,
  onToggleActivity,
  expandDetails = false,
  onToggleExpandDetails,
  onRename,
  onDelete,
  onOpenDetails,
  onCompact,
  compactDisabled,
  onInjectDebugAsk,
  onClear,
  clearDisabledReason,
  onSwitchWorktree,
  session,
  onFork,
  usage,
  transcript,
}: {
  showActivity: boolean;
  onToggleActivity: () => void;
  /** Developer tools (Settings → Labs): parks a FAKE permission ask. */
  onInjectDebugAsk?: () => void;
  /** The global Expand details preference and its flip (the TUI's ctrl+t). */
  expandDetails?: boolean;
  onToggleExpandDetails?: () => void;
  onRename?: () => void;
  onDelete?: () => void;
  /** Opens the session details dialog (the `/session` built-in's). */
  onOpenDetails?: () => void;
  /** Manual compaction (B1.2): present only when the daemon supports it. */
  onCompact?: () => void;
  /** True while a run streams — the daemon 412s a mid-run compact. */
  compactDisabled?: boolean;
  /** Clear conversation (the `/clear` handoff): present for a chat row. */
  onClear?: () => void;
  /** Non-empty renders Clear conversation disabled with this reason. */
  clearDisabledReason?: string;
  /** Opens the worktree picker (the `/worktrees` built-in): present only
   *  when the daemon lists worktrees and the row may mint a successor. */
  onSwitchWorktree?: () => void;
  /** The open session: the copy-ID and fork rows gate on its capabilities. */
  session: AgentSession;
  /** Fork the chat as-is (the TUI's `f`): present when the row may offer it. */
  onFork?: () => void;
  usage?: UsageFigures | null;
  /** Select / copy the whole conversation (the TUI's ctrl+g / ctrl+y). */
  transcript?: TranscriptActionProps;
}) {
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        className="size-8 shrink-0 text-muted-foreground"
        onClick={() => setOpen(true)}
      >
        <Ellipsis className="size-4" />
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="bottom" className="p-0">
          <SheetTitle className="sr-only">Chat options</SheetTitle>
          <div className="py-2">
            <UsageMenuRow usage={usage} />
            <button
              type="button"
              onClick={() => {
                onToggleActivity();
                setOpen(false);
              }}
              className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors"
            >
              <Wrench className="size-4 text-muted-foreground" />
              {showActivity ? "Hide Tools" : "Show Tools"}
            </button>
            {onToggleExpandDetails && (
              <button
                type="button"
                onClick={() => {
                  onToggleExpandDetails();
                  setOpen(false);
                }}
                className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors"
              >
                <ListTree className="size-4 text-muted-foreground" />
                {expandDetails ? "Collapse details" : "Expand details"}
              </button>
            )}
            {onCompact && (
              <button
                type="button"
                disabled={compactDisabled}
                onClick={() => {
                  onCompact();
                  setOpen(false);
                }}
                className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors disabled:opacity-50"
              >
                <FoldVertical className="size-4 text-muted-foreground" />
                Compact conversation
              </button>
            )}
            {onInjectDebugAsk && (
              <button
                type="button"
                onClick={() => {
                  onInjectDebugAsk();
                  setOpen(false);
                }}
                className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors"
              >
                <Bug className="size-4 text-muted-foreground" />
                Inject fake approval
              </button>
            )}
            {onClear && (
              <ClearConversationSheetItem
                onSelect={onClear}
                disabledReason={clearDisabledReason}
                onDone={() => setOpen(false)}
              />
            )}
            {onSwitchWorktree && (
              <SwitchWorktreeSheetItem
                onSelect={onSwitchWorktree}
                onDone={() => setOpen(false)}
              />
            )}
            {transcript && (
              <TranscriptSheetItems
                {...transcript}
                onDone={() => setOpen(false)}
              />
            )}
            {onOpenDetails && (
              <SessionDetailsSheetItem
                onSelect={onOpenDetails}
                onDone={() => setOpen(false)}
              />
            )}
            <CopySessionIdSheetItem
              session={session}
              onDone={() => setOpen(false)}
            />
            {onFork && (
              <ForkChatSheetItem
                session={session}
                onSelect={onFork}
                onDone={() => setOpen(false)}
              />
            )}
            {onRename && (
              <button
                type="button"
                onClick={() => {
                  onRename();
                  setOpen(false);
                }}
                className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-muted/50 transition-colors"
              >
                <Pencil className="size-4 text-muted-foreground" />
                Rename
              </button>
            )}
            {onDelete && (
              <button
                type="button"
                onClick={() => {
                  onDelete();
                  setOpen(false);
                }}
                className="flex w-full items-center gap-3 px-4 py-3 text-sm text-muted-foreground hover:bg-muted/50 transition-colors"
              >
                <Trash2 className="size-4" />
                Delete
              </button>
            )}
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}

function AttachmentPanel({
  attachment,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  attachment: Attachment;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  return (
    <SidePanel
      icon={FileText}
      title={attachment.name}
      closeLabel="Close file"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      windowControls={windowControls}
    >
      <div className="flex-1 overflow-auto">
        <FilePreview
          name={attachment.name}
          content={attachment.content}
          url={attachment.url}
        />
      </div>
    </SidePanel>
  );
}

/**
 * A side-panel thread branched off a single message, backed by a REAL daemon
 * session seeded with the parent conversation (source_session_id carryover).
 * The root message is shown read-only at the top; replies genuinely converse
 * with the agent, streaming through the same chat hook as the main view. The
 * thread session is minted lazily on the first send, renamed "Thread: …",
 * and remembered in the browser-local thread map so reopening the thread
 * rehydrates its transcript. It also shows up in the sidebar under that
 * name, which is the escape hatch for anything the panel keeps minimal.
 */
function ThreadPanel({
  parentSessionId,
  rootMessage,
  botName,
  onClose,
  onConvertToChat,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  parentSessionId: string;
  rootMessage: AgentMessage;
  botName: string;
  onClose: () => void;
  /** Detach the thread and open its session as an ordinary chat. */
  onConvertToChat?: (threadSessionId: string) => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  // The global Show Tools preference — shared with the chat's ··· menu.
  const { showToolCalls: showTools, setShowToolCalls } = useShowToolCalls();
  // "Queue only" (Settings → Personalize) withdraws the queued row's Steer
  // action here too — the thread's strip is the second QueuedMessageStrip.
  const { behavior: threadEnterBehavior } = useEnterSendBehavior();
  const threadSteerAllowed = threadEnterBehavior !== "queue-only";
  const rootKey = threadKeyForMessage(rootMessage);
  // The persisted thread session, when this root message already has one —
  // the hook rehydrates its transcript. A session minted DURING this panel's
  // lifetime deliberately does NOT re-key the hook: it is adopted via
  // adoptSession instead (the draft-minting pattern), because re-keying
  // would refetch the transcript mid-stream and wipe the optimistic messages.
  const [initialThreadId] = useState<string | null>(() =>
    getThreadSession(parentSessionId, rootKey),
  );
  const threadIdRef = useRef<string | null>(initialThreadId);
  // The parent was mid-run (daemon 412): the seeded fork has to wait.
  const [sourceBusy, setSourceBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  // Re-seeds the composer after a refused first send (keep the text) or a
  // queued-message edit (text AND its staged files come back).
  const [seedText, setSeedText] = useState<string | null>(null);
  const [seedFiles, setSeedFiles] = useState<File[] | undefined>(undefined);
  const threadScrollRef = useRef<HTMLDivElement>(null);
  const endRef = useRef<HTMLDivElement>(null);

  const {
    messages,
    isStreaming,
    status,
    error,
    sendMessage,
    adoptSession,
    queuedMessages,
    queueMessage,
    deleteQueued,
    takeQueued,
    steerQueued,
    pendingApproval,
    approvalQueueLength,
    respondToApproval,
    pendingAuthorization,
    openAuthorization,
    copyAuthorizationLink,
    recheckAuthorization,
    cancelAuthorization,
    statusMessage,
  } = useAgentChat(initialThreadId);

  // The thread session's history starts with the seeded parent conversation;
  // only the thread's own exchange (from the quoted first message) renders.
  const replies = useMemo(
    () =>
      stripRootQuote(
        sliceThreadReplies(messages, rootMessage.content),
        rootMessage.content,
      ),
    [messages, rootMessage.content],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: the scroll follows every new reply by design
  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [replies.length]);

  // Mirror the thread's activity into the persisted map so the parent
  // transcript's reply indicator stays live (count + last-reply time).
  useEffect(() => {
    if (!threadIdRef.current || replies.length === 0) return;
    const countable = replies.filter(
      (reply) =>
        reply.role === "user" ||
        Boolean(reply.content.trim()) ||
        Boolean(reply.failed),
    );
    if (countable.length === 0) return;
    let lastAt = 0;
    for (const reply of countable) {
      if (reply.timestamp > lastAt) lastAt = reply.timestamp;
    }
    syncThreadActivity(parentSessionId, rootKey, countable.length, lastAt);
  }, [replies, parentSessionId, rootKey]);

  const handleSend = useCallback(
    async (content: string, files?: File[]) => {
      // Defense in depth: a mock session id must never mint a daemon thread
      // session (the panel router already sends mock threads elsewhere).
      if (isMockTourSession(parentSessionId)) return;
      setSourceBusy(false);
      setCreateError(null);
      if (threadIdRef.current) {
        void sendMessage(content, files);
        return;
      }
      // First send: mint the seeded thread session up front — the hook's own
      // lazy mint would create an UNSEEDED session with no parent context.
      try {
        const threadId = await createThreadHarnessSession(
          parentSessionId,
          threadTitleFromRoot(rootMessage.content),
        );
        threadIdRef.current = threadId;
        adoptSession(threadId);
        registerThreadSession(parentSessionId, rootKey, threadId);
      } catch (caught) {
        // A refused first send keeps the text: re-seed the composer with it.
        setSeedText(content);
        if (caught instanceof ThreadSourceBusyError) {
          setSourceBusy(true);
        } else {
          setCreateError(
            caught instanceof Error ? caught.message : String(caught),
          );
        }
        return;
      }
      void sendMessage(
        composeThreadPrompt(rootMessage.content, content),
        files,
      );
    },
    [parentSessionId, rootKey, rootMessage.content, sendMessage, adoptSession],
  );

  // Editing a queued reply pulls it out of the queue into the composer,
  // attachments included.
  const handleEditQueued = (id: string) => {
    const hit = takeQueued(id);
    if (!hit) return;
    setSeedText(hit.text || null);
    setSeedFiles(hit.files);
  };

  return (
    <SidePanel
      icon={MessageSquareText}
      title="Thread"
      closeLabel="Close thread"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      minWidth={340}
      windowControls={windowControls}
      headerExtra={
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="size-7 shrink-0 text-muted-foreground"
              aria-label="Thread options"
            >
              <Ellipsis className="size-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={() => setShowToolCalls(!showTools)}>
              {showTools ? "Hide Tools" : "Show Tools"}
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={!onConvertToChat}
              onClick={() => {
                const threadId = threadIdRef.current;
                if (!threadId || !onConvertToChat) return;
                unregisterThreadSession(parentSessionId, threadId);
                onConvertToChat(threadId);
              }}
            >
              Open as full chat
            </DropdownMenuItem>
            <CopyTranscriptMenuItem
              messages={[rootMessage, ...replies]}
              botName={botName}
              label="Copy thread"
              successMessage="Thread copied"
            />
          </DropdownMenuContent>
        </DropdownMenu>
      }
    >
      {/* Body: root message, live replies, composer */}
      <div className="relative flex-1 min-h-0">
        <div
          ref={threadScrollRef}
          className="h-full overflow-y-auto px-3 pt-3 pb-40 max-[499px]:pb-24 lg:px-4"
        >
          <div className="rounded-lg border border-dashed border-border/70 px-1 py-1">
            <MessageBubble message={rootMessage} botName={botName} />
          </div>
          {replies.length > 0 && (
            <div className="my-2 flex items-center gap-2 px-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/70">
              <span className="h-px flex-1 bg-border" />
              {replies.length} {replies.length === 1 ? "reply" : "replies"}
              <span className="h-px flex-1 bg-border" />
            </div>
          )}
          {replies.map((reply) => (
            <MessageBubble
              key={reply.id}
              message={reply}
              botName={botName}
              showActivity={showTools}
            />
          ))}
          {pendingApproval && (
            <ApprovalPanel
              approval={pendingApproval}
              onRespond={respondToApproval}
              queuePosition={
                approvalQueueLength
                  ? { index: 1, total: approvalQueueLength }
                  : undefined
              }
            />
          )}
          {pendingAuthorization && (
            <AuthorizationPanel
              authorization={pendingAuthorization}
              onOpen={openAuthorization}
              onCopyLink={copyAuthorizationLink}
              onRecheck={recheckAuthorization}
              onCancel={cancelAuthorization}
            />
          )}
          {isStreaming && !pendingAuthorization && (
            <StreamingIndicator
              awaiting={awaitingPhaseFor(pendingApproval)}
              message={
                replies[replies.length - 1]?.role === "assistant"
                  ? replies[replies.length - 1]
                  : undefined
              }
            />
          )}
          {!isStreaming && statusMessage && (
            <StatusLine status={statusMessage} />
          )}
          <div ref={endRef} />
        </div>
        <TextSelectionToolbar
          containerRef={threadScrollRef}
          onAddToChat={(text) =>
            setSeedText((prev) => {
              const quoted = `${text
                .split("\n")
                .map((line) => `> ${line}`)
                .join("\n")}\n\n`;
              return prev ? prev + quoted : quoted;
            })
          }
          addLabel="Add to thread"
        />
        <div className="absolute bottom-0 left-0 right-0 px-3 lg:px-4 pb-4 max-[499px]:px-0 max-[499px]:pb-0">
          <div className="space-y-1.5">
            <QueuedMessageStrip
              queued={queuedMessages}
              isStreaming={isStreaming}
              onSteer={threadSteerAllowed ? steerQueued : undefined}
              onEdit={handleEditQueued}
              onDelete={deleteQueued}
            />
            {sourceBusy && (
              <p className="px-2 text-xs text-muted-foreground">
                Wait for the current response to finish before starting a
                thread.
              </p>
            )}
            {createError && (
              <p className="px-2 text-xs text-destructive break-words">
                {createError}
              </p>
            )}
            {status === "error" && error && (
              <div className="flex items-center gap-2 rounded-lg border border-destructive/40 bg-background bg-gradient-to-b from-destructive/5 to-destructive/5 px-3 py-2">
                <AlertCircle className="size-4 shrink-0 text-destructive" />
                <p className="min-w-0 flex-1 text-sm text-destructive break-words">
                  {error}
                </p>
              </div>
            )}
            <MockProviderNotice />
            <ChatInput
              onSend={handleSend}
              onQueue={queueMessage}
              isStreaming={isStreaming}
              disabled={!!pendingApproval}
              initialText={seedText}
              initialFiles={seedFiles}
              onInitialTextConsumed={() => {
                setSeedText(null);
                setSeedFiles(undefined);
              }}
              onModelChange={() => {}}
              placeholder={isStreaming ? "Queue a reply…" : "Reply in thread…"}
              mobileDocked
            />
          </div>
        </div>
      </div>
    </SidePanel>
  );
}

/**
 * The thread panel for the Labs mock chat: a read-only view of the root
 * message's canned replies. Entirely local — a mock session id must never
 * mint a daemon thread session, so this replaces ThreadPanel outright.
 */
function MockThreadPanel({
  rootMessage,
  botName,
  onClose,
  maximized,
  onToggleMaximize,
  windowControls,
}: {
  rootMessage: AgentMessage;
  botName: string;
  onClose: () => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
}) {
  const replies = rootMessage.replies ?? [];
  return (
    <SidePanel
      icon={MessageSquareText}
      title="Thread"
      closeLabel="Close thread"
      maximized={maximized}
      onToggleMaximize={onToggleMaximize}
      onClose={onClose}
      minWidth={340}
      windowControls={windowControls}
    >
      <div className="flex-1 min-h-0 overflow-y-auto px-3 pt-3 pb-4 lg:px-4">
        <div className="rounded-lg border border-dashed border-border/70 px-1 py-1">
          <MessageBubble message={rootMessage} botName={botName} />
        </div>
        {replies.length > 0 && (
          <div className="my-2 flex items-center gap-2 px-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/70">
            <span className="h-px flex-1 bg-border" />
            {replies.length} {replies.length === 1 ? "reply" : "replies"}
            <span className="h-px flex-1 bg-border" />
          </div>
        )}
        {replies.map((reply) => (
          <MessageBubble key={reply.id} message={reply} botName={botName} />
        ))}
        <p className="px-2 pt-2 text-xs text-muted-foreground">
          Mock thread &mdash; read-only demo content.
        </p>
      </div>
    </SidePanel>
  );
}

function TextSelectionToolbar({
  containerRef,
  onAddToChat,
  onAskInSideChat,
  addLabel = "Add to chat",
  askLabel = "Ask in a chat thread",
}: {
  containerRef: React.RefObject<HTMLElement | null>;
  onAddToChat: (text: string) => void;
  /** Omitted = the toolbar offers only the add action (the thread panel). */
  onAskInSideChat?: (text: string) => void;
  addLabel?: string;
  askLabel?: string;
}) {
  const [pos, setPos] = useState<{ x: number; y: number } | null>(null);
  const [selectedText, setSelectedText] = useState("");
  const toolbarRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const handleMouseUp = () => {
      requestAnimationFrame(() => {
        const selection = window.getSelection();
        const text = selection?.toString().trim();
        if (!text || text.length < 3) {
          setPos(null);
          return;
        }

        const range = selection?.getRangeAt(0);
        if (!range) return;

        if (!container.contains(range.commonAncestorContainer)) {
          setPos(null);
          return;
        }

        const rect = range.getBoundingClientRect();
        const containerRect = container.getBoundingClientRect();

        setSelectedText(text);
        setPos({
          x: rect.left + rect.width / 2 - containerRect.left,
          y: rect.top - containerRect.top - 8,
        });
      });
    };

    const handleMouseDown = (e: MouseEvent) => {
      if (toolbarRef.current?.contains(e.target as Node)) return;
      setPos(null);
    };

    container.addEventListener("mouseup", handleMouseUp);
    document.addEventListener("mousedown", handleMouseDown);
    return () => {
      container.removeEventListener("mouseup", handleMouseUp);
      document.removeEventListener("mousedown", handleMouseDown);
    };
  }, [containerRef]);

  if (!pos) return null;

  return (
    <div
      ref={toolbarRef}
      className="absolute z-50 flex items-center gap-0.5 rounded-lg border bg-popover px-1 py-0.5 shadow-lg"
      style={{
        left: pos.x,
        top: pos.y,
        transform: "translate(-50%, -100%)",
      }}
    >
      <button
        type="button"
        onClick={() => {
          onAddToChat(selectedText);
          setPos(null);
          window.getSelection()?.removeAllRanges();
        }}
        className="flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-foreground hover:bg-muted transition-colors whitespace-nowrap"
      >
        <MessageCircle className="size-3.5" />
        {addLabel}
      </button>
      {onAskInSideChat && (
        <>
          <div className="w-px h-4 bg-border" />
          <button
            type="button"
            onClick={() => {
              onAskInSideChat(selectedText);
              setPos(null);
              window.getSelection()?.removeAllRanges();
            }}
            className="flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm text-foreground hover:bg-muted transition-colors whitespace-nowrap"
          >
            <CirclePlus className="size-3.5" />
            {askLabel}
          </button>
        </>
      )}
    </div>
  );
}

export function ChatView({
  session,
  messages,
  isStreaming,
  onSend,
  botName,
  live = false,
  usage,
  contextOccupancy = 0,
  statusMessage = null,
  error,
  onRetry,
  lastFailurePermanent = false,
  onNewChat,
  onEditFailed,
  recoverDraft = null,
  onRecoverDraftConsumed,
  sidebarOpen,
  sidebarSide,
  onToggleSidebar,
  pendingApproval,
  approvalQueueLength,
  onRespondApproval,
  pendingClarification,
  onRespondClarification,
  pendingAuthorization = null,
  onOpenAuthorization,
  onCopyAuthorizationLink,
  onRecheckAuthorization,
  onCancelAuthorization,
  onRename,
  onDelete,
  onFork,
  onOpenDetails,
  onSidePanelOpenChange,
  initialDraft,
  onInitialDraftConsumed,
  queuedMessages = [],
  onQueueMessage,
  onOpenSession,
  onSteerQueued,
  onDeleteQueued,
  onTakeQueued,
  onTakeAllQueued,
  onClearQueue,
  queuePaused,
  onResumeQueue,
  pendingSteers,
  steerTrace,
  onSteerMessage,
  onRetractSteers,
  onInjectDebugAsk,
  onCancelRun,
  onDraftChange,
  onCompact,
  onClear,
  clearDisabledReason,
  onSwitchWorktree,
  contextInfo,
  modelResolution = "ok",
  providerRoute,
  pendingMode,
  modeSwitchDeferred = false,
  debugMcpServers,
  debugMcpTools,
  readOnlyPlaceholder,
  mode,
  onModeChange,
  profile,
  models,
  autoModelLabel,
  onSwitchModel,
  onSwitchEffort,
  currentEffort,
  currentModelReasoning,
  effortSupported,
  onLocalCommand,
  builtinGates,
  mediaCapabilities,
  fleet,
  teamsSupported,
  onCancelChild,
  onInspectChild,
  enrollment = null,
}: {
  session: AgentSession;
  messages: AgentMessage[];
  isStreaming: boolean;
  onSend: (content: string, files?: File[]) => void;
  botName: string;
  /** True while the daemon connection is up. */
  live?: boolean;
  usage?: UsageFigures;
  /** The last turn failed with this text; rendered as an inline strip. */
  error?: string | null;
  onRetry?: () => void;
  /** The daemon typed that failure PERMANENT: the strip withholds Retry (the
      identical request is rejected) and offers New chat instead. */
  lastFailurePermanent?: boolean;
  onNewChat?: () => void;
  /** The strip's Edit: takes back the prompt a run-entry failure refused
      (the hook drops the failed exchange and clears the failure) and returns
      its text for the composer; null when nothing is held. Absent = no Edit
      button. */
  onEditFailed?: () => string | null;
  /** A text-only prompt a transport fault dropped: seeded into the composer
      for edit-before-resend, then reported consumed. */
  recoverDraft?: ComposerSeed | null;
  onRecoverDraftConsumed?: () => void;
  sidebarOpen: boolean;
  sidebarSide: SessionListSide;
  onToggleSidebar: () => void;
  pendingApproval: ApprovalRequest | null;
  /** Asks queued behind the daemon, head included: the panel's "1 of N". */
  approvalQueueLength?: number;
  onRespondApproval: (choice: ApprovalChoice) => void;
  pendingClarification: ClarificationRequest | null;
  onRespondClarification: (response: string) => void;
  /** The MCP browser authorization the run is parked on: the takeover card
      replaces the composer until the sign-in is confirmed or cancelled. */
  pendingAuthorization?: AuthorizationRequest | null;
  onOpenAuthorization?: () => Promise<unknown>;
  onCopyAuthorizationLink?: () => Promise<unknown>;
  onRecheckAuthorization?: () => Promise<unknown>;
  onCancelAuthorization?: () => Promise<unknown>;
  onRename?: () => void;
  onDelete?: () => void;
  /** Fork the open chat as-is (the TUI's `f`): a copy on the same model,
      effort and placement. The menu item gates itself on the row's `fork`
      capability; the workspace withholds it for an AI-debug chat. */
  onFork?: () => void;
  /** Opens the session details dialog (exact id + Copy, state, model,
      placement…) — the `/session` built-in's dialog, owned by the workspace.
      Offered for every real (non-mock) selected session. */
  onOpenDetails?: () => void;
  /** Fires when the right-hand side panel (artifact/attachment) opens or
      closes, so the parent can collapse the chat list while it's open. */
  onSidePanelOpenChange?: (open: boolean) => void;
  /** Plain-text prompt to pre-fill the composer with on mount (a "next step"
      chip that seeds the message without sending it). */
  initialDraft?: string | null;
  onInitialDraftConsumed?: () => void;
  /** Messages held while a run is active (see QueuedMessageStrip). */
  queuedMessages?: QueuedMessage[];
  /** Holds a message (text plus its staged files) for the next run. */
  onQueueMessage?: (text: string, files?: File[]) => void;
  /** Open a session as the main chat (thread → full chat conversion). */
  onOpenSession?: (sessionId: string) => void;
  onSteerQueued?: (id: string) => void;
  onDeleteQueued?: (id: string) => void;
  /** Removes a queued message and returns its text and files (the Edit
      action re-seeds both into the composer). */
  onTakeQueued?: (id: string) => ComposerSeed | null;
  /** Pulls the WHOLE queue back as one merged draft (Edit all / ↑ on an
      empty composer); the queue empties. */
  onTakeAllQueued?: () => ComposerSeed | null;
  /** Drops every held message (Clear all / Esc on an empty idle composer). */
  onClearQueue?: () => void;
  /** Non-null while the queue is held after a non-clean stop (cancel, a
      failed turn, a lost connection); the strip shows the reason. */
  queuePaused?: QueuePause | null;
  /** Sends the held queue as one prompt and lifts the pause (Send now /
      Enter on an empty idle composer). */
  onResumeQueue?: () => void;
  /** Steers the daemon accepted but has not yet applied to the run. */
  pendingSteers?: PendingSteer[];
  /** Injects composer text (plus staged image attachments, ADR 0251) into
      the in-flight run at the next step. Absent when the daemon lacks the
      steer capability — mid-run sends then queue. */
  onSteerMessage?: (text: string, files?: File[]) => void;
  /** Retracts the whole pending steer bundle; resolves to the steers that
      never reached the run, so their text can be recomposed. */
  onRetractSteers?: () => Promise<PendingSteer[]>;
  /** Developer tools (Settings → Labs): the steer correlation trace rendered
      under the queue strip — steer ids, drain watermarks, decisions (the
      TUI's DebugSteer). Absent while the tools are off. */
  steerTrace?: readonly SteerTraceEntry[];
  /** Developer tools: the ··· menu's "Inject fake approval" — parks a FAKE
      permission ask that never reaches the daemon (the `/debug-ask`
      built-in's twin). Absent while the tools are off. */
  onInjectDebugAsk?: () => void;
  /** Cancels the in-flight run (Esc with no panel open). */
  onCancelRun?: () => void;
  /** Fires when the composer's "holds unsent text" state flips (and `false`
      when the composer unmounts); the workspace arms its leave guard from it. */
  onDraftChange?: (hasText: boolean) => void;
  /** Manually compacts the conversation (B1.2); present only when the
      daemon's manual_compaction capability is on. Disabled while streaming. */
  onCompact?: () => void;
  /** Clear conversation — the `/clear` handoff to an empty-history
      successor with the same settings; present for a chat row. */
  onClear?: () => void;
  /** Non-empty renders Clear conversation disabled with this daemon reason. */
  clearDisabledReason?: string;
  /** Switch worktree — opens the `/worktrees` placement picker (a clear or
      fork of this chat rooted at a sibling git worktree); present only when
      the daemon lists worktrees and the row may mint a successor. */
  onSwitchWorktree?: () => void;
  /** The session's effective model + context window (B1.1): feeds the slim
      approximate context meter near the composer. */
  contextInfo?: {
    modelLabel: string;
    contextWindow: number;
    /** resolved_model.reasoning_effort; "" / absent = none echoed. */
    effort?: string;
  } | null;
  /** Whether the snapshot read behind `contextInfo` is pending/landed/failed:
      the status strip says "resolving model…" only while pending. */
  modelResolution?: ModelResolution;
  /** The downstream provider the current/last turn was routed to ("" = none
      reported); the status strip's `model/route` suffix. */
  providerRoute?: string;
  /** A mode switch deferred until the run ends (the strip's "(pending)"). */
  pendingMode?: SessionPermissionMode | null;
  /** True when the caller defers a mid-run mode switch: the composer's Mode
      pill stays enabled while streaming instead of being disabled. */
  modeSwitchDeferred?: boolean;
  /** Reporting MCP servers bound to a debug session (the strip's notice). */
  debugMcpServers?: string[];
  /** The debugger MCP tools those servers mounted (the strip's notice). */
  debugMcpTools?: string[];
  /** The latest turn's input tokens (turn.end): the meter's occupancy. */
  contextOccupancy?: number;
  /** The transient status line under the transcript (a no-progress nudge,
      the recover notice, how the last run stopped); null = nothing to say. */
  statusMessage?: StatusMessage | null;
  /** Disables the composer and shows this placeholder instead (the Labs
      mock chat is read-only demo content). */
  readOnlyPlaceholder?: string;
  /** The session's current permission mode, for the composer's Mode selector. */
  mode?: SessionPermissionMode;
  /** Renders the composer's Mode selector when provided (the mock tour chat
      omits it — a read-only demo has no permission posture to set). */
  onModeChange?: (mode: SessionPermissionMode) => void;
  /** The tool profile this chat was created with ("" | "no-fs"), shown
      read-only inside the composer's Mode menu; undefined = unknown (a chat
      Studio did not mint — the daemon never reports it back). */
  profile?: SessionToolProfile;
  /** Live daemon models for the mid-chat switch picker. */
  models?: ComposerModelOption[];
  autoModelLabel?: string;
  /** Picking a model forks this chat onto it (daemon fixes model at create). */
  onSwitchModel?: (option: ComposerModelOption | null) => void;
  /** Picking an effort tier forks this chat onto it (wire value; "" = auto). */
  onSwitchEffort?: (wire: string) => void;
  /** The session's EFFECTIVE tier (resolved_model.reasoning_effort). */
  currentEffort?: string;
  /** The session's model `reasoning` flag (false → the picker's warning). */
  currentModelReasoning?: boolean;
  /** False when the daemon's model_selection capability is off. */
  effortSupported?: boolean;
  /** Answers a Studio built-in slash command typed in the composer (`/clear
      /help /session /retry /diagnostics /compact`) instead of sending it; a
      refusal keeps the text and shows its warning. */
  onLocalCommand?: (
    command: StudioBuiltinCommand,
  ) => BuiltinOutcome | undefined;
  /** Which gated built-ins the daemon enables (`/compact`). */
  builtinGates?: BuiltinGates;
  /** The session's resolved input modalities — what the composer may stage
      as image/audio attachments (a refused file names its reason). */
  mediaCapabilities?: MediaCapabilities;
  /** Every child this session's runs delegated, across turns: the Agents
      panel's model and the header button's badge. */
  fleet?: DelegationFleet;
  /** The daemon's `teams` capability; false disables the Teams tab when no
      team has run. */
  teamsSupported?: boolean;
  /** Cancels one live delegated child by its session id (the inline cards'
      and the Agents panel's cancel controls; absent = no controls). */
  onCancelChild?: (childId: string) => void | Promise<void>;
  /** Opens one delegated child's stored transcript read-only (the inline
      cards' Inspect control; absent = no control). */
  onInspectChild?: (childId: string, label: string) => void;
  /** The chat's workspace-services enrollment (the TUI's /tools-connect
      notice + actions); null on the mock tour. The notice renders itself
      only while the daemon's capability applies and the enrollment is
      unsettled. */
  enrollment?: WorkspaceEnrollmentView | null;
}) {
  const messagesEndRef = useRef<HTMLDivElement>(null);
  // Whether the transcript is scrolled to (near) the bottom; when it isn't,
  // a floating control above the composer jumps back down. The ref mirror is
  // what the follow effect reads — it must see the value as of the latest
  // scroll, not the latest render.
  const [atBottom, setAtBottom] = useState(true);
  const atBottomRef = useRef(true);
  const messagesContainerRef = useRef<HTMLElement>(null);
  // The messages column alone (no selection toolbar, no composer): the
  // target of the transcript-scoped select-all.
  const transcriptRef = useRef<HTMLDivElement>(null);
  // The TUI's `↑ NN%` cue: how far through the conversation the view sits.
  // Tracked only while unpinned — the floating pill is its sole reader, and
  // re-rendering on every streaming scroll while pinned would be churn.
  const [scrollPercent, setScrollPercent] = useState(100);
  // The global Show Tools preference (persisted; shared with thread panels).
  const { showToolCalls: showActivity, setShowToolCalls } = useShowToolCalls();
  // The global Expand details preference (the TUI's ctrl+t): whether tool
  // rows, reasoning summaries and raw error payloads start expanded.
  const { expandDetails, setExpandDetails } = useExpandDetails();
  // The status-line facts (Settings → Status line): every session, model,
  // context, usage and runtime fact the header and footer templates may
  // render, built from this view's props + the runtime status.
  const runtime = useRuntimeStatus();
  const statusFacts = useMemo(
    () =>
      buildStatusFacts({
        session,
        mode,
        isStreaming,
        awaitingApproval: !!pendingApproval,
        contextInfo,
        contextOccupancy,
        usage,
        queued: queuedMessages.length,
        providerRoute,
        runtime,
      }),
    [
      session,
      mode,
      isStreaming,
      pendingApproval,
      contextInfo,
      contextOccupancy,
      usage,
      queuedMessages.length,
      providerRoute,
      runtime,
    ],
  );
  // Every path this conversation's Edit/Write calls touched, first-seen
  // order — the header's "N files" indicator and the changed-files panel.
  const changedFiles = useMemo(
    () => changedFilesFromMessages(messages),
    [messages],
  );
  // The single right-hand panel — a discriminated union makes "one panel at a
  // time" structural rather than something to coordinate by hand.
  const [panel, setPanel] = useState<ActivePanel | null>(null);
  // The composer holds unsent text (reported by ChatInput's onDraftChange):
  // the Esc layering's fourth arm reads it here, and the workspace lifts it
  // into the leave guard through the prop of the same name.
  const [hasDraft, setHasDraft] = useState(false);
  const handleDraftChange = useCallback(
    (hasText: boolean) => {
      setHasDraft(hasText);
      onDraftChange?.(hasText);
    },
    [onDraftChange],
  );
  const [appendText, setAppendText] = useState<string | null>(null);
  // The Enter preference (Settings → Chat) decides the streaming placeholder.
  // "Queue only" — the client-level never-steer switch (mecatui --no-steer) —
  // also withdraws every steer affordance: the composer's steer action and
  // the queued row's Steer, whatever the daemon supports.
  const { behavior: enterBehavior } = useEnterSendBehavior();
  const steerAllowed = enterBehavior !== "queue-only";
  // Editing a queued message pulls it out of the queue into the composer —
  // text and staged files alike. Edit all merges the whole queue; Retract
  // pulls the never-applied steer bundle back the same way.
  const [editSeed, setEditSeed] = useState<ComposerSeed | null>(null);
  // A prompt recovered from a transport fault rides the same seed: the
  // composer takes it back for edit-before-resend (the TUI's recoverPrompt).
  useEffect(() => {
    if (!recoverDraft) return;
    setEditSeed(recoverDraft);
    onRecoverDraftConsumed?.();
  }, [recoverDraft, onRecoverDraftConsumed]);
  // The strip's Edit pulls a refused prompt back into the composer the same
  // way (the hook drops the failed exchange and clears the failure first).
  const handleEditFailed = () => {
    const text = onEditFailed?.();
    if (text) setEditSeed({ text });
  };
  const handleEditQueued = (id: string) => {
    const hit = onTakeQueued?.(id);
    if (hit) setEditSeed(hit);
  };
  const handleEditAllQueued = () => {
    const hit = onTakeAllQueued?.();
    if (hit) setEditSeed(hit);
  };
  const handleRetractSteers = async () => {
    const retracted = await onRetractSteers?.();
    const merged = mergeQueued(retracted ?? []);
    if (merged) setEditSeed(merged);
  };
  // When maximized, the panel fills the pane and the conversation column is
  // hidden. Always reset when the panel is closed.
  const [panelMaximized, setPanelMaximized] = useState(false);
  const isMobile = useIsMobile();
  const handleAppendConsumed = useCallback(() => setAppendText(null), []);
  // Threads branched off this chat's messages (browser-local), for the
  // Slack-style reply indicators under their root messages.
  const threadMap = useThreadMap(session.id);

  const closeSidePanel = useCallback(() => {
    setPanel(null);
    setPanelMaximized(false);
  }, []);
  const toggleMaximize = useCallback(() => setPanelMaximized((v) => !v), []);
  // The TUI's ctrl+o / `/mcp`: a shortcut or built-in outside this view asks
  // for the MCP tools panel through a window event (see mcp-panel.tsx).
  const openMcpPanel = useCallback(() => setPanel({ kind: "mcp" }), []);
  useOpenMcpPanelRequests(openMcpPanel);
  // Previewing a composer attachment opens the canvas on an object URL; the
  // previous URL is revoked when replaced or on unmount so attach/preview
  // cycles never leak blobs.
  const previewUrlRef = useRef<string | null>(null);
  const releasePreviewUrl = useCallback(() => {
    if (previewUrlRef.current) {
      URL.revokeObjectURL(previewUrlRef.current);
      previewUrlRef.current = null;
    }
  }, []);
  useEffect(() => releasePreviewUrl, [releasePreviewUrl]);
  const handlePreviewFile = useCallback(
    (file: File) => {
      releasePreviewUrl();
      const url = URL.createObjectURL(file);
      previewUrlRef.current = url;
      setPanel({
        kind: "attachment",
        attachment: { name: file.name, type: file.type, url },
      });
    },
    [releasePreviewUrl],
  );

  const handleConvertThread = useCallback(
    (threadSessionId: string) => {
      setPanel(null);
      onOpenSession?.(threadSessionId);
    },
    [onOpenSession],
  );

  const handleStartThread = useCallback(
    (message: AgentMessage) => setPanel({ kind: "thread", message }),
    [],
  );

  // Drill-down from a row in the inline activity list to the call's full
  // untruncated input/output in the side panel.
  const handleOpenToolCall = useCallback(
    (call: ToolCallInfo) => setPanel({ kind: "toolcall", call }),
    [],
  );

  // Expand from the approval card to the ask's full-height view in the side
  // panel (reason + decoded args + raw toggle, verdicts pinned at the foot).
  const handleExpandApproval = useCallback(
    (approval: ApprovalRequest) => setPanel({ kind: "approval", approval }),
    [],
  );

  // A Write in the changed-files list opens its content in the file preview
  // (the same canvas the per-turn produced-file chips open).
  const handleOpenChangedFile = useCallback(
    (file: { name: string; content?: string }) =>
      setPanel({
        kind: "attachment",
        attachment: { name: file.name, type: "", content: file.content },
      }),
    [],
  );

  // The Agents panel (the TUI's f6 overlay): opened from the header button,
  // the agents.toggle shortcut, or an inline delegation card, which lands on
  // the child/group/member it names. The default tab is context-sensitive
  // (a live team, then a live fan-out, then whichever family has history).
  const openDelegationPanel = useCallback(
    (tab?: DelegationTab, focus: DelegationFocus | null = null) =>
      setPanel({
        kind: "delegation",
        tab: tab ?? preferredDelegationTab(fleet),
        focus,
      }),
    [fleet],
  );
  const handleOpenDelegation = useCallback(
    (card: DelegationInfo) => {
      const target = focusForDelegationCard(card);
      openDelegationPanel(target.tab, target.focus);
    },
    [openDelegationPanel],
  );
  const handleDelegationTabChange = useCallback(
    (tab: DelegationTab) => setPanel({ kind: "delegation", tab, focus: null }),
    [],
  );
  const handleDelegationFocus = useCallback(
    (focus: DelegationFocus | null) =>
      setPanel((current) =>
        current?.kind === "delegation" ? { ...current, focus } : current,
      ),
    [],
  );
  useShortcut("agents.toggle", () => {
    if (panel?.kind === "delegation") {
      closeSidePanel();
      return;
    }
    openDelegationPanel();
  });
  // Expand / collapse details across the whole transcript (the TUI's
  // ctrl+t): flips the global preference every open disclosure follows.
  useShortcut("chat.expandDetails", () => setExpandDetails(!expandDetails));
  // The MCP tools panel hotkey (the TUI's ctrl+o): toggles the panel, and is
  // registered ONLY while the daemon serves an MCP inventory, so the chord
  // keeps its native meaning against a daemon without MCP.
  useShortcut(
    "mcp.inventory",
    () => {
      if (panel?.kind === "mcp") {
        closeSidePanel();
        return;
      }
      openMcpPanel();
    },
    { enabled: mcpPanelAvailable(runtime.serverCapabilities) },
  );
  // The header's Agents button appears once any child has run in this chat;
  // its badge counts the children still running (team members working).
  const delegationCounts = useMemo(() => {
    if (!fleet) return null;
    const total =
      fleet.subagents.length + fleet.parallelGroups.length + fleet.teams.length;
    if (total === 0) return null;
    const counts = fleetCounts(fleet);
    return {
      running:
        counts.subagents.running +
        counts.parallel.running +
        counts.team.working,
    };
  }, [fleet]);

  // The tool-call panel tracks the LIVE call: the stream replaces call
  // objects as results land, so re-resolve by callId each render — the
  // clicked snapshot would otherwise read "running" forever.
  const activePanel = useMemo<ActivePanel | null>(() => {
    if (panel?.kind !== "toolcall") return panel;
    for (const msg of messages) {
      const live = msg.toolCalls?.find((c) => c.callId === panel.call.callId);
      if (live) return { kind: "toolcall", call: live };
    }
    return panel;
  }, [panel, messages]);

  // The expanded ask view tracks the ask on screen: once that ask is
  // answered or retracted (the head ask's id no longer matches), it closes.
  useEffect(() => {
    if (panel?.kind !== "approval") return;
    if (pendingApproval?.approvalId !== panel.approval.approvalId) {
      closeSidePanel();
    }
  }, [panel, pendingApproval, closeSidePanel]);

  // Jump to the latest message whenever the active chat changes so users
  // always land at the bottom (most-recent) of the conversation.
  // biome-ignore lint/correctness/useExhaustiveDependencies: scrolling is intentionally driven by session.id changes
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: "instant" });
    atBottomRef.current = true;
    setAtBottom(true);
  }, [session.id]);

  // Pinned-follow: while the user sits at the bottom, a sent message and the
  // streaming response keep the view pinned there; once they scroll up, their
  // position holds (the floating arrow offers the way back down).
  // biome-ignore lint/correctness/useExhaustiveDependencies: re-runs on every transcript update by design
  useEffect(() => {
    if (!atBottomRef.current) return;
    const el = messagesContainerRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages]);

  // Transcript-scoped select-all (the TUI's ctrl+g): the messages column
  // only — never the sidebar and navigation a page-level ⌘A would take.
  // Reached from ⌘A/Ctrl+A with the transcript focused and from the ···
  // menus' "Select conversation".
  const selectTranscript = () => {
    const el = transcriptRef.current;
    if (el) selectElementContents(el);
  };

  // Esc, layered (close.esc): an open Radix dialog/menu — and the composer's
  // autocomplete — consume their own Escape before the dispatcher sees it
  // (`defaultPrevented`), so by the time this fires nothing transient is
  // open. `resolveEscapeAction` picks exactly ONE arm: drop a transcript
  // selection first (an Esc meant to clear a selection can never stop a
  // run), else close the side panel if one is up, else interrupt a
  // streaming run (the Claude Code convention: Esc cancels), else forward the
  // press to the composer's double-Esc clear (`escapePress`). On mobile the
  // panel lives in a Radix Sheet that owns its own Escape, so only the
  // selection, cancel and clear arms fire there.
  const { escapePress } = useComposerEscape({
    hasDraft,
    // A parked ask is DENIED before the panel or the run (the TUI's Esc in
    // the approval modal): Esc must never cancel a run waiting on a verdict.
    pendingAsk: pendingApproval !== null,
    onDenyAsk: () => onRespondApproval("deny"),
    panelOpen: panel !== null,
    isStreaming,
    hasSelectionIn: messagesContainerRef,
    onClosePanel: () => {
      // Inside the Agents panel a drilled-into child/group/view steps back
      // to its roster first (the TUI's esc layering); the next Esc closes.
      if (
        panel !== null &&
        panel.kind === "delegation" &&
        panel.focus !== null
      ) {
        setPanel({ ...panel, focus: null });
        return;
      }
      closeSidePanel();
    },
    onCancelRun,
  });

  // The keyboard verdicts (the TUI's a/y allow, w always, d/n deny) for the
  // main chat's head ask — registered only while one is waiting. The thread
  // panel's card answers its own ask through the verdict bar's local keys.
  useApprovalShortcuts({
    approval: pendingApproval,
    onRespond: onRespondApproval,
    debugSession: Boolean(session.debugTargetSessionId),
  });

  // Keyboard paging of the transcript (the TUI's PgUp/PgDn/Home/End). These
  // fire while the caret sits in the composer too — `comboFiresWhileTyping`
  // admits PgUp/PgDn, which never insert text. Only one ChatView mounts at a
  // time, so the dispatcher's id→handler map has a single owner.
  const scrollTranscriptBy = (direction: -1 | 1) => {
    const el = messagesContainerRef.current;
    if (!el) return;
    el.scrollBy({
      top: direction * el.clientHeight * PAGE_FRACTION,
      behavior: "smooth",
    });
  };
  useShortcut("transcript.pageUp", () => scrollTranscriptBy(-1));
  useShortcut("transcript.pageDown", () => scrollTranscriptBy(1));
  useShortcut("transcript.top", () => {
    messagesContainerRef.current?.scrollTo({ top: 0, behavior: "smooth" });
  });
  useShortcut("transcript.bottom", () => {
    // Instant, like the TUI: the view is at the bottom before the next
    // frame, so re-pinning here can't fight the onScroll pin detector (a
    // smooth scroll would report "unpinned" until it lands).
    messagesEndRef.current?.scrollIntoView({ behavior: "instant" });
    atBottomRef.current = true;
    setAtBottom(true);
  });

  // The right-hand panel only renders on non-mobile layouts; let the parent
  // collapse the chat list while it's open so both panels fit side by side.
  const sidePanelOpen = !isMobile && panel !== null;
  useEffect(() => {
    onSidePanelOpenChange?.(sidePanelOpen);
  }, [sidePanelOpen, onSidePanelOpenChange]);

  // The sidebar toggle renders on the header edge nearest the panel it
  // controls: leading when the session list docks left, trailing when right.
  // With the list docked right, an open side panel occupies its slot — the
  // toggle then means "give me the list back": close the panel, and the
  // workspace restores the sidebar to its pre-panel state.
  const panelHoldsSidebarSlot =
    sidebarSide === "right" && activePanel !== null && !isMobile;
  const sidebarToggle = !isMobile && (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="size-8 shrink-0 text-muted-foreground"
          onClick={() => {
            if (panelHoldsSidebarSlot) {
              closeSidePanel();
              return;
            }
            onToggleSidebar();
          }}
          aria-label={
            panelHoldsSidebarSlot
              ? "Close preview"
              : sidebarOpen
                ? "Hide sidebar"
                : "Show sidebar"
          }
        >
          {sidebarOpen ? (
            sidebarSide === "left" ? (
              <PanelLeftClose className="size-4" />
            ) : (
              <PanelRightClose className="size-4" />
            )
          ) : sidebarSide === "left" ? (
            <PanelLeftOpen className="size-4" />
          ) : (
            <PanelRightOpen className="size-4" />
          )}
        </Button>
      </TooltipTrigger>
      <TooltipContent side="bottom">
        {panelHoldsSidebarSlot
          ? "Close preview"
          : sidebarOpen
            ? "Hide sidebar"
            : "Show sidebar"}
      </TooltipContent>
    </Tooltip>
  );

  return (
    <div className="flex h-full">
      <div
        className={cn(
          "flex min-w-0 flex-1 flex-col bg-background",
          panelMaximized && "hidden",
        )}
      >
        <div className="flex h-[60px] items-center gap-2 border-b border-border px-3 max-[499px]:h-14 lg:gap-3 lg:px-6">
          {isMobile && (
            <Button
              variant="ghost"
              size="icon"
              className="size-7 shrink-0 text-muted-foreground"
              onClick={onToggleSidebar}
              aria-label="Back to chats"
            >
              <ArrowLeft className="size-4" />
            </Button>
          )}
          {sidebarSide === "left" && sidebarToggle}
          {isStreaming && (
            <Loader2
              aria-label={streamingSpinnerLabel(
                awaitingPhaseFor(pendingApproval),
              )}
              className="size-4 shrink-0 animate-spin text-brand"
            />
          )}
          <h2
            className="min-w-0 flex-1 truncate text-sm font-semibold select-none"
            onDoubleClick={onRename}
            title={onRename ? "Double-click to rename" : undefined}
          >
            {session.title || "Untitled"}
          </h2>
          {/* The user's header status line (Settings → Status line): a
              reserved lane over the session facts, empty until customised. */}
          <TemplatedStatusLine
            surface="header"
            facts={statusFacts}
            className="hidden max-w-[45%] shrink truncate text-xs text-muted-foreground min-[500px]:block"
          />
          {/* mecatui's header `mode <x>`: silent on Manual, coloured for
              Plan / Accept edits, "· pending" while a mid-run switch is held. */}
          <PermissionModeBadge
            mode={mode ?? "default"}
            pendingMode={pendingMode}
            enabled={!!onModeChange}
          />
          {sidebarSide === "right" && sidebarToggle}
          {delegationCounts && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="relative size-8 shrink-0 text-muted-foreground"
                  aria-label={
                    delegationCounts.running > 0
                      ? `Agents (${delegationCounts.running} running)`
                      : "Agents"
                  }
                  aria-pressed={panel?.kind === "delegation"}
                  onClick={() => {
                    if (panel?.kind === "delegation") {
                      closeSidePanel();
                      return;
                    }
                    openDelegationPanel();
                  }}
                >
                  <Network className="size-4" />
                  {delegationCounts.running > 0 && (
                    <span
                      aria-hidden="true"
                      className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-brand px-1 text-[10px] font-medium leading-none text-white"
                    >
                      {delegationCounts.running}
                    </span>
                  )}
                </Button>
              </TooltipTrigger>
              <TooltipContent side="bottom">
                Agents — subagents, parallel runs, teams
              </TooltipContent>
            </Tooltip>
          )}
          {mcpPanelAvailable(runtime.serverCapabilities) && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-8 shrink-0 text-muted-foreground"
                  aria-label={MCP_PANEL_TITLE}
                  aria-pressed={panel?.kind === "mcp"}
                  onClick={() => {
                    if (panel?.kind === "mcp") {
                      closeSidePanel();
                      return;
                    }
                    openMcpPanel();
                  }}
                >
                  <Plug className="size-4" />
                </Button>
              </TooltipTrigger>
              <TooltipContent side="bottom">
                MCP tools — connectors, sources and ToolHive groups
              </TooltipContent>
            </Tooltip>
          )}
          {changedFiles.length > 0 && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-8 shrink-0 gap-1 px-2 text-muted-foreground tabular-nums"
                  aria-label={`${changedFiles.length} file${changedFiles.length === 1 ? "" : "s"} changed — open the list`}
                  aria-pressed={panel?.kind === "changed-files"}
                  onClick={() => {
                    if (panel?.kind === "changed-files") {
                      closeSidePanel();
                      return;
                    }
                    setPanel({ kind: "changed-files" });
                  }}
                >
                  <Pencil className="size-3.5" aria-hidden="true" />
                  <span className="text-xs">
                    {changedFiles.length}{" "}
                    {changedFiles.length === 1 ? "file" : "files"}
                  </span>
                </Button>
              </TooltipTrigger>
              <TooltipContent side="bottom">
                Files changed in this conversation
              </TooltipContent>
            </Tooltip>
          )}
          {!isMobile && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-8 shrink-0 text-muted-foreground"
                  aria-label="Chat options"
                >
                  <Ellipsis className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-56">
                {/* Token usage as the daemon reported it. Hidden until any lands. */}
                <UsageMenuRow usage={usage} />
                {/* View */}
                <DropdownMenuItem
                  onClick={() => setShowToolCalls(!showActivity)}
                >
                  <Wrench className="size-4 mr-2 text-muted-foreground" />
                  {showActivity ? "Hide Tools" : "Show Tools"}
                </DropdownMenuItem>
                <DropdownMenuItem
                  onClick={() => setExpandDetails(!expandDetails)}
                >
                  <ListTree className="size-4 mr-2 text-muted-foreground" />
                  {expandDetails ? "Collapse details" : "Expand details"}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                {/* Conversation actions */}
                {onCompact && (
                  <DropdownMenuItem disabled={isStreaming} onClick={onCompact}>
                    <FoldVertical className="size-4 mr-2 text-muted-foreground" />
                    Compact conversation
                  </DropdownMenuItem>
                )}
                {onClear && (
                  <ClearConversationMenuItem
                    onSelect={onClear}
                    disabledReason={clearDisabledReason}
                  />
                )}
                {onFork && (
                  <ForkChatMenuItem session={session} onSelect={onFork} />
                )}
                {onSwitchWorktree && (
                  <SwitchWorktreeMenuItem onSelect={onSwitchWorktree} />
                )}
                {onInjectDebugAsk && (
                  <DropdownMenuItem onClick={onInjectDebugAsk}>
                    <Bug className="size-4 mr-2 text-muted-foreground" />
                    Inject fake approval
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                {/* Copy: the transcript (select / copy) and the session id */}
                <DropdownMenuSub>
                  <DropdownMenuSubTrigger>
                    <Copy className="size-4 mr-2 text-muted-foreground" />
                    Copy
                  </DropdownMenuSubTrigger>
                  <DropdownMenuSubContent className="w-56">
                    <TranscriptMenuItems
                      messages={messages}
                      botName={botName}
                      onSelectTranscript={selectTranscript}
                    />
                    <CopySessionIdMenuItem session={session} />
                  </DropdownMenuSubContent>
                </DropdownMenuSub>
                {onOpenDetails && (
                  <SessionDetailsMenuItem onSelect={onOpenDetails} />
                )}
                <DropdownMenuSeparator />
                {/* This chat */}
                {onRename && (
                  <DropdownMenuItem onClick={onRename}>
                    <Pencil className="size-4 mr-2 text-muted-foreground" />
                    Rename
                  </DropdownMenuItem>
                )}
                {onDelete && (
                  <DropdownMenuItem onClick={onDelete}>
                    <Trash2 className="size-4 mr-2" />
                    Delete
                  </DropdownMenuItem>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {isMobile && (
            <MobileChatMenu
              showActivity={showActivity}
              onToggleActivity={() => setShowToolCalls(!showActivity)}
              expandDetails={expandDetails}
              onToggleExpandDetails={() => setExpandDetails(!expandDetails)}
              onRename={onRename}
              onDelete={onDelete}
              onOpenDetails={onOpenDetails}
              onCompact={onCompact}
              compactDisabled={isStreaming}
              onInjectDebugAsk={onInjectDebugAsk}
              onClear={onClear}
              clearDisabledReason={clearDisabledReason}
              onSwitchWorktree={onSwitchWorktree}
              session={session}
              onFork={onFork}
              usage={usage}
              transcript={{
                messages,
                botName,
                onSelectTranscript: selectTranscript,
              }}
            />
          )}
        </div>

        {/* The session facts live under ⋯ → Session details; the amber strip
            (DEBUG target + privacy line) stays visible on an AI-debug
            session only, where it is the durable disclosure. */}
        {session.debugTargetSessionId ? (
          <ChatStatusStrip
            session={session}
            live={live}
            resolvedModelId={contextInfo?.modelLabel || null}
            reasoningEffort={contextInfo?.effort}
            modelResolution={modelResolution}
            models={models}
            providerRoute={providerRoute}
            mode={mode}
            pendingMode={pendingMode}
            debugMcpServers={debugMcpServers}
            debugMcpTools={debugMcpTools}
            onOpenSession={onOpenSession}
          />
        ) : null}

        <div className="relative flex-1 min-h-0">
          <section
            ref={messagesContainerRef}
            onScroll={(event) => {
              const el = event.currentTarget;
              const pinned =
                el.scrollHeight - el.scrollTop - el.clientHeight < 80;
              atBottomRef.current = pinned;
              setAtBottom(pinned);
              // Only the unpinned pill reads this; React bails out when the
              // rounded figure hasn't changed, so no re-render churn.
              if (!pinned) {
                setScrollPercent(
                  scrollPositionPercent(
                    el.scrollTop,
                    el.scrollHeight,
                    el.clientHeight,
                  ),
                );
              }
            }}
            // Focusable on click (not in the Tab order) so the native
            // Home/End/PgUp/PgDn work once the transcript itself has focus;
            // named so that focus target is announced.
            aria-label="Conversation"
            tabIndex={-1}
            // ⌘A / Ctrl+A with the transcript focused selects ONLY the
            // conversation (the TUI's ctrl+g). In the composer — or a text
            // field rendered inside the transcript — the key keeps its
            // native meaning; the global dispatcher never claims mod+a.
            onKeyDown={(event) => {
              if (!isSelectAllChord(event)) return;
              event.preventDefault();
              selectTranscript();
            }}
            className="h-full overflow-y-auto px-3 pt-1 pb-48 max-[499px]:pb-24 lg:px-6 lg:pt-2 lg:pb-56"
          >
            <TextSelectionToolbar
              containerRef={messagesContainerRef}
              onAddToChat={(text) => setAppendText(text)}
              onAskInSideChat={(text) => setAppendText(text)}
            />
            <div
              ref={transcriptRef}
              className="flex-1 min-w-0 flex flex-col gap-0 w-full max-w-[768px]"
            >
              {messages.map((msg) => (
                <MessageBubble
                  key={msg.id}
                  message={msg}
                  onOpenArtifact={(artifact) =>
                    setPanel({ kind: "artifact", artifact })
                  }
                  onOpenAttachment={(attachment) =>
                    setPanel({ kind: "attachment", attachment })
                  }
                  onOpenToolCall={handleOpenToolCall}
                  onOpenDelegation={handleOpenDelegation}
                  onCancelDelegation={onCancelChild}
                  onInspectDelegation={onInspectChild}
                  onStartThread={handleStartThread}
                  threadSummary={threadMap[threadKeyForMessage(msg)]}
                  botName={botName}
                  showActivity={showActivity}
                  streaming={isStreaming && msg.id === messages.at(-1)?.id}
                />
              ))}
              {pendingApproval && (
                <ApprovalPanel
                  approval={pendingApproval}
                  onRespond={onRespondApproval}
                  onExpand={handleExpandApproval}
                  debugSession={Boolean(session.debugTargetSessionId)}
                  queuePosition={
                    approvalQueueLength
                      ? { index: 1, total: approvalQueueLength }
                      : undefined
                  }
                />
              )}
              {isStreaming && !pendingAuthorization && (
                <StreamingIndicator
                  awaiting={awaitingPhaseFor(pendingApproval)}
                  message={
                    messages[messages.length - 1]?.role === "assistant"
                      ? messages[messages.length - 1]
                      : undefined
                  }
                />
              )}
              {!isStreaming && statusMessage && (
                <StatusLine status={statusMessage} />
              )}
              <div ref={messagesEndRef} />
            </div>
          </section>
          <div className="absolute bottom-0 left-0 right-0 px-3 lg:px-4 pb-4 max-[499px]:px-0 max-[499px]:pb-0">
            {!atBottom && (
              <div className="pointer-events-none absolute -top-12 left-0 right-0 flex justify-center">
                <ScrollToBottomPill
                  percent={scrollPercent}
                  onClick={() =>
                    messagesEndRef.current?.scrollIntoView({
                      behavior: "smooth",
                    })
                  }
                />
              </div>
            )}
            <div className="max-w-[768px] space-y-1.5 max-[499px]:max-w-none">
              {/* The fleet chip (the TUI footer's delegation segments): the
                  persistent running/done glance per family, above the
                  context meter, each segment opening the Agents panel. */}
              <FleetStatusChip
                fleet={fleet}
                onOpen={(tab) => openDelegationPanel(tab)}
              />
              {/* The footer status line (Settings → Status line). Its default
                  template is `{{context_meter}}` — the shipped effective-model
                  + three-band context meter + usage facets — so the visible
                  default is unchanged until someone customises it. */}
              <TemplatedStatusLine
                surface="footer"
                facts={statusFacts}
                className="px-2 text-[11px] text-muted-foreground/80"
              />
              <QueuedMessageStrip
                queued={queuedMessages}
                pendingSteers={pendingSteers}
                paused={queuePaused}
                isStreaming={isStreaming}
                onSteer={
                  steerAllowed && onSteerQueued
                    ? (id) => onSteerQueued(id)
                    : undefined
                }
                onEdit={handleEditQueued}
                onDelete={(id) => onDeleteQueued?.(id)}
                onResume={onResumeQueue}
                onEditAll={onTakeAllQueued ? handleEditAllQueued : undefined}
                onClearAll={onClearQueue}
                onRetractSteers={
                  onRetractSteers ? () => void handleRetractSteers() : undefined
                }
              />
              <SteerTraceLine entries={steerTrace} />
              {error && (
                <TurnErrorStrip
                  error={error}
                  permanent={lastFailurePermanent}
                  onRetry={onRetry}
                  onNewChat={onNewChat}
                  onEdit={onEditFailed ? handleEditFailed : undefined}
                />
              )}
              {enrollment && (
                <WorkspaceEnrollmentNotice enrollment={enrollment} />
              )}
              <MockProviderNotice />
              {pendingClarification ? (
                <ClarificationPanel
                  clarification={pendingClarification}
                  onRespond={onRespondClarification}
                />
              ) : pendingAuthorization ? (
                // The run is parked on a browser sign-in: the takeover card
                // owns the composer slot until the sign-in is confirmed
                // (re-check) or abandoned (cancel) through the daemon's
                // AUTHORIZATION controls.
                <AuthorizationPanel
                  authorization={pendingAuthorization}
                  onOpen={onOpenAuthorization ?? noAuthorizationAction}
                  onCopyLink={onCopyAuthorizationLink ?? noAuthorizationAction}
                  onRecheck={onRecheckAuthorization ?? noAuthorizationAction}
                  onCancel={onCancelAuthorization ?? noAuthorizationAction}
                />
              ) : (
                <ChatInput
                  onSend={onSend}
                  onQueue={onQueueMessage}
                  onSteer={steerAllowed ? onSteerMessage : undefined}
                  onLocalCommand={onLocalCommand}
                  builtinGates={builtinGates}
                  mediaCapabilities={mediaCapabilities}
                  onPreviewAttachment={handlePreviewFile}
                  focusKey={session.id}
                  draftKey={session.id}
                  escapePress={escapePress}
                  onDraftChange={handleDraftChange}
                  mobileDocked
                  modelLockedLabel={
                    live ? session.model || "Auto-routed" : undefined
                  }
                  onModelChange={() => {}}
                  models={models}
                  autoModelLabel={autoModelLabel}
                  onSwitchModel={live ? onSwitchModel : undefined}
                  currentModelId={session.model ?? ""}
                  onSwitchEffort={live ? onSwitchEffort : undefined}
                  currentEffort={currentEffort}
                  currentModelReasoning={currentModelReasoning}
                  effortSupported={effortSupported}
                  mode={mode}
                  onModeChange={onModeChange}
                  profile={profile}
                  isStreaming={isStreaming}
                  modeSwitchDeferred={modeSwitchDeferred}
                  pendingMode={pendingMode}
                  disabled={!!pendingApproval || readOnlyPlaceholder != null}
                  appendText={appendText}
                  onAppendConsumed={handleAppendConsumed}
                  initialText={editSeed ? editSeed.text || null : initialDraft}
                  initialFiles={editSeed?.files}
                  onInitialTextConsumed={() => {
                    if (editSeed !== null) setEditSeed(null);
                    else onInitialDraftConsumed?.();
                  }}
                  queuedCount={queuedMessages.length}
                  onResumeQueue={onResumeQueue}
                  onEditAllQueued={
                    onTakeAllQueued ? handleEditAllQueued : undefined
                  }
                  onClearQueue={onClearQueue}
                  placeholder={
                    readOnlyPlaceholder ??
                    (isStreaming
                      ? // "Steer" is only an honest promise while the daemon
                        // actually supports it (C1.2) — absent, sends queue.
                        enterBehavior === "steer" && onSteerMessage
                        ? "Steer the agent..."
                        : "Queue a message..."
                      : "Send a message...")
                  }
                />
              )}
            </div>
          </div>
        </div>
      </div>
      {!isMobile && activePanel !== null && (
        <SidePanelForKind
          panel={activePanel}
          parentSessionId={session.id}
          botName={botName}
          onClose={closeSidePanel}
          onConvertToChat={handleConvertThread}
          maximized={panelMaximized}
          onToggleMaximize={toggleMaximize}
          fleet={fleet}
          teamsSupported={teamsSupported}
          onCancelChild={onCancelChild}
          onDelegationTabChange={handleDelegationTabChange}
          onDelegationFocus={handleDelegationFocus}
          onRespondApproval={onRespondApproval}
          debugSession={Boolean(session.debugTargetSessionId)}
          changedFiles={changedFiles}
          onOpenChangedFile={handleOpenChangedFile}
          enrollment={enrollment}
        />
      )}
      {/* On mobile the same panels render as a full-height bottom sheet: the
          grab handle owns dismissal (no window controls), and dvh keeps the
          thread composer above the on-screen keyboard. */}
      {isMobile && activePanel !== null && (
        <Sheet
          open
          onOpenChange={(open) => {
            if (!open) closeSidePanel();
          }}
        >
          <SheetContent side="bottom" className="flex h-[94dvh] flex-col p-0">
            <SheetTitle className="sr-only">
              {activePanel.kind === "thread"
                ? "Thread"
                : activePanel.kind === "attachment"
                  ? activePanel.attachment.name
                  : activePanel.kind === "toolcall"
                    ? toolDisplayName(activePanel.call.name)
                    : activePanel.kind === "approval"
                      ? `${toolDisplayName(activePanel.approval.toolName || "Tool")} — permission ask`
                      : activePanel.kind === "delegation"
                        ? "Agents"
                        : activePanel.kind === "changed-files"
                          ? "Changed files"
                          : activePanel.kind === "mcp"
                            ? MCP_PANEL_TITLE
                            : activePanel.artifact.name}
            </SheetTitle>
            <div className="flex min-h-0 flex-1 flex-col">
              <SidePanelForKind
                panel={activePanel}
                parentSessionId={session.id}
                botName={botName}
                onClose={closeSidePanel}
                onConvertToChat={handleConvertThread}
                maximized
                onToggleMaximize={() => {}}
                windowControls={false}
                fleet={fleet}
                teamsSupported={teamsSupported}
                onCancelChild={onCancelChild}
                onDelegationTabChange={handleDelegationTabChange}
                onDelegationFocus={handleDelegationFocus}
                onRespondApproval={onRespondApproval}
                debugSession={Boolean(session.debugTargetSessionId)}
                changedFiles={changedFiles}
                onOpenChangedFile={handleOpenChangedFile}
                enrollment={enrollment}
              />
            </div>
          </SheetContent>
        </Sheet>
      )}
    </div>
  );
}

/** Renders the right-hand panel for the active kind. */
function SidePanelForKind({
  panel,
  parentSessionId,
  botName,
  onClose,
  onConvertToChat,
  maximized,
  onToggleMaximize,
  windowControls,
  fleet,
  teamsSupported,
  onCancelChild,
  onDelegationTabChange,
  onDelegationFocus,
  onRespondApproval,
  debugSession = false,
  changedFiles = [],
  onOpenChangedFile,
  enrollment = null,
}: {
  panel: ActivePanel;
  parentSessionId: string;
  botName: string;
  onClose: () => void;
  onConvertToChat?: (threadSessionId: string) => void;
  maximized: boolean;
  onToggleMaximize: () => void;
  windowControls?: boolean;
  fleet?: DelegationFleet;
  teamsSupported?: boolean;
  onCancelChild?: (childId: string) => void | Promise<void>;
  onDelegationTabChange: (tab: DelegationTab) => void;
  onDelegationFocus: (focus: DelegationFocus | null) => void;
  /** Answers the expanded ask from the approval detail panel's foot. */
  onRespondApproval?: (choice: ApprovalChoice) => void;
  /** True on an AI-debug chat: a debugger MCP ask offers no Always allow. */
  debugSession?: boolean;
  /** The conversation's changed files (first-seen order) for that panel. */
  changedFiles?: ChangedFile[];
  /** Opens a Write's content from the changed-files list in the preview. */
  onOpenChangedFile?: (file: { name: string; content?: string }) => void;
  /** The chat's enrollment controller for the MCP panel's connect/cancel. */
  enrollment?: WorkspaceEnrollmentView | null;
}) {
  const shared = { onClose, maximized, onToggleMaximize, windowControls };
  switch (panel.kind) {
    case "mcp":
      return (
        <McpPanel
          sessionId={
            isMockTourSession(parentSessionId) ? null : parentSessionId
          }
          enrollment={enrollment}
          {...shared}
        />
      );
    case "changed-files":
      return (
        <ChangedFilesPanel
          files={changedFiles}
          onOpenFile={onOpenChangedFile}
          {...shared}
        />
      );
    case "delegation":
      return (
        <DelegationPanel
          fleet={fleet}
          tab={panel.tab}
          focus={panel.focus}
          onTabChange={onDelegationTabChange}
          onFocus={onDelegationFocus}
          teamsSupported={teamsSupported}
          onCancelChild={onCancelChild}
          {...shared}
        />
      );
    case "artifact":
      return <MarkdownCanvasPanel artifact={panel.artifact} {...shared} />;
    case "attachment":
      return <AttachmentPanel attachment={panel.attachment} {...shared} />;
    case "toolcall":
      return <ToolCallPanel call={panel.call} {...shared} />;
    case "approval":
      return (
        <ApprovalDetailPanel
          approval={panel.approval}
          onRespond={(choice) => onRespondApproval?.(choice)}
          debugSession={debugSession}
          {...shared}
        />
      );
    case "thread":
      // Mock chat threads stay local: read-only replies, no daemon session.
      if (isMockTourSession(parentSessionId)) {
        return (
          <MockThreadPanel
            rootMessage={panel.message}
            botName={botName}
            {...shared}
          />
        );
      }
      return (
        <ThreadPanel
          // Re-key per root message: switching threads must remount the
          // panel so its chat hook re-binds to the right thread session.
          key={`${parentSessionId}:${panel.message.id}`}
          parentSessionId={parentSessionId}
          rootMessage={panel.message}
          botName={botName}
          onConvertToChat={onConvertToChat}
          {...shared}
        />
      );
  }
}
