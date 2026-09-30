// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import {
  cancelRun,
  resolvePlanAsk,
  resolveRunPermission,
  startRun,
  watchSessionActivity,
} from "@mecatl-studio/contracts/generated";
import {
  forkSessionMutation,
  getRuntimeOptions,
  getRuntimeSettingsOptions,
  getSessionDetailOptions,
  getSessionTranscriptOptions,
  listSessionsOptions,
  listSessionsQueryKey,
} from "@mecatl-studio/contracts/query";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
  AlertCircle,
  ArrowUp,
  Copy,
  LoaderCircle,
  Maximize2,
  Mic,
  MicOff,
  MoreHorizontal,
  PanelRightClose,
  Square,
} from "lucide-react";
import {
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  useEffect,
  useRef,
  useState,
} from "react";
import { Button } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { Textarea } from "../../components/ui/textarea";
import { captureSseFailure, protectedRequestsPaused } from "../../lib/api-client";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import { maxPanelWidth, minPanelWidth, usePanelWidth } from "../../lib/panel-width";
import {
  defaultAgentName,
  useAgentAvatar,
  useAgentDisplayName,
  useUserAvatar,
  useUserDisplayName,
} from "../../lib/profile-preferences";
import { useAuthRecovery } from "../auth/auth-recovery-context";
import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import type { ComposerModelOption } from "./chat-composer";
import { useChatEscape } from "./chat-escape";
import {
  applyRunDelivery,
  approvalKey,
  approvalMatchesToolCall,
  type ChatMessage,
  errorMessage,
  initialRunDeliveryState,
  messagesFromTranscript,
  retractApproval,
} from "./chat-state";
import { Message } from "./chat-workspace";
import { EscapeHintContext } from "./escape-hint-context";
import { groupModels, ModelEffortMenu } from "./model-effort-menu";
import { exactPlanControlAvailability, followPlanContinuationFromBff } from "./plan-continuation";
import { PlanReviewCard, type PlanVerdict } from "./plan-review-card";
import {
  CATCHING_UP_NOTICE,
  decideTruncation,
  isActiveSessionState,
  type RunStreamEnd,
  runStreamEnd,
} from "./run-stream";
import { registerThreadSession } from "./thread-map";
import { useVoiceInput } from "./use-voice-input";

/** Collects an SSE transport failure for any of a thread run's streams, including reattached ones. */
interface StreamFailure {
  error?: unknown;
}

/**
 * The side panel a "Reply in thread" click opens next to the still-open
 * parent chat. Deliberately independent of `ChatWorkspace`'s state: its own
 * transcript query, its own SSE-driven run, its own composer — so a thread
 * reply can never collide with (or get cancelled by) whatever the parent
 * chat is doing. The only thing it shares with the parent is the pure
 * `chat-state` transforms and the `Message` presentation component.
 */
