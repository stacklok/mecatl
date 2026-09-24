// SPDX-License-Identifier: Apache-2.0

import type {
  ListSessionsResponse,
  RunStreamEvent,
  SessionSummaryResponse,
  SessionUsageResponse,
} from "@mecatl-studio/contracts";
import {
  cancelRun,
  resolveRunPermission,
  retrySession,
  startRun,
  steerRun,
  watchSessionActivity,
} from "@mecatl-studio/contracts/generated";
import {
  clearSessionMutation,
  compactSessionMutation,
  createSessionMutation,
  deleteSessionMutation,
  forkSessionMutation,
  getRuntimeOptions,
  getRuntimeSettingsOptions,
  getSessionDetailOptions,
  getSessionTranscriptOptions,
  listSessionsOptions,
  listSessionsQueryKey,
  renameSessionMutation,
  setSessionModeMutation,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
  AlertCircle,
  ArrowDown,
  Bot,
  Bug,
  Copy,
  Eraser,
  ExternalLink,
  GitFork,
  ListTree,
  MessageSquareText,
  MoreHorizontal,
  NotebookPen,
  Pencil,
  RotateCcw,
  Square,
  Trash2,
  User,
  Wrench,
} from "lucide-react";
import {
  type PointerEvent as ReactPointerEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../../components/ui/alert-dialog";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../../components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { Input } from "../../components/ui/input";
import { captureSseFailure, protectedRequestsPaused } from "../../lib/api-client";
import { notifyRunCompletion } from "../../lib/browser-notifications";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import {
  defaultAgentName,
  useAgentDisplayName,
  useEnterSendBehavior,
  useExpandDetails,
  useSessionListSide,
  useShowToolCalls,
  useStartOn,
  useUserDisplayName,
} from "../../lib/profile-preferences";
import { useAuthRecovery } from "../auth/auth-recovery-context";
import { useShortcut } from "../shortcuts/shortcut-provider";
import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import { type AwayFacts, AwayNoticeTracker } from "./away-notice";
import {
  ChatComposer,
  type ComposerEnterAction,
  type ComposerModelOption,
  type DraftChatConfiguration,
} from "./chat-composer";
import { groupSessions, useChatFolders } from "./chat-folders";
import { takeNextQueuedMessage, useQueuedMessages } from "./chat-queue";
import { consumeChatSeed } from "./chat-seed";
import { ChatSessionControls } from "./chat-session-controls";
import {
  type ChatMessage,
  enqueueApproval,
  errorMessage,
  failureFromResult,
  payloadImages,
  payloadText,
  permissionAsk,
  permissionAskId,
  type RunFailure,
  rawResultPayload,
  retractApproval,
  startsNewRun,
  stopReasonFromResult,
  toolCall,
  toolResult,
} from "./chat-state";
import { ChatStatus, type ChatStatusFacts, deriveChatStatus } from "./chat-status";
import { ChatTranscript, isNearTranscriptBottom } from "./chat-transcript";
import { type ContentPreview, ContentPreviewPanel } from "./content-preview-panel";
import { ContinueLatestChip } from "./continue-latest-chip";
import { DEBUG_OPENING_PROMPT, DEBUG_SESSION_CONSENT } from "./debug-session";
import { DraftGreeting } from "./draft-greeting";
import { clearFailedRun, readFailedRun, saveFailedRun } from "./failed-run-storage";
import { FailedTurnCard } from "./failed-turn-card";
import { pickLatestEligibleChat } from "./latest-chat";
import { appendCanvasQuote, useLocalCanvas } from "./local-canvas";
import {
  type ChatImage,
  chatImageDisplay,
  type ImageAttachment,
  imagePreview,
} from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import { QueuedMessageList } from "./queued-message-list";
import { ReasoningDisclosure } from "./reasoning-disclosure";
import {
  abortsOnSessionSwitch,
  acceptsDelivery,
  CATCHING_UP_NOTICE,
  canRetryFailedRun,
  controlTarget,
  createActivityDeduplicator,
  decideTruncation,
  drainsQueue,
  isActiveSessionState,
  isStaleRunControl,
  ownsChatView,
  type RunOwnership,
  type RunStreamEnd,
  type RunTarget,
  runStreamEnd,
} from "./run-stream";
import { ChatsMenuButton, SessionSidebar } from "./session-sidebar";
import {
  adoptSessionTitle,
  type SessionTitleRevision,
  sessionTitleFromEvent,
} from "./session-title";
import { hasVisibleStopReason, StopReasonChip } from "./stop-reason-chip";
import { StreamingIndicator } from "./streaming-indicator";
import {
  registerThreadSession,
  threadKeyForMessage,
  threadTitleFromRoot,
  useThreadMap,
  useThreadSessionIds,
} from "./thread-map";
import { type ToolActivity, ToolActivityList } from "./tool-activity";
import { formatTurnStat, usageMenuLines } from "./turn-stats";
import { useChatMessages } from "./use-chat-messages";
import { shouldCheckDeliveryAfterInventory } from "./use-delivery-follow";
import { useLatestChatAutoOpen } from "./use-latest-chat-auto-open";

/** A pending destructive confirmation, rendered as one shared AlertDialog. */
interface ConfirmPrompt {
  confirmLabel?: string;
  description: string;
  onConfirm: () => void;
  title: string;
}

/**
 * One run's stream and the session it belongs to. The workspace holds at most
 * one; a stream whose owner is no longer the active one stops applying state.
 */
interface RunOwner extends RunOwnership {
  controller: AbortController;
}

/** Collects an SSE transport failure for any of a run's streams, including reattached ones. */
interface StreamFailure {
  error?: unknown;
}

/** A pending single-line text prompt, rendered as one shared Dialog. */
interface TextPrompt {
  confirmLabel: string;
  initialValue: string;
  label: string;
  onConfirm: (value: string) => void;
  title: string;
}

const defaultDraftConfiguration: DraftChatConfiguration = {
  mode: "default",
  reasoningEffort: "default",
  toolAccess: "all",
};

export function ChatWorkspace({ sessionId }: { sessionId?: string }) {
  const recovery = useAuthRecovery();
  const [arrivalSeed] = useState(() => consumeChatSeed(new URL(window.location.href)));
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const sessions = useQuery(listSessionsOptions());
  const runtime = useQuery(getRuntimeOptions());
  const runtimeSettings = useQuery(getRuntimeSettingsOptions());
  const transcript = useQuery({
    ...getSessionTranscriptOptions({ path: { sessionId: sessionId ?? "" } }),
    enabled: Boolean(sessionId),
  });
  const sessionDetail = useQuery({
    ...getSessionDetailOptions({ path: { sessionId: sessionId ?? "" } }),
    enabled: Boolean(sessionId),
  });
  const createSession = useMutation(createSessionMutation());
  const deleteSession = useMutation(deleteSessionMutation());
  const renameSession = useMutation(renameSessionMutation());
  const setMode = useMutation(setSessionModeMutation());
  const compactSession = useMutation(compactSessionMutation());
  const forkSession = useMutation(forkSessionMutation());
  const clearSession = useMutation(clearSessionMutation());
  const [isRunning, setIsRunning] = useState(false);
  const activeRun = useRef<RunOwner | undefined>(undefined);
  const { loadedTranscriptSession, messages, setMessages } = useChatMessages(
    sessionId,
    isRunning,
    transcript.data,
    activeRun,
  );
  const [reattached, setReattached] = useState(false);
  const [reattachEpoch, setReattachEpoch] = useState(0);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [sidebarHidden, setSidebarHidden] = useState(false);
  const [contentPreview, setContentPreview] = useState<ContentPreview>();
  const [selectionAction, setSelectionAction] = useState<SelectionAction>();
  const [confirmPrompt, setConfirmPrompt] = useState<ConfirmPrompt>();
  const [textPrompt, setTextPrompt] = useState<TextPrompt>();
  const [textPromptValue, setTextPromptValue] = useState("");
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [runTarget, setRunTarget] = useState<RunTarget>();
  const [approvals, setApprovals] = useState<ApprovalRequest[]>([]);
  const [failedRun, setFailedRun] = useState<RunFailure>();
  const [draftConfiguration, setDraftConfiguration] = useState(defaultDraftConfiguration);
  const [seedText, setSeedText] = useState<string | undefined>(arrivalSeed.seed?.text);
  const [seedRequiresConfirmation, setSeedRequiresConfirmation] = useState(
    arrivalSeed.seed?.requiresConfirmation ?? false,
  );
  const [statusFacts, setStatusFacts] = useState<ChatStatusFacts>({ phase: "idle" });
  const [returnNotice, setReturnNotice] = useState<string>();
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden");
  const [reconnectGeneration, setReconnectGeneration] = useState(0);
  const [liveUsage, setLiveUsage] = useState<SessionUsageResponse>();
  const [controlPending, setControlPending] = useState(false);
  const viewedSessionId = useRef(sessionId);
  const [titleCache, setTitleCache] = useState(() => new Map<string, SessionTitleRevision>());
  const awayTracker = useRef(new AwayNoticeTracker());
  const awayFacts = useRef<AwayFacts | undefined>(undefined);
  const returnNoticeTimer = useRef<number | undefined>(undefined);
  const lastInventoryRow = useRef<SessionSummaryResponse | undefined>(undefined);
  const transcriptScroll = useRef<HTMLDivElement>(null);
  const [atTranscriptBottom, setAtTranscriptBottom] = useState(true);
  const chatFolders = useChatFolders();
  const agentName = useAgentDisplayName().value.trim() || defaultAgentName;
  const userName = useUserDisplayName().value.trim() || "You";
  const sessionListSide = useSessionListSide().value;
  const { setValue: setShowToolCalls, value: showToolCalls } = useShowToolCalls();
  const { setValue: setExpandDetails, value: expandDetails } = useExpandDetails();
  const disabledModels = useDisabledModels().disabled;
  const enterSendBehavior = useEnterSendBehavior().value;
  const queuedMessages = useQueuedMessages(sessionId ?? "");
  const canvas = useLocalCanvas(sessionId ?? "draft");
  const threadMap = useThreadMap(sessionId ?? "");
  const threadSessionIds = useThreadSessionIds();
  const titledSessionItems = useMemo(
    () =>
      (sessions.data?.items ?? []).map((session) => ({
        ...session,
        title: titleCache.get(session.id)?.title ?? session.title,
      })),
    [sessions.data?.items, titleCache],
  );
  const visibleSessionItems = useMemo(
    () => titledSessionItems.filter((session) => !threadSessionIds.has(session.id)),
    [titledSessionItems, threadSessionIds],
  );
  const navigationOrder = useMemo(
    () =>
      groupSessions(visibleSessionItems, chatFolders.state).flatMap((group) =>
        group.items.map((session) => session.id),
      ),
    [chatFolders.state, visibleSessionItems],
  );
  const latestChat = useMemo(
    // visibleSessionItems already excludes thread-backing sessions.
    () => pickLatestEligibleChat(visibleSessionItems, new Set()),
    [visibleSessionItems],
  );
  const startOn = useStartOn();
  const { requestDraft } = useLatestChatAutoOpen({
    hold: Boolean(seedText),
    latestChatId: latestChat?.id,
    loading: sessions.isPending,
    onOpen: (id) =>
      void navigate({ search: { sessionId: id }, to: "/workspace/chat", replace: true }),
    sessionId,
    startOn: startOn.value,
  });

  const adoptTitle = useCallback((candidate: SessionTitleRevision) => {
    setTitleCache((current) => {
      const previous = current.get(candidate.id);
      const adopted = adoptSessionTitle(previous, candidate);
      if (!adopted || adopted === previous) return current;
      const next = new Map(current);
      next.set(candidate.id, adopted);
      return next;
    });
  }, []);

  useEffect(() => {
    for (const session of sessions.data?.items ?? []) adoptTitle(session);
  }, [adoptTitle, sessions.data?.items]);

  useEffect(() => {
    if (arrivalSeed.url.href !== window.location.href) {
      window.history.replaceState(window.history.state, "", arrivalSeed.url);
    }
  }, [arrivalSeed.url]);

  useEffect(() => {
    viewedSessionId.current = sessionId;
    // A run belongs to one session: leaving it stops its stream here, so its
    // asks, controls, and queue can never act on the chat the user opened.
    const owner = activeRun.current;
    if (abortsOnSessionSwitch(owner, sessionId)) {
      owner?.controller.abort();
      activeRun.current = undefined;
      setRunTarget(undefined);
      setIsRunning(false);
      setReattached(false);
    }
    setError(undefined);
    setNotice(undefined);
    setReturnNotice(undefined);
    awayTracker.current.clear();
    setApprovals([]);
    setFailedRun(sessionId ? readFailedRun(sessionId) : undefined);
    setLiveUsage(undefined);
    setContentPreview(undefined);
    setSelectionAction(undefined);
    if (!activeRun.current || activeRun.current.sessionId !== sessionId)
      setStatusFacts({ phase: "idle" });
  }, [sessionId]);

  useEffect(() => () => activeRun.current?.controller.abort(), []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: follow every streamed message update while the reader remains at the bottom
  useEffect(() => {
    if (!atTranscriptBottom) return;
    const frame = window.requestAnimationFrame(() => {
      const element = transcriptScroll.current;
      if (element) element.scrollTop = element.scrollHeight;
    });
    return () => window.cancelAnimationFrame(frame);
  }, [atTranscriptBottom, messages]);

  const selectedSession = titledSessionItems.find((session) => session.id === sessionId);
  const chatStatus = deriveChatStatus({
    ...statusFacts,
    approvals,
    sessionState: selectedSession?.state,
  });

  useEffect(() => {
    if (!sessionId || lastInventoryRow.current?.id === sessionId) return;
    lastInventoryRow.current = sessions.data?.items.find((item) => item.id === sessionId);
  }, [sessionId, sessions.data?.items]);

  // The inventory is the authority for titles and for deciding whether an idle
  // origin chat may have received a short scheduled delivery between polls.
  useEffect(() => {
    if (!sessionId || !visible || runtime.data?.connection !== "online") return;
    const interval = window.setInterval(() => {
      if (document.visibilityState === "hidden" || viewedSessionId.current !== sessionId) return;
      void (async () => {
        const result = await sessions.refetch();
        if (!result.isSuccess || viewedSessionId.current !== sessionId) return;
        const next = result.data.items.find((item) => item.id === sessionId);
        const previous = lastInventoryRow.current;
        lastInventoryRow.current = next;
        if (
          shouldCheckDeliveryAfterInventory({
            connected: runtime.data?.connection === "online",
            idle: !activeRun.current && !isActiveSessionState(next?.state),
            next,
            previous,
            sessionId,
            visible: document.visibilityState !== "hidden",
          })
        ) {
          await transcript.refetch();
        }
      })();
    }, 20_000);
    return () => window.clearInterval(interval);
  }, [runtime.data?.connection, sessionId, sessions.refetch, transcript.refetch, visible]);

  useEffect(() => {
    if (!sessionId) return;
    const facts: AwayFacts = {
      connection: runtime.data?.connection ?? "connecting",
      phase:
        selectedSession?.state === "awaiting"
          ? "awaiting"
          : isActiveSessionState(selectedSession?.state) || isRunning
            ? "working"
            : selectedSession
              ? "idle"
              : "unknown",
      sessionId,
    };
    awayFacts.current = facts;
    const onVisibility = () => {
      const now = Date.now();
      if (document.visibilityState === "hidden") {
        setVisible(false);
        setReturnNotice(undefined);
        awayTracker.current.restartHide(awayFacts.current ?? facts, now);
        return;
      }
      setVisible(true);
      const returnGeneration = awayTracker.current.beginReturn();
      void (async () => {
        const [connection, inventory, detail] = await Promise.all([
          runtime.refetch(),
          sessions.refetch(),
          sessionDetail.refetch(),
        ]);
        const refreshed =
          connection.isSuccess &&
          inventory.isSuccess &&
          detail.isSuccess &&
          viewedSessionId.current === sessionId;
        const row = inventory.data?.items.find((item) => item.id === sessionId);
        if (
          !awayTracker.current.isCurrentReturn(returnGeneration) ||
          document.visibilityState === "hidden" ||
          viewedSessionId.current !== sessionId
        ) {
          return;
        }
        lastInventoryRow.current = row;
        if (refreshed && !activeRun.current && isActiveSessionState(row?.state)) {
          setReconnectGeneration((current) => current + 1);
        }
        const notice = awayTracker.current.resume(
          {
            connection: connection.data?.connection ?? "connecting",
            phase:
              row?.state === "awaiting"
                ? "awaiting"
                : isActiveSessionState(row?.state)
                  ? "working"
                  : row
                    ? "idle"
                    : "unknown",
            refreshed,
            sessionId,
          },
          Date.now(),
          returnGeneration,
        );
        if (!notice || viewedSessionId.current !== sessionId) return;
        if (returnNoticeTimer.current) window.clearTimeout(returnNoticeTimer.current);
        setReturnNotice(notice.text);
        returnNoticeTimer.current = window.setTimeout(
          () => setReturnNotice(undefined),
          notice.durationMs,
        );
      })();
    };
    const onFocus = () => {
      if (document.visibilityState !== "hidden" && runtime.data?.connection === "online") {
        void sessions.refetch();
      }
    };
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("focus", onFocus);
    if (document.visibilityState === "hidden") awayTracker.current.hide(facts, Date.now());
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("focus", onFocus);
    };
  }, [
    isRunning,
    runtime.data?.connection,
    runtime.refetch,
    selectedSession,
    sessionDetail.refetch,
    sessionId,
    sessions.refetch,
  ]);

  useEffect(
    () => () => {
      if (returnNoticeTimer.current) window.clearTimeout(returnNoticeTimer.current);
      awayTracker.current.clear();
    },
    [],
  );
  const approval = approvals[0];
  const models: ComposerModelOption[] =
    runtimeSettings.data?.models
      .filter((model) => !disabledModels.has(modelPreferenceId(model)))
      .map((model) => ({
        id: model.id,
        image: model.image,
        label: model.displayName,
        providerId: model.providerId,
      })) ?? [];
  const watchable = isActiveSessionState(selectedSession?.state);
  const imageAttachmentsSupported = sessionId
    ? sessionDetail.data?.capabilities.image === true
    : draftConfiguration.model
      ? runtimeSettings.data?.models.some(
          (model) =>
            model.id === draftConfiguration.model?.id &&
            model.providerId === draftConfiguration.model.providerId &&
            model.image,
        ) === true
      : runtime.data?.capabilities.image === true;
  const displayedDetail = sessionDetail.data
    ? {
        ...sessionDetail.data,
        capabilities: {
          image: sessionDetail.data.capabilities.image,
          manualCompaction:
            sessionDetail.data.capabilities.manualCompaction ||
            runtime.data?.capabilities.manualCompaction === true,
          modelSelection:
            sessionDetail.data.capabilities.modelSelection ||
            runtime.data?.capabilities.modelSelection === true,
        },
        usage: liveUsage ? addUsage(sessionDetail.data.usage, liveUsage) : sessionDetail.data.usage,
      }
    : undefined;
  const usageLines = displayedDetail ? usageMenuLines(displayedDetail.usage) : [];

  // Attachment lifetime is deliberately keyed only to session identity and its watchable state.
  // biome-ignore lint/correctness/useExhaustiveDependencies: helpers and QueryClient are stable for this lifetime
  useEffect(() => {
    if (!sessionId || !watchable || activeRun.current || protectedRequestsPaused()) return;

    const controller = new AbortController();
    const owner: RunOwner = { controller, sessionId };
    activeRun.current = owner;
    setIsRunning(true);
    setReattached(true);
    setStatusFacts({ phase: "following" });
    setError(undefined);
    setNotice(undefined);
    setApprovals([]);
    setMessages([]);

    void (async () => {
      const streamFailure: StreamFailure = {};
      let end: RunStreamEnd = { kind: "unfollowed" };
      try {
        const stream = await watchActivity(owner, streamFailure);
        end = await consumeRun(owner, stream, streamFailure, crypto.randomUUID(), "", true);
        if (streamFailure.error && !controller.signal.aborted) throw streamFailure.error;
      } catch (caught) {
        end = { kind: "uncertain" };
        if (!controller.signal.aborted) {
          if (ownsChatView(owner, activeRun.current, viewedSessionId.current)) {
            setError(`Could not confirm this run's outcome: ${errorMessage(caught)}`);
          }
          await queryClient.invalidateQueries({
            queryKey: getSessionTranscriptOptions({ path: { sessionId } }).queryKey,
          });
        }
      } finally {
        if (activeRun.current === owner) {
          let refreshed: SessionSummaryResponse | undefined;
          if (!controller.signal.aborted) {
            try {
              refreshed = await refreshSession(sessionId);
            } catch {
              // A failed refresh cannot certify that an old replay result is current.
            }
          }
          if (!refreshed || isActiveSessionState(refreshed.state)) {
            if (end.kind === "settled") end = { kind: "uncertain" };
          }
          activeRun.current = undefined;
          if (end.kind === "settled") setRunTarget(undefined);
          setApprovals([]);
          setIsRunning(false);
          setReattached(false);
          if (end.kind !== "settled")
            setStatusFacts((current) => ({ phase: "closed", runId: current.runId }));
          if (end.kind === "settled") {
            if (end.failure) setFailedRun(end.failure);
            notifyRunCompletion(refreshed?.title ?? "Chat", Boolean(end.failure));
          }
          if (drainsQueue(end, sessionId, viewedSessionId.current, controller.signal.aborted)) {
            drainNextQueuedMessage(sessionId);
          }
        }
      }
    })();

    return () => {
      controller.abort();
      if (activeRun.current === owner) {
        activeRun.current = undefined;
        setRunTarget(undefined);
        setApprovals([]);
        setIsRunning(false);
        setReattached(false);
      }
    };
  }, [reconnectGeneration, reattachEpoch, sessionId, watchable]);

  async function selectSession(id: string) {
    setSidebarOpen(false);
    await navigate({ search: { sessionId: id }, to: "/workspace/chat" });
  }

  async function renameChat(id: string, title: string) {
    try {
      const renamed = await renameSession.mutateAsync({
        body: { title },
        path: { sessionId: id },
      });
      adoptTitle({ id, ...renamed });
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  function openTextPrompt(prompt: TextPrompt) {
    setTextPrompt(prompt);
    setTextPromptValue(prompt.initialValue);
  }

  /**
   * Creates the chat a draft run belongs to and opens it. The run's owner takes
   * the new session before the view navigates, so opening it does not count
   * as leaving the run; a run aborted meanwhile leaves the user where they are.
   */
  async function createSessionForRun(owner: RunOwner) {
    const created = await createSession.mutateAsync({ body: draftConfiguration });
    await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
    if (owner.controller.signal.aborted || activeRun.current !== owner) return undefined;
    owner.sessionId = created.id;
    viewedSessionId.current = created.id;
    await selectSession(created.id);
    return created.id;
  }

  async function startDraft() {
    requestDraft();
    activeRun.current?.controller.abort();
    setDraftConfiguration(defaultDraftConfiguration);
    setFailedRun(undefined);
    setLiveUsage(undefined);
    setNotice(undefined);
    setMessages([]);
    setSidebarOpen(false);
    setSeedRequiresConfirmation(false);
    await navigate({ search: { sessionId: undefined }, to: "/workspace/chat" });
  }

  async function forkStandaloneChat() {
    if (!sessionId || !displayedDetail?.model) return;
    setError(undefined);
    setNotice("Forking this conversation…");
    try {
      const successor = await forkSession.mutateAsync({
        body: {
          model: { id: displayedDetail.model.id, providerId: displayedDetail.model.providerId },
          reasoningEffort: displayedDetail.model.reasoningEffort,
        },
        path: { sessionId },
      });
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
      await selectSession(successor.id);
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setNotice(undefined);
    }
  }

  async function clearStandaloneChat() {
    if (!sessionId) return;
    setError(undefined);
    setNotice("Clearing this conversation…");
    try {
      const successor = await clearSession.mutateAsync({ path: { sessionId } });
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
      await selectSession(successor.id);
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setNotice(undefined);
    }
  }

  async function startDebugSession(targetSessionId: string) {
    setError(undefined);
    setNotice("Creating a diagnostic chat…");
    try {
      const created = await createSession.mutateAsync({
        body: { debugTargetSessionId: targetSessionId },
      });
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
      await selectSession(created.id);
      // Seeded, not auto-sent: the composer already reviews a starter prompt
      // before sending it, and this consent-gated action deserves the same
      // final look before the evidence actually goes to the model.
      setSeedRequiresConfirmation(false);
      setSeedText(DEBUG_OPENING_PROMPT);
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setNotice(undefined);
    }
  }

  async function openSideThread(message: ChatMessage) {
    if (!sessionId) return;
    const messageKey = threadKeyForMessage(message);
    const existing = threadMap[messageKey]?.sessionId;
    if (existing) {
      setContentPreview({
        kind: "thread",
        messageKey,
        parentSessionId: sessionId,
        sessionId: existing,
      });
      return;
    }
    if (isRunning) {
      setNotice("Wait for the current response to finish before starting a side thread.");
      return;
    }
    const model = displayedDetail?.model;
    if (!model) {
      setError("This chat has no resolved model to carry into a side thread.");
      return;
    }

    setError(undefined);
    setNotice("Creating a focused side thread…");
    try {
      const successor = await forkSession.mutateAsync({
        body: {
          model: { id: model.id, providerId: model.providerId },
          reasoningEffort: model.reasoningEffort,
        },
        path: { sessionId },
      });
      registerThreadSession(sessionId, messageKey, successor.id);
      try {
        await renameSession.mutateAsync({
          body: { title: threadTitleFromRoot(message.content) },
          path: { sessionId: successor.id },
        });
      } catch {
        // The association is still valid if the optional presentation rename fails.
      }
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
      setContentPreview({
        kind: "thread",
        messageKey,
        parentSessionId: sessionId,
        sessionId: successor.id,
      });
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setNotice(undefined);
    }
  }

  async function sendPrompt(
    prompt: string,
    images: ImageAttachment[] = [],
    targetSessionId = sessionId,
    onAccepted?: (accepted: boolean) => void,
  ) {
    let accepted = false;
    setError(undefined);
    setNotice(undefined);
    setFailedRun(undefined);
    setLiveUsage(undefined);
    setIsRunning(true);
    setStatusFacts({ phase: "sending" });
    let activeSessionId = targetSessionId;
    const assistantId = crypto.randomUUID();
    const controller = new AbortController();
    const owner: RunOwner = { controller, sessionId: targetSessionId };
    activeRun.current = owner;
    const owns = () => ownsChatView(owner, activeRun.current, viewedSessionId.current);
    let end: RunStreamEnd = { kind: "unfollowed" };

    try {
      activeSessionId ??= await createSessionForRun(owner);
      if (!activeSessionId) return;
      loadedTranscriptSession.current = activeSessionId;
      clearFailedRun(activeSessionId);
      if (owns()) {
        setMessages((current) => [
          ...current,
          {
            content: prompt,
            id: crypto.randomUUID(),
            images: images.length ? images : undefined,
            role: "user",
          },
          { content: "", id: assistantId, role: "assistant" },
        ]);
      }
      const streamFailure: StreamFailure = {};
      const response = await startRun({
        body: {
          images: images.map(({ data, mimeType, name }) => ({ data, mimeType, name })),
          prompt,
        },
        onSseError: captureSseFailure(streamFailure),
        path: { sessionId: activeSessionId },
        signal: controller.signal,
        sseMaxRetryAttempts: 1,
      });
      end = await consumeRun(
        owner,
        response.stream,
        streamFailure,
        assistantId,
        prompt,
        false,
        () => {
          accepted = true;
          onAccepted?.(true);
        },
      );

      if (streamFailure.error && !controller.signal.aborted) {
        throw streamFailure.error;
      }
      if (end.kind === "settled") {
        const failure = end.failure;
        if (failure) {
          if (owns()) setFailedRun(failure);
          saveFailedRun(activeSessionId, failure);
        } else {
          clearFailedRun(activeSessionId);
        }
        if (!controller.signal.aborted) {
          notifyRunCompletion(selectedSession?.title ?? "Your chat", Boolean(failure));
        }
      }
    } catch (caught) {
      end = { kind: "uncertain" };
      if (!controller.signal.aborted) {
        if (owns()) setError(`Could not confirm this run's outcome: ${errorMessage(caught)}`);
      }
    } finally {
      if (!accepted) onAccepted?.(false);
      await finishRun(owner, end);
    }
  }

  /**
   * Releases a finished run's hold on the view. Only the run that still owns
   * the view clears the run state; a run the user left was already detached
   * when the view switched, and its queue waits for its own chat.
   */
  async function finishRun(owner: RunOwner, end: RunStreamEnd) {
    const owned = activeRun.current === owner;
    if (owned) {
      activeRun.current = undefined;
      if (end.kind === "settled") setRunTarget(undefined);
      setApprovals([]);
    }
    if (owner.sessionId) {
      try {
        await refreshSession(owner.sessionId);
      } catch {
        // Run outcome comes from the stream; a failed inventory refresh cannot undo it.
      }
    }
    if (!owned) return;
    setLiveUsage(undefined);
    setIsRunning(false);
    if (end.kind !== "settled")
      setStatusFacts((current) => ({ phase: "closed", runId: current.runId }));
    if (
      drainsQueue(end, owner.sessionId, viewedSessionId.current, owner.controller.signal.aborted)
    ) {
      drainNextQueuedMessage(owner.sessionId);
    }
  }

  async function handleComposerSend(
    prompt: string,
    action: ComposerEnterAction,
    images: ImageAttachment[],
  ) {
    if (recovery.phase !== "ready") {
      setNotice("Verify your sign-in before sending this draft.");
      return false;
    }
    if (!isRunning && !(statusFacts.phase === "closed" && controlTarget(runTarget, sessionId))) {
      // Keep the draft until the first stream delivery proves the write was accepted.
      // The rest of the run continues in the background, leaving queue/steer available.
      return await new Promise<boolean>((resolve) => {
        void sendPrompt(prompt, images, sessionId, resolve);
      });
    }
    if (images.length > 0) {
      setNotice("Wait for the active run to finish before sending images.");
      return false;
    }
    if (!sessionId) {
      setNotice("Wait for this chat to finish starting, then send the message again.");
      return false;
    }
    const target = controlTarget(runTarget, sessionId);
    if (action === "steer" && target) {
      try {
        await steerRun({
          body: { text: prompt },
          path: { runId: target.runId, sessionId: target.sessionId },
          throwOnError: true,
        });
        setNotice("Sent — steering the active run.");
        return true;
      } catch (caught) {
        if (!isStaleRunControl(caught)) {
          setError(errorMessage(caught));
          return false;
        }
        queuedMessages.add(prompt);
        setNotice("The run ended before steering. Message queued for this chat.");
        return true;
      }
    }
    queuedMessages.add(prompt);
    setNotice("Message queued for the next run.");
    return true;
  }

  function drainNextQueuedMessage(activeSessionId: string) {
    if (protectedRequestsPaused()) return;
    if (viewedSessionId.current !== activeSessionId) return;
    const next = takeNextQueuedMessage(activeSessionId);
    if (next) void sendPrompt(next.text, [], activeSessionId);
  }

  async function retryFailedRun() {
    if (!canRetryFailedRun(failedRun, sessionId, isRunning) || !sessionId || !failedRun) return;
    setError(undefined);
    setNotice(undefined);
    setFailedRun(undefined);
    setLiveUsage(undefined);
    setIsRunning(true);
    setStatusFacts({ phase: "sending" });
    const retrySessionId = sessionId;
    const assistantId = crypto.randomUUID();
    const controller = new AbortController();
    const owner: RunOwner = { controller, sessionId: retrySessionId };
    activeRun.current = owner;
    const owns = () => ownsChatView(owner, activeRun.current, viewedSessionId.current);
    let end: RunStreamEnd = { kind: "unfollowed" };
    setMessages((current) => [...current, { content: "", id: assistantId, role: "assistant" }]);

    try {
      const streamFailure: StreamFailure = {};
      const response = await retrySession({
        onSseError: captureSseFailure(streamFailure),
        path: { sessionId: retrySessionId },
        signal: controller.signal,
        sseMaxRetryAttempts: 1,
      });
      end = await consumeRun(owner, response.stream, streamFailure, assistantId, failedRun.prompt);
      if (streamFailure.error && !controller.signal.aborted) throw streamFailure.error;
      if (end.kind === "settled") {
        const failure = end.failure;
        if (failure) {
          if (owns()) setFailedRun(failure);
          saveFailedRun(retrySessionId, failure);
        } else {
          clearFailedRun(retrySessionId);
        }
        if (!controller.signal.aborted) {
          notifyRunCompletion(selectedSession?.title ?? "Your chat", Boolean(failure));
        }
      }
    } catch (caught) {
      end = { kind: "uncertain" };
      if (!controller.signal.aborted) {
        if (owns()) {
          setError(`Could not confirm this retry's outcome: ${errorMessage(caught)}`);
          setFailedRun(failedRun);
        }
      }
    } finally {
      await finishRun(owner, end);
    }
  }

  /** Opens (or, with `resumeFrom`, reopens) the activity stream of the run owner's session. */
  async function watchActivity(owner: RunOwner, streamFailure: StreamFailure, resumeFrom?: string) {
    const response = await watchSessionActivity({
      onSseError: captureSseFailure(streamFailure),
      path: { sessionId: owner.sessionId ?? "" },
      query: resumeFrom ? { resumeFrom } : undefined,
      signal: owner.controller.signal,
      sseMaxRetryAttempts: 1,
    });
    return response.stream;
  }

  /**
   * Applies one run's deliveries to the view while `owner` still owns it. A
   * bounded replay (`run.truncated` "bound") is followed by reattaching from
   * its cursor, so a long session catches up to the live run instead of
   * stopping at its oldest events; a truncation never reads as a run outcome.
   */
  async function consumeRun(
    owner: RunOwner,
    firstStream: AsyncIterable<RunStreamEvent>,
    streamFailure: StreamFailure,
    assistantId: string,
    prompt: string,
    replay = false,
    onAccepted?: () => void,
  ): Promise<RunStreamEnd> {
    let failure: RunFailure | undefined;
    let sawResult = false;
    let activeAssistantId = assistantId;
    let activePrompt = prompt;
    let turnStartedAt = Date.now();
    let followedRunId: string | undefined;
    let reattaches = 0;
    let unfollowed = false;
    const shouldApply = createActivityDeduplicator();
    const owns = () => ownsChatView(owner, activeRun.current, viewedSessionId.current);

    if (replay && owns()) setMessages([]);

    let stream: AsyncIterable<RunStreamEvent> | undefined = firstStream;
    while (stream) {
      let resumeFrom: string | undefined;
      for await (const delivery of stream) {
        onAccepted?.();
        onAccepted = undefined;
        if (!acceptsDelivery(owner, activeRun.current, viewedSessionId.current, delivery)) {
          if (owns()) continue;
          // The user left this run's chat: stop reading, and report no outcome.
          unfollowed = true;
          break;
        }
        if (!shouldApply(delivery)) continue;
        if (delivery.type === "run.error") {
          failure = { message: delivery.message, permanent: false, prompt: activePrompt };
          setStatusFacts({ failure, phase: "following", runId: followedRunId });
          continue;
        }
        if (delivery.type === "run.truncated") {
          // A truncation ends this stream; it says nothing about the run itself.
          const decision = decideTruncation(delivery, reattaches);
          setNotice(decision.notice);
          if (decision.action === "reattach") {
            if (owner.sessionId) {
              try {
                await queryClient.fetchQuery({
                  ...getSessionTranscriptOptions({ path: { sessionId: owner.sessionId } }),
                  staleTime: 0,
                });
              } catch {
                // Replay still has a cursor; a later refresh can load the saved transcript.
              }
            }
            resumeFrom = decision.cursor;
          } else {
            unfollowed = true;
            loadedTranscriptSession.current = undefined;
            if (owner.sessionId) {
              void queryClient.invalidateQueries({
                queryKey: getSessionTranscriptOptions({ path: { sessionId: owner.sessionId } })
                  .queryKey,
              });
            }
          }
          break;
        }
        if (delivery.type === "run.started") {
          // A repeated start of the run already followed (a reattach may or
          // may not replay it) keeps the active assistant message and turn.
          if (!startsNewRun(followedRunId, delivery.runId)) continue;
          followedRunId = delivery.runId;
          setRunTarget({ runId: delivery.runId, sessionId: delivery.sessionId });
          setStatusFacts({ phase: "following", runId: delivery.runId });
          failure = undefined;
          sawResult = false;
          turnStartedAt = Date.now();
          if (replay) {
            activeAssistantId = crypto.randomUUID();
            activePrompt = "";
          }
          continue;
        }
        const event = delivery.event;
        // A stream resumed from a cursor carries no run.started for its first
        // run. When that first durable event belongs to another run, it still
        // moves the controls there; its user_prompt starts the new message.
        if (
          event.runId &&
          owner.sessionId !== undefined &&
          startsNewRun(followedRunId, event.runId)
        ) {
          followedRunId = event.runId;
          setRunTarget({ runId: event.runId, sessionId: owner.sessionId });
          setStatusFacts({ phase: "following", runId: event.runId });
          failure = undefined;
          sawResult = false;
        }
        if (event.usage && !replay) setLiveUsage(event.usage);
        if (event.kind === "session.title" && owner.sessionId) {
          const title = sessionTitleFromEvent(owner.sessionId, event.payload);
          if (title) adoptTitle(title);
        }
        if (event.kind === "user_prompt" && (replay || event.delivery)) {
          const promptText = event.delivery ? event.text : payloadText(event.payload) || event.text;
          activePrompt = promptText;
          const images = payloadImages(event.payload);
          activeAssistantId = crypto.randomUUID();
          setMessages((current) => [
            ...current,
            {
              content: promptText,
              delivery: event.delivery,
              id: crypto.randomUUID(),
              images: images.length ? images : undefined,
              role: "user",
            },
          ]);
        } else if (event.kind === "message.delta") {
          ensureAssistant(activeAssistantId);
          updateAssistant(activeAssistantId, (message) => ({
            ...message,
            content: message.content + event.text,
          }));
        } else if (event.kind === "reasoning.delta") {
          ensureAssistant(activeAssistantId);
          updateAssistant(activeAssistantId, (message) => ({
            ...message,
            reasoning: (message.reasoning ?? "") + event.text,
          }));
        } else if (event.kind === "tool.call") {
          const tool = toolCall(event.payload);
          if (tool) {
            ensureAssistant(activeAssistantId);
            updateAssistant(activeAssistantId, (message) => ({
              ...message,
              tools: [...(message.tools ?? []), tool],
            }));
          }
        } else if (event.kind === "tool.result") {
          const result = toolResult(event.payload);
          if (result) {
            updateAssistant(activeAssistantId, (message) => ({
              ...message,
              tools: message.tools?.map((tool) =>
                tool.id === result.callId
                  ? { ...tool, isError: result.isError, output: result.content }
                  : tool,
              ),
            }));
          }
        } else if (event.kind === "permission.ask") {
          const approval = permissionAsk(event.payload);
          if (approval) setApprovals((current) => enqueueApproval(current, approval));
        } else if (event.kind === "permission.retract") {
          const askId = permissionAskId(event.payload);
          setApprovals((current) => retractApproval(current, askId));
          if (owner.sessionId) awayTracker.current.record(owner.sessionId, "approval-resolved");
        } else if (event.kind === "approval") {
          const askId = permissionAskId(event.payload);
          setApprovals((current) => retractApproval(current, askId));
          if (owner.sessionId) awayTracker.current.record(owner.sessionId, "approval-resolved");
        } else if (event.kind === "result") {
          sawResult = true;
          failure = failureFromResult(event.payload, activePrompt);
          const resultText = payloadText(event.payload) || event.text;
          const stop = stopReasonFromResult(event.payload);
          setStatusFacts({
            failure,
            phase: "following",
            runId: followedRunId,
            sawResult,
            stopReason: stop,
          });
          if (owner.sessionId) awayTracker.current.record(owner.sessionId, "result");
          const turnStat =
            !replay && event.usage
              ? formatTurnStat(event.usage, Date.now() - turnStartedAt)
              : undefined;
          ensureAssistant(activeAssistantId);
          updateAssistant(activeAssistantId, (message) => ({
            ...message,
            content: message.content || resultText,
            failure: failure
              ? {
                  detail: rawResultPayload(event.payload),
                  message: failure.message,
                  permanent: failure.permanent,
                }
              : message.failure,
            stopReason: stop,
            turnStat: turnStat ?? message.turnStat,
          }));
        }
      }

      stream = undefined;
      if (resumeFrom && !streamFailure.error && !owner.controller.signal.aborted && owns()) {
        reattaches += 1;
        stream = await watchActivity(owner, streamFailure, resumeFrom);
      }
    }

    // Once the replay has caught up and the stream ended on its own, the
    // catch-up notice no longer describes anything.
    if (!unfollowed && owns()) {
      setNotice((current) => (current === CATCHING_UP_NOTICE ? undefined : current));
    }
    return runStreamEnd({ failure, sawResult }, unfollowed, prompt);
  }

  async function refreshSession(
    activeSessionId: string,
  ): Promise<SessionSummaryResponse | undefined> {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() }),
      queryClient.invalidateQueries({
        queryKey: getSessionTranscriptOptions({ path: { sessionId: activeSessionId } }).queryKey,
      }),
      queryClient.invalidateQueries({
        queryKey: getSessionDetailOptions({ path: { sessionId: activeSessionId } }).queryKey,
      }),
    ]);
    return queryClient
      .getQueryData<ListSessionsResponse>(listSessionsQueryKey())
      ?.items.find((item) => item.id === activeSessionId);
  }

  function ensureAssistant(id: string) {
    setMessages((current) =>
      current.some((message) => message.id === id)
        ? current
        : [...current, { content: "", id, role: "assistant" }],
    );
  }

  function updateAssistant(id: string, update: (message: ChatMessage) => ChatMessage) {
    setMessages((current) =>
      current.map((message) => (message.id === id ? update(message) : message)),
    );
  }

  async function stopRun() {
    const target = controlTarget(runTarget, sessionId);
    if (!target) {
      return;
    }
    setControlPending(true);
    try {
      await cancelRun({
        path: { runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setControlPending(false);
    }
  }

  async function respondToApproval(verdict: ApprovalVerdict) {
    const target = controlTarget(runTarget, sessionId);
    if (!target || !approval) {
      return;
    }
    setControlPending(true);
    try {
      await resolveRunPermission({
        body: { verdict },
        path: { askId: approval.askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      if (viewedSessionId.current === target.sessionId) {
        setApprovals((current) => retractApproval(current, approval.askId));
      }
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setControlPending(false);
    }
  }

  async function changeMode(mode: DraftChatConfiguration["mode"]) {
    if (!sessionId || isRunning) return;
    setError(undefined);
    setNotice(undefined);
    try {
      await setMode.mutateAsync({ body: { mode }, path: { sessionId } });
      await queryClient.invalidateQueries({
        queryKey: getSessionDetailOptions({ path: { sessionId } }).queryKey,
      });
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  async function compactConversation() {
    if (!sessionId || isRunning) return;
    setError(undefined);
    setNotice(undefined);
    try {
      const result = await compactSession.mutateAsync({ path: { sessionId } });
      setNotice(
        result.compacted
          ? "Conversation context was compacted."
          : "The conversation did not need compaction.",
      );
      await refreshSession(sessionId);
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  async function forkConversation(
    model: { id: string; providerId: string },
    reasoningEffort: DraftChatConfiguration["reasoningEffort"],
  ) {
    if (!sessionId || isRunning) return;
    setError(undefined);
    setNotice(undefined);
    try {
      const successor = await forkSession.mutateAsync({
        body: { model, reasoningEffort },
        path: { sessionId },
      });
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
      await selectSession(successor.id);
    } catch (caught) {
      setError(errorMessage(caught));
    }
  }

  function navigateBy(forward: boolean) {
    if (navigationOrder.length === 0) return;
    const current = navigationOrder.indexOf(sessionId ?? "");
    const next =
      current === -1
        ? forward
          ? 0
          : navigationOrder.length - 1
        : forward
          ? Math.min(current + 1, navigationOrder.length - 1)
          : Math.max(current - 1, 0);
    const nextId = navigationOrder[next];
    if (nextId) void selectSession(nextId);
  }

  function toggleChatList() {
    if (window.matchMedia("(min-width: 760px)").matches) {
      setSidebarHidden((hidden) => !hidden);
    } else {
      setSidebarOpen((open) => !open);
    }
  }

  function captureTranscriptSelection(event: ReactPointerEvent<HTMLDivElement>) {
    const selection = window.getSelection();
    const text = selection?.toString().trim() ?? "";
    if (!selection || !text || selection.rangeCount === 0) {
      setSelectionAction(undefined);
      return;
    }
    const range = selection.getRangeAt(0);
    if (!event.currentTarget.contains(range.commonAncestorContainer)) return;
    const rect = range.getBoundingClientRect();
    setSelectionAction({
      left: Math.min(window.innerWidth - 140, Math.max(12, rect.left + rect.width / 2)),
      text,
      top: Math.max(12, rect.top - 12),
    });
  }

  useShortcut("chat.new", () => void startDraft());
  useShortcut("chat.toggleList", toggleChatList);
  useShortcut("chat.next", () => navigateBy(true));
  useShortcut("chat.next.vim", () => navigateBy(true));
  useShortcut("chat.prev", () => navigateBy(false));
  useShortcut("chat.prev.vim", () => navigateBy(false));
  useShortcut("chat.latest", () => {
    if (latestChat) void selectSession(latestChat.id);
  });
  useShortcut("chat.copyId", () => {
    if (sessionId) void navigator.clipboard.writeText(sessionId);
  });
  useShortcut("chat.fork", () => void forkStandaloneChat());
  useShortcut("chat.clear", () => void clearStandaloneChat());
  useShortcut("chat.expandDetails", () => setExpandDetails(!expandDetails));
  useShortcut("close.esc", () => {
    if (contentPreview) setContentPreview(undefined);
    else if (sidebarOpen) setSidebarOpen(false);
    else if (isRunning) void stopRun();
  });

  return (
    <div className="relative flex h-full min-w-0">
      <SessionSidebar
        collapsed={sidebarHidden}
        creating={createSession.isPending || isRunning}
        deletingId={deleteSession.isPending ? deleteSession.variables?.path.sessionId : undefined}
        folders={chatFolders.state}
        items={visibleSessionItems}
        onClose={() => setSidebarOpen(false)}
        onCreate={() => void startDraft()}
        onCreateFolder={(activeSessionId) => {
          openTextPrompt({
            confirmLabel: "Create",
            initialValue: "",
            label: "Folder name",
            onConfirm: (name) => chatFolders.create(name, activeSessionId),
            title: "New folder",
          });
        }}
        onDelete={(session) => {
          setConfirmPrompt({
            description: "This cannot be undone.",
            onConfirm: () => {
              void deleteSession
                .mutateAsync({ path: { sessionId: session.id } })
                .then(async () => {
                  await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
                  if (session.id === sessionId) {
                    await navigate({ search: { sessionId: undefined }, to: "/workspace/chat" });
                  }
                })
                .catch((caught) => setError(errorMessage(caught)));
            },
            title: `Delete “${session.title}”?`,
          });
        }}
        onDeleteFolder={(folder) => {
          setConfirmPrompt({
            description: "Its chats will stay in Chat History.",
            onConfirm: () => chatFolders.remove(folder.id),
            title: `Delete folder “${folder.name}”?`,
          });
        }}
        onMoveToFolder={chatFolders.move}
        onRename={(session, title) => {
          void renameChat(session.id, title);
        }}
        onRenameFolder={(folder) => {
          openTextPrompt({
            confirmLabel: "Rename",
            initialValue: folder.name,
            label: "Folder name",
            onConfirm: (name) => chatFolders.rename(folder.id, name),
            title: "Rename folder",
          });
        }}
        onSelect={(id) => void selectSession(id)}
        open={sidebarOpen}
        renamingId={renameSession.isPending ? renameSession.variables?.path.sessionId : undefined}
        selectedId={sessionId}
        side={sessionListSide}
      />

      <section className="relative flex min-w-0 flex-1 flex-col bg-background">
        <header className="flex h-16 shrink-0 items-center gap-3 border-b px-4 sm:px-6">
          <ChatsMenuButton
            onClick={() => {
              setSidebarHidden(false);
              setSidebarOpen(true);
            }}
            showOnDesktop={sidebarHidden}
          />
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <h1 className="truncate text-sm font-semibold">
              {selectedSession?.title ?? "New chat"}
            </h1>
            {selectedSession?.debugTargetSessionId && (
              <Badge
                title="A read-only diagnostic chat; it never modifies its target."
                variant="info"
              >
                <Bug aria-hidden="true" />
                Debug
              </Badge>
            )}
          </div>
          <Button
            aria-label="Open local canvas"
            onClick={() => setContentPreview({ kind: "canvas" })}
            size="sm"
            variant="ghost"
          >
            <NotebookPen aria-hidden="true" />
            <span className="hidden sm:inline">Canvas</span>
          </Button>
          {sessionId && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button aria-label="Chat options" size="icon" variant="ghost">
                  <MoreHorizontal aria-hidden="true" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-52">
                {usageLines.length > 0 && (
                  <div className="mb-1 border-b px-2 py-1.5">
                    <p className="text-xs font-medium text-muted-foreground">Token usage</p>
                    {usageLines.map((line) => (
                      <p className="text-sm tabular-nums" key={line}>
                        {line}
                      </p>
                    ))}
                  </div>
                )}
                <DropdownMenuItem onSelect={() => setShowToolCalls(!showToolCalls)}>
                  <Wrench aria-hidden="true" />
                  {showToolCalls ? "Hide Tools" : "Show Tools"}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => setExpandDetails(!expandDetails)}>
                  <ListTree aria-hidden="true" />
                  {expandDetails ? "Collapse details" : "Expand details"}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem onSelect={() => void navigator.clipboard.writeText(sessionId)}>
                  <Copy aria-hidden="true" />
                  Copy session ID
                </DropdownMenuItem>
                <DropdownMenuItem
                  disabled={forkSession.isPending || !displayedDetail?.model}
                  onSelect={() => void forkStandaloneChat()}
                >
                  <GitFork aria-hidden="true" />
                  Fork chat
                </DropdownMenuItem>
                <DropdownMenuItem
                  disabled={clearSession.isPending}
                  onSelect={() => void clearStandaloneChat()}
                >
                  <Eraser aria-hidden="true" />
                  Clear conversation
                </DropdownMenuItem>
                {runtime.data?.capabilities.sessionDebug && (
                  <DropdownMenuItem
                    disabled={createSession.isPending}
                    onSelect={() => {
                      setConfirmPrompt({
                        confirmLabel: "Send evidence & debug",
                        description: DEBUG_SESSION_CONSENT,
                        onConfirm: () => void startDebugSession(sessionId),
                        title: `Debug with AI: “${selectedSession?.title}”?`,
                      });
                    }}
                  >
                    <Bug aria-hidden="true" />
                    Debug with AI
                  </DropdownMenuItem>
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  disabled={selectedSession && !selectedSession.capabilities.rename}
                  onSelect={() => {
                    openTextPrompt({
                      confirmLabel: "Rename",
                      initialValue: selectedSession?.title ?? "",
                      label: "Chat name",
                      onConfirm: (title) => {
                        void renameChat(sessionId, title);
                      },
                      title: "Rename chat",
                    });
                  }}
                  title={selectedSession?.capabilities.renameReason || undefined}
                >
                  <Pencil aria-hidden="true" />
                  Rename
                </DropdownMenuItem>
                <DropdownMenuItem
                  className="text-destructive focus:text-destructive"
                  disabled={selectedSession && !selectedSession.capabilities.delete}
                  onSelect={() => {
                    setConfirmPrompt({
                      description: "This cannot be undone.",
                      onConfirm: () => {
                        void deleteSession
                          .mutateAsync({ path: { sessionId } })
                          .then(async () => {
                            await queryClient.invalidateQueries({
                              queryKey: listSessionsQueryKey(),
                            });
                            await navigate({
                              search: { sessionId: undefined },
                              to: "/workspace/chat",
                            });
                          })
                          .catch((caught) => setError(errorMessage(caught)));
                      },
                      title: `Delete “${selectedSession?.title}”?`,
                    });
                  }}
                  title={selectedSession?.capabilities.deleteReason || undefined}
                >
                  <Trash2 aria-hidden="true" />
                  Delete
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {(isRunning ||
            (statusFacts.phase === "closed" && controlTarget(runTarget, sessionId))) && (
            <div className="flex items-center gap-2">
              {isRunning && (
                <Badge variant="success">
                  {reattached ? "Reconnected to run" : "Mecatl is working"}
                </Badge>
              )}
              {controlTarget(runTarget, sessionId) && (
                <Button
                  disabled={controlPending}
                  onClick={() => void stopRun()}
                  size="sm"
                  variant="outline"
                >
                  <Square aria-hidden="true" className="fill-current" />
                  Stop
                </Button>
              )}
            </div>
          )}
        </header>

        <div
          className="min-h-0 flex-1 overflow-y-auto"
          onPointerUp={captureTranscriptSelection}
          onScroll={(event) => {
            setSelectionAction(undefined);
            const element = event.currentTarget;
            setAtTranscriptBottom(isNearTranscriptBottom(element));
          }}
          ref={transcriptScroll}
        >
          <div className="mx-auto flex min-h-full max-w-3xl flex-col px-4 py-8 sm:px-6">
            {transcript.isPending && sessionId && !isRunning ? (
              <p className="m-auto text-sm text-muted-foreground">Loading conversation…</p>
            ) : messages.length === 0 ? (
              <div className="m-auto flex flex-col items-center">
                <DraftGreeting
                  onPickSeed={(seed) => {
                    setSeedRequiresConfirmation(false);
                    setSeedText(seed);
                  }}
                />
                {!sessionId && (
                  <ContinueLatestChip latest={latestChat} onContinue={selectSession} />
                )}
              </div>
            ) : (
              <ChatTranscript
                agentName={agentName}
                messages={messages}
                onOpenThread={(message) => void openSideThread(message)}
                onPreviewImage={(image) =>
                  setContentPreview({ file: imagePreview(image, true), kind: "file" })
                }
                onPreviewTool={(tool) => setContentPreview({ kind: "tool", tool })}
                showToolCalls={showToolCalls}
                streamingMessageId={
                  isRunning && messages.at(-1)?.role === "assistant"
                    ? messages.at(-1)?.id
                    : undefined
                }
                threadDisabled={forkSession.isPending}
                threadSessionIdForMessage={(message) =>
                  threadMap[threadKeyForMessage(message)]?.sessionId
                }
                userName={userName}
              />
            )}
          </div>
        </div>

        {!atTranscriptBottom && (
          <Button
            aria-label="Scroll to latest message"
            className="absolute bottom-32 left-1/2 z-10 size-9 -translate-x-1/2 rounded-full shadow-lg"
            onClick={() => {
              const element = transcriptScroll.current;
              element?.scrollTo({ behavior: "smooth", top: element.scrollHeight });
              setAtTranscriptBottom(true);
            }}
            size="icon"
            variant="secondary"
          >
            <ArrowDown aria-hidden="true" />
          </Button>
        )}

        {chatStatus && (
          <div className="mx-auto mb-2 w-[calc(100%-2rem)] max-w-3xl">
            <ChatStatus status={chatStatus} />
          </div>
        )}
        {returnNotice && (
          <div
            className="mx-auto mb-3 w-[calc(100%-2rem)] max-w-3xl rounded-lg border bg-background px-3 py-2 text-sm"
            role="status"
          >
            {returnNotice}
          </div>
        )}
        {error && (
          <div className="mx-auto mb-3 flex w-[calc(100%-2rem)] max-w-3xl items-start gap-2 rounded-lg bg-destructive/10 px-3 py-2 text-sm text-foreground">
            <AlertCircle aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            {error}
            {watchable && !isRunning && (
              <Button
                disabled={protectedRequestsPaused()}
                onClick={() => {
                  setError(undefined);
                  setReattachEpoch((epoch) => epoch + 1);
                }}
                size="sm"
                variant="outline"
              >
                Retry stream
              </Button>
            )}
          </div>
        )}
        {notice && (
          <div className="mx-auto mb-3 w-[calc(100%-2rem)] max-w-3xl rounded-lg bg-success/10 px-3 py-2 text-sm text-foreground">
            {notice}
          </div>
        )}
        {approval && (
          <ApprovalPanel
            approval={approval}
            disabled={controlPending}
            onRespond={(verdict) => void respondToApproval(verdict)}
            position={1}
            total={approvals.length}
          />
        )}
        {failedRun && (
          <RunFailurePanel
            failure={failedRun}
            onDismiss={() => {
              setFailedRun(undefined);
              if (sessionId) clearFailedRun(sessionId);
            }}
            onRetry={() => void retryFailedRun()}
            retrying={isRunning}
          />
        )}
        {sessionId && displayedDetail && (
          <ChatSessionControls
            compacting={compactSession.isPending}
            detail={displayedDetail}
            disabled={isRunning}
            forking={forkSession.isPending}
            modePending={setMode.isPending}
            models={models}
            onCompact={() => void compactConversation()}
            onFork={(model, effort) => void forkConversation(model, effort)}
            onModeChange={(mode) => void changeMode(mode)}
            safetyLevel={runtime.data?.capabilities.posture}
          />
        )}
        <QueuedMessageList
          items={queuedMessages.items}
          onDelete={queuedMessages.remove}
          onEdit={queuedMessages.update}
        />
        <ChatComposer
          configuration={sessionId ? undefined : draftConfiguration}
          disabled={createSession.isPending}
          imageAttachmentsSupported={imageAttachmentsSupported}
          models={models}
          onConfigurationChange={setDraftConfiguration}
          onPreviewImage={(image) =>
            setContentPreview({ file: imagePreview(image, false), kind: "file" })
          }
          onSeedConsumed={() => {
            setSeedText(undefined);
            setSeedRequiresConfirmation(false);
          }}
          onSend={handleComposerSend}
          safetyLevel={runtime.data?.capabilities.posture}
          seedCanConfirm={
            runtime.data?.connection === "online" &&
            !isRunning &&
            !(statusFacts.phase === "closed" && controlTarget(runTarget, sessionId)) &&
            !watchable &&
            !createSession.isPending &&
            (!sessionId || Boolean(selectedSession))
          }
          seedRequiresConfirmation={seedRequiresConfirmation}
          seedText={seedText}
          working={
            isRunning ||
            (statusFacts.phase === "closed" && Boolean(controlTarget(runTarget, sessionId)))
          }
          workingBehavior={enterSendBehavior}
        />
      </section>
      {contentPreview && (
        <ContentPreviewPanel
          canvas={canvas.value}
          onCanvasChange={canvas.setValue}
          onClose={() => setContentPreview(undefined)}
          preview={contentPreview}
        />
      )}
      {selectionAction && (
        <div
          className="fixed z-50 flex -translate-x-1/2 -translate-y-full gap-1 rounded-lg border bg-popover p-1 text-popover-foreground shadow-lg"
          style={{ left: selectionAction.left, top: selectionAction.top }}
        >
          <Button
            onClick={() => {
              void navigator.clipboard.writeText(selectionAction.text);
              setSelectionAction(undefined);
            }}
            size="sm"
            variant="ghost"
          >
            <Copy aria-hidden="true" />
            Copy
          </Button>
          <Button
            onClick={() => {
              canvas.setValue(appendCanvasQuote(canvas.value, selectionAction.text));
              setContentPreview({ kind: "canvas" });
              setSelectionAction(undefined);
              window.getSelection()?.removeAllRanges();
            }}
            size="sm"
            variant="ghost"
          >
            <NotebookPen aria-hidden="true" />
            Add to canvas
          </Button>
        </div>
      )}
      <Dialog onOpenChange={(open) => !open && setTextPrompt(undefined)} open={Boolean(textPrompt)}>
        <DialogContent>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const value = textPromptValue.trim();
              if (value && textPrompt) {
                textPrompt.onConfirm(value);
                setTextPrompt(undefined);
              }
            }}
          >
            <DialogHeader>
              <DialogTitle>{textPrompt?.title}</DialogTitle>
            </DialogHeader>
            <Input
              aria-label={textPrompt?.label}
              autoFocus
              className="mt-4"
              onChange={(event) => setTextPromptValue(event.target.value)}
              placeholder={textPrompt?.label}
              value={textPromptValue}
            />
            <DialogFooter className="mt-4">
              <Button onClick={() => setTextPrompt(undefined)} type="button" variant="ghost">
                Cancel
              </Button>
              <Button disabled={!textPromptValue.trim()} type="submit">
                {textPrompt?.confirmLabel}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      <AlertDialog
        onOpenChange={(open) => !open && setConfirmPrompt(undefined)}
        open={Boolean(confirmPrompt)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmPrompt?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmPrompt?.description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                confirmPrompt?.onConfirm();
                setConfirmPrompt(undefined);
              }}
            >
              {confirmPrompt?.confirmLabel ?? "Delete"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function RunFailurePanel({
  failure,
  onDismiss,
  onRetry,
  retrying,
}: {
  failure: RunFailure;
  onDismiss: () => void;
  onRetry: () => void;
  retrying: boolean;
}) {
  return (
    <div className="mx-auto mb-3 flex w-[calc(100%-2rem)] max-w-3xl flex-wrap items-center gap-3 rounded-xl border border-destructive/30 bg-destructive/5 p-3">
      <AlertCircle aria-hidden="true" className="size-4 shrink-0 text-destructive" />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">The agent couldn’t finish this request</p>
        <p className="mt-0.5 text-xs text-muted-foreground">{failure.message}</p>
      </div>
      <div className="flex gap-2">
        {!failure.permanent && (
          <Button disabled={retrying} onClick={onRetry} size="sm">
            <RotateCcw aria-hidden="true" />
            {retrying ? "Retrying…" : "Retry"}
          </Button>
        )}
        <Button onClick={onDismiss} size="sm" variant="ghost">
          Dismiss
        </Button>
      </div>
    </div>
  );
}

/** Exported so the side-thread panel can render a thread's messages with the exact same primitives as the main transcript, instead of duplicating this JSX. */
export function Message({
  agentAvatar,
  agentName,
  message,
  onOpenThread,
  onPreviewImage,
  onPreviewTool,
  showToolCalls,
  streaming,
  threadDisabled,
  threadSessionId,
  userAvatar,
  userName,
}: {
  agentAvatar: string;
  agentName: string;
  message: ChatMessage;
  onOpenThread?: () => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  showToolCalls: boolean;
  streaming: boolean;
  threadDisabled: boolean;
  threadSessionId?: string;
  userAvatar: string;
  userName: string;
}) {
  const user = message.role === "user";
  // A turn that ended with no text, no visible tool activity, and nothing
  // else worth surfacing (reasoning, a failure, a stat line, a stop-reason
  // chip) renders nothing at all — never a "Thinking…" placeholder for a
  // run that has already finished (e.g. StopNoProgress with an empty final
  // turn).
  const hasVisibleTools = showToolCalls && Boolean(message.tools?.length);
  const hasExtras =
    Boolean(message.images?.length) ||
    Boolean(message.reasoning) ||
    Boolean(message.failure) ||
    Boolean(message.turnStat) ||
    hasVisibleStopReason(message.stopReason ?? "");
  if (!user && !streaming && !message.content && !hasVisibleTools && !hasExtras) {
    return null;
  }
  return (
    <article className={`flex gap-3 ${user ? "justify-end" : "justify-start"}`}>
      {!user && <MessageAvatar avatarUrl={agentAvatar} fallback="agent" name={agentName} />}
      <div
        className={
          user
            ? "max-w-[85%] whitespace-pre-wrap rounded-2xl rounded-br-md bg-secondary px-4 py-2.5 text-sm leading-6"
            : "min-w-0 max-w-[90%] text-sm leading-7"
        }
      >
        <p
          className={`mb-1 text-[11px] font-medium ${user ? "text-right text-muted-foreground" : "text-muted-foreground"}`}
        >
          {user ? userName : agentName}
        </p>
        {message.reasoning && (
          <ReasoningDisclosure streaming={streaming} text={message.reasoning} />
        )}
        {message.images && message.images.length > 0 && (
          <div className="mb-2 grid grid-cols-2 gap-2">
            {message.images.map((image) => {
              const display = chatImageDisplay(image);
              return (
                <div
                  className="overflow-hidden rounded-lg border bg-background"
                  key={image.id ?? `${image.name}-${image.data ?? image.url}`}
                >
                  {display.kind === "link" ? (
                    <a
                      className="flex items-center gap-2 px-3 py-2 text-sm text-foreground underline underline-offset-2 hover:text-brand-ink"
                      href={display.href}
                      rel="noopener noreferrer"
                      target="_blank"
                    >
                      <ExternalLink aria-hidden="true" className="size-4 shrink-0" />
                      <span className="min-w-0 truncate">{image.name}</span>
                    </a>
                  ) : display.kind === "name" ? (
                    <span className="block truncate px-3 py-2 text-sm text-muted-foreground">
                      {image.name}
                    </span>
                  ) : onPreviewImage ? (
                    <button
                      aria-label={`Preview ${image.name}`}
                      className="block w-full"
                      onClick={() => onPreviewImage(image)}
                      type="button"
                    >
                      <img
                        alt={image.name}
                        className="max-h-48 w-full object-cover"
                        src={display.src}
                      />
                    </button>
                  ) : (
                    <img
                      alt={image.name}
                      className="max-h-48 w-full object-cover"
                      src={display.src}
                    />
                  )}
                </div>
              );
            })}
          </div>
        )}
        {message.content ? (
          user ? (
            message.content
          ) : (
            <MarkdownMessage>{message.content}</MarkdownMessage>
          )
        ) : message.tools?.length ? null : streaming ? (
          <StreamingIndicator />
        ) : null}
        {showToolCalls && message.tools && message.tools.length > 0 && (
          <ToolActivityList onPreview={onPreviewTool} tools={message.tools} />
        )}
        {!streaming && !message.failure && <StopReasonChip stopReason={message.stopReason ?? ""} />}
        {message.failure && (
          <FailedTurnCard
            detail={message.failure.detail}
            permanent={message.failure.permanent}
            summary={message.failure.message}
          />
        )}
        {!streaming && message.turnStat && (
          <p
            className="mt-1.5 text-[11px] tabular-nums text-muted-foreground/70"
            title="This turn's tokens sent ↑ and received ↓, model time, and the share of input served from the prompt cache."
          >
            {message.turnStat}
          </p>
        )}
        {onOpenThread && message.content && (
          <button
            aria-label={threadSessionId ? "Open side thread" : "Reply in side thread"}
            className="mt-2 flex items-center gap-1.5 rounded-full px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-50"
            disabled={threadDisabled}
            onClick={onOpenThread}
            type="button"
          >
            <MessageSquareText aria-hidden="true" className="size-3.5" />
            {threadSessionId ? "Open thread" : "Reply in thread"}
          </button>
        )}
      </div>
      {user && <MessageAvatar avatarUrl={userAvatar} fallback="user" name={userName} />}
    </article>
  );
}

interface SelectionAction {
  left: number;
  text: string;
  top: number;
}

function MessageAvatar({
  avatarUrl,
  fallback,
  name,
}: {
  avatarUrl: string;
  fallback: "agent" | "user";
  name: string;
}) {
  return (
    <span
      className={`mt-1 flex size-7 shrink-0 items-center justify-center overflow-hidden rounded-full ${fallback === "agent" ? "bg-brand/10 text-brand-ink" : "bg-muted text-muted-foreground"}`}
    >
      {avatarUrl ? (
        <img alt={name} className="size-full object-cover" src={avatarUrl} />
      ) : fallback === "agent" ? (
        <Bot aria-hidden="true" className="size-4" />
      ) : (
        <User aria-hidden="true" className="size-4" />
      )}
    </span>
  );
}

function addUsage(
  baseline: SessionUsageResponse,
  currentRun: SessionUsageResponse,
): SessionUsageResponse {
  return {
    cacheReadTokens: (
      BigInt(baseline.cacheReadTokens) + BigInt(currentRun.cacheReadTokens)
    ).toString(),
    cacheWriteTokens: (
      BigInt(baseline.cacheWriteTokens) + BigInt(currentRun.cacheWriteTokens)
    ).toString(),
    inputTokens: (BigInt(baseline.inputTokens) + BigInt(currentRun.inputTokens)).toString(),
    outputTokens: (BigInt(baseline.outputTokens) + BigInt(currentRun.outputTokens)).toString(),
    reasoningTokens: (
      BigInt(baseline.reasoningTokens) + BigInt(currentRun.reasoningTokens)
    ).toString(),
  };
}
