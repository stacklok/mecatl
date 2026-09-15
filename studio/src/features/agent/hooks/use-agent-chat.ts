"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  loadSentAttachments,
  saveSentAttachments,
} from "@/lib/attachment-store";
import { fileFromToolCall } from "@/lib/file-meta";
import {
  cancelHarnessRun,
  cancelHarnessSteer,
  createHarnessSession,
  fetchSessionTranscriptMessages,
  HarnessApiError,
  type PromptPart,
  respondToHarnessApproval,
  retryHarnessRun,
  steerHarnessRun,
  streamHarnessPrompt,
} from "@/lib/harness/client";
import { watchSessionEvents } from "@/lib/harness/watch";
import {
  encodeSessionPermissionMode,
  type SessionPermissionMode,
  type SessionTranscript,
} from "@/lib/protocol";
import { refreshSlashCommands } from "../composer-capabilities";
import { useRuntimeStatus } from "../runtime-status";
import type {
  AgentMessage,
  ApprovalChoice,
  ApprovalRequest,
  Attachment,
  ClarificationRequest,
  DelegationInfo,
  RetryDisposition,
  SteerEchoPart,
  StreamEvent,
  ToolCallInfo,
} from "../types";

type ChatStatus =
  | "idle"
  | "streaming"
  | "waiting_approval"
  | "waiting_clarification"
  | "error";

/** A message held while a run is active, drained as a prompt when idle.
 *  `files` are staged attachments the text must not lose on the way. */
export interface QueuedMessage {
  id: string;
  text: string;
  files?: File[];
}

/** A steer the daemon accepted but has not yet drained into the run. */
export interface PendingSteer {
  id: string;
  text: string;
  /** Staged attachments the steer carried (requeued with the text if the
   *  run ends before the drain). */
  files?: File[];
}

/**
 * Splits the ordered pending-steer list on the drain echo's watermark: every
 * steer up to AND including the matching id was merged into the drained
 * bundle and drops. An empty or unmatched id clears the whole list — the
 * daemon's correlation FIFO is authoritative, so an echo we cannot correlate
 * means the local list is stale. Never text-match.
 */
export function splitPendingSteersOnWatermark(
  pending: readonly PendingSteer[],
  messageId: string,
): PendingSteer[] {
  if (!messageId) return [];
  const index = pending.findIndex((steer) => steer.id === messageId);
  if (index === -1) return [];
  return pending.slice(index + 1);
}

/** Formats every vision provider accepts; anything else gets re-encoded. */
const WIRE_IMAGE_TYPES = new Set([
  "image/png",
  "image/jpeg",
  "image/webp",
  "image/gif",
]);
/** Above this, re-encode: phone photos are 4–12 MB and base64 inflates by
 *  a third — big payloads 413 at the daemon's byte budget. */
const MAX_INLINE_BYTES = 1_500_000;
/** Longest edge after a re-encode; ample for vision models. */
const MAX_IMAGE_EDGE = 1600;

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return btoa(binary);
}

/**
 * Encode one picked image as a daemon prompt part. A small image in a
 * provider-friendly format goes as-is; everything else — big photos, HEIC
 * from iPhones — is downscaled onto a canvas and re-encoded as JPEG (the
 * browser decodes whatever the platform can, so Safari transcodes HEIC
 * here; a browser that cannot decode the format fails loudly instead).
 */
async function imageToPart(file: File): Promise<PromptPart> {
  if (WIRE_IMAGE_TYPES.has(file.type) && file.size <= MAX_INLINE_BYTES) {
    return {
      kind: "image",
      mime_type: file.type,
      data: bytesToBase64(new Uint8Array(await file.arrayBuffer())),
    };
  }
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise<HTMLImageElement>((resolve, reject) => {
      const el = new Image();
      el.onload = () => resolve(el);
      el.onerror = () =>
        reject(
          new Error(
            `${file.name}: this browser cannot decode ${file.type || "that format"} — attach a JPEG or PNG instead.`,
          ),
        );
      el.src = url;
    });
    const scale = Math.min(
      1,
      MAX_IMAGE_EDGE / Math.max(img.naturalWidth, img.naturalHeight),
    );
    const width = Math.max(1, Math.round(img.naturalWidth * scale));
    const height = Math.max(1, Math.round(img.naturalHeight * scale));
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("canvas unavailable");
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, width, height);
    ctx.drawImage(img, 0, 0, width, height);
    const blob = await new Promise<Blob | null>((resolve) =>
      canvas.toBlob(resolve, "image/jpeg", 0.85),
    );
    if (!blob) throw new Error(`${file.name}: could not encode the image.`);
    return {
      kind: "image",
      mime_type: "image/jpeg",
      data: bytesToBase64(new Uint8Array(await blob.arrayBuffer())),
    };
  } finally {
    URL.revokeObjectURL(url);
  }
}

function messagesFromTranscript(transcript: SessionTranscript): AgentMessage[] {
  const messages: AgentMessage[] = [];
  let sequence = 0;
  for (const entry of transcript.messages) {
    sequence += 1;
    if (entry.role === "user") {
      messages.push({
        id: `history-user-${sequence}`,
        role: "user",
        content: entry.text,
        timestamp: 0,
      });
      continue;
    }
    if (entry.role === "assistant") {
      messages.push({
        id: `history-assistant-${sequence}`,
        role: "assistant",
        content: entry.text,
        timestamp: 0,
        toolCalls: entry.toolCalls.length
          ? entry.toolCalls.map((call) => ({
              callId: call.id,
              name: call.name,
              input: call.args,
              file: fileFromToolCall(call.name, call.args),
              status: "completed" as const,
            }))
          : undefined,
      });
      continue;
    }
    // A tool entry resolves the matching call on the latest assistant turn.
    const result = entry.toolResult;
    if (!result) continue;
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index];
      const call = message.toolCalls?.find(
        (candidate) => candidate.callId === result.callId,
      );
      if (call) {
        call.output = result.content;
        call.isError = result.isError;
        call.status = result.isError ? "failed" : "completed";
        break;
      }
    }
  }
  if (!transcript.complete && messages.length) {
    messages[0] = {
      ...messages[0],
      notices: [
        "This transcript could not be proven complete; earlier turns may be missing.",
        ...(messages[0].notices ?? []),
      ],
    };
  }
  return messages;
}

/** Quiet human framing for a durable-log approval verdict line. */
const APPROVAL_VERDICT_LABELS: Record<string, string> = {
  allow_once: "allowed once",
  allow_always: "always allowed",
  deny: "denied",
};