export function SideThreadPanel({
  messageKey,
  onClose,
  parentSessionId,
  sessionId,
}: {
  messageKey: string;
  onClose: () => void;
  parentSessionId: string;
  sessionId: string;
}) {
  const recovery = useAuthRecovery();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const width = usePanelWidth("contentPreview");
  const [activeSessionId, setActiveSessionId] = useState(sessionId);
  const [maximized, setMaximized] = useState(false);
  const [showTools, setShowTools] = useState(true);
  const [prompt, setPrompt] = useState("");
  const [escapeClearHint, setEscapeClearHint] = useState(false);
  const threadRoot = useRef<HTMLElement>(null);
  const forkSession = useMutation(forkSessionMutation());
  const sessionDetail = useQuery(getSessionDetailOptions({ path: { sessionId: activeSessionId } }));
  const runtimeSettings = useQuery(getRuntimeSettingsOptions());
  const disabledModels = useDisabledModels().disabled;
  const agentName = useAgentDisplayName().value.trim() || defaultAgentName;
  const agentAvatar = useAgentAvatar().value;
  const userName = useUserDisplayName().value.trim() || "You";
  const userAvatar = useUserAvatar().value;
  const run = useSideThreadRun(activeSessionId);
  const voice = useVoiceInput(prompt, setPrompt);
  const textarea = useRef<HTMLTextAreaElement>(null);

  const model = sessionDetail.data?.model;
  const models: ComposerModelOption[] =
    runtimeSettings.data?.models
      .filter((candidate) => !disabledModels.has(modelPreferenceId(candidate)))
      .map((candidate) => ({
        id: candidate.id,
        image: candidate.image,
        label: candidate.displayName,
        providerId: candidate.providerId,
      })) ?? [];
  const busy = run.isRunning || forkSession.isPending;
  const escapeAsk =
    run.approvals.find(
      (approval) =>
        approval.controlTarget?.sessionId === activeSessionId &&
        approval.controlTarget.runId === run.runId,
    ) ?? run.approvals.find((approval) => approval.controlTarget?.sessionId === activeSessionId);
  useChatEscape({
    active: true,
    askAvailable: escapeAsk ? !run.approvalDisabled(escapeAsk) : false,
    draft: Boolean(prompt),
    onClearDraft: () => setPrompt(""),
    onClosePanel: onClose,
    onDenyAsk: () => {
      if (escapeAsk) void run.respondToApproval(escapeAsk, "deny");
    },
    onHintChange: setEscapeClearHint,
    onIteratePlan: () => {
      if (escapeAsk) void run.respondToPlan(escapeAsk, "iterate");
    },
    onStopRun: () => void run.stopRun(),
    onUnavailablePlan: () =>
      run.setError(
        escapeAsk
          ? (run.planUnavailableReason(escapeAsk) ??
              "Exact plan verdict is unavailable. Refresh activity.")
          : "Exact plan verdict is unavailable.",
      ),
    navigationKey: activeSessionId,
    ownsFocus: (target) => target instanceof Node && Boolean(threadRoot.current?.contains(target)),
    panelOpen: false,
    pendingAsk: escapeAsk?.tool === "PresentPlan" ? "plan" : escapeAsk ? "ordinary" : "none",
    planAvailable: escapeAsk?.tool === "PresentPlan" && !run.planUnavailableReason(escapeAsk),
    runActive: run.isRunning && Boolean(run.runId),
  });

  function startResize(event: ReactPointerEvent<HTMLButtonElement>) {
    event.currentTarget.focus();
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = width.value;
    const resize = (moveEvent: PointerEvent) =>
      width.setValue(startWidth - moveEvent.clientX + startX);
    const finish = () => {
      window.removeEventListener("pointermove", resize);
      window.removeEventListener("pointerup", finish);
    };
    window.addEventListener("pointermove", resize);
    window.addEventListener("pointerup", finish);
  }

  async function forkToModel(
    nextModel: { id: string; providerId: string },
    reasoningEffort: string,
  ) {
    try {
      const successor = await forkSession.mutateAsync({
        body: { model: nextModel, reasoningEffort: reasoningEffort as never },
        path: { sessionId: activeSessionId },
      });
      registerThreadSession(parentSessionId, messageKey, successor.id);
      setActiveSessionId(successor.id);
      await queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() });
    } catch (caught) {
      run.setError(errorMessage(caught));
    }
  }

  async function copyThread() {
    const text = run.messages
      .filter((message) => message.content)
      .map((message) => `${message.role === "user" ? userName : agentName}: ${message.content}`)
      .join("\n\n");
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // Clipboard access is an optional convenience.
    }
  }

  async function openAsFullChat() {
    await navigate({ search: { sessionId: activeSessionId }, to: "/workspace/chat" });
    onClose();
  }

  async function submit() {
    const next = prompt.trim();
    if (!next || busy || recovery.phase !== "ready") return;
    voice.stop();
    // Keep the draft until the stream proves the write was accepted.
    const accepted = await new Promise<boolean>((resolve) => {
      void run.sendPrompt(next, resolve);
    });
    if (accepted) setPrompt("");
  }

  return (
    <EscapeHintContext.Provider value={escapeAsk}>
      <button
        aria-label="Close thread"
        className="absolute inset-0 z-30 bg-black/35 min-[760px]:hidden"
        onClick={onClose}
        type="button"
      />
      <aside
        aria-label="Thread"
        data-chat-surface="thread"
        className={
          maximized
            ? "fixed inset-0 z-50 flex flex-col bg-background"
            : "absolute inset-x-0 bottom-0 z-40 flex h-[94dvh] flex-col rounded-t-2xl border bg-background shadow-2xl min-[760px]:relative min-[760px]:inset-auto min-[760px]:order-3 min-[760px]:h-full min-[760px]:w-[var(--content-panel-width)] min-[760px]:shrink-0 min-[760px]:rounded-none min-[760px]:border-y-0 min-[760px]:border-r-0"
        }
        style={
          maximized ? undefined : ({ "--content-panel-width": `${width.value}px` } as CSSProperties)
        }
        ref={threadRoot}
      >
        {!maximized && (
          <button
            aria-label="Resize thread panel"
            className="absolute inset-y-0 -left-1 z-10 hidden w-2 cursor-col-resize touch-none border-0 bg-transparent p-0 hover:bg-brand/20 min-[760px]:block"
            onKeyDown={(event) => {
              if (event.key === "ArrowLeft") width.setValue(width.value + 12);
              else if (event.key === "ArrowRight") width.setValue(width.value - 12);
              else return;
              event.preventDefault();
            }}
            onPointerDown={startResize}
            title={`Resize thread panel (${minPanelWidth}–${maxPanelWidth}px)`}
            type="button"
          />
        )}
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
          <h2 className="min-w-0 flex-1 truncate text-sm font-semibold">Thread</h2>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button aria-label="Thread options" size="icon" variant="ghost">
                <MoreHorizontal aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuCheckboxItem checked={showTools} onCheckedChange={setShowTools}>
                Show Tools
              </DropdownMenuCheckboxItem>
              <DropdownMenuItem onSelect={() => void openAsFullChat()}>
                Open as full chat
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => void copyThread()}>
                <Copy aria-hidden="true" />
                Copy thread
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <Button
            aria-label={maximized ? "Restore thread panel" : "Maximize thread panel"}
            onClick={() => setMaximized((current) => !current)}
            size="icon"
            variant="ghost"
          >
            <Maximize2 aria-hidden="true" className={maximized ? "rotate-180" : undefined} />
          </Button>
          <Button aria-label="Close thread" onClick={onClose} size="icon" variant="ghost">
            <PanelRightClose aria-hidden="true" />
          </Button>
          {!escapeAsk && <span className="text-xs text-muted-foreground">Esc to Close</span>}
        </header>

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="flex min-h-full flex-col px-4 py-6">
            {run.messages.length === 0 ? (
              <p className="m-auto text-sm text-muted-foreground">Loading thread…</p>
            ) : (
              <div className="space-y-6">
                {run.messages.map((message, index) => (
                  <Message
                    agentAvatar={agentAvatar}
                    agentName={agentName}
                    approvalDisabled={run.approvalDisabled}
                    approvalUncertain={run.approvalUncertain}
                    approvals={run.approvals.filter((approval) =>
                      message.tools?.some((tool) => approvalMatchesToolCall(approval, tool)),
                    )}
                    key={message.id}
                    message={message}
                    onRespondToApproval={run.respondToApproval}
                    onRespondToPlan={run.respondToPlan}
                    planUnavailableReason={run.planUnavailableReason}
                    showToolCalls={showTools}
                    streaming={run.isRunning && index === run.messages.length - 1}
                    threadDisabled
                    userAvatar={userAvatar}
                    userName={userName}
                  />
                ))}
              </div>
            )}
            {run.approvals
              .filter(
                (approval) =>
                  !run.messages.some((message) =>
                    message.tools?.some((tool) => approvalMatchesToolCall(approval, tool)),
                  ),
              )
              .map((approval) =>
                approval.tool === "PresentPlan" ? (
                  <PlanReviewCard
                    approval={approval}
                    disabled={run.approvalDisabled(approval)}
                    key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
                    onRespond={(verdict) => void run.respondToPlan(approval, verdict)}
                    uncertain={run.approvalUncertain(approval)}
                    unavailableReason={run.planUnavailableReason(approval)}
                  />
                ) : (
                  <ApprovalPanel
                    approval={approval}
                    disabled={run.approvalDisabled(approval)}
                    key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
                    onRespond={(verdict) => void run.respondToApproval(approval, verdict)}
                    uncertain={run.approvalUncertain(approval)}
                  />
                ),
              )}
          </div>
        </div>

        {run.error && (
          <div className="mx-4 mb-3 flex items-start gap-2 rounded-lg bg-destructive/10 px-3 py-2 text-sm text-foreground">
            <AlertCircle aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            {run.error}
          </div>
        )}

        {run.notice && (
          <div className="mx-4 mb-3 rounded-lg bg-success/10 px-3 py-2 text-sm text-foreground">
            {run.notice}
          </div>
        )}

        {run.isRunning && run.runId && (
          <div className="mx-4 mb-2 flex items-center justify-end">
            <Button
              disabled={run.controlPending}
              onClick={() => void run.stopRun()}
              size="sm"
              variant="outline"
            >
              <Square aria-hidden="true" className="fill-current" />
              Stop
            </Button>
            {!escapeAsk && !run.controlPending && (
              <span className="ml-2 text-xs text-muted-foreground">Esc to Stop</span>
            )}
          </div>
        )}

        <form
          className="px-4 pb-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <div className="rounded-2xl border bg-card p-2 shadow-[0_8px_30px_rgb(0_0_0/0.06)] focus-within:ring-2 focus-within:ring-ring/40">
            <Textarea
              aria-label="Reply in thread"
              className="max-h-40 min-h-14 resize-none border-0 bg-transparent px-2 py-2 shadow-none focus-visible:ring-0 dark:bg-transparent"
              disabled={busy}
              onChange={(event) => setPrompt(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && !event.shiftKey) {
                  event.preventDefault();
                  void submit();
                }
              }}
              placeholder={busy ? "Mecatl is working…" : "Reply in thread…"}
              ref={textarea}
              value={prompt}
            />
            <div className="flex items-center justify-between gap-2 px-1 pb-1 pt-2">
              <div className="flex items-center gap-1.5">
                {voice.isSupported && (
                  <Button
                    aria-label={voice.isListening ? "Stop dictation" : "Start dictation"}
                    aria-pressed={voice.isListening}
                    className="size-8 shrink-0 rounded-full"
                    disabled={busy}
                    onClick={voice.toggle}
                    size="icon"
                    type="button"
                    variant={voice.isListening ? "secondary" : "ghost"}
                  >
                    {voice.isListening ? <MicOff aria-hidden="true" /> : <Mic aria-hidden="true" />}
                  </Button>
                )}
              </div>
              <Button
                aria-label={busy ? "Mecatl is working" : "Send reply"}
                className="size-8 shrink-0 rounded-full"
                disabled={busy || recovery.phase !== "ready" || !prompt.trim()}
                size="icon"
                type="submit"
              >
                {busy ? (
                  <LoaderCircle aria-hidden="true" className="animate-spin" />
                ) : (
                  <ArrowUp aria-hidden="true" />
                )}
              </Button>
            </div>
          </div>
        </form>
        {!escapeAsk && !run.isRunning && prompt && !escapeClearHint && (
          <p className="px-4 pb-2 text-xs text-muted-foreground">Esc twice to clear draft</p>
        )}
        {escapeClearHint && (
          <p className="px-4 pb-2 text-xs text-muted-foreground" role="status">
            Press Escape again to clear the unsent draft.
          </p>
        )}
        {/* Hidden without a deployment model inventory: see the note in
            chat-composer.tsx. */}
        {models.length > 0 ? (
          <div className="px-4 pb-4">
            <ModelEffortMenu
              disabled={busy || !sessionDetail.data?.capabilities.modelSelection}
              effort={model?.reasoningEffort ?? "default"}
              groupedModels={groupModels(models)}
              model={model}
              onEffortChange={(effort) => model && void forkToModel(model, effort)}
              onModelChange={(nextModel) =>
                nextModel && void forkToModel(nextModel, model?.reasoningEffort ?? "default")
              }
            />
          </div>
        ) : null}
      </aside>
    </EscapeHintContext.Provider>
  );
}

