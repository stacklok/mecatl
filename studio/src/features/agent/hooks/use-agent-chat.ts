"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import {
  mapPromptValidationError,
  resolveMediaCapabilities,
} from "@/lib/attachment-inline";
import {
  loadSentAttachments,
  type SentAttachmentRecord,
  saveSentAttachments,
} from "@/lib/attachment-store";
import { fileFromToolCall } from "@/lib/file-meta";
import {
  cancelHarnessChild,
  cancelHarnessRun,
  cancelHarnessSteer,
  cancelMcpAuthorization,
  createHarnessSession,
  fetchHarnessSessionDetail,
  fetchMcpAuthorizationUrl,
  fetchSessionTranscriptMessages,
  HarnessApiError,
  type HarnessSessionDetail,
  type McpAuthorizationControlOutcome,
  type PromptPart,
  recheckMcpAuthorization,
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
import { rememberSessionProfile } from "@/lib/session-profile-memory";
import type { SessionToolProfile } from "@/lib/tool-profile";
import { changedFileFromToolCall } from "@/lib/tool-summary";
import {
  enqueueAsk,
  formatVerdictNotice,
  isChildAsk,
  resolveAsk,
  retractAsk,
  retractedAskNotice,
} from "../approval-queue";
import { refreshSlashCommands } from "../composer-capabilities";
import {
  debugAskRequest,
  debugAskResolvedNotice,
  withoutSyntheticAsks,
} from "../debug-ask";
import {
  applyDelegationEvent,
  type DelegationFleet,
  emptyFleet,
  isDelegationEvent,
  markCancelling,
  markCardCancelling,
  reduceDelegationFleet,
} from "../delegation-fleet";
import { deliveryMessage, hasDeliveryNote } from "../delivery-message";
import { attachHookToMessage, reduceHookEvent } from "../hook-notice";
import {
  AUTHORIZATION_CHECK_IN_FLIGHT_NOTICE,
  AUTHORIZATION_LINK_COPIED_NOTICE,
  AUTHORIZATION_POLL_INTERVAL_MS,
  AUTHORIZATION_POPUP_BLOCKED_NOTICE,
  AUTHORIZATION_STILL_PENDING_NOTICE,
  authorizationRequiredNotice,
  authorizationResolvedNotice,
  openAuthorizationWindow,
  reduceAuthorizationEvent,
} from "../mcp-authorization-phase";
import { authFailureMessage } from "../offline-cause";
import {
  isPlanApprovalVerdict,
  isPlanAsk,
  PLAN_APPROVED_PROCEED_TEXT,
  shouldAutoProceed,
} from "../plan-ask";
import { buildPromptPayload } from "../prompt-payload";
import { emitRunFinished } from "../run-signals";
import { useRuntimeStatus } from "../runtime-status";
import type { SessionTitleUpdate } from "../session-title";
import { resolveSteerSupported } from "../steer-support";
import {
  appendSteerTrace,
  type SteerTraceDecision,
  type SteerTraceEntry,
  traceDrainedSteers,
} from "../steer-trace";
import {
  type StatusMessage,
  statusFromStopReason,
  stopReasonLabel,
} from "../stop-reason";
import { accumulateTurnStats } from "../turn-stats";
import type {
  AgentMessage,
  ApprovalChoice,
  ApprovalRequest,
  Attachment,
  AuthorizationRequest,
  ClarificationRequest,
  RetryDisposition,
  SteerEchoPart,
  StreamEvent,
  StreamProgress,
  ToolCallInfo,
} from "../types";
import {
  AUTO_RETRY_NOTICE,
  type FailedRehydrate,
  failedRehydrateState,
  isPermanentFailure,
  promptBelongsTo,
  REHYDRATED_FAILURE_ERROR,
  RETRY_INELIGIBLE_NO_PROMPT,
  recoverableDraft,
  retryFailureExplanation,
  retryRoute,
  retryStartFailure,
  type ScopedPrompt,
  shouldAutoRetry,
  trimFailedExchange,
  unmarkFailedRehydrate,
} from "./failed-step-retry";
import { stampTrailingAssistantStop } from "./stop-stamp";

type ChatStatus =
  | "idle"
  | "streaming"
  | "waiting_approval"
  /** The run is parked on an MCP browser sign-in (authorization.required). */
  | "waiting_authorization"
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

/** Why the queue is held after a non-clean stop (the TUI's "paused: …"). */
export type QueuePauseCause = "cancelled" | "error" | "transport";

/** The queue is held: nothing drains until the user resumes or clears it. */
export interface QueuePause {
  /** Plain-language reason shown in the strip ("paused: <reason>"). */
  reason: string;
}

/** Maps a non-clean stop to the reason the paused strip shows. */
export function pauseReasonFor(cause: QueuePauseCause): string {
  switch (cause) {
    case "cancelled":
      return "cancelled";
    case "error":
      return "the last turn failed";
    case "transport":
      return "connection lost";
  }
}

/**
 * Merges held rows (queued messages, retracted steers) into ONE prompt: texts
 * joined by a blank line, files concatenated in order. Null when there is
 * nothing to merge.
 */
export function mergeQueued(
  rows: readonly { text: string; files?: File[] }[],
): { text: string; files?: File[] } | null {
  if (rows.length === 0) return null;
  const text = rows
    .map((row) => row.text)
    .filter(Boolean)
    .join("\n\n");
  const files = rows.flatMap((row) => row.files ?? []);
  return { text, files: files.length > 0 ? files : undefined };
}

/**
 * The queue drain gate, extracted pure: only a clean, connected idle with
 * something queued, no pending steer (they requeue first), no in-flight flush
 * and NO pause sends the next message.
 */
export function shouldDrainQueue(input: {
  status: string;
  connected: boolean;
  queued: number;
  pendingSteers: number;
  paused: boolean;
  flushing: boolean;
}): boolean {
  return (
    input.status === "idle" &&
    input.connected &&
    input.queued > 0 &&
    input.pendingSteers === 0 &&
    !input.paused &&
    !input.flushing
  );
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
      const message = deliveryMessage(
        `history-user-${sequence}`,
        entry.text,
        0,
      );
      // A scheduled task's delivery note renders as a card, once per fire +
      // kind (the daemon documents a bounded double-record of one note).
      if (message.delivery && hasDeliveryNote(messages, message.delivery)) {
        continue;
      }
      messages.push(message);
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
              rawArgs: call.args || undefined,
              file: fileFromToolCall(call.name, call.args),
              changedPath: changedFileFromToolCall(call.name, call.args),
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
        call.parts = result.parts;
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
 * The transcript-side delegation applier — start cards, live progress, child
 * terminals, and the Parallel/Team group headers — lives in
 * `../delegation-fleet` (shared with the session-scoped fleet reducer) and
 * is re-exported here for the hook's callers and tests.
 */
export { applyDelegationEvent };

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
      // The durable record of what the user asked — or a scheduled task's
      // delivery note, which renders as a card once per fire + kind even
      // when replay and live both carry it.
      if (!event.text) return messages;
      const message = deliveryMessage(nextId(), event.text, Date.now());
      if (message.delivery && hasDeliveryNote(messages, message.delivery)) {
        return messages;
      }
      return [...messages, message];
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
          steered: true,
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
            rawArgs: event.rawArgs,
            file: event.file,
            changedPath: event.changedPath,
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
                  parts: event.parts,
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
    case "approval_verdict":
      // The verdict half of a permission ask, rendered as a quiet one-liner —
      // the SAME line respondToApproval records locally, so a rebuilt
      // transcript reads exactly as the live one did.
      return onAssistant((message) => ({
        ...message,
        notices: [
          ...(message.notices ?? []),
          formatVerdictNotice(event.toolName, event.verdict),
        ],
      }));
    case "hook":
      // A hook fire lands on the tool call it names (the row's chip) or,
      // call-less, as a marked notice on the trailing assistant message.
      return reduceHookEvent(messages, event, onAssistant);
    case "notice":
      return onAssistant((message) => ({
        ...message,
        notices: [...(message.notices ?? []), event.text],
      }));
    case "provider_route":
      // The turn's downstream provider stamps the trailing assistant bubble
      // (a LIVE watch of a run driven elsewhere); the daemon never logs it,
      // so a replay never carries this frame.
      return onAssistant((message) => ({ ...message, route: event.label }));
    case "authorization":
      // The parked-on-sign-in phase leaves its trace on the turn; the phase
      // itself is hook state (the takeover card), like an approval ask.
      return onAssistant((message) => ({
        ...message,
        notices: [
          ...(message.notices ?? []),
          authorizationRequiredNotice(event.displayName),
        ],
      }));
    case "authorization_resolved":
      return onAssistant((message) => ({
        ...message,
        notices: [
          ...(message.notices ?? []),
          authorizationResolvedNotice(event.displayName, event.status),
        ],
      }));
    case "delegation":
    case "delegation_progress":
    case "delegation_end":
    case "parallel_start":
    case "parallel_end":
    case "team_member":
    case "team_tasks":
    case "team_findings":
    case "team_end":
      // Every delegation frame lands on the turn that owns its tool call
      // (a background child's end can arrive after a later turn opened);
      // a start with no owning turn opens a fresh bubble like other activity.
      return applyDelegationEvent(messages, event, {
        openAssistant: () => ({
          id: nextId(),
          role: "assistant",
          content: "",
          timestamp: Date.now(),
        }),
      });
    case "turn_end":
      // One model exchange closed: its cost folds onto the trailing
      // assistant bubble's stat line (replay keeps the stats too).
      return onAssistant((message) => ({
        ...message,
        turnStats: accumulateTurnStats(message.turnStats, event),
      }));
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
          failurePermanent: isPermanentFailure(event),
          toolCalls: (message.toolCalls ?? []).map((call) =>
            call.status === "running"
              ? { ...call, status: "failed" as const }
              : call,
          ),
        }));
      }
      // A non-error stop worth naming (turn limit, budget, cancelled, …)
      // stamps the chip so a stopped turn never reads as a quiet success; a
      // run that streamed no deltas still carries its final text here.
      const stopLabel = stopReasonLabel(event.stop);
      const backfill =
        event.text && last?.role === "assistant" && !last.content
          ? event.text
          : "";
      if (!stopLabel && !backfill) return messages;
      return onAssistant((message) => ({
        ...message,
        content: message.content || backfill,
        ...(stopLabel ? { stopReason: event.stop } : {}),
      }));
    }
    default:
      // Approval asks, retractions, usage, and the transient status line are
      // hook state, not transcript (a replay must not resurrect a status).
      return messages;
  }
}

/**
 * An MCP authorization presentation/control refusal in the daemon's own
 * words, plus the way out when the authorization itself is gone daemon-side
 * (the four 404 reasons: expired, no longer pending, unclaimable, no match).
 */