/**
 * Renders the steer drain echo's committed media bundle (ADR 0251) as
 * message-attachment chips: inline bytes become data: URLs the existing
 * thumbnail path already displays; url-sourced parts pass the URL through.
 */
export function attachmentsFromSteerParts(
  parts: readonly SteerEchoPart[] | undefined,
): Attachment[] | undefined {
  if (!parts?.length) return undefined;
  const attachments: Attachment[] = [];
  for (const [index, part] of parts.entries()) {
    const extension = part.mimeType.split("/")[1] || "bin";
    attachments.push({
      name: `${part.kind}-${index + 1}.${extension}`,
      type: part.mimeType,
      url: part.data
        ? `data:${part.mimeType};base64,${part.data}`
        : part.url || undefined,
    });
  }
  return attachments;
}

/**
 * Applies one live child-activity event (`delegation_progress` /
 * `delegation_end`, D1) onto the delegation card it belongs to, searching the
 * transcript backwards for the entry keyed by `childId`. Pure — a new array
 * on a hit, the SAME array when the child has no card (a progress frame whose
 * start this visit never saw updates nothing).
 */
export function applyDelegationUpdate(
  messages: AgentMessage[],
  event: Extract<
    StreamEvent,
    { type: "delegation_progress" | "delegation_end" }
  >,
): AgentMessage[] {
  const apply = (delegation: DelegationInfo): DelegationInfo =>
    event.type === "delegation_progress"
      ? {
          ...delegation,
          toolCount: event.toolCount ?? delegation.toolCount,
          inputTokens: event.inputTokens ?? delegation.inputTokens,
          outputTokens: event.outputTokens ?? delegation.outputTokens,
          lastTool: event.toolName ?? delegation.lastTool,
        }
      : {
          ...delegation,
          toolCount: event.toolCount ?? delegation.toolCount,
          inputTokens: event.inputTokens ?? delegation.inputTokens,
          outputTokens: event.outputTokens ?? delegation.outputTokens,
          stop: event.stop || "end_turn",
          durationMs: event.durationMs,
          cause: event.cause,
        };
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (
      !message.delegations?.some(
        (delegation) => delegation.childId === event.childId,
      )
    ) {
      continue;
    }
    const updated: AgentMessage = {
      ...message,
      delegations: message.delegations.map((delegation) =>
        delegation.childId === event.childId ? apply(delegation) : delegation,
      ),
    };
    return [...messages.slice(0, index), updated, ...messages.slice(index + 1)];
  }
  return messages;
}

/**
 * Applies one watch-delivered StreamEvent to a transcript being rebuilt from
 * the durable session watch (ADR 0250). Pure and functional (a new array per
 * change) so the hook can feed it from replay batches and live frames alike.
 *
 * The shape mirrors the live prompt path: assistant activity (tokens, tool
 * calls, delegations, notices) accumulates onto the TRAILING assistant
 * message, and a user-authored record (`user_prompt`, a committed steer
 * echo) closes it — the next assistant activity opens a fresh bubble.
 * Events with no transcript surface return the list unchanged.
 */
export function reduceWatchEvent(
  messages: AgentMessage[],
  event: StreamEvent,
  nextId: () => string,
): AgentMessage[] {
  const last = messages.at(-1);
  /** Applies onto the trailing assistant message, opening one if needed. */
  const onAssistant = (
    apply: (message: AgentMessage) => AgentMessage,
  ): AgentMessage[] => {
    if (last?.role === "assistant") {
      return [...messages.slice(0, -1), apply(last)];
    }
    return [
      ...messages,
      apply({
        id: nextId(),
        role: "assistant",
        content: "",
        timestamp: Date.now(),
      }),
    ];
  };
  switch (event.type) {
    case "user_prompt": {
      // The durable record of what the user asked.
      if (!event.text) return messages;
      return [
        ...messages,
        {
          id: nextId(),
          role: "user",
          content: event.text,
          timestamp: Date.now(),
        },
      ];
    }
    case "steer": {
      // The committed mid-run steer: text plus its media bundle (ADR 0251),
      // so a rebuilt transcript keeps the steer's attachments.
      const attachments = attachmentsFromSteerParts(event.parts);
      if (!event.text && !attachments) return messages;
      return [
        ...messages,
        {
          id: nextId(),
          role: "user",
          content: event.text,
          timestamp: Date.now(),
          attachments,
        },
      ];
    }
    case "token":
      return onAssistant((message) => ({
        ...message,
        content: message.content + event.text,
      }));
    case "reasoning":
      return onAssistant((message) => ({
        ...message,
        reasoning: (message.reasoning ?? "") + event.text,
      }));
    case "tool_call":
      return onAssistant((message) => ({
        ...message,
        toolCalls: [
          ...(message.toolCalls ?? []),
          {
            callId: event.callId,
            name: event.name,
            input: event.input,
            file: event.file,
            status: "running" as const,
          },
        ],
      }));
    case "tool_result": {
      // Resolve the matching call on the latest message that carries it.
      for (let index = messages.length - 1; index >= 0; index -= 1) {
        const message = messages[index];
        if (!message.toolCalls?.some((call) => call.callId === event.callId)) {
          continue;
        }
        const updated: AgentMessage = {
          ...message,
          toolCalls: message.toolCalls.map((call) =>
            call.callId === event.callId
              ? {
                  ...call,
                  output: event.output,
                  isError: event.isError,
                  status: event.isError
                    ? ("failed" as const)
                    : ("completed" as const),
                }
              : call,
          ),
        };
        return [
          ...messages.slice(0, index),
          updated,
          ...messages.slice(index + 1),
        ];
      }
      return messages;
    }
    case "approval_verdict": {
      // The verdict half of a permission ask, rendered as a quiet one-liner.
      const label = APPROVAL_VERDICT_LABELS[event.verdict] ?? event.verdict;
      return onAssistant((message) => ({
        ...message,
        notices: [
          ...(message.notices ?? []),
          `Permission: ${event.toolName || "tool"} ${label || "resolved"}`,
        ],
      }));
    }
    case "notice":
      return onAssistant((message) => ({
        ...message,
        notices: [...(message.notices ?? []), event.text],
      }));
    case "delegation":
      return onAssistant((message) => ({
        ...message,
        delegations: [
          ...(message.delegations ?? []),
          {
            kind: event.kind,
            label: event.label,
            detail: event.detail,
            childId: event.childId,
            background: event.background,
            routingReason: event.routingReason,
          },
        ],
      }));
    case "delegation_progress":
    case "delegation_end":
      return applyDelegationUpdate(messages, event);
    case "run_result": {
      if (event.stop === "error") {
        const detail =
          event.errorText || "The run failed without a specific error.";
        return onAssistant((message) => ({
          ...message,
          failed: true,
          failureDetail: event.permanent
            ? `${detail} (permanent — retrying the identical request cannot succeed)`
            : detail,
          toolCalls: (message.toolCalls ?? []).map((call) =>
            call.status === "running"
              ? { ...call, status: "failed" as const }
              : call,
          ),
        }));
      }
      if (event.text && last?.role === "assistant" && !last.content) {
        // A run that streamed no deltas still carries its final text here.
        return onAssistant((message) => ({ ...message, content: event.text }));
      }
      return messages;
    }
    default:
      // Approval asks, retractions, and usage are hook state, not transcript.
      return messages;
  }
}

