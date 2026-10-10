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
  MessageSquareText,
  Mic,
  MicOff,
  MoreHorizontal,
  Square,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
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
import { copyToClipboard } from "../../lib/clipboard";
import { errorMessage } from "../../lib/error-message";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
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
  type ChatMessage,
  initialRunDeliveryState,
  messageOwnsApproval,
  messagesFromTranscript,
  retractApproval,
  unmatchedApprovals,
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
import { SidePanelShell } from "./side-panel-shell";
import { registerThreadSession } from "./thread-map";
import { useVoiceInput } from "./use-voice-input";
import { VerdictLedger } from "./verdict-ledger";

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
  const [activeSessionId, setActiveSessionId] = useState(sessionId);
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
    await copyToClipboard(text, "Thread");
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
      <SidePanelShell
        actions={
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                aria-label="Thread options"
                className="size-8 text-muted-foreground"
                size="icon"
                variant="ghost"
              >
                <MoreHorizontal aria-hidden="true" className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuCheckboxItem checked={showTools} onCheckedChange={setShowTools}>
                Show Tools
              </DropdownMenuCheckboxItem>
              <DropdownMenuItem onSelect={() => void openAsFullChat()}>
                <Maximize2 aria-hidden="true" />
                Open as full chat
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => void copyThread()}>
                <Copy aria-hidden="true" />
                Copy thread
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        }
        bodyClassName="flex flex-col overflow-hidden"
        closeLabel="Close thread"
        escapeHint={!escapeAsk}
        icon={
          <MessageSquareText aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
        }
        maximizable
        onClose={onClose}
        rootRef={threadRoot}
        surface="thread"
        title="Thread"
      >
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="flex min-h-full flex-col px-3 pt-3 pb-6 lg:px-4">
            {run.messages.length === 0 ? (
              <p className="m-auto text-sm text-muted-foreground">Loading thread…</p>
            ) : (
              <div className="space-y-4">
                {run.messages.map((message, index) => (
                  <Message
                    agentAvatar={agentAvatar}
                    agentName={agentName}
                    approvalDisabled={run.approvalDisabled}
                    approvalUncertain={run.approvalUncertain}
                    approvals={run.approvals.filter((approval) =>
                      messageOwnsApproval(message, approval),
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
            {unmatchedApprovals(run.approvals, run.messages).map((approval) =>
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
          className="px-3 pb-3 lg:px-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <div className="relative rounded-2xl bg-zinc-50 dark:bg-zinc-900">
            <div className="relative rounded-2xl border bg-background transition-colors focus-within:border-zinc-400 dark:focus-within:border-zinc-600">
              <div className="px-4 pt-4 pb-2">
                <Textarea
                  aria-label="Reply in thread"
                  className="field-sizing-content max-h-40 min-h-[4.5rem] resize-none rounded-none border-0 bg-transparent px-0 py-0 text-sm shadow-none placeholder:opacity-60 focus-visible:ring-0 dark:bg-transparent"
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
              </div>
              <div className="flex items-center gap-1 px-2 pb-2">
                {voice.isSupported && (
                  <Button
                    aria-label={voice.isListening ? "Stop dictation" : "Start dictation"}
                    aria-pressed={voice.isListening}
                    className={
                      voice.isListening
                        ? "size-8 shrink-0 rounded-full bg-brand/10 text-brand hover:bg-brand/20"
                        : "size-8 shrink-0 rounded-full text-muted-foreground hover:bg-muted/60"
                    }
                    disabled={busy}
                    onClick={voice.toggle}
                    size="icon"
                    type="button"
                    variant="ghost"
                  >
                    {voice.isListening ? <MicOff aria-hidden="true" /> : <Mic aria-hidden="true" />}
                  </Button>
                )}
                <Button
                  aria-label={busy ? "Mecatl is working" : "Send reply"}
                  className="ml-auto size-8 shrink-0 rounded-full bg-brand text-brand-foreground hover:bg-brand/90"
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
            {/* Hidden without a deployment model inventory: see the note in
                chat-composer.tsx. */}
            {models.length > 0 ? (
              <div className="-mt-4 flex items-center gap-1 rounded-b-2xl border border-t-0 border-zinc-300 bg-zinc-50 px-2 pt-5 pb-1.5 dark:border-zinc-700 dark:bg-zinc-900">
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
      </SidePanelShell>
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
  // A side thread can move to a successor session without remounting, so a
  // verdict that lands late must not write into the session now on screen.
  const viewedSessionId = useRef(sessionId);
  viewedSessionId.current = sessionId;
  const verdicts = useRef(new VerdictLedger());
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
    verdicts.current.clear();
    setControlPending(false);
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
              verdicts.current.phase(approval.controlTarget) !== "acknowledged",
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
            verdicts.current.reset({ askId, runId: delivery.event.runId, sessionId });
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
        approval.controlTarget && verdicts.current.phase(approval.controlTarget) === "uncertain",
    );
  }

  function approvalUncertain(approval: ApprovalRequest): boolean {
    void verdictEpoch;
    return Boolean(
      approval.controlTarget && verdicts.current.phase(approval.controlTarget) === "uncertain",
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
        verdicts.current.blocks(target),
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
    if (!verdicts.current.begin(target)) return;
    setControlPending(true);
    try {
      await resolvePlanAsk({
        body: { verdict },
        path: { askId: target.askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      verdicts.current.acknowledge(target);
      if (viewedSessionId.current !== target.sessionId) return;
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
        if (!controller.signal.aborted && viewedSessionId.current === target.sessionId) {
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
      verdicts.current.markUncertain(target);
      setVerdictEpoch((value) => value + 1);
      if (viewedSessionId.current === target.sessionId)
        setError(`Could not confirm this verdict: ${errorMessage(caught)}`);
    } finally {
      planFollowAbort.current = undefined;
      if (viewedSessionId.current === target.sessionId) setControlPending(false);
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
    if (!verdicts.current.begin(target)) return;
    setControlPending(true);
    try {
      await resolveRunPermission({
        body: { verdict },
        path: { askId: target.askId, runId: target.runId, sessionId: target.sessionId },
        throwOnError: true,
      });
      verdicts.current.acknowledge(target);
      if (viewedSessionId.current !== target.sessionId) return;
      setApprovals((current) => retractApproval(current, target.askId, target.runId));
      setNotice(`Permission ${verdict.replaceAll("_", " ")} recorded.`);
    } catch (caught) {
      verdicts.current.markUncertain(target);
      setVerdictEpoch((value) => value + 1);
      if (viewedSessionId.current === target.sessionId)
        setError(`Could not confirm this verdict: ${errorMessage(caught)}`);
    } finally {
      if (viewedSessionId.current === target.sessionId) setControlPending(false);
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