function authorizationFailure(caught: unknown): string {
  const message = caught instanceof Error ? caught.message : String(caught);
  if (caught instanceof HarnessApiError && caught.status === 404) {
    return `${message}. Re-check to see where the run stands.`;
  }
  return message;
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
    /** The composer's pending reasoning-effort tier ("" = the operator's
     *  default, field omitted); read at mint time like createMode. Rides
     *  the create independently of the model pick — an auto-routed session
     *  can still ask for a tier. */
    createEffort?: () => string;
    /** The composer's pending tool profile ("" = the deployment default,
     *  field omitted; "no-fs" = the file-less catalog, ADR 0291); read at
     *  mint time like createMode. Fixed at create, so the choice is also
     *  REMEMBERED browser-locally for the live chat's display-only line. */
    createProfile?: () => SessionToolProfile;
    /**
     * The session's daemon lifecycle state from the inventory poll
     * (idle/running/awaiting/…). Reactive — when it reads running/awaiting
     * and the daemon supports `watch_session_events`, the hook attaches a
     * durable watch (ADR 0250) to render the externally-driven run live
     * instead of a frozen "running" badge.
     */
    sessionState?: string;
    /**
     * Fired at every run terminal this hook observes (the live prompt stream
     * and the durable watch) with the daemon's `stop`. The host re-reads
     * daemon-owned per-session state the run may have changed — the
     * permission mode, which a plan-run terminal flips on its own.
     */
    onRunEnded?: (stop: string) => void;
    /**
     * A `session.title` event (the daemon's first-prompt seed, an auto-title,
     * a rename from another client) heard on the prompt stream or a watch.
     * Session metadata for the host's inventory row — never a bubble.
     */
    onTitle?: (sessionId: string, title: SessionTitleUpdate) => void;
    /**
     * The always-armed metadata watch heard a LIVE run-bearing frame on an
     * idle chat this tab is not driving: a run started elsewhere. The host
     * re-walks the inventory so the row flips to running and the full watch
     * attaches. Fired once per run id.
     */
    onExternalRunDetected?: (sessionId: string) => void;
  },
) {
  const {
    connected,
    features,
    serverCapabilities,
    refresh: refreshRuntime,
  } = useRuntimeStatus();
  const [messages, setMessages] = useState<AgentMessage[]>([]);
  const [status, setStatus] = useState<ChatStatus>("idle");
  const [error, setError] = useState<string | null>(null);
  /** The transient status line (a no-progress nudge, the recover notice, how
   *  the last run stopped): cleared by the next run and on chat open. */
  const [statusMessage, setStatusMessage] = useState<StatusMessage | null>(
    null,
  );
  /** Permission asks in arrival order (FIFO): the head is the one on screen,
   *  later asks wait behind it (the panel's "1 of N"). Mirrored on a ref
   *  because the stream handlers fold asks in synchronously and must read
   *  the CURRENT queue (dedupe, advance-to-next) ahead of React's commit. */
  const [approvalQueue, setApprovalQueue] = useState<ApprovalRequest[]>([]);
  const approvalQueueRef = useRef<ApprovalRequest[]>([]);
  const pendingApproval = approvalQueue[0] ?? null;
  const replaceApprovalQueue = useCallback((next: ApprovalRequest[]) => {
    approvalQueueRef.current = next;
    setApprovalQueue(next);
  }, []);
  /** Lands a permission-ask notice (a verdict, a withdrawn child ask) on the
   *  trailing assistant bubble — the turn the ask interrupted. */
  const appendApprovalNotice = useCallback((text: string) => {
    setMessages((prev) =>
      reduceWatchEvent(
        prev,
        { type: "notice", text },
        () => `notice-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      ),
    );
  }, []);
  const [pendingClarification] = useState<ClarificationRequest | null>(null);
  /** The MCP browser authorization the run is parked on, if any. Mirrored on
   *  a ref because the stream handlers fold events into it synchronously. */
  const [pendingAuthorization, setPendingAuthorization] =
    useState<AuthorizationRequest | null>(null);
  const pendingAuthorizationRef = useRef<AuthorizationRequest | null>(null);
  // The assistant-bubble ids of the turn that parked: the continuation run
  // (recheck/cancel) renders into it. Null when the phase was found by a
  // watch replay and no live bubble is known.
  const authorizationIdsRef = useRef<{ assistant: string } | null>(null);
  // One authorization control in flight at a time.
  const authorizationBusyRef = useRef(false);
  // The control in flight — its abort, whether a cancel pre-empted it, and a
  // promise that settles once it has released the busy flag — so Cancel can
  // take over from a 3 s poll tick instead of being dropped by the guard.
  const authorizationControlRef = useRef<{
    controller: AbortController;
    preempted: boolean;
    settled: Promise<void>;
  } | null>(null);
  /** Steers the daemon accepted but has not yet drained into the run, in send
   *  order. The stream's `steer` echo splits this list on its watermark id. */
  const [pendingSteers, setPendingSteers] = useState<PendingSteer[]>([]);
  const steerSerialRef = useRef(0);
  /** The steer correlation trace (steer-trace.ts): every steer id with the
   *  daemon's decision and the drain watermark, bounded. Always recorded;
   *  the strip renders it only while Settings → Labs "Developer tools" is on
   *  (the TUI's DebugSteer). */
  const [steerTrace, setSteerTrace] = useState<SteerTraceEntry[]>([]);
  /** Developer tools: the status a FAKE ask (debug-ask.ts) displaced, put
   *  back when it resolves; null while none is parked. */
  const debugAskStatusRef = useRef<ChatStatus | null>(null);
  /** Rotates the fake ask's canned payload and salts its id. */
  const debugAskCycleRef = useRef(0);
  /** Set by every non-clean stop (cancel, failed turn, lost connection): the
   *  queue then waits visibly until the user resumes, edits, or clears it —
   *  or sends a fresh prompt. Cleared automatically once nothing is held. */
  const [queuePaused, setQueuePaused] = useState<QueuePause | null>(null);
  const [usage, setUsage] = useState({
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    reasoningTokens: 0,
    estimatedCost: null as number | null,
  });
  // Every child this session's runs delegated (subagents, parallel groups,
  // teams), aggregated across turns for the Agents panel. Per-visit like
  // `usage`: the transcript rehydrate carries no delegation data, so it holds
  // what this tab observed live or via the durable watch's replay.
  const [fleet, setFleet] = useState<DelegationFleet>(emptyFleet);
  // The latest turn's input tokens (turn.end) — the context occupancy the
  // meter reads. ASSIGNED per turn, never summed; 0 until a turn ends.
  const [contextOccupancy, setContextOccupancy] = useState(0);
  // The GET-session detail: the resolved model + context window the meter
  // is measured against, and the daemon's durable cumulative token usage.
  const [sessionDetail, setSessionDetail] =
    useState<HarnessSessionDetail | null>(null);
  // What this chat may send as media (the TUI's `attach:` gate): the
  // session's resolved modalities once the detail lands, the deployment's
  // compatibility echo before that or for a draft. A ref so the send/steer
  // closures read the CURRENT gate without rebuilding on every detail load.
  const mediaCapabilitiesRef = useRef(resolveMediaCapabilities(null, {}));
  mediaCapabilitiesRef.current = resolveMediaCapabilities(
    sessionDetail?.sessionCapabilities,
    serverCapabilities,
  );
  // Whether that detail read is in flight, landed, or failed — so the status
  // strip says "resolving model…" only while a read is genuinely pending and
  // falls back honestly against an older daemon or a failed fetch.
  const [sessionDetailStatus, setSessionDetailStatus] = useState<
    "loading" | "ok" | "failed"
  >("loading");
  // The downstream provider the current/last turn was routed to
  // (provider.route, ADR 0210): the strip's `model/route` suffix. Cleared on
  // send and on chat open; absent on a cache hit (never fabricated).
  const [providerRoute, setProviderRoute] = useState("");

  // The daemon session backing this chat: the route id, or the one minted for
  // a draft on first send. A ref so an in-flight stream keeps its binding
  // while the parent navigates to the new id.
  const daemonIdRef = useRef<string | null>(sessionId);
  const abortRef = useRef<AbortController | null>(null);
  // The prompt this tab last sent — text AND files — scoped to the chat and
  // the send it belongs to, cleared once a resend or a composer recovery
  // consumed it: a stale prompt never replays across chats or runs.
  const lastPromptRef = useRef<ScopedPrompt | null>(null);
  const promptSerialRef = useRef(0);
  // The last failed terminal's typed disposition (ADR 0239), captured from
  // run_result frames: "retryable" routes retryLast through the retry
  // endpoint instead of re-sending the prompt. Cleared when a run starts.
  const lastDispositionRef = useRef<RetryDisposition | undefined>(undefined);
  // How far that failed step's stream got (ADR 0239), captured with the
  // disposition: retryable + precommit is the ONE shape the hook retries on
  // its own, and a RETRY that ends precommit is explained as such.
  const lastStreamProgressRef = useRef<StreamProgress | undefined>(undefined);
  const lastPermanentRef = useRef(false);
  // The single automatic retry a prompt gets has been spent (re-armed by the
  // next prompt this tab sends, never by a retry — it cannot re-arm itself).
  const autoRetriedRef = useRef(false);
  // Runs this tab drove since the chat opened (prompt or retry): the
  // failed-rehydrate marking below applies only while none has.
  const localRunsRef = useRef(0);
  // The latest authoritative transcript rebuild, and the failed-rehydrate
  // marking applied to it (once per rebuild).
  const rehydratedRef = useRef<{
    id: string;
    messages: AgentMessage[];
  } | null>(null);
  const [rehydrateSerial, setRehydrateSerial] = useState(0);
  const failedMarkRef = useRef<{
    rebuilt: { id: string; messages: AgentMessage[] };
    mark: FailedRehydrate;
  } | null>(null);
  // The daemon typed the last failure PERMANENT: the strip withholds Retry.
  const [lastFailurePermanent, setLastFailurePermanent] = useState(false);
  // A text-only prompt a transport fault dropped, handed back to the
  // composer for edit-before-resend (the TUI's recoverPrompt).
  const [recoverDraft, setRecoverDraft] = useState<{ text: string } | null>(
    null,
  );
  // The prompt a run-entry failure refused before any frame arrived (the
  // session mint or POST /prompt itself — a 409 lease, a 503 drain, a 401):
  // the strip's Edit hands it back to the composer and drops the failed
  // exchange. Text only; attachments are never re-staged implicitly (the
  // Retry resend still carries them). Cleared by the next send and on chat
  // open, so it never names a prompt from another chat or run.
  const [failedPrompt, setFailedPrompt] = useState<string | null>(null);
  // The automatic retry armed by a retryable + precommit failure; fired by
  // an effect once the error state has committed (retryLast reads status).
  const [autoRetryPending, setAutoRetryPending] = useState(false);
  // The plan-approved auto-proceed (TUI parity): ARMED when this tab approves
  // a PresentPlan ask (session + ask id, so a watcher that merely sees the
  // terminal never fires it), set PENDING by that run's plan_approved
  // terminal, and fired by an effect once the run has settled to idle — the
  // proceed prompt is an ordinary startRun, which reads `status`.
  const planProceedArmRef = useRef<{
    sessionId: string;
    askId: string;
  } | null>(null);
  const [planProceedPending, setPlanProceedPending] = useState(false);
  // Set by that effect right before it calls retryLast, so the retried turn
  // can say it was automatic (the strip's onClick calls in with a MouseEvent,
  // so retryLast takes no parameter).
  const autoRetryModeRef = useRef(false);
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
  // The same fact as state, for the workspace's leave guard: closing the tab
  // ends a run THIS tab drives (`POST /prompt` cancels on disconnect); a
  // watched run driven elsewhere survives it and never sets this.
  const [drivingRun, setDrivingRun] = useState(false);
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
  const sentAttachmentsRef = useRef(new Map<string, SentAttachmentRecord[]>());

  const onSessionCreatedRef = useRef(options?.onSessionCreated);
  onSessionCreatedRef.current = options?.onSessionCreated;
  const createModeRef = useRef(options?.createMode);
  createModeRef.current = options?.createMode;
  const createModelRef = useRef(options?.createModel);
  createModelRef.current = options?.createModel;
  const createEffortRef = useRef(options?.createEffort);
  createEffortRef.current = options?.createEffort;
  const createProfileRef = useRef(options?.createProfile);
  createProfileRef.current = options?.createProfile;
  const onRunEndedRef = useRef(options?.onRunEnded);
  onRunEndedRef.current = options?.onRunEnded;
  const onTitleRef = useRef(options?.onTitle);
  onTitleRef.current = options?.onTitle;
  const onExternalRunDetectedRef = useRef(options?.onExternalRunDetected);
  onExternalRunDetectedRef.current = options?.onExternalRunDetected;

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
          // A text attachment was inlined into the recorded prompt; show the
          // typed text with its chip, not the delimited blocks.
          const display = pool[index].display;
          if (display !== undefined) message.content = display;
          pool.splice(index, 1);
        }
      }
    }
    setMessages(rebuilt);
    // The failed-rehydrate effect below marks THIS rebuild (once) when the
    // inventory says the session is failed.
    rehydratedRef.current = { id, messages: rebuilt };
    setRehydrateSerial((serial) => serial + 1);
  }, []);

  /**
   * Reads the GET-session detail: the resolved model + context window the
   * meter is measured against, and the daemon's durable cumulative token
   * usage, which replaces this visit's client-side sum when present (an
   * older daemon reports none, so the sum stays).
   */
  const loadSessionDetail = useCallback(
    async (id: string, signal?: AbortSignal) => {
      const detail = await fetchHarnessSessionDetail(id, signal);
      if (signal?.aborted) return;
      setSessionDetail(detail);
      setSessionDetailStatus("ok");
      if (detail.tokenUsage) {
        setUsage({ ...detail.tokenUsage, estimatedCost: null });
      }
    },
    [],
  );

  /**
   * The plan-review half of a run terminal, shared by the live stream and
   * the durable watch: tells the host the run ended (mode pill refresh), and
   * turns an arm this tab set by approving a PresentPlan ask into the
   * pending auto-proceed when the run ended on `plan_approved`. Whatever the
   * stop, the arm is spent — one approval answers exactly one terminal.
   */
  const settlePlanTerminal = useCallback(
    (daemonId: string, stop: string | undefined) => {
      onRunEndedRef.current?.(stop ?? "");
      // The page-wide run-terminal signal (mecatui re-lists learned-skill
      // receipts on every ResultMsg): fired here so BOTH live arms — the
      // prompt stream and the durable watch — announce, and replay never does.
      emitRunFinished({ sessionId: daemonId, stop: stop ?? "" });
      const arm = planProceedArmRef.current;
      planProceedArmRef.current = null;
      if (
        shouldAutoProceed({
          stop,
          armed: arm !== null && arm.sessionId === daemonId,
          queueLength: approvalQueueRef.current.length,
        })
      ) {
        setPlanProceedPending(true);
      }
    },
    [],
  );

  // Opening a chat (or switching) reads its detail. A failure just leaves
  // the meter on the visit-local figures — it is never load-bearing.
  useEffect(() => {
    setSessionDetail(null);
    setContextOccupancy(0);
    setProviderRoute("");
    // A draft has nothing to resolve yet: settled, not "resolving".
    setSessionDetailStatus(sessionId ? "loading" : "ok");
    if (!sessionId || !connected) return;
    const controller = new AbortController();
    void loadSessionDetail(sessionId, controller.signal).catch(() => {
      // Never load-bearing — but the strip must stop saying "resolving".
      if (!controller.signal.aborted) setSessionDetailStatus("failed");
    });
    return () => controller.abort();
  }, [sessionId, connected, loadSessionDetail]);

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
    // Failed-step retry state is per chat: a prompt, disposition, or
    // permanent verdict from the previous chat must never carry over.
    lastPromptRef.current = null;
    lastStreamProgressRef.current = undefined;
    lastPermanentRef.current = false;
    autoRetriedRef.current = false;
    localRunsRef.current = 0;
    rehydratedRef.current = null;
    failedMarkRef.current = null;
    setLastFailurePermanent(false);
    setRecoverDraft(null);
    setFailedPrompt(null);
    setAutoRetryPending(false);
    planProceedArmRef.current = null;
    setPlanProceedPending(false);
    setMessages([]);
    setFleet(emptyFleet());
    replaceApprovalQueue([]);
    setPendingAuthorization(null);
    pendingAuthorizationRef.current = null;
    authorizationIdsRef.current = null;
    setError(null);
    setStatusMessage(null);
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

  // Durable failed-step retry (the TUI's "/retry is available for every
  // bound idle session"): a chat whose inventory state reads `failed` gets its
  // trailing turn marked failed and the error strip — with Retry — back, even
  // though the transcript carries no error text and no disposition (Retry
  // then asks the daemon first, which is authoritative). Applied once per
  // transcript rebuild, whichever of the rebuild and the poll's `failed`
  // lands second, and never once a run this tab drove has replaced the
  // rebuilt view (a stale `failed` poll after a successful local retry must
  // not re-mark the fresh turn). Leaving `failed` with no local run (another
  // client moved the session on) reverses the marking.
  // biome-ignore lint/correctness/useExhaustiveDependencies: rehydrateSerial re-runs the check after each transcript rebuild (the ref holds the rebuild)
  useEffect(() => {
    if (!sessionId) return;
    const rebuilt = rehydratedRef.current;
    if (!rebuilt || rebuilt.id !== sessionId) return;
    if (localRunsRef.current > 0) return;
    if (sessionState === "failed") {
      if (failedMarkRef.current?.rebuilt === rebuilt) return;
      const mark = failedRehydrateState(rebuilt.messages);
      if (!mark) return;
      failedMarkRef.current = { rebuilt, mark };
      setMessages(mark.messages);
      lastPromptRef.current = mark.lastPrompt
        ? {
            sessionId,
            serial: ++promptSerialRef.current,
            text: mark.lastPrompt,
          }
        : null;
      // Unknown disposition: retryLast routes through the daemon's retry
      // endpoint, whose 409 falls back to re-sending the prompt above.
      lastDispositionRef.current = undefined;
      lastStreamProgressRef.current = undefined;
      lastPermanentRef.current = false;
      setLastFailurePermanent(false);
      setError(REHYDRATED_FAILURE_ERROR);
      setStatus("error");
      return;
    }
    const marked = failedMarkRef.current;
    if (!marked || marked.rebuilt !== rebuilt || !sessionState) return;
    failedMarkRef.current = null;
    lastPromptRef.current = null;
    setMessages((prev) => unmarkFailedRehydrate(prev, marked.mark));
    setError(null);
    setStatus((current) => (current === "error" ? "idle" : current));
  }, [sessionId, sessionState, rehydrateSerial]);

  // A rehydrated chat whose inventory state reads `cancelled` (stopped from
  // another client, or from this tab before a reload): the transcript
  // endpoint carries no stop reason, so the last turn gets its `cancelled`
  // chip here — once per rebuild, and never once a run this tab drove has
  // replaced the rebuilt view (a stale `cancelled` poll after a local run
  // must not mark the fresh turn).
  const cancelledMarkRef = useRef<object | null>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: rehydrateSerial re-runs the check after each transcript rebuild (the ref holds the rebuild)
  useEffect(() => {
    if (!sessionId || sessionState !== "cancelled") return;
    const rebuilt = rehydratedRef.current;
    if (!rebuilt || rebuilt.id !== sessionId) return;
    if (localRunsRef.current > 0) return;
    if (cancelledMarkRef.current === rebuilt) return;
    cancelledMarkRef.current = rebuilt;
    setMessages((prev) =>
      stampTrailingAssistantStop(
        prev,
        "cancelled",
        () => "history-assistant-cancelled",
        0,
      ),
    );
  }, [sessionId, sessionState, rehydrateSerial]);

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
    // Asks seen in replay that no later verdict/retract resolved, in arrival
    // order: surfaced at the boundary — exactly the parked-approval case
    // (state "awaiting"; a parent's and a surfaced child's ask can both park).
    let parkedAsks: ApprovalRequest[] = [];
    // Likewise a browser authorization seen in replay that nothing resolved:
    // the run is parked on it, so the takeover card shows at the boundary.
    let parkedAuthorization: AuthorizationRequest | null = null;
    const surfaceAuthorization = (request: AuthorizationRequest) => {
      // The continuation renders into the parked turn's bubble when the
      // rebuild has one; otherwise the recheck opens a fresh bubble.
      const lastAssistant = [...rebuilt]
        .reverse()
        .find((message) => message.role === "assistant");
      authorizationIdsRef.current = lastAssistant
        ? { assistant: lastAssistant.id }
        : null;
      pendingAuthorizationRef.current = request;
      setPendingAuthorization(request);
      setStatus("waiting_authorization");
    };
    const nextId = () => {
      serial += 1;
      return `watch-${serial}`;
    };
    // The delegation fleet rebuilt from the same replay (the replay spans the
    // whole session, so it is the authoritative fleet for it).
    let rebuiltFleet = emptyFleet();
    const flush = () => {
      setMessages(rebuilt);
      setFleet(rebuiltFleet);
    };
    /** Shows the parked queue; the run streams again once it is empty. */
    const showParkedAsks = () => {
      replaceApprovalQueue(parkedAsks);
      if (parkedAsks.length) {
        setStatus("waiting_approval");
      } else {
        setStatus((current) =>
          current === "waiting_approval" ? "streaming" : current,
        );
      }
    };
    /** A verdict settled the ask (here or on another client): drop it, and
     *  the next queued ask — if any — takes the screen. */
    const settleAsk = (approvalId: string) => {
      parkedAsks = resolveAsk(parkedAsks, approvalId);
      if (live) showParkedAsks();
    };

    setStatus("streaming");
    replaceApprovalQueue([]);
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
            if (parkedAsks.length) showParkedAsks();
            if (parkedAuthorization) surfaceAuthorization(parkedAuthorization);
          }
          return;
        }
        // The LATEST run-bearing event names the current run (a replay spans
        // every earlier run of the session too).
        if (event.runId) runIdRef.current = event.runId;
        switch (event.type) {
          case "approval":
            // A known askId is a re-surface, not a second ask; a new one
            // queues behind whatever is already parked (FIFO).
            parkedAsks = enqueueAsk(parkedAsks, {
              approvalId: event.approvalId,
              sessionId,
              toolName: event.toolName,
              description: event.description,
              details: event.details,
              reason: event.reason,
              args: event.args,
              // Against the DAEMON session id: a child's ask is prefixed with
              // the CHILD session id, never this one.
              child: isChildAsk(event.approvalId, sessionId),
            });
            if (live) showParkedAsks();
            break;
          case "retract": {
            // Withdrawn (its child was cancelled); the run is still going. A
            // vanished child ask leaves a notice on the turn — a silent
            // disappearance reads as a glitch.
            const retracted = retractAsk(parkedAsks, event.approvalId);
            parkedAsks = retracted.queue;
            if (retracted.removed?.child) {
              rebuilt = reduceWatchEvent(
                rebuilt,
                { type: "notice", text: retractedAskNotice(retracted.wasHead) },
                nextId,
              );
              if (live) flush();
            }
            if (live) showParkedAsks();
            break;
          }
          case "authorization":
          case "authorization_resolved": {
            // The parked-on-sign-in phase: a pending `authorization` with no
            // later resolved is what "parked" looks like in the log.
            parkedAuthorization = reduceAuthorizationEvent(
              parkedAuthorization,
              event,
              sessionId,
            );
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            if (live) {
              flush();
              if (parkedAuthorization) {
                surfaceAuthorization(parkedAuthorization);
              } else if (pendingAuthorizationRef.current) {
                pendingAuthorizationRef.current = null;
                setPendingAuthorization(null);
                setStatus((current) =>
                  current === "waiting_authorization" ? "streaming" : current,
                );
              }
            }
            break;
          }
          case "approval_verdict":
            // Another client resolved the ask; the quiet verdict line also
            // lands in the transcript via the reducer.
            settleAsk(event.approvalId);
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            if (live) flush();
            break;
          case "turn_end":
            // The latest turn's input is the context occupancy — replay's
            // LAST turn_end sets it too, so a reload never falls back to the
            // summed-usage numerator; the stats land on the bubble either way.
            setContextOccupancy(event.inputTokens);
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            frames += 1;
            if (live || frames % 200 === 0) flush();
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
          case "provider_route":
            // The replay spans earlier runs too: only the LIVE route counts
            // for the strip; the bubble marker is per-turn either way.
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            if (live) {
              setProviderRoute(event.label);
              flush();
            }
            break;
          case "status":
            // Transient: the status line only, never the transcript.
            if (live) {
              setStatusMessage({
                text: event.text,
                tone: event.tone,
                kind: event.kind,
              });
            }
            break;
          case "title":
            // Session metadata, not transcript: the host adopts it onto the
            // inventory row (header, sidebar, tab title) in BOTH phases —
            // its revision guard keeps a replayed older title from
            // regressing the row, so replay is safe to forward.
            onTitleRef.current?.(sessionId, {
              title: event.title,
              provenance: event.provenance,
              revision: event.revision,
            });
            break;
          case "run_result":
            rebuilt = reduceWatchEvent(rebuilt, event, nextId);
            if (live) {
              // The terminal result ends the watch; the normal
              // completed-state flow takes over from here.
              flush();
              runIdRef.current = "";
              setStatusMessage(statusFromStopReason(event.stop));
              // An ask left over from the ended run is dead: never keep it.
              parkedAsks = [];
              replaceApprovalQueue([]);
              settlePlanTerminal(sessionId, event.stop);
              // The daemon's durable cumulative usage supersedes the sum.
              void loadSessionDetail(sessionId).catch(() => undefined);
              if (event.stop === "error") {
                lastDispositionRef.current = event.retryDisposition;
                lastStreamProgressRef.current = event.streamProgress;
                lastPermanentRef.current = isPermanentFailure(event);
                setLastFailurePermanent(lastPermanentRef.current);
                setError(
                  event.errorText || "The run failed without a specific error.",
                );
                setQueuePaused({ reason: pauseReasonFor("error") });
                setStatus("error");
              } else {
                setStatus("idle");
              }
              controller.abort();
            }
            break;
          default:
            if (isDelegationEvent(event)) {
              rebuiltFleet = reduceDelegationFleet(rebuiltFleet, event);
            }
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
      replaceApprovalQueue([]);
      // The pause lands BEFORE idle so the drain never fires into a run this
      // tab can no longer see.
      setQueuePaused({ reason: pauseReasonFor("transport") });
      setStatus("idle");
      void rehydrate(sessionId).catch(() => undefined);
    });

    return () => {
      controller.abort();
      if (watchAbortRef.current === controller) watchAbortRef.current = null;
      // A torn-down watch (chat switch, state flip, disconnect) must not
      // leave the streaming badge stuck; real terminals set their own state.
      setStatus((current) =>
        current === "streaming" ||
        current === "waiting_approval" ||
        current === "waiting_authorization"
          ? "idle"
          : current,
      );
    };
  }, [
    sessionId,
    connected,
    watchSupported,
    watchable,
    rehydrate,
    loadSessionDetail,
    replaceApprovalQueue,
    settlePlanTerminal,
  ]);

  // The always-armed METADATA watch (mecatui's armLiveFeed): an open chat
  // with no run to render still hears `session.title` (the first-prompt
  // seed, an auto-title, a rename from another client) and notices a run
  // started elsewhere. It attaches exactly when the full watch above does
  // NOT (the row reads idle/completed/… per inventory) and hands over the
  // moment the row flips to running/awaiting. The daemon has no "from now"
  // for a session with no run, so it replays from the beginning (or from the
  // last cursor this tab saw for the session) and SKIMS: nothing is
  // rendered, titles are forwarded in either phase behind the host's
  // revision guard, run detection fires on LIVE frames only — a replayed
  // history must never storm the inventory — and once per run id. Metadata
  // only: a fault ends it quietly until the next open / flip / reconnect,
  // except a refused resume cursor, which restarts once from the beginning.
  const metadataWatchRef = useRef<{ sessionId: string; cursor: string }>({
    sessionId: "",
    cursor: "",
  });
  useEffect(() => {
    if (!sessionId || !connected || !watchSupported || watchable) return;
    if (drivingRef.current === sessionId) return;
    const controller = new AbortController();
    const seenRuns = new Set<string>();
    // The full watch's last cursor is the newer position when it ran after
    // this watch (idle → running → idle); both are scoped to this session.
    const resume =
      watchCursorRef.current ||
      (metadataWatchRef.current.sessionId === sessionId
        ? metadataWatchRef.current.cursor
        : "");
    const attach = (cursor: string) =>
      watchSessionEvents(
        sessionId,
        (delivery) => {
          if (delivery.cursor) {
            metadataWatchRef.current = { sessionId, cursor: delivery.cursor };
          }
          const event = delivery.event;
          if (!event) return;
          if (event.type === "title") {
            onTitleRef.current?.(sessionId, {
              title: event.title,
              provenance: event.provenance,
              revision: event.revision,
            });
            return;
          }
          if (delivery.phase !== "live" || !event.runId) return;
          // This tab's own run fans out on the durable feed too.
          if (drivingRef.current === sessionId) return;
          if (seenRuns.has(event.runId)) return;
          seenRuns.add(event.runId);
          onExternalRunDetectedRef.current?.(sessionId);
        },
        { cursor, signal: controller.signal },
      );
    void Promise.resolve()
      .then(() => attach(resume))
      .catch(() => {
        if (controller.signal.aborted) return;
        metadataWatchRef.current = { sessionId: "", cursor: "" };
        if (resume) {
          void Promise.resolve()
            .then(() => attach(""))
            .catch(() => undefined);
        }
      });
    return () => controller.abort();
  }, [sessionId, connected, watchSupported, watchable]);

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
        if (isDelegationEvent(event)) {
          // The session-scoped fleet (Agents panel) folds every delegation
          // frame, independent of which bubble the card lands on.
          setFleet((prev) => reduceDelegationFleet(prev, event));
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
              rawArgs: event.rawArgs,
              file: event.file,
              changedPath: event.changedPath,
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
                      parts: event.parts,
                      isError: event.isError,
                      status: event.isError ? "failed" : "completed",
                    }
                  : call,
              ),
            }));
            break;
          case "approval":
            // FIFO: a second ask (a surfaced child's, a parallel read
            // batch's) queues behind the one on screen instead of replacing
            // it; a known askId is a re-surface, not a new ask.
            // A GENUINE ask displaces a parked developer-tools fake one
            // first (debug-ask.ts): a fake ask must never hide the
            // daemon's real ask behind it.
            replaceApprovalQueue(
              enqueueAsk(withoutSyntheticAsks(approvalQueueRef.current), {
                approvalId: event.approvalId,
                sessionId: daemonId,
                toolName: event.toolName,
                description: event.description,
                details: event.details,
                reason: event.reason,
                args: event.args,
                // Against the DAEMON session id (never Studio's route id):
                // a child's ask is prefixed with the CHILD session id.
                child: isChildAsk(event.approvalId, daemonId),
              }),
            );
            setStatus("waiting_approval");
            break;
          case "retract": {
            // The ask was withdrawn (its child was cancelled); the run is
            // still going. A vanished child ask leaves a notice on the turn
            // — a silent disappearance reads as a glitch. The next queued
            // ask, if any, takes the screen; streaming resumes only once
            // nothing is left.
            const retracted = retractAsk(
              approvalQueueRef.current,
              event.approvalId,
            );
            replaceApprovalQueue(retracted.queue);
            if (retracted.removed?.child) {
              patch((message) => ({
                ...message,
                notices: [
                  ...(message.notices ?? []),
                  retractedAskNotice(retracted.wasHead),
                ],
              }));
            }
            if (!retracted.queue.length) {
              setStatus((current) =>
                current === "waiting_approval" ? "streaming" : current,
              );
            }
            break;
          }
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
              // Developer-tools trace: the echo's watermark and what it
              // split off (steer-trace.ts).
              setSteerTrace((trace) =>
                traceDrainedSteers(trace, drained, event.messageId, Date.now()),
              );
              const drainedBubbleIds = new Set(
                drained.map((p) => `steer-user-${p.id}`),
              );
              setMessages((msgs) => {
                // The echo IS the "steer applied" moment: stamp the bubbles.
                const moved = msgs
                  .filter((m) => drainedBubbleIds.has(m.id))
                  .map((m) => ({ ...m, steered: true }));
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
                            steered: true,
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
          case "hook":
            // Attributed to the call on this turn's bubble (its chip), else
            // a marked notice; the call's status is left to its own result.
            patch((message) => attachHookToMessage(message, event));
            break;
          case "notice":
            patch((message) => ({
              ...message,
              notices: [...(message.notices ?? []), event.text],
            }));
            break;
          case "provider_route":
            // Metadata for the status strip's model segment and this turn's
            // bubble marker, not transcript text.
            setProviderRoute(event.label);
            patch((message) => ({ ...message, route: event.label }));
            break;
          case "status":
            // Transient (no-progress nudge, recover notice): the status line
            // under the transcript, never the bubble's durable notices.
            setStatusMessage({
              text: event.text,
              tone: event.tone,
              kind: event.kind,
            });
            break;
          case "authorization": {
            // The daemon parked the tool call on a browser sign-in and will
            // CLOSE this stream without a result; the run continues over the
            // authorization control stream (recheck/cancel). A re-observed
            // pending status (a recheck that found nothing new) refreshes
            // the request without repeating the notice.
            const previous = pendingAuthorizationRef.current;
            const next = reduceAuthorizationEvent(previous, event, daemonId);
            pendingAuthorizationRef.current = next;
            setPendingAuthorization(next);
            if (!next) break;
            // The continuation renders into the turn that parked.
            authorizationIdsRef.current = ids;
            setStatus("waiting_authorization");
            if (previous?.authorizationId !== next.authorizationId) {
              patch((message) => ({
                ...message,
                notices: [
                  ...(message.notices ?? []),
                  authorizationRequiredNotice(event.displayName),
                ],
              }));
            }
            break;
          }
          case "authorization_resolved": {
            const previous = pendingAuthorizationRef.current;
            const next = reduceAuthorizationEvent(previous, event, daemonId);
            if (next === previous) break;
            pendingAuthorizationRef.current = next;
            setPendingAuthorization(next);
            patch((message) => ({
              ...message,
              notices: [
                ...(message.notices ?? []),
                authorizationResolvedNotice(event.displayName, event.status),
              ],
            }));
            // Back to the run: granted resumes it, a terminal refusal records
            // the call's failure and the model carries on — either way the
            // stream's own result owns the eventual idle.
            setStatus((current) =>
              current === "waiting_authorization" ? "streaming" : current,
            );
            break;
          }
          case "delegation":
          case "delegation_progress":
          case "delegation_end":
          case "parallel_start":
          case "parallel_end":
          case "team_member":
          case "team_tasks":
          case "team_findings":
          case "team_end":
            // Delegation cards + group headers: a start lands on the turn
            // that owns its tool call (else this run's assistant bubble —
            // a pending steer bubble may trail it); progress/terminals find
            // their card wherever it sits, so a background child's end
            // after a later turn still resolves the right card.
            setMessages((prev) =>
              applyDelegationEvent(prev, event, { assistantId: ids.assistant }),
            );
            break;
          case "turn_end":
            // One model exchange closed: fold its cost onto the bubble's
            // stat line and take its input as the current context occupancy.
            patch((message) => ({
              ...message,
              turnStats: accumulateTurnStats(message.turnStats, event),
            }));
            setContextOccupancy(event.inputTokens);
            break;
          case "usage":
            // The daemon reports per-run figures; the chat total is their
            // sum until the run's terminal re-reads the session's durable
            // cumulative usage (loadSessionDetail), which supersedes it.
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
            // The run is over; a control scoped to it would be stale — and
            // so would an ask left over from it (the daemon retracts
            // pre-seal, but a dead ask must never stay on screen).
            runIdRef.current = "";
            replaceApprovalQueue([]);
            // The run ended while an ask was still on screen (another client
            // answered it — a plan approved from mecatui, say): the parked
            // status must not outlive the run, or the stream's end handler
            // keeps it and the chat reads "Plan review" forever. Back to
            // streaming; that handler then lands idle as for any clean end.
            setStatus((current) =>
              current === "waiting_approval" ? "streaming" : current,
            );
            settlePlanTerminal(daemonId, event.stop);
            // The daemon's durable cumulative usage supersedes the sum.
            void loadSessionDetail(daemonId).catch(() => undefined);
            // How the run ended, on the status line: a limit stop or cancel
            // is named; a clean end_turn (or the error bar's error) clears it.
            setStatusMessage(statusFromStopReason(event.stop));
            if (event.stop === "error") {
              // The typed disposition routes the Retry button (ADR 0239).
              lastDispositionRef.current = event.retryDisposition;
              // Stream progress decides the one automatic retry (retryable
              // + precommit) and explains a retry that never reached the
              // model; a PERMANENT verdict withholds Retry altogether.
              lastStreamProgressRef.current = event.streamProgress;
              const permanent = isPermanentFailure(event);
              lastPermanentRef.current = permanent;
              setLastFailurePermanent(permanent);
              const detail =
                event.errorText || "The run failed without a specific error.";
              patch((message) => ({
                ...message,
                failed: true,
                failureDetail: event.permanent
                  ? `${detail} (permanent — retrying the identical request cannot succeed)`
                  : detail,
                failurePermanent: permanent,
                toolCalls: (message.toolCalls ?? []).map((call) =>
                  call.status === "running"
                    ? { ...call, status: "failed" as const }
                    : call,
                ),
              }));
              setError(detail);
              setQueuePaused({ reason: pauseReasonFor("error") });
              setStatus("error");
            } else {
              // A run that produced no deltas (a rehydrated approve, a
              // recovered run) still carries its final text here; a non-error
              // stop worth naming stamps the turn's stop-reason chip.
              const stopLabel = stopReasonLabel(event.stop);
              if (event.text || stopLabel) {
                patch((message) => ({
                  ...message,
                  content: message.content || event.text,
                  ...(stopLabel ? { stopReason: event.stop } : {}),
                }));
              }
            }
            break;
          case "user_prompt": {
            // The HTTP live wire omits EvUserPrompt today (the durable log's
            // record of what was asked; the prompt this tab sent is already
            // on screen). Should it ever relay the gRPC exception — a
            // scheduled task's delivery note drained into THIS run at a turn
            // boundary — the note must render, never be dropped. Appended in
            // arrival order; a duplicate record renders once.
            const note = deliveryMessage(
              `delivery-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
              event.text,
              Date.now(),
            );
            const delivery = note.delivery;
            if (!delivery) break;
            setMessages((prev) =>
              hasDeliveryNote(prev, delivery) ? prev : [...prev, note],
            );
            break;
          }
          case "title":
            // The daemon's title lifecycle rides this tab's own stream too
            // (the first-prompt seed lands with the run): session metadata
            // for the host's inventory row, never this turn's bubble.
            onTitleRef.current?.(daemonId, {
              title: event.title,
              provenance: event.provenance,
              revision: event.revision,
            });
            break;
          default:
            break;
        }
      };
    },
    [loadSessionDetail, replaceApprovalQueue, settlePlanTerminal],
  );

  // The run starter proper. Internal callers (the queue drain, Retry's
  // resend) use it directly so a paused queue STAYS paused; the exported
  // `sendMessage` below is the composer's fresh-prompt path, which lifts the
  // pause (the TUI rule: sending a fresh prompt also resumes the queue).
  const startRun = useCallback(
    async (
      content: string,
      files?: File[],
      opts?: {
        /** A harness-authored turn (the plan-approved proceed prompt):
         *  recorded like any prompt, rendered as a harness note. */
        synthetic?: boolean;
      },
    ) => {
      if (!connected) return;
      if (
        status === "streaming" ||
        status === "waiting_approval" ||
        status === "waiting_authorization"
      ) {
        // Defense in depth behind the composer's own routing: text sent while
        // a run is live is HELD, never fired into the funnel (which would
        // refuse with "already has an active run" — or, parked on a browser
        // sign-in, 409 mcp_authorization_pending) and never dropped.
        queueMessage(content, files);
        return;
      }

      // Every staged file crosses (TUI parity): images and audio as media
      // parts, gated on the session's resolved modalities; anything else is
      // inlined into the prompt text as a delimited block. A refusal names
      // the file and abandons the send before anything reaches the daemon.
      // Media chips carry the SAME bytes the wire part holds (a data: URL)
      // and text chips the inlined content, so thumbnails and the canvas
      // preview work on the live message; rehydrated transcripts get them
      // back from the sent-attachments store.
      const payload = await buildPromptPayload({
        text: content,
        files: files ?? [],
        capabilities: mediaCapabilitiesRef.current,
        encodeImage: imageToPart,
      });
      if (!payload.ok) {
        setError(payload.error);
        return;
      }
      const { parts, attachments } = payload;
      // What the daemon records — the typed text plus the inlined files.
      const wireText = payload.text;

      const userMessage: AgentMessage = {
        id: `user-${Date.now()}`,
        role: "user",
        content,
        timestamp: Date.now(),
        attachments,
        ...(opts?.synthetic ? { synthetic: true } : {}),
      };

      setMessages((prev) => [...prev, userMessage]);
      setStatus("streaming");
      if (attachments && daemonIdRef.current) {
        const log = sentAttachmentsRef.current.get(daemonIdRef.current) ?? [];
        log.push({ content: wireText, attachments, display: content });
        sentAttachmentsRef.current.set(daemonIdRef.current, log);
        void saveSentAttachments(daemonIdRef.current, log);
      }
      setError(null);
      setStatusMessage(null);
      // The route belongs to a turn: a new prompt starts with none reported.
      setProviderRoute("");
      // Scoped to this chat (null for a draft minted on this very send) and
      // this send; files ride along so a resend never drops attachments.
      lastPromptRef.current = {
        sessionId: daemonIdRef.current,
        serial: ++promptSerialRef.current,
        text: content,
        files,
      };
      // A fresh prompt daemon-side: a fresh one-retry budget, a fresh
      // permanent verdict, nothing left to recover, and the rehydrated
      // failed marking (if any) no longer owns the view.
      autoRetriedRef.current = false;
      lastStreamProgressRef.current = undefined;
      lastPermanentRef.current = false;
      localRunsRef.current += 1;
      setLastFailurePermanent(false);
      setRecoverDraft(null);
      setFailedPrompt(null);

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
          const createEffort = createEffortRef.current?.() ?? "";
          const createProfile = createProfileRef.current?.() ?? "";
          daemonId = await createHarnessSession(
            encodeSessionPermissionMode(createModeRef.current?.() ?? "default"),
            {
              ...(createModel
                ? {
                    modelId: createModel.modelId,
                    providerId: createModel.providerId,
                  }
                : {}),
              ...(createEffort ? { reasoningEffort: createEffort } : {}),
              ...(createProfile ? { profile: createProfile } : {}),
            },
          );
          daemonIdRef.current = daemonId;
          // The profile is fixed at create and never reported back: remember
          // it BEFORE the host learns the id, so the live chat's Tools line
          // can read it the moment the mode hook re-keys onto the new id.
          rememberSessionProfile(daemonId, createProfile);
          void refreshSlashCommands(daemonId);
          // The pre-mint record above keyed nothing; re-key it now.
          if (attachments) {
            const log = sentAttachmentsRef.current.get(daemonId) ?? [];
            if (
              !log.some(
                (r) => r.content === wireText && r.attachments === attachments,
              )
            ) {
              log.push({ content: wireText, attachments, display: content });
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
        setDrivingRun(true);
        runIdRef.current = "";
        lastDispositionRef.current = undefined;
        await streamHarnessPrompt(
          daemonId,
          wireText,
          parts,
          makeStreamHandler(daemonId, ids),
          controller.signal,
          { onRunStarted: adoptRunId },
        );
        // The ONE automatic, prompt-free retry (TUI parity): the daemon typed
        // the failure retryable and the stream died before anything was
        // committed, so re-driving the step duplicates nothing. Armed here,
        // fired by the effect below once the error state has committed.
        if (
          shouldAutoRetry({
            disposition: lastDispositionRef.current,
            streamProgress: lastStreamProgressRef.current,
            alreadyRetried: autoRetriedRef.current,
            permanent: lastPermanentRef.current,
          })
        ) {
          autoRetriedRef.current = true;
          setAutoRetryPending(true);
        }
        // A parked approval keeps its own status: the stream ends while the
        // run is still waiting on the operator, and flipping to idle here
        // would hide the pending prompt. So does a parked browser
        // authorization (the stream ENDS on it). A failed turn keeps its
        // error state.
        setStatus((current) =>
          current === "waiting_approval" ||
          current === "waiting_authorization" ||
          current === "error"
            ? current
            : "idle",
        );
      } catch (caught) {
        if (controller.signal.aborted) {
          // A cancelled run holds the queue (never auto-fires the next
          // message): the pause lands with idle, in the same batch.
          setQueuePaused({ reason: pauseReasonFor("cancelled") });
          setStatus("idle");
          return;
        }
        // A refused credential (a 401, the proxy's oidc_* refusal) is named
        // by its cause class, and the runtime re-probes at once so the
        // auth-recovery banner appears without waiting for the poll.
        const authFailure = authFailureMessage(caught);
        if (authFailure) void refreshRuntime();
        // A prompt the SDK refused to build (a part the session's modalities
        // reject, media over its ceilings) gets the composer's own wording.
        const message =
          authFailure ??
          mapPromptValidationError(caught) ??
          (caught instanceof Error ? caught.message : String(caught));
        setError(message);
        setQueuePaused({ reason: pauseReasonFor("transport") });
        setStatus("error");
        // No terminal frame reached this tab, so nothing says whether the
        // daemon recorded the prompt: hand a text-only prompt back to the
        // composer for edit-before-resend (media is never replayed
        // implicitly — the Retry button's resend still carries it).
        const draft = recoverableDraft({ text: content, files });
        if (draft) setRecoverDraft(draft);
        // The strip's Edit can take the text back whether or not files rode
        // along (the composer recovery above is text-only by design).
        if (content.trim()) setFailedPrompt(content);
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
          setDrivingRun(false);
        }
      }
    },
    [
      status,
      connected,
      queueMessage,
      makeStreamHandler,
      adoptRunId,
      refreshRuntime,
    ],
  );

  /** The composer's send: a fresh user prompt also lifts a paused queue, so
   *  the held messages follow once this run ends cleanly. */
  const sendMessage = useCallback(
    async (content: string, files?: File[]) => {
      setQueuePaused(null);
      await startRun(content, files);
    },
    [startRun],
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

  /** Re-sends the last prompt — text AND files — after a failure: the
   *  fallback when the daemon has no failed step to re-drive (a 409 from the
   *  retry endpoint, a permanent verdict, a cancel). The held prompt is
   *  consumed so it can never replay twice; false when none was held for
   *  THIS chat. */
  const resendLast = useCallback(async (): Promise<boolean> => {
    const prompt = lastPromptRef.current;
    if (!promptBelongsTo(prompt, daemonIdRef.current)) return false;
    lastPromptRef.current = null;
    setFailedPrompt(null);
    // Drop the failed exchange so the retry replaces it instead of stacking.
    setMessages((prev) => trimFailedExchange(prev, prompt.text));
    setError(null);
    setStatus("idle");
    // startRun, not sendMessage: a retry re-drives the failed turn and
    // leaves the held queue paused for the user to resume.
    await startRun(prompt.text, prompt.files);
    return true;
  }, [startRun]);

  /**
   * The error strip's Retry (and the one automatic retry). For every failed
   * turn the daemon might re-drive — typed RETRYABLE, typed UNKNOWN, or
   * untyped (an older daemon, a transport fault, a chat reopened after a
   * reload) — this drives `POST .../retry` (ADR 0239): the daemon decides
   * eligibility from durable state, re-drives the recorded failed step
   * itself and relays the run as SSE — no user message is re-sent, which is
   * exactly the duplicate-effects path the endpoint exists to prevent. Its
   * 409 `failed_step_retry_ineligible` falls back to re-sending the held
   * prompt. Only a PERMANENT verdict (the identical request is rejected) and
   * a turn that did not fail (a cancel) skip the daemon and resend directly.
   */
  const retryLast = useCallback(async () => {
    // Read-and-clear first: only the hook's own automatic retry arms it.
    const automatic = autoRetryModeRef.current;
    autoRetryModeRef.current = false;
    if (status === "streaming") return;
    const daemonId = daemonIdRef.current;
    if (
      retryRoute(lastDispositionRef.current, status === "error") === "resend" ||
      !daemonId ||
      !connected
    ) {
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
    // A run this tab drives: the rehydrated failed marking no longer owns
    // the view, and the retry's own terminal decides the verdict afresh.
    localRunsRef.current += 1;
    lastStreamProgressRef.current = undefined;
    lastPermanentRef.current = false;
    setLastFailurePermanent(false);

    const ids = { assistant: `assistant-${Date.now()}` };
    setMessages((prev) => [
      ...prev,
      {
        id: ids.assistant,
        role: "assistant",
        content: "",
        timestamp: Date.now(),
        // The automatic retry says so on the turn it streams into.
        notices: automatic ? [AUTO_RETRY_NOTICE] : undefined,
      },
    ]);
    const patch = (apply: (message: AgentMessage) => AgentMessage) =>
      setMessages((prev) =>
        prev.map((message) =>
          message.id === ids.assistant ? apply(message) : message,
        ),
      );
    const controller = new AbortController();
    abortRef.current = controller;
    // This tab drives the retried run: the watch must not attach on top.
    drivingRef.current = daemonId;
    setDrivingRun(true);
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
      // A retry that failed again `precommit` never reached the model: say
      // so, instead of the bare provider text (the TUI's "retry stopped
      // before the model was called").
      if (lastStreamProgressRef.current === "precommit") {
        setError((current) =>
          retryFailureExplanation({
            streamProgress: "precommit",
            error: current ?? "",
          }),
        );
      }
      setStatus((current) =>
        current === "waiting_approval" ||
        current === "waiting_authorization" ||
        current === "error"
          ? current
          : "idle",
      );
    } catch (caught) {
      if (controller.signal.aborted) {
        setQueuePaused({ reason: pauseReasonFor("cancelled") });
        setStatus("idle");
        return;
      }
      if (
        caught instanceof HarnessApiError &&
        (caught.code === "failed_step_retry_ineligible" ||
          caught.status === 409)
      ) {
        // The daemon has no eligible failed step (another client acted, the
        // state moved on, or the failure was never a recorded step): fall
        // back to re-sending the prompt.
        ineligible = true;
      } else {
        // The retry never started (transport, a refusal that is not a 409):
        // non-destructive — the strip names it and keeps Retry available. A
        // refused credential is named by its cause class and re-probes the
        // runtime at once so the auth-recovery banner appears.
        const authFailure = authFailureMessage(caught);
        if (authFailure) void refreshRuntime();
        const message =
          authFailure ??
          retryStartFailure(
            caught instanceof Error ? caught.message : String(caught),
          );
        patch((current) => ({
          ...current,
          failed: true,
          failureDetail: message,
        }));
        setError(message);
        setQueuePaused({ reason: pauseReasonFor("transport") });
        setStatus("error");
      }
    } finally {
      abortRef.current = null;
      if (drivingRef.current === daemonId) {
        drivingRef.current = null;
        setDrivingRun(false);
      }
    }
    if (ineligible && !(await resendLast())) {
      // Nothing held to re-send either (the prompt was recovered into the
      // composer, or the failed run was driven elsewhere): say what is left.
      patch((current) => ({
        ...current,
        failed: true,
        failureDetail: RETRY_INELIGIBLE_NO_PROMPT,
      }));
      setError(RETRY_INELIGIBLE_NO_PROMPT);
      setStatus("error");
    }
  }, [
    status,
    connected,
    resendLast,
    makeStreamHandler,
    adoptRunId,
    refreshRuntime,
  ]);

  // Fires the automatic retry armed by startRun once the failed terminal has
  // committed to state: retryLast reads `status`, so it must run from a
  // render that already sees "error" — never from the stream's continuation.
  useEffect(() => {
    if (!autoRetryPending) return;
    setAutoRetryPending(false);
    if (status !== "error") return;
    autoRetryModeRef.current = true;
    void retryLast();
  }, [autoRetryPending, status, retryLast]);

  // Fires the plan-approved proceed prompt (TUI parity, cmd/mecatui/ui/
  // update.go's `plan_approved` arm): the interactive resumeApproval path
  // Studio answers asks through ends the plan run on `plan_approved` WITHOUT
  // starting execution — only the parked-plan ApprovePlan RPC does — so the
  // client that approved sends the harness-framed prompt itself. It runs
  // from a render that already sees "idle" (startRun reads `status`), holds
  // while an ask is still on screen, and is spent once it fires.
  useEffect(() => {
    if (!planProceedPending) return;
    if (status !== "idle" || !connected || approvalQueue.length > 0) return;
    setPlanProceedPending(false);
    void startRun(PLAN_APPROVED_PROCEED_TEXT, undefined, { synthetic: true });
  }, [planProceedPending, status, connected, approvalQueue.length, startRun]);

  /** The composer took the recovered prompt: it is the user's draft now, and
   *  the resend fallback must not replay the same text behind it. */
  const consumeRecoverDraft = useCallback(() => {
    setRecoverDraft(null);
    lastPromptRef.current = null;
  }, []);

  /** The strip's Edit (the TUI's esc after a run-entry failure): hands the
   *  refused prompt's text back for edit-before-resend, drops the failed
   *  exchange it left in the transcript, and clears the failure. Null when
   *  no refused prompt is held for THIS chat. The held prompt is consumed —
   *  the composer owns the text now, so Retry can never replay it behind
   *  the user's edit. */
  const takeFailedPrompt = useCallback((): string | null => {
    const text = failedPrompt;
    if (text === null) return null;
    setFailedPrompt(null);
    setRecoverDraft(null);
    lastPromptRef.current = null;
    setMessages((prev) => trimFailedExchange(prev, text));
    setError(null);
    setStatus((current) => (current === "error" ? "idle" : current));
    return text;
  }, [failedPrompt]);

  /** A message typed while a run was active, held client-side: the daemon is
   *  strictly one-run-at-a-time (a mid-run prompt answers 412), so the queue
   *  lives here and drains one message per completed run. */
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([]);
  const flushingRef = useRef(false);

  const deleteQueued = useCallback((id: string) => {
    setQueuedMessages((prev) => prev.filter((m) => m.id !== id));
  }, []);

  /** Removes the message from the queue and returns its text AND staged
   *  files (for editing — the attachments come back to the composer too). */
  const takeQueued = useCallback(
    (id: string): { text: string; files?: File[] } | null => {
      const hit = queuedMessages.find((m) => m.id === id);
      if (!hit) return null;
      setQueuedMessages((prev) => prev.filter((m) => m.id !== id));
      return { text: hit.text, files: hit.files };
    },
    [queuedMessages],
  );

  /** Pulls the WHOLE queue back for editing as one merged draft (texts joined
   *  by a blank line, files in order); the queue empties. Non-destructive. */
  const takeAllQueued = useCallback((): {
    text: string;
    files?: File[];
  } | null => {
    const merged = mergeQueued(queuedMessages);
    if (!merged) return null;
    setQueuedMessages([]);
    return merged;
  }, [queuedMessages]);

  /** Drops every held message (the paused strip's Clear all / Esc). */
  const clearQueue = useCallback(() => {
    setQueuedMessages([]);
    setQueuePaused(null);
  }, []);

  // ── MCP browser authorization: the parked-run phase ────────────────────

  /** Patches the pending request in place, ignoring a stale id. */
  const patchPendingAuthorization = useCallback(
    (authorizationId: string, patch: Partial<AuthorizationRequest>) => {
      const current = pendingAuthorizationRef.current;
      if (!current || current.authorizationId !== authorizationId) return;
      const next = { ...current, ...patch };
      pendingAuthorizationRef.current = next;
      setPendingAuthorization(next);
    },
    [],
  );

  /**
   * Opens the sign-in page. The blank window opens synchronously in the
   * click's task (pop-up blockers attribute it to the gesture), then the URL
   * is fetched live from the daemon and the window navigated to it.
   */
  const openAuthorization = useCallback(async () => {
    const daemonId = daemonIdRef.current;
    const pending = pendingAuthorizationRef.current;
    if (!daemonId || !pending) return;
    const { authorizationId } = pending;
    patchPendingAuthorization(authorizationId, {
      error: undefined,
      notice: undefined,
    });
    try {
      const outcome = await openAuthorizationWindow(() =>
        fetchMcpAuthorizationUrl(daemonId, authorizationId),
      );
      if (outcome === "blocked") {
        patchPendingAuthorization(authorizationId, {
          notice: AUTHORIZATION_POPUP_BLOCKED_NOTICE,
        });
        return;
      }
      // The page is open: the sign-in now completes out of band, so the 3 s
      // re-check loop watches for it (the TUI's polling after a presentation).
      patchPendingAuthorization(authorizationId, { polling: true });
    } catch (caught) {
      patchPendingAuthorization(authorizationId, {
        error: authorizationFailure(caught),
      });
    }
  }, [patchPendingAuthorization]);

  /** Copies the live sign-in URL for a browser this one cannot pop up. */
  const copyAuthorizationLink = useCallback(async (): Promise<boolean> => {
    const daemonId = daemonIdRef.current;
    const pending = pendingAuthorizationRef.current;
    if (!daemonId || !pending) return false;
    const { authorizationId } = pending;
    patchPendingAuthorization(authorizationId, {
      error: undefined,
      notice: undefined,
    });
    try {
      const url = await fetchMcpAuthorizationUrl(daemonId, authorizationId);
      await navigator.clipboard.writeText(url);
      // The link is in the operator's hands: poll for the sign-in like open.
      patchPendingAuthorization(authorizationId, {
        notice: AUTHORIZATION_LINK_COPIED_NOTICE,
        polling: true,
      });
      return true;
    } catch (caught) {
      patchPendingAuthorization(authorizationId, {
        error: authorizationFailure(caught),
      });
      return false;
    }
  }, [patchPendingAuthorization]);

  /**
   * Drives one authorization control stream (recheck / cancel) through the
   * SAME stream handler the prompt path uses, so the continuation run's
   * tokens, tool calls and result render exactly like a prompt's. The run is
   * parked — the daemon closed the prompt stream — so this tab drives again
   * for the continuation (a watch must not attach on top), and the
   * continuation is a NEW run whose id the handler adopts.
   */
  const driveAuthorizationControl = useCallback(
    async (
      control: (
        sessionId: string,
        authorizationId: string,
        onEvent: (event: StreamEvent) => void,
        signal?: AbortSignal,
      ) => Promise<McpAuthorizationControlOutcome>,
      options?: {
        /** A 3 s poll tick: silent when another control is in flight, and it
         *  leaves the panel's notice alone (the polling line owns that). */
        poll?: boolean;
        /** Cancel: takes over from an in-flight poll instead of being dropped. */
        preempt?: boolean;
      },
    ) => {
      const daemonId = daemonIdRef.current;
      if (!daemonId || !pendingAuthorizationRef.current) return;
      if (authorizationBusyRef.current) {
        // One control at a time — only one stream may adopt the continuation
        // run, or the transcript renders it twice. A poll tick waits for the
        // next; a click says so instead of looking ignored.
        const inFlight = authorizationControlRef.current;
        if (!options?.preempt || !inFlight) {
          if (!options?.poll) {
            patchPendingAuthorization(
              pendingAuthorizationRef.current.authorizationId,
              { notice: AUTHORIZATION_CHECK_IN_FLIGHT_NOTICE },
            );
          }
          return;
        }
        inFlight.preempted = true;
        inFlight.controller.abort();
        await inFlight.settled;
        if (authorizationBusyRef.current) return;
      }
      const pending = pendingAuthorizationRef.current;
      if (!pending) return;
      authorizationBusyRef.current = true;
      const { authorizationId } = pending;
      patchPendingAuthorization(
        authorizationId,
        options?.poll
          ? { error: undefined }
          : { error: undefined, notice: undefined },
      );
      // The continuation renders into the turn that parked; a phase found by
      // a watch replay with no known bubble gets a fresh one.
      const ids =
        authorizationIdsRef.current ??
        (() => {
          const fresh = { assistant: `assistant-${Date.now()}` };
          setMessages((prev) => [
            ...prev,
            {
              id: fresh.assistant,
              role: "assistant",
              content: "",
              timestamp: Date.now(),
            },
          ]);
          authorizationIdsRef.current = fresh;
          return fresh;
        })();
      const patch = (apply: (message: AgentMessage) => AgentMessage) =>
        setMessages((prev) =>
          prev.map((message) =>
            message.id === ids.assistant ? apply(message) : message,
          ),
        );
      // The control stream owns the view now: a durable watch on the parked
      // run would render the continuation twice.
      watchAbortRef.current?.abort();
      const controller = new AbortController();
      let settle: () => void = () => undefined;
      const inFlight = {
        controller,
        preempted: false,
        settled: new Promise<void>((resolve) => {
          settle = resolve;
        }),
      };
      authorizationControlRef.current = inFlight;
      abortRef.current = controller;
      drivingRef.current = daemonId;
      setDrivingRun(true);
      runIdRef.current = "";
      lastDispositionRef.current = undefined;
      setError(null);
      try {
        const outcome = await control(
          daemonId,
          authorizationId,
          makeStreamHandler(daemonId, ids),
          controller.signal,
        );
        if (outcome.status === "pending") {
          // The sign-in is not done yet: the run stays parked, the card stays
          // (a poll tick says nothing — the polling line already does).
          if (!options?.poll) {
            patchPendingAuthorization(authorizationId, {
              notice: AUTHORIZATION_STILL_PENDING_NOTICE,
            });
          }
          return;
        }
        if (!outcome.sawResult) {
          // A terminal status with no continuation on the wire: the daemon
          // settled the parked call itself. The transcript has the record.
          runIdRef.current = "";
          setStatus((current) =>
            current === "waiting_authorization" || current === "streaming"
              ? "idle"
              : current,
          );
          void rehydrate(daemonId).catch(() => undefined);
          return;
        }
        setStatus((current) =>
          current === "waiting_approval" ||
          current === "waiting_authorization" ||
          current === "error"
            ? current
            : "idle",
        );
      } catch (caught) {
        if (controller.signal.aborted) {
          // A poll Cancel pre-empted keeps the parked phase: the cancel
          // control that follows owns the outcome.
          if (!inFlight.preempted) setStatus("idle");
          return;
        }
        if (caught instanceof HarnessApiError && caught.status === 404) {
          // The authorization is gone daemon-side (expired, or resolved from
          // another client): the run moved on without this tab. Leave the
          // phase, keep the daemon's words on the turn, and refresh.
          pendingAuthorizationRef.current = null;
          setPendingAuthorization(null);
          patch((message) => ({
            ...message,
            notices: [
              ...(message.notices ?? []),
              `Browser authorization ended: ${caught.message}`,
            ],
          }));
          runIdRef.current = "";
          setStatus("idle");
          void rehydrate(daemonId).catch(() => undefined);
          return;
        }
        patchPendingAuthorization(authorizationId, {
          error: authorizationFailure(caught),
        });
      } finally {
        authorizationBusyRef.current = false;
        if (authorizationControlRef.current === inFlight) {
          authorizationControlRef.current = null;
        }
        if (abortRef.current === controller) abortRef.current = null;
        if (drivingRef.current === daemonId) {
          drivingRef.current = null;
          setDrivingRun(false);
        }
        settle();
      }
    },
    [makeStreamHandler, patchPendingAuthorization, rehydrate],
  );

  /** "I've finished — re-check": asks the daemon to re-inspect the sign-in
   *  and streams the continuation when it went through. */
  const recheckAuthorization = useCallback(
    () => driveAuthorizationControl(recheckMcpAuthorization),
    [driveAuthorizationControl],
  );

  /** Abandons the sign-in through the AUTHORIZATION control — never the run's
   *  cancel, which cannot reach a parked run. It stops the 3 s polling first
   *  and takes over from a poll already in flight, so the click is never
   *  dropped by the one-control-at-a-time guard. */
  const cancelAuthorization = useCallback(async () => {
    const pending = pendingAuthorizationRef.current;
    if (pending?.polling) {
      patchPendingAuthorization(pending.authorizationId, { polling: false });
    }
    await driveAuthorizationControl(cancelMcpAuthorization, { preempt: true });
  }, [driveAuthorizationControl, patchPendingAuthorization]);

  // The 3 s re-check loop (the TUI's poll after a presentation): armed once
  // the sign-in page was opened or its link copied (`polling`), each tick asks
  // the daemon whether the sign-in went through, so a completed sign-in
  // resumes the run without a click. It stops when the request resolves —
  // from the control stream OR a watch's `authorization_resolved` — or is
  // cancelled (pending → null / polling → false: this effect's cleanup), on a
  // chat switch (the switch effect resets the request) and on unmount. Ticks
  // never overlap: the drive's in-flight guard drops a tick while a control
  // (or the granted continuation it streams) is still running, so exactly one
  // stream adopts the continuation run.
  const pollingAuthorizationId = pendingAuthorization?.polling
    ? pendingAuthorization.authorizationId
    : null;
  useEffect(() => {
    if (!pollingAuthorizationId) return;
    const timer = setInterval(() => {
      if (
        pendingAuthorizationRef.current?.authorizationId !==
        pollingAuthorizationId
      ) {
        return;
      }
      void driveAuthorizationControl(recheckMcpAuthorization, { poll: true });
    }, AUTHORIZATION_POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [pollingAuthorizationId, driveAuthorizationControl]);

  const cancelChat = useCallback(async () => {
    // Hold the queue BEFORE anything flips to idle: a cancel must never fire
    // the next queued message on its own (the abort branch below and this
    // function both set idle; both carry the pause).
    setQueuePaused({ reason: pauseReasonFor("cancelled") });
    if (pendingAuthorizationRef.current) {
      // The run is PARKED on a browser sign-in: its own cancel control would
      // answer stale — the authorization control is the one that moves it
      // (and it takes over from a 3 s poll tick still in flight).
      await cancelAuthorization();
      return;
    }
    abortRef.current?.abort();
    if (daemonIdRef.current) {
      // Scoped to the run this hook knows about (ADR 0249): if that run
      // already ended, the daemon answers 409 stale_run_control and the
      // session's NEXT run is left untouched — exactly what "cancel" meant.
      const outcome = await cancelHarnessRun(
        daemonIdRef.current,
        runIdRef.current,
      );
      // The tab aborted its own stream, so the daemon's `cancelled` terminal
      // never arrives: the trailing turn is stamped here — unless the run
      // had already ended on its own (a stale refusal), when the terminal
      // this tab saw owns the label and "cancelled" would mislabel it.
      if (outcome !== "stale") {
        setMessages((prev) =>
          stampTrailingAssistantStop(
            prev,
            "cancelled",
            () => `cancelled-${Date.now()}`,
          ),
        );
        setStatusMessage(statusFromStopReason("cancelled"));
      }
    }
    // The daemon retracts a cancelled run's asks pre-seal, but the client
    // must not keep a dead ask on screen either way.
    replaceApprovalQueue([]);
    setStatus("idle");
  }, [cancelAuthorization, replaceApprovalQueue]);

  /**
   * Cancels ONE running delegated child (subagent, parallel branch, or team
   * member) by its session id while the parent run keeps streaming. The
   * fleet lane and the turn's card show `cancelling…` optimistically; the
   * child's own terminal frame (`delegation_end`, or the team's `team_end`
   * disposition) clears the flag. "Already finished" and a refusal clear it
   * here and say so — a cancel that quietly did nothing reads as a glitch.
   */
  const cancelChild = useCallback(async (childId: string) => {
    const daemonId = daemonIdRef.current;
    if (!daemonId || !childId) return;
    setFleet((prev) => markCancelling(prev, childId, true));
    setMessages((prev) => markCardCancelling(prev, childId, true));
    const clear = () => {
      setFleet((prev) => markCancelling(prev, childId, false));
      setMessages((prev) => markCardCancelling(prev, childId, false));
    };
    try {
      const outcome = await cancelHarnessChild(daemonId, childId);
      if (outcome === "not_found") {
        clear();
        toast.info("That agent already finished.");
      }
    } catch (caught) {
      clear();
      toast.error(
        `Could not cancel the agent: ${caught instanceof Error ? caught.message : String(caught)}`,
      );
    }
  }, []);

  // Steer is capability-gated (C1.2): the live `capabilities.steer` off
  // /v1/compatibility DECIDES when present — an explicit false (`--no-steer`
  // / operator `steer: false`) wins over the STATIC `http_steer` registry
  // row a rebuilt daemon lists unconditionally; the row is only the fallback
  // for an older daemon without the key. Absent both, mid-run sends queue.
  const steerSupported = resolveSteerSupported(serverCapabilities, features);

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
      if (pendingAuthorizationRef.current) {
        // The run is parked on a browser sign-in: nothing is consuming a
        // steer, so hold the text until the continuation ends.
        queueMessage(trimmed, files);
        return;
      }
      if (!daemonId || !steerSupported || !runId) {
        queueMessage(trimmed, files);
        return;
      }
      // Same classification as sendMessage: media parts gated on the
      // session's modalities, text files inlined into the steer text, and
      // chips carrying the same bytes/content for the optimistic bubble.
      const payload = await buildPromptPayload({
        text: trimmed,
        files: files ?? [],
        capabilities: mediaCapabilitiesRef.current,
        encodeImage: imageToPart,
      });
      if (!payload.ok) {
        setError(payload.error);
        return;
      }
      const { parts, attachments } = payload;
      steerSerialRef.current += 1;
      const id = `steer-${Date.now()}-${steerSerialRef.current}`;
      // Developer-tools trace (steer-trace.ts): what became of this steer.
      const trace = (decision: SteerTraceDecision) =>
        setSteerTrace((prev) =>
          appendSteerTrace(prev, {
            id,
            text: trimmed,
            decision,
            at: Date.now(),
          }),
        );
      try {
        const { outcome } = await steerHarnessRun(daemonId, payload.text, id, {
          expectedRunId: runId,
          parts,
        });
        if (outcome === "accepted" || outcome === "appended") {
          trace("accepted");
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
            { id, text: trimmed, files: files?.length ? files : undefined },
          ]);
          return;
        }
        trace("held");
        queueMessage(trimmed, files);
      } catch (caught) {
        if (
          caught instanceof HarnessApiError &&
          caught.code === "stale_run_control"
        ) {
          // The named run already ended — exactly the too_late outcome:
          // requeue quietly, never an error toast (A2.3/C1.3).
          trace("too_late");
          runIdRef.current = "";
          queueMessage(trimmed, files);
          return;
        }
        trace("held");
        queueMessage(trimmed, files);
        setError(
          mapPromptValidationError(caught) ??
            (caught instanceof Error ? caught.message : String(caught)),
        );
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
  const cancelPendingSteers = useCallback(async (): Promise<PendingSteer[]> => {
    const daemonId = daemonIdRef.current;
    const bundle = pendingSteers;
    // A retracted steer never reached the run: its optimistic bubble comes
    // out of the transcript and the text goes back to the caller (the
    // composer re-seeds it — "retract, then recompose").
    const dropBubbles = () => {
      const bubbleIds = new Set(bundle.map((p) => `steer-user-${p.id}`));
      setMessages((prev) => prev.filter((m) => !bubbleIds.has(m.id)));
    };
    if (!daemonId) {
      setPendingSteers([]);
      dropBubbles();
      return bundle;
    }
    try {
      const outcome = await cancelHarnessSteer(daemonId, runIdRef.current);
      setPendingSteers([]);
      if (outcome === "retracted") {
        // Developer-tools trace: every steer of the bundle came back.
        const at = Date.now();
        setSteerTrace((trace) =>
          bundle.reduce(
            (acc, steer) =>
              appendSteerTrace(acc, {
                id: steer.id,
                text: steer.text,
                decision: "retracted",
                at,
              }),
            trace,
          ),
        );
        dropBubbles();
        return bundle;
      }
      // none_pending: the bundle already drained into the run — the echo
      // owns the bubbles, and there is nothing to recompose.
      return [];
    } catch (caught) {
      // Unknown daemon state: keep the list rather than pretend it retracted.
      setError(caught instanceof Error ? caught.message : String(caught));
      return [];
    }
  }, [pendingSteers]);

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
  // A paused queue (cancel / failed turn / lost connection) never drains on
  // its own — the strip offers Send now / Edit all / Clear all, and the
  // composer's Enter / ↑ / Esc on an empty line do the same.
  useEffect(() => {
    // The plan-approved proceed prompt goes first: it starts the execution
    // run the held messages were typed for, and two runs cannot start at once.
    if (planProceedPending) return;
    if (
      !shouldDrainQueue({
        status,
        connected,
        queued: queuedMessages.length,
        pendingSteers: pendingSteers.length,
        paused: queuePaused !== null,
        flushing: flushingRef.current,
      })
    ) {
      return;
    }
    flushingRef.current = true;
    const next = queuedMessages[0];
    setQueuedMessages((prev) => prev.filter((m) => m.id !== next.id));
    void startRun(next.text, next.files).finally(() => {
      flushingRef.current = false;
    });
  }, [
    status,
    connected,
    queuedMessages,
    pendingSteers,
    queuePaused,
    planProceedPending,
    startRun,
  ]);

  // Nothing held → nothing to pause: a stale pause (a cancel with an empty
  // queue, the last row deleted or edited away) clears itself. Orphaned
  // steers are still on `pendingSteers` in the commit that requeues them, so
  // this never races the requeue above.
  useEffect(() => {
    if (
      queuePaused !== null &&
      queuedMessages.length === 0 &&
      pendingSteers.length === 0
    ) {
      setQueuePaused(null);
    }
  }, [queuePaused, queuedMessages, pendingSteers]);

  /** Sends the whole held queue as ONE prompt (texts joined by a blank line,
   *  files in order) and lifts the pause — the strip's Send now / Enter on an
   *  empty idle composer. */
  const resumeQueue = useCallback(() => {
    const merged = mergeQueued(queuedMessages);
    setQueuePaused(null);
    if (!merged) return;
    setQueuedMessages([]);
    void startRun(merged.text, merged.files);
  }, [queuedMessages, startRun]);

  /**
   * Answers one queued ask — by default the head (the one on screen). The
   * queue advances: the next ask, if any, takes the screen and the run stays
   * `waiting_approval`; only an emptied queue returns it to streaming.
   */
  const respondToApproval = useCallback(
    async (choice: ApprovalChoice, askId?: string) => {
      const daemonId = daemonIdRef.current;
      const queue = approvalQueueRef.current;
      const answered =
        (askId ? queue.find((ask) => ask.approvalId === askId) : queue[0]) ??
        null;
      const approvalId = answered?.approvalId;
      const remaining = approvalId ? resolveAsk(queue, approvalId) : queue;
      replaceApprovalQueue(remaining);
      if (answered?.synthetic) {
        // A developer-tools FAKE ask (debug-ask.ts): no verdict goes to the
        // daemon — nothing asked. Put back EXACTLY the status the injection
        // displaced (idle stays idle, a live run stays streaming — the
        // composer's Enter behaviour depends on it) unless a genuine ask is
        // still queued, and leave a visible record of the choice on the
        // turn, the way a real verdict is recorded.
        if (!remaining.length) {
          const previous = debugAskStatusRef.current ?? "idle";
          setStatus((current) =>
            current === "waiting_approval" ? previous : current,
          );
        }
        debugAskStatusRef.current = null;
        appendApprovalNotice(debugAskResolvedNotice(choice));
        return;
      }
      // The verdict resumes the SAME run — the prompt stream stays open and
      // keeps delivering (the daemon acks the approve; only the run's end
      // closes the stream). Mirror the retract handler: back to streaming,
      // never idle — a premature idle here let the steer-requeue and queue
      // drain effects fire against the still-live run (a pending steer got
      // re-sent as a prompt that 412s). The stream's own end handler owns
      // the eventual idle.
      if (!remaining.length) {
        setStatus((current) =>
          current === "waiting_approval" ? "streaming" : current,
        );
      }
      if (!daemonId || !approvalId) return;
      // The daemon's verdict is three-way. "session" and "always" both map
      // to allow_always — the daemon models one persistent grant scope, and
      // splitting hairs the backend does not model would be a lie in the UI.
      const verdict =
        choice === "deny"
          ? ("deny" as const)
          : choice === "once"
            ? ("allow_once" as const)
            : ("allow_always" as const);
      // A PresentPlan verdict: approve & run / auto-accept edits ARM the
      // auto-proceed for THIS tab (the daemon ends the plan run on
      // plan_approved without starting execution); iterate clears it.
      if (isPlanAsk(answered?.toolName)) {
        planProceedArmRef.current = isPlanApprovalVerdict(verdict)
          ? { sessionId: daemonId, askId: approvalId }
          : null;
      }
      // EvApproval is log-only — it never rides the live prompt stream — so
      // a session THIS tab drives would show no record of what was decided
      // until a reload. Record it locally, in the very words the durable
      // watch's approval_verdict arm renders. No duplicate can arise: the
      // watch REBUILDS the transcript from the log (this line is replaced
      // by its twin, never joined by it), and a run this tab drives is never
      // watched at the same time.
      appendApprovalNotice(formatVerdictNotice(answered?.toolName, verdict));
      try {
        await respondToHarnessApproval(
          daemonId,
          approvalId,
          verdict,
          runIdRef.current,
        );
      } catch (caught) {
        // A verdict the daemon never took cannot have approved a plan.
        planProceedArmRef.current = null;
        if (
          caught instanceof HarnessApiError &&
          caught.code === "stale_run_control"
        ) {
          // The run this dialog belonged to already ended (e.g. another
          // client answered, or a schedule fire replaced it). Not an error:
          // every ask of that run is dead with it — drop the whole queue,
          // refresh quietly and let the transcript show what happened.
          runIdRef.current = "";
          replaceApprovalQueue([]);
          setStatus("idle");
          void rehydrate(daemonId).catch(() => undefined);
          return;
        }
        setError(caught instanceof Error ? caught.message : String(caught));
        setStatus("error");
      }
    },
    [appendApprovalNotice, rehydrate, replaceApprovalQueue],
  );

  const respondToClarification = useCallback(async (_response: string) => {
    setStatus("idle");
  }, []);

  /**
   * Developer tools (Settings → Labs): parks a FAKE Shell ask on the panel so
   * its layout, focus and verdict flow can be exercised without a model — the
   * TUI's `/debug-ask`. Marked `synthetic`, it never reaches the daemon:
   * `respondToApproval` short-circuits on it, and a genuine ask arriving
   * meanwhile displaces it. Refused (false) while any ask is already pending,
   * mirroring the TUI's dedupe. The status it displaces is remembered so the
   * resolve can restore it exactly.
   */
  const injectDebugApproval = useCallback((): boolean => {
    if (approvalQueueRef.current.length > 0) return false;
    const cycle = debugAskCycleRef.current;
    debugAskCycleRef.current += 1;
    replaceApprovalQueue([debugAskRequest(daemonIdRef.current ?? "", cycle)]);
    setStatus((current) => {
      debugAskStatusRef.current = current;
      return "waiting_approval";
    });
    return true;
  }, [replaceApprovalQueue]);

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
    // treats both as "run active" (queue/steer, never a raw prompt). So is a
    // run parked on a browser authorization.
    isStreaming:
      status === "streaming" ||
      status === "waiting_approval" ||
      status === "waiting_authorization",
    queuedMessages,
    queueMessage,
    deleteQueued,
    takeQueued,
    takeAllQueued,
    clearQueue,
    /** Non-null while the queue is held after a non-clean stop. */
    queuePaused,
    resumeQueue,
    steerQueued,
    pendingSteers,
    /** The steer correlation trace (developer tools; steer-trace.ts). */
    steerTrace,
    steerMessage,
    /** Mid-run steering is available (capability/feature gate, C1.2). */
    steerSupported,
    cancelPendingSteers,
    status,
    error,
    /** The transient status line under the transcript: a no-progress nudge or
     *  the recover notice while a run is live, or how the last run stopped
     *  (null once a fresh run starts or nothing needs saying). */
    statusMessage,
    harnessLive: connected,
    sendMessage,
    adoptSession,
    retryLast,
    /** The daemon typed the last failure PERMANENT: the identical request
     *  is rejected, so the strip withholds Retry and offers a new chat. */
    lastFailurePermanent,
    /** A text-only prompt a transport fault dropped, for the composer to
     *  take back (edit-before-resend); null when nothing is recoverable. */
    recoverDraft,
    consumeRecoverDraft,
    /** The text of a prompt a run-entry failure refused (the mint or the
     *  prompt POST itself); null when none is held for this chat. The strip
     *  offers Edit while it is set. */
    failedPrompt,
    takeFailedPrompt,
    refreshTranscript,
    cancelChat,
    /** THIS tab's prompt / retry / authorization stream is driving a run
     *  (the leave guard's second arm: closing the tab would end it). False
     *  for a watched run driven elsewhere — closing the tab leaves it be. */
    drivingRun,
    /** Cancels one live delegated child by its session id; the parent run
     *  keeps going. Optimistic `cancelling…` on its lane/card until its end. */
    cancelChild,
    /** The MCP browser authorization the run is parked on (null = none). */
    pendingAuthorization,
    openAuthorization,
    copyAuthorizationLink,
    recheckAuthorization,
    cancelAuthorization,
    /** The head of the FIFO permission-ask queue (null = none pending). */
    pendingApproval,
    /** Asks queued, head included — the panel's "1 of N" (0 = none). */
    approvalQueueLength: approvalQueue.length,
    pendingClarification,
    respondToApproval,
    /** Developer tools: parks a FAKE ask (never sent); false when an ask is
     *  already pending. */
    injectDebugApproval,
    respondToClarification,
    usage,
    /** Every child this session's runs delegated, aggregated across turns
     *  (per-visit, like usage — rebuilt from the durable watch's replay). */
    fleet,
    /** The latest turn's input tokens: the context meter's occupancy. */
    contextOccupancy,
    /** The GET-session detail (resolved model + window, durable usage). */
    sessionDetail,
    /** Whether that detail read is pending, landed, or failed — the status
     *  strip's "resolving model…" is honest only while it is pending. */
    sessionDetailStatus,
    /** The downstream provider the current/last turn was routed to ("" =
     *  none reported / cache hit); cleared on send and on chat open. */
    providerRoute,
  };
}
