// SPDX-License-Identifier: Apache-2.0

import type {
  ListSessionsResponse,
  RunStreamEvent,
  SessionSummaryResponse,
  SessionUsageResponse,
} from "@mecatl-studio/contracts";
import {
  cancelAuthorization,
  cancelRun,
  recheckAuthorization,
  resolvePlanAsk,
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
  Bug,
  Copy,
  Eraser,
  GitFork,
  ListTodo,
  ListTree,
  LoaderCircle,
  MoreHorizontal,
  NotebookPen,
  Pencil,
  RotateCcw,
  Square,
  Trash2,
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
import { copyToClipboard } from "../../lib/clipboard";
import { errorMessage } from "../../lib/error-message";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import {
  defaultAgentName,
  useAgentAvatar,
  useAgentDisplayName,
  useEnterSendBehavior,
  useExpandDetails,
  useSessionListSide,
  useShowToolCalls,
  useStartOn,
  useUserAvatar,
  useUserDisplayName,
} from "../../lib/profile-preferences";
import { useIsMobile } from "../../lib/use-mobile";
import { useAuthRecovery } from "../auth/auth-recovery-context";
import { useShortcut, useShortcutSuppression } from "../shortcuts/shortcut-provider";
import { ApprovalDetailContext } from "./approval-detail-context";
import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import {
  type AuthorizationHandoff,
  type AuthorizationOperation,
  recordAuthorizationEvent,
} from "./authorization-review";
import { type AwayFacts, AwayNoticeTracker } from "./away-notice";
import {
  ChatComposer,
  type ComposerEnterAction,
  type ComposerModelOption,
  type DraftChatConfiguration,
} from "./chat-composer";
import { useChatEscape } from "./chat-escape";
import { groupSessions, useChatFolders } from "./chat-folders";
import { takeNextQueuedMessage, useQueuedMessages } from "./chat-queue";
import { consumeChatSeed } from "./chat-seed";
import { SessionUsageControls } from "./chat-session-controls";
import {
  approvalKey,
  type ChatMessage,
  enqueueApproval,
  failureFromResult,
  messageOwnsApproval,
  payloadImages,
  payloadRecord,
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
import { ChatTranscript, isNearTranscriptBottom, MessageImages } from "./chat-transcript";
import {
  type ContentPreview,
  ContentPreviewPanel,
  restoreActivityOpenerFocus,
} from "./content-preview-panel";
import { ContinueLatestChip } from "./continue-latest-chip";
import { DEBUG_OPENING_PROMPT, DEBUG_SESSION_CONSENT } from "./debug-session";
import { DelegationCardRow, type DelegationFocus } from "./delegation-card";
import {
  applyDelegationDelivery,
  createDelegationFleet,
  type DelegationFleet,
  markDelegationHistoryIncomplete,
  markDelegationRunUnfollowed,
} from "./delegation-fleet";
import { type DelegationAnchor, placeDelegationCards } from "./delegation-placement";
import { DraftGreeting } from "./draft-greeting";
import { EscapeHintContext } from "./escape-hint-context";
import { clearFailedRun, readFailedRun, saveFailedRun } from "./failed-run-storage";
import { FailedTurnCard } from "./failed-turn-card";
import { FleetStatusChip } from "./fleet-status-chip";
import { pickLatestEligibleChat } from "./latest-chat";
import { appendCanvasQuote, useLocalCanvas } from "./local-canvas";
import { type ChatImage, type ImageAttachment, imagePreview } from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import {
  MessageActions,
  MessageAvatar,
  MessageRow,
  messageAuthorClass,
  messageBodyClass,
  messageRowClass,
} from "./message-bubble";
import { MessageMinimap } from "./message-minimap";
import { PermissionModeBadge } from "./permission-mode-badge";
import { exactPlanControlAvailability, followPlanContinuationFromBff } from "./plan-continuation";
import { PlanReviewCard, type PlanVerdict } from "./plan-review-card";
import { QueuedMessageStrip } from "./queued-message-strip";
import { ReasoningDisclosure } from "./reasoning-disclosure";
import {
  abortsOnSessionSwitch,
  acceptsDelivery,
  CATCHING_UP_NOTICE,
  controlTarget,
  createActivityDeduplicator,
  decideTruncation,
  drainsQueue,
  isActiveSessionState,
  isStaleRunControl,
  MAX_ACTIVITY_REATTACHES,
  ownsChatView,
  type RunOwnership,
  type RunStreamEnd,
  type RunTarget,
  runStreamEnd,
} from "./run-stream";
import { isInspectOnlySession, isProvenChatSession, SessionInspection } from "./session-inspection";
import { ChatsMenuButton, SessionSidebar } from "./session-sidebar";
import {
  adoptSessionTitle,
  type SessionTitleRevision,
  sessionTitleFromEvent,
} from "./session-title";
import {
  appendSteerTrace,
  observedSteerTrace,
  SteerTrace,
  type SteerTraceEntry,
} from "./steer-trace";
import { hasVisibleStopReason, StopReasonChip } from "./stop-reason-chip";
import { StreamingIndicator } from "./streaming-indicator";
import { TextSelectionToolbar } from "./text-selection-toolbar";
import {
  matchingThreadKeyForMessage,
  readThreadAssociations,
  recordedRootForMessage,
  registerThreadSession,
  relinkLegacyThread,
  threadKeyForRoot,
  threadTitleFromRoot,
  useThreadAssociations,
  useThreadSessionIds,
} from "./thread-map";
import type { ToolActivity } from "./tool-activity";
import { ToolCallList } from "./tool-call-list";
import { formatTurnStat, usageMenuLines } from "./turn-stats";
import { useChatMessages } from "./use-chat-messages";
import { shouldRefreshTranscriptAfterInventory } from "./use-delivery-follow";
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

interface AuthorizationContinuation {
  activeAssistantId: string;
  activePrompt: string;
  followedRunId?: string;
  shouldApply: (delivery: RunStreamEvent) => boolean;
}

interface UncertainAuthorization {
  beforeCursor: string;
  callId: string;
  continuation: AuthorizationContinuation;
}

interface AuthorizationActivityCursor {
  authorizationId: string;
  callId: string;
  cursor: string;
  runId: string;
  sessionId: string;
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

function delegationFamilyForKind(kind: string): DelegationFocus["family"] | undefined {
  if (kind === "subagent.start" || kind === "subagent.tool" || kind === "subagent.end") {
    return "subagent";
  }
  if (kind === "parallel.start" || kind === "parallel.branch" || kind === "parallel.end") {
    return "parallel";
  }
  if (
    kind === "team.start" ||
    kind === "team.member" ||
    kind === "team.tasks" ||
    kind === "team.findings" ||
    kind === "team.end"
  ) {
    return "team";
  }
  return undefined;
}

function hasUnsettledDelegation(fleet: DelegationFleet, runId: string): boolean {
  return (
    fleet.subagents.some((item) => item.runId === runId && item.state !== "finished") ||
    fleet.parallelGroups.some(
      (item) =>
        item.runId === runId &&
        (item.state !== "finished" || item.branches.some((branch) => branch.state !== "finished")),
    ) ||
    fleet.teams.some(
      (item) =>
        item.runId === runId &&
        (item.state !== "finished" || item.members.some((member) => member.state !== "finished")),
    )
  );
}

function foldDelegationDelivery(fleet: DelegationFleet, delivery: RunStreamEvent): DelegationFleet {
  const next = applyDelegationDelivery(fleet, delivery);
  return delivery.type === "run.event" &&
    delivery.event.kind === "result" &&
    hasUnsettledDelegation(next, delivery.event.runId)
    ? markDelegationRunUnfollowed(next, delivery.event.runId)
    : next;
}

export function ChatWorkspace({ sessionId }: { sessionId?: string }) {
  const recovery = useAuthRecovery();
  const [arrivalSeed] = useState(() => consumeChatSeed(new URL(window.location.href)));
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const sessions = useQuery(listSessionsOptions());
  // A row proved during this mounted account/session view survives a transient
  // inventory poll that omits it. A direct URL starts without this proof.
  const lastInventoryRow = useRef<SessionSummaryResponse | undefined>(undefined);
  const runtime = useQuery(getRuntimeOptions());
  const runtimeSettings = useQuery(getRuntimeSettingsOptions());
  const inventorySession =
    sessions.data?.items.find((item) => item.id === sessionId) ??
    (lastInventoryRow.current?.id === sessionId ? lastInventoryRow.current : undefined);
  const provenChat = !sessionId || (inventorySession && isProvenChatSession(inventorySession));
  const transcript = useQuery({
    ...getSessionTranscriptOptions({ path: { sessionId: sessionId ?? "" } }),
    enabled: Boolean(sessionId && provenChat),
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
  const [delegationFleet, setDelegationFleet] = useState(() =>
    createDelegationFleet(sessionId ?? ""),
  );
  const [delegationAnchors, setDelegationAnchors] = useState<Record<string, DelegationAnchor>>({});
  const [activityFocus, setActivityFocus] = useState<DelegationFocus>();
  const [activityFamily, setActivityFamily] = useState<DelegationFocus["family"]>();
  const [activityFocusRequest, setActivityFocusRequest] = useState(0);
  const activityOpener = useRef<HTMLButtonElement>(null);
  const activityOpenerFocus = useRef<DelegationFocus>(undefined);
  const sessionActivityControl = useRef<HTMLButtonElement>(null);
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
  const [planContinuationActive, setPlanContinuationActive] = useState(false);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [sidebarHidden, setSidebarHidden] = useState(false);
  const [contentPreview, setContentPreview] = useState<ContentPreview>();
  const authorizationFlows = useRef(new Set<string>());
  const authorizationUncertain = useRef(new Map<string, UncertainAuthorization>());
  const authorizationActivityCursor = useRef<AuthorizationActivityCursor | undefined>(undefined);
  const activityRefresh = useRef<
    { authorizationId: string; callId: string; cursor: string; sessionId: string } | undefined
  >(undefined);
  const planReattach = useRef<{ cursor?: string; runId: string; sessionId: string } | undefined>(
    undefined,
  );
  const [, setAuthorizationUncertainEpoch] = useState(0);
  const [authorizationBusy, setAuthorizationBusy] = useState<string>();
  const [inspectionOpen, setInspectionOpen] = useState(false);
  const [selectionAction, setSelectionAction] = useState<SelectionAction>();
  const [appendText, setAppendText] = useState<string>();
  const [confirmPrompt, setConfirmPrompt] = useState<ConfirmPrompt>();
  const [textPrompt, setTextPrompt] = useState<TextPrompt>();
  const [textPromptValue, setTextPromptValue] = useState("");
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [runTarget, setRunTarget] = useState<RunTarget>();
  const [approvals, setApprovals] = useState<ApprovalRequest[]>([]);
  const ordinaryVerdictsInFlight = useRef(new Set<string>());
  const ordinaryVerdictsUncertain = useRef(new Set<string>());
  const settledAskKeys = useRef(new Set<string>());
  const [ordinaryVerdictEpoch, setOrdinaryVerdictEpoch] = useState(0);
  const [failedRun, setFailedRun] = useState<RunFailure>();
  const [draftConfiguration, setDraftConfiguration] = useState(defaultDraftConfiguration);
  const [composerDraftPresent, setComposerDraftPresent] = useState(false);
  const [clearDraftSignal, setClearDraftSignal] = useState(0);
  const [steeringQueuedId, setSteeringQueuedId] = useState<string>();
  const [escapeClearHint, setEscapeClearHint] = useState(false);
  const workspaceRoot = useRef<HTMLDivElement>(null);
  const chatOptionsTrigger = useRef<HTMLButtonElement>(null);
  const chatOptionsEscapeSession = useRef<string | undefined>(undefined);
  // The phone header's chat options menu opens Canvas only once the menu has closed and
  // returned focus to its trigger, so the panel takes focus and later returns it there.
  const chatOptionsOpensCanvas = useRef(false);
  useEffect(() => {
    const clearPending = () => {
      chatOptionsEscapeSession.current = undefined;
    };
    const clearOnOtherKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") clearPending();
    };
    document.addEventListener("pointerdown", clearPending, true);
    document.addEventListener("keydown", clearOnOtherKey, true);
    return () => {
      document.removeEventListener("pointerdown", clearPending, true);
      document.removeEventListener("keydown", clearOnOtherKey, true);
    };
  }, []);
  const panelOpener = useRef<HTMLElement | null>(null);
  const panelWasOpen = useRef(false);
  const [seedText, setSeedText] = useState<string | undefined>(arrivalSeed.seed?.text);
  const [seedRequiresConfirmation, setSeedRequiresConfirmation] = useState(
    arrivalSeed.seed?.requiresConfirmation ?? false,
  );
  const [statusFacts, setStatusFacts] = useState<ChatStatusFacts>({ phase: "idle" });
  const [returnNotice, setReturnNotice] = useState<string>();
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden");
  const [reconnectGeneration, setReconnectGeneration] = useState(0);
  const [liveUsage, setLiveUsage] = useState<SessionUsageResponse>();
  const [steerTrace, setSteerTrace] = useState<SteerTraceEntry[]>([]);
  const [showSteerTrace, setShowSteerTrace] = useState(false);
  const [controlPending, setControlPending] = useState(false);
  const planVerdictsInFlight = useRef(new Set<string>());
  const planVerdictsUncertain = useRef(new Set<string>());
  const [planVerdictEpoch, setPlanVerdictEpoch] = useState(0);
  const planFollowController = useRef<AbortController | undefined>(undefined);
  const viewedSessionId = useRef(sessionId);
  const [titleCache, setTitleCache] = useState(() => new Map<string, SessionTitleRevision>());
  const awayTracker = useRef(new AwayNoticeTracker());
  const awayFacts = useRef<AwayFacts | undefined>(undefined);
  const returnNoticeTimer = useRef<number | undefined>(undefined);
  const activityFollowedSession = useRef<string | undefined>(undefined);
  const interruptedSettledSession = useRef<string | undefined>(undefined);
  const transcriptScroll = useRef<HTMLDivElement>(null);
  const minimapNavigation = useRef(false);
  const threadCreation = useRef(false);
  const [atTranscriptBottom, setAtTranscriptBottom] = useState(true);
  const visibleDelegationFleet =
    delegationFleet.sessionId === (sessionId ?? "")
      ? delegationFleet
      : createDelegationFleet(sessionId ?? "");
  const delegationPlacement = useMemo(
    () => placeDelegationCards(messages, visibleDelegationFleet, delegationAnchors),
    [messages, visibleDelegationFleet, delegationAnchors],
  );
  const chatFolders = useChatFolders();
  const agentName = useAgentDisplayName().value.trim() || defaultAgentName;
  const userName = useUserDisplayName().value.trim() || "You";
  const agentAvatar = useAgentAvatar().value;
  const userAvatar = useUserAvatar().value;
  const sessionListSide = useSessionListSide().value;
  const { setValue: setShowToolCalls, value: showToolCalls } = useShowToolCalls();
  const { setValue: setExpandDetails, value: expandDetails } = useExpandDetails();
  const disabledModels = useDisabledModels().disabled;
  const enterSendBehavior = useEnterSendBehavior().value;
  const queuedMessages = useQueuedMessages(provenChat ? (sessionId ?? "") : "");
  const canvas = useLocalCanvas(sessionId ?? "draft");
  const isMobile = useIsMobile();
  const threadAssociations = useThreadAssociations(sessionId ?? "", transcript.data);
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
      groupSessions(
        visibleSessionItems.filter((session) => !isInspectOnlySession(session)),
        chatFolders.state,
      ).flatMap((group) => group.items.map((session) => session.id)),
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
    planFollowController.current?.abort();
    planFollowController.current = undefined;
    activityRefresh.current = undefined;
    planReattach.current = undefined;
    setPlanContinuationActive(false);
    authorizationActivityCursor.current = undefined;
    activityFollowedSession.current = undefined;
    interruptedSettledSession.current = undefined;
    setDelegationFleet(createDelegationFleet(sessionId ?? ""));
    setDelegationAnchors({});
    setActivityFocus(undefined);
    setActivityFamily(undefined);
    activityOpenerFocus.current = undefined;
    activityOpener.current = null;
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
    ordinaryVerdictsInFlight.current.clear();
    ordinaryVerdictsUncertain.current.clear();
    planVerdictsInFlight.current.clear();
    planVerdictsUncertain.current.clear();
    settledAskKeys.current.clear();
    setFailedRun(sessionId ? readFailedRun(sessionId) : undefined);
    setLiveUsage(undefined);
    setSteerTrace([]);
    setShowSteerTrace(false);
    setContentPreview(undefined);
    setInspectionOpen(false);
    setConfirmPrompt(undefined);
    setTextPrompt(undefined);
    setSelectionAction(undefined);
    if (!activeRun.current || activeRun.current.sessionId !== sessionId)
      setStatusFacts({ phase: "idle" });
    if (!sessionId) {
      setMessages([]);
    }
    return () => {
      // Cleanup runs before a new session's effects set up, and before the
      // settled reader's cleanup on unmount. Old activity must not finalize
      // into a view that has already left its session.
      if (viewedSessionId.current === sessionId) viewedSessionId.current = undefined;
    };
  }, [sessionId, setMessages]);

  useEffect(
    () => () => {
      activeRun.current?.controller.abort();
      planFollowController.current?.abort();
    },
    [],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: follow every streamed message update while the reader remains at the bottom
  useEffect(() => {
    if (!atTranscriptBottom) return;
    const frame = window.requestAnimationFrame(() => {
      const element = transcriptScroll.current;
      if (element) element.scrollTop = element.scrollHeight;
    });
    return () => window.cancelAnimationFrame(frame);
  }, [atTranscriptBottom, messages]);

  const selectedSession =
    titledSessionItems.find((session) => session.id === sessionId) ?? inventorySession;
  const inspectOnlySelected = Boolean(selectedSession && isInspectOnlySession(selectedSession));
  const inspectOnlyTarget = inspectOnlySelected ? sessionId : undefined;
  useEffect(() => {
    if (inspectOnlyTarget) setInspectionOpen(true);
  }, [inspectOnlyTarget]);
  const chatStatus = deriveChatStatus({
    ...statusFacts,
    approvals,
    sessionState: selectedSession?.state,
  });

  useEffect(() => {
    if (!sessionId) {
      lastInventoryRow.current = undefined;
      return;
    }
    const row = sessions.data?.items.find((item) => item.id === sessionId);
    if (row) lastInventoryRow.current = row;
  }, [sessionId, sessions.data?.items]);

  // The inventory is the authority for titles and for deciding whether an idle
  // origin chat may have received a short scheduled delivery between polls.
  useEffect(() => {
    if (!sessionId || !visible || runtime.data?.connection !== "online") return;
    const inventoryQueryKey = listSessionsQueryKey();
    const transcriptQueryKey = getSessionTranscriptOptions({ path: { sessionId } }).queryKey;
    const interval = window.setInterval(() => {
      if (document.visibilityState === "hidden" || viewedSessionId.current !== sessionId) return;
      if (queryClient.isFetching({ exact: true, queryKey: inventoryQueryKey })) return;
      void (async () => {
        const result = await sessions.refetch();
        if (!result.isSuccess || viewedSessionId.current !== sessionId) return;
        const next = result.data.items.find((item) => item.id === sessionId);
        const previous = lastInventoryRow.current;
        if (next) lastInventoryRow.current = next;
        if (
          shouldRefreshTranscriptAfterInventory({
            connected: runtime.data?.connection === "online",
            idle: !activeRun.current && !isActiveSessionState(next?.state),
            lastTranscriptCheckAt: queryClient.getQueryState(transcriptQueryKey)?.dataUpdatedAt,
            next,
            now: Date.now(),
            previous,
            sessionId,
            visible: document.visibilityState !== "hidden",
          })
        ) {
          if (queryClient.isFetching({ exact: true, queryKey: transcriptQueryKey })) return;
          await transcript.refetch();
        }
      })();
    }, 20_000);
    return () => window.clearInterval(interval);
  }, [
    queryClient,
    runtime.data?.connection,
    sessionId,
    sessions.refetch,
    transcript.refetch,
    visible,
  ]);

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
          viewedSessionId.current === sessionId &&
          (connection.data?.connection === "offline" || (inventory.isSuccess && detail.isSuccess));
        const row = inventory.isSuccess
          ? inventory.data.items.find((item) => item.id === sessionId)
          : undefined;
        if (
          !awayTracker.current.isCurrentReturn(returnGeneration) ||
          document.visibilityState === "hidden" ||
          viewedSessionId.current !== sessionId
        ) {
          return;
        }
        if (row) lastInventoryRow.current = row;
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
          now,
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
  const displayedPreview =
    contentPreview?.kind === "authorization"
      ? {
          ...contentPreview,
          authorization:
            messages
              .flatMap((message) => message.authorizations ?? [])
              .find(
                (authorization) =>
                  authorization.sessionId === contentPreview.authorization.sessionId &&
                  authorization.authorizationId === contentPreview.authorization.authorizationId,
              ) ?? contentPreview.authorization,
        }
      : contentPreview;
  useEffect(() => {
    if (contentPreview && !panelWasOpen.current) {
      const focused = document.activeElement;
      if (focused instanceof HTMLElement && !focused.closest('[data-chat-surface="thread"]'))
        panelOpener.current = focused;
    } else if (!contentPreview) {
      panelOpener.current = null;
    }
    panelWasOpen.current = Boolean(contentPreview);
  }, [contentPreview]);
  const models: ComposerModelOption[] =
    runtimeSettings.data?.models
      .filter((model) => !disabledModels.has(modelPreferenceId(model)))
      .map((model) => ({
        id: model.id,
        image: model.image,
        label: model.displayName,
        providerId: model.providerId,
        reasoning: model.reasoning,
      })) ?? [];
  const watchable =
    Boolean(selectedSession && isProvenChatSession(selectedSession)) &&
    isActiveSessionState(selectedSession?.state);
  const shouldWatch = watchable || planContinuationActive;
  const settledState = selectedSession?.state ?? sessionDetail.data?.state;
  const settled =
    settledState === "idle" ||
    settledState === "completed" ||
    settledState === "failed" ||
    settledState === "cancelled";
  const pendingAuthorization = messages.some((message) =>
    message.authorizations?.some((authorization) => authorization.status === "pending"),
  );
  const uncertainAuthorization = sessionId
    ? [...authorizationUncertain.current.keys()].some((key) => key.startsWith(`${sessionId}\u0000`))
    : false;
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
  // The live chat's Mode, Model, and Compact controls are blocked while a run
  // is live or the chat is not a public chat, and while any of them is in flight.
  const sessionControlsDisabled = isRunning || !selectedSession?.capabilities.publicChat;
  const liveControlsDisabled =
    sessionControlsDisabled ||
    compactSession.isPending ||
    forkSession.isPending ||
    setMode.isPending;
  const seedModel = sessionId ? displayedDetail?.model : draftConfiguration.model;
  const seedModelId = seedModel?.id || (sessionId ? selectedSession?.modelId : undefined);
  const seedModelLabel = seedModelId
    ? (models.find(
        (model) =>
          model.id === seedModelId &&
          (!seedModel?.providerId || model.providerId === seedModel.providerId),
      )?.label ?? seedModelId)
    : "Automatic";
  const seedMode = sessionId ? displayedDetail?.mode : draftConfiguration.mode;
  const seedContext = {
    target: sessionId ? (selectedSession?.title ?? "Loading chat") : "New chat",
    model: seedModelLabel,
    mode:
      seedMode === "default"
        ? "Manual"
        : seedMode === "plan"
          ? "Plan"
          : seedMode === "acceptEdits"
            ? "Accept edits"
            : "Loading permission mode",
    toolAccess: sessionId
      ? undefined
      : draftConfiguration.toolAccess === "all"
        ? "All"
        : "No filesystem",
  };

  // A proven server-owned continuation may need a new reader even before the
  // session inventory reflects its execution run.
  // biome-ignore lint/correctness/useExhaustiveDependencies: helpers and QueryClient are stable for this lifetime
  useEffect(() => {
    const refresh = activityRefresh.current;
    const planRefresh = planReattach.current;
    if (
      !sessionId ||
      (!shouldWatch && refresh?.sessionId !== sessionId && planRefresh?.sessionId !== sessionId) ||
      activeRun.current ||
      protectedRequestsPaused()
    )
      return;

    const resumeFrom =
      planRefresh?.sessionId === sessionId
        ? planRefresh.cursor
        : refresh?.sessionId === sessionId
          ? refresh.cursor
          : undefined;
    activityRefresh.current = undefined;
    planReattach.current = undefined;
    const uncertain = refresh
      ? authorizationUncertain.current.get(`${sessionId}\u0000${refresh.authorizationId}`)
      : undefined;
    const continuation =
      uncertain && uncertain.callId === refresh?.callId ? uncertain.continuation : undefined;
    const controller = new AbortController();
    const owner: RunOwner = { controller, sessionId };
    activeRun.current = owner;
    setIsRunning(true);
    setReattached(true);
    setStatusFacts({ phase: "following" });
    if (planRefresh?.sessionId === sessionId) setRunTarget({ runId: planRefresh.runId, sessionId });
    setError(undefined);
    setNotice(undefined);
    setApprovals((current) => retainUncertainApprovals(current));
    if (!resumeFrom) {
      setMessages([]);
      // A status-driven attach can interrupt a settled replay. Preserve its
      // observed cards; their unfinished outcomes were marked unknown.
      if (interruptedSettledSession.current !== sessionId) {
        setDelegationFleet(createDelegationFleet(sessionId));
      }
      setDelegationAnchors({});
    }
    interruptedSettledSession.current = undefined;

    void (async () => {
      const streamFailure: StreamFailure = {};
      let end: RunStreamEnd = { kind: "unfollowed" };
      try {
        const stream = await watchActivity(owner, streamFailure, resumeFrom);
        end = await consumeRun(
          owner,
          stream,
          streamFailure,
          continuation?.activeAssistantId ?? crypto.randomUUID(),
          continuation?.activePrompt ?? "",
          !resumeFrom,
          undefined,
          refresh
            ? (kind, payload) => {
                if (kind !== "authorization.required") return;
                const candidate = payloadRecord(payload);
                if (
                  candidate?.authorizationId !== refresh.authorizationId ||
                  candidate.callId !== refresh.callId ||
                  candidate.status !== "pending"
                )
                  return;
                const key = `${sessionId}\u0000${candidate.authorizationId}`;
                if (authorizationUncertain.current.get(key) !== uncertain) return;
                authorizationUncertain.current.delete(key);
                setAuthorizationUncertainEpoch((current) => current + 1);
              }
            : undefined,
          undefined,
          continuation,
        );
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
          if (planRefresh?.sessionId === sessionId) setPlanContinuationActive(false);
          if (end.kind === "settled" || end.kind === "authorization") setRunTarget(undefined);
          setApprovals((current) => retainUncertainApprovals(current));
          setIsRunning(false);
          setReattached(false);
          if (end.kind === "authorization")
            setStatusFacts((current) => ({
              authorizationPending: current.authorizationPending,
              phase: "idle",
            }));
          else if (end.kind !== "settled")
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
        setApprovals((current) => retainUncertainApprovals(current));
        setIsRunning(false);
        setReattached(false);
      }
    };
  }, [reconnectGeneration, reattachEpoch, sessionId, shouldWatch]);

  // Finished chats keep their saved transcript. Rebuild only the delegation
  // projection from the bounded activity replay, with no live run owner or
  // controls. A session switch or new run aborts this read before it can fold.
  // biome-ignore lint/correctness/useExhaustiveDependencies: helpers are stable; the replay lifetime follows session and running state
  useEffect(() => {
    if (
      !sessionId ||
      !provenChat ||
      !settled ||
      isRunning ||
      activeRun.current ||
      pendingAuthorization ||
      uncertainAuthorization ||
      activityFollowedSession.current === sessionId ||
      protectedRequestsPaused()
    ) {
      return;
    }

    const controller = new AbortController();
    const owner: RunOwner = { controller, sessionId };
    const observedRuns = new Set<string>();
    let replayCompleted = false;
    const owns = () =>
      !controller.signal.aborted &&
      viewedSessionId.current === sessionId &&
      activeRun.current === undefined;
    const updateFleet = (update: (fleet: DelegationFleet) => DelegationFleet) =>
      setDelegationFleet((current) =>
        owns() && current.sessionId === sessionId ? update(current) : current,
      );
    const markUnfinished = (interrupted = false) =>
      setDelegationFleet((current) => {
        if (viewedSessionId.current !== sessionId || current.sessionId !== sessionId)
          return current;
        let next = current;
        for (const runId of observedRuns) {
          if (hasUnsettledDelegation(next, runId)) {
            next = markDelegationRunUnfollowed(next, runId);
          }
        }
        // A stopped history read may have unseen later runs even when every
        // observed child finished. Keep those cards intact and disclose the
        // unread remainder without inventing its contents.
        return interrupted ? { ...next, incompleteHistory: true } : next;
      });
    void (async () => {
      const streamFailure: StreamFailure = {};
      let reattaches = 0;
      try {
        let stream: AsyncIterable<RunStreamEvent> | undefined = await watchActivity(
          owner,
          streamFailure,
        );
        while (stream && owns()) {
          let resumeFrom: string | undefined;
          for await (const delivery of stream) {
            if (!owns()) break;
            if (delivery.type === "run.started" && delivery.sessionId !== sessionId) continue;
            if (delivery.type === "run.event" && delivery.event.runId) {
              observedRuns.add(delivery.event.runId);
            }
            updateFleet((current) => foldDelegationDelivery(current, delivery));
            if (delivery.type !== "run.truncated") continue;
            const decision = decideTruncation(delivery, reattaches);
            if (decision.action === "reattach") {
              resumeFrom = decision.cursor;
            } else {
              updateFleet(markDelegationHistoryIncomplete);
            }
            break;
          }
          stream = undefined;
          if (resumeFrom && !streamFailure.error && owns()) {
            reattaches += 1;
            stream = await watchActivity(owner, streamFailure, resumeFrom);
          }
        }
        if (streamFailure.error) throw streamFailure.error;
        replayCompleted = owns();
      } catch {
        if (owns()) {
          updateFleet(markDelegationHistoryIncomplete);
        }
      } finally {
        if (owns()) markUnfinished();
      }
    })();

    return () => {
      controller.abort();
      // A new run in this same chat cuts replay short. Keep the observed card,
      // but do not keep claiming its old child is still running. On a status
      // driven attach this cleanup precedes the live reader's setup.
      if (viewedSessionId.current === sessionId) {
        markUnfinished(!replayCompleted);
        if (!activeRun.current) interruptedSettledSession.current = sessionId;
      }
    };
  }, [sessionId, provenChat, settled, isRunning, pendingAuthorization, uncertainAuthorization]);

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
    if (!sessionId || !selectedSession?.capabilities.fork || !displayedDetail?.model) return;
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
    if (!sessionId || !selectedSession?.capabilities.fork) return;
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
    const sourceId = sessionId;
    const knownRoot = await recordedRootForMessage(message, transcript.data);
    const knownKey = knownRoot && threadKeyForRoot(knownRoot);
    const known =
      knownKey &&
      (await readThreadAssociations(sourceId, transcript.data)).byKey[knownKey]?.sessionId;
    if (known && viewedSessionId.current === sourceId) {
      setContentPreview({
        kind: "thread",
        messageKey: knownKey,
        parentSessionId: sourceId,
        sessionId: known,
      });
      return;
    }
    if (isRunning) {
      setNotice("Wait for the current response to finish before starting a side thread.");
      return;
    }
    if (threadCreation.current) return;

    threadCreation.current = true;
    setError(undefined);
    let creating = false;
    try {
      // A render-local anchor, even one with a saved ordinal, is insufficient
      // until the latest authoritative transcript proves its final role/text.
      const refreshed = await transcript.refetch();
      if (viewedSessionId.current !== sourceId) return;
      if (!refreshed.isSuccess) {
        setNotice("Could not refresh the saved transcript. Try again.");
        return;
      }
      const latest = refreshed.data;
      const root = await recordedRootForMessage(message, latest);
      if (!root || latest?.sessionId !== sourceId) {
        setNotice("Wait for this message to be saved before starting a side thread.");
        return;
      }
      const messageKey = threadKeyForRoot(root);
      const existing = (await readThreadAssociations(sourceId, latest)).byKey[messageKey]
        ?.sessionId;
      if (existing) {
        setContentPreview({
          kind: "thread",
          messageKey,
          parentSessionId: sourceId,
          sessionId: existing,
        });
        return;
      }
      const model = displayedDetail?.model;
      if (!model) {
        setError("This chat has no resolved model to carry into a side thread.");
        return;
      }
      setNotice("Creating a focused side thread…");
      creating = true;
      // Fork copies the source's current full history. This row is only its UI anchor.
      const successor = await forkSession.mutateAsync({
        body: {
          model: { id: model.id, providerId: model.providerId },
          reasoningEffort: model.reasoningEffort,
        },
        path: { sessionId: sourceId },
      });
      registerThreadSession(sourceId, messageKey, successor.id);
      try {
        await renameSession.mutateAsync({
          body: { title: threadTitleFromRoot(message.content) },
          path: { sessionId: successor.id },
        });
      } catch {
        // The association is still valid if the optional presentation rename fails.
      }
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
      if (viewedSessionId.current === sourceId) {
        setContentPreview({
          kind: "thread",
          messageKey,
          parentSessionId: sourceId,
          sessionId: successor.id,
        });
      }
    } catch (caught) {
      if (viewedSessionId.current === sourceId) setError(errorMessage(caught));
    } finally {
      threadCreation.current = false;
      if (creating && viewedSessionId.current === sourceId) setNotice(undefined);
    }
  }

  async function relinkOlderThread(message: ChatMessage) {
    if (!sessionId || isRunning) return;
    const sourceId = sessionId;
    try {
      const refreshed = await transcript.refetch();
      if (viewedSessionId.current !== sourceId) return;
      if (!refreshed.isSuccess) {
        setNotice("Could not refresh the saved transcript. Try again.");
        return;
      }
      const latest = refreshed.data;
      const root = await recordedRootForMessage(message, latest);
      if (!root || latest?.sessionId !== sourceId) {
        setNotice("Wait for this message to be saved before relinking its thread.");
        return;
      }
      const messageKey = threadKeyForRoot(root);
      const associations = await readThreadAssociations(sourceId, latest);
      const candidate = associations.legacyCandidates[messageKey];
      if (
        !candidate ||
        !(await relinkLegacyThread(sourceId, candidate.legacyKey, messageKey, latest))
      ) {
        setNotice("The older thread is no longer available for this message.");
        return;
      }
      setContentPreview({
        kind: "thread",
        messageKey,
        parentSessionId: sourceId,
        sessionId: candidate.sessionId,
      });
    } catch (caught) {
      if (viewedSessionId.current === sourceId) setError(errorMessage(caught));
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
      if (end.kind === "settled" || end.kind === "authorization") setRunTarget(undefined);
      setApprovals((current) => retainUncertainApprovals(current));
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
    if (end.kind === "authorization")
      setStatusFacts((current) => ({
        authorizationPending: current.authorizationPending,
        phase: "idle",
      }));
    else if (end.kind !== "settled")
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
    if (sessionId && !selectedSession?.capabilities.publicChat) return false;
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

  /** Steers one queued message into the live run. It leaves the queue only once the steer is accepted. */
  async function steerQueuedMessage(id: string) {
    const item = queuedMessages.items.find((candidate) => candidate.id === id);
    const target = controlTarget(runTarget, sessionId);
    if (!item || !target || steeringQueuedId) return;
    setSteeringQueuedId(id);
    setError(undefined);
    setNotice(undefined);
    try {
      await steerRun({
        body: { text: item.text },
        path: { runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      queuedMessages.remove(id);
      setNotice("Sent — steering the active run.");
    } catch (caught) {
      if (isStaleRunControl(caught))
        setNotice("The run ended before steering. The message stays queued.");
      else setError(errorMessage(caught));
    } finally {
      setSteeringQueuedId(undefined);
    }
  }

  function drainNextQueuedMessage(activeSessionId: string) {
    if (protectedRequestsPaused()) return;
    if (viewedSessionId.current !== activeSessionId) return;
    const next = takeNextQueuedMessage(activeSessionId);
    if (next) void sendPrompt(next.text, [], activeSessionId);
  }

  async function retryFailedRun() {
    if (!sessionId || !failedRun || failedRun.permanent || isRunning) return;
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
      onSseEvent: ({ data, id }) => {
        if (
          !id ||
          data?.type !== "run.event" ||
          data.event.kind !== "authorization.required" ||
          !owner.sessionId ||
          !ownsChatView(owner, activeRun.current, viewedSessionId.current)
        )
          return;
        const payload = payloadRecord(data.event.payload);
        if (
          typeof payload?.authorizationId !== "string" ||
          typeof payload.callId !== "string" ||
          payload.status !== "pending"
        )
          return;
        const previous = authorizationActivityCursor.current;
        // A later status may omit runId. It can advance a cursor only when
        // it matches an already identified handoff.
        if (
          !data.event.runId &&
          (previous?.sessionId !== owner.sessionId ||
            previous.authorizationId !== payload.authorizationId ||
            previous.callId !== payload.callId)
        )
          return;
        authorizationActivityCursor.current = {
          authorizationId: payload.authorizationId,
          callId: payload.callId,
          cursor: id,
          runId: data.event.runId || previous?.runId || "",
          sessionId: owner.sessionId,
        };
      },
      path: { sessionId: owner.sessionId ?? "" },
      query: resumeFrom ? { resumeFrom } : undefined,
      signal: owner.controller.signal,
      sseMaxRetryAttempts: 1,
    });
    return response.stream;
  }

  /** Find the displayed handoff's durable position before sending a control. */
  async function authorizationCheckpoint(authorization: AuthorizationHandoff) {
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 5_000);
    let resumeFrom: string | undefined;
    try {
      for (let page = 0; page <= MAX_ACTIVITY_REATTACHES; page += 1) {
        let checkpoint: string | undefined;
        const streamFailure: StreamFailure = {};
        const response = await watchSessionActivity({
          onSseError: captureSseFailure(streamFailure),
          onSseEvent: ({ data, id }) => {
            if (!id || data?.type !== "run.event") return;
            const event = data.event;
            const payload = payloadRecord(event.payload);
            if (
              event.kind === "authorization.required" &&
              event.runId === authorization.runId &&
              payload?.authorizationId === authorization.authorizationId &&
              payload.callId === authorization.callId &&
              payload.status === "pending"
            )
              checkpoint = id;
          },
          path: { sessionId: authorization.sessionId },
          query: resumeFrom ? { resumeFrom } : undefined,
          signal: controller.signal,
          sseMaxRetryAttempts: 1,
        });
        let nextCursor: string | undefined;
        for await (const delivery of response.stream) {
          if (checkpoint) return checkpoint;
          if (delivery.type === "run.error") return undefined;
          if (delivery.type === "run.truncated") {
            if (delivery.reason !== "bound" || !delivery.cursor) return undefined;
            nextCursor = delivery.cursor;
            break;
          }
        }
        if (streamFailure.error || !nextCursor) return undefined;
        resumeFrom = nextCursor;
      }
    } catch {
      // No control is sent without a durable before-position.
    } finally {
      controller.abort();
      window.clearTimeout(timeout);
    }
    return undefined;
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
    onAuthorizationStatus?: (kind: string, payload: unknown) => void,
    onRunError?: () => void,
    continuation?: AuthorizationContinuation,
  ): Promise<RunStreamEnd> {
    let failure: RunFailure | undefined;
    let sawResult = false;
    let sawAuthorizationPark = false;
    let sawAuthorizationStatus = false;
    let continuationStarted = false;
    let activeAssistantId = continuation?.activeAssistantId ?? assistantId;
    let activePrompt = continuation?.activePrompt ?? prompt;
    let turnStartedAt = Date.now();
    let followedRunId: string | undefined = continuation?.followedRunId;
    let reattaches = 0;
    let unfollowed = false;
    const shouldApply = continuation?.shouldApply ?? createActivityDeduplicator();
    const owns = () => ownsChatView(owner, activeRun.current, viewedSessionId.current);
    const persistContinuation = () => {
      if (!continuation) return;
      continuation.activeAssistantId = activeAssistantId;
      continuation.activePrompt = activePrompt;
      continuation.followedRunId = followedRunId;
    };

    // A prior run in this chat may have completed, but this reader must earn
    // its own terminal observation before it can suppress settled replay.
    if (owns() && activityFollowedSession.current === owner.sessionId) {
      activityFollowedSession.current = undefined;
    }
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
        setDelegationFleet((current) =>
          current.sessionId === owner.sessionId
            ? foldDelegationDelivery(current, delivery)
            : current,
        );
        if (delivery.type === "run.error") {
          onRunError?.();
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
            setDelegationFleet((current) =>
              current.sessionId === owner.sessionId
                ? markDelegationHistoryIncomplete(current)
                : current,
            );
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
          if (sawAuthorizationStatus) {
            continuationStarted = true;
            sawAuthorizationPark = false;
          }
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
          persistContinuation();
          continue;
        }
        const event = delivery.event;
        const observedSteer = observedSteerTrace(delivery);
        if (observedSteer) setSteerTrace((current) => appendSteerTrace(current, observedSteer));
        if (sawAuthorizationStatus && event.runId) {
          continuationStarted = true;
          sawAuthorizationPark = false;
        }
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
          persistContinuation();
        }
        const delegationFamily = delegationFamilyForKind(event.kind);
        const payload = event.payload;
        const parentCallId =
          typeof payload === "object" && payload !== null && "parentCallId" in payload
            ? payload.parentCallId
            : undefined;
        if (
          !event.unknown &&
          delegationFamily &&
          typeof parentCallId === "string" &&
          parentCallId.length > 0 &&
          owner.sessionId
        ) {
          ensureAssistant(activeAssistantId);
          const anchorKey = JSON.stringify([
            owner.sessionId,
            event.runId,
            delegationFamily,
            parentCallId,
          ]);
          const anchor = { assistantId: activeAssistantId };
          setDelegationAnchors((current) =>
            current[anchorKey] ? current : { ...current, [anchorKey]: anchor },
          );
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
          persistContinuation();
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
              tools: [...(message.tools ?? []), { ...tool, runId: event.runId || undefined }],
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
        } else if (
          event.kind === "authorization.required" ||
          event.kind === "authorization.resolved"
        ) {
          const authorizationSessionId = owner.sessionId;
          onAuthorizationStatus?.(event.kind, event.payload);
          sawAuthorizationStatus = true;
          if (authorizationSessionId) {
            setMessages((current) =>
              recordAuthorizationEvent(current, event, authorizationSessionId, activeAssistantId),
            );
            if (event.kind === "authorization.resolved") {
              const payload = payloadRecord(event.payload);
              if (
                typeof payload?.authorizationId === "string" &&
                typeof payload.status === "string" &&
                payload.status !== "pending" &&
                payload.status.length > 0
              ) {
                const key = `${authorizationSessionId}\u0000${payload.authorizationId}`;
                const uncertain = authorizationUncertain.current.get(key);
                if (
                  uncertain?.callId === payload.callId &&
                  authorizationUncertain.current.delete(key)
                )
                  setAuthorizationUncertainEpoch((current) => current + 1);
              }
            }
          }
          if (event.kind === "authorization.required") {
            sawAuthorizationPark = true;
            setRunTarget(undefined);
            setStatusFacts({ authorizationPending: true, phase: "following" });
          } else if (!event.runId) {
            setStatusFacts({ phase: "idle" });
          }
        } else if (event.kind === "permission.ask") {
          const approval = permissionAsk(event.payload);
          if (approval && !settledAskKeys.current.has(`${event.runId}\u0000${approval.askId}`)) {
            const controlTarget =
              owner.sessionId && event.runId
                ? { askId: approval.askId, runId: event.runId, sessionId: owner.sessionId }
                : undefined;
            setApprovals((current) =>
              enqueueApproval(current, controlTarget ? { ...approval, controlTarget } : approval),
            );
          }
        } else if (event.kind === "permission.retract") {
          const askId = permissionAskId(event.payload);
          if (askId) {
            settledAskKeys.current.add(`${event.runId}\u0000${askId}`);
            if (owner.sessionId) {
              const key = approvalKey({ askId, runId: event.runId, sessionId: owner.sessionId });
              ordinaryVerdictsUncertain.current.delete(key);
              planVerdictsUncertain.current.delete(key);
            }
            setOrdinaryVerdictEpoch((value) => value + 1);
          }
          setApprovals((current) => retractApproval(current, askId, event.runId || undefined));
          if (owner.sessionId) awayTracker.current.record(owner.sessionId, "approval-resolved");
        } else if (event.kind === "approval") {
          const askId = permissionAskId(event.payload);
          if (askId) {
            settledAskKeys.current.add(`${event.runId}\u0000${askId}`);
            if (owner.sessionId) {
              const key = approvalKey({ askId, runId: event.runId, sessionId: owner.sessionId });
              ordinaryVerdictsUncertain.current.delete(key);
              planVerdictsUncertain.current.delete(key);
            }
            setOrdinaryVerdictEpoch((value) => value + 1);
          }
          setApprovals((current) => retractApproval(current, askId, event.runId || undefined));
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
    persistContinuation();

    // Once the replay has caught up and the stream ended on its own, the
    // catch-up notice no longer describes anything.
    if (!unfollowed && owns()) {
      setNotice((current) => (current === CATCHING_UP_NOTICE ? undefined : current));
    }
    if (owns() && followedRunId) {
      setDelegationFleet((current) =>
        current.sessionId === owner.sessionId && hasUnsettledDelegation(current, followedRunId)
          ? markDelegationRunUnfollowed(current, followedRunId)
          : current,
      );
    }
    if (sawResult && !unfollowed && owns() && !streamFailure.error && owner.sessionId) {
      activityFollowedSession.current = owner.sessionId;
    }
    return runStreamEnd(
      {
        authorizationPark: sawAuthorizationPark,
        authorizationStatus: sawAuthorizationStatus,
        continuationStarted,
        failure,
        sawResult,
      },
      unfollowed,
    );
  }

  async function operateAuthorization(
    operation: AuthorizationOperation,
    authorization: AuthorizationHandoff,
  ) {
    if (
      viewedSessionId.current !== authorization.sessionId ||
      authorization.status !== "pending" ||
      protectedRequestsPaused()
    )
      return;
    const key = `${authorization.sessionId}\u0000${authorization.authorizationId}`;
    if (authorizationUncertain.current.has(key)) return;
    // One view has one stream owner; another authorization control cannot
    // replace an in-flight control for a different handoff either.
    if (authorizationFlows.current.size > 0) return;
    authorizationFlows.current.add(key);
    setAuthorizationBusy(key);
    const observed = authorizationActivityCursor.current;
    let beforeCursor =
      observed?.sessionId === authorization.sessionId &&
      observed.runId === authorization.runId &&
      observed.authorizationId === authorization.authorizationId &&
      observed.callId === authorization.callId
        ? observed.cursor
        : undefined;
    if (!beforeCursor) {
      beforeCursor = await authorizationCheckpoint(authorization);
      if (!beforeCursor) {
        authorizationFlows.current.delete(key);
        setAuthorizationBusy((current) => (current === key ? undefined : current));
        setError(
          "Could not locate this handoff in durable activity. Refresh the chat and try again.",
        );
        return;
      }
      authorizationActivityCursor.current = {
        authorizationId: authorization.authorizationId,
        callId: authorization.callId,
        cursor: beforeCursor,
        runId: authorization.runId,
        sessionId: authorization.sessionId,
      };
    }

    // The previous activity reader only follows this view. Aborting it does
    // not cancel the server-owned authorization; the SDK control is separate.
    const oldOwner = activeRun.current;
    const owner: RunOwner = {
      controller: new AbortController(),
      sessionId: authorization.sessionId,
    };
    activeRun.current = owner;
    oldOwner?.controller.abort();
    setIsRunning(true);
    let end: RunStreamEnd = { kind: "uncertain" };
    let sawMatchingStatus = false;
    let sawStreamError = false;
    const continuation: AuthorizationContinuation = {
      activeAssistantId: crypto.randomUUID(),
      activePrompt: "",
      shouldApply: createActivityDeduplicator(),
    };
    try {
      const streamFailure: StreamFailure = {};
      const request = {
        onSseError: captureSseFailure(streamFailure),
        path: {
          authorizationId: authorization.authorizationId,
          sessionId: authorization.sessionId,
        },
        signal: owner.controller.signal,
        sseMaxRetryAttempts: 1,
      };
      const response =
        operation === "recheck"
          ? await recheckAuthorization(request)
          : await cancelAuthorization(request);
      end = await consumeRun(
        owner,
        response.stream,
        streamFailure,
        continuation.activeAssistantId,
        "",
        false,
        undefined,
        (_kind, payload) => {
          const candidate = payloadRecord(payload);
          if (
            candidate?.authorizationId === authorization.authorizationId &&
            candidate.callId === authorization.callId &&
            typeof candidate.status === "string" &&
            candidate.status.length > 0 &&
            (_kind === "authorization.required"
              ? candidate.status === "pending"
              : candidate.status !== "pending")
          )
            sawMatchingStatus = true;
        },
        () => {
          sawStreamError = true;
        },
        continuation,
      );
      if (streamFailure.error && !owner.controller.signal.aborted) throw streamFailure.error;
      if (
        !owner.controller.signal.aborted &&
        (!sawMatchingStatus ||
          sawStreamError ||
          end.kind === "unfollowed" ||
          (end.kind === "settled" && end.failure))
      )
        throw new Error("Authorization status was not confirmed by activity.");
    } catch (caught) {
      if (!owner.controller.signal.aborted && viewedSessionId.current === authorization.sessionId) {
        authorizationUncertain.current.set(key, {
          beforeCursor,
          callId: authorization.callId,
          continuation,
        });
        setAuthorizationUncertainEpoch((current) => current + 1);
        setError(`Could not confirm authorization outcome: ${errorMessage(caught)}`);
      }
      throw caught;
    } finally {
      authorizationFlows.current.delete(key);
      setAuthorizationBusy((current) => (current === key ? undefined : current));
      await finishRun(owner, end);
    }
  }

  function refreshAuthorizationActivity(authorization: AuthorizationHandoff) {
    const key = `${authorization.sessionId}\u0000${authorization.authorizationId}`;
    const uncertain = authorizationUncertain.current.get(key);
    if (
      viewedSessionId.current !== authorization.sessionId ||
      !uncertain ||
      protectedRequestsPaused()
    )
      return;
    // Resume after the last observed event before the control. An opaque cursor
    // establishes ordering without comparing run-local event sequence numbers.
    activityRefresh.current = {
      authorizationId: authorization.authorizationId,
      callId: authorization.callId,
      cursor: uncertain.beforeCursor,
      sessionId: authorization.sessionId,
    };
    setReconnectGeneration((current) => current + 1);
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

  function retainUncertainApprovals(current: ApprovalRequest[]): ApprovalRequest[] {
    return current.filter(
      (candidate) =>
        candidate.controlTarget &&
        (ordinaryVerdictsUncertain.current.has(approvalKey(candidate.controlTarget)) ||
          planVerdictsUncertain.current.has(approvalKey(candidate.controlTarget))),
    );
  }

  function ordinaryApprovalUncertain(candidate: ApprovalRequest): boolean {
    void ordinaryVerdictEpoch;
    return Boolean(
      candidate.controlTarget &&
        ordinaryVerdictsUncertain.current.has(approvalKey(candidate.controlTarget)),
    );
  }

  function ordinaryApprovalDisabled(candidate: ApprovalRequest): boolean {
    const target = candidate.controlTarget;
    const activeTarget = controlTarget(runTarget, sessionId);
    return Boolean(
      !target ||
        target.askId !== candidate.askId ||
        target.sessionId !== activeTarget?.sessionId ||
        target.runId !== activeTarget?.runId ||
        target.sessionId !== viewedSessionId.current ||
        controlPending ||
        ordinaryVerdictsInFlight.current.has(approvalKey(target)) ||
        ordinaryVerdictsUncertain.current.has(approvalKey(target)),
    );
  }

  async function respondToApproval(candidate: ApprovalRequest, verdict: ApprovalVerdict) {
    if (
      candidate.tool === "PresentPlan" ||
      (verdict !== "deny" && !candidate.args.trim()) ||
      ordinaryApprovalDisabled(candidate)
    )
      return;
    const target = candidate.controlTarget;
    if (!target) return;
    const key = approvalKey(target);
    ordinaryVerdictsInFlight.current.add(key);
    setControlPending(true);
    try {
      await resolveRunPermission({
        body: { verdict },
        path: { askId: target.askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      settledAskKeys.current.add(`${target.runId}\u0000${target.askId}`);
      if (viewedSessionId.current === target.sessionId) {
        setApprovals((current) => retractApproval(current, target.askId, target.runId));
        setNotice(`Permission ${verdict.replaceAll("_", " ")} recorded.`);
      }
    } catch (caught) {
      ordinaryVerdictsUncertain.current.add(key);
      setOrdinaryVerdictEpoch((value) => value + 1);
      if (viewedSessionId.current === target.sessionId)
        setError(`Could not confirm this verdict: ${errorMessage(caught)}`);
    } finally {
      ordinaryVerdictsInFlight.current.delete(key);
      setControlPending(false);
    }
  }

  function planUnavailableReason(candidate: ApprovalRequest): string | undefined {
    const featureReason = exactPlanControlAvailability(runtime.data?.features).reason;
    if (featureReason) return featureReason;
    const activeTarget = controlTarget(runTarget, sessionId);
    const target = candidate.controlTarget;
    return !target ||
      target.askId !== candidate.askId ||
      target.sessionId !== activeTarget?.sessionId ||
      target.runId !== activeTarget?.runId ||
      target.sessionId !== viewedSessionId.current
      ? "This plan ask is stale or its exact run is unavailable. Refresh activity."
      : undefined;
  }

  function planApprovalDisabled(candidate: ApprovalRequest): boolean {
    void planVerdictEpoch;
    const target = candidate.controlTarget;
    return Boolean(
      !target ||
        controlPending ||
        planVerdictsInFlight.current.has(approvalKey(target)) ||
        planVerdictsUncertain.current.has(approvalKey(target)),
    );
  }

  function planApprovalUncertain(candidate: ApprovalRequest): boolean {
    void planVerdictEpoch;
    return Boolean(
      candidate.controlTarget &&
        planVerdictsUncertain.current.has(approvalKey(candidate.controlTarget)),
    );
  }

  async function respondToPlan(candidate: ApprovalRequest, verdict: PlanVerdict) {
    if (
      candidate.tool !== "PresentPlan" ||
      planUnavailableReason(candidate) ||
      planApprovalDisabled(candidate)
    ) {
      setError("This plan ask is stale. Refresh activity before deciding.");
      return;
    }
    const target = candidate.controlTarget;
    if (!target) return;
    const key = approvalKey(target);
    planVerdictsInFlight.current.add(key);
    const askId = target.askId;
    setControlPending(true);
    setError(undefined);
    try {
      await resolvePlanAsk({
        body: { verdict },
        path: { askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      settledAskKeys.current.add(`${target.runId}\u0000${askId}`);
      if (viewedSessionId.current === target.sessionId) {
        setApprovals((current) => retractApproval(current, askId, target.runId));
      }
      if (viewedSessionId.current !== target.sessionId) return;
      if (verdict === "iterate") {
        if (viewedSessionId.current === target.sessionId) setNotice("Plan iteration requested.");
        return;
      }
      if (viewedSessionId.current === target.sessionId) {
        setNotice("Plan approval accepted; checking whether execution started.");
      }
      const controller = new AbortController();
      planFollowController.current = controller;
      const evidence = await followPlanContinuationFromBff(
        { askId, planRunId: target.runId, sessionId: target.sessionId },
        controller.signal,
      );
      if (controller.signal.aborted || viewedSessionId.current !== target.sessionId) return;
      if (evidence.kind === "started") {
        // The verdict watcher establishes the new run and a durable replay
        // checkpoint. Transfer this view's one reader to that run so its
        // transcript and controls follow the same evidence.
        planReattach.current = {
          cursor: evidence.resumeFrom,
          runId: evidence.runId,
          sessionId: target.sessionId,
        };
        setPlanContinuationActive(true);
        activeRun.current?.controller.abort();
        activeRun.current = undefined;
        setRunTarget({ runId: evidence.runId, sessionId: target.sessionId });
        setReattachEpoch((current) => current + 1);
      } else if (evidence.kind === "failed") {
        setNotice(undefined);
        setError("Plan approval was recorded, but execution could not start.");
      } else {
        setNotice(undefined);
        setError(
          "Plan approval was recorded, but could not confirm whether execution started. Refresh activity to check.",
        );
      }
    } catch (caught) {
      planVerdictsUncertain.current.add(key);
      setPlanVerdictEpoch((value) => value + 1);
      if (viewedSessionId.current === target.sessionId)
        setError(`Could not confirm this verdict: ${errorMessage(caught)}`);
    } finally {
      planFollowController.current = undefined;
      planVerdictsInFlight.current.delete(key);
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
      // The toolbar centres on this point; keep its three actions on screen.
      left: Math.min(
        window.innerWidth - SELECTION_TOOLBAR_HALF_WIDTH,
        Math.max(SELECTION_TOOLBAR_HALF_WIDTH, rect.left + rect.width / 2),
      ),
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
    if (sessionId && selectedSession?.capabilities.copyId)
      void copyToClipboard(sessionId, "Session ID");
  });
  useShortcut("chat.fork", () => {
    if (selectedSession && isProvenChatSession(selectedSession)) void forkStandaloneChat();
  });
  useShortcut("chat.clear", () => {
    if (selectedSession && isProvenChatSession(selectedSession)) void clearStandaloneChat();
  });
  useShortcut("chat.expandDetails", () => setExpandDetails(!expandDetails));
  useShortcutSuppression(inspectionOpen || Boolean(confirmPrompt) || Boolean(textPrompt));
  const escapeAsk =
    approvals.find((candidate) => {
      const target = candidate.controlTarget;
      return target?.sessionId === sessionId && target?.runId === runTarget?.runId;
    }) ?? approvals.find((candidate) => candidate.controlTarget?.sessionId === sessionId);
  useChatEscape({
    active: true,
    askAvailable: escapeAsk
      ? escapeAsk.tool === "PresentPlan"
        ? !planApprovalDisabled(escapeAsk)
        : !ordinaryApprovalDisabled(escapeAsk)
      : false,
    draft: composerDraftPresent,
    onClearDraft: () => setClearDraftSignal((value) => value + 1),
    onClosePanel: () => {
      if (contentPreview) {
        setContentPreview(undefined);
        if (contentPreview.kind === "activity") {
          restoreActivityOpenerFocus({
            fallbackOpener: sessionActivityControl.current,
            opener: activityOpener.current,
            openerFocus: activityOpenerFocus.current,
          });
        } else {
          const opener = panelOpener.current;
          if (opener?.isConnected) opener.focus();
        }
      } else setSidebarOpen(false);
    },
    onDenyAsk: () => {
      if (escapeAsk) void respondToApproval(escapeAsk, "deny");
    },
    onHintChange: setEscapeClearHint,
    onIteratePlan: () => {
      if (escapeAsk) void respondToPlan(escapeAsk, "iterate");
    },
    onStopRun: () => void stopRun(),
    onUnavailablePlan: () =>
      setError(
        escapeAsk
          ? (planUnavailableReason(escapeAsk) ??
              "Exact plan verdict is unavailable. Refresh activity.")
          : "Exact plan verdict is unavailable.",
      ),
    navigationKey: sessionId,
    ownsFocus: (target) => {
      const node = target instanceof Node ? target : document.activeElement;
      if (chatOptionsEscapeSession.current !== undefined) {
        const sameSession = chatOptionsEscapeSession.current === sessionId;
        chatOptionsEscapeSession.current = undefined;
        if (sameSession && node === document.body) return true;
      }
      return Boolean(
        node &&
          workspaceRoot.current?.contains(node) &&
          !(node instanceof Element && node.closest('[data-chat-surface="thread"]')),
      );
    },
    overlayOpen: inspectionOpen || Boolean(confirmPrompt) || Boolean(textPrompt),
    panelOpen: Boolean(contentPreview || sidebarOpen),
    pendingAsk: escapeAsk?.tool === "PresentPlan" ? "plan" : escapeAsk ? "ordinary" : "none",
    planAvailable: escapeAsk?.tool === "PresentPlan" && !planUnavailableReason(escapeAsk),
    runActive: isRunning && Boolean(controlTarget(runTarget, sessionId)),
  });
  const openApprovalDetail = useCallback(
    (approval: ApprovalRequest) =>
      setContentPreview({
        askId: approval.askId,
        kind: "approval",
        runId: approval.controlTarget?.runId ?? "",
      }),
    [],
  );
  const detailIndex =
    contentPreview?.kind === "approval"
      ? approvals.findIndex(
          (candidate) =>
            candidate.askId === contentPreview.askId &&
            (candidate.controlTarget?.runId ?? "") === contentPreview.runId,
        )
      : -1;
  const detailApproval = detailIndex >= 0 ? approvals[detailIndex] : undefined;
  // The detail panel shows a live ask only: once the ledger settles it (or the
  // stream retracts it) the panel closes with its card.
  useEffect(() => {
    if (contentPreview?.kind === "approval" && !detailApproval) setContentPreview(undefined);
  }, [contentPreview, detailApproval]);

  return (
    <div className="relative flex h-full min-w-0" data-chat-surface="workspace" ref={workspaceRoot}>
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

      {sessionId && (!selectedSession || !isProvenChatSession(selectedSession)) ? (
        <section className="flex min-w-0 flex-1 flex-col bg-background">
          <header className="flex h-16 shrink-0 items-center gap-3 border-b px-4 sm:px-6">
            <ChatsMenuButton
              onClick={() => {
                setSidebarHidden(false);
                setSidebarOpen(true);
              }}
              showOnDesktop={sidebarHidden}
            />
            <h1 className="truncate text-sm font-semibold">
              {selectedSession?.title ?? "Loading session…"}
            </h1>
          </header>
          {selectedSession && isInspectOnlySession(selectedSession) ? (
            <div className="m-auto flex flex-col items-center gap-3 p-6 text-center">
              <p className="text-sm text-muted-foreground">Inspect-only session · Read-only</p>
              <Button onClick={() => setInspectionOpen(true)} variant="outline">
                Session details
              </Button>
            </div>
          ) : (
            <p className="m-auto text-sm text-muted-foreground">
              {sessions.isPending ? "Checking session…" : "This session is unavailable as a chat."}
            </p>
          )}
        </section>
      ) : (
        <section className="relative flex min-w-0 flex-1 flex-col bg-background">
          <header className="flex h-16 shrink-0 items-center gap-3 border-b px-4 max-[499px]:gap-2 max-[499px]:px-3 sm:px-6">
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
              {sessionId && <PermissionModeBadge mode={displayedDetail?.mode} />}
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
            <FleetStatusChip
              className="max-[499px]:hidden"
              fleet={sessionId ? visibleDelegationFleet : undefined}
              onOpen={(family, opener) => {
                activityOpener.current = opener;
                activityOpenerFocus.current = undefined;
                setActivityFocus(undefined);
                setActivityFamily(family);
                setActivityFocusRequest((value) => value + 1);
                setContentPreview({ kind: "activity" });
              }}
            />
            <Button
              aria-label="Open session activity"
              disabled={!sessionId}
              onClick={(event) => {
                activityOpener.current = event.currentTarget;
                activityOpenerFocus.current = undefined;
                setActivityFocus(undefined);
                setActivityFamily(undefined);
                setActivityFocusRequest((value) => value + 1);
                setContentPreview({ kind: "activity" });
              }}
              ref={sessionActivityControl}
              size="sm"
              variant="ghost"
            >
              <ListTodo aria-hidden="true" />
              <span className="hidden sm:inline">Activity</span>
            </Button>
            {!(isMobile && sessionId) && (
              <Button
                aria-label="Open local canvas"
                onClick={() => setContentPreview({ kind: "canvas" })}
                size="sm"
                variant="ghost"
              >
                <NotebookPen aria-hidden="true" />
                <span className="hidden sm:inline">Canvas</span>
              </Button>
            )}
            {sessionId && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    aria-label="Chat options"
                    ref={chatOptionsTrigger}
                    size="icon"
                    variant="ghost"
                  >
                    <MoreHorizontal aria-hidden="true" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent
                  align="end"
                  className="w-52"
                  onEscapeKeyDown={() => {
                    chatOptionsEscapeSession.current = sessionId;
                  }}
                  onCloseAutoFocus={(event) => {
                    event.preventDefault();
                    chatOptionsTrigger.current?.focus();
                    if (chatOptionsOpensCanvas.current) {
                      chatOptionsOpensCanvas.current = false;
                      setContentPreview({ kind: "canvas" });
                    }
                  }}
                >
                  <DropdownMenuItem onSelect={() => setInspectionOpen(true)}>
                    <ListTree aria-hidden="true" />
                    Inspect session
                  </DropdownMenuItem>
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
                  <DropdownMenuItem onSelect={() => setShowSteerTrace(!showSteerTrace)}>
                    <Bug aria-hidden="true" />
                    {showSteerTrace ? "Hide developer steer trace" : "Show developer steer trace"}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onSelect={() => void copyToClipboard(sessionId, "Session ID")}>
                    <Copy aria-hidden="true" />
                    Copy session ID
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={
                      forkSession.isPending ||
                      !displayedDetail?.model ||
                      !selectedSession?.capabilities.fork
                    }
                    onSelect={() => void forkStandaloneChat()}
                  >
                    <GitFork aria-hidden="true" />
                    Fork chat
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={clearSession.isPending || !selectedSession?.capabilities.fork}
                    onSelect={() => void clearStandaloneChat()}
                  >
                    <Eraser aria-hidden="true" />
                    Clear conversation
                  </DropdownMenuItem>
                  {isMobile && (
                    <DropdownMenuItem
                      onSelect={() => {
                        chatOptionsOpensCanvas.current = true;
                      }}
                    >
                      <NotebookPen aria-hidden="true" />
                      Open local canvas
                    </DropdownMenuItem>
                  )}
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
                  <LoaderCircle
                    aria-hidden="true"
                    className="size-4 shrink-0 animate-spin text-brand min-[500px]:hidden"
                  />
                )}
                {isRunning && (
                  <Badge className="max-[499px]:sr-only" variant="success">
                    {statusFacts.authorizationPending
                      ? "Waiting for authorization"
                      : reattached
                        ? "Reconnected to run"
                        : "Mecatl is working"}
                  </Badge>
                )}
                {controlTarget(runTarget, sessionId) && (
                  <>
                    <Button
                      className="max-[499px]:size-11 max-[499px]:px-0"
                      disabled={controlPending}
                      onClick={() => void stopRun()}
                      size="sm"
                      variant="outline"
                    >
                      <Square aria-hidden="true" className="fill-current" />
                      <span className="max-[499px]:sr-only">Stop</span>
                    </Button>
                    {!escapeAsk &&
                      !contentPreview &&
                      !sidebarOpen &&
                      isRunning &&
                      !controlPending && (
                        <span className="text-xs text-muted-foreground max-[499px]:hidden">
                          Esc to Stop
                        </span>
                      )}
                  </>
                )}
              </div>
            )}
          </header>

          <div className="relative min-h-0 flex-1">
            <section
              aria-label="Conversation transcript"
              className="h-full min-h-0 overflow-y-auto"
              onKeyDown={(event) => {
                if (
                  ["ArrowDown", "ArrowUp", "End", "Home", "PageDown", "PageUp", " "].includes(
                    event.key,
                  )
                ) {
                  minimapNavigation.current = false;
                }
              }}
              onPointerDown={() => {
                minimapNavigation.current = false;
              }}
              onPointerUp={captureTranscriptSelection}
              onScroll={(event) => {
                setSelectionAction(undefined);
                if (minimapNavigation.current) return;
                const element = event.currentTarget;
                setAtTranscriptBottom(isNearTranscriptBottom(element));
              }}
              onTouchMove={() => {
                minimapNavigation.current = false;
              }}
              onWheel={() => {
                minimapNavigation.current = false;
              }}
              ref={transcriptScroll}
            >
              <div className="mx-auto flex min-h-full max-w-3xl flex-col pt-2 pb-6 pl-4 pr-12 sm:pl-6">
                {transcript.isPending && sessionId && !isRunning ? (
                  <p className="m-auto text-sm text-muted-foreground">Loading conversation…</p>
                ) : messages.length === 0 && approvals.length === 0 ? (
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
                  <EscapeHintContext.Provider value={escapeAsk}>
                    <ApprovalDetailContext.Provider value={openApprovalDetail}>
                      <ChatTranscript
                        agentAvatar={agentAvatar}
                        agentName={agentName}
                        approvalDisabled={(candidate) =>
                          candidate.tool === "PresentPlan"
                            ? planApprovalDisabled(candidate)
                            : ordinaryApprovalDisabled(candidate)
                        }
                        approvalUncertain={(candidate) =>
                          candidate.tool === "PresentPlan"
                            ? planApprovalUncertain(candidate)
                            : ordinaryApprovalUncertain(candidate)
                        }
                        approvals={approvals}
                        delegationsByMessageId={delegationPlacement.byMessageId}
                        legacyThreadSessionIdForMessage={(message) => {
                          const key = matchingThreadKeyForMessage(
                            message,
                            transcript.data,
                            threadAssociations,
                          );
                          return key
                            ? threadAssociations.legacyCandidates[key]?.sessionId
                            : undefined;
                        }}
                        messages={messages}
                        onCopyMessage={(message) =>
                          void copyToClipboard(message.content, "Message")
                        }
                        onOpenActivity={(focus, opener) => {
                          activityOpener.current = opener;
                          activityOpenerFocus.current = focus;
                          setActivityFocus(focus);
                          setActivityFamily(undefined);
                          setActivityFocusRequest((value) => value + 1);
                          setContentPreview({ kind: "activity" });
                        }}
                        onOpenThread={(message) => void openSideThread(message)}
                        onRelinkThread={(message) => void relinkOlderThread(message)}
                        onReviewAuthorization={(authorization) =>
                          setContentPreview({ authorization, kind: "authorization" })
                        }
                        onPreviewImage={(image) =>
                          setContentPreview({ file: imagePreview(image, true), kind: "file" })
                        }
                        onPreviewTool={(tool) => setContentPreview({ kind: "tool", tool })}
                        onRespondToApproval={(candidate, verdict) =>
                          void respondToApproval(candidate, verdict)
                        }
                        onRespondToPlan={(candidate, verdict) =>
                          void respondToPlan(candidate, verdict)
                        }
                        planUnavailableReason={planUnavailableReason}
                        showToolCalls={showToolCalls}
                        streamingMessageId={
                          isRunning && messages.at(-1)?.role === "assistant"
                            ? messages.at(-1)?.id
                            : undefined
                        }
                        threadDisabled={forkSession.isPending}
                        threadSessionIdForMessage={(message) => {
                          const key = matchingThreadKeyForMessage(
                            message,
                            transcript.data,
                            threadAssociations,
                          );
                          return key ? threadAssociations.byKey[key]?.sessionId : undefined;
                        }}
                        userAvatar={userAvatar}
                        userName={userName}
                      />
                    </ApprovalDetailContext.Provider>
                  </EscapeHintContext.Provider>
                )}
                {delegationPlacement.unanchored.length > 0 && (
                  <DelegationCardRow
                    activities={delegationPlacement.unanchored}
                    onOpen={(focus, opener) => {
                      activityOpener.current = opener;
                      activityOpenerFocus.current = focus;
                      setActivityFocus(focus);
                      setActivityFamily(undefined);
                      setActivityFocusRequest((value) => value + 1);
                      setContentPreview({ kind: "activity" });
                    }}
                  />
                )}
              </div>
            </section>
            <MessageMinimap
              agentName={agentName}
              approvals={approvals}
              delegationsByMessageId={delegationPlacement.byMessageId}
              key={sessionId ?? "draft"}
              messages={messages}
              onNavigate={() => {
                minimapNavigation.current = true;
                setAtTranscriptBottom(false);
              }}
              scrollportRef={transcriptScroll}
              sessionId={sessionId}
              showToolCalls={showToolCalls}
              streamingMessageId={
                isRunning && messages.at(-1)?.role === "assistant" ? messages.at(-1)?.id : undefined
              }
              userName={userName}
            />
          </div>

          {!atTranscriptBottom && (
            <Button
              aria-label="Scroll to latest message"
              className="absolute bottom-32 left-1/2 z-10 size-9 -translate-x-1/2 rounded-full shadow-lg"
              onClick={() => {
                minimapNavigation.current = false;
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
          {showSteerTrace && <SteerTrace entries={steerTrace} />}
          <QueuedMessageStrip
            items={queuedMessages.items}
            onDelete={queuedMessages.remove}
            onEdit={queuedMessages.update}
            onSteer={
              isRunning && controlTarget(runTarget, sessionId)
                ? (id) => void steerQueuedMessage(id)
                : undefined
            }
            steeringId={steeringQueuedId}
          />
          {!escapeAsk &&
            !contentPreview &&
            !sidebarOpen &&
            !isRunning &&
            composerDraftPresent &&
            !escapeClearHint && (
              <p className="mx-auto mb-1.5 w-full max-w-3xl px-5 text-xs text-muted-foreground min-[500px]:px-7">
                Esc twice to clear draft
              </p>
            )}
          {escapeClearHint && (
            <p
              className="mx-auto mb-1.5 w-full max-w-3xl px-5 text-xs text-muted-foreground min-[500px]:px-7"
              role="status"
            >
              Press Escape again to clear the unsent draft.
            </p>
          )}
          <ChatComposer
            appendText={appendText}
            canForkModel={displayedDetail?.capabilities.modelSelection === true}
            clearDraftSignal={clearDraftSignal}
            configuration={sessionId ? undefined : draftConfiguration}
            disabled={
              createSession.isPending ||
              Boolean(sessionId && !selectedSession?.capabilities.publicChat)
            }
            draftKey={sessionId ?? "new"}
            imageAttachmentsSupported={imageAttachmentsSupported}
            liveControlsDisabled={liveControlsDisabled}
            liveMode={sessionId ? displayedDetail?.mode : undefined}
            liveModel={sessionId ? displayedDetail?.model : undefined}
            models={models}
            onConfigurationChange={setDraftConfiguration}
            onDraftChange={setComposerDraftPresent}
            onForkModel={(model, effort) => void forkConversation(model, effort)}
            onLiveModeChange={(mode) => void changeMode(mode)}
            onPreviewFile={(file) => setContentPreview({ file, kind: "file" })}
            onPreviewImage={(image) =>
              setContentPreview({ file: imagePreview(image, false), kind: "file" })
            }
            onAppendConsumed={() => setAppendText(undefined)}
            onSeedConsumed={() => {
              setSeedText(undefined);
              setSeedRequiresConfirmation(false);
            }}
            onSend={handleComposerSend}
            pillRowEnd={
              sessionId && displayedDetail ? (
                <SessionUsageControls
                  compacting={compactSession.isPending}
                  detail={displayedDetail}
                  disabled={sessionControlsDisabled}
                  forking={forkSession.isPending}
                  modePending={setMode.isPending}
                  onCompact={() => void compactConversation()}
                  variant="pills"
                />
              ) : undefined
            }
            seedCanConfirm={
              runtime.data?.connection === "online" &&
              !isRunning &&
              !(statusFacts.phase === "closed" && controlTarget(runTarget, sessionId)) &&
              !watchable &&
              !createSession.isPending &&
              (!sessionId || selectedSession?.capabilities.publicChat === true) &&
              (!sessionId || (Boolean(selectedSession) && sessionDetail.isSuccess))
            }
            seedContext={seedContext}
            seedRequiresConfirmation={seedRequiresConfirmation}
            seedText={seedText}
            sheetFooter={
              sessionId && displayedDetail ? (
                <SessionUsageControls
                  compacting={compactSession.isPending}
                  detail={displayedDetail}
                  disabled={sessionControlsDisabled}
                  forking={forkSession.isPending}
                  modePending={setMode.isPending}
                  onCompact={() => void compactConversation()}
                  variant="sheet"
                />
              ) : undefined
            }
            working={
              isRunning ||
              (statusFacts.phase === "closed" && Boolean(controlTarget(runTarget, sessionId)))
            }
            workingBehavior={enterSendBehavior}
          />
        </section>
      )}
      {selectedSession && inspectionOpen && (
        <SessionInspection
          key={selectedSession.id}
          onClose={() => setInspectionOpen(false)}
          onDebug={(targetId) => {
            if (targetId !== sessionId || !runtime.data?.capabilities.sessionDebug) return;
            setConfirmPrompt({
              confirmLabel: "Send evidence & debug",
              description: DEBUG_SESSION_CONSENT,
              onConfirm: () => void startDebugSession(targetId),
              title: `Debug with AI: “${selectedSession.title}”?`,
            });
          }}
          onOpenSuccessor={(id) => void selectSession(id)}
          runtime={runtime.data}
          session={selectedSession}
        />
      )}
      {displayedPreview && provenChat && (
        <EscapeHintContext.Provider value={escapeAsk}>
          <ContentPreviewPanel
            escapeManagedExternally
            escapeHint={!escapeAsk}
            approval={
              detailApproval
                ? {
                    approval: detailApproval,
                    disabled:
                      detailApproval.tool === "PresentPlan" ||
                      !detailApproval.controlTarget ||
                      ordinaryApprovalDisabled(detailApproval),
                    onRespond: (verdict) => void respondToApproval(detailApproval, verdict),
                    position: detailIndex + 1,
                    total: approvals.length,
                    uncertain: ordinaryApprovalUncertain(detailApproval),
                  }
                : undefined
            }
            activity={{
              fallbackOpener: sessionActivityControl.current,
              fleet: visibleDelegationFleet,
              family: activityFamily,
              focus: activityFocus,
              focusRequest: activityFocusRequest,
              onFocusChange: setActivityFocus,
              opener: activityOpener.current,
              openerFocus: activityOpenerFocus.current,
            }}
            authorizationDisabled={authorizationBusy !== undefined}
            authorizationUncertain={
              displayedPreview?.kind === "authorization" &&
              authorizationUncertain.current.has(
                `${displayedPreview.authorization.sessionId}\u0000${displayedPreview.authorization.authorizationId}`,
              )
            }
            canvas={canvas.value}
            onCanvasChange={canvas.setValue}
            onAuthorizationOperation={operateAuthorization}
            onRefreshAuthorizationActivity={refreshAuthorizationActivity}
            onClose={() => {
              setContentPreview(undefined);
              const opener = panelOpener.current;
              if (opener?.isConnected) opener.focus();
            }}
            preview={displayedPreview}
          />
        </EscapeHintContext.Provider>
      )}
      {selectionAction && (
        <TextSelectionToolbar
          left={selectionAction.left}
          onAddToCanvas={() => {
            canvas.setValue(appendCanvasQuote(canvas.value, selectionAction.text));
            setContentPreview({ kind: "canvas" });
            setSelectionAction(undefined);
            window.getSelection()?.removeAllRanges();
          }}
          onAddToChat={() => {
            setAppendText(selectionAction.text);
            setSelectionAction(undefined);
            window.getSelection()?.removeAllRanges();
          }}
          onCopy={() => {
            void copyToClipboard(selectionAction.text, "Selection");
            setSelectionAction(undefined);
          }}
          top={selectionAction.top}
        />
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
  approvalDisabled,
  approvalUncertain,
  approvals = [],
  message,
  onOpenThread,
  onPreviewImage,
  onPreviewTool,
  onRespondToApproval,
  onRespondToPlan,
  planUnavailableReason,
  showToolCalls,
  streaming,
  threadDisabled,
  threadSessionId,
  userAvatar,
  userName,
}: {
  agentAvatar: string;
  agentName: string;
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  approvals?: ApprovalRequest[];
  message: ChatMessage;
  onOpenThread?: () => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  onRespondToPlan?: (approval: ApprovalRequest, verdict: PlanVerdict) => void;
  planUnavailableReason?: (approval: ApprovalRequest) => string | undefined;
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
  const matching = approvals.filter((approval) => messageOwnsApproval(message, approval));
  const unmatched = approvals.filter((approval) => !matching.includes(approval));
  const hasVisibleTools = (showToolCalls || matching.length > 0) && Boolean(message.tools?.length);
  const hasExtras =
    Boolean(message.images?.length) ||
    Boolean(message.reasoning) ||
    Boolean(message.failure) ||
    Boolean(message.turnStat) ||
    hasVisibleStopReason(message.stopReason ?? "") ||
    approvals.length > 0;
  if (!user && !streaming && !message.content && !hasVisibleTools && !hasExtras) {
    return null;
  }
  const name = user ? userName : agentName;
  return (
    <article className={messageRowClass}>
      <MessageRow
        actions={
          <MessageActions
            onOpenThread={onOpenThread && message.content ? onOpenThread : undefined}
            threadDisabled={threadDisabled}
            threadOpen={Boolean(threadSessionId)}
          />
        }
        avatar={
          <MessageAvatar
            avatarUrl={user ? userAvatar : agentAvatar}
            fallback={user ? "user" : "agent"}
            name={name}
          />
        }
      >
        <p className={messageAuthorClass}>{name}</p>
        {message.reasoning && (
          <ReasoningDisclosure streaming={streaming} text={message.reasoning} />
        )}
        {message.images && message.images.length > 0 && (
          <MessageImages images={message.images} onPreviewImage={onPreviewImage} />
        )}
        {message.content ? (
          <div className={messageBodyClass}>
            {user ? (
              <p className="whitespace-pre-wrap">{message.content}</p>
            ) : (
              <MarkdownMessage>{message.content}</MarkdownMessage>
            )}
          </div>
        ) : message.tools?.length ? null : streaming ? (
          <StreamingIndicator />
        ) : null}
        {hasVisibleTools && message.tools && (
          <ToolCallList
            approvalDisabled={approvalDisabled}
            approvalUncertain={approvalUncertain}
            approvals={matching}
            onPreview={onPreviewTool}
            onRespondToApproval={onRespondToApproval}
            onRespondToPlan={onRespondToPlan}
            planUnavailableReason={planUnavailableReason}
            tools={message.tools}
          />
        )}
        {unmatched.map((approval) =>
          approval.tool === "PresentPlan" ? (
            <PlanReviewCard
              approval={approval}
              disabled={!approval.controlTarget || (approvalDisabled?.(approval) ?? false)}
              key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
              onRespond={(verdict) => onRespondToPlan?.(approval, verdict)}
              uncertain={approvalUncertain?.(approval)}
              unavailableReason={planUnavailableReason?.(approval)}
            />
          ) : (
            <ApprovalPanel
              approval={approval}
              disabled={!approval.controlTarget || (approvalDisabled?.(approval) ?? false)}
              key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
              onRespond={(verdict) => onRespondToApproval?.(approval, verdict)}
              uncertain={approvalUncertain?.(approval)}
            />
          ),
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
            className="mt-1 text-[11px] tabular-nums text-muted-foreground/70"
            title="This turn's tokens sent ↑ and received ↓, model time, and the share of input served from the prompt cache."
          >
            {message.turnStat}
          </p>
        )}
      </MessageRow>
    </article>
  );
}

/** Half the selection toolbar's width plus a margin, in CSS pixels. */
const SELECTION_TOOLBAR_HALF_WIDTH = 180;

interface SelectionAction {
  left: number;
  text: string;
  top: number;
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