/**
 * Chat state for one daemon session. Daemon-only: the sidebar id IS the
 * daemon session id — there is no client-side session mapping and no demo
 * fallback. Opening a chat rehydrates its history from the authoritative
 * transcript endpoint; a null id is a draft whose daemon session is minted on
 * the first send.
 */
export function useAgentChat(
  sessionId: string | null,
  options?: {
    onSessionCreated?: (sessionId: string) => void;
    /** The permission mode a draft's lazily-minted session is created with
     *  (the composer's pending Mode selection). Read at mint time — a ref-like
     *  getter, because the selection can change after this render's closure. */
    createMode?: () => SessionPermissionMode;
    /** The composer's pending model pick ("" = auto-routed); read at mint
     *  time like createMode. */
    createModel?: () => { modelId: string; providerId: string } | null;
    /**
     * The session's daemon lifecycle state from the inventory poll
     * (idle/running/awaiting/…). Reactive — when it reads running/awaiting
     * and the daemon supports `watch_session_events`, the hook attaches a
     * durable watch (ADR 0250) to render the externally-driven run live
     * instead of a frozen "running" badge.
     */
    sessionState?: string;
  },
) {
  const { connected, features, serverCapabilities } = useRuntimeStatus();
  const [messages, setMessages] = useState<AgentMessage[]>([]);
  const [status, setStatus] = useState<ChatStatus>("idle");
  const [error, setError] = useState<string | null>(null);
  const [pendingApproval, setPendingApproval] =
    useState<ApprovalRequest | null>(null);
  const [pendingClarification] = useState<ClarificationRequest | null>(null);
  /** Steers the daemon accepted but has not yet drained into the run, in send
   *  order. The stream's `steer` echo splits this list on its watermark id. */
  const [pendingSteers, setPendingSteers] = useState<PendingSteer[]>([]);
  const steerSerialRef = useRef(0);
  const [usage, setUsage] = useState({
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
    estimatedCost: null as number | null,
  });

  // The daemon session backing this chat: the route id, or the one minted for
  // a draft on first send. A ref so an in-flight stream keeps its binding
  // while the parent navigates to the new id.
  const daemonIdRef = useRef<string | null>(sessionId);
  const abortRef = useRef<AbortController | null>(null);
  const lastPromptRef = useRef<string | null>(null);
  // The last failed terminal's typed disposition (ADR 0239), captured from
  // run_result frames: "retryable" routes retryLast through the retry
  // endpoint instead of re-sending the prompt. Cleared when a run starts.
  const lastDispositionRef = useRef<RetryDisposition | undefined>(undefined);
  // The active run's opaque identity (Event.run_id, ADR 0249), captured from
  // the first run-bearing event of the prompt stream — or the latest one a
  // watch delivered. Approve/cancel send it as expected_run_id so a stale
  // control can never act on the session's NEXT run. "" = unknown.
  const runIdRef = useRef("");
  /** The daemon named the run this tab drives (ADR 0249): scope controls to it. */
  const adoptRunId = useCallback((runId: string) => {
    if (runId && !runIdRef.current) runIdRef.current = runId;
  }, []);
  // The session id whose run THIS hook's prompt stream is driving right now —
  // the durable watch must not attach on top of it (the prompt path owns the
  // view). An id, not a boolean: a stream can outlive a chat switch.
  const drivingRef = useRef<string | null>(null);
  // The live watch's teardown + resume position (per-session, opaque).
  const watchAbortRef = useRef<AbortController | null>(null);
  const watchCursorRef = useRef("");
  // Set when a watch faulted for this session id: the transcript fallback is
  // already showing and re-attaching would loop. Cleared when the session
  // leaves the running/awaiting stretch (a later run gets a fresh watch).
  const watchFaultedRef = useRef<string | null>(null);
  // Attachments sent this visit, keyed by session: the daemon's transcript
  // carries no attachment bytes, so every rehydrate would strip the chips —
  // this ref re-attaches them by matching user turns in send order.
  const sentAttachmentsRef = useRef(
    new Map<string, { content: string; attachments: Attachment[] }[]>(),
  );

  const onSessionCreatedRef = useRef(options?.onSessionCreated);
  onSessionCreatedRef.current = options?.onSessionCreated;
  const createModeRef = useRef(options?.createMode);
  createModeRef.current = options?.createMode;
  const createModelRef = useRef(options?.createModel);
  createModelRef.current = options?.createModel;

  /**
   * Rebuilds the message list from the daemon's authoritative transcript,
   * re-attaching this visit's sent-attachment bytes. Shared by the open
   * rehydrate, the watch-fault fallback, and the stale-run-control refresh.
   */
  const rehydrate = useCallback(async (id: string, signal?: AbortSignal) => {
    const transcript = await fetchSessionTranscriptMessages(id, signal);
    if (signal?.aborted) return;
    const rebuilt = messagesFromTranscript(transcript);
    let sent = sentAttachmentsRef.current.get(id);
    if (!sent?.length) {
      // A fresh visit: the bytes live only in IndexedDB.
      const stored = await loadSentAttachments(id);
      if (signal?.aborted) return;
      if (stored.length) {
        sentAttachmentsRef.current.set(id, stored);
        sent = stored;
      }
    }
    if (sent?.length) {
      const pool = [...sent];
      for (const message of rebuilt) {
        if (message.role !== "user") continue;
        const index = pool.findIndex(
          (record) => record.content === message.content,
        );
        if (index !== -1) {
          message.attachments = pool[index].attachments;
          pool.splice(index, 1);
        }
      }
    }
    setMessages(rebuilt);
  }, []);

  // The durable watch is gated on the daemon's open feature registry and the
  // session actually having a run to watch (running/awaiting per inventory).
  const watchSupported = features.has("watch_session_events");
  const sessionState = options?.sessionState ?? "";
  const watchable = sessionState === "running" || sessionState === "awaiting";

  // Opening a chat (or switching chats) rehydrates from the daemon.
  // watchSupported/watchable are deliberately NOT dependencies: they are read
  // at open time only — a mid-view flip is the watch effect's business, not a
  // reason to refetch (and re-wipe) the transcript.
  // biome-ignore lint/correctness/useExhaustiveDependencies: see above
  useEffect(() => {
    daemonIdRef.current = sessionId;
    // Slash commands are per-session (GET /v1/commands?session_id=): refresh
    // the composer's list for the chat being opened.
    if (sessionId) void refreshSlashCommands(sessionId);
    runIdRef.current = "";
    watchCursorRef.current = "";
    lastDispositionRef.current = undefined;
    setMessages([]);
    setPendingApproval(null);
    setError(null);
    setStatus("idle");
    // Usage is per-visit, per-chat: the context meter must not carry one
    // chat's spend into the next.
    setUsage({
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      reasoningTokens: 0,
      estimatedCost: null,
    });
    if (!sessionId || !connected) return;
    // A run another client is driving: the watch effect below owns the
    // rebuild (its replay covers the whole transcript), so the fetch here
    // would only race it and be overwritten.
    if (watchSupported && watchable && drivingRef.current !== sessionId) {
      return;
    }
    const controller = new AbortController();
    void rehydrate(sessionId, controller.signal).catch((caught) => {
      if (controller.signal.aborted) return;
      setError(caught instanceof Error ? caught.message : String(caught));
      setStatus("error");
    });
    return () => controller.abort();
  }, [sessionId, connected, rehydrate]);

  // The durable session watch (ADR 0250): when this chat's run is being
  // driven ELSEWHERE (a schedule fire, another tab, a gRPC client) and the
  // daemon supports it, attach from the beginning — the replay rebuilds the
  // transcript, the live boundary switches to the streaming view, and the
  // terminal result hands back to the normal completed-state flow. Studio's
  // own prompt-stream path is untouched: a run this tab drives never watches.
  useEffect(() => {
    if (!sessionId || !connected || !watchSupported || !watchable) {
      // Leaving the running/awaiting stretch clears the fault latch, so the
      // session's NEXT run gets a fresh watch.
      watchFaultedRef.current = null;
      return;
    }
    if (drivingRef.current === sessionId) return;
    if (watchFaultedRef.current === sessionId) return;
    const controller = new AbortController();
    watchAbortRef.current = controller;

    // The transcript being rebuilt from replay + live frames. Replay flushes
    // in batches (a long history must not commit thousands of renders); every
    // live frame renders immediately, like the prompt path.
    let rebuilt: AgentMessage[] = [];
    let live = false;
    let frames = 0;
    let serial = 0;
    // An ask seen in replay that no later verdict/retract resolved: surfaced
    // at the boundary — exactly the parked-approval case (state "awaiting").
    let parkedAsk: ApprovalRequest | null = null;
    const nextId = () => {
      serial += 1;
      return `watch-${serial}`;
    };
    const flush = () => setMessages(rebuilt);
    const resolveAsk = (approvalId: string) => {
      if (parkedAsk?.approvalId === approvalId) parkedAsk = null;
      if (!live) return;
      setPendingApproval((current) =>
        current?.approvalId === approvalId ? null : current,
      );
      setStatus((current) =>
        current === "waiting_approval" ? "streaming" : current,
      );
    };

    setStatus("streaming");
    setPendingApproval(null);
    setError(null);

    void watchSessionEvents(
      sessionId,
      (delivery) => {
        if (delivery.cursor) watchCursorRef.current = delivery.cursor;
        const event = delivery.event;
        if (!event) {
          if (delivery.phase === "live" && !live) {
            // The replay→live boundary: the rebuild is complete. Show it and
            // surface a still-unresolved ask (the parked-approval case).
            live = true;
            flush();
            if (parkedAsk) {
              setPendingApproval(parkedAsk);
              setStatus("waiting_approval");
            }
          }
          return;
        }
        // The LATEST run-bearing event names the current run (a replay spans
        // every earlier run of the session too).
        if (event.runId) runIdRef.current = event.runId;
        switch (event.type) {
          case "approval":
            parkedAsk = {
              approvalId: event.approvalId,
              sessionId,
              toolName: event.toolName,
              description: event.description,
              details: event.details,
            };
            if (live) {
              setPendingApproval(parkedAsk);
              setStatus("waiting_approval");
            }
            break;
          case "retract":
            resolveAsk(event.approvalId);
            break;
          case "approval_verdict":
            // Another client resolved the ask; the quiet verdict line also
            // lands in the transcript via the reducer.
            resolveAsk(event.approvalId);
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            if (live) flush();
            break;
          case "usage":
            // Only live frames accumulate: replay covers finished runs whose
            // figures this visit never counted anywhere else either.
            if (live) {
              setUsage((prev) => ({
                inputTokens: prev.inputTokens + event.inputTokens,
                outputTokens: prev.outputTokens + event.outputTokens,
                cacheReadTokens:
                  prev.cacheReadTokens + (event.cacheReadTokens ?? 0),
                cacheWriteTokens:
                  prev.cacheWriteTokens + (event.cacheWriteTokens ?? 0),
                reasoningTokens:
                  prev.reasoningTokens + (event.reasoningTokens ?? 0),
                estimatedCost: event.estimatedCost,
              }));
            }
            break;
          case "run_result":
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            if (live) {
              // The terminal result ends the watch; the normal
              // completed-state flow takes over from here.
              flush();
              runIdRef.current = "";
              if (event.stop === "error") {
                lastDispositionRef.current = event.retryDisposition;
                setError(
                  event.errorText || "The run failed without a specific error.",
                );
                setStatus("error");
              } else {
                setStatus("idle");
              }
              controller.abort();
            }
            break;
          default:
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            frames += 1;
            if (live || frames % 200 === 0) flush();
        }
      },
      { signal: controller.signal },
    ).catch(() => {
      if (controller.signal.aborted) return;
      // Any watch fault — activity_gap / cursor_expired / an exhausted
      // reconnect budget / a pre-stream refusal — falls back to the
      // authoritative transcript: the codes differ, the recovery is the
      // same, and the latch stops a re-attach loop while the run continues.
      watchFaultedRef.current = sessionId;
      setPendingApproval(null);
      setStatus("idle");
      void rehydrate(sessionId).catch(() => undefined);
    });

    return () => {
      controller.abort();
      if (watchAbortRef.current === controller) watchAbortRef.current = null;
      // A torn-down watch (chat switch, state flip, disconnect) must not
      // leave the streaming badge stuck; real terminals set their own state.
      setStatus((current) =>
        current === "streaming" || current === "waiting_approval"
          ? "idle"
          : current,
      );
    };
  }, [sessionId, connected, watchSupported, watchable, rehydrate]);

  const queueMessage = useCallback((text: string, files?: File[]) => {
    const trimmed = text.trim();
    if (!trimmed && !files?.length) return;
    setQueuedMessages((prev) => [
      ...prev,
      {
        id: `queued-${Date.now()}-${prev.length}`,
        text: trimmed,
        files: files?.length ? files : undefined,
      },
    ]);
  }, []);

  /**
   * Builds the per-run stream-event handler shared by the prompt stream and
   * the failed-step retry relay (ADR 0239) — both drive the SAME translated
   * event switch. `ids.assistant` is deliberately mutable: the steer drain
   * echo re-anchors accumulation onto a fresh assistant bubble.
   */
  const makeStreamHandler = useCallback(
    (daemonId: string, ids: { assistant: string }) => {
      // Every update is a functional setState: tokens and tool results arrive
      // faster than React commits, so reading the previous array from the
      // closure would drop frames (rerender-functional-setstate).
      const patch = (apply: (message: AgentMessage) => AgentMessage) =>
        setMessages((prev) =>
          prev.map((message) =>
            message.id === ids.assistant ? apply(message) : message,
          ),
        );
      return (event: StreamEvent) => {
        // The first run-bearing event names the run (ADR 0249); the id
        // scopes this run's approve/cancel/steer controls.
        if (event.runId && !runIdRef.current) {
          runIdRef.current = event.runId;
        }
        switch (event.type) {
          case "token":
            patch((message) => ({
              ...message,
              content: message.content + event.text,
            }));
            break;
          case "reasoning":
            patch((message) => ({
              ...message,
              reasoning: (message.reasoning ?? "") + event.text,
            }));
            break;
          case "tool_call": {
            const call: ToolCallInfo = {
              callId: event.callId,
              name: event.name,
              input: event.input,
              file: event.file,
              status: "running",
            };
            patch((message) => ({
              ...message,
              toolCalls: [...(message.toolCalls ?? []), call],
            }));
            break;
          }
          case "tool_result":
            patch((message) => ({
              ...message,
              toolCalls: (message.toolCalls ?? []).map((call) =>
                call.callId === event.callId
                  ? {
                      ...call,
                      output: event.output,
                      isError: event.isError,
                      status: event.isError ? "failed" : "completed",
                    }
                  : call,
              ),
            }));
            break;
          case "approval":
            setPendingApproval({
              approvalId: event.approvalId,
              sessionId: daemonId,
              toolName: event.toolName,
              description: event.description,
              details: event.details,
            });
            setStatus("waiting_approval");
            break;
          case "retract":
            // The ask was withdrawn (e.g. its child was cancelled); the
            // run is still going.
            setPendingApproval((current) =>
              current?.approvalId === event.approvalId ? null : current,
            );
            setStatus((current) =>
              current === "waiting_approval" ? "streaming" : current,
            );
            break;
          case "steer": {
            // The daemon drained the pending steer bundle into the run.
            // The accepted steers are already optimistic user bubbles;
            // MOVE them to the drain boundary and open a fresh assistant
            // bubble there, so the reply to the injection streams below
            // it in reading order. No duplicate echo message is added —
            // the optimistic bubbles carry the same text the echo merges.
            const nextAssistantId = `assistant-${Date.now() + 1}`;
            ids.assistant = nextAssistantId;
            setPendingSteers((prev) => {
              const remaining = splitPendingSteersOnWatermark(
                prev,
                event.messageId,
              );
              const remainingIds = new Set(remaining.map((p) => p.id));
              const drained = prev.filter((p) => !remainingIds.has(p.id));
              const drainedBubbleIds = new Set(
                drained.map((p) => `steer-user-${p.id}`),
              );
              setMessages((msgs) => {
                const moved = msgs.filter((m) => drainedBubbleIds.has(m.id));
                const rest = msgs.filter((m) => !drainedBubbleIds.has(m.id));
                // A steer accepted by the daemon but missing locally
                // (e.g. after a reload) still surfaces via the echo — with
                // its committed media bundle (ADR 0251).
                const echoAttachments = attachmentsFromSteerParts(event.parts);
                const bubbles =
                  moved.length > 0
                    ? moved
                    : event.text || echoAttachments
                      ? [
                          {
                            id: `steer-echo-${Date.now()}`,
                            role: "user" as const,
                            content: event.text,
                            timestamp: Date.now(),
                            attachments: echoAttachments,
                          },
                        ]
                      : [];
                return [
                  ...rest,
                  ...bubbles,
                  {
                    id: nextAssistantId,
                    role: "assistant",
                    content: "",
                    timestamp: Date.now() + 1,
                  },
                ];
              });
              return remaining;
            });
            break;
          }
          case "notice":
            patch((message) => ({
              ...message,
              notices: [...(message.notices ?? []), event.text],
            }));
            break;
          case "delegation":
            patch((message) => ({
              ...message,
              delegations: [
                ...(message.delegations ?? []),
                {
                  kind: event.kind,
                  label: event.label,
                  detail: event.detail,
                  childId: event.childId,
                  background: event.background,
                  routingReason: event.routingReason,
                },
              ],
            }));
            break;
          case "delegation_progress":
          case "delegation_end":
            // Live child cards (D1): counters tick while the child works;
            // the terminal stamps stop/duration/cause onto the card.
            setMessages((prev) => applyDelegationUpdate(prev, event));
            break;
          case "usage":
            // The daemon reports per-run figures; the chat total is
            // their sum. (Lost on reload: the HTTP read surface does
            // not expose the session's cumulative usage yet.)
            setUsage((prev) => ({
              inputTokens: prev.inputTokens + event.inputTokens,
              outputTokens: prev.outputTokens + event.outputTokens,
              cacheReadTokens:
                prev.cacheReadTokens + (event.cacheReadTokens ?? 0),
              cacheWriteTokens:
                prev.cacheWriteTokens + (event.cacheWriteTokens ?? 0),
              reasoningTokens:
                prev.reasoningTokens + (event.reasoningTokens ?? 0),
              estimatedCost: event.estimatedCost,
            }));
            break;
          case "run_result":
            // The run is over; a control scoped to it would be stale.
            runIdRef.current = "";
            if (event.stop === "error") {
              // The typed disposition routes the Retry button (ADR 0239).
              lastDispositionRef.current = event.retryDisposition;
              const detail =
                event.errorText || "The run failed without a specific error.";
              patch((message) => ({
                ...message,
                failed: true,
                failureDetail: event.permanent
                  ? `${detail} (permanent — retrying the identical request cannot succeed)`
                  : detail,
                toolCalls: (message.toolCalls ?? []).map((call) =>
                  call.status === "running"
                    ? { ...call, status: "failed" as const }
                    : call,
                ),
              }));
              setError(detail);
              setStatus("error");
            } else if (event.text) {
              // A run that produced no deltas (a rehydrated approve, a
              // recovered run) still carries its final text here.
              patch((message) => ({
                ...message,
                content: message.content || event.text,
              }));
            }
            break;
          default:
            break;
        }
      };
    },
    [],
  );

  const sendMessage = useCallback(
    async (content: string, files?: File[]) => {
      if (!connected) return;
      if (status === "streaming" || status === "waiting_approval") {
        // Defense in depth behind the composer's own routing: text sent while
        // a run is live is HELD, never fired into the funnel (which would
        // refuse with "already has an active run") and never dropped.
        queueMessage(content, files);
        return;
      }

      // Only images cross the wire — the daemon's prompt parts are
      // image/audio only (documents are a daemon capability gap).
      const images = (files ?? []).filter((file) =>
        file.type.startsWith("image/"),
      );
      let parts: PromptPart[];
      try {
        parts = await Promise.all(images.map(imageToPart));
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        return;
      }
      // Each image attachment carries the SAME bytes the wire part holds (a
      // data: URL), so the chip's thumbnail and the canvas preview work on
      // the live message. Rehydrated transcripts have no bytes — the daemon
      // never echoes attachment content — so old messages stay preview-less.
      const attachments: Attachment[] | undefined =
        images.length > 0
          ? images.map((file, index) => {
              const part = parts[index];
              return {
                name: file.name,
                type: file.type,
                url: part
                  ? `data:${part.mime_type};base64,${part.data}`
                  : undefined,
              };
            })
          : undefined;

      const userMessage: AgentMessage = {
        id: `user-${Date.now()}`,
        role: "user",
        content,
        timestamp: Date.now(),
        attachments,
      };

      setMessages((prev) => [...prev, userMessage]);
      setStatus("streaming");
      if (attachments && daemonIdRef.current) {
        const log = sentAttachmentsRef.current.get(daemonIdRef.current) ?? [];
        log.push({ content, attachments });
        sentAttachmentsRef.current.set(daemonIdRef.current, log);
        void saveSentAttachments(daemonIdRef.current, log);
      }
      setError(null);
      lastPromptRef.current = content;

      const ids = { assistant: `assistant-${Date.now()}` };
      setMessages((prev) => [
        ...prev,
        {
          id: ids.assistant,
          role: "assistant",
          content: "",
          timestamp: Date.now(),
        },
      ]);

      const controller = new AbortController();
      abortRef.current = controller;

      // Failure patches outside the stream handler target the CURRENT
      // assistant bubble (the steer echo may have re-anchored it).
      const patch = (apply: (message: AgentMessage) => AgentMessage) =>
        setMessages((prev) =>
          prev.map((message) =>
            message.id === ids.assistant ? apply(message) : message,
          ),
        );

      let daemonId = daemonIdRef.current;
      try {
        if (!daemonId) {
          const createModel = createModelRef.current?.() ?? null;
          daemonId = await createHarnessSession(
            encodeSessionPermissionMode(createModeRef.current?.() ?? "default"),
            createModel
              ? {
                  modelId: createModel.modelId,
                  providerId: createModel.providerId,
                }
              : {},
          );
          daemonIdRef.current = daemonId;
          void refreshSlashCommands(daemonId);
          // The pre-mint record above keyed nothing; re-key it now.
          if (attachments) {
            const log = sentAttachmentsRef.current.get(daemonId) ?? [];
            if (
              !log.some(
                (r) => r.content === content && r.attachments === attachments,
              )
            ) {
              log.push({ content, attachments });
            }
            sentAttachmentsRef.current.set(daemonId, log);
            void saveSentAttachments(
              daemonId,
              sentAttachmentsRef.current.get(daemonId) ?? [],
            );
          }
          onSessionCreatedRef.current?.(daemonId);
        }
        // This tab drives the run now: the durable watch must not attach on
        // top of the prompt stream, and the run's identity starts unknown.
        drivingRef.current = daemonId;
        runIdRef.current = "";
        lastDispositionRef.current = undefined;
        await streamHarnessPrompt(
          daemonId,
          content,
          parts,
          makeStreamHandler(daemonId, ids),
          controller.signal,
          { onRunStarted: adoptRunId },
        );
        // A parked approval keeps its own status: the stream ends while the
        // run is still waiting on the operator, and flipping to idle here
        // would hide the pending prompt. A failed turn keeps its error state.
        setStatus((current) =>
          current === "waiting_approval" || current === "error"
            ? current
            : "idle",
        );
      } catch (caught) {
        if (controller.signal.aborted) {
          setStatus("idle");
          return;
        }
        const message =
          caught instanceof Error ? caught.message : String(caught);
        setError(message);
        setStatus("error");
        patch((current) => ({
          ...current,
          failed: true,
          failureDetail: current.failureDetail ?? message,
          toolCalls: (current.toolCalls ?? []).map((call) =>
            call.status === "running"
              ? { ...call, status: "failed" as const }
              : call,
          ),
        }));
      } finally {
        abortRef.current = null;
        if (daemonId && drivingRef.current === daemonId) {
          drivingRef.current = null;
        }
      }
    },
    [status, connected, queueMessage, makeStreamHandler, adoptRunId],
  );

  /**
   * Binds this hook to an ALREADY-CREATED daemon session without re-keying it.
   * The thread panel mints its own seeded session (source_session_id must be
   * carried, and the 412-busy case has to keep the composer text), then
   * adopts the id here so the first send streams against it. Re-keying the
   * hook instead would refetch the transcript mid-stream and wipe the
   * optimistic messages — the same reason a draft keeps its null hook id
   * after minting.
   */
  const adoptSession = useCallback((id: string) => {
    daemonIdRef.current = id;
    void refreshSlashCommands(id);
  }, []);

  /** Re-sends the last prompt after a failure — the legacy Retry path, kept
   *  for permanent/unknown dispositions and older daemons. */
  const resendLast = useCallback(async () => {
    const prompt = lastPromptRef.current;
    if (!prompt) return;
    // Drop the failed exchange so the retry replaces it instead of stacking.
    setMessages((prev) => {
      const trimmed = [...prev];
      while (trimmed.length) {
        const last = trimmed[trimmed.length - 1];
        if (last.role === "assistant" && (last.failed || !last.content)) {
          trimmed.pop();
          continue;
        }
        if (last.role === "user" && last.content === prompt) {
          trimmed.pop();
        }
        break;
      }
      return trimmed;
    });
    setError(null);
    setStatus("idle");
    await sendMessage(prompt);
  }, [sendMessage]);

  /**
   * The error banner's Retry. When the failed terminal was typed RETRYABLE
   * (ADR 0239), this drives `POST .../retry`: the daemon re-drives the
   * recorded failed step itself and relays the run as SSE — no user message
   * is re-sent, which is exactly the duplicate-effects path the endpoint
   * exists to prevent. A 409 `failed_step_retry_ineligible` (the intent
   * raced away) falls back to the resend path; a PERMANENT or untyped
   * failure keeps the resend path (and the composer's edit-and-resend).
   */
  const retryLast = useCallback(async () => {
    if (status === "streaming") return;
    const daemonId = daemonIdRef.current;
    if (lastDispositionRef.current !== "retryable" || !daemonId || !connected) {
      await resendLast();
      return;
    }
    // Drop the failed assistant bubble — the retried step streams into a
    // fresh one; the user message stays (nothing is re-sent).
    setMessages((prev) => {
      const trimmed = [...prev];
      while (trimmed.length) {
        const last = trimmed[trimmed.length - 1];
        if (last.role === "assistant" && (last.failed || !last.content)) {
          trimmed.pop();
          continue;
        }
        break;
      }
      return trimmed;
    });
    setError(null);
    setStatus("streaming");

    const ids = { assistant: `assistant-${Date.now()}` };
    setMessages((prev) => [
      ...prev,
      {
        id: ids.assistant,
        role: "assistant",
        content: "",
        timestamp: Date.now(),
      },
    ]);
    const controller = new AbortController();
    abortRef.current = controller;
    // This tab drives the retried run: the watch must not attach on top.
    drivingRef.current = daemonId;
    runIdRef.current = "";
    lastDispositionRef.current = undefined;
    let ineligible = false;
    try {
      await retryHarnessRun(
        daemonId,
        makeStreamHandler(daemonId, ids),
        controller.signal,
        { onRunStarted: adoptRunId },
      );
      setStatus((current) =>
        current === "waiting_approval" || current === "error"
          ? current
          : "idle",
      );
    } catch (caught) {
      if (controller.signal.aborted) {
        setStatus("idle");
        return;
      }
      if (
        caught instanceof HarnessApiError &&
        (caught.code === "failed_step_retry_ineligible" ||
          caught.status === 409)
      ) {
        // The retry intent is gone daemon-side (another client acted, or
        // the state moved on): quietly fall back to re-sending the prompt.
        ineligible = true;
      } else {
        const message =
          caught instanceof Error ? caught.message : String(caught);
        setError(message);
        setStatus("error");
      }
    } finally {
      abortRef.current = null;
      if (drivingRef.current === daemonId) drivingRef.current = null;
    }
    if (ineligible) await resendLast();
  }, [status, connected, resendLast, makeStreamHandler, adoptRunId]);

  /** A message typed while a run was active, held client-side: the daemon is
   *  strictly one-run-at-a-time (a mid-run prompt answers 412), so the queue
   *  lives here and drains one message per completed run. */
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([]);
  const flushingRef = useRef(false);

  const deleteQueued = useCallback((id: string) => {
    setQueuedMessages((prev) => prev.filter((m) => m.id !== id));
  }, []);

  /** Removes the message from the queue and returns its text (for editing). */
  const takeQueued = useCallback(
    (id: string) => {
      const hit = queuedMessages.find((m) => m.id === id);
      if (hit) setQueuedMessages((prev) => prev.filter((m) => m.id !== id));
      return hit?.text ?? null;
    },
    [queuedMessages],
  );

  const cancelChat = useCallback(async () => {
    abortRef.current?.abort();
    if (daemonIdRef.current) {
      // Scoped to the run this hook knows about (ADR 0249): if that run
      // already ended, the daemon answers 409 stale_run_control and the
      // session's NEXT run is left untouched — exactly what "cancel" meant.
      await cancelHarnessRun(daemonIdRef.current, runIdRef.current);
    }
    setStatus("idle");
  }, []);

  // Steer is capability-gated (C1.2): the live `capabilities.steer` off
  // /v1/compatibility, or the `http_steer` feature-registry row a rebuilt
  // daemon serves. Absent both, mid-run sends queue instead.
  const steerSupported =
    serverCapabilities.steer === true || features.has("http_steer");

  /**
   * Injects a message — text plus staged image attachments (ADR 0251) — into
   * the in-flight run at the next turn boundary. accepted/appended park it on
   * the pending list until the drain echo; too_late, a 409
   * `stale_run_control` (the strict steer's "that run already ended"), or a
   * failed request fall back to the queue so nothing is lost — the queue
   * drains it as a normal prompt.
   *
   * Every steer is STRICT (expected_run_id, ADR 0252): an unqualified steer
   * that loses the terminal race would be PROMOTED into a follow-up run
   * behind Studio's back, so an unknown run id queues rather than steers.
   */
  const steerMessage = useCallback(
    async (text: string, files?: File[]) => {
      const trimmed = text.trim();
      if (!trimmed && !files?.length) return;
      const daemonId = daemonIdRef.current;
      const runId = runIdRef.current;
      if (!daemonId || !steerSupported || !runId) {
        queueMessage(trimmed, files);
        return;
      }
      const images = (files ?? []).filter((file) =>
        file.type.startsWith("image/"),
      );
      let parts: PromptPart[];
      try {
        parts = await Promise.all(images.map(imageToPart));
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
        return;
      }
      // Same bytes as the wire parts, so the optimistic bubble's chips show
      // thumbnails (mirrors sendMessage's attachment handling).
      const attachments: Attachment[] | undefined =
        images.length > 0
          ? images.map((file, index) => {
              const part = parts[index];
              return {
                name: file.name,
                type: file.type,
                url: part
                  ? `data:${part.mime_type};base64,${part.data}`
                  : undefined,
              };
            })
          : undefined;
      steerSerialRef.current += 1;
      const id = `steer-${Date.now()}-${steerSerialRef.current}`;
      try {
        const { outcome } = await steerHarnessRun(daemonId, trimmed, id, {
          expectedRunId: runId,
          parts,
        });
        if (outcome === "accepted" || outcome === "appended") {
          // The injected text IS a chat message — show it in the transcript
          // right away, attachments included. pendingSteers stays internal
          // bookkeeping (watermark reconciliation at the drain echo).
          setMessages((prev) => [
            ...prev,
            {
              id: `steer-user-${id}`,
              role: "user",
              content: trimmed,
              timestamp: Date.now(),
              attachments,
            },
          ]);
          setPendingSteers((prev) => [
            ...prev,
            { id, text: trimmed, files: images.length ? images : undefined },
          ]);
          return;
        }
        queueMessage(trimmed, files);
      } catch (caught) {
        if (
          caught instanceof HarnessApiError &&
          caught.code === "stale_run_control"
        ) {
          // The named run already ended — exactly the too_late outcome:
          // requeue quietly, never an error toast (A2.3/C1.3).
          runIdRef.current = "";
          queueMessage(trimmed, files);
          return;
        }
        queueMessage(trimmed, files);
        setError(caught instanceof Error ? caught.message : String(caught));
      }
    },
    [queueMessage, steerSupported],
  );

  /** "Steer": pull a queued message and inject it into the in-flight run at
   *  the next turn boundary; on an idle chat it just sends. */
  const steerQueued = useCallback(
    (id: string) => {
      const hit = queuedMessages.find((m) => m.id === id);
      if (!hit) return;
      setQueuedMessages((prev) => prev.filter((m) => m.id !== id));
      if (status === "streaming" || status === "waiting_approval") {
        void steerMessage(hit.text, hit.files);
        return;
      }
      void sendMessage(hit.text, hit.files);
    },
    [status, queuedMessages, steerMessage, sendMessage],
  );

  /**
   * Retracts the pending steer bundle. The daemon models one bundle per run —
   * only the whole thing can be retracted, not a single message. On
   * `none_pending` the bundle already drained (the echo reconciles the list),
   * so the pending list clears on either outcome.
   */
  const cancelPendingSteers = useCallback(async () => {
    const daemonId = daemonIdRef.current;
    if (!daemonId) {
      setPendingSteers([]);
      return;
    }
    try {
      await cancelHarnessSteer(daemonId, runIdRef.current);
      setPendingSteers([]);
    } catch (caught) {
      // Unknown daemon state: keep the list rather than pretend it retracted.
      setError(caught instanceof Error ? caught.message : String(caught));
    }
  }, []);

  // On run end, steers that never drained were dropped with the run (the
  // daemon's steer buffer is run-scoped, best-effort): move them to the FRONT
  // of the queue, in order, so the drain below sends them as normal prompts —
  // never lose text. Runs on error too: the texts wait visibly in the strip.
  useEffect(() => {
    if (status !== "idle" && status !== "error") return;
    if (pendingSteers.length === 0) return;
    const orphaned = pendingSteers;
    setPendingSteers([]);
    // Their optimistic bubbles come out of the transcript too — the daemon
    // dropped these with the run, so the queue (visible) owns the text now.
    const bubbleIds = new Set(orphaned.map((p) => `steer-user-${p.id}`));
    setMessages((prev) => prev.filter((m) => !bubbleIds.has(m.id)));
    setQueuedMessages((prev) => [
      ...orphaned.map((steer) => ({
        id: `queued-${steer.id}`,
        text: steer.text,
        files: steer.files,
      })),
      ...prev,
    ]);
  }, [status, pendingSteers]);

  // Drain the queue one message per completed run. Only a clean idle flushes:
  // an error waits for the user (retry/edit), a parked approval waits for the
  // verdict, and orphaned pending steers get requeued (above) before anything
  // sends. flushingRef bridges the async gap before sendMessage flips the
  // status, so a re-render can't double-send.
  useEffect(() => {
    if (
      status !== "idle" ||
      !connected ||
      queuedMessages.length === 0 ||
      pendingSteers.length > 0 ||
      flushingRef.current
    ) {
      return;
    }
    flushingRef.current = true;
    const next = queuedMessages[0];
    setQueuedMessages((prev) => prev.filter((m) => m.id !== next.id));
    void sendMessage(next.text, next.files).finally(() => {
      flushingRef.current = false;
    });
  }, [status, connected, queuedMessages, pendingSteers, sendMessage]);

  const respondToApproval = useCallback(
    async (choice: ApprovalChoice) => {
      const daemonId = daemonIdRef.current;
      const approvalId = pendingApproval?.approvalId;
      setPendingApproval(null);
      // The verdict resumes the SAME run — the prompt stream stays open and
      // keeps delivering (the daemon acks the approve; only the run's end
      // closes the stream). Mirror the retract handler: back to streaming,
      // never idle — a premature idle here let the steer-requeue and queue
      // drain effects fire against the still-live run (a pending steer got
      // re-sent as a prompt that 412s). The stream's own end handler owns
      // the eventual idle.
      setStatus((current) =>
        current === "waiting_approval" ? "streaming" : current,
      );
      if (!daemonId || !approvalId) return;
      try {
        // The daemon's verdict is three-way. "session" and "always" both map
        // to allow_always — the daemon models one persistent grant scope, and
        // splitting hairs the backend does not model would be a lie in the UI.
        await respondToHarnessApproval(
          daemonId,
          approvalId,
          choice === "deny"
            ? "deny"
            : choice === "once"
              ? "allow_once"
              : "allow_always",
          runIdRef.current,
        );
      } catch (caught) {
        if (
          caught instanceof HarnessApiError &&
          caught.code === "stale_run_control"
        ) {
          // The run this dialog belonged to already ended (e.g. another
          // client answered, or a schedule fire replaced it). Not an error:
          // refresh quietly and let the transcript show what happened.
          runIdRef.current = "";
          setStatus("idle");
          void rehydrate(daemonId).catch(() => undefined);
          return;
        }
        setError(caught instanceof Error ? caught.message : String(caught));
        setStatus("error");
      }
    },
    [pendingApproval, rehydrate],
  );

  const respondToClarification = useCallback(async (_response: string) => {
    setStatus("idle");
  }, []);

  /** Re-fetches the authoritative transcript (e.g. after a manual compaction
   *  rewrote the model history, B1.3). No-op on a draft with no session. */
  const refreshTranscript = useCallback(async () => {
    const daemonId = daemonIdRef.current;
    if (!daemonId) return;
    await rehydrate(daemonId);
  }, [rehydrate]);

  return {
    messages,
    // A parked approval is still an in-flight run daemon-side; the composer
    // treats both as "run active" (queue/steer, never a raw prompt).
    isStreaming: status === "streaming" || status === "waiting_approval",
    queuedMessages,
    queueMessage,
    deleteQueued,
    takeQueued,
    steerQueued,
    pendingSteers,
    steerMessage,
    /** Mid-run steering is available (capability/feature gate, C1.2). */
    steerSupported,
    cancelPendingSteers,
    status,
    error,
    harnessLive: connected,
    sendMessage,
    adoptSession,
    retryLast,
    refreshTranscript,
    cancelChat,
    pendingApproval,
    pendingClarification,
    respondToApproval,
    respondToClarification,
    usage,
  };
}