/**
 * Drives one thread's SSE run independently of every other session on the
 * page: its own message list, its own in-flight controller, its own
 * reattach-on-mount watch. Mirrors the shape of `ChatWorkspace`'s run
 * handling but never touches its state — a second concurrent run here can
 * never race or cancel the parent chat's.
 */
function useSideThreadRun(sessionId: string) {
  const queryClient = useQueryClient();
  const runtime = useQuery(getRuntimeOptions());
  const sessions = useQuery(listSessionsOptions());
  const transcript = useQuery({
    ...getSessionTranscriptOptions({ path: { sessionId } }),
    enabled: Boolean(sessionId),
  });
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [isRunning, setIsRunning] = useState(false);
  const [runId, setRunId] = useState<string>();
  const [approvals, setApprovals] = useState<ApprovalRequest[]>([]);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [controlPending, setControlPending] = useState(false);
  const runAbort = useRef<AbortController | undefined>(undefined);
  const planFollowAbort = useRef<AbortController | undefined>(undefined);
  const planReattach = useRef<{ cursor?: string; runId: string; sessionId: string } | undefined>(
    undefined,
  );
  const [reattachEpoch, setReattachEpoch] = useState(0);
  const [planContinuationActive, setPlanContinuationActive] = useState(false);
  const runIdRef = useRef<string | undefined>(undefined);
  const verdictInFlight = useRef(new Set<string>());
  const uncertainVerdicts = useRef(new Set<string>());
  const acknowledgedVerdicts = useRef(new Set<string>());
  const [verdictEpoch, setVerdictEpoch] = useState(0);

  useEffect(() => () => planFollowAbort.current?.abort(), []);

  useEffect(() => {
    if (!isRunning && transcript.data)
      setMessages(messagesFromTranscript(transcript.data.messages));
  }, [isRunning, transcript.data]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: intentionally resets on session identity alone, not on any value read inside
  useEffect(() => {
    runAbort.current?.abort();
    planFollowAbort.current?.abort();
    planReattach.current = undefined;
    setPlanContinuationActive(false);
    runAbort.current = undefined;
    setError(undefined);
    setNotice(undefined);
    setApprovals([]);
    setMessages([]);
    setIsRunning(false);
    setRunId(undefined);
    runIdRef.current = undefined;
    verdictInFlight.current.clear();
    uncertainVerdicts.current.clear();
    acknowledgedVerdicts.current.clear();
  }, [sessionId]);

  const selected = sessions.data?.items.find((session) => session.id === sessionId);
  const watchable = isActiveSessionState(selected?.state);
  const shouldWatch = watchable || planContinuationActive;

  // A proven plan continuation can reattach before the inventory catches up.
  // biome-ignore lint/correctness/useExhaustiveDependencies: consume/refresh are stable for this lifetime
  useEffect(() => {
    const planRefresh = planReattach.current;
    if (
      !sessionId ||
      (!shouldWatch && planRefresh?.sessionId !== sessionId) ||
      runAbort.current ||
      protectedRequestsPaused()
    )
      return;
    planReattach.current = undefined;
    const controller = new AbortController();
    runAbort.current = controller;
    setIsRunning(true);
    setError(undefined);
    setNotice(undefined);
    setApprovals((current) => retainUncertainApprovals(current));
    if (!planRefresh?.cursor) setMessages([]);
    if (planRefresh?.sessionId === sessionId) {
      runIdRef.current = planRefresh.runId;
      setRunId(planRefresh.runId);
    }

    void (async () => {
      const streamFailure: StreamFailure = {};
      try {
        const stream = await watchActivity(controller, streamFailure, planRefresh?.cursor);
        await consume(
          stream,
          controller,
          streamFailure,
          crypto.randomUUID(),
          "",
          !planRefresh?.cursor,
          planRefresh?.cursor ? messages : [],
        );
        if (streamFailure.error && !controller.signal.aborted) throw streamFailure.error;
      } catch (caught) {
        if (!controller.signal.aborted) setError(errorMessage(caught));
      } finally {
        if (runAbort.current === controller) {
          runAbort.current = undefined;
          if (planRefresh?.sessionId === sessionId) setPlanContinuationActive(false);
          setRunId(undefined);
          runIdRef.current = undefined;
          setApprovals((current) => retainUncertainApprovals(current));
          setIsRunning(false);
          if (!controller.signal.aborted) await refresh();
        }
      }
    })();

    return () => {
      controller.abort();
      if (runAbort.current === controller) {
        runAbort.current = undefined;
        setIsRunning(false);
      }
    };
  }, [reattachEpoch, sessionId, shouldWatch]);

  async function refresh() {
    await Promise.all([
      queryClient.invalidateQueries({
        queryKey: getSessionTranscriptOptions({ path: { sessionId } }).queryKey,
      }),
      queryClient.invalidateQueries({
        queryKey: getSessionDetailOptions({ path: { sessionId } }).queryKey,
      }),
      queryClient.invalidateQueries({ queryKey: listSessionsQueryKey() }),
    ]);
  }

  /** Opens (or, with `resumeFrom`, reopens) this thread's activity stream. */
  async function watchActivity(
    controller: AbortController,
    streamFailure: StreamFailure,
    resumeFrom?: string,
  ) {
    const response = await watchSessionActivity({
      onSseError: captureSseFailure(streamFailure),
      path: { sessionId },
      query: resumeFrom ? { resumeFrom } : undefined,
      signal: controller.signal,
      sseMaxRetryAttempts: 1,
    });
    return response.stream;
  }

  /**
   * Folds one run's deliveries into the thread. A bounded replay
   * (`run.truncated` "bound") is followed by reattaching from its cursor, the
   * same way the main chat follows it; the delivery state carries across the
   * reattach, so a resumed stream keeps appending into the current assistant
   * message. A truncation is a stream boundary, never a run outcome.
   */
  async function consume(
    firstStream: AsyncIterable<RunStreamEvent>,
    controller: AbortController,
    streamFailure: StreamFailure,
    assistantId: string,
    prompt: string,
    replay: boolean,
    seedMessages: ChatMessage[] = [],
    onAccepted?: () => void,
  ): Promise<RunStreamEnd> {
    let state = initialRunDeliveryState(assistantId, prompt, replay ? [] : seedMessages);
    let reattaches = 0;
    let unfollowed = false;
    let stream: AsyncIterable<RunStreamEvent> | undefined = firstStream;
    while (stream) {
      let resumeFrom: string | undefined;
      for await (const delivery of stream) {
        onAccepted?.();
        onAccepted = undefined;
        if (delivery.type === "run.truncated") {
          const decision = decideTruncation(delivery, reattaches);
          setNotice(decision.notice);
          if (decision.action === "reattach") resumeFrom = decision.cursor;
          else unfollowed = true;
          break;
        }
        state = applyRunDelivery(state, delivery, {
          newId: () => crypto.randomUUID(),
          now: Date.now(),
          replay,
          sessionId,
        });
        setMessages(state.messages);
        setApprovals(
          state.approvals.filter(
            (approval) =>
              !approval.controlTarget ||
              !acknowledgedVerdicts.current.has(approvalKey(approval.controlTarget)),
          ),
        );
        if (
          delivery.type === "run.event" &&
          (delivery.event.kind === "approval" || delivery.event.kind === "permission.retract")
        ) {
          const askId =
            typeof delivery.event.payload === "object" &&
            delivery.event.payload !== null &&
            "askId" in delivery.event.payload
              ? delivery.event.payload.askId
              : undefined;
          if (typeof askId === "string") {
            const key = approvalKey({ askId, runId: delivery.event.runId, sessionId });
            uncertainVerdicts.current.delete(key);
            verdictInFlight.current.delete(key);
            acknowledgedVerdicts.current.delete(key);
            setVerdictEpoch((value) => value + 1);
          }
        }
        if (state.runId) {
          runIdRef.current = state.runId;
          setRunId(state.runId);
        }
      }

      stream = undefined;
      if (resumeFrom && !streamFailure.error && !controller.signal.aborted) {
        reattaches += 1;
        stream = await watchActivity(controller, streamFailure, resumeFrom);
      }
    }

    // Once the replay has caught up and the stream ended on its own, the
    // catch-up notice no longer describes anything.
    if (!unfollowed) {
      setNotice((current) => (current === CATCHING_UP_NOTICE ? undefined : current));
    }
    return runStreamEnd(state, unfollowed);
  }

  async function sendPrompt(prompt: string, onAccepted?: (accepted: boolean) => void) {
    if (!sessionId || isRunning || protectedRequestsPaused()) {
      onAccepted?.(false);
      return;
    }
    let accepted = false;
    setError(undefined);
    setNotice(undefined);
    setIsRunning(true);
    const assistantId = crypto.randomUUID();
    const controller = new AbortController();
    runAbort.current = controller;
    const seeded: ChatMessage[] = [
      ...messages,
      { content: prompt, id: crypto.randomUUID(), role: "user" },
      { content: "", id: assistantId, role: "assistant" },
    ];
    setMessages(seeded);

    try {
      const streamFailure: StreamFailure = {};
      const response = await startRun({
        body: { prompt },
        onSseError: captureSseFailure(streamFailure),
        path: { sessionId },
        signal: controller.signal,
        sseMaxRetryAttempts: 1,
      });
      const end = await consume(
        response.stream,
        controller,
        streamFailure,
        assistantId,
        prompt,
        false,
        seeded,
        () => {
          accepted = true;
          onAccepted?.(true);
        },
      );
      if (streamFailure.error && !controller.signal.aborted) throw streamFailure.error;
      // An unfollowed stream says nothing about the run, so it reports no failure.
      if (end.kind === "settled" && end.failure) setError(end.failure.message);
    } catch (caught) {
      if (!controller.signal.aborted) setError(errorMessage(caught));
    } finally {
      if (!accepted) onAccepted?.(false);
      if (runAbort.current === controller) {
        runAbort.current = undefined;
        setRunId(undefined);
        runIdRef.current = undefined;
        setApprovals((current) => retainUncertainApprovals(current));
        setIsRunning(false);
        if (!controller.signal.aborted) await refresh();
      }
    }
  }

  async function stopRun() {
    if (!sessionId || !runId) return;
    setControlPending(true);
    try {
      await cancelRun({ path: { runId, sessionId }, throwOnError: true });
    } catch (caught) {
      setError(errorMessage(caught));
    } finally {
      setControlPending(false);
    }
  }

  function retainUncertainApprovals(current: ApprovalRequest[]): ApprovalRequest[] {
    return current.filter(
      (approval) =>
        approval.controlTarget &&
        uncertainVerdicts.current.has(approvalKey(approval.controlTarget)),
    );
  }

  function approvalUncertain(approval: ApprovalRequest): boolean {
    void verdictEpoch;
    return Boolean(
      approval.controlTarget && uncertainVerdicts.current.has(approvalKey(approval.controlTarget)),
    );
  }

  function approvalDisabled(approval: ApprovalRequest): boolean {
    const target = approval.controlTarget;
    return Boolean(
      !target ||
        target.askId !== approval.askId ||
        target.sessionId !== sessionId ||
        target.runId !== runIdRef.current ||
        controlPending ||
        verdictInFlight.current.has(approvalKey(target)) ||
        uncertainVerdicts.current.has(approvalKey(target)),
    );
  }

  function planUnavailableReason(approval: ApprovalRequest): string | undefined {
    const featureReason = exactPlanControlAvailability(runtime.data?.features).reason;
    if (featureReason) return featureReason;
    const target = approval.controlTarget;
    return !target ||
      target.askId !== approval.askId ||
      target.sessionId !== sessionId ||
      target.runId !== runIdRef.current
      ? "This plan ask is stale or its exact run is unavailable. Refresh activity."
      : undefined;
  }

  async function respondToPlan(approval: ApprovalRequest, verdict: PlanVerdict) {
    if (
      approval.tool !== "PresentPlan" ||
      planUnavailableReason(approval) ||
      approvalDisabled(approval)
    )
      return;
    const target = approval.controlTarget;
    if (!target) return;
    const key = approvalKey(target);
    verdictInFlight.current.add(key);
    setControlPending(true);
    try {
      await resolvePlanAsk({
        body: { verdict },
        path: { askId: target.askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      acknowledgedVerdicts.current.add(key);
      setApprovals((current) => retractApproval(current, target.askId, target.runId));
      if (verdict === "iterate") {
        setNotice("Plan iteration requested.");
      } else {
        setNotice("Plan approval accepted; checking whether execution started.");
        const controller = new AbortController();
        planFollowAbort.current = controller;
        const evidence = await followPlanContinuationFromBff(
          { askId: target.askId, planRunId: target.runId, sessionId: target.sessionId },
          controller.signal,
        );
        if (!controller.signal.aborted) {
          if (evidence.kind === "started") {
            planReattach.current = {
              cursor: evidence.resumeFrom,
              runId: evidence.runId,
              sessionId: target.sessionId,
            };
            setPlanContinuationActive(true);
            runAbort.current?.abort();
            runAbort.current = undefined;
            runIdRef.current = evidence.runId;
            setRunId(evidence.runId);
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
        }
      }
    } catch (caught) {
      uncertainVerdicts.current.add(key);
      setVerdictEpoch((value) => value + 1);
      setError(`Could not confirm this verdict: ${errorMessage(caught)}`);
    } finally {
      planFollowAbort.current = undefined;
      verdictInFlight.current.delete(key);
      setControlPending(false);
    }
  }

  async function respondToApproval(approval: ApprovalRequest, verdict: ApprovalVerdict) {
    if (
      approval.tool === "PresentPlan" ||
      (verdict !== "deny" && !approval.args.trim()) ||
      approvalDisabled(approval)
    )
      return;
    const target = approval.controlTarget;
    if (!target) return;
    const key = approvalKey(target);
    verdictInFlight.current.add(key);
    setControlPending(true);
    try {
      await resolveRunPermission({
        body: { verdict },
        path: { askId: target.askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      acknowledgedVerdicts.current.add(key);
      setApprovals((current) => retractApproval(current, target.askId, target.runId));
      setNotice(`Permission ${verdict.replaceAll("_", " ")} recorded.`);
    } catch (caught) {
      uncertainVerdicts.current.add(key);
      setVerdictEpoch((value) => value + 1);
      setError(`Could not confirm this verdict: ${errorMessage(caught)}`);
    } finally {
      verdictInFlight.current.delete(key);
      setControlPending(false);
    }
  }

  return {
    approvals,
    approvalDisabled,
    approvalUncertain,
    controlPending,
    error,
    isRunning,
    messages,
    notice,
    respondToApproval,
    respondToPlan,
    planUnavailableReason,
    runId,
    sendPrompt,
    setError,
    stopRun,
  };
}
